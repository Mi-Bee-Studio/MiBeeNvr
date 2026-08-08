package cleanup

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
	"github.com/stretchr/testify/require"
)

// createPendingTask inserts a pending cleanup task row for the given camera.
func createPendingTask(t *testing.T, db *storage.DB, cameraID string) {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, db.CreateArchiveCleanupTask(ctx, storage.ArchiveCleanupTask{
		CameraID:       cameraID,
		CameraName:     "Test Camera",
		RecordingCount: 2,
		TotalSize:      2048,
		Status:         "pending",
	}))
}

// recentDoneTask fetches the single recent (done/failed) task for a camera, if any.
func recentDoneTask(t *testing.T, db *storage.DB, cameraID string) *storage.ArchiveCleanupTask {
	t.Helper()
	ctx := context.Background()
	recent, err := db.ListRecentArchiveCleanupTasks(ctx, time.Now().Add(-time.Hour))
	require.NoError(t, err)
	for i := range recent {
		if recent[i].CameraID == cameraID {
			return &recent[i]
		}
	}
	return nil
}

func TestProcessTask_HappyPath(t *testing.T) {
	env := newTestEnv(t)
	defer env.close(t)
	deleter := NewArchiveDeleter(env.db, env.store)
	ctx := context.Background()
	cameraID := "cam1" // inserted by newTestEnv

	// Camera directory with a segment file on disk.
	require.NoError(t, env.store.EnsureCameraDir(cameraID))
	segPath := filepath.Join(env.store.RootDir(), cameraID, "seg.mp4")
	require.NoError(t, os.WriteFile(segPath, []byte("fake-data"), 0o644))

	// Two recordings for the camera.
	require.NoError(t, env.db.InsertRecording(ctx, &model.Recording{
		ID: "rec-1", CameraID: cameraID, FilePath: segPath,
		Format: model.FormatH264, StartedAt: time.Now().Add(-time.Hour), EndedAt: time.Now(),
	}))
	require.NoError(t, env.db.InsertRecording(ctx, &model.Recording{
		ID: "rec-2", CameraID: cameraID, FilePath: filepath.Join(env.store.RootDir(), cameraID, "seg2.mp4"),
		Format: model.FormatH264, StartedAt: time.Now().Add(-2 * time.Hour), EndedAt: time.Now().Add(-time.Hour),
	}))
	createPendingTask(t, env.db, cameraID)

	deleter.processTask(ctx, storage.ArchiveCleanupTask{CameraID: cameraID, Status: "pending"})

	// Recordings gone from DB.
	got, err := env.db.GetRecording(ctx, "rec-1")
	require.NoError(t, err)
	require.Nil(t, got)
	got, err = env.db.GetRecording(ctx, "rec-2")
	require.NoError(t, err)
	require.Nil(t, got)

	// Camera directory gone from disk.
	_, err = os.Stat(filepath.Join(env.store.RootDir(), cameraID))
	require.True(t, os.IsNotExist(err), "camera dir should be removed")

	// Camera row gone.
	cam, err := env.db.GetCamera(ctx, cameraID)
	require.NoError(t, err)
	require.Nil(t, cam)

	// Task marked done.
	task := recentDoneTask(t, env.db, cameraID)
	require.NotNil(t, task, "task should be listed as recent")
	require.Equal(t, "done", task.Status)
}

func TestRecoverStaleTasks(t *testing.T) {
	env := newTestEnv(t)
	defer env.close(t)
	deleter := NewArchiveDeleter(env.db, env.store)
	ctx := context.Background()

	// Simulate a crash mid-task: task left in 'running'.
	require.NoError(t, env.db.CreateArchiveCleanupTask(ctx, storage.ArchiveCleanupTask{
		CameraID: "cam1", CameraName: "Test Camera", RecordingCount: 2, TotalSize: 2048, Status: "running",
	}))
	// A pending task must be left untouched.
	require.NoError(t, env.db.CreateArchiveCleanupTask(ctx, storage.ArchiveCleanupTask{
		CameraID: "cam2", CameraName: "Cam 2", RecordingCount: 1, TotalSize: 1024, Status: "pending",
	}))

	deleter.recoverStaleTasks(ctx)

	active, err := env.db.ListActiveArchiveCleanupTasks(ctx)
	require.NoError(t, err)
	require.Len(t, active, 2)
	statuses := make(map[string]string, len(active))
	for _, task := range active {
		statuses[task.CameraID] = task.Status
	}
	require.Equal(t, "pending", statuses["cam1"], "stale running task reset to pending")
	require.Equal(t, "pending", statuses["cam2"], "pending task left untouched")
}

func TestProcessTask_MissingCameraDir(t *testing.T) {
	env := newTestEnv(t)
	defer env.close(t)
	deleter := NewArchiveDeleter(env.db, env.store)
	ctx := context.Background()
	cameraID := "cam1"

	// Recording row exists but its camera directory does not exist on disk.
	require.NoError(t, env.db.InsertRecording(ctx, &model.Recording{
		ID: "rec-1", CameraID: cameraID, FilePath: filepath.Join(env.store.RootDir(), cameraID, "seg.mp4"),
		Format: model.FormatH264, StartedAt: time.Now().Add(-time.Hour), EndedAt: time.Now(),
	}))
	createPendingTask(t, env.db, cameraID)

	deleter.processTask(ctx, storage.ArchiveCleanupTask{CameraID: cameraID, Status: "pending"})

	// Recordings are still deleted (the primary artifact); a missing camera dir
	// is non-fatal and the task still finishes as done.
	got, err := env.db.GetRecording(ctx, "rec-1")
	require.NoError(t, err)
	require.Nil(t, got)
	task := recentDoneTask(t, env.db, cameraID)
	require.NotNil(t, task)
	require.Equal(t, "done", task.Status)
}

func TestProcessTask_Idempotent(t *testing.T) {
	env := newTestEnv(t)
	defer env.close(t)
	deleter := NewArchiveDeleter(env.db, env.store)
	ctx := context.Background()
	cameraID := "cam1"

	require.NoError(t, env.store.EnsureCameraDir(cameraID))
	require.NoError(t, env.db.InsertRecording(ctx, &model.Recording{
		ID: "rec-1", CameraID: cameraID, FilePath: filepath.Join(env.store.RootDir(), cameraID, "seg.mp4"),
		Format: model.FormatH264, StartedAt: time.Now().Add(-time.Hour), EndedAt: time.Now(),
	}))
	createPendingTask(t, env.db, cameraID)

	task := storage.ArchiveCleanupTask{CameraID: cameraID, Status: "pending"}
	deleter.processTask(ctx, task)
	// Second run: recordings/dir/row are already gone — must not error and must
	// still complete as done.
	deleter.processTask(ctx, task)

	recent, err := env.db.ListRecentArchiveCleanupTasks(ctx, time.Now().Add(-time.Hour))
	require.NoError(t, err)
	require.Len(t, recent, 1, "task row is upserted, not duplicated")
	require.Equal(t, "done", recent[0].Status)
	require.NotNil(t, recent[0].CompletedAt)
}
