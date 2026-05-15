## 2026-05-10 Session Start
- Plan: recording-filter-optimization
- Wave 1: Task 1 (backend TDD) starting

## Task 1: Server-side search filtering — Complete

- **TDD RED→GREEN→REFACTOR** executed cleanly. All 7 new tests pass (6 storage + 1 API).
- **Go backtick vs double-quoted strings for LIKE escaping**: Backtick strings are raw — `\\\\` in backticks is literally 4 backslashes, not 2. For LIKE escape patterns, MUST use double-quoted strings: `strings.ReplaceAll(s, "\\\\", "\\\\\\\\")` to replace single `\\` with `\\\\`.
- **SQLite LIKE is case-insensitive by default** for ASCII — no COLLATE needed.
- **ESCAPE clause**: `'\\''` in Go double-quoted string produces `ESCAPE '\\'` in SQL (single backslash escape char). Confirmed correct via manual SQLite testing.
- **Both WHERE builders must stay in sync**: `ListRecordings()` and `CountRecordingsWithFilter()` have duplicate WHERE clause logic. Task plan explicitly forbids extracting a shared helper.
- **Test pattern**: `TestListRecordingsWithFilter` is the canonical pattern for db_test.go; `TestListRecordings_FilterByCameraID` for handler_test.go.
- **Files modified**: model/types.go (+1 field), storage/db.go (+2 search blocks), storage/db_test.go (+6 tests), api/handler.go (+1 line), api/handler_test.go (+1 test)
- **`go vet` clean, 25/25 TestListRecordings* tests pass**
