package config

// vision.drop_mark_timeout_s (#823 review red line): the drop-report marking
// budget is an operator-tunable timeout, not a hardcoded constant — hundreds
// of ranges against a saturated WAL legitimately need more than 60s, while a
// fast disk wastes a long goroutine-pinning budget.

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestApplyDefaults_DropMarkTimeout(t *testing.T) {
	var cfg Config
	cfg.ApplyDefaults()
	require.Equal(t, 60, cfg.Vision.DropMarkTimeoutSecs, "default marking budget")
}

func TestValidate_DropMarkTimeout(t *testing.T) {
	valid := []int{1, 60, 300, 3600}
	for _, v := range valid {
		cfg := &Config{}
		cfg.ApplyDefaults()
		cfg.Vision.DropMarkTimeoutSecs = v
		require.NoError(t, Validate(cfg), "value %d must validate", v)
	}
	invalid := []int{-1, 3601}
	for _, v := range invalid {
		cfg := &Config{}
		cfg.ApplyDefaults()
		cfg.Vision.DropMarkTimeoutSecs = v
		require.Error(t, Validate(cfg), "value %d must be rejected", v)
	}
}
