# Learnings

## Settings Page Implementation
- Svelte 4 syntax confirmed: `export let`, `on:click`, `{#if}`, `{#each}` — no runes
- Header nav pattern: `<nav class="flex gap-4">` with `<a>` links; active page gets `text-cyan-500 font-medium`
- Card pattern: `card p-6 border border-slate-700/60` for content sections
- Table pattern: `table-container` + `table` class from app.css
- CSS toggle switch built with Tailwind utility classes (translate-x-6/translate-x-1 + bg color switching)
- API returns `listRecordings` as `{recordings: [...], total: N}` — do not change this format
- `apiRequest<T>()` handles auth headers automatically
- Vite build: ~540ms, 119 modules, all clean

## i18n Internationalization
- i18n API: `getCurrentLang()` (NOT `currentLang()`), `setLang()`, `onLangChange()`, `t(key, params)`
- Reactivity pattern: `let lang = getCurrentLang(); onLangChange(() => { lang = getCurrentLang(); });` triggers Svelte re-renders
- `t()` supports interpolation: `t('key', { name: 'value' })` replaces `{name}` in translation string
- format.ts functions: `formatDate`, `formatDuration`, `formatFileSize` — all import `getCurrentLang()` for locale-aware output
- zh duration format: `1时 30分 15秒` vs en: `1h 30m 15s`
- zh date format uses `zh-CN` locale via `toLocaleString`
- LanguageSwitcher uses `<select>` with `on:change` calling `setLang()`
- Brand name "MiBee NVR" is kept hardcoded (not i18n'd)
- Login page does NOT get LanguageSwitcher (pre-login, no header)
- RecordingDetail has nav links + LanguageSwitcher but no Logout button (different header layout)

## API Test Infrastructure (handler_test.go)
- `TestHandler(db, store)` passes nil config — GET/PUT settings return 500 "config not available"
- For settings tests with real config, use `newHandlerWithConfig(db, store, cfg)` (custom helper)
- For auth+config tests, use `newHandlerWithConfigAndAuth(db, store, user, hash, cfg)`
- Test helper names must NOT start with "Test" or Go treats them as test functions
- `ast_grep_replace` leaves `$$$` metavariables when the original call doesn't match a full AST node — prefer manual edit for simple renames
- SQLite: OFFSET without LIMIT is invalid SQL — `OFFSET 999` alone causes DB error; always test with `limit=N&offset=M`
- Frames handler: only works with `format=mjpeg` recordings; returns 400 for H.264
- Frames handler: FilePath must be a directory; file path returns 404
- Frames handler: filters .jpg/.jpeg case-insensitively, sorts by filename, assigns sequential indices
- Settings handler validates: retention_days >= 1, disk_threshold_percent 1-100, check_interval must parse as duration
- Total test count: 61 tests in handler_test.go (was 26, added 35 new)
