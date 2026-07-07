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

	// New field changes → not equal.
	require.False(t, targetConfigEqual(base, PushTargetConfig{ID: "a", Name: "n", Protocol: "rtmp", URL: "rtmp://h/live/k", Enabled: true, Platform: "youtube"}))
	require.False(t, targetConfigEqual(base, PushTargetConfig{ID: "a", Name: "n", Protocol: "rtmp", URL: "rtmp://h/live/k", Enabled: true, TranscodePolicy: "force_sw"}))

	// VideoPresetOverride change → not equal.
	withOverride := PushTargetConfig{
		ID: "a", Name: "n", Protocol: "rtmp", URL: "rtmp://h/live/k", Enabled: true,
		VideoPresetOverride: &VideoPresetOverrides{Resolution: "1920x1080", Framerate: 30, VideoBitrateKbps: 4500, GopSeconds: 2, Profile: "high", Bframes: 1},
	}
	require.False(t, targetConfigEqual(base, withOverride))
	require.True(t, targetConfigEqual(withOverride, withOverride))

	// Different VideoPresetOverride field → not equal.
	withOverrideAlt := PushTargetConfig{
		ID: "a", Name: "n", Protocol: "rtmp", URL: "rtmp://h/live/k", Enabled: true,
		VideoPresetOverride: &VideoPresetOverrides{Resolution: "1280x720"},
	}
	require.False(t, targetConfigEqual(withOverride, withOverrideAlt))

	// nil vs non-nil VideoPresetOverride → not equal.
	require.False(t, targetConfigEqual(withOverride, base))
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

func TestManager_ListAllPresets_ReturnsFive(t *testing.T) {
	m := NewManager(nil, nil)
	m.presetRegistry = NewPresetRegistry()
	presets := m.ListAllPresets()
	require.Len(t, presets, 5)

	// Verify sorted order.
	for i := 1; i < len(presets); i++ {
		require.LessOrEqual(t, presets[i-1].Name, presets[i].Name,
			"presets must be sorted by name")
	}
}

func TestManager_GetPreset_Found(t *testing.T) {
	m := NewManager(nil, nil)
	m.presetRegistry = NewPresetRegistry()

	p, ok := m.GetPreset("youtube")
	require.True(t, ok)
	require.Equal(t, "youtube", p.Name)
	require.Equal(t, "YouTube Live", p.Description)
}

func TestManager_GetPreset_NotFound(t *testing.T) {
	m := NewManager(nil, nil)
	m.presetRegistry = NewPresetRegistry()

	p, ok := m.GetPreset("nonexistent")
	require.False(t, ok)
	require.Empty(t, p.Name)
}

func TestManager_Presets_NilManager(t *testing.T) {
	var m *Manager
	require.Nil(t, m.ListAllPresets())

	p, ok := m.GetPreset("youtube")
	require.False(t, ok)
	require.Empty(t, p.Name)
}
