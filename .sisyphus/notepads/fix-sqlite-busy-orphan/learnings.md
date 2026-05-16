# Learnings

## 2026-05-14 Pre-Work Research
- SQLite WAL mode, busy_timeout=5s, modernc pure-Go (no CGO)
- InsertRecording is called from 4 recorders + 1 plugin + WebDAV/FTP/HTTP uploads
- DeleteRecordingsBatch (db.go:447-473) is the ONLY existing transaction pattern in the codebase
- Recorders use context.Background() for DB inserts (cannot be cancelled by shutdown)
- Test pattern: t.TempDir() + real SQLite, 100% require, t.Helper() in helpers
- File naming: {cameraID}_{YYYYMMDD_HHMMSS}_{nanoseconds}.mp4
- Recording ID and filename UUID are DIFFERENT nanosecond timestamps
- PRAGMA foreign_keys is NOT enabled — foreign key constraints are NOT enforced
- MergeManager does 3 separate DB ops per group: InsertRecording + SetMerged + DeleteRecordingsBatch
- SetMerged(true) is redundant — should insert with merged=true directly
