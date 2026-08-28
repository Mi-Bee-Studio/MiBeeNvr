package camera

// Incremental long-tail coverage (#599) beyond accessors_extra_test.go:
// GetStreamURL branch set, the GB28181 playback sink + invite/bye hooks with a
// live (passive) GB recorder, snapshot-URL auto-population against the SOAP
// fake, the disabled-transcode-config guard, H.265/bare-ONVIF codec-info
// branches, and the direct ArchiveCamera flow.

import (
	"context"
	"testing"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/recorder"
	"github.com/stretchr/testify/require"
)

func TestGetStreamURLBranches(t *testing.T) {
	t.Parallel()
	cm, store, _, _ := newTestManagerWithCfg(t, func() *config.Config {
		cfg := testConfig()
		cfg.Cameras = []config.CameraConfig{
			{ID: "rtsp-cam", Protocol: "rtsp", Encoding: "h264", URL: "rtsp://127.0.0.1:1/main"},
			{ID: "onvif-cam", Protocol: "onvif", ONVIFEndpoint: "http://127.0.0.1:1/onvif/device_service"},
		}
		return cfg
	}())

	// RTSP camera: the config URL IS the stream URL.
	require.Equal(t, "rtsp://127.0.0.1:1/main", cm.GetStreamURL("rtsp-cam"))
	// Unknown camera / ONVIF camera without a recorder.
	require.Empty(t, cm.GetStreamURL("ghost"))
	require.Empty(t, cm.GetStreamURL("onvif-cam"))

	// Non-ONVIF recorder behind an ONVIF camera → empty (type assert fails).
	cm.SetTestRecorder("onvif-cam", recorder.NewH264Recorder(
		recorder.H264Config{CameraID: "onvif-cam"}, store))
	require.Empty(t, cm.GetStreamURL("onvif-cam"))

	// Real ONVIF recorder (never started — rtspURL still empty, branch runs).
	cm.SetTestRecorder("onvif-cam", recorder.NewONVIFRecorder(
		recorder.ONVIFConfig{CameraID: "onvif-cam"},
		nil, nil))
	require.Empty(t, cm.GetStreamURL("onvif-cam"), "unstarted ONVIF recorder has no rtspURL yet")
}

func TestGB28181PlaybackSinkAndHooks(t *testing.T) {
	t.Parallel()
	cfg := testConfig()
	cfg.Cameras = []config.CameraConfig{
		{
			ID: "gb-cam", Name: "GB", Protocol: "gb28181", Encoding: "h264",
			GB28181: config.GB28181ChannelConfig{DeviceID: "d", ChannelID: "c"},
		},
		{ID: "srt-cam", Protocol: "srt", Encoding: "h264"},
	}
	cm, _, _, _ := newTestManagerWithCfg(t, cfg)

	// Non-GB / unknown cameras → typed error.
	_, err := cm.NewGB28181PlaybackSink("srt-cam")
	require.ErrorContains(t, err, "not a GB28181 camera")
	_, err = cm.NewGB28181PlaybackSink("ghost")
	require.ErrorContains(t, err, "not a GB28181 camera")

	// GB camera: the sink recorder starts passively and is returned as an
	// AUWriter; stopping it must be clean.
	sink, err := cm.NewGB28181PlaybackSink("gb-cam")
	require.NoError(t, err)
	require.NotNil(t, sink)
	if s, ok := sink.(interface{ Stop() error }); ok {
		require.NoError(t, s.Stop())
	}

	// Invite/Bye hooks: nil-recorder path then a live passive GB recorder.
	cm.OnGB28181Invite("ghost")
	cm.OnGB28181Bye("ghost")
	gbRec := recorder.NewGB28181Recorder(recorder.GB28181Config{CameraID: "gb-live", Encoding: "h264"}, nil)
	require.NoError(t, gbRec.Start(context.Background()))
	t.Cleanup(func() { _ = gbRec.Stop() })
	cm.SetTestRecorder("gb-live", gbRec)
	cm.OnGB28181Invite("gb-live")
	cm.OnGB28181Bye("gb-live")
}

func TestAutoPopulateSnapshotURLViaFake(t *testing.T) {
	t.Parallel()
	fake := newCamSOAPFake(t)

	// Success: URI fetched via the SOAP fake and persisted into config+YAML.
	cfg := testConfig()
	cfg.Cameras = []config.CameraConfig{fake.onvifCamera("snap-cam")}
	cm, _, db, cfgPath := newTestManagerWithCfg(t, cfg)
	require.NoError(t, db.UpsertCamera(context.Background(), "snap-cam", "n", "onvif", "", "", "", "", fake.srv.URL+"/onvif/device_service", "", "", ""))

	cm.autoPopulateSnapshotURL(context.Background(), "snap-cam")
	require.Equal(t, "http://127.0.0.1/snapshot.jpg", cm.GetCameraConfig("snap-cam").SnapshotURL)
	disk, err := config.Load(cfgPath)
	require.NoError(t, err)
	found := false
	for i := range disk.Cameras {
		if disk.Cameras[i].ID == "snap-cam" {
			require.Equal(t, "http://127.0.0.1/snapshot.jpg", disk.Cameras[i].SnapshotURL)
			found = true
		}
	}
	require.True(t, found, "snapshot URL must be persisted to YAML")

	// Empty-profiles device: nothing written, no error surface.
	fake.setEmptyProfiles(true)
	cfg2 := testConfig()
	cfg2.Cameras = []config.CameraConfig{fake.onvifCamera("nop-cam")}
	cm2, _, _, _ := newTestManagerWithCfg(t, cfg2)
	cm2.autoPopulateSnapshotURL(context.Background(), "nop-cam")
	require.Empty(t, cm2.GetCameraConfig("nop-cam").SnapshotURL)
}

func TestCodecInfoH265AndBareONVIF(t *testing.T) {
	t.Parallel()
	cfg := testConfig()
	cfg.Cameras = []config.CameraConfig{}
	cm, store, _, _ := newTestManagerWithCfg(t, cfg)

	cm.SetTestRecorder("h265-cam", recorder.NewH265Recorder(
		recorder.H265Config{CameraID: "h265-cam"}, store))
	cm.SetTestRecorder("onvif-bare", recorder.NewONVIFRecorder(
		recorder.ONVIFConfig{CameraID: "onvif-bare"}, nil, nil))

	// H.265 recorder: not flagged h264.
	require.Equal(t, "h265", cm.GetSourceCodec("h265-cam"))
	require.False(t, cm.GetCodecInfo("h265-cam").IsH264)

	// Bare ONVIF recorder (no delegate): empty source codec, generic fallback
	// codec info.
	require.Empty(t, cm.GetSourceCodec("onvif-bare"))
	require.True(t, cm.GetCodecInfo("onvif-bare").IsH264, "generic fallback defaults IsH264=true")

	require.Equal(t, model.CodecInfo{}, cm.GetCodecInfo("ghost"))
}

func TestEnqueueTranscodeDisabledConfig(t *testing.T) {
	t.Parallel()
	cfg := testConfig()
	cfg.Cameras = []config.CameraConfig{
		{
			ID: "t-cam", Protocol: "srt", Encoding: "h264",
			Transcoding: &config.CameraTranscodingConfig{Enabled: false},
		},
	}
	cm, _, _, _ := newTestManagerWithCfg(t, cfg)
	// Disabled per-camera transcoding: no-op guard, no panic.
	cm.EnqueueTranscode("t-cam", "rec-1", "/tmp/x.mp4", "h264")
	cm.EnqueueTranscode("ghost", "rec-1", "/tmp/x.mp4", "h264")
}

func TestArchiveCameraDirect(t *testing.T) {
	t.Parallel()
	cfg := testConfig()
	cfg.Cameras = []config.CameraConfig{
		{ID: "arch-cam", Name: "A", Protocol: "srt", Encoding: "h264"},
	}
	cm, _, db, _ := newTestManagerWithCfg(t, cfg)
	ctx := context.Background()
	require.NoError(t, db.UpsertCamera(ctx, "arch-cam", "A", "srt", "h264", "", "", "", "", "", "", ""))

	require.NoError(t, cm.ArchiveCamera(ctx, "arch-cam"))
	require.Nil(t, cm.GetCameraConfig("arch-cam"), "archived camera leaves the YAML config")
	row, err := db.GetCamera(ctx, "arch-cam")
	require.NoError(t, err)
	require.NotNil(t, row)
	require.True(t, row.Archived)

	// Unknown camera → error (not found).
	require.Error(t, cm.ArchiveCamera(ctx, "ghost"))
}
