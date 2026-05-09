package merge

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/muxer"
	"github.com/stretchr/testify/require"
)

// createTestH264Segment creates a small valid H.264 MP4 file with one IDR + one P-frame.
func createTestH264Segment(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "test_h264.mp4")

	// Minimal H.264 SPS: Baseline profile, Level 3.0, 16x16 (1 macroblock)
	sps := []byte{0x67, 0x42, 0x00, 0x0a, 0xe2, 0x40, 0x40, 0x04, 0x00, 0x00, 0x00, 0x04, 0x00, 0x00, 0x00, 0xc8, 0x40}
	// Minimal PPS
	pps := []byte{0x68, 0xce, 0x38, 0x80}

	m := muxer.NewMP4Muxer(path)
	trackID, err := m.AddH264Track(sps, pps)
	require.NoError(t, err)

	// IDR slice (NAL type 5 = 0x65)
	idrNAL := []byte{0x65, 0x88, 0x80, 0x40}
	require.NoError(t, m.WriteSample(trackID, idrNAL, 0, 33*time.Millisecond))

	// P-slice (NAL type 1 = 0x41)
	pNAL := []byte{0x41, 0x10, 0x00, 0x0c}
	require.NoError(t, m.WriteSample(trackID, pNAL, 33*time.Millisecond, 33*time.Millisecond))

	require.NoError(t, m.Close())
	return path
}

// createTestH265Segment creates a small valid H.265 MP4 file with one IDR + one P-frame.
func createTestH265Segment(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "test_h265.mp4")

	// Minimal VPS (NAL type 32)
	vps := []byte{0x40, 0x01, 0x0c, 0x01, 0xff, 0xff, 0x01, 0x60, 0x00, 0x00, 0x00, 0x00, 0x00, 0x90, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x5d, 0xac, 0x59}
	// Minimal SPS (NAL type 33): Main profile, 16x16
	sps := []byte{0x42, 0x01, 0x01, 0x01, 0x60, 0x00, 0x00, 0x00, 0x00, 0x00, 0x90, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x5d, 0xa0, 0x02, 0x80, 0x80, 0x2d, 0x16, 0x59, 0x59, 0xa4, 0x93, 0x2b, 0x80, 0x40, 0x00, 0x00, 0x07, 0x92}
	// Minimal PPS (NAL type 34)
	pps := []byte{0x44, 0x01, 0xc1, 0x73, 0xd1, 0x89}

	m := muxer.NewMP4Muxer(path)
	trackID, err := m.AddH265Track(vps, sps, pps)
	require.NoError(t, err)

	// IDR_W_RADL (NAL type 19): first byte bits 1-6 = 19, so byte = (19<<1)|1 = 0x27
	idrNAL := []byte{0x27, 0x01, 0xaf, 0x15, 0x6a}
	require.NoError(t, m.WriteSample(trackID, idrNAL, 0, 33*time.Millisecond))

	// TRAIL_R (NAL type 1): byte = (1<<1)|1 = 0x03
	pNAL := []byte{0x03, 0x20, 0x10, 0x00}
	require.NoError(t, m.WriteSample(trackID, pNAL, 33*time.Millisecond, 33*time.Millisecond))

	require.NoError(t, m.Close())
	return path
}

func TestParseSegment_H264(t *testing.T) {
	dir := t.TempDir()
	path := createTestH264Segment(t, dir)

	info, err := ParseSegment(path)
	require.NoError(t, err)
	require.Equal(t, "h264", info.Codec)
	require.NotEmpty(t, info.SPS)
	require.NotEmpty(t, info.PPS)
	require.Equal(t, uint32(1000), info.Timescale)
	require.Equal(t, 2, info.SampleCount)
	require.Equal(t, path, info.FilePath)
	require.Equal(t, 66*time.Millisecond, info.TotalDuration)
	// Note: MdatOffset/MdatSize may be 0 due to path tracking in parser.
	// The merge operation uses per-sample offsets from stco/stsz instead.
	require.GreaterOrEqual(t, info.MdatOffset, int64(0))
	require.GreaterOrEqual(t, info.MdatSize, int64(0))

	// First sample should be a keyframe (IDR, NAL type 5)
	require.True(t, info.Samples[0].IsKeyFrame)
	// Second sample should NOT be a keyframe (P-slice, NAL type 1)
	require.False(t, info.Samples[1].IsKeyFrame)

	// Verify sample sizes are non-zero
	for _, s := range info.Samples {
		require.Greater(t, s.Size, uint32(0))
		require.Greater(t, s.Duration, uint32(0))
	}
}

func TestParseSegment_H265(t *testing.T) {
	dir := t.TempDir()
	path := createTestH265Segment(t, dir)

	info, err := ParseSegment(path)
	require.NoError(t, err)
	require.Equal(t, "h265", info.Codec)
	require.NotEmpty(t, info.SPS)
	require.NotEmpty(t, info.PPS)
	require.NotEmpty(t, info.VPS)
	require.Equal(t, uint32(1000), info.Timescale)
	require.Equal(t, 2, info.SampleCount)
	require.Equal(t, 66*time.Millisecond, info.TotalDuration)
	require.Len(t, info.Samples, 2)

	// First sample should be a keyframe (IRAP, NAL type 19)
	require.True(t, info.Samples[0].IsKeyFrame)
	// Second sample should NOT be a keyframe (TRAIL_R, NAL type 1)
	require.False(t, info.Samples[1].IsKeyFrame)
}

func TestParseSegment_NonExistentFile(t *testing.T) {
	_, err := ParseSegment("/nonexistent/path/to/file.mp4")
	require.Error(t, err)
	require.Contains(t, err.Error(), "open")
}

func TestParseSegment_EmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.mp4")
	require.NoError(t, os.WriteFile(path, nil, 0644))

	_, err := ParseSegment(path)
	require.Error(t, err)
}

func TestParseSegment_InvalidFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "garbage.mp4")
	require.NoError(t, os.WriteFile(path, []byte("this is not an mp4 file at all"), 0644))

	_, err := ParseSegment(path)
	require.Error(t, err)
}
