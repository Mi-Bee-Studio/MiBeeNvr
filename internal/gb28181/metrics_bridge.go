package gb28181

import (
	"log/slog"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	gbmetrics "github.com/mickeyzzc/gb28181-go/metrics"
)

// MetricsBridge adapts the gb28181-go observability seam onto the NVR: the
// GB35114 level-B/C AU-signature failures land in the Prometheus counter
// and, rate-limited to one per channel per minute, a tamper warning log
// (#1079 — a failing signature is an operational event, not debug noise).
// All other library hooks stay no-ops until the NVR bridges them.
type MetricsBridge struct {
	gbmetrics.NoopHooks

	failures *prometheus.CounterVec
	lastWarn sync.Map // channelID → unix nano of the last warning
}

// NewMetricsBridge wires the bridge onto the GB35114 failure counter.
func NewMetricsBridge(failures *prometheus.CounterVec) *MetricsBridge {
	return &MetricsBridge{failures: failures}
}

// GB35114AUVerifyFailed implements gbmetrics.Hooks.
func (b *MetricsBridge) GB35114AUVerifyFailed(channelID string) {
	if b.failures != nil {
		b.failures.WithLabelValues(channelID).Inc()
	}
	now := time.Now().UnixNano()
	if v, loaded := b.lastWarn.LoadOrStore(channelID, now); loaded {
		if now-v.(int64) < int64(time.Minute) {
			return
		}
		b.lastWarn.Store(channelID, now)
	}
	slog.Warn("gb28181: GB35114 media AU signature verification failed — possible stream tampering",
		"channel", channelID)
}
