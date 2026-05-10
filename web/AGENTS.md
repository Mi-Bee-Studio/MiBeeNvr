# MiBee NVR — Web Frontend

**Stack**: Svelte 5.55 + Vite 8.0 + TailwindCSS 4.2 (Vite plugin, no separate config file)
**Build**: `npm run build` → `dist/` → copy to `internal/ui/static/` → rebuild Go binary to embed

## OVERVIEW

SPA frontend for MiBee NVR. Hash-based routing (no router library), localStorage auth, i18n (en/zh), dark/light theme, HLS live view, Chart.js stats.

## STRUCTURE

```
src/
  main.js              # Entry — mounts App.svelte
  App.svelte           # Root component — hash-based router (#/recordings, #/cameras, #/dashboard, etc.)
  app.css              # Global styles — CSS custom properties for dark/light theme + Tailwind import
  routes/
    Login.svelte       # Auth form
    Recordings.svelte  # Recording list with filters, pagination, pin/unpin, batch operations
    RecordingDetail.svelte  # Video playback (H.264 <video>) or JPEG frame viewer (MJPEG) with lazy loading
    Cameras.svelte     # Camera CRUD management, inline name editing, ONVIF discovery
    Dashboard.svelte   # Camera grid with snapshots (auto-refresh 3s) + HLS live view
    LiveView.svelte    # Full-screen HLS live view with hls.js, auth header injection
    Stats.svelte       # Chart.js-powered storage trends and per-camera statistics
    Settings.svelte    # Cleanup config (retention, disk threshold), merge settings
  components/
    Header.svelte            # Navigation bar, mobile menu, route-aware
    Pagination.svelte       # Reusable pagination controls (Svelte 4 style $: reactive)
    LanguageSwitcher.svelte  # en/zh toggle via `$lib/i18n` setLang()
    ThemeToggle.svelte      # Dark/light/system theme switch, syncs across tabs
    Toast.svelte            # Toast notification display with fly transitions
    PtzControl.svelte       # PTZ directional pad for ONVIF cameras
  lib/
    api.ts             # API client — types, auth (localStorage + Basic Auth), all endpoint wrappers, XHR downloads
    format.ts          # Date/size/duration formatting utilities
    preferences.ts     # localStorage preferences (items_per_page, auto_refresh, theme)
    toast.ts           # Svelte store-based toast notification system
    i18n/
      index.svelte.ts  # i18n setup — exports `t()` and reactive `state` via $state (Svelte 5 rune file)
      en.json          # English strings
      zh.json          # Chinese strings
  assets/              # Static images (hero, svelte/vite logos)
```

## WHERE TO LOOK

| Task | Location | Notes |
|------|----------|-------|
| Add new page | `src/routes/` | Create component, add route case in `App.svelte` parseRoute() |
| Add API call | `src/lib/api.ts` | Add typed function, export it |
| Add i18n string | `src/lib/i18n/en.json` + `zh.json` | Add key to both, use `t('key')` in components |
| Change auth flow | `src/lib/api.ts` | storeCredentials/getCredentials/clearCredentials in localStorage |
| Modify styles | Tailwind inline classes | No separate CSS files except `app.css` for imports |
| Add shared component | `src/components/` | Import with relative path |
| Add toast notification | `src/lib/toast.ts` | `showToast(message, type)` — auto-dismisses after 3s |
| Change user preference | `src/lib/preferences.ts` | getPreference/setPreference with localStorage backing |
| HLS live view | `routes/Dashboard.svelte` or `LiveView.svelte` | hls.js with `xhrSetup` for auth, `enableWorker: false` for RPi |
| Theme support | `src/components/ThemeToggle.svelte` | CSS vars on `:root` and `[data-theme="light"]` |

## CONVENTIONS

- **Routing**: Hash-based (`#/recordings`, `#/cameras`, `#/dashboard`) — no SvelteKit, no svelte-routing. Route logic in `App.svelte` `parseRoute()`
- **Auth**: Basic Auth stored in localStorage as base64-encoded `username:password` via `btoa()`/`atob()`. Attached as `Authorization` header on every API call
- **API client**: `apiRequest<T>()` generic wrapper handles auth header injection and error parsing. Base URL is `/api` (relative, works when embedded in Go binary)
- **Path alias**: `$lib` → `./src/lib` (configured in vite.config.js)
- **Styling**: TailwindCSS 4 via `@tailwindcss/vite` plugin — no `tailwind.config.js`, no `postcss.config.js`. Theme via CSS custom properties in `app.css`
- **Language**: TypeScript in `src/lib/`, JavaScript in routes/main — no strict TS enforcement
- **No SvelteKit**: Plain Vite + Svelte setup. No file-based routing, no server-side rendering
- **Svelte 5 runes**: `$state<T>()` for all component state, `$derived()` for computed values, `$effect()` for reactive side effects, `$props()` for typed component props
- **Data fetching**: `$effect()` with debounce pattern for reactive filter changes, `AbortController` for canceling in-flight requests
- **File downloads**: XHR with `responseType: 'blob'` + `onprogress` callback for large files (MP4). Small files (JPEG) can use fetch→blob
- **HLS playback**: hls.js with `xhrSetup` for auth header injection. `enableWorker: false` for RPi browser compat. Dynamic import for code splitting
- **Lazy loading**: MJPEG frames use window-based lazy loading (batch 50, unload outside window of ±20 frames)
- **Direct DOM**: Performance-critical paths (canvas rendering, progress bars) bypass Svelte reactivity with direct DOM manipulation
- **Mixed Svelte versions**: Most components use Svelte 5 runes. `Pagination.svelte` still uses Svelte 4 `$:` reactive statements
- **Toast system**: Svelte `writable` store (not $state) — `showToast(message, type)` auto-dismisses after 3s
- **i18n reactivity**: `index.svelte.ts` uses `$state` for `currentLang` — all components reading `t()` re-render on language change
- **Chart.js**: Theme-aware chart colors, recreated on theme change via MutationObserver on `data-theme` attribute

## ANTI-PATTERNS (THIS FRONTEND)

- **DO NOT** add a router library — routing is manually handled in App.svelte via hash parsing
- **DO NOT** use `localStorage` for sensitive data beyond auth — base64 is NOT encryption
- **DO NOT** forget to copy `dist/` to `internal/ui/static/` after build — the Go binary serves embedded assets, not live files
- **DO NOT** create `tailwind.config.js` — TailwindCSS 4 uses CSS-based config, Vite plugin handles everything
- **DO NOT** embed credentials in URLs (`//user:pass@host/path`) — modern browsers block embedded auth in `<video>` src, `<img>` src, and `<a>` href downloads
- **DO NOT** use `fetch()` for large file downloads — no progress callback; use XHR (`XMLHttpRequest`) with `onprogress` for download progress UI
- **DO NOT** use `fetch→blob→URL.createObjectURL→<a>.click()` for MP4 downloads — user sees no progress, entire file loads to RAM; use XHR with `onprogress` callback + progress bar UI

## COMMANDS

```bash
rtk npm run dev       # Dev server with HMR (http://localhost:5173)
rtk npm run build     # Production build → dist/
# After build, embed in Go binary:
cp -r dist/* ../internal/ui/static/
cd .. && rtk make build
```
