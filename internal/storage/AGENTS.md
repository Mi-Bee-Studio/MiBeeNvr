# MiBee NVR — Storage Package

## OVERVIEW

SQLite metadata (DB) + file operations (Manager). All recording/camera CRUD, UTC timestamp handling, atomic file lifecycle.

## STRUCTURE

```
db.go          # DB struct — SQLite WAL mode, recordings/cameras CRUD, time format handling
db_test.go     # DB tests — time parsing, CRUD operations, query builder
manager.go     # Manager struct — file create/write/close with temp→atomic rename
manager_test.go # Manager tests — segment lifecycle, disk usage, crash recovery
```

## WHERE TO LOOK

| Task | Location | Notes |
|------|----------|-------|
| Change DB schema | `db.go` `Init()` | CREATE IF NOT EXISTS migrations |
| Fix time handling | `timeToDB()` / `parseTime()` / `scanTime()` | UTC storage, 5+ legacy format backward compat |
| Add camera field | `CameraRow` struct + `createCamerasTable()` | Add column + update SELECT/INSERT/UPDATE |
| Query recordings | `ListRecordings()` | Dynamic query builder with filters, sorting, pagination |
| File operations | `manager.go` | `CreateSegment(temp)` / `WriteFrame()` / `CloseSegment(temp→final)` |
| Crash recovery | `CleanupTempFiles()` | Removes orphaned temp files from previous crash |
| Disk usage | `GetDiskUsage()` | `fsusage.Statfs()` on root dir |
| Per-camera merge config | `GetMergeConfig()` / `SaveMergeConfig()` | JSON column in cameras table |

## CONVENTIONS

- **SQLite pragmas**: WAL mode, NORMAL sync, 5s busy timeout, 2MB cache — tuned for SD card on RPi
- **Timestamps**: All stored as UTC strings `2006-01-02 15:04:05.999999999`. `parseTime()` handles 5+ legacy formats
- **Atomic writes**: `CreateSegment()` creates temp file → `CloseSegment()` renames to final path. Prevents partial files on crash
- **Dynamic query builder**: `ListRecordings()` builds SQL from filter params. Uses `WHERE 1=1` base + conditional `AND` clauses
- **Nullable fields**: `CameraRow.MergeEnabled` uses `*bool` (nil = use global). Scanned with `NullBool` helper
- **Error handling**: Non-fatal errors log warning and continue (e.g., file deletion after DB delete)

## ANTI-PATTERNS

- **DO NOT** use `time.Time.String()` for DB storage — contains monotonic clock, incompatible with SQLite `datetime()`
- **DO NOT** treat `retention_days: 0` as "keep forever" — code treats 0 as unconfigured, defaults to 30
- **DO NOT** forget to add `t.Helper()` in test helper functions
