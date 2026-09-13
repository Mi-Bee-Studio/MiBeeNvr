package timelapse

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestSweepStaleExtractDirs (#762 production observation 2026-09-14):
// interrupted periodic merges (server crash mid-window, or a Ctrl-C'd
// timelapse-merge CLI — one run as root even left root-owned dirs) leak
// periodic_extract_* directories under <dataDir>/tmp that nothing ever
// reclaims — the deferred cleanup only covers the happy path. The manager
// must sweep leftovers older than the grace period at Run() start, while
// never touching dirs a live merge could still be using.
func TestSweepStaleExtractDirs(t *testing.T) {
	t.Helper()
	dataDir := t.TempDir()
	tmpBase := filepath.Join(dataDir, "tmp")
	require.NoError(t, os.MkdirAll(tmpBase, 0o755))

	old := filepath.Join(tmpBase, "periodic_extract_h264_stale")
	fresh := filepath.Join(tmpBase, "periodic_extract_h265_live")
	unrelated := filepath.Join(tmpBase, "keep_me")
	for _, d := range []string{old, fresh, unrelated} {
		require.NoError(t, os.MkdirAll(d, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(d, "frame_000001.jpg"), []byte("x"), 0o644))
	}
	// Backdate the stale one past the grace period (24h).
	past := time.Now().Add(-25 * time.Hour)
	require.NoError(t, os.Chtimes(old, past, past))
	// Fresh one just created — a possibly-live merge window.

	mgr := NewPeriodicMergeManager(nil, nil, nil, 30, dataDir, time.Hour, time.UTC)
	n := mgr.sweepStaleExtractDirs()
	require.Equal(t, 1, n, "only the >24h-old extract dir should be swept")

	_, err := os.Stat(old)
	require.True(t, os.IsNotExist(err), "stale extract dir must be removed")
	for _, keep := range []string{fresh, unrelated} {
		_, err := os.Stat(keep)
		require.NoError(t, err, "%s must survive the sweep", keep)
	}
}

// TestSweepStaleExtractDirs_NoBaseIsNoop: a manager without a dataDir (empty
// tempDirBase) must not crash or create anything.
func TestSweepStaleExtractDirs_NoBaseIsNoop(t *testing.T) {
	t.Helper()
	mgr := NewPeriodicMergeManager(nil, nil, nil, 30, "", time.Hour, time.UTC)
	require.Zero(t, mgr.sweepStaleExtractDirs())
}
