package recorder

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/streamhub"
	"github.com/stretchr/testify/require"
)

// newGBRotationRecorder mirrors newGBAdaptiveRecorder with a configurable
// SegmentDur (the #953-follow-up rotation tests need short durations).
func newGBRotationRecorder(t *testing.T, segDur time.Duration) (*GB28181Recorder, *storage.Manager) {
	t.Helper()
	store, err := storage.NewManager(t.TempDir())
	require.NoError(t, err)
	rec := NewGB28181Recorder(GB28181Config{
		CameraID:      "gb-rot",
		Encoding:      "h264",
		SegmentDur:    segDur,
		Store:         store,
		RecordEnabled: true,
	}, nil)
	rec.Hub = streamhub.New()
	rec.Hub.SetCameraID("gb-rot")
	require.NoError(t, rec.Start(context.Background()))
	rec.OnInvite()
	return rec, store
}

func mp4FilesUnder(t *testing.T, store *storage.Manager) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(store.RootDir(), "*", "*", "*", "*", "*.mp4"))
	require.NoError(t, err)
	return matches
}

// TestGB28181_RotationAtIDRKeepsGOPTail (#953 follow-up): the duration
// rollover must land on the IDR that opens the next segment. Closing on
// whatever frame crossed SegmentDur dropped every AU between that close and
// the next IDR — up to one GOP per boundary.
func TestGB28181_RotationAtIDRKeepsGOPTail(t *testing.T) {
	rec, store := newGBRotationRecorder(t, 250*time.Millisecond)
	t.Cleanup(func() { _ = rec.Stop() })

	rec.WriteNALU([][]byte{[]byte(gbAdaSPS), []byte(gbAdaPPS), []byte(gbAdaIDR)}, 90000, true)
	require.Equal(t, 1, gbAdaFrameCount(rec), "IDR must open the segment")

	// 30 P-frames paced past SegmentDur (~330ms real > 250ms, well under
	// the 2× fallback at 500ms): none of them may be dropped — the old
	// code closed the segment at the first P past the duration and
	// discarded the rest of the GOP.
	for i := 1; i <= 30; i++ {
		rec.WriteNALU([][]byte{[]byte(gbAdaPFrame)}, 90000+int64(i)*3000, false)
		time.Sleep(10 * time.Millisecond)
	}
	require.GreaterOrEqual(t, gbAdaFrameCount(rec), 29,
		"P-frames past SegmentDur must stay on disk until the next IDR")

	// The next IDR performs the rollover: segment 1 closes (finalized on
	// disk), segment 2 opens at this IDR.
	rec.WriteNALU([][]byte{[]byte(gbAdaSPS), []byte(gbAdaPPS), []byte(gbAdaIDR)}, 90000+35*3000, true)
	require.Equal(t, 1, gbAdaFrameCount(rec), "rollover IDR must open the fresh segment")
	require.Len(t, mp4FilesUnder(t, store), 1, "segment 1 must be finalized at the IDR rollover")

	for i := 1; i <= 3; i++ {
		rec.WriteNALU([][]byte{[]byte(gbAdaPFrame)}, 90000+int64(40+i)*3000, false)
	}
	require.Equal(t, 4, gbAdaFrameCount(rec), "post-rollover frames land in segment 2")
	_ = rec.Stop()
	require.Len(t, mp4FilesUnder(t, store), 2, "exactly two segments after stop")
}

// TestGB28181_RotationFallbackAt2xDuration: a stream that sends no further
// IDR still gets a bounded segment — the 2×SegmentDur fallback closes it.
func TestGB28181_RotationFallbackAt2xDuration(t *testing.T) {
	rec, store := newGBRotationRecorder(t, 120*time.Millisecond)
	t.Cleanup(func() { _ = rec.Stop() })

	rec.WriteNALU([][]byte{[]byte(gbAdaSPS), []byte(gbAdaPPS), []byte(gbAdaIDR)}, 90000, true)
	require.Equal(t, 1, gbAdaFrameCount(rec))

	// P-frames only, past 2×SegmentDur (400ms > 240ms): the fallback must
	// close the segment without waiting forever for an IDR.
	for i := 1; i <= 45; i++ {
		rec.WriteNALU([][]byte{[]byte(gbAdaPFrame)}, 90000+int64(i)*3000, false)
		time.Sleep(10 * time.Millisecond)
	}
	require.Len(t, mp4FilesUnder(t, store), 1, "2×SegmentDur fallback must finalize the segment")
	rec.mu.Lock()
	muxOpen := rec.muxer != nil
	rec.mu.Unlock()
	require.False(t, muxOpen, "muxer must be closed by the fallback (no IDR arrived)")
}

// ingestFrameCount snapshots the ingest recorder's per-segment write count.
func ingestFrameCount(rec *IngestRecorder) int {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return rec.frameCount
}

// TestIngestRecorder_RotationAtIDRKeepsGOPTail (#953 follow-up): same
// semantics as the GB28181 test — the rollover lands on the IDR that opens
// the next segment, so the GOP tail past SegmentDur stays on disk. The AU
// assembler holds one picture, hence the ±1 counting slack.
func TestIngestRecorder_RotationAtIDRKeepsGOPTail(t *testing.T) {
	rec, store, _ := newIngestRecorder(t, 250*time.Millisecond)
	t.Cleanup(func() { _ = rec.Stop() })

	rec.WriteNALU([][]byte{testSPS, testPPS, testIDR}, 0, true)
	for i := 1; i <= 30; i++ {
		rec.WriteNALU([][]byte{testPFrame}, int64(i)*90, false)
		time.Sleep(10 * time.Millisecond)
	}
	require.GreaterOrEqual(t, ingestFrameCount(rec), 28,
		"P-frames past SegmentDur must stay on disk until the next IDR")

	// The next IDR flushes the pending P into segment 1 and performs the
	// rollover; one more P flushes the IDR itself into segment 2.
	rec.WriteNALU([][]byte{testSPS, testPPS, testIDR}, 35*90, true)
	rec.WriteNALU([][]byte{testPFrame}, 36*90, false)
	require.Len(t, mp4FilesUnder(t, store), 1, "segment 1 must be finalized at the IDR rollover")
	_ = rec.Stop()
	require.Len(t, mp4FilesUnder(t, store), 2, "exactly two segments after stop")
}

// TestIngestRecorder_RotationFallbackAt2xDuration: bounded segments for a
// stream that never sends another IDR.
func TestIngestRecorder_RotationFallbackAt2xDuration(t *testing.T) {
	rec, store, _ := newIngestRecorder(t, 120*time.Millisecond)
	t.Cleanup(func() { _ = rec.Stop() })

	rec.WriteNALU([][]byte{testSPS, testPPS, testIDR}, 0, true)
	for i := 1; i <= 45; i++ {
		rec.WriteNALU([][]byte{testPFrame}, int64(i)*90, false)
		time.Sleep(10 * time.Millisecond)
	}
	require.Len(t, mp4FilesUnder(t, store), 1, "2×SegmentDur fallback must finalize the segment")
	rec.mu.Lock()
	muxOpen := rec.muxer != nil
	rec.mu.Unlock()
	require.False(t, muxOpen, "muxer must be closed by the fallback (no IDR arrived)")
}
