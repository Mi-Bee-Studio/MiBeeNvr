package middleware

import "net/http"

// COOPHeaders returns a middleware that adds Cross-Origin-Opener-Policy and
// Cross-Origin-Embedder-Policy headers to every response. These headers are
// required for SharedArrayBuffer access in WebCodecs worker threads.
func COOPHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cross-Origin-Opener-Policy", "same-origin")
		w.Header().Set("Cross-Origin-Embedder-Policy", "require-corp")
		next.ServeHTTP(w, r)
	})
}
