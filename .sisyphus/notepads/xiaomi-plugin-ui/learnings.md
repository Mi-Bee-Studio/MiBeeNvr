# Learnings

## 2026-05-15: Fixed xiaomiDevices() return type + added listPlugins()

- `xiaomiDevices()` return type was `Promise<XiaomiDevice[]>` but backend returns `{devices: [...], message?: string}`. Fixed by adding `XiaomiDevicesResponse` interface and updating return type.
- Added `Plugin` interface and `listPlugins()` function calling `GET /api/plugins`.
- The frontend project uses Vite+Svelte (no standalone `tsc`) — TypeScript is compiled by Vite internally. No `typescript` package in devDependencies.
- Build verification is via `cd web && npm run build` (which runs `vite build`).
- Pre-existing Svelte 5 deprecation warnings and a11y warnings are not related to our changes.

## 2026-05-15: Task 4+5 — xiaomi auto-detect + plugin-aware camera form

- Svelte class attribute with expression must use `class="{expr}"` not `class={expr}"` — stray quote after closing brace causes parse error
- `xiaomiDevices()` returns `{devices: XiaomiDevice[], message?: string}` — always destructure `.devices` from the response
- `$effect()` runs on every change of any referenced `$state` variable — adding `formProtocol === 'xiaomi'` branch auto-sets encoding to h265
- `{@const}` tags in Svelte 5 are useful for computed values inside `{#if}` blocks (e.g., parsing DID from URL, finding matching device)
- Pre-existing Svelte 5 deprecation warnings (`on:click` → `onclick`) and a11y warnings in other files are not related to our changes

## Task 6: Go tests for /api/plugins + xiaomi camera creation

- `/api/plugins` is behind auth middleware (registered inside the `r.Use(h.authMW)` group in `Routes()`)
- `TestHandler()` uses `noopAuthMW()` (no auth), `TestHandlerWithAuth()` creates real auth middleware
- For auth tests, use `middleware.HashPassword()` to create bcrypt hash, then `TestHandlerWithAuth(db, store, "admin", hash)`
- For unauthenticated test, send request without basic auth credentials to `TestHandlerWithAuth` handler
- Xiaomi plugin registers via `init()` in `plugins/xiaomi/plugin.go` — imported directly (not `_`) in `handler.go`
- `handlePlugins` only returns `name` and `protocols` fields — no secrets leak by design
- Camera creation with xiaomi protocol needs a URL (e.g. `xiaomi://655448418`) — the URL-empty check applies to all non-ONVIF protocols
- `handleCreateCamera` returns 201 (StatusCreated), not 200 — tests must check for 201
- `newTestCamHandler()` is the right helper for camera CRUD tests — it sets up CameraManager, Config, and DB
- All 4 new tests pass, 661 total tests pass across 22 packages

## 2026-05-15: Final Wave — Post-review fixes

- **BUG FIXED**: `XiaomiDevice.ip` → `XiaomiDevice.localip` — backend CloudDevice uses `json:"localip"` tag, not `json:"ip"`. Frontend type and template references updated in api.ts and Cameras.svelte (3 locations).
- **BUG FIXED**: Duplicate `<option value="http">HTTP</option>` in protocol dropdown — removed extra line.
- F1 (Plan Compliance): APPROVE — Must Have 5/5, Must NOT Have 9/9, Tasks 6/6
- F2 (Code Quality): APPROVE — Build PASS, 1 pre-existing `as any`, no new issues
- F3 (Manual QA): APPROVE — Live NVR response shape verified, `localip` field confirmed
- F4 (Scope Fidelity): REJECT on scope creep (previous plan changes mixed in), but plan tasks 6/6 compliant, Must NOT Have 9/9 clean
- Scope creep is from previous `fix-sqlite-busy-orphan` plan — not from this plan's subagents
- Note: AGENTS.md already warns about this: `CloudDevice.IP uses json:"localip"` — the frontend was already wrong before this plan
