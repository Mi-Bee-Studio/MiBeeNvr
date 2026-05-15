# Fix Xiaomi Plugin UI + Device Discovery

## TL;DR

> **Quick Summary**: Fix xiaomi device discovery page to auto-detect existing auth and show devices immediately. Add plugin-aware camera edit form with dynamic protocol selector. Add `GET /api/plugins` endpoint. Fix `validProtocols` to accept xiaomi protocol.
> 
> **Deliverables**:
> - `validProtocols` map includes `"xiaomi"` — camera creation no longer fails
> - `GET /api/plugins` endpoint returns registered plugins (name, protocols) — no secrets
> - Frontend `xiaomiDevices()` type fixed to match actual backend response shape
> - Xiaomi discovery section auto-detects auth on mount, auto-fetches devices
> - Camera form protocol selector includes "Xiaomi" with conditional encoding/URL fields
> - Xiaomi device info shown in camera edit form for existing xiaomi cameras
> 
> **Estimated Effort**: Short
> **Parallel Execution**: YES - 2 waves
> **Critical Path**: Task 1 → Task 3 → Task 4 → Task 5 → Deploy

---

## Context

### Original Request
User discovered that xiaomi device discovery page is empty despite having authenticated previously. The page requires manual login each time. Additionally, the camera edit form has hardcoded protocol options and cannot handle plugin-registered protocols like xiaomi.

### Interview Summary
**Key Discussions**:
- Xiaomi device discovery page: `xiaomiLoggedIn = $state(false)` never auto-detects existing server-side token
- `validProtocols` map missing "xiaomi" — backend rejects camera creation
- Camera form has hardcoded protocol selector (rtsp, http, onvif)
- No frontend API for discovering registered plugins
- Frontend `xiaomiDevices()` type mismatch — expects `XiaomiDevice[]` but backend returns `{devices: [], message?: string}`

**Research Findings**:
- `handleXiaomiDevices` returns `{devices: CloudDevice[], message?: string}` — NOT a bare array
- `plugin.All()` returns all registered plugins — `[{Name(), Protocols(), ConfigSchema()}]`
- `ConfigSchema()` for xiaomi returns `config.XiaomiConfig{}` which includes Token and UserID — MUST filter secrets
- `addXiaomiDevice()` hardcodes `encoding: 'h264'` — recorder probes actual codec at runtime, so this is acceptable
- Three auth states exist: no token (200 + message), authenticated (200 + devices), expired (401)

### Metis Review
**Identified Gaps** (addressed):
- `xiaomiDevices()` type mismatch → Fix return type to `{devices, message?}`
- ConfigSchema() exposes secrets → Filter sensitive fields in plugins API response
- Three auth states (no token, authenticated, expired) → Frontend must handle all three
- Camera form URL validation assumes RTSP/HTTP → Skip URL validation for xiaomi protocol
- Editing existing xiaomi cameras → Show xiaomi-specific fields, lock protocol

---

## Work Objectives

### Core Objective
Make xiaomi plugin fully usable through the web UI — from device discovery to camera creation/editing — without manual workarounds.

### Concrete Deliverables
- `internal/api/handler.go`: `"xiaomi": true` in validProtocols + `handlePlugins()` + updated error message
- `web/src/lib/api.ts`: Fixed `xiaomiDevices()` type + new `listPlugins()` function
- `web/src/routes/Cameras.svelte`: Auto-detect auth, dynamic protocol selector, xiaomi form fields

### Definition of Done
- [x] `curl -u admin:admin -X POST .../api/cameras -d '{"protocol":"xiaomi",...}'` returns 200 (not 400)
- [x] `curl -u admin:admin .../api/plugins` returns plugins list without secrets
- [x] Opening Cameras page auto-shows xiaomi devices when token exists
- [x] Camera form protocol dropdown includes "Xiaomi"
- [x] Selecting "Xiaomi" shows appropriate encoding options (H.264, H.265) and URL placeholder

### Must Have
- validProtocols includes "xiaomi"
- GET /api/plugins endpoint returns plugin info (name, protocols only — NO secrets)
- Frontend auto-detects xiaomi auth status on mount
- Camera form supports xiaomi protocol selection
- All three auth states handled (no token, authenticated, expired)

### Must NOT Have (Guardrails)
- Do NOT make validProtocols dynamic/plugin-driven — just add "xiaomi" hardcoded with a comment
- Do NOT build generic JSON schema form renderer — hardcode xiaomi-specific fields
- Do NOT change form layout, styling, or submission API shape
- Do NOT touch ONVIF discovery, stream_encoding UI, or merge config
- Do NOT extract xiaomi discovery into a separate Svelte component
- Do NOT add server-side caching for xiaomi device list
- Do NOT expose Token, UserID, or any secrets from ConfigSchema() in API response
- Do NOT modify DB schema
- Do NOT refactor xiaomiDevices() into a new auth-status endpoint — reuse existing endpoint

---

## Verification Strategy (MANDATORY)

> **ZERO HUMAN INTERVENTION** - ALL verification is agent-executed.

### Test Decision
- **Infrastructure exists**: YES (Go testify + Playwright)
- **Automated tests**: YES (tests after — implement first, then add tests)
- **Framework**: Go testify/require (backend), Playwright (E2E frontend)

### QA Policy
Every task includes agent-executed QA scenarios.
Evidence saved to `.sisyphus/evidence/task-{N}-{scenario-slug}.{ext}`.

- **Backend**: Bash (curl) — Send requests, assert status + response fields
- **Frontend**: Playwright — Navigate, interact, assert DOM, screenshot

---

## Execution Strategy

### Parallel Execution Waves

```
Wave 1 (Start Immediately — backend fixes, unblock frontend):
├── Task 1: Fix validProtocols + error message [quick]
├── Task 2: Add GET /api/plugins endpoint [quick]
└── Task 3: Fix xiaomiDevices() type + add listPlugins() [quick]

Wave 2 (After Wave 1 — frontend features):
├── Task 4: Auto-detect xiaomi auth + idempotent refresh [unspecified-high]
├── Task 5: Plugin-aware camera form with xiaomi fields [visual-engineering]
└── Task 6: Add tests [unspecified-high]

Wave FINAL (After ALL tasks):
├── Task F1: Plan compliance audit (oracle)
├── Task F2: Code quality review (unspecified-high)
├── Task F3: Real manual QA (unspecified-high)
└── Task F4: Scope fidelity check (deep)

Critical Path: Task 1 → Task 4 → Task 5 → Task 6 → F1-F4
Max Concurrent: 3 (Wave 1)
```

### Dependency Matrix

| Task | Depends On | Blocks | Wave |
|------|-----------|--------|------|
| 1 | - | 4, 5, 6 | 1 |
| 2 | - | 3, 5, 6 | 1 |
| 3 | 2 | 4, 5 | 1 |
| 4 | 1, 3 | 6 | 2 |
| 5 | 1, 2, 3 | 6 | 2 |
| 6 | 1-5 | F1-F4 | 2 |

### Agent Dispatch Summary

- **Wave 1**: 3 tasks — T1 → `quick`, T2 → `quick`, T3 → `quick`
- **Wave 2**: 3 tasks — T4 → `unspecified-high`, T5 → `visual-engineering`, T6 → `unspecified-high`
- **FINAL**: 4 tasks — F1 → `oracle`, F2 → `unspecified-high`, F3 → `unspecified-high`, F4 → `deep`

---

## TODOs

---

- [x] 1. Fix validProtocols + error message to accept xiaomi protocol

  **What to do**:
  - In `internal/api/handler.go` lines 734-744, add `"xiaomi": true,` to the `validProtocols` map
  - Add comment `// Plugin protocols` above the xiaomi line
  - Find the error message at the `validProtocols` check (around line 777) — update the error string to include "xiaomi" in the list of valid protocols shown to the user
  - Search for the exact error message string: `fmt.Errorf("invalid protocol")` or similar

  **Must NOT do**:
  - Do NOT make validProtocols dynamic by querying plugin.All()
  - Do NOT refactor the validation logic

  **Recommended Agent Profile**:
  - **Category**: `quick`
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 1 (with Tasks 2, 3)
  - **Blocks**: Tasks 4, 5, 6
  - **Blocked By**: None

  **References**:

  **Pattern References**:
  - `internal/api/handler.go:734-744` — validProtocols map, current values: rtsp, http, onvif + legacy combined
  - `internal/api/handler.go:777` (approximate) — error message when protocol is invalid, search for `invalid protocol` string

  **Acceptance Criteria**:

  ```
  Scenario: Create xiaomi camera via API
    Tool: Bash (curl)
    Preconditions: NVR running at http://192.168.63.31:9090, auth admin/admin
    Steps:
      1. curl -s -u admin:admin -X POST http://192.168.63.31:9090/api/cameras -H 'Content-Type: application/json' -d '{"name":"test-xiaomi-protocol","protocol":"xiaomi","encoding":"h264","url":"xiaomi://999999999","enabled":false}'
      2. Check response status code is 200 (not 400)
      3. Check response JSON contains camera object with protocol="xiaomi"
      4. Delete test camera: curl -s -u admin:admin -X DELETE http://192.168.63.31:9090/api/cameras/{id}
    Expected Result: 200 OK with camera JSON, protocol field is "xiaomi"
    Failure Indicators: 400 status, "invalid protocol" in response body
    Evidence: .sisyphus/evidence/task-1-xiaomi-protocol-accepted.txt

  Scenario: Invalid protocol still rejected with updated error
    Tool: Bash (curl)
    Preconditions: NVR running
    Steps:
      1. curl -s -u admin:admin -X POST http://192.168.63.31:9090/api/cameras -H 'Content-Type: application/json' -d '{"name":"test-bad","protocol":"invalid_proto","encoding":"h264","url":"rtsp://x","enabled":false}'
      2. Check response is 400
      3. Check error message contains "xiaomi" in the list of valid protocols
    Expected Result: 400 with error message listing valid protocols including "xiaomi"
    Failure Indicators: Error message does not mention "xiaomi"
    Evidence: .sisyphus/evidence/task-1-invalid-protocol-rejected.txt
  ```

  **Commit**: NO (groups with Task 5)

---

- [x] 2. Add GET /api/plugins endpoint

  **What to do**:
  - In `internal/api/handler.go`, add a new handler method `handlePlugins(w, r)`
  - Import `"github.com/Mi-Bee-Studio/MiBeeNvr/internal/plugin"` if not already imported
  - Call `plugin.All()` to get all registered plugins
  - For each plugin, return ONLY `{name: string, protocols: []string}` — do NOT include ConfigSchema() output as it may contain secrets
  - Register the route: `r.Get("/api/plugins", h.handlePlugins)` in the `Routes()` method, inside the authenticated section (after authMW)
  - Place it near the other top-level API routes (around lines 100-160)

  **Must NOT do**:
  - Do NOT expose ConfigSchema() output — it contains XiaomiConfig with Token and UserID fields
  - Do NOT make this a public endpoint — it must be behind authMW
  - Do NOT add plugin config details beyond name and protocols

  **Recommended Agent Profile**:
  - **Category**: `quick`
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 1 (with Tasks 1, 3)
  - **Blocks**: Tasks 3, 5, 6
  - **Blocked By**: None

  **References**:

  **Pattern References**:
  - `internal/api/handler.go:101-163` — Routes() method showing route registration pattern
  - `internal/api/handler.go:154-159` — Xiaomi route registration example (pattern to follow)
  - `internal/api/handler.go:2028-2061` — handleXiaomiDevices example (handler pattern to follow)

  **API/Type References**:
  - `internal/plugin/plugin.go:31-48` — plugin.All() returns []RecorderPlugin
  - `internal/plugin/plugin.go:13-29` — RecorderPlugin interface: Name(), Protocols()

  **Acceptance Criteria**:

  ```
  Scenario: GET /api/plugins returns plugin list
    Tool: Bash (curl)
    Preconditions: NVR running, xiaomi plugin registered via init()
    Steps:
      1. curl -s -u admin:admin http://192.168.63.31:9090/api/plugins
      2. Check response is 200
      3. Parse JSON, verify it has "plugins" array
      4. Verify at least one plugin with name="xiaomi" and protocols=["xiaomi"]
      5. Search response body for "token", "user_id", "Token", "UserID" — must NOT be present
    Expected Result: {"plugins":[{"name":"xiaomi","protocols":["xiaomi"]}]}
    Failure Indicators: 404 (route not registered), secrets in response, empty plugins array
    Evidence: .sisyphus/evidence/task-2-plugins-api.txt

  Scenario: Unauthenticated request rejected
    Tool: Bash (curl)
    Steps:
      1. curl -s http://192.168.63.31:9090/api/plugins (no auth)
      2. Check response is 401
    Expected Result: 401 Unauthorized
    Evidence: .sisyphus/evidence/task-2-plugins-auth.txt
  ```

  **Commit**: NO (groups with Task 5)

---

- [x] 3. Fix xiaomiDevices() type + add listPlugins() in frontend API

  **What to do**:
  - In `web/src/lib/api.ts`, fix the `xiaomiDevices()` function (line 649) return type
  - Current: `Promise<XiaomiDevice[]>` — WRONG, backend returns `{devices: CloudDevice[], message?: string}`
  - New: Return `Promise<{devices: XiaomiDevice[], message?: string}>`
  - Add a new `XiaomiDevicesResponse` interface for this shape
  - Add `Plugin` interface: `{ name: string, protocols: string[] }`
  - Add `listPlugins()` function that calls `GET /api/plugins` and returns `Promise<Plugin[]>`
  - Extract the `plugins` array from the response

  **Must NOT do**:
  - Do NOT change the backend response format — fix only the frontend type
  - Do NOT add error handling beyond what apiRequest already provides

  **Recommended Agent Profile**:
  - **Category**: `quick`
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES (only depends on Task 2 for actual API call to work, but code can be written in parallel)
  - **Parallel Group**: Wave 1 (with Tasks 1, 2)
  - **Blocks**: Tasks 4, 5
  - **Blocked By**: Task 2 (for end-to-end verification)

  **References**:

  **Pattern References**:
  - `web/src/lib/api.ts:624-665` — Existing xiaomi API functions, follow this pattern for listPlugins()
  - `web/src/lib/api.ts:642-648` — xiaomiAuth() pattern: `apiRequest('/xiaomi/auth', {...})`

  **API/Type References**:
  - `internal/api/handler.go:2028-2061` — Backend response shape: `{"devices": [...], "message": "..."}`

  **Acceptance Criteria**:

  ```
  Scenario: xiaomiDevices() returns correct shape
    Tool: Bash (curl)
    Preconditions: NVR running with xiaomi token configured
    Steps:
      1. curl -s -u admin:admin http://192.168.63.31:9090/api/xiaomi/devices
      2. Verify response is {"devices":[...]} (not a bare array)
      3. cd web && npx tsc --noEmit — verify TypeScript compilation succeeds
    Expected Result: Frontend types match backend response
    Evidence: .sisyphus/evidence/task-3-types-fixed.txt

  Scenario: listPlugins() returns plugins
    Tool: Bash (curl)
    Steps:
      1. curl -s -u admin:admin http://192.168.63.31:9090/api/plugins
      2. Verify response matches new Plugin interface
    Expected Result: {"plugins":[{"name":"xiaomi","protocols":["xiaomi"]}]}
    Evidence: .sisyphus/evidence/task-3-listplugins.txt
  ```

  **Commit**: NO (groups with Task 5)

---

- [x] 4. Auto-detect xiaomi auth + idempotent device refresh

  **What to do**:
  - In `web/src/routes/Cameras.svelte`, fix the xiaomi discovery section to auto-detect auth on mount
  - In `onMount()` (around line 476-479), add a call to `xiaomiDevices()` to probe auth status
  - Handle three states from the response:
    1. Response has `devices` array with items AND no `message` field → authenticated, set `xiaomiLoggedIn = true`, populate `xiaomiDeviceList`
    2. Response has `{devices: [], message: "not authenticated"}` → no token, keep showing login form
    3. Response returns 401 → token expired, show re-login message
  - Update `xiaomiDeviceList` assignment: extract from `response.devices` instead of treating response as array
  - Fix ALL existing `xiaomiDeviceList = await xiaomiDevices()` calls to use `response.devices`
  - Add a refresh function that re-fetches devices (idempotent — can be called anytime)
  - On refresh, filter out devices that already have cameras (match by DID in existing camera URLs)
  - Keep the existing refresh button (line 706) working — wire it to the new refresh function

  **Must NOT do**:
  - Do NOT create a new backend endpoint for auth-status — reuse the existing `/api/xiaomi/devices` response
  - Do NOT store credentials in the frontend
  - Do NOT auto-login — only detect existing server-side token state
  - Do NOT extract xiaomi section into a separate component

  **Recommended Agent Profile**:
  - **Category**: `unspecified-high`
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES (but logically after Task 1 + 3)
  - **Parallel Group**: Wave 2 (with Tasks 5, 6)
  - **Blocks**: Task 6
  - **Blocked By**: Tasks 1, 3

  **References**:

  **Pattern References**:
  - `web/src/routes/Cameras.svelte:88-103` — Xiaomi state variables to update
  - `web/src/routes/Cameras.svelte:375-474` — Xiaomi auth functions to modify
  - `web/src/routes/Cameras.svelte:603-741` — Xiaomi UI section to update
  - `web/src/routes/Cameras.svelte:476-479` — onMount where auto-detect logic goes

  **API/Type References**:
  - `web/src/lib/api.ts:649` — xiaomiDevices() now returns `{devices, message?}`, update call sites
  - `web/src/routes/Cameras.svelte:422` — `xiaomiDeviceList = await xiaomiDevices()` must become `const res = await xiaomiDevices(); xiaomiDeviceList = res.devices;`
  - `web/src/routes/Cameras.svelte:456-474` — addXiaomiDevice() — may need to filter added devices from list

  **Acceptance Criteria**:

  ```
  Scenario: Auto-detect existing auth on page load
    Tool: Playwright
    Preconditions: NVR has xiaomi token saved in config (already authenticated)
    Steps:
      1. Navigate to http://192.168.63.31:9090/#/cameras
      2. Wait for page load
      3. Find the xiaomi discovery section (expanded or click to expand)
      4. Verify device list is shown WITHOUT requiring manual login
      5. Verify at least one device appears (e.g., "chuangmi.camera.029a02")
    Expected Result: Device list auto-populated, no login form visible
    Failure Indicators: Login form shown despite valid token, empty device list
    Evidence: .sisyphus/evidence/task-4-auto-detect.png

  Scenario: No token shows login form
    Tool: Playwright
    Preconditions: NVR has no xiaomi token in config
    Steps:
      1. Navigate to cameras page
      2. Expand xiaomi section
      3. Verify login form (username/password fields) is visible
      4. Verify no device list shown
    Expected Result: Login form visible, no device list
    Evidence: .sisyphus/evidence/task-4-no-token.png

  Scenario: Refresh button re-fetches devices
    Tool: Playwright
    Steps:
      1. Navigate to cameras page with valid xiaomi auth
      2. Wait for auto-detect to show devices
      3. Click refresh button in xiaomi section
      4. Verify device list updates (spinner shown briefly, then list reappears)
    Expected Result: Device list refreshed without error
    Evidence: .sisyphus/evidence/task-4-refresh.png
  ```

  **Commit**: NO (groups with Task 5)

---

- [x] 5. Plugin-aware camera form with xiaomi fields

  **What to do**:
  - In `web/src/routes/Cameras.svelte`, update the camera edit/add form:

  **Protocol selector** (lines 760-771):
  - Add `<option value="xiaomi">Xiaomi</option>` after the ONVIF option
  - Consider loading additional plugins from `listPlugins()` and merging, but for this PR just add xiaomi manually

  **Encoding selector** (lines 773-789):
  - Add a new condition: `{#if formProtocol === 'xiaomi'}`
  - Inside: `<option value="h264">H.264</option>` and `<option value="h265">H.265</option>`

  **URL field**:
  - When protocol is xiaomi, show placeholder `xiaomi://device_id`
  - URL validation must be protocol-aware: when xiaomi, validate format is `xiaomi://\d+`

  **Username/password fields**:
  - When protocol is xiaomi, hide or disable username/password fields (not used for xiaomi)

  **Xiaomi device info card** (in camera edit form):
  - When editing an existing camera with `protocol === 'xiaomi'`:
    - Parse DID from URL (`xiaomi://655448418` → `655448418`)
    - If xiaomiDeviceList has a matching device, show info card with: Model, LAN IP, Online status
    - Style: subtle info box below the URL field
  - When creating a new camera with xiaomi protocol:
    - Show note: "Add Xiaomi cameras from the Device Discovery section below"
    - OR allow manual DID entry via URL field

  **formProtocol change handler**:
  - When switching to xiaomi, auto-set encoding to `h265` (most xiaomi cameras are h265)
  - When switching away from xiaomi, reset encoding to appropriate default

  **Must NOT do**:
  - Do NOT change form layout or styling
  - Do NOT change form submission API call shape
  - Do NOT build generic JSON schema form renderer
  - Do NOT touch ONVIF discovery logic

  **Recommended Agent Profile**:
  - **Category**: `visual-engineering`
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 2 (with Tasks 4, 6)
  - **Blocks**: Task 6
  - **Blocked By**: Tasks 1, 2, 3

  **References**:

  **Pattern References**:
  - `web/src/routes/Cameras.svelte:760-789` — Current protocol + encoding selectors (exact code to modify)
  - `web/src/routes/Cameras.svelte:456-474` — addXiaomiDevice() shows how xiaomi cameras are created

  **API/Type References**:
  - `web/src/lib/api.ts:625-631` — XiaomiDevice interface: did, name, model, ip, isOnline
  - `web/src/lib/api.ts` — listPlugins() function (from Task 3)

  **Acceptance Criteria**:

  ```
  Scenario: Xiaomi protocol in camera form
    Tool: Playwright
    Steps:
      1. Navigate to cameras page
      2. Click "Add Camera" button
      3. Find protocol dropdown
      4. Verify "Xiaomi" option exists
      5. Select "Xiaomi"
      6. Verify encoding shows H.264 and H.265 options
      7. Verify URL field shows xiaomi:// placeholder
      8. Verify username/password fields are hidden or disabled
    Expected Result: Xiaomi option works, appropriate fields shown/hidden
    Evidence: .sisyphus/evidence/task-5-form-xiaomi.png

  Scenario: Create xiaomi camera from form
    Tool: Playwright
    Steps:
      1. Open add camera form
      2. Select Xiaomi protocol
      3. Set name to "Test Xiaomi Camera"
      4. Set URL to "xiaomi://999999999"
      5. Set encoding to H.265
      6. Click Save
      7. Verify success toast appears
      8. Verify camera appears in list with protocol="xiaomi"
      9. Delete the test camera
    Expected Result: Camera created successfully with xiaomi protocol
    Evidence: .sisyphus/evidence/task-5-create-xiaomi-camera.png

  Scenario: Edit existing xiaomi camera shows device info
    Tool: Playwright
    Preconditions: Camera cam-1ad370df exists with protocol=xiaomi, url=xiaomi://655448418
    Steps:
      1. Navigate to cameras page
      2. Find camera "小米智能摄像机 云台版2K" (or similar)
      3. Click edit button
      4. Verify form shows xiaomi protocol selected
      5. Verify device info card shows: Model, LAN IP, online status
    Expected Result: Xiaomi-specific device info displayed in edit form
    Evidence: .sisyphus/evidence/task-5-edit-xiaomi-camera.png
  ```

  **Commit**: YES
  - Message: `feat(xiaomi): plugin UI with device discovery auto-detect and camera form integration`
  - Files: `internal/api/handler.go`, `web/src/lib/api.ts`, `web/src/routes/Cameras.svelte`
  - Pre-commit: `cd web && npm run build`

---

- [x] 6. Add tests

  **What to do**:
  - Add backend test for `handlePlugins` in `tests/integration_test.go` or a new test file
  - Test: `GET /api/plugins` returns 200 with plugins array containing xiaomi
  - Test: `GET /api/plugins` requires authentication (unauthenticated returns 401)
  - Test: Response does NOT contain sensitive fields (token, user_id)
  - Add/update test for validProtocols accepting xiaomi:
    - `POST /api/cameras` with `protocol: "xiaomi"` returns 200 (not 400)
  - Use `TestHandler()` or `TestHandlerWithAuth()` factories from api package
  - Use `testify/require` exclusively, `t.Helper()` in all helpers

  **Must NOT do**:
  - Do NOT use `assert` — only `require`
  - Do NOT forget `t.Helper()` in test helpers
  - Do NOT use mocks for SQLite — use real temp file

  **Recommended Agent Profile**:
  - **Category**: `unspecified-high`
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 2 (with Tasks 4, 5)
  - **Blocks**: F1-F4
  - **Blocked By**: Tasks 1-5

  **References**:

  **Pattern References**:
  - `tests/integration_test.go` — Existing test patterns
  - `internal/api/handler.go` — `TestHandler()` and `TestHandlerWithAuth()` factories

  **Acceptance Criteria**:

  ```
  Scenario: All tests pass
    Tool: Bash
    Steps:
      1. rtk go test ./internal/api/... -v -run TestPlugins
      2. Verify PASS
      3. rtk go test ./internal/api/... -v -run TestCamera.*Xiaomi
      4. Verify PASS
      5. rtk go test ./... -v 2>&1 | tail -5
      6. Verify no failures
    Expected Result: All tests pass, 0 failures
    Evidence: .sisyphus/evidence/task-6-tests-pass.txt
  ```

  **Commit**: YES (amend into Task 5 commit, or separate commit)
  - Message: `test(xiaomi): add tests for plugin API and xiaomi protocol acceptance`
  - Files: Test file
  - Pre-commit: `rtk go test ./... -v`

---


## Final Verification Wave (MANDATORY — after ALL implementation tasks)

> 4 review agents run in PARALLEL. ALL must APPROVE.

- [x] F1. **Plan Compliance Audit** — `oracle`
  Read the plan end-to-end. For each "Must Have": verify implementation exists. For each "Must NOT Have": search codebase for forbidden patterns. Check evidence files exist. Compare deliverables against plan.
  Output: `Must Have [N/N] | Must NOT Have [N/N] | Tasks [N/N] | VERDICT: APPROVE/REJECT`

- [x] F2. **Code Quality Review** — `unspecified-high`
  Run `go vet ./...` + `cd web && npm run build`. Review all changed files for: `as any`/`@ts-ignore`, empty catches, console.log in prod, commented-out code, unused imports. Check AI slop.
  Output: `Build [PASS/FAIL] | Lint [PASS/FAIL] | Files [N clean/N issues] | VERDICT`

- [x] F3. **Real Manual QA** — `unspecified-high`
  Deploy to RPi. Execute curl tests for all backend endpoints. Open web UI, test xiaomi discovery auto-detect, test camera form with xiaomi protocol. Capture screenshots.
  Output: `Scenarios [N/N pass] | VERDICT`

- [x] F4. **Scope Fidelity Check** — `deep`
  For each task: read "What to do", read actual diff. Verify 1:1 — everything in spec was built, nothing beyond spec. Check "Must NOT do" compliance.
  Output: `Tasks [N/N compliant] | VERDICT`

---

## Commit Strategy

- **Single commit**: `feat(xiaomi): plugin UI with device discovery auto-detect and camera form integration`

---

## Success Criteria

### Verification Commands
```bash
# validProtocols fix
curl -s -u admin:admin -X POST http://192.168.63.31:9090/api/cameras \
  -H 'Content-Type: application/json' \
  -d '{"name":"test-xiaomi","protocol":"xiaomi","encoding":"h264","url":"xiaomi://12345","enabled":false}' | jq .
# Expected: 200 OK with camera JSON (not 400 "invalid protocol")

# GET /api/plugins — no secrets
curl -s -u admin:admin http://192.168.63.31:9090/api/plugins | jq .
# Expected: {"plugins":[{"name":"xiaomi","protocols":["xiaomi"]}]}
# Must NOT contain "token", "user_id", "Token", "UserID"

# Xiaomi devices auto-detect
curl -s -u admin:admin http://192.168.63.31:9090/api/xiaomi/devices | jq .
# Expected: {"devices":[...]} with camera devices, OR {"devices":[],"message":"not authenticated"}
```

### Final Checklist
- [x] validProtocols includes "xiaomi"
- [x] GET /api/plugins returns plugin info without secrets
- [x] xiaomiDevices() type matches backend response shape
- [x] Cameras page auto-shows xiaomi devices when token exists
- [x] Camera form has "Xiaomi" in protocol dropdown
- [x] Xiaomi protocol shows H.264/H.265 encoding options
- [x] All tests pass: `rtk go test ./internal/api/... -v`
- [x] Frontend builds: `cd web && rtk npm run build`
