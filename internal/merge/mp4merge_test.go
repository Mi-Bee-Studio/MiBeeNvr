package merge

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/mp4util"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/muxer"
	"github.com/abema/go-mp4"
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
	err = MergeMP4Segments(context.Background(), []*SegmentInfo{info1, info2}, outputPath)
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
	err = MergeMP4Segments(context.Background(), []*SegmentInfo{info1, info2}, outputPath)
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
	err = MergeMP4Segments(context.Background(), []*SegmentInfo{info}, outputPath)
	require.NoError(t, err)

	// Verify merged file is parseable and has same samples
	merged, err := ParseSegment(outputPath)
	require.NoError(t, err)
	require.Equal(t, 3, merged.SampleCount)
	require.Equal(t, 99*time.Millisecond, merged.TotalDuration)
}

func TestMergeMP4Segments_EmptyList(t *testing.T) {
	outputPath := filepath.Join(t.TempDir(), "merged.mp4")
	err := MergeMP4Segments(context.Background(), nil, outputPath)
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
	err = MergeMP4Segments(context.Background(), []*SegmentInfo{info1, info2, info3}, outputPath)
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
	err = MergeMP4Segments(context.Background(), []*SegmentInfo{info1, info2}, outputPath)
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
	err = MergeMP4Segments(context.Background(), []*SegmentInfo{info1, info2}, outputPath)
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

	// When audio configs differ, the merge should SUCCEED but drop audio
	// (video-only output). This handles camera reconnect scenarios where
	// audio negotiation changes mid-session.
	outputPath := filepath.Join(dir, "merged.mp4")
	err = MergeMP4Segments(context.Background(), []*SegmentInfo{info1, info2}, outputPath)
	require.NoError(t, err, "merge should succeed with audio dropped")

	// Verify output is video-only (no audio).
	merged, err := ParseSegment(outputPath)
	require.NoError(t, err)
	require.False(t, merged.HasAudio, "merged output should not have audio")
	require.Equal(t, 2, merged.SampleCount, "video samples should be preserved")
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

	// When audio presence differs, the merge should SUCCEED but drop audio.
	// This is the real-world scenario: camera reconnected after audio_enabled
	// was toggled, or G.711 negotiation succeeded/failed mid-session.
	outputPath := filepath.Join(dir, "merged.mp4")
	err = MergeMP4Segments(context.Background(), []*SegmentInfo{info1, info2}, outputPath)
	require.NoError(t, err, "merge should succeed with audio dropped")

	// Verify output is video-only.
	merged, err := ParseSegment(outputPath)
	require.NoError(t, err)
	require.False(t, merged.HasAudio, "merged output should not have audio")
	require.Equal(t, 2, merged.SampleCount, "video samples should be preserved")
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
	err = MergeMP4Segments(context.Background(), []*SegmentInfo{info1, info2}, outputPath)
	require.NoError(t, err)

	merged, err := ParseSegment(outputPath)
	require.NoError(t, err)
	require.Equal(t, "h265", merged.Codec)
	require.Equal(t, 3, merged.SampleCount) // 1 + 2 video
	require.True(t, merged.HasAudio)
	require.Equal(t, 3, merged.AudioSampleCount) // 2 + 1 audio
	require.Equal(t, testAudioConfig, merged.AudioConfig)
}

// --- Additional error path tests ---

func TestMergeMP4Segments_CodecMismatch(t *testing.T) {
	dir := t.TempDir()

	sps := []byte{0x67, 0x42, 0x00, 0x0a, 0xe2, 0x40, 0x40, 0x04, 0x00, 0x00, 0x00, 0x04, 0x00, 0x00, 0x00, 0xc8, 0x40}
	pps := []byte{0x68, 0xce, 0x38, 0x80}
	idr := []byte{0x65, 0x88, 0x80, 0x40}

	h264Seg := createH264SegmentWithSamples(t, dir, "h264.mp4", sps, pps, [][]byte{idr})
	h264Info, err := ParseSegment(h264Seg)
	require.NoError(t, err)

	vps := []byte{0x40, 0x01, 0x0c, 0x01, 0xff, 0xff, 0x01, 0x60, 0x00, 0x00, 0x00, 0x00, 0x00, 0x90, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x5d, 0xac, 0x59}
	h265Sps := []byte{0x42, 0x01, 0x01, 0x01, 0x60, 0x00, 0x00, 0x00, 0x00, 0x00, 0x90, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x5d, 0xa0, 0x02, 0x80, 0x80, 0x2d, 0x16, 0x59, 0x59, 0xa4, 0x93, 0x2b, 0x80, 0x40, 0x00, 0x00, 0x07, 0x92}
	h265Pps := []byte{0x44, 0x01, 0xc1, 0x73, 0xd1, 0x89}
	h265idr := []byte{0x27, 0x01, 0xaf, 0x15, 0x6a}

	h265Seg := createH265SegmentWithSamples(t, dir, "h265.mp4", vps, h265Sps, h265Pps, [][]byte{h265idr})
	h265Info, err := ParseSegment(h265Seg)
	require.NoError(t, err)

	outputPath := filepath.Join(dir, "merged.mp4")
	err = MergeMP4Segments(context.Background(), []*SegmentInfo{h264Info, h265Info}, outputPath)
	require.Error(t, err)
	require.Contains(t, err.Error(), "codec mismatch")
}

func TestMergeMP4Segments_VPSMismatch(t *testing.T) {
	dir := t.TempDir()

	vps1 := []byte{0x40, 0x01, 0x0c, 0x01, 0xff, 0xff, 0x01, 0x60, 0x00, 0x00, 0x00, 0x00, 0x00, 0x90, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x5d, 0xac, 0x59}
	vps2 := []byte{0x40, 0x01, 0x0c, 0x01, 0xff, 0xff, 0x01, 0x60, 0x00, 0x00, 0x00, 0x00, 0x00, 0x90, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x5d, 0xac, 0x58} // different
	sps := []byte{0x42, 0x01, 0x01, 0x01, 0x60, 0x00, 0x00, 0x00, 0x00, 0x00, 0x90, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x5d, 0xa0, 0x02, 0x80, 0x80, 0x2d, 0x16, 0x59, 0x59, 0xa4, 0x93, 0x2b, 0x80, 0x40, 0x00, 0x00, 0x07, 0x92}
	pps := []byte{0x44, 0x01, 0xc1, 0x73, 0xd1, 0x89}
	idr := []byte{0x27, 0x01, 0xaf, 0x15, 0x6a}

	seg1 := createH265SegmentWithSamples(t, dir, "seg1.mp4", vps1, sps, pps, [][]byte{idr})
	seg2 := createH265SegmentWithSamples(t, dir, "seg2.mp4", vps2, sps, pps, [][]byte{idr})

	info1, err := ParseSegment(seg1)
	require.NoError(t, err)
	info2, err := ParseSegment(seg2)
	require.NoError(t, err)

	outputPath := filepath.Join(dir, "merged.mp4")
	err = MergeMP4Segments(context.Background(), []*SegmentInfo{info1, info2}, outputPath)
	require.Error(t, err)
	require.Contains(t, err.Error(), "VPS mismatch")
}

func TestMergeMP4Segments_EmptyFirstSegment(t *testing.T) {
	dir := t.TempDir()

	sps := []byte{0x67, 0x42, 0x00, 0x0a, 0xe2, 0x40, 0x40, 0x04, 0x00, 0x00, 0x00, 0x04, 0x00, 0x00, 0x00, 0xc8, 0x40}
	pps := []byte{0x68, 0xce, 0x38, 0x80}

	// Create a dummy SegmentInfo with no samples (no SampleCount, no HasAudio)
	emptyInfo := &SegmentInfo{
		Codec: "h264",
		SPS:   sps,
		PPS:   pps,
	}
	outputPath := filepath.Join(dir, "merged.mp4")
	err := MergeMP4Segments(context.Background(), []*SegmentInfo{emptyInfo}, outputPath)
	require.Error(t, err)
	require.Contains(t, err.Error(), "empty sample table")
}

func TestMergeMP4Segments_ContextCancelled(t *testing.T) {
	dir := t.TempDir()

	sps := []byte{0x67, 0x42, 0x00, 0x0a, 0xe2, 0x40, 0x40, 0x04, 0x00, 0x00, 0x00, 0x04, 0x00, 0x00, 0x00, 0xc8, 0x40}
	pps := []byte{0x68, 0xce, 0x38, 0x80}
	idr := []byte{0x65, 0x88, 0x80, 0x40}

	seg := createH264SegmentWithSamples(t, dir, "seg.mp4", sps, pps, [][]byte{idr, idr})
	info, err := ParseSegment(seg)
	require.NoError(t, err)

	// Create a cancelled context
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	outputPath := filepath.Join(dir, "merged.mp4")
	err = MergeMP4Segments(ctx, []*SegmentInfo{info}, outputPath)
	require.Error(t, err)
	require.Contains(t, err.Error(), "context canceled")
}

func TestMergeMP4Segments_SpsParseWarning(t *testing.T) {
	dir := t.TempDir()

	// Malformed SPS but valid enough for the muxer (just bytes).
	// If SPS parsing fails, MergeMP4Segments logs a warning and continues with 0x0 resolution.
	sps := []byte{0x67, 0x64, 0x00, 0x1e, 0xac, 0xd9, 0x40, 0xb4}
	pps := []byte{0x68, 0xce, 0x38, 0x80}
	idr := []byte{0x65, 0x88, 0x80, 0x40}

	seg := createH264SegmentWithSamples(t, dir, "seg.mp4", sps, pps, [][]byte{idr})
	info, err := ParseSegment(seg)
	require.NoError(t, err)

	outputPath := filepath.Join(dir, "merged.mp4")
	err = MergeMP4Segments(context.Background(), []*SegmentInfo{info}, outputPath)
	require.NoError(t, err)

	merged, err := ParseSegment(outputPath)
	require.NoError(t, err)
	require.Equal(t, 1, merged.SampleCount)
}

func TestMergeMP4Segments_EmptyFilePath(t *testing.T) {
	dir := t.TempDir()

	sps := []byte{0x67, 0x42, 0x00, 0x0a, 0xe2, 0x40, 0x40, 0x04, 0x00, 0x00, 0x00, 0x04, 0x00, 0x00, 0x00, 0xc8, 0x40}
	pps := []byte{0x68, 0xce, 0x38, 0x80}
	idr := []byte{0x65, 0x88, 0x80, 0x40}

	path := createH264SegmentWithSamples(t, dir, "seg.mp4", sps, pps, [][]byte{idr})
	info, err := ParseSegment(path)
	require.NoError(t, err)
	// Clear the file path to trigger the error
	info.FilePath = ""

	outputPath := filepath.Join(dir, "merged.mp4")
	err = MergeMP4Segments(context.Background(), []*SegmentInfo{info}, outputPath)
	require.Error(t, err)
	require.Contains(t, err.Error(), "empty FilePath")
}

// --- limitedWriter tests ---

func TestLimitedWriter_WriteEOF(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.bin")
	f, err := os.Create(path)
	require.NoError(t, err)
	defer f.Close()

	lw := &limitedWriter{w: f, remaining: 5, pos: 0}

	n, err := lw.Write([]byte("hello"))
	require.NoError(t, err)
	require.Equal(t, 5, n)

	// Second write should return EOF
	n, err = lw.Write([]byte("world"))
	require.Error(t, err)
	require.Equal(t, io.EOF, err)
	require.Equal(t, 0, n)
}

func TestLimitedWriter_WriteTruncated(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.bin")
	f, err := os.Create(path)
	require.NoError(t, err)
	defer f.Close()

	lw := &limitedWriter{w: f, remaining: 3, pos: 0}

	n, err := lw.Write([]byte("hello"))
	require.NoError(t, err)
	require.Equal(t, 3, n)

	// Read back to verify
	data := make([]byte, 3)
	f.Seek(0, 0)
	f.Read(data)
	require.Equal(t, "hel", string(data))
}

func TestLimitedWriter_SeekForward(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.bin")
	f, err := os.Create(path)
	require.NoError(t, err)
	defer f.Close()

	f.Write([]byte("hello"))

	lw := &limitedWriter{w: f, remaining: 10, pos: 5}

	newPos, serr := lw.Seek(3, 1) // relative seek forward
	require.NoError(t, serr)
	require.Equal(t, int64(8), newPos)
	require.Equal(t, int64(8), lw.pos)
}

func TestLimitedWriter_SeekBackward(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.bin")
	f, err := os.Create(path)
	require.NoError(t, err)
	defer f.Close()

	f.Write([]byte("hello world"))
	f.Seek(5, 0) // sync underlying file to pos 5

	lw := &limitedWriter{w: f, remaining: 10, pos: 5}

	newPos, rerr := lw.Seek(-3, 1) // relative seek backward
	require.NoError(t, rerr)
	require.Equal(t, int64(2), newPos)
	require.Equal(t, int64(2), lw.pos)
}

func TestParseAudioConfig_ExtendedSampleRate(t *testing.T) {
	// sampleRateIndex = (config[0] >> 3) & 0x0F = 0x0F → extended
	// config[0] = 0x78 = 0111 1000 → bits 7-3 = 01111 = 15 (extended)
	// config[0] bit 0 = 0, config[1] >> 6 = 0 → channelConfig = 0 (default 2)
	// Extended rate 48000 = 0xBB80
	// config[1]<<16 | config[2]<<8 | config[3]&0xFC = 0x00BB80
	config := []byte{0x78, 0x00, 0xBB, 0x80}
	ch, rate := parseAudioConfig(config)
	if ch != 2 {
		t.Errorf("channelCount = %d, want 2", ch)
	}
	if rate != 48000 {
		t.Errorf("sampleRate = %d, want 48000", rate)
	}
}

func TestParseAudioConfig_ChannelConfig(t *testing.T) {
	// sampleRateIndex = (config[0] >> 3) & 0x0F = 4 → 44100Hz
	// config[0] = 0x20 = 0010 0000 → bits 7-3 = 00100 = 4
	// config[0] bit 0 = 0, config[1] bits 7-6 = 01 → channelConfig = 1
	config := []byte{0x20, 0x40}
	ch, rate := parseAudioConfig(config)
	if ch != 1 {
		t.Errorf("channelCount = %d, want 1", ch)
	}
	if rate != 44100 {
		t.Errorf("sampleRate = %d, want 44100", rate)
	}
}

func TestParseAudioConfig_DefaultValues(t *testing.T) {
	// Empty config should return defaults: 2 channels, 44100 Hz
	ch, rate := parseAudioConfig(nil)
	if ch != 2 {
		t.Errorf("channelCount = %d, want 2", ch)
	}
	if rate != 44100 {
		t.Errorf("sampleRate = %d, want 44100", rate)
	}

	ch, rate = parseAudioConfig([]byte{0})
	if ch != 2 {
		t.Errorf("channelCount = %d, want 2 (default for single byte)", ch)
	}
	if rate != 44100 {
		t.Errorf("sampleRate = %d, want 44100", rate)
	}
}

// --- writeMergeFtyp tests ---

func TestWriteMergeFtyp_H264(t *testing.T) {
	var buf bytes.Buffer
	n, err := writeMergeFtyp(&buf, "h264")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// 32 bytes: 8 (box) + 4 (major) + 4 (version) + 4*4 (4 brands) = 32
	if n != 32 {
		t.Errorf("written = %d, want 32", n)
	}
	if buf.Len() != 32 {
		t.Errorf("buf.Len() = %d, want 32", buf.Len())
	}

	// Verify expected bytes
	want := []byte{
		0x00, 0x00, 0x00, 0x20, // box size = 32
		'f', 't', 'y', 'p', // ftyp box type
		'i', 's', 'o', 'm', // major brand
		0x00, 0x00, 0x00, 0x00, // minor version
		'i', 's', 'o', 'm', // compatible brand 0
		'i', 's', 'o', '2', // compatible brand 1
		'm', 'p', '4', '1', // compatible brand 2
		'a', 'v', 'c', '1', // compatible brand 3 (h264)
	}
	if !bytes.Equal(buf.Bytes(), want) {
		t.Errorf("writeMergeFtyp output mismatch\ngot:  % x\nwant: % x", buf.Bytes(), want)
	}
}

func TestWriteMergeFtyp_H265(t *testing.T) {
	var buf bytes.Buffer
	n, err := writeMergeFtyp(&buf, "h265")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 32 {
		t.Errorf("written = %d, want 32", n)
	}
	// Verify the last brand is hev1 instead of avc1
	data := buf.Bytes()
	if len(data) < 32 {
		t.Fatalf("output too short: %d bytes", len(data))
	}
	brand := string(data[28:32])
	if brand != "hev1" {
		t.Errorf("last brand = %q, want hev1", brand)
	}
}

func TestWriteMergeFtyp_Unknown(t *testing.T) {
	var buf bytes.Buffer
	n, err := writeMergeFtyp(&buf, "mjpeg")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// 28 bytes: 8 + 4 + 4 + 4*3 = 28 (no codec-specific brand)
	if n != 28 {
		t.Errorf("written = %d, want 28", n)
	}
	// Verify no avc1 or hev1 brand
	data := buf.Bytes()
	if len(data) < 28 {
		t.Fatalf("output too short: %d bytes", len(data))
	}
	brand := string(data[24:28])
	if brand != "mp41" {
		t.Errorf("last brand = %q, want mp41", brand)
	}
}

// --- bytesWriter tests ---

func TestBytesWriter_SeekEnd(t *testing.T) {
	bw := &bytesWriter{data: []byte("hello world"), pos: 5}
	newPos, err := bw.Seek(-3, 2) // seek from end
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if newPos != 8 {
		t.Errorf("newPos = %d, want 8", newPos)
	}
	if bw.pos != 8 {
		t.Errorf("bw.pos = %d, want 8", bw.pos)
	}
}

func TestBytesWriter_SeekNegative(t *testing.T) {
	bw := &bytesWriter{data: []byte("hello world"), pos: 5}
	newPos, err := bw.Seek(-10, 1) // seek backward past 0
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// pos clamped to 0
	if newPos != 0 {
		t.Errorf("newPos = %d, want 0", newPos)
	}
	if bw.pos != 0 {
		t.Errorf("bw.pos = %d, want 0", bw.pos)
	}
}

// --- limitedWriter Seek error test ---

type errWriteSeeker struct{}

func (e *errWriteSeeker) Write(p []byte) (int, error) { return len(p), nil }
func (e *errWriteSeeker) Seek(offset int64, whence int) (int64, error) {
	return 0, fmt.Errorf("seek error")
}

func TestLimitedWriter_SeekError(t *testing.T) {
	lw := &limitedWriter{w: &errWriteSeeker{}, remaining: 100, pos: 0}
	_, err := lw.Seek(10, 0)
	if err == nil {
		t.Fatal("expected error from seek")
	}
	if err.Error() != "seek error" {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestMergeHvcC_ByteAlignment is the merge-package mirror of timelapse's
// TestBuildHvcC_ConservativeTierAndCompat. Before #236, the merge package had
// NO byte-level hvcC test — only a round-trip ParseSegment check that would
// silently accept a subtly-wrong hvcC. This test pins the conservative
// Main-tier / zeroed-compat defaults that make ONVIF SPS inconsistencies
// (cam-fa049182) playable in Edge, now that the construction is shared via
// mp4util.BuildHvcC.
func TestMergeHvcC_ByteAlignment(t *testing.T) {
	// Inconsistent SPS captured from cam-fa049182 (Main profile + High tier +
	// stray compat bit). See timelapse TestBuildHvcC_ConservativeTierAndCompat
	// for the byte-by-byte decode.
	inconsistentSPS := []byte{
		0x42, 0x01, 0x21, 0x40, 0x00, 0x00, 0x03,
		0x00, 0x90, 0x00, 0x00, 0x03, 0x00,
		0x96, 0xa0, 0x01, 0x40, 0x20, 0x05, 0xa1,
	}
	pps := []byte{0x44, 0x01, 0xc1, 0x73, 0xd1, 0x89}
	vps := []byte{0x40, 0x01, 0x0c, 0x01, 0xff, 0xff, 0x01, 0x60}

	// The merge path now builds hvcC via the shared mp4util.BuildHvcC and
	// marshals it inside writeMergeH265SampleEntry. Marshal here the same way
	// to inspect the exact bytes that reach the output file.
	var buf bytes.Buffer
	hvcC := mp4util.BuildHvcC(vps, inconsistentSPS, pps)
	if _, err := mp4.Marshal(&buf, hvcC, mp4.Context{}); err != nil {
		t.Fatalf("mp4.Marshal(hvcC) failed: %v", err)
	}
	out := buf.Bytes()

	// Byte 1 must be 0x01 (Main tier forced + Main profile), NOT 0x21.
	if got := out[1]; got != 0x01 {
		t.Errorf("hvcC[1] = 0x%02x (space=%d tier=%d profile_idc=%d), want 0x01 "+
			"(Main tier forced; Edge rejects tier=1 + profile_idc=1)", got, got>>6, (got>>5)&1, got&0x1F)
	}
	// Bytes 2-5 (profile compat) and 6-11 (constraint) must be zeroed.
	for i := 2; i <= 11; i++ {
		if out[i] != 0x00 {
			t.Errorf("hvcC[%d] = 0x%02x, want 0x00 (forced to zero)", i, out[i])
		}
	}
	// numOfArrays (byte 22) must be 3.
	if out[22] != 3 {
		t.Errorf("hvcC[22] (numOfArrays) = %d, want 3", out[22])
	}
}
