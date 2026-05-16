# 前端组件集成修复

## TL;DR

> **目标**: 将已编写但未集成的 Header.svelte（含 ThemeToggle + LanguageSwitcher + 汉堡菜单）接入 App.svelte，删除各路由页面中重复的内联 header，修复 Svelte 5 语法问题，部署到 RPi 验证。
>
> **交付物**:
> - 共享导航栏在所有非 Login 页面显示
> - 主题切换（暗/亮）按钮在 UI 中可见且可用
> - 语言切换（中/英）在所有页面（含 Login）可用
> - 移动端汉堡菜单在 ≤767px 视口下可用
> - Login 页面有独立的主题/语言切换（不带完整 Header）
>
> **工作量估算**: Medium
> **并行执行**: YES - 3 waves
> **关键路径**: T1(语言切换器修复) → T2(App.svelte 集成) → T3-T7(删除内联 header) → T8(Login 添加控件) → T9(构建部署验证)

---

## Context

### 原始问题
上一轮计划 T9-T12 标记完成但实际未完成：组件文件存在（Header.svelte 373行、ThemeToggle.svelte、LanguageSwitcher.svelte），但从未被 App.svelte 或任何路由页面 import。每个路由页面使用各自复制的内联 `<header>`。

### Metis 审查要点
- **不传 `activeRoute` prop**: Header.svelte 自带 `hashchange` 监听器同步路由（lines 52-65），prop 是冗余的
- **必须加 `pt-[68px]`**: Header 使用 `position: fixed; height: 68px`，页面内容会被遮挡
- **LanguageSwitcher 有语法错误**: 多余的 `>` 在第 24-25 行
- **Svelte 4→5 迁移范围**: 仅修 LanguageSwitcher 的 `on:change`，不动其他文件的 `on:click` 等（另案处理）

---

## Work Objectives

### 核心目标
将 Header.svelte 接入 App.svelte，使 ThemeToggle / LanguageSwitcher / 汉堡菜单在 UI 中可见可用。

### 具体交付物
- App.svelte 中非 Login 路由显示共享 Header 组件
- 5 个路由页面删除各自的内联 header（消除代码重复）
- Login 页面有独立的主题/语言切换按钮
- LanguageSwitcher 修复为 Svelte 5 语法
- 前端构建 + 部署 + 浏览器验证

### Must Have
- ThemeToggle 按钮在 UI 中可见，点击可切换暗/亮主题
- LanguageSwitcher 在所有页面（含 Login）可用
- 汉堡菜单在 ≤767px 视口下可见
- 所有页面内容不被 fixed header 遮挡（`pt-[68px]`）
- `npm run build` 零错误
- 部署到 RPi 后浏览器验证通过

### Must NOT Have (Guardrails)
- ❌ 不修改 Header.svelte 的内部逻辑（hashchange 监听、主题应用、导航项）— 它已经正确
- ❌ 不传 `activeRoute` prop 给 Header — 它自己同步
- ❌ 不在本次任务中迁移其他文件的 Svelte 4 `on:*` 语法
- ❌ 不新增 npm 依赖
- ❌ 不修改后端代码
- ❌ 不修改 Pagination.svelte、Toast.svelte 或其他未涉及组件

---

## Verification Strategy

> **ZERO HUMAN INTERVENTION** — ALL verification is agent-executed.

### Test Decision
- **Infrastructure exists**: NO (frontend only, no test framework)
- **Automated tests**: None
- **Framework**: N/A

### QA Policy
- **Frontend/UI**: Build 验证（`npm run build` 零错误）+ 部署后 API 检查
- **API/Backend**: curl 验证静态文件可访问 + API 正常
- **Evidence**: 保存到 `.sisyphus/evidence/`

---

## Execution Strategy

### Parallel Execution Waves

```
Wave 1 (Start Immediately — 组件修复):
├── Task 1: 修复 LanguageSwitcher Svelte 5 语法 [quick]

Wave 2 (After Wave 1 — App 集成 + 清理内联 header):
├── Task 2: App.svelte 集成 Header 组件 [quick]
├── Task 3: Recordings.svelte 删除内联 header [quick]
├── Task 4: Cameras.svelte 删除内联 header [quick]
├── Task 5: Stats.svelte 删除内联 header [quick]
├── Task 6: Settings.svelte 删除内联 header [quick]
├── Task 7: RecordingDetail.svelte 删除内联 header [quick]
├── Task 8: Login.svelte 添加主题/语言切换 [quick]

Wave 3 (After Wave 2 — 构建部署验证):
├── Task 9: 构建前端 + 部署到 RPi + 端到端验证 [unspecified-high]
```

### Dependency Matrix

| Task | Blocked By | Blocks |
|------|-----------|--------|
| T1 | - | T2 |
| T2 | T1 | T3-T9 |
| T3 | T2 | T9 |
| T4 | T2 | T9 |
| T5 | T2 | T9 |
| T6 | T2 | T9 |
| T7 | T2 | T9 |
| T8 | T2 | T9 |
| T9 | T3-T8 | - |

### Agent Dispatch Summary
- **Wave 1**: 1 task — T1 → `quick`
- **Wave 2**: 7 tasks — T2-T8 → `quick`
- **Wave 3**: 1 task — T9 → `unspecified-high`

---

## TODOs

- [x] 1. **修复 LanguageSwitcher Svelte 5 语法**

  **What to do**:
  - 修复 `web/src/components/LanguageSwitcher.svelte`:
    - Line 23: `on:change={handleChange}` → `onchange={handleChange}` (Svelte 5 event handler syntax)
    - Line 24-25: 删除多余的 `>` (stray closing bracket)
  - 验证修复后组件能正常工作

  **Must NOT do**:
  - 不修改其他文件中的 `on:click` 等 Svelte 4 语法（另案处理）

  **Recommended Agent Profile**:
  - **Category**: `quick`
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: NO (blocks T2)
  - **Parallel Group**: Wave 1
  - **Blocks**: T2
  - **Blocked By**: None

  **References**:
  - `web/src/components/LanguageSwitcher.svelte` — 全文件 28 行，需要修复 line 23 (`on:change` → `onchange`) 和 line 24-25 (多余的 `>`)
  - `web/src/lib/i18n/index.ts` — 理解 `setLang()` 函数签名，确保 handleChange 调用正确

  **Acceptance Criteria**:
  - [ ] `grep 'on:change' web/src/components/LanguageSwitcher.svelte` 返回空
  - [ ] `grep 'onchange' web/src/components/LanguageSwitcher.svelte` 返回匹配
  - [ ] `cd web && npm run build` 零错误

  **QA Scenarios:**
  ```
  Scenario: LanguageSwitcher uses Svelte 5 syntax
    Tool: Bash
    Steps:
      1. grep 'on:change' web/src/components/LanguageSwitcher.svelte
      2. grep 'onchange' web/src/components/LanguageSwitcher.svelte
      3. cd web && npm run build
    Expected Result: No Svelte 4 event directives, build succeeds
    Evidence: .sisyphus/evidence/task-1-langswitch-fix.txt
  ```

  **Commit**: NO (groups with final commit)

---

- [x] 2. **App.svelte 集成 Header 组件**

  **What to do**:
  - 在 `web/src/App.svelte` 中:
    1. 添加 `import Header from './components/Header.svelte';`
    2. 在 `{#if currentRoute === 'login'}` 之前（即非 login 分支中），添加 `<Header showBack={currentRoute === 'recording-detail'} />`
    3. 具体结构：Login 独立渲染，其余所有路由渲染 Header + 路由组件
  - 最终 App.svelte 模板结构:
    ```svelte
    {#if currentRoute === 'login'}
      <Login />
    {:else}
      <Header showBack={currentRoute === 'recording-detail'} />
      {#if currentRoute === 'recordings'}
        <Recordings />
      {:else if currentRoute === 'recording-detail'}
        <RecordingDetail recordingId={params.id} />
      {:else if currentRoute === 'cameras'}
        <Cameras />
      {:else if currentRoute === 'stats'}
        <Stats />
      {:else if currentRoute === 'settings'}
        <Settings />
      {/if}
    {/if}
    ```
  - 注意：不传 `activeRoute` prop — Header 自带 hashchange 监听器同步路由

  **Must NOT do**:
  - 不传 `activeRoute` prop（Header 自行同步）
  - 不修改 Header.svelte 内部逻辑
  - 不修改路由解析逻辑

  **Recommended Agent Profile**:
  - **Category**: `quick`
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: NO
  - **Parallel Group**: Wave 2 (first task)
  - **Blocks**: T3-T9
  - **Blocked By**: T1

  **References**:
  - `web/src/App.svelte` (85 行) — 当前 hash 路由器，需要在非 login 分支添加 Header
  - `web/src/components/Header.svelte` — 完整导航组件，接受 `showBack?: boolean` 和 `backLabel?: string` props
  - `web/src/components/Header.svelte:11-19` — `$props()` 定义，只需 `showBack`

  **Acceptance Criteria**:
  - [ ] `grep 'import Header' web/src/App.svelte` 返回匹配
  - [ ] `grep '<Header' web/src/App.svelte` 返回匹配
  - [ ] `cd web && npm run build` 零错误

  **QA Scenarios:**
  ```
  Scenario: Header imported and rendered in App.svelte
    Tool: Bash
    Steps:
      1. grep 'import Header' web/src/App.svelte
      2. grep '<Header' web/src/App.svelte
      3. cd web && npm run build
    Expected Result: Import and usage found, build succeeds
    Evidence: .sisyphus/evidence/task-2-app-header.txt
  ```

  **Commit**: NO (groups with final commit)

---

- [x] 3. **Recordings.svelte 删除内联 header**

  **What to do**:
  - 在 `web/src/routes/Recordings.svelte` 中:
    1. 删除 import 中的 `logout` (line 9)
    2. 删除 `import LanguageSwitcher from '../components/LanguageSwitcher.svelte';` (line 13)
    3. 删除 lang 变量声明和 onLangChange 订阅 (lines 19-22) — header 删除后无使用
    4. 删除内联 `<header>` 块 (lines 137-157)
    5. 给 `<div class="min-h-screen th-bg-primary">` 添加 `pt-[68px]` 补偿 fixed header

  **Must NOT do**:
  - 不修改 Recordings 的功能逻辑（筛选、分页、删除等）

  **Recommended Agent Profile**:
  - **Category**: `quick`
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES (with T4-T8)
  - **Parallel Group**: Wave 2
  - **Blocks**: T9
  - **Blocked By**: T2

  **References**:
  - `web/src/routes/Recordings.svelte:1-16` — imports 区域，需要删除 logout、LanguageSwitcher、lang/onLangChange
  - `web/src/routes/Recordings.svelte:135-160` — `<div>` 包裹 + `<header>` 块 + `<main>` 起始
  - `web/src/components/Header.svelte:167-173` — Header 使用 `position: fixed; height: 68px`，需要 pt-[68px] 补偿

  **Acceptance Criteria**:
  - [ ] `grep '<header' web/src/routes/Recordings.svelte` 返回空
  - [ ] `grep 'LanguageSwitcher' web/src/routes/Recordings.svelte` 返回空
  - [ ] `grep 'logout' web/src/routes/Recordings.svelte` 返回空
  - [ ] `grep 'pt-\[68px\]' web/src/routes/Recordings.svelte` 返回匹配

  **QA Scenarios:**
  ```
  Scenario: Inline header removed from Recordings
    Tool: Bash
    Steps:
      1. grep '<header' web/src/routes/Recordings.svelte
      2. grep 'LanguageSwitcher' web/src/routes/Recordings.svelte
      3. grep 'pt-\[68px\]' web/src/routes/Recordings.svelte
      4. cd web && npm run build
    Expected Result: No inline header, no LanguageSwitcher import, has pt-[68px], build succeeds
    Evidence: .sisyphus/evidence/task-3-recordings-cleanup.txt
  ```

  **Commit**: NO (groups with final commit)

---

- [x] 4. **Cameras.svelte 删除内联 header**

  **What to do**:
  - 在 `web/src/routes/Cameras.svelte` 中:
    1. 删除 import 中的 `logout` → `import { listCameras, createCamera, updateCamera, deleteCamera } from '$lib/api';`
    2. 删除 `import LanguageSwitcher from '../components/LanguageSwitcher.svelte';` (line 5)
    3. 删除 lang 变量声明和 onLangChange 订阅 (lines 8-10) — header 删除后无使用
    4. 删除内联 `<header>` 块 (lines 145-167)
    5. 给 `<div class="min-h-screen th-bg-primary">` 添加 `pt-[68px]`

  **Must NOT do**:
  - 不修改 Cameras 的功能逻辑（CRUD 操作等）

  **Recommended Agent Profile**:
  - **Category**: `quick`
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES (with T3, T5-T8)
  - **Parallel Group**: Wave 2
  - **Blocks**: T9
  - **Blocked By**: T2

  **References**:
  - `web/src/routes/Cameras.svelte:1-7` — imports 区域
  - `web/src/routes/Cameras.svelte:143-170` — `<div>` + `<header>` + `<main>`

  **Acceptance Criteria**:
  - [ ] `grep '<header' web/src/routes/Cameras.svelte` 返回空
  - [ ] `grep 'LanguageSwitcher' web/src/routes/Cameras.svelte` 返回空
  - [ ] `grep 'logout' web/src/routes/Cameras.svelte` 返回空
  - [ ] `grep 'pt-\[68px\]' web/src/routes/Cameras.svelte` 返回匹配

  **QA Scenarios:**
  ```
  Scenario: Inline header removed from Cameras
    Tool: Bash
    Steps:
      1. grep '<header' web/src/routes/Cameras.svelte
      2. grep 'LanguageSwitcher' web/src/routes/Cameras.svelte
      3. grep 'pt-\[68px\]' web/src/routes/Cameras.svelte
      4. cd web && npm run build
    Expected Result: No inline header, no LanguageSwitcher import, has pt-[68px], build succeeds
    Evidence: .sisyphus/evidence/task-4-cameras-cleanup.txt
  ```

  **Commit**: NO (groups with final commit)

---

- [x] 5. **Stats.svelte 删除内联 header**

  **What to do**:
  - 在 `web/src/routes/Stats.svelte` 中:
    1. 删除 import 中的 `logout` → `import { getStats, listCameras } from '$lib/api';`
    2. 删除 `import LanguageSwitcher from '../components/LanguageSwitcher.svelte';` (line 5)
    3. 删除 lang 变量声明和 onLangChange 订阅 (lines 9-11) — header 删除后无使用
    4. 删除内联 `<header>` 块 (lines 76-98)
    5. 给 `<div class="min-h-screen th-bg-primary">` 添加 `pt-[68px]`

  **Must NOT do**:
  - 不修改 Stats 的功能逻辑

  **Recommended Agent Profile**:
  - **Category**: `quick`
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES (with T3-T4, T6-T8)
  - **Parallel Group**: Wave 2
  - **Blocks**: T9
  - **Blocked By**: T2

  **References**:
  - `web/src/routes/Stats.svelte:1-7` — imports 区域
  - `web/src/routes/Stats.svelte:74-101` — `<div>` + `<header>` + `<main>`

  **Acceptance Criteria**:
  - [ ] `grep '<header' web/src/routes/Stats.svelte` 返回空
  - [ ] `grep 'LanguageSwitcher' web/src/routes/Stats.svelte` 返回空
  - [ ] `grep 'logout' web/src/routes/Stats.svelte` 返回空
  - [ ] `grep 'pt-\[68px\]' web/src/routes/Stats.svelte` 返回匹配

  **QA Scenarios:**
  ```
  Scenario: Inline header removed from Stats
    Tool: Bash
    Steps:
      1. grep '<header' web/src/routes/Stats.svelte
      2. grep 'LanguageSwitcher' web/src/routes/Stats.svelte
      3. grep 'pt-\[68px\]' web/src/routes/Stats.svelte
      4. cd web && npm run build
    Expected Result: No inline header, no LanguageSwitcher import, has pt-[68px], build succeeds
    Evidence: .sisyphus/evidence/task-5-stats-cleanup.txt
  ```

  **Commit**: NO (groups with final commit)

---

- [x] 6. **Settings.svelte 删除内联 header**

  **What to do**:
  - 在 `web/src/routes/Settings.svelte` 中:
    1. 删除 import 中的 `logout` → `import { getSettings, updateSettings } from '$lib/api';`
    2. 删除 `import LanguageSwitcher from '../components/LanguageSwitcher.svelte';` (line 5)
    3. 删除 lang 变量声明和 onLangChange 订阅 (lines 9-11) — header 删除后无使用
    4. 删除内联 `<header>` 块 (lines 92-115)
    5. 给 `<div class="min-h-screen th-bg-primary">` 添加 `pt-[68px]`

  **Must NOT do**:
  - 不修改 Settings 的功能逻辑

  **Recommended Agent Profile**:
  - **Category**: `quick`
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES (with T3-T5, T7-T8)
  - **Parallel Group**: Wave 2
  - **Blocks**: T9
  - **Blocked By**: T2

  **References**:
  - `web/src/routes/Settings.svelte:1-7` — imports 区域
  - `web/src/routes/Settings.svelte:90-118` — `<div>` + `<header>` + `<main>`

  **Acceptance Criteria**:
  - [ ] `grep '<header' web/src/routes/Settings.svelte` 返回空
  - [ ] `grep 'LanguageSwitcher' web/src/routes/Settings.svelte` 返回空
  - [ ] `grep 'logout' web/src/routes/Settings.svelte` 返回空
  - [ ] `grep 'pt-\[68px\]' web/src/routes/Settings.svelte` 返回匹配

  **QA Scenarios:**
  ```
  Scenario: Inline header removed from Settings
    Tool: Bash
    Steps:
      1. grep '<header' web/src/routes/Settings.svelte
      2. grep 'LanguageSwitcher' web/src/routes/Settings.svelte
      3. grep 'pt-\[68px\]' web/src/routes/Settings.svelte
      4. cd web && npm run build
    Expected Result: No inline header, no LanguageSwitcher import, has pt-[68px], build succeeds
    Evidence: .sisyphus/evidence/task-6-settings-cleanup.txt
  ```

  **Commit**: NO (groups with final commit)

---

- [x] 7. **RecordingDetail.svelte 删除内联 header**

  **What to do**:
  - 在 `web/src/routes/RecordingDetail.svelte` 中:
    1. 删除 `import LanguageSwitcher from '../components/LanguageSwitcher.svelte';` (line 14)
    2. 删除 lang 变量声明和 onLangChange 订阅 (lines 19-21) — header 删除后无使用
    3. 删除内联 `<header>` 块 (lines 247-272) — 包含 back button + nav + LanguageSwitcher
    4. 给 `<div class="min-h-screen th-bg-primary">` (line 245) 添加 `pt-[68px]`
    5. **保留** `goBack()` 函数 (line 208-210) — error 状态页面仍在使用 (line 285)
  - 注意：Header 的 `showBack={true}` (由 App.svelte 传入) 提供导航栏的返回按钮，页面内的 goBack 是 error 兜底

  **Must NOT do**:
  - 不删除 `goBack()` 函数 — error 状态的返回按钮仍在使用
  - 不修改视频播放、帧查看等功能逻辑

  **Recommended Agent Profile**:
  - **Category**: `quick`
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES (with T3-T6, T8)
  - **Parallel Group**: Wave 2
  - **Blocks**: T9
  - **Blocked By**: T2

  **References**:
  - `web/src/routes/RecordingDetail.svelte:1-16` — imports 区域
  - `web/src/routes/RecordingDetail.svelte:208-210` — `goBack()` 函数，保留！error 状态用
  - `web/src/routes/RecordingDetail.svelte:245-275` — `<div>` + `<header>` + `<main>`
  - `web/src/routes/RecordingDetail.svelte:282-288` — error 状态中的 goBack 按钮，保留

  **Acceptance Criteria**:
  - [ ] `grep '<header' web/src/routes/RecordingDetail.svelte` 返回空
  - [ ] `grep 'LanguageSwitcher' web/src/routes/RecordingDetail.svelte` 返回空
  - [ ] `grep 'pt-\[68px\]' web/src/routes/RecordingDetail.svelte` 返回匹配
  - [ ] `grep 'function goBack' web/src/routes/RecordingDetail.svelte` 返回匹配 (保留)

  **QA Scenarios:**
  ```
  Scenario: Inline header removed, goBack preserved
    Tool: Bash
    Steps:
      1. grep '<header' web/src/routes/RecordingDetail.svelte
      2. grep 'LanguageSwitcher' web/src/routes/RecordingDetail.svelte
      3. grep 'pt-\[68px\]' web/src/routes/RecordingDetail.svelte
      4. grep 'function goBack' web/src/routes/RecordingDetail.svelte
      5. cd web && npm run build
    Expected Result: No inline header, no LanguageSwitcher import, has pt-[68px], goBack preserved, build succeeds
    Evidence: .sisyphus/evidence/task-7-detail-cleanup.txt
  ```

  **Commit**: NO (groups with final commit)

---

- [x] 8. **Login.svelte 添加主题/语言切换**

  **What to do**:
  - 在 `web/src/routes/Login.svelte` 中:
    1. 添加 `import ThemeToggle from '../components/ThemeToggle.svelte';`
    2. 添加 `import LanguageSwitcher from '../components/LanguageSwitcher.svelte';`
    3. 在登录卡片**外部**、页面**右上角**放置 ThemeToggle + LanguageSwitcher:
       在最外层 `<div>` (line 44) 的**开始标签后**、card `<div>` 之前，添加:
       ```svelte
       <div class="fixed top-4 right-4 flex items-center gap-2 z-50">
         <ThemeToggle />
         <LanguageSwitcher />
       </div>
       ```
  - 不要修改 Login 的表单逻辑

  **Must NOT do**:
  - 不在 Login 页面显示完整 Header（只有主题+语言切换）
  - 不修改 `on:submit|preventDefault` 或 `on:keydown` (Svelte 4 compat, 正常工作)

  **Recommended Agent Profile**:
  - **Category**: `quick`
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES (with T3-T7)
  - **Parallel Group**: Wave 2
  - **Blocks**: T9
  - **Blocked By**: T2

  **References**:
  - `web/src/routes/Login.svelte:1-4` — imports 区域
  - `web/src/routes/Login.svelte:44-45` — 外层 div 和 card div，控件放在这之间
  - `web/src/components/ThemeToggle.svelte` — 主题切换按钮组件
  - `web/src/components/LanguageSwitcher.svelte` — 语言切换组件（T1 修复后）

  **Acceptance Criteria**:
  - [ ] `grep 'ThemeToggle' web/src/routes/Login.svelte` 返回匹配
  - [ ] `grep 'LanguageSwitcher' web/src/routes/Login.svelte` 返回匹配
  - [ ] `cd web && npm run build` 零错误

  **QA Scenarios:**
  ```
  Scenario: Login page has theme and language controls
    Tool: Bash
    Steps:
      1. grep 'ThemeToggle' web/src/routes/Login.svelte
      2. grep 'LanguageSwitcher' web/src/routes/Login.svelte
      3. cd web && npm run build
    Expected Result: Both imports found, build succeeds
    Evidence: .sisyphus/evidence/task-8-login-controls.txt
  ```

  **Commit**: NO (groups with final commit)

## Final Verification Wave


- [x] 9. **构建前端 + 部署到 RPi + 端到端验证**

  **What to do**:
  1. 构建前端: `cd web && npm run build`
  2. 复制 dist 到 embedded static: `cp -r web/dist/* internal/ui/static/`
  3. 构建并部署: `make deploy`
  4. 运行部署检查: `make deploy-check`
  5. 端到端验证:
     - `curl -s -o /dev/null -w "%{http_code}" http://192.168.63.31:9090/` → 200
     - `curl -sf http://192.168.63.31:9090/ | grep -c 'navbar'` → ≥1 (Header 组件存在)
     - `curl -s -o /dev/null -w "%{http_code}" -u admin:admin http://192.168.63.31:9090/api/health` → 200
     - `curl -s -o /dev/null -w "%{http_code}" http://192.168.63.31:9090/api/health` → 401 (API 受保护)
     - `go test ./...` → 全部通过
  6. 验证无残留内联 header: `grep -rn '<header' web/src/routes/ --include="*.svelte"` → 0 匹配
  7. 验证 LanguageSwitcher 未被路由页面 import: `grep -rn 'import.*LanguageSwitcher' web/src/routes/ --include="*.svelte"` → 0 匹配

  **Must NOT do**:
  - 不修改后端代码
  - 不修改 Go 测试

  **Recommended Agent Profile**:
  - **Category**: `unspecified-high`
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: NO
  - **Parallel Group**: Wave 3 (final)
  - **Blocks**: None
  - **Blocked By**: T3-T8

  **References**:
  - `Makefile` — `deploy` target: cross-compile → scp → ssh restart
  - `Makefile` — `deploy-check` target: verify service active + health

  **Acceptance Criteria**:
  - [ ] `cd web && npm run build` 零错误
  - [ ] `make deploy` 成功
  - [ ] `make deploy-check` 通过
  - [ ] `curl -s -o /dev/null -w "%{http_code}" http://192.168.63.31:9090/` → 200
  - [ ] `curl -sf http://192.168.63.31:9090/ | grep -c 'navbar'` → ≥1
  - [ ] `curl -s -o /dev/null -w "%{http_code}" http://192.168.63.31:9090/api/health` → 401
  - [ ] `go test ./...` 全部通过
  - [ ] `grep -rn '<header' web/src/routes/ --include="*.svelte"` → 0 匹配

  **QA Scenarios:**
  ```
  Scenario: Full deploy and E2E verification
    Tool: Bash
    Steps:
      1. cd web && npm run build
      2. cp -r web/dist/* internal/ui/static/
      3. make deploy
      4. sleep 3
      5. curl -s -o /dev/null -w "%{http_code}" http://192.168.63.31:9090/
      6. curl -sf http://192.168.63.31:9090/ | grep -c 'navbar'
      7. curl -s -o /dev/null -w "%{http_code}" -u admin:admin http://192.168.63.31:9090/api/health
      8. curl -s -o /dev/null -w "%{http_code}" http://192.168.63.31:9090/api/health
      9. grep -rn '<header' web/src/routes/ --include="*.svelte"
     10. grep -rn 'import.*LanguageSwitcher' web/src/routes/ --include="*.svelte"
     11. go test ./...
    Expected Result: Build succeeds, deploy succeeds, all HTTP checks pass, no inline headers remain, all tests pass
    Evidence: .sisyphus/evidence/task-9-deploy-e2e.txt

  Scenario: Theme toggle visible in SPA bundle
    Tool: Bash
    Steps:
      1. curl -sf http://192.168.63.31:9090/ | grep -c 'ThemeToggle\|theme-toggle\|data-theme'
    Expected Result: ≥1 (theme system is embedded in the SPA)
    Evidence: .sisyphus/evidence/task-9-theme-in-spa.txt
  ```

  **Commit**: YES
  - Message: `fix(ui): integrate Header component, enable theme/language switching`
  - Files: `web/src/App.svelte`, `web/src/routes/*.svelte`, `web/src/components/LanguageSwitcher.svelte`
  - Pre-commit: `cd web && npm run build`

> 无独立 verification wave — T9 已包含端到端验证。

---

## Commit Strategy

- **Single commit**: `fix(ui): integrate Header component, enable theme/language switching`
- **Files**: `web/src/App.svelte`, `web/src/routes/*.svelte`, `web/src/components/LanguageSwitcher.svelte`
- **Pre-commit**: `cd web && npm run build`

---

## Success Criteria

### Verification Commands
```bash
cd web && npm run build                                    # Expected: exit 0, no errors
grep -c "import Header" web/src/App.svelte                  # Expected: 1
grep -rn "import.*LanguageSwitcher" web/src/routes/ --include="*.svelte"  # Expected: 0 (moved to Header)
grep -c "navbar glass" web/src/components/Header.svelte     # Expected: ≥1
make deploy && make deploy-check                            # Expected: service active, health ok
```

### Final Checklist
- [x] ThemeToggle 按钮在 UI 中可见且可切换暗/亮主题
- [x] LanguageSwitcher 在所有页面（含 Login）可用
- [x] 汉堡菜单在 ≤767px 视口下可见
- [x] 所有页面内容不被 fixed header 遮挡
- [x] 无重复 header（旧的 inline header 全部删除）
- [x] RecordingDetail 显示返回按钮
- [x] Login 页面不显示完整 Header（只有主题+语言切换）
- [x] npm run build 零错误
- [x] 部署到 RPi 后所有页面正常
