package app

import (
	"testing"
)

// TestBuildAppDeps_SnapshotCapturerWired guards the production wiring for the
// FFmpeg-gated latest-frame path (#657): the API handler must be constructed
// with the snapshot capturer, or H.264/H.265 cameras keep answering 404 even
// with FFmpeg present (same wiring-bug class as the #653 onAction=nil lesson).
func TestBuildAppDeps_SnapshotCapturerWired(t *testing.T) {
	t.Helper()
	cfg, configPath := minimalConfig(t)

	deps, cleanup, err := buildAppDeps(cfg, configPath)
	if err != nil {
		t.Fatalf("buildAppDeps: %v", err)
	}
	defer cleanup()

	if deps.handler == nil {
		t.Fatal("deps.handler is nil")
	}
	if !deps.handler.HasSnapshotCapturer() {
		t.Fatal("api handler has no snapshot capturer wired: latest-frame stays JPEG-only")
	}
}

// TestBuildAppDeps_MQTTSnapshotRunnerWired guards the #656 wiring: with
// mqtt.enabled the snapshot runner (capture → persist → event) must be built
// and handed to the action dispatcher.
func TestBuildAppDeps_MQTTSnapshotRunnerWired(t *testing.T) {
	t.Helper()
	cfg, configPath := minimalConfig(t)
	cfg.MQTT.Enabled = true
	cfg.MQTT.Broker = "tcp://127.0.0.1:1" // unreachable is fine: connect happens in Start, not build

	deps, cleanup, err := buildAppDeps(cfg, configPath)
	if err != nil {
		t.Fatalf("buildAppDeps: %v", err)
	}
	defer cleanup()

	if deps.snapRunner == nil {
		t.Fatal("deps.snapRunner is nil with mqtt.enabled=true: snapshot trigger would be dropped")
	}
}

// TestBuildAppDeps_MQTTDisabled_NoSnapRunner mirrors MQTTDisabled_NoClient.
func TestBuildAppDeps_MQTTDisabled_NoSnapRunner(t *testing.T) {
	t.Helper()
	cfg, configPath := minimalConfig(t)
	cfg.MQTT.Enabled = false

	deps, cleanup, err := buildAppDeps(cfg, configPath)
	if err != nil {
		t.Fatalf("buildAppDeps: %v", err)
	}
	defer cleanup()

	if deps.snapRunner != nil {
		t.Fatal("deps.snapRunner should be nil with mqtt.enabled=false")
	}
}
