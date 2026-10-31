// Package otelx owns the OpenTelemetry provider lifecycle for the NVR.
//
// Instrumentation sites (middleware, recorder reconnect loop, merge/cleanup
// runs, GB28181 sessions, Vision pushes) only touch the GLOBAL otel.Tracer /
// propagator — no-ops until SetupTracing succeeds — so telemetry stays opt-in
// with near-zero cost when disabled. This package is the single place that
// builds exporters, providers and the resource describing this instance.
//
// Exports go over OTLP/HTTP (protobuf over plain HTTP(S)), no gRPC dialer:
// targets are user-configured URLs — an OpenTelemetry Collector, or any
// OTLP-native ingest such as VictoriaLogs' /opentelemetry/v1/logs.
package otelx

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"sync"
	"time"

	otelslog "go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkresource "go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

const (
	// shutdownTimeout bounds provider flushes; losing the tail of a shutdown
	// must never stall the exit path.
	shutdownTimeout = 5 * time.Second
	// defaultSampleRatio is the tracing head-sampling ratio when the config
	// leaves sample_ratio unset. Head sampling cannot see the outcome (error
	// biasing is tail sampling, a collector concern) — a modest ratio keeps
	// steady-state volume low while long-lived camera/merge sessions still
	// surface regularly.
	defaultSampleRatio = 0.1
)

var (
	mu             sync.Mutex
	tracerProvider *sdktrace.TracerProvider
	buildVersion   = "dev"
	instanceID     string
)

// SetBuildInfo records the identity used in OTel resource attributes. Called
// from main before app assembly: the version is an ldflags var that lives
// there, and the device ID is the stable fleet identity already exposed via
// /api/health.
func SetBuildInfo(version, instance string) {
	mu.Lock()
	defer mu.Unlock()
	if version != "" {
		buildVersion = version
	}
	instanceID = instance
}

func newResource() *sdkresource.Resource {
	attrs := []attribute.KeyValue{
		attribute.String("service.name", "mibee-nvr"),
		attribute.String("service.version", buildVersion),
	}
	if instanceID != "" {
		attrs = append(attrs, attribute.String("service.instance.id", instanceID))
	}
	res, err := sdkresource.New(context.Background(),
		sdkresource.WithAttributes(attrs...),
		sdkresource.WithFromEnv(), // OTEL_SERVICE_NAME / OTEL_RESOURCE_ATTRS still win
	)
	if err != nil {
		// A failed resource merge degrades attributes, never telemetry.
		return sdkresource.NewWithAttributes("", attrs...)
	}
	return res
}

// TracingConfig mirrors observability.tracing (resolved, not raw yaml).
type TracingConfig struct {
	Endpoint    string
	SampleRatio float64
}

// SetupTracing installs the global TracerProvider (batched OTLP/HTTP export,
// parent-based ratio sampling) and the W3C trace-context propagator. Incoming
// traceparent headers then join requests to existing distributed traces, and
// otel.Tracer calls anywhere in the process start recording.
func SetupTracing(cfg TracingConfig) error {
	if _, err := parseHTTPURL(cfg.Endpoint); err != nil {
		return fmt.Errorf("tracing endpoint: %w", err)
	}
	exp, err := otlptracehttp.New(context.Background(),
		otlptracehttp.WithEndpointURL(cfg.Endpoint))
	if err != nil {
		return fmt.Errorf("otlp trace exporter: %w", err)
	}
	ratio := cfg.SampleRatio
	if ratio <= 0 {
		ratio = defaultSampleRatio
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithResource(newResource()),
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(ratio))),
		sdktrace.WithBatcher(exp),
	)
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{}))
	mu.Lock()
	tracerProvider = tp
	mu.Unlock()
	return nil
}

// OTLPLogHandler is the remote_log.format=otlp sink: an slog.Handler bridged
// onto an OTLP/HTTP log exporter. It is a drop-in sibling of
// remotelog.Handler (same Close() contract) so the app wiring can treat both
// uniformly.
type OTLPLogHandler struct {
	inner     slog.Handler // otelslog bridge
	min       slog.Level
	lp        *sdklog.LoggerProvider
	closeOnce *sync.Once // shared by WithAttrs/WithGroup clones
}

// NewOTLPLogHandler builds the OTLP log pipeline for endpoint with a minimum
// level gate (the otelslog bridge has no level filter of its own).
func NewOTLPLogHandler(endpoint string, level slog.Level) (*OTLPLogHandler, error) {
	if _, err := parseHTTPURL(endpoint); err != nil {
		return nil, fmt.Errorf("otlp log endpoint: %w", err)
	}
	exp, err := otlploghttp.New(context.Background(),
		otlploghttp.WithEndpointURL(endpoint))
	if err != nil {
		return nil, fmt.Errorf("otlp log exporter: %w", err)
	}
	lp := sdklog.NewLoggerProvider(
		sdklog.WithResource(newResource()),
		sdklog.WithProcessor(sdklog.NewBatchProcessor(exp)), // default 512-record / 1s batches
	)
	return &OTLPLogHandler{
		inner: otelslog.NewHandler("mibee-nvr",
			otelslog.WithLoggerProvider(lp),
			otelslog.WithVersion(buildVersion)),
		min:       level,
		lp:        lp,
		closeOnce: &sync.Once{},
	}, nil
}

func (h *OTLPLogHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.min
}

func (h *OTLPLogHandler) Handle(ctx context.Context, r slog.Record) error {
	if r.Level < h.min {
		return nil
	}
	return h.inner.Handle(ctx, r)
}

func (h *OTLPLogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &OTLPLogHandler{inner: h.inner.WithAttrs(attrs), min: h.min, lp: h.lp, closeOnce: h.closeOnce}
}

func (h *OTLPLogHandler) WithGroup(name string) slog.Handler {
	return &OTLPLogHandler{inner: h.inner.WithGroup(name), min: h.min, lp: h.lp, closeOnce: h.closeOnce}
}

// Close flushes buffered records and stops the exporter. It matches the
// remotelog.Handler Close contract (called from the app service stop path).
func (h *OTLPLogHandler) Close() {
	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	h.closeOnce.Do(func() {
		if err := h.lp.Shutdown(ctx); err != nil {
			slog.Warn("otlp log provider shutdown", "error", err)
		}
	})
}

// Shutdown flushes the global tracer provider after the app services have
// stopped (final spans from shutdown paths deserve the same export chance).
// Safe to call when tracing was never enabled.
func Shutdown() {
	mu.Lock()
	tp := tracerProvider
	tracerProvider = nil
	mu.Unlock()
	if tp == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := tp.Shutdown(ctx); err != nil {
		slog.Warn("otlp trace provider shutdown", "error", err)
	}
}

// parseHTTPURL validates an OTLP/HTTP endpoint: absolute, scheme http/https.
// Exporter options read OTEL_EXPORTER_OTLP_* envs on top of this.
func parseHTTPURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("scheme must be http or https, got %q", u.Scheme)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("missing host")
	}
	return u, nil
}
