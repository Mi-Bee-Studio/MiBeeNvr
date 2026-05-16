## Issues Log

## 2026-05-10 T4 PTZ Protocol Guard
- 3 subagent attempts all failed (timeout/confusion). Task is trivially small (5-line change).
- Solution: Implement manually or retry with oracle agent
- T4 has NO downstream dependencies — does not block Wave 2 tasks
- Deferring to later in session or manual implementation

## 2026-05-10 T3 First Attempt Timeout
- First T3 attempt with `quick` category timed out after 30min
- Retry with `deep` category succeeded in 2m33s
- Lesson: Use `deep` for any task involving DB schema changes, even if seemingly simple

## 2026-05-10 T10 Frontend Display Bug Audit

### Bug 1: Camera `recorder_status` field never displays (FIXED)
- **Root cause**: Backend `CameraRow` serializes status as `json:"status"`, but frontend `Camera` interface had `recorder_status?: string`
- Since `response.json()` uses the JSON key directly, `camera.recorder_status` was always `undefined`
- **Impact**:
  - Cameras.svelte: recorder status text below the time-ago badge never showed
  - Dashboard.svelte: `getStatusBadge()` always returned neutral (gray) dot — never green for "recording" or red for "error"
- **Fix**: Renamed `recorder_status` → `status` in Camera interface (api.ts) and all 3 template references

### Bug 2: Dashboard.svelte `onDestroy()` missing closing brace (FIXED)
- **Root cause**: Concurrent task (T11 merge monitoring) inserted `mergeInterval` cleanup inside `onDestroy()` but lost the closing `});`
- This caused the `$effect()` block (camera mode management) to be nested inside `onDestroy()`, producing a parse error
- **Fix**: Added missing `});` to close `onDestroy()` before the `$effect()` block

### Pages Audited (no additional bugs found):
- **Login.svelte**: Simple form, data flow correct. Pre-existing Svelte 5 warnings (non-$state vars, deprecated `on:` directives) — not display bugs
- **Recordings.svelte**: All table columns properly bound. Camera name lookup, format badge, merged status, sort/pagination all correct
- **RecordingDetail.svelte**: Video player, JPEG frame viewer, metadata grid (duration/size/frames/end-time), merged badge all display correctly
- **Cameras.svelte**: Edit form `openEditForm()` correctly populates all 13 fields (name, protocol, url, username, password, enabled, description, location, brand, model, serial_number, retention_days). Table displays all columns. Merge config section loads properly
- **Dashboard.svelte**: Camera grid, snapshot refresh, HLS expand, PTZ overlay all functional after fix
- **Stats.svelte**: Summary cards (storage/recordings/cameras), storage bar, health indicators, system resources (CPU/memory/network), camera table, Chart.js trend/camera charts all properly bound
- **Settings.svelte**: Cleanup config (retention/threshold/interval), WebDAV config (enabled/path/rw), frontend preferences (items-per-page/auto-refresh), merge settings all load and display correctly
- **LiveView.svelte**: Camera name, protocol badge, HLS player, PTZ control all display properly
- **Header.svelte**: Nav items, mobile menu, theme/language toggle, logout all functional
