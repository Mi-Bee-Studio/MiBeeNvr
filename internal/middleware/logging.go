package middleware

import (
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// pathPrefixes are route prefixes whose dynamic segments should be normalized
// to avoid high-cardinality log values (e.g. /api/recordings/123456789 → /api/recordings/{id}).
var pathPrefixes = []string{
	"/api/recordings/",
	"/api/cameras/",
}

// normalizePath replaces dynamic ID segments in known route prefixes with {id}.
func normalizePath(path string) string {
	for _, prefix := range pathPrefixes {
		if strings.HasPrefix(path, prefix) {
			return prefix[:len(prefix)-1] + "/{id}" + path[len(prefix)-1:]
		}
	}
	return path
}

// RequestLogger returns a middleware that logs each request using slog.LogAttrs.
// Paths in skipPaths are not logged. Each request gets a trace_id that is
// injected into the context and the X-Request-Id response header, so downstream
// handlers can correlate their logs via LoggerFromContext(ctx).
func RequestLogger(logger *slog.Logger, skipPaths ...string) func(next http.Handler) http.Handler {
	skip := make(map[string]bool, len(skipPaths))
	for _, p := range skipPaths {
		skip[p] = true
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if skip[r.URL.Path] {
				next.ServeHTTP(w, r)
				return
			}
			// Generate or reuse trace ID for this request.
			tid := r.Header.Get(RequestIDHeader)
			if tid == "" {
				tid = GenerateTraceID()
			}
			w.Header().Set(RequestIDHeader, tid)
			ctx := ContextWithTraceID(r.Context(), tid)

			start := time.Now()
			ww := &StatusRecorder{ResponseWriter: w, Status: http.StatusOK}
			next.ServeHTTP(ww, r.WithContext(ctx))
			logger.LogAttrs(ctx, slog.LevelInfo, "request",
				slog.String("trace_id", tid),
				slog.String("method", r.Method),
				slog.String("path", normalizePath(r.URL.Path)),
				slog.Int("status", ww.Status),
				slog.Duration("duration", time.Since(start)),
				slog.Int("bytes", ww.Bytes),
				slog.String("remote_addr", r.RemoteAddr),
			)
		})
	}
}
