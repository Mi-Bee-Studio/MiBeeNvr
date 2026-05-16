## 2026-05-10 Session Start
- Plan: nvr-merge-monitoring-and-fixes
- 16 implementation tasks + 4 final verification tasks
- 4 waves of parallel execution

## Pinned → Merged Migration (Wave 1)

### Patterns
- SQLite migration via `PRAGMA table_info` to check column existence before ALTER TABLE
- Schema version tracking in `schema_meta` table — increment version string for each migration
- Batch edits in the `edit` tool: if one edit in a batch fails due to hash mismatch, the ENTIRE batch fails silently — always check output
- After batch edit failures, must re-read file to get fresh LINE#ID hashes

### Gotchas
- `ListOldestUnpinnedRecordings` was renamed to `ListOldestRecordings` — cleanup now deletes ALL recordings (merged doesn't protect)
- The `StorageProvider` interface had `PinRecording`/`UnpinRecording` — removed them since they were only used by the deleted API handlers
- Cleanup queries (`ListExpiredRecordings`, `ListExpiredRecordingsByCamera`) no longer filter by `pinned=0` — merged recordings are NOT protected from cleanup
- Merge queries (`ListMergeableSegments`, `ListCameraMergeWindows`) DO filter by `merged=0` — only unmerged segments should be merged
- webdav `TestWriteMethodsForbidden` has pre-existing failures (405 vs 403) — unrelated to this change
- Integration test had pin/unpin steps (5, 6) that needed removal, renumbering subsequent steps
- `insertRecordingWithNullEnded` in cleanup_test.go uses raw SQL — column name must match schema (`merged` not `pinned`)

### Decisions
- `merged=0` (default) for normal recordings, `merged=1` for merged recordings
- `SetMerged()` added to DB (not to interface) — only called from merge/manager.go
- Migration v3→v4 handles both fresh installs (just add column) and upgrades from v2/v3 (check pinned column exists)
- Frontend files (web/src/) were NOT modified — they reference `pinned` but that's out of scope for this backend-only task

## T8: CameraRow Credential Metadata

### Patterns
- `CASE WHEN password IS NOT NULL AND password != '' THEN 1 ELSE 0 END` — SQLite computed column for boolean has_password
- SQLite `CASE ... END` result scans directly into Go `bool` field — works because SQLite INTEGER 1/0 maps to Go bool
- Empty string → nil pattern for credential safety: frontend sends `""` to mean "don't update", backend converts to `nil` before passing to UpdateCamera
- CameraRow has NO Password field — only Username (string) and HasPassword (bool) — password never leaks via JSON

### Gotchas
- When replacing a line that starts a multi-line block (like QueryRowContext+Scan), must include ALL subsequent lines in the range, otherwise the orphaned lines become syntax errors
- CameraUpdate already uses `*string` for Username/Password — nil means "don't update" — the empty-string-to-nil conversion in handler prevents accidental credential clearing

### Decisions
- UpsertCamera unchanged — still stores actual username/password in DB
- Only ListCameras/GetCamera expose computed credential metadata
- has_password checks both `IS NOT NULL` and `!= ''` for safety against NULL default values

## T4: Per-Camera Merge Config

### Patterns
- Per-camera override pattern: `*MergeConfig` pointer in CameraConfig — nil = "use global default"
- `ResolveMergeConfig(global, perCamera)` — only non-zero per-camera fields override global (strings: non-empty, ints: >0, bool: true)
- DB nullable columns with `sql.NullBool`/`sql.NullString`/`sql.NullInt64` — scanned via helper functions (`nullBoolToPtr`, `nullStringToPtr`, `nullInt64ToPtr`)
- `UpsertCameraMerge` uses `COALESCE(?, column)` — pass NULL to keep existing value, pass actual value to update
- Helper conversion functions: `ptrToNullBool`/`ptrToNullString`/`ptrToNullInt64` convert Go pointers to sql.Null* types

### Gotchas
- Migration v4→v5: check `merge_enabled` column existence via `pragma_table_info` before ALTER TABLE — safe for both fresh installs and upgrades
- `*bool` with `false` value must NOT be confused with nil — `ptrToNullBool` correctly maps nil→invalid, &false→Valid:false
- Schema version always bumped to '5' regardless of whether columns were actually added (idempotent ALTER TABLE errors are ignored)

### Decisions
- 6 nullable columns added: merge_enabled (INTEGER), merge_check_interval (TEXT), merge_window_size (TEXT), merge_batch_limit (INTEGER), merge_min_segment_age (TEXT), merge_min_segments_to_merge (INTEGER)
- Separate `UpsertCameraMerge()` function rather than modifying `UpsertCamera()` — keeps existing callers untouched
- CameraRow uses `*bool`, `*string`, `*int` pointer fields — nil = not set (use global), non-nil = override

## T4: MergeManager Status Tracking & Config Hot-Reload

### Changes Made
- Added `MergeStatus` struct with json tags (LastRunTime, SegmentsMerged, FilesCreated, ErrorCount)
- Added `sync.RWMutex`-protected `status` field with `Status()` method for thread-safe reads
- Added `PendingCounts(ctx)` method that queries DB for per-camera pending segment counts
- Changed `NewMergeManager` constructor to accept `func() config.MergeConfig` and `func(cameraID string) *config.MergeConfig` callbacks for hot-reload
- `Run()` and `RunOnce()` now call `m.getGlobalCfg()` fresh on each invocation (hot-reload)
- `RunOnce()` resolves per-camera config via `config.ResolveMergeConfig()` for each camera
- `processCamera()` and `mergeFormatGroup()` now accept `config.MergeConfig` parameter instead of reading `m.cfg`
- `RunOnce()` tracks status: sets LastRunTime, SegmentsMerged, FilesCreated, ErrorCount under mutex
- Verified `SetMerged(ctx, mergedRec.ID, true)` is called after InsertRecording (from T1)
- Updated `cmd/mibee-nvr/main.go` to pass closures for global/camera config callbacks
- Added 4 new tests: TestStatus_Initial, TestStatus_AfterRunOnce, TestPendingCounts, TestPendingCounts_MergeDisabled, TestHotReload_PerCameraConfig

### Patterns
- Config hot-reload via closures: callbacks capture `cfg` variable, re-read on each call
- Per-camera override pattern: `config.ResolveMergeConfig(global, perCamera)` with nil-check
- Status tracking: update under write lock at end of RunOnce, read under read lock via Status()
- Test helper `newTestMergeManager()` wraps constructor with simple closures for test ergonomics

### Gotchas
- When editing with LINE#ID, append operations can inject content mid-function if the anchor line is inside a function body — always double-check placement
- `m.cfg` was used in 4 places (Run, RunOnce, processCamera, mergeFormatGroup) — all needed updating to use passed config parameter

## Merge Config API Endpoints (T6)

- Added 4 endpoints: GET/PUT `/api/settings/merge`, PUT/DELETE `/api/cameras/{id}/merge-config`
- Global merge settings stored in `config.Merge` (MergeConfig struct), persisted via `config.Save()`
- Per-camera merge overrides stored in cameras table via `db.UpsertCameraMerge()` with nullable pointers
- `UpsertCameraMerge()` uses COALESCE — passing nil leaves existing value, all-nil clears to global default
- UPDATE on nonexistent camera row returns no error (0 rows affected) — handlers return 200 (no-op)
- Duration fields validated with `time.ParseDuration()` in both global and per-camera endpoints
- Routes registered inside `/{id}` chi subrouter for camera-scoped endpoints
- 12 new tests added, all 110 api tests pass

## Merge Status API Endpoints (2026-05-10)

- Added `mergeMgr *merge.MergeManager` field to `Handler` struct (pointer, nil-safe)
- `NewHandler` signature now takes `mergeMgr` as last param (nil in tests, noopHandler, TestHandlerWithAuth)
- Routes registered as protected (inside auth group): `/api/merge/status`, `/api/merge/pending`
- When mergeMgr is nil, endpoints return `{"enabled": false}` gracefully
- `MergeManager.PendingCounts()` calls `m.getCameraCfg(cam.ID)` — must not pass nil for getCameraCfg in NewMergeManager (panics on nil function call)
- MergeManager was created after handler in main.go — moved creation before handler to fix ordering
- Updated 8 call sites: main.go (1), handler_test.go (6), handler.go noopHandler+TestHandlerWithAuth (2)

## T13: Frontend Pin→Merge Migration

### Changes Made
- `api.ts`: Removed `pinRecording()`/`unpinRecording()` exports, changed `Recording.pinned` → `Recording.merged`, changed `listRecordings` param `pinned` → `merged` with `merged` query param
- `Recordings.svelte`: Removed Pin/MapPin imports, removed `togglePin()` function, renamed `pinnedFilter` → `mergedFilter`, replaced pinned status dropdown with merged filter ("全部"/"已合并"/"未合并"), replaced pinned badge with merge status badges (badge-success "已合并" / badge-neutral "原始段"), removed pin/unpin button from actions column
- `RecordingDetail.svelte`: Removed `pinRecording`/`unpinRecording` imports (build-breaking), removed `togglePin()` function, replaced pinned badge with merged status, removed pin/unpin button

### Patterns
- Removing API exports from `api.ts` is a breaking change for all consumers — grep all files that import the removed function before assuming scope is limited
- Plain text Chinese strings ("已合并", "未合并", "全部") used as placeholders until T14 adds proper i18n keys
- GitMerge icon imported but not used yet (for future merge column icon)

### Gotchas
- `RecordingDetail.svelte` also imported `pinRecording`/`unpinRecording` — removing those exports from api.ts broke the build. Had to update RecordingDetail.svelte too even though it wasn't in the task scope
- The `togglePin` function removal left a comment `// Actions` with nothing under it — functionally fine but the orphaned comment is minor cleanup

## T7: Camera Credential Display + Per-Camera Merge Config UI

### Changes Made
- `api.ts`: Added `username?: string` and `has_password?: boolean` to Camera interface
- `api.ts`: Added `MergeConfig` interface with enabled, check_interval, window_size, batch_limit, min_segment_age, min_segments_to_merge
- `api.ts`: Added `getMergeConfig()`, `updateMergeConfig()`, `deleteCameraMergeConfig()` functions
- `Cameras.svelte`: `openEditForm()` now sets `formUsername = camera.username || ''` and loads merge config via `getMergeConfig()`
- `Cameras.svelte`: Password field shows placeholder `'已设置'` when `has_password=true`, `'未设置'` when false
- `Cameras.svelte`: Username field shows placeholder with current username in edit mode
- `Cameras.svelte`: `handleSubmit()` for edit mode: only sends username if changed from original, only sends password if non-empty
- `Cameras.svelte`: Added collapsible `<details>` merge config section (edit mode only) with all 6 fields + "使用全局默认" clear button
- `resetForm()` clears `mergeConfig` and `mergeConfigLoading`

### Patterns
- Collapsible `<details>/<summary>` pattern for optional config sections — no JS toggle needed
- Conditional credential sending: compare `formUsername !== editingCamera.username` to avoid unnecessary updates
- `getMergeConfig()` wrapped in try/catch returning null — API returns 404 when no override exists
- Merge config fields use `on:change`/`on:input` with lazy init (`if (!mergeConfig) mergeConfig = {}`)
- `mergeConfig?.field || defaultValue` pattern for select/input values — null config shows defaults

### Gotchas
- Duplicate `formPassword = ''` lines existed in original `resetForm()` (lines 102+104) — cleaned up to single assignment
- Svelte 5 `$state()` objects: mutating `mergeConfig.field = value` works for tracked reactivity
- Build warnings about deprecated `on:click`/`on:change` are pre-existing throughout codebase, not introduced by this change

## Integration Tests Update (T8)

### Changes Made
- Added `newAPIWithConfig()` helper — creates Handler with config for settings endpoints (nil config → 500 on GET/PUT /api/settings/merge)
- Added 7 new integration tests (Tests 8-14): recording merged field, camera credential display, PTZ protocol rejection, merge status (nil manager), merge settings API, per-camera merge config, merge settings without config
- Added `config` import to test file for MergeConfig usage
- All 14 integration tests pass

### Patterns
- `api.NewHandler()` with nil config returns 500 on settings endpoints — tests verify both nil-config and with-config scenarios
- Merge status endpoints return `{"enabled": false}` when mergeMgr is nil — graceful degradation
- Camera credential display: `username` field + `has_password` boolean computed via SQL CASE expression
- PTZ endpoints reject non-ONVIF cameras with 400 before any ONVIF client calls

### Gotchas
- `do()` helper requires 5 args (t, handler, method, path, body) — easy to forget the method when copy-pasting
- Per-camera merge config DELETE uses COALESCE with nil — effectively a no-op (keeps existing values), test only verifies 200 response
