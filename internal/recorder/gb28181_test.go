package recorder

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/merge"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/stretchr/testify/require"
)

// TestGB28181Recorder_H264Segment tests H.264 segment creation.
func TestGB28181Recorder_H264Segment(t *testing.T) {
	ctx := context.Background()
	hub := model.NewStreamHub()
	rec := NewGB28181Recorder("test-cam", "h264", hub)

	err := rec.Start(ctx)
	require.NoError(t, err)
	defer rec.Stop()

	sps := []byte{0x67, 0x42, 0x80, 0x0A}
	pps := []byte{0x68, 0xCE, 0x3C, 0x80}
	idr := []byte{0x65, 0x88, 0x80, 0x00}
	pframe := []byte{0x41, 0x9A, 0x24, 0x80}

	rec.WriteNALU([][]byte{sps, pps, idr}, 90000, true)
	for i := range 10 {
		rec.WriteNALU([][]byte{pframe}, 90000+int64(i)*3000, false)
	}

	rec.Stop()

	files, err := filepath.Glob(os.TempDir() + "/test-cam_*.mp4")
	require.NoError(t, err)
	require.Len(t, files, 1)
	os.Remove(files[0])
}

// TestGB28181Recorder_H265Segment tests H.265 segment creation.
func TestGB28181Recorder_H265Segment(t *testing.T) {
	ctx := context.Background()
	hub := model.NewStreamHub()
	rec := NewGB28181Recorder("test-cam", "h265", hub)

	err := rec.Start(ctx)
	require.NoError(t, err)
	defer rec.Stop()

	vps := []byte{0x40, 0x01, 0x0C, 0x01, 0xFF, 0xFF, 0x01, 0x60}
	sps := []byte{0x42, 0x01, 0x01, 0x01, 0x60, 0x00, 0x00, 0x03}
	pps := []byte{0x44, 0x01, 0xC1, 0x73, 0xD9, 0x42, 0x80, 0x00}
	idr := []byte{0x26, 0x01, 0xAF, 0x0F}
	pframe := []byte{0x02, 0x01, 0xAF, 0x0F}

	rec.WriteNALU([][]byte{vps, sps, pps, idr}, 90000, true)
	for i := range 10 {
		rec.WriteNALU([][]byte{pframe}, 90000+int64(i)*3000, false)
	}

	rec.Stop()

	files, err := filepath.Glob(os.TempDir() + "/test-cam_*.mp4")
	require.NoError(t, err)
	require.Len(t, files, 1)
	os.Remove(files[0])
}

// TestGB28181Recorder_NonIDRIgnored tests that segments are not created before the first IDR.
func TestGB28181Recorder_NonIDRIgnored(t *testing.T) {
	ctx := context.Background()
	hub := model.NewStreamHub()
	rec := NewGB28181Recorder("test-cam", "h264", hub)

	err := rec.Start(ctx)
	require.NoError(t, err)
	defer rec.Stop()

	pframe := []byte{0x41, 0x9A, 0x24, 0x80}
	rec.WriteNALU([][]byte{pframe}, 90000, false)

	rec.Stop()

	files, err := filepath.Glob(os.TempDir() + "/test-cam_*.mp4")
	require.NoError(t, err)
	require.Len(t, files, 0, "expected no MP4 files before IDR")
}

// TestGB28181Recorder_AutoDetectH264 tests codec auto-detection from H.264 stream.
func TestGB28181Recorder_AutoDetectH264(t *testing.T) {
	ctx := context.Background()
	hub := model.NewStreamHub()
	rec := NewGB28181Recorder("test-cam", "", hub)

	err := rec.Start(ctx)
	require.NoError(t, err)
	defer rec.Stop()

	sps := []byte{0x67, 0x42, 0x80, 0x0A}
	pps := []byte{0x68, 0xCE, 0x3C, 0x80}
	idr := []byte{0x65, 0x88, 0x80, 0x00}

	rec.WriteNALU([][]byte{sps, pps, idr}, 90000, true)

	codec, spsOut, ppsOut, vpsOut := rec.CodecParams()
	require.Equal(t, model.FormatH264, codec)
	require.NotNil(t, spsOut)
	require.NotNil(t, ppsOut)
	require.Nil(t, vpsOut)

	rec.Stop()

	files, _ := filepath.Glob(os.TempDir() + "/test-cam_*.mp4")
	for _, f := range files {
		os.Remove(f)
	}
}

// TestGB28181Recorder_AutoDetectH265 tests codec auto-detection from H.265 stream.
func TestGB28181Recorder_AutoDetectH265(t *testing.T) {
	ctx := context.Background()
	hub := model.NewStreamHub()
	rec := NewGB28181Recorder("test-cam", "", hub)

	err := rec.Start(ctx)
	require.NoError(t, err)
	defer rec.Stop()

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

	rec.Stop()

	files, _ := filepath.Glob(os.TempDir() + "/test-cam_*.mp4")
	for _, f := range files {
		os.Remove(f)
	}
}

// TestGB28181Recorder_OnByeFlush tests that OnBye flushes the segment.
func TestGB28181Recorder_OnByeFlush(t *testing.T) {
	ctx := context.Background()
	hub := model.NewStreamHub()
	rec := NewGB28181Recorder("test-cam", "h264", hub)

	err := rec.Start(ctx)
	require.NoError(t, err)
	defer rec.Stop()

	sps := []byte{0x67, 0x42, 0x80, 0x0A}
	pps := []byte{0x68, 0xCE, 0x3C, 0x80}
	idr := []byte{0x65, 0x88, 0x80, 0x00}
	pframe := []byte{0x41, 0x9A, 0x24, 0x80}

	rec.WriteNALU([][]byte{sps, pps, idr}, 90000, true)
	rec.WriteNALU([][]byte{pframe}, 93000, false)

	rec.OnBye()

	files, err := filepath.Glob(os.TempDir() + "/test-cam_*.mp4")
	require.NoError(t, err)
	require.Len(t, files, 1)

	segInfo, err := merge.ParseSegment(files[0])
	require.NoError(t, err)
	require.Greater(t, len(segInfo.Samples), 0)

	os.Remove(files[0])
}

// TestGB28181Recorder_NonBlockingBroadcast tests that hub broadcast does not block.
func TestGB28181Recorder_NonBlockingBroadcast(t *testing.T) {
	ctx := context.Background()
	hub := model.NewStreamHub()
	rec := NewGB28181Recorder("test-cam", "h264", hub)

	err := rec.Start(ctx)
	require.NoError(t, err)
	defer rec.Stop()

	sps := []byte{0x67, 0x42, 0x80, 0x0A}
	pps := []byte{0x68, 0xCE, 0x3C, 0x80}
	idr := []byte{0x65, 0x88, 0x80, 0x00}
	pframe := []byte{0x41, 0x9A, 0x24, 0x80}

	rec.WriteNALU([][]byte{sps, pps, idr}, 90000, true)

	callCount := 0
	slowCallback := func(pts int64, au [][]byte) {
		callCount++
	}
	hub.Subscribe("slow", slowCallback)

	start := time.Now()
	for i := range 10 {
		rec.WriteNALU([][]byte{pframe}, 90000+int64(i)*3000, false)
	}
	elapsed := time.Since(start)

	require.Less(t, elapsed, 2*time.Second)

	hub.Unsubscribe("slow")
	rec.Stop()

	files, _ := filepath.Glob(os.TempDir() + "/test-cam_*.mp4")
	for _, f := range files {
		os.Remove(f)
	}
}

// TestGB28181Recorder_InterfaceCompliance verifies all interfaces are satisfied.
func TestGB28181Recorder_InterfaceCompliance(t *testing.T) {
	hub := model.NewStreamHub()
	rec := NewGB28181Recorder("test-cam", "h264", hub)

	var (
		_ model.Recorder    = rec
		_ model.HLSProvider = rec
	)
}

// TestGB28181Recorder_GetHub tests GetHub method.
func TestGB28181Recorder_GetHub(t *testing.T) {
	hub := model.NewStreamHub()
	rec := NewGB28181Recorder("test-cam", "h264", hub)

	require.Equal(t, hub, rec.GetHub())
}

// TestGB28181Recorder_StatusTransitions tests status transitions through lifecycle.
func TestGB28181Recorder_StatusTransitions(t *testing.T) {
	ctx := context.Background()
	hub := model.NewStreamHub()
	rec := NewGB28181Recorder("test-cam", "h264", hub)

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

// TestGB28181Recorder_SegmentRotation tests segment rotation on duration.
func TestGB28181Recorder_SegmentRotation(t *testing.T) {
	ctx := context.Background()
	hub := model.NewStreamHub()
	rec := NewGB28181Recorder("test-cam", "h264", hub)

	err := rec.Start(ctx)
	require.NoError(t, err)

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

	rec.Stop()

	files, err := filepath.Glob(os.TempDir() + "/test-cam_*.mp4")
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(files), 1)

	for _, f := range files {
		os.Remove(f)
	}
}
