package middleware

import (
	"net/http"
	"strings"
)

// defaultFrameAncestors is used when no explicit frame-ancestors policy is
// configured. 'self' permits the app to be framed only by pages of its own
// origin — the same intent as the legacy X-Frame-Options: DENY default
// (nothing else frames it), but expressed via the modern CSP directive so it
// can be relaxed for trusted embedders (e.g. the fnOS desktop).
const defaultFrameAncestors = "'self'"

// SecurityHeaders returns a middleware that adds common security headers to
// every response. HSTS is intentionally omitted: on LAN HTTP deployments it
// bricks access for a year. If TLS is needed, use a reverse proxy
// (Caddy/nginx) and let it set HSTS.
//
// frameAncestors controls who may embed the UI in an <iframe> via the CSP
// frame-ancestors directive. It accepts a space-separated list of sources
// (e.g. "'self'", "http://192.168.1.10 http://192.168.1.11"). An empty value
// falls back to 'self' (no cross-origin framing). This replaces the legacy
// X-Frame-Options header, which cannot express a cross-origin allow-list and
// thus broke embedding in the fnOS desktop (the desktop page is served from a
// different origin than the NVR's :9090, so even SAMEORIGIN rejected it).
func SecurityHeaders(frameAncestors string) func(http.Handler) http.Handler {
	ancestors := strings.TrimSpace(frameAncestors)
	if ancestors == "" {
		ancestors = defaultFrameAncestors
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Content-Type-Options", "nosniff")
			// X-Frame-Options is intentionally NOT set: CSP frame-ancestors is its
			// modern successor and is strictly more expressive (allow-list vs the
			// single-origin SAMEORIGIN / blanket DENY). Browsers that understand
			// frame-ancestors ignore X-Frame-Options anyway; setting both can only
			// make the policy stricter than intended.
			w.Header().Set("X-XSS-Protection", "1; mode=block")
			w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
			w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
			// CSP: Svelte 5 uses inline styles for dynamic styling; unsafe-inline is required.
			// wasm-unsafe-eval: required for libde265 WASM H.265 decoder (enables H.265
			// live playback on plain HTTP without WebCodecs/HTTPS) and ONNX Runtime Web
			// (browser-side AI inference). connect-src ws:/wss:: WebSocket live streaming.
			// worker-src blob:: WasmPlayer's decoder worker.
			// frame-ancestors: controls cross-origin embedding (e.g. fnOS desktop iframe).
			w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self' 'unsafe-inline' 'wasm-unsafe-eval'; style-src 'self' 'unsafe-inline'; img-src blob: data: 'self'; media-src blob: 'self'; connect-src 'self' ws: wss:; worker-src 'self' blob:; frame-ancestors "+ancestors)
			next.ServeHTTP(w, r)
		})
	}
}
