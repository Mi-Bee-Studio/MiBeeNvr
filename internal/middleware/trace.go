package middleware

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
)

// traceIDKey is the context key for the per-request trace ID.
type traceIDKey struct{}

// RequestIDHeader is the HTTP response/request header carrying the trace ID.
const RequestIDHeader = "X-Request-Id"

// GenerateTraceID returns a short random hex string suitable for log correlation.
// Uses crypto/rand for unpredictability (16 bytes → 32 hex chars, truncated to 16).
func GenerateTraceID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// ContextWithTraceID returns a new context with the trace ID embedded.
func ContextWithTraceID(ctx context.Context, traceID string) context.Context {
	return context.WithValue(ctx, traceIDKey{}, traceID)
}

// TraceIDFromContext extracts the trace ID from the context.
// Returns empty string if no trace ID is present.
func TraceIDFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(traceIDKey{}).(string); ok {
		return v
	}
	return ""
}

// LoggerFromContext returns a slog.Logger pre-loaded with the trace_id attribute
// from the context. If no trace ID is present, the logger is returned without it.
// Use this in handlers to ensure every log line is correlated to its request:
//
//	logger := middleware.LoggerFromContext(r.Context())
//	logger.Info("processing recording", "id", recID)
func LoggerFromContext(ctx context.Context) *slog.Logger {
	tid := TraceIDFromContext(ctx)
	if tid == "" {
		return slog.Default()
	}
	return slog.Default().With("trace_id", tid)
}

// TraceMiddleware injects a trace ID into the request context and response header.
// If the incoming request already has an X-Request-Id header, it is reused
// (allows upstream proxies or clients to propagate correlation IDs).
func TraceMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tid := r.Header.Get(RequestIDHeader)
		if tid == "" {
			tid = GenerateTraceID()
		}
		w.Header().Set(RequestIDHeader, tid)
		ctx := ContextWithTraceID(r.Context(), tid)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
