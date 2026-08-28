package camera

// Long-tail coverage for camera package accessors (#583): stream-key
// resolution, transcoding wiring, codec accessors (constructed recorders,
// never started), GB28181 sub-channel/recording-wanted/device-meta series,
// and timelapse poller plumbing. All hermetic.

import (
	"context"
	"testing"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/recorder"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/timelapse"
	"github.com/stretchr/testify/require"
)

func TestStreamKeyResolvers(t *testing.T) {
	t.Parallel()
	cfg := testConfig()
	cfg.Cameras = []config.CameraConfig{
		{ID: "push-rtmp", Protocol: "rtmp", StreamKey: "key-rtmp"},
		{ID: "push-whip", Protocol: "whip", StreamKey: "key-whip"},
		{ID: "push-srt", Protocol: "srt", SRTPassphrase: "pass-srt", SRTStreamID: "sid-1"},
		{ID: "pull-cam", Protocol: "rtsp", URL: "rtsp://127.0.0.1:8554/main"},
	}
	cm, _, _, _ := newTestManagerWithCfg(t, cfg)
	cm.cfg.RTMP.StreamKeys = map[string]string{"legacy-cam": "key-legacy"}

	// Per-camera RTMP key wins; legacy map is the fallback; misses fail.
	id, ok := cm.ResolveStreamKey("key-rtmp")
	require.True(t, ok)
	require.Equal(t, "push-rtmp", id)
	id, ok = cm.ResolveStreamKey("key-legacy")
	require.True(t, ok)
	require.Equal(t, "legacy-cam", id)
	_, ok = cm.ResolveStreamKey("nope")
	require.False(t, ok)

	// Key map copy.
	require.Equal(t, map[string]string{"push-rtmp": "key-rtmp"}, cm.RTMPKeyMap())

	// WHIP resolution is protocol-scoped.
	id, ok = cm.ResolveWHIPKey("key-whip")
	require.True(t, ok)
	require.Equal(t, "push-whip", id)
	_, ok = cm.ResolveWHIPKey("key-rtmp")
	require.False(t, ok, "RTMP keys must not authenticate WHIP publishers")

	// SRT push parameters.
	srts := cm.SRTStreamConfigs()
	require.Len(t, srts, 1)
	require.Equal(t, "push-srt", srts[0].CameraID)
	require.Equal(t, "pass-srt", srts[0].Passphrase)
	require.Equal(t, "sid-1", srts[0].StreamID)

	// Stream URL: rtsp cameras resolve from config; unknown cameras empty.
	require.Equal(t, "rtsp://127.0.0.1:8554/main", cm.GetStreamURL("pull-cam"))
	require.Empty(t, cm.GetStreamURL("ghost"))
}

func TestEnqueueTranscodeNilManager(t *testing.T) {
	t.Parallel()
	cm, _, _, _ := newTestManager(t)
	cm.SetTranscodeManager(nil)
	// Must be a silent no-op, not a panic.
	cm.EnqueueTranscode("cam-h264", "rec-1", "/x/a.mp4", "h265")
}

func TestCodecAccessors(t *testing.T) {
	t.Parallel()
	cfg := testConfig()
	cfg.Cameras = []config.CameraConfig{
		{ID: "h264-cam", Protocol: "rtsp", Encoding: "h264"},
		{ID: "mjpeg-cam", Protocol: "rtsp", Encoding: "mjpeg"},
		{ID: "jpeg-cam", Protocol: "http", Encoding: "jpeg"},
		{ID: "ingest-cam", Protocol: "srt", Encoding: "h264"},
	}
	cm, store, _, _ := newTestManagerWithCfg(t, cfg)

	h264 := recorder.NewH264Recorder(recorder.H264Config{CameraID: "h264-cam"}, store)
	mjpeg := recorder.NewMJPEGRecorder(recorder.MJPEGConfig{CameraID: "mjpeg-cam"}, store)
	jpeg := recorder.NewHTTPJPEGRecorder(recorder.HTTPJPEGConfig{CameraID: "jpeg-cam"}, store)
	ingest := recorder.NewIngestRecorder(recorder.IngestConfig{CameraID: "ingest-cam"})
	cm.SetTestRecorder("h264-cam", h264)
	cm.SetTestRecorder("mjpeg-cam", mjpeg)
	cm.SetTestRecorder("jpeg-cam", jpeg)
	cm.SetTestRecorder("ingest-cam", ingest)

	// Source codec per recorder type.
	require.Equal(t, "h264", cm.GetSourceCodec("h264-cam"))
	require.Equal(t, "mjpeg", cm.GetSourceCodec("mjpeg-cam"))
	require.Equal(t, "jpeg", cm.GetSourceCodec("jpeg-cam"))
	require.Empty(t, cm.GetSourceCodec("ghost"))

	// SPS accessors: h264-family recorders report h264-ness even before keyframes.
	_, _, isH264 := cm.GetSPS("h264-cam")
	require.True(t, isH264)
	_, _, isH264 = cm.GetSPS("ingest-cam")
	require.True(t, isH264)
	_, _, isH264 = cm.GetSPS("mjpeg-cam")
	require.False(t, isH264)
	_, _, isH264 = cm.GetSPS("ghost")
	require.False(t, isH264)

	// CodecInfo: h264 recorder reports h264-ness (params empty until stream).
	ci := cm.GetCodecInfo("h264-cam")
	require.True(t, ci.IsH264)
	require.Equal(t, model.CodecInfo{}, cm.GetCodecInfo("ghost"))
}

func TestGB28181SubChannelAndRecordingWanted(t *testing.T) {
	t.Parallel()
	cfg := testConfig()
	cfg.Cameras = []config.CameraConfig{
		{
			ID:       "gb-main",
			Protocol: "gb28181",
			GB28181:  config.GB28181ChannelConfig{DeviceID: "34020000002000000001", ChannelID: "34020000001320000001"},
		},
	}
	cm, _, _, _ := newTestManagerWithCfg(t, cfg)

	// No sub-channel yet: empty + recording wanted by default.
	require.Empty(t, cm.GB28181SubChannelID("34020000002000000001", "34020000001320000001"))
	require.True(t, cm.GB28181RecordingWanted("34020000002000000001", "34020000001320000001"))
	require.False(t, cm.GB28181RecordingWanted("ghost", "ghost"), "unbound channel → not wanted")

	// Persist a sub-channel (config file is wired by newTestManagerWithCfg).
	require.NoError(t, cm.SetGB28181SubChannel("34020000002000000001", "34020000001320000001", "34020000001320000002"))
	require.Equal(t, "34020000001320000002", cm.GB28181SubChannelID("34020000002000000001", "34020000001320000001"))
	require.Error(t, cm.SetGB28181SubChannel("ghost", "ghost", "x"))

	// Recording explicitly disabled → not wanted.
	noRec := false
	cfg2 := testConfig()
	cfg2.Cameras = []config.CameraConfig{
		{
			ID:               "gb-main",
			Protocol:         "gb28181",
			RecordingEnabled: &noRec,
			GB28181:          config.GB28181ChannelConfig{DeviceID: "34020000002000000001", ChannelID: "34020000001320000001"},
		},
	}
	cm2, _, _, _ := newTestManagerWithCfg(t, cfg2)
	require.False(t, cm2.GB28181RecordingWanted("34020000002000000001", "34020000001320000001"))

	// Playback audio writer is intentionally nil (interface symmetry).
	require.Nil(t, cm.GB28181PlaybackAudioWriter("gb-main"))
}

func TestUpdateGB28181DeviceMeta(t *testing.T) {
	t.Parallel()
	cfg := testConfig()
	cfg.Cameras = []config.CameraConfig{
		{
			ID:       "gb-a",
			Protocol: "gb28181",
			GB28181:  config.GB28181ChannelConfig{DeviceID: "dev-1", ChannelID: "ch-1"},
		},
		{ID: "gb-b", Protocol: "gb28181", GB28181: config.GB28181ChannelConfig{DeviceID: "dev-1", ChannelID: "ch-2"}},
	}
	cm, _, db, _ := newTestManagerWithCfg(t, cfg)

	// Seed DB rows: gb-a carries a user-entered brand, gb-b is empty.
	ctx := context.Background()
	require.NoError(t, db.UpsertCamera(ctx, "gb-a", "A", "gb28181", "", "", "", "", "", "", "", ""))
	require.NoError(t, db.UpsertCamera(ctx, "gb-b", "B", "gb28181", "", "", "", "", "", "", "", ""))
	require.NoError(t, db.UpdateCameraMetadata(ctx, "gb-a", "", "", "KeptBrand", "", "", 0))

	require.NoError(t, cm.UpdateGB28181DeviceMeta("dev-1", "Hikvision", "DS-2CD"))

	rowA, err := db.GetCamera(ctx, "gb-a")
	require.NoError(t, err)
	require.Equal(t, "KeptBrand", rowA.Brand, "existing brand must not be overwritten")
	require.Equal(t, "DS-2CD", rowA.Model)

	rowB, err := db.GetCamera(ctx, "gb-b")
	require.NoError(t, err)
	require.Equal(t, "Hikvision", rowB.Brand)
	require.Equal(t, "DS-2CD", rowB.Model)

	// Unknown device is a no-op success.
	require.NoError(t, cm.UpdateGB28181DeviceMeta("ghost", "X", "Y"))
}

func TestTimelapsePollerPlumbing(t *testing.T) {
	t.Parallel()
	cm, store, _, _ := newTestManager(t)

	// Stopping an unknown poller is a no-op.
	cm.stopTimelapseFramePoller("ghost")

	// resolveLatestFramer unwraps delegates and finds HTTPJPEG recorders.
	jpeg := recorder.NewHTTPJPEGRecorder(recorder.HTTPJPEGConfig{CameraID: "c"}, store)
	framer := resolveLatestFramer(jpeg)
	require.NotNil(t, framer, "HTTPJPEGRecorder provides LatestFrame")
	require.Nil(t, framer(), "no frame captured yet")

	// Non-framer recorders resolve to nil.
	h264 := recorder.NewH264Recorder(recorder.H264Config{CameraID: "c"}, store)
	require.Nil(t, resolveLatestFramer(h264))
	require.Nil(t, resolveLatestFramer(nil))

	// setFramePoller/stopTimelapseFramePoller round-trip with a real capturer
	// (never started — Stop on a stopped capturer is a no-op).
	capturer := timelapse.NewSnapshotCapturer(timelapse.SnapshotCapturerConfig{
		CameraID: "cam-h264",
		Interval: 0, // never ticks
		Store:    store,
	}, store)
	cm.setFramePoller("cam-h264", capturer)
	cm.stopTimelapseFramePoller("cam-h264")
}

// delegatingStubRecorder exercises delegate unwrapping in resolveLatestFramer.
type delegatingStubRecorder struct {
	inner model.Recorder
}

func (d *delegatingStubRecorder) Start(ctx context.Context) error { return nil }
func (d *delegatingStubRecorder) Stop() error                     { return nil }
func (d *delegatingStubRecorder) Status() model.RecorderStatus    { return "" }
func (d *delegatingStubRecorder) Delegate() model.Recorder        { return d.inner }

func TestResolveLatestFramerThroughDelegate(t *testing.T) {
	t.Parallel()
	_, store, _, _ := newTestManager(t)
	inner := recorder.NewHTTPJPEGRecorder(recorder.HTTPJPEGConfig{CameraID: "c"}, store)
	outer := &delegatingStubRecorder{inner: inner}
	require.NotNil(t, resolveLatestFramer(outer))
}
