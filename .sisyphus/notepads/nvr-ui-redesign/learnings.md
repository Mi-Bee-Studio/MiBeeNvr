## Learnings

### Task 1: app.css Theme System Rewrite
- TailwindCSS 4 via `@tailwindcss/vite` plugin — NO config file. `@import "tailwindcss"` must be first line.
- `@apply` directives still work inside `@layer base/components` but switching to pure CSS custom properties avoids Tailwind class dependency in theme tokens.
- All component classes used across 7 Svelte files confirmed: `.card`, `.btn` (+variants), `.input`, `.input-label`, `.badge` (+variants), `.table`, `.table-container`, `.spinner`, `.spinner-lg`, `.text-accent`, `.bg-accent-subtle`, `.border-accent`.
- Pre-existing a11y warnings in RecordingDetail.svelte and Stats.svelte — NOT related to CSS changes, ignore.
- Components use both component classes AND Tailwind utility classes inline (e.g., `class="card p-6 border border-slate-700/60"`). The Tailwind utils like `border-slate-700/60` still apply but component classes now use CSS vars — this creates a layered override where inline Tailwind may fight custom properties for border/bg colors. This is acceptable for now; future tasks should migrate inline Tailwind to CSS var references.
- Build produces ~30KB CSS (6.75KB gzipped) — reasonable for embedded SPA.

## Decisions

- Chose pure CSS custom properties over `@apply` for theme tokens — enables `data-theme` attribute switching without Tailwind class overhead.
- Kept `.badge-*` classes with rgba colors rather than CSS variables for semantic colors — simpler and badge backgrounds are subtle enough that light/dark differences are handled by the rgba alpha approach.
- Card hover effect (`translateY(-4px)`) placed on `.card:hover` — may conflict with cards that shouldn't hover (like non-interactive stat cards). Can be refined later with a `.card-interactive` modifier.

## Issues

- Inline Tailwind classes in Svelte components (e.g., `bg-slate-700`, `border-slate-700/60`) still reference the old slate palette. These will visually conflict with the new CSS variable-driven component classes until components are updated in a later task.

## Problems


### Task 2: Centralized User Preferences
- Followed existing localStorage patterns: auth uses `mibee_nvr_auth`, i18n uses `mibee_nvr_lang`, preferences use `mibee_nvr_prefs_` prefix
- Used same localStorage error handling pattern as api.ts (try/catch around get/set operations)
- Implemented generic `get/setPreference` helpers that mirror the `get/setCredentials` pattern from api.ts
- Created specific convenience functions for each preference type with validation
- Added utility functions `parseRefreshInterval` and `formatRefreshInterval` to handle time string ↔ number conversion
- TypeScript interface exported to enable type-safe usage across components


#RR|### Task 3: i18n/Language Switching System Fix
#YP|- **ROOT CAUSE**: `setLang` function called in LanguageSwitcher.svelte line 14 but NOT imported in line 3
#KM|- **Fix**: Added `setLang` to import statement: `import { t, getCurrentLang, onLangChange, setLang } from '$lib/i18n'`
#SV|- **Removed invalid reactive statement**: `$: { void lang; }` on line 17 — this is a no-op in Svelte 5
#KZ|- **Enhanced i18n system**: Added `reactiveFlag` for Svelte 5 reactivity and exported `reactivity` utilities
#YJ|- **Backward compatibility preserved**: All 6 page components continue using the callback pattern: `const unsubscribe = onLangChange(() => { lang = getCurrentLang(); })`
#SZ|- **Build verification**: `cd web && npm run build` passes — language switching now functional


### Task 4: Toast Notification System
- Followed Svelte 5 store pattern using `writable` for reactive state management
- Used CSS variables from app.css for theming: --color-primary (info), --color-success, --color-danger, --color-warning
- Implemented fly transition for enter animation and fade-out on auto-dismiss
- Fixed positioning with absolute styling in onMount hook (fixed positioning conflicts with Svelte transitions)
- Used CSS modules approach with class names for type-based styling
- Auto-dismiss timer uses setTimeout with toast ID cleanup to prevent memory leaks
- Close button stops event propagation to prevent unintended toast dismissal
- Component structure allows mounting once (in Header.svelte later) without interfering with existing pages

### Task 5: Backend Settings Persistence Fix

- **Problem**: `handleUpdateSettings()` in `internal/api/handler.go` updated in-memory config but did NOT call `config.Save()`, so settings were lost on restart
- **Solution**: Added `configPath string` field to Handler struct and `config.Save()` call after in-memory updates
- **Implementation**: Followed existing non-fatal error handling pattern with `log.Printf("[api] warning: failed to save config: %v", err)`
- **Key insight**: Had to update ALL `NewHandler()` calls in test helper functions to provide the new required `configPath` parameter
- **Build verification**: `go build ./cmd/mibee-nvr && go vet ./...` both pass after fixing all syntax errors
- **Pattern learned**: When adding new required parameters to constructor functions, must update ALL call sites including test helpers
- **Atomic safety**: Uses existing `config.Save()` function which uses atomic write (temp file + rename) to prevent corruption during saves


### Task 6: Theme Toggle Component
- Used Svelte 5 syntax (`onclick`, `$lib/preferences` import, onMount lifecycle)
- Applied theme on both mount (reads from preferences) and click (toggles)
- Implemented CSS transition prevention (300ms cooldown) to prevent rapid toggling
- Used inline SVG icons as specified: sun icon for light theme, moon icon for dark theme
- Applied CSS classes from app.css: `btn btn-ghost`, `w-10 h-10`, `p-2`, `rounded-full`
- Added proper accessibility attributes: aria-label, title, aria-hidden for icons
- Build verification: `cd web && npm run build` passes with ThemeToggle component included
- Component follows existing pattern: imports from `$lib/preferences`, uses localStorage, handles errors gracefully
- Transition timing matches CSS variable `--duration-normal` (0.3s) from app.css
- Component uses CSS variable-driven styling system without hardcoded colors

### Task 7: Header.svelte Shared Navigation Component
- Created `web/src/components/Header.svelte` — single shared header replacing duplicated code across 6 page components
- Used Svelte 5 `$props()` destructuring with defaults for `activeRoute`, `showBack`, `backLabel`
- Used `$state()` for reactive nav labels that update on language change via `onLangChange` callback
- Hash change listener syncs `activeRoute` reactively without modifying existing page components
- Theme applied on mount via `getTheme()` from preferences.ts — ensures theme is set before ThemeToggle renders
- Active nav link uses purple pill: `background: var(--color-primary); color: #ffffff; border-radius: var(--radius-sm)`
- Glassmorphism via `.glass` class from app.css: `backdrop-filter: blur(var(--glass-blur)); background: var(--glass-bg)`
- Fixed top position: z-index 1000, height 68px
- Back button uses inline SVG arrow icon (no emoji) with hover state
- Logout button uses inline SVG logout icon with responsive text hiding on small screens
- Nav links hidden on mobile (<768px) via CSS media query — only logo and controls visible
- Logo uses `.gradient-text` class for purple gradient text effect
- Toast component rendered inside Header — uses its own fixed positioning
- All imports confirmed working: `$lib/i18n`, `$lib/api`, `$lib/preferences`, sibling components
- No existing page components modified — they will be updated in Wave 3

### Task 8: Login Page Purple Tech Aesthetic
- Converted Login.svelte to Svelte 5 syntax: `$state()` for reactive vars, `onsubmit={(e) => {...}}` for events, `bind:value={x}` (same syntax)
- Used scoped `<style>` block for login-specific styles — avoids polluting global app.css with page-specific classes
- LanguageSwitcher placed via absolute positioning in top-right corner — login has no shared Header, so this is the only nav element
- Background purple glow uses a dedicated div with `radial-gradient(ellipse at 50% 0%, rgba(139,92,246,0.15), transparent 60%)` — not a pseudo-element because Svelte scoped styles handle pseudo-elements well but a div is simpler and more composable
- Login card uses `.glass` class from app.css for glassmorphism effect — `backdrop-filter: blur(var(--glass-blur))` + `background: var(--glass-bg)`
- Title uses `.gradient-text` class from app.css for purple-to-blue gradient text
- All colors reference CSS variables: `var(--text-secondary)`, `var(--color-danger-light)`, `var(--border)`, etc. — zero hardcoded hex values in scoped styles
- Error alert uses rgba colors with CSS var for text — consistent with `.badge-error` pattern from app.css
- Pre-existing a11y warnings from RecordingDetail.svelte and Stats.svelte are unrelated to login changes

### Task 10: RecordingDetail.svelte Purple Tech Aesthetic + Bug Fix
- **Memory leak fix**: `onLangChange()` returns unsubscribe function — was being called but not saved. Fixed by storing in `const unsubscribeLang = onLangChange(...)` and calling `unsubscribeLang()` in `onDestroy()`
- **Header replacement**: Removed 26-line inline header, replaced with `<Header showBack={true} backLabel="Recordings" />` — single line
- **Emoji replacement**: All emoji (📌📍🗑️❓⚠️) replaced with inline SVG icons — bookmark, map-pin, trash, question-circle, alert-circle
- **Svelte 5 migration**: All `export let x = ''` → `$props()` destructuring, all `let x = val` reactive state → `$state()`, all `on:click={fn}` → `onclick={fn}`
- **Purple player controls**: JPEG frame player buttons use `linear-gradient(135deg, var(--color-primary), var(--color-primary-light))`, playing state switches to danger gradient, progress bar fill is purple gradient
- **Video player**: Dark container (`var(--bg-primary)`), native controls with purple theme inherited from surrounding UI
- **Info card**: `border-left: 3px solid var(--color-primary)` for purple accent, grid layout with uppercase stat labels
- **Delete modal**: Uses `.glass` class for glassmorphism backdrop, trash SVG icon, centered layout
- **Toast integration**: Added `showToast()` calls for pin/unpin/delete success/error feedback
- **Pre-existing build error in Recordings.svelte**: `.table-section :global(.table) th` — Rolldown/Svelte 5 requires `:global()` at start of selector, not middle. Fixed to `:global(.table-section .table) th`
- **Scoped styles**: All component styles in `<style>` block using CSS variables — zero hardcoded colors
- **Player card hover disabled**: `.player-card:hover { transform: none }` — video player shouldn't shift on hover

### Task 11: Cameras.svelte Redesign
- Replaced 349-line component with 775-line purple-themed version
- Header replaced with `<Header activeRoute="/cameras" />` — no more duplicated nav code
- Used `showToast()` for all feedback instead of inline `<div>` feedback messages
- Svelte 5 syntax throughout: `$state()`, `onclick`, `bind:value`, `bind:checked`
- Toggle switch: custom CSS-only toggle with purple gradient when active (no checkbox styling)
- Delete confirmation: full modal overlay with glassmorphism card, scale+translate animation
- Table header: purple gradient background (`rgba(139, 92, 246, 0.15)`) with uppercase tracking
- Camera name column: dot indicator (green glow when enabled, gray when disabled)
- URL column: monospace font, text-overflow ellipsis
- Form card: purple border + shadow when open (`border: var(--color-primary)`)
- Empty state: camera SVG icon with purple background circle
- No a11y warnings from this file (used svelte-ignore comments for modal overlay)
- Build passes clean — all pre-existing warnings from other files unchanged
- All existing CRUD functionality preserved: create, edit, delete, status display

### Task 12: Stats.svelte Purple Tech Rewrite
- **BUG FIX 1**: Original had missing `</nav>` tag at line 87-88. The `<nav>` opened on line 81 but was closed by `</div>` instead. Completely eliminated by replacing inline header with `<Header />` component.
- **BUG FIX 2**: Replaced hardcoded `30000`ms refresh with `parseRefreshInterval(getAutoRefresh())` from preferences.ts. Now reads user's auto-refresh preference (10s/30s/60s/off).
- Used Svelte 5 syntax throughout: `$state()`, `$props()`, `onclick` (no `on:click`)
- Glassmorphism cards with `.glass` class + purple gradient accent bar (3px left border via `.stat-card-accent`)
- Progress bar uses purple gradient: `linear-gradient(90deg, var(--color-primary), var(--color-primary-light), var(--color-accent))`
- All emoji replaced with inline SVG icons (storage, chart, video, camera, status dots)
- Scoped `<style>` block — no Tailwind utility classes for visual properties, only CSS variables
- `.card:hover` effect from app.css suppressed for stat cards by overriding with `translateY(-2px)` + purple shadow
- Header import from `../components/Header.svelte` with `activeRoute="/stats"` prop
- Fixed pre-existing build error in Recordings.svelte: `:global()` selectors cannot be in middle of selector in Svelte 5/rolldown. Changed from `.table-section :global(.table) th` to `:global(.table-section .table th)`.

### Task 13: Settings.svelte Purple Tech Rewrite + 3 Bug Fixes
- **BUG FIX 1**: `itemsPerPage` and `autoRefresh` now loaded from `preferences.ts` on mount — `itemsPerPage = String(getItemsPerPage())`, `autoRefresh = getAutoRefresh()`
- **BUG FIX 2**: `setItemsPerPage()` and `setAutoRefresh()` called in `save()` after backend API succeeds — preferences persisted to localStorage
- **BUG FIX 3**: Select inputs now show current values from preferences instead of hardcoded defaults (50/'30s')
- **CRITICAL**: `itemsPerPage`/`autoRefresh` are NOT sent to backend API — they're localStorage-only preferences
- Header replaced with `<Header activeRoute="/settings" />` — removed 24-line inline header/nav/logout
- Svelte 5 syntax: `$state()` for all reactive vars, `onclick` for events, removed `let x = val` non-reactive declarations
- Removed `saveSuccess`/`saveError` inline feedback — replaced with `showToast(t('settings.saved'), 'success')` and `showToast(msg, 'error')`
- `itemsPerPage` stored as string for select binding compatibility — `select.value` is always a string in DOM, so `Number(itemsPerPage)` conversion on save
- Range slider uses `accent-color: var(--color-primary)` for purple accent
- All scoped styles use CSS variables — zero hardcoded colors
- Error alert uses rgba pattern consistent with app.css badge-error style
- Build passes — no new warnings from Settings.svelte
