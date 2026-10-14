package xiaomi

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
	"github.com/stretchr/testify/require"
)

// newRotationRecorder builds a XiaomiRecorder over a temp-dir store with a
// short SegmentDur; sps/pps are armed the way the CS2 session parser would.
func newRotationRecorder(t *testing.T, segDur time.Duration) (*XiaomiRecorder, *storage.Manager) {
	t.Helper()
	store, err := storage.NewManager(t.TempDir())
	require.NoError(t, err)
	rec := NewXiaomiRecorder(XiaomiRecorderConfig{
		CameraID:   "xiaomi-rot",
		SegmentDur: segDur,
	}, store)
	rec.sps = []byte{0x67, 0x42, 0x00, 0x0a, 0xf8, 0x41, 0xa2}
	rec.pps = []byte{0x68, 0xce, 0x38, 0x80}
	return rec, store
}

func mp4FilesUnder(t *testing.T, store *storage.Manager) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(store.RootDir(), "*", "*", "*", "*", "*.mp4"))
	require.NoError(t, err)
	return matches
}

// TestXiaomiRecorder_RotationAtIDRKeepsGOPTail (#953 follow-up): the
// duration rollover lands on the IDR that opens the next segment — the GOP
// tail past SegmentDur stays on disk instead of being discarded until the
// next IDR.
func TestXiaomiRecorder_RotationAtIDRKeepsGOPTail(t *testing.T) {
	rec, store := newRotationRecorder(t, 250*time.Millisecond)

	var lastTS uint64
	idr := []byte{0x65, 0x88, 0x84, 0x00, 0x10}
	p := []byte{0x41, 0x9a, 0x10, 0x00}

	rec.processH264NALU(idr, 0, &lastTS)
	for i := 1; i <= 30; i++ {
		rec.processH264NALU(p, uint64(i)*40, &lastTS)
		time.Sleep(10 * time.Millisecond)
	}
	rec.mu.Lock()
	fc := rec.frameCount
	rec.mu.Unlock()
	require.GreaterOrEqual(t, fc, 29, "P-frames past SegmentDur must stay on disk until the next IDR")

	rec.processH264NALU(idr, 35*40, &lastTS)
	require.Len(t, mp4FilesUnder(t, store), 1, "segment 1 must be finalized at the IDR rollover")
	rec.mu.Lock()
	fc = rec.frameCount
	rec.mu.Unlock()
	require.Equal(t, 1, fc, "rollover IDR must open the fresh segment")
	rec.processH264NALU(p, 36*40, &lastTS)
	rec.processH264NALU(p, 37*40, &lastTS)
	rec.closeCurrentSegment()
	require.Len(t, mp4FilesUnder(t, store), 2, "exactly two segments after close")
}

// TestXiaomiRecorder_RotationFallbackAt2xDuration: bounded segments for a
// stream that never sends another IDR.
func TestXiaomiRecorder_RotationFallbackAt2xDuration(t *testing.T) {
	rec, store := newRotationRecorder(t, 120*time.Millisecond)

	var lastTS uint64
	idr := []byte{0x65, 0x88, 0x84, 0x00, 0x10}
	p := []byte{0x41, 0x9a, 0x10, 0x00}

	rec.processH264NALU(idr, 0, &lastTS)
	for i := 1; i <= 45; i++ {
		rec.processH264NALU(p, uint64(i)*40, &lastTS)
		time.Sleep(10 * time.Millisecond)
	}
	require.Len(t, mp4FilesUnder(t, store), 1, "2×SegmentDur fallback must finalize the segment")
	rec.mu.Lock()
	muxOpen := rec.muxer != nil
	rec.mu.Unlock()
	require.False(t, muxOpen, "muxer must be closed by the fallback (no IDR arrived)")
	_ = rec.Stop()
}
