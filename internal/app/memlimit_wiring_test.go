package app

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

// TestBuildAppDeps_MemorySoftLimitGaugeSet: the #756 gauge publishes the
// limit recorded by main.go's applyMemoryLimit (0 = not set is valid).
func TestBuildAppDeps_MemorySoftLimitGaugeSet(t *testing.T) {
	t.Helper()
	cfg, configPath := minimalConfig(t)

	prev := memlimitAppliedFn
	memlimitAppliedFn = func() int64 { return 512 << 20 }
	t.Cleanup(func() { memlimitAppliedFn = prev })

	deps, cleanupFn, err := buildAppDeps(cfg, configPath)
	if err != nil {
		t.Fatalf("buildAppDeps: %v", err)
	}
	defer cleanupFn()

	if v := testutil.ToFloat64(deps.metrics.MemorySoftLimitBytes); int64(v) != 512<<20 {
		t.Errorf("nvr_memlimit_bytes = %d, want 512MiB", int64(v))
	}
}
