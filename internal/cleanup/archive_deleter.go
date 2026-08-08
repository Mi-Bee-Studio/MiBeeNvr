package cleanup

// This file defines ArchiveDeleter, a background worker that polls the
// archive_cleanup_tasks table and processes pending archive cleanup tasks
// (delete recordings, camera directory, and camera row). It implements
// pkg/app.Service and recovers tasks stuck in 'running' state after a crash.

import (
	"context"
	"log/slog"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
)

var archiveDeleterLogger = slog.Default().With("component", "archive-deleter")

// ArchiveDeleter polls for pending archive cleanup tasks and processes them
// serially (disk I/O is the bottleneck). Tasks are tracked in the
// archive_cleanup_tasks table; on startup any task left in 'running' is
// reset to 'pending' so the loop picks it up again (crash recovery).
type ArchiveDeleter struct {
	db     *storage.DB
	store  *storage.Manager
	cancel context.CancelFunc
	done   chan struct{}
}

// NewArchiveDeleter creates the archive cleanup worker.
func NewArchiveDeleter(db *storage.DB, store *storage.Manager) *ArchiveDeleter {
	return &ArchiveDeleter{
		db:    db,
		store: store,
	}
}

// Name returns the service name registered in pkg/app.
func (d *ArchiveDeleter) Name() string { return "archive-deleter" }

// Start launches the polling loop in a background goroutine and returns
// immediately. Long-running work runs on a ctx derived from Start and honors
// cancellation.
func (d *ArchiveDeleter) Start(ctx context.Context) error {
	ctx, d.cancel = context.WithCancel(ctx)
	d.done = make(chan struct{})
	go func() {
		defer close(d.done)
		d.recoverStaleTasks(ctx)
		d.runLoop(ctx)
	}()
	return nil
}

// Stop cancels the loop and joins the worker goroutine so no files are
// deleted under the storage tree after Stop returns.
func (d *ArchiveDeleter) Stop() error {
	if d.cancel != nil {
		d.cancel()
	}
	if d.done != nil {
		<-d.done
	}
	return nil
}

// runLoop polls for active cleanup tasks every 3 seconds and prunes
// completed-task history every ~3 minutes (every 60th poll).
func (d *ArchiveDeleter) runLoop(ctx context.Context) {
	pruneCounter := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(3 * time.Second):
		}
		tasks, err := d.db.ListActiveArchiveCleanupTasks(ctx)
		if err != nil {
			archiveDeleterLogger.Warn("failed to list archive cleanup tasks", "error", err)
			continue
		}
		for _, task := range tasks {
			d.processTask(ctx, task)
		}
		pruneCounter++
		if pruneCounter%60 == 0 { // ~3 minutes
			d.maybePrune(ctx)
		}
	}
}

// processTask runs the full cleanup chain: mark running, delete recordings
// (SQL), delete camera directory (disk), delete camera row, mark done.
// Directory/row deletions are non-fatal — the recordings are the primary
// artifact and their SQL deletion already succeeded.
func (d *ArchiveDeleter) processTask(ctx context.Context, task storage.ArchiveCleanupTask) {
	// 1. Mark running so a crash mid-task is recoverable.
	if err := d.db.UpdateArchiveCleanupTaskStatus(ctx, task.CameraID, "running", ""); err != nil {
		archiveDeleterLogger.Warn("failed to mark task running",
			"camera_id", task.CameraID, "error", err)
		// Continue — non-fatal; the task stays pending and is retried.
	}

	// 2. Delete recordings (single SQL, idempotent).
	if err := d.db.DeleteRecordingsByCamera(ctx, task.CameraID); err != nil {
		archiveDeleterLogger.Warn("failed to delete recordings",
			"camera_id", task.CameraID, "error", err)
		_ = d.db.UpdateArchiveCleanupTaskStatus(ctx, task.CameraID, "failed", err.Error())
		return
	}

	// 3. Delete camera directory (os.RemoveAll, idempotent).
	if err := d.store.DeleteCameraDir(task.CameraID); err != nil {
		archiveDeleterLogger.Warn("failed to remove camera directory",
			"camera_id", task.CameraID, "error", err)
		// Continue — non-fatal, recordings are already deleted.
	}

	// 4. Delete camera row (idempotent — no error if row already gone).
	if err := d.db.DeleteCameraRow(ctx, task.CameraID); err != nil {
		archiveDeleterLogger.Warn("failed to delete camera row",
			"camera_id", task.CameraID, "error", err)
		// Continue — non-fatal for cleanup.
	}

	if err := d.db.UpdateArchiveCleanupTaskStatus(ctx, task.CameraID, "done", ""); err != nil {
		archiveDeleterLogger.Warn("failed to mark task done",
			"camera_id", task.CameraID, "error", err)
	}
}

// recoverStaleTasks resets any task left in 'running' by a previous crash
// back to 'pending' so the loop re-processes it. Reuses existing CRUD methods.
func (d *ArchiveDeleter) recoverStaleTasks(ctx context.Context) {
	tasks, err := d.db.ListActiveArchiveCleanupTasks(ctx)
	if err != nil {
		archiveDeleterLogger.Warn("failed to list tasks for crash recovery", "error", err)
		return
	}
	for _, task := range tasks {
		if task.Status != "running" {
			continue
		}
		if err := d.db.UpdateArchiveCleanupTaskStatus(ctx, task.CameraID, "pending", ""); err != nil {
			archiveDeleterLogger.Warn("failed to reset stale task",
				"camera_id", task.CameraID, "error", err)
			continue
		}
		archiveDeleterLogger.Info("recovered stale archive cleanup task", "camera_id", task.CameraID)
	}
}

// maybePrune deletes completed/failed tasks older than one hour.
func (d *ArchiveDeleter) maybePrune(ctx context.Context) {
	cutoff := time.Now().Add(-1 * time.Hour)
	if err := d.db.PruneCompletedArchiveCleanupTasks(ctx, cutoff); err != nil {
		archiveDeleterLogger.Warn("failed to prune completed tasks", "error", err)
	}
}
