package gb28181

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// TestMetricsBridgeCounts verifies the counter increments per channel and
// stays nil-safe without a vec.
func TestMetricsBridgeCounts(t *testing.T) {
	failures := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "test_gb35114_au_verify_failures_total",
	}, []string{"channel_id"})
	b := NewMetricsBridge(failures)

	b.GB35114AUVerifyFailed("34020000001310000001")
	b.GB35114AUVerifyFailed("34020000001310000001")
	b.GB35114AUVerifyFailed("34020000001310000002")

	if got := testutil.ToFloat64(failures.WithLabelValues("34020000001310000001")); got != 2 {
		t.Fatalf("channel A counter = %v, want 2", got)
	}
	if got := testutil.ToFloat64(failures.WithLabelValues("34020000001310000002")); got != 1 {
		t.Fatalf("channel B counter = %v, want 1", got)
	}

	// nil vec must not panic (bridge installed without the metric).
	NewMetricsBridge(nil).GB35114AUVerifyFailed("x")
}
