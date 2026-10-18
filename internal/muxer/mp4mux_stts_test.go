package muxer

// mp4mux_stts_test.go — regression test for the stts run-length encoding in
// writeStbl (issue #1011 follow-up): consecutive equal sample durations must
// compress to single entries, and the compressed table must expand back to
// the exact per-sample durations through the package every consumer uses to
// read segments (mediaprobe).

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/mediaprobe"
	"github.com/abema/go-mp4"
	"github.com/stretchr/testify/require"
)

// readSttsEntries walks the file's first video trak and returns its stts
// entries (raw, unexpanded).
func readSttsEntries(t *testing.T, path string) []mp4.SttsEntry {
	t.Helper()
	f, err := os.Open(path)
	require.NoError(t, err)
	defer f.Close()
	var entries []mp4.SttsEntry
	var inVideoTrak bool
	_, err = mp4.ReadBoxStructure(f, func(h *mp4.ReadHandle) (interface{}, error) {
		if !h.BoxInfo.IsSupportedType() {
			return nil, nil
		}
		switch h.BoxInfo.Type.String() {
		case "trak":
			inVideoTrak = !inVideoTrak // crude toggle; only one trak in fixtures
			return h.Expand()
		case "stts":
			if inVideoTrak {
				box, _, err := h.ReadPayload()
				if err != nil {
					return nil, err
				}
				if stts, ok := box.(*mp4.Stts); ok {
					entries = stts.Entries
				}
			}
			return nil, nil
		default:
			return h.Expand()
		}
	})
	require.NoError(t, err)
	return entries
}

func TestSttsRunLengthEncoded(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rle.mp4")

	m := NewMP4Muxer(path)
	trackID, err := m.AddH264Track(testSPS, testPPS)
	require.NoError(t, err)

	// 3×33ms then 2×40ms → two runs.
	durs := []time.Duration{33 * time.Millisecond, 33 * time.Millisecond, 33 * time.Millisecond, 40 * time.Millisecond, 40 * time.Millisecond}
	var pts time.Duration
	for _, d := range durs {
		require.NoError(t, m.WriteSample(trackID, []byte{0x41, 0x10, 0x00, 0x0c}, pts, d))
		pts += d
	}
	require.NoError(t, m.Close())

	entries := readSttsEntries(t, path)
	require.Len(t, entries, 2, "two duration runs must compress to two stts entries")
	require.Equal(t, uint32(3), entries[0].SampleCount)
	require.Equal(t, uint32(33), entries[0].SampleDelta)
	require.Equal(t, uint32(2), entries[1].SampleCount)
	require.Equal(t, uint32(40), entries[1].SampleDelta)

	// Lossless through the consumer path: the parser must expand the runs
	// back to the exact per-sample durations.
	info, err := mediaprobe.ParseSegment(path)
	require.NoError(t, err)
	require.Equal(t, 5, info.SampleCount)
	want := []uint32{33, 33, 33, 40, 40}
	for i, w := range want {
		require.Equal(t, w, info.Samples[i].Duration, "expanded stts must preserve sample %d duration", i)
	}
}
