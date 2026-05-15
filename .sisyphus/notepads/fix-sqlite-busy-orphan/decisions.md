# Decisions

## 2026-05-14 Architecture Decisions
- Create separate InsertRecordingWithRetry method (not modify InsertRecording) to avoid affecting sync HTTP handlers
- MergeAndReplaceRecordings wraps INSERT+DELETE in single transaction, inserts with merged=true directly
- Orphan reconciliation is startup-only, synchronous, single batch transaction with INSERT OR IGNORE
- Format defaults to h264 for reconciled files (cannot distinguish h264/h265 from filename alone)
- Skip camera directories not in cameras table (foreign_keys not enforced)
