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
// periodic temp directories under <dataDir>/tmp that nothing ever reclaims —
// the deferred cleanup only covers the happy path. The manager must sweep
// BOTH temp-dir families (periodic_extract_* from frame extraction AND
// periodic_go_merge_* from the Go merge pipeline — the latter carries the
// full copied frame set, so its leaks are LARGER) at Run() start, while
// never touching dirs a live merge could still be using.
func TestSweepStaleExtractDirs(t *testing.T) {
	t.Helper()
	dataDir := t.TempDir()
	tmpBase := filepath.Join(dataDir, "tmp")
	require.NoError(t, os.MkdirAll(tmpBase, 0o755))

	oldExtract := filepath.Join(tmpBase, "periodic_extract_h264_stale")
	oldGoMerge := filepath.Join(tmpBase, "periodic_go_merge_123456_stale")
	fresh := filepath.Join(tmpBase, "periodic_extract_h265_live")
	unrelated := filepath.Join(tmpBase, "keep_me")
	for _, d := range []string{oldExtract, oldGoMerge, fresh, unrelated} {
		require.NoError(t, os.MkdirAll(d, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(d, "frame_000001.jpg"), []byte("x"), 0o644))
	}
	// Backdate the stale ones past the grace period (24h).
	past := time.Now().Add(-25 * time.Hour)
	require.NoError(t, os.Chtimes(oldExtract, past, past))
	require.NoError(t, os.Chtimes(oldGoMerge, past, past))
	// Fresh one just created — a possibly-live merge window.

	mgr := NewPeriodicMergeManager(nil, nil, nil, 30, dataDir, time.Hour, time.UTC)
	n := mgr.sweepStaleTempDirs()
	require.Equal(t, 2, n, "both stale extract AND go-merge dirs should be swept")

	for _, gone := range []string{oldExtract, oldGoMerge} {
		_, err := os.Stat(gone)
		require.True(t, os.IsNotExist(err), "stale dir %s must be removed", gone)
	}
	for _, keep := range []string{fresh, unrelated} {
		_, err := os.Stat(keep)
		require.NoError(t, err, "%s must survive the sweep", keep)
	}
}

// TestSweepTempDirs_ConfigurableGrace: the grace period must be injectable
// (storage.periodic_temp_grace_s, review #797-1) — a hardcoded 24h is a
// safety knob (too small eats live merges on slow disks; too large leaks).
func TestSweepTempDirs_ConfigurableGrace(t *testing.T) {
	t.Helper()
	dataDir := t.TempDir()
	tmpBase := filepath.Join(dataDir, "tmp")
	require.NoError(t, os.MkdirAll(tmpBase, 0o755))

	dir := filepath.Join(tmpBase, "periodic_go_merge_young_stale")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	// 90min old: inside the 24h default grace, but past a 1h override.
	require.NoError(t, os.Chtimes(dir, time.Now().Add(-90*time.Minute), time.Now().Add(-90*time.Minute)))

	// Default grace keeps it.
	def := NewPeriodicMergeManager(nil, nil, nil, 30, dataDir, time.Hour, time.UTC)
	require.Zero(t, def.sweepStaleTempDirs())
	_, err := os.Stat(dir)
	require.NoError(t, err, "90min-old dir must survive the 24h default grace")

	// 1h override sweeps it.
	fast := NewPeriodicMergeManager(nil, nil, nil, 30, dataDir, time.Hour, time.UTC,
		WithTempDirGrace(time.Hour))
	require.Equal(t, 1, fast.sweepStaleTempDirs())
	_, err = os.Stat(dir)
	require.True(t, os.IsNotExist(err), "90min-old dir must be swept under a 1h grace")
}

// TestSweepTempDirs_CreationPrefixesShared guards the single-source-of-truth
// for temp-dir prefixes (review #797-2 nit): the creation sites and the sweep
// must share the same constants, so renaming one without the other can never
// silently blind the sweep.
func TestSweepTempDirs_CreationPrefixesShared(t *testing.T) {
	t.Helper()
	require.Contains(t, periodicTempDirPrefixes, periodicTempExtractPrefix)
	require.Contains(t, periodicTempDirPrefixes, periodicTempGoMergePrefix)
	// And the creation sites actually use the constants (compile-time reuse
	// is the real guard; this pins the prefix VALUES against drift).
	require.Equal(t, "periodic_extract_", periodicTempExtractPrefix)
	require.Equal(t, "periodic_go_merge_", periodicTempGoMergePrefix)
}

// TestSweepStaleExtractDirs_NoBaseIsNoop: a manager without a dataDir (empty
// tempDirBase) must not crash or create anything.
func TestSweepStaleExtractDirs_NoBaseIsNoop(t *testing.T) {
	t.Helper()
	mgr := NewPeriodicMergeManager(nil, nil, nil, 30, "", time.Hour, time.UTC)
	require.Zero(t, mgr.sweepStaleTempDirs())
}
