package metrics

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestNewMetrics_RegistersRecentCollectors guards the registration wiring of
// the recent metric families: a collector that is assigned to its Metrics
// field but missing from reg.MustRegister still COUNTS silently (Inc/Observe
// work; unit tests via testutil.ToFloat64 read the collector directly, never
// the registry) yet never appears in the /metrics exposition — exactly how
// the #783 bucket-finalize counters and the #786 pixgate telemetry shipped
// invisible on production while every test stayed green. Pre-touching each
// collector forces a child series, so a Gather over the served registry
// proves registration end to end.
func TestNewMetrics_RegistersRecentCollectors(t *testing.T) {
	m := NewMetrics()

	m.RollingBucketFinalizedTotal.WithLabelValues("idle_ttl").Inc()
	m.RollingBucketLifetimeSeconds.WithLabelValues("idle_ttl").Observe(1)
	m.PixgateSamplesTotal.WithLabelValues("cam", "hub").Inc()
	m.PixgateTriggersTotal.WithLabelValues("cam").Inc()
	m.PixgateLastAreaPct.WithLabelValues("cam").Set(0)
	m.PixgateLastSampleTimestamp.WithLabelValues("cam").Set(1)

	fams, err := m.Registry.Gather()
	require.NoError(t, err)
	registered := map[string]bool{}
	for _, f := range fams {
		registered[f.GetName()] = true
	}

	for _, name := range []string{
		"nvr_rolling_merge_bucket_finalized_total",
		"nvr_rolling_merge_bucket_lifetime_seconds",
		"nvr_pixgate_samples_total",
		"nvr_pixgate_triggers_total",
		"nvr_pixgate_last_area_pct",
		"nvr_pixgate_last_sample_timestamp_seconds",
	} {
		require.True(t, registered[name],
			"%s carries data but is absent from the served registry — registration missing", name)
	}
}
