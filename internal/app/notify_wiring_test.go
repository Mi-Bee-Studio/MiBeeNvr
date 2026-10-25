package app

import (
	"strings"
	"testing"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
)

// TestBuildAppDeps_NotificationsWired guards the production wiring: an
// enabled notifications section must construct the dispatcher AND hook it
// into the health pipeline (the only dispatch point for camera
// offline/frozen events — a service without the sink would silently lose
// every health alert).
func TestBuildAppDeps_NotificationsWired(t *testing.T) {
	t.Helper()
	cfg, configPath := minimalConfig(t)
	cfg.Health.Enabled = true // full mode: the alert pipeline must exist for the sink
	cfg.Notifications.Enabled = true
	cfg.Notifications.Sinks = []config.NotificationSinkConfig{
		{Name: "hub", URL: "http://127.0.0.1:1/api/ingest/webhook", Token: "tok"},
	}

	deps, cleanup, err := buildAppDeps(cfg, configPath)
	if err != nil {
		t.Fatalf("buildAppDeps: %v", err)
	}
	defer cleanup()

	if deps.notifySvc == nil {
		t.Fatal("deps.notifySvc is nil with notifications.enabled=true")
	}
	if !deps.healthMgr.HasAlertSink() {
		t.Fatal("health pipeline has no alert sink: health events would never reach the dispatcher")
	}
	if stats := deps.notifySvc.Stats(); len(stats) != 1 || stats[0].Name != "hub" {
		t.Fatalf("sink not built from config: %+v", stats)
	}
}

func TestBuildAppDeps_NotificationsDisabled_NoService(t *testing.T) {
	t.Helper()
	cfg, configPath := minimalConfig(t)
	cfg.Notifications.Enabled = false

	deps, cleanup, err := buildAppDeps(cfg, configPath)
	if err != nil {
		t.Fatalf("buildAppDeps: %v", err)
	}
	defer cleanup()

	if deps.notifySvc != nil {
		t.Fatal("deps.notifySvc should be nil with notifications.enabled=false")
	}
	if deps.healthMgr.HasAlertSink() {
		t.Fatal("health pipeline must stay unwired with notifications disabled")
	}
}

func TestBuildAppDeps_NotificationsBadConfig_Fails(t *testing.T) {
	t.Helper()
	cfg, configPath := minimalConfig(t)
	cfg.Notifications.Enabled = true
	// No sinks — construction must fail loudly, not limp along.
	cfg.Notifications.Sinks = nil

	_, _, err := buildAppDeps(cfg, configPath)
	if err == nil || !strings.Contains(err.Error(), "sinks") {
		t.Fatalf("expected a sinks error, got %v", err)
	}
}
