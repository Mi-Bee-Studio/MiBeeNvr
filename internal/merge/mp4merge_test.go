package merge

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/muxer"
	"github.com/stretchr/testify/require"
)

// createH264SegmentWithSamples creates an H.264 MP4 with the given SPS/PPS and NALU samples.
// Each sample entry is (naluData, pts, duration).
func createH264SegmentWithSamples(t *testing.T, dir string, name string, sps, pps []byte, samples [][]byte) string {
	t.Helper()
	path := filepath.Join(dir, name)

	m := muxer.NewMP4Muxer(path)
	trackID, err := m.AddH264Track(sps, pps)
	require.NoError(t, err)

	for i, nalu := range samples {
		pts := time.Duration(i) * 33 * time.Millisecond
		require.NoError(t, m.WriteSample(trackID, nalu, pts, 33*time.Millisecond))
	}

	require.NoError(t, m.Close())
	return path
}

func TestMergeMP4Segments_SameSPS(t *testing.T) {
	dir := t.TempDir()

	sps := []byte{0x67, 0x42, 0x00, 0x0a, 0xe2, 0x40, 0x40, 0x04, 0x00, 0x00, 0x00, 0x04, 0x00, 0x00, 0x00, 0xc8, 0x40}
	pps := []byte{0x68, 0xce, 0x38, 0x80}
	idrNAL := []byte{0x65, 0x88, 0x80, 0x40}
	pNAL := []byte{0x41, 0x10, 0x00, 0x0c}

	// Create 2 segments with same SPS/PPS
	seg1 := createH264SegmentWithSamples(t, dir, "seg1.mp4", sps, pps, [][]byte{idrNAL, pNAL})
	seg2 := createH264SegmentWithSamples(t, dir, "seg2.mp4", sps, pps, [][]byte{idrNAL, pNAL, pNAL})

	// Parse both segments
	info1, err := ParseSegment(seg1)
	require.NoError(t, err)
	info2, err := ParseSegment(seg2)
	require.NoError(t, err)

	// Merge
	outputPath := filepath.Join(dir, "merged.mp4")
	err = MergeMP4Segments([]*SegmentInfo{info1, info2}, outputPath)
	require.NoError(t, err)

	// Verify output file exists and has content
	fi, err := os.Stat(outputPath)
	require.NoError(t, err)
	require.Greater(t, fi.Size(), int64(0))

	// Verify merged file is parseable
	merged, err := ParseSegment(outputPath)
	require.NoError(t, err)
	require.Equal(t, "h264", merged.Codec)
	// 2 + 3 = 5 total samples
	require.Equal(t, 5, merged.SampleCount)
	require.Equal(t, info1.SPS, merged.SPS)
	require.Equal(t, info1.PPS, merged.PPS)
	// Total duration: 5 samples * 33ms = 165ms
	require.Equal(t, 165*time.Millisecond, merged.TotalDuration)
}

func TestMergeMP4Segments_DifferentSPS(t *testing.T) {
	dir := t.TempDir()

	sps1 := []byte{0x67, 0x42, 0x00, 0x0a, 0xe2, 0x40, 0x40, 0x04, 0x00, 0x00, 0x00, 0x04, 0x00, 0x00, 0x00, 0xc8, 0x40}
	pps1 := []byte{0x68, 0xce, 0x38, 0x80}
	sps2 := []byte{0x67, 0x64, 0x00, 0x1f, 0xac, 0xd9, 0x40, 0x50, 0x05, 0xbb, 0x01, 0x10, 0x00, 0x00, 0x03, 0x00, 0x00, 0x03, 0x00, 0x00, 0x03, 0x00, 0x00, 0x03, 0x00, 0x7b, 0xac, 0x09}
	pps2 := []byte{0x68, 0xde, 0x3c, 0x80}

	idrNAL := []byte{0x65, 0x88, 0x80, 0x40}

	seg1 := createH264SegmentWithSamples(t, dir, "seg1.mp4", sps1, pps1, [][]byte{idrNAL})
	seg2 := createH264SegmentWithSamples(t, dir, "seg2.mp4", sps2, pps2, [][]byte{idrNAL})

	info1, err := ParseSegment(seg1)
	require.NoError(t, err)
	info2, err := ParseSegment(seg2)
	require.NoError(t, err)

	outputPath := filepath.Join(dir, "merged.mp4")
	err = MergeMP4Segments([]*SegmentInfo{info1, info2}, outputPath)
	require.Error(t, err)
	require.Contains(t, err.Error(), "SPS/PPS mismatch")
}

func TestMergeMP4Segments_SingleSegment(t *testing.T) {
	dir := t.TempDir()

	sps := []byte{0x67, 0x42, 0x00, 0x0a, 0xe2, 0x40, 0x40, 0x04, 0x00, 0x00, 0x00, 0x04, 0x00, 0x00, 0x00, 0xc8, 0x40}
	pps := []byte{0x68, 0xce, 0x38, 0x80}
	idrNAL := []byte{0x65, 0x88, 0x80, 0x40}
	pNAL := []byte{0x41, 0x10, 0x00, 0x0c}

	seg := createH264SegmentWithSamples(t, dir, "single.mp4", sps, pps, [][]byte{idrNAL, pNAL, pNAL})

	info, err := ParseSegment(seg)
	require.NoError(t, err)

	outputPath := filepath.Join(dir, "merged.mp4")
	err = MergeMP4Segments([]*SegmentInfo{info}, outputPath)
	require.NoError(t, err)

	// Verify merged file is parseable and has same samples
	merged, err := ParseSegment(outputPath)
	require.NoError(t, err)
	require.Equal(t, 3, merged.SampleCount)
	require.Equal(t, 99*time.Millisecond, merged.TotalDuration)
}

func TestMergeMP4Segments_EmptyList(t *testing.T) {
	outputPath := filepath.Join(t.TempDir(), "merged.mp4")
	err := MergeMP4Segments(nil, outputPath)
	require.Error(t, err)
	require.Contains(t, err.Error(), "no segments")
}

func TestMergeMP4Segments_ThreeSegments(t *testing.T) {
	dir := t.TempDir()

	sps := []byte{0x67, 0x42, 0x00, 0x0a, 0xe2, 0x40, 0x40, 0x04, 0x00, 0x00, 0x00, 0x04, 0x00, 0x00, 0x00, 0xc8, 0x40}
	pps := []byte{0x68, 0xce, 0x38, 0x80}
	idrNAL := []byte{0x65, 0x88, 0x80, 0x40}
	pNAL := []byte{0x41, 0x10, 0x00, 0x0c}

	seg1 := createH264SegmentWithSamples(t, dir, "seg1.mp4", sps, pps, [][]byte{idrNAL})
	seg2 := createH264SegmentWithSamples(t, dir, "seg2.mp4", sps, pps, [][]byte{pNAL, pNAL})
	seg3 := createH264SegmentWithSamples(t, dir, "seg3.mp4", sps, pps, [][]byte{idrNAL, pNAL})

	info1, err := ParseSegment(seg1)
	require.NoError(t, err)
	info2, err := ParseSegment(seg2)
	require.NoError(t, err)
	info3, err := ParseSegment(seg3)
	require.NoError(t, err)

	outputPath := filepath.Join(dir, "merged.mp4")
	err = MergeMP4Segments([]*SegmentInfo{info1, info2, info3}, outputPath)
	require.NoError(t, err)

	merged, err := ParseSegment(outputPath)
	require.NoError(t, err)
	// 1 + 2 + 2 = 5 samples
	require.Equal(t, 5, merged.SampleCount)
	require.Equal(t, 165*time.Millisecond, merged.TotalDuration)

	// Keyframes at positions 0 and 3 (seg1 IDR + seg3 IDR)
	require.True(t, merged.Samples[0].IsKeyFrame)
	require.False(t, merged.Samples[1].IsKeyFrame)
	require.False(t, merged.Samples[2].IsKeyFrame)
	require.True(t, merged.Samples[3].IsKeyFrame)
	require.False(t, merged.Samples[4].IsKeyFrame)
}

func TestMergeMP4Segments_H265(t *testing.T) {
	dir := t.TempDir()

	vps := []byte{0x40, 0x01, 0x0c, 0x01, 0xff, 0xff, 0x01, 0x60, 0x00, 0x00, 0x00, 0x00, 0x00, 0x90, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x5d, 0xac, 0x59}
	sps := []byte{0x42, 0x01, 0x01, 0x01, 0x60, 0x00, 0x00, 0x00, 0x00, 0x00, 0x90, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x5d, 0xa0, 0x02, 0x80, 0x80, 0x2d, 0x16, 0x59, 0x59, 0xa4, 0x93, 0x2b, 0x80, 0x40, 0x00, 0x00, 0x07, 0x92}
	pps := []byte{0x44, 0x01, 0xc1, 0x73, 0xd1, 0x89}
	idrNAL := []byte{0x27, 0x01, 0xaf, 0x15, 0x6a}
	pNAL := []byte{0x03, 0x20, 0x10, 0x00}

	seg1 := createH265SegmentWithSamples(t, dir, "h265_seg1.mp4", vps, sps, pps, [][]byte{idrNAL})
	seg2 := createH265SegmentWithSamples(t, dir, "h265_seg2.mp4", vps, sps, pps, [][]byte{pNAL, pNAL})

	info1, err := ParseSegment(seg1)
	require.NoError(t, err)
	info2, err := ParseSegment(seg2)
	require.NoError(t, err)

	outputPath := filepath.Join(dir, "merged_h265.mp4")
	err = MergeMP4Segments([]*SegmentInfo{info1, info2}, outputPath)
	require.NoError(t, err)

	merged, err := ParseSegment(outputPath)
	require.NoError(t, err)
	require.Equal(t, "h265", merged.Codec)
	require.Equal(t, 3, merged.SampleCount)
	require.Equal(t, 99*time.Millisecond, merged.TotalDuration)
}

// createH265SegmentWithSamples creates an H.265 MP4 with the given VPS/SPS/PPS and NALU samples.
func createH265SegmentWithSamples(t *testing.T, dir string, name string, vps, sps, pps []byte, samples [][]byte) string {
	t.Helper()
	path := filepath.Join(dir, name)

	m := muxer.NewMP4Muxer(path)
	trackID, err := m.AddH265Track(vps, sps, pps)
	require.NoError(t, err)

	for i, nalu := range samples {
		pts := time.Duration(i) * 33 * time.Millisecond
		require.NoError(t, m.WriteSample(trackID, nalu, pts, 33*time.Millisecond))
	}

	require.NoError(t, m.Close())
	return path
}
