package recorder

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/merge"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
	"github.com/stretchr/testify/require"
)

// newGBRecorder builds a GB28181Recorder backed by a temp-dir storage
// manager (real segment files, no /tmp pollution), wired the way the camera
// manager wires it.
func newGBRecorder(t *testing.T, cameraID, encoding string, segDur time.Duration) (*GB28181Recorder, *storage.Manager) {
	t.Helper()
	store, err := storage.NewManager(t.TempDir())
	require.NoError(t, err)
	rec := NewGB28181Recorder(GB28181Config{
		CameraID:      cameraID,
		Encoding:      encoding,
		SegmentDur:    segDur,
		Store:         store,
		RecordEnabled: true,
	}, nil)
	rec.Hub = model.NewStreamHub()
	rec.Hub.SetCameraID(cameraID)
	require.NoError(t, rec.Start(context.Background()))
	rec.OnInvite()
	return rec, store
}

// gbFiles returns all segment files under the camera's recording dir
// (storage lays segments out in per-date subdirectories).
func gbFiles(t *testing.T, store *storage.Manager, cameraID string) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(store.RootDir(), cameraID, "*", "*", "*", "*.mp4"))
	require.NoError(t, err)
	return matches
}

// TestGB28181Recorder_H264Segment tests H.264 segment creation.
func TestGB28181Recorder_H264Segment(t *testing.T) {
	rec, store := newGBRecorder(t, "test-cam", "h264", 10*time.Minute)
	defer func() { _ = rec.Stop() }()

	sps := []byte{0x67, 0x42, 0x80, 0x0A}
	pps := []byte{0x68, 0xCE, 0x3C, 0x80}
	idr := []byte{0x65, 0x88, 0x80, 0x00}
	pframe := []byte{0x41, 0x9A, 0x24, 0x80}

	rec.WriteNALU([][]byte{sps, pps, idr}, 90000, true)
	for i := range 10 {
		rec.WriteNALU([][]byte{pframe}, 90000+int64(i)*3000, false)
	}

	require.NoError(t, rec.Stop())
	files := gbFiles(t, store, "test-cam")
	require.Len(t, files, 1)
}

// TestGB28181Recorder_H265Segment tests H.265 segment creation.
func TestGB28181Recorder_H265Segment(t *testing.T) {
	rec, store := newGBRecorder(t, "test-cam", "h265", 10*time.Minute)
	defer func() { _ = rec.Stop() }()

	vps := []byte{0x40, 0x01, 0x0C, 0x01, 0xFF, 0xFF, 0x01, 0x60}
	sps := []byte{0x42, 0x01, 0x01, 0x01, 0x60, 0x00, 0x00, 0x03}
	pps := []byte{0x44, 0x01, 0xC1, 0x73, 0xD9, 0x42, 0x80, 0x00}
	idr := []byte{0x26, 0x01, 0xAF, 0x0F}
	pframe := []byte{0x02, 0x01, 0xAF, 0x0F}

	rec.WriteNALU([][]byte{vps, sps, pps, idr}, 90000, true)
	for i := range 10 {
		rec.WriteNALU([][]byte{pframe}, 90000+int64(i)*3000, false)
	}

	require.NoError(t, rec.Stop())
	files := gbFiles(t, store, "test-cam")
	require.Len(t, files, 1)
}

// TestGB28181Recorder_NonIDRIgnored tests that segments are not created before the first IDR.
func TestGB28181Recorder_NonIDRIgnored(t *testing.T) {
	rec, store := newGBRecorder(t, "test-cam", "h264", 10*time.Minute)
	defer func() { _ = rec.Stop() }()

	pframe := []byte{0x41, 0x9A, 0x24, 0x80}
	rec.WriteNALU([][]byte{pframe}, 90000, false)

	require.NoError(t, rec.Stop())
	require.Len(t, gbFiles(t, store, "test-cam"), 0, "expected no MP4 files before IDR")
}

// TestGB28181Recorder_AutoDetectH264 tests codec auto-detection from H.264 stream.
func TestGB28181Recorder_AutoDetectH264(t *testing.T) {
	rec, _ := newGBRecorder(t, "test-cam", "", 10*time.Minute)
	defer func() { _ = rec.Stop() }()

	sps := []byte{0x67, 0x42, 0x80, 0x0A}
	pps := []byte{0x68, 0xCE, 0x3C, 0x80}
	idr := []byte{0x65, 0x88, 0x80, 0x00}

	rec.WriteNALU([][]byte{sps, pps, idr}, 90000, true)

	codec, spsOut, ppsOut, vpsOut := rec.CodecParams()
	require.Equal(t, model.FormatH264, codec)
	require.NotNil(t, spsOut)
	require.NotNil(t, ppsOut)
	require.Nil(t, vpsOut)
}

// TestGB28181Recorder_AutoDetectH265 tests codec auto-detection from H.265 stream.
func TestGB28181Recorder_AutoDetectH265(t *testing.T) {
	rec, _ := newGBRecorder(t, "test-cam", "", 10*time.Minute)
	defer func() { _ = rec.Stop() }()

	vps := []byte{0x40, 0x01, 0x0C, 0x01, 0xFF, 0xFF, 0x01, 0x60}
	sps := []byte{0x42, 0x01, 0x01, 0x01, 0x60, 0x00, 0x00, 0x03}
	pps := []byte{0x44, 0x01, 0xC1, 0x73, 0xD9, 0x42, 0x80, 0x00}
	idr := []byte{0x26, 0x01, 0xAF, 0x0F}

	rec.WriteNALU([][]byte{vps, sps, pps, idr}, 90000, true)

	codec, spsOut, ppsOut, vpsOut := rec.CodecParams()
	require.Equal(t, model.FormatH265, codec)
	require.NotNil(t, spsOut)
	require.NotNil(t, ppsOut)
	require.NotNil(t, vpsOut)
}

// TestGB28181Recorder_OnByeFlush tests that OnBye flushes the segment.
func TestGB28181Recorder_OnByeFlush(t *testing.T) {
	rec, store := newGBRecorder(t, "test-cam", "h264", 10*time.Minute)
	defer func() { _ = rec.Stop() }()

	sps := []byte{0x67, 0x42, 0x80, 0x0A}
	pps := []byte{0x68, 0xCE, 0x3C, 0x80}
	idr := []byte{0x65, 0x88, 0x80, 0x00}
	pframe := []byte{0x41, 0x9A, 0x24, 0x80}

	rec.WriteNALU([][]byte{sps, pps, idr}, 90000, true)
	rec.WriteNALU([][]byte{pframe}, 93000, false)

	rec.OnBye()

	files := gbFiles(t, store, "test-cam")
	require.Len(t, files, 1)

	segInfo, err := merge.ParseSegment(files[0])
	require.NoError(t, err)
	require.Greater(t, len(segInfo.Samples), 0)
}

// TestGB28181Recorder_NonBlockingBroadcast tests that hub broadcast does not block.
func TestGB28181Recorder_NonBlockingBroadcast(t *testing.T) {
	rec, _ := newGBRecorder(t, "test-cam", "h264", 10*time.Minute)
	defer func() { _ = rec.Stop() }()

	sps := []byte{0x67, 0x42, 0x80, 0x0A}
	pps := []byte{0x68, 0xCE, 0x3C, 0x80}
	idr := []byte{0x65, 0x88, 0x80, 0x00}
	pframe := []byte{0x41, 0x9A, 0x24, 0x80}

	rec.WriteNALU([][]byte{sps, pps, idr}, 90000, true)

	callCount := 0
	slowCallback := func(pts int64, au [][]byte) {
		callCount++
	}
	require.NoError(t, rec.Hub.Subscribe("slow", slowCallback))

	start := time.Now()
	for i := range 10 {
		rec.WriteNALU([][]byte{pframe}, 90000+int64(i)*3000, false)
	}
	elapsed := time.Since(start)

	require.Less(t, elapsed, 2*time.Second)
	// Hub delivery is asynchronous (consumer drain goroutine).
	require.Eventually(t, func() bool { return callCount > 0 }, 2*time.Second, 10*time.Millisecond)

	rec.Hub.Unsubscribe("slow")
}

// TestGB28181Recorder_InterfaceCompliance verifies all interfaces are satisfied.
func TestGB28181Recorder_InterfaceCompliance(t *testing.T) {
	rec, _ := newGBRecorder(t, "test-cam", "h264", 10*time.Minute)

	var (
		_ model.Recorder    = rec
		_ model.HLSProvider = rec
	)
}

// TestGB28181Recorder_GetHub tests GetHub method.
func TestGB28181Recorder_GetHub(t *testing.T) {
	hub := model.NewStreamHub()
	rec := NewGB28181Recorder(GB28181Config{CameraID: "test-cam", Encoding: "h264"}, hub)

	require.Equal(t, hub, rec.GetHub())
}

// TestGB28181Recorder_StatusTransitions tests status transitions through lifecycle.
func TestGB28181Recorder_StatusTransitions(t *testing.T) {
	ctx := context.Background()
	rec := NewGB28181Recorder(GB28181Config{CameraID: "test-cam", Encoding: "h264"}, nil)

	require.Equal(t, model.StatusStopped, rec.Status())

	err := rec.Start(ctx)
	require.NoError(t, err)
	require.Equal(t, model.StatusReconnecting, rec.Status())

	rec.OnInvite()
	require.Equal(t, model.StatusRecording, rec.Status())

	rec.OnBye()
	require.Equal(t, model.StatusReconnecting, rec.Status())

	err = rec.Stop()
	require.NoError(t, err)
	require.Equal(t, model.StatusStopped, rec.Status())
}

// TestGB28181Recorder_StoppedStaysStopped is a regression test: stale RTP
// arriving after Stop (session teardown racing the recorder stop) must not
// resurrect the recorder or write segments.
func TestGB28181Recorder_StoppedStaysStopped(t *testing.T) {
	rec, store := newGBRecorder(t, "test-cam", "h264", 10*time.Minute)

	sps := []byte{0x67, 0x42, 0x80, 0x0A}
	pps := []byte{0x68, 0xCE, 0x3C, 0x80}
	idr := []byte{0x65, 0x88, 0x80, 0x00}
	pframe := []byte{0x41, 0x9A, 0x24, 0x80}

	rec.WriteNALU([][]byte{sps, pps, idr}, 90000, true)
	require.NoError(t, rec.Stop())

	// Stale AU after stop: no segment, no status change.
	rec.WriteNALU([][]byte{pframe}, 93000, false)
	require.Equal(t, model.StatusStopped, rec.Status())
	files := gbFiles(t, store, "test-cam")
	require.Len(t, files, 1, "no additional segments after stop")
}

// TestGB28181Recorder_RecordDisabled tests that RecordEnabled=false keeps the
// camera live-only: hub fan-out happens but no segments are written.
func TestGB28181Recorder_RecordDisabled(t *testing.T) {
	store, err := storage.NewManager(t.TempDir())
	require.NoError(t, err)
	rec := NewGB28181Recorder(GB28181Config{
		CameraID:      "live-only",
		Encoding:      "h264",
		SegmentDur:    10 * time.Minute,
		Store:         store,
		RecordEnabled: false,
	}, nil)
	rec.Hub = model.NewStreamHub()
	rec.Hub.SetCameraID("live-only")
	require.NoError(t, rec.Start(context.Background()))
	rec.OnInvite()
	defer func() { _ = rec.Stop() }()

	received := make(chan int64, 4)
	require.NoError(t, rec.Hub.Subscribe("sub", func(pts int64, au [][]byte) {
		select {
		case received <- pts:
		default:
		}
	}))

	sps := []byte{0x67, 0x42, 0x80, 0x0A}
	pps := []byte{0x68, 0xCE, 0x3C, 0x80}
	idr := []byte{0x65, 0x88, 0x80, 0x00}
	rec.WriteNALU([][]byte{sps, pps, idr}, 90000, true)

	select {
	case <-received:
	case <-time.After(2 * time.Second):
		t.Fatal("hub fan-out did not happen for live-only camera")
	}

	require.NoError(t, rec.Stop())
	require.Len(t, gbFiles(t, store, "live-only"), 0, "live-only camera must not write segments")
}

// TestGB28181Recorder_SegmentRotation tests segment rotation on duration.
func TestGB28181Recorder_SegmentRotation(t *testing.T) {
	rec, store := newGBRecorder(t, "test-cam", "h264", 10*time.Minute)
	defer func() { _ = rec.Stop() }()

	sps := []byte{0x67, 0x42, 0x80, 0x0A}
	pps := []byte{0x68, 0xCE, 0x3C, 0x80}
	idr := []byte{0x65, 0x88, 0x80, 0x00}
	pframe := []byte{0x41, 0x9A, 0x24, 0x80}

	rec.WriteNALU([][]byte{sps, pps, idr}, 90000, true)

	rec.mu.Lock()
	rec.segStart = time.Now().Add(-11 * time.Minute)
	rec.mu.Unlock()

	rec.WriteNALU([][]byte{pframe}, 93000, false)

	rec.WriteNALU([][]byte{sps, pps, idr}, 96000, true)

	require.NoError(t, rec.Stop())
	files := gbFiles(t, store, "test-cam")
	require.GreaterOrEqual(t, len(files), 1)
}

// TestGB28181Recorder_SegmentDurHonored verifies the configured segment
// duration drives rotation (not the old hardcoded 10 minutes).
func TestGB28181Recorder_SegmentDurHonored(t *testing.T) {
	rec, store := newGBRecorder(t, "test-cam", "h264", 50*time.Millisecond)
	defer func() { _ = rec.Stop() }()

	sps := []byte{0x67, 0x42, 0x80, 0x0A}
	pps := []byte{0x68, 0xCE, 0x3C, 0x80}
	idr := []byte{0x65, 0x88, 0x80, 0x00}

	rec.WriteNALU([][]byte{sps, pps, idr}, 90000, true)
	for i := range 5 {
		time.Sleep(30 * time.Millisecond)
		rec.WriteNALU([][]byte{sps, pps, idr}, 93000+int64(i)*3000, true)
	}

	require.NoError(t, rec.Stop())
	files := gbFiles(t, store, "test-cam")
	require.GreaterOrEqual(t, len(files), 2, "50ms SegmentDur should rotate multiple times")
}

// TestDetectCodec_SliceAmbiguity is a real-world regression: an H.264 P-slice
// with nal_ref_idc=2 (0x41) shifted into the H.265 VPS type slot (32) and
// mislabeled the stream h265 — the recorder then waited forever for
// VPS/SPS/PPS and never opened a segment (observed with a real MiBee camera).
func TestDetectCodec_SliceAmbiguity(t *testing.T) {
	pSlice := []byte{0x41, 0x9A, 0x24} // H.264 P, nri=2
	sei := []byte{0x06, 0x05, 0x01}
	if got := detectCodec([][]byte{sei, pSlice}, ""); got != "" {
		t.Fatalf("slice-only AU must defer (\"\") — got %q", got)
	}
	if got := detectCodec([][]byte{sei, pSlice}, "h264"); got != "h264" {
		t.Fatalf("encoding hint must be used for slice-only AU — got %q", got)
	}
	sps := []byte{0x67, 0x42, 0x00}
	if got := detectCodec([][]byte{pSlice, sps}, ""); got != "h264" {
		t.Fatalf("H.264 SPS must win — got %q", got)
	}
	vps := []byte{0x40, 0x01, 0x0C}
	if got := detectCodec([][]byte{pSlice, vps}, ""); got != "h265" {
		t.Fatalf("H.265 VPS must win — got %q", got)
	}
}
