package cleanup

// Tests for the archived-recording retention strategy (time_retention.go) and
// transcode-task history retention. These paths delete user data, so every
// guard (keep-forever, AI-processing protection, empty-group teardown) is
// asserted explicitly. See #565.

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
	"github.com/stretchr/testify/require"
)

// archiveCamera inserts a camera row and flags it archived with the given
// archive retention (0 = keep forever).
func archiveCamera(t *testing.T, env *testEnv, id string, archiveRetentionDays int) {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, env.db.UpsertCamera(ctx, id, "Archived Cam", "rtsp", "", "rtsp://localhost/"+id, "", "", "", "", "", ""))
	_, err := env.db.DB().ExecContext(ctx,
		`UPDATE cameras SET archived=1, archived_at=?, archive_retention_days=? WHERE id=?;`,
		time.Now().UTC().Add(-24*time.Hour), archiveRetentionDays, id)
	require.NoError(t, err)
}

func recordingExists(t *testing.T, env *testEnv, id string) bool {
	t.Helper()
	rec, err := env.db.GetRecording(context.Background(), id)
	require.NoError(t, err)
	return rec != nil
}

// markRecordingArchived flags the recording row itself as archived — the
// expired-archived query filters on recordings.archived, not just the camera.
func markRecordingArchived(t *testing.T, env *testEnv, id string) {
	t.Helper()
	_, err := env.db.DB().ExecContext(context.Background(),
		`UPDATE recordings SET archived=1 WHERE id=?;`, id)
	require.NoError(t, err)
}

func TestArchivedRetentionCleanup_ExpiredDeleted(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t)
	defer env.close(t)

	archiveCamera(t, env, "archcam", 7)
	// Ended 10 days ago — beyond the 7-day archive retention.
	env.insertTestRecording(t, "arch-expired", "archcam", "archcam/seg1.mp4", time.Now().UTC().Add(-10*24*time.Hour), false)
	markRecordingArchived(t, env, "arch-expired")
	// Well within global retention (30d) so timeBasedCleanup must not touch it either way.
	env.insertTestRecording(t, "live-kept", "cam1", "cam1/seg1.mp4", time.Now().UTC().Add(-2*24*time.Hour), false)

	cfg := defaultCleanupConfig()
	cm, err := NewCleanupManager(env.db, env.store, cfg)
	require.NoError(t, err)
	require.NoError(t, cm.RunOnce(ctx))

	require.False(t, recordingExists(t, env, "arch-expired"), "expired archived recording should be deleted from DB")
	_, err = os.Stat(filepath.Join(env.store.RootDir(), "archcam", "seg1.mp4"))
	require.True(t, os.IsNotExist(err), "expired archived recording file should be deleted")

	require.True(t, recordingExists(t, env, "live-kept"), "active camera recording within retention must survive")
}

func TestArchivedRetentionCleanup_KeepForeverWhenZero(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t)
	defer env.close(t)

	// archive_retention_days=0 means keep forever.
	archiveCamera(t, env, "archcam", 0)
	env.insertTestRecording(t, "arch-old", "archcam", "archcam/seg1.mp4", time.Now().UTC().Add(-400*24*time.Hour), false)
	markRecordingArchived(t, env, "arch-old")

	cfg := defaultCleanupConfig()
	cm, err := NewCleanupManager(env.db, env.store, cfg)
	require.NoError(t, err)
	require.NoError(t, cm.RunOnce(ctx))

	require.True(t, recordingExists(t, env, "arch-old"), "archive_retention_days=0 must keep recordings forever")
}

func TestArchivedRetentionCleanup_EmptyGroupTornDown(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t)
	defer env.close(t)

	archiveCamera(t, env, "archcam", 7)
	env.insertTestRecording(t, "arch-last", "archcam", "archcam/seg1.mp4", time.Now().UTC().Add(-10*24*time.Hour), false)
	markRecordingArchived(t, env, "arch-last")

	cfg := defaultCleanupConfig()
	cm, err := NewCleanupManager(env.db, env.store, cfg)
	require.NoError(t, err)
	require.NoError(t, cm.RunOnce(ctx))

	require.False(t, recordingExists(t, env, "arch-last"), "last expired recording should be deleted")

	// Empty archived group: camera row and directory are removed.
	archived, err := env.db.ListArchivedCameras(ctx)
	require.NoError(t, err)
	for _, cam := range archived {
		require.NotEqual(t, "archcam", cam.ID, "empty archived camera row should be deleted")
	}
	_, err = os.Stat(filepath.Join(env.store.RootDir(), "archcam"))
	require.True(t, os.IsNotExist(err), "empty archived camera directory should be deleted")
}

func TestArchivedRetentionCleanup_AIProcessingProtected(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t)
	defer env.close(t)

	archiveCamera(t, env, "archcam", 7)
	env.insertTestRecording(t, "arch-ai", "archcam", "archcam/seg1.mp4", time.Now().UTC().Add(-10*24*time.Hour), false)
	markRecordingArchived(t, env, "arch-ai")
	require.NoError(t, env.db.UpdateRecordingAIStatus(ctx, "arch-ai", "processing", ""))

	cfg := defaultCleanupConfig()
	cm, err := NewCleanupManager(env.db, env.store, cfg)
	require.NoError(t, err)
	require.NoError(t, cm.RunOnce(ctx))

	require.True(t, recordingExists(t, env, "arch-ai"),
		"recording being processed by MiBeeVision must never be deleted by retention")

	// Camera row must survive too — the recording is still there, so the group
	// is not empty.
	archived, err := env.db.ListArchivedCameras(ctx)
	require.NoError(t, err)
	found := false
	for _, cam := range archived {
		if cam.ID == "archcam" {
			found = true
		}
	}
	require.True(t, found, "archived camera with remaining recordings must not be removed")
}

// enqueueCompletedTaskOld inserts a transcode task marked completed with
// completed_at backdated past the given duration (deterministic — no sleeps).
func enqueueCompletedTaskOld(t *testing.T, env *testEnv, age time.Duration) {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, env.db.EnqueueTask(ctx, &storage.TranscodeTask{
		CameraID:     "cam1",
		RecordingID:  "rec-tc",
		InputPath:    "/tmp/in.mp4",
		InputFormat:  "h265",
		OutputPath:   "/tmp/out.mp4",
		OutputFormat: "h264",
	}))
	var id int64
	require.NoError(t, env.db.DB().QueryRowContext(ctx, `SELECT MAX(id) FROM transcoding_tasks;`).Scan(&id))
	require.NotZero(t, id)
	require.NoError(t, env.db.UpdateTaskStatus(ctx, id, "completed", 100, ""))
	_, err := env.db.DB().ExecContext(ctx,
		`UPDATE transcoding_tasks SET completed_at=? WHERE id=?;`,
		time.Now().UTC().Add(-age), id)
	require.NoError(t, err)
}

func TestTranscodeHistoryCleanup(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t)
	defer env.close(t)

	enqueueCompletedTaskOld(t, env, 2*time.Hour)

	cfg := defaultCleanupConfig()
	cm, err := NewCleanupManager(env.db, env.store, cfg)
	require.NoError(t, err)
	cm.SetTranscodeHistoryRetention(time.Hour)
	require.NoError(t, cm.RunOnce(ctx))

	_, total, err := env.db.ListTranscodeTasks(ctx, storage.TranscodeTaskFilter{})
	require.NoError(t, err)
	require.Equal(t, 0, total, "completed task older than retention should be deleted")
}

func TestTranscodeHistoryCleanup_DisabledByDefault(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t)
	defer env.close(t)

	enqueueCompletedTaskOld(t, env, 72*time.Hour)

	// No SetTranscodeHistoryRetention → retention 0 → disabled.
	cfg := defaultCleanupConfig()
	cm, err := NewCleanupManager(env.db, env.store, cfg)
	require.NoError(t, err)
	require.NoError(t, cm.RunOnce(ctx))

	_, total, err := env.db.ListTranscodeTasks(ctx, storage.TranscodeTaskFilter{})
	require.NoError(t, err)
	require.Equal(t, 1, total, "transcode history cleanup disabled (retention=0) must keep tasks")
}
