package storage

import (
	"context"
	"database/sql"
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
	err = d.db.QueryRowContext(ctx,
		"SELECT COUNT(*), COALESCE(SUM(file_size),0) FROM recordings WHERE camera_id=? AND archived=1",
		cameraID).Scan(&count, &totalSize)
	return
}

// GetCameraRecordingStats returns recording count and total file size for a non-archived camera.
func (d *DB) GetCameraRecordingStats(ctx context.Context, cameraID string) (count int, totalSize int64, err error) {
	err = d.db.QueryRowContext(ctx,
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
