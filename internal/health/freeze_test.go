package health

import (
	"sync"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
)

const testFreezeTimeout = 5 * time.Second

// newTestFreezeDetector creates a FreezeDetector for testing.
func newTestFreezeDetector(timeout time.Duration) (*FreezeDetector, *[]model.HealthEvent) {
	var events []model.HealthEvent
	var mu sync.Mutex
	handler := func(cameraID string, event model.HealthEvent) {
		mu.Lock()
		events = append(events, event)
		mu.Unlock()
	}
	return NewFreezeDetector(timeout, handler), &events
}

// collectFreezeEvents safely reads collected events.
func collectFreezeEvents(t *testing.T, events *[]model.HealthEvent) []model.HealthEvent {
	t.Helper()
	return *events
}

func TestFreezeDetected(t *testing.T) {
	t.Helper()
	detector, events := newTestFreezeDetector(testFreezeTimeout)

	detector.SetRecording("cam-1", true)
	// Simulate no frames for > threshold
	detector.cameras["cam-1"].lastFrameTime.Store(time.Now().Add(-testFreezeTimeout - time.Second))

	detector.Check()

	got := collectFreezeEvents(t, events)
	if len(got) != 1 {
		t.Fatalf("expected 1 freeze event, got %d", len(got))
	}
	if got[0].EventType != string(model.HealthEventFreezeDetected) {
		t.Errorf("expected event type %s, got %s", model.HealthEventFreezeDetected, got[0].EventType)
	}
	if got[0].CameraID != "cam-1" {
		t.Errorf("expected camera ID cam-1, got %s", got[0].CameraID)
	}
	if got[0].Status != string(model.HealthStatusError) {
		t.Errorf("expected status %s, got %s", model.HealthStatusError, got[0].Status)
	}
}

func TestFreezeRecovered(t *testing.T) {
	t.Helper()
	detector, events := newTestFreezeDetector(testFreezeTimeout)

	// First trigger a freeze
	detector.SetRecording("cam-1", true)
	detector.cameras["cam-1"].lastFrameTime.Store(time.Now().Add(-testFreezeTimeout - time.Second))
	detector.Check()

	// Now frames resume
	detector.OnFrameReceived("cam-1")

	got := collectFreezeEvents(t, events)
	if len(got) != 2 {
		t.Fatalf("expected 2 events (freeze + recovery), got %d", len(got))
	}
	if got[1].EventType != string(model.HealthEventFreezeRecovered) {
		t.Errorf("expected event type %s, got %s", model.HealthEventFreezeRecovered, got[1].EventType)
	}
	if got[1].CameraID != "cam-1" {
		t.Errorf("expected camera ID cam-1, got %s", got[1].CameraID)
	}
	if got[1].Status != string(model.HealthStatusHealthy) {
		t.Errorf("expected status %s, got %s", model.HealthStatusHealthy, got[1].Status)
	}
}

func TestNoFreezeWhenStopped(t *testing.T) {
	t.Helper()
	detector, events := newTestFreezeDetector(testFreezeTimeout)

	// Not recording — should not detect freeze
	detector.SetRecording("cam-1", false)
	detector.cameras["cam-1"].lastFrameTime.Store(time.Now().Add(-testFreezeTimeout - time.Second))
	detector.Check()

	got := collectFreezeEvents(t, events)
	if len(got) != 0 {
		t.Fatalf("expected 0 events (stopped, not recording), got %d", len(got))
	}
}

func TestNoFreezeUnderThreshold(t *testing.T) {
	t.Helper()
	detector, events := newTestFreezeDetector(testFreezeTimeout)

	detector.SetRecording("cam-1", true)
	// Brief gap under threshold
	detector.cameras["cam-1"].lastFrameTime.Store(time.Now().Add(-testFreezeTimeout / 2))
	detector.Check()

	got := collectFreezeEvents(t, events)
	if len(got) != 0 {
		t.Fatalf("expected 0 events (under threshold), got %d", len(got))
	}
}

func TestNoDuplicateFreezeEvent(t *testing.T) {
	t.Helper()
	detector, events := newTestFreezeDetector(testFreezeTimeout)

	detector.SetRecording("cam-1", true)
	detector.cameras["cam-1"].lastFrameTime.Store(time.Now().Add(-testFreezeTimeout - time.Second))
	detector.Check() // first detection

	// Check again — should not emit duplicate
	detector.Check()

	got := collectFreezeEvents(t, events)
	if len(got) != 1 {
		t.Fatalf("expected 1 event (no duplicate), got %d", len(got))
	}
}

func TestFreezeMultipleCameras(t *testing.T) {
	t.Helper()
	detector, events := newTestFreezeDetector(testFreezeTimeout)

	// Cam-1 is frozen
	detector.SetRecording("cam-1", true)
	detector.cameras["cam-1"].lastFrameTime.Store(time.Now().Add(-testFreezeTimeout - time.Second))

	// Cam-2 is fine (recent frame)
	detector.SetRecording("cam-2", true)
	detector.cameras["cam-2"].lastFrameTime.Store(time.Now())

	detector.Check()

	got := collectFreezeEvents(t, events)
	if len(got) != 1 {
		t.Fatalf("expected 1 event (cam-1 only), got %d", len(got))
	}
	if got[0].CameraID != "cam-1" {
		t.Errorf("expected event for cam-1, got %s", got[0].CameraID)
	}

	// Now cam-2 crosses threshold
	detector.cameras["cam-2"].lastFrameTime.Store(time.Now().Add(-testFreezeTimeout - time.Second))
	detector.Check()

	got = collectFreezeEvents(t, events)
	if len(got) != 2 {
		t.Fatalf("expected 2 events total, got %d", len(got))
	}
	if got[1].CameraID != "cam-2" {
		t.Errorf("expected event for cam-2, got %s", got[1].CameraID)
	}
}

func TestFreezeNoRecoveryWithoutPriorFreeze(t *testing.T) {
	t.Helper()
	detector, events := newTestFreezeDetector(testFreezeTimeout)

	// Frame received but no prior freeze
	detector.SetRecording("cam-1", true)
	detector.OnFrameReceived("cam-1")

	got := collectFreezeEvents(t, events)
	if len(got) != 0 {
		t.Fatalf("expected 0 events (no prior freeze), got %d", len(got))
	}
}

func TestFreezeSetRecordingResetsTimer(t *testing.T) {
	t.Helper()
	detector, events := newTestFreezeDetector(testFreezeTimeout)

	// Start recording
	detector.SetRecording("cam-1", true)
	detector.cameras["cam-1"].lastFrameTime.Store(time.Now().Add(-testFreezeTimeout - time.Second))

	// Stop recording — timer should NOT reset (no longer recording)
	detector.SetRecording("cam-1", false)
	detector.cameras["cam-1"].lastFrameTime.Store(time.Now().Add(-testFreezeTimeout - time.Second))

	// Start recording again — transition resets timer
	detector.SetRecording("cam-1", true)

	detector.Check()

	got := collectFreezeEvents(t, events)
	if len(got) != 0 {
		t.Fatalf("expected 0 events (timer reset on start), got %d", len(got))
	}
}

func TestRemoveCameraFreeze(t *testing.T) {
	t.Helper()
	detector, events := newTestFreezeDetector(testFreezeTimeout)

	detector.SetRecording("cam-1", true)
	detector.cameras["cam-1"].lastFrameTime.Store(time.Now().Add(-testFreezeTimeout - time.Second))
	detector.RemoveCamera("cam-1")

	detector.Check()

	got := collectFreezeEvents(t, events)
	if len(got) != 0 {
		t.Fatalf("expected 0 events after removal, got %d", len(got))
	}
}
