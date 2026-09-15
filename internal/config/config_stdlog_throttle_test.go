package config

// observability.stdlog_throttle (#813 review ②): the interval is an
// operator-tunable value, not a hardcoded constant — large-journal-quota
// deployments may want denser signal or to disable the limiter entirely.

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestApplyDefaults_StdlogThrottle(t *testing.T) {
	var cfg Config
	cfg.ApplyDefaults()
	require.Equal(t, "10s", cfg.Observability.StdlogThrottle)
}

func TestValidate_StdlogThrottle(t *testing.T) {
	valid := []string{"off", "0s", "10s", "1m", "250ms"}
	for _, v := range valid {
		cfg := &Config{}
		cfg.ApplyDefaults()
		cfg.Observability.StdlogThrottle = v
		require.NoError(t, Validate(cfg), "value %q must validate", v)
	}
	invalid := []string{"banana", "-3s", "10"} // "10" lacks a unit
	for _, v := range invalid {
		cfg := &Config{}
		cfg.ApplyDefaults()
		cfg.Observability.StdlogThrottle = v
		require.Error(t, Validate(cfg), "value %q must be rejected", v)
	}
}

func TestStdlogThrottleDuration(t *testing.T) {
	require.Zero(t, (ObservabilityConfig{}).StdlogThrottleDuration(), "empty → disabled")
	require.Zero(t, (ObservabilityConfig{StdlogThrottle: "off"}).StdlogThrottleDuration())
	require.Zero(t, (ObservabilityConfig{StdlogThrottle: "0s"}).StdlogThrottleDuration())
	require.Zero(t, (ObservabilityConfig{StdlogThrottle: "banana"}).StdlogThrottleDuration())
	require.Equal(t, 45*time.Second, (ObservabilityConfig{StdlogThrottle: "45s"}).StdlogThrottleDuration())
	require.Equal(t, time.Minute, (ObservabilityConfig{StdlogThrottle: "1m"}).StdlogThrottleDuration())
}
