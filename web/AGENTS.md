# MiBee NVR — Web Frontend

**Stack**: Svelte 5.55 + Vite 8.0 + TailwindCSS 4.2 (Vite plugin, no separate config file)
**Build**: `npm run build` → `dist/` → copy to `internal/ui/static/` → rebuild Go binary to embed

## OVERVIEW

SPA frontend for MiBee NVR. Hash-based routing (no router library), localStorage auth, i18n (en/zh).

## STRUCTURE

```
src/
  main.js              # Entry — mounts App.svelte
  App.svelte           # Root component — hash-based router (#/recordings, #/cameras, etc.)
  app.css              # Global styles (Tailwind import)
  routes/
    Login.svelte       # Auth form
    Recordings.svelte  # Recording list with filters, pagination, pin/unpin
    RecordingDetail.svelte  # Video playback (H.264 <video>) or JPEG frame viewer (MJPEG)
    Cameras.svelte     # Camera CRUD management
    Stats.svelte       # Disk usage, recording/camera counts
    Settings.svelte    # Cleanup config (retention, disk threshold)
  components/
    LanguageSwitcher.svelte  # en/zh toggle
    Pagination.svelte       # Reusable pagination controls
  lib/
    api.ts             # API client — types, auth (localStorage + Basic Auth), all endpoint wrappers
    format.ts          # Date/size formatting utilities
    i18n/
      index.ts         # i18n setup — exports `t()` translate function
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

## CONVENTIONS

- **Routing**: Hash-based (`#/recordings`, `#/cameras`) — no SvelteKit, no svelte-routing. Route logic in `App.svelte` `parseRoute()`
- **Auth**: Basic Auth stored in localStorage as base64-encoded `username:password` via `btoa()`/`atob()`. Attached as `Authorization` header on every API call
- **API client**: `apiRequest<T>()` generic wrapper handles auth header injection and error parsing. Base URL is `/api` (relative, works when embedded in Go binary)
- **Path alias**: `$lib` → `./src/lib` (configured in vite.config.js)
- **Styling**: TailwindCSS 4 via `@tailwindcss/vite` plugin — no `tailwind.config.js`, no `postcss.config.js`
- **Language**: TypeScript in `src/lib/`, JavaScript in routes/main — no strict TS enforcement
- **No SvelteKit**: Plain Vite + Svelte setup. No file-based routing, no server-side rendering

## ANTI-PATTERNS (THIS FRONTEND)

- **DO NOT** add a router library — routing is manually handled in App.svelte via hash parsing
- **DO NOT** use `localStorage` for sensitive data beyond auth — base64 is NOT encryption
- **DO NOT** forget to copy `dist/` to `internal/ui/static/` after build — the Go binary serves embedded assets, not live files
- **DO NOT** create `tailwind.config.js` — TailwindCSS 4 uses CSS-based config, Vite plugin handles everything

## COMMANDS

```bash
rtk npm run dev       # Dev server with HMR (http://localhost:5173)
rtk npm run build     # Production build → dist/
# After build, embed in Go binary:
cp -r dist/* ../internal/ui/static/
cd .. && rtk make build
```
