package slogx

import (
	"context"
	"log/slog"

	oteltrace "go.opentelemetry.io/otel/trace"
)

// WithTraceContext wraps next so records emitted while an OpenTelemetry span
// is active in the context carry otel_trace_id/otel_span_id attributes.
//
// The names are deliberately distinct from the request-scoped trace_id
// (X-Request-Id correlation) that RequestLogger injects — both IDs can then
// coexist in one record without overwriting each other, and journald grep
// habits keep working. Trace context also flows into the OTLP log bridge
// (remote_log.format=otlp) automatically, making logs and spans joinable in
// the backend.
//
// Zero overhead when no span is active (a context lookup + type assertion).
func WithTraceContext(next slog.Handler) slog.Handler {
	return &traceCtxHandler{next: next}
}

type traceCtxHandler struct {
	next slog.Handler
}

func (h *traceCtxHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.next.Enabled(ctx, level)
}

func (h *traceCtxHandler) Handle(ctx context.Context, r slog.Record) error {
	if sc := oteltrace.SpanContextFromContext(ctx); sc.IsValid() {
		r.AddAttrs(
			slog.String("otel_trace_id", sc.TraceID().String()),
			slog.String("otel_span_id", sc.SpanID().String()),
		)
	}
	return h.next.Handle(ctx, r)
}

func (h *traceCtxHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &traceCtxHandler{next: h.next.WithAttrs(attrs)}
}

func (h *traceCtxHandler) WithGroup(name string) slog.Handler {
	return &traceCtxHandler{next: h.next.WithGroup(name)}
}
