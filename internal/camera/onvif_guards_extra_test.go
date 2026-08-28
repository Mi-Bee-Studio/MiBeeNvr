package camera

// Guard-path coverage for camera ONVIF client getters (#583): every getter
// fails fast (no network) for unknown or non-ONVIF cameras before any
// client construction. Plus the recorder-factory fallbacks for protocols
// without recorders.

import (
	"context"
	"testing"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/stretchr/testify/require"
)

func TestONVIFGetterGuards(t *testing.T) {
	t.Parallel()
	cfg := testConfig()
	cfg.Cameras = []config.CameraConfig{
		{ID: "plain-rtsp", Protocol: "rtsp", Encoding: "h264", URL: "rtsp://127.0.0.1:1/x"},
		{ID: "dead-onvif", Protocol: "onvif", ONVIFEndpoint: "http://127.0.0.1:1/onvif/device_service"},
	}
	cm, _, _, _ := newTestManagerWithCfg(t, cfg)
	ctx := context.Background()

	// Non-ONVIF camera → ONVIFNotCameraError from every getter, no dialing.
	for _, fn := range []func(string) error{
		func(id string) error { _, err := cm.GetONVIFClient(ctx, id); return err },
		func(id string) error { _, err := cm.GetONVIFPTZController(ctx, id); return err },
		func(id string) error { _, err := cm.GetImagingController(ctx, id); return err },
		func(id string) error { _, err := cm.GetSnapshotProvider(ctx, id); return err },
		func(id string) error { _, err := cm.GetDeviceManager(ctx, id); return err },
	} {
		require.Error(t, fn("plain-rtsp"), "non-ONVIF camera must be rejected before dialing")
		require.Error(t, fn("ghost"), "unknown camera must be rejected")
	}

	// ONVIF camera with a dead endpoint → ONVIFConnectionError (connection
	// refused on loopback port 1 — fails fast, no real dialing).
	_, err := cm.GetONVIFClient(ctx, "dead-onvif")
	require.Error(t, err)

	// closeAllONVIFClients resets the caches without panicking.
	cm.closeAllONVIFClients()
}

func TestRecorderFactoryUnknownProtocol(t *testing.T) {
	t.Parallel()
	cm, _, _, _ := newTestManager(t)

	rec := cm.createRecorder(
		config.CameraConfig{ID: "weird", Protocol: "carrier-pigeon", Encoding: "h264"},
		0,
	)
	require.Nil(t, rec, "unknown protocol must produce no recorder")
}

func TestGetCachedDeviceInfoMiss(t *testing.T) {
	t.Parallel()
	cm, _, _, _ := newTestManager(t)

	// Unknown camera → miss, no dialing.
	require.Nil(t, cm.GetCachedDeviceInfo(context.Background(), "ghost"))
}
