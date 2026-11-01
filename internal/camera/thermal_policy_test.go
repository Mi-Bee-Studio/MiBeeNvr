package camera

// Thermal-channel default policy (dual-spectrum support): a camera whose
// imaging_type is (or becomes) "thermal" is appended to vision.skip_cameras —
// visible-light detection models only produce noise on thermal frames. The
// append is transition-based and idempotent; manual removal must survive
// later unrelated edits.

import (
	"context"
	"testing"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/stretchr/testify/require"
)

func TestThermalVisionSkipOnAdd(t *testing.T) {
	t.Parallel()
	cfg := testConfig()
	cfg.Cameras = nil
	mgr, _ := newManagerWithBus(t, cfg)

	_, err := mgr.AddCamera(context.Background(), config.CameraConfig{
		ID: "cam-hot", Name: "Thermal", Protocol: "http", Encoding: "jpeg",
		URL: "http://127.0.0.1:1/frame", ImagingType: config.ImagingTypeThermal,
	})
	require.NoError(t, err)
	_, err = mgr.AddCamera(context.Background(), config.CameraConfig{
		ID: "cam-vis", Name: "Visible", Protocol: "http", Encoding: "jpeg",
		URL: "http://127.0.0.1:1/frame2",
	})
	require.NoError(t, err)

	require.True(t, cfg.Vision.ShouldSkipCamera("cam-hot"), "thermal camera must land in vision.skip_cameras")
	require.False(t, cfg.Vision.ShouldSkipCamera("cam-vis"), "visible camera must not")

	// The skip list is persisted with the same config write.
	reloaded, err := config.Load(mgr.configPath)
	require.NoError(t, err)
	require.True(t, reloaded.Vision.ShouldSkipCamera("cam-hot"))
}

func TestThermalVisionSkipOnUpdateTransition(t *testing.T) {
	t.Parallel()
	cfg := testConfig()
	cfg.Cameras = []config.CameraConfig{
		{ID: "cam-x", Name: "Dual", Protocol: "http", Encoding: "jpeg", URL: "http://127.0.0.1:1/f"},
	}
	mgr, _ := newManagerWithBus(t, cfg)

	thermal := config.ImagingTypeThermal
	_, err := mgr.UpdateCamera(context.Background(), "cam-x", CameraUpdate{ImagingType: &thermal})
	require.NoError(t, err)
	require.Equal(t, config.ImagingTypeThermal, mgr.GetCameraConfig("cam-x").ImagingType)
	require.True(t, cfg.Vision.ShouldSkipCamera("cam-x"))

	// A later unrelated edit must NOT duplicate the entry.
	name := "Dual 2"
	_, err = mgr.UpdateCamera(context.Background(), "cam-x", CameraUpdate{Name: &name})
	require.NoError(t, err)
	count := 0
	for _, id := range cfg.Vision.SkipCameras {
		if id == "cam-x" {
			count++
		}
	}
	require.Equal(t, 1, count, "skip entry must stay unique across later edits")

	// A deliberate manual un-skip survives later edits (removal is manual).
	cfg.Vision.SkipCameras = nil
	vis := "visible"
	_, err = mgr.UpdateCamera(context.Background(), "cam-x", CameraUpdate{ImagingType: &vis})
	require.NoError(t, err)
	require.False(t, cfg.Vision.ShouldSkipCamera("cam-x"))
	_, err = mgr.UpdateCamera(context.Background(), "cam-x", CameraUpdate{ImagingType: &thermal})
	require.NoError(t, err)
	require.True(t, cfg.Vision.ShouldSkipCamera("cam-x"), "re-marking thermal re-applies the default")
}
