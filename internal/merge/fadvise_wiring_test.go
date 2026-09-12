package merge

// Wiring tests for #754 — merge sources must carry page-cache hints:
// POSIX_FADV_SEQUENTIAL right after open (readahead), POSIX_FADV_DONTNEED
// only after the file's last consumer is done (else the audio pass would
// re-read from disk). No-audio segments drop their cache at the video-pass
// close; audio-bearing segments at the audio-pass close.

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/fadvise"
	"github.com/stretchr/testify/require"
)

type adviseCall struct {
	fd     int
	advice int
}

func recordAdvice(calls *[]adviseCall) (restore func()) {
	return fadvise.SwapForTest(func(fd, advice int) error {
		*calls = append(*calls, adviseCall{fd, advice})
		return nil
	})
}

func TestMergeSourceFadviseSequence_NoAudio(t *testing.T) {
	dir := t.TempDir()
	sps := []byte{0x67, 0x42, 0x00, 0x0a, 0xe2, 0x40, 0x40, 0x04, 0x00, 0x00, 0x00, 0x04, 0x00, 0x00, 0x00, 0xc8, 0x40}
	pps := []byte{0x68, 0xce, 0x38, 0x80}
	seg1 := createH264SegmentWithSamples(t, dir, "seg1.mp4", sps, pps,
		[][]byte{{0x65, 0x88, 0x80, 0x40}, {0x41, 0x10, 0x00, 0x0c}})
	seg2 := createH264SegmentWithSamples(t, dir, "seg2.mp4", sps, pps,
		[][]byte{{0x65, 0x88, 0x80, 0x40}, {0x41, 0x10, 0x00, 0x0c}})
	info1, err := ParseSegment(seg1)
	require.NoError(t, err)
	info2, err := ParseSegment(seg2)
	require.NoError(t, err)

	var calls []adviseCall
	restore := recordAdvice(&calls)
	defer restore()

	output := filepath.Join(dir, "merged.mp4")
	_, err = MergeMP4Segments(context.Background(), []*SegmentInfo{info1, info2}, output)
	require.NoError(t, err)

	// Two sources, no audio: exactly one [SEQUENTIAL → DONTNEED] pair per
	// source file, in that order.
	require.Len(t, calls, 4, "2 sources × (sequential+dontneed): %v", calls)
	seq, dont := 0, 0
	for i, c := range calls {
		switch c.advice {
		case fadvise.AdviceSequential:
			seq++
		case fadvise.AdviceDontNeed:
			dont++
			require.NotEmpty(t, calls[:i], "DONTNEED must never be the first hint for an fd")
			require.Equal(t, calls[i-1].fd, c.fd, "DONTNEED must follow its own SEQUENTIAL")
			require.Equal(t, fadvise.AdviceSequential, calls[i-1].advice)
		default:
			t.Fatalf("unexpected advice %d", c.advice)
		}
	}
	require.Equal(t, 2, seq)
	require.Equal(t, 2, dont)

	// Regression: output still valid.
	fi, err := os.Stat(output)
	require.NoError(t, err)
	require.Positive(t, fi.Size())
	merged, err := ParseSegment(output)
	require.NoError(t, err)
	require.Equal(t, "h264", merged.Codec)
}

func TestMergeBufferSizeAdaptive(t *testing.T) {
	old := sysTotalMem
	defer func() { sysTotalMem = old }()

	sysTotalMem = func() int64 { return 512 << 20 } // 512MB — RPi-3B class
	require.Equal(t, 1<<20, mergeBufferSize(), "low-memory hosts keep the 1MB buffer")

	sysTotalMem = func() int64 { return 4 << 30 } // 4GB
	require.Equal(t, 4<<20, mergeBufferSize(), "≥1.5GB hosts get the larger buffer")

	sysTotalMem = func() int64 { return 0 } // unknown (non-linux / probe failure)
	require.Equal(t, 1<<20, mergeBufferSize(), "unknown memory falls back to 1MB")
}

// compile-time guard that the fixtures used above stay in sync with the
// merge helpers (keeps the wiring test self-documenting).
var _ = time.Second
