# Storage Layer Research — MiBee NVR

> Technical research document covering the SQLite storage architecture, query patterns, N+1 issues, growth projections, and best practices.

**Version:** 1.0  
**Context:** Storage overhaul planning for v0.9.0  
**Cross-reference:** [docs/zh/storage-research.md](../zh/storage-research.md)

---

## 1. Executive Summary

The MiBee NVR storage layer uses SQLite with the `modernc.org/sqlite` pure-Go driver, operating in WAL mode. The production database at Banana Pi M5 (`192.168.1.10`) holds **67,000+ recording rows** across an **86MB database file**, growing at approximately **90GB/day** in recording data (not DB size — the database only stores metadata).

**Key findings:**

| Finding | Severity | Impact |
|---------|----------|--------|
| 47% merge failure rate | Critical | ~half of segment merges fail, leaving orphaned pending recordings |
| 8 N+1 query patterns confirmed | High | Cleanup, merge, API, and stats all exhibit N+1 loops |
| Redundant/overlapping indexes | Medium | `idx_recordings_merged` and `idx_recordings_camera` overlap with composite indexes added in v22 |
| No ANALYZE scheduling | Medium | Query planner may pick suboptimal plans mid-session; `PRAGMA optimize` runs only at startup |
| GetRecordingTrends loads all rows into Go | Medium | No SQL-level GROUP BY; 67K+ rows scanned into memory on each stats request |

**Recommendations:**

1. Fix the N+1 patterns (batch queries, aggregate in SQL, not Go)
2. Schedule periodic `ANALYZE` and WAL checkpoint operations
3. Add covering indexes for the three most-expensive query patterns
4. Implement batch DELETE with `IN` clauses (already partially done in `DeleteRecordingsBatch`)
5. Monitor growth thresholds and prepare partitioning or PostgreSQL migration path

---

## 2. Current Architecture Analysis

### 2.1 Schema Overview

The database has 3 core tables and 8 secondary tables, all created via migrations in `Init()` (`internal/storage/db.go:50`):

**Core tables:**

- **`cameras`** — 20+ columns: `id` (TEXT PK), `name`, `protocol`, `encoding`, `url`, `username`, `password`, `enabled`, `description`, `location`, `brand`, `model`, `serial_number`, `retention_days`, `onvif_endpoint`, `profile_token`, `stream_encoding`, `archived`, `archived_at`, `archive_retention_days`, `merge_enabled`, `merge_check_interval`, `merge_window_size`, `merge_batch_limit`, `merge_min_segment_age`, `merge_min_segments_to_merge`, `merge_duration`, `stream_key`, `srt_passphrase`, `srt_stream_id`, `created_at`

- **`recordings`** — 25+ columns: `id` (TEXT PK), `camera_id` (FK→cameras), `file_path`, `format`, `started_at`, `ended_at`, `duration`, `file_size`, `frame_count`, `merged`, `merge_status`, `merge_path`, `merge_tier`, `merge_progress`, `merge_error`, `retry_count`, `archived`, `ai_status`, `ai_processed_at`, `ai_error`, `created_at`

- **`schema_meta`** — schema version tracking (`key`/`value`)

**Secondary tables:**

`feature_flags`, `camera_health_events`, `transcoding_tasks`, `ai_events`, and several merge-related tables (`merged_recordings`, `merge_queue`, `pending_ai_recordings`, `storage_stats`).

### 2.2 Migration History (22+ migrations)

Migrations are embedded in `Init()` (`internal/storage/db.go:50–379`), each using `IF NOT EXISTS` / column-existence checks for idempotency:

| Version | Change | Key Code |
|---------|--------|----------|
| 1→2 | Camera metadata columns (description, location, brand, model, serial) | `db.go:106` |
| 2→3 | Camera `retention_days` column | `db.go:120` |
| 3→4 | `pinned` → `merged` rename, `idx_recordings_merged` index | `db.go:125` |
| 4→5 | Camera merge config columns (5 columns) | `db.go:146` |
| 5→6 | ONVIF columns (`onvif_endpoint`, `profile_token`) | `db.go:163` |
| 6→7 | `stream_encoding` column | `db.go:169` |
| 7→8 | Archive columns + indexes (`archived`, `archived_at`, `archive_retention_days`) | `db.go:176` |
| 8→9 | `feature_flags` table + `idx_recordings_camera_time` index | `db.go:188` |
| 9→10 | `camera_health_events` table + indexes | `db.go:207` |
| 10→11 | `transcoding_tasks` table + indexes | `db.go:224` |
| 11→12 | Transcoding indexes (4 covering indexes) | `db.go:248` |
| 12→13 | `merge_status` column + index on recordings | `db.go:258` |
| 13→14 | Transcode `framerate` column | `db.go:272` |
| 14→15 | `merge_path`, `merge_error` columns on recordings | `db.go:280` |
| 15→16 | `merge_tier` column | `db.go:290` |
| 16→17 | Camera `merge_duration` column | `db.go:298` |
| 17→18 | `merge_progress` column | `db.go:306` |
| 18→19 | Transcode `bitrate`, `crf` columns | `db.go:314` |
| 19→20 | `ai_events` table + indexes | `db.go:323` |
| 20→21 | `ai_status`, `ai_processed_at`, `ai_error` columns | `db.go:344` |
| 21→22 | SRT/RTMP push columns (`stream_key`, `srt_passphrase`, `srt_stream_id`) | `db.go:355` |
| v22+ | `idx_recordings_archived_time` + `idx_recordings_camera_ended` covering indexes | `db.go:367` |

### 2.3 All Indexes (recordings table)

| Index Name | Columns | Created In | Notes |
|------------|---------|------------|-------|
| `idx_recordings_camera` | `camera_id` | v1 | Overlapped by `idx_recordings_camera_time` |
| `idx_recordings_time` | `started_at` | v1 | Still useful for standalone time queries |
| `idx_recordings_merged` | `merged` | v4 | Low selectivity (~1/3 merged=0) |
| `idx_recordings_archived` | `archived` | v8 | Zero selectivity (archived=0 matches ALL rows) |
| `idx_recordings_camera_time` | `camera_id, started_at, ended_at, archived` | v9 | 4-column composite for ListRecordings |
| `idx_recordings_merge_status` | `merge_status` | v13 | Medium selectivity |
| `idx_recordings_archived_time` | `archived, started_at DESC` | v22 | Covering index for list queries |
| `idx_recordings_camera_ended` | `camera_id, ended_at DESC` | v22 | For GetAllLastRecordingTimes |

### 2.4 DSN Configuration

Configured in `New()` (`internal/storage/db.go:42`):

```text
_pragma=journal_mode(WAL)
_pragma=synchronous(NORMAL)
_pragma=busy_timeout(15000)
_pragma=cache_size(-20000)
_pragma=mmap_size(268435456)
```

Key characteristics:
- **WAL mode**: Concurrent readers don't block writers; write transactions don't block reads
- **NORMAL sync**: FSYNC every CHECKPOINT, not every transaction — acceptable crash safety for metadata
- **15s busy_timeout**: Long timeout for SQLITE_BUSY contention on RPi under load
- **20MB cache (-20000 pages)**: ~20MB page cache for B-tree lookups
- **256MB mmap**: Memory-mapped I/O for faster reads on RPi 3B (1GB RAM constraint)

### 2.5 Query Surface Area

The storage package exposes approximately 40 query methods covering CRUD for cameras and recordings, merge operations (window listing, segment listing, singleton detection), cleanup queries (expired recordings by camera, oldest recordings, AI status), stats aggregation, and transcoding task management. Files: `db_recording.go` (586 lines), `db_merge.go` (229 lines), `db_stats.go` (157 lines), `db_transcoding.go` (80+ lines), `db_ai.go` (50+ lines).

---

## 3. Production DB Health Report

### 3.1 Current State

| Metric | Value | Source |
|--------|-------|--------|
| Total recordings | ~67,000 rows | `CountRecordings()` |
| Database file size | ~86 MB | On-disk measurement |
| Merge failure rate | ~47% | Production observation (merge attempts vs successes) |
| Daily recording growth | ~90 GB/day (file data) | Estimated from segment duration × cameras |
| SQLite page size | 4096 bytes (default) | `PRAGMA page_size` |
| WAL file size | Variable (5–20 MB) | Depends on checkpoint frequency |
| Cache hit ratio | Unknown | No `PRAGMA stats` collected |

### 3.2 Growth Rate

At 67K records and assuming a ~30-day retention window:
- **Daily new recordings:** ~2,200 rows/day
- **Daily DB growth:** ~2.8 MB/day (metadata only, ~1.3 KB/row)
- **Recording file data:** ~90 GB/day across all cameras
- **90GB/day** of video files means the database metadata grows at ~0.003% of storage — the DB itself is not the bottleneck.

### 3.3 WAL and Fragmentation

- WAL mode means the WAL file accumulates writes until a checkpoint occurs
- Without explicit checkpoint scheduling, WAL can grow large during high-recording periods
- No VACUUM schedule exists; deleted rows leave free pages in the database file
- `PRAGMA optimize` runs only at startup (`db.go:377`), not periodically

---

## 4. SQLite Scaling Thresholds

### 4.1 Theoretical Limits

| Parameter | SQLite Limit | Relevant to NVR? |
|-----------|-------------|-------------------|
| Max database size | 281 TB | No (86 MB current) |
| Max rows per table | ~2^64 | No (67K current) |
| Max columns per table | 2000 | Approaching (25+ columns on recordings) |
| Max attached DBs | 125 | Not used |
| Max query parameter count | 32766 | Relevant for batch IN clauses |

### 4.2 Practical Thresholds for Time-Series Metadata

For time-series metadata like recording logs, SQLite is practical up to **10–50 GB** before performance degrades noticeably:

- **B-tree depth**: At 86 MB / ~67K rows, depth is ~2-3 levels. At 10 GB / ~8M rows, depth reaches ~4-5.
- **INSERT throughput**: ~50-200 writes/second on RPi 3B SD card (WAL mode)
- **Query performance**: Degrades proportionally to index size + WAL file size
- **CHECKPOINT overhead**: Can cause latency spikes on RPi 3B during large WAL flushes

### 4.3 RPi 3B Constraints

| Resource | Limit | Impact on SQLite |
|----------|-------|-----------------|
| RAM | 1 GB | mmap_size=256MB (25% of RAM) leaves 768MB for app + OS |
| SD card I/O | ~10-20 MB/s sequential | WAL checkpoints and ANALYZE can cause write latency |
| CPU | 4× Cortex-A53 @ 1.2 GHz | Query compilation overhead is minimal; the bottleneck is I/O |
| Process memory | 512 MB budget | Cache_size of 20MB is reasonable; don't increase |

**Key concern**: SD cards have limited write endurance. Every extra index update, ANALYZE, or VACUUM writes to the journal/WAL. Minimize write amplification.

---

## 5. NVR Industry Comparison

### 5.1 Frigate

- **Database**: PostgreSQL (via `psycopg2` + `asyncpg`)
- **Schema**: Event-centric (events table with camera, label, zone, thumbnail, snapshot)
- **Strengths**: Proper joins, window functions, concurrent read/write, rich querying
- **Tradeoff**: Heavier deployment (database server), more RAM consumption
- **Relevance**: PostgreSQL is the gold standard for multi-camera NVR metadata

### 5.2 Shinobi

- **Database**: MySQL (MariaDB) or SQLite (configurable)
- **Schema**: Monitors, recordings, logs separate tables; recordings can be extensive
- **Strengths**: MySQL handles concurrent access well; configurable engine
- **Tradeoff**: SQLite mode suffers the same limitations as MiBee NVR at scale
- **Relevance**: Validates that SQLite choice is acceptable for small-to-medium deployments

### 5.3 ZoneMinder

- **Database**: MySQL (MariaDB)
- **Schema**: Events, Frames, Stats — heavily normalized with frame-level detail
- **Strengths**: Mature schema with proper indexing; handles thousands of events
- **Tradeoff**: Complex schema increases query overhead; requires MySQL tuning
- **Relevance**: Demonstrates the cost of over-normalization for video metadata

### 5.4 Kerberos.io

- **Database**: SQLite (single file per instance)
- **Schema**: Minimal — recordings, cameras, snapshots
- **Strengths**: Simple, portable, easy to backup
- **Tradeoff**: No multi-process access; limited to single-instance use
- **Relevance**: Similar architecture to MiBee NVR; validates single-file approach

### 5.5 MotionEye

- **Database**: SQLite
- **Schema**: Very minimal — events, movies, images
- **Strengths**: Dead simple, zero configuration
- **Tradeoff**: No querying capability; flat file mapping
- **Relevance**: Shows the minimal viable schema for NVR storage

### 5.6 Comparison Summary

| Feature | MiBee NVR | Frigate | Shinobi | ZoneMinder | Kerberos.io | MotionEye |
|---------|-----------|---------|---------|------------|-------------|-----------|
| Engine | SQLite | PostgreSQL | MySQL/SQLite | MySQL | SQLite | SQLite |
| Pure Go | Yes | No | No | No | No | No |
| Schema size | ~25 columns | ~15 columns | ~20 columns | ~40+ columns | ~10 columns | ~8 columns |
| Grows to 10M rows | Concern | Fine | Fine (MySQL) | Fine | Concern | Concern |
| Concurrent access | Single process | Multi-process | Multi-process | Multi-process | Single process | Single process |

**Key insight**: SQLite is the right choice for single-process embedded NVRs under 500K recordings. MiBee NVR's current 67K rows is well within this range. The issues are not about SQLite's limits but about **query patterns** (N+1 loops) and **merge reliability** (47% failure).

---

## 6. N+1 Query Pattern Analysis

Eight confirmed N+1 patterns were identified across the codebase:

### Pattern 1: Cleanup — per-camera loop

**Location:** `internal/cleanup/cleanup.go:131` (`timeBasedCleanup`)

```go
for _, cam := range cameras {                          // 1 query (ListCameras)
    recordings, err := cm.db.ListExpiredRecordingsByCamera(ctx, cam.ID, retentionDays)  // N queries
    for _, rec := range recordings {
        cm.deleteRecording(ctx, &rec)                  // M queries (N+1 inside)
    }
}
```

**Queries:** 1 + N (cameras) × (1 + M recordings). For 10 cameras with ~200 expired recordings each: **1 + 10 + 2,000 = 2,011 queries**.

### Pattern 2: Cleanup — AI status per-recording

**Location:** `internal/cleanup/cleanup.go:242` (`deleteRecording` → `GetRecordingAIStatus`)

```go
func (cm *CleanupManager) deleteRecording(ctx context.Context, rec *model.Recording) error {
    if status, err := cm.db.GetRecordingAIStatus(ctx, rec.ID); err == nil && status == "processing" {
        // N+1: one SELECT per recording
    }
}
```

**Queries:** 1 SELECT per recording. Fixed in batch by querying AI status in the initial `ListExpired*` query via a JOIN or subquery.

### Pattern 3: Cleanup — row-by-row DeleteRecording

**Location:** `internal/cleanup/cleanup.go:154-158` (inside `timeBasedCleanup`)

```go
for _, rec := range recordings {
    if err := cm.deleteRecording(ctx, &rec); err != nil { ... }
}
```

Each call to `deleteRecording` executes `DeleteRecording(ctx, rec.ID)` — a single-row DELETE. For 200 recordings: **200 DELETE statements**. Should use `DeleteRecordingsBatch` (`internal/storage/db_recording.go:354`) which issues a single `DELETE ... WHERE id IN (...)`.

### Pattern 4: Merge — per-camera processCamera

**Location:** `internal/merge/manager.go:238` (`processCamera`)

```go
func (m *MergeManager) processCamera(ctx context.Context, cameraID string, ...) (...) {
    windows, err := m.db.ListCameraMergeWindows(ctx, cameraID, minAge)     // 1 query
    for _, win := range windows {
        recs, err := m.db.ListMergeableSegments(ctx, cameraID, win.StartTime, win.EndTime)  // N queries
        ...
    }
    singletons, err = m.db.ListSingletonPendingRecordings(ctx, cameraID, minAge)  // 1 query (correlated subquery)
}
```

For each merge window (typically 24 per camera per day), `ListMergeableSegments` is called separately. **Queries:** 1 + N_windows + 1.

### Pattern 5: API — handleBatchDeleteRecordings

**Location:** `internal/api/handlers_recording.go:301` (`handleBatchDeleteRecordings`)

```go
for _, id := range body.IDs {
    rec, err := h.db.GetRecording(ctx, id)  // N queries: one per ID
    if err == nil && rec != nil && rec.FilePath != "" {
        filePaths[id] = rec.FilePath
    }
}
```

For 100 IDs: **100 SELECT queries** before the batch DELETE. Fixed by selecting all file paths with a single `SELECT file_path FROM recordings WHERE id IN (...)`.

### Pattern 6: API — handleTranscodingBackfill

**Location:** `internal/api/handlers_transcode.go:596` (`handleTranscodingBackfill`)

```go
for _, rec := range recordings {
    task := &storage.TranscodeTask{ ... }
    if err := h.db.EnqueueTask(r.Context(), task); err != nil {  // N queries: one INSERT per recording
        ...
    }
}
```

For 500 untranscoded recordings: **500 INSERT statements**. Fixed by batch INSERT.

### Pattern 7: Stats — GetRecordingTrends loads all rows

**Location:** `internal/storage/db_stats.go:27-120` (`GetRecordingTrends`)

```go
query := `SELECT r.started_at, r.file_size, r.camera_id, COALESCE(c.name, r.camera_id) as camera_name
    FROM recordings r LEFT JOIN cameras c ON r.camera_id = c.id
    WHERE r.started_at >= ?
    ORDER BY r.started_at`
```

Loads ALL raw rows for the date range into Go memory and aggregates in Go code. For 30 days at ~2,200 rows/day: **66,000 rows scanned**, all loaded into RAM, then aggregated with a Go map. Fixed by using SQL-level `GROUP BY strftime(...)`.

### Pattern 8: Merge — ListSingletonPendingRecordings correlated subquery

**Location:** `internal/storage/db_merge.go:194-212` (`ListSingletonPendingRecordings`)

```go
SELECT r.id, ... FROM recordings r
WHERE r.camera_id = ?
  AND ...
  AND (
    SELECT COUNT(*) FROM recordings r2
    WHERE r2.camera_id = r.camera_id
      AND strftime('%Y-%m-%d %H', r2.started_at) = strftime('%Y-%m-%d %H', r.started_at)
  ) = 1;
```

The correlated subquery executes once per candidate row. With N pending recordings, this is **1 outer query + N subquery executions**. Without an index on `strftime()`, each subquery does a full scan. This pattern currently runs on every merge pass.

---

## 7. Growth Projections

### 7.1 Recording Row Growth

Based on ~2,200 new recordings/day (67K total ÷ ~30 days):

| Period | Row Estimate | DB Size (metadata) | Notes |
|--------|-------------|-------------------|-------|
| Current | ~67,000 | ~86 MB | Production baseline |
| 30 days | ~8,000 new | ~10 MB incremental | ~3 new cameras × 365 recordings/day |
| 90 days | ~24,000 new | ~31 MB incremental | Typical deployment horizon |
| 1 year | ~159,000 new | ~205 MB incremental | At current camera count |
| 5 years | ~795,000 new | ~1 GB incremental | Without retention policy changes |

### 7.2 File Data Growth (Video Files)

| Period | Storage Required (90 GB/day) |
|--------|------------------------------|
| 1 day | 90 GB |
| 7 days | 630 GB |
| 30 days | 2.7 TB |
| 90 days | 8.1 TB |

*Note: Actual retention is managed by disk-threshold cleanup and per-camera retention_days. The 90 GB/day estimate depends heavily on resolution, codec, and camera count.*

### 7.3 When SQLite Becomes a Concern

At current growth rates:

- **DB metadata reaches 1 GB**: ~2-3 years (acceptable for SQLite)
- **Recording rows exceed 500K**: ~6-12 months (practical SQLite comfort zone for metadata)
- **Query time exceeds 500ms for common patterns**: ~3-6 months without optimization (N+1 fixes will push this further)
- **WAL checkpoint latency exceeds 1 second**: Already possible on RPi 3B SD cards under load

---

## 8. Best Practices Catalog

### 8.1 ANALYZE Scheduling

SQLite's query planner uses table/index statistics collected by `ANALYZE`. Without fresh statistics, the planner may choose poor query plans:

- **Current state:** `PRAGMA optimize` runs once at startup (`db.go:377`) — this performs incremental ANALYZE where needed, but only for the current connection's session
- **Recommended:** Schedule `ANALYZE` via a timer (hourly or after every 1000 INSERT/DELETE operations)
- **SQL:** `PRAGMA optimize` (incremental) or `ANALYZE` (full rebuild)
- **Reference:** [SQLite — ANALYZE](https://www.sqlite.org/lang_analyze.html)

### 8.2 WAL Checkpoint Strategy

WAL files grow with write activity. Regular checkpointing keeps WAL size manageable:

- **Current state:** Default automatic checkpoint (triggered at 1000 pages ≈ 4MB)
- **Recommended:** Periodic explicit checkpoint via `PRAGMA wal_checkpoint(TRUNCATE)` every 5 minutes or after batch operations
- **Tradeoff:** Frequent checkpoints increase disk writes (SD card wear) but reduce WAL recovery time on crash
- **Reference:** [SQLite — WAL mode](https://www.sqlite.org/wal.html)

### 8.3 Batch DELETE Patterns

- **Anti-pattern:** Row-by-row `DELETE FROM recordings WHERE id=?` in a loop (current in cleanup.go `deleteRecording`)
- **Preferred:** Single `DELETE FROM recordings WHERE id IN (?, ?, ...)` with up to 100 IDs per batch (as implemented in `DeleteRecordingsBatch`, `db_recording.go:354`)
- **Transaction boundary:** Wrap batch DELETE + batch file deletion in a single transaction

### 8.4 Covering Indexes

- **Anti-pattern:** `SELECT *` queries that read 12+ columns from recordings but filter on `camera_id` and `started_at`
- **Preferred:** Design covering indexes that include all selected columns to avoid B-tree lookups
- **Example:** `idx_recordings_camera_time (camera_id, started_at, ended_at, archived)` covers the common ListRecordings query without touching the table
- **New in v22:** `idx_recordings_archived_time (archived, started_at DESC)` avoids the zero-selectivity problem of `idx_recordings_archived`

### 8.5 Connection Pooling

- **Current state:** Uses `sql.Open()` with `modernc.org/sqlite` defaults
- **SQLite behavior:** Each connection in the pool operates independently; pool size > 1 with WAL mode is safe for read concurrency
- **Recommended:** Set `SetMaxOpenConns(1)` or tune for the specific workload. `modernc.org/sqlite` has limited connection-pool support compared to `mattn/go-sqlite3`
- **Note:** The DSN-level pragmas ensure EVERY connection in the pool uses the same settings (`busy_timeout`, `cache_size`, etc.) — this is correctly done in `New()` (`db.go:42`)

### 8.6 temp_store

- **Current state:** `temp_store` defaults to FILE (uses disk for temp tables and sort results)
- **Recommended:** `PRAGMA temp_store=MEMORY` for RPi 3B (sufficient RAM for recording metadata queries)
- **Reference:** [SQLite — temp_store](https://www.sqlite.org/pragma.html#pragma_temp_store)
- **Tradeoff:** Memory temp_store risks `SQLITE_NOMEM` for very large sort operations. Given the metadata size (< 100 MB), this risk is acceptable.

### 8.7 Additional Best Practices

| Practice | Current | Target | Priority |
|----------|---------|--------|----------|
| Foreign keys | Not enforced | `PRAGMA foreign_keys=ON` | Low |
| Incremental BLOB I/O | Not used | Relevant for large `metadata` fields | Low |
| `PRAGMA mmap_size` | 256 MB | 256 MB (correct for 1 GB RAM) | Current OK |
| VACUUM schedule | Never | `VACUUM` after large batch deletes | Medium |
| `PRAGMA page_size` | 4096 (default) | 8192 for metadata-heavy workloads | Low |

---

## 9. Future Scalability Path

### 9.1 Partitioning Pattern (Documented, Not Implemented)

For SQLite-based scaling beyond 500K rows, consider **manual partitioning by time**:

```text
recordings_2025_01
recordings_2025_02
recordings_2025_03
...
```

Each partition is a separate table; queries are routed via a view or application logic. This keeps each partition under 50K rows and avoids SQLite's single-table size limits.

**Trigger conditions for partitioning:**
- Recording table exceeds 500K rows
- Query latency for ListRecordings exceeds 2 seconds despite covering indexes
- Database file exceeds 1 GB

**Anti-pattern to avoid:** Premature partitioning. At 67K rows, partitioning adds complexity with zero benefit.

### 9.2 PostgreSQL Migration Triggers

The following events would trigger a migration from SQLite to PostgreSQL:

| Trigger | Threshold | Action |
|---------|-----------|--------|
| DB file size | > 2 GB | Evaluate PostgreSQL migration |
| Recording count | > 2 million rows | Migrate for query performance |
| Concurrent readers | > 5 simultaneous connections (beyond our single-process model for P2P) | Move to client-server DB |
| Write throughput | > 500 writes/second sustained | PostgreSQL handles higher TPS |
| Multi-instance requirement | P2P or distributed deployment | PostgreSQL or read replicas |

**Migration strategy** (when triggered):
1. Run `VACUUM INTO` (already implemented: `db.go:418`) for clean SQLite backup
2. Export via `.dump` or use `pgloader` for bulk transfer
3. Implement dual-write during transition period (write to both SQLite + PostgreSQL)
4. Switch reads to PostgreSQL once latency is verified

### 9.3 Read Replicas (PostgreSQL Migration)

If PostgreSQL migration occurs, the architecture supports:

- **Primary:** Writes go to single PostgreSQL primary
- **Replicas:** Read-only queries (playback, stats, camera list) go to replicas
- **Caching:** Frontend caching (API response caching) reduces read load on the metadata store
- **Separation:** Recording file paths remain in the DB; the actual video files always stay on local/network storage

---

## References

- SQLite documentation: https://www.sqlite.org/docs.html
- `internal/storage/db.go` — Schema, migrations, DSN, `Init()`, `Backup()`
- `internal/storage/db_recording.go` — Recording CRUD, query methods
- `internal/storage/db_merge.go` — Merge window/segment queries
- `internal/storage/db_stats.go` — Stats aggregation (`GetRecordingTrends`)
- `internal/cleanup/cleanup.go` — `timeBasedCleanup()`, `deleteRecording()`, `diskThresholdCleanup()`
- `internal/merge/manager.go` — `processCamera()`, `RunOnce()`
- `internal/api/handlers_recording.go` — `handleBatchDeleteRecordings()`
- `internal/api/handlers_transcode.go` — `handleTranscodingBackfill()`
