package app

import (
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
)

// TestBuildAppDeps_TempDirGraceWired guards the #797 review wiring:
// storage.periodic_temp_grace_s must flow into every periodic-merge manager
// as ONE shared value (the temp base is shared across cameras — per-camera
// granularity would let the strictest camera silently win). Same
// wiring-bug class as #653's onAction=nil.
func TestBuildAppDeps_TempDirGraceWired(t *testing.T) {
	t.Helper()
	cfg, configPath := minimalConfig(t)
	cfg.Storage.PeriodicTempGraceS = 3600 // 1h override (defaults give 86400)
	cfg.Cameras = append(cfg.Cameras, config.CameraConfig{
		ID:       "cam-grace",
		Protocol: "rtsp",
		URL:      "rtsp://127.0.0.1:1/x",
		Encoding: "h264",
		Timelapse: &config.CameraTimelapseConfig{
			Enabled:       true,
			Interval:      "30s",
			MergeDuration: "1h",
		},
	})
	cfg.ApplyDefaults()

	deps, cleanupFn, err := buildAppDeps(cfg, configPath)
	if err != nil {
		t.Fatalf("buildAppDeps: %v", err)
	}
	defer cleanupFn()

	mgr, ok := deps.periodicMergeManagers["cam-grace"]
	if !ok {
		t.Fatal("periodicMergeManagers has no entry for the timelapse camera")
	}
	if got := mgr.TempDirGrace(); got != time.Hour {
		t.Fatalf("TempDirGrace = %v, want 1h (storage.periodic_temp_grace_s=3600)", got)
	}
}

// TestBuildAppDeps_TempDirGraceDefaults: absent config → every manager falls
// back to the 24h default (not zero — zero would sweep everything at boot).
func TestBuildAppDeps_TempDirGraceDefaults(t *testing.T) {
	t.Helper()
	cfg, configPath := minimalConfig(t)
	cfg.Storage.PeriodicTempGraceS = 0 // absent — default must apply
	cfg.Cameras = append(cfg.Cameras, config.CameraConfig{
		ID:       "cam-grace-def",
		Protocol: "rtsp",
		URL:      "rtsp://127.0.0.1:1/x",
		Encoding: "h264",
		Timelapse: &config.CameraTimelapseConfig{
			Enabled:       true,
			Interval:      "30s",
			MergeDuration: "1h",
		},
	})
	cfg.ApplyDefaults()

	deps, cleanupFn, err := buildAppDeps(cfg, configPath)
	if err != nil {
		t.Fatalf("buildAppDeps: %v", err)
	}
	defer cleanupFn()

	mgr := deps.periodicMergeManagers["cam-grace-def"]
	if mgr == nil {
		t.Fatal("periodicMergeManagers has no entry for the timelapse camera")
	}
	if got := mgr.TempDirGrace(); got != 24*time.Hour {
		t.Fatalf("TempDirGrace = %v, want the 24h default", got)
	}
}
