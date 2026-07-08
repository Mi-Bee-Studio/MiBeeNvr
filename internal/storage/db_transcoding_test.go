package storage

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestTranscodeTask_EnqueueDequeue(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	now := time.Now().UTC()
	task := &TranscodeTask{
		CameraID:     "cam1",
		RecordingID:  "rec-001",
		InputPath:    "/recordings/cam1/seg1.mp4",
		InputFormat:  "h264",
		OutputPath:   "/recordings/cam1/seg1_hevc.mp4",
		OutputFormat: "hevc",
		CreatedAt:    formatTime(now),
	}

	err := db.EnqueueTask(ctx, task)
	require.NoError(t, err)
	require.NotZero(t, task.ID, "EnqueueTask should populate ID")

	got, err := db.DequeueTask(ctx)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, task.ID, got.ID)
	require.Equal(t, "cam1", got.CameraID)
	require.Equal(t, "rec-001", got.RecordingID)
	require.Equal(t, "/recordings/cam1/seg1.mp4", got.InputPath)
	require.Equal(t, "h264", got.InputFormat)
	require.Equal(t, "/recordings/cam1/seg1_hevc.mp4", got.OutputPath)
	require.Equal(t, "hevc", got.OutputFormat)
	require.Equal(t, "running", got.Status, "DequeueTask should claim the task")
	require.Equal(t, float64(0.0), got.Progress)
	require.False(t, got.Error.Valid, "Error should be NULL for new task")
	require.False(t, got.OriginalDeleted)
	require.True(t, got.StartedAt.Valid, "started_at should be set when task is claimed")
}

func TestTranscodeTask_UpdateStatus(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	task := &TranscodeTask{
		CameraID:     "cam1",
		RecordingID:  "rec-002",
		InputPath:    "/in.mp4",
		InputFormat:  "h264",
		OutputPath:   "/out.mp4",
		OutputFormat: "hevc",
		CreatedAt:    formatTime(time.Now().UTC()),
	}
	err := db.EnqueueTask(ctx, task)
	require.NoError(t, err)

	err = db.UpdateTaskStatus(ctx, task.ID, "running", 0.5, "")
	require.NoError(t, err)

	got, err := db.GetTaskByID(ctx, task.ID)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, "running", got.Status)
	require.Equal(t, float64(0.5), got.Progress)
	require.True(t, got.StartedAt.Valid, "started_at should be set when status becomes running")

	err = db.UpdateTaskStatus(ctx, task.ID, "completed", 1.0, "")
	require.NoError(t, err)

	got2, err := db.GetTaskByID(ctx, task.ID)
	require.NoError(t, err)
	require.NotNil(t, got2)
	require.Equal(t, "completed", got2.Status)
	require.Equal(t, float64(1.0), got2.Progress)
	require.True(t, got2.CompletedAt.Valid, "completed_at should be set when status becomes completed")
}

func TestTranscodeTask_Cancel(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	task := &TranscodeTask{
		CameraID:     "cam1",
		RecordingID:  "rec-003",
		InputPath:    "/in.mp4",
		InputFormat:  "h264",
		OutputPath:   "/out.mp4",
		OutputFormat: "hevc",
		CreatedAt:    formatTime(time.Now().UTC()),
	}
	err := db.EnqueueTask(ctx, task)
	require.NoError(t, err)

	err = db.CancelTask(ctx, task.ID)
	require.NoError(t, err)

	got, err := db.GetTaskByID(ctx, task.ID)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, "cancelled", got.Status)
	require.True(t, got.CompletedAt.Valid, "completed_at should be set on cancellation")

	// Cancelling an already-cancelled task should not error
	err = db.CancelTask(ctx, task.ID)
	require.NoError(t, err)
}

func TestTranscodeTask_GetByStatus(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	now := formatTime(time.Now().UTC())
	for range 3 {
		task := &TranscodeTask{
			CameraID:     "cam1",
			RecordingID:  "rec-00%d",
			InputPath:    "/in.mp4",
			InputFormat:  "h264",
			OutputPath:   "/out.mp4",
			OutputFormat: "hevc",
			CreatedAt:    now,
		}
		require.NoError(t, db.EnqueueTask(ctx, task))
	}

	pending, err := db.GetTasksByStatus(ctx, "pending")
	require.NoError(t, err)
	require.Len(t, pending, 3)

	// Dequeue one (atomically claims it)
	claimed, err := db.DequeueTask(ctx)
	require.NoError(t, err)
	require.NotNil(t, claimed)
	require.Equal(t, "running", claimed.Status, "DequeueTask should claim the task")

	// Now 2 pending, 1 running
	pending2, err := db.GetTasksByStatus(ctx, "pending")
	require.NoError(t, err)
	require.Len(t, pending2, 2)
}

func TestTranscodeTask_GetByID(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	task := &TranscodeTask{
		CameraID:     "cam2",
		RecordingID:  "rec-010",
		InputPath:    "/in.mp4",
		InputFormat:  "h264",
		OutputPath:   "/out.mp4",
		OutputFormat: "hevc",
		CreatedAt:    formatTime(time.Now().UTC()),
	}
	err := db.EnqueueTask(ctx, task)
	require.NoError(t, err)

	got, err := db.GetTaskByID(ctx, task.ID)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, task.ID, got.ID)
	require.Equal(t, "cam2", got.CameraID)

	// Non-existent ID returns nil
	notFound, err := db.GetTaskByID(ctx, 99999)
	require.NoError(t, err)
	require.Nil(t, notFound)
}

func TestTranscodeTask_DeleteCompleted(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	now := time.Now().UTC()

	// Old completed task (completed 2 hours ago)
	oldTask := &TranscodeTask{
		CameraID:     "cam1",
		RecordingID:  "rec-old",
		InputPath:    "/old.mp4",
		InputFormat:  "h264",
		OutputPath:   "/old_out.mp4",
		OutputFormat: "hevc",
		CreatedAt:    formatTime(now.Add(-3 * time.Hour)),
	}
	require.NoError(t, db.EnqueueTask(ctx, oldTask))
	require.NoError(t, db.UpdateTaskStatus(ctx, oldTask.ID, "completed", 1.0, ""))
	// Set completed_at to the past (2 hours ago) so it's older than the 1h threshold
	pastTime := formatTime(now.Add(-2 * time.Hour))
	_, err := db.db.ExecContext(ctx, "UPDATE transcoding_tasks SET completed_at = ? WHERE id = ?", pastTime, oldTask.ID)
	require.NoError(t, err)

	// Recent completed task (completed 5 minutes ago)
	recentTask := &TranscodeTask{
		CameraID:     "cam1",
		RecordingID:  "rec-recent",
		InputPath:    "/recent.mp4",
		InputFormat:  "h264",
		OutputPath:   "/recent_out.mp4",
		OutputFormat: "hevc",
		CreatedAt:    formatTime(now.Add(-10 * time.Minute)),
	}
	require.NoError(t, db.EnqueueTask(ctx, recentTask))
	require.NoError(t, db.UpdateTaskStatus(ctx, recentTask.ID, "completed", 1.0, ""))

	// Pending task (should not be deleted)
	pendingTask := &TranscodeTask{
		CameraID:     "cam1",
		RecordingID:  "rec-pending",
		InputPath:    "/pending.mp4",
		InputFormat:  "h264",
		OutputPath:   "/pending_out.mp4",
		OutputFormat: "hevc",
		CreatedAt:    formatTime(now.Add(-3 * time.Hour)),
	}
	require.NoError(t, db.EnqueueTask(ctx, pendingTask))

	// Delete completed tasks older than 1 hour
	deleted, err := db.DeleteCompletedTasks(ctx, time.Hour)
	require.NoError(t, err)
	require.Equal(t, int64(1), deleted, "only the old completed task should be deleted")

	// Old task should be gone
	got, err := db.GetTaskByID(ctx, oldTask.ID)
	require.NoError(t, err)
	require.Nil(t, got)

	// Recent completed task still exists
	got2, err := db.GetTaskByID(ctx, recentTask.ID)
	require.NoError(t, err)
	require.NotNil(t, got2)

	// Pending task still exists
	got3, err := db.GetTaskByID(ctx, pendingTask.ID)
	require.NoError(t, err)
	require.NotNil(t, got3)
}

func TestTranscodeTask_DequeueOrder(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	// Insert 3 tasks with staggered creation times
	base := time.Now().UTC().Add(-time.Hour)
	for i := range 3 {
		createdAt := formatTime(base.Add(time.Duration(i) * 10 * time.Minute))
		task := &TranscodeTask{
			CameraID:     "cam1",
			RecordingID:  fmt.Sprintf("rec-fifo-%d", i),
			InputPath:    "/in.mp4",
			InputFormat:  "h264",
			OutputPath:   "/out.mp4",
			OutputFormat: "hevc",
			CreatedAt:    createdAt,
		}
		require.NoError(t, db.EnqueueTask(ctx, task))
	}
	// Count rows in table
	var count int
	require.NoError(t, db.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM transcoding_tasks").Scan(&count))

	// Dequeue should return in FIFO order (oldest first)
	task1, err := db.DequeueTask(ctx)
	require.NoError(t, err)
	require.NotNil(t, task1)

	task2, err := db.DequeueTask(ctx)
	require.NoError(t, err)
	require.NotNil(t, task2)

	task3, err := db.DequeueTask(ctx)
	require.NoError(t, err)
	require.NotNil(t, task3)

	t.Logf("task1: ID=%d, CreatedAt=%s", task1.ID, task1.CreatedAt)
	t.Logf("task2: ID=%d, CreatedAt=%s", task2.ID, task2.CreatedAt)
	t.Logf("task3: ID=%d, CreatedAt=%s", task3.ID, task3.CreatedAt)
	require.True(t, task1.ID < task2.ID, "tasks should be dequeued in FIFO order (oldest first)")
	require.True(t, task2.ID < task3.ID, "tasks should be dequeued in FIFO order")

	// No more pending tasks
	_, err = db.DequeueTask(ctx)
	require.Error(t, err, "DequeueTask should return error when no pending tasks")
	require.ErrorIs(t, err, sql.ErrNoRows)
}

func TestTranscodeTask_UpdateStatusError(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	task := &TranscodeTask{
		CameraID:     "cam1",
		RecordingID:  "rec-err",
		InputPath:    "/in.mp4",
		InputFormat:  "h264",
		OutputPath:   "/out.mp4",
		OutputFormat: "hevc",
		CreatedAt:    formatTime(time.Now().UTC()),
	}
	require.NoError(t, db.EnqueueTask(ctx, task))

	// Update with error message
	require.NoError(t, db.UpdateTaskStatus(ctx, task.ID, "failed", 0.3, "out of disk space"))

	got, err := db.GetTaskByID(ctx, task.ID)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, "failed", got.Status)
	require.True(t, got.Error.Valid, "Error should be set")
	require.Equal(t, "out of disk space", got.Error.String)
	require.Equal(t, float64(0.3), got.Progress)
	require.True(t, got.CompletedAt.Valid, "completed_at should be set for failed status")
}

func TestTranscodeTask_EnqueueTasksBatch_Empty(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	t.Run("nil slice", func(t *testing.T) {
		err := db.EnqueueTasksBatch(ctx, nil)
		require.NoError(t, err)
	})

	t.Run("empty slice", func(t *testing.T) {
		err := db.EnqueueTasksBatch(ctx, []TranscodeTask{})
		require.NoError(t, err)
	})
}

func TestTranscodeTask_EnqueueTasksBatch_Single(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	now := formatTime(time.Now().UTC())

	tasks := []TranscodeTask{{
		CameraID:     "cam1",
		RecordingID:  "rec-batch-1",
		InputPath:    "/recordings/cam1/seg1.mp4",
		InputFormat:  "h264",
		OutputPath:   "/recordings/cam1/seg1_transcoded.mp4",
		OutputFormat: "hevc",
		CreatedAt:    now,
	}}

	err := db.EnqueueTasksBatch(ctx, tasks)
	require.NoError(t, err)

	// Verify count
	var count int
	require.NoError(t, db.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM transcoding_tasks").Scan(&count))
	require.Equal(t, 1, count)

	// Verify dequeued task matches
	got, err := db.DequeueTask(ctx)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, "cam1", got.CameraID)
	require.Equal(t, "rec-batch-1", got.RecordingID)
	require.Equal(t, "/recordings/cam1/seg1.mp4", got.InputPath)
	require.Equal(t, "h264", got.InputFormat)
	require.Equal(t, "/recordings/cam1/seg1_transcoded.mp4", got.OutputPath)
	require.Equal(t, "hevc", got.OutputFormat)
	require.Equal(t, "running", got.Status, "DequeueTask should claim the task")
	require.False(t, got.OriginalDeleted)
	require.True(t, got.StartedAt.Valid, "started_at should be set when task is claimed")
}

func TestTranscodeTask_EnqueueTasksBatch_Multiple(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	now := formatTime(time.Now().UTC())

	tasks := make([]TranscodeTask, 5)
	for i := range 5 {
		tasks[i] = TranscodeTask{
			CameraID:     "cam1",
			RecordingID:  fmt.Sprintf("rec-batch-%d", i),
			InputPath:    "/in.mp4",
			InputFormat:  "h264",
			OutputPath:   "/out.mp4",
			OutputFormat: "hevc",
			CreatedAt:    now,
		}
	}

	err := db.EnqueueTasksBatch(ctx, tasks)
	require.NoError(t, err)

	// Verify all 5 inserted
	var count int
	require.NoError(t, db.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM transcoding_tasks").Scan(&count))
	require.Equal(t, 5, count)

	// Verify all can be dequeued in FIFO order
	for i := range 5 {
		got, err := db.DequeueTask(ctx)
		require.NoError(t, err)
		require.NotNil(t, got)
		require.Equal(t, fmt.Sprintf("rec-batch-%d", i), got.RecordingID)
	}

	// No more pending tasks
	_, err = db.DequeueTask(ctx)
	require.Error(t, err)
	require.ErrorIs(t, err, sql.ErrNoRows)
}

func TestTranscodeTask_EnqueueTasksBatch_RollbackOnError(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	// Use a cancelled context to trigger rollback
	cancelCtx, cancel := context.WithCancel(ctx)
	cancel()

	tasks := []TranscodeTask{{
		CameraID:     "cam1",
		RecordingID:  "rec-rollback",
		InputPath:    "/in.mp4",
		InputFormat:  "h264",
		OutputPath:   "/out.mp4",
		OutputFormat: "hevc",
		CreatedAt:    formatTime(time.Now().UTC()),
	}}

	err := db.EnqueueTasksBatch(cancelCtx, tasks)
	require.Error(t, err, "should fail with cancelled context")

	// Verify no rows were inserted (transaction rolled back)
	var count int
	require.NoError(t, db.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM transcoding_tasks").Scan(&count))
	require.Zero(t, count, "no rows should remain after rollback")
}
