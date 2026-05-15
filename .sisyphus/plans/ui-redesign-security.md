# MiBee NVR Web UI Redesign + Security Fix

## TL;DR

> **Quick Summary**: Redesign the entire MiBee NVR web UI with a minimalist tech aesthetic (black/white theme referencing mlsbs.top), replace all icons with lucide-svelte, integrate Chart.js with new backend time-series API, and fix a critical gortsplib security vulnerability.
> 
> **Deliverables**:
> - Upgraded gortsplib v5.5.2 (security fix)
> - New backend `/api/stats/trends` endpoint for time-series data
> - Redesigned CSS design system (minimalist black/white + purple accent)
> - All icons replaced with lucide-svelte
> - Chart.js integration on Stats page
> - System-preference-based theme detection (no FOUC)
> - Optimized page layouts across all 6 routes
> 
> **Estimated Effort**: Large
> **Parallel Execution**: YES - 3 waves
> **Critical Path**: Security Fix → CSS Design System → Icons → Layout → Backend API → Chart.js → Final QA

---

## Context

### Original Request
重新设计 web ui 的设计，要带有简约科技感，包括所有图标和黑白主题的配色。参考 https://www.mlsbs.top/#/ 的黑白主题和风格。安排解决 Dependabot security alert #1。

### Interview Summary
**Key Discussions**:
- Icon approach: lucide-svelte (confirmed) — line-art style matching minimalist tech aesthetic
- Default theme: Follow system `prefers-color-scheme` (confirmed) — 3-tier priority: user choice > system pref > dark default
- Charts: Chart.js with NEW backend time-series API (confirmed) — storage trends, per-camera recording stats
- Design scope: Visual + layout optimization (confirmed) — not just reskin, improve information hierarchy and spacing
- Emoji: Replace ALL with lucide icons (confirmed) — unified visual language
- Reference: mlsbs.top — minimalist black/white with purple accents, large whitespace, flat cards

**Research Findings**:
- Current frontend: Svelte 5.55 + Vite 8 + TailwindCSS 4.2, 12 components, ~3,000 lines
- Only 5 inline SVGs + 9 emoji/text icons — small replacement scope
- Theme system already works via CSS custom properties + `data-theme` attribute
- No external UI libraries currently — adding lucide-svelte + chart.js as first production deps
- gortsplib v5.5.1 → v5.5.2: RTSP-over-HTTP tunnel fix (SSRF prevention)

### Metis Review
**Identified Gaps** (addressed):
- Chart.js needs backend data → Added new `/api/stats/trends` API endpoint task
- FOUC risk with prefers-color-scheme → Theme detection in index.html inline script
- lucide-svelte + Svelte 5 compatibility → Added verification step
- Emoji replacement visual distinction loss → Pair icons with distinct styling/colors

---

## Work Objectives

### Core Objective
Transform the MiBee NVR web UI into a minimalist tech-forward design with unified icon system, informative data visualizations, and seamless theme switching — while fixing a critical security vulnerability.

### Concrete Deliverables
- `go.mod` updated: gortsplib v5.5.2
- `internal/api/handler.go`: New `/api/stats/trends` endpoint
- `web/src/app.css`: Completely redesigned CSS design tokens
- `web/index.html`: Inline theme detection script
- `web/src/components/ThemeToggle.svelte`: Updated with system preference support
- All `.svelte` files: Icons replaced with lucide-svelte
- `web/src/routes/Stats.svelte`: Chart.js charts with real data
- All route components: Optimized layouts and spacing

### Definition of Done
- [ ] `go build ./...` succeeds with gortsplib v5.5.2
- [ ] `go test ./...` all pass
- [ ] `cd web && npm run build` succeeds
- [ ] All 6 pages render correctly in both dark and light themes
- [ ] Zero inline SVGs remain in source (replaced by lucide)
- [ ] Stats page shows at least 2 charts with real backend data
- [ ] Theme follows system preference on first visit

### Must Have
- gortsplib v5.5.2 upgrade (security)
- Black/white minimalist design with purple accent (#8b5cf6)
- lucide-svelte icons replacing ALL inline SVGs and emojis
- Chart.js with backend time-series data
- System preference theme detection without FOUC
- Both dark and light themes fully functional
- All existing functionality preserved (routes, CRUD, auth, i18n)
- Responsive design (mobile 375px, tablet 768px, desktop 1280px)

### Must NOT Have (Guardrails)
- NO router library — keep hash-based routing in App.svelte
- NO `tailwind.config.js` — TailwindCSS 4 uses CSS-based config
- NO changes to database schema — use SQL aggregation on existing tables
- NO build pipeline changes — `npm run build → copy dist/ → rebuild Go binary`
- NO broken i18n — all text changes update both `en.json` and `zh.json`
- NO `svelte-chartjs` wrapper — use chart.js directly to avoid extra dependency
- NO new test framework — rely on agent-executed Playwright QA
- NO backend API breaking changes — only additive new endpoints
- NO AI slop: excessive comments, over-abstraction, unnecessary utility functions
- NO emoji remaining in Stats.svelte or anywhere else

---

## Verification Strategy

> **ZERO HUMAN INTERVENTION** — ALL verification is agent-executed. No exceptions.

### Test Decision
- **Infrastructure exists**: NO (frontend), YES (backend: `go test`)
- **Automated tests**: Backend: existing tests must pass. Frontend: No unit tests.
- **Framework**: Go testing + Playwright browser QA for frontend
- **QA Method**: Agent-executed Playwright scenarios for every page

### QA Policy
Every task MUST include agent-executed QA scenarios.
Evidence saved to `.sisyphus/evidence/task-{N}-{scenario-slug}.{ext}`.

- **Frontend/UI**: Playwright — navigate, interact, assert DOM, screenshot
- **Backend**: Bash — build, test, curl API endpoints
- **Full integration**: Playwright — cross-page flows, theme switching, responsive

---

## Execution Strategy

### Parallel Execution Waves

```
Wave 1 (Foundation — 3 parallel tasks):
├── Task 1: Security fix — gortsplib v5.5.2 upgrade [quick]
├── Task 2: CSS design system redesign — app.css tokens + component classes [visual-engineering]
└── Task 3: Install + verify lucide-svelte + chart.js [quick]

Wave 2 (Core implementation — after Wave 1):
├── Task 4: Theme detection — index.html inline script + ThemeToggle update [quick]
├── Task 5: Replace all icons with lucide-svelte (5 SVGs + 9 emojis) [quick]
├── Task 6: Backend stats trends API endpoint [unspecified-high]
└── Task 7: Layout optimization — all route components [visual-engineering]

Wave 3 (Integration — after Wave 2):
├── Task 8: Chart.js integration on Stats page [visual-engineering]
└── Task 9: Build + embed + full integration verification [unspecified-high]

Wave FINAL (Verification — after ALL tasks):
├── Task F1: Plan compliance audit [oracle]
├── Task F2: Code quality review [unspecified-high]
├── Task F3: Real manual QA — all pages, both themes, responsive [unspecified-high]
└── Task F4: Scope fidelity check [deep]
→ Present results → Get explicit user okay

Critical Path: Task 1 (security) + Task 2 (CSS) → Task 5 (icons) + Task 7 (layout) → Task 8 (charts) → Final
Parallel Speedup: ~50% faster than sequential
Max Concurrent: 4 (Wave 2)
```

### Dependency Matrix

| Task | Blocked By | Blocks |
|------|-----------|--------|
| 1 | - | - |
| 2 | - | 4, 5, 7 |
| 3 | - | 5, 8 |
| 4 | 2 | 7 |
| 5 | 2, 3 | 7 |
| 6 | - | 8 |
| 7 | 2, 4, 5 | 9 |
| 8 | 3, 6, 7 | 9 |
| 9 | 7, 8 | F1-F4 |

### Agent Dispatch Summary

- **Wave 1**: 3 tasks — T1 → `quick`, T2 → `visual-engineering`, T3 → `quick`
- **Wave 2**: 4 tasks — T4 → `quick`, T5 → `quick`, T6 → `unspecified-high`, T7 → `visual-engineering`
- **Wave 3**: 2 tasks — T8 → `visual-engineering`, T9 → `unspecified-high`
- **FINAL**: 4 tasks — F1 → `oracle`, F2 → `unspecified-high`, F3 → `unspecified-high`, F4 → `deep`

---

## TODOs

- [x] 1. Security Fix — Upgrade gortsplib to v5.5.2

  **What to do**:
  - Run `go get github.com/bluenviron/gortsplib/v5@v5.5.2`
  - Run `go mod tidy`
  - Verify `go build ./...` succeeds
  - Verify `go test ./internal/recorder/... -v` all pass
  - Check `grep gortsplib go.mod` shows `v5.5.2`

  **Must NOT do**:
  - Do not modify recorder source code (h264.go, mjpeg.go)
  - Do not upgrade any other dependencies

  **Recommended Agent Profile**:
  - **Category**: `quick`
    - Reason: Single dependency bump with verification, well-defined steps
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 1 (with Tasks 2, 3)
  - **Blocks**: Nothing
  - **Blocked By**: None

  **References**:
  **Pattern References**:
  - `go.mod:7` — Current gortsplib v5.5.1 declaration to update
  - `go.sum` — Will be updated by `go mod tidy`

  **API/Type References**:
  - `internal/recorder/h264.go:15-18` — Imports gortsplib, verify build still works after upgrade
  - `internal/recorder/mjpeg.go:14-16` — Imports gortsplib, verify build still works after upgrade

  **Test References**:
  - `internal/recorder/h264_test.go` — Must pass after upgrade
  - `internal/recorder/mjpeg_test.go` — Must pass after upgrade

  **WHY Each Reference Matters**:
  - `go.mod:7`: This is the exact line that declares the vulnerable version
  - recorder files: These import gortsplib directly — must verify they compile cleanly
  - test files: Regression safety — v5.5.2 is API-compatible but tests catch any surprises

  **Acceptance Criteria**:
  - [ ] `grep gortsplib go.mod` output contains `v5.5.2`
  - [ ] `go build ./...` exits 0
  - [ ] `go test ./internal/recorder/... -v` all pass

  **QA Scenarios:**
  ```
  Scenario: Security upgrade builds and tests pass
    Tool: Bash
    Preconditions: Go toolchain available, current code compiles
    Steps:
      1. Run `go get github.com/bluenviron/gortsplib/v5@v5.5.2`
      2. Run `go mod tidy`
      3. Run `go build ./...` and check exit code is 0
      4. Run `go test ./internal/recorder/... -v` and verify all tests pass
      5. Run `grep gortsplib go.mod` and verify output contains `v5.5.2`
    Expected Result: All commands succeed, version updated to v5.5.2
    Failure Indicators: Build errors, test failures, version not updated
    Evidence: .sisyphus/evidence/task-1-security-upgrade.txt
  ```

  **Commit**: YES
  - Message: `fix(deps): upgrade gortsplib v5.5.2 (security)`
  - Files: `go.mod, go.sum`
  - Pre-commit: `go build ./... && go test ./internal/recorder/...`

- [x] 2. CSS Design System Redesign — Minimalist Black/White Theme

  **What to do**:
  - Redesign all CSS custom properties in `web/src/app.css` for minimalist black/white theme:
    - Dark theme `:root`: Near-black backgrounds (#0a0a0a→#0f0f0f, #141414→#161616, etc.), white text, subtle borders
    - Light theme `[data-theme="light"]`: Pure white backgrounds, dark text, soft borders
    - Keep purple accent `#8b5cf6` for interactive elements (buttons, links, active states)
    - Simplify shadows: remove purple glow shadows, use subtle gray shadows only
    - Reduce button weight: flat/solid backgrounds instead of gradients, minimal shadow
    - Simplify `.card`: flatter elevation, less dramatic hover lift
    - Update `.glass` glassmorphism to be more subtle
  - Reference mlsbs.top style: large whitespace, flat cards, minimal shadows, 8-12px radius
  - Update `.badge` styles to be more subtle (less saturated backgrounds)
  - Keep the 3-tier variable system (`:root` dark → `[data-theme="light"]` → component classes)
  - Keep all component class names (`.card`, `.btn`, `.input`, etc.) — only change values

  **Must NOT do**:
  - Do not add `tailwind.config.js`
  - Do not change component class names (`.card`, `.btn`, etc.)
  - Do not remove any CSS custom properties — only change their values
  - Do not add new CSS custom properties without good reason
  - Do not use Tailwind `@theme` directives

  **Recommended Agent Profile**:
  - **Category**: `visual-engineering`
    - Reason: CSS design system work requiring aesthetic judgment and visual refinement
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 1 (with Tasks 1, 3)
  - **Blocks**: Tasks 4, 5, 7
  - **Blocked By**: None

  **References**:
  **Pattern References**:
  - `web/src/app.css:18-109` — ALL CSS custom properties (design tokens) to redesign
  - `web/src/app.css:179-442` — Component classes (.card, .btn, .input, .badge, etc.)
  - `.playwright-mcp/mlsbs-homepage-full.png` — Reference screenshot of mlsbs.top design

  **API/Type References**:
  - `web/src/components/ThemeToggle.svelte:9-33` — Theme application mechanism (don't change logic, but understand how theme switches)

  **WHY Each Reference Matters**:
  - `app.css:18-109`: This is the ENTIRE design token system — every color, shadow, radius, transition
  - `app.css:179-442`: Component classes that consume the tokens — these visual appearances will change when tokens change
  - `ThemeToggle.svelte`: Understanding theme switching ensures CSS changes work in both themes
  - Reference screenshot: Visual target to match

  **Acceptance Criteria**:
  - [ ] `cd web && npm run build` succeeds (exit 0)
  - [ ] No CSS custom properties removed (same variable names exist)
  - [ ] No component class names changed
  - [ ] Both dark and light themes produce valid visual output (screenshot evidence)

  **QA Scenarios:**
  ```
  Scenario: CSS redesign builds and themes switch correctly
    Tool: Bash + Playwright
    Preconditions: npm dependencies installed
    Steps:
      1. Run `cd web && npm run build` — verify exit 0, no errors
      2. Start dev server `cd web && npm run dev`
      3. Open http://localhost:5173 in Playwright
      4. Take screenshot of default theme → .sisyphus/evidence/task-2-dark-theme.png
      5. Click theme toggle button
      6. Take screenshot of light theme → .sisyphus/evidence/task-2-light-theme.png
      7. Verify no purple gradient backgrounds on buttons (check DOM for gradient classes)
      8. Verify card shadows are subtle gray, not purple glow
    Expected Result: Build succeeds, both themes render, design matches minimalist black/white aesthetic
    Failure Indicators: Build errors, missing CSS variables, broken theme toggle, purple gradient backgrounds remain
    Evidence: .sisyphus/evidence/task-2-dark-theme.png, task-2-light-theme.png

  Scenario: CSS variable count preserved
    Tool: Bash
    Steps:
      1. `grep -c '\-\-' web/src/app.css` before and after changes
      2. Verify count is >= original count (no variables removed)
    Expected Result: Variable count preserved or increased (never decreased)
    Evidence: .sisyphus/evidence/task-2-css-variables.txt
  ```

  **Commit**: YES
  - Message: `style(web): redesign CSS design system (minimalist black/white)`
  - Files: `web/src/app.css`
  - Pre-commit: `cd web && npm run build`

- [x] 3. Install + Verify lucide-svelte + chart.js

  **What to do**:
  - Run `cd web && npm install lucide-svelte chart.js`
  - Verify lucide-svelte works with Svelte 5.55.4:
    - Create a temporary test: import `{ Camera }` from `lucide-svelte` in any .svelte file
    - Run `npm run build` — verify no import/resolution errors
  - Verify chart.js loads:
    - Import `Chart` from `chart.js` in a .svelte file
    - Run `npm run build` — verify no errors
  - Remove temporary test imports after verification
  - Record bundle size impact: `ls -la web/dist/assets/*.js` after build

  **Must NOT do**:
  - Do not add `svelte-chartjs` or any chart wrapper library
  - Do not add any other npm packages
  - Do not start using icons or charts in components yet

  **Recommended Agent Profile**:
  - **Category**: `quick`
    - Reason: Package installation + verification, straightforward
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 1 (with Tasks 1, 2)
  - **Blocks**: Tasks 5, 8
  - **Blocked By**: None

  **References**:
  **Pattern References**:
  - `web/package.json` — Current dependencies (no production deps)
  - `web/vite.config.js` — Vite config for understanding build pipeline

  **External References**:
  - lucide-svelte docs: https://lucide.dev/guide/installation
  - chart.js docs: https://www.chartjs.org/docs/latest/getting-started/

  **WHY Each Reference Matters**:
  - `package.json`: Need to understand current dependency state before adding new ones
  - `vite.config.js`: Ensures new packages work with existing Vite configuration

  **Acceptance Criteria**:
  - [ ] `web/package.json` contains `lucide-svelte` and `chart.js` in dependencies
  - [ ] `cd web && npm run build` succeeds with both packages installed
  - [ ] Temporary test import of `{ Camera }` from lucide-svelte compiles without error
  - [ ] Bundle size recorded in evidence file

  **QA Scenarios:**
  ```
  Scenario: Packages install and build correctly
    Tool: Bash
    Steps:
      1. Run `cd web && npm install lucide-svelte chart.js`
      2. Verify `grep lucide-svelte web/package.json` succeeds
      3. Verify `grep chart.js web/package.json` succeeds
      4. Run `cd web && npm run build` — verify exit 0
      5. Record bundle size: `ls -la web/dist/assets/*.js > ../.sisyphus/evidence/task-3-bundle-size.txt`
    Expected Result: Both packages in package.json, build succeeds
    Failure Indicators: npm install errors, build errors, missing packages
    Evidence: .sisyphus/evidence/task-3-bundle-size.txt

  Scenario: lucide-svelte + Svelte 5 compatibility
    Tool: Bash
    Steps:
      1. Create temp file: add `import { Camera } from 'lucide-svelte'` to any .svelte file
      2. Run `cd web && npm run build`
      3. Remove temp import
      4. Verify build exit 0
    Expected Result: Import resolves, build succeeds, no Svelte version conflicts
    Failure Indicators: Import errors, Svelte version mismatch warnings
    Evidence: .sisyphus/evidence/task-3-compat.txt
  ```

  **Commit**: YES
  - Message: `build(web): add lucide-svelte and chart.js dependencies`
  - Files: `web/package.json, web/package-lock.json`
  - Pre-commit: `cd web && npm run build`

---

- [x] 4. Theme Detection — System Preference + No FOUC

  **What to do**:
  - Add inline `<script>` block in `web/index.html` BEFORE any CSS links:
    ```html
    <script>
      (function() {
        var saved = localStorage.getItem('mibee_nvr_prefs');
        var theme = saved ? JSON.parse(saved).theme : null;
        if (!theme) {
          theme = window.matchMedia('(prefers-color-scheme: light)').matches ? 'light' : 'dark';
        }
        document.documentElement.setAttribute('data-theme', theme);
      })();
    </script>
    ```
  - Update `web/src/lib/preferences.ts` DEFAULT_PREFERENCES.theme from `'dark'` to `null` (meaning: follow system)
  - Update `web/src/components/ThemeToggle.svelte`:
    - Add system preference detection: `window.matchMedia('(prefers-color-scheme: light)')`
    - Show 3-state toggle or auto/system indicator
    - 3-tier priority: user explicit choice > system preference > default dark
  - Add `prefers-color-scheme` change listener to auto-update when system theme changes

  **Must NOT do**:
  - Do not remove localStorage persistence
  - Do not change the `data-theme` attribute mechanism
  - Do not use Tailwind `dark:` class strategy

  **Recommended Agent Profile**:
  - **Category**: `quick`
    - Reason: Small targeted changes to 3 files, well-defined logic
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 2 (with Tasks 5, 6, 7)
  - **Blocks**: Task 7
  - **Blocked By**: Task 2 (CSS must be done first for theme to render correctly)

  **References**:
  **Pattern References**:
  - `web/index.html` — HTML entry point, add inline script before CSS
  - `web/src/lib/preferences.ts` — DEFAULT_PREFERENCES, getTheme/setTheme functions
  - `web/src/components/ThemeToggle.svelte:9-33` — Current theme toggle logic

  **WHY Each Reference Matters**:
  - `index.html`: The inline script must run before any CSS loads to prevent FOUC
  - `preferences.ts`: Theme persistence layer, need to add system-preference-aware defaults
  - `ThemeToggle.svelte`: UI component that needs to show system/user preference state

  **Acceptance Criteria**:
  - [ ] `grep prefers-color-scheme web/index.html` finds the detection script
  - [ ] `cd web && npm run build` succeeds
  - [ ] No FOUC when loading page (verified by Playwright screenshot timing)

  **QA Scenarios:**
  ```
  Scenario: System preference detection works on first visit
    Tool: Playwright
    Steps:
      1. Clear localStorage: `page.evaluate(() => localStorage.clear())`
      2. Set system preference to light: emulate media feature `prefers-color-scheme: light`
      3. Navigate to http://localhost:5173
      4. Check `document.documentElement.getAttribute('data-theme')` equals `light`
      5. Set system preference to dark, reload
      6. Check `data-theme` equals `dark`
    Expected Result: Theme follows system preference when no user choice saved
    Failure Indicators: Always shows dark regardless of system preference, FOUC visible
    Evidence: .sisyphus/evidence/task-4-system-theme.png

  Scenario: User choice overrides system preference
    Tool: Playwright
    Steps:
      1. Set system to `light`, navigate to page
      2. Click theme toggle to switch to dark
      3. Reload page
      4. Verify theme is still `dark` (user choice persists)
    Expected Result: User explicit choice wins over system preference
    Evidence: .sisyphus/evidence/task-4-user-override.png
  ```

  **Commit**: YES
  - Message: `feat(web): add system theme detection with prefers-color-scheme`
  - Files: `web/index.html, web/src/lib/preferences.ts, web/src/components/ThemeToggle.svelte`
  - Pre-commit: `cd web && npm run build`

- [x] 5. Replace All Icons with lucide-svelte

  **What to do**:
  - Replace all inline SVGs with lucide-svelte imports:
    - `Header.svelte`: ArrowLeft (back), Menu (hamburger), LogOut (logout)
    - `ThemeToggle.svelte`: Sun (light), Moon (dark)
  - Replace all emoji/text icons:
    - `Recordings.svelte`: 📌→`Pin`, 📍→`MapPin`, 🗑️→`Trash2`
    - `Stats.svelte`: 💾→`HardDrive`, 📊→`BarChart3`, 🎬→`Video`, 📷→`Camera`
    - `Pagination.svelte`: ←→`ChevronLeft`, →→`ChevronRight`
    - `Toast.svelte`: ✕→`X`
  - Add `import { IconName } from 'lucide-svelte'` at top of each component
  - Use `<IconName size={20} />` or `<IconName size={16} />` consistently
  - For Stats icons: pair with colored backgrounds or distinct styling to maintain visual distinction

  **Must NOT do**:
  - Do not leave any inline `<svg>` tags in .svelte files
  - Do not leave any emoji icons (📌📍🗑️💾📊🎬📷) in .svelte files
  - Do not use lucide icons for text content like “←”, “→” in Pagination — use ChevronLeft/ChevronRight

  **Recommended Agent Profile**:
  - **Category**: `quick`
    - Reason: Mechanical find-and-replace across 7 files, well-defined mapping
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 2 (with Tasks 4, 6, 7)
  - **Blocks**: Task 7
  - **Blocked By**: Tasks 2, 3 (CSS must be done for visual verification; lucide-svelte must be installed)

  **References**:
  **Pattern References**:
  - `web/src/components/Header.svelte` — 3 inline SVGs at lines ~79-82, ~108-113, ~136-140
  - `web/src/components/ThemeToggle.svelte` — 2 inline SVGs (sun/moon) at lines ~45-73
  - `web/src/routes/Stats.svelte` — 4 emoji icons (💾📊🎬📷)
  - `web/src/routes/Recordings.svelte` — 3 emoji icons (📌📍🗑️)
  - `web/src/components/Pagination.svelte` — 2 text arrows (← →)
  - `web/src/components/Toast.svelte` — 1 text icon (✕)

  **External References**:
  - lucide icon search: https://lucide.dev/icons/ — verify exact icon names exist

  **WHY Each Reference Matters**:
  - Each file contains specific icons that need direct replacement
  - lucide.dev confirms icon availability (e.g., `Pin`, `MapPin`, `Trash2` are all real lucide icons)

  **Acceptance Criteria**:
  - [ ] `grep -r '<svg' web/src/ --include='*.svelte'` returns 0 matches
  - [ ] `grep -rP '[\x{1F300}-\x{1F9FF}]' web/src/ --include='*.svelte'` returns 0 matches
  - [ ] `cd web && npm run build` succeeds
  - [ ] All pages render correctly with new icons (screenshot evidence)

  **QA Scenarios:**
  ```
  Scenario: All icons replaced and render correctly
    Tool: Playwright + Bash
    Steps:
      1. Run `grep -r '<svg' web/src/ --include='*.svelte'` — expect 0 matches
      2. Run `grep -rP '[\x{1F300}-\x{1F9FF}]' web/src/ --include='*.svelte'` — expect 0 matches
      3. Start dev server, open http://localhost:5173
      4. Navigate to each page (#/recordings, #/cameras, #/stats, #/settings)
      5. Take screenshots → .sisyphus/evidence/task-5-icons-{page}.png
      6. Verify all lucide icons render as SVG elements (check DOM for lucide class attributes)
    Expected Result: Zero inline SVGs, zero emoji, all lucide icons render
    Failure Indicators: Missing icons, broken imports, emoji still present
    Evidence: .sisyphus/evidence/task-5-icons-{page}.png

  Scenario: Header and ThemeToggle icons work
    Tool: Playwright
    Steps:
      1. Navigate to any page
      2. Verify hamburger/back/logout icons render in header
      3. Click theme toggle — verify sun/moon icons switch correctly
      4. Screenshot both states
    Expected Result: All header icons render, theme toggle shows correct icon for current theme
    Evidence: .sisyphus/evidence/task-5-header-icons.png
  ```

  **Commit**: YES
  - Message: `feat(web): replace all icons with lucide-svelte`
  - Files: `web/src/components/*.svelte, web/src/routes/Recordings.svelte, web/src/routes/Stats.svelte`
  - Pre-commit: `cd web && npm run build`

- [x] 6. Backend Stats Trends API Endpoint

  **What to do**:
  - Add new endpoint `GET /api/stats/trends` in `internal/api/handler.go`
  - Add `GetStorageTrends(db *DB, days int)` in `internal/storage/db.go`:
    - Query recordings table aggregated by day for last N days
    - Return: `[{date: "2026-05-01", recordings: 45, total_size: 1234567890, cameras: {"cam1": 20, "cam2": 25}}]`
    - Default `days=7`, max `days=30`
    - Use existing `recordings` table with `started_at` column for time grouping
  - Add handler method `(h *Handler) handleStatsTrends()` in `internal/api/handler.go`:
    - Accept `?days=N` query parameter
    - Return JSON array of daily aggregations
  - Register route in `Routes()` method: `r.Get("/stats/trends", h.handleStatsTrends)`
  - Add response type to `internal/model/types.go` if needed

  **Must NOT do**:
  - Do not create new database tables — aggregate from existing `recordings` table using SQL
  - Do not modify existing `/api/stats` endpoint
  - Do not change database schema
  - Do not add any ORM — continue using raw SQL queries like existing code

  **Recommended Agent Profile**:
  - **Category**: `unspecified-high`
    - Reason: Backend work requiring Go SQL query design + API handler + route registration
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES
  - **Parallel Group**: Wave 2 (with Tasks 4, 5, 7)
  - **Blocks**: Task 8
  - **Blocked By**: None (backend is independent of frontend redesign)

  **References**:
  **Pattern References**:
  - `internal/api/handler.go` — Existing handler pattern: `(h *Handler) handleStats()` and `Routes()` registration
  - `internal/storage/db.go` — Existing query pattern: `GetStorageStats()`, `ListRecordings()` for SQL style
  - `internal/model/types.go` — Existing type definitions: `StorageStats`, response patterns

  **API/Type References**:
  - `internal/storage/db.go:Init()` — Schema shows `recordings` table columns: `id, camera_id, camera_name, started_at, ended_at, file_path, file_size, format`
  - `internal/storage/db.go:timeToDB()` — UTC timestamp format for SQL queries

  **Test References**:
  - `tests/integration_test.go` — Existing integration test patterns

  **WHY Each Reference Matters**:
  - `handler.go`: Must follow existing handler patterns (method on Handler struct, JSON response, error handling)
  - `db.go:Init()`: Need to know exact column names and types for SQL aggregation queries
  - `db.go:timeToDB()`: Timestamp format must match for WHERE clauses
  - `types.go`: Response types should follow existing naming conventions

  **Acceptance Criteria**:
  - [ ] `go build ./...` succeeds
  - [ ] `go test ./...` all pass
  - [ ] `curl /api/stats/trends` returns JSON array (after auth)
  - [ ] `curl /api/stats/trends?days=7` returns 7 days of aggregated data

  **QA Scenarios:**
  ```
  Scenario: Trends API returns valid data
    Tool: Bash (curl)
    Preconditions: NVR server running with some recordings
    Steps:
      1. Build and start the server with test config
      2. `curl -u admin:password http://localhost:9090/api/stats/trends`
      3. Verify response is JSON array with date, recordings, total_size fields
      4. `curl -u admin:password http://localhost:9090/api/stats/trends?days=3`
      5. Verify response contains at most 3 entries
    Expected Result: JSON array of daily stats with expected fields
    Failure Indicators: 404, 500, missing fields, non-JSON response
    Evidence: .sisyphus/evidence/task-6-trends-api.txt

  Scenario: Edge case - days parameter validation
    Tool: Bash
    Steps:
      1. `curl -u admin:password http://localhost:9090/api/stats/trends?days=0` — expect default (7 days)
      2. `curl -u admin:password http://localhost:9090/api/stats/trends?days=100` — expect max cap (30 days)
      3. `curl -u admin:password http://localhost:9090/api/stats/trends?days=abc` — expect default (7 days)
    Expected Result: Invalid/edge values handled gracefully
    Evidence: .sisyphus/evidence/task-6-trends-edge.txt
  ```

  **Commit**: YES
  - Message: `feat(api): add /api/stats/trends endpoint for time-series data`
  - Files: `internal/api/handler.go, internal/storage/db.go, internal/model/types.go`
  - Pre-commit: `go build ./... && go test ./...`

- [x] 7. Layout Optimization — All Route Components

  **What to do**:
  - Optimize layouts across all 6 route components for minimalist tech aesthetic:
    - `Login.svelte`: Center-align login form, larger spacing, cleaner card
    - `Recordings.svelte`: Improve filter bar layout, better table spacing, card hover states
    - `RecordingDetail.svelte`: Better video player framing, cleaner action buttons
    - `Cameras.svelte`: Cleaner camera cards/form, better status indicators
    - `Stats.svelte`: Prepare layout for chart integration (placeholder areas), improve stat cards
    - `Settings.svelte`: Better form spacing, cleaner sections
  - Optimize `Header.svelte`:
    - Cleaner navigation, better active state indicator
    - Improve mobile hamburger menu animation
  - Improve `Pagination.svelte`: Better spacing and icon alignment
  - Ensure consistent spacing scale: use `gap-4`/`gap-6`/`gap-8` pattern
  - Verify responsive at: 375px, 768px, 1280px
  - Use Svelte transitions (`fade`, `fly`) for page element appearances

  **Must NOT do**:
  - Do not change route structure or add new routes
  - Do not change form validation logic
  - Do not remove any existing features (pin/unpin, filters, CRUD)
  - Do not change API calls or data flow
  - Do not modify `App.svelte` routing logic
  - Do not rewrite component HTML structure from scratch — only adjust spacing/padding/grids

  **Recommended Agent Profile**:
  - **Category**: `visual-engineering`
    - Reason: Layout/spacing optimization requiring visual judgment
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: NO — depends on Tasks 2, 4, 5 completing first
  - **Parallel Group**: Wave 2 (but must start after T2+T4+T5)
  - **Blocks**: Task 9
  - **Blocked By**: Tasks 2, 4, 5

  **References**:
  **Pattern References**:
  - `web/src/routes/Login.svelte` — 102 lines, auth form to refine
  - `web/src/routes/Recordings.svelte` — 304 lines, recording list + filters
  - `web/src/routes/RecordingDetail.svelte` — 503 lines, video player + frame viewer
  - `web/src/routes/Cameras.svelte` — 318 lines, camera CRUD
  - `web/src/routes/Stats.svelte` — 212 lines, stats display (prepare for charts)
  - `web/src/routes/Settings.svelte` — 211 lines, settings form
  - `web/src/components/Header.svelte` — 358 lines, navigation bar
  - `web/src/components/Pagination.svelte` — 56 lines, pagination controls

  **WHY Each Reference Matters**:
  - Every route component needs layout refinement — these are the exact files to modify
  - Stats.svelte needs chart placeholder areas prepared for Task 8

  **Acceptance Criteria**:
  - [ ] `cd web && npm run build` succeeds
  - [ ] All 6 pages render without overflow at 375px, 768px, 1280px
  - [ ] Screenshots of all pages at all 3 breakpoints

  **QA Scenarios:**
  ```
  Scenario: Responsive layout at 3 breakpoints
    Tool: Playwright
    Steps:
      1. Resize browser to 375px width (mobile)
      2. Navigate to each page, take screenshot → .sisyphus/evidence/task-7-375-{page}.png
      3. Resize to 768px (tablet), repeat screenshots → task-7-768-{page}.png
      4. Resize to 1280px (desktop), repeat screenshots → task-7-1280-{page}.png
      5. Verify no horizontal overflow on any page at any width
    Expected Result: Clean layout at all breakpoints, no overflow
    Failure Indicators: Horizontal scroll, elements overlapping, text cutoff
    Evidence: .sisyphus/evidence/task-7-{width}-{page}.png

  Scenario: Header mobile menu works
    Tool: Playwright
    Steps:
      1. Resize to 375px (mobile view)
      2. Click hamburger menu icon
      3. Verify nav links appear in overlay/dropdown
      4. Click each nav link, verify navigation works
      5. Close menu, verify it collapses
    Expected Result: Mobile menu opens, navigates, and closes correctly
    Evidence: .sisyphus/evidence/task-7-mobile-menu.png
  ```

  **Commit**: YES
  - Message: `style(web): optimize page layouts for minimalist aesthetic`
  - Files: `web/src/routes/*.svelte, web/src/components/Header.svelte, web/src/components/Pagination.svelte`
  - Pre-commit: `cd web && npm run build`

---

- [x] 8. Chart.js Integration — Stats Page

  **What to do**:
  - Create chart components in Stats.svelte:
    - Storage usage over time (line chart): daily total_size from `/api/stats/trends`
    - Recordings per camera (bar chart): per-camera recording count from trends data
    - Optional: Storage distribution doughnut (current snapshot from `/api/stats`)
  - Use Chart.js directly (not svelte-chartjs):
    - Import `Chart` from `chart.js'` and register necessary components
    - Create canvas elements in template
    - Initialize charts in `onMount`, destroy in `onDestroy`
  - Match minimalist black/white theme:
    - Chart colors: white/gray lines on dark, dark/gray on light
    - Grid lines: subtle, matching CSS `--border` color
    - Labels: use CSS `--text-secondary` color
  - Auto-refresh charts on 30s interval (existing Stats.svelte pattern)
  - Lazy load Chart.js only when Stats page is mounted (dynamic import or accept bundle increase)

  **Must NOT do**:
  - Do not use `svelte-chartjs` wrapper
  - Do not forget `chart.destroy()` in `onDestroy` (memory leak on RPi 3B)
  - Do not add charts to other pages
  - Do not make Chart.js a global dependency — scope to Stats.svelte

  **Recommended Agent Profile**:
  - **Category**: `visual-engineering`
    - Reason: Chart design + integration requiring visual refinement
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: NO
  - **Parallel Group**: Wave 3 (after Wave 2)
  - **Blocks**: Task 9
  - **Blocked By**: Tasks 3, 6, 7

  **References**:
  **Pattern References**:
  - `web/src/routes/Stats.svelte` — Current stats display, auto-refresh pattern

  **API/Type References**:
  - New `/api/stats/trends` endpoint (Task 6) — Returns `[{date, recordings, total_size, cameras}]`
  - Existing `/api/stats` endpoint — Returns `{total_bytes, used_bytes, recording_count, camera_count}`
  - `web/src/lib/api.ts` — API client pattern for adding new fetch calls

  **External References**:
  - Chart.js docs: https://www.chartjs.org/docs/latest/charts/line.html
  - Chart.js theming: https://www.chartjs.org/docs/latest/configuration/#global-settings

  **WHY Each Reference Matters**:
  - `Stats.svelte`: Where charts will be added, need to understand current structure
  - `/api/stats/trends`: Data source for time-series charts — must match response shape
  - `api.ts`: Need to add new API call function following existing pattern
  - Chart.js docs: Correct chart configuration, color theming, and lifecycle management

  **Acceptance Criteria**:
  - [ ] `cd web && npm run build` succeeds
  - [ ] Stats page shows at least 2 charts (storage trend + per-camera bar)
  - [ ] Charts render without console errors
  - [ ] Charts update on 30s auto-refresh

  **QA Scenarios:**
  ```
  Scenario: Charts render with real data
    Tool: Playwright
    Preconditions: NVR server running with some recordings, trends API available
    Steps:
      1. Navigate to #/stats page
      2. Wait for charts to load (2s timeout)
      3. Verify 2+ <canvas> elements exist in DOM
      4. Take screenshot → .sisyphus/evidence/task-8-charts.png
      5. Check browser console for errors — expect 0
    Expected Result: Charts visible, no console errors, canvas elements present
    Failure Indicators: No canvas, console errors, blank chart areas
    Evidence: .sisyphus/evidence/task-8-charts.png

  Scenario: Chart theme matches current theme
    Tool: Playwright
    Steps:
      1. Navigate to #/stats with dark theme
      2. Verify chart labels and grid lines are light-colored
      3. Switch to light theme
      4. Verify chart labels and grid lines are dark-colored
      5. Take screenshots of both → .sisyphus/evidence/task-8-chart-theme-{dark,light}.png
    Expected Result: Chart colors adapt to current theme
    Evidence: .sisyphus/evidence/task-8-chart-theme-dark.png, task-8-chart-theme-light.png

  Scenario: Charts cleanup on unmount (no memory leak)
    Tool: Playwright
    Steps:
      1. Navigate to #/stats
      2. Navigate away to #/recordings
      3. Check console for Chart.js destroy warnings — expect none
      4. Navigate back to #/stats
      5. Verify charts re-render cleanly
    Expected Result: No memory leak warnings, charts re-create properly
    Evidence: .sisyphus/evidence/task-8-cleanup.txt
  ```

  **Commit**: YES
  - Message: `feat(web): integrate Chart.js on Stats page with trends data`
  - Files: `web/src/routes/Stats.svelte, web/src/lib/api.ts`
  - Pre-commit: `cd web && npm run build`

- [x] 9. Build + Embed + Full Integration Verification

  **What to do**:
  - Run `cd web && npm run build` to produce production dist/
  - Copy `cp -r web/dist/* internal/ui/static/` to embed in Go binary
  - Run `go build -o mibee-nvr ./cmd/mibee-nvr/` to produce final binary
  - Start the binary with test config
  - Verify ALL pages work end-to-end:
    - Login flow
    - Recordings list, filters, detail page, video playback
    - Camera CRUD (add, edit, delete)
    - Stats page with charts
    - Settings page
    - Theme switching (dark/light + system)
    - Language switching (en/zh)
    - Responsive behavior
  - Fix any integration issues found

  **Must NOT do**:
  - Do not make design changes — only fix integration bugs
  - Do not skip any page verification

  **Recommended Agent Profile**:
  - **Category**: `unspecified-high`
    - Reason: Full-stack integration testing and bug fixing across Go + Svelte
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: NO
  - **Parallel Group**: Wave 3 (sequential, after Tasks 7 and 8)
  - **Blocks**: F1-F4
  - **Blocked By**: Tasks 7, 8

  **References**:
  **Pattern References**:
  - `cmd/mibee-nvr/main.go` — Entry point, understand how embedded UI is served
  - `internal/ui/embed.go` — `//go:embed` of static files
  - `deploy/` — Example configs for running the server

  **WHY Each Reference Matters**:
  - `main.go`: Need to understand server startup for integration testing
  - `embed.go`: Confirms where static files are embedded from

  **Acceptance Criteria**:
  - [ ] Go binary builds with embedded SPA
  - [ ] All 6 pages load without errors
  - [ ] `go build ./...` + `go test ./...` pass
  - [ ] `cd web && npm run build` produces clean output

  **QA Scenarios:**
  ```
  Scenario: Full integration smoke test
    Tool: Bash + Playwright
    Steps:
      1. `cd web && npm run build`
      2. `cp -r web/dist/* internal/ui/static/`
      3. `go build -o /tmp/mibee-nvr-test ./cmd/mibee-nvr/`
      4. Start server with test config
      5. Open http://localhost:9090 in Playwright
      6. Test login → recordings → recording detail → cameras → stats → settings
      7. Switch themes, verify both work
      8. Switch languages (en→zh→en), verify all text updates
      9. Take screenshots of each page → .sisyphus/evidence/task-9-integration-{page}.png
    Expected Result: All pages load, all features work, no console errors
    Failure Indicators: 404s, JS errors, broken layout, missing features
    Evidence: .sisyphus/evidence/task-9-integration-{page}.png
  ```

  **Commit**: YES
  - Message: `feat(web): complete UI redesign + security fix`
  - Files: All changed files
  - Pre-commit: `go build ./... && go test ./... && cd web && npm run build`

## Final Verification Wave (MANDATORY — after ALL implementation tasks)

- [x] F1. **Plan Compliance Audit** — `oracle`
  Read the plan end-to-end. For each "Must Have": verify implementation exists (read file, curl endpoint, run command). For each "Must NOT Have": search codebase for forbidden patterns — reject with file:line if found. Check evidence files exist in `.sisyphus/evidence/`. Compare deliverables against plan.
  Output: `Must Have [N/N] | Must NOT Have [N/N] | Tasks [N/N] | VERDICT: APPROVE/REJECT`

- [x] F2. **Code Quality Review** — `unspecified-high`
  Run `go build ./...` + `go vet ./...` + `go test ./...`. Run `cd web && npm run build`. Review all changed files for: `as any`/`@ts-ignore`, empty catches, console.log in prod, commented-out code, unused imports. Check AI slop: excessive comments, over-abstraction, generic names.
  Output: `Build [PASS/FAIL] | Lint [PASS/FAIL] | Tests [N pass/N fail] | Files [N clean/N issues] | VERDICT`

- [x] F3. **Real Manual QA** — `unspecified-high` (+ `playwright` skill)
  Start from clean state. Execute EVERY QA scenario from EVERY task. Test cross-task integration (theme switching + icons + charts working together). Test edge cases: first visit (system theme), theme toggle, mobile layout, empty state. Save evidence to `.sisyphus/evidence/final-qa/`.
  Output: `Scenarios [N/N pass] | Integration [N/N] | Edge Cases [N tested] | VERDICT`

- [x] F4. **Scope Fidelity Check** — `deep`
  For each task: read "What to do", read actual diff (git log/diff). Verify 1:1 — everything in spec was built (no missing), nothing beyond spec was built (no creep). Check "Must NOT do" compliance. Detect cross-task contamination.
  Output: `Tasks [N/N compliant] | Contamination [CLEAN/N issues] | Unaccounted [CLEAN/N files] | VERDICT`

---

## Commit Strategy

| Commit | Message | Files | Pre-check |
|--------|---------|-------|-----------|
| 1 | `fix(deps): upgrade gortsplib v5.5.2 (security)` | `go.mod, go.sum` | `go build ./... && go test ./...` |
| 2 | `style(web): redesign CSS design system (minimalist black/white)` | `web/src/app.css, web/index.html` | `npm run build` |
| 3 | `feat(web): add lucide-svelte icons + system theme detection` | `web/src/components/*.svelte, web/index.html, web/package.json` | `npm run build` |
| 4 | `feat(api): add /api/stats/trends endpoint` | `internal/api/handler.go, internal/storage/db.go` | `go test ./...` |
| 5 | `feat(web): integrate Chart.js on Stats page` | `web/src/routes/Stats.svelte` | `npm run build` |
| 6 | `style(web): optimize page layouts` | `web/src/routes/*.svelte, web/src/components/*.svelte` | `npm run build` |
| Final | `feat(web): complete UI redesign + security fix` | All above | Full build + test |

---

## Success Criteria

### Verification Commands
```bash
# Security fix
grep "gortsplib" go.mod  # Expected: v5.5.2
go build ./...           # Expected: exit 0
go test ./...            # Expected: all pass

# Frontend build
cd web && npm run build  # Expected: exit 0, no warnings

# No inline SVGs remain
grep -r '<svg' web/src/ --include='*.svelte'  # Expected: 0 matches

# No emoji icons remain in components
grep -rP '[\x{1F300}-\x{1F9FF}]' web/src/ --include='*.svelte'  # Expected: 0 matches

# Chart.js loaded
grep "chart.js" web/package.json  # Expected: found

# lucide-svelte loaded
grep "lucide-svelte" web/package.json  # Expected: found

# System theme detection
grep "prefers-color-scheme" web/index.html  # Expected: found
```

### Final Checklist
- [ ] All "Must Have" present
- [ ] All "Must NOT Have" absent
- [ ] Go build + tests pass
- [ ] Frontend builds cleanly
- [ ] All 6 pages render in both themes
- [ ] Charts display real data from backend API
- [ ] Theme follows system preference
- [ ] Responsive at 375px / 768px / 1280px
