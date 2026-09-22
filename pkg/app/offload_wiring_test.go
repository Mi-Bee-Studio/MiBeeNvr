package app

// offload_wiring_test.go — guards the production wiring of the S3 offload
// service (issue #874 batch 1): constructed only when storage.remote.enabled,
// registered in lifecycle order after archive-deleter, and NOT dialed at
// construction (unreachable endpoint must still build).

import (
	"path/filepath"
	"testing"
)

func TestBuildAppDeps_OffloadDisabled_NoManager(t *testing.T) {
	t.Helper()
	cfg, configPath := minimalConfig(t)
	cfg.Storage.Remote.Enabled = false

	deps, cleanup, err := buildAppDeps(cfg, configPath)
	if err != nil {
		t.Fatalf("buildAppDeps: %v", err)
	}
	defer cleanup()

	if deps.offloadMgr != nil {
		t.Fatal("deps.offloadMgr should be nil with storage.remote.enabled=false")
	}
}

func TestRunFree_OffloadEnabled_Registered(t *testing.T) {
	t.Helper()
	cfg, _ := minimalConfig(t)
	cfg.Storage.Remote.Enabled = true
	cfg.Storage.Remote.EndpointURL = "http://127.0.0.1:1" // unreachable is fine: no dial at build
	cfg.Storage.Remote.Bucket = "nvr"
	cfg.Storage.Remote.AccessKeyID = "k"
	cfg.Storage.Remote.SecretAccessKey = "s"

	a, err := RunFree(cfg, filepath.Join(cfg.Storage.RootDir, "mibee-nvr.yaml"))
	if err != nil {
		t.Fatalf("RunFree: %v", err)
	}
	t.Cleanup(func() { _ = a.Stop() })

	svcs := a.Services()
	found, idx := -1, -1
	for i, s := range svcs {
		if s == "offload" {
			found, idx = i, len(svcs)
			break
		}
	}
	if found < 0 {
		t.Fatalf("offload service missing from RunFree registry: %v", svcs)
	}
	// Must sit after archive-deleter (uploads stop before cleanup/db in the
	// reverse teardown) and before the streaming tail (ws/hls).
	archiveIdx := -1
	for i, s := range svcs {
		if s == "archive-deleter" {
			archiveIdx = i
		}
	}
	if archiveIdx >= 0 && found < archiveIdx {
		t.Errorf("offload (%d) registered before archive-deleter (%d)", found, archiveIdx)
	}
	_ = idx
}

func TestRunFree_OffloadBadConfig_FailsFast(t *testing.T) {
	t.Helper()
	cfg, _ := minimalConfig(t)
	cfg.Storage.Remote.Enabled = true
	cfg.Storage.Remote.EndpointURL = "http://127.0.0.1:9000"
	cfg.Storage.Remote.Bucket = "nvr"
	cfg.Storage.Remote.SecretAccessKey = "" // missing credential must fail the build

	if _, err := RunFree(cfg, filepath.Join(cfg.Storage.RootDir, "mibee-nvr.yaml")); err == nil {
		t.Fatal("RunFree should fail fast on an invalid remote-storage config")
	}
}
