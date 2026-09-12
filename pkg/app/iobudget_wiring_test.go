package app

import (
	"testing"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/iobudget"
)

// TestBuildAppDeps_IOBudgetWired guards the #751 wiring: with
// io.budget_bytes_per_sec configured, buildAppDeps must build ONE shared
// bucket, hand it to merge + timelapse (package setters) and to the cleanup
// manager. The wiring-bug class this guards against: a consumer forgotten in
// the distribution list silently runs unthrottled (same lesson as #653's
// onAction=nil).
func TestBuildAppDeps_IOBudgetWired(t *testing.T) {
	t.Helper()
	cfg, configPath := minimalConfig(t)
	cfg.IO.BudgetBytesPerSec = 8 << 20 // 8 MiB/s
	cfg.IO.BudgetBurstBytes = 16 << 20

	deps, cleanupFn, err := buildAppDeps(cfg, configPath)
	if err != nil {
		t.Fatalf("buildAppDeps: %v", err)
	}
	defer cleanupFn()

	if deps.ioBudget == nil {
		t.Fatal("deps.ioBudget is nil with io.budget_bytes_per_sec set: no consumer is paced")
	}
	if got := deps.ioBudget.Rate(); got != 8<<20 {
		t.Errorf("bucket rate = %d, want 8MiB", got)
	}
	if got := deps.ioBudget.Capacity(); got != 16<<20 {
		t.Errorf("bucket capacity = %d, want 16MiB", got)
	}
	if deps.cleanupMgr == nil {
		t.Fatal("deps.cleanupMgr is nil")
	}
	if deps.cleanupMgr.HasIOBudget() != deps.ioBudget {
		t.Error("cleanup manager does not hold the shared bucket")
	}

	// The merge/timelapse package-level setters are not directly observable
	// without importing those packages' internals; charging itself is covered
	// by their package tests. Here we verify the bucket works end-to-end.
	if err := deps.ioBudget.Wait(t.Context(), iobudget.ConsumerMerge, 1); err != nil {
		t.Errorf("bucket Wait on freshly built bucket: %v", err)
	}
}

// TestBuildAppDeps_IOBudgetDefaultOff: zero config (the default) builds no
// bucket — every consumer's fast path stays a nil check, zero overhead.
func TestBuildAppDeps_IOBudgetDefaultOff(t *testing.T) {
	t.Helper()
	cfg, configPath := minimalConfig(t)
	if cfg.IO.BudgetBytesPerSec != 0 {
		t.Fatalf("test premise: minimalConfig default budget should be 0, got %d", cfg.IO.BudgetBytesPerSec)
	}

	deps, cleanupFn, err := buildAppDeps(cfg, configPath)
	if err != nil {
		t.Fatalf("buildAppDeps: %v", err)
	}
	defer cleanupFn()

	if deps.ioBudget != nil {
		t.Error("deps.ioBudget should be nil when io.budget_bytes_per_sec=0 (default off)")
	}
	if deps.cleanupMgr.HasIOBudget() != nil {
		t.Error("cleanup manager should have no budget when io section is unset")
	}
}
