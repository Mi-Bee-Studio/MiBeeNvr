package merge

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/muxer"
	"github.com/stretchr/testify/require"
)

// Segment-construction helpers shared by merge tests (moved with the
// parser in #877 2b; they build fixtures via the muxer, independent of
// the parser).

// createTestH264SegmentWithParams creates an H.264 MP4 with custom SPS/PPS bytes.
func createTestH264SegmentWithParams(t *testing.T, dir string, sps, pps []byte) string {
	t.Helper()
	path := filepath.Join(dir, fmt.Sprintf("test_h264_%x.mp4", sps))

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

// createH264SegmentWithG711Audio creates an H.264 MP4 with G.711 μ-law audio track.
func createH264SegmentWithG711Audio(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "test_h264_g711.mp4")
	sps := []byte{0x67, 0x42, 0x00, 0x0a, 0xe2, 0x40, 0x40, 0x04, 0x00, 0x00, 0x00, 0x04, 0x00, 0x00, 0x00, 0xc8, 0x40}
	pps := []byte{0x68, 0xce, 0x38, 0x80}

	m := muxer.NewMP4Muxer(path)
	videoTrackID, err := m.AddH264Track(sps, pps)
	require.NoError(t, err)
	// G.711 μ-law 8kHz: config = [1, 0x00, 0x00, 0x1F, 0x40]
	audioTrackID, err := m.AddAudioTrack("g711", []byte{1, 0x00, 0x00, 0x1F, 0x40})
	require.NoError(t, err)

	// 2 video samples
	idrNAL := []byte{0x65, 0x88, 0x80, 0x40}
	require.NoError(t, m.WriteSample(videoTrackID, idrNAL, 0, 33*time.Millisecond))
	pNAL := []byte{0x41, 0x10, 0x00, 0x0c}
	require.NoError(t, m.WriteSample(videoTrackID, pNAL, 33*time.Millisecond, 33*time.Millisecond))

	// 2 audio samples (1-byte G.711 payloads)
	require.NoError(t, m.WriteAudioSample(audioTrackID, []byte{0x55}, 0, 20*time.Millisecond))
	require.NoError(t, m.WriteAudioSample(audioTrackID, []byte{0xAA}, 20*time.Millisecond, 20*time.Millisecond))

	require.NoError(t, m.Close())
	return path
}
