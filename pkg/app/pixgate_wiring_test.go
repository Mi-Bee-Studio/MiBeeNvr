package app

import (
	"path/filepath"
	"testing"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/pixgate"
)

// TestRunFree_PixgateHubResolverWired guards the #643 wiring: with a
// pixgate-enabled camera the manager must be built with the shared-hub
// resolver, or the sampler silently keeps opening its own RTSP pull + full
// decode (the exact cost this issue removes). Same wiring-bug class as the
// #653 onAction=nil lesson.
func TestRunFree_PixgateHubResolverWired(t *testing.T) {
	t.Helper()
	cfg, _ := minimalConfig(t)
	cfg.Cameras = []config.CameraConfig{{
		ID:       "pg-cam",
		Protocol: "rtsp",
		Encoding: "h264",
		URL:      "rtsp://127.0.0.1:1/sub", // unreachable is fine: nothing starts
		Pixgate:  &config.CameraPixgateConfig{Enabled: true},
	}}

	a, err := RunFree(cfg, filepath.Join(cfg.Storage.RootDir, "mibee-nvr.yaml"))
	if err != nil {
		t.Fatalf("RunFree: %v", err)
	}

	svc := a.Get("pixgate")
	if svc == nil {
		t.Fatal("pixgate service not registered with a pixgate-enabled camera")
	}
	m, ok := svc.(*pixgate.Manager)
	if !ok {
		t.Fatalf("pixgate service is %T, want *pixgate.Manager", svc)
	}
	if !m.HasHubResolver() {
		t.Fatal("pixgate manager has no hub resolver wired: sampler would keep its own RTSP pull + full-stream decode")
	}
}

// TestRunFree_PixgateMetricsWired guards the #786 wiring: the sampler's
// Prometheus telemetry (heartbeat + counters) must be handed the app metrics
// instance. A deployed build silently ran with Config.Metrics=nil — samples
// kept flowing (journal stats lines) while every nvr_pixgate_* series stayed
// empty, exactly the observability gap the metrics exist to close.
func TestRunFree_PixgateMetricsWired(t *testing.T) {
	t.Helper()
	cfg, _ := minimalConfig(t)
	cfg.Cameras = []config.CameraConfig{{
		ID:       "pg-cam",
		Protocol: "rtsp",
		Encoding: "h264",
		URL:      "rtsp://127.0.0.1:1/sub",
		Pixgate:  &config.CameraPixgateConfig{Enabled: true},
	}}

	a, err := RunFree(cfg, filepath.Join(cfg.Storage.RootDir, "mibee-nvr.yaml"))
	if err != nil {
		t.Fatalf("RunFree: %v", err)
	}

	svc := a.Get("pixgate")
	if svc == nil {
		t.Fatal("pixgate service not registered")
	}
	m, ok := svc.(*pixgate.Manager)
	if !ok {
		t.Fatalf("pixgate service is %T, want *pixgate.Manager", svc)
	}
	if !m.MetricsWired() {
		t.Fatal("pixgate manager has no Metrics wired: nvr_pixgate_* series stay empty while the sampler runs")
	}
}
