package middleware

import "net/http"

// COOPHeaders returns a middleware that adds Cross-Origin-Opener-Policy and
// Cross-Origin-Embedder-Policy headers to responses when the connection uses TLS.
// These headers require a secure context (HTTPS) — on plain HTTP they are silently
// ignored by browsers and only produce console warnings.
func COOPHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS != nil {
			w.Header().Set("Cross-Origin-Opener-Policy", "same-origin")
			w.Header().Set("Cross-Origin-Embedder-Policy", "require-corp")
		}
		next.ServeHTTP(w, r)
	})
}
