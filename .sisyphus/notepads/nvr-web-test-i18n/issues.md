(empty)

## Bug Fix Session - 4 Critical Bugs

### Bug 1: Pagination event mismatch
- **Root cause**: Pagination.svelte uses prop callback `onPageChange`, but Recordings.svelte used `on:page-change` Svelte event syntax
- **Fix**: Changed Recordings.svelte to pass callback as prop: `onPageChange={handlePageChange}`
- **Approach**: Option B (prop-based) - simplest, no changes to Pagination component needed

### Bug 2: Settings save response type mismatch
- **Root cause**: Backend PUT /api/settings returns `{status: "updated"}`, but frontend assigned it to `settings` and accessed `.cameras` (undefined → crash)
- **Fix**: After PUT, re-fetch via GET. Also changed `updateSettings` return type in api.ts from `SettingsConfig` to `{ status: string }`

### Bug 3: JPEG frame download backend support
- **Root cause**: `handleDownloadRecording` had no `?frame=N` query param support
- **Fix**: Added frame parameter parsing before existing directory handling. Added `isImageFile` helper. Added 5 unit tests.
- **Tests**: TestDownloadFrame_Success, TestDownloadFrame_FirstFrame, TestDownloadFrame_OutOfRange, TestDownloadFrame_InvalidIndex, TestDownloadFrame_IgnoredForH264

### Bug 4: onLangChange memory leaks
- **Root cause**: All components called `onLangChange()` but discarded the returned unsubscribe function
- **Fix**: Import `onDestroy`, store unsubscribe return value, call it in `onDestroy` callback
- **Files**: Login.svelte, Recordings.svelte, Stats.svelte, Settings.svelte, LanguageSwitcher.svelte
