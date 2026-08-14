// MiBee NVR Service Worker
// App Shell: Cache First (fast loads, works offline for UI)
// API: Network First (fresh data, fallback to cache when offline)
// Media streams (HLS/FLV/WebRTC): Never cached (always network)
//
// CACHE_VERSION is rewritten at build time by the vite sw-version plugin to a
// unique per-build value (timestamp). This is CRITICAL: without a new version
// per deploy, the SW keeps serving stale JS chunks (WasmPlayer/orchestrator)
// from the old cache even after a new binary ships — a user's browser would
// run the OLD frontend indefinitely, masking all frontend fixes.
//
// During `vite dev` the placeholder is NOT rewritten, so we fall back to a
// dev-only random version (each reload is a new cache — fine for dev).
const CACHE_VERSION = 'mibee-nvr-1786723453195';
const APP_SHELL = [
  '/',
  '/index.html',
  '/favicon.svg',
  '/manifest.json',
];

// Install: pre-cache app shell
self.addEventListener('install', (event) => {
  event.waitUntil(
    caches.open(CACHE_VERSION).then((cache) => cache.addAll(APP_SHELL))
  );
  self.skipWaiting();
});

// Activate: clean up ALL caches that aren't the current build's version.
// (Previously this only deleted caches with a *different known* name, but
// since CACHE_VERSION now changes every build, any older cache — including the
// old 'mibee-nvr-v2' and any prior build's timestamped cache — must be purged,
// otherwise stale chunks survive and the user runs old code.)
self.addEventListener('activate', (event) => {
  event.waitUntil(
    caches.keys().then((keys) =>
      Promise.all(keys.filter(k => k !== CACHE_VERSION).map(k => caches.delete(k)))
    ).then(() => self.clients.claim())
  );
});

// Fetch: strategy depends on request type
self.addEventListener('fetch', (event) => {
  const req = event.request;
  const url = new URL(req.url);

  // Only handle same-origin requests
  if (url.origin !== self.location.origin) return;

  // Skip non-GET (POST/PUT/DELETE always go to network)
  if (req.method !== 'GET') return;

  // Media streams: always network (HLS m3u8, FLV, WebSocket, WebRTC)
  if (url.pathname.includes('/stream') || url.pathname.includes('/api/cameras/') && url.pathname.includes('stream')) {
    return; // Let browser handle normally
  }

  // AI model files (/models/*.onnx): NEVER cached by the SW. The app manages
  // its own model cache (Cache API 'mibee-nvr-ai-models') with strict integrity
  // checks (Content-Length) and self-heal. If the SW also cache-first'd these,
  // a stale/truncated model would survive forever here — bypassing the app's
  // integrity gate — and ONNX Runtime would fail with "protobuf parsing failed"
  // (issue #109) on every load, with no way to recover without clearing site
  // data. Let the request fall through to the browser (the app's fetch still
  // goes through its own caching layer in runtime.ts).
  if (url.pathname.startsWith('/models/')) {
    return; // Let browser handle normally (app-level Cache API still applies)
  }

  // API requests: Network First (fall back to cache when offline)
  if (url.pathname.startsWith('/api/')) {
    event.respondWith(
      fetch(req)
        .then((resp) => {
          // Cache successful GET responses for offline fallback
          if (resp.ok) {
            const clone = resp.clone();
            caches.open(CACHE_VERSION).then((cache) => cache.put(req, clone));
          }
          return resp;
        })
        .catch(() => caches.match(req).then((cached) => cached || new Response('Offline', { status: 503 })))
    );
    return;
  }

  // HTML documents (index.html, routes): Network First to get latest version,
  // fallback to cache when offline. This ensures app updates are picked up.
  if (req.headers.get('accept')?.includes('text/html') || url.pathname === '/' || url.pathname === '/index.html') {
    event.respondWith(
      fetch(req)
        .then((resp) => {
          if (resp.ok) {
            const clone = resp.clone();
            caches.open(CACHE_VERSION).then((cache) => cache.put(req, clone));
          }
          return resp;
        })
        .catch(() => caches.match(req).then((cached) => cached || caches.match('/index.html')))
    );
    return;
  }

  // Static assets (JS/CSS with hash filenames): Cache First, fallback to network
  event.respondWith(
    caches.match(req).then((cached) => {
      if (cached) return cached;
      return fetch(req).then((resp) => {
        if (resp.ok) {
          const clone = resp.clone();
          caches.open(CACHE_VERSION).then((cache) => cache.put(req, clone));
        }
        return resp;
      });
    })
  );
});
