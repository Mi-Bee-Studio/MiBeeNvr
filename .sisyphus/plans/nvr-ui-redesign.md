# NVR Web UI — Bug Fix & Purple Tech Redesign

## TL;DR

> **Quick Summary**: Fix all 20 identified bugs (6 critical, 3 high, 6 medium, 5 low) and redesign the entire MiBee NVR web UI from the current slate/cyan dark theme to a dual-theme (dark + light) purple tech aesthetic matching mlsbs.top's design system.
>
> **Deliverables**:
> - All 20 bugs fixed (language switching, settings persistence, auto-refresh, HTML structure, etc.)
> - Dual-theme system (dark + light) with CSS variables
> - Shared Header component extracted from all 6 pages
> - Complete visual redesign of all pages (Login, Recordings, RecordingDetail, Cameras, Stats, Settings)
> - Toast notification system
> - Theme toggle (dark/light)
> - Backend fix for settings persistence
>
> **Estimated Effort**: Large
> **Parallel Execution**: YES - 5 waves
> **Critical Path**: Task 1 (CSS theme system) → Task 7 (shared header) → Tasks 8-12 (page redesigns) → Task 13 (backend fix) → Wave 5 (integration & QA)

---

## Context

### Original Request
用户要求对部署在 192.168.63.31:9090 的 MiBee NVR Web UI 进行全面功能测试、bug修复和UI重设计。参考 mlsbs.top 的紫色科技风，同时实现深色和浅色两个主题。用户已发现中英文切换失效、每页条数保存无显示等问题。

### Interview Summary
**Key Discussions**:
- Login credentials: admin/admin
- UI改造范围: 全部页面重设计
- 风格偏好: 参考 mlsbs.top 的紫色科技风，需要深色+浅色双主题
- 通过代码审查发现20个bug（6 critical, 3 high, 6 medium, 5 low）

**Research Findings**:
- 前端: Svelte 5 + Vite 8 + TailwindCSS 4 (无config文件，用@tailwindcss/vite插件)
- 后端: Go + Chi + SQLite + BasicAuth，PUT /api/settings 不持久化
- i18n: 自定义callback-based系统，LanguageSwitcher中setLang未import
- 参考UI: 主紫色 #8b5cf6, 次紫色 #a78bfa, 强调蓝 #38bdf8, 毛玻璃效果

### Metis Review
**Identified Gaps** (addressed):
- 需要确认theme persistence策略 → localStorage
- 需要确认是否需要icon library → 使用内联SVG，不引入额外依赖
- 需要确认移动端响应式 → 当前不做特殊移动端优化
- 后端config.Save()需要传入config路径 → 从handler现有的configPath获取

---

## Work Objectives

### Core Objective
修复NVR Web UI的所有已知bug，并将整个界面从当前的slate/cyan深色工业风重设计为参考mlsbs.top的紫色科技风双主题系统。

### Concrete Deliverables
- `web/src/app.css` — 重写的CSS变量驱动的双主题设计系统
- `web/src/components/Header.svelte` — 新增共享Header组件
- `web/src/components/ThemeToggle.svelte` — 新增主题切换组件
- `web/src/components/Toast.svelte` — 新增Toast通知系统
- `web/src/components/LanguageSwitcher.svelte` — 修复setLang import bug
- `web/src/lib/i18n/index.ts` — 增强Svelte 5响应式支持
- `web/src/lib/preferences.ts` — 新增用户偏好管理模块
- `web/src/routes/Login.svelte` — 重设计+添加语言切换器
- `web/src/routes/Recordings.svelte` — bug修复+重设计
- `web/src/routes/RecordingDetail.svelte` — bug修复+重设计
- `web/src/routes/Cameras.svelte` — 重设计
- `web/src/routes/Stats.svelte` — bug修复+重设计
- `web/src/routes/Settings.svelte` — bug修复+重设计
- `internal/api/handler.go` — 修复settings持久化

### Definition of Done
- [ ] LanguageSwitcher 切换中英文正常工作，所有文本正确翻译
- [ ] Settings页面的itemsPerPage和autoRefresh保存后生效，刷新页面后保持
- [ ] Recordings页面使用Settings中设置的itemsPerPage和autoRefresh
- [ ] Stats页面使用Settings中设置的autoRefresh
- [ ] 深色/浅色主题可切换且切换后所有页面正确显示
- [ ] 所有页面UI风格统一为紫色科技风
- [ ] 后端settings修改持久化到YAML文件
- [ ] `rtk make build` 构建成功，部署到RPi后功能正常

### Must Have
- 所有20个bug修复
- 深色+浅色双主题
- 共享Header组件（导航+语言切换+主题切换+登出）
- 用户偏好localStorage持久化
- 后端config.Save()修复

### Must NOT Have (Guardrails)
- 不引入新的npm依赖（不装icon库、UI库等）
- 不改动Go录制/存储/FTP/WebDAV/MQTT逻辑
- 不创建tailwind.config.js（TailwindCSS 4用CSS配置）
- 不添加router库（保持hash-based手动路由）
- 不添加测试框架（本次不加测试）
- 不使用emoji作为图标（用内联SVG替代）
- 不做移动端特殊优化（保持现有响应式）
- Header组件不要过度抽象，保持简单可维护

---

## Verification Strategy

> **ZERO HUMAN INTERVENTION** - ALL verification is agent-executed. No exceptions.

### Test Decision
- **Infrastructure exists**: NO
- **Automated tests**: None
- **Framework**: none

### QA Policy
Every task MUST include agent-executed QA scenarios.
Evidence saved to `.sisyphus/evidence/task-{N}-{scenario-slug}.{ext}`.

- **Frontend/UI**: Use Playwright (playwright skill) - Navigate, interact, assert DOM, screenshot
- **Backend API**: Use Bash (curl) - Send requests, assert status + response fields
- **Build**: Use Bash - npm run build, make build, verify no errors

---

## Execution Strategy

### Parallel Execution Waves

```
Wave 1 (Start Immediately — foundation + infra):
├── Task 1: CSS theme system + design tokens [visual-engineering]
├── Task 2: User preferences module [quick]
├── Task 3: i18n system fix + enhancement [quick]
├── Task 4: Toast notification component [quick]
└── Task 5: Backend settings persistence fix [quick]

Wave 2 (After Wave 1 — shared components):
├── Task 6: ThemeToggle component (depends: 1, 2) [quick]
└── Task 7: Shared Header component (depends: 1, 3, 4, 6) [visual-engineering]

Wave 3 (After Wave 2 — page redesigns, MAX PARALLEL):
├── Task 8: Login page redesign (depends: 1, 3, 7) [visual-engineering]
├── Task 9: Recordings page bug fix + redesign (depends: 1, 2, 3, 7) [visual-engineering]
├── Task 10: RecordingDetail page bug fix + redesign (depends: 1, 3, 7) [visual-engineering]
├── Task 11: Cameras page redesign (depends: 1, 3, 7) [visual-engineering]
├── Task 12: Stats page bug fix + redesign (depends: 1, 3, 7) [visual-engineering]
└── Task 13: Settings page bug fix + redesign (depends: 1, 2, 3, 7) [visual-engineering]

Wave 4 (After Wave 3 — backend + build):
└── Task 14: Backend settings Save() fix + deploy (depends: 5, 8-13) [quick]

Wave FINAL (After ALL tasks — 4 parallel reviews):
├── Task F1: Plan compliance audit [oracle]
├── Task F2: Code quality review [unspecified-high]
├── Task F3: Real manual QA on 192.168.63.31 [unspecified-high]
└── Task F4: Scope fidelity check [deep]

Critical Path: Task 1 → Task 6 → Task 7 → Tasks 8-13 → Task 14 → F1-F4
Parallel Speedup: ~65% faster than sequential
Max Concurrent: 6 (Wave 3)
```

### Dependency Matrix

| Task | Depends On | Blocks | Wave |
|------|-----------|--------|------|
| 1 | - | 6,7,8,9,10,11,12,13 | 1 |
| 2 | - | 6,9,13 | 1 |
| 3 | - | 7,8,9,10,11,12,13 | 1 |
| 4 | - | 7 | 1 |
| 5 | - | 14 | 1 |
| 6 | 1,2 | 7 | 2 |
| 7 | 1,3,4,6 | 8,9,10,11,12,13 | 2 |
| 8 | 1,3,7 | 14 | 3 |
| 9 | 1,2,3,7 | 14 | 3 |
| 10 | 1,3,7 | 14 | 3 |
| 11 | 1,3,7 | 14 | 3 |
| 12 | 1,3,7 | 14 | 3 |
| 13 | 1,2,3,7 | 14 | 3 |
| 14 | 5,8-13 | F1-F4 | 4 |

### Agent Dispatch Summary

- **Wave 1**: 5 tasks — T1 → `visual-engineering`, T2-T5 → `quick`
- **Wave 2**: 2 tasks — T6 → `quick`, T7 → `visual-engineering`
- **Wave 3**: 6 tasks — T8-T13 → `visual-engineering`
- **Wave 4**: 1 task — T14 → `quick`
- **FINAL**: 4 tasks — F1 → `oracle`, F2 → `unspecified-high`, F3 → `unspecified-high`, F4 → `deep`

---

## TODOs

### Wave 1 — Foundation & Infrastructure

- [x] 1. CSS Theme System + Design Tokens

  **What to do**:
  - Rewrite `web/src/app.css` to use CSS custom properties (variables) for all colors, shadows, border-radii, and transitions
  - Create `:root` (dark theme default) and `[data-theme="light"]` CSS variable sets
  - Dark theme palette: bg-primary #0a0a0a, bg-secondary #141414, bg-tertiary #1a1a1a, bg-elevated #1e1e1e
  - Light theme palette: bg-primary #ffffff, bg-secondary #f8fafc, bg-tertiary #f1f5f9
  - Purple accent system: --color-primary #8b5cf6, --color-primary-light #a78bfa, --color-accent #38bdf8
  - Redesign all component classes (.card, .btn variants, .input, .badge, .table, .spinner) using CSS variables
  - Add glassmorphism effects: backdrop-filter blur, semi-transparent backgrounds
  - Add purple focus rings: `box-shadow: 0 0 0 3px rgba(139, 92, 246, 0.4)`
  - Add gradient button: `linear-gradient(135deg, #8b5cf6, #a78bfa)`
  - Add card hover effects: `translateY(-4px)` + gradient top bar reveal
  - Update font-family to `Inter, -apple-system, BlinkMacSystemFont, sans-serif`
  - Add smooth transitions: `cubic-bezier(0.16, 1, 0.3, 1)` for interactive elements

  **Must NOT do**:
  - Do NOT create tailwind.config.js
  - Do NOT add any npm dependencies
  - Do NOT remove existing TailwindCSS imports

  **Recommended Agent Profile**:
  - **Category**: `visual-engineering`
  - **Skills**: [`frontend-ui-ux`]

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 1 (with Tasks 2, 3, 4, 5)
  - **Blocks**: Tasks 6, 7, 8, 9, 10, 11, 12, 13
  - **Blocked By**: None

  **References**:
  **Pattern References**:
  - `web/src/app.css` — Current design system to be replaced (all component classes)
  - `web/AGENTS.md` — TailwindCSS 4 conventions (no config file, CSS-based config)

  **External References**:
  - Reference design tokens extracted from mlsbs.top (see draft `.sisyphus/drafts/nvr-ui-redesign.md`)
  - Primary purple: #8b5cf6, Secondary: #a78bfa, Accent: #38bdf8
  - Dark backgrounds: #0a0a0a → #1e1e1e, Light backgrounds: #ffffff → #f1f5f9
  - Shadows, gradients, glassmorphism effects documented in draft

  **Acceptance Criteria**:

  QA Scenarios:
  ```
  Scenario: CSS variables defined for both themes
    Tool: Bash (grep)
    Steps:
      1. grep --include="*.css" -c "--color-primary" web/src/app.css
      2. grep --include="*.css" -c "data-theme.*light" web/src/app.css
      3. grep --include="*.css" -c "#8b5cf6" web/src/app.css
    Expected Result: All counts >= 1
    Evidence: .sisyphus/evidence/task-1-css-vars.txt

  Scenario: Build succeeds with new CSS
    Tool: Bash
    Steps:
      1. cd web && npm run build
    Expected Result: Build succeeds with exit code 0, no errors
    Evidence: .sisyphus/evidence/task-1-build.txt
  ```

  **Commit**: YES (groups with Wave 1)
  - Message: `feat(web): add CSS variable-driven dual theme system`
  - Files: `web/src/app.css`

- [x] 2. User Preferences Module

  **What to do**:
  - Create `web/src/lib/preferences.ts` — centralized user preference management
  - Use localStorage for persistence with key prefix `mibee_nvr_prefs_`
  - Export functions: `getPreference(key, defaultValue)`, `setPreference(key, value)`, `getItemsPerPage()`, `setItemsPerPage(n)`, `getAutoRefresh()`, `setAutoRefresh(val)`, `getTheme()`, `setTheme(theme)`
  - Support types: number (itemsPerPage), string (autoRefresh: '10s'|'30s'|'60s'|'off'), string (theme: 'dark'|'light')
  - Parse interval strings ('10s' → 10000, '30s' → 30000, '60s' → 60000, 'off' → 0)

  **Must NOT do**:
  - Do NOT add any npm dependencies
  - Do NOT create a backend API for preferences (localStorage is sufficient)

  **Recommended Agent Profile**:
  - **Category**: `quick`
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 1 (with Tasks 1, 3, 4, 5)
  - **Blocks**: Tasks 6, 9, 13
  - **Blocked By**: None

  **References**:
  **Pattern References**:
  - `web/src/lib/api.ts:5-15` — localStorage pattern for auth (same approach for preferences)
  - `web/src/lib/i18n/index.ts` — localStorage pattern for language (key: `mibee_nvr_lang`)
  - `web/src/routes/Settings.svelte:25-26` — Current default values: itemsPerPage=50, autoRefresh='30s'

  **API/Type References**:
  - `web/src/lib/api.ts:SettingsConfig` — Existing settings type (cleanup only)

  **Acceptance Criteria**:

  QA Scenarios:
  ```
  Scenario: Preferences persist in localStorage
    Tool: Bash
    Steps:
      1. cd web && node -e "const p = require('./src/lib/preferences.ts');" 2>&1 || echo 'TS module needs build check'
      2. Verify file exists and exports expected functions
    Expected Result: File created with all exported functions
    Evidence: .sisyphus/evidence/task-2-prefs-module.txt

  Scenario: Build succeeds
    Tool: Bash
    Steps:
      1. cd web && npm run build
    Expected Result: Exit code 0, no TypeScript errors
    Evidence: .sisyphus/evidence/task-2-build.txt
  ```

  **Commit**: YES (groups with Wave 1)
  - Message: `feat(web): add centralized user preferences module`
  - Files: `web/src/lib/preferences.ts`

- [x] 3. Fix i18n System + Svelte 5 Reactivity

  **What to do**:
  - Fix `web/src/components/LanguageSwitcher.svelte`: add `setLang` to import from `$lib/i18n` (line 3)
  - Remove invalid reactive statement `$: { void lang; }` (line 17) — not needed in Svelte 5
  - Update `web/src/lib/i18n/index.ts` to use Svelte 5 reactive primitives:
    - Replace callback-based `listeners` array with a Svelte 5 `$state` or `$effect` friendly approach
    - Keep backward compatibility: `onLangChange()` still works for components that use it
    - Add `getCurrentLang()` as a reactive getter that components can use in `$effect`
  - Add `t()` as a reactive translation function that triggers re-render on lang change
  - Ensure all 6 page components properly re-render when language changes

  **Must NOT do**:
  - Do NOT add i18n library (svelte-i18n, etc.)
  - Do NOT change the en.json/zh.json file structure

  **Recommended Agent Profile**:
  - **Category**: `quick`
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 1 (with Tasks 1, 2, 4, 5)
  - **Blocks**: Tasks 7, 8, 9, 10, 11, 12, 13
  - **Blocked By**: None

  **References**:
  - `web/src/components/LanguageSwitcher.svelte:3` — Missing `setLang` in import (THE BUG)
  - `web/src/components/LanguageSwitcher.svelte:14` — Calls `setLang(target.value)` which fails
  - `web/src/lib/i18n/index.ts:34-39` — `setLang` function definition
  - `web/src/lib/i18n/index.ts:41-46` — `onLangChange` callback pattern
  - All route files (Login, Recordings, RecordingDetail, Cameras, Stats, Settings) — all use `onLangChange` pattern

  **Acceptance Criteria**:

  QA Scenarios:
  ```
  Scenario: setLang is properly imported in LanguageSwitcher
    Tool: Bash (grep)
    Steps:
      1. grep "setLang" web/src/components/LanguageSwitcher.svelte
    Expected Result: setLang appears in both import line AND handleChange function
    Evidence: .sisyphus/evidence/task-3-i18n-import.txt

  Scenario: Build succeeds after i18n changes
    Tool: Bash
    Steps:
      1. cd web && npm run build
    Expected Result: Exit code 0, no errors
    Evidence: .sisyphus/evidence/task-3-build.txt
  ```

  **Commit**: YES (groups with Wave 1)
  - Message: `fix(web): fix language switching - add missing setLang import`
  - Files: `web/src/components/LanguageSwitcher.svelte`, `web/src/lib/i18n/index.ts`

- [x] 4. Toast Notification Component

  **What to do**:
  - Create `web/src/components/Toast.svelte` — reusable toast notification component
  - Support types: success (green), error (red), info (purple), warning (amber)
  - Auto-dismiss after 3 seconds with fade-out animation
  - Position: fixed top-right, stacked
  - Use the new CSS theme variables (from Task 1)
  - Export a toast store/context that any component can call: `showToast(message, type)`
  - Create a simple Svelte 5 module `web/src/lib/toast.ts` with a writable store pattern

  **Must NOT do**:
  - Do NOT add any npm dependencies
  - Do NOT use alert/prompt/confirm

  **Recommended Agent Profile**:
  - **Category**: `quick`
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 1 (with Tasks 1, 2, 3, 5)
  - **Blocks**: Task 7
  - **Blocked By**: None

  **References**:
  - `web/src/app.css` — Use CSS variables for theming (builds on Task 1)
  - `web/src/routes/Cameras.svelte:33-37` — Current feedback pattern (inline div) to be replaced

  **Acceptance Criteria**:

  QA Scenarios:
  ```
  Scenario: Toast component renders and auto-dismisses
    Tool: Bash
    Steps:
      1. cd web && npm run build
    Expected Result: Build succeeds, Toast.svelte and toast.ts exist
    Evidence: .sisyphus/evidence/task-4-toast-build.txt
  ```

  **Commit**: YES (groups with Wave 1)
  - Message: `feat(web): add toast notification system`
  - Files: `web/src/components/Toast.svelte`, `web/src/lib/toast.ts`

- [x] 5. Backend Settings Persistence Fix

  **What to do**:
  - Fix `internal/api/handler.go` `handleUpdateSettings()`: add `config.Save(configPath, h.config)` call after updating in-memory config
  - The Handler struct needs access to the config file path — check if it already has it, if not add a `configPath string` field
  - Also fix input validation: reject negative `limit` values in recordings endpoint

  **Must NOT do**:
  - Do NOT change the config file format
  - Do NOT modify recording/storage/FTP/WebDAV/MQTT logic
  - Do NOT add new API endpoints

  **Recommended Agent Profile**:
  - **Category**: `quick`
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 1 (with Tasks 1, 2, 3, 4)
  - **Blocks**: Task 14
  - **Blocked By**: None

  **References**:
  - `internal/api/handler.go` — `handleUpdateSettings()` function — needs config.Save() call
  - `internal/config/config.go` — `Save(path, cfg)` function — the atomic save function to call
  - `cmd/mibee-nvr/main.go` — How Handler is initialized (does it get configPath?)
  - `internal/storage/db.go` — Query parameter handling for limit/offset validation

  **Acceptance Criteria**:

  QA Scenarios:
  ```
  Scenario: Settings persist to disk after update
    Tool: Bash (curl)
    Steps:
      1. curl -u admin:admin -X PUT -H 'Content-Type: application/json' -d '{"cleanup":{"retention_days":99}}' http://192.168.63.31:9090/api/settings
      2. curl -u admin:admin http://192.168.63.31:9090/api/settings
    Expected Result: retention_days shows 99 in response
    Evidence: .sisyphus/evidence/task-5-settings-persist.txt

  Scenario: Go build succeeds
    Tool: Bash
    Steps:
      1. rtk go vet ./internal/api/...
      2. rtk make build
    Expected Result: Exit code 0, no errors
    Evidence: .sisyphus/evidence/task-5-build.txt
  ```

  **Commit**: YES (separate commit)
  - Message: `fix(api): persist settings to disk on update`
  - Files: `internal/api/handler.go`

### Wave 2 — Shared Components

- [x] 6. ThemeToggle Component

  **What to do**:
  - Create `web/src/components/ThemeToggle.svelte`
  - Toggle button between dark (moon icon) and light (sun icon) — use inline SVG
  - On click: toggle `data-theme` attribute on `document.documentElement`
  - Read/write theme preference via `preferences.ts` from Task 2
  - Apply theme on component mount by reading stored preference
  - Smooth transition when switching (CSS transition on background/color)

  **Must NOT do**:
  - Do NOT add icon library dependency
  - Do NOT use emoji for icons

  **Recommended Agent Profile**:
  - **Category**: `quick`
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: NO
  - **Parallel Group**: Wave 2
  - **Blocks**: Task 7
  - **Blocked By**: Tasks 1, 2

  **References**:
  - `web/src/lib/preferences.ts` (Task 2) — getTheme/setTheme functions
  - `web/src/app.css` (Task 1) — CSS variables for both themes
  - Reference: mlsbs.top uses a dark/light toggle

  **Acceptance Criteria**:

  QA Scenarios:
  ```
  Scenario: ThemeToggle renders and switches theme
    Tool: Bash
    Steps:
      1. cd web && npm run build
      2. grep -c "data-theme" web/src/components/ThemeToggle.svelte
    Expected Result: Build succeeds, data-theme usage found
    Evidence: .sisyphus/evidence/task-6-theme-toggle.txt
  ```

  **Commit**: YES (groups with Wave 2)
  - Message: `feat(web): add theme toggle component`
  - Files: `web/src/components/ThemeToggle.svelte`

- [x] 7. Shared Header Component

  **What to do**:
  - Create `web/src/components/Header.svelte` — shared navigation header
  - Props: `activeRoute: string` (current page for highlighting), `showBack?: boolean`, `backLabel?: string`, `onBack?: () => void`
  - Include: Logo/title "MiBee NVR", nav links (Recordings, Cameras, Stats, Settings), LanguageSwitcher, ThemeToggle, Logout button
  - Auto-detect current route from hash for active highlighting (or use prop)
  - Use purple gradient pill for active nav item (matching mlsbs.top style)
  - Glassmorphism: `backdrop-filter: blur(20px)` + semi-transparent background
  - Fixed top navbar with `z-index: 1000`
  - Include Toast container in the Header component (renders active toasts)
  - This component replaces the duplicated header in ALL 6 page files

  **Must NOT do**:
  - Do NOT over-abstract — keep it simple, just header + nav + right-side controls
  - Do NOT add a router library
  - Do NOT add state management library

  **Recommended Agent Profile**:
  - **Category**: `visual-engineering`
  - **Skills**: [`frontend-ui-ux`]

  **Parallelization**:
  - **Can Run In Parallel**: NO
  - **Parallel Group**: Wave 2
  - **Blocks**: Tasks 8, 9, 10, 11, 12, 13
  - **Blocked By**: Tasks 1, 3, 4, 6

  **References**:
  **Pattern References**:
  - `web/src/routes/Recordings.svelte:134-155` — Current header pattern (duplicated in all pages)
  - `web/src/routes/Stats.svelte:74-97` — Another header copy (note: missing `</div>` bug here)
  - `web/src/routes/RecordingDetail.svelte:234-259` — Different header (has back button, no full nav)

  **Component References**:
  - `web/src/components/LanguageSwitcher.svelte` — Import and use as-is
  - `web/src/components/ThemeToggle.svelte` (Task 6) — Import and use
  - `web/src/components/Toast.svelte` (Task 4) — Import and render container
  - `web/src/lib/api.ts:logout` — Import logout function
  - `web/src/lib/i18n/index.ts:t` — For nav link text

  **Acceptance Criteria**:

  QA Scenarios:
  ```
  Scenario: Header component exists with all expected elements
    Tool: Bash
    Steps:
      1. grep -c "LanguageSwitcher" web/src/components/Header.svelte
      2. grep -c "ThemeToggle" web/src/components/Header.svelte
      3. grep -c "data-theme" web/src/components/Header.svelte || echo 'theme applied via CSS'
      4. cd web && npm run build
    Expected Result: All references found, build succeeds
    Evidence: .sisyphus/evidence/task-7-header-build.txt
  ```

  **Commit**: YES (groups with Wave 2)
  - Message: `refactor(web): extract shared Header component`
  - Files: `web/src/components/Header.svelte`

### Wave 3 — Page Redesigns (ALL PARALLEL)

- [x] 8. Login Page Redesign

  **What to do**:
  - Rewrite `web/src/routes/Login.svelte` with purple tech aesthetic
  - Replace slate/cyan with purple theme colors
  - Add LanguageSwitcher component (currently missing on login page!)
  - Login card: glassmorphism effect, purple gradient title text, purple focus rings on inputs
  - Background: subtle purple radial glow effect (blurred, low opacity)
  - Use shared CSS classes from updated app.css
  - Remove the duplicated header code — login page is special (no nav, just logo + switcher)

  **Must NOT do**:
  - Do NOT add LanguageSwitcher inside the Header component for login (login has no nav header)
  - Do NOT use emoji for icons

  **Recommended Agent Profile**:
  - **Category**: `visual-engineering`
  - **Skills**: [`frontend-ui-ux`]

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 3 (with Tasks 9, 10, 11, 12, 13)
  - **Blocks**: Task 14
  - **Blocked By**: Tasks 1, 3, 7

  **References**:
  - `web/src/routes/Login.svelte` — Current login page (102 lines)
  - `web/src/components/LanguageSwitcher.svelte` — Add to login page
  - `web/src/app.css` (Task 1) — New design system classes
  - Reference: mlsbs.top glassmorphism login aesthetic

  **Acceptance Criteria**:

  QA Scenarios:
  ```
  Scenario: Login page has language switcher and purple theme
    Tool: Bash (grep)
    Steps:
      1. grep -c "LanguageSwitcher" web/src/routes/Login.svelte
      2. grep -c "#8b5cf6\|purple\|violet" web/src/routes/Login.svelte web/src/app.css
      3. cd web && npm run build
    Expected Result: LanguageSwitcher found, purple colors present, build succeeds
    Evidence: .sisyphus/evidence/task-8-login-redesign.txt
  ```

  **Commit**: YES (groups with Wave 3)
  - Message: `feat(web): redesign login page with purple tech theme`
  - Files: `web/src/routes/Login.svelte`

- [x] 9. Recordings Page — Bug Fix + Redesign

  **What to do**:
  - Rewrite `web/src/routes/Recordings.svelte` with purple tech aesthetic
  - **BUG FIX**: Read `itemsPerPage` from `preferences.ts` instead of hardcoded `limit = 50` (line 28)
  - **BUG FIX**: Read `autoRefresh` from `preferences.ts` instead of hardcoded `30000` (line 108-110)
  - **BUG FIX**: Replace `$: loadRecordings()` reactive statement (line 118) with explicit function calls to prevent infinite API calls
  - Replace duplicated header with shared `<Header activeRoute="recordings" />` component
  - Table: purple header row, hover effects, purple action buttons
  - Pagination: purple active page indicator
  - Filters: purple-themed selects and inputs
  - Delete confirmation modal: purple theme

  **Must NOT do**:
  - Do NOT change API calls or endpoints
  - Do NOT add new filtering features

  **Recommended Agent Profile**:
  - **Category**: `visual-engineering`
  - **Skills**: [`frontend-ui-ux`]

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 3 (with Tasks 8, 10, 11, 12, 13)
  - **Blocks**: Task 14
  - **Blocked By**: Tasks 1, 2, 3, 7

  **References**:
  - `web/src/routes/Recordings.svelte` — Current 324 lines, all to be updated
  - `web/src/lib/preferences.ts` (Task 2) — getItemsPerPage(), getAutoRefresh()
  - `web/src/components/Header.svelte` (Task 7) — Shared header component
  - `web/src/components/Pagination.svelte` — Existing pagination (keep logic, update styling)

  **Acceptance Criteria**:

  QA Scenarios:
  ```
  Scenario: Recordings uses preferences for pagination and refresh
    Tool: Bash (grep)
    Steps:
      1. grep "getItemsPerPage\|preferences" web/src/routes/Recordings.svelte
      2. grep "getAutoRefresh\|preferences" web/src/routes/Recordings.svelte
      3. grep -c "Header" web/src/routes/Recordings.svelte
    Expected Result: preferences imports found, Header component used
    Evidence: .sisyphus/evidence/task-9-recordings-fix.txt

  Scenario: No more hardcoded limit/refresh values
    Tool: Bash (grep)
    Steps:
      1. grep -n "let limit = 50" web/src/routes/Recordings.svelte
      2. grep -n "30000" web/src/routes/Recordings.svelte
    Expected Result: Both return 0 matches (no hardcoded values)
    Evidence: .sisyphus/evidence/task-9-no-hardcode.txt
  ```

  **Commit**: YES (groups with Wave 3)
  - Message: `fix(web): fix recordings pagination/refresh bugs and redesign`
  - Files: `web/src/routes/Recordings.svelte`

- [x] 10. RecordingDetail Page — Bug Fix + Redesign

  **What to do**:
  - Rewrite `web/src/routes/RecordingDetail.svelte` with purple tech aesthetic
  - **BUG FIX**: Save `onLangChange` unsubscribe handle and call it in `onDestroy` (line 20-21)
  - Replace duplicated header with shared Header component (with back button support)
  - Video player: dark background, purple controls
  - JPEG frame player: purple progress bar, purple control buttons
  - Replace emoji (📌📍🗑️) with inline SVG icons
  - Info card: purple accent, modern grid layout
  - Delete confirmation modal: purple theme

  **Must NOT do**:
  - Do NOT change video/frame loading logic
  - Do NOT change blob URL management

  **Recommended Agent Profile**:
  - **Category**: `visual-engineering`
  - **Skills**: [`frontend-ui-ux`]

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 3 (with Tasks 8, 9, 11, 12, 13)
  - **Blocks**: Task 14
  - **Blocked By**: Tasks 1, 3, 7

  **References**:
  - `web/src/routes/RecordingDetail.svelte` — Current 527 lines
  - `web/src/components/Header.svelte` (Task 7) — With back button support

  **Acceptance Criteria**:

  QA Scenarios:
  ```
  Scenario: Memory leak fixed, no emoji icons
    Tool: Bash (grep)
    Steps:
      1. grep -n "onDestroy" web/src/routes/RecordingDetail.svelte | head -3
      2. grep -c "📌\|📍\|🗑️" web/src/routes/RecordingDetail.svelte
    Expected Result: onDestroy includes unsubscribe, emoji count = 0
    Evidence: .sisyphus/evidence/task-10-detail-fix.txt
  ```

  **Commit**: YES (groups with Wave 3)
  - Message: `fix(web): fix detail page memory leak and redesign`
  - Files: `web/src/routes/RecordingDetail.svelte`

- [x] 11. Cameras Page Redesign

  **What to do**:
  - Rewrite `web/src/routes/Cameras.svelte` with purple tech aesthetic
  - Replace duplicated header with shared `<Header activeRoute="cameras" />`
  - Table: purple header, hover effects
  - Add/Edit form: purple-themed inputs, purple gradient submit button
  - Delete confirmation: purple theme with red accent
  - Use Toast for success/error feedback instead of inline div

  **Must NOT do**:
  - Do NOT add pagination (out of scope)
  - Do NOT change camera CRUD API calls

  **Recommended Agent Profile**:
  - **Category**: `visual-engineering`
  - **Skills**: [`frontend-ui-ux`]

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 3 (with Tasks 8, 9, 10, 12, 13)
  - **Blocks**: Task 14
  - **Blocked By**: Tasks 1, 3, 7

  **References**:
  - `web/src/routes/Cameras.svelte` — Current 349 lines
  - `web/src/components/Header.svelte` (Task 7)
  - `web/src/lib/toast.ts` (Task 4) — showToast for feedback

  **Acceptance Criteria**:

  QA Scenarios:
  ```
  Scenario: Camera page uses shared Header and Toast
    Tool: Bash (grep)
    Steps:
      1. grep -c "Header" web/src/routes/Cameras.svelte
      2. grep -c "showToast\|toast" web/src/routes/Cameras.svelte
    Expected Result: Both found
    Evidence: .sisyphus/evidence/task-11-cameras-redesign.txt
  ```

  **Commit**: YES (groups with Wave 3)
  - Message: `feat(web): redesign cameras page with purple theme`
  - Files: `web/src/routes/Cameras.svelte`

- [x] 12. Stats Page — Bug Fix + Redesign

  **What to do**:
  - Rewrite `web/src/routes/Stats.svelte` with purple tech aesthetic
  - **BUG FIX**: Fix missing `</div>` closing tag (around line 87-88)
  - **BUG FIX**: Read `autoRefresh` from `preferences.ts` instead of hardcoded `30000` (line 62-66)
  - Replace duplicated header with shared `<Header activeRoute="stats" />`
  - Storage stat cards: glassmorphism with purple gradient accent
  - Progress bar: purple gradient instead of green/amber/red
  - Camera table: purple-themed

  **Must NOT do**:
  - Do NOT change stats API calls

  **Recommended Agent Profile**:
  - **Category**: `visual-engineering`
  - **Skills**: [`frontend-ui-ux`]

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 3 (with Tasks 8, 9, 10, 11, 13)
  - **Blocks**: Task 14
  - **Blocked By**: Tasks 1, 3, 7

  **References**:
  - `web/src/routes/Stats.svelte` — Current 242 lines, HTML structure bug at line 87-88
  - `web/src/lib/preferences.ts` (Task 2) — getAutoRefresh()
  - `web/src/components/Header.svelte` (Task 7)

  **Acceptance Criteria**:

  QA Scenarios:
  ```
  Scenario: HTML structure valid and uses preferences
    Tool: Bash (grep)
    Steps:
      1. grep -c "getAutoRefresh" web/src/routes/Stats.svelte
      2. grep -c "30000" web/src/routes/Stats.svelte
    Expected Result: getAutoRefresh found, no hardcoded 30000
    Evidence: .sisyphus/evidence/task-12-stats-fix.txt
  ```

  **Commit**: YES (groups with Wave 3)
  - Message: `fix(web): fix stats page HTML and refresh, redesign`
  - Files: `web/src/routes/Stats.svelte`

- [x] 13. Settings Page — Bug Fix + Redesign

  **What to do**:
  - Rewrite `web/src/routes/Settings.svelte` with purple tech aesthetic
  - **BUG FIX**: Load `itemsPerPage` and `autoRefresh` from `preferences.ts` on mount
  - **BUG FIX**: Save `itemsPerPage` and `autoRefresh` to `preferences.ts` in `save()` function
  - **BUG FIX**: Show current values from preferences (not just defaults)
  - Replace duplicated header with shared `<Header activeRoute="settings" />`
  - Cleanup policy section: purple-themed inputs, range slider with purple accent
  - Frontend preferences section: purple-themed selects
  - Save button: purple gradient
  - Show toast on save success/failure

  **Must NOT do**:
  - Do NOT send itemsPerPage/autoRefresh to backend API (localStorage only)

  **Recommended Agent Profile**:
  - **Category**: `visual-engineering`
  - **Skills**: [`frontend-ui-ux`]

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 3 (with Tasks 8, 9, 10, 11, 12)
  - **Blocks**: Task 14
  - **Blocked By**: Tasks 1, 2, 3, 7

  **References**:
  - `web/src/routes/Settings.svelte:25-26` — Default values that need to be loaded from preferences
  - `web/src/routes/Settings.svelte:69-75` — Save payload missing itemsPerPage/autoRefresh
  - `web/src/lib/preferences.ts` (Task 2) — getItemsPerPage/setItemsPerPage/getAutoRefresh/setAutoRefresh
  - `web/src/components/Header.svelte` (Task 7)
  - `web/src/lib/toast.ts` (Task 4)

  **Acceptance Criteria**:

  QA Scenarios:
  ```
  Scenario: Settings loads and saves preferences correctly
    Tool: Bash (grep)
    Steps:
      1. grep -c "getItemsPerPage\|setItemsPerPage" web/src/routes/Settings.svelte
      2. grep -c "getAutoRefresh\|setAutoRefresh" web/src/routes/Settings.svelte
    Expected Result: Both get and set functions found
    Evidence: .sisyphus/evidence/task-13-settings-fix.txt
  ```

  **Commit**: YES (groups with Wave 3)
  - Message: `fix(web): fix settings persistence and redesign`
  - Files: `web/src/routes/Settings.svelte`

### Wave 4 — Build & Deploy

- [x] 14. Full Build + Deploy to Test Server

  **What to do**:
  - Run `cd web && npm run build` to build the SPA
  - Copy dist files: `cp -r web/dist/* internal/ui/static/`
  - Run `rtk make build` to build Go binary with embedded SPA
  - Cross-compile: `rtk make cross` for ARM64
  - SSH to 192.168.63.31 as mickey: deploy the built binary
  - Restart the NVR service: `ssh mickey@192.168.63.31 'sudo systemctl restart mibee-nvr'`
  - Verify service is running: `ssh mickey@192.168.63.31 'systemctl status mibee-nvr'`
  - Health check: `curl http://192.168.63.31:9090/api/health`

  **Must NOT do**:
  - Do NOT modify any source code in this task

  **Recommended Agent Profile**:
  - **Category**: `quick`
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: NO
  - **Parallel Group**: Wave 4 (sequential)
  - **Blocks**: F1-F4
  - **Blocked By**: Tasks 5, 8, 9, 10, 11, 12, 13

  **References**:
  - `web/AGENTS.md` — Build commands
  - `Makefile` — Build targets
  - Deploy target: 192.168.63.31, user mickey, service mibee-nvr

  **Acceptance Criteria**:

  QA Scenarios:
  ```
  Scenario: Build and deploy succeeds
    Tool: Bash
    Steps:
      1. cd web && npm run build
      2. cp -r web/dist/* internal/ui/static/
      3. rtk make cross
      4. scp ./mibee-nvr-arm64 mickey@192.168.63.31:/tmp/mibee-nvr
      5. ssh mickey@192.168.63.31 'sudo systemctl stop mibee-nvr && sudo cp /tmp/mibee-nvr /mnt/data/nvr/bin/mibee-nvr && sudo systemctl start mibee-nvr'
      6. curl -sf http://192.168.63.31:9090/api/health
    Expected Result: Health check returns {"status":"ok"}
    Evidence: .sisyphus/evidence/task-14-deploy.txt

  Scenario: Frontend loads correctly
    Tool: Bash (curl)
    Steps:
      1. curl -sf http://192.168.63.31:9090/ | head -5
    Expected Result: HTML response with embedded SPA
    Evidence: .sisyphus/evidence/task-14-frontend.txt
  ```

  **Commit**: YES
  - Message: `chore: build and deploy purple tech UI`
  - Files: build artifacts, `internal/ui/static/*`
## Final Verification Wave

- [x] F1. **Plan Compliance Audit** — `oracle` ✅ APPROVE (15/15 Must Have, 6/6 Must NOT Have, 14/14 Tasks)
  Read the plan end-to-end. For each "Must Have": verify implementation exists. For each "Must NOT Have": search codebase for forbidden patterns. Check evidence files. Compare deliverables against plan.
  Output: `Must Have [N/N] | Must NOT Have [N/N] | Tasks [N/N] | VERDICT: APPROVE/REJECT`

- [x] F2. **Code Quality Review** — `unspecified-high` ✅ APPROVE (after fixing: handler.go brace bug, Recordings $state, Toast unused import)
  Run `rtk go vet ./...` + `cd web && rtk npm run build`. Review all changed files for: unused imports, broken HTML structure, console.log in prod, commented-out code, inconsistent naming. Check for AI slop.
  Output: `Build [PASS/FAIL] | Go Vet [PASS/FAIL] | Files [N clean/N issues] | VERDICT`

- [x] F3. **Real Manual QA** — `unspecified-high` (+ playwright skill) ✅ APPROVE (all 7 scenarios pass, 0 console errors, screenshots saved)
  SSH to 192.168.63.31 (user: mickey). Deploy build. Open browser to 192.168.63.31:9090. Test: login (admin/admin), language switching (zh/en), theme switching (dark/light), camera CRUD, recordings pagination, settings persistence, all pages render correctly. Save screenshots to `.sisyphus/evidence/final-qa/`.
  Output: `Scenarios [N/N pass] | Integration [N/N] | Edge Cases [N tested] | VERDICT`

- [x] F4. **Scope Fidelity Check** — `deep` ✅ APPROVE (14/14 compliant, zero contamination)
  For each task: read spec, read actual diff. Verify 1:1 — everything built, no creep. Check "Must NOT do" compliance. Detect cross-task contamination.
  Output: `Tasks [N/N compliant] | Contamination [CLEAN/N issues] | VERDICT`

---

## Commit Strategy

- **Wave 1**: `fix(web): fix i18n, add theme system and preferences` — app.css, preferences.ts, i18n/index.ts, Toast.svelte
- **Wave 1**: `fix(backend): persist settings to disk on update` — internal/api/handler.go
- **Wave 2**: `refactor(web): extract shared Header component` — Header.svelte, ThemeToggle.svelte
- **Wave 3**: `feat(web): redesign all pages with purple tech theme` — all routes/*.svelte
- **Wave 4**: `chore: build and deploy` — build artifacts

---

## Success Criteria

### Verification Commands
```bash
cd web && rtk npm run build  # Expected: successful build, no errors
rtk make build               # Expected: Go binary built with embedded SPA
```

### Final Checklist
- [ ] All 20 bugs fixed
- [ ] Dark theme renders with purple (#8b5cf6) accent
- [ ] Light theme renders correctly
- [ ] Language switching works on all pages (zh ↔ en)
- [ ] Settings page itemsPerPage persists and applies to Recordings
- [ ] Settings page autoRefresh persists and applies to Recordings/Stats
- [ ] Shared Header component used on all 6 pages
- [ ] No duplicate header code across pages
- [ ] No new npm dependencies added
- [ ] `npm run build` succeeds
- [ ] `make build` succeeds
