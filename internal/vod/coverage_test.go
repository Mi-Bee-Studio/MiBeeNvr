package vod

// Coverage backfill (#611 weak-area list): H.265 init segments, the three
// audio sample entries (AAC/G.711/Opus), AAC config parsing, sidecar
// round-trip + staleness, the playlist accessors, and the H.265 probe-oracle
// keyframe branch. Fixtures reuse the production muxer like vod_test.go.

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/abema/go-mp4"
	"github.com/stretchr/testify/require"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/merge"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/muxer"
)

// Minimal H.265 parameter sets (same lineage as internal/merge's fixtures).
var (
	testVPS     = []byte{0x40, 0x01, 0x0c, 0x01, 0xff, 0xff, 0x01, 0x60, 0x00, 0x00, 0x00, 0x00, 0x00, 0x90, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x5d, 0xac, 0x59}
	testH265SPS = []byte{0x42, 0x01, 0x01, 0x01, 0x60, 0x00, 0x00, 0x00, 0x00, 0x00, 0x90, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x5d, 0xa0, 0x02, 0x80, 0x80, 0x2d, 0x16, 0x59, 0x59, 0xa4, 0x93, 0x2b, 0x80, 0x40, 0x00, 0x00, 0x07, 0x92}
	testH265PPS = []byte{0x44, 0x01, 0xc1, 0x73, 0xd1, 0x89}
)

// buildTestH265MP4 writes an H.265 MP4 with IDR_W_RADL (type 19) every
// gopSize samples, TRAIL_R (type 1) in between.
func buildTestH265MP4(t *testing.T, dir, name string, sampleCount, gopSize int) string {
	t.Helper()
	path := filepath.Join(dir, name)
	m := muxer.NewMP4Muxer(path)
	trackID, err := m.AddH265Track(testVPS, testH265SPS, testH265PPS)
	require.NoError(t, err)
	for i := range sampleCount {
		hdr := byte(0x03) // TRAIL_R: (1<<1)|1
		if i%gopSize == 0 {
			hdr = 0x27 // IDR_W_RADL: (19<<1)|1
		}
		nalu := append([]byte{hdr, 0x9A, 0x02, 0x05}, bytes.Repeat([]byte{byte(i)}, 24)...)
		require.NoError(t, m.WriteSample(trackID, nalu, time.Duration(i)*33*time.Millisecond, 33*time.Millisecond))
	}
	require.NoError(t, m.Close())
	return path
}

// buildTestMP4WithAudio writes a small H.264 MP4 plus one audio track.
func buildTestMP4WithAudio(t *testing.T, dir, name, audioCodec string, audioConfig []byte) string {
	t.Helper()
	path := filepath.Join(dir, name)
	m := muxer.NewMP4Muxer(path)
	videoTrack, err := m.AddH264Track(testSPS, testPPS)
	require.NoError(t, err)
	audioTrack, err := m.AddAudioTrack(audioCodec, audioConfig)
	require.NoError(t, err)
	for i := range 6 {
		hdr := byte(0x41)
		if i == 0 {
			hdr = 0x65
		}
		nalu := append([]byte{hdr, 0x9A, 0x02, 0x05}, bytes.Repeat([]byte{byte(i)}, 16)...)
		require.NoError(t, m.WriteSample(videoTrack, nalu, time.Duration(i)*33*time.Millisecond, 33*time.Millisecond))
	}
	for i := range 6 {
		require.NoError(t, m.WriteAudioSample(audioTrack, []byte{0x55, 0xAA}, time.Duration(i)*20*time.Millisecond, 20*time.Millisecond))
	}
	require.NoError(t, m.Close())
	return path
}

// collectBoxes walks an init segment and returns every box type string in
// traversal order, expanding the container boxes on the way down.
func collectBoxes(t *testing.T, data []byte) []string {
	t.Helper()
	var types []string
	_, err := mp4.ReadBoxStructure(bytes.NewReader(data), func(h *mp4.ReadHandle) (interface{}, error) {
		bt := h.BoxInfo.Type.String()
		types = append(types, bt)
		switch bt {
		case "moov", "trak", "mdia", "minf", "stbl", "stsd", "mvex",
			"avc1", "hvc1", "mp4a", "Opus":
			return h.Expand()
		}
		return nil, nil
	})
	require.NoError(t, err)
	return types
}

func TestBuildInitSegment_H265(t *testing.T) {
	dir := t.TempDir()
	path := buildTestH265MP4(t, dir, "h265.mp4", 5, 5)
	info, err := merge.ParseSegment(path)
	require.NoError(t, err)
	require.Equal(t, "h265", info.Codec)
	require.NotEmpty(t, info.VPS)

	initSeg, err := BuildInitSegment(info, false)
	require.NoError(t, err)

	joined := strings.Join(collectBoxes(t, initSeg), ",")
	require.Contains(t, joined, "hvc1")
	require.Contains(t, joined, "hvcC")
}

func TestBuildInitSegment_AudioEntries(t *testing.T) {
	cases := []struct {
		name       string
		audioCodec string
		audioCfg   []byte
		wantBoxes  []string
	}{
		{"g711 mulaw", "g711", []byte{1, 0x00, 0x00, 0x1F, 0x40}, []string{"ulaw"}},
		{"g711 alaw", "g711", []byte{0, 0x00, 0x00, 0x1F, 0x40}, []string{"alaw"}},
		{"opus", "opus", []byte{1, 0x00, 0x00, 0xBB, 0x80, 0x00, 0x00}, []string{"Opus", "dOps"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			path := buildTestMP4WithAudio(t, dir, "seg.mp4", c.audioCodec, c.audioCfg)
			info, err := merge.ParseSegment(path)
			require.NoError(t, err)
			require.True(t, info.HasAudio)
			require.Equal(t, c.audioCodec, info.AudioCodec)

			initSeg, err := BuildInitSegment(info, true)
			require.NoError(t, err)

			joined := strings.Join(collectBoxes(t, initSeg), ",")
			for _, want := range c.wantBoxes {
				require.Contains(t, joined, want)
			}
			// Two traks (video + audio) and both trex entries.
			require.Equal(t, 2, strings.Count(joined, "trak"))
			require.Equal(t, 2, strings.Count(joined, "trex"))
			require.Contains(t, joined, "smhd")
		})
	}
}

// TestBuildInitSegment_AAC: the legacy-default AAC path (AudioCodec "" with
// an AudioSpecificConfig) — built from a hand-filled SegmentInfo because the
// production muxer fixture does not round-trip an esds codec label.
func TestBuildInitSegment_AAC(t *testing.T) {
	dir := t.TempDir()
	info := parseInfo(t, buildTestMP4(t, dir, "seg.mp4", 5, 5))
	info.HasAudio = true
	info.AudioCodec = "" // legacy default = AAC
	info.AudioConfig = []byte{0x12, 0x10}
	info.AudioTimescale = 44100

	initSeg, err := BuildInitSegment(info, true)
	require.NoError(t, err)
	joined := strings.Join(collectBoxes(t, initSeg), ",")
	require.Contains(t, joined, "mp4a")
	require.Contains(t, joined, "esds")
	require.Equal(t, 2, strings.Count(joined, "trak"))
}

func TestBuildInitSegment_Errors(t *testing.T) {
	_, err := BuildInitSegment(&merge.SegmentInfo{Timescale: 1000, Codec: "mjpeg"}, false)
	require.ErrorContains(t, err, "unsupported codec")

	_, err = BuildInitSegment(&merge.SegmentInfo{Codec: "h264"}, false)
	require.ErrorContains(t, err, "no timescale")

	// H.264 sample entry needs real parameter sets.
	_, err = BuildInitSegment(&merge.SegmentInfo{
		Timescale: 1000, Codec: "h264",
		SPS: []byte{0x67, 0x42}, PPS: []byte{0x68},
	}, false)
	require.ErrorIs(t, err, errShortParamSets)
}

func TestParseAACAudioConfig(t *testing.T) {
	cases := []struct {
		config []byte
		ch     uint16
		rate   uint32
	}{
		{nil, 2, 44100},                // defaults
		{[]byte{0x12, 0x10}, 2, 64000}, // index 2, channel config 0 → default 2
		{[]byte{0x20, 0x40}, 1, 44100}, // index 4, channel config 1
		{[]byte{0x79, 0x00}, 4, 44100}, // index 15 (explicit rate, unparserd) → default rate
	}
	for _, c := range cases {
		ch, rate := parseAACAudioConfig(c.config)
		require.Equal(t, c.ch, ch, "config=%v", c.config)
		require.Equal(t, c.rate, rate, "config=%v", c.config)
	}
}

func TestSidecar_RoundtripAndStale(t *testing.T) {
	dir := t.TempDir()
	path := buildTestMP4(t, dir, "seg.mp4", 10, 5)

	frags := []Fragment{
		{First: 0, End: 5, StartUnits: 0, DurationUnits: 165},
		{First: 5, End: 10, StartUnits: 165, DurationUnits: 165},
	}
	writeSidecar(path, frags, 1000)

	got, ts, ok := readSidecar(path)
	require.True(t, ok)
	require.Equal(t, uint32(1000), ts)
	require.Equal(t, frags, got)

	// Stale: identity mismatch after a size change.
	staleRec := buildTestMP4(t, dir, "stale.mp4", 5, 5)
	writeSidecar(staleRec, frags, 1000) // identity recorded for this file
	_, _, ok = readSidecar(staleRec)
	require.True(t, ok) // identity still matches (nothing changed yet)
	require.NoError(t, os.WriteFile(staleRec, []byte("replaced by a re-merge"), 0o644))
	_, _, ok = readSidecar(staleRec)
	require.False(t, ok, "size change must invalidate the sidecar")

	// Corrupt JSON / wrong version / empty frags.
	sc := sidecarPath(path)
	raw, err := os.ReadFile(sc)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(sc, []byte("{not json"), 0o644))
	_, _, ok = readSidecar(path)
	require.False(t, ok)

	var d sidecarData
	require.NoError(t, json.Unmarshal(raw, &d))
	d.Version = 1
	bad, err := json.Marshal(&d)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(sc, bad, 0o644))
	_, _, ok = readSidecar(path)
	require.False(t, ok)

	d.Version = sidecarVersion
	d.Frags = nil
	bad, err = json.Marshal(&d)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(sc, bad, 0o644))
	_, _, ok = readSidecar(path)
	require.False(t, ok)

	// Missing sidecar file.
	_, _, ok = readSidecar(filepath.Join(dir, "nope.mp4"))
	require.False(t, ok)
}

func TestManagerAccessors(t *testing.T) {
	dir := t.TempDir()
	path := buildTestMP4(t, dir, "seg.mp4", 5, 5)
	fi := fileInfo(t, path)
	rec := model.Recording{
		ID: "r1", CameraID: "c1", FilePath: path, FileSize: fi.Size(), Format: model.FormatH264,
		StartedAt: time.Now().UTC().Add(-time.Hour), EndedAt: time.Now().UTC(),
	}

	m := NewManager()
	require.Equal(t, float64(TargetFragmentDur), m.TargetSec())

	info, err := m.GetInfo(rec)
	require.NoError(t, err)
	require.Equal(t, 5, info.SampleCount)

	// GetInfo on a vanished file surfaces the parse error.
	gone := rec
	gone.FilePath = filepath.Join(dir, "gone.mp4")
	_, err = m.GetInfo(gone)
	require.Error(t, err)
}

// TestProbeOracle_H265 exercises the H.265 branch of the stss-less keyframe
// oracle (NAL types 16-21 are keyframes; IDR_W_RADL=19 in the fixture).
func TestProbeOracle_H265(t *testing.T) {
	dir := t.TempDir()
	path := buildTestH265MP4(t, dir, "h265.mp4", 20, 5)
	info, err := merge.ParseSegmentNoProbe(path)
	require.NoError(t, err)

	f, err := os.Open(path)
	require.NoError(t, err)
	defer f.Close()
	o := newProbeOracle(f, info)

	for _, want := range []int{0, 5, 10, 15} {
		got, ok := o.nextAtOrAfter(want)
		require.True(t, ok, "no keyframe from %d", want)
		require.Equal(t, want, got, "must land exactly on the IDR")
	}
	_, ok := o.nextAtOrAfter(16)
	require.False(t, ok, "no keyframe after the last IDR")
}
