package mediaprobe

import (
	"bytes"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/abema/go-mp4"
	"github.com/stretchr/testify/require"
)

func TestParseSegment_NonExistentFile(t *testing.T) {
	_, err := ParseSegment("/nonexistent/path/to/file.mp4")
	require.Error(t, err)
	require.Contains(t, err.Error(), "open")
}

func TestParseSegment_EmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.mp4")
	require.NoError(t, os.WriteFile(path, nil, 0o644))

	_, err := ParseSegment(path)
	require.Error(t, err)
}

func TestParseSegment_InvalidFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "garbage.mp4")
	require.NoError(t, os.WriteFile(path, []byte("this is not an mp4 file at all"), 0o644))

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
	require.NoError(t, os.WriteFile(path, data, 0o644))
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
	require.NoError(t, os.WriteFile(path, buf.Bytes(), 0o644))

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
	require.NoError(t, os.WriteFile(path, buf.Bytes(), 0o644))

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
	require.NoError(t, os.WriteFile(path, []byte{0x00}, 0o644))
	f, err := os.Open(path)
	require.NoError(t, err)
	defer f.Close()

	samples := []SampleEntry{{Offset: 100, Size: 10}}
	err = detectKeyframes(f, samples, "h264")
	require.NoError(t, err)
	require.False(t, samples[0].IsKeyFrame)
}
