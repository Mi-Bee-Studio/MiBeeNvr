package app

import (
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
)

// TestBuildAppDeps_TimelapseExtractionWiring guards the recording→timelapse
// wiring (same wiring-bug class as the #653 onAction=nil lesson):
//   - the per-camera timelapse.interval must reach the periodic-merge manager
//     (WithExtractionInterval) — without it, MJPEG dual-mode cameras sample at
//     1/output-fps and the "timelapse" is a full-speed copy of the day;
//   - the cleanup-backed source deleter must be wired into both the scheduled
//     managers and the API handler, or opt-in delete_recordings_after_merge
//     silently does nothing.
func TestBuildAppDeps_TimelapseExtractionWiring(t *testing.T) {
	t.Helper()
	cfg, configPath := minimalConfig(t)

	// minimalConfig ships zero cameras — add one with a timelapse block
	// carrying a non-default interval and the source-deletion flag on.
	cfg.Cameras = append(cfg.Cameras, config.CameraConfig{
		ID: "wiring-cam", Protocol: "rtsp", Encoding: "h264", URL: "rtsp://127.0.0.1:554/test",
		Timelapse: &config.CameraTimelapseConfig{
			Enabled:                    true,
			Interval:                   "5s",
			DeleteRecordingsAfterMerge: true,
		},
	})
	camID := "wiring-cam"

	deps, cleanup, err := buildAppDeps(cfg, configPath)
	if err != nil {
		t.Fatalf("buildAppDeps: %v", err)
	}
	defer cleanup()

	mgr, ok := deps.periodicMergeManagers[camID]
	if !ok {
		t.Fatal("no periodic-merge manager built for camera with timelapse config")
	}
	if got := mgr.ExtractionInterval(); got != 5*time.Second {
		t.Errorf("ExtractionInterval = %v, want 5s (timelapse.interval must be wired)", got)
	}
	if !mgr.HasSourceDeleter() {
		t.Error("periodic-merge manager has no source deleter: delete_recordings_after_merge would silently do nothing")
	}
	if !deps.handler.HasTimelapseSourceDeleter() {
		t.Error("api handler has no timelapse source deleter: manual merges cannot honor delete_recordings_after_merge")
	}
}

// TestBuildAppDeps_TimelapseDefaults_NoConfig: a camera without a timelapse
// block gets no periodic-merge manager (existing behavior preserved).
func TestBuildAppDeps_TimelapseDefaults_NoConfig(t *testing.T) {
	t.Helper()
	cfg, configPath := minimalConfig(t)

	deps, cleanup, err := buildAppDeps(cfg, configPath)
	if err != nil {
		t.Fatalf("buildAppDeps: %v", err)
	}
	defer cleanup()

	if len(deps.periodicMergeManagers) != 0 {
		t.Errorf("expected 0 periodic-merge managers without timelapse config, got %d", len(deps.periodicMergeManagers))
	}
}
