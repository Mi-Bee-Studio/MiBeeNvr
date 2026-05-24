package health

import (
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
)

const testThreshold = 5 * time.Second

// newTestMonitor creates a ConnectionMonitor for testing.
func newTestMonitor(threshold time.Duration) (*ConnectionMonitor, *[]model.HealthEvent) {
	var events []model.HealthEvent
	handler := func(cameraID string, event model.HealthEvent) {
		events = append(events, event)
	}
	return NewConnectionMonitor(threshold, handler), &events
}

// getEvents safely reads collected events.
func getEvents(t *testing.T, events *[]model.HealthEvent) []model.HealthEvent {
	t.Helper()
	// events slice is append-only from handler, safe to read
	return *events
}

func TestConnectionLostAfterThreshold(t *testing.T) {
	t.Helper()
	monitor, events := newTestMonitor(testThreshold)

	// Camera starts in error state
	monitor.OnStatusChange("cam-1", string(model.StatusError))
	// Advance time past threshold in the monitor's internal state
	monitor.cameras["cam-1"].statusSince = time.Now().Add(-testThreshold - time.Second)

	// Check should detect threshold breach
	monitor.Check()

	got := getEvents(t, events)
	if len(got) != 1 {
		t.Fatalf("expected 1 event, got %d", len(got))
	}
	if got[0].EventType != string(model.HealthEventConnectionLost) {
		t.Errorf("expected event type %s, got %s", model.HealthEventConnectionLost, got[0].EventType)
	}
	if got[0].CameraID != "cam-1" {
		t.Errorf("expected camera ID cam-1, got %s", got[0].CameraID)
	}
	if got[0].Status != string(model.HealthStatusError) {
		t.Errorf("expected status %s, got %s", model.HealthStatusError, got[0].Status)
	}
}

func TestConnectionLostAfterReconnectingThreshold(t *testing.T) {
	t.Helper()
	monitor, events := newTestMonitor(testThreshold)

	// Camera enters reconnecting state
	monitor.OnStatusChange("cam-1", string(model.StatusReconnecting))
	monitor.cameras["cam-1"].statusSince = time.Now().Add(-testThreshold - time.Second)

	monitor.Check()

	got := getEvents(t, events)
	if len(got) != 1 {
		t.Fatalf("expected 1 event, got %d", len(got))
	}
	if got[0].EventType != string(model.HealthEventConnectionLost) {
		t.Errorf("expected event type %s, got %s", model.HealthEventConnectionLost, got[0].EventType)
	}
}

func TestConnectionRestored(t *testing.T) {
	t.Helper()
	monitor, events := newTestMonitor(testThreshold)

	// Camera goes into error, passes threshold, gets alert
	monitor.OnStatusChange("cam-1", string(model.StatusError))
	monitor.cameras["cam-1"].statusSince = time.Now().Add(-testThreshold - time.Second)
	monitor.Check()

	// Camera recovers to recording
	monitor.OnStatusChange("cam-1", string(model.StatusRecording))

	got := getEvents(t, events)
	if len(got) != 2 {
		t.Fatalf("expected 2 events (lost + restored), got %d", len(got))
	}
	if got[1].EventType != string(model.HealthEventConnectionRestored) {
		t.Errorf("expected event type %s, got %s", model.HealthEventConnectionRestored, got[1].EventType)
	}
	if got[1].Status != string(model.HealthStatusHealthy) {
		t.Errorf("expected status %s, got %s", model.HealthStatusHealthy, got[1].Status)
	}
}

func TestConnectionNoFalseEventsOnQuickReconnect(t *testing.T) {
	t.Helper()
	monitor, events := newTestMonitor(testThreshold)

	// Camera briefly errors then recovers — all under threshold
	monitor.OnStatusChange("cam-1", string(model.StatusError))
	// Check immediately — not past threshold
	monitor.Check()

	monitor.OnStatusChange("cam-1", string(model.StatusRecording))
	monitor.Check()

	got := getEvents(t, events)
	if len(got) != 0 {
		t.Fatalf("expected 0 events for quick reconnect, got %d: %+v", len(got), got)
	}
}

func TestConnectionNoEventWhenAlreadyAlerted(t *testing.T) {
	t.Helper()
	monitor, events := newTestMonitor(testThreshold)

	// Camera errors, passes threshold, gets alert
	monitor.OnStatusChange("cam-1", string(model.StatusError))
	monitor.cameras["cam-1"].statusSince = time.Now().Add(-testThreshold - time.Second)
	monitor.Check()

	// Check again — should NOT emit duplicate
	monitor.Check()

	got := getEvents(t, events)
	if len(got) != 1 {
		t.Fatalf("expected 1 event (no duplicate), got %d", len(got))
	}
}

func TestConnectionNoRestoredWithoutPriorLost(t *testing.T) {
	t.Helper()
	monitor, events := newTestMonitor(testThreshold)

	// Camera goes error → recording without ever crossing threshold
	monitor.OnStatusChange("cam-1", string(model.StatusError))
	// Immediately recovers (no Check() called, no alert fired)
	monitor.OnStatusChange("cam-1", string(model.StatusRecording))

	got := getEvents(t, events)
	if len(got) != 0 {
		t.Fatalf("expected 0 events (no prior lost alert), got %d", len(got))
	}
}

func TestConnectionMultipleCameras(t *testing.T) {
	t.Helper()
	monitor, events := newTestMonitor(testThreshold)

	// Cam-1 errors and crosses threshold
	monitor.OnStatusChange("cam-1", string(model.StatusError))
	monitor.cameras["cam-1"].statusSince = time.Now().Add(-testThreshold - time.Second)

	// Cam-2 errors but hasn't crossed threshold yet
	monitor.OnStatusChange("cam-2", string(model.StatusError))

	monitor.Check()

	got := getEvents(t, events)
	if len(got) != 1 {
		t.Fatalf("expected 1 event (cam-1 only), got %d", len(got))
	}
	if got[0].CameraID != "cam-1" {
		t.Errorf("expected event for cam-1, got %s", got[0].CameraID)
	}

	// Cam-2 now crosses threshold
	monitor.cameras["cam-2"].statusSince = time.Now().Add(-testThreshold - time.Second)
	monitor.Check()

	got = getEvents(t, events)
	if len(got) != 2 {
		t.Fatalf("expected 2 events total, got %d", len(got))
	}
	if got[1].CameraID != "cam-2" {
		t.Errorf("expected event for cam-2, got %s", got[1].CameraID)
	}
}

func TestConnectionStatusFlipBeforeThreshold(t *testing.T) {
	t.Helper()
	monitor, events := newTestMonitor(testThreshold)

	// Camera flips: error → reconnecting → error
	monitor.OnStatusChange("cam-1", string(model.StatusError))
	// Simulate time passing but not enough
	monitor.cameras["cam-1"].statusSince = time.Now().Add(-testThreshold / 2)
	monitor.Check()

	// Flip to reconnecting (resets timer)
	monitor.OnStatusChange("cam-1", string(model.StatusReconnecting))
	monitor.Check()

	// Still not past threshold from reconnecting start
	got := getEvents(t, events)
	if len(got) != 0 {
		t.Fatalf("expected 0 events, got %d", len(got))
	}
}

func TestConnectionRemoveCamera(t *testing.T) {
	t.Helper()
	monitor, events := newTestMonitor(testThreshold)

	monitor.OnStatusChange("cam-1", string(model.StatusError))
	monitor.cameras["cam-1"].statusSince = time.Now().Add(-testThreshold - time.Second)
	monitor.RemoveCamera("cam-1")

	// Check should not emit — camera removed
	monitor.Check()

	got := getEvents(t, events)
	if len(got) != 0 {
		t.Fatalf("expected 0 events after removal, got %d", len(got))
	}
}

func TestConnectionFirstStatusIgnored(t *testing.T) {
	t.Helper()
	monitor, events := newTestMonitor(testThreshold)

	// First status report for a camera — should not trigger any transition
	monitor.OnStatusChange("cam-new", string(model.StatusError))
	monitor.cameras["cam-new"].statusSince = time.Now().Add(-testThreshold - time.Second)
	monitor.Check()

	// The first status does register and Check() should fire since it IS in error state
	got := getEvents(t, events)
	if len(got) != 1 {
		t.Fatalf("expected 1 event (first status can trigger Check), got %d", len(got))
	}
}

func TestConnectionRestoredFromReconnecting(t *testing.T) {
	t.Helper()
	monitor, events := newTestMonitor(testThreshold)

	// Camera reconnecting, passes threshold
	monitor.OnStatusChange("cam-1", string(model.StatusReconnecting))
	monitor.cameras["cam-1"].statusSince = time.Now().Add(-testThreshold - time.Second)
	monitor.Check()

	// Recovers directly from reconnecting to recording
	monitor.OnStatusChange("cam-1", string(model.StatusRecording))

	got := getEvents(t, events)
	if len(got) != 2 {
		t.Fatalf("expected 2 events (lost + restored), got %d", len(got))
	}
	if got[1].EventType != string(model.HealthEventConnectionRestored) {
		t.Errorf("expected restored event, got %s", got[1].EventType)
	}
}
