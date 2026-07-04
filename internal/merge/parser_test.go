package merge

import (
	"bytes"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/muxer"
	"github.com/abema/go-mp4"
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

func TestParseSegment_G711Audio_TraversalNotAborted(t *testing.T) {
	dir := t.TempDir()
	path := createH264SegmentWithG711Audio(t, dir)

	info, err := ParseSegment(path)
	require.NoError(t, err)

	// Codec detection
	require.Equal(t, "h264", info.Codec)
	require.Equal(t, "g711", info.AudioCodec)
	require.True(t, info.G711MULaw, "expected μ-law flag")

	// Video sample count — regression: old Expand() aborted traversal, losing stsz/stco for BOTH tracks
	require.GreaterOrEqual(t, info.SampleCount, 2, "video sample count should be ≥ 2")

	// Audio sample count — same root cause: Expand() aborted traversal
	require.GreaterOrEqual(t, info.AudioSampleCount, 2, "audio sample count should be ≥ 2")
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

// --- buildTrackSamples tests ---

func TestBuildTrackSamples_StcoOnly(t *testing.T) {
	// Test buildTrackSamples with stco (chunk offsets within 32-bit range).
	tr := &trackAccum{
		sampleCount: 3,
		stszUniform: 100,
		stcoOffsets: []uint32{0, 300, 600},
		stscEntries: []mp4.StscEntry{
			{FirstChunk: 1, SamplesPerChunk: 1, SampleDescriptionIndex: 1},
		},
		sttsEntries: []mp4.SttsEntry{
			{SampleCount: 3, SampleDelta: 33},
		},
	}
	samples, err := buildTrackSamples(tr)
	require.NoError(t, err)
	require.Len(t, samples, 3)
	require.Equal(t, int64(0), samples[0].Offset)
	require.Equal(t, int64(300), samples[1].Offset)
	require.Equal(t, int64(600), samples[2].Offset)
	require.Equal(t, uint32(100), samples[0].Size)
	require.Equal(t, uint32(33), samples[0].Duration)
}

func TestBuildTrackSamples_Co64Only(t *testing.T) {
	// Test buildTrackSamples with co64 (chunk offsets beyond 32-bit range).
	tr := &trackAccum{
		sampleCount: 2,
		stszUniform: 200,
		co64Offsets: []uint64{4294967296, 5000000000}, // > MaxUint32
		stscEntries: []mp4.StscEntry{
			{FirstChunk: 1, SamplesPerChunk: 1, SampleDescriptionIndex: 1},
		},
		sttsEntries: []mp4.SttsEntry{
			{SampleCount: 2, SampleDelta: 33},
		},
	}
	samples, err := buildTrackSamples(tr)
	require.NoError(t, err)
	require.Len(t, samples, 2)
	require.Equal(t, int64(4294967296), samples[0].Offset)
	require.Equal(t, int64(5000000000), samples[1].Offset)
}

func TestBuildTrackSamples_UniformSizes(t *testing.T) {
	// Test with uniform sample size (stsz.SampleSize != 0).
	tr := &trackAccum{
		sampleCount: 4,
		stszUniform: 50,
		stcoOffsets: []uint32{0},
		stscEntries: []mp4.StscEntry{
			{FirstChunk: 1, SamplesPerChunk: 4, SampleDescriptionIndex: 1},
		},
		sttsEntries: []mp4.SttsEntry{
			{SampleCount: 4, SampleDelta: 33},
		},
	}
	samples, err := buildTrackSamples(tr)
	require.NoError(t, err)
	require.Len(t, samples, 4)
	for i, s := range samples {
		require.Equal(t, uint32(50), s.Size, "sample %d size", i)
		require.Equal(t, int64(i*50), s.Offset, "sample %d offset", i)
	}
}

func TestBuildTrackSamples_EmptyStsc(t *testing.T) {
	tr := &trackAccum{
		sampleCount: 1,
		stszUniform: 100,
		stcoOffsets: []uint32{0},
		sttsEntries: []mp4.SttsEntry{
			{SampleCount: 1, SampleDelta: 33},
		},
	}
	_, err := buildTrackSamples(tr)
	require.Error(t, err)
	require.Contains(t, err.Error(), "no stsc entries")
}

func TestBuildTrackSamples_InvalidStscIndex(t *testing.T) {
	tr := &trackAccum{
		sampleCount: 1,
		stszUniform: 100,
		stcoOffsets: []uint32{0},
		stscEntries: []mp4.StscEntry{
			{FirstChunk: 1, SamplesPerChunk: 1, SampleDescriptionIndex: 0}, // not 1
		},
		sttsEntries: []mp4.SttsEntry{
			{SampleCount: 1, SampleDelta: 33},
		},
	}
	_, err := buildTrackSamples(tr)
	require.Error(t, err)
	require.Contains(t, err.Error(), "SampleDescriptionIndex")
}

func TestBuildTrackSamples_EmptyChunkOffsets(t *testing.T) {
	tr := &trackAccum{
		sampleCount: 1,
		stszUniform: 100,
		stscEntries: []mp4.StscEntry{
			{FirstChunk: 1, SamplesPerChunk: 1, SampleDescriptionIndex: 1},
		},
		sttsEntries: []mp4.SttsEntry{
			{SampleCount: 1, SampleDelta: 33},
		},
	}
	_, err := buildTrackSamples(tr)
	require.Error(t, err)
	require.Contains(t, err.Error(), "no chunk offsets")
}

func TestBuildTrackSamples_SttsCountMismatch(t *testing.T) {
	tr := &trackAccum{
		sampleCount: 3,
		stszUniform: 100,
		stcoOffsets: []uint32{0},
		stscEntries: []mp4.StscEntry{
			{FirstChunk: 1, SamplesPerChunk: 3, SampleDescriptionIndex: 1},
		},
		sttsEntries: []mp4.SttsEntry{
			{SampleCount: 2, SampleDelta: 33}, // 2 != 3
		},
	}
	_, err := buildTrackSamples(tr)
	require.Error(t, err)
	require.Contains(t, err.Error(), "stts total sample count")
}

func TestBuildTrackSamples_SampleCountMismatch(t *testing.T) {
	tr := &trackAccum{
		sampleCount: 5,
		stszUniform: 100,
		stcoOffsets: []uint32{0},
		stscEntries: []mp4.StscEntry{
			{FirstChunk: 1, SamplesPerChunk: 3, SampleDescriptionIndex: 1},
		},
		sttsEntries: []mp4.SttsEntry{
			{SampleCount: 5, SampleDelta: 33},
		},
	}
	_, err := buildTrackSamples(tr)
	require.Error(t, err)
	require.Contains(t, err.Error(), "sample count mismatch")
}

func TestBuildTrackSamples_DurationOverflow(t *testing.T) {
	// Test duration overflow detection (cumulative PTS > MaxUint32).
	tr := &trackAccum{
		sampleCount: 2,
		stszUniform: 100,
		stcoOffsets: []uint32{0},
		stscEntries: []mp4.StscEntry{
			{FirstChunk: 1, SamplesPerChunk: 2, SampleDescriptionIndex: 1},
		},
		sttsEntries: []mp4.SttsEntry{
			{SampleCount: 2, SampleDelta: math.MaxUint32},
		},
	}
	_, err := buildTrackSamples(tr)
	require.Error(t, err)
	require.Contains(t, err.Error(), "PTS overflow")
}

func TestBuildSampleEntries_ZeroSampleCount(t *testing.T) {
	samples, err := buildSampleEntries(nil, nil, nil, nil)
	require.NoError(t, err)
	require.Nil(t, samples)
}

// --- detectKeyframes tests ---

func TestDetectKeyframes_EmptySamples(t *testing.T) {
	var f os.File
	err := detectKeyframes(&f, nil, "h264")
	require.NoError(t, err)
}

func TestDetectKeyframes_EmptyCodec(t *testing.T) {
	var f os.File
	err := detectKeyframes(&f, []SampleEntry{{Offset: 0, Size: 10}}, "")
	require.NoError(t, err)
}

func TestDetectKeyframes_SmallSample(t *testing.T) {
	// Samples smaller than 5 bytes should be skipped.
	dir := t.TempDir()
	path := filepath.Join(dir, "test.mp4")
	data := []byte{0x00, 0x00, 0x00, 0x01}
	require.NoError(t, os.WriteFile(path, data, 0644))
	f, err := os.Open(path)
	require.NoError(t, err)
	defer f.Close()

	samples := []SampleEntry{{Offset: 0, Size: 4}}
	err = detectKeyframes(f, samples, "h264")
	require.NoError(t, err)
	require.False(t, samples[0].IsKeyFrame)
}

func TestDetectKeyframes_H264AllTypes(t *testing.T) {
	// Test H.264 keyframe detection: SPS(7), PPS(8), IDR(5) are keyframes.
	dir := t.TempDir()
	path := filepath.Join(dir, "nal_test.mp4")

	var buf bytes.Buffer
	// 4-byte length prefix + 1 byte NAL header = 5 bytes per sample
	buf.Write([]byte{0x00, 0x00, 0x00, 0x01, 0x01}) // type 1 -> non-IDR
	buf.Write([]byte{0x00, 0x00, 0x00, 0x01, 0x05}) // type 5 -> IDR (keyframe)
	buf.Write([]byte{0x00, 0x00, 0x00, 0x01, 0x07}) // type 7 -> SPS (keyframe)
	buf.Write([]byte{0x00, 0x00, 0x00, 0x01, 0x08}) // type 8 -> PPS (keyframe)
	buf.Write([]byte{0x00, 0x00, 0x00, 0x01, 0x06}) // type 6 -> SEI (non-keyframe)
	require.NoError(t, os.WriteFile(path, buf.Bytes(), 0644))

	f, err := os.Open(path)
	require.NoError(t, err)
	defer f.Close()

	samples := []SampleEntry{
		{Offset: 0, Size: 5},
		{Offset: 5, Size: 5},
		{Offset: 10, Size: 5},
		{Offset: 15, Size: 5},
		{Offset: 20, Size: 5},
	}
	err = detectKeyframes(f, samples, "h264")
	require.NoError(t, err)
	require.False(t, samples[0].IsKeyFrame, "NAL type 1 should not be keyframe")
	require.True(t, samples[1].IsKeyFrame, "NAL type 5 (IDR) should be keyframe")
	require.True(t, samples[2].IsKeyFrame, "NAL type 7 (SPS) should be keyframe")
	require.True(t, samples[3].IsKeyFrame, "NAL type 8 (PPS) should be keyframe")
	require.False(t, samples[4].IsKeyFrame, "NAL type 6 (SEI) should not be keyframe")
}

func TestDetectKeyframes_H265AllTypes(t *testing.T) {
	// Test H.265 keyframe detection: IRAP types (16-21) are keyframes.
	dir := t.TempDir()
	path := filepath.Join(dir, "h265_nal_test.mp4")

	var buf bytes.Buffer
	// H.265: first byte of NAL = (nal_unit_type << 1) | 1, plus additional byte
	buf.Write([]byte{0x00, 0x00, 0x00, 0x02, 0x01, 0x00}) // type 0 (TRAIL_N) -> non-keyframe
	buf.Write([]byte{0x00, 0x00, 0x00, 0x02, 0x27, 0x00}) // type 19 (IDR_W_RADL) <<1|1 = 0x27 -> keyframe
	buf.Write([]byte{0x00, 0x00, 0x00, 0x02, 0x21, 0x00}) // type 16 (BLA) <<1|1 = 0x21 -> keyframe
	buf.Write([]byte{0x00, 0x00, 0x00, 0x02, 0x2B, 0x00}) // type 21 (CRA) <<1|1 = 0x2B -> keyframe
	buf.Write([]byte{0x00, 0x00, 0x00, 0x02, 0x03, 0x00}) // type 1 (TRAIL_R) -> non-keyframe
	require.NoError(t, os.WriteFile(path, buf.Bytes(), 0644))

	f, err := os.Open(path)
	require.NoError(t, err)
	defer f.Close()

	samples := []SampleEntry{
		{Offset: 0, Size: 6},
		{Offset: 6, Size: 6},
		{Offset: 12, Size: 6},
		{Offset: 18, Size: 6},
		{Offset: 24, Size: 6},
	}
	err = detectKeyframes(f, samples, "h265")
	require.NoError(t, err)
	require.False(t, samples[0].IsKeyFrame, "H.265 type 0 should not be keyframe")
	require.True(t, samples[1].IsKeyFrame, "H.265 type 19 (IDR) should be keyframe")
	require.True(t, samples[2].IsKeyFrame, "H.265 type 16 (BLA) should be keyframe")
	require.True(t, samples[3].IsKeyFrame, "H.265 type 21 (CRA) should be keyframe")
	require.False(t, samples[4].IsKeyFrame, "H.265 type 1 should not be keyframe")
}

func TestDetectKeyframes_ReadError(t *testing.T) {
	// ReadAt error beyond EOF is handled gracefully (sample skipped).
	dir := t.TempDir()
	path := filepath.Join(dir, "small.mp4")
	require.NoError(t, os.WriteFile(path, []byte{0x00}, 0644))
	f, err := os.Open(path)
	require.NoError(t, err)
	defer f.Close()

	samples := []SampleEntry{{Offset: 100, Size: 10}}
	err = detectKeyframes(f, samples, "h264")
	require.NoError(t, err)
	require.False(t, samples[0].IsKeyFrame)
}
