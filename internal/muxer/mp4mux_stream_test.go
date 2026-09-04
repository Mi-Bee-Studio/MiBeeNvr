package muxer

// Streaming (incremental-flush) muxer layout tests — #521 root fix.
//
// The pre-fix muxer accumulated every frame in memory (track.samples[].data)
// and wrote the whole file at Close(): tens of MB burst writes per segment,
// concurrent Closes congesting SD/eMMC, writeFrames frozen 15-50s, ring
// overflow drops. The fix streams media bytes to the file as they arrive
// (ftyp | mdat | moov layout; Close only patches the mdat size and appends
// moov), so writes spread across the segment's lifetime.
//
// These tests pin the streaming contract:
//
//	S1 media on disk BEFORE Close — the observable the fix exists for;
//	S2 box layout ftyp|mdat|moov with patched mdat size (walkable boxes);
//	S3 interleaved A/V samples decode back with exact content via
//	   merge.ParseSegment — the #1 downstream consumer.

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/merge"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// streamFrame builds a video NAL payload of roughly sizeKB kilobytes whose
// first byte identifies it (0x65 IDR / 0x41 non-IDR) and whose tail carries
// the sample index for content verification.
func streamFrame(idr bool, idx int, sizeKB int) []byte {
	b := make([]byte, sizeKB*1024)
	if idr {
		b[0] = 0x65
	} else {
		b[0] = 0x41
	}
	b[1] = byte(idx)
	b[len(b)-1] = byte(idx)
	return b
}

// S1: media bytes must hit the file DURING the segment. 16 × 128KB frames
// (2MB total) with a bufio.Writer ≤1MB means ≥1MB is flushed before Close.
func TestStreaming_MediaOnDiskBeforeClose(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "seg.mp4")

	m := NewMP4Muxer(path)
	trackID, err := m.AddH264Track(testSPS, testPPS)
	require.NoError(t, err)

	for i := range 16 {
		require.NoError(t, m.WriteSample(trackID, streamFrame(i == 0, i, 128), time.Duration(i)*33*time.Millisecond, 33*time.Millisecond))
	}

	// BEFORE Close: the file must exist and carry most of the media.
	info, err := os.Stat(path)
	require.NoError(t, err, "media file must be created by WriteSample, not deferred to Close")
	assert.GreaterOrEqual(t, info.Size(), int64(1024*1024),
		"≥1MB of the 2MB media must already be on disk (incremental flush), got %d", info.Size())

	require.NoError(t, m.Close())
}

// S2: final layout is ftyp | mdat | moov with the mdat size field patched to
// the real value — every top-level box's declared size must walk the file
// exactly to EOF.
func TestStreaming_BoxLayoutAndMdatSizePatched(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "seg.mp4")

	m := NewMP4Muxer(path)
	trackID, err := m.AddH264Track(testSPS, testPPS)
	require.NoError(t, err)
	for i := range 4 {
		require.NoError(t, m.WriteSample(trackID, streamFrame(i == 0, i, 8), time.Duration(i)*33*time.Millisecond, 33*time.Millisecond))
	}
	require.NoError(t, m.Close())

	raw, err := os.ReadFile(path)
	require.NoError(t, err)

	var types []string
	pos := int64(0)
	for pos < int64(len(raw)) {
		require.GreaterOrEqual(t, int64(len(raw))-pos, int64(8), "truncated box header at %d", pos)
		size := int64(binary.BigEndian.Uint32(raw[pos : pos+4]))
		typ := string(raw[pos+4 : pos+8])
		require.Greater(t, size, int64(7), "box %s at %d has invalid size %d (mdat placeholder leaked?)", typ, pos, size)
		types = append(types, typ)
		pos += size
	}
	require.Equal(t, int64(len(raw)), pos, "declared box sizes must tile the file exactly")
	require.Equal(t, []string{"ftyp", "mdat", "moov"}, types, "streaming layout must be ftyp|mdat|moov")
}

// S3: interleaved video+audio samples round-trip through merge.ParseSegment
// (the rolling-merge reader) with exact content, count, and duration.
func TestStreaming_InterleavedRoundTrip(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "seg.mp4")

	m := NewMP4Muxer(path)
	vTrack, err := m.AddH264Track(testSPS, testPPS)
	require.NoError(t, err)
	aTrack, err := m.AddAudioTrack("g711", nil)
	require.NoError(t, err)

	type wantSample struct {
		size  int
		first byte
		last  byte
	}
	var wantVideo []wantSample

	// Interleave: 3 video frames × 2 audio frames each, distinctive payloads.
	for i := range 3 {
		v := streamFrame(i == 0, i, 4)
		require.NoError(t, m.WriteSample(vTrack, v, time.Duration(i)*100*time.Millisecond, 100*time.Millisecond))
		wantVideo = append(wantVideo, wantSample{first: v[0], last: v[len(v)-1], size: len(v) + 4})
		for j := range 2 {
			a := []byte{0xA0, byte(i*2 + j), 0x55, byte(i*2 + j)}
			require.NoError(t, m.WriteAudioSample(aTrack, a, time.Duration(i)*100*time.Millisecond+time.Duration(j)*20*time.Millisecond, 20*time.Millisecond))
		}
	}
	require.NoError(t, m.Close())

	info, err := merge.ParseSegment(path)
	require.NoError(t, err, "rolling-merge parser must accept the streaming layout")
	assert.Equal(t, "h264", info.Codec)
	assert.Equal(t, 3, info.SampleCount, "video samples")
	assert.InDelta(t, 300, info.TotalDuration.Milliseconds(), 5)

	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Len(t, info.Samples, 3)
	for i, s := range info.Samples {
		w := wantVideo[i]
		assert.Equal(t, uint32(w.size), s.Size, "video sample %d stsz size (incl. 4-byte prefix)", i)
		// Content at the stco-provided offset: 4-byte NAL length then payload.
		got := raw[s.Offset : s.Offset+int64(s.Size)]
		var lenBuf [4]byte
		binary.BigEndian.PutUint32(lenBuf[:], uint32(w.size-4))
		assert.True(t, bytes.HasPrefix(got, lenBuf[:]), "video sample %d must start with its NAL length prefix", i)
		assert.Equal(t, w.first, got[4], "video sample %d payload head", i)
		assert.Equal(t, w.last, got[len(got)-1], "video sample %d payload tail", i)
	}
}

// The audio track in the interleaved layout must also survive the parser
// (count via moov), and the mdat payload length must match the sum of all
// written samples (video prefixed, audio raw).
func TestStreaming_AudioOnlyLayout(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "a.mp4")

	m := NewMP4Muxer(path)
	aTrack, err := m.AddAudioTrack("g711", nil)
	require.NoError(t, err)
	for i := range 5 {
		require.NoError(t, m.WriteAudioSample(aTrack, []byte{0x01, byte(i), 0x02, byte(i)}, time.Duration(i)*20*time.Millisecond, 20*time.Millisecond))
	}
	require.NoError(t, m.Close())

	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	// ftyp + mdat + moov walk (as S2) and mdat payload == 5 × 4 bytes audio.
	var mdatPayload int64
	pos := int64(0)
	for pos < int64(len(raw)) {
		size := int64(binary.BigEndian.Uint32(raw[pos : pos+4]))
		typ := string(raw[pos+4 : pos+8])
		if typ == "mdat" {
			mdatPayload = size - 8
		}
		pos += size
	}
	require.Equal(t, int64(len(raw)), pos)
	assert.Equal(t, int64(20), mdatPayload, "audio-only mdat payload = 5 raw 4-byte samples")
}
