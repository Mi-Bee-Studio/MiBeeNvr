package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/avi"
)

// TestEstimateAVIDuration (#761): AVI is the default MJPEG segment container
// now, so `repair duration` must derive a zero-duration AVI orphan's length
// from the container itself (frame index × dwMicroSecPerFrame) instead of
// falling into the MJPEG-directory estimator and failing on a file path.
func TestEstimateAVIDuration(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "cam_20260913_120000_1.avi")

	// Build a real 5-frame video-only AVI with the production muxer
	// (incremental file mode, same as the recorder).
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	require.NoError(t, err)
	m := avi.NewVideoOnlyMuxer(f, 32, 24)
	frame := make([]byte, 2048)
	frame[0], frame[1] = 0xFF, 0xD8
	for range 5 {
		require.NoError(t, m.WriteVideo(frame, 0))
	}
	require.NoError(t, m.Close())
	require.NoError(t, f.Close())

	dur, err := estimateAVIDuration(path)
	require.NoError(t, err)
	// 5 frames at the muxer's default 33333us/frame ≈ 0.1667s.
	require.InDelta(t, 5*33333/1e6, dur, 1e-9)
}

// TestEstimateAVIDuration_RejectsGarbage: a non-AVI file must error (not
// panic, not return 0) so `repair duration` can mark it unrepairable.
func TestEstimateAVIDuration_RejectsGarbage(t *testing.T) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "junk.avi")
	require.NoError(t, os.WriteFile(path, []byte("not an avi at all"), 0o644))

	_, err := estimateAVIDuration(path)
	require.Error(t, err)
}
