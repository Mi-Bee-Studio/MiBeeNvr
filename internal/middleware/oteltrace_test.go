package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	sdktracetest "go.opentelemetry.io/otel/sdk/trace/tracetest"
	oteltrace "go.opentelemetry.io/otel/trace"
)

// One provider+recorder for the whole package: the middleware's package-level
// httpTracer is a delegating tracer that binds on the FIRST global
// SetTracerProvider (the delegate-sync is once-only), so swapping providers
// per test would silently leave spans in the first recorder.
var otTestSR = sdktracetest.NewSpanRecorder()

func init() {
	otel.SetTracerProvider(sdktrace.NewTracerProvider(
		sdktrace.WithSpanProcessor(otTestSR)))
	otel.SetTextMapPropagator(propagation.TraceContext{})
}

func otNewRouter() *chi.Mux {
	r := chi.NewRouter()
	r.Use(OTelTrace("/api/health", "/api/readyz"))
	r.Get("/api/cameras/{id}", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	r.Get("/api/health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	return r
}

func TestOTelTrace_SpanNamedByRoutePattern(t *testing.T) {
	rec := httptest.NewRecorder()
	otNewRouter().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/cameras/cam-12345", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	spans := otTestSR.Ended()
	if len(spans) == 0 {
		t.Fatal("no span recorded")
	}
	sp := spans[len(spans)-1]
	if sp.Name() != "/api/cameras/{id}" {
		t.Errorf("span name = %q, want route pattern %q", sp.Name(), "/api/cameras/{id}")
	}
	var hasRoute, hasStatus bool
	for _, a := range sp.Attributes() {
		if string(a.Key) == "http.route" {
			hasRoute = true
		}
		if string(a.Key) == "http.response.status_code" {
			hasStatus = a.Value.AsInt64() == http.StatusOK
		}
	}
	if !hasRoute {
		t.Error("missing http.route attribute")
	}
	if !hasStatus {
		t.Error("missing/incorrect http.response.status_code attribute")
	}
	otTestSR.Reset()
}

func TestOTelTrace_SkipsHealthProbes(t *testing.T) {
	rec := httptest.NewRecorder()
	otNewRouter().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/health", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if n := len(otTestSR.Ended()); n != 0 {
		t.Errorf("health probe produced %d spans, want 0", n)
	}
	otTestSR.Reset()
}

func TestOTelTrace_JoinsUpstreamTraceparent(t *testing.T) {
	const (
		upTraceID = "4bf92f3577b34da6a3ce929d0e0e4736"
		upSpanID  = "00f067aa0ba902b7"
	)
	req := httptest.NewRequest(http.MethodGet, "/api/cameras/cam-1", nil)
	req.Header.Set("traceparent", "00-"+upTraceID+"-"+upSpanID+"-01")
	otNewRouter().ServeHTTP(httptest.NewRecorder(), req)

	spans := otTestSR.Ended()
	if len(spans) == 0 {
		t.Fatal("no span recorded")
	}
	sp := spans[len(spans)-1]
	if got := sp.SpanContext().TraceID().String(); got != upTraceID {
		t.Errorf("trace id = %q, want upstream %q", got, upTraceID)
	}
	if sp.SpanContext().SpanID().String() == upSpanID {
		t.Error("server span must get its own span id, not reuse the upstream one")
	}
	if sp.Parent().SpanID().String() != upSpanID {
		t.Errorf("parent span id = %q, want upstream %q", sp.Parent().SpanID().String(), upSpanID)
	}
	otTestSR.Reset()
}

// Compile-time guard: the middleware produces a usable server span kind.
var _ oteltrace.SpanKind = oteltrace.SpanKindServer
