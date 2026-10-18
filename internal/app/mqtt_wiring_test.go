package app

import (
	"testing"
)

// TestBuildAppDeps_MQTTTriggerWired guards the production wiring: the MQTT
// client must be constructed with a real action dispatcher so record/stop
// trigger messages reach the camera manager (the subscription itself is
// useless without it — regression guard for the onAction=nil wiring bug).
func TestBuildAppDeps_MQTTTriggerWired(t *testing.T) {
	t.Helper()
	cfg, configPath := minimalConfig(t)
	cfg.MQTT.Enabled = true
	cfg.MQTT.Broker = "tcp://127.0.0.1:1" // unreachable is fine: connect happens in Start, not build

	deps, cleanup, err := buildAppDeps(cfg, configPath)
	if err != nil {
		t.Fatalf("buildAppDeps: %v", err)
	}
	defer cleanup()

	if deps.mqttClient == nil {
		t.Fatal("deps.mqttClient is nil with mqtt.enabled=true")
	}
	if !deps.mqttClient.HasActionHandler() {
		t.Fatal("deps.mqttClient has no action handler: MQTT trigger messages would be silently dropped")
	}
}

func TestBuildAppDeps_MQTTDisabled_NoClient(t *testing.T) {
	t.Helper()
	cfg, configPath := minimalConfig(t)
	cfg.MQTT.Enabled = false

	deps, cleanup, err := buildAppDeps(cfg, configPath)
	if err != nil {
		t.Fatalf("buildAppDeps: %v", err)
	}
	defer cleanup()

	if deps.mqttClient != nil {
		t.Fatal("deps.mqttClient should be nil with mqtt.enabled=false")
	}
}
