# 录像页过滤系统全面优化

## TL;DR

> **Quick Summary**: 将录像页搜索从客户端过滤改为服务器端 SQL LIKE 搜索，修复分页/搜索不一致，添加 pinned 筛选器，修复 clearFilters，修复 TypeScript 类型，添加 AbortController 竞态保护。
> 
> **Deliverables**:
> - 后端 API 支持 `?search=` 参数，LIKE 模糊搜索 camera_id/format/file_path
> - 前端搜索改为服务器端，删除 `filteredRecordings` 客户端过滤
> - 前端添加 pinned 筛选器 UI
> - 修复 clearFilters 重置逻辑
> - 修复 TypeScript 类型定义
> - 添加 AbortController 防止请求竞态
> 
> **Estimated Effort**: Medium
> **Parallel Execution**: YES - 2 waves + final
> **Critical Path**: Task 1 (后端 TDD) → Task 2+3 (前端并行) → Final Verification

---

## Context

### Original Request
用户希望优化录像页的过滤功能。经测试发现当前搜索框是客户端过滤（只搜索当前页），与分页计数严重不一致。用户要求全面优化所有过滤问题。

### Interview Summary
**Key Discussions**:
- 8 个问题被识别，用户选择全部修复
- 搜索范围：纯 recordings 表字段 LIKE（不 JOIN cameras 表）
- 搜索字段：camera_id, format, file_path
- 测试策略：TDD（后端），前端用 Agent QA
- clearFilters 应重置为初始状态（最近1小时）

**Research Findings**:
- `searchQuery` 未在 `$effect` 依赖数组中（仅客户端过滤）
- `filteredRecordings` 在 5 处使用：toggleSelectAll(2), checkbox(1), table(1), derived(1)
- `CountRecordingsWithFilter` 和 `ListRecordings` 有重复的 WHERE 构建逻辑
- 后端已有 `pinned` 过滤支持，仅缺前端 UI
- `RecordingFilter.Pinned` 已存在（`*bool`），handler 已解析 `?pinned=true/false`

### Metis Review
**Identified Gaps** (addressed):
- `searchQuery` 未在 `$effect` 依赖数组中 → 必须添加
- `filteredRecordings` 有 5 处引用需全部替换为 `recordings` → 逐个替换
- `CountRecordingsWithFilter` 也必须添加 Search LIKE → 两处同步更新
- LIKE 通配符注入风险 → 需转义 `%` 和 `_`
- 搜索防抖 100ms 对 RPi 3B 可能偏短 → 保持现状，SQLite LIKE 对千级数据足够快

---

## Work Objectives

### Core Objective
将录像搜索从前端客户端过滤迁移到后端服务器端 SQL LIKE 搜索，同时修复所有已识别的过滤相关问题。

### Concrete Deliverables
- `internal/model/types.go`: RecordingFilter 新增 `Search string` 字段
- `internal/storage/db.go`: ListRecordings 和 CountRecordingsWithFilter 添加 Search LIKE WHERE
- `internal/api/handler.go`: 解析 `?search=` 查询参数
- `web/src/lib/api.ts`: listRecordings 添加 search 参数，修复 format 类型
- `web/src/routes/Recordings.svelte`: 删除 filteredRecordings，添加 pinned 筛选，修复 clearFilters，添加 AbortController

### Definition of Done
- [ ] `rtk go test ./internal/storage/... -v -run TestListRecordings` → 所有搜索测试通过
- [ ] `rtk go test ./internal/api/... -v -run TestListRecordings` → 所有搜索测试通过
- [ ] `cd web && rtk npm run build` → 构建成功，无 TypeScript 错误
- [ ] 搜索 `?search=` 返回正确过滤结果和 total 计数

### Must Have
- 搜索必须覆盖 camera_id, format, file_path 三个字段（OR 匹配）
- LIKE 通配符必须转义（防注入）
- ListRecordings 和 CountRecordingsWithFilter 的 WHERE 必须同步
- filteredRecordings 必须完全删除，所有引用改为 recordings
- clearFilters 必须重置为最近1小时（与初始状态一致）
- pinned 筛选器 UI（All/Pinned/Unpinned）
- AbortController 竞态保护

### Must NOT Have (Guardrails)
- 不要 JOIN cameras 表搜索摄像头名称
- 不要修改 API 响应格式（保持 `{recordings: [], total: number}`）
- 不要修改 Pagination.svelte 组件
- 不要添加前端测试框架（无 vitest/jest）
- 不要改变 $effect 的 100ms 防抖机制
- 不要添加共享 WHERE 构建器 — 保持两处独立更新
- 不要忘记转义 LIKE 通配符 `%` 和 `_`

---

## Verification Strategy (MANDATORY)

> **ZERO HUMAN INTERVENTION** - ALL verification is agent-executed. No exceptions.

### Test Decision
- **Infrastructure exists**: YES (Go testify)
- **Automated tests**: TDD
- **Framework**: Go testing + testify/require
- **TDD**: Each backend task follows RED (failing test) → GREEN (minimal impl) → REFACTOR

### QA Policy
Every task MUST include agent-executed QA scenarios.
Evidence saved to `.sisyphus/evidence/task-{N}-{scenario-slug}.{ext}`.

- **Backend API**: Use Bash (curl) — Send requests, assert status + response fields
- **Frontend**: Use Bash (npm run build) — Verify build succeeds, no TS errors
- **Go tests**: Use Bash (go test) — Run test suite, assert pass/fail

---

## Execution Strategy

### Parallel Execution Waves

```
Wave 1 (TDD - backend search):
└── Task 1: 后端搜索功能 (model + db + handler + tests) [deep]

Wave 2 (frontend - parallel after Task 1):
├── Task 2: 前端搜索 + 类型修复 + AbortController (depends: 1) [unspecified-high]
└── Task 3: 前端 pinned 筛选 + clearFilters 修复 (depends: 1) [quick]

Wave FINAL (After ALL tasks — 4 parallel reviews):
├── F1: Plan compliance audit (oracle)
├── F2: Code quality review (unspecified-high)
├── F3: Real QA (unspecified-high)
└── F4: Scope fidelity check (deep)
→ Present results → Get explicit user okay

Critical Path: Task 1 → Task 2 → F1-F4 → user okay
Parallel Speedup: Tasks 2+3 can run in parallel
Max Concurrent: 2 (Wave 2)
```

### Dependency Matrix

| Task | Blocked By | Blocks |
|------|-----------|--------|
| 1    | -         | 2, 3   |
| 2    | 1         | FINAL  |
| 3    | 1         | FINAL  |
| FINAL| 2, 3      | -      |

### Agent Dispatch Summary

- **Wave 1**: 1 task - T1 → `deep`
- **Wave 2**: 2 tasks - T2 → `unspecified-high`, T3 → `quick`
- **FINAL**: 4 tasks - F1 → `oracle`, F2 → `unspecified-high`, F3 → `unspecified-high`, F4 → `deep`

---

## TODOs

- [x] 1. 后端搜索功能（TDD）

  **What to do**:
  - **RED**: 先写失败测试
    - `internal/storage/db_test.go`: 添加 `TestListRecordings_SearchByCameraID`, `TestListRecordings_SearchByFormat`, `TestListRecordings_SearchByFilePath`, `TestListRecordings_SearchEmpty`, `TestListRecordings_SearchWithOtherFilters`, `TestListRecordings_SearchLikeWildcardEscape`
    - `internal/api/handler_test.go`: 添加 `TestListRecordings_SearchQuery`
  - **GREEN**: 最小实现让测试通过
    - `internal/model/types.go`: 在 `RecordingFilter` struct 中添加 `Search string` 字段
    - `internal/storage/db.go`: 在 `ListRecordings()` 和 `CountRecordingsWithFilter()` 中添加 Search LIKE WHERE 子句：
      ```go
      if filter.Search != "" {
          searchTerm := strings.ReplaceAll(filter.Search, "%", "\\%")
          searchTerm = strings.ReplaceAll(searchTerm, "_", "\\_")
          searchTerm = "%" + searchTerm + "%"
          where = append(where, "(camera_id LIKE ? ESCAPE '\\' OR format LIKE ? ESCAPE '\\' OR file_path LIKE ? ESCAPE '\\')")
          args = append(args, searchTerm, searchTerm, searchTerm)
      }
      ```
    - `internal/api/handler.go`: 在 `handleListRecordings` 中解析 `?search=` 参数并设置 `filter.Search`
  - **REFACTOR**: 确认代码风格与现有模式一致

  **Must NOT do**:
  - 不要 JOIN cameras 表
  - 不要修改 API 响应格式
  - 不要忘记更新 CountRecordingsWithFilter
  - 不要忘记转义 LIKE 通配符

  **Recommended Agent Profile**:
  - **Category**: `deep`
    - Reason: TDD 流程需要先写测试再实现，涉及数据库查询构建，需要仔细思考
  - **Skills**: []
  - **Skills Evaluated but Omitted**:
    - `git-master`: 不需要复杂 git 操作

  **Parallelization**:
  - **Can Run In Parallel**: NO
  - **Parallel Group**: Wave 1 (solo)
  - **Blocks**: Tasks 2, 3
  - **Blocked By**: None (can start immediately)

  **References** (CRITICAL):

  **Pattern References** (existing code to follow):
  - `internal/storage/db.go:241-298` — ListRecordings WHERE clause builder pattern. This is the EXACT pattern to follow for adding the Search LIKE clause
  - `internal/storage/db.go:300-325` — CountRecordingsWithFilter WHERE builder. MUST apply identical Search logic here too
  - `internal/api/handler.go:308-367` — handleListRecordings query param parsing pattern. Follow this exact pattern for parsing `?search=`
  - `internal/api/handler_test.go:223-289` — TestListRecordings_FilterByCameraID/Format/Pinned/TimeRange test patterns. Follow this exact structure for search tests
  - `internal/storage/db_test.go:85-103` — TestListRecordingsWithFilter test pattern. Follow this for DB-level search tests

  **API/Type References**:
  - `internal/model/types.go:69-79` — RecordingFilter struct. Add `Search string` field here
  - `internal/model/types.go:34-57` — Format constants (FormatH264, FormatMJPEG, FormatH265). These are the format values in DB

  **Acceptance Criteria**:

  **If TDD (tests enabled):**
  - [ ] Test file updated: internal/storage/db_test.go with 6+ new test functions
  - [ ] Test file updated: internal/api/handler_test.go with 1+ new test functions
  - [ ] `rtk go test ./internal/storage/... -v -run TestListRecordings` → PASS
  - [ ] `rtk go test ./internal/api/... -v -run TestListRecordings` → PASS

  **QA Scenarios (MANDATORY):**

  ```
  Scenario: Search by camera_id returns matching recordings
    Tool: Bash (go test)
    Preconditions: Test DB with seeded recordings
    Steps:
      1. Run: rtk go test ./internal/storage/... -v -run TestListRecordings_SearchByCameraID
      2. Assert: all tests PASS
    Expected Result: 0 failures
    Evidence: .sisyphus/evidence/task-1-search-camera-id.txt

  Scenario: Search escapes LIKE wildcards
    Tool: Bash (go test)
    Preconditions: Test DB with recording containing '%' in camera_id
    Steps:
      1. Run: rtk go test ./internal/storage/... -v -run TestListRecordings_SearchLikeWildcardEscape
      2. Assert: test PASS, searching for '%' returns only exact matches
    Expected Result: 0 failures
    Evidence: .sisyphus/evidence/task-1-search-escape.txt

  Scenario: Count and list return same results for search
    Tool: Bash (go test)
    Preconditions: Test DB with seeded recordings
    Steps:
      1. Run: rtk go test ./internal/storage/... -v -run TestListRecordings_SearchWithOtherFilters
      2. Assert: count matches list length when offset=0, limit=9999
    Expected Result: 0 failures
    Evidence: .sisyphus/evidence/task-1-search-count-consistency.txt
  ```

  **Commit**: YES
  - Message: `feat(storage): add server-side search filter for recordings (TDD)`
  - Files: `internal/model/types.go, internal/storage/db.go, internal/storage/db_test.go, internal/api/handler.go, internal/api/handler_test.go`
  - Pre-commit: `rtk go test ./internal/storage/... ./internal/api/... -v -run TestListRecordings`

- [x] 2. 前端搜索对接 + 类型修复 + AbortController

  **What to do**:
  - `web/src/lib/api.ts`:
    - 在 `Recording` interface 中将 `format: 'h264' | 'mjpeg'` 改为 `format: 'h264' | 'mjpeg' | 'h265'`
    - 在 `listRecordings` params 中添加 `search?: string`
    - 在 query params 构建中添加 `if (params.search) queryParams.set('search', params.search)`
  - `web/src/routes/Recordings.svelte`:
    - 将 `searchQuery` 添加到 `$effect` 依赖数组（第205行）：`const _ = [cameraId, format, startDate, endDate, offset, limit, sortBy, sortOrder, searchQuery]`
    - 在 `loadRecordings()` 中传入 `search: searchQuery || undefined`
    - 删除 `filteredRecordings` 的 `$derived` 定义（第80-86行）
    - 将所有 `filteredRecordings` 引用替换为 `recordings`（共4处：toggleSelectAll 第52/55行、checkbox 第353行、table each 第414行）
    - 添加 AbortController 竞态保护：在 loadRecordings 开头取消上一次请求
      ```typescript
      let abortController: AbortController | null = null;
      async function loadRecordings() {
        if (abortController) abortController.abort();
        abortController = new AbortController();
        // ... 在 fetch 中传入 signal
      }
      ```
      注意：apiRequest 使用 fetch，需要将 signal 传入。可能需要修改 apiRequest 签名或 loadRecordings 改用直接 fetch。

  **Must NOT do**:
  - 不要添加前端测试框架
  - 不要修改 Pagination.svelte
  - 不要改变 API 响应格式
  - 不要删除 searchQuery 状态变量（保留给输入框绑定）

  **Recommended Agent Profile**:
  - **Category**: `unspecified-high`
    - Reason: 涉及 Svelte 5 响应式系统（$effect/$derived）和 API 对接，需要仔细处理状态依赖
  - **Skills**: []
  - **Skills Evaluated but Omitted**:
    - `visual-engineering`: 无 UI 设计工作

  **Parallelization**:
  - **Can Run In Parallel**: YES (with Task 3)
  - **Parallel Group**: Wave 2 (with Task 3)
  - **Blocks**: FINAL
  - **Blocked By**: Task 1

  **References** (CRITICAL):

  **Pattern References**:
  - `web/src/routes/Recordings.svelte:80-86` — filteredRecordings $derived. THIS MUST BE DELETED entirely
  - `web/src/routes/Recordings.svelte:52,55` — toggleSelectAll uses filteredRecordings. Replace with recordings
  - `web/src/routes/Recordings.svelte:353` — checkbox checked state uses filteredRecordings.length. Replace with recordings.length
  - `web/src/routes/Recordings.svelte:414` — {#each filteredRecordings}. Replace with {#each recordings}
  - `web/src/routes/Recordings.svelte:203-209` — $effect with debounce. ADD searchQuery to dependency array
  - `web/src/routes/Recordings.svelte:106-128` — loadRecordings function. ADD search param here

  **API/Type References**:
  - `web/src/lib/api.ts:7-18` — Recording interface. Fix format type at line 11
  - `web/src/lib/api.ts:275-302` — listRecordings function. Add search param here

  **Acceptance Criteria**:
  - [ ] `cd web && rtk npm run build` → 成功，无 TypeScript 错误
  - [ ] 搜索框输入后触发服务器端请求（检查网络面板）
  - [ ] filteredRecordings 不再存在于代码中

  **QA Scenarios (MANDATORY):**

  ```
  Scenario: Frontend builds without TypeScript errors
    Tool: Bash (npm build)
    Preconditions: Task 1 backend changes are merged
    Steps:
      1. Run: cd web && rtk npm run build
      2. Assert: exit code 0, no TS errors in output
    Expected Result: Build succeeds
    Failure Indicators: TypeScript compilation errors, undefined type errors
    Evidence: .sisyphus/evidence/task-2-frontend-build.txt

  Scenario: Search query reaches server
    Tool: Bash (curl + grep)
    Preconditions: App is running (dev or built)
    Steps:
      1. Run: curl -s 'http://localhost:9090/api/recordings?search=test' | grep -q '"recordings"'
      2. Assert: response contains recordings array
    Expected Result: HTTP 200 with valid JSON
    Failure Indicators: 404, 500, or missing search param error
    Evidence: .sisyphus/evidence/task-2-search-endpoint.txt

  Scenario: No filteredRecordings references remain
    Tool: Bash (grep)
    Preconditions: Code changes applied
    Steps:
      1. Run: grep -r 'filteredRecordings' web/src/
      2. Assert: No matches found
    Expected Result: grep returns exit code 1 (no matches)
    Evidence: .sisyphus/evidence/task-2-no-client-filter.txt
  ```

  **Commit**: YES
  - Message: `feat(web): wire server-side search, fix types, add AbortController`
  - Files: `web/src/lib/api.ts, web/src/routes/Recordings.svelte`
  - Pre-commit: `cd web && rtk npm run build`

- [x] 3. 前端 pinned 筛选器 + clearFilters 修复

  **What to do**:
  - `web/src/routes/Recordings.svelte`:
    - 添加 `pinnedFilter` 状态变量：`let pinnedFilter = $state(''); // '' = all, 'true' = pinned, 'false' = unpinned`
    - 在过滤栏中添加 pinned 下拉选择器（在 format 选择器之后）：
      ```html
      <select id="pinned" class="input" bind:value={pinnedFilter}>
        <option value="">{t('recordings.allStatus')}</option>
        <option value="true">{t('recordings.pinnedOnly')}</option>
        <option value="false">{t('recordings.unpinnedOnly')}</option>
      </select>
      ```
    - 将 `pinnedFilter` 添加到 `$effect` 依赖数组
    - 在 `loadRecordings()` 中传入 `pinned: pinnedFilter === 'true' ? true : pinnedFilter === 'false' ? false : undefined`
    - 修复 `clearFilters()` 函数，重置为初始状态：
      ```typescript
      function clearFilters() {
        searchQuery = '';
        cameraId = '';
        format = '';
        pinnedFilter = '';
        startDate = toLocalDT(new Date(Date.now() - 3600000));
        endDate = toLocalDT(new Date());
      }
      ```
  - `web/src/lib/i18n/en.json`: 添加 recordings.allStatus, recordings.pinnedOnly, recordings.unpinnedOnly
  - `web/src/lib/i18n/zh.json`: 添加对应的中文翻译

  **Must NOT do**:
  - 不要修改后端（pinned 已支持）
  - 不要修改 Pagination.svelte
  - 不要添加新的 API 调用（复用 listRecordings 的 pinned 参数）

  **Recommended Agent Profile**:
  - **Category**: `quick`
    - Reason: 纯前端 UI 添加 + 简单函数修复，无复杂逻辑
  - **Skills**: []
  - **Skills Evaluated but Omitted**:
    - `visual-engineering`: 仅添加一个 select 元素，不需要设计工作

  **Parallelization**:
  - **Can Run In Parallel**: YES (with Task 2)
  - **Parallel Group**: Wave 2 (with Task 2)
  - **Blocks**: FINAL
  - **Blocked By**: Task 1

  **References** (CRITICAL):

  **Pattern References**:
  - `web/src/routes/Recordings.svelte:253-271` — 现有的 camera 和 format select 元素。按相同模式添加 pinned select
  - `web/src/routes/Recordings.svelte:167-173` — clearFilters 函数。THIS MUST BE FIXED to reset to initial defaults
  - `web/src/routes/Recordings.svelte:33-34` — startDate/endDate 初始值。clearFilters 应重置为这些值
  - `web/src/routes/Recordings.svelte:203-209` — $effect 依赖数组。ADD pinnedFilter here

  **API/Type References**:
  - `web/src/lib/api.ts:278` — listRecordings 已有 pinned?: boolean 参数定义。直接使用即可
  - `web/src/lib/i18n/en.json` — 添加 i18n keys 到此文件
  - `web/src/lib/i18n/zh.json` — 添加对应中文翻译到此文件

  **Acceptance Criteria**:
  - [ ] `cd web && rtk npm run build` → 成功
  - [ ] pinned 下拉框显示 3 个选项（All/Pinned/Unpinned）
  - [ ] clearFilters 重置后时间范围回到最近1小时

  **QA Scenarios (MANDATORY):**

  ```
  Scenario: Frontend builds with pinned filter
    Tool: Bash (npm build)
    Preconditions: Code changes applied
    Steps:
      1. Run: cd web && rtk npm run build
      2. Assert: exit code 0
    Expected Result: Build succeeds
    Evidence: .sisyphus/evidence/task-3-pinned-build.txt

  Scenario: clearFilters resets to initial state
    Tool: Bash (grep)
    Preconditions: Code changes applied
    Steps:
      1. Grep clearFilters in Recordings.svelte
      2. Verify startDate reset uses toLocalDT(new Date(Date.now() - 3600000))
      3. Verify endDate reset uses toLocalDT(new Date())
    Expected Result: clearFilters resets dates to last 1 hour, not empty strings
    Evidence: .sisyphus/evidence/task-3-clear-filters.txt

  Scenario: pinned filter wired to API
    Tool: Bash (curl)
    Preconditions: App is running
    Steps:
      1. Run: curl -s 'http://localhost:9090/api/recordings?pinned=true' | grep -q '"recordings"'
      2. Assert: returns valid JSON
    Expected Result: HTTP 200
    Evidence: .sisyphus/evidence/task-3-pinned-api.txt
  ```

  **Commit**: YES
  - Message: `feat(web): add pinned filter UI and fix clearFilters`
  - Files: `web/src/routes/Recordings.svelte, web/src/lib/i18n/en.json, web/src/lib/i18n/zh.json`
  - Pre-commit: `cd web && rtk npm run build`

## Final Verification Wave (MANDATORY — after ALL implementation tasks)

- [x] F1. **Plan Compliance Audit** — `oracle`
  Read the plan end-to-end. For each "Must Have": verify implementation exists (read file, curl endpoint, run command). For each "Must NOT Have": search codebase for forbidden patterns — reject with file:line if found. Check evidence files exist in .sisyphus/evidence/. Compare deliverables against plan.
  Output: `Must Have [N/N] | Must NOT Have [N/N] | Tasks [N/N] | VERDICT: APPROVE/REJECT`

- [x] F2. **Code Quality Review** — `unspecified-high`
  Run `rtk go vet ./...` + `rtk go test ./... -v` + `cd web && rtk npm run build`. Review all changed files for: `as any`/`@ts-ignore`, empty catches, console.log in prod, commented-out code, unused imports. Check AI slop: excessive comments, over-abstraction, generic names.
  Output: `Build [PASS/FAIL] | Lint [PASS/FAIL] | Tests [N pass/N fail] | Files [N clean/N issues] | VERDICT`

- [x] F3. **Real Manual QA** — `unspecified-high`
  Start from clean state. Execute EVERY QA scenario from EVERY task — follow exact steps, capture evidence. Test cross-task integration: search + pinned filter, search + date range, search + format filter. Save to `.sisyphus/evidence/final-qa/`.
  Output: `Scenarios [N/N pass] | Integration [N/N] | Edge Cases [N tested] | VERDICT`

- [x] F4. **Scope Fidelity Check** — `oracle`
  For each task: read "What to do", read actual diff (git log/diff). Verify 1:1 — everything in spec was built (no missing), nothing beyond spec was built (no creep). Check "Must NOT do" compliance. Detect cross-task contamination: Task N touching Task M's files. Flag unaccounted changes.
  Output: `Tasks [N/N compliant] | Contamination [CLEAN/N issues] | Unaccounted [CLEAN/N files] | VERDICT`

---

## Commit Strategy

- **Task 1**: `feat(storage): add server-side search filter for recordings (TDD)` - internal/model/types.go, internal/storage/db.go, internal/storage/db_test.go, internal/api/handler.go, internal/api/handler_test.go
  - Pre-commit: `rtk go test ./internal/storage/... ./internal/api/... -v -run TestListRecordings`
- **Task 2**: `feat(web): wire server-side search, fix types, add AbortController` - web/src/lib/api.ts, web/src/routes/Recordings.svelte
  - Pre-commit: `cd web && rtk npm run build`
- **Task 3**: `feat(web): add pinned filter UI and fix clearFilters` - web/src/routes/Recordings.svelte, web/src/lib/i18n/en.json, web/src/lib/i18n/zh.json
  - Pre-commit: `cd web && rtk npm run build`

---

## Success Criteria

### Verification Commands
```bash
rtk go test ./internal/storage/... -v -run TestListRecordings  # Expected: PASS, all search tests
rtk go test ./internal/api/... -v -run TestListRecordings       # Expected: PASS, all search tests
cd web && rtk npm run build                                      # Expected: success, no TS errors
rtk go vet ./...                                                  # Expected: no warnings
```

### Final Checklist
- [ ] All "Must Have" present
- [ ] All "Must NOT Have" absent
- [ ] All tests pass
- [ ] Frontend builds without errors
