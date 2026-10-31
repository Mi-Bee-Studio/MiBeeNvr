package recorder

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	oteltrace "go.opentelemetry.io/otel/trace"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/backoff"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/metrics"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
)

// reconnectDeps carries the recorder-specific hooks for the shared
// auto-reconnect loop. Every pull recorder (h264/h265 via baseRecorder, mjpeg,
// http_jpeg, timelapse) drives the same retry cycle; only the connect function
// and logging/metrics handles differ.
type reconnectDeps struct {
	CameraID string
	Store    SegmentStore
	Metrics  *metrics.Metrics // optional — gauge updates skipped when nil
	Log      *slog.Logger

	// Connect performs one connection+streaming attempt. It returns the
	// terminal error (nil when the ctx was cancelled mid-attempt) and whether
	// media actually flowed (resets the backoff tier).
	Connect func(ctx context.Context) (err error, connected bool)

	// RecordError bumps the recorder's error counter ("connection", ...).
	RecordError func(errorType string)

	// SetStatus transitions the recorder status (thread-safe).
	SetStatus func(model.RecorderStatus)

	// MinBackoff floors every retry delay (after jitter, before the storage
	// override is applied via max). Zero = the shared tier ladder as-is.
	// The HTTP JPEG puller sets 5s (#711): ESP32-class MJPEG cameras treat
	// sub-5s reconnects as hammering (camera-side anti-hammer guard answers
	// 503 with exponential backoff), and their single-slot HTTP server
	// already collapses under a 1-2s reconnect storm.
	MinBackoff time.Duration
}

// nextBackoff computes one retry delay: the tiered ladder + jitter (storage
// failures get the flat ~60s storage backoff), floored at min. The floor
// never shortens a longer tier or the storage backoff (#711).
func nextBackoff(retryCount int, storageFailed bool, floor time.Duration) time.Duration {
	b := backoff.TieredBackoffWithJitter(retryCount)
	if storageFailed {
		b = backoff.StorageBackoffWithJitter()
	}
	if b < floor {
		b = floor
	}
	return b
}

// retryAfterCap bounds how long a server-advertised Retry-After may pause the
// reconnect loop: the camera guard's cooldown caps at 300s, but a hostile or
// buggy value must not silence a camera for hours.
const retryAfterCap = 10 * time.Minute

// backoffFor is nextBackoff plus the Retry-After escalation (#711): when the
// connect error carries a server-advertised cooldown (camera anti-hammer 503
// + Retry-After), waiting ANY less renews the camera-side window — the two
// backoff systems interlock into a permanent 503. Longer wins, capped.
func backoffFor(err error, retryCount int, storageFailed bool, floor time.Duration) time.Duration {
	b := nextBackoff(retryCount, storageFailed, floor)
	var hint interface{ RetryAfterHint() time.Duration }
	if errors.As(err, &hint) {
		if ra := hint.RetryAfterHint(); ra > b {
			if ra > retryAfterCap {
				ra = retryAfterCap
			}
			b = ra
		}
	}
	return b
}

// reconnectTracer spans every connect attempt (no-op until the global OTel
// provider is installed by otelx.SetupTracing).
var reconnectTracer = otel.Tracer("mibee-nvr/recorder")

// runReconnectLoop is the shared auto-reconnect cycle: call Connect, on
// failure sleep with tiered backoff + jitter (storage failures get the flat
// storage backoff), transition to StatusReconnecting, and retry until ctx is
// done. Callers keep their own wrappers around it — done-channel close, the
// final StatusStopped transition, panic recovery, inner stream-cancel
// plumbing (via the Connect closure), and any idle watchdog goroutines.
func runReconnectLoop(ctx context.Context, d reconnectDeps) {
	var retryCount int
	for {
		spanCtx, span := reconnectTracer.Start(ctx, "recorder.connect",
			oteltrace.WithAttributes(attribute.String("camera_id", d.CameraID)))
		err, connected := d.Connect(spanCtx)
		span.SetAttributes(attribute.Bool("connected", connected))
		if err != nil && ctx.Err() == nil {
			span.SetStatus(codes.Error, err.Error())
		}
		span.End()
		if ctx.Err() != nil {
			return
		}
		if connected {
			retryCount = 0
			if d.Metrics != nil {
				d.Metrics.CameraReconnectBackoffSeconds.WithLabelValues(d.CameraID).Set(0)
			}
		}
		retryCount++
		storageFailed := isStorageFailed(d.Store, d.CameraID)
		backoff := backoffFor(err, retryCount, storageFailed, d.MinBackoff)
		if d.Metrics != nil {
			d.Metrics.CameraReconnectBackoffSeconds.WithLabelValues(d.CameraID).Set(backoff.Seconds())
		}
		d.Log.Error("connection error, reconnecting",
			"camera_id", d.CameraID, "error", err,
			"backoff", backoff, "attempt", retryCount, "storage_failed", storageFailed)
		d.RecordError("connection")
		d.SetStatus(model.StatusReconnecting)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
	}
}
