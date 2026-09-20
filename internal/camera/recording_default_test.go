package camera

import (
	"testing"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The global recording.default_enabled (settings toggle for pure-live
// deployments): cameras without an explicit recording_enabled inherit it.
func TestCamerasInheritingRecordingDefault(t *testing.T) {
	mgr, _, _, _ := newTestManager(t)
	cfg := mgr.cfg

	cfg.Cameras = []config.CameraConfig{
		{ID: "nil-cam", Protocol: "rtsp", URL: "rtsp://127.0.0.1:554/a"},
		{ID: "explicit-true", Protocol: "rtsp", URL: "rtsp://127.0.0.1:554/b", RecordingEnabled: ptrBool(true)},
		{ID: "explicit-false", Protocol: "rtsp", URL: "rtsp://127.0.0.1:554/c", RecordingEnabled: ptrBool(false)},
		{ID: "nil-gb", Protocol: "gb28181"},
	}

	inherited := mgr.camerasInheritingRecordingDefault()
	assert.ElementsMatch(t, []string{"nil-cam", "nil-gb"}, inherited)
}

// Stopped cameras must not be resurrected by a default change: the recycle
// only touches cameras that are actually running.
func TestApplyRecordingDefaultLeavesStoppedCamerasAlone(t *testing.T) {
	mgr, _, _, _ := newTestManager(t)
	mgr.cfg.Cameras = []config.CameraConfig{
		{ID: "stopped-nil", Protocol: "rtsp", URL: "rtsp://127.0.0.1:554/a"},
	}

	restarted := mgr.ApplyRecordingDefault(false)
	assert.Empty(t, restarted, "a never-started camera must not be started by a default change")
	// Effective gate now resolves through the global default.
	mgr.cfg.Recording.DefaultEnabled = ptrBool(false)
	assert.False(t, mgr.cfg.RecordingGate(mgr.cfg.Cameras[0].RecordingEnabled))
}

// GB28181 auto-enrolled channels (EnsureGB28181Camera) are born with a nil
// gate — they must inherit the global default, so a pure-live box gets
// live-only channels without touching each one.
func TestEnsureGB28181CameraInheritsRecordingDefault(t *testing.T) {
	cfg := testConfig()
	off := false
	cfg.Recording = config.RecordingConfig{DefaultEnabled: &off}
	mgr, _, _, _ := newTestManagerWithCfg(t, cfg)

	require.NoError(t, mgr.EnsureGB28181Camera("34020000002000000001", "34020000001320000099", "", "192.168.63.99"))
	cam := mgr.GetCameraConfig("gb-34020000001320000099")
	require.NotNil(t, cam)
	assert.Nil(t, cam.RecordingEnabled, "auto-enrolled channel keeps nil gate (inherits global)")
	assert.False(t, cfg.RecordingGate(cam.RecordingEnabled))
	assert.Contains(t, mgr.camerasInheritingRecordingDefault(), "gb-34020000001320000099")
}
