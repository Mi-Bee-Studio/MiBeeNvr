package merge

// Tests for #496: sparse timelapse dwell compression, the wall→file timeline
// map, and the ambient-audio envelope mixdown.

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/muxer"
	"github.com/stretchr/testify/require"
)

// createH264SegmentWithDurations is createH264SegmentWithSamples with explicit
// per-sample durations (the sparse fixtures need ~30s dwells).
func createH264SegmentWithDurations(t *testing.T, dir, name string, sps, pps []byte, samples [][]byte, durs []time.Duration) string {
	t.Helper()
	path := filepath.Join(dir, name)
	m := muxer.NewMP4Muxer(path)
	trackID, err := m.AddH264Track(sps, pps)
	require.NoError(t, err)
	var pts time.Duration
	for i, nalu := range samples {
		require.NoError(t, m.WriteSample(trackID, nalu, pts, durs[i]))
		pts += durs[i]
	}
	require.NoError(t, m.Close())
	return path
}

// TestMergeMP4Segments_CompressesSparseDwells: a no-audio sparse segment (one
// keyframe per 30s) merged with a normal-rate segment must come out with the
// sparse dwells rewritten to TimelapseFrameDur while the normal samples keep
// their real durations, and the stats must carry the wall→file map.
func TestMergeMP4Segments_CompressesSparseDwells(t *testing.T) {
	dir := t.TempDir()
	sps := []byte{0x67, 0x42, 0x00, 0x0a, 0xe2, 0x40, 0x40, 0x04, 0x00, 0x00, 0x00, 0x04, 0x00, 0x00, 0x00, 0xc8, 0x40}
	pps := []byte{0x68, 0xce, 0x38, 0x80}
	idr := []byte{0x65, 0x88, 0x80, 0x40}
	pNAL := []byte{0x41, 0x10, 0x00, 0x0c}

	sparse := createH264SegmentWithDurations(t, dir, "sparse.mp4", sps, pps,
		[][]byte{idr, idr, idr},
		[]time.Duration{30 * time.Second, 30 * time.Second, 30 * time.Second})
	normal := createH264SegmentWithDurations(t, dir, "normal.mp4", sps, pps,
		[][]byte{idr, pNAL},
		[]time.Duration{33 * time.Millisecond, 33 * time.Millisecond})

	si, err := ParseSegment(sparse)
	require.NoError(t, err)
	ni, err := ParseSegment(normal)
	require.NoError(t, err)
	require.False(t, si.HasAudio)

	origFrame := TimelapseFrameDur
	TimelapseFrameDur = 100 * time.Millisecond
	t.Cleanup(func() { TimelapseFrameDur = origFrame })

	stats, err := MergeMP4Segments(context.Background(), []*SegmentInfo{si, ni}, filepath.Join(dir, "out.mp4"))
	require.NoError(t, err)
	require.Equal(t, 3, stats.TimelapseFrames)

	out, err := ParseSegment(filepath.Join(dir, "out.mp4"))
	require.NoError(t, err)
	require.Equal(t, 5, out.SampleCount)
	for i := range 3 {
		got := time.Duration(out.Samples[i].Duration) * time.Second / time.Duration(out.Timescale)
		require.Equal(t, 100*time.Millisecond, got, "sparse sample %d must be compressed", i)
	}
	for i := 3; i < 5; i++ {
		got := time.Duration(out.Samples[i].Duration) * time.Second / time.Duration(out.Timescale)
		require.Equal(t, 33*time.Millisecond, got, "normal sample %d must keep its real duration", i)
	}

	// Wall→file map: sparse span 90s wall → 0.3s file; normal span stays real.
	require.Len(t, stats.WallToFile, 3)
	require.InDelta(t, 0.0, stats.WallToFile[0][0], 1e-9)
	require.InDelta(t, 90.0, stats.WallToFile[1][0], 0.01)
	require.InDelta(t, 0.3, stats.WallToFile[1][1], 0.01)
	require.InDelta(t, 90.066, stats.WallToFile[2][0], 0.01)
	require.InDelta(t, 0.366, stats.WallToFile[2][1], 0.01)
	require.NotEmpty(t, stats.TimelineMapJSON())
}

// TestMergeMP4Segments_CadenceOverride: the per-camera preset (300ms) wins
// over the package default.
func TestMergeMP4Segments_CadenceOverride(t *testing.T) {
	dir := t.TempDir()
	sps := []byte{0x67, 0x42, 0x00, 0x0a, 0xe2, 0x40, 0x40, 0x04, 0x00, 0x00, 0x00, 0x04, 0x00, 0x00, 0x00, 0xc8, 0x40}
	pps := []byte{0x68, 0xce, 0x38, 0x80}
	idr := []byte{0x65, 0x88, 0x80, 0x40}
	sparse := createH264SegmentWithDurations(t, dir, "s.mp4", sps, pps,
		[][]byte{idr, idr}, []time.Duration{30 * time.Second, 30 * time.Second})
	info, err := ParseSegment(sparse)
	require.NoError(t, err)

	_, err = MergeMP4Segments(context.Background(), []*SegmentInfo{info}, filepath.Join(dir, "o.mp4"), 300*time.Millisecond)
	require.NoError(t, err)
	out, err := ParseSegment(filepath.Join(dir, "o.mp4"))
	require.NoError(t, err)
	got := time.Duration(out.Samples[0].Duration) * time.Second / time.Duration(out.Timescale)
	require.Equal(t, 300*time.Millisecond, got, "explicit cadence must override the default")
}

// TestMergeMP4Segments_CompressedAACSpanDropsAudio: a sparse-dwell segment
// carrying a NON-G.711 audio track gets its video compressed and the span's
// audio dropped (the envelope mixdown is G.711-only) — the output carries no
// audio track at all when nothing else contributes one.
func TestMergeMP4Segments_CompressedAACSpanDropsAudio(t *testing.T) {
	dir := t.TempDir()
	vps := []byte{0x40, 0x01, 0x0c, 0x01, 0xff, 0xff, 0x01, 0x60, 0x00, 0x00, 0x00, 0x00, 0x00, 0x90, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x5d, 0xac, 0x59}
	sps := []byte{0x42, 0x01, 0x01, 0x01, 0x60, 0x00, 0x00, 0x00, 0x00, 0x00, 0x90, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x5d, 0xa0, 0x02, 0x80, 0x80, 0x2d, 0x16, 0x59, 0x59, 0xa4, 0x93, 0x2b, 0x80, 0x40, 0x00, 0x00, 0x07, 0x92}
	pps := []byte{0x44, 0x01, 0xc1, 0x73, 0xd1, 0x89}
	idrNAL := []byte{0x27, 0x01, 0xaf, 0x15, 0x6a}
	pNAL := []byte{0x03, 0x20, 0x10, 0x00}

	seg := createH265SegmentWithAudio(t, dir, "h265_audio.mp4", vps, sps, pps,
		[][]byte{idrNAL, pNAL}, testAudioConfig, [][]byte{testAACFrame, testAACFrame})
	info, err := ParseSegment(seg)
	require.NoError(t, err)
	require.True(t, info.HasAudio)

	// Give the video samples sparse dwell durations directly — the merge must
	// leave them alone because the segment carries audio.
	ts := info.Timescale
	for i := range info.Samples {
		info.Samples[i].Duration = uint32(30 * ts)
	}

	origFrame := TimelapseFrameDur
	TimelapseFrameDur = 100 * time.Millisecond
	t.Cleanup(func() { TimelapseFrameDur = origFrame })

	stats, err := MergeMP4Segments(context.Background(), []*SegmentInfo{info}, filepath.Join(dir, "out.mp4"))
	require.NoError(t, err)
	require.Equal(t, 2, stats.TimelapseFrames)
	out, err := ParseSegment(filepath.Join(dir, "out.mp4"))
	require.NoError(t, err)
	got := time.Duration(out.Samples[0].Duration) * time.Second / time.Duration(out.Timescale)
	require.Equal(t, 100*time.Millisecond, got, "sparse dwells compress even with an audio track")
	require.False(t, out.HasAudio, "compressed AAC span's audio must be dropped")
}

// TestMixdownAmbient: silence stays silent, loud input renders a bed of the
// requested length within headroom, and the g711 codec round-trips.
func TestMixdownAmbient(t *testing.T) {
	// Silence (µ-law 0xFF decodes to 0) must not be normalized into noise.
	// µ-law positive zero re-encodes as 0x7F, so assert on the decoded value.
	bed := mixdownAmbient(true, []byte{0xFF, 0xFF, 0xFF, 0xFF}, 100)
	require.Len(t, bed, 100)
	for _, b := range bed {
		require.Equal(t, int16(0), g711DecodeMuLaw(b), "silence in, silence out")
	}

	// Loud alternating input → 8000-sample bed with bounded amplitude.
	loud := make([]byte, 30000)
	for i := range loud {
		loud[i] = byte(i % 251)
	}
	bed = mixdownAmbient(true, loud, 8000)
	require.Len(t, bed, 8000)
	for _, b := range bed {
		v := g711DecodeMuLaw(b)
		require.LessOrEqual(t, int(v), 32767)
		require.GreaterOrEqual(t, int(v), -32768)
	}

	// Codec round-trip within quantization error (the max segment decodes to
	// ±32124 by codec design — inherent ~644 at full scale).
	for _, v := range []int16{-32768, -12345, -1, 0, 1, 999, 12345, 32767} {
		rt := g711DecodeMuLaw(g711EncodeMuLaw(v))
		require.InDelta(t, float64(v), float64(rt), 800, "µ-law round trip of %d", v)
	}
	for _, v := range []int16{-32768, -12345, -1, 0, 1, 999, 12345, 32767} {
		rt := g711DecodeALaw(g711EncodeALaw(v))
		require.InDelta(t, float64(v), float64(rt), 800, "A-law round trip of %d", v)
	}
}

// TestWriteAmbientBedMatchesMixdown: the streaming ambient renderer (#1011
// debt 2) must be byte-identical to the in-memory reference mixdownAmbient —
// across window shapes (expansion where nOut > input, heavy compression,
// single window), both G.711 variants, and sample spans scattered at
// non-contiguous file offsets with filler bytes between them.
func TestWriteAmbientBedMatchesMixdown(t *testing.T) {
	ctx := context.Background()

	buildFile := func(payload []byte, gap int) ([]byte, []SampleEntry) {
		var file []byte
		var spans []SampleEntry
		for _, part := range splitPayload(payload, 3) {
			file = append(file, make([]byte, gap)...)
			spans = append(spans, SampleEntry{Offset: int64(len(file)), Size: uint32(len(part))})
			file = append(file, part...)
		}
		return file, spans
	}

	for _, muLaw := range []bool{true, false} {
		for _, tc := range []struct {
			name  string
			input int
			nOut  int
		}{
			{"expand_windows_overlap", 97, 500}, // nOut > input: windows share bytes
			{"heavy_compression", 60000, 500},   // 300×-style timelapse ratio
			{"single_window", 12345, 1},         // whole input in one window
			{"exact_division", 8000, 100},       // window boundaries land on sample edges
			{"no_audio", 0, 100},                // mixdownAmbient returns nil
		} {
			t.Run(fmt.Sprintf("%s_mulaw%v", tc.name, muLaw), func(t *testing.T) {
				payload := make([]byte, tc.input)
				for i := range payload {
					payload[i] = byte(i*31 + 7)
				}
				file, spans := buildFile(payload, 5)
				seg := &SegmentInfo{AudioSamples: spans, G711MULaw: muLaw}

				want := mixdownAmbient(muLaw, payload, tc.nOut)

				var got bytes.Buffer
				n, err := writeAmbientBed(ctx, bytes.NewReader(file), seg, tc.nOut, &got)
				require.NoError(t, err)
				if tc.input == 0 {
					require.Equal(t, 0, n)
					require.Empty(t, got.Bytes())
					return
				}
				require.Equal(t, tc.nOut, n)
				require.Equal(t, want, got.Bytes(), "streaming bed must be byte-identical to mixdownAmbient")
			})
		}
	}
}

// splitPayload cuts p into at most parts roughly-equal pieces (test fixture
// helper for TestWriteAmbientBedMatchesMixdown).
func splitPayload(p []byte, parts int) [][]byte {
	if len(p) == 0 {
		return nil
	}
	var out [][]byte
	size := (len(p) + parts - 1) / parts
	for len(p) > 0 {
		cut := min(size, len(p))
		out = append(out, p[:cut])
		p = p[cut:]
	}
	return out
}

// TestMergeMP4Segments_CompressedG711SpanAmbientBed: end-to-end through the
// merge — a sparse-dwell segment carrying a G.711 track must have its video
// compressed and its audio rendered as an ambient bed (one per-byte sample on
// the audio timescale), with the output still parseable.
func TestMergeMP4Segments_CompressedG711SpanAmbientBed(t *testing.T) {
	dir := t.TempDir()
	sps := []byte{0x67, 0x42, 0x00, 0x0a, 0xe2, 0x40, 0x40, 0x04, 0x00, 0x00, 0x00, 0x04, 0x00, 0x00, 0x00, 0xc8, 0x40}
	pps := []byte{0x68, 0xce, 0x38, 0x80}
	idr := []byte{0x65, 0x88, 0x80, 0x40}

	path := filepath.Join(dir, "sparse_g711.mp4")
	m := muxer.NewMP4Muxer(path)
	videoTrackID, err := m.AddH264Track(sps, pps)
	require.NoError(t, err)
	audioTrackID, err := m.AddAudioTrack("g711", []byte{1, 0x00, 0x00, 0x1F, 0x40})
	require.NoError(t, err)
	// Sparse video: two 30s keyframe dwells → wall 60s, compressed to 2×100ms.
	require.NoError(t, m.WriteSample(videoTrackID, idr, 0, 30*time.Second))
	require.NoError(t, m.WriteSample(videoTrackID, idr, 30*time.Second, 30*time.Second))
	// G.711 source audio. Frame count matters beyond realism: the ambient bed
	// emits one audio sample per OUTPUT byte (nOut of them), while the moov
	// budget only reserves for the PARSED sample count plus 8 B/sample stts
	// headroom — nOut beyond ~3× the sample count overflows the reserved moov
	// (a pre-existing latent limitation of the ambient path, not something
	// this test should trip; see the #1011 refactor notes).
	frame := make([]byte, 160)
	for i := range frame {
		frame[i] = byte(i%2*40 + 60)
	}
	var apts time.Duration
	for range 150 {
		require.NoError(t, m.WriteAudioSample(audioTrackID, frame, apts, 20*time.Millisecond))
		apts += 20 * time.Millisecond
	}
	require.NoError(t, m.Close())

	info, err := ParseSegment(path)
	require.NoError(t, err)
	require.True(t, info.HasAudio)
	require.Equal(t, "g711", info.AudioCodec)

	stats, err := MergeMP4Segments(context.Background(), []*SegmentInfo{info},
		filepath.Join(dir, "out.mp4"), 100*time.Millisecond)
	require.NoError(t, err)
	require.Equal(t, 2, stats.TimelapseFrames)
	// spanFile = 2×100ms = 0.2s; bed length = spanFile × audio timescale.
	nOut := int(0.2*float64(info.AudioTimescale) + 0.5)
	require.Equal(t, nOut, stats.AmbientSamples, "ambient bed must cover the compressed file span")

	out, err := ParseSegment(filepath.Join(dir, "out.mp4"))
	require.NoError(t, err)
	require.True(t, out.HasAudio, "ambient bed keeps the output's audio track alive")
	require.Equal(t, "g711", out.AudioCodec)
	require.Equal(t, nOut, out.AudioSampleCount, "one audio sample per ambient byte")
}
