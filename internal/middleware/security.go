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
		// CSP: Svelte 5 uses inline styles for dynamic styling; unsafe-inline is required
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; img-src blob: data: 'self'; media-src blob: 'self'")
		next.ServeHTTP(w, r)
	})
}
