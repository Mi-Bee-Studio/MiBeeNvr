package relay

import (
	"testing"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/stretchr/testify/require"
)

// TestTargetConfigEqual verifies the equality function that drives the
// SetCameraTargets diff (a mismatch triggers a reconnect).
func TestTargetConfigEqual(t *testing.T) {
	base := PushTargetConfig{ID: "a", Name: "n", Protocol: "rtmp", URL: "rtmp://h/live/k", Enabled: true}
	require.True(t, targetConfigEqual(base, base))

	// Any field change → not equal.
	require.False(t, targetConfigEqual(base, PushTargetConfig{ID: "a", Name: "n", Protocol: "rtmp", URL: "rtmp://h/live/k", Enabled: false}))
	require.False(t, targetConfigEqual(base, PushTargetConfig{ID: "a", Name: "changed", Protocol: "rtmp", URL: "rtmp://h/live/k", Enabled: true}))
	require.False(t, targetConfigEqual(base, PushTargetConfig{ID: "a", Name: "n", Protocol: "rtsp", URL: "rtmp://h/live/k", Enabled: true}))
	require.False(t, targetConfigEqual(base, PushTargetConfig{ID: "a", Name: "n", Protocol: "rtmp", URL: "rtmp://h/live/other", Enabled: true}))
	require.False(t, targetConfigEqual(base, PushTargetConfig{ID: "b", Name: "n", Protocol: "rtmp", URL: "rtmp://h/live/k", Enabled: true}))
}

// TestTernary covers the tiny helper used in manager logging.
func TestTernary(t *testing.T) {
	require.Equal(t, "yes", ternary(true, "yes", "no"))
	require.Equal(t, "no", ternary(false, "yes", "no"))
}

// TestManager_NilSafe verifies the manager methods are nil-safe so camera
// Add/Update/Remove don't need nil checks at call sites.
func TestManager_NilSafe(t *testing.T) {
	var m *Manager
	// All of these must be no-ops, not panics.
	m.SetCameraTargets("cam", []config.PushTargetConfig{})
	m.RemoveCamera("cam")
	require.Equal(t, []TargetStatus{}, m.CameraStatus("cam"))
}

// TestStatus_Defaults verifies the exported status constants and TargetStatus
// zero value behave as the API/UI expect.
func TestStatus_Defaults(t *testing.T) {
	require.Equal(t, RelayStatus("idle"), StatusIdle)
	require.Equal(t, RelayStatus("streaming"), StatusStreaming)
	ts := TargetStatus{ID: "x", Status: StatusStreaming, Protocol: "rtmp"}
	require.Equal(t, "streaming", string(ts.Status))
}
