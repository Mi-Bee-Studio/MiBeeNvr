package transcoding

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
)

// stubTaskLister returns no active tasks — everything on disk is an orphan.
type stubTaskLister struct{}

func (stubTaskLister) ListTranscodeTasks(context.Context, storage.TranscodeTaskFilter) ([]storage.TranscodeTask, int, error) {
	return nil, 0, nil
}

// TestCleanOrphanedTranscodes_UnreadableDirSkipsNotAborts (#762 production
// observation 2026-09-14): one unreadable directory (root-owned leftover from
// a sudo-run CLI on M5) aborted the WHOLE orphan walk — every cycle errored
// and nothing past the bad entry was ever cleaned. The walk must skip the
// unreadable entry (WARN) and keep deleting orphans in sibling trees.
func TestCleanOrphanedTranscodes_UnreadableDirSkipsNotAborts(t *testing.T) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("running as root — permission-based fixture would not fail readdir")
	}
	dataDir := t.TempDir()
	// Lexical order: a_orphan < locked_dir < z_orphan — the locked dir sits
	// between the two orphans so an aborted walk leaves the last one behind.
	aOrphan := filepath.Join(dataDir, "a_orphan.transcoded.mp4")
	zOrphan := filepath.Join(dataDir, "z_orphan.transcoded.mp4")
	require.NoError(t, os.WriteFile(aOrphan, []byte("x"), 0o644))
	require.NoError(t, os.WriteFile(zOrphan, []byte("x"), 0o644))

	locked := filepath.Join(dataDir, "locked_dir")
	require.NoError(t, os.MkdirAll(filepath.Join(locked, "inner"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(locked, "inner", "y.transcoded.mp4"), []byte("x"), 0o644))
	require.NoError(t, os.Chmod(locked, 0)) // unreadable to the current user
	// Restore before t.TempDir's own cleanup (LIFO: registered later, runs first).
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

	err := CleanOrphanedTranscodes(context.Background(), dataDir, stubTaskLister{})
	require.NoError(t, err, "one unreadable dir must not fail the whole sweep")

	_, err = os.Stat(aOrphan)
	require.True(t, os.IsNotExist(err), "orphan before the locked dir must be deleted")
	_, err = os.Stat(zOrphan)
	require.True(t, os.IsNotExist(err), "orphan after the locked dir must still be deleted")
}
