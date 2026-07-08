# Storage Optimization Guide

> Target: Raspberry Pi 3B (1GB RAM, SD card root, USB HDD data).  
> Database: SQLite via modernc.org/sqlite (pure Go, CGO_ENABLED=0).  
> Module: `github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage`

---

## 1. Architecture Overview

MiBee NVR stores all metadata in a single SQLite database with **WAL (Write-Ahead Log)** mode, using a pure-Go SQLite driver (`modernc.org/sqlite`) — no CGO, no cross-compile issues. MP4 segment files are managed by the `Manager` struct with atomic `temp → rename` semantics.

### Component Layers

```
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

```
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

[Pending T4] Migration v23 adds `idx_recordings_camera_merge_status` on `(camera_id, started_at, merge_status)`.

---

## 4. Query Optimization Guide

### Example 1: ListRecordings (Paginated)

Uses `idx_recordings_archived_time` covering index. Dynamic query builder appends filters:

```go
func (d *DB) ListRecordings(ctx context.Context, filter model.RecordingFilter) ([]model.Recording, error) {
    where, args := []string{}, []any{}
    if filter.CameraID != "" { where = append(where, "camera_id=?"); args = append(args, filter.CameraID) }
    if !filter.StartTime.IsZero() { where = append(where, "started_at>=?"); args = append(args, formatTime(filter.StartTime)) }
    // ... more filters ...
    sqlstr := "SELECT ... FROM recordings" + buildWhere(where) + " ORDER BY " + sortBy + " " + sortOrder
}
```

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

### GetRecordingTrends — Future Optimization [Pending T6]

Push aggregation to SQL to avoid transferring raw rows to Go:

```sql
SELECT date(r.started_at) as day, COUNT(*) as recordings,
       SUM(r.file_size) as total_size, r.camera_id
FROM recordings r WHERE r.started_at >= ?
GROUP BY date(r.started_at), r.camera_id ORDER BY day;
```

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

[Pending T9] Cleanup cycle calls `IncrementalVacuum` after batch deletes.

---

## 7. Merge Pipeline Design

### Flow

```
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

**Trade-off:** Long queries (GetRecordingTrends) block writes. Mitigations: WAL concurrent reads, low-activity `PRAGMA optimize`, context-cancellable queries.

### WAL Read Concurrency

```
Writer (WAL):  INSERT/UPDATE/DELETE  →  appends to WAL file
Reader 1/2:    SELECT                →  reads from main DB + WAL index, concurrent
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

#### Proposed Metrics [Pending T9]

| Metric | Type | Purpose |
|--------|------|---------|
| `nvr_db_wal_size_bytes` | Gauge | WAL size — alert >100MB |
| `nvr_db_fragmentation_ratio` | Gauge | Fragmentation — alert >20% |
| `nvr_db_query_duration_seconds` | Histogram | Query latency (top-5 patterns) |
| `nvr_db_connection_busy_errors_total` | Counter | SQLITE_BUSY error rate |
| `nvr_cleanup_duration_seconds` | Histogram | Time per cleanup cycle |

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

```
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

```
Phase 1 (Current): SQLite single-file, WAL mode, optimized indexes
Phase 2 (T8):      Time-based partition tables or separate DB files
Phase 3 (T12):     PostgreSQL migration with pgx driver
Phase 4 (Future):  ClickHouse for analytics queries (GetRecordingTrends, long-range stats)
```

---

> **References:**
> - `internal/storage/db.go` — DSN, Init, migrations, time handling
> - `internal/storage/db_recording.go` — Recording CRUD, batch operations
> - `internal/storage/db_stats.go` — CountRecordings, GetRecordingTrends
> - `internal/storage/db_merge.go` — MergeAndReplaceRecordings, ListMergeableSegments
> - `internal/storage/retry.go` — RetryOnBusy, IsBusyError
> - `internal/cleanup/cleanup.go` — CleanupManager, time-based/disk-threshold cleanup
> - `internal/merge/manager.go` — MergeManager, processCamera, mergeFormatGroup
> - `internal/metrics/metrics.go` — Merge metrics, storage metrics
> - `internal/storage/AGENTS.md` — Storage conventions
> - `docs/en/metrics.md` — Complete metrics reference
