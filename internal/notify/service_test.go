package notify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/event"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
)

// receiver is a test webhook receiver capturing delivered alerts.
type receiver struct {
	srv    *httptest.Server
	mu     sync.Mutex
	alerts []Alert
}

func newReceiver(t *testing.T) *receiver {
	t.Helper()
	rcv := &receiver{}
	rcv.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var a Alert
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &a)
		rcv.mu.Lock()
		rcv.alerts = append(rcv.alerts, a)
		rcv.mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(rcv.srv.Close)
	return rcv
}

func (rcv *receiver) count() int {
	rcv.mu.Lock()
	defer rcv.mu.Unlock()
	return len(rcv.alerts)
}

func (rcv *receiver) snapshot() []Alert {
	rcv.mu.Lock()
	defer rcv.mu.Unlock()
	out := make([]Alert, len(rcv.alerts))
	copy(out, rcv.alerts)
	return out
}

func (rcv *receiver) waitCount(t *testing.T, n int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if rcv.count() >= n {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("expected %d delivered alerts, got %d", n, rcv.count())
}

// startService wires a real EventBus + Service against the given receivers.
func startService(t *testing.T, rcvs ...*receiver) (*event.EventBus, *Service) {
	t.Helper()
	bus := event.NewEventBus(64)
	opts := ServiceOptions{Bus: bus, CameraNamer: func(string) string { return "Studio" }}
	for i, rcv := range rcvs {
		opts.Sinks = append(opts.Sinks, SinkOptions{
			Name: string(rune('a' + i)), URL: rcv.srv.URL, Token: "tok",
			RetryDelays: []time.Duration{time.Millisecond},
		})
	}
	svc, err := NewService(opts)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	if err := svc.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = svc.Stop() })
	return bus, svc
}

func TestService_BusEventsDelivered(t *testing.T) {
	rcv := newReceiver(t)
	bus, _ := startService(t, rcv)

	bus.Publish(context.Background(), event.TopicDoorbellRing, event.DoorbellEvent{
		CallID: "call-1", CameraID: "cam-1", Name: "Front", Timestamp: time.Now(),
	})
	bus.Publish(context.Background(), event.TopicStorageHealthChanged, event.StorageHealthChanged{
		CameraID: "cam-1", PreviousState: "healthy", CurrentState: "degraded",
	})
	bus.Publish(context.Background(), "onvif.motionalarm", map[string]any{
		"camera_id": "cam-1", "topic": "tns1:VideoAnalytics/MotionAlarm", "camera_name": "Studio",
	})
	bus.Publish(context.Background(), event.TopicCameraAdded, map[string]any{
		"camera_id": "cam-new", "name": "Porch", "source": "manual",
	})
	bus.Publish(context.Background(), event.TopicAIEventCreated, map[string]any{
		"event_id": int64(9), "camera_id": "cam-1", "event_type": "person", "severity": "warning",
	})
	// Not on the whitelist — must NOT map.
	bus.Publish(context.Background(), event.TopicSegmentCompleted, event.SegmentCompleted{CameraID: "cam-1"})

	rcv.waitCount(t, 5)
	time.Sleep(100 * time.Millisecond)

	alerts := rcv.snapshot()
	if len(alerts) != 5 {
		t.Fatalf("segment.completed must not produce an alert, total=%d", len(alerts))
	}
	seen := map[string]bool{}
	for _, a := range alerts {
		seen[a.Type] = true
	}
	for _, want := range []string{TypeDoorbellRing, TypeStorageHealth, TypeCameraMotion, TypeCameraAdded, TypeAIDetection} {
		if !seen[want] {
			t.Errorf("alert type %s not delivered; got %v", want, seen)
		}
	}
}

func TestService_HealthSink(t *testing.T) {
	rcv := newReceiver(t)
	_, svc := startService(t, rcv)

	svc.PublishAlert("cam-1", model.HealthEvent{
		EventType: string(model.HealthEventConnectionLost), Status: "error", Message: "no frames for 30s", CreatedAt: time.Now(),
	})
	svc.PublishAlert("cam-1", model.HealthEvent{
		EventType: string(model.HealthEventConnectionRestored), Status: "healthy", Message: "back", CreatedAt: time.Now(),
	})
	rcv.waitCount(t, 2)

	alerts := rcv.snapshot()
	lost, restored := alerts[0], alerts[1]
	if lost.Resolved || !restored.Resolved {
		t.Errorf("lost/restored pairing broken: %+v %+v", lost, restored)
	}
	if lost.DedupKey != restored.DedupKey || lost.DedupKey == "" {
		t.Errorf("dedup keys must match and be set: %q vs %q", lost.DedupKey, restored.DedupKey)
	}
}

func TestService_SendTestBypassesFilters(t *testing.T) {
	// A sink whose event filter excludes notify.test must still receive the
	// test alert — verifying a receiver setup is the button's whole purpose.
	bus := event.NewEventBus(8)
	svc, err := NewService(ServiceOptions{
		Bus:   bus,
		Sinks: []SinkOptions{{Name: "t", URL: "http://127.0.0.1:1", Token: "tok", Events: []string{TypeCameraConnection}}},
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	svc.SendTest("warning", "custom title")
	if got := len(svc.sinks[0].queue); got != 1 {
		t.Fatalf("test alert should be queued bypassing filters, depth=%d", got)
	}
	if svc.sinks[0].dropFilt.Load() != 0 {
		t.Errorf("test alert must not be counted as filter-dropped, got %d", svc.sinks[0].dropFilt.Load())
	}
	a := <-svc.sinks[0].queue
	if a.Type != TypeNotifyTest || a.Title != "custom title" || a.Severity != SeverityWarning {
		t.Errorf("test alert wrong: %+v", a)
	}

	// Invalid severity falls back to info.
	svc.SendTest("bogus", "")
	a = <-svc.sinks[0].queue
	if a.Severity != SeverityInfo {
		t.Errorf("invalid severity should fall back to info, got %s", a.Severity)
	}
}

func TestService_MultipleSinksFanOut(t *testing.T) {
	rcvA, rcvB := newReceiver(t), newReceiver(t)
	bus, _ := startService(t, rcvA, rcvB)

	bus.Publish(context.Background(), event.TopicGB28181Alarm, event.GB28181AlarmEvent{
		DeviceID: "dev", ChannelID: "gb-1", AlarmPriority: "1", ReceivedAt: time.Now(),
	})
	rcvA.waitCount(t, 1)
	rcvB.waitCount(t, 1)
	if a := rcvA.snapshot()[0]; a.Severity != SeverityCritical {
		t.Errorf("priority-1 alarm should be critical, got %s", a.Severity)
	}
}

func TestService_Stats(t *testing.T) {
	rcv := newReceiver(t)
	_, svc := startService(t, rcv)
	svc.SendTest("info", "")
	stats := svc.Stats()
	if len(stats) != 1 || stats[0].Name != "a" {
		t.Fatalf("stats=%+v", stats)
	}
	if stats[0].URL == rcv.srv.URL || stats[0].URL[len(stats[0].URL)-3:] != "***" {
		t.Errorf("stats URL must mask the token: %q", stats[0].URL)
	}
}

func TestService_InvalidOptions(t *testing.T) {
	bus := event.NewEventBus(8)
	if _, err := NewService(ServiceOptions{Bus: bus}); err == nil {
		t.Error("no sinks must fail")
	}
	if _, err := NewService(ServiceOptions{Bus: bus, Sinks: []SinkOptions{{Name: "t", URL: "http://x", Token: "ok"}, {Name: "bad", URL: "junk", Token: "x"}}}); err == nil {
		t.Error("invalid sink must fail construction")
	}
	if _, err := NewService(ServiceOptions{Sinks: []SinkOptions{{Name: "t", URL: "http://x", Token: "ok"}}}); err == nil {
		t.Error("nil bus must fail")
	}
}

func TestService_StartStopIdempotent(t *testing.T) {
	rcv := newReceiver(t)
	svc, err := NewService(ServiceOptions{
		Bus:   event.NewEventBus(8),
		Sinks: []SinkOptions{{Name: "t", URL: rcv.srv.URL, Token: "tok"}},
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	ctx := context.Background()
	if err := svc.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := svc.Start(ctx); err != nil {
		t.Fatalf("second start must be a no-op: %v", err)
	}
	if err := svc.Stop(); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if err := svc.Stop(); err != nil {
		t.Fatalf("second stop must be a no-op: %v", err)
	}
}
