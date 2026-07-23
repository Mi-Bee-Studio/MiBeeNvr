package middleware

import "net/http"

// SecurityHeaders returns a middleware that adds common security headers to every response.
// HSTS is intentionally omitted: on LAN HTTP deployments it bricks access for a year.
// If TLS is needed, use a reverse proxy (Caddy/nginx) and let it set HSTS.
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("X-XSS-Protection", "1; mode=block")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		// CSP: Svelte 5 uses inline styles for dynamic styling; unsafe-inline is required.
		// wasm-unsafe-eval: required for libde265 WASM H.265 decoder (enables H.265
		// live playback on plain HTTP without WebCodecs/HTTPS) and ONNX Runtime Web
		// (browser-side AI inference). connect-src ws:/wss:: WebSocket live streaming.
		// worker-src blob:: WasmPlayer's decoder worker.
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self' 'unsafe-inline' 'wasm-unsafe-eval'; style-src 'self' 'unsafe-inline'; img-src blob: data: 'self'; media-src blob: 'self'; connect-src 'self' ws: wss:; worker-src 'self' blob:")
		next.ServeHTTP(w, r)
	})
}
