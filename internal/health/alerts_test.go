package health

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
)

// --- Mocks ---

// mockStorage tracks InsertHealthEvent calls.
type mockStorage struct {
	mu     sync.Mutex
	events []model.HealthEvent
}

func (m *mockStorage) InsertHealthEvent(_ context.Context, event model.HealthEvent) error {
	m.mu.Lock()
	m.events = append(m.events, event)
	m.mu.Unlock()
	return nil
}

func (m *mockStorage) GetLatestCameraHealth(_ context.Context, _ string) (*model.HealthEvent, error) {
	return nil, nil
}

func (m *mockStorage) DeleteHealthEventsByType(_ context.Context, _ string, _ time.Time) (int64, error) {
	return 0, nil
}

func (m *mockStorage) insertedEvents(t *testing.T) []model.HealthEvent {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]model.HealthEvent, len(m.events))
	copy(cp, m.events)
	return cp
}

// mockMQTT tracks Publish calls.
type mockMQTT struct {
	mu       sync.Mutex
	messages []mockMsg
}

type mockMsg struct {
	topic   string
	payload any
}

func (m *mockMQTT) Publish(topic string, payload any) error {
	m.mu.Lock()
	m.messages = append(m.messages, mockMsg{topic: topic, payload: payload})
	m.mu.Unlock()
	return nil
}

func (m *mockMQTT) publishedMessages(t *testing.T) []mockMsg {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]mockMsg, len(m.messages))
	copy(cp, m.messages)
	return cp
}

// --- Helpers ---

// newTestPipeline creates an AlertPipeline with mock deps for testing.
func newTestPipeline(t *testing.T, cooldown time.Duration, mqttEnabled bool) (*AlertPipeline, *mockStorage, *mockMQTT) {
	t.Helper()
	store := &mockStorage{}
	mqtt := &mockMQTT{}
	p := NewAlertPipeline(cooldown, mqttEnabled, store, mqtt, "mibee-nvr")
	return p, store, mqtt
}

// makeEvent creates a test HealthEvent.
func makeEvent(t *testing.T, cameraID, eventType, status, message string) model.HealthEvent {
	t.Helper()
	return model.HealthEvent{
		CameraID:  cameraID,
		EventType: eventType,
		Status:    status,
		Message:   message,
		CreatedAt: time.Now().UTC(),
	}
}

// --- Tests ---

func TestAlertDispatch(t *testing.T) {
	t.Helper()
	pipeline, store, mqtt := newTestPipeline(t, 5*time.Minute, true)

	event := makeEvent(t, "cam-1", string(model.HealthEventConnectionLost), string(model.HealthStatusError), "Camera offline")
	if err := pipeline.HandleEvent("cam-1", event); err != nil {
		t.Fatalf("HandleEvent returned error: %v", err)
	}

	// Verify storage was called
	stored := store.insertedEvents(t)
	if len(stored) != 1 {
		t.Fatalf("expected 1 stored event, got %d", len(stored))
	}
	if stored[0].CameraID != "cam-1" {
		t.Errorf("expected camera ID cam-1, got %s", stored[0].CameraID)
	}
	if stored[0].EventType != string(model.HealthEventConnectionLost) {
		t.Errorf("expected event type %s, got %s", model.HealthEventConnectionLost, stored[0].EventType)
	}

	// Verify MQTT was called
	msgs := mqtt.publishedMessages(t)
	if len(msgs) != 1 {
		t.Fatalf("expected 1 MQTT message, got %d", len(msgs))
	}
	if msgs[0].topic != "health/cam-1" {
		t.Errorf("expected topic health/cam-1, got %s", msgs[0].topic)
	}
}

func TestAlertCooldownSuppression(t *testing.T) {
	t.Helper()
	cooldown := 5 * time.Minute
	pipeline, store, mqtt := newTestPipeline(t, cooldown, true)

	event := makeEvent(t, "cam-1", string(model.HealthEventConnectionLost), string(model.HealthStatusError), "Camera offline")

	// First event — should be dispatched
	if err := pipeline.HandleEvent("cam-1", event); err != nil {
		t.Fatalf("first HandleEvent returned error: %v", err)
	}

	// Second identical event within cooldown — should be suppressed
	if err := pipeline.HandleEvent("cam-1", event); err != nil {
		t.Fatalf("second HandleEvent returned error: %v", err)
	}

	// Third identical event within cooldown — should still be suppressed
	if err := pipeline.HandleEvent("cam-1", event); err != nil {
		t.Fatalf("third HandleEvent returned error: %v", err)
	}

	stored := store.insertedEvents(t)
	if len(stored) != 1 {
		t.Fatalf("expected 1 stored event (cooldown suppresses duplicates), got %d", len(stored))
	}

	msgs := mqtt.publishedMessages(t)
	if len(msgs) != 1 {
		t.Fatalf("expected 1 MQTT message (cooldown suppresses duplicates), got %d", len(msgs))
	}
}

func TestAlertCooldownExpiration(t *testing.T) {
	t.Helper()
	cooldown := 100 * time.Millisecond
	pipeline, store, mqtt := newTestPipeline(t, cooldown, true)

	event := makeEvent(t, "cam-1", string(model.HealthEventConnectionLost), string(model.HealthStatusError), "Camera offline")

	// First event
	if err := pipeline.HandleEvent("cam-1", event); err != nil {
		t.Fatalf("first HandleEvent returned error: %v", err)
	}

	// Wait for cooldown to expire
	time.Sleep(cooldown + 50*time.Millisecond)

	// Second event after cooldown — should be dispatched
	if err := pipeline.HandleEvent("cam-1", event); err != nil {
		t.Fatalf("second HandleEvent returned error: %v", err)
	}

	stored := store.insertedEvents(t)
	if len(stored) != 2 {
		t.Fatalf("expected 2 stored events (cooldown expired), got %d", len(stored))
	}

	msgs := mqtt.publishedMessages(t)
	if len(msgs) != 2 {
		t.Fatalf("expected 2 MQTT messages (cooldown expired), got %d", len(msgs))
	}
}

func TestAlertDifferentEvents(t *testing.T) {
	t.Helper()
	cooldown := 5 * time.Minute
	pipeline, store, _ := newTestPipeline(t, cooldown, true)

	event1 := makeEvent(t, "cam-1", string(model.HealthEventConnectionLost), string(model.HealthStatusError), "Camera offline")
	event2 := makeEvent(t, "cam-1", string(model.HealthEventFreezeDetected), string(model.HealthStatusWarning), "Video freeze detected")

	// Both should dispatch — different event types
	if err := pipeline.HandleEvent("cam-1", event1); err != nil {
		t.Fatalf("first HandleEvent returned error: %v", err)
	}
	if err := pipeline.HandleEvent("cam-1", event2); err != nil {
		t.Fatalf("second HandleEvent returned error: %v", err)
	}

	stored := store.insertedEvents(t)
	if len(stored) != 2 {
		t.Fatalf("expected 2 stored events (different event types), got %d", len(stored))
	}
	if stored[0].EventType != string(model.HealthEventConnectionLost) {
		t.Errorf("expected first event type %s, got %s", model.HealthEventConnectionLost, stored[0].EventType)
	}
	if stored[1].EventType != string(model.HealthEventFreezeDetected) {
		t.Errorf("expected second event type %s, got %s", model.HealthEventFreezeDetected, stored[1].EventType)
	}
}

func TestAlertDifferentCameras(t *testing.T) {
	t.Helper()
	cooldown := 5 * time.Minute
	pipeline, store, _ := newTestPipeline(t, cooldown, true)

	event1 := makeEvent(t, "cam-1", string(model.HealthEventConnectionLost), string(model.HealthStatusError), "Camera offline")
	event2 := makeEvent(t, "cam-2", string(model.HealthEventConnectionLost), string(model.HealthStatusError), "Camera offline")

	if err := pipeline.HandleEvent("cam-1", event1); err != nil {
		t.Fatalf("cam-1 HandleEvent returned error: %v", err)
	}
	if err := pipeline.HandleEvent("cam-2", event2); err != nil {
		t.Fatalf("cam-2 HandleEvent returned error: %v", err)
	}

	stored := store.insertedEvents(t)
	if len(stored) != 2 {
		t.Fatalf("expected 2 stored events (different cameras), got %d", len(stored))
	}
}

func TestAlertMQTTDisabled(t *testing.T) {
	t.Helper()
	pipeline, store, mqtt := newTestPipeline(t, 5*time.Minute, false)

	event := makeEvent(t, "cam-1", string(model.HealthEventConnectionLost), string(model.HealthStatusError), "Camera offline")
	if err := pipeline.HandleEvent("cam-1", event); err != nil {
		t.Fatalf("HandleEvent returned error: %v", err)
	}

	// Storage should still be called
	stored := store.insertedEvents(t)
	if len(stored) != 1 {
		t.Fatalf("expected 1 stored event, got %d", len(stored))
	}

	// MQTT should NOT be called
	msgs := mqtt.publishedMessages(t)
	if len(msgs) != 0 {
		t.Fatalf("expected 0 MQTT messages (mqtt disabled), got %d", len(msgs))
	}
}

func TestAlertComputeStatusError(t *testing.T) {
	t.Helper()
	pipeline, _, _ := newTestPipeline(t, 5*time.Minute, true)

	event := makeEvent(t, "cam-1", string(model.HealthEventConnectionLost), string(model.HealthStatusError), "Camera offline")
	_ = pipeline.HandleEvent("cam-1", event)

	status := pipeline.GetCameraStatus("cam-1")
	if status != string(model.HealthStatusError) {
		t.Errorf("expected status %s, got %s", model.HealthStatusError, status)
	}
}

func TestAlertComputeStatusWarning(t *testing.T) {
	t.Helper()
	pipeline, _, _ := newTestPipeline(t, 5*time.Minute, true)

	event := makeEvent(t, "cam-1", string(model.HealthEventStreamAnomaly), string(model.HealthStatusWarning), "Low bitrate")
	_ = pipeline.HandleEvent("cam-1", event)

	status := pipeline.GetCameraStatus("cam-1")
	if status != string(model.HealthStatusWarning) {
		t.Errorf("expected status %s, got %s", model.HealthStatusWarning, status)
	}
}

func TestAlertComputeStatusHealthy(t *testing.T) {
	t.Helper()
	pipeline, _, _ := newTestPipeline(t, 5*time.Minute, true)

	event := makeEvent(t, "cam-1", string(model.HealthEventConnectionRestored), string(model.HealthStatusHealthy), "Connection restored")
	_ = pipeline.HandleEvent("cam-1", event)

	status := pipeline.GetCameraStatus("cam-1")
	if status != string(model.HealthStatusHealthy) {
		t.Errorf("expected status %s, got %s", model.HealthStatusHealthy, status)
	}
}

func TestAlertComputeStatusUnknown(t *testing.T) {
	t.Helper()
	pipeline, _, _ := newTestPipeline(t, 5*time.Minute, true)

	// No events — should return unknown
	status := pipeline.GetCameraStatus("cam-unknown")
	if status != string(model.HealthStatusUnknown) {
		t.Errorf("expected status %s, got %s", model.HealthStatusUnknown, status)
	}
}

func TestAlertGetAllStatuses(t *testing.T) {
	t.Helper()
	pipeline, _, _ := newTestPipeline(t, 5*time.Minute, true)

	_ = pipeline.HandleEvent("cam-1", makeEvent(t, "cam-1", string(model.HealthEventConnectionLost), string(model.HealthStatusError), "Offline"))
	_ = pipeline.HandleEvent("cam-2", makeEvent(t, "cam-2", string(model.HealthEventStreamAnomaly), string(model.HealthStatusWarning), "Low bitrate"))

	statuses := pipeline.GetAllStatuses()
	if len(statuses) != 2 {
		t.Fatalf("expected 2 statuses, got %d", len(statuses))
	}
	if statuses["cam-1"] != string(model.HealthStatusError) {
		t.Errorf("expected cam-1 status %s, got %s", model.HealthStatusError, statuses["cam-1"])
	}
	if statuses["cam-2"] != string(model.HealthStatusWarning) {
		t.Errorf("expected cam-2 status %s, got %s", model.HealthStatusWarning, statuses["cam-2"])
	}
}

func TestAlertNilStorage(t *testing.T) {
	t.Helper()
	pipeline := NewAlertPipeline(5*time.Minute, true, nil, &mockMQTT{}, "mibee-nvr")

	event := makeEvent(t, "cam-1", string(model.HealthEventConnectionLost), string(model.HealthStatusError), "Camera offline")
	if err := pipeline.HandleEvent("cam-1", event); err != nil {
		t.Fatalf("HandleEvent with nil storage returned error: %v", err)
	}

	// Status should still be tracked
	status := pipeline.GetCameraStatus("cam-1")
	if status != string(model.HealthStatusError) {
		t.Errorf("expected status %s, got %s", model.HealthStatusError, status)
	}
}

func TestAlertNilMQTT(t *testing.T) {
	t.Helper()
	store := &mockStorage{}
	pipeline := NewAlertPipeline(5*time.Minute, true, store, nil, "mibee-nvr")

	event := makeEvent(t, "cam-1", string(model.HealthEventConnectionLost), string(model.HealthStatusError), "Camera offline")
	if err := pipeline.HandleEvent("cam-1", event); err != nil {
		t.Fatalf("HandleEvent with nil MQTT returned error: %v", err)
	}

	// Storage should still be called
	stored := store.insertedEvents(t)
	if len(stored) != 1 {
		t.Fatalf("expected 1 stored event, got %d", len(stored))
	}
}

func TestAlertStatusUpdateOnNewEvent(t *testing.T) {
	t.Helper()
	cooldown := 100 * time.Millisecond
	pipeline, _, _ := newTestPipeline(t, cooldown, true)

	// First event: error
	_ = pipeline.HandleEvent("cam-1", makeEvent(t, "cam-1", string(model.HealthEventConnectionLost), string(model.HealthStatusError), "Offline"))
	if s := pipeline.GetCameraStatus("cam-1"); s != string(model.HealthStatusError) {
		t.Fatalf("expected error status, got %s", s)
	}

	// Wait for cooldown
	time.Sleep(cooldown + 50*time.Millisecond)

	// Second event: healthy (restored)
	_ = pipeline.HandleEvent("cam-1", makeEvent(t, "cam-1", string(model.HealthEventConnectionRestored), string(model.HealthStatusHealthy), "Restored"))
	if s := pipeline.GetCameraStatus("cam-1"); s != string(model.HealthStatusHealthy) {
		t.Fatalf("expected healthy status, got %s", s)
	}
}

func TestAlertPipelinePerMessageCooldown(t *testing.T) {
	t.Helper()
	cooldown := 5 * time.Minute
	pipeline, store, _ := newTestPipeline(t, cooldown, true)

	event1 := makeEvent(t, "cam-1", string(model.HealthEventStreamAnomaly), string(model.HealthStatusWarning), "IDR interval too long")
	event2 := makeEvent(t, "cam-1", string(model.HealthEventStreamAnomaly), string(model.HealthStatusWarning), "Low FPS detected")

	// First event — should dispatch
	if err := pipeline.HandleEvent("cam-1", event1); err != nil {
		t.Fatalf("first HandleEvent returned error: %v", err)
	}

	// Different message within same cooldown — should NOT be suppressed
	if err := pipeline.HandleEvent("cam-1", event2); err != nil {
		t.Fatalf("second HandleEvent (different message) returned error: %v", err)
	}

	stored := store.insertedEvents(t)
	if len(stored) != 2 {
		t.Fatalf("expected 2 stored events (different messages under same event type), got %d", len(stored))
	}
	if stored[0].Message != "IDR interval too long" {
		t.Errorf("expected first event message 'IDR interval too long', got %s", stored[0].Message)
	}
	if stored[1].Message != "Low FPS detected" {
		t.Errorf("expected second event message 'Low FPS detected', got %s", stored[1].Message)
	}
}

func TestAlertPipelineSeverityEscalation(t *testing.T) {
	t.Helper()
	cooldown := 50 * time.Millisecond
	pipeline, store, _ := newTestPipeline(t, cooldown, true)

	event := makeEvent(t, "cam-1", string(model.HealthEventStreamAnomaly), string(model.HealthStatusWarning), "IDR interval too long")

	// First emit — should stay warning
	if err := pipeline.HandleEvent("cam-1", event); err != nil {
		t.Fatalf("first HandleEvent returned error: %v", err)
	}

	stored := store.insertedEvents(t)
	if len(stored) != 1 {
		t.Fatalf("expected 1 stored event, got %d", len(stored))
	}
	if stored[0].Status != string(model.HealthStatusWarning) {
		t.Errorf("expected first event status %s, got %s", model.HealthStatusWarning, stored[0].Status)
	}

	// Wait for cooldown to expire
	time.Sleep(cooldown + 50*time.Millisecond)

	// Second emit — same message, should escalate to error
	if err := pipeline.HandleEvent("cam-1", event); err != nil {
		t.Fatalf("second HandleEvent returned error: %v", err)
	}

	stored = store.insertedEvents(t)
	if len(stored) != 2 {
		t.Fatalf("expected 2 stored events, got %d", len(stored))
	}
	if stored[1].Status != string(model.HealthStatusError) {
		t.Errorf("expected second event status %s (escalated), got %s", model.HealthStatusError, stored[1].Status)
	}
}
