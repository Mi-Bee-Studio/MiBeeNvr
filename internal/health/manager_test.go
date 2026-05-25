package health

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
)

// --- Mocks ---

// mockRecorder implements model.Recorder and supports GetHub().
type mockRecorder struct {
	hub    *model.StreamHub
	mu     sync.Mutex
	status model.RecorderStatus
}

func (r *mockRecorder) GetHub() *model.StreamHub {
	return r.hub
}

func (r *mockRecorder) Start(_ context.Context) error { return nil }
func (r *mockRecorder) Stop() error                   { return nil }
func (r *mockRecorder) Status() model.RecorderStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.status
}

func newMockRecorderWithHub() *mockRecorder {
	return &mockRecorder{
		hub:    model.NewStreamHub(),
		status: model.StatusRecording,
	}
}

// newMockRecorderWithoutHub returns a recorder that doesn't implement GetHub.
type mockRecorderNoHub struct{}

func (r *mockRecorderNoHub) Start(_ context.Context) error { return nil }
func (r *mockRecorderNoHub) Stop() error                   { return nil }
func (r *mockRecorderNoHub) Status() model.RecorderStatus  { return model.StatusRecording }

// --- Helpers ---

// newTestManagerConfig returns an enabled HealthConfig for testing.
func newTestManagerConfig() config.HealthConfig {
	return config.HealthConfig{
		Enabled:         true,
		EventsRetention: "720h",
		Alerts: config.HealthAlertsConfig{
			Cooldown: "5m",
			MQTT:     false,
		},
		Layer1: config.HealthLayer1Config{
			OfflineThreshold: "30s",
		},
		Layer2: config.HealthLayer2Config{
			BitrateChangeThreshold: 0.5,
			MinFPS:                 5,
			MaxIDRInterval:         "30s",
		},
		Layer2_5: config.HealthLayer2_5Config{
			FreezeTimeout: "10s",
		},
	}
}

// newDisabledConfig returns a disabled HealthConfig.
func newDisabledConfig() config.HealthConfig {
	return config.HealthConfig{Enabled: false}
}

// newTestManager creates a Manager with mock deps for testing.
func newTestManager(t *testing.T, cfg config.HealthConfig) *Manager {
	t.Helper()
	return NewManager(cfg, nil)
}

// --- Tests ---

func TestManagerCreation(t *testing.T) {
	t.Helper()
	m := newTestManager(t, newTestManagerConfig())
	if m == nil {
		t.Fatal("expected non-nil manager with enabled config")
	}
	if m.conn == nil {
		t.Error("expected connection monitor to be initialized")
	}
	if m.collector == nil {
		t.Error("expected stream stats collector to be initialized")
	}
	if m.freeze == nil {
		t.Error("expected freeze detector to be initialized")
	}
	if m.pipeline == nil {
		t.Error("expected alert pipeline to be initialized")
	}
}

func TestManagerDisabled(t *testing.T) {
	t.Helper()
	m := newTestManager(t, newDisabledConfig())
	if m != nil {
		t.Fatal("expected nil manager when health is disabled")
	}
}

func TestManagerOnCameraAdded(t *testing.T) {
	t.Helper()
	m := newTestManager(t, newTestManagerConfig())
	rec := newMockRecorderWithHub()

	m.OnCameraAdded("cam-1", rec)

	// Verify StreamHub has subscribers
	if count := rec.hub.ConsumerCount(); count != 2 {
		t.Errorf("expected 2 hub consumers (stats + freeze), got %d", count)
	}

	// Verify connection monitor tracks the camera
	m.conn.mu.Lock()
	_, exists := m.conn.cameras["cam-1"]
	m.conn.mu.Unlock()
	if !exists {
		t.Error("expected camera to be tracked in connection monitor")
	}

	// Verify freeze detector tracks the camera
	m.freeze.mu.Lock()
	_, freezeExists := m.freeze.cameras["cam-1"]
	m.freeze.mu.Unlock()
	if !freezeExists {
		t.Error("expected camera to be tracked in freeze detector")
	}
}

func TestManagerOnCameraAddedNoHub(t *testing.T) {
	t.Helper()
	m := newTestManager(t, newTestManagerConfig())
	rec := &mockRecorderNoHub{}

	// Should not panic when recorder has no hub
	m.OnCameraAdded("cam-1", rec)

	// Connection monitor should still track the camera
	m.conn.mu.Lock()
	_, exists := m.conn.cameras["cam-1"]
	m.conn.mu.Unlock()
	if !exists {
		t.Error("expected camera to be tracked in connection monitor even without hub")
	}
}

func TestManagerOnCameraRemoved(t *testing.T) {
	t.Helper()
	m := newTestManager(t, newTestManagerConfig())
	rec := newMockRecorderWithHub()

	// Add first
	m.OnCameraAdded("cam-1", rec)
	if count := rec.hub.ConsumerCount(); count != 2 {
		t.Fatalf("expected 2 consumers after add, got %d", count)
	}

	// Remove
	m.OnCameraRemoved("cam-1", rec)

	// Verify hub consumers are gone
	if count := rec.hub.ConsumerCount(); count != 0 {
		t.Errorf("expected 0 hub consumers after removal, got %d", count)
	}

	// Verify connection monitor removed the camera
	m.conn.mu.Lock()
	_, connExists := m.conn.cameras["cam-1"]
	m.conn.mu.Unlock()
	if connExists {
		t.Error("expected camera to be removed from connection monitor")
	}

	// Verify freeze detector removed the camera
	m.freeze.mu.Lock()
	_, freezeExists := m.freeze.cameras["cam-1"]
	m.freeze.mu.Unlock()
	if freezeExists {
		t.Error("expected camera to be removed from freeze detector")
	}

	// Verify collector removed the camera
	m.collector.mu.Lock()
	_, collectorExists := m.collector.cameras["cam-1"]
	m.collector.mu.Unlock()
	if collectorExists {
		t.Error("expected camera to be removed from collector")
	}
}

func TestManagerOnStatusChange(t *testing.T) {
	t.Helper()
	m := newTestManager(t, newTestManagerConfig())
	rec := newMockRecorderWithHub()
	m.OnCameraAdded("cam-1", rec)

	// Simulate status change to error
	m.OnStatusChange("cam-1", string(model.StatusError))

	// Connection monitor should track the new status
	m.conn.mu.Lock()
	state := m.conn.cameras["cam-1"]
	m.conn.mu.Unlock()
	if state == nil {
		t.Fatal("expected camera state to exist")
	}
	if state.currentStatus != string(model.StatusError) {
		t.Errorf("expected status %s, got %s", model.StatusError, state.currentStatus)
	}

	// Freeze detector should update recording state
	m.freeze.mu.Lock()
	freezeState := m.freeze.cameras["cam-1"]
	m.freeze.mu.Unlock()
	if freezeState == nil {
		t.Fatal("expected freeze state to exist")
	}
	if freezeState.isRecording.Load() {
		t.Error("expected isRecording=false when status is error")
	}
}

func TestManagerStartStop(t *testing.T) {
	t.Helper()
	m := newTestManager(t, newTestManagerConfig())

	ctx := context.Background()
	if err := m.Start(ctx); err != nil {
		t.Fatalf("Start returned error: %v", err)
	}

	// Give the goroutine a moment to start
	time.Sleep(50 * time.Millisecond)

	// Verify it's running by checking cancel is set
	if m.cancel == nil {
		t.Error("expected cancel function to be set after Start")
	}

	m.Stop()

	// Verify done channel is closed
	select {
	case <-m.done:
		// Good — done channel is closed
	default:
		t.Error("expected done channel to be closed after Stop")
	}
}

func TestManagerStartStopNil(t *testing.T) {
	t.Helper()
	// Calling Start/Stop on nil manager should be no-op
	var m *Manager
	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start on nil manager returned error: %v", err)
	}
	m.Stop() // Should not panic
}

func TestManagerOnCameraAddedNil(t *testing.T) {
	t.Helper()
	var m *Manager
	m.OnCameraAdded("cam-1", newMockRecorderWithHub()) // Should not panic
}

func TestManagerOnCameraRemovedNil(t *testing.T) {
	t.Helper()
	var m *Manager
	m.OnCameraRemoved("cam-1", newMockRecorderWithHub()) // Should not panic
}

func TestManagerOnStatusChangeNil(t *testing.T) {
	t.Helper()
	var m *Manager
	m.OnStatusChange("cam-1", string(model.StatusError)) // Should not panic
}

func TestManagerGetCameraHealth(t *testing.T) {
	t.Helper()
	m := newTestManager(t, newTestManagerConfig())

	// No events — should return nil status (pipeline returns unknown)
	health := m.GetCameraHealth("cam-unknown")
	if health == nil {
		t.Fatal("expected non-nil CameraHealth")
	}
	if health.CameraID != "cam-unknown" {
		t.Errorf("expected camera ID cam-unknown, got %s", health.CameraID)
	}
}

func TestManagerGetCameraHealthNil(t *testing.T) {
	t.Helper()
	var m *Manager
	if h := m.GetCameraHealth("cam-1"); h != nil {
		t.Error("expected nil from nil manager")
	}
}

func TestManagerGetAllHealth(t *testing.T) {
	t.Helper()
	m := newTestManager(t, newTestManagerConfig())

	// No events yet
	all := m.GetAllHealth()
	if len(all) != 0 {
		t.Errorf("expected empty map, got %d entries", len(all))
	}

	// Trigger some events via pipeline
	_ = m.pipeline.HandleEvent("cam-1", model.HealthEvent{
		CameraID:  "cam-1",
		EventType: string(model.HealthEventConnectionLost),
		Status:    string(model.HealthStatusError),
		Message:   "Offline",
	})

	all = m.GetAllHealth()
	if len(all) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(all))
	}
	if all["cam-1"].CameraID != "cam-1" {
		t.Errorf("expected camera ID cam-1, got %s", all["cam-1"].CameraID)
	}
}

func TestManagerGetAllHealthNil(t *testing.T) {
	t.Helper()
	var m *Manager
	if h := m.GetAllHealth(); h != nil {
		t.Error("expected nil from nil manager")
	}
}

func TestManagerAllLayersFeedPipeline(t *testing.T) {
	t.Helper()
	m := newTestManager(t, newTestManagerConfig())
	store := &mockStorage{}
	m.pipeline.storage = store

	// Layer 1: Connection monitor detects offline
	m.conn.OnStatusChange("cam-1", string(model.StatusError))
	// Simulate time past threshold
	m.conn.mu.Lock()
	m.conn.cameras["cam-1"].statusSince = time.Now().Add(-31 * time.Second)
	m.conn.mu.Unlock()
	m.conn.Check()

	events := store.insertedEvents(t)
	if len(events) != 1 {
		t.Fatalf("expected 1 event from connection monitor, got %d", len(events))
	}
	if events[0].EventType != string(model.HealthEventConnectionLost) {
		t.Errorf("expected connection_lost, got %s", events[0].EventType)
	}
}

func TestManagerDoubleStart(t *testing.T) {
	t.Helper()
	m := newTestManager(t, newTestManagerConfig())

	ctx := context.Background()
	if err := m.Start(ctx); err != nil {
		t.Fatalf("first Start returned error: %v", err)
	}
	// Second start should work without panic (overwrites cancel)
	if err := m.Start(ctx); err != nil {
		t.Fatalf("second Start returned error: %v", err)
	}
	m.Stop()
}

func TestManager_StatusPolling(t *testing.T) {
	t.Helper()

	// Shared mutable status map for the closure
	statuses := map[string]string{"cam-1": "recording"}

	m := newTestManager(t, newTestManagerConfig())
	m.SetStatusFunc(func() map[string]string {
		return statuses
	})

	// Add camera (sets initial status in conn and knownStatuses)
	rec := newMockRecorderWithHub()
	m.OnCameraAdded("cam-1", rec)

	// Initial poll — no transition expected
	m.pollStatuses()

	// Verify initial state
	m.conn.mu.Lock()
	state := m.conn.cameras["cam-1"]
	m.conn.mu.Unlock()
	if state == nil {
		t.Fatal("expected camera state to exist")
	}
	if state.currentStatus != string(model.StatusRecording) {
		t.Errorf("expected status %s, got %s", model.StatusRecording, state.currentStatus)
	}

	// Simulate status change to error
	statuses["cam-1"] = "error"

	// Poll — should detect transition
	m.pollStatuses()

	// Verify connection monitor updated
	m.conn.mu.Lock()
	state = m.conn.cameras["cam-1"]
	m.conn.mu.Unlock()
	if state == nil {
		t.Fatal("expected camera state to exist after change")
	}
	if state.currentStatus != string(model.StatusError) {
		t.Errorf("expected status %s, got %s", model.StatusError, state.currentStatus)
	}

	// Verify freeze detector updated
	m.freeze.mu.Lock()
	freezeState := m.freeze.cameras["cam-1"]
	m.freeze.mu.Unlock()
	if freezeState == nil {
		t.Fatal("expected freeze state to exist")
	}
	if freezeState.isRecording.Load() {
		t.Error("expected isRecording=false when status is error")
	}
}

func TestManagerOnStatusChangeResetsCollector(t *testing.T) {
	t.Helper()
	m := newTestManager(t, newTestManagerConfig())
	rec := newMockRecorderWithHub()
	m.OnCameraAdded("cam-1", rec)

	// Simulate frames to set lastIDRTime on the collector
	cb := m.collector.OnFrame("cam-1")
	for i := 0; i < 5; i++ {
		au := makeH264Frame(t, 1, 500) // non-IDR
		cb(int64(i), au)
	}
	// Feed IDR frame to set lastIDRTime
	idrAU := makeH264Frame(t, 5, 500)
	cb(10, idrAU)

	// Wait a moment so old time is measurably in the past
	time.Sleep(2 * time.Millisecond)

	oldStats := m.collector.GetStats("cam-1")
	oldIDRTime := oldStats.LastIDRTime

	// Simulate status transition: recording → error → recording (reconnect)
	m.OnStatusChange("cam-1", string(model.StatusError))
	m.OnStatusChange("cam-1", string(model.StatusRecording))

	// Verify collector was reset — lastIDRTime should be refreshed
	newStats := m.collector.GetStats("cam-1")
	if newStats.LastIDRTime.Before(oldIDRTime) || newStats.LastIDRTime.Equal(oldIDRTime) {
		t.Error("expected lastIDRTime to be reset to current time after reconnect")
	}

	// Verify counters were reset
	if newStats.FrameCount != 0 {
		t.Errorf("expected frame count 0 after reset, got %d", newStats.FrameCount)
	}
}

func TestManagerAutoRemediation(t *testing.T) {
	t.Helper()

	cfg := newTestManagerConfig()
	cfg.AutoRemediation = config.HealthAutoRemediationConfig{
		Enabled:            true,
		MaxRestartsPerHour: 10,
		CooldownMinutes:    1,
		BlacklistHours:     1,
		GlobalMaxPerMin:    10,
	}
	m := newTestManager(t, cfg)
	if m == nil {
		t.Fatal("expected non-nil manager")
	}
	if m.autoRemediate == nil {
		t.Fatal("expected autoRemediate to be created when Enabled=true")
	}

	var mu sync.Mutex
	var restartCalled bool

	m.SetRestarter(func(_ context.Context, cameraID string) error {
		mu.Lock()
		restartCalled = true
		mu.Unlock()
		return nil
	})

	m.SetCameraEnabledFn(func(cameraID string) bool {
		return true
	})

	// Simulate camera with StatusError — should trigger restart
	m.knownStatuses["cam-1"] = string(model.StatusError)
	m.autoRemediate.CheckAll(m.knownStatuses)

	mu.Lock()
	called := restartCalled
	mu.Unlock()

	if !called {
		t.Error("expected restarter to be called for camera with StatusError")
	}

	// Reset and test with non-error status — should NOT trigger restart
	restartCalled = false
	delete(m.knownStatuses, "cam-1")
	m.knownStatuses["cam-1"] = string(model.StatusReconnecting)
	m.autoRemediate.CheckAll(m.knownStatuses)

	mu.Lock()
	called = restartCalled
	mu.Unlock()

	if called {
		t.Error("expected restarter NOT to be called for StatusReconnecting")
	}
}
