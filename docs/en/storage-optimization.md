# Storage Optimization Guide

> Target: Raspberry Pi 3B (1GB RAM, SD card root, USB HDD data).  
> Database: SQLite via modernc.org/sqlite (pure Go, CGO_ENABLED=0).  
> Module: `github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage`

---

## 1. Architecture Overview

MiBee NVR stores all metadata in a single SQLite database with **WAL (Write-Ahead Log)** mode, using a pure-Go SQLite driver (`modernc.org/sqlite`) — no CGO, no cross-compile issues. MP4 segment files are managed by the `Manager` struct with atomic `temp → rename` semantics.

### Component Layers

```text
┌─────────────────────────────────────────┐
│  Internal API Handlers                   │
├─────────────────────────────────────────┤
│  DB Layer (internal/storage/)            │
│  ┌─────────────┐  ┌──────────────────┐   │
│  │ DB struct    │  │ Manager struct    │   │
│  │ (SQLite CRUD)│  │ (File operations) │   │
│  └─────────────┘  └──────────────────┘   │
├─────────────────────────────────────────┤
│  modernc.org/sqlite (pure Go)            │
│  WAL mode, mmap_size=256MB              │
└─────────────────────────────────────────┘
```

### DSN Configuration

Constructed in `internal/storage/db.go:New()`. DSN-level `_pragma` ensures every connection inherits settings — critical for `busy_timeout` across goroutines.

```go
func New(dbPath string) (*DB, error) {
    dsn := dbPath + "?_pragma=journal_mode(WAL)" +
        "&_pragma=synchronous(NORMAL)" +
        "&_pragma=busy_timeout(15000)" +
        "&_pragma=cache_size(-20000)" +
        "&_pragma=mmap_size(268435456)"
    db, err := sql.Open("sqlite", dsn)
    if err != nil { return nil, err }
    return &DB{path: dbPath, db: db}, nil
}
```

### Connection Lifecycle

1. `New(dbPath)` — Opens single `*sql.DB` with DSN pragmas
2. `Init(ctx)` — Creates tables, runs migrations, creates indexes
3. `Close()` — Closes the underlying database
4. `Backup(ctx, destPath)` — Uses `VACUUM INTO` for online backup

### Key Constraints (RPi 3B)

- Max 4 HLS streams, segment duration ≤ 30s, process memory ≤ 512MB
- SD card rootfs → NORMAL sync (WAL mitigates fsync overhead)
- Busy timeout 15s → accommodates SD card latency spikes

---

## 2. DSN/PRAGMA Tuning Reference

| PRAGMA | Value | Rationale | RPi Constraint |
|--------|-------|-----------|----------------|
| `journal_mode` | `WAL` | Concurrent reads during writes; no readers block writers | WAL checkpoint writes 3×+ less fsync than DELETE mode on SD |
| `synchronous` | `NORMAL` | Crash-safe at NORMAL; FULL adds 2×+ fsync per transaction | NORMAL vs FULL reduces SD wear 40-60% |
| `busy_timeout` | `15000` (15s) | Automatic retry on SQLITE_BUSY | SD latency spikes can hit 5-10s |
| `cache_size` | `-20000` (20MB) | Negative = KiB. Query planner + index lookup cache | 1GB RAM → 20MB conservative |
| `mmap_size` | `268435456` (256MB) | Memory-map reads; avoids syscall per lookup | 50% of memory budget; enough for ~10GB DB |
| `temp_store` | `MEMORY` | Temp tables/indexes in RAM | Avoids SD card writes for temp data |
| `analysis_limit` | `1000` | ANALYZE with sampled rows | 1000 rows sufficient for ~100K-row tables |

### Full DSN String

```text
file:/mnt/data/nvr/mibee-nvr.db?_pragma=journal_mode(WAL)&
  _pragma=synchronous(NORMAL)&_pragma=busy_timeout(15000)&
  _pragma=cache_size(-20000)&_pragma=mmap_size(268435456)
```

### Post-Init Optimization

After migrations, `Init()` runs `PRAGMA optimize` to trigger SQLite's self-tuning (statistics refresh, index analysis).

---

## 3. Index Strategy

### Current Indexes (schema v22)

| Index | Columns | Purpose | Created In |
|-------|---------|---------|------------|
| `idx_recordings_camera` | `camera_id` | **Legacy** — superseded by composite index | Init |
| `idx_recordings_time` | `started_at` | Time-range scans without camera filter | Init |
| `idx_recordings_merged` | `merged` | **Legacy** — superseded | Migration v4 |
| `idx_recordings_archived` | `archived` | **Legacy** — superseded | Migration v8 |
| `idx_recordings_camera_time` | `camera_id, started_at, ended_at, archived` | Primary: ListRecordings | Migration v9 |
| `idx_recordings_archived_time` | `archived, started_at DESC` | Dominant list pattern (WHERE archived=0 ORDER BY started_at DESC) | Migration v19 |
| `idx_recordings_camera_ended` | `camera_id, ended_at DESC` | Covering: GetAllLastRecordingTimes | Migration v19 |
| `idx_recordings_merge_status` | `merge_status` | Merge pipeline queries | Migration v13 |

### Redundancy Analysis

Three indexes are **superseded** by composite indexes:
1. **`idx_recordings_camera`** — Covered by `idx_recordings_camera_time` via leftmost prefix.
2. **`idx_recordings_merged`** — Replaced by `idx_recordings_merge_status` (richer TEXT states).
3. **`idx_recordings_archived`** — Covered by `idx_recordings_archived_time`.

### Why Keep `idx_recordings_time`

Covers time-range scans without `camera_id` filter, used by `GetRecordingTrends`:

```sql
SELECT * FROM recordings WHERE started_at >= '2026-01-01'
  AND started_at < '2026-02-01' ORDER BY started_at;
```

### Index Maintenance Plan

```sql
REINDEX idx_recordings_camera_time;
REINDEX idx_recordings_archived_time;
```

[Done] `idx_recordings_camera_merge_status` on `(camera_id, merge_status, started_at)` now exists (added post-v23, `db.go` Init). Serves `ListMergeableSegments`/`ListSingletonPendingRecordings` (`WHERE camera_id=? AND merge_status='pending' AND started_at>=?`). Additionally `idx_recordings_archived_ended (archived, ended_at)` serves global retention scans (`ListExpiredRecordings`/`ListOldestRecordings` — formerly full-scan + sort).

---

## 4. Query Optimization Guide

### Example 1: ListRecordings (Paginated) — list + cached count

Uses `idx_recordings_archived_time` covering index (index-sorted by `started_at DESC`, no temp sort). Handlers call `ListRecordingsWithTotal`, which runs the efficient `ListRecordings` for the page plus a **cached** `CountRecordingsWithFilter` for the total:

```sql
-- page (uses idx_recordings_archived_time, no sort)
SELECT id, camera_id, ... FROM recordings WHERE <filters> ORDER BY started_at DESC LIMIT ? OFFSET ?;
-- total (uses idx_recordings_archived_ended covering index, cached 2s)
SELECT COUNT(*) FROM recordings WHERE <filters>;
```

**Why not `COUNT(*) OVER()` (single query)?** Production profiling on 86K rows proved the window-function approach is a **regression**: SQLite's planner cannot satisfy ORDER BY from the index when `OVER()` materializes the set, so it falls back to a full index scan + TEMP B-TREE FOR ORDER BY — **3.9s** vs **~6ms** for the separated queries. The count cache (2s TTL, keyed by filter signature excluding Limit/Offset/Sort) collapses repeated counts from rapid pagination and concurrent views (gallery + list) so the COUNT typically runs once per filter change, not once per page.

The WHERE-clause builder is shared (`recordingsFilterWhere`) with the standalone `CountRecordingsWithFilter` (used directly by callers that need only a count, no page). The plain `ListRecordings` (no total) remains for callers that don't need pagination totals.

### Example 2: GetAllLastRecordingTimes — Was #1 Bottleneck

**Before (pre-v19):** Full scan of `idx_recordings_camera_time` (71K+ entries per `/api/cameras` request):

```sql
SELECT camera_id, MAX(ended_at) FROM recordings WHERE ended_at IS NOT NULL GROUP BY camera_id;
```

**After (v19):** `idx_recordings_camera_ended` covering index → O(cameras), not O(recordings):

```go
_, _ = d.db.ExecContext(ctx, "CREATE INDEX IF NOT EXISTS idx_recordings_camera_ended ON recordings(camera_id, ended_at DESC)")
```

### Example 3: ListSingletonPendingRecordings (CTE Rewrite)

Current query uses correlated subquery. **Optimization** — rewrite as CTE:

```sql
WITH hourly_counts AS (
    SELECT id, camera_id, format, strftime('%Y-%m-%d %H', started_at) AS hour_bucket,
           COUNT(*) OVER (PARTITION BY camera_id, format, strftime('%Y-%m-%d %H', started_at)) AS cnt
    FROM recordings WHERE camera_id = ? AND merge_status = 'pending'
      AND ended_at IS NOT NULL AND ended_at < ?
)
SELECT * FROM hourly_counts WHERE cnt = 1;
```

[Pending T4] Deployed with the `incompatible` status migration.

### Example 4: ListExpiredRecordings

Sub-millisecond at ~100K rows via `idx_recordings_camera_time(ended_at)`:

```sql
SELECT id, camera_id, file_path, format, started_at, ended_at,
       duration, file_size, frame_count, merged, merge_status, archived
FROM recordings WHERE ended_at IS NOT NULL AND archived = 0
  AND camera_id = ? AND ended_at < datetime('now', '-' || ? || ' days')
ORDER BY ended_at ASC;
```

### GetRecordingTrends — SQL aggregation + timeout [Done]

Aggregation is pushed to SQL (GROUP BY date/camera in `db_stats.go:44`), so only aggregated rows transfer to Go — not raw recordings. A 10s `heavyQueryTimeout` context bounds the query (caller deadlines take precedence), and it runs on the read pool so it never blocks `InsertRecording`. Further optimization (materialized daily-stats table refreshed by cleanup) remains optional for 500K+ scale.

---

## 5. Batch Operation Patterns

### Batch DELETE: 200 Rows/Batch

Cleanup module deletes recordings in batches. The batch API accepts up to 100 IDs:

```go
func (d *DB) DeleteRecordingsBatch(ctx context.Context, ids []string) ([]string, error) {
    placeholders, args := make([]string, len(ids)), make([]interface{}, len(ids))
    for i, id := range ids { placeholders[i] = "?"; args[i] = id }
    q := "DELETE FROM recordings WHERE id IN (" + strings.Join(placeholders, ",") + ")"
    res, err := d.db.ExecContext(ctx, q, args...)
    // ...
}
```

### Batch INSERT (Orphan Recovery)

500-record batches in single transactions:

```go
const orphanBatchSize = 500
func (d *DB) InsertOrphanRecordings(ctx context.Context, recordings []*model.Recording) (int, error) {
    for i := 0; i < len(recordings); i += orphanBatchSize {
        tx, err := d.db.BeginTx(ctx, nil)
        for _, r := range recordings[i:min(i+orphanBatchSize, len(recordings))] {
            tx.ExecContext(ctx, q, r.ID, r.CameraID, ...)
        }
        if err := tx.Commit(); err != nil { return totalInserted, err }
    }
}
```

### Transactional Merge-Replace

`MergeAndReplaceRecordings` combines INSERT + DELETE in one transaction. **Never delete source files before the DB transaction commits:**

```go
if err := storage.RetryOnBusy(ctx, func() error {
    return m.db.MergeAndReplaceRecordings(ctx, mergedRec, ids)
}); err != nil {
    os.Remove(finalPath); continue
}
for _, r := range recordings { m.store.DeleteFile(r.FilePath) }
```

### Event Publishing on Deletion

Publish events so MiBeeVision cancels in-progress AI processing. Skip recordings with `ai_status = "processing"`:

```go
func (cm *CleanupManager) deleteRecording(ctx context.Context, rec *model.Recording) error {
    if status, _ := cm.db.GetRecordingAIStatus(ctx, rec.ID); status == "processing" { return nil }
    if err := cm.db.DeleteRecording(ctx, rec.ID); err != nil { return err }
    // best-effort file delete
    if err := cm.store.DeleteFile(rec.FilePath); err != nil {
        logger.Warn("failed to delete file", "file_path", rec.FilePath, "error", err)
    }
    cm.eventBus.Publish(ctx, event.TopicSegmentDeleted, event.SegmentDeleted{
        RecordingID: rec.ID, CameraID: rec.CameraID, FilePath: rec.FilePath, Reason: "retention_expired",
    })
    return nil
}
```

### SQLITE_BUSY Retry Pattern

All merge DB operations wrapped with `RetryOnBusy` (exponential backoff: 100ms → 200ms → 400ms, max 3 retries). [Pending T5] adaptive sleep in batch DELETE using sliding window of recent busy-error frequency.

---

## 6. DB Maintenance Schedule

### ANALYZE Cadence (PRAGMA optimize)

`PRAGMA optimize` runs on **every startup** after migrations and **once per cleanup cycle** (default hourly). For DBs >500K rows, schedule full `ANALYZE` weekly during low activity. `analysis_limit=1000` keeps it fast.

### WAL Checkpoint Triggers

Default: automatic at ~1MB (wal_autocheckpoint=1000). When WAL exceeds 50MB, try PASSIVE first. If 3 consecutive passive checkpoints report `busy > 0`, switch to TRUNCATE:

```sql
PRAGMA wal_checkpoint(PASSIVE);
PRAGMA wal_checkpoint(TRUNCATE);  -- brief write lock, <10ms
```

### incremental_vacuum (Never Full VACUUM)

Full `VACUUM` rewrites the entire DB, doubles storage temporarily, and risks OOM on RPi 3B. Use incremental reclaim:

```sql
PRAGMA auto_vacuum = INCREMENTAL;
PRAGMA incremental_vacuum(256);  -- ~2MB per cycle
```

[Done] `performDatabaseMaintenance` (cleanup.go) calls `IncrementalVacuum(1000)` when fragmentation > 20%, escalating WAL checkpoint from PASSIVE→TRUNCATE after 3 consecutive busy cycles. Full `VACUUM` is never run (blocks 24/7 inserts).

---

## 7. Merge Pipeline Design

### Flow

```text
Recordings created (status = 'pending')
    │
    ▼
MergeManager.RunOnce() (ticker: configurable, default 1h)
    │
    ├─ ListCameraMergeWindows() → hourly windows with 2+ segments
    ├─ processCamera() per camera
    │   ├─ acquireMergeLock() (per-camera mutex, non-blocking)
    │   ├─ ListMergeableSegments()
    │   ├─ groupByFormat() → separate H.264, H.265, MJPEG, AVI
    │   ├─ mergeFormatGroup()
    │   │   ├─ ParseSegment() (MP4 sample table extraction)
    │   │   ├─ SHA-256 SPS/PPS grouping
    │   │   ├─ Disk space check (1.1x estimate)
    │   │   ├─ MergeMP4Segments() (streaming merge, 1MB buffer)
    │   │   ├─ MergeAndReplaceRecordings() (transactional)
    │   │   └─ Delete source files (post-commit)
    │   └─ ListSingletonPendingRecordings() → mark as 'merged'
    │
    └─ Update metrics
```

### Segment Lifecycle States

| Status | Meaning | Transitions |
|--------|---------|-------------|
| `pending` | Awaiting merge | → `merged`, `failed`, `incompatible` |
| `merged` | Part of merged output | (terminal, deletable by retention) |
| `failed` | Parse error or SPS/PPS mismatch | (terminal) |
| `incompatible` | [Pending T4] Cross-format/codec, never mergeable | (terminal) |

### Failure Semantics

- **Parse failure**: Marked `failed`, skipped in future passes
- **Disk space insufficient**: Camera skipped, retried next pass
- **DB transaction failure**: Merge output deleted, source files preserved
- **SPS/PPS group too small**: Marked `failed` to avoid repeated parsing

### Backfill Endpoint [Pending T10]

`POST /api/cameras/{id}/merge/backfill` manually triggers merge for historical segments, including already-marked ones (forced reprocess).

---

## 8. Connection Pool Design

### SetMaxOpenConns(1) — Single Writer Serialization

SQLite has a single writer. With multiple connections: A begins write → B attempts write → `SQLITE_BUSY` → retry. With `SetMaxOpenConns(1)`, all writes serialize on one connection:

```go
d.db.SetMaxOpenConns(1)    // Serialize all writes
d.db.SetMaxIdleConns(1)    // Keep 1 warm connection
d.db.SetConnMaxLifetime(0) // SQLite is local, no stale conns
```

**Trade-off:** Long queries (GetRecordingTrends) block writes. Mitigations: WAL concurrent reads, low-activity `PRAGMA optimize`, context-cancellable queries, **separate read pool** (see below).

### Separate Read Pool (read/write isolation)

At 500K+ rows, the single serialized writer connection becomes a bottleneck: a heavy `GetRecordingTrends` or `ListRecordings` SELECT blocks `InsertRecording` (the 24/7 recording hot path). The DB now opens a **second, read-only connection pool** so SELECTs never contend with the writer:

```go
// Writer pool: single serialized connection for INSERT/UPDATE/DELETE/transactions
d.db        // SetMaxOpenConns(1)

// Read pool: up to N concurrent connections for SELECT, query_only enforced
d.readDB    // SetMaxOpenConns(3), _pragma=query_only(1)
```

All `QueryContext`/`QueryRowContext` calls route through `readConn()` (falls back to `d.db` when `readDB` is nil, e.g. in tests). Transactions and `ExecContext` (writes) stay on `d.db`. WAL mode permits concurrent readers alongside the single writer, so `InsertRecording` is no longer stalled by a slow analytics query.

**Safety:** the read pool sets `query_only(1)` — any write through it is rejected by SQLite, so a misrouted SELECT can never mutate data.

**Tuning:** `SetReadPoolSize(n)` raises the pool on high-RAM hosts (Banana Pi M5). Each connection holds its own ~20MB page cache, so the default 3 connections ≈ 60MB beyond the writer's cache. Lower it under memory pressure.

### WAL Read Concurrency

```text
Writer (WAL):  INSERT/UPDATE/DELETE  →  appends to WAL file
Reader 1/2/3:  SELECT (read pool)    →  reads from main DB + WAL index, concurrent
```

### temp_store = MEMORY

Keeps temp tables/indexes in RAM instead of SD card writes. Acceptable within 512MB budget; critical for RPi 3B where SD I/O is the bottleneck.

---

## 9. Observability

### Prometheus Metrics

All metrics on a **custom registry** (`prometheus.NewRegistry()`), not global default.

#### Storage & Database Metrics

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `nvr_recording_count` | Gauge | — | Current recording records |
| `nvr_storage_used_bytes` | Gauge | — | Disk space used by recordings |
| `nvr_storage_total_bytes` | Gauge | — | Total disk space |
| `nvr_recording_bytes_total` | CounterVec | camera_id, codec | Bytes written per camera/codec |
| `nvr_storage_write_errors_total` | Counter | — | Write I/O errors |
| `nvr_cleanup_deleted_total` | CounterVec | reason | Deletions by reason |

#### Merge Pipeline Metrics

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `nvr_merge_attempts_total` | Counter | — | Merge attempts |
| `nvr_merge_successes_total` | Counter | — | Successful merges |
| `nvr_merge_failures_total` | CounterVec | reason | Failures (sps_pps_mismatch, parse_error, db_error, disk_space, timeout, audio_mismatch, io_error) |
| `nvr_merge_duration_seconds` | Histogram | — | Merge duration |
| `nvr_merge_size_bytes` | Histogram | — | Output file size |
| `nvr_merge_pending_segments` | GaugeVec | camera_id | Awaiting merge per camera |

#### SQLite Health Metrics [Done]

All implemented and wired. `nvr_sqlite_*` metrics are updated on a **60-second ticker** (near-real-time, no longer gated to the hourly cleanup cycle) from `cleanup.Run()`.

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `nvr_sqlite_wal_size_bytes` | Gauge | — | WAL size — alert >100MB |
| `nvr_sqlite_db_size_bytes` | Gauge | — | DB file size |
| `nvr_sqlite_fragmentation_ratio` | Gauge | — | freelist_count/page_count — alert >20% |
| `nvr_sqlite_query_duration_seconds` | Histogram | query_name | Per-query latency (instrumented: `InsertRecording`, `ListRecordings`, `ListRecordingsWithTotal`, `CountRecordingsWithFilter`, `GetRecordingTrends`) |
| `nvr_sqlite_busy_errors_total` | Counter | — | SQLITE_BUSY retries across all ops (incremented in `RetryOnBusy`) |
| `nvr_sqlite_open_connections` | Gauge | — | Writer pool open connections (from `db.Stats()`) |
| `nvr_sqlite_in_use_connections` | Gauge | — | Writer pool in-use connections |
| `nvr_cleanup_duration_seconds` | Histogram | — | Time per cleanup cycle |

### Alerting Thresholds

| Condition | Severity | Threshold | Action |
|-----------|----------|-----------|--------|
| WAL file size | Warning | >50MB | TRUNCATE checkpoint |
| WAL file size | Critical | >200MB | Analyze read patterns |
| DB fragmentation | Warning | >20% | incremental_vacuum |
| Query duration (p99) | Warning | >500ms | ANALYZE, check indexes |
| Merge failure rate | Warning | >10% in 1h | Investigate corruption |
| SQLITE_BUSY rate | Warning | >100/hour | Reduce concurrent writers |

---

## 10. Future Scaling Playbook

### Decision Tree

```text
Current DB Size?
│
├─ < 100MB → No action needed. Current config sufficient.
│
├─ 100MB – 500MB → ANALYZE weekly, PRAGMA optimize on startup
│   └─ Ensure idx_recordings_archived_time exists (v19+)
│
├─ 500MB – 10GB → Partition by time (monthly tables)
│   └─ [Pending T8] recordings_2026_01, recordings_2026_02, etc.
│       UNION ALL view for transparent querying
│
└─ > 10GB → Migrate to PostgreSQL (external process)
    └─ [Pending T12] Steps: pgx/v5 driver, pg_cron, streaming replication
```

### When to Partition (500K+ Rows)

Signs: `ListRecordings` with time filter >200ms, `CountRecordingsWithFilter` >500ms, `GetRecordingTrends` (7d) >1s, WAL checkpoint >5s.

**Proposed [Pending T8]:**

```sql
CREATE TABLE recordings_2026_01 (
    CHECK (started_at >= '2026-01-01' AND started_at < '2026-02-01')
) INHERITS (recordings);
-- Or SQLite approach: ATTACH 'recordings_2026_01.db' AS jan;
```

### When to Migrate to PostgreSQL (10GB+ DB)

Signs: DB >10GB, VACUUM >30min, WAL >500MB, need concurrent writes, need PITR/replication.

**Prerequisites [Pending T12]:** PostgreSQL 16+, pgx/v5 driver, schema translation (DATETIME→TIMESTAMPTZ, INTEGER→SERIAL/BIGSERIAL, REAL→DOUBLE PRECISION).

### When NOT to Migrate

Stay on SQLite if: recordings <500K, DB <5GB, single NVR instance, RAM <1GB, SD card primary storage.

### Long-Term Strategy

```text
Phase 1 (Current): SQLite single-file, WAL mode, optimized indexes
Phase 2 (T8):      Time-based partition tables or separate DB files
Phase 3 (T12):     PostgreSQL migration with pgx driver
Phase 4 (Future):  ClickHouse for analytics queries (GetRecordingTrends, long-range stats)
```

---

## 11. Storage Overhaul Changelog (2026-07)

A multi-stage optimization targeting 500K–1M `recordings` rows. All changes are backward-compatible (no schema break, no external deps).

### Stage A — Write-path & index hygiene
- **Redundant-index write amplification fixed** (`db.go` Init): `idx_recordings_merged`, `idx_recordings_archived`, `idx_recordings_camera` are now unconditionally `DROP IF EXISTS`. Previously their `CREATE IF NOT EXISTS` ran every startup but the DROP was gated on `schema_version=='22'`, so fresh installs (v23+) recreated them and never dropped → 3 extra B-tree writes per INSERT.
- **Batched merge-status updates** (`db_merge.go`): `SetMergeStatus`/`SetMergeError`/`UpdateMergeProgressBatch` use a single chunked `WHERE id IN (...)` (500/chunk) instead of a per-row loop. Timelapse progress writes (N segments × M ticks) collapsed from N×M statements to ⌈N/500⌉ per tick.

### Stage B — Read/write connection isolation
- **Separate read-only pool** (`db.go`): `readDB` (`query_only(1)`, default 3 conns) serves all SELECTs; the writer stays single-serialized. WAL allows concurrent readers + one writer, so `InsertRecording` is never blocked by a heavy SELECT. `SetReadPoolSize(n)` tunes on high-RAM hosts.
- **Heavy-query timeout** (`db_stats.go`, `db_recording.go`): `GetRecordingTrends` + `CountRecordingsWithFilter` bounded to 10s (caller deadlines take precedence), protecting the read pool from a single slow query.

### Stage C — Large-table query optimization
- **New indexes** (`db.go`): `idx_recordings_camera_merge_status(camera_id, merge_status, started_at)` (merge candidate lookup), `idx_recordings_archived_ended(archived, ended_at)` (global retention/expiry scan — formerly full-scan + sort).
- **Single-query pagination** (`db_recording.go`): `ListRecordingsWithTotal` returns a page (covering index, no sort) plus a **cached** total (2s TTL, keyed by filter signature). The initial attempt used `COUNT(*) OVER()` but production profiling on 86K rows proved it a regression (3.9s — the window function forces a full scan + temp B-tree sort). Reverted to list + cached-count: ~6ms per page, COUNT cached across rapid pagination.
- **Frontend polling discipline** (`Recordings.svelte`): `visibilitychange` pauses all polling when the tab is hidden; the 3s transcode-status poll self-stops when no task is running/pending and restarts on demand.

### Stage D — Streaming / heavy-IO debouncing
- **HLS segment scan debounced** (`hls/manager.go`): `observeNewSegments` was an `os.ReadDir` after *every frame write* (~25/s/camera). Now throttled to once per 2s per stream — syscall rate dropped ~50×.
- **JPEG latest-frame zero-copy** (`recorder/http_jpeg.go`): `LatestFrame()` returns a shared immutable slice via `atomic.Pointer[[]byte]` (was a full `make`+`copy` per poll × viewers).
- **FLV tag single-allocation** (`flv/writer.go`): `videoFrameTag` precomputes size and allocates the full tag buffer once (was 3 allocations/frame). A `sync.Pool` is intentionally *not* used — the tag buffer is shared across viewer channels and cached in gopCache, so pool reclamation would be unsafe.
- **Frame-directory listing cache** (`api/handler.go`): `?frame=N` MJPEG downloads memoize the sorted file list (500ms TTL, mtime-invalidated) instead of `os.ReadDir`+sort per request.

### Stage E — Observability
- **Query-latency histogram activated** (`metrics.go` + `db_*.go`): `nvr_sqlite_query_duration_seconds{query_name}` was defined but never observed; now instrumented on `InsertRecording`, `ListRecordings`, `ListRecordingsWithTotal`, `CountRecordingsWithFilter`, `GetRecordingTrends`.
- **SQLITE_BUSY counter** (`metrics.go` + `retry.go`): `nvr_sqlite_busy_errors_total` incremented in `RetryOnBusy` via a package hook.
- **Real-time SQLite metrics** (`cleanup.go`): WAL/DB size, fragmentation, pool stats updated on a 60s ticker (was hourly, gated to the cleanup cycle).
- **Write-path benchmarks** (`benchmark_write_test.go`): `BenchmarkInsertRecording`, `BenchmarkSetMergeStatus`, `BenchmarkUpdateMergeProgressBatch`, `BenchmarkCountRecordingsWithFilter` for regression detection.

### What was deliberately NOT done
- **PostgreSQL migration** (Pending T12) — SQLite handles 500K–1M rows with the read pool; migration is reserved for 10GB+ DBs.
- **Time-based partitioning** (Pending T8) — deferred until 500K+ rows shows measurable degradation in production metrics.
- **Keyset/cursor pagination** (OFFSET replacement) — OFFSET remains for shallow paging; cursor pagination is a future option if deep-page latency becomes visible.
- **External caches** (Redis, etc.) — violates the single-static-binary principle; OS page cache + `http.ServeFile` suffice for playback.

---

> **References:**
> - `internal/storage/db.go` — DSN, Init, migrations, read pool, time handling
> - `internal/storage/db_recording.go` — Recording CRUD, ListRecordingsWithTotal, batch operations
> - `internal/storage/db_merge.go` — MergeAndReplaceRecordings, batched SetMergeStatus/UpdateMergeProgressBatch, chunkIDs
> - `internal/storage/db_stats.go` — CountRecordings, GetRecordingTrends, heavyQueryTimeout
> - `internal/storage/retry.go` — RetryOnBusy (busy-error hook), IsBusyError
> - `internal/storage/benchmark_write_test.go` — write-path + batch benchmarks
> - `internal/cleanup/cleanup.go` — CleanupManager, 60s SQLite-metrics ticker, WAL checkpoint escalation
> - `internal/merge/manager.go` — MergeManager, processCamera, mergeFormatGroup
> - `internal/metrics/metrics.go` — Merge/storage/SQLite metrics, ObserveQueryDuration/IncSQLiteBusyErrors
> - `internal/storage/AGENTS.md` — Storage conventions
> - `docs/en/metrics.md` — Complete metrics reference
