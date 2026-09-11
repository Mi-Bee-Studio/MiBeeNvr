package cleanup

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/stretchr/testify/require"
)

// TestDeepOrphanCleanup_NestedTree is the regression core of the 2026-09-11
// M5 finding: 16.2 GB / 28k files of orphans lived in the nested
// YYYYMM/DD/HH trees that the top-level-only orphan scanner never reached
// (leaked pre-#117 merge outputs + abandoned MJPEG frame dirs). The deep
// scan must reach them while respecting the same safety rails: referenced
// files (file_path AND merge_path) survive, young files survive, non-media
// files survive, and emptied date dirs get pruned.
func TestDeepOrphanCleanup_NestedTree(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)
	defer env.close(t)

	cfg := defaultCleanupConfig()
	cfg.RetentionDays = 365
	cm, err := NewCleanupManager(env.db, env.store, cfg)
	require.NoError(t, err)

	now := time.Now()
	old := now.Add(-2 * time.Hour)
	touch := func(p string) {
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte("data"), 0o644))
		require.NoError(t, os.Chtimes(p, old, old))
	}

	// 1. Referenced file_path in the nested tree — survives (helper writes the file).
	referenced := filepath.Join(env.store.RootDir(), "cam1", "202609", "11", "10", "cam1_20260911_100000_kept.mp4")
	env.insertTimelapseRecording(t, "rec-keep", "cam1", "202609/11/10/cam1_20260911_100000_kept.mp4", now.Add(-1*time.Hour))
	require.NoError(t, os.Chtimes(referenced, old, old))

	// 2. Orphan merged output deep in the tree — deleted (the pre-#117 leak class).
	orphanMerge := filepath.Join(env.store.RootDir(), "cam1", "202609", "02", "16", "1788339401891077224.mp4")
	touch(orphanMerge)

	// 3. Orphan MJPEG frame file inside an abandoned dir — deleted.
	orphanFrame := filepath.Join(env.store.RootDir(), "cam1", "202607", "23", "07", "cam1_20260723_073710_x", "frame_000013.h265")
	touch(orphanFrame)

	// 4. Young orphan (in-flight write race) — survives.
	youngOrphan := filepath.Join(env.store.RootDir(), "cam1", "202609", "11", "10", "cam1_20260911_110000_young.mp4")
	require.NoError(t, os.MkdirAll(filepath.Dir(youngOrphan), 0o755))
	require.NoError(t, os.WriteFile(youngOrphan, []byte("data"), 0o644))

	// 5. Non-media file — survives (the scanner only removes media artifacts).
	notes := filepath.Join(env.store.RootDir(), "cam1", "202609", "11", "10", "notes.txt")
	touch(notes)

	require.NoError(t, cm.RunOnce(context.Background()))

	_, err = os.Stat(referenced)
	require.NoError(t, err, "referenced file_path must survive")
	_, err = os.Stat(orphanMerge)
	require.True(t, os.IsNotExist(err), "nested orphan merge output must be deleted")
	_, err = os.Stat(orphanFrame)
	require.True(t, os.IsNotExist(err), "nested orphan frame file must be deleted")
	_, err = os.Stat(youngOrphan)
	require.NoError(t, err, "orphan younger than 1h must survive")
	_, err = os.Stat(notes)
	require.NoError(t, err, "non-media file must survive")

	// The emptied date dirs are pruned, the referenced file's parents stay.
	_, err = os.Stat(filepath.Dir(orphanMerge))
	require.True(t, os.IsNotExist(err), "emptied date dirs should be pruned")
	_, err = os.Stat(filepath.Dir(referenced))
	require.NoError(t, err, "dir holding a referenced file must stay")
}

// TestDeepOrphanCleanup_MergePathReferencedSurvives pins the #117 lesson in
// the deep scanner: merge_path is a live reference. A merged-output .mp4 on
// disk whose recording row still points at it via merge_path MUST survive.
func TestDeepOrphanCleanup_MergePathReferencedSurvives(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)
	defer env.close(t)

	cfg := defaultCleanupConfig()
	cfg.RetentionDays = 365
	cm, err := NewCleanupManager(env.db, env.store, cfg)
	require.NoError(t, err)

	old := time.Now().Add(-2 * time.Hour)
	mergeOut := filepath.Join(env.store.RootDir(), "cam1", "202609", "11", "09", "1789099000000000001.mp4")
	require.NoError(t, os.MkdirAll(filepath.Dir(mergeOut), 0o755))
	require.NoError(t, os.WriteFile(mergeOut, []byte("merged"), 0o644))
	require.NoError(t, os.Chtimes(mergeOut, old, old))

	env.insertMergedRecording(t, "rec-m1", "cam1", "202609/11/09/cam1_20260911_090000_src.mp4", "202609/11/09/1789099000000000001.mp4")

	require.NoError(t, cm.RunOnce(context.Background()))
	_, err = os.Stat(mergeOut)
	require.NoError(t, err, "merge_path-referenced output must survive deep orphan cleanup")
}

// TestDeepOrphanCleanup_DailyGate verifies the deep walk runs at most once
// per deepOrphanInterval — the recursive walk is IO the box shouldn't pay
// every cleanup cycle (M5: single spinning disk).
func TestDeepOrphanCleanup_DailyGate(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)
	defer env.close(t)

	cfg := defaultCleanupConfig()
	cfg.RetentionDays = 365
	cm, err := NewCleanupManager(env.db, env.store, cfg)
	require.NoError(t, err)

	orphan := filepath.Join(env.store.RootDir(), "cam1", "202609", "02", "16", "1788339400000000000.mp4")
	require.NoError(t, os.MkdirAll(filepath.Dir(orphan), 0o755))
	require.NoError(t, os.WriteFile(orphan, []byte("x"), 0o644))
	require.NoError(t, os.Chtimes(orphan, time.Now().Add(-2*time.Hour), time.Now().Add(-2*time.Hour)))

	// First cycle: deep scan runs, orphan goes.
	require.NoError(t, cm.RunOnce(context.Background()))
	_, err = os.Stat(orphan)
	require.True(t, os.IsNotExist(err))

	// Plant a fresh orphan and run again immediately: within the gate window
	// the deep scan must NOT run — the nested orphan survives this cycle.
	orphan2 := filepath.Join(env.store.RootDir(), "cam1", "202609", "02", "17", "1788339500000000000.mp4")
	require.NoError(t, os.MkdirAll(filepath.Dir(orphan2), 0o755))
	require.NoError(t, os.WriteFile(orphan2, []byte("x"), 0o644))
	require.NoError(t, os.Chtimes(orphan2, time.Now().Add(-2*time.Hour), time.Now().Add(-2*time.Hour)))
	require.NoError(t, cm.RunOnce(context.Background()))
	_, err = os.Stat(orphan2)
	require.NoError(t, err, "deep scan must be gated to once per interval")

	// Expire the gate: the next cycle sweeps it.
	cm.deepOrphanLast = time.Now().Add(-deepOrphanInterval - time.Minute)
	require.NoError(t, cm.RunOnce(context.Background()))
	_, err = os.Stat(orphan2)
	require.True(t, os.IsNotExist(err), "deep scan must run again after the interval")
}

// insertMergedRecording inserts a recording whose artifacts live in the
// nested tree: a consumed source path (file need not exist anymore) and a
// merged output that IS on disk and referenced via merge_path.
func (e *testEnv) insertMergedRecording(t *testing.T, id, cameraID, filePathRel, mergePathRel string) {
	t.Helper()
	now := time.Now()
	rec := &model.Recording{
		ID:          id,
		CameraID:    cameraID,
		FilePath:    filepath.Join(e.store.RootDir(), cameraID, filePathRel),
		MergePath:   filepath.Join(e.store.RootDir(), cameraID, mergePathRel),
		Format:      "h264",
		StartedAt:   now.Add(-2 * time.Hour),
		EndedAt:     now.Add(-1 * time.Hour),
		Duration:    3600,
		FileSize:    2048,
		MergeStatus: model.MergeStatusMerged,
	}
	require.NoError(t, e.db.InsertRecording(context.Background(), rec))
	// InsertRecording does not persist merge_path — the merge engine writes it
	// via SetMergeResult; mirror that here.
	require.NoError(t, e.db.SetMergeResult(context.Background(), id,
		rec.MergePath, "rolling"))
}
