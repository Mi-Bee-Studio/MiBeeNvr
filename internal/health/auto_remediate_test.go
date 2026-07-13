package health

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
)

// --- Mocks ---

// mockRestartFn tracks RestartRecorder calls.
type mockRestartFn struct {
	mu    sync.Mutex
	calls []string // cameraIDs of restart calls
	err   error    // error to return (nil by default)
}

func (m *mockRestartFn) call(_ context.Context, cameraID string) error {
	m.mu.Lock()
	m.calls = append(m.calls, cameraID)
	m.mu.Unlock()
	return m.err
}

func (m *mockRestartFn) callCount(t *testing.T) int {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.calls)
}

func (m *mockRestartFn) calledWith(t *testing.T) []string {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]string, len(m.calls))
	copy(cp, m.calls)
	return cp
}

// --- Helpers ---

// atomicBool wraps an atomic bool for use as IsCameraEnabledFunc.
type atomicBool struct {
	val atomic.Bool
}

func (a *atomicBool) isEnabled(_ string) bool {
	return a.val.Load()
}

// newTestRemediator creates an AutoRemediator with sensible test defaults.
// Default config: enabled, 3 restarts/hr, 5min cooldown, 1hr blacklist, 10 global/min.
func newTestRemediator(t *testing.T) (*AutoRemediator, *mockRestartFn, *atomicBool) {
	t.Helper()
	return newTestRemediatorWithConfig(t, config.HealthAutoRemediationConfig{
		Enabled:            true,
		MaxRestartsPerHour: 3,
		CooldownMinutes:    5,
		BlacklistHours:     1,
		GlobalMaxPerMin:    10,
	})
}

// newTestRemediatorWithConfig creates an AutoRemediator with the given config.
func newTestRemediatorWithConfig(t *testing.T, cfg config.HealthAutoRemediationConfig) (*AutoRemediator, *mockRestartFn, *atomicBool) {
	t.Helper()
	enabled := &atomicBool{}
	enabled.val.Store(true)
	restartMock := &mockRestartFn{}
	r := NewAutoRemediator(cfg, restartMock.call, enabled.isEnabled)
	return r, restartMock, enabled
}

// --- Tests ---

func TestAutoRemediator_TriggersOnStatusError(t *testing.T) {
	t.Parallel()
	r, mock, _ := newTestRemediator(t)

	err := r.Check("cam-1", string(model.StatusError))
	if err != nil {
		t.Fatalf("Check() returned error: %v", err)
	}
	if got := mock.callCount(t); got != 1 {
		t.Fatalf("expected 1 restart call, got %d", got)
	}
	if got := mock.calledWith(t); len(got) != 1 || got[0] != "cam-1" {
		t.Fatalf("expected restart for cam-1, got %v", got)
	}
}

func TestAutoRemediator_IgnoresStatusReconnecting(t *testing.T) {
	t.Parallel()
	r, mock, _ := newTestRemediator(t)

	// Without offlineDurationFn wired, reconnecting is conservatively ignored
	// (no duration info to gate on).
	err := r.Check("cam-1", string(model.StatusReconnecting))
	if err != nil {
		t.Fatalf("Check() returned error: %v", err)
	}
	if got := mock.callCount(t); got != 0 {
		t.Fatalf("expected 0 restart calls for reconnecting without duration fn, got %d", got)
	}

	// Also test StatusRecording is ignored.
	err = r.Check("cam-2", string(model.StatusRecording))
	if err != nil {
		t.Fatalf("Check() returned error: %v", err)
	}
	if got := mock.callCount(t); got != 0 {
		t.Fatalf("expected 0 restart calls for recording, got %d", got)
	}
}

// TestAutoRemediator_ReconnectingTimeoutTriggersRestart covers the IP-change
// scenario: a recorder stuck in reconnecting beyond ReconnectingTimeoutMinutes
// gets a hard restart (which can then escalate to blacklist + rediscovery).
// Without this, a camera whose IP changed loops forever in its own reconnect
// backoff and rediscovery never fires.
func TestAutoRemediator_ReconnectingTimeoutTriggersRestart(t *testing.T) {
	t.Parallel()
	r, mock, _ := newTestRemediator(t)
	r.SetOfflineDurationFn(func(string) time.Duration { return 15 * time.Minute }) // > 10min default

	err := r.Check("cam-1", string(model.StatusReconnecting))
	if err != nil {
		t.Fatalf("Check() returned error: %v", err)
	}
	if got := mock.callCount(t); got != 1 {
		t.Fatalf("expected 1 restart call after reconnecting timeout, got %d", got)
	}
}

func TestAutoRemediator_ReconnectingBelowTimeoutIgnored(t *testing.T) {
	t.Parallel()
	r, mock, _ := newTestRemediator(t)
	r.SetOfflineDurationFn(func(string) time.Duration { return 2 * time.Minute }) // < 10min default

	err := r.Check("cam-1", string(model.StatusReconnecting))
	if err != nil {
		t.Fatalf("Check() returned error: %v", err)
	}
	if got := mock.callCount(t); got != 0 {
		t.Fatalf("expected 0 restart calls for brief reconnecting, got %d", got)
	}
}

func TestAutoRemediator_RespectsMaxRestartsPerHour(t *testing.T) {
	t.Parallel()
	cfg := config.HealthAutoRemediationConfig{
		Enabled:            true,
		MaxRestartsPerHour: 2,
		CooldownMinutes:    0, // no cooldown for this test
		BlacklistHours:     1,
		GlobalMaxPerMin:    100,
	}
	r, mock, _ := newTestRemediatorWithConfig(t, cfg)

	// First two should succeed.
	for i := range 2 {
		err := r.Check("cam-1", string(model.StatusError))
		if err != nil {
			t.Fatalf("Check %d returned error: %v", i+1, err)
		}
	}
	if got := mock.callCount(t); got != 2 {
		t.Fatalf("expected 2 restart calls, got %d", got)
	}

	// Third attempt should be rate-limited (max 2/hour).
	err := r.Check("cam-1", string(model.StatusError))
	if err == nil {
		t.Fatal("expected error on 3rd attempt within rate limit")
	}
	if got := mock.callCount(t); got != 2 {
		t.Fatalf("expected still 2 restart calls after rate limit, got %d", got)
	}
}

func TestAutoRemediator_BlacklistAfterMaxFailures(t *testing.T) {
	t.Parallel()
	cfg := config.HealthAutoRemediationConfig{
		Enabled:            true,
		MaxRestartsPerHour: 3,
		CooldownMinutes:    0,
		BlacklistHours:     1,
		GlobalMaxPerMin:    100,
	}
	r, mock, _ := newTestRemediatorWithConfig(t, cfg)

	// Exhaust all 3 attempts.
	for i := range 3 {
		err := r.Check("cam-1", string(model.StatusError))
		if err != nil {
			t.Fatalf("Check %d returned error: %v", i+1, err)
		}
	}
	if got := mock.callCount(t); got != 3 {
		t.Fatalf("expected 3 restart calls, got %d", got)
	}

	// Camera should now be blacklisted.
	if !r.IsBlacklisted("cam-1") {
		t.Fatal("expected cam-1 to be blacklisted after 3 failures")
	}

	// 4th attempt should be blocked by blacklist.
	err := r.Check("cam-1", string(model.StatusError))
	if err == nil {
		t.Fatal("expected error on blacklisted camera")
	}
	if got := mock.callCount(t); got != 3 {
		t.Fatalf("expected still 3 restart calls after blacklist, got %d", got)
	}
}

func TestAutoRemediator_GlobalRateLimit(t *testing.T) {
	t.Parallel()
	cfg := config.HealthAutoRemediationConfig{
		Enabled:            true,
		MaxRestartsPerHour: 100,
		CooldownMinutes:    0,
		BlacklistHours:     1,
		GlobalMaxPerMin:    2, // only 2 restarts per minute globally
	}
	r, mock, _ := newTestRemediatorWithConfig(t, cfg)

	// First two cameras succeed.
	err := r.Check("cam-1", string(model.StatusError))
	if err != nil {
		t.Fatalf("Check cam-1 returned error: %v", err)
	}
	err = r.Check("cam-2", string(model.StatusError))
	if err != nil {
		t.Fatalf("Check cam-2 returned error: %v", err)
	}
	if got := mock.callCount(t); got != 2 {
		t.Fatalf("expected 2 restart calls, got %d", got)
	}

	// Third camera should be blocked by global rate limit.
	err = r.Check("cam-3", string(model.StatusError))
	if err == nil {
		t.Fatal("expected error on 3rd global restart within 1 minute")
	}
	if got := mock.callCount(t); got != 2 {
		t.Fatalf("expected still 2 restart calls after global rate limit, got %d", got)
	}
}

func TestAutoRemediator_IgnoresDisabledCamera(t *testing.T) {
	t.Parallel()
	r, mock, enabled := newTestRemediator(t)

	// Disable the camera.
	enabled.val.Store(false)

	err := r.Check("cam-1", string(model.StatusError))
	if err != nil {
		t.Fatalf("Check() returned error: %v", err)
	}
	if got := mock.callCount(t); got != 0 {
		t.Fatalf("expected 0 restart calls for disabled camera, got %d", got)
	}
}

func TestAutoRemediator_CooldownAfterAttempt(t *testing.T) {
	t.Parallel()
	cfg := config.HealthAutoRemediationConfig{
		Enabled:            true,
		MaxRestartsPerHour: 100,
		CooldownMinutes:    5, // 5-minute cooldown
		BlacklistHours:     1,
		GlobalMaxPerMin:    100,
	}
	r, mock, _ := newTestRemediatorWithConfig(t, cfg)

	// First attempt succeeds.
	err := r.Check("cam-1", string(model.StatusError))
	if err != nil {
		t.Fatalf("first Check returned error: %v", err)
	}
	if got := mock.callCount(t); got != 1 {
		t.Fatalf("expected 1 restart call, got %d", got)
	}

	// Immediate second attempt blocked by cooldown.
	err = r.Check("cam-1", string(model.StatusError))
	if err == nil {
		t.Fatal("expected error on 2nd attempt during cooldown")
	}
	if got := mock.callCount(t); got != 1 {
		t.Fatalf("expected still 1 restart call during cooldown, got %d", got)
	}
}

// --- CheckAll test ---

func TestAutoRemediator_CheckAll(t *testing.T) {
	t.Parallel()
	r, mock, _ := newTestRemediator(t)

	statuses := map[string]string{
		"cam-1": string(model.StatusError),
		"cam-2": string(model.StatusReconnecting),
		"cam-3": string(model.StatusRecording),
		"cam-4": string(model.StatusError),
	}

	r.CheckAll(statuses)

	calls := mock.calledWith(t)
	if len(calls) != 2 {
		t.Fatalf("expected 2 restart calls, got %d: %v", len(calls), calls)
	}
	// Both cam-1 and cam-4 should have been restarted.
	expected := map[string]bool{"cam-1": true, "cam-4": true}
	for _, id := range calls {
		if !expected[id] {
			t.Fatalf("unexpected restart for camera %s", id)
		}
	}
}

// --- Disabled config test ---

func TestAutoRemediator_DisabledConfig(t *testing.T) {
	t.Parallel()
	cfg := config.HealthAutoRemediationConfig{
		Enabled:            false,
		MaxRestartsPerHour: 3,
		CooldownMinutes:    5,
		BlacklistHours:     1,
		GlobalMaxPerMin:    10,
	}
	r, mock, _ := newTestRemediatorWithConfig(t, cfg)

	err := r.Check("cam-1", string(model.StatusError))
	if err != nil {
		t.Fatalf("Check() returned error: %v", err)
	}
	if got := mock.callCount(t); got != 0 {
		t.Fatalf("expected 0 restart calls when disabled, got %d", got)
	}
}
