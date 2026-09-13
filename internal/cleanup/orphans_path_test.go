package cleanup

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
)

// TestDeepOrphanCleanup_RelativeRefProtectsFile guards the sub_-layer data-loss
// bug (2026-09-13): tierrec's subRecorder stores file_path RELATIVE to the
// storage root (cam-1/202609/12/13/sub_cam-1_x.mp4), while the deep orphan
// walk yields ABSOLUTE paths. referencedUnder compared the raw strings, so
// relative refs never matched and every sub_ segment older than the 1h rail
// was deleted as an "orphan" — 9.3k dangling rows and a silently destroyed
// sub-stream tier on one production camera.
func TestDeepOrphanCleanup_RelativeRefProtectsFile(t *testing.T) {
	t.Helper()
	env := newTestEnv(t)
	defer env.close(t)
	ctx := context.Background()

	old := time.Now().Add(-2 * time.Hour) // beyond the 1h age rail
	relPath := filepath.Join("cam1", "202609", "12", "13", "sub_cam1_20260912_130000_1.mp4")
	absPath := filepath.Join(env.store.RootDir(), relPath)
	require.NoError(t, os.MkdirAll(filepath.Dir(absPath), 0o755))
	require.NoError(t, os.WriteFile(absPath, []byte("sub-stream segment"), 0o644))
	require.NoError(t, os.Chtimes(absPath, old, old))

	// tierrec-style row: RELATIVE file_path (the bug trigger).
	require.NoError(t, env.db.InsertRecording(ctx, &model.Recording{
		ID: "rec-rel", CameraID: "cam1", FilePath: relPath, Format: model.FormatH265,
		StartedAt: old, EndedAt: old, MergeStatus: model.MergeStatusPending, Layer: model.LayerSub,
	}))

	cm, err := NewCleanupManager(env.db, env.store, defaultCleanupConfig())
	require.NoError(t, err)

	deleted := cm.deepOrphanCleanup(ctx, []string{"cam1"})
	require.Zero(t, deleted, "relative-ref sub_ file must not be orphan-deleted")
	require.FileExists(t, absPath, "sub-stream segment file must survive the deep scan")
}

// TestDeepOrphanCleanup_StillDeletesTrueOrphans pins the rail after the
// normalization fix: an old media file referenced by NO row (either path
// form) is still reclaimed, and absolute-ref rows keep their protection.
func TestDeepOrphanCleanup_StillDeletesTrueOrphans(t *testing.T) {
	t.Helper()
	env := newTestEnv(t)
	defer env.close(t)
	ctx := context.Background()

	old := time.Now().Add(-2 * time.Hour)

	// True orphan: no DB row at all.
	orphan := filepath.Join(env.store.RootDir(), "cam1", "202609", "12", "13", "cam1_orphan.mp4")
	require.NoError(t, os.MkdirAll(filepath.Dir(orphan), 0o755))
	require.NoError(t, os.WriteFile(orphan, []byte("junk"), 0o644))
	require.NoError(t, os.Chtimes(orphan, old, old))

	// Absolute-ref protected file (merge-manager style row).
	protAbs := filepath.Join(env.store.RootDir(), "cam1", "202609", "12", "13", "cam1_bucket.mp4")
	require.NoError(t, os.WriteFile(protAbs, []byte("bucket"), 0o644))
	require.NoError(t, os.Chtimes(protAbs, old, old))
	rec := &model.Recording{
		ID: "rec-abs", CameraID: "cam1", FilePath: protAbs, Format: model.FormatH265,
		StartedAt: old, EndedAt: old, MergeStatus: model.MergeStatusMerged,
	}
	require.NoError(t, env.db.InsertRecording(ctx, rec))

	cm, err := NewCleanupManager(env.db, env.store, defaultCleanupConfig())
	require.NoError(t, err)

	deleted := cm.deepOrphanCleanup(ctx, []string{"cam1"})
	require.Equal(t, 1, deleted, "only the unreferenced orphan is deleted")
	require.NoFileExists(t, orphan)
	require.FileExists(t, protAbs)
}
