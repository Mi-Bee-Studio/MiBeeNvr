# MiBee NVR — Storage Package

## OVERVIEW

SQLite metadata (DB) + file operations (Manager). All recording/camera CRUD, UTC timestamp handling, atomic file lifecycle.

## STRUCTURE

```
db.go                # DB struct — SQLite WAL, read+write pools, Init() migrations, time format, PRAGMA setup
db_test.go           # DB tests — time parsing, CRUD operations, query builder
db_recording.go      # Recording CRUD, ListRecordings/ListRecordingsWithTotal, batch ops, filter builders
db_merge.go          # Merge ops — MergeAndReplaceRecordings, batched SetMergeStatus/UpdateMergeProgressBatch, chunkIDs, ListMergedRecordingsForValidation/ResetMergeStatus (startup integrity)
db_stats.go          # CountRecordings, GetRecordingTrends, heavyQueryTimeout
db_archive.go        # Archive camera + recordings
db_camera.go         # Camera CRUD + ONVIF/ingest columns
db_transcoding.go    # Transcode task queue (DequeueTask is atomic UPDATE...RETURNING, stays on writer)
db_ai.go             # AI event + status CRUD
db_recording.go      # (see above)
manager.go           # Manager struct — file create/write/close with temp→atomic rename
manager_test.go      # Manager tests — segment lifecycle, disk usage, crash recovery
benchmark_write_test.go # Write-path benchmarks (InsertRecording, SetMergeStatus, batch progress, CountRecordings)
benchmark_test.go    # ListRecordings read benchmark
retry.go             # RetryOnBusy + SetBusyErrorHook (SQLITE_BUSY retry, increments metrics counter)
```

## WHERE TO LOOK

| Task | Location | Notes |
|------|----------|-------|
| Change DB schema | `db.go` `Init()` | CREATE IF NOT EXISTS migrations; redundant-index DROP at end (unconditional) |
| Fix time handling | `timeToDB()` / `parseTime()` / `scanTime()` | UTC storage, 5+ legacy format backward compat |
| Add camera field | `CameraRow` struct + `createCamerasTable()` | Add column + update SELECT/INSERT/UPDATE | **Schema v27**: Added `stable_id` column for ONVIF serial persistence |
| Archive cleanup tasks | `db_archive.go` | CRUD for archive_cleanup_tasks table (async delete queue) |
| Query recordings (paginated) | `ListRecordingsWithTotal()` | Page via `ListRecordings` (covering index) + cached count (2s TTL). Do NOT use `COUNT(*) OVER()` — it's a proven regression (full scan + temp sort). |
| Query recordings (count only) | `CountRecordingsWithFilter()` | Shares `recordingsFilterWhere` with List methods; cached when called via ListRecordingsWithTotal |
| Batch merge-status update | `SetMergeStatus`/`SetMergeError`/`UpdateMergeProgressBatch` | Chunked `WHERE id IN (...)` via `chunkIDs(ids, 500)` |
| Startup merge integrity | `ListMergedRecordingsForValidation()` / `ResetMergeStatus()` | Startup scan: list rows where `merge_status='merged'`; caller `os.Stat`s each `merge_path` and resets any that are missing/empty so playback falls back to frames. Called from `pkg/app/run.go:validateMergedRecordings` |
| Route a new SELECT | use `d.readConn()` (read pool) | Writes/transactions stay on `d.db` (single serialized writer) |
| Tune read pool | `SetReadPoolSize(n)` | Default 3 conns (~60MB page cache); raise on high-RAM hosts |
| Read pool metrics | `ReadPoolStats()` | Returns read-pool `sql.DBStats` (open/in-use/wait); used by cleanup's 60s metrics ticker |
| File operations | `manager.go` | `CreateSegment(temp)` / `WriteFrame()` / `CloseSegment(temp→final)` |
| Crash recovery | `CleanupTempFiles()` | Removes orphaned temp files from previous crash |
| Disk usage | `GetDiskUsage()` | `fsusage.Statfs()` on root dir |
| Per-camera merge config | `GetMergeConfig()` / `SaveMergeConfig()` | JSON column in cameras table |

## CONVENTIONS

- **Read/write pool isolation**: `d.db` (writer, `MaxOpenConns=1`) serializes all writes; `d.readDB` (reader, `MaxOpenConns=3`, `query_only=1`) serves SELECTs concurrently. WAL mode allows concurrent readers + one writer. SELECT methods call `d.readConn()`; writes/transactions use `d.db`. NEVER route a write through `readConn()` — `query_only` rejects it, but keep the discipline.
- **SQLite pragmas** (`db.go` `New()`): WAL mode, `synchronous=NORMAL`, `busy_timeout=15000`, `cache_size=-20000` (20MB), `mmap_size=256MB`, `temp_store=MEMORY`, `analysis_limit=1000` (≥3.46). All via DSN `_pragma` so every pool connection inherits them.
- **Connection lifecycle**: writer pool 1 conn / 1 idle / no max lifetime; read pool 3 conns / 3 idle. Both closed in `Close()`.
- **Index hygiene**: `Init()` unconditionally `DROP IF EXISTS` the 3 redundant single-column indexes (`idx_recordings_camera`, `idx_recordings_merged`, `idx_recordings_archived`) superseded by compound indexes. Keep this DROP unconditional — the gated version left stale indexes on fresh installs.
- **Timestamps**: All stored as UTC strings `2006-01-02 15:04:05.999999999`. `parseTime()` handles 5+ legacy formats
- **Atomic writes**: `CreateSegment()` creates temp file → `CloseSegment()` renames to final path. Prevents partial files on crash
- **Filter builder**: `recordingsFilterWhere()` + `recordingsOrderByClause()` shared by List/Count methods. Whitelisted sort columns only.
- **Count cache**: `countRecordingsCached()` memoizes `CountRecordingsWithFilter` per filter signature (2s TTL). Avoids re-running a full COUNT on every paginated page navigation. Keyed by filter fields excluding Limit/Offset/Sort. NEVER use `COUNT(*) OVER()` in a combined list+count query — SQLite's planner can't optimize it (forces full scan + temp B-tree sort, ~3.9s on 86K rows vs ~6ms separated).
- **Nullable fields**: `CameraRow.MergeEnabled` uses `*bool` (nil = use global). Scanned with `NullBool` helper
- **Query metrics**: hot methods call `defer d.observeQuery("name", time.Now())`. To instrument a new method, just add the defer; metrics are no-op when `SetMetrics` wasn't called (tests).
- **Heavy-query timeout**: `withHeavyQueryTimeout(ctx)` bounds analytics queries to 10s unless the caller set an earlier deadline.
- **Error handling**: Non-fatal errors log warning and continue (e.g., file deletion after DB delete)

## ANTI-PATTERNS

- **DO NOT** use `time.Time.String()` for DB storage — contains monotonic clock, incompatible with SQLite `datetime()`
- **DO NOT** treat `retention_days: 0` as "keep forever" — code treats 0 as unconfigured, defaults to 30
- **DO NOT** forget to add `t.Helper()` in test helper functions
