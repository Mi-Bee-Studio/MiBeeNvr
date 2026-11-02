package camera

// Dual-spectrum GB auto-labeling: a channel the device's catalog marks as
// thermal (PhotoelectricImagingType=2, GB/T 28181-2022 — gb28181-go v0.15.1)
// enrolls with imaging_type=thermal and the default policy that follows it
// (vision skip). The label applies only at CREATE time — a manual
// imaging_type edit on an enrolled camera is never overwritten.

import (
	"context"
	"testing"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/stretchr/testify/require"
)

func TestEnsureGB28181CameraThermalAutoLabel(t *testing.T) {
	t.Parallel()
	cfg := testConfig()
	cfg.Cameras = nil
	mgr, _ := newManagerWithBus(t, cfg)
	mgr.SetGB28181ChannelImagingProbe(func(deviceID, channelID string) (int, bool) {
		require.Equal(t, "34020000001110000001", deviceID)
		if channelID == "34020000001320000002" {
			return 2, true // thermal marking from the catalog
		}
		return 1, true // visible light
	})

	require.NoError(t, mgr.EnsureGB28181Camera("34020000001110000001", "34020000001320000001", "可见光通道", ""))
	require.NoError(t, mgr.EnsureGB28181Camera("34020000001110000001", "34020000001320000002", "热成像通道", ""))

	vis := mgr.GetCameraConfig("gb-34020000001320000001")
	require.NotNil(t, vis)
	require.Empty(t, vis.ImagingType, "visible-light marking must not set imaging_type")
	require.False(t, cfg.Vision.ShouldSkipCamera("gb-34020000001320000001"))

	hot := mgr.GetCameraConfig("gb-34020000001320000002")
	require.NotNil(t, hot)
	require.Equal(t, config.ImagingTypeThermal, hot.ImagingType)
	require.True(t, cfg.Vision.ShouldSkipCamera("gb-34020000001320000002"),
		"thermal auto-label must apply the AI-push skip default policy")

	// CREATE-time only: a later manual edit is never clobbered by a
	// re-enroll of the same channel (idempotent path returns early) or by
	// the probe at all.
	visType := "visible"
	_, err := mgr.UpdateCamera(context.Background(), "gb-34020000001320000002", CameraUpdate{ImagingType: &visType})
	require.NoError(t, err)
	require.NoError(t, mgr.EnsureGB28181Camera("34020000001110000001", "34020000001320000002", "热成像通道", ""))
	require.Equal(t, "visible", mgr.GetCameraConfig("gb-34020000001320000002").ImagingType)
}

func TestEnsureGB28181CameraNoProbeNoLabel(t *testing.T) {
	t.Parallel()
	cfg := testConfig()
	cfg.Cameras = nil
	mgr, _ := newManagerWithBus(t, cfg) // no probe wired

	require.NoError(t, mgr.EnsureGB28181Camera("34020000001110000001", "34020000001320000003", "", ""))
	cam := mgr.GetCameraConfig("gb-34020000001320000003")
	require.NotNil(t, cam)
	require.Empty(t, cam.ImagingType, "without a probe the enroll must not guess an imaging type")
	require.False(t, cfg.Vision.ShouldSkipCamera("gb-34020000001320000003"))
}
