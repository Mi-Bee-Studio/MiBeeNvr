package camera

import (
	"testing"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/stretchr/testify/require"
)

// gbDedupConfig builds a config with one ONVIF camera at a fixed IP.
func gbDedupConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg := testConfig()
	cfg.Cameras = []config.CameraConfig{{
		ID:            "front-onvif",
		Name:          "Front ONVIF",
		Protocol:      "onvif",
		URL:           "",
		ONVIFEndpoint: "http://192.168.63.240/onvif/device_service",
	}}
	return cfg
}

func TestCameraIDByHostIP(t *testing.T) {
	mgr, _, _, _ := newTestManagerWithCfg(t, gbDedupConfig(t))

	id, ok := mgr.CameraIDByHostIP("192.168.63.240")
	require.True(t, ok)
	require.Equal(t, "front-onvif", id)

	_, ok = mgr.CameraIDByHostIP("192.168.63.999")
	require.False(t, ok)
	_, ok = mgr.CameraIDByHostIP("")
	require.False(t, ok, "empty IP must never match")
}

func TestCameraIDByHostIP_IgnoresGB28181Cameras(t *testing.T) {
	cfg := gbDedupConfig(t)
	cfg.Cameras = append(cfg.Cameras, config.CameraConfig{
		ID:       "gb-34020000001320000001",
		Protocol: "gb28181",
		GB28181:  config.GB28181ChannelConfig{DeviceID: "d", ChannelID: "34020000001320000001"},
	})
	mgr, _, _, _ := newTestManagerWithCfg(t, cfg)

	// GB cameras have no URL; they must not participate in IP matching.
	_, ok := mgr.CameraIDByHostIP("192.168.63.240")
	require.True(t, ok)
}

func TestEnsureGB28181Camera_SkipsWhenHostCameraExists(t *testing.T) {
	// The camera registers via GB28181 AFTER it was added as ONVIF from the
	// same IP: auto-enroll must skip instead of creating a second entry.
	mgr, _, _, _ := newTestManagerWithCfg(t, gbDedupConfig(t))

	require.NoError(t, mgr.EnsureGB28181Camera(
		"34020000001310000001", "34020000001320000001", "GB Channel", "192.168.63.240"))

	// No gb- camera created; the ONVIF camera is untouched.
	_, ok := mgr.GB28181CameraIDByChannel("34020000001310000001", "34020000001320000001")
	require.False(t, ok, "auto-enroll must be suppressed on host collision")
	snap := mgr.loadSnapshot()
	require.Len(t, snap.configs, 1)
}

func TestEnsureGB28181Camera_EnrollsWhenNoCollision(t *testing.T) {
	mgr, _, _, _ := newTestManagerWithCfg(t, gbDedupConfig(t))

	require.NoError(t, mgr.EnsureGB28181Camera(
		"34020000001310000009", "34020000001320000009", "", "192.168.63.9"))

	id, ok := mgr.GB28181CameraIDByChannel("34020000001310000009", "34020000001320000009")
	require.True(t, ok)
	require.Equal(t, "gb-34020000001320000009", id)
}

func TestEnsureGB28181Camera_EmptySourceIPStillEnrolls(t *testing.T) {
	// Unknown source ("" — e.g. device resolved before NetAddr recorded):
	// behave like today, enroll by channel identity.
	mgr, _, _, _ := newTestManagerWithCfg(t, gbDedupConfig(t))

	require.NoError(t, mgr.EnsureGB28181Camera(
		"34020000001310000002", "34020000001320000002", "", ""))

	_, ok := mgr.GB28181CameraIDByChannel("34020000001310000002", "34020000001320000002")
	require.True(t, ok)
}
