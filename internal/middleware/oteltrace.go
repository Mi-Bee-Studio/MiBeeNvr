package middleware

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	oteltrace "go.opentelemetry.io/otel/trace"
)

// httpTracer is resolved via the global provider: before otelx.SetupTracing
// runs (tracing disabled) it is a no-op, and the delegating tracer re-binds
// once a real provider is installed.
var httpTracer = otel.Tracer("mibee-nvr/http")

// OTelTrace creates a server span for every request. W3C traceparent headers
// are extracted first, so requests join upstream distributed traces when
// present. The span is renamed to the matched chi route pattern after the
// handler returns (chi installs its RouteContext before the middleware chain
// runs, but the pattern is only known post-match) — id-bearing routes keep a
// bounded cardinality; the pre-normalized path stays as fallback.
//
// Mounted unconditionally: with tracing disabled the global tracer is a no-op
// and this costs a context lookup per request.
func OTelTrace(skipPaths ...string) func(http.Handler) http.Handler {
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
			ctx := otel.GetTextMapPropagator().Extract(r.Context(), propagation.HeaderCarrier(r.Header))
			path := normalizePath(r.URL.Path)
			ctx, span := httpTracer.Start(ctx, r.Method+" "+path,
				oteltrace.WithSpanKind(oteltrace.SpanKindServer),
				oteltrace.WithAttributes(
					attribute.String("http.request.method", r.Method),
					attribute.String("url.path", path),
				))
			defer span.End()

			origCtx := r.Context() // chi's RouteContext lives here
			ww := &StatusRecorder{ResponseWriter: w, Status: http.StatusOK}
			next.ServeHTTP(ww, r.WithContext(ctx))

			if rctx := chi.RouteContext(origCtx); rctx != nil {
				if pat := rctx.RoutePattern(); pat != "" {
					span.SetName(pat)
					span.SetAttributes(attribute.String("http.route", pat))
				}
			}
			status := ww.Status
			span.SetAttributes(attribute.Int("http.response.status_code", status))
			if status >= http.StatusInternalServerError {
				span.SetStatus(codes.Error, http.StatusText(status))
			}
		})
	}
}
