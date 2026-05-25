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

// --- Audio track helpers and tests ---

// testAudioConfig is AAC-LC, 44100Hz, stereo AudioSpecificConfig.
// audioObjectType=2(5bits) + samplingFreqIndex=4(4bits) + channelConfig=2(4bits) + rest=0.
var testAudioConfig = []byte{0x12, 0x10}

// testAACFrame is a minimal fake AAC frame for testing.
var testAACFrame = []byte{0x21, 0x1a, 0x7e, 0x00, 0x44, 0x8a}

// createH264SegmentWithAudio creates an H.264 MP4 with both video and AAC audio tracks.
func createH264SegmentWithAudio(t *testing.T, dir, name string, sps, pps []byte, videoSamples [][]byte, audioConfig []byte, audioSamples [][]byte) string {
	t.Helper()
	path := filepath.Join(dir, name)

	m := muxer.NewMP4Muxer(path)
	videoTrackID, err := m.AddH264Track(sps, pps)
	require.NoError(t, err)
	audioTrackID, err := m.AddAudioTrack("aac", audioConfig)
	require.NoError(t, err)

	for i, nalu := range videoSamples {
		pts := time.Duration(i) * 33 * time.Millisecond
		require.NoError(t, m.WriteSample(videoTrackID, nalu, pts, 33*time.Millisecond))
	}
	for i, frame := range audioSamples {
		pts := time.Duration(i) * 23 * time.Millisecond
		require.NoError(t, m.WriteAudioSample(audioTrackID, frame, pts, 23*time.Millisecond))
	}

	require.NoError(t, m.Close())
	return path
}

// createH265SegmentWithAudio creates an H.265 MP4 with both video and AAC audio tracks.
func createH265SegmentWithAudio(t *testing.T, dir, name string, vps, sps, pps []byte, videoSamples [][]byte, audioConfig []byte, audioSamples [][]byte) string {
	t.Helper()
	path := filepath.Join(dir, name)

	m := muxer.NewMP4Muxer(path)
	videoTrackID, err := m.AddH265Track(vps, sps, pps)
	require.NoError(t, err)
	audioTrackID, err := m.AddAudioTrack("aac", audioConfig)
	require.NoError(t, err)

	for i, nalu := range videoSamples {
		pts := time.Duration(i) * 33 * time.Millisecond
		require.NoError(t, m.WriteSample(videoTrackID, nalu, pts, 33*time.Millisecond))
	}
	for i, frame := range audioSamples {
		pts := time.Duration(i) * 23 * time.Millisecond
		require.NoError(t, m.WriteAudioSample(audioTrackID, frame, pts, 23*time.Millisecond))
	}

	require.NoError(t, m.Close())
	return path
}

func TestParseSegment_WithAudio(t *testing.T) {
	dir := t.TempDir()

	sps := []byte{0x67, 0x42, 0x00, 0x0a, 0xe2, 0x40, 0x40, 0x04, 0x00, 0x00, 0x00, 0x04, 0x00, 0x00, 0x00, 0xc8, 0x40}
	pps := []byte{0x68, 0xce, 0x38, 0x80}
	idrNAL := []byte{0x65, 0x88, 0x80, 0x40}
	pNAL := []byte{0x41, 0x10, 0x00, 0x0c}

	path := createH264SegmentWithAudio(t, dir, "av.mp4", sps, pps,
		[][]byte{idrNAL, pNAL},
		testAudioConfig,
		[][]byte{testAACFrame, testAACFrame, testAACFrame})

	info, err := ParseSegment(path)
	require.NoError(t, err)

	// Video track
	require.Equal(t, "h264", info.Codec)
	require.Equal(t, 2, info.SampleCount)
	require.Equal(t, sps, info.SPS)
	require.Equal(t, pps, info.PPS)

	// Audio track
	require.True(t, info.HasAudio)
	require.Equal(t, testAudioConfig, info.AudioConfig)
	require.Equal(t, 3, info.AudioSampleCount)
	require.Len(t, info.AudioSamples, 3)
}

func TestParseSegment_VideoOnlyNoAudio(t *testing.T) {
	dir := t.TempDir()

	sps := []byte{0x67, 0x42, 0x00, 0x0a, 0xe2, 0x40, 0x40, 0x04, 0x00, 0x00, 0x00, 0x04, 0x00, 0x00, 0x00, 0xc8, 0x40}
	pps := []byte{0x68, 0xce, 0x38, 0x80}
	idrNAL := []byte{0x65, 0x88, 0x80, 0x40}

	path := createH264SegmentWithSamples(t, dir, "video_only.mp4", sps, pps, [][]byte{idrNAL})
	info, err := ParseSegment(path)
	require.NoError(t, err)

	require.Equal(t, "h264", info.Codec)
	require.Equal(t, 1, info.SampleCount)
	require.False(t, info.HasAudio)
	require.Nil(t, info.AudioConfig)
	require.Equal(t, 0, info.AudioSampleCount)
}

func TestMergeMP4Segments_WithAudio(t *testing.T) {
	dir := t.TempDir()

	sps := []byte{0x67, 0x42, 0x00, 0x0a, 0xe2, 0x40, 0x40, 0x04, 0x00, 0x00, 0x00, 0x04, 0x00, 0x00, 0x00, 0xc8, 0x40}
	pps := []byte{0x68, 0xce, 0x38, 0x80}
	idrNAL := []byte{0x65, 0x88, 0x80, 0x40}
	pNAL := []byte{0x41, 0x10, 0x00, 0x0c}

	seg1 := createH264SegmentWithAudio(t, dir, "seg1.mp4", sps, pps,
		[][]byte{idrNAL, pNAL},
		testAudioConfig,
		[][]byte{testAACFrame, testAACFrame})
	seg2 := createH264SegmentWithAudio(t, dir, "seg2.mp4", sps, pps,
		[][]byte{idrNAL, pNAL, pNAL},
		testAudioConfig,
		[][]byte{testAACFrame, testAACFrame, testAACFrame})

	info1, err := ParseSegment(seg1)
	require.NoError(t, err)
	info2, err := ParseSegment(seg2)
	require.NoError(t, err)

	outputPath := filepath.Join(dir, "merged.mp4")
	err = MergeMP4Segments([]*SegmentInfo{info1, info2}, outputPath)
	require.NoError(t, err)

	merged, err := ParseSegment(outputPath)
	require.NoError(t, err)

	// Video: 2 + 3 = 5 samples
	require.Equal(t, "h264", merged.Codec)
	require.Equal(t, 5, merged.SampleCount)
	require.Equal(t, info1.SPS, merged.SPS)
	require.Equal(t, info1.PPS, merged.PPS)

	// Audio: 2 + 3 = 5 samples
	require.True(t, merged.HasAudio)
	require.Equal(t, 5, merged.AudioSampleCount)
	require.Equal(t, testAudioConfig, merged.AudioConfig)
}

func TestMergeMP4Segments_AudioConfigMismatch(t *testing.T) {
	dir := t.TempDir()

	sps := []byte{0x67, 0x42, 0x00, 0x0a, 0xe2, 0x40, 0x40, 0x04, 0x00, 0x00, 0x00, 0x04, 0x00, 0x00, 0x00, 0xc8, 0x40}
	pps := []byte{0x68, 0xce, 0x38, 0x80}
	idrNAL := []byte{0x65, 0x88, 0x80, 0x40}

	config1 := []byte{0x12, 0x10} // AAC-LC, 44100Hz, stereo
	config2 := []byte{0x11, 0x90} // AAC-LC, 48000Hz, mono

	seg1 := createH264SegmentWithAudio(t, dir, "seg1.mp4", sps, pps,
		[][]byte{idrNAL}, config1, [][]byte{testAACFrame})
	seg2 := createH264SegmentWithAudio(t, dir, "seg2.mp4", sps, pps,
		[][]byte{idrNAL}, config2, [][]byte{testAACFrame})

	info1, err := ParseSegment(seg1)
	require.NoError(t, err)
	info2, err := ParseSegment(seg2)
	require.NoError(t, err)

	outputPath := filepath.Join(dir, "merged.mp4")
	err = MergeMP4Segments([]*SegmentInfo{info1, info2}, outputPath)
	require.Error(t, err)
	require.Contains(t, err.Error(), "audio config mismatch")
}

func TestMergeMP4Segments_MixedAudioPresence(t *testing.T) {
	dir := t.TempDir()

	sps := []byte{0x67, 0x42, 0x00, 0x0a, 0xe2, 0x40, 0x40, 0x04, 0x00, 0x00, 0x00, 0x04, 0x00, 0x00, 0x00, 0xc8, 0x40}
	pps := []byte{0x68, 0xce, 0x38, 0x80}
	idrNAL := []byte{0x65, 0x88, 0x80, 0x40}

	// Segment with audio
	seg1 := createH264SegmentWithAudio(t, dir, "seg1.mp4", sps, pps,
		[][]byte{idrNAL}, testAudioConfig, [][]byte{testAACFrame})
	// Segment without audio
	seg2 := createH264SegmentWithSamples(t, dir, "seg2.mp4", sps, pps, [][]byte{idrNAL})

	info1, err := ParseSegment(seg1)
	require.NoError(t, err)
	info2, err := ParseSegment(seg2)
	require.NoError(t, err)

	outputPath := filepath.Join(dir, "merged.mp4")
	err = MergeMP4Segments([]*SegmentInfo{info1, info2}, outputPath)
	require.Error(t, err)
	require.Contains(t, err.Error(), "audio")
}

func TestMergeMP4Segments_H265WithAudio(t *testing.T) {
	dir := t.TempDir()

	vps := []byte{0x40, 0x01, 0x0c, 0x01, 0xff, 0xff, 0x01, 0x60, 0x00, 0x00, 0x00, 0x00, 0x00, 0x90, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x5d, 0xac, 0x59}
	sps := []byte{0x42, 0x01, 0x01, 0x01, 0x60, 0x00, 0x00, 0x00, 0x00, 0x00, 0x90, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x5d, 0xa0, 0x02, 0x80, 0x80, 0x2d, 0x16, 0x59, 0x59, 0xa4, 0x93, 0x2b, 0x80, 0x40, 0x00, 0x00, 0x07, 0x92}
	pps := []byte{0x44, 0x01, 0xc1, 0x73, 0xd1, 0x89}
	idrNAL := []byte{0x27, 0x01, 0xaf, 0x15, 0x6a}
	pNAL := []byte{0x03, 0x20, 0x10, 0x00}

	seg1 := createH265SegmentWithAudio(t, dir, "h265_seg1.mp4", vps, sps, pps,
		[][]byte{idrNAL}, testAudioConfig, [][]byte{testAACFrame, testAACFrame})
	seg2 := createH265SegmentWithAudio(t, dir, "h265_seg2.mp4", vps, sps, pps,
		[][]byte{pNAL, pNAL}, testAudioConfig, [][]byte{testAACFrame})

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
	require.Equal(t, 3, merged.SampleCount) // 1 + 2 video
	require.True(t, merged.HasAudio)
	require.Equal(t, 3, merged.AudioSampleCount) // 2 + 1 audio
	require.Equal(t, testAudioConfig, merged.AudioConfig)
}
