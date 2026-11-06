package middleware

import (
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// pathPrefixes are route prefixes whose dynamic segments should be normalized
// to avoid high-cardinality log values (e.g. /api/recordings/123456789 → /api/recordings/{id}).
var pathPrefixes = []string{
	"/api/recordings/",
	"/api/cameras/",
}

// normalizePath replaces dynamic ID segments in known route prefixes with {id}.
// The id segment is REPLACED, not prefixed: the suffix after it must survive
// verbatim so multi-segment paths keep their real shape
// (/api/recordings/123/ai-status → /api/recordings/{id}/ai-status). The
// pre-fix version kept the real id after {id}, corrupting every multi-segment
// path in request logs (MiBeeVision ai-status incident, 2026-09-16).
func normalizePath(path string) string {
	for _, prefix := range pathPrefixes {
		if strings.HasPrefix(path, prefix) {
			rest := path[len(prefix):]
			if idx := strings.Index(rest, "/"); idx >= 0 {
				return prefix + "{id}" + rest[idx:]
			}
			return prefix + "{id}"
		}
	}
	return path
}

// remoteHost strips the ephemeral port from r.RemoteAddr so suppression keys
// stay stable across reconnects from the same client.
func remoteHost(remoteAddr string) string {
	if host, _, err := net.SplitHostPort(remoteAddr); err == nil {
		return host
	}
	return remoteAddr
}

// logSuppressor collapses repeated 4xx request logs sharing one
// normalized-path+status+remote-host key into the first line per window; the
// count of dropped lines rides the next logged line as suppressed=N (#46: a
// consumer write-back storm was 1593 INFO lines in one hour with the SAME
// normalized shape, pushing M5's journald budget over its retention cliff and
// truncating the forensic window). 2xx/3xx stay Debug (not consulted); 5xx
// stays Warn — those are rare and each line matters.
type logSuppressor struct {
	window time.Duration
	now    func() time.Time

	mu     sync.Mutex
	states map[string]*suppressState
}

type suppressState struct {
	last       time.Time
	suppressed int
}

func newLogSuppressor(window time.Duration) *logSuppressor {
	return &logSuppressor{window: window, now: time.Now, states: make(map[string]*suppressState)}
}

// allow reports whether this occurrence should be logged. When true it also
// returns how many identical occurrences were swallowed since the last logged
// line for this key (0 when none).
func (s *logSuppressor) allow(key string) (bool, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.states[key]
	if !ok {
		s.states[key] = &suppressState{last: s.now()}
		return true, 0
	}
	now := s.now()
	if now.Sub(st.last) >= s.window {
		n := st.suppressed
		st.suppressed = 0
		st.last = now
		return true, n
	}
	st.suppressed++
	return false, 0
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
	suppressor := newLogSuppressor(time.Minute)
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
			// Level by outcome (#685, M5 field evidence: routine 2xx polling
			// was 26% of the 24h log volume): success is Debug material, a
			// 4xx deserves a look, a 5xx is a real problem. A 4xx that keeps
			// repeating with the same shape from the same host is a stuck
			// client, not new information — first per minute only (#46).
			level := slog.LevelDebug
			switch {
			case ww.Status >= http.StatusInternalServerError:
				level = slog.LevelWarn
			case ww.Status >= http.StatusBadRequest:
				level = slog.LevelInfo
			}
			attrs := []slog.Attr{
				slog.String("trace_id", tid),
				slog.String("method", r.Method),
				slog.String("path", normalizePath(r.URL.Path)),
				slog.Int("status", ww.Status),
				slog.Duration("duration", time.Since(start)),
				slog.Int("bytes", ww.Bytes),
				slog.String("remote_addr", r.RemoteAddr),
			}
			if level == slog.LevelInfo {
				key := strings.Join([]string{
					normalizePath(r.URL.Path),
					strconv.Itoa(ww.Status),
					remoteHost(r.RemoteAddr),
				}, "|")
				if ok, suppressed := suppressor.allow(key); !ok {
					return
				} else if suppressed > 0 {
					attrs = append(attrs, slog.Int("suppressed", suppressed))
				}
			}
			logger.LogAttrs(ctx, level, "request", attrs...)
		})
	}
}
