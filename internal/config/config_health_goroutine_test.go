package config

// health.goroutine_baseline / goroutine_per_camera (#816 review): the
// health-check tripwire coefficients are operator-tunable — heavier
// per-camera workloads (sub-stream consumers, cascade, relays) run more
// goroutines per camera than the observed ~85 baseline workload.

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestApplyDefaults_GoroutinePolicy(t *testing.T) {
	var cfg Config
	cfg.ApplyDefaults()
	require.Equal(t, 300, cfg.Health.GoroutineBaseline)
	require.Equal(t, 150, cfg.Health.GoroutinePerCamera)
}

func TestValidate_GoroutinePolicy(t *testing.T) {
	cfg := &Config{}
	cfg.ApplyDefaults()

	// Explicit values in a sane range validate.
	cfg.Health.GoroutineBaseline = 500
	cfg.Health.GoroutinePerCamera = 250
	require.NoError(t, Validate(cfg))

	// Negative coefficients are nonsense — reject.
	cfg.Health.GoroutineBaseline = -1
	require.Error(t, Validate(cfg))
	cfg.Health.GoroutineBaseline = 300
	cfg.Health.GoroutinePerCamera = -5
	require.Error(t, Validate(cfg))
}
