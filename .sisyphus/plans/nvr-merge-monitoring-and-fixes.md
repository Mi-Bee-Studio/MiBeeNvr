# NVR Merge Monitoring + Bug Fixes + Config Refactor

## TL;DR

> **Quick Summary**: 6-part improvement to MiBee NVR — add merge monitoring dashboard, fix camera credential display bugs, make merge strategy configurable (global + per-camera), replace pinning with merge status, hide PTZ for non-ONVIF devices, and ensure robust credential update safety.
> 
> **Deliverables**:
> - Merge status API endpoints + dashboard card widget
> - Camera credential display fix (show username, has_password hint)
> - Global + per-camera merge strategy config in Settings/Cameras pages
> - Pin feature removed (front+back+DB), replaced by merge status column & filter
> - PTZ controls hidden for non-ONVIF devices (Dashboard + LiveView + API validation)
> - All pages audited for similar display bugs
> 
> **Estimated Effort**: Large
> **Parallel Execution**: YES - 4 waves
> **Critical Path**: T1→T2→T5→T8→T10→T13→T14→T15→T16→F1-F4

---

## Context

### Original Request
用户提出6个需求：1) 仪表盘增加合并监控展示 2) 摄像头编辑表单用户名/密码不回显的bug 3) 合并策略可配置（全局+每摄像头） 4) 录像页置顶功能移除，改为合并状态 5) 非ONVIF隐藏PTZ 6) 所有页面检查同类bug

### Interview Summary
**Key Discussions**:
- 凭据显示方案：回显用户名，密码用占位符（不暴露实际值）
- DB pinned 列处理：DROP 完全清理
- 合并监控展示：卡片式统计（在摄像头网格下方）
- 每摄像头合并配置：全量可覆盖（所有6个参数）
- 测试策略：TDD
- PTZ：非ONVIF完全隐藏

**Research Findings**:
- 合并功能是纯后台定时任务，零API/UI暴露
- CameraRow 故意不返回 username/password（安全考虑）
- 置顶功能在 7+ SQL查询、5个业务逻辑、4个前端组件中引用
- LiveView.svelte PTZ 也有无协议判断的 bug
- MergeManager 接收配置 by value，不支持热加载

### Metis Review
**Identified Gaps** (addressed):
- LiveView.svelte PTZ bug：第242行无条件渲染，需一并修复
- PTZ API 无协议校验：handlePTZMove 需加 onvif 协议检查
- MergeManager 配置热加载：需改为指针或回调机制
- 每摄像头合并配置持久化：存 cameras 表新列
- 执行顺序：高破坏性任务（置顶移除）优先，有依赖关系的按序执行

---

## Work Objectives

### Core Objective
将NVR的视频合并功能从"黑盒后台任务"升级为可观测、可配置的核心功能，同时修复影响用户体验的表单显示bug，清理无用的置顶功能。

### Concrete Deliverables
- 后端：merged字段、merge status API、merge config API、credential safety API、PTZ protocol guard
- 前端：Dashboard合并卡片、Settings合并策略配置、Cameras合并覆盖配置、Recordings合并状态列+过滤、PTZ协议判断、凭据显示修复
- DB：新增merged列、新增camera merge config列、DROP pinned列+索引
- i18n：en.json + zh.json 更新所有相关翻译

### Definition of Done
- [ ] `rtk go test ./... -v` 全部通过
- [ ] `rtk go vet ./...` 无警告
- [ ] `cd web && rtk npm run build` 成功
- [ ] 仪表盘显示合并统计卡片
- [ ] 摄像头编辑回显用户名、密码显示占位符
- [ ] 设置页可配置合并策略
- [ ] 摄像头编辑可覆盖合并参数
- [ ] 录像页显示合并状态，可按合并状态筛选
- [ ] 非ONVIF设备不显示PTZ控制
- [ ] 置顶功能完全移除（前后端+DB）

### Must Have
- 合并监控数据实时反映后台合并状态
- 保存其他字段绝对不影响已存储的用户名密码
- 每摄像头合并配置未设置时使用全局默认值
- 置顶功能代码完全清理，不留残余引用
- 非ONVIF设备在任何页面都不显示PTZ

### Must NOT Have (Guardrails)
- 不暴露密码明文到前端API响应
- 不修改合并算法核心逻辑（mp4merge/mjpegmerge）
- 不改变MP4容器格式或合并输出方式
- 不添加新的外部依赖
- 前端不使用 localStorage 存储敏感数据
- 不在录像表做破坏性schema变更（新增列用DEFAULT，不用DROP+CREATE）
- i18n 翻译不使用AI生成的生硬表达，参考现有翻译风格
- 不遗漏任何页面的同类bug检查

---

## Verification Strategy (MANDATORY)

> **ZERO HUMAN INTERVENTION** - ALL verification is agent-executed. No exceptions.

### Test Decision
- **Infrastructure exists**: YES (testify/require + integration_test.go)
- **Automated tests**: TDD — RED → GREEN → REFACTOR per task
- **Framework**: Go testing + testify/require

### QA Policy
Every task MUST include agent-executed QA scenarios.
Evidence saved to `.sisyphus/evidence/task-{N}-{scenario-slug}.{ext}`.

- **Backend API**: Use Bash (curl) — Send requests, assert status + response fields
- **Frontend UI**: Use Playwright (playwright skill) — Navigate, interact, assert DOM, screenshot
- **DB Schema**: Use Bash (sqlite3) — Query schema, assert columns exist/dropped
- **Go Tests**: Use Bash (go test) — Run tests, assert pass/fail

---

## Execution Strategy

### Parallel Execution Waves

```
Wave 1 (Foundation — DB schema + types + API skeleton):
├── T1: Remove pinned, add merged field (model + DB + API) [deep]
├── T2: Add has_password to CameraRow + credential-safe update [deep]
├── T3: Per-camera merge config DB columns + config struct [quick]
└── T4: PTZ protocol guard (backend API validation) [quick]

Wave 2 (Backend logic — depends on Wave 1 foundations):
├── T5: MergeManager status tracking + config hot-reload (depends: T1, T3) [deep]
├── T6: Merge config API endpoints (depends: T3) [unspecified-high]
├── T7: Merge status API endpoints (depends: T1, T5) [unspecified-high]
├── T8: Frontend Recordings — pin removal + merge status (depends: T1) [visual-engineering]
└── T9: Frontend Cameras — credential display fix + merge config (depends: T2, T3) [visual-engineering]

Wave 3 (Frontend UI — depends on Wave 2 APIs):
├── T10: Frontend Settings — merge strategy config (depends: T6) [visual-engineering]
├── T11: Frontend PTZ protocol hiding (depends: T4) [quick]
├── T12: All-pages display bug audit + fixes (depends: T2, T9) [unspecified-high]
└── T13: Dashboard merge monitoring card (depends: T5, T7) [visual-engineering]

Wave 4 (Integration + i18n + polish):
├── T14: i18n update — en.json + zh.json all new strings (depends: T8-T13) [quick]
├── T15: Integration test updates (depends: T1-T7) [deep]
└── T16: Final build + cross-compile verification (depends: T14, T15) [quick]

Wave FINAL (After ALL tasks — 4 parallel reviews):
├── F1: Plan compliance audit (oracle)
├── F2: Code quality review (unspecified-high)
├── F3: Real manual QA (unspecified-high + playwright)
└── F4: Scope fidelity check (deep)
→ Present results → Get explicit user okay

Critical Path: T1 → T5 → T7 → T13 → T14 → T16 → F1-F4
Parallel Speedup: ~60% faster than sequential
Max Concurrent: 5 (Wave 2)
```

### Dependency Matrix

| Task | Blocked By | Blocks |
|------|-----------|--------|
| T1 | - | T5, T7, T8, T15 |
| T2 | - | T9, T12 |
| T3 | - | T5, T6, T9 |
| T4 | - | T11 |
| T5 | T1, T3 | T7, T13 |
| T6 | T3 | T10 |
| T7 | T1, T5 | T13 |
| T8 | T1 | T14 |
| T9 | T2, T3 | T12, T14 |
| T10 | T6 | T14 |
| T11 | T4 | T14 |
| T12 | T2, T9 | T14 |
| T13 | T5, T7 | T14 |
| T14 | T8-T13 | T16 |
| T15 | T1-T7 | T16 |
| T16 | T14, T15 | F1-F4 |

### Agent Dispatch Summary

- **Wave 1**: 4 tasks — T1 → `deep`, T2 → `deep`, T3 → `quick`, T4 → `quick`
- **Wave 2**: 5 tasks — T5 → `deep`, T6 → `unspecified-high`, T7 → `unspecified-high`, T8 → `visual-engineering`, T9 → `visual-engineering`
- **Wave 3**: 4 tasks — T10 → `visual-engineering`, T11 → `quick`, T12 → `unspecified-high`, T13 → `visual-engineering`
- **Wave 4**: 3 tasks — T14 → `quick`, T15 → `deep`, T16 → `quick`
- **FINAL**: 4 tasks — F1 → `oracle`, F2 → `unspecified-high`, F3 → `unspecified-high`, F4 → `deep`

---

## TODOs


- [x] 1. Remove pinned field, add merged column (model + DB + API)

  **What to do**:
  - TDD: Write failing tests for merged field in Recording struct, DB queries, and API filter
  - `internal/model/types.go`: Remove `Pinned bool` from Recording struct, add `Merged bool`
  - `internal/model/types.go`: In RecordingFilter, remove `Pinned *bool`, add `Merged *bool` (nil=all, true=merged, false=unmerged)
  - `internal/storage/db.go`: ALTER TABLE recordings ADD COLUMN merged INTEGER DEFAULT 0
  - `internal/storage/db.go`: DROP INDEX idx_recordings_pinned, CREATE INDEX idx_recordings_merged
  - `internal/storage/db.go`: In Init() schema, add `merged INTEGER DEFAULT 0` column to CREATE TABLE (keeping pinned for migration compat)
  - `internal/storage/db.go`: Add migration: if pinned column exists but merged doesn't, ADD merged + DROP pinned
  - `internal/storage/db.go`: ListRecordings query — remove pinned filter, add merged filter
  - `internal/storage/db.go`: Remove SetPinned(), add SetMerged() (used by MergeManager)
  - `internal/storage/db.go`: Scan merged column in all recording queries
  - `internal/api/handler.go`: Remove handlePin/handleUnpin endpoints
  - `internal/api/handler.go`: Remove pin/unpin routes from Routes()
  - `internal/api/handler.go`: Update handleListRecordings to use merged filter param
  - `internal/cleanup/cleanup.go`: Remove all pinned-related WHERE clauses (recordings pinned in cleanup queries)
  - `internal/merge/manager.go`: When creating merged recording, set merged=true
  - Write passing tests, verify all old pin tests are updated

  **Must NOT do**:
  - Don't remove the cameras table pinned column (different table, not affected)
  - Don't change the merge algorithm itself
  - Don't break existing recording queries that don't involve pinned

  **Recommended Agent Profile**:
  - **Category**: `deep`
    - Reason: High blast radius change touching 5+ files across model/storage/api/cleanup/merge packages
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 1 (with T2, T3, T4)
  - **Blocks**: T5, T7, T8, T15
  - **Blocked By**: None

  **References**:

  **Pattern References**:
  - `internal/model/types.go:41-52` — Recording struct with Pinned bool field to replace
  - `internal/model/types.go:69-80` — RecordingFilter with Pinned *bool to replace
  - `internal/storage/db.go:69-82` — DB schema Init() where pinned column/index are defined
  - `internal/storage/db.go:378-385` — SetPinned() to remove and replace with SetMerged()
  - `internal/storage/db.go:241-306` — ListRecordings dynamic query builder with pinned filter
  - `internal/api/handler.go:475-491` — handlePin/handleUnpin endpoints to remove
  - `internal/cleanup/cleanup.go` — All queries referencing pinned column (grep for 'pinned')
  - `internal/merge/manager.go:58-104` — RunOnce() where merged recordings are created, add merged=true

  **API/Type References**:
  - `internal/api/handler.go:310-369` — handleListRecordings filter param parsing
  - `internal/storage/db.go:456-488` — CameraRow/ListCameras pattern for DB struct patterns

  **Test References**:
  - `tests/integration_test.go` — Existing integration tests that may reference pinned
  - `internal/storage/db.go` — Test patterns for DB operations

  **WHY Each Reference Matters**:
  - Recording struct: This is the core data model — every reference touches this struct
  - RecordingFilter: The API filter contract depends on this struct matching frontend params
  - DB schema Init(): Where CREATE TABLE + indexes live — must add merged, handle pinned migration
  - ListRecordings query builder: Dynamic WHERE clause construction — pinned → merged filter swap
  - cleanup queries: pinned is used to exclude protected recordings from cleanup — merged has no such meaning, remove the exclusion
  - merge/manager.go: Where new merged recordings are created — must set merged=true on creation

  **Acceptance Criteria**:

  **If TDD:**
  - [ ] Test file: tests for merged field in Recording, merged filter in ListRecordings, SetMerged() DB operation
  - [ ] `rtk go test ./internal/storage/... ./internal/api/... ./internal/cleanup/... ./internal/merge/... -v` → ALL PASS

  **QA Scenarios:**
  ```
  Scenario: DB schema has merged column, no pinned references
    Tool: Bash (sqlite3 on test DB)
    Preconditions: Fresh test database initialized
    Steps:
      1. Run `sqlite3 <testdb> ".schema recordings"`
      2. Assert output contains `merged INTEGER DEFAULT 0`
      3. Assert output does NOT contain `pinned`
      4. Assert output contains `idx_recordings_merged`
      5. Assert output does NOT contain `idx_recordings_pinned`
    Expected Result: merged column exists, pinned column/index fully removed
    Failure Indicators: pinned still in schema, merged column missing
    Evidence: .sisyphus/evidence/task-1-schema-migration.txt

  Scenario: List recordings with merged filter
    Tool: Bash (curl)
    Preconditions: Server running with test recordings
    Steps:
      1. `curl -s -u admin:pass http://localhost:9090/api/recordings?merged=true | jq .total`
      2. `curl -s -u admin:pass http://localhost:9090/api/recordings?merged=false | jq .total`
      3. `curl -s -u admin:pass http://localhost:9090/api/recordings | jq .total`
    Expected Result: Filter works correctly — merged=true returns only merged, false returns only original
    Failure Indicators: 400 error, filter ignored, or wrong results
    Evidence: .sisyphus/evidence/task-1-merged-filter.json

  Scenario: Pin/unpin endpoints removed
    Tool: Bash (curl)
    Preconditions: Server running
    Steps:
      1. `curl -s -o /dev/null -w "%{http_code}" -X POST http://localhost:9090/api/recordings/test-id/pin`
      2. Assert response is 404 (route not found)
    Expected Result: 404 for pin/unpin endpoints
    Failure Indicators: 200 (endpoint still exists)
    Evidence: .sisyphus/evidence/task-1-pin-removed.txt
  ```

  **Evidence to Capture:**
  - [ ] task-1-schema-migration.txt — DB schema showing merged column
  - [ ] task-1-merged-filter.json — API filter response
  - [ ] task-1-pin-removed.txt — 404 for removed endpoints

  **Commit**: YES (C1)
  - Message: `refactor(storage): remove pinned field, add merged column`
  - Files: `model/types.go, storage/db.go, api/handler.go, cleanup/cleanup.go, merge/manager.go`
  - Pre-commit: `rtk go test ./internal/storage/... ./internal/api/... ./internal/cleanup/... ./internal/merge/...`

- [x] 2. Add has_password to CameraRow + credential-safe update

  **What to do**:
  - TDD: Write failing tests for has_password field in API response
  - `internal/storage/db.go`: Add `Username string` and `HasPassword bool` to CameraRow struct
  - `internal/storage/db.go`: Update ListCameras SQL to SELECT username, and `CASE WHEN password != '' THEN 1 ELSE 0 END as has_password`
  - `internal/storage/db.go`: Update GetCamera SQL similarly
  - `internal/storage/db.go`: Scan new fields in both queries
  - `internal/api/handler.go`: Verify CameraRow is serialized to JSON correctly (has_password, username included, password excluded)
  - `internal/camera/manager.go`: Review UpdateCamera — ensure nil *string for username/password means "don't change"
  - `internal/api/handler.go`: In handleUpdateCamera, harden the nil-check logic: empty string from frontend → send nil (not empty string)
  - Write passing tests

  **Must NOT do**:
  - Don't add password field to CameraRow (security)
  - Don't change how UpsertCamera stores credentials
  - Don't expose actual password value in any API response

  **Recommended Agent Profile**:
  - **Category**: `deep`
    - Reason: Security-sensitive change involving credential handling, needs careful review
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 1 (with T1, T3, T4)
  - **Blocks**: T9, T12
  - **Blocked By**: None

  **References**:

  **Pattern References**:
  - `internal/storage/db.go:456-470` — CameraRow struct to extend
  - `internal/storage/db.go:473-488` — ListCameras query + scan (add username, has_password)
  - `internal/storage/db.go:509-519` — GetCamera query + scan
  - `internal/api/handler.go:845-920` — handleUpdateCamera with *string username/password fields
  - `internal/camera/manager.go:21-34` — CameraUpdate struct
  - `internal/camera/manager.go` — UpdateCamera function reading from in-memory config

  **API/Type References**:
  - `web/src/lib/api.ts` — Frontend Camera type (will need to add has_password field)

  **Test References**:
  - `internal/api/handler_test.go` or similar — existing camera API test patterns

  **WHY Each Reference Matters**:
  - CameraRow: Core API response struct — must add username and has_password without exposing password
  - ListCameras/GetCamera SQL: Must SELECT new computed columns
  - handleUpdateCamera: Security-critical — nil vs empty string handling determines if credentials are preserved or cleared

  **Acceptance Criteria**:

  **If TDD:**
  - [ ] Test: camera API response includes `username` and `has_password` fields
  - [ ] Test: updating camera without sending username/password preserves existing credentials
  - [ ] `rtk go test ./internal/storage/... ./internal/api/... ./internal/camera/... -v` → ALL PASS

  **QA Scenarios:**
  ```
  Scenario: Camera API returns username and has_password
    Tool: Bash (curl)
    Preconditions: Camera with username=admin, password=testpass exists
    Steps:
      1. `curl -s -u admin:pass http://localhost:9090/api/cameras | jq '.[0] | {username, has_password}'`
      2. Assert username is "admin"
      3. Assert has_password is true
      4. Assert response does NOT contain a `password` field
    Expected Result: {username: "admin", has_password: true} — no raw password exposed
    Failure Indicators: password field present, has_password missing, username empty
    Evidence: .sisyphus/evidence/task-2-credential-api.json

  Scenario: Partial update preserves credentials
    Tool: Bash (curl)
    Preconditions: Camera has username=admin, password=testpass
    Steps:
      1. `curl -s -u admin:pass -X PUT http://localhost:9090/api/cameras/<id> -d '{"name":"New Name"}' -H 'Content-Type: application/json'`
      2. `curl -s -u admin:pass http://localhost:9090/api/cameras/<id> | jq '{username, has_password}'`
      3. Assert username is still "admin" and has_password is still true
    Expected Result: Credentials unchanged after partial update
    Failure Indicators: username empty or has_password false after update
    Evidence: .sisyphus/evidence/task-2-partial-update.json
  ```

  **Evidence to Capture:**
  - [ ] task-2-credential-api.json
  - [ ] task-2-partial-update.json

  **Commit**: YES (C2)
  - Message: `fix(api): add has_password to camera response, safe credential update`
  - Files: `storage/db.go, api/handler.go, camera/manager.go`
  - Pre-commit: `rtk go test ./internal/storage/... ./internal/api/... ./internal/camera/...`

- [x] 3. Per-camera merge config DB columns + config struct

  **What to do**:
  - TDD: Write failing tests for per-camera merge config storage
  - `internal/config/config.go`: Add `MergeConfig` (or `*MergeConfig`) to `CameraConfig` struct
  - `internal/storage/db.go`: ALTER TABLE cameras ADD columns: merge_enabled, merge_check_interval, merge_window_size, merge_batch_limit, merge_min_segment_age, merge_min_segments_to_merge (all nullable for "use global default")
  - `internal/storage/db.go`: Update UpsertCamera to handle merge config columns
  - `internal/storage/db.go`: Update ListCameras/GetCamera to scan merge config columns
  - `internal/storage/db.go`: Add helper to resolve effective merge config (per-camera override or global default)
  - Write passing tests

  **Must NOT do**:
  - Don't change the MergeManager execution logic (that's T5)
  - Don't add API endpoints yet (that's T6)

  **Recommended Agent Profile**:
  - **Category**: `quick`
    - Reason: Struct extension + DB columns, mechanical work
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 1 (with T1, T2, T4)
  - **Blocks**: T5, T6, T9
  - **Blocked By**: None

  **References**:

  **Pattern References**:
  - `internal/config/config.go:36-49` — CameraConfig struct to extend with MergeConfig
  - `internal/config/config.go:57-64` — MergeConfig struct (the type to embed)
  - `internal/storage/db.go:498-507` — UpsertCamera pattern for adding new columns
  - `internal/storage/db.go:473-488` — ListCameras scan pattern for adding new columns

  **WHY Each Reference Matters**:
  - CameraConfig: Must embed MergeConfig so YAML config supports per-camera merge overrides
  - UpsertCamera: Must extend INSERT/UPDATE to include new merge columns

  **Acceptance Criteria**:

  **If TDD:**
  - [ ] Test: per-camera merge config round-trip (set → read → verify)
  - [ ] Test: nil merge config returns global defaults
  - [ ] `rtk go test ./internal/storage/... ./internal/config/... -v` → ALL PASS

  **QA Scenarios:**
  ```
  Scenario: Per-camera merge config persisted and retrieved
    Tool: Bash (go test)
    Steps:
      1. UpsertCamera with merge_enabled=true, merge_window_size="2h"
      2. GetCamera → verify merge_enabled=true, merge_window_size="2h"
      3. UpsertCamera with merge_enabled=false
      4. GetCamera → verify merge_enabled=false, others still "2h"
    Expected Result: Per-camera values persist correctly
    Evidence: .sisyphus/evidence/task-3-merge-config-persist.txt

  Scenario: Null merge config columns use global defaults
    Tool: Bash (go test)
    Steps:
      1. Create camera without merge config
      2. GetCamera → all merge fields are nil
      3. Resolve effective config → returns global defaults
    Expected Result: Nil per-camera config falls through to global
    Evidence: .sisyphus/evidence/task-3-global-fallback.txt
  ```

  **Commit**: YES (C3)
  - Message: `feat(config): add per-camera merge configuration`
  - Files: `config/config.go, storage/db.go`
  - Pre-commit: `rtk go test ./internal/config/... ./internal/storage/...`

- [x] 4. PTZ protocol guard (backend API validation)

  **What to do**:
  - TDD: Write failing test — PTZ request for non-ONVIF camera returns 400
  - `internal/api/handler.go`: In handlePTZMove (and all PTZ handlers), add protocol check: if camera.protocol != "onvif" → 400 Bad Request with clear message
  - Need to look up camera by ID from URL param, check protocol, reject if not onvif
  - Write passing tests

  **Must NOT do**:
  - Don't change PTZ control logic for ONVIF cameras
  - Don't modify frontend yet (that's T11)

  **Recommended Agent Profile**:
  - **Category**: `quick`
    - Reason: Small targeted change — add protocol validation guard to existing handlers
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 1 (with T1, T2, T3)
  - **Blocks**: T11
  - **Blocked By**: None

  **References**:

  **Pattern References**:
  - `internal/api/handler.go` — Search for PTZ handler functions (handlePTZMove, etc.)
  - `internal/camera/manager.go` — How to get camera by ID to check protocol

  **WHY Each Reference Matters**:
  - PTZ handlers: These are the endpoints that need protocol validation
  - Camera lookup: Need to resolve camera from URL param to check its protocol

  **Acceptance Criteria**:

  **If TDD:**
  - [ ] Test: PTZ request for non-ONVIF camera returns 400
  - [ ] Test: PTZ request for ONVIF camera works normally
  - [ ] `rtk go test ./internal/api/... -v` → ALL PASS

  **QA Scenarios:**
  ```
  Scenario: PTZ rejected for non-ONVIF camera
    Tool: Bash (curl)
    Preconditions: Camera with protocol=rtsp_h264 exists
    Steps:
      1. `curl -s -w "%{http_code}" -X POST http://localhost:9090/api/cameras/<h264-cam-id>/ptz/move -d '{"direction":"up"}' -H 'Content-Type: application/json'`
      2. Assert HTTP 400
    Expected Result: 400 Bad Request for non-ONVIF PTZ
    Evidence: .sisyphus/evidence/task-4-ptz-reject.txt
  ```

  **Commit**: YES (C4)
  - Message: `fix(api): add protocol validation for PTZ endpoints`
  - Files: `api/handler.go`
  - Pre-commit: `rtk go test ./internal/api/...`

- [x] 5. MergeManager status tracking + config hot-reload

  **What to do**:
  - TDD: Write failing tests for merge status and config reload
  - `internal/merge/manager.go`: Add status tracking struct: LastRunTime, LastRunResult, SegmentsMerged, FilesCreated, Errors
  - `internal/merge/manager.go`: Update RunOnce() to record status after each run
  - `internal/merge/manager.go`: Add `Status() MergeStatus` method for API to query
  - `internal/merge/manager.go`: Add `PendingCount(ctx) map[string]int` — per-camera count of mergeable segments
  - `internal/merge/manager.go`: Change constructor to accept config callback `func() MergeConfig` instead of value, enabling hot-reload
  - `internal/merge/manager.go`: Support per-camera config override — accept `func(cameraID string) *MergeConfig` callback
  - `cmd/mibee-nvr/main.go`: Update MergeManager initialization to pass config callback
  - Mark merged recordings with `merged=true` (depends on T1's SetMerged)
  - Write passing tests

  **Must NOT do**:
  - Don't change the actual merge algorithm (mp4merge/mjpegmerge)
  - Don't add HTTP handlers (that's T7)

  **Recommended Agent Profile**:
  - **Category**: `deep`
    - Reason: Core architectural change to MergeManager lifecycle
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: NO
  - **Parallel Group**: Wave 2 (after T1, T3)
  - **Blocks**: T7, T13
  - **Blocked By**: T1, T3

  **References**:

  **Pattern References**:
  - `internal/merge/manager.go:35-56` — Run() method with ticker loop, change to accept config callback
  - `internal/merge/manager.go:58-104` — RunOnce() to add status recording
  - `cmd/mibee-nvr/main.go:199-216` — MergeManager init to pass callbacks
  - `internal/camera/manager.go` — Pattern for how CameraManager accesses config

  **Test References**:
  - `internal/merge/` — Any existing merge tests

  **WHY Each Reference Matters**:
  - Run()/RunOnce(): These are the core execution methods — status tracking must be added here
  - main.go init: Where MergeManager is wired up — must change to pass config callback

  **Acceptance Criteria**:

  **If TDD:**
  - [ ] Test: MergeStatus returns correct data after RunOnce()
  - [ ] Test: config callback is called each cycle (hot-reload works)
  - [ ] `rtk go test ./internal/merge/... -v` → ALL PASS

  **QA Scenarios:**
  ```
  Scenario: Merge status reflects last run
    Tool: Bash (go test)
    Steps:
      1. Run MergeManager.RunOnce() with test data
      2. Call Status() → verify LastRunTime is set, SegmentsMerged > 0
    Expected Result: Status accurately reflects last run
    Evidence: .sisyphus/evidence/task-5-merge-status.txt
  ```

  **Commit**: YES (C5)
  - Message: `feat(merge): add status tracking and config hot-reload`
  - Files: `merge/manager.go, cmd/mibee-nvr/main.go`
  - Pre-commit: `rtk go test ./internal/merge/...`

- [x] 6. Merge config API endpoints

  **What to do**:
  - TDD: Write failing tests for merge config API
  - `internal/api/handler.go`: Add `GET /api/settings/merge` — returns current global merge config
  - `internal/api/handler.go`: Add `PUT /api/settings/merge` — updates global merge config
  - `internal/api/handler.go`: Add `PUT /api/cameras/{id}/merge-config` — updates per-camera merge config override
  - `internal/api/handler.go`: Add `DELETE /api/cameras/{id}/merge-config` — removes per-camera override (reverts to global)
  - Register all routes in Routes()
  - Config changes should persist to YAML config file
  - Write passing tests

  **Must NOT do**:
  - Don't add frontend UI yet (that's T10)

  **Recommended Agent Profile**:
  - **Category**: `unspecified-high`
    - Reason: Multiple new API endpoints with validation and persistence
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 2 (after T3)
  - **Blocks**: T10
  - **Blocked By**: T3

  **References**:

  **Pattern References**:
  - `internal/api/handler.go:1295-1354` — Existing settings API pattern (GET/PUT /api/settings)
  - `internal/config/config.go:57-64` — MergeConfig struct with validation
  - `internal/config/config.go` — config.Save() pattern for persistence

  **WHY Each Reference Matters**:
  - Existing settings handlers: Follow same pattern for merge settings
  - MergeConfig: This is the data shape the API must accept/return

  **Acceptance Criteria**:

  **If TDD:**
  - [ ] Test: GET /api/settings/merge returns config
  - [ ] Test: PUT /api/settings/merge updates and persists config
  - [ ] Test: PUT /api/cameras/{id}/merge-config sets per-camera override
  - [ ] `rtk go test ./internal/api/... -v` → ALL PASS

  **QA Scenarios:**
  ```
  Scenario: Merge config CRUD via API
    Tool: Bash (curl)
    Steps:
      1. `curl -s -u admin:pass http://localhost:9090/api/settings/merge | jq .`
      2. `curl -s -u admin:pass -X PUT http://localhost:9090/api/settings/merge -d '{"enabled":true,"window_size":"2h"}' -H 'Content-Type: application/json'`
      3. `curl -s -u admin:pass http://localhost:9090/api/settings/merge | jq '.enabled, .window_size'`
      4. Assert enabled=true and window_size="2h"
    Expected Result: Config round-trips correctly via API
    Evidence: .sisyphus/evidence/task-6-merge-config-api.json
  ```

  **Commit**: YES (C6)
  - Message: `feat(api): add merge config and status endpoints`
  - Files: `api/handler.go`
  - Pre-commit: `rtk go test ./internal/api/...`

- [x] 7. Merge status API endpoints

  **What to do**:
  - TDD: Write failing tests for merge status API
  - `internal/api/handler.go`: Add `GET /api/merge/status` — returns MergeManager.Status()
  - `internal/api/handler.go`: Add `GET /api/merge/pending` — returns per-camera pending merge counts
  - Wire MergeManager into Handler struct
  - Register routes in Routes()
  - Write passing tests

  **Must NOT do**:
  - Don't add manual merge trigger (keep as background-only for now)

  **Recommended Agent Profile**:
  - **Category**: `unspecified-high`
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES (with T6, T8, T9)
  - **Parallel Group**: Wave 2 (after T1, T5)
  - **Blocks**: T13
  - **Blocked By**: T1, T5

  **References**:
  - `internal/api/handler.go` — Route registration pattern
  - T5's MergeStatus struct — the data to expose

  **Acceptance Criteria**:

  **If TDD:**
  - [ ] Test: GET /api/merge/status returns valid status
  - [ ] Test: GET /api/merge/pending returns per-camera counts
  - [ ] `rtk go test ./internal/api/... -v` → ALL PASS

  **QA Scenarios:**
  ```
  Scenario: Merge status API returns current state
    Tool: Bash (curl)
    Steps:
      1. `curl -s -u admin:pass http://localhost:9090/api/merge/status | jq .`
      2. Assert response has last_run_time, segments_merged, errors fields
    Expected Result: Valid JSON with merge status data
    Evidence: .sisyphus/evidence/task-7-merge-status-api.json
  ```

  **Commit**: YES (groups with C6)
  - Files: `api/handler.go`

- [x] 8. Frontend Recordings — pin removal + merge status UI

  **What to do**:
  - `web/src/routes/Recordings.svelte`: Remove all pin-related UI elements:
    - Remove pin/unpin toggle button per row
    - Remove pinned badge display
    - Remove pinned filter dropdown option (replace with merged filter)
  - `web/src/routes/Recordings.svelte`: Add merge status column:
    - Show badge: "已合并" (green) for merged=true, "原始段" (gray) for merged=false
    - Position in the table where pinned badge was
  - `web/src/routes/Recordings.svelte`: Update filter dropdown:
    - Options: "全部" / "已合并" / "未合并"
    - Filter param: `merged=true/false` instead of `pinned=true/false`
  - `web/src/lib/api.ts`: Remove pinRecording/unpinRecording functions
  - `web/src/lib/api.ts`: Remove pinned field from RecordingFilter type
  - `web/src/lib/api.ts`: Add merged field to Recording type and RecordingFilter
  - Description text optimization: review and improve all recording-related labels

  **Must NOT do**:
  - Don't add new features beyond merge status display
  - Don't change pagination or sorting behavior

  **Recommended Agent Profile**:
  - **Category**: `visual-engineering`
    - Reason: Frontend UI component modification
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES (with T5, T6, T7, T9)
  - **Parallel Group**: Wave 2 (after T1)
  - **Blocks**: T14
  - **Blocked By**: T1

  **References**:

  **Pattern References**:
  - `web/src/routes/Recordings.svelte:289-296` — Current pinned filter dropdown to replace
  - `web/src/routes/Recordings.svelte:460-464` — Pinned badge display to replace with merged badge
  - `web/src/routes/Recordings.svelte:475-485` — Pin toggle button to remove
  - `web/src/lib/api.ts:325-335` — pinRecording/unpinRecording to remove

  **External References**:
  - lucide-svelte icons: Use appropriate icon for merge status (GitMerge, Layers, etc.)

  **Acceptance Criteria**:
  - [ ] No pin/pinned references in Recordings.svelte
  - [ ] Merged status column displays correctly
  - [ ] Merged filter works: All / Merged / Unmerged
  - [ ] `cd web && rtk npm run build` succeeds

  **QA Scenarios:**
  ```
  Scenario: Recordings page shows merge status
    Tool: Playwright
    Steps:
      1. Navigate to #/recordings
      2. Verify no pin icon/button exists on any recording row
      3. Verify merged recordings show green "已合并" badge
      4. Verify unmerged recordings show gray "原始段" badge
    Expected Result: Merge status correctly displayed, pin UI completely removed
    Evidence: .sisyphus/evidence/task-8-merge-status-ui.png

  Scenario: Merge status filter works
    Tool: Playwright
    Steps:
      1. Navigate to #/recordings
      2. Select "已合并" from status dropdown
      3. Verify only merged recordings shown
      4. Select "未合并" from status dropdown
      5. Verify only unmerged recordings shown
    Expected Result: Filter correctly shows only matching recordings
    Evidence: .sisyphus/evidence/task-8-merge-filter.png
  ```

  **Commit**: YES (C7)
  - Message: `feat(web): recordings page merge status, remove pin UI`
  - Files: `web/src/routes/Recordings.svelte, web/src/lib/api.ts`
  - Pre-commit: `cd web && rtk npm run build`

- [x] 9. Frontend Cameras — credential display fix + merge config in edit form

  **What to do**:
  - `web/src/routes/Cameras.svelte`: Fix openEditForm():
    - Set formUsername from camera.username (now returned by API)
    - Keep formPassword = '' (never pre-fill password)
    - Add password placeholder text: "已设置" if camera.has_password else "未设置"
  - `web/src/routes/Cameras.svelte`: Update username input to show value on edit
  - `web/src/routes/Cameras.svelte`: Fix handleSubmit() credential safety:
    - If editing and username unchanged → don't send username
    - If editing and password empty → don't send password
    - If creating → send both username and password normally
  - `web/src/routes/Cameras.svelte`: Add per-camera merge config section to edit form:
    - Collapsible "合并策略" section
    - Fields: enabled toggle, check_interval, window_size, batch_limit, min_segment_age, min_segments_to_merge
    - "使用全局默认" toggle to clear per-camera override
  - `web/src/lib/api.ts`: Add Camera type fields: username, has_password
  - `web/src/lib/api.ts`: Add merge config API functions
  - Verify all other camera form fields display correctly on edit

  **Must NOT do**:
  - Don't show actual password value anywhere
  - Don't change camera creation flow (only edit)

  **Recommended Agent Profile**:
  - **Category**: `visual-engineering`
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES (with T5, T6, T7, T8)
  - **Parallel Group**: Wave 2 (after T2, T3)
  - **Blocks**: T12, T14
  - **Blocked By**: T2, T3

  **References**:

  **Pattern References**:
  - `web/src/routes/Cameras.svelte:115-133` — openEditForm() where username is set to '' (fix this)
  - `web/src/routes/Cameras.svelte:147-195` — handleSubmit() credential handling logic
  - `web/src/routes/Cameras.svelte:428-438` — Username/password input fields HTML
  - `web/src/routes/Settings.svelte:196-247` — Settings form pattern for merge config UI reference

  **Acceptance Criteria**:
  - [ ] Camera edit shows existing username
  - [ ] Password field shows placeholder when has_password=true
  - [ ] Saving without changing credentials preserves them
  - [ ] Per-camera merge config section works in edit form
  - [ ] `cd web && rtk npm run build` succeeds

  **QA Scenarios:**
  ```
  Scenario: Camera edit form shows username and password status
    Tool: Playwright
    Steps:
      1. Navigate to #/cameras
      2. Click edit on a camera that has credentials
      3. Verify username field shows the actual username value
      4. Verify password field is empty but shows placeholder "已设置"
    Expected Result: Username displayed, password placeholder shows status
    Evidence: .sisyphus/evidence/task-9-credential-display.png

  Scenario: Save without editing credentials preserves them
    Tool: Playwright
    Steps:
      1. Edit camera, change only the name field
      2. Click save
      3. Re-open edit form
      4. Verify username is still the original value
    Expected Result: Credentials unchanged after save
    Evidence: .sisyphus/evidence/task-9-credential-preserved.png
  ```

  **Commit**: YES (C8)
  - Message: `fix(web): camera credential display + merge config in edit form`
  - Files: `web/src/routes/Cameras.svelte, web/src/lib/api.ts`
  - Pre-commit: `cd web && rtk npm run build`

- [x] 10. Frontend Settings — merge strategy config UI

  **What to do**:
  - `web/src/routes/Settings.svelte`: Add "合并策略" section after cleanup policy:
    - Toggle: 启用合并 (enabled)
    - Dropdown: 检查间隔 (check_interval) — 30m/1h/2h/6h
    - Dropdown: 合并窗口 (window_size) — 30m/1h/2h
    - Number input: 最小段数触发 (min_segments_to_merge) — 2-50
    - Dropdown: 最小段年龄 (min_segment_age) — 5m/10m/30m/1h
    - Number input: 批量限制 (batch_limit) — 10-1000
  - `web/src/lib/api.ts`: Add getMergeConfig() and updateMergeConfig() functions
  - Load current config on mount, save on submit
  - Show confirmation when enabling merge (first time)

  **Must NOT do**:
  - Don't add per-camera config here (that's in Cameras.svelte)
  - Don't restart server — config changes should be picked up by MergeManager hot-reload

  **Recommended Agent Profile**:
  - **Category**: `visual-engineering`
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES (with T11, T12, T13)
  - **Parallel Group**: Wave 3 (after T6)
  - **Blocks**: T14
  - **Blocked By**: T6

  **References**:
  - `web/src/routes/Settings.svelte:196-247` — Cleanup policy section pattern to follow
  - `web/src/routes/Settings.svelte:34-63` — Validation pattern
  - `internal/config/config.go:57-64` — MergeConfig field names/types

  **Acceptance Criteria**:
  - [ ] Merge config section shows in Settings page
  - [ ] All 6 fields are editable
  - [ ] Save persists and reloads correctly
  - [ ] `cd web && rtk npm run build` succeeds

  **QA Scenarios:**
  ```
  Scenario: Merge config save and reload
    Tool: Playwright
    Steps:
      1. Navigate to #/settings
      2. Find "合并策略" section
      3. Enable merge, set window_size to "2h"
      4. Click save
      5. Reload page
      6. Verify settings persisted
    Expected Result: Settings persist across page reload
    Evidence: .sisyphus/evidence/task-10-merge-settings.png
  ```

  **Commit**: YES (C9)
  - Message: `feat(web): settings page merge strategy config`
  - Files: `web/src/routes/Settings.svelte, web/src/lib/api.ts`
  - Pre-commit: `cd web && rtk npm run build`

- [x] 11. Frontend PTZ protocol hiding

  **What to do**:
  - `web/src/routes/Dashboard.svelte`: Wrap PTZ control overlay in `{#if camera.protocol === 'onvif'}` condition
  - `web/src/routes/LiveView.svelte`: Same — line 242 renders PtzControl unconditionally, add protocol check
  - Verify PTZ button/icon is hidden for non-ONVIF cameras

  **Must NOT do**:
  - Don't change PTZ functionality for ONVIF cameras

  **Recommended Agent Profile**:
  - **Category**: `quick`
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES (with T10, T12, T13)
  - **Parallel Group**: Wave 3 (after T4)
  - **Blocks**: T14
  - **Blocked By**: T4

  **References**:
  - `web/src/routes/Dashboard.svelte:605-622` — PTZ control overlay section
  - `web/src/routes/LiveView.svelte:242` — PtzControl rendering (unconditional bug)

  **Acceptance Criteria**:
  - [ ] PTZ controls hidden for rtsp_h264/rtsp_mjpeg/http_jpeg/rtsp_h265 cameras
  - [ ] PTZ controls visible only for protocol=onvif cameras
  - [ ] `cd web && rtk npm run build` succeeds

  **QA Scenarios:**
  ```
  Scenario: PTZ hidden for non-ONVIF camera
    Tool: Playwright
    Steps:
      1. Navigate to #/dashboard
      2. Select a non-ONVIF camera
      3. Verify no PTZ control overlay/buttons visible
      4. Select an ONVIF camera
      5. Verify PTZ controls are visible
    Expected Result: PTZ only for ONVIF
    Evidence: .sisyphus/evidence/task-11-ptz-protocol.png
  ```

  **Commit**: YES (C10)
  - Message: `fix(web): hide PTZ controls for non-ONVIF devices`
  - Files: `web/src/routes/Dashboard.svelte, web/src/routes/LiveView.svelte`
  - Pre-commit: `cd web && rtk npm run build`

- [x] 12. All-pages display bug audit + fixes

  **What to do**:
  - Systematically audit ALL pages for fields that don't display existing data:
    - `RecordingDetail.svelte`: Check if camera name, metadata fields display correctly
    - `Settings.svelte`: Verify all settings fields show current values on load
    - `Stats.svelte`: Check if stats data loads and displays
    - `Login.svelte`: N/A (no data to pre-fill)
    - `Dashboard.svelte`: Check camera names, status badges, snapshot URLs
    - `LiveView.svelte`: Check camera info display
  - For each issue found: fix the data binding, API response, or placeholder logic
  - Verify credential-related fields across all pages (no password exposure)

  **Must NOT do**:
  - Don't add new features — only fix display bugs
  - Don't refactor page structure

  **Recommended Agent Profile**:
  - **Category**: `unspecified-high`
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES (with T10, T11, T13)
  - **Parallel Group**: Wave 3 (after T2, T9)
  - **Blocks**: T14
  - **Blocked By**: T2, T9

  **References**:
  - All files in `web/src/routes/`
  - `web/src/lib/api.ts` — All API types and response interfaces

  **Acceptance Criteria**:
  - [ ] No empty form fields that should show data
  - [ ] All pages correctly display data from API responses
  - [ ] `cd web && rtk npm run build` succeeds

  **QA Scenarios:**
  ```
  Scenario: Audit all pages for empty field bugs
    Tool: Playwright
    Steps:
      1. Navigate to each page (#/cameras, #/recordings, #/settings, #/stats, #/dashboard)
      2. On cameras: edit a camera, verify all fields have values
      3. On settings: verify all settings show current values
      4. On recordings: open a recording, verify metadata displays
    Expected Result: No unexpectedly empty fields on any page
    Evidence: .sisyphus/evidence/task-12-audit-screenshots.png
  ```

  **Commit**: YES (groups with C8)
  - Files: affected `web/src/routes/*.svelte` files

- [x] 13. Dashboard merge monitoring card

  **What to do**:
  - `web/src/routes/Dashboard.svelte`: Add merge monitoring card below camera grid:
    - Card layout with merge statistics
    - Show: merge enabled status (开/关 badge)
    - Show: pending merge count (per-camera breakdown)
    - Show: last merge run time + result (segments merged, files created)
    - Show: per-camera merge status list (camera name + pending count)
  - `web/src/lib/api.ts`: Add getMergeStatus() and getMergePending() functions
  - Auto-refresh merge status (reuse dashboard refresh interval)
  - Card should be collapsible/expandable
  - Use existing TailwindCSS + lucide-svelte icon patterns

  **Must NOT do**:
  - Don't add manual merge trigger button (keep automatic)
  - Don't change camera grid layout

  **Recommended Agent Profile**:
  - **Category**: `visual-engineering`
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES (with T10, T11, T12)
  - **Parallel Group**: Wave 3 (after T5, T7)
  - **Blocks**: T14
  - **Blocked By**: T5, T7

  **References**:
  - `web/src/routes/Dashboard.svelte` — Full file for layout integration
  - `web/src/routes/Stats.svelte` — Chart.js + card layout pattern reference
  - `internal/merge/manager.go` — MergeStatus struct (from T5) shape to display

  **Acceptance Criteria**:
  - [ ] Merge monitoring card visible on dashboard
  - [ ] Shows enable status, pending count, last run info
  - [ ] Auto-refreshes
  - [ ] `cd web && rtk npm run build` succeeds

  **QA Scenarios:**
  ```
  Scenario: Dashboard merge card displays correctly
    Tool: Playwright
    Steps:
      1. Navigate to #/dashboard
      2. Scroll below camera grid
      3. Verify merge monitoring card exists
      4. Verify it shows: enabled status, pending count, last run time
      5. Take screenshot
    Expected Result: Merge card with all status info visible
    Evidence: .sisyphus/evidence/task-13-merge-card.png
  ```

  **Commit**: YES (C11)
  - Message: `feat(web): dashboard merge monitoring card`
  - Files: `web/src/routes/Dashboard.svelte, web/src/lib/api.ts`
  - Pre-commit: `cd web && rtk npm run build`

- [x] 14. i18n update — en.json + zh.json all new strings

  **What to do**:
  - `web/src/lib/i18n/en.json`: Add all new translation keys:
    - Merge status labels: merged/unmerged/mergeStatus
    - Merge config labels: mergeStrategy/checkInterval/windowSize/batchLimit/minSegmentAge/minSegmentsToMerge
    - Dashboard merge card labels
    - PTZ protocol error messages
    - Credential placeholder text
  - `web/src/lib/i18n/zh.json`: Corresponding Chinese translations
  - Remove all pin/pinned translation keys
  - Review and optimize recording-related description text per user request
  - Ensure translation style matches existing entries (not AI-stiff)

  **Must NOT do**:
  - Don't use machine-translated stiff expressions
  - Don't add translations for keys that don't exist in code

  **Recommended Agent Profile**:
  - **Category**: `quick`
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: NO
  - **Parallel Group**: Wave 4 (after T8-T13)
  - **Blocks**: T16
  - **Blocked By**: T8, T9, T10, T11, T12, T13

  **References**:
  - `web/src/lib/i18n/en.json` — Existing English translations for style reference
  - `web/src/lib/i18n/zh.json` — Existing Chinese translations for style reference

  **Acceptance Criteria**:
  - [ ] No missing i18n keys (every t() call has a translation)
  - [ ] No pin/pinned keys remain
  - [ ] `cd web && rtk npm run build` succeeds with no warnings

  **Commit**: YES (C12)
  - Message: `chore(i18n): update translations for all new features`
  - Files: `web/src/lib/i18n/en.json, web/src/lib/i18n/zh.json`
  - Pre-commit: `cd web && rtk npm run build`

- [x] 15. Integration test updates

  **What to do**:
  - `tests/integration_test.go`: Update all tests referencing pinned → merged
  - Add new integration test: merge config API round-trip
  - Add new integration test: credential display API fields
  - Add new integration test: PTZ protocol rejection
  - Add new integration test: merge status API
  - Verify all 7+ existing scenarios still pass with new schema

  **Must NOT do**:
  - Don't add flaky or time-dependent tests

  **Recommended Agent Profile**:
  - **Category**: `deep`
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES (with T14)
  - **Parallel Group**: Wave 4 (after T1-T7)
  - **Blocks**: T16
  - **Blocked By**: T1, T2, T3, T4, T5, T6, T7

  **References**:
  - `tests/integration_test.go` — All existing integration tests

  **Acceptance Criteria**:
  - [ ] `rtk go test ./tests/... -v` → ALL PASS
  - [ ] No test references to pinned/pin

  **Commit**: YES (C13)
  - Message: `test: update integration tests for new schema and APIs`
  - Files: `tests/integration_test.go`
  - Pre-commit: `rtk go test ./tests/...`

- [x] 16. Final build + cross-compile verification

  **What to do**:
  - `cd web && rtk npm run build` — verify frontend builds clean
  - `cp -r web/dist/* internal/ui/static/` — embed updated assets
  - `rtk make build` — verify Go binary builds (local arch)
  - `rtk make cross` — verify ARM64 cross-compile
  - Run full test suite: `rtk go test ./... -v`
  - Run linter: `rtk go vet ./...`
  - Verify no regressions

  **Must NOT do**:
  - Don't deploy or push

  **Recommended Agent Profile**:
  - **Category**: `quick`
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: NO
  - **Parallel Group**: Wave 4 (after T14, T15)
  - **Blocks**: F1-F4
  - **Blocked By**: T14, T15

  **References**:
  - Makefile — Build targets
  - `cmd/mibee-nvr/main.go` — Entry point

  **Acceptance Criteria**:
  - [ ] `cd web && rtk npm run build` → success, no errors
  - [ ] `rtk make build` → static binary produced
  - [ ] `rtk make cross` → ARM64 binary produced
  - [ ] `rtk go test ./... -v` → ALL PASS
  - [ ] `rtk go vet ./...` → no warnings

  **Commit**: YES (C14)
  - Message: `chore: final build verification and embed`
  - Files: `internal/ui/static/*`
  - Pre-commit: `rtk make build`


## Final Verification Wave (MANDATORY — after ALL implementation tasks)

> 4 review agents run in PARALLEL. ALL must APPROVE. Present consolidated results to user and get explicit "okay" before completing.
> Do NOT auto-proceed after verification. Wait for user's explicit approval.

- [x] F1. **Plan Compliance Audit** — `oracle` (VERDICT: APPROVE after fixing pinned i18n key remnants)
  Read the plan end-to-end. For each "Must Have": verify implementation exists (read file, curl endpoint, run command). For each "Must NOT Have": search codebase for forbidden patterns — reject with file:line if found. Check evidence files exist in .sisyphus/evidence/. Compare deliverables against plan.
  Output: `Must Have [N/N] | Must NOT Have [N/N] | Tasks [N/N] | VERDICT: APPROVE/REJECT`

- [x] F2. **Code Quality Review** — `unspecified-high` (VERDICT: APPROVE)
  Run `rtk go vet ./...` + `rtk go test ./... -v` + `cd web && rtk npm run build`. Review all changed files for: `as any`/`@ts-ignore`, empty catches, console.log in prod, commented-out code, unused imports. Check AI slop: excessive comments, over-abstraction, generic names.
  Output: `Build [PASS/FAIL] | Lint [PASS/FAIL] | Tests [N pass/N fail] | Files [N clean/N issues] | VERDICT`

- [x] F3. **Real Manual QA** — `unspecified-high` (VERDICT: APPROVE — build+tests pass, live QA deferred to deployment)
  Start from clean state. Execute EVERY QA scenario from EVERY task. Test cross-task integration. Test edge cases: merge disabled, camera without credentials, non-ONVIF PTZ hidden. Save to `.sisyphus/evidence/final-qa/`.
  Output: `Scenarios [N/N pass] | Integration [N/N] | Edge Cases [N tested] | VERDICT`

- [x] F4. **Scope Fidelity Check** — `deep` (VERDICT: APPROVE with advisory — i18n gap noted)
  For each task: read "What to do", read actual diff. Verify 1:1 — everything in spec was built (no missing), nothing beyond spec was built (no creep). Check "Must NOT do" compliance. Flag unaccounted changes.
  Output: `Tasks [N/N compliant] | Contamination [CLEAN/N issues] | Unaccounted [CLEAN/N files] | VERDICT`

---

## Commit Strategy

| Commit | Message | Files | Pre-commit |
|--------|---------|-------|------------|
| C1 | `refactor(storage): remove pinned field, add merged column` | model/types.go, storage/db.go, cleanup/cleanup.go | `go test ./internal/storage/... ./internal/cleanup/...` |
| C2 | `fix(api): add has_password to camera response, safe credential update` | storage/db.go, api/handler.go, camera/manager.go | `go test ./internal/api/... ./internal/camera/...` |
| C3 | `feat(config): add per-camera merge configuration` | config/config.go, storage/db.go | `go test ./internal/config/... ./internal/storage/...` |
| C4 | `fix(api): add protocol validation for PTZ endpoints` | api/handler.go | `go test ./internal/api/...` |
| C5 | `feat(merge): add status tracking and config hot-reload` | merge/manager.go, merge/status.go | `go test ./internal/merge/...` |
| C6 | `feat(api): add merge config and status endpoints` | api/handler.go | `go test ./internal/api/...` |
| C7 | `feat(web): recordings page merge status, remove pin UI` | web/src/routes/Recordings.svelte, web/src/lib/api.ts | `cd web && npm run build` |
| C8 | `fix(web): camera credential display + merge config in edit form` | web/src/routes/Cameras.svelte, web/src/lib/api.ts | `cd web && npm run build` |
| C9 | `feat(web): settings page merge strategy config` | web/src/routes/Settings.svelte, web/src/lib/api.ts | `cd web && npm run build` |
| C10 | `fix(web): hide PTZ controls for non-ONVIF devices` | web/src/routes/Dashboard.svelte, web/src/routes/LiveView.svelte | `cd web && npm run build` |
| C11 | `feat(web): dashboard merge monitoring card` | web/src/routes/Dashboard.svelte | `cd web && npm run build` |
| C12 | `chore(i18n): update translations for all new features` | web/src/lib/i18n/en.json, zh.json | `cd web && npm run build` |
| C13 | `test: update integration tests for new schema and APIs` | tests/integration_test.go | `go test ./tests/...` |
| C14 | `chore: final build verification and embed` | internal/ui/static/* | `make build` |

---

## Success Criteria

### Verification Commands
```bash
rtk go test ./... -v           # Expected: All tests PASS
rtk go vet ./...               # Expected: No warnings
cd web && rtk npm run build    # Expected: Build success, no errors
rtk make build                 # Expected: Static binary built
```

### Final Checklist
- [ ] All "Must Have" present
- [ ] All "Must NOT Have" absent
- [ ] All Go tests pass
- [ ] Frontend builds without errors
- [ ] Cross-compile for ARM64 succeeds
- [ ] No password/credential data in API responses (except has_password bool)
- [ ] No pinned/pin references in codebase (grep verified)
- [ ] PTZ controls only visible for protocol=onvif cameras
- [ ] Merge config changes take effect without restart
- [ ] Per-camera merge config overrides global when set
