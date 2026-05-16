# Web UI Optimization — MiBee NVR

## TL;DR

> **Quick Summary**: 全面优化 MiBee NVR Web UI，涵盖录像页过滤/排序/批量删除/连续播放、摄像头管理扩展、设置页 Bug 修复、全局 UX 改善。涉及 Go 后端 API 新增 + Svelte 5 前端重构。
> 
> **Deliverables**:
> - 后端: 录像排序/批量删除 API、摄像头元数据+实时状态 API、DB 迁移
> - 前端: 录像页过滤器重设计、列排序、批量删除 UI、分页跳转、回到顶部、连续播放、摄像头管理扩展表单、UX 改善(空状态/错误/加载/表单验证/密码切换/键盘快捷键/保存确认)
> - Bug 修复: 设置页 itemsPerPage/autoRefresh 断联、返回按钮双箭头
> 
> **Estimated Effort**: Large
> **Parallel Execution**: YES - 5 waves
> **Critical Path**: Wave 1 (后端API) → Wave 3 (前端功能) → Wave 4 (高级功能) → Wave 5 (UX) → FINAL

---

## Context

### Original Request
用户要求优化 MiBee NVR 的 Web UI，包括：录像页下拉过滤无效需重设计、过滤器太小需支持时间窗口、摄像头显示ID改名称、批量删除、回到顶部、列排序、多视频连续播放、设置页Bug、返回按钮双箭头、分页跳转、摄像头管理扩展字段。并要求 UI 设计团队审视额外 UX 改善。

### Interview Summary
**Key Discussions**:
- 过滤器: 去掉 Pinned 状态下拉，替换为搜索框 + 时间窗口日期范围选择
- 摄像头扩展: 增加 备注/描述、安装位置、实时录像状态、设备信息(品牌/型号/序列号)
- 连续播放: 支持播放列表模式 + 同摄像头按时间自动连播，两者都要
- UX 审计: 空状态优化、错误状态重试、加载骨架屏、表单实时验证、密码显隐切换、播放器键盘快捷键、设置保存确认
- 测试: 部署到 192.168.63.31，浏览器测试通过为止

**Research Findings**:
- 后端无批量删除端点、无排序参数、摄像头实时状态未暴露到API
- Camera 数据模型仅5个字段，需要 DB 迁移扩展
- itemsPerPage/autoRefresh 均为本地变量未连接 preferences.ts（同一Bug模式）
- 返回按钮双箭头: i18n 文本含 "←" 前缀 + ArrowLeft 图标重复
- 摄像头元数据字段仅存 DB，不同步到 config YAML（避免双持久化复杂度）
- 播放列表限制为同格式自动连播（H.264+MJPEG混合播放器引擎切换过于复杂，后续迭代）

### Metis Review
**Identified Gaps** (addressed):
- autoRefresh 与 itemsPerPage 有相同 Bug → 一并修复
- 摄像头名称显示用前端 lookup（cameras 数组已加载）→ 不改后端
- 摄像头元数据仅 DB 存储，不同步 config YAML → 减少复杂度
- 批量删除部分失败用 best-effort 策略 → 删除成功的返回，失败的报告
- DB 迁移用简单版本检查 + ALTER TABLE → 不建完整迁移框架

---

## Work Objectives

### Core Objective
优化 MiBee NVR Web UI 的录像管理、摄像头管理、播放体验和全局 UX，使系统达到可部署到测试环境并通过浏览器测试的状态。

### Concrete Deliverables
- `POST /api/recordings/batch-delete` 端点
- `GET /api/recordings?sort_by=&order=` 参数支持
- `GET /api/cameras` 返回 recorder 状态 + 元数据字段
- DB migration 系统 (简单版本检查)
- 录像页: 搜索框 + 日期范围过滤 + 列排序 + 批量选择删除 + 分页跳转 + 回到顶部
- 播放页: 同摄像头连续播放 + 懒加载/预加载 + 键盘快捷键
- 摄像头页: 扩展表单(备注/位置/设备信息) + 名称可内联编辑 + 实时状态显示
- 设置页: itemsPerPage/autoRefresh Bug 修复 + 保存确认
- 全局: 空状态/错误状态/加载骨架屏/表单验证/密码切换/返回箭头修复

### Definition of Done
- [x] `rtk make cross` 编译成功
- [x] 部署到192.168.63.31，所有功能浏览器测试通过
- [x] 所有新增 UI 文本有 en.json + zh.json 翻译

### Must Have
- 所有 11 项用户明确要求的功能
- 7 项 UX 审计改善
- 后端 API 向后兼容（新参数均为可选）
- 所有新 i18n key 同步更新 en/zh

### Must NOT Have (Guardrails)
- 不做跨格式(H.264+MJPEG)混合播放列表
- 不做 WebSocket 实时状态推送（用 polling）
- 不建完整 DB 迁移框架（简单版本检查即可）
- 摄像头元数据不同步到 config YAML
- 不做页面过渡动画、FOUC 预防、PWA 离线支持（后续迭代）
- 不做 Stats 图表交互（本次范围外）

---

## Verification Strategy

> **ZERO HUMAN INTERVENTION** - ALL verification is agent-executed. No exceptions.

### Test Decision
- **Infrastructure exists**: YES (tests/integration_test.go)
- **Automated tests**: Tests-after (单元测试随实现补)
- **Framework**: Go test + Playwright browser testing

### QA Policy
Every task MUST include agent-executed QA scenarios.
Evidence saved to `.sisyphus/evidence/task-{N}-{scenario-slug}.{ext}`.

- **Backend API**: Use Bash (curl) — Send requests, assert status + response fields against 192.168.63.31:9090
- **Frontend UI**: Use Playwright (/playwright skill) — Navigate, interact, assert DOM, screenshot against 192.168.63.31:9090
- **Build/Deploy**: Use Bash — `rtk make cross` + scp to 192.168.63.31 + systemd restart

### Build & Deploy Commands
```bash
rtk make cross                    # Cross-compile for ARM64
scp mibee-nvr-arm64 root@192.168.63.31:/mnt/data/nvr/bin/mibee-nvr
ssh root@192.168.63.31 'systemctl restart mibee-nvr'
```

---

## Execution Strategy

### Parallel Execution Waves

```
Wave 1 (Backend API - 3 tasks, PARTIAL PARALLEL):
├── Task 1: Recordings API: sort + batch-delete [deep]
├── Task 2: Camera backend: migration + metadata + status [deep]
└── Task 3: i18n foundation: all new translation keys [quick]

Wave 2 (Frontend Quick Fixes - 5 tasks, MAX PARALLEL):
├── Task 4: Fix itemsPerPage + autoRefresh disconnect [quick]
├── Task 5: Fix back button double arrows [quick]
├── Task 6: Show camera name in recordings table [quick]
├── Task 7: Settings save confirmation dialog [quick]
└── Task 8: Password visibility toggle [quick]

Wave 3 (Frontend Features - 5 tasks, depends Wave 1+2):
├── Task 9: Recordings filter redesign [unspecified-high]
├── Task 10: Column sorting UI [unspecified-high]
├── Task 11: Batch delete UI with checkboxes [unspecified-high]
├── Task 12: Pagination jump-to-page [quick]
└── Task 13: Back-to-top floating button [quick]

Wave 4 (Advanced Features - 3 tasks, depends Wave 3):
├── Task 14: Continuous playback + lazy/preloading [deep]
├── Task 15: Camera metadata management UI [visual-engineering]
└── Task 16: Playback keyboard shortcuts [quick]

Wave 5 (UX Polish - 2 tasks, depends Wave 3):
├── Task 17: Empty/error/loading states redesign [visual-engineering]
└── Task 18: Form validation real-time feedback [unspecified-high]

Wave FINAL (After ALL tasks — 4 parallel reviews):
├── Task F1: Plan compliance audit [oracle]
├── Task F2: Code quality review [unspecified-high]
├── Task F3: Real browser QA on 192.168.63.31 [unspecified-high + playwright]
└── Task F4: Scope fidelity check [deep]
→ Present results → Get explicit user okay

Critical Path: T1 → T9 → T14 → F1-F4 → user okay
Max Concurrent: 5 (Wave 2)
```

### Dependency Matrix

| Task | Blocked By | Blocks |
|------|-----------|--------|
| 1 | - | 9, 10, 11 |
| 2 | - | 15 |
| 3 | - | 9-18 |
| 4 | - | 12 |
| 5 | - | - |
| 6 | - | 9 |
| 7 | - | - |
| 8 | - | - |
| 9 | 1, 3, 6 | 14 |
| 10 | 1, 3 | - |
| 11 | 1, 3 | - |
| 12 | 3, 4 | - |
| 13 | - | - |
| 14 | 9 | - |
| 15 | 2, 3 | - |
| 16 | 3 | - |
| 17 | 3 | - |
| 18 | 3 | - |

### Agent Dispatch Summary

- **Wave 1**: 3 tasks — T1 `deep`, T2 `deep`, T3 `quick`
- **Wave 2**: 5 tasks — T4-T8 `quick`
- **Wave 3**: 5 tasks — T9-T11 `unspecified-high`, T12-T13 `quick`
- **Wave 4**: 3 tasks — T14 `deep`, T15 `visual-engineering`, T16 `quick`
- **Wave 5**: 2 tasks — T17 `visual-engineering`, T18 `unspecified-high`
- **FINAL**: 4 tasks — F1 `oracle`, F2 `unspecified-high`, F3 `unspecified-high`, F4 `deep`

---

## TODOs

- [x] 1. Recordings API: sort_by + batch-delete endpoint

  **What to do**:
  - Add `SortBy` (string) and `Order` (string) fields to `RecordingFilter` in `internal/model/types.go`
  - Modify `ListRecordings()` in `internal/storage/db.go` to accept sort params and build dynamic ORDER BY clause
    - Supported sort fields: `started_at`, `duration`, `file_size`, `camera_id`
    - Default: `started_at DESC` (preserve current behavior)
    - Validate sort input: whitelist allowed fields, default to started_at if invalid
  - Add `BatchDeleteRecordings(ids []string) ([]string, error)` method to `internal/storage/db.go`
    - Returns list of successfully deleted IDs
    - Best-effort: delete what works, collect failures in error
    - Delete DB records first, then attempt file deletion (non-fatal file errors, log warning)
    - Use transaction for DB deletes
  - Add `handleBatchDeleteRecordings` handler in `internal/api/handler.go`
    - Route: `POST /api/recordings/batch-delete`
    - Request body: `{"ids": ["id1", "id2", ...]}`
    - Response: `{"deleted": ["id1", ...], "failed": [{"id": "id2", "error": "..."}]}`
    - Validate: max 100 IDs per request, reject empty array
  - Register new route in `Routes()` method

  **Must NOT do**:
  - Do not change existing DELETE /api/recordings/{id} behavior
  - Do not add sorting to cameras API (out of scope)
  - Do not modify the Recording struct itself

  **Recommended Agent Profile**:
  - **Category**: `deep`
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES (with Task 2 and Task 3)
  - **Parallel Group**: Wave 1
  - **Blocks**: Tasks 9, 10, 11
  - **Blocked By**: None

  **References**:
  **Pattern References**:
  - `internal/storage/db.go:ListRecordings()` — Current recordings query with filter, modify ORDER BY
  - `internal/storage/db.go:RecordingFilter` — Filter struct to extend with SortBy/Order
  - `internal/api/handler.go:handleDeleteRecording()` — Existing single-delete pattern for reference
  - `internal/api/handler.go:Routes()` — Route registration location
  - `internal/model/types.go:RecordingFilter` — Type definition location

  **External References**:
  - AGENTS.md conventions: error handling (log warning + continue for non-fatal failures)

  **Acceptance Criteria**:
  - [ ] `GET /api/recordings?sort_by=duration&order=asc` returns recordings sorted by duration ascending
  - [ ] `GET /api/recordings?sort_by=invalid_field` falls back to started_at DESC
  - [ ] `POST /api/recordings/batch-delete` with valid IDs returns `{"deleted": [...], "failed": []}`
  - [ ] Batch delete with empty array returns 400 error
  - [ ] Existing `DELETE /api/recordings/{id}` still works unchanged

  **QA Scenarios**:
  ```
  Scenario: Sort recordings by duration descending
    Tool: Bash (curl)
    Preconditions: At least 5 recordings exist on 192.168.63.31
    Steps:
      1. curl -s -u user:pass 'http://192.168.63.31:9090/api/recordings?sort_by=duration&order=desc&limit=5'
      2. Parse JSON, extract .recordings[].duration into array
      3. Verify array is in descending order (each element >= next)
    Expected Result: Durations returned in strictly non-increasing order
    Failure Indicators: Durations not sorted, 400 error, empty response
    Evidence: .sisyphus/evidence/task-1-sort-duration-desc.json

  Scenario: Batch delete recordings
    Tool: Bash (curl)
    Preconditions: At least 3 recordings exist
    Steps:
      1. List recordings: curl -s -u user:pass 'http://192.168.63.31:9090/api/recordings?limit=3'
      2. Extract IDs: jq '.recordings[].id'
      3. curl -X POST -u user:pass -H 'Content-Type: application/json' -d '{"ids":["<id1>","<id2>"]}' 'http://192.168.63.31:9090/api/recordings/batch-delete'
      4. Verify response has deleted array with the IDs
      5. Re-list recordings, verify deleted IDs no longer appear
    Expected Result: Deleted IDs removed from listing, response includes deleted/failed arrays
    Failure Indicators: 404, 500, or IDs still present after delete
    Evidence: .sisyphus/evidence/task-1-batch-delete.json

  Scenario: Batch delete with empty array rejected
    Tool: Bash (curl)
    Steps:
      1. curl -s -o /dev/null -w '%{http_code}' -X POST -u user:pass -H 'Content-Type: application/json' -d '{"ids":[]}' 'http://192.168.63.31:9090/api/recordings/batch-delete'
    Expected Result: HTTP 400
    Evidence: .sisyphus/evidence/task-1-batch-delete-empty.txt
  ```

  **Commit**: YES
  - Message: `feat(api): add recordings sort and batch-delete endpoints`
  - Files: `internal/api/handler.go, internal/storage/db.go, internal/model/types.go`
  - Pre-commit: `rtk go vet ./... && rtk go test ./... -v`

- [x] 2. Camera backend: DB migration + metadata fields + recorder status

  **What to do**:
  - **DB Migration system** in `internal/storage/db.go` `Init()`:
    - Read current version from `schema_meta` table (already exists, version='1')
    - If version='1', execute ALTER TABLE statements to add new columns to cameras
    - Update version to '2' after migration
    - Make migration idempotent (check column existence or wrap in try)
  - **New camera columns**:
    - `description TEXT DEFAULT ''` — 备注/描述
    - `location TEXT DEFAULT ''` — 安装位置
    - `brand TEXT DEFAULT ''` — 设备品牌
    - `model TEXT DEFAULT ''` — 设备型号
    - `serial_number TEXT DEFAULT ''` — 序列号
  - **Update CameraRow struct** in `storage/db.go` to include new fields + `status` (computed, not stored)
  - **Update UpsertCamera()** to handle new fields in INSERT/UPDATE
  - **Update Camera struct** in `model/types.go` to add Description, Location, Brand, Model, SerialNumber
  - **Expose recorder status** in `GET /api/cameras`:
    - Modify `handleListCameras` to call `camMgr.Status()` (already exists in camera/manager.go)
    - Inject status into each camera response: `{...cameraRow, status: "recording"}`
    - Also add to `handleGetCamera` for single camera
  - **Update camera create/update handlers** to accept new metadata fields
  - **Update config.CameraConfig** — Do NOT add new fields to YAML config (DB-only metadata)

  **Must NOT do**:
  - Do NOT add new fields to config YAML (description/location/brand are DB-only)
  - Do NOT build a full migration framework — simple version check + ALTER TABLE
  - Do NOT add WebSocket for status — use polling from existing pattern
  - Do NOT modify CameraManager.Start/Stop logic

  **Recommended Agent Profile**:
  - **Category**: `deep`
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES (with Task 1 and Task 3 — different functions in handler.go/db.go)
  - **Parallel Group**: Wave 1
  - **Blocks**: Task 15
  - **Blocked By**: None

  **References**:
  **Pattern References**:
  - `internal/storage/db.go:Init()` — Schema init location, add migration after CREATE TABLE
  - `internal/storage/db.go:CameraRow` — Struct to extend with new fields
  - `internal/storage/db.go:UpsertCamera()` — CRUD method to update for new columns
  - `internal/storage/db.go:schema_meta` table — Already exists for version tracking
  - `internal/api/handler.go:handleListCameras()` — Add status injection here
  - `internal/camera/manager.go:Status()` — Returns `map[string]RecorderStatus`, already exists
  - `internal/model/types.go:Camera` — Struct to extend
  - `internal/model/types.go:RecorderStatus` — Status constants (recording/stopped/error/reconnecting)

  **API/Type References**:
  - `internal/camera/manager.go:CameraManager` — Has `h` Handler field for accessing from API

  **Acceptance Criteria**:
  - [ ] `GET /api/cameras` returns each camera with `status` field (recording/stopped/error/reconnecting)
  - [ ] `GET /api/cameras` returns new fields: description, location, brand, model, serial_number
  - [ ] `PUT /api/cameras/:id` accepts and persists new metadata fields
  - [ ] Existing cameras without new fields return empty string defaults
  - [ ] DB migration runs idempotently on upgrade

  **QA Scenarios**:
  ```
  Scenario: Camera list includes recorder status and metadata
    Tool: Bash (curl)
    Steps:
      1. curl -s -u user:pass 'http://192.168.63.31:9090/api/cameras'
      2. Parse JSON array, check first camera object
      3. Verify fields exist: .status, .description, .location, .brand, .model, .serial_number
    Expected Result: All new fields present, status is one of recording/stopped/error/reconnecting
    Failure Indicators: Missing fields, null values for status
    Evidence: .sisyphus/evidence/task-2-camera-status.json

  Scenario: Update camera metadata
    Tool: Bash (curl)
    Steps:
      1. Get first camera ID: curl ... | jq '.[0].id'
      2. curl -X PUT -u user:pass -H 'Content-Type: application/json' -d '{"description":"前门","location":"1楼入口","brand":"Hikvision"}' 'http://192.168.63.31:9090/api/cameras/<id>'
      3. Re-fetch camera, verify fields updated
    Expected Result: Updated fields persisted correctly
    Evidence: .sisyphus/evidence/task-2-camera-update.json
  ```

  **Commit**: YES
  - Message: `feat(api): add camera metadata fields and recorder status`
  - Files: `internal/storage/db.go, internal/api/handler.go, internal/model/types.go, internal/camera/manager.go`
  - Pre-commit: `rtk go vet ./... && rtk go test ./... -v`

- [x] 3. i18n foundation: all new translation keys

  **What to do**:
  - Add ALL new i18n keys to both `web/src/lib/i18n/en.json` and `web/src/lib/i18n/zh.json` simultaneously
  - New keys needed (organized by section):
    - **Recordings filter**: search placeholder, date range labels (start/end), clear filters
    - **Recordings sort**: ascending, descending, sort by labels
    - **Batch delete**: select all, deselect all, N selected, confirm batch delete, delete selected
    - **Pagination**: jump to page, page input placeholder
    - **Back to top**: aria label
    - **Camera metadata**: description, location, brand, model, serial number labels
    - **Camera status**: recording, stopped, error, reconnecting
    - **Camera name edit**: edit name, save name, cancel edit
    - **Playback**: next recording, previous recording, playlist, auto-next, playing next
    - **Empty states**: no recordings hint, no cameras hint, add first camera
    - **Error states**: retry button, dismiss, connection lost
    - **Loading**: loading recordings, loading cameras
    - **Settings**: unsaved changes warning, confirm save title, confirm save message
    - **Password toggle**: show password, hide password aria labels
    - **Keyboard shortcuts**: space to play/puse, arrow keys hint
    - **General**: selected count, confirm, cancel, loading
  - Fix existing key: `detail.back` — remove the `← ` prefix (causes double arrow)

  **Must NOT do**:
  - Do NOT add keys with only English (must add zh simultaneously)
  - Do NOT remove or rename existing keys (break existing UI)

  **Recommended Agent Profile**:
  - **Category**: `quick`
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES (with all Wave 1 tasks)
  - **Parallel Group**: Wave 1
  - **Blocks**: Tasks 9-18 (all frontend tasks need these keys)
  - **Blocked By**: None

  **References**:
  - `web/src/lib/i18n/en.json` — English translations, add new keys following existing structure
  - `web/src/lib/i18n/zh.json` — Chinese translations, mirror en.json structure
  - `web/src/lib/i18n/index.svelte.ts` — i18n system, supports `{variable}` interpolation

  **Acceptance Criteria**:
  - [ ] en.json and zh.json have identical key sets
  - [ ] `detail.back` key no longer contains `← ` prefix in either language
  - [ ] No new keys missing from either file

  **QA Scenarios**:
  ```
  Scenario: i18n keys are symmetric
    Tool: Bash (jq)
    Steps:
      1. jq -r 'keys' web/src/lib/i18n/en.json > /tmp/en-keys.txt
      2. jq -r 'keys' web/src/lib/i18n/zh.json > /tmp/zh-keys.txt
      3. diff /tmp/en-keys.txt /tmp/zh-keys.txt
    Expected Result: No diff output (identical key sets)
    Failure Indicators: Diff shows missing or extra keys
    Evidence: .sisyphus/evidence/task-3-i18n-symmetry.txt

  Scenario: Back button text no longer has arrow prefix
    Tool: Bash (jq)
    Steps:
      1. jq -r '.detail.back' web/src/lib/i18n/en.json
      2. Verify output does not start with ←
      3. jq -r '.detail.back' web/src/lib/i18n/zh.json
      4. Verify output does not start with ←
    Expected Result: Value is 'Back' / '返回' without arrow prefix
    Evidence: .sisyphus/evidence/task-3-back-text.txt
  ```

  **Commit**: YES
  - Message: `feat(i18n): add translation keys for UI optimization`
  - Files: `web/src/lib/i18n/en.json, web/src/lib/i18n/zh.json`
  - Pre-commit: `cd web && rtk npm run build`

- [x] 4. Fix itemsPerPage + autoRefresh preference disconnect

  **What to do**:
  - In `web/src/routes/Settings.svelte`:
    - Import `getItemsPerPage, setItemsPerPage, getAutoRefresh, setAutoRefresh` from `lib/preferences.ts`
    - Initialize `itemsPerPage` from `getItemsPerPage()` instead of hardcoded 50
    - Initialize `autoRefresh` from `getAutoRefresh()` instead of hardcoded '30s'
    - When itemsPerPage select changes, call `setItemsPerPage(value)`
    - When autoRefresh select changes, call `setAutoRefresh(value)`
    - These saves are separate from the backend settings save
  - In `web/src/routes/Recordings.svelte`:
    - Import `getItemsPerPage, getAutoRefresh` from `lib/preferences.ts`
    - Replace hardcoded `limit = 50` with `limit = getItemsPerPage()`
    - Replace hardcoded `setInterval(() => loadRecordings(), 30000)` with dynamic interval from `getAutoRefresh()`
    - Listen for preference changes (e.g., via `$effect` on itemsPerPage)
  - Verify `preferences.ts` functions are correctly exported and work

  **Must NOT do**:
  - Do NOT change the backend settings API (itemsPerPage stays localStorage-only)
  - Do NOT change the preference key names in localStorage

  **Recommended Agent Profile**:
  - **Category**: `quick`
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES (with all Wave 2 tasks)
  - **Parallel Group**: Wave 2
  - **Blocks**: Task 12
  - **Blocked By**: None

  **References**:
  - `web/src/lib/preferences.ts:getItemsPerPage/setItemsPerPage/getAutoRefresh/setAutoRefresh` — Already exist, just not called
  - `web/src/routes/Settings.svelte:18-19` — Local state variables to connect
  - `web/src/routes/Settings.svelte:169-177` — itemsPerPage select element
  - `web/src/routes/Recordings.svelte:23` — Hardcoded `limit = 50`
  - `web/src/routes/Recordings.svelte:104-106` — Hardcoded `setInterval(..., 30000)`

  **Acceptance Criteria**:
  - [ ] Set itemsPerPage to 20 in Settings → refresh → Recordings page loads 20 per page
  - [ ] Set autoRefresh to '60s' in Settings → refresh → Recordings refreshes every 60s

  **QA Scenarios**:
  ```
  Scenario: itemsPerPage persists across page navigation
    Tool: Playwright
    Steps:
      1. Navigate to Settings page
      2. Select '20' in itemsPerPage dropdown
      3. Navigate to Recordings page
      4. Count table rows
      5. Verify at most 20 rows shown
    Expected Result: Recordings table shows max 20 rows
    Evidence: .sisyphus/evidence/task-4-items-per-page.png

  Scenario: autoRefresh interval changes
    Tool: Bash (curl + timing)
    Steps:
      1. Set autoRefresh to '10s' in localStorage directly
      2. Load Recordings page
      3. Wait and observe refresh behavior in network tab
    Expected Result: Page refreshes every ~10 seconds
    Evidence: .sisyphus/evidence/task-4-auto-refresh.txt
  ```

  **Commit**: YES (groups with T5-T8)
  - Message: `fix(ui): connect settings preferences and fix UI bugs`

- [x] 5. Fix back button double arrows

  **What to do**:
  - This is already addressed in Task 3 (i18n key fix removing `← ` prefix)
  - Verify the fix renders correctly in `web/src/components/Header.svelte`
  - The `detail.back` key in en.json should be `"Back"` not `"← Back"`
  - The ArrowLeft icon in Header.svelte provides the arrow already
  - If any other i18n keys have `← ` prefix, remove them too
  - Check `zh.json` `detail.back` is `"返回"` not `"← 返回"`

  **Recommended Agent Profile**:
  - **Category**: `quick`
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 2
  - **Blocks**: None
  - **Blocked By**: None (Task 3 may already fix the i18n key)

  **References**:
  - `web/src/components/Header.svelte:80` — `<ArrowLeft size={20} />` icon
  - `web/src/components/Header.svelte:81` — `{backLabel || t('detail.back')}` text
  - `web/src/lib/i18n/en.json` — `detail.back` key
  - `web/src/lib/i18n/zh.json` — `detail.back` key

  **Acceptance Criteria**:
  - [ ] Back button shows single arrow icon + text (no double arrow)
  - [ ] Works on both en and zh languages

  **QA Scenarios**:
  ```
  Scenario: Back button shows single arrow
    Tool: Playwright
    Steps:
      1. Navigate to a recording detail page
      2. Find the back button element
      3. Take screenshot of back button
      4. Count arrow characters in rendered text
    Expected Result: Single ArrowLeft icon + text 'Back'/'返回'
    Evidence: .sisyphus/evidence/task-5-back-button.png
  ```

  **Commit**: YES (groups with T4)

- [x] 6. Show camera name in recordings table

  **What to do**:
  - In `web/src/routes/Recordings.svelte`:
    - Create a helper function `getCameraName(cameraId: string): string`
    - Lookup `cameraId` in the already-loaded `cameras` array (loaded on mount)
    - Return `camera.name` if found, else fall back to `cameraId` (camera may be deleted)
  - Replace `{recording.camera_id}` in table cell (line 209) with `{getCameraName(recording.camera_id)}`
  - Add a small muted camera ID below the name for reference: `<span class='text-xs th-text-tertiary'>{recording.camera_id}</span>`

  **Must NOT do**:
  - Do NOT modify the backend API to add camera_name to recording response
  - Do NOT make a separate API call to resolve names

  **Recommended Agent Profile**:
  - **Category**: `quick`
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 2
  - **Blocks**: Task 9
  - **Blocked By**: None

  **References**:
  - `web/src/routes/Recordings.svelte:60` — `cameras` array already loaded
  - `web/src/routes/Recordings.svelte:209` — Current `{recording.camera_id}` to replace
  - `web/src/lib/api.ts:Camera` interface — has `id` and `name` fields

  **Acceptance Criteria**:
  - [ ] Recordings table shows camera name instead of raw ID
  - [ ] Deleted cameras fall back to showing ID

  **QA Scenarios**:
  ```
  Scenario: Camera name displayed in recordings table
    Tool: Playwright
    Steps:
      1. Navigate to Recordings page
      2. Find first recording's camera column cell
      3. Verify it shows a name (not just alphanumeric ID)
      4. Verify muted ID text visible below name
    Expected Result: Camera name shown as primary text, ID as small secondary text
    Evidence: .sisyphus/evidence/task-6-camera-name.png
  ```

  **Commit**: YES (groups with T4, T5)

- [x] 7. Settings save confirmation dialog

  **What to do**:
  - In `web/src/routes/Settings.svelte`:
    - Before saving retention_days changes, show confirmation dialog if:
      - New retention_days < current retention_days (will trigger immediate cleanup)
      - Disk threshold is being changed
    - Dialog text: warn that reducing retention may delete recordings immediately
    - Add Cancel + Confirm buttons
    - If user cancels, don't save
  - Add visual indicator for unsaved changes (e.g., asterisk on save button)
  - Follow existing modal pattern from Recordings.svelte delete confirmation

  **Recommended Agent Profile**:
  - **Category**: `quick`
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 2
  - **Blocks**: None
  - **Blocked By**: None

  **References**:
  - `web/src/routes/Settings.svelte:60-66` — Current save logic to wrap with confirmation
  - `web/src/routes/Recordings.svelte:285-308` — Existing modal pattern to follow
  - `web/src/routes/Settings.svelte:24-36` — validationErrors pattern

  **Acceptance Criteria**:
  - [ ] Reducing retention_days shows confirmation dialog
  - [ ] Cancel prevents save
  - [ ] Confirm saves normally
  - [ ] No dialog when only increasing retention or not changing retention

  **QA Scenarios**:
  ```
  Scenario: Destructive retention change shows confirmation
    Tool: Playwright
    Steps:
      1. Navigate to Settings page
      2. Change retention_days from 30 to 1
      3. Click Save
      4. Verify confirmation dialog appears
      5. Click Cancel
      6. Verify settings not saved (retention still 30)
    Expected Result: Dialog shown, cancel prevents save
    Evidence: .sisyphus/evidence/task-7-settings-confirm.png

  Scenario: Increasing retention skips confirmation
    Tool: Playwright
    Steps:
      1. Change retention_days from 30 to 60
      2. Click Save
      3. Verify no confirmation dialog, direct save
    Expected Result: Save completes without dialog
    Evidence: .sisyphus/evidence/task-7-settings-no-confirm.txt
  ```

  **Commit**: YES (groups with T4, T5)

- [x] 8. Password visibility toggle

  **What to do**:
  - Add eye icon toggle to password fields in:
    - `web/src/routes/Login.svelte` — login password field
    - `web/src/routes/Cameras.svelte` — camera username/password fields
  - Import `Eye` and `EyeOff` from `lucide-svelte` (already a project dependency)
  - Pattern: `<input type={showPassword ? 'text' : 'password'}>` with toggle button
  - Use absolute positioned button inside input wrapper
  - Add `aria-label` for accessibility

  **Recommended Agent Profile**:
  - **Category**: `quick`
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 2
  - **Blocks**: None
  - **Blocked By**: None

  **References**:
  - `web/src/routes/Login.svelte:76-89` — Password input field to enhance
  - `web/src/routes/Cameras.svelte:218-224` — Camera password field to enhance
  - `lucide-svelte` — Eye/EyeOff icons already available

  **Acceptance Criteria**:
  - [ ] Click eye icon toggles password visibility
  - [ ] Works on both Login and Camera forms
  - [ ] Has proper aria-label

  **QA Scenarios**:
  ```
  Scenario: Password toggle on login form
    Tool: Playwright
    Steps:
      1. Navigate to Login page
      2. Type text in password field
      3. Verify input type is 'password' (masked)
      4. Click eye icon
      5. Verify input type changes to 'text' (visible)
      6. Click eye icon again
      7. Verify input type reverts to 'password'
    Expected Result: Password visibility toggles correctly
    Evidence: .sisyphus/evidence/task-8-password-toggle.png
  ```

  **Commit**: YES (groups with T4, T5)

- [x] 9. Recordings filter redesign: search + date range

  **What to do**:
  - In `web/src/routes/Recordings.svelte`:
    - **Remove** the pinned status dropdown filter entirely
    - **Add search input**: text field that filters by camera name (frontend) and recording ID
      - Debounce 300ms, show search icon from lucide-svelte
    - **Add date range filter**: two `<input type="datetime-local">` for start and end date
      - Pass to existing API `start`/`end` params (already supported in backend, just not used)
      - Add preset buttons: Last 1h, Last 24h, Last 7d, Last 30d
      - Use RFC3339 format when sending to API
    - **Redesign filter layout**: horizontal flex row with search + date pickers + preset buttons
      - Use `flex-wrap` for responsive layout
      - Add 'Clear filters' button that resets all filters
    - Keep existing camera filter dropdown and format dropdown
  - Layout suggestion: `flex flex-wrap gap-3 items-center`
    - Camera dropdown | Format dropdown | Search input | Date start | Date end | Presets | Clear

  **Must NOT do**:
  - Do NOT remove the pinned functionality entirely (keep pin/unpin buttons in actions column)
  - Do NOT change backend filter API (start/end already supported)

  **Recommended Agent Profile**:
  - **Category**: `unspecified-high`
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: NO (depends on T1, T3, T6)
  - **Parallel Group**: Wave 3 (with T10, T11 — different sections of Recordings.svelte)
  - **Blocks**: Task 14
  - **Blocked By**: Tasks 1, 3, 6

  **References**:
  - `web/src/routes/Recordings.svelte:148-172` — Current filter section to redesign
  - `web/src/lib/api.ts:listRecordings()` — Already accepts `start`/`end` params
  - `web/src/routes/Recordings.svelte:115-121` — `$effect` that triggers data load with filters
  - `web/src/routes/Recordings.svelte:60` — `cameras` array loaded for name lookup

  **Acceptance Criteria**:
  - [ ] Search input filters recordings by camera name
  - [ ] Date range picker sends start/end params to API
  - [ ] Preset buttons set date range correctly
  - [ ] Clear filters resets all filters to defaults
  - [ ] Pinned dropdown removed, pin/unpin still works in action column

  **QA Scenarios**:
  ```
  Scenario: Date range filter returns correct recordings
    Tool: Playwright
    Steps:
      1. Navigate to Recordings page
      2. Set start date to 1 hour ago
      3. Set end date to now
      4. Verify only recent recordings shown
      5. Check network tab for start/end query params
    Expected Result: Only recordings within date range shown
    Evidence: .sisyphus/evidence/task-9-date-filter.png

  Scenario: Search filters by camera name
    Tool: Playwright
    Steps:
      1. Type camera name in search box
      2. Verify table rows filter to match only that camera's recordings
      3. Clear search box
      4. Verify all recordings reappear
    Expected Result: Search filters results in real-time
    Evidence: .sisyphus/evidence/task-9-search-filter.png
  ```

  **Commit**: YES
  - Message: `feat(ui): redesign recordings filters with search and date range`

- [x] 10. Column sorting UI on recordings table

  **What to do**:
  - In `web/src/routes/Recordings.svelte`:
    - Add sort state: `sortBy: string` and `sortOrder: 'asc' | 'desc'`
    - Make table headers clickable for sortable columns: Date, Duration, Size, Camera
    - Add sort indicator icons (ChevronUp/ChevronDown from lucide-svelte) to sorted column
    - On column click: if same column, toggle order; if different column, set asc
    - Pass `sort_by` and `order` params to `listRecordings()` API call
  - Style: cursor-pointer on sortable headers, subtle hover bg

  **Must NOT do**:
  - Do NOT add client-side sorting (all sorting is server-side via API)
  - Do NOT make Format or Status columns sortable

  **Recommended Agent Profile**:
  - **Category**: `unspecified-high`
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES (with T9, T11 — different concerns)
  - **Parallel Group**: Wave 3
  - **Blocks**: None
  - **Blocked By**: Tasks 1, 3

  **References**:
  - `web/src/routes/Recordings.svelte:194-256` — Table headers to make clickable
  - Task 1 — Backend sort_by/order API params

  **Acceptance Criteria**:
  - [ ] Click Duration header → sorts ascending, shows up arrow
  - [ ] Click again → sorts descending, shows down arrow
  - [ ] Click Date header → switches sort column, resets to desc
  - [ ] Sort persists across filter changes and pagination

  **QA Scenarios**:
  ```
  Scenario: Sort by duration ascending
    Tool: Playwright
    Steps:
      1. Click Duration column header
      2. Verify up arrow icon shown on Duration header
      3. Read duration values from table
      4. Verify values are in ascending order
    Expected Result: Durations sorted ascending with visual indicator
    Evidence: .sisyphus/evidence/task-10-sort-duration.png
  ```

  **Commit**: YES
  - Message: `feat(ui): add column sorting to recordings table`

- [x] 11. Batch delete UI with checkboxes

  **What to do**:
  - In `web/src/routes/Recordings.svelte`:
    - Add checkbox column to recordings table (first column)
    - Add 'Select all' checkbox in table header
    - Track selected recording IDs in a `Set<string>` state
    - Show selection count: 'N selected' badge when items selected
    - Add floating action bar at bottom when items selected: [N selected] [Delete Selected] [Cancel]
    - Delete button opens confirmation modal showing count
    - On confirm: call `batchDeleteRecordings()` API (new function in api.ts)
    - After delete: refresh list, clear selection, show toast
  - Add `batchDeleteRecordings(ids: string[])` to `web/src/lib/api.ts`

  **Must NOT do**:
  - Do NOT select across pages (selection is per-page only)
  - Do NOT show batch delete bar when no items selected

  **Recommended Agent Profile**:
  - **Category**: `unspecified-high`
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES (with T9, T10 — different UI areas)
  - **Parallel Group**: Wave 3
  - **Blocks**: None
  - **Blocked By**: Tasks 1, 3

  **References**:
  - `web/src/routes/Recordings.svelte:244-250` — Existing single delete action
  - `web/src/routes/Recordings.svelte:285-308` — Existing delete confirmation modal
  - Task 1 — Backend batch delete API endpoint
  - `web/src/lib/api.ts` — Add `batchDeleteRecordings()` function

  **Acceptance Criteria**:
  - [ ] Checkboxes appear in each row + header
  - [ ] Select all selects/deselects all visible rows
  - [ ] Selection count shown in floating bar
  - [ ] Delete confirmation shows count
  - [ ] After delete, recordings removed and selection cleared

  **QA Scenarios**:
  ```
  Scenario: Batch select and delete recordings
    Tool: Playwright
    Steps:
      1. Navigate to Recordings page
      2. Check 3 recording checkboxes
      3. Verify '3 selected' badge shown
      4. Click 'Delete Selected' button
      5. Verify confirmation modal shows 'Delete 3 recordings?'
      6. Click confirm
      7. Verify toast shows success
      8. Verify deleted recordings no longer in list
    Expected Result: 3 recordings deleted, list refreshed
    Evidence: .sisyphus/evidence/task-11-batch-delete.png
  ```

  **Commit**: YES
  - Message: `feat(ui): add batch delete with checkboxes`

- [x] 12. Pagination jump-to-page input

  **What to do**:
  - In `web/src/components/Pagination.svelte`:
    - Add a small input field after page numbers: 'Go to page [___] of N'
    - Input accepts numbers only, max value = totalPages
    - On Enter or blur with valid number, call `onPageChange(targetPage)`
    - Style: inline with pagination controls, matching existing design
  - In `web/src/routes/Recordings.svelte`:
    - Pass `totalPages` to Pagination component (may need new prop)

  **Recommended Agent Profile**:
  - **Category**: `quick`
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES (with T9-T11, T13)
  - **Parallel Group**: Wave 3
  - **Blocks**: None
  - **Blocked By**: Tasks 3, 4

  **References**:
  - `web/src/components/Pagination.svelte` — Component to enhance

  **Acceptance Criteria**:
  - [ ] Input shows current page, accepts new page number
  - [ ] Enter navigates to valid page
  - [ ] Invalid input (negative, > total) is clamped or rejected

  **QA Scenarios**:
  ```
  Scenario: Jump to specific page
    Tool: Playwright
    Steps:
      1. Navigate to Recordings page with 5+ pages
      2. Type '3' in jump-to-page input
      3. Press Enter
      4. Verify page 3 is active
      5. Verify URL hash or page indicator shows 3
    Expected Result: Navigates to page 3
    Evidence: .sisyphus/evidence/task-12-jump-page.png
  ```

  **Commit**: YES
  - Message: `feat(ui): add pagination jump-to-page input`

- [x] 13. Back-to-top floating button

  **What to do**:
  - In `web/src/routes/Recordings.svelte`:
    - Add floating button fixed at bottom-right
    - Show only when scrolled down > 300px (use scroll event listener)
    - Use ArrowUp icon from lucide-svelte
    - On click: smooth scroll to top (`window.scrollTo({top: 0, behavior: 'smooth'})`)
    - Style: circular button with primary accent, shadow, subtle hover effect
    - Add fade-in/fade-out transition
    - `aria-label` for accessibility
  - Consider extracting as reusable component if useful on other pages

  **Recommended Agent Profile**:
  - **Category**: `quick`
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES (with all Wave 3 tasks)
  - **Parallel Group**: Wave 3
  - **Blocks**: None
  - **Blocked By**: None

  **References**:
  - `web/src/routes/Recordings.svelte` — Add scroll listener and button
  - `lucide-svelte` — ArrowUp icon

  **Acceptance Criteria**:
  - [ ] Button appears after scrolling down 300px
  - [ ] Click smoothly scrolls to top
  - [ ] Button hidden when at top

  **QA Scenarios**:
  ```
  Scenario: Back to top button visibility and function
    Tool: Playwright
    Steps:
      1. Navigate to Recordings page with many entries
      2. Scroll down
      3. Verify floating button appears
      4. Click button
      5. Verify page scrolled to top
      6. Verify button disappears
    Expected Result: Button appears on scroll, click scrolls to top
    Evidence: .sisyphus/evidence/task-13-back-to-top.png
  ```

  **Commit**: YES
  - Message: `feat(ui): add back-to-top floating button`

- [x] 14. Continuous playback + lazy/preloading

  **What to do**:
  - In `web/src/routes/RecordingDetail.svelte`:
    - **Same-camera auto-next**: When current recording ends, query next recording by same camera_id with started_at > current ended_at, sorted by started_at ASC, limit 1
    - **Playlist mode**: Accept multiple recording IDs via URL hash (e.g., `#/recordings/play?id=1&id=2&id=3`)
      - Show playlist sidebar/panel with recording list and current playing indicator
      - Auto-advance to next on completion
    - **Preloading**: When 80% of current recording played, prefetch next recording's blob in background
      - For H.264: Start XHR blob download of next video
      - For MJPEG: Pre-fetch frame list and start loading first N frames
    - **Lazy loading for MJPEG**: Instead of loading ALL frames upfront, load in windowed batches (e.g., 50 frames at a time)
      - Load initial batch, then load more as playback progresses
      - Unload old frames to save memory (RPi 3B constraint: 905MB RAM)
    - Add playback controls: 'Next' / 'Previous' buttons, playlist toggle
    - Add transition between recordings (brief loading indicator)
    - Show playlist position: 'Playing 3/10'

  **Must NOT do**:
  - Do NOT build cross-format playlist (H.264 + MJPEG mixed) — same-format only
  - Do NOT preload more than 1 recording ahead (memory constraint)
  - Do NOT use WebSocket for status

  **Recommended Agent Profile**:
  - **Category**: `deep`
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: NO (depends on T9 for filter/search integration)
  - **Parallel Group**: Wave 4 (sequential)
  - **Blocks**: None
  - **Blocked By**: Task 9

  **References**:
  - `web/src/routes/RecordingDetail.svelte:352-373` — H.264 video playback
  - `web/src/routes/RecordingDetail.svelte:374-482` — MJPEG canvas player
  - `web/src/routes/RecordingDetail.svelte:97-143` — MJPEG frame preloading (to make lazy)
  - `web/src/lib/api.ts:listRecordings()` — Query next recording by camera_id + time
  - AGENTS.md: Memory budget — RPi 3B has 905MB RAM, segment_duration > 30s causes 60MB+

  **Acceptance Criteria**:
  - [ ] Playing a recording, when it ends, next recording from same camera auto-starts
  - [ ] Next/Previous buttons work in playlist mode
  - [ ] MJPEG lazy loads frames in batches instead of all-at-once
  - [ ] Next recording preloads at 80% of current playback
  - [ ] Playlist shows current position

  **QA Scenarios**:
  ```
  Scenario: Auto-next plays next recording from same camera
    Tool: Playwright
    Steps:
      1. Navigate to Recordings page
      2. Click on a recording from Camera A to play
      3. Wait for playback to complete (or seek to end)
      4. Verify next recording from Camera A starts playing
      5. Verify playlist indicator shows 'Playing 2/N'
    Expected Result: Seamless transition to next recording
    Evidence: .sisyphus/evidence/task-14-auto-next.png

  Scenario: MJPEG lazy loading doesn't load all frames upfront
    Tool: Bash (memory check)
    Steps:
      1. Open MJPEG recording with 100+ frames
      2. Monitor browser memory via devtools
      3. Verify only first batch loaded initially
    Expected Result: Memory usage lower than pre-loading all frames
    Evidence: .sisyphus/evidence/task-14-lazy-loading.txt
  ```

  **Commit**: YES
  - Message: `feat(ui): add continuous playback with lazy/preloading`

- [x] 15. Camera metadata management UI

  **What to do**:
  - In `web/src/routes/Cameras.svelte`:
    - **Expand form**: Add new fields to Add/Edit form:
      - Description/Notes (textarea)
      - Location (text input, placeholder: e.g. '1F Entrance')
      - Brand (text input)
      - Model (text input)
      - Serial Number (text input)
    - **Show recorder status**: Add status badge in camera table
      - recording → green badge
      - stopped → gray badge
      - error → red badge
      - reconnecting → yellow badge
    - **Inline name edit**: Make camera name in table clickable
      - Click → turns into input field
      - Enter or blur → saves name via updateCamera API
      - Escape → cancels edit
      - Show pencil icon on hover
    - **Expand table**: Add Description and Status columns
  - Update `web/src/lib/api.ts`:
    - Add new fields to Camera interface and CreateCameraRequest/UpdateCameraRequest

  **Must NOT do**:
  - Do NOT make camera ID editable
  - Do NOT add username/password columns to table (security)

  **Recommended Agent Profile**:
  - **Category**: `visual-engineering`
  - **Skills**: [`/frontend-ui-ux`]

  **Parallelization**:
  - **Can Run In Parallel**: YES (with T14, T16)
  - **Parallel Group**: Wave 4
  - **Blocks**: None
  - **Blocked By**: Tasks 2, 3

  **References**:
  - `web/src/routes/Cameras.svelte:182-212` — Form fields to extend
  - `web/src/routes/Cameras.svelte:272-312` — Table columns to add to
  - `web/src/lib/api.ts:Camera` — Interface to extend
  - Task 2 — Backend camera metadata + status API

  **Acceptance Criteria**:
  - [ ] Camera form has description, location, brand, model, serial number fields
  - [ ] Camera table shows status badge and description
  - [ ] Clicking camera name enables inline edit
  - [ ] Enter saves, Escape cancels
  - [ ] Status badge shows correct color per state

  **QA Scenarios**:
  ```
  Scenario: Edit camera metadata
    Tool: Playwright
    Steps:
      1. Navigate to Cameras page
      2. Click Edit on a camera
      3. Fill in: description='Front door', location='1F Entrance', brand='Hikvision'
      4. Save form
      5. Verify table shows new description
      6. Reload page, verify data persisted
    Expected Result: All metadata fields saved and displayed
    Evidence: .sisyphus/evidence/task-15-camera-metadata.png

  Scenario: Inline name edit
    Tool: Playwright
    Steps:
      1. Click on a camera name in the table
      2. Verify it turns into an input field
      3. Type new name
      4. Press Enter
      5. Verify name updates in table
      6. Reload, verify persisted
    Expected Result: Name saved via inline edit
    Evidence: .sisyphus/evidence/task-15-inline-name.png
  ```

  **Commit**: YES
  - Message: `feat(ui): add camera metadata management`

- [x] 16. Playback keyboard shortcuts

  **What to do**:
  - In `web/src/routes/RecordingDetail.svelte`:
    - Add global `keydown` listener on mount, remove on unmount
    - **MJPEG player** keyboard shortcuts:
      - Space: toggle play/pause
      - ArrowLeft: previous frame
      - ArrowRight: next frame
    - **H.264 player** (native video):
      - Space: toggle play/pause
      - ArrowLeft: seek back 5s
      - ArrowRight: seek forward 5s
    - **Global**:
      - Escape: go back (same as back button)
    - Add shortcut hints tooltip or overlay (press '?' to show)
    - Prevent shortcuts when typing in inputs (if any exist on page)

  **Recommended Agent Profile**:
  - **Category**: `quick`
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES (with T14, T15)
  - **Parallel Group**: Wave 4
  - **Blocks**: None
  - **Blocked By**: Task 3

  **References**:
  - `web/src/routes/RecordingDetail.svelte:435-480` — MJPEG playback controls
  - `web/src/routes/RecordingDetail.svelte:360-373` — H.264 video element

  **Acceptance Criteria**:
  - [ ] Space toggles play/pause for both H.264 and MJPEG
  - [ ] Arrow keys navigate frames (MJPEG) or seek (H.264)
  - [ ] Escape goes back

  **QA Scenarios**:
  ```
  Scenario: Keyboard shortcuts control MJPEG playback
    Tool: Playwright
    Steps:
      1. Open an MJPEG recording
      2. Press Space → verify playback toggles
      3. Press ArrowRight → verify next frame shown
      4. Press ArrowLeft → verify previous frame shown
    Expected Result: All keyboard shortcuts work
    Evidence: .sisyphus/evidence/task-16-keyboard.png
  ```

  **Commit**: YES (groups with T14)
  - Message: `feat(ui): add playback keyboard shortcuts`

- [x] 17. Empty/error/loading states redesign

  **What to do**:
  - **Empty states** — Apply to Recordings, Cameras pages:
    - Add large icon (lucide-svelte: Video/Camera) centered
    - Add heading: 'No recordings yet' / 'No cameras configured'
    - Add descriptive hint text
    - Add CTA button: 'Add camera' / 'Check camera settings'
    - Style: `p-12 text-center` with muted colors
  - **Error states** — Apply to all data-fetching pages:
    - Show error icon (AlertCircle) + error message
    - Add 'Retry' button that re-fetches data
    - Add 'Dismiss' button
    - Red-tinted background card
  - **Loading states** — Replace spinners with skeleton screens:
    - Recordings: skeleton table rows (3-5 rows with animated gradient)
    - Cameras: skeleton cards
    - Only show skeleton on initial load; use subtle refresh indicator on subsequent loads
    - Use CSS animation for skeleton pulse effect

  **Recommended Agent Profile**:
  - **Category**: `visual-engineering`
  - **Skills**: [`/frontend-ui-ux`]

  **Parallelization**:
  - **Can Run In Parallel**: YES (with T18)
  - **Parallel Group**: Wave 5
  - **Blocks**: None
  - **Blocked By**: Task 3

  **References**:
  - `web/src/routes/Recordings.svelte:184-191` — Current loading/empty/error states
  - `web/src/routes/Cameras.svelte:168-171` — Current loading state
  - `web/src/app.css:191-196` — Card hover effect (animation reference)
  - `lucide-svelte` — VideoOff, CameraOff, AlertCircle icons

  **Acceptance Criteria**:
  - [ ] Empty recordings shows icon + hint + CTA
  - [ ] Error state shows retry button
  - [ ] Loading shows skeleton rows instead of spinner

  **QA Scenarios**:
  ```
  Scenario: Empty recordings state
    Tool: Playwright
    Steps:
      1. Filter recordings to empty result (far future date range)
      2. Verify icon shown
      3. Verify hint text visible
      4. Verify CTA button exists
    Expected Result: Rich empty state with icon, text, and action
    Evidence: .sisyphus/evidence/task-17-empty-state.png

  Scenario: Error state with retry
    Tool: Playwright
    Steps:
      1. Simulate network error (disconnect briefly)
      2. Verify error message with retry button shown
      3. Click retry
      4. Verify data loads after reconnect
    Expected Result: Error shows retry, click reloads data
    Evidence: .sisyphus/evidence/task-17-error-state.png
  ```

  **Commit**: YES
  - Message: `feat(ux): improve empty, error, and loading states`

- [x] 18. Form validation real-time feedback

  **What to do**:
  - In `web/src/routes/Cameras.svelte`:
    - Add `onblur` validation for required fields (Name, URL, Protocol)
    - Show error message below field immediately when validation fails
    - Add red border on invalid fields, clear on valid
    - Add `aria-invalid` and `aria-describedby` for accessibility
    - On `oninput`, clear existing error for that field (live correction)
  - In `web/src/routes/Settings.svelte`:
    - Add live validation for retention_days (min 1, max 365)
    - Add live validation for disk_threshold (min 50, max 99)
    - Show current vs proposed value comparison for destructive changes

  **Recommended Agent Profile**:
  - **Category**: `unspecified-high`
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES (with T17)
  - **Parallel Group**: Wave 5
  - **Blocks**: None
  - **Blocked By**: Task 3

  **References**:
  - `web/src/routes/Cameras.svelte:33-39` — Current validation logic (submit-only)
  - `web/src/routes/Cameras.svelte:82-98` — validate() function to enhance
  - `web/src/routes/Settings.svelte:24-36` — Settings validation pattern

  **Acceptance Criteria**:
  - [ ] Clicking into Name field then tabbing out shows error if empty
  - [ ] Typing in field with error clears error in real-time
  - [ ] Invalid fields have red border
  - [ ] Form cannot submit with validation errors

  **QA Scenarios**:
  ```
  Scenario: Real-time form validation on camera form
    Tool: Playwright
    Steps:
      1. Open Add Camera form
      2. Click Name field, then tab out (blur) without typing
      3. Verify error message appears below Name field
      4. Verify Name field has red border
      5. Type a name in the field
      6. Verify error clears immediately
    Expected Result: Error shown on blur, cleared on input
    Evidence: .sisyphus/evidence/task-18-form-validation.png
  ```

  **Commit**: YES
  - Message: `feat(ux): add real-time form validation feedback`

## Final Verification Wave (MANDATORY — after ALL implementation tasks)

> 4 review agents run in PARALLEL. ALL must APPROVE. Present consolidated results to user and get explicit "okay" before completing.
> Deploy to 192.168.63.31 first, then run all verification.

- [x] F1. **Plan Compliance Audit** — `oracle` — APPROVE: 18/18 tasks verified
  Read the plan end-to-end. For each "Must Have": verify implementation exists (read file, curl endpoint, run command). For each "Must NOT Have": search codebase for forbidden patterns — reject with file:line if found. Check evidence files exist in .sisyphus/evidence/. Compare deliverables against plan.
  Output: `Must Have [N/N] | Must NOT Have [N/N] | Tasks [N/N] | VERDICT: APPROVE/REJECT`

- [x] F2. **Code Quality Review** — APPROVE (after HIGH fixes): 2 HIGH fixed, 10 MEDIUM/LOW tracked
  Run `rtk go vet ./...` + `rtk go test ./... -v`. Review all changed files for: `as any`/type assertions, empty catches, fmt.Printf in prod, commented-out code, unused imports. Check AI slop: excessive comments, over-abstraction, generic names. Run `cd web && rtk npm run build` to verify frontend compiles.
  Output: `Build [PASS/FAIL] | Lint [PASS/FAIL] | Tests [N pass/N fail] | Files [N clean/N issues] | VERDICT`

- [x] F3. **Real Browser QA** — APPROVE (after reactive state fix): All pages tested, zero console errors, 3 commits total
  Deploy to 192.168.63.31. Start from clean state. Execute EVERY QA scenario from EVERY task — follow exact steps, capture evidence. Test cross-task integration (features working together). Test edge cases: empty state, invalid input, rapid actions. Save screenshots to `.sisyphus/evidence/final-qa/`.
  Output: `Scenarios [N/N pass] | Integration [N/N] | Edge Cases [N tested] | VERDICT`
  Deploy to 192.168.63.31. Start from clean state. Execute EVERY QA scenario from EVERY task — follow exact steps, capture evidence. Test cross-task integration (features working together). Test edge cases: empty state, invalid input, rapid actions. Save screenshots to `.sisyphus/evidence/final-qa/`.
  Output: `Scenarios [N/N pass] | Integration [N/N] | Edge Cases [N tested] | VERDICT`

- [x] F4. **Scope Fidelity Check** — APPROVE: 13/13 constraints verified
  For each task: read "What to do", read actual diff (git log/diff). Verify 1:1 — everything in spec was built, nothing beyond spec was built. Check "Must NOT do" compliance. Detect cross-task contamination. Flag unaccounted changes.
  Output: `Tasks [N/N compliant] | Contamination [CLEAN/N issues] | Unaccounted [CLEAN/N files] | VERDICT`

---

## Commit Strategy

- **Wave 1**: `feat(api): add recordings sort and batch-delete endpoints` — internal/api/handler.go, internal/storage/db.go, internal/model/types.go
- **Wave 1**: `feat(api): add camera metadata fields and recorder status` — internal/storage/db.go, internal/api/handler.go, internal/model/types.go, internal/camera/manager.go
- **Wave 1**: `feat(i18n): add translation keys for UI optimization` — web/src/lib/i18n/en.json, web/src/lib/i18n/zh.json
- **Wave 2**: `fix(ui): connect settings preferences and fix UI bugs` — web/src/routes/Settings.svelte, web/src/routes/Recordings.svelte, web/src/components/Header.svelte, web/src/lib/preferences.ts
- **Wave 3**: `feat(ui): redesign recordings filters with search and date range` — web/src/routes/Recordings.svelte
- **Wave 3**: `feat(ui): add column sorting to recordings table` — web/src/routes/Recordings.svelte
- **Wave 3**: `feat(ui): add batch delete with checkboxes` — web/src/routes/Recordings.svelte
- **Wave 3**: `feat(ui): add pagination jump-to-page and back-to-top` — web/src/components/Pagination.svelte, web/src/routes/Recordings.svelte
- **Wave 4**: `feat(ui): add continuous playback with lazy/preloading` — web/src/routes/RecordingDetail.svelte
- **Wave 4**: `feat(ui): add camera metadata management` — web/src/routes/Cameras.svelte, web/src/lib/api.ts
- **Wave 5**: `feat(ux): improve empty/error/loading states and form validation` — multiple files

---

## Success Criteria

### Verification Commands
```bash
rtk make cross                                          # Expected: builds ./mibee-nvr-arm64
rtk go test ./... -v                                    # Expected: all tests pass
cd web && rtk npm run build                             # Expected: builds to web/dist/

# API verification
curl -s -u user:pass 'http://192.168.63.31:9090/api/recordings?sort_by=duration&order=desc&limit=5' | jq '.recordings | length'
curl -s -u user:pass 'http://192.168.63.31:9090/api/recordings?start=2025-01-01T00:00:00Z&end=2026-12-31T23:59:59Z' | jq '.total'
curl -s -u user:pass 'http://192.168.63.31:9090/api/cameras' | jq '.[0] | .status, .description, .location'
curl -s -X POST -u user:pass -H 'Content-Type: application/json' -d '{"ids":["test"]}' 'http://192.168.63.31:9090/api/recordings/batch-delete'
```

### Final Checklist
- [x] All "Must Have" present
- [x] All "Must NOT Have" absent
- [x] All tests pass
- [x] Browser testing on 192.168.63.31 all green
