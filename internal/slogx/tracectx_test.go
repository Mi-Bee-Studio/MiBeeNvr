package slogx

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

func TestWithTraceContext_InjectsActiveSpanIDs(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(WithTraceContext(slog.NewJSONHandler(&buf, nil)))

	tp := sdktrace.NewTracerProvider() // AlwaysSample by default
	ctx, span := tp.Tracer("test").Start(context.Background(), "op")
	logger.InfoContext(ctx, "inside span")
	span.End()

	var rec map[string]any
	if err := json.Unmarshal(buf.Bytes(), &rec); err != nil {
		t.Fatalf("output is not JSON: %v (%q)", err, buf.String())
	}
	sc := span.SpanContext()
	if got, _ := rec["otel_trace_id"].(string); got != sc.TraceID().String() {
		t.Errorf("otel_trace_id = %q, want %q", got, sc.TraceID().String())
	}
	if got, _ := rec["otel_span_id"].(string); got != sc.SpanID().String() {
		t.Errorf("otel_span_id = %q, want %q", got, sc.SpanID().String())
	}
}

func TestWithTraceContext_NoSpanNoInjection(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(WithTraceContext(slog.NewJSONHandler(&buf, nil)))
	logger.Info("bare call without span")
	if strings.Contains(buf.String(), "otel_trace_id") || strings.Contains(buf.String(), "otel_span_id") {
		t.Errorf("no active span, yet trace attrs were injected: %q", buf.String())
	}
}
