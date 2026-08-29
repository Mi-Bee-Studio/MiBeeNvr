package recorder

import (
	"context"
	"log/slog"
	"time"

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
}

// runReconnectLoop is the shared auto-reconnect cycle: call Connect, on
// failure sleep with tiered backoff + jitter (storage failures get the flat
// storage backoff), transition to StatusReconnecting, and retry until ctx is
// done. Callers keep their own wrappers around it — done-channel close, the
// final StatusStopped transition, panic recovery, inner stream-cancel
// plumbing (via the Connect closure), and any idle watchdog goroutines.
func runReconnectLoop(ctx context.Context, d reconnectDeps) {
	var retryCount int
	for {
		err, connected := d.Connect(ctx)
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
		backoff := TieredBackoffWithJitter(retryCount)
		storageFailed := isStorageFailed(d.Store, d.CameraID)
		if storageFailed {
			backoff = StorageBackoffWithJitter()
		}
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
