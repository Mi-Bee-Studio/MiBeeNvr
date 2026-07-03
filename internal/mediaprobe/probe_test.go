package mediaprobe

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/muxer"
	"github.com/stretchr/testify/require"
)

// createH264MP4 builds a tiny H.264 MP4 with N frames for testing.
// Uses the known-good 640x128 SPS so resolution parsing succeeds.
func createH264MP4(t *testing.T, dir string, frames int) string {
	t.Helper()
	path := filepath.Join(dir, "test_h264.mp4")
	// Known-good H.264 SPS: Baseline profile, 640x128 (from internal/merge tests).
	sps := []byte{0x67, 0x42, 0xc0, 0x1e, 0xd9, 0x00, 0xa0, 0x47, 0xfe, 0xc8}
	pps := []byte{0x68, 0xce, 0x38, 0x80}

	m := muxer.NewMP4Muxer(path)
	trackID, err := m.AddH264Track(sps, pps)
	require.NoError(t, err)

	idrNAL := []byte{0x65, 0x88, 0x80, 0x40}
	require.NoError(t, m.WriteSample(trackID, idrNAL, 0, 33*time.Millisecond))
	for i := 1; i < frames; i++ {
		pNAL := []byte{0x41, 0x10, 0x00, 0x0c}
		require.NoError(t, m.WriteSample(trackID, pNAL, time.Duration(i)*33*time.Millisecond, 33*time.Millisecond))
	}
	require.NoError(t, m.Close())
	return path
}

// createH265MP4 builds a tiny H.265 MP4 with N frames for testing.
// Uses the known-good 1920x1080 HEVC SPS so resolution parsing succeeds.
func createH265MP4(t *testing.T, dir string, frames int) string {
	t.Helper()
	path := filepath.Join(dir, "test_h265.mp4")
	vps := []byte{0x40, 0x01, 0x0c, 0x01, 0xff, 0xff, 0x01, 0x60, 0x00, 0x00, 0x00, 0x00, 0x00, 0x90, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x5d, 0xac, 0x59}
	// Known-good HEVC SPS: Main profile, 1920x1080 (from internal/merge tests).
	sps := []byte{
		0x42, 0x01, 0x01, 0x01, 0x60, 0x00, 0x00, 0x03, 0x00, 0x90,
		0x00, 0x00, 0x03, 0x00, 0x00, 0x03, 0x00, 0x78, 0xa0, 0x03,
		0xc0, 0x80, 0x10, 0xe5, 0x96, 0x66, 0x69, 0x24, 0xca, 0xe0,
		0x10, 0x00, 0x00, 0x03, 0x00, 0x10, 0x00, 0x00, 0x03, 0x01,
		0xe0, 0x80,
	}
	pps := []byte{0x44, 0x01, 0xc1, 0x73, 0xd1, 0x89}

	m := muxer.NewMP4Muxer(path)
	trackID, err := m.AddH265Track(vps, sps, pps)
	require.NoError(t, err)

	idrNAL := []byte{0x27, 0x01, 0xaf, 0x15, 0x6a}
	require.NoError(t, m.WriteSample(trackID, idrNAL, 0, 33*time.Millisecond))
	for i := 1; i < frames; i++ {
		pNAL := []byte{0x03, 0x20, 0x10, 0x00}
		require.NoError(t, m.WriteSample(trackID, pNAL, time.Duration(i)*33*time.Millisecond, 33*time.Millisecond))
	}
	require.NoError(t, m.Close())
	return path
}

func TestProbeMP4_H264(t *testing.T) {
	dir := t.TempDir()
	path := createH264MP4(t, dir, 3)

	info, err := ProbeMP4(path)
	require.NoError(t, err)
	require.Equal(t, "h264", info.CodecName)
	require.Equal(t, "h264", info.Codec)
	require.Equal(t, 3, info.FrameCount, "frame count should match samples written")
	require.Equal(t, 640, info.Width)
	require.Equal(t, 128, info.Height)
	// 3 frames × 33ms = 99ms; allow small float tolerance.
	require.InDelta(t, 0.099, info.Duration, 0.01)
}

func TestProbeMP4_H265(t *testing.T) {
	dir := t.TempDir()
	path := createH265MP4(t, dir, 2)

	info, err := ProbeMP4(path)
	require.NoError(t, err)
	// ffprobe-compatible name for H.265 is "hevc".
	require.Equal(t, "hevc", info.CodecName)
	require.Equal(t, "h265", info.Codec)
	require.Equal(t, 2, info.FrameCount)
	require.Equal(t, 1920, info.Width)
	require.Equal(t, 1080, info.Height)
}

func TestProbeDuration(t *testing.T) {
	dir := t.TempDir()
	path := createH264MP4(t, dir, 5)

	dur, err := ProbeDuration(path)
	require.NoError(t, err)
	// 5 frames × 33ms = 165ms.
	require.InDelta(t, 0.165, dur, 0.01)
}

func TestProbeMP4_NonExistent(t *testing.T) {
	_, err := ProbeMP4("/nonexistent/path/file.mp4")
	require.Error(t, err)
}

func TestProbeMP4_InvalidFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "notmp4.mp4")
	require.NoError(t, os.WriteFile(path, []byte("not a real mp4"), 0o644))
	_, err := ProbeMP4(path)
	require.Error(t, err)
}

func TestIsLikelyMP4(t *testing.T) {
	require.True(t, IsLikelyMP4("/data/recordings/cam/rec.mp4"))
	require.True(t, IsLikelyMP4("/data/x.MP4"))
	require.True(t, IsLikelyMP4("/data/seg.transcoded.mp4"))
	require.True(t, IsLikelyMP4("/data/x.m4v"))
	require.False(t, IsLikelyMP4("/data/frame.jpg"))
	require.False(t, IsLikelyMP4("/data/clip.avi"))
}
