package storage

import (
	"context"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/stretchr/testify/require"
)

func TestCreateAndListActiveArchiveCleanupTask(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	err := db.CreateArchiveCleanupTask(ctx, ArchiveCleanupTask{
		CameraID:       "cam-1",
		CameraName:     "Front Door",
		RecordingCount: 7,
		TotalSize:      4096,
		Status:         "pending",
	})
	require.NoError(t, err)

	active, err := db.ListActiveArchiveCleanupTasks(ctx)
	require.NoError(t, err)
	require.Len(t, active, 1)
	task := active[0]
	require.Equal(t, "cam-1", task.CameraID)
	require.Equal(t, "Front Door", task.CameraName)
	require.Equal(t, 7, task.RecordingCount)
	require.Equal(t, int64(4096), task.TotalSize)
	require.Equal(t, "pending", task.Status)
	require.False(t, task.CreatedAt.IsZero(), "created_at should be populated")
	require.Nil(t, task.CompletedAt, "completed_at should be nil for a pending task")
}

func TestCreateArchiveCleanupTaskDuplicate(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	require.NoError(t, db.CreateArchiveCleanupTask(ctx, ArchiveCleanupTask{
		CameraID: "cam-1", CameraName: "First", RecordingCount: 1, TotalSize: 10, Status: "pending",
	}))
	// camera_id is the PK — INSERT OR REPLACE must overwrite without error.
	require.NoError(t, db.CreateArchiveCleanupTask(ctx, ArchiveCleanupTask{
		CameraID: "cam-1", CameraName: "Second", RecordingCount: 9, TotalSize: 42, Status: "pending",
	}))

	active, err := db.ListActiveArchiveCleanupTasks(ctx)
	require.NoError(t, err)
	require.Len(t, active, 1, "duplicate insert must replace, not append")
	require.Equal(t, "Second", active[0].CameraName)
	require.Equal(t, 9, active[0].RecordingCount)
	require.Equal(t, int64(42), active[0].TotalSize)
}

func TestUpdateArchiveCleanupTaskStatusDone(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	require.NoError(t, db.CreateArchiveCleanupTask(ctx, ArchiveCleanupTask{
		CameraID: "cam-1", CameraName: "Cam 1", RecordingCount: 3, TotalSize: 300, Status: "pending",
	}))
	require.NoError(t, db.UpdateArchiveCleanupTaskStatus(ctx, "cam-1", "done", ""))

	recent, err := db.ListRecentArchiveCleanupTasks(ctx, time.Now().Add(-time.Hour))
	require.NoError(t, err)
	require.Len(t, recent, 1)
	require.Equal(t, "done", recent[0].Status)
	require.NotNil(t, recent[0].CompletedAt, "completed_at must be set on done")
	require.False(t, recent[0].CompletedAt.IsZero())

	// Terminal tasks are no longer listed as active.
	active, err := db.ListActiveArchiveCleanupTasks(ctx)
	require.NoError(t, err)
	require.Empty(t, active)
}

func TestUpdateArchiveCleanupTaskStatusFailed(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	require.NoError(t, db.CreateArchiveCleanupTask(ctx, ArchiveCleanupTask{
		CameraID: "cam-1", CameraName: "Cam 1", RecordingCount: 3, TotalSize: 300, Status: "pending",
	}))
	require.NoError(t, db.UpdateArchiveCleanupTaskStatus(ctx, "cam-1", "failed", "disk error: boom"))

	recent, err := db.ListRecentArchiveCleanupTasks(ctx, time.Now().Add(-time.Hour))
	require.NoError(t, err)
	require.Len(t, recent, 1)
	require.Equal(t, "failed", recent[0].Status)
	require.Equal(t, "disk error: boom", recent[0].Error)
	require.NotNil(t, recent[0].CompletedAt, "completed_at must be set on failed")
}

func TestListRecentArchiveCleanupTasks(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	// First done task.
	require.NoError(t, db.CreateArchiveCleanupTask(ctx, ArchiveCleanupTask{
		CameraID: "cam-a", CameraName: "Cam A", RecordingCount: 1, TotalSize: 100, Status: "pending",
	}))
	require.NoError(t, db.UpdateArchiveCleanupTaskStatus(ctx, "cam-a", "done", ""))
	recent, err := db.ListRecentArchiveCleanupTasks(ctx, time.Now().Add(-time.Hour))
	require.NoError(t, err)
	require.Len(t, recent, 1)

	// Second done task → both returned, newest completed first.
	require.NoError(t, db.CreateArchiveCleanupTask(ctx, ArchiveCleanupTask{
		CameraID: "cam-b", CameraName: "Cam B", RecordingCount: 2, TotalSize: 200, Status: "pending",
	}))
	require.NoError(t, db.UpdateArchiveCleanupTaskStatus(ctx, "cam-b", "done", ""))
	recent, err = db.ListRecentArchiveCleanupTasks(ctx, time.Now().Add(-time.Hour))
	require.NoError(t, err)
	require.Len(t, recent, 2)
	require.Equal(t, "cam-b", recent[0].CameraID, "newest completed task listed first")
	require.Equal(t, "cam-a", recent[1].CameraID)
}

func TestPruneCompletedArchiveCleanupTasks(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	// Old done task (completed 2h ago) — should be pruned.
	old := time.Now().Add(-2 * time.Hour)
	require.NoError(t, db.CreateArchiveCleanupTask(ctx, ArchiveCleanupTask{
		CameraID: "cam-old", CameraName: "Old", RecordingCount: 1, TotalSize: 100, Status: "done", CompletedAt: &old,
	}))
	// Recent done task — must survive the prune.
	require.NoError(t, db.CreateArchiveCleanupTask(ctx, ArchiveCleanupTask{
		CameraID: "cam-recent", CameraName: "Recent", RecordingCount: 1, TotalSize: 100, Status: "pending",
	}))
	require.NoError(t, db.UpdateArchiveCleanupTaskStatus(ctx, "cam-recent", "done", ""))

	// Cutoff strictly before the recent task's completed_at (set moments ago) so
	// only the 2h-old task falls below it.
	require.NoError(t, db.PruneCompletedArchiveCleanupTasks(ctx, time.Now().Add(-time.Minute)))

	recent, err := db.ListRecentArchiveCleanupTasks(ctx, time.Now().Add(-3*time.Hour))
	require.NoError(t, err)
	require.Len(t, recent, 1, "old task pruned, recent task retained")
	require.Equal(t, "cam-recent", recent[0].CameraID)
}

func TestDeleteRecordingsByCamera(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	now := time.Now()
	require.NoError(t, db.InsertRecording(ctx, &model.Recording{ID: "r1", CameraID: "cam-1", FilePath: "/a.mp4", Format: model.FormatH264, StartedAt: now}))
	require.NoError(t, db.InsertRecording(ctx, &model.Recording{ID: "r2", CameraID: "cam-1", FilePath: "/b.mp4", Format: model.FormatH264, StartedAt: now}))
	require.NoError(t, db.InsertRecording(ctx, &model.Recording{ID: "r3", CameraID: "cam-2", FilePath: "/c.mp4", Format: model.FormatH264, StartedAt: now}))

	require.NoError(t, db.DeleteRecordingsByCamera(ctx, "cam-1"))

	got, err := db.GetRecording(ctx, "r1")
	require.NoError(t, err)
	require.Nil(t, got)
	got, err = db.GetRecording(ctx, "r2")
	require.NoError(t, err)
	require.Nil(t, got)
	// Recordings of other cameras must be untouched.
	got, err = db.GetRecording(ctx, "r3")
	require.NoError(t, err)
	require.NotNil(t, got)
}
