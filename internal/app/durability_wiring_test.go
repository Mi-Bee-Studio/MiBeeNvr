package app

import "testing"

// TestBuildAppDeps_DurabilityWired guards the #760 wiring: the YAML
// storage.durability tier reaches the storage manager. Default (empty) must
// stay strict — relaxed is strictly opt-in.
func TestBuildAppDeps_DurabilityWired(t *testing.T) {
	t.Helper()

	cfg, configPath := minimalConfig(t)
	cfg.Storage.Durability = "relaxed"
	deps, cleanupFn, err := buildAppDeps(cfg, configPath)
	if err != nil {
		t.Fatalf("buildAppDeps: %v", err)
	}
	defer cleanupFn()
	if !deps.store.DurabilityRelaxed() {
		t.Error("storage.durability=relaxed did not reach the storage manager")
	}

	cfg2, configPath2 := minimalConfig(t)
	deps2, cleanupFn2, err := buildAppDeps(cfg2, configPath2)
	if err != nil {
		t.Fatalf("buildAppDeps: %v", err)
	}
	defer cleanupFn2()
	if deps2.store.DurabilityRelaxed() {
		t.Error("default (empty) durability must remain strict")
	}
}
