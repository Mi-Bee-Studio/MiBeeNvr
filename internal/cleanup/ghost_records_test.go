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

// Ghost rows: recordings whose merge_status is 'pending' but whose file never
// existed on disk (2026-09-08 production incident — the startup temp-cleanup
// scan deleted an in-flight segment temp; the DB row was still inserted and
// became a permanent 404 entry). staleRecordCleanup deletes such rows once
// they are older than the grace window; the grace window protects rows whose
// storage root may be temporarily unmounted (per-camera override hiccup).
func TestStaleRecordCleanup_DeletesGhostPendingRows(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t)
	defer env.close(t)

	insertGhost := func(t *testing.T, id string, age time.Duration, onDisk bool) {
		t.Helper()
		fullPath := filepath.Join(env.store.RootDir(), "cam1", id+".mp4")
		started := time.Now().UTC().Add(-age)
		_, err := env.db.DB().ExecContext(ctx,
			`INSERT INTO recordings(id, camera_id, file_path, format, started_at, ended_at, duration, file_size, frame_count, merge_status)
			 VALUES(?,?,?,?,?,?,?,?,?,?);`,
			id, "cam1", fullPath, model.FormatH265,
			started, started.Add(time.Minute), 60, 0, 900, model.MergeStatusPending)
		require.NoError(t, err)
		if onDisk {
			require.NoError(t, os.MkdirAll(filepath.Dir(fullPath), 0o755))
			require.NoError(t, os.WriteFile(fullPath, []byte("data"), 0o644))
		}
	}

	insertGhost(t, "ghost-old", 48*time.Hour, false) // missing + old → deleted
	insertGhost(t, "ghost-fresh", time.Hour, false)  // missing but fresh → kept (grace)
	insertGhost(t, "live-old", 48*time.Hour, true)   // file present → kept

	cfg := defaultCleanupConfig()
	cm, err := NewCleanupManager(env.db, env.store, cfg)
	require.NoError(t, err)

	cm.staleRecordCleanup(ctx)

	ghostOld, err := env.db.GetRecording(ctx, "ghost-old")
	require.NoError(t, err)
	require.Nil(t, ghostOld, "old ghost row (file never existed) must be deleted")

	ghostFresh, err := env.db.GetRecording(ctx, "ghost-fresh")
	require.NoError(t, err)
	require.NotNil(t, ghostFresh, "fresh ghost row must stay inside the grace window (mount-hiccup guard)")

	live, err := env.db.GetRecording(ctx, "live-old")
	require.NoError(t, err)
	require.NotNil(t, live, "pending row with file on disk must never be deleted")
	require.Equal(t, model.MergeStatusPending, live.MergeStatus)
}

// AI-active rows are protected from retention cleanup; the ghost sweep must
// respect the same protection (MiBeeVision may be mid-writeback).
func TestStaleRecordCleanup_ProtectsAIActiveRows(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t)
	defer env.close(t)

	fullPath := filepath.Join(env.store.RootDir(), "cam1", "ghost-ai.mp4")
	started := time.Now().UTC().Add(-48 * time.Hour)
	_, err := env.db.DB().ExecContext(ctx,
		`INSERT INTO recordings(id, camera_id, file_path, format, started_at, ended_at, duration, file_size, frame_count, merge_status, ai_status)
		 VALUES(?,?,?,?,?,?,?,?,?,?,?);`,
		"ghost-ai", "cam1", fullPath, model.FormatH265,
		started, started.Add(time.Minute), 60, 0, 900, model.MergeStatusPending, "processing")
	require.NoError(t, err)

	cfg := defaultCleanupConfig()
	cm, err := NewCleanupManager(env.db, env.store, cfg)
	require.NoError(t, err)

	cm.staleRecordCleanup(ctx)

	rec, err := env.db.GetRecording(ctx, "ghost-ai")
	require.NoError(t, err)
	require.NotNil(t, rec, "AI-active ghost row must be left for the AI writeback lifecycle")
}
