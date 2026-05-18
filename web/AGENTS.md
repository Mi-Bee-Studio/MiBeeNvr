# MiBee NVR — Frontend Context

## OVERVIEW
Svelte 5 SPA frontend. Hash-based routing, dark/light theme, i18n (EN/ZH), Chart.js stats, HLS live streaming. Builds to web/dist/, embedded in Go binary via go:embed.

## STRUCTURE
```
src/
  App.svelte           # Root — hash routing, auth guard, route rendering
  main.js              # Entry — i18n init, mount App
  routes/              # 9 page components (see below)
  components/          # 8 reusable components
  lib/
    api.ts             # Complete REST API client (830 lines, 50+ functions)
    i18n/              # en.json (477 keys), zh.json, index.svelte.ts ($state reactive)
    preferences.ts     # localStorage preferences (items/page, auto-refresh, theme)
    format.ts          # formatDate, formatDuration, formatFileSize
    hls-config.ts      # hls.js configuration
    hls-errors.ts      # Error handling, zombie detection
    toast.ts           # Toast notification system
    components/        # DiscoveryPanel.svelte (ONVIF + Xiaomi discovery)
```

## ROUTES
| Route | File | Lines | Purpose |
|-------|------|-------|---------|
| #/login | Login.svelte | — | Auth with username/password |
| #/ | Dashboard.svelte | — | Live monitoring grid (1-4 cameras), HLS, PTZ |
| #/recordings | Recordings.svelte | 600 | List with filters, pagination, batch delete |
| #/recording/:id | RecordingDetail.svelte | 771 | MP4/MJPEG playback, download, keyboard nav |
| #/cameras | Cameras.svelte | 1144 | Camera CRUD, ONVIF discovery, Xiaomi cloud auth |
| #/live/:id | LiveView.svelte | — | Single camera HLS, fullscreen, PTZ |
| #/stats | Stats.svelte | 761 | Chart.js charts, storage trends, system health |
| #/settings | Settings.svelte | 585 | Cleanup policy, WebDAV, merge, features |
| #/archives | Archives.svelte | 536 | Archive groups, playback, delete |

## WHERE TO LOOK
| Task | Location | Notes |
|------|----------|-------|
| Add page route | App.svelte route map + new file in routes/ | Hash-based, no router library |
| Change API call | lib/api.ts | All REST calls centralized, Basic auth |
| Add i18n key | lib/i18n/en.json + zh.json | Key format: category.specific |
| Change theme | lib/preferences.ts + app.css + ThemeToggle.svelte | CSS variables, localStorage |
| Fix HLS player | components/VideoPlayer.svelte | hls.js integration, zombie detection |
| Fix PTZ control | components/PtzControl.svelte | Pointer events, move/stop API |
| Add chart | routes/Stats.svelte | Chart.js, see existing patterns |
| Fix recording playback | routes/RecordingDetail.svelte | MP4 blob URL or MJPEG canvas |

## CONVENTIONS
- **Svelte 5 reactivity**: $state(), $derived(), $effect() — no legacy stores
- **Hash routing**: Custom in App.svelte (window.onhashchange), no router library
- **Auth**: Basic auth credentials in localStorage, sent via Authorization header in api.ts
- **Theme**: data-theme attribute on <html>, set inline in index.html (prevents FOUC)
- **i18n**: t('key', {param}) function, $state reactive lang switching, localStorage persistence
- **Tailwind CSS v4**: @tailwindcss/vite plugin, no tailwind.config.js
- **Icons**: lucide-svelte throughout
- **Build**: vite build → web/dist/ → copied to internal/ui/static/ → go:embed

## E2E TESTING (../e2e-tests/)
- **Framework**: Playwright (Chromium, **HEADED MODE REQUIRED**)
- **Config**: e2e-tests/playwright.config.ts
- **Run**: cd e2e-tests && npx playwright test --headed
- **Test files**: 6 spec files in e2e-tests/tests/
- **Anti-pattern**: NEVER run frontend E2E tests headless — visual bugs go undetected

## ANTI-PATTERNS
- **DO NOT** hardcode UI text — use t('key') from i18n
- **DO NOT** use Svelte 4 syntax ($: declarations, legacy stores) — use $state/$derived/$effect
- **DO NOT** skip i18n key in both en.json AND zh.json
- **DO NOT** use os.ReadFile()+w.Write() pattern in frontend — use api.ts functions
- **DO NOT** run E2E tests headless — visual regressions invisible, HLS/canvas rendering differs
- **DO NOT** embed full video URLs — use auth blob URLs from api.ts