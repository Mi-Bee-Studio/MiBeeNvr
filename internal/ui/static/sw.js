// MiBee NVR Service Worker
// App Shell: Cache First (fast loads, works offline for UI)
// API: Network First (fresh data, fallback to cache when offline)
// Media streams (HLS/FLV/WebRTC): Never cached (always network)

const CACHE_VERSION = 'mibee-nvr-v2';
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

// Activate: clean up old caches
self.addEventListener('activate', (event) => {
  event.waitUntil(
    caches.keys().then((keys) =>
      Promise.all(keys.filter(k => k !== CACHE_VERSION).map(k => caches.delete(k)))
    )
  );
  self.clients.claim();
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
