package health

import (
	"sync"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
)

// captureSink records every alert the pipeline forwards.
type captureSink struct {
	mu     sync.Mutex
	alerts []struct {
		cameraID string
		event    model.HealthEvent
	}
}

func (c *captureSink) PublishAlert(cameraID string, event model.HealthEvent) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.alerts = append(c.alerts, struct {
		cameraID string
		event    model.HealthEvent
	}{cameraID, event})
}

func (c *captureSink) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.alerts)
}

func TestAlertPipeline_SinkDispatch(t *testing.T) {
	sink := &captureSink{}
	p := NewAlertPipeline(5*time.Minute, false, nil, nil, "mibee-nvr")
	p.alertSink = sink

	_ = p.HandleEvent("cam-1", model.HealthEvent{EventType: string(model.HealthEventConnectionLost), Message: "down"})
	// Same cooldown key within the window is suppressed — the sink must NOT
	// see it either: the sink sits behind the same gating as MQTT/storage.
	_ = p.HandleEvent("cam-1", model.HealthEvent{EventType: string(model.HealthEventConnectionLost), Message: "down"})
	// A different message is a different cooldown key and dispatches.
	_ = p.HandleEvent("cam-1", model.HealthEvent{EventType: string(model.HealthEventConnectionLost), Message: "down again"})

	if sink.count() != 2 {
		t.Fatalf("sink should see the two distinct events, got %d", sink.count())
	}
	got := sink.alerts[0]
	if got.cameraID != "cam-1" || got.event.EventType != string(model.HealthEventConnectionLost) {
		t.Errorf("sink payload wrong: %+v", got)
	}
}

func TestAlertPipeline_NilSinkNoop(t *testing.T) {
	p := NewAlertPipeline(0, false, nil, nil, "mibee-nvr")
	if err := p.HandleEvent("cam-1", model.HealthEvent{EventType: string(model.HealthEventStreamAnomaly)}); err != nil {
		t.Fatalf("nil sink must be a no-op: %v", err)
	}
}

func TestManager_SetAlertSink(t *testing.T) {
	// Metrics-only mode has no pipeline — SetAlertSink must be a safe no-op.
	m := NewManager(config.HealthConfig{Enabled: false}, nil)
	m.SetAlertSink(&captureSink{})

	// Full mode wires the sink into the pipeline.
	full := NewManager(config.HealthConfig{
		Enabled: true,
		Layer1:  config.HealthLayer1Config{OfflineThreshold: "30s"},
		Alerts:  config.HealthAlertsConfig{Cooldown: "5m"},
	}, nil)
	sink := &captureSink{}
	full.SetAlertSink(sink)
	if !full.HasAlertSink() {
		t.Fatal("expected pipeline with the sink wired")
	}
}
