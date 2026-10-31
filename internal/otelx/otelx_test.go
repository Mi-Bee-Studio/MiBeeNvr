package otelx

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"go.opentelemetry.io/otel"
)

func TestSetupTracing_BadEndpoint(t *testing.T) {
	for _, ep := range []string{"", "not-a-url", "ftp://collector:4318/v1/traces", "http://"} {
		if err := SetupTracing(TracingConfig{Endpoint: ep}); err == nil {
			t.Errorf("endpoint %q: expected validation error, got nil", ep)
		}
	}
}

func TestNewOTLPLogHandler_BadEndpoint(t *testing.T) {
	if _, err := NewOTLPLogHandler("bogus", slog.LevelInfo); err == nil {
		t.Error("expected validation error for bogus endpoint")
	}
}

func TestOTLPLogHandler_LevelGate(t *testing.T) {
	// Port 1 on loopback: connection refused fast — the exporter never
	// delivers, Close() just drains a failed batch.
	h, err := NewOTLPLogHandler("http://127.0.0.1:1/v1/logs", slog.LevelWarn)
	if err != nil {
		t.Fatal(err)
	}
	if h.Enabled(context.Background(), slog.LevelInfo) {
		t.Error("info must be gated at minimum warn")
	}
	if !h.Enabled(context.Background(), slog.LevelError) {
		t.Error("error must pass the gate")
	}
	// Below-minimum records are dropped by Handle, not by the bridge.
	if err := h.Handle(context.Background(), slog.Record{}); err != nil {
		t.Errorf("below-minimum Handle: %v", err)
	}
	h.Close()
	h.Close() // idempotent (shared once across WithAttrs/WithGroup clones)
}

func TestOTLPLogHandler_ExportsToOTLPEndpoint(t *testing.T) {
	var posts atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n, _ := io.Copy(io.Discard, r.Body)
		if n > 0 {
			posts.Add(1)
		}
	}))
	defer srv.Close()

	h, err := NewOTLPLogHandler(srv.URL+"/v1/logs", slog.LevelInfo)
	if err != nil {
		t.Fatal(err)
	}
	slog.New(h).Info("hello otlp", "key", "value")
	h.Close() // flush
	if posts.Load() == 0 {
		t.Error("no OTLP log export POST received after Close flush")
	}
}

func TestSetupTracing_ExportsSpansToOTLPEndpoint(t *testing.T) {
	var posts atomic.Int64
	var paths atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n, _ := io.Copy(io.Discard, r.Body)
		if n > 0 {
			posts.Add(1)
			paths.Store(r.URL.Path)
		}
	}))
	defer srv.Close()

	if err := SetupTracing(TracingConfig{Endpoint: srv.URL + "/v1/traces", SampleRatio: 1}); err != nil {
		t.Fatal(err)
	}
	// Obtain the tracer AFTER setup: the global delegating tracer binds to
	// the installed provider on first use after SetTracerProvider.
	_, span := otel.Tracer("otelx-test").Start(context.Background(), "e2e.span")
	span.End()
	Shutdown() // flush
	if posts.Load() == 0 {
		t.Fatal("no OTLP trace export POST received after Shutdown flush")
	}
	if got := paths.Load(); got != "/v1/traces" {
		t.Errorf("export path = %v, want /v1/traces", got)
	}
	Shutdown() // no-op after flush
}
