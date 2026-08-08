package storage

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// ArchiveCameraDB marks a camera as archived in the database.
func (d *DB) ArchiveCameraDB(ctx context.Context, cameraID string) error {
	_, err := d.db.ExecContext(ctx,
		"UPDATE cameras SET archived=1, archived_at=datetime('now') WHERE id=?",
		cameraID)
	return err
}

// ArchiveAllRecordings marks all non-archived recordings for a camera as archived.
// Returns the number of rows affected.
func (d *DB) ArchiveAllRecordings(ctx context.Context, cameraID string) (int64, error) {
	result, err := d.db.ExecContext(ctx,
		"UPDATE recordings SET archived=1 WHERE camera_id=? AND archived=0",
		cameraID)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// GetArchiveGroupStats returns recording count and total file size for an archived camera.
func (d *DB) GetArchiveGroupStats(ctx context.Context, cameraID string) (count int, totalSize int64, err error) {
	err = d.readConn().QueryRowContext(ctx,
		"SELECT COUNT(*), COALESCE(SUM(file_size),0) FROM recordings WHERE camera_id=? AND archived=1",
		cameraID).Scan(&count, &totalSize)
	return
}

// GetCameraRecordingStats returns recording count and total file size for a non-archived camera.
func (d *DB) GetCameraRecordingStats(ctx context.Context, cameraID string) (count int, totalSize int64, err error) {
	err = d.readConn().QueryRowContext(ctx,
		"SELECT COUNT(*), COALESCE(SUM(file_size),0) FROM recordings WHERE camera_id=? AND archived=0",
		cameraID).Scan(&count, &totalSize)
	return
}

// SetArchiveRetention updates the archive_retention_days for an archived camera.
func (d *DB) SetArchiveRetention(ctx context.Context, cameraID string, retentionDays int) error {
	result, err := d.db.ExecContext(ctx,
		"UPDATE cameras SET archive_retention_days=? WHERE id=? AND archived=1",
		retentionDays, cameraID)
	if err != nil {
		return err
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// ArchiveCleanupTask tracks an async delete of a camera's recordings.
// Status is one of: pending | running | done | failed.
type ArchiveCleanupTask struct {
	CameraID       string
	CameraName     string
	RecordingCount int
	TotalSize      int64
	Status         string
	Error          string
	CreatedAt      time.Time
	CompletedAt    *time.Time
}

// CreateArchiveCleanupTask inserts or replaces a cleanup task row (camera_id is the PK).
func (d *DB) CreateArchiveCleanupTask(ctx context.Context, task ArchiveCleanupTask) error {
	createdAt := any(nil)
	if !task.CreatedAt.IsZero() {
		createdAt = timeToDB(task.CreatedAt)
	}
	completedAt := any(nil)
	if task.CompletedAt != nil {
		completedAt = timeToDB(*task.CompletedAt)
	}
	q := `INSERT OR REPLACE INTO archive_cleanup_tasks (
		camera_id, camera_name, recording_count, total_size, status, error, created_at, completed_at
	) VALUES (?, ?, ?, ?, ?, ?, COALESCE(?, datetime('now')), ?);`
	_, err := d.db.ExecContext(ctx, q,
		task.CameraID, task.CameraName, task.RecordingCount, task.TotalSize,
		task.Status, task.Error, createdAt, completedAt)
	if err != nil {
		return fmt.Errorf("create archive cleanup task: %w", err)
	}
	return nil
}

// ListActiveArchiveCleanupTasks returns all pending/running cleanup tasks, oldest first.
func (d *DB) ListActiveArchiveCleanupTasks(ctx context.Context) ([]ArchiveCleanupTask, error) {
	rows, err := d.readConn().QueryContext(ctx,
		`SELECT camera_id, camera_name, recording_count, total_size, status, error, created_at, completed_at
		 FROM archive_cleanup_tasks WHERE status IN ('pending','running') ORDER BY created_at`)
	if err != nil {
		return nil, fmt.Errorf("list active archive cleanup tasks: %w", err)
	}
	defer rows.Close()
	var tasks []ArchiveCleanupTask
	for rows.Next() {
		task, err := scanArchiveCleanupTask(rows)
		if err != nil {
			return nil, fmt.Errorf("scan archive cleanup task: %w", err)
		}
		tasks = append(tasks, task)
	}
	return tasks, rows.Err()
}

// ListRecentArchiveCleanupTasks returns up to 20 recently completed/failed tasks,
// newest first.
func (d *DB) ListRecentArchiveCleanupTasks(ctx context.Context, since time.Time) ([]ArchiveCleanupTask, error) {
	rows, err := d.readConn().QueryContext(ctx,
		`SELECT camera_id, camera_name, recording_count, total_size, status, error, created_at, completed_at
		 FROM archive_cleanup_tasks WHERE status IN ('done','failed') AND completed_at > ?
		 ORDER BY completed_at DESC LIMIT 20`,
		timeToDB(since))
	if err != nil {
		return nil, fmt.Errorf("list recent archive cleanup tasks: %w", err)
	}
	defer rows.Close()
	var tasks []ArchiveCleanupTask
	for rows.Next() {
		task, err := scanArchiveCleanupTask(rows)
		if err != nil {
			return nil, fmt.Errorf("scan archive cleanup task: %w", err)
		}
		tasks = append(tasks, task)
	}
	return tasks, rows.Err()
}

// HasArchiveCleanupTaskForCamera checks if any cleanup task exists for a camera.
func (d *DB) HasArchiveCleanupTaskForCamera(ctx context.Context, cameraID string) (bool, error) {
	var count int
	err := d.readConn().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM archive_cleanup_tasks WHERE camera_id = ?`, cameraID).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("check archive cleanup task for camera: %w", err)
		}
	return count > 0, nil
}

// UpdateArchiveCleanupTaskStatus updates status, error, and (for terminal states)
// completed_at of a cleanup task.
func (d *DB) UpdateArchiveCleanupTaskStatus(ctx context.Context, cameraID, status, errMsg string) error {
	completedAt := any(nil)
	if status == "done" || status == "failed" {
		completedAt = timeToDB(time.Now().UTC())
	}
	q := `UPDATE archive_cleanup_tasks SET status=?, error=?, completed_at=COALESCE(?, completed_at) WHERE camera_id=?;`
	_, err := d.db.ExecContext(ctx, q, status, errMsg, completedAt, cameraID)
	if err != nil {
		return fmt.Errorf("update archive cleanup task status: %w", err)
	}
	return nil
}

// PruneCompletedArchiveCleanupTasks deletes completed/failed tasks that finished
// before the given cutoff.
func (d *DB) PruneCompletedArchiveCleanupTasks(ctx context.Context, before time.Time) error {
	_, err := d.db.ExecContext(ctx,
		`DELETE FROM archive_cleanup_tasks WHERE status IN ('done','failed') AND completed_at < ?`,
		timeToDB(before))
	if err != nil {
		return fmt.Errorf("prune completed archive cleanup tasks: %w", err)
	}
	return nil
}

// DeleteRecordingsByCamera deletes all recording rows for a camera.
// Used by archive cleanup to remove recordings after their files are deleted.
func (d *DB) DeleteRecordingsByCamera(ctx context.Context, cameraID string) error {
	_, err := d.db.ExecContext(ctx, `DELETE FROM recordings WHERE camera_id=?`, cameraID)
	if err != nil {
		return fmt.Errorf("delete recordings by camera: %w", err)
	}
	return nil
}

// scanArchiveCleanupTask scans a row into an ArchiveCleanupTask.
func scanArchiveCleanupTask(s scanner) (ArchiveCleanupTask, error) {
	var t ArchiveCleanupTask
	var createdAt, completedAt sql.NullString
	if err := s.Scan(
		&t.CameraID, &t.CameraName, &t.RecordingCount, &t.TotalSize,
		&t.Status, &t.Error, &createdAt, &completedAt,
	); err != nil {
		return t, err
	}
	t.CreatedAt = scanTime(createdAt)
	if parsed := scanTime(completedAt); !parsed.IsZero() {
		t.CompletedAt = &parsed
	}
	return t, nil
}
