package vod

import (
	"bytes"
	"encoding/binary"
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

var testSPS = []byte{0x67, 0x42, 0x00, 0x0a, 0xe2, 0x40, 0x40, 0x04, 0x00, 0x00, 0x00, 0x04, 0x00, 0x00, 0x00, 0xc8, 0x40}
var testPPS = []byte{0x68, 0xCB, 0x83, 0xCB, 0x20}

// buildTestMP4 writes a small H.264 MP4 via the production muxer: nalu frames
// alternating IDR (0x65) / non-IDR (0x41) headers with a keyframe every
// gopSize samples. Returns the file path.
func buildTestMP4(t *testing.T, dir, name string, sampleCount, gopSize int) string {
	t.Helper()
	path := filepath.Join(dir, name)
	m := muxer.NewMP4Muxer(path)
	trackID, err := m.AddH264Track(testSPS, testPPS)
	require.NoError(t, err)
	for i := 0; i < sampleCount; i++ {
		hdr := byte(0x41) // non-IDR
		if i%gopSize == 0 {
			hdr = 0x65 // IDR
		}
		nalu := append([]byte{hdr, 0x9A, 0x02, 0x05}, bytes.Repeat([]byte{byte(i)}, 24)...)
		pts := time.Duration(i) * 33 * time.Millisecond
		require.NoError(t, m.WriteSample(trackID, nalu, pts, 33*time.Millisecond))
	}
	require.NoError(t, m.Close())
	return path
}

func parseInfo(t *testing.T, path string) *merge.SegmentInfo {
	t.Helper()
	info, err := merge.ParseSegment(path)
	require.NoError(t, err)
	require.NotZero(t, info.Timescale)
	require.Equal(t, "h264", info.Codec)
	return info
}

func TestPlanFragments_KeyframeAligned(t *testing.T) {
	dir := t.TempDir()
	path := buildTestMP4(t, dir, "seg.mp4", 20, 5)
	info := parseInfo(t, path)
	require.Equal(t, 20, info.SampleCount)
	// Keyframes at 0,5,10,15.
	require.True(t, info.Samples[0].IsKeyFrame)
	require.False(t, info.Samples[1].IsKeyFrame)
	require.True(t, info.Samples[5].IsKeyFrame)

	// Target 100ms @33ms/sample → cut at the first keyframe with ≥100ms
	// accumulated → fragments [0,5),[5,10),[10,15),[15,20).
	frags := PlanFragments(info, 0.1, stssOracle{samples: info.Samples})
	require.Len(t, frags, 4)
	for i, f := range frags {
		require.Equal(t, 5*i, f.First)
		require.Equal(t, 5*(i+1), f.End)
	}
	// Cumulative units must stay consistent.
	var sum uint64
	for _, f := range frags {
		require.Equal(t, sum, f.StartUnits)
		sum += f.DurationUnits
	}
	var total uint64
	for _, s := range info.Samples {
		total += uint64(s.Duration)
	}
	require.Equal(t, total, sum)
}

func TestBuildFragment_BoxRoundtrip(t *testing.T) {
	dir := t.TempDir()
	path := buildTestMP4(t, dir, "seg.mp4", 10, 5)
	info := parseInfo(t, path)

	frags := PlanFragments(info, 0.1, stssOracle{samples: info.Samples})
	require.NotEmpty(t, frags)
	f := frags[0]

	data, err := BuildFragment(info, f, 7, false)
	require.NoError(t, err)
	require.NotEmpty(t, data.Moof)

	// Parse the moof back and verify structure + offsets.
	var (
		gotMfhd   *mp4.Mfhd
		gotTraf   int
		gotTfhd   []*mp4.Tfhd
		gotTfdt   []*mp4.Tfdt
		gotTrun   []*mp4.Trun
	)
	_, err = mp4.ReadBoxStructure(bytes.NewReader(data.Moof), func(h *mp4.ReadHandle) (interface{}, error) {
		switch h.BoxInfo.Type.String() {
		case "moof":
			return h.Expand()
		case "mfhd":
			b, _, err := h.ReadPayload()
			if err != nil {
				return nil, err
			}
			gotMfhd = b.(*mp4.Mfhd)
		case "traf":
			gotTraf++
			return h.Expand()
		case "tfhd":
			b, _, err := h.ReadPayload()
			if err != nil {
				return nil, err
			}
			gotTfhd = append(gotTfhd, b.(*mp4.Tfhd))
		case "tfdt":
			b, _, err := h.ReadPayload()
			if err != nil {
				return nil, err
			}
			gotTfdt = append(gotTfdt, b.(*mp4.Tfdt))
		case "trun":
			b, _, err := h.ReadPayload()
			if err != nil {
				return nil, err
			}
			gotTrun = append(gotTrun, b.(*mp4.Trun))
		}
		return nil, nil
	})
	require.NoError(t, err)
	require.NotNil(t, gotMfhd)
	require.Equal(t, uint32(7), gotMfhd.SequenceNumber)
	require.Equal(t, 1, gotTraf) // no audio in fixture
	require.Len(t, gotTrun, 1)

	require.Len(t, gotTfhd, 1)
	require.Equal(t, uint32(1), gotTfhd[0].TrackID)
	require.NotZero(t, gotTfhd[0].GetFlags()&mp4.TfhdDefaultBaseIsMoof)

	require.Len(t, gotTfdt, 1)
	require.Equal(t, uint64(0), gotTfdt[0].GetBaseMediaDecodeTime()) // first fragment

	trun := gotTrun[0]
	require.Equal(t, uint32(f.End-f.First), trun.SampleCount)
	require.Equal(t, int32(len(data.Moof))+8, trun.DataOffset)
	require.Len(t, trun.Entries, f.End-f.First)
	// First sample is a keyframe → sync sample flags.
	require.Equal(t, uint32(sampleFlagsKeyframe), trun.Entries[0].SampleFlags)
	require.Equal(t, uint32(sampleFlagsOther), trun.Entries[1].SampleFlags)

	// Assemble the full segment exactly like the HTTP handler and verify the
	// data_offset really lands on the first sample byte in mdat.
	var seg bytes.Buffer
	seg.Write(data.Moof)
	var mdatHeader [8]byte
	mdatBytes := data.TotalBytes - int64(len(data.Moof)) - 8
	binary.BigEndian.PutUint32(mdatHeader[0:4], uint32(mdatBytes+8))
	copy(mdatHeader[4:8], "mdat")
	seg.Write(mdatHeader[:])
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	for _, ranges := range [][]ByteRange{data.VideoRanges, data.AudioRanges} {
		for _, br := range ranges {
			seg.Write(raw[br.Offset : br.Offset+br.Size])
		}
	}
	require.Equal(t, data.TotalBytes, int64(seg.Len()))

	// data_offset points at the first sample's 4-byte NAL length prefix.
	firstOff := trun.DataOffset
	nalLen := binary.BigEndian.Uint32(seg.Bytes()[firstOff : firstOff+4])
	require.Equal(t, info.Samples[f.First].Size-4, nalLen)
	require.Equal(t, byte(0x65), seg.Bytes()[firstOff+4]) // IDR NAL header

	// Byte accounting: the trun sample sizes sum to the copied video bytes.
	var trunBytes uint32
	for _, e := range trun.Entries {
		trunBytes += e.SampleSize
	}
	var rangeBytes int64
	for _, br := range data.VideoRanges {
		rangeBytes += br.Size
	}
	require.Equal(t, int64(trunBytes), rangeBytes)
}

func TestBuildFragment_SecondFragmentTfdt(t *testing.T) {
	dir := t.TempDir()
	info := parseInfo(t, buildTestMP4(t, dir, "seg.mp4", 10, 5))
	frags := PlanFragments(info, 0.1, stssOracle{samples: info.Samples})
	require.Len(t, frags, 2)

	data, err := BuildFragment(info, frags[1], 8, false)
	require.NoError(t, err)
	var baseTime uint64
	_, err = mp4.ReadBoxStructure(bytes.NewReader(data.Moof), func(h *mp4.ReadHandle) (interface{}, error) {
		switch h.BoxInfo.Type.String() {
		case "moof", "traf":
			return h.Expand()
		case "tfdt":
			b, _, err := h.ReadPayload()
			if err != nil {
				return nil, err
			}
			baseTime = b.(*mp4.Tfdt).GetBaseMediaDecodeTime()
		}
		return nil, nil
	})
	require.NoError(t, err)
	require.Equal(t, frags[1].StartUnits, baseTime)
}

func TestBuildFragment_BadRange(t *testing.T) {
	dir := t.TempDir()
	info := parseInfo(t, buildTestMP4(t, dir, "seg.mp4", 10, 5))
	_, err := BuildFragment(info, Fragment{First: 8, End: 3}, 1, false)
	require.Error(t, err)
	_, err = BuildFragment(info, Fragment{First: 0, End: 999}, 1, false)
	require.Error(t, err)
}

func TestBuildInitSegment_ParsesBack(t *testing.T) {
	dir := t.TempDir()
	info := parseInfo(t, buildTestMP4(t, dir, "seg.mp4", 5, 5))

	initSeg, err := BuildInitSegment(info, false)
	require.NoError(t, err)

	var (
		boxTypes []string
		trakSeen int
	)
	_, err = mp4.ReadBoxStructure(bytes.NewReader(initSeg), func(h *mp4.ReadHandle) (interface{}, error) {
		bt := h.BoxInfo.Type.String()
		boxTypes = append(boxTypes, bt)
		switch bt {
		case "trak":
			trakSeen++
			return h.Expand()
		case "stsd", "stbl", "moov", "mdia", "minf", "avc1", "hvc1":
			return h.Expand()
		}
		return nil, nil
	})
	require.NoError(t, err)
	require.Equal(t, 1, trakSeen) // video only (fixture has no audio)
	joined := strings.Join(boxTypes, ",")
	require.Contains(t, joined, "ftyp")
	require.Contains(t, joined, "moov")
	require.Contains(t, joined, "mvhd")
	require.Contains(t, joined, "stsd")
	require.Contains(t, joined, "mvex")
	require.Contains(t, joined, "avc1")
	require.Contains(t, joined, "avcC")
}

func TestIncludeAudio(t *testing.T) {
	require.False(t, IncludeAudio(&merge.SegmentInfo{}))
	require.False(t, IncludeAudio(&merge.SegmentInfo{HasAudio: true, AudioCodec: "g711"}))
	require.True(t, IncludeAudio(&merge.SegmentInfo{HasAudio: true, AudioCodec: "aac"}))
	require.True(t, IncludeAudio(&merge.SegmentInfo{HasAudio: true, AudioCodec: "opus"}))
}

// TestProbeOracle_GOPPrediction exercises the stss-less path: the oracle must
// find the first boundary with a linear scan, learn the GOP period, and then
// land EXACTLY on keyframe positions via prediction (fixture: IDR every 50).
func TestProbeOracle_GOPPrediction(t *testing.T) {
	dir := t.TempDir()
	path := buildTestMP4(t, dir, "seg.mp4", 500, 50)
	info, err := merge.ParseSegmentNoProbe(path)
	require.NoError(t, err)
	require.False(t, info.KeyframesFromStss)

	f, err := os.Open(path)
	require.NoError(t, err)
	defer f.Close()
	o := newProbeOracle(f, info)

	for _, want := range []int{0, 50, 100, 150, 200, 250, 300, 350, 400, 450} {
		got, ok := o.nextAtOrAfter(want)
		require.True(t, ok, "no keyframe found from %d", want)
		require.Equal(t, want, got, "boundary must land exactly on the keyframe")
	}
	// Beyond the last keyframe: not found.
	_, ok := o.nextAtOrAfter(451)
	require.False(t, ok)
}

// TestFragments_ViaManager_NoStss runs the full manager path on a NoProbe
// parse (probe oracle internally) and verifies keyframe-aligned boundaries.
func TestFragments_ViaManager_NoStss(t *testing.T) {
	dir := t.TempDir()
	path := buildTestMP4(t, dir, "seg.mp4", 20, 5)
	fi := fileInfo(t, path)
	m := NewManager()
	rec := model.Recording{ID: "r1", CameraID: "c1", FilePath: path, FileSize: fi.Size(), Format: model.FormatH264,
		StartedAt: time.Now().UTC().Add(-time.Hour), EndedAt: time.Now().UTC()}

	frags, ts, err := m.Fragments(rec)
	require.NoError(t, err)
	require.NotZero(t, ts)
	require.NotEmpty(t, frags)
	// Boundaries at the learned keyframe positions (0,5,10,15).
	require.Equal(t, 0, frags[0].First)
	for i := 1; i < len(frags); i++ {
		require.Equal(t, frags[i-1].End, frags[i].First)
		require.Equal(t, 0, frags[i].First%5)
	}
	require.Equal(t, 20, frags[len(frags)-1].End)

	// Plan cache: second call returns the same slice (pointer identity) —
	// and persists a sidecar plan next to the fixture.
	frags2, _, err := m.Fragments(rec)
	require.NoError(t, err)
	require.Same(t, &frags[0], &frags2[0])
}


func TestManagerPlaylist(t *testing.T) {
	dir := t.TempDir()
	p1 := buildTestMP4(t, dir, "a.mp4", 20, 5)
	p2 := buildTestMP4(t, dir, "b.mp4", 10, 5)
	fi1, fi2 := fileInfo(t, p1), fileInfo(t, p2)

	m := NewManager()
	recs := []model.Recording{
		{ID: "rec-a", CameraID: "cam1", FilePath: p1, FileSize: fi1.Size(), Format: model.FormatH264,
			StartedAt: time.Now().UTC().Add(-time.Hour), EndedAt: time.Now().UTC().Add(-30 * time.Minute)},
		{ID: "rec-b", CameraID: "cam1", FilePath: p2, FileSize: fi2.Size(), Format: model.FormatH264,
			StartedAt: time.Now().UTC().Add(-29 * time.Minute), EndedAt: time.Now().UTC().Add(-25 * time.Minute)},
	}

	playlist, count, err := m.BuildPlaylist("cam1", recs)
	require.NoError(t, err)
	require.Equal(t, 2, count)

	lines := strings.Split(playlist, "\n")
	require.Contains(t, playlist, "#EXTM3U")
	require.Contains(t, playlist, "#EXT-X-PLAYLIST-TYPE:VOD")
	require.Contains(t, playlist, "#EXT-X-ENDLIST")
	require.Contains(t, playlist, `#EXT-X-MAP:URI="/api/cameras/cam1/playback/rec-a/init.mp4"`)
	require.Contains(t, playlist, `#EXT-X-MAP:URI="/api/cameras/cam1/playback/rec-b/init.mp4"`)
	require.Equal(t, 1, strings.Count(playlist, "#EXT-X-DISCONTINUITY\n"))
	// Default fragment target is 6s; the fixtures are 0.66s/0.33s total, so
	// each recording renders as ONE fragment covering all its samples.
	require.Contains(t, playlist, "/api/cameras/cam1/playback/rec-a/f0-20.m4s")
	require.Contains(t, playlist, "/api/cameras/cam1/playback/rec-b/f0-10.m4s")
	require.Contains(t, playlist, "#EXT-X-TARGETDURATION:")

	extinfCount := 0
	for _, l := range lines {
		if strings.HasPrefix(l, "#EXTINF:") {
			extinfCount++
		}
	}
	require.Equal(t, 2, extinfCount) // one per recording at the 6s target

	// Same recordings again hit the cache (still consistent output).
	playlist2, _, err := m.BuildPlaylist("cam1", recs)
	require.NoError(t, err)
	require.Equal(t, playlist, playlist2)
}

func TestSegmentCache(t *testing.T) {
	dir := t.TempDir()
	path := buildTestMP4(t, dir, "a.mp4", 10, 5)
	fi := fileInfo(t, path)

	c := newSegmentCache(2)
	rec := model.Recording{ID: "r1", FilePath: path, FileSize: fi.Size()}

	info1, err := c.Get(rec)
	require.NoError(t, err)
	info2, err := c.Get(rec)
	require.NoError(t, err)
	require.Same(t, info1, info2) // cached pointer identity

	// Changing file identity re-parses (different pointer, same content).
	rec.FileSize += 7
	info3, err := c.Get(rec)
	require.NoError(t, err)
	require.NotSame(t, info1, info3)
	require.Equal(t, info1.SampleCount, info3.SampleCount)

	// LRU eviction at capacity 2.
	c.Get(model.Recording{ID: "x", FilePath: buildTestMP4(t, dir, "x.mp4", 5, 5), FileSize: 0})
	recB := model.Recording{ID: "r2", FilePath: buildTestMP4(t, dir, "b.mp4", 5, 5)}
	recB.FileSize = fileInfo(t, recB.FilePath).Size()
	c.Get(recB)
	c.mu.Lock()
	require.Equal(t, 2, c.ll.Len())
	c.mu.Unlock()
}

func fileInfo(t *testing.T, path string) os.FileInfo {
	t.Helper()
	fi, err := os.Stat(path)
	require.NoError(t, err)
	return fi
}
