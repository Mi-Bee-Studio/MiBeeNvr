package camera

// Public-adapter and accessor-surface tests (#568): pkg/camera.Manager
// wrapper, error-detail map, hub registry helpers, RTMP key map, protocol
// toggle, and the pure PTZ vector mapping. All deterministic — no network,
// no recorder startup required.

import (
	"context"
	"testing"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/onvif"
	pkgcamera "github.com/Mi-Bee-Studio/MiBeeNvr/pkg/camera"
	"github.com/stretchr/testify/require"
)

func TestPublicAdapter_Surface(t *testing.T) {
	mgr, _, _, _ := newTestManager(t)
	pub := mgr.AsPublic()

	cams := pub.List()
	require.NotEmpty(t, cams)
	ids := map[string]bool{}
	for _, c := range cams {
		ids[c.ID()] = true
	}
	require.True(t, ids["cam-h264"], "configured cameras must be listed")

	cam, err := pub.Get("cam-h264")
	require.NoError(t, err)
	require.Equal(t, "cam-h264", cam.ID())
	require.Equal(t, "H264 Camera", cam.Name())
	require.Equal(t, "rtsp", cam.Protocol())
	require.Equal(t, "h264", cam.Encoding())
	require.False(t, cam.AudioEnabled(), "audio disabled by default")

	_, err = pub.Get("no-such")
	require.ErrorIs(t, err, pkgcamera.ErrCameraNotFound)

	// Status: unknown camera → NotFound; known camera maps recorder state.
	_, err = pub.Status("no-such")
	require.ErrorIs(t, err, pkgcamera.ErrCameraNotFound)
	st, err := pub.Status("cam-h264")
	require.NoError(t, err)
	require.Equal(t, "cam-h264", st.ID)
	require.False(t, st.Recording, "no recorder started — not recording")

	// An error detail surfaces in the public status.
	mgr.SetErrorDetail("cam-h264", &model.CameraErrorDetail{Message: "boom"})
	st, err = pub.Status("cam-h264")
	require.NoError(t, err)
	require.Equal(t, "boom", st.Error)
	mgr.SetErrorDetail("cam-h264", nil)
	require.Nil(t, mgr.GetErrorDetail("cam-h264"))

	// Hub: unknown camera → NotFound; GetOrCreateHub-backed camera returns
	// a wrapped hub.
	_, err = pub.Hub("no-such")
	require.ErrorIs(t, err, pkgcamera.ErrCameraNotFound)
}

func TestGetOrCreateHub_Idempotent(t *testing.T) {
	mgr, _, _, _ := newTestManager(t)
	h1 := mgr.GetOrCreateHub("cam-h264")
	require.NotNil(t, h1)
	h2 := mgr.GetOrCreateHub("cam-h264")
	require.Same(t, h1, h2, "second call must return the existing hub")
	require.NotNil(t, mgr.GetHub("cam-h264"))
	require.Nil(t, mgr.GetHub("no-such"))

	hubs := mgr.Hubs()
	require.Len(t, hubs, 1)
	require.Same(t, h1, hubs["cam-h264"])
}

func TestGetSPS_EmptyWhenNoRecorder(t *testing.T) {
	mgr, _, _, _ := newTestManager(t)
	sps, pps, isH264 := mgr.GetSPS("cam-h264")
	require.Nil(t, sps)
	require.Nil(t, pps)
	require.False(t, isH264)
	require.Empty(t, mgr.GetSourceCodec("cam-h264"))
}

func TestRTMPKeyMap(t *testing.T) {
	mgr, _, _, _ := newTestManager(t)
	require.Empty(t, mgr.RTMPKeyMap(), "no RTMP cameras configured")

	hub := mgr.GetOrCreateHub("cam-h264")
	require.NotNil(t, hub)
}

func TestSetProtocolEnabled_NoopPath(t *testing.T) {
	mgr, _, _, _ := newTestManager(t)
	// Disabling a protocol with no running recorders must be a clean no-op.
	mgr.SetProtocolEnabled("rtsp", false)
	mgr.SetProtocolEnabled("rtsp", true)
}

func TestArchiveActivate(t *testing.T) {
	mgr, _, _, _ := newTestManager(t)
	ctx := context.Background()

	// Archive removes the camera from the live config (DB rows are archived
	// separately; a config-only camera has no DB row).
	require.NoError(t, mgr.ArchiveCamera(ctx, "cam-h264"))
	require.Nil(t, mgr.GetCameraConfig("cam-h264"), "archived camera leaves the config")

	// Archiving an unknown camera errors cleanly.
	require.Error(t, mgr.ArchiveCamera(ctx, "no-such"))

	// Activating an archived (removed) camera fails cleanly.
	require.Error(t, mgr.ActivateCamera(ctx, "cam-h264", "admin", "secret"))
}

func TestGB28181Accessors_NilWithoutRecorder(t *testing.T) {
	mgr, _, _, _ := newTestManager(t)
	require.Nil(t, mgr.GB28181NALUWriter("cam-h264"), "non-GB camera → nil AU writer")
	require.Nil(t, mgr.GB28181AudioWriter("cam-h264"))
	sink, err := mgr.NewGB28181PlaybackSink("cam-h264")
	require.Error(t, err, "no recorder running — sink creation must fail cleanly")
	require.Nil(t, sink)
	require.NoError(t, mgr.ArchiveGB28181Camera("dev", "ch"), "unknown channel archives nothing")
	require.Empty(t, mgr.GB28181SubChannelID("dev", "ch"))
	mgr.OnGB28181Invite("cam-h264")
	mgr.OnGB28181Bye("cam-h264")
	require.NoError(t, mgr.UpdateGB28181DeviceMeta("cam-h264", "fw", "hw"))
}

func TestPTZVectorFor(t *testing.T) {
	require.InDelta(t, 1.0, ptzVectorFor("up", 255).Tilt, 0.001)
	require.InDelta(t, 0.5, -ptzVectorFor("left", 0).Pan, 0.001, "speed 0 falls back to 0.5")
	require.InDelta(t, 128.0/255.0, ptzVectorFor("zoom-in", 128).Zoom, 0.001)
	require.InDelta(t, -0.5, ptzVectorFor("zoom-out", 0).Zoom, 0.001)
	require.Equal(t, onvif.PTZVector{}, ptzVectorFor("stop", 0), "unknown direction → zero vector")
	require.InDelta(t, 0.5, ptzVectorFor("down-right", 0).Pan, 0.001)
}
