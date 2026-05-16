## 2026-05-03 Task: i18n reactivity fix

### ROOT CAUSE (confirmed by Oracle agent)
- `currentLang` was a plain `let` variable, not `$state`
- `void reactiveFlag` gets optimized away by Svelte 5 compiler — the `void` keyword tells the compiler the value is unused
- Even `$state(0)` on reactiveFlag didn't help — `void` still optimized away
- `_langVer = $state(0)` + `onLangChange()` — mutation from callback doesn't trigger re-render because $state not tracked through function calls in Svelte 5

### SOLUTION (confirmed working)
- Make `currentLang` itself `$state`: `export const state = $state({ currentLang: 'en' })`
- Have `t()` read `state.currentLang` directly: `const lang = state.currentLang` where `lang` is actually used in `locales[lang]`
- The compiler CANNOT optimize this away because the value is used
- Svelte 5 uses runtime signal tracking (like Solid.js) — any $state getter called during template expression evaluation is captured as a dependency, even through function calls

### Key insight
- Svelte 5 $state variables ARE tracked through function calls in templates
- But ONLY if the return value is actually used (not `void`ed)
- `const lang = state.currentLang` works because `lang` is used in `locales[lang]`
- `void state.currentLang` does NOT work because `void` signals unused value

### 5 failed approaches (DO NOT REPEAT)
1. `void reactiveFlag` in plain .ts — $state not available
2. `$state(0)` reactiveFlag in .svelte.ts — void still optimized away
3. `_langVer = $state(0)` + `onLangChange` in components — mutation from callback doesn't trigger re-render
4. `data-lang-ver={_langVer}` attribute — same issue
5. `{#key langKey}` in App.svelte — $state mutation from callback not tracked by {#key}

### Playwright gotchas
- `fill()` does NOT trigger Svelte 5's `bind:value` reactive setter — form appears filled but Svelte doesn't know the value changed
- Use `page.setExtraHTTPCredentials()` for auth instead of embedding credentials in URL (which breaks `fetch()`)
- Subagents CANNOT use Playwright MCP tools — must execute directly as Atlas
- glm-4.5-air model unreliable for edits — completed in seconds without making changes

### Files changed
- `web/src/lib/i18n/index.svelte.ts` — complete rewrite ($state for currentLang)
- `web/src/components/Header.svelte` — simplified (removed $state + onLangChange boilerplate)
- `web/src/components/LanguageSwitcher.svelte` — simplified (imports state directly)
- `web/src/App.svelte` — simplified (removed {#key langKey})
- `web/src/lib/format.ts` — updated imports
- `web/src/routes/*.svelte` — unchanged (they already use {t('key')} directly)

## 2026-05-03 Task: Recordings data not rendering + deployment

### BUG: RPi was serving OLD frontend binary
- Local build had `index-Bj8BRbAA.js`, RPi served `index-CI30DCNZ.js`
- Root cause: binary wasn't redeployed after i18n fix
- Fix: rebuild Go binary with `cp -r web/dist/* internal/ui/static/` + `CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build`

### BUG: Recordings page showed 'No recordings found' despite API returning data
- API calls returned 200 with 50 recordings, but `recordings` stayed empty
- Root cause: Svelte 5 implicit `$state` for top-level `let` NOT working when assigned inside async callback in Recordings.svelte
- Other pages (Cameras, Stats, Settings) worked fine with plain `let` — issue was specific to Recordings.svelte
- Fix: Added explicit `$state()` runes: `let recordings = $state<Recording[]>([])`, `let totalRecordings = $state(0)`, `let loading = $state(false)`, `let error = $state('')`
- Also replaced bare `$effect(() => { loadRecordings(); })` with debounced version to avoid double-fire with onMount

### Playwright auth workaround
- `page.setExtraHTTPCredentials()` doesn't help — frontend uses localStorage for auth, not browser HTTP stack
- `fill()` on login form doesn't trigger Svelte 5 `bind:value`
- Solution: `page.goto('/')` → `page.evaluate(() => localStorage.setItem('mibee_nvr_auth', btoa('admin:admin')))` → `page.reload()`
- Or use `page.route('**/api/**')` to inject auth headers (but doesn't help with `isAuthenticated()` which checks localStorage)

### E2E test results (all PASS)
- Recordings page: 50 rows, pagination (2556 total), i18n EN↔ZH, theme toggle ✅
- Cameras page: 5 cameras, table with Edit/Delete, i18n EN↔ZH ✅
- Stats page: storage stats, camera stats, i18n in Chinese ✅
- Settings page: cleanup config, frontend prefs, save button, i18n in Chinese ✅
- Language switcher: switches ALL text on ALL pages reactively ✅
- Theme toggle: light↔dark via data-theme attribute ✅
- Header: nav links, language switcher, theme toggle, logout button ✅
- Login page: renders correctly ✅, form submission WORKS (fill + dispatchEvent input + click submit) → redirects to #/recordings ✅
- Mobile 375px: hamburger visible ✅, click opens menu ✅, 4 nav links (录像/摄像头/统计/设置) ✅, click link navigates + closes menu ✅, logout text hidden (icon only) ✅
- Logout flow: click 退出登录 → redirects to #/login ✅, re-login works ✅
- RecordingDetail page: video blob URL PLAYS ✅ (CSP fix deployed), camera name/date/format/duration/size metadata ✅, back button ✅
- Console errors: 0 ✅
- CSP fix: `media-src blob: 'self'` added to Content-Security-Policy header — blob: URLs now work for video playback
