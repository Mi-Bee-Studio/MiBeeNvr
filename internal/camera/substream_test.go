package camera

// Tests for the sub-stream target resolver (#513): config → pull target for
// the on-demand substream manager. The ONVIF GetStreamUri branch needs a live
// device and is exercised on real hardware (M5); here we cover the config
// matrix that gates it.

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/substream"
)

func TestResolveSubTarget(t *testing.T) {
	cfg := &config.Config{Cameras: []config.CameraConfig{
		{
			ID:           "cam-rtsp-sub",
			Protocol:     "rtsp",
			URL:          "rtsp://192.168.1.10:554/main",
			SubStreamURL: "rtsp://192.168.1.10:554/sub",
			Username:     "admin",
			Password:     "secret",
		},
		{ID: "cam-rtsp-nosub", Protocol: "rtsp", URL: "rtsp://192.168.1.11:554/main"},
		{
			ID:              "cam-onvif-token",
			Protocol:        "onvif",
			URL:             "http://192.168.1.12/onvif/device_service",
			SubProfileToken: "profile_2",
		},
		{ID: "cam-onvif-manual", Protocol: "onvif", URL: "http://192.168.1.13/onvif/device_service", SubStreamURL: "rtsp://192.168.1.13:554/sub2"},
		{ID: "cam-onvif-nothing", Protocol: "onvif", URL: "http://192.168.1.14/onvif/device_service"},
		{ID: "cam-xiaomi", Protocol: "xiaomi"},
	}}
	cm := NewCameraManager(cfg, nil, nil, "")

	// rtsp: manual sub URL + credentials pass through.
	tgt, ok, err := cm.resolveSubTarget(context.Background(), "cam-rtsp-sub")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "rtsp://192.168.1.10:554/sub", tgt.URL)
	require.Equal(t, "admin", tgt.Username)
	require.Equal(t, "secret", tgt.Password)

	// rtsp without sub_stream_url: not available.
	_, ok, err = cm.resolveSubTarget(context.Background(), "cam-rtsp-nosub")
	require.NoError(t, err)
	require.False(t, ok)

	// onvif with a discovered token but no live device: resolution errors
	// (surfaced to Acquire as "resolve sub-stream: …", which egress treats as
	// fallback-to-main — never a hard failure).
	_, ok, err = cm.resolveSubTarget(context.Background(), "cam-onvif-token")
	require.Error(t, err)
	require.False(t, ok)

	// onvif with manual sub_stream_url wins without touching the device.
	tgt, ok, err = cm.resolveSubTarget(context.Background(), "cam-onvif-manual")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "rtsp://192.168.1.13:554/sub2", tgt.URL)

	// onvif with neither: not available.
	_, ok, err = cm.resolveSubTarget(context.Background(), "cam-onvif-nothing")
	require.NoError(t, err)
	require.False(t, ok)

	// unsupported protocol: not available.
	_, ok, err = cm.resolveSubTarget(context.Background(), "cam-xiaomi")
	require.NoError(t, err)
	require.False(t, ok)

	// unknown camera: not available.
	_, ok, err = cm.resolveSubTarget(context.Background(), "cam-missing")
	require.NoError(t, err)
	require.False(t, ok)
}

// TestSubStreamsAccessors verifies the manager-level surface egress endpoints
// use: nil-safe when the constructor ran with a nil config (reduced test
// constructors), ErrNoSubStream for cameras without sub configuration.
func TestSubStreamsAccessors(t *testing.T) {
	cfg := &config.Config{Cameras: []config.CameraConfig{
		{ID: "cam-1", Protocol: "rtsp", URL: "rtsp://192.168.1.10:554/main"},
	}}
	cm := NewCameraManager(cfg, nil, nil, "")
	require.NotNil(t, cm.SubStreams())

	_, err := cm.AcquireSubStream(context.Background(), "cam-1")
	require.ErrorIs(t, err, substream.ErrNoSubStream)
	// Release on a never-acquired camera must not panic.
	cm.ReleaseSubStream("cam-1")
	cm.ReleaseSubStream("cam-unknown")
}
