package camera

import (
	"context"
	"errors"
	"testing"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestStatusSnapshot_FailedStartCameraExposesError is the core regression test
// for the bug where ONVIF cameras whose recorder failed to start (e.g. camera IP
// changed → "no route to host" at GetProfiles) were invisible to the health
// manager → never auto-remediated → never rediscovered.
//
// The fix: markStartFailed records the camera, and statusSnapshot (wired into the
// health manager via SetHealthManager) surfaces it as StatusError so the existing
// CheckAll → restart → blacklist → rediscovery chain can self-heal it.
func TestStatusSnapshot_FailedStartCameraExposesError(t *testing.T) {
	mgr := NewCameraManager(testConfig(), nil, nil, "")

	// No cameras tracked initially.
	assert.Empty(t, mgr.statusSnapshot(), "no cameras should be tracked initially")

	// Simulate ONVIF recorder Start() failure (camera IP changed, unreachable).
	mgr.markStartFailed("cam-onvif-1", errors.New("dial tcp 192.168.63.201:8080: no route to host"))

	// The camera must now be visible to the health loop as StatusError — this is
	// the trigger status that auto-remediate.Check acts on (auto_remediate.go:123).
	snap := mgr.statusSnapshot()
	require.Contains(t, snap, "cam-onvif-1",
		"failed-start camera must be visible to the health manager's status loop")
	assert.Equal(t, string(model.StatusError), snap["cam-onvif-1"],
		"failed-start camera must be reported as StatusError so auto-remediate can act on it")

	// Clear it (simulating successful restart after rediscovery finds the new IP).
	mgr.clearStartFailed("cam-onvif-1")
	snap = mgr.statusSnapshot()
	assert.NotContains(t, snap, "cam-onvif-1",
		"camera should be removed from failed-start tracking after clearStartFailed")
}

// TestStatusSnapshot_FailedStartDoesNotShadowActiveRecorder verifies the guard:
// if a camera is in BOTH cm.recorders (live recorder) AND cm.failedStartCameras
// (stale entry from a prior failed start that was restarted but not cleaned),
// the real recorder status wins. A successfully restarted camera must NOT be
// reported as StatusError by a stale failed-start entry.
func TestStatusSnapshot_FailedStartDoesNotShadowActiveRecorder(t *testing.T) {
	mgr := NewCameraManager(testConfig(), nil, nil, "")

	// Camera failed to start, then was restarted successfully but the stale
	// failedStartCameras entry wasn't cleaned (defensive scenario).
	mgr.markStartFailed("cam-onvif-1", errors.New("old failure"))
	mgr.SetTestRecorder("cam-onvif-1", &mockStatusRecorder{st: model.StatusRecording})

	snap := mgr.statusSnapshot()
	require.Contains(t, snap, "cam-onvif-1")
	assert.Equal(t, string(model.StatusRecording), snap["cam-onvif-1"],
		"active recorder's real status must dominate the stale failed-start entry")
}

// TestMarkStartFailed_Idempotent ensures calling markStartFailed multiple times
// (e.g. health loop retries → restart → fails again) doesn't accumulate or panic.
func TestMarkStartFailed_Idempotent(t *testing.T) {
	mgr := NewCameraManager(testConfig(), nil, nil, "")

	mgr.markStartFailed("cam-1", errors.New("first failure"))
	mgr.markStartFailed("cam-1", errors.New("second failure"))
	mgr.markStartFailed("cam-2", errors.New("other camera"))

	snap := mgr.statusSnapshot()
	assert.Len(t, snap, 2, "two distinct cameras should be tracked")
	assert.Equal(t, string(model.StatusError), snap["cam-1"])
	assert.Equal(t, string(model.StatusError), snap["cam-2"])

	// Clearing one doesn't affect the other.
	mgr.clearStartFailed("cam-1")
	snap = mgr.statusSnapshot()
	assert.NotContains(t, snap, "cam-1")
	assert.Contains(t, snap, "cam-2")

	// Clearing a non-existent camera is a no-op (no panic).
	mgr.clearStartFailed("never-existed")
}

// mockStatusRecorder is a minimal model.Recorder for testing statusSnapshot.
type mockStatusRecorder struct {
	st model.RecorderStatus
}

func (m *mockStatusRecorder) Start(ctx context.Context) error { return nil }
func (m *mockStatusRecorder) Stop() error                     { return nil }
func (m *mockStatusRecorder) Status() model.RecorderStatus    { return m.st }
