package storage

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
)

// mergeStatusFromBool converts the legacy Merged bool to a merge_status string.
func mergeStatusFromBool(merged bool) string {
	if merged {
		return model.MergeStatusMerged
	}
	return model.MergeStatusPending
}

// scanRecording scans the standard recording columns (with merge_status) from a row.
func scanRecording(r *model.Recording, startedAtStr, endedAtStr, mergeStatusStr sql.NullString) error {
	r.StartedAt = scanTime(startedAtStr)
	r.EndedAt = scanTime(endedAtStr)
	r.MergeStatus = mergeStatusFromBool(r.Merged)
	if mergeStatusStr.Valid && mergeStatusStr.String != "" {
		r.MergeStatus = mergeStatusStr.String
	}
	return nil
}

func (d *DB) InsertRecording(ctx context.Context, r *model.Recording) error {
	q := `INSERT INTO recordings(id, camera_id, file_path, format, started_at, ended_at, duration, file_size, frame_count, merged, merge_status) VALUES(?,?,?,?,?,?,?,?,?,?,?);`
	mergeStatus := mergeStatusFromBool(r.Merged)
	_, err := d.db.ExecContext(ctx, q, r.ID, r.CameraID, r.FilePath, r.Format, timeToDB(r.StartedAt), timeToDB(r.EndedAt), r.Duration, r.FileSize, r.FrameCount, r.Merged, mergeStatus)
	return err
}

// InsertRecordingWithRetry wraps InsertRecording with retry logic for SQLITE_BUSY errors.
// It retries up to maxRetries attempts with a fixed backoff between retries.
// Non-SQLITE_BUSY errors are returned immediately without retry.
func (d *DB) InsertRecordingWithRetry(ctx context.Context, r *model.Recording, maxRetries int, backoff time.Duration) error {
	var lastErr error
	for attempt := 0; attempt < maxRetries; attempt++ {
		if attempt > 0 {
			logger.Warn("insert recording: database busy, retrying",
				"camera_id", r.CameraID,
				"file_path", r.FilePath,
				"attempt", attempt,
				"max_retries", maxRetries,
				"error", lastErr,
			)
			time.Sleep(backoff)
		}
		err := d.InsertRecording(ctx, r)
		if err == nil {
			return nil
		}
		if !strings.Contains(err.Error(), "database is locked") && !strings.Contains(err.Error(), "SQLITE_BUSY") {
			return err
		}
		lastErr = err
	}
	logger.Error("insert recording: exhausted retries",
		"camera_id", r.CameraID,
		"file_path", r.FilePath,
		"max_retries", maxRetries,
		"error", lastErr,
	)
	return fmt.Errorf("insert recording failed after %d attempts: %w", maxRetries, lastErr)
}

func (d *DB) UpdateRecording(ctx context.Context, r *model.Recording) error {
	q := `UPDATE recordings SET camera_id=?, file_path=?, format=?, started_at=?, ended_at=?, duration=?, file_size=?, frame_count=?, merged=?, merge_status=? WHERE id=?;`
	_, err := d.db.ExecContext(ctx, q, r.CameraID, r.FilePath, r.Format, timeToDB(r.StartedAt), timeToDB(r.EndedAt), r.Duration, r.FileSize, r.FrameCount, r.Merged, r.MergeStatus, r.ID)
	return err
}

func (d *DB) GetRecording(ctx context.Context, id string) (*model.Recording, error) {
	row := d.db.QueryRowContext(ctx, `SELECT id, camera_id, file_path, format, started_at, ended_at, duration, file_size, frame_count, merged, merge_status, archived FROM recordings WHERE id=?;`, id)
	var r model.Recording
	var startedAtStr, endedAtStr sql.NullString
	if err := row.Scan(&r.ID, &r.CameraID, &r.FilePath, &r.Format, &startedAtStr, &endedAtStr, &r.Duration, &r.FileSize, &r.FrameCount, &r.Merged, &r.MergeStatus, &r.Archived); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	r.StartedAt = scanTime(startedAtStr)
	r.EndedAt = scanTime(endedAtStr)
	if r.MergeStatus == "" {
		r.MergeStatus = mergeStatusFromBool(r.Merged)
	}
	return &r, nil
}

func (d *DB) ListRecordings(ctx context.Context, filter model.RecordingFilter) ([]model.Recording, error) {
	where := []string{}
	args := []any{}
	if filter.CameraID != "" {
		where = append(where, "camera_id=?")
		args = append(args, filter.CameraID)
	}
	if filter.Merged != nil {
		where = append(where, "merge_status=?")
		args = append(args, mergeStatusFromBool(*filter.Merged))
	}
	if !filter.StartTime.IsZero() {
		where = append(where, "started_at>=?")
		args = append(args, formatTime(filter.StartTime))
	}
	if !filter.EndTime.IsZero() {
		where = append(where, "started_at<=?")
		args = append(args, formatTime(filter.EndTime))
	}
	if filter.Format != "" {
		where = append(where, "format=?")
		args = append(args, filter.Format)
	}
	if filter.Search != "" {
		pattern := "%" + escapeLike(filter.Search) + "%"
		where = append(where, "(camera_id LIKE ? ESCAPE '\\' OR format LIKE ? ESCAPE '\\' OR file_path LIKE ? ESCAPE '\\')")
		args = append(args, pattern, pattern, pattern)
	}
	if filter.Archived == nil {
		where = append(where, "archived=0")
	} else if *filter.Archived {
		where = append(where, "archived=1")
	} else {
		where = append(where, "archived=0")
	}
	sqlstr := "SELECT id, camera_id, file_path, format, started_at, ended_at, duration, file_size, frame_count, merged, merge_status, archived FROM recordings"
	if len(where) > 0 {
		sqlstr += " WHERE " + strings.Join(where, " AND ")
	}
	// Build ORDER BY clause from filter (whitelisted columns only)
	allowedSortFields := map[string]bool{"started_at": true, "duration": true, "file_size": true, "camera_id": true}
	sortBy := "started_at"
	if filter.SortBy != "" && allowedSortFields[filter.SortBy] {
		sortBy = filter.SortBy
	}
	sortOrder := "DESC"
	if strings.EqualFold(filter.SortOrder, "asc") {
		sortOrder = "ASC"
	}
	sqlstr += " ORDER BY " + sortBy + " " + sortOrder
	if filter.Limit > 0 {
		sqlstr += fmt.Sprintf(" LIMIT %d", filter.Limit)
	}
	if filter.Offset > 0 {
		sqlstr += fmt.Sprintf(" OFFSET %d", filter.Offset)
	}
	sqlstr += ";"
	rows, err := d.db.QueryContext(ctx, sqlstr, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var res []model.Recording
	for rows.Next() {
		var r model.Recording
		var startedAtStr, endedAtStr sql.NullString
		var mergeStatusStr sql.NullString
		if err := rows.Scan(&r.ID, &r.CameraID, &r.FilePath, &r.Format, &startedAtStr, &endedAtStr, &r.Duration, &r.FileSize, &r.FrameCount, &r.Merged, &mergeStatusStr, &r.Archived); err != nil {
			return nil, err
		}
		r.MergeStatus = mergeStatusFromBool(r.Merged)
		if mergeStatusStr.Valid && mergeStatusStr.String != "" {
			r.MergeStatus = mergeStatusStr.String
		}
		r.StartedAt = scanTime(startedAtStr)
		r.EndedAt = scanTime(endedAtStr)
		res = append(res, r)
	}
	return res, nil
}

func (d *DB) CountRecordingsWithFilter(ctx context.Context, filter model.RecordingFilter) (int, error) {
	where := []string{}
	args := []any{}
	if filter.CameraID != "" {
		where = append(where, "camera_id=?")
		args = append(args, filter.CameraID)
	}
	if filter.Merged != nil {
		where = append(where, "merge_status=?")
		args = append(args, mergeStatusFromBool(*filter.Merged))
	}
	if !filter.StartTime.IsZero() {
		where = append(where, "started_at>=?")
		args = append(args, formatTime(filter.StartTime))
	}
	if !filter.EndTime.IsZero() {
		where = append(where, "started_at<=?")
		args = append(args, formatTime(filter.EndTime))
	}
	if filter.Format != "" {
		where = append(where, "format=?")
		args = append(args, filter.Format)
	}
	if filter.Search != "" {
		pattern := "%" + escapeLike(filter.Search) + "%"
		where = append(where, "(camera_id LIKE ? ESCAPE '\\' OR format LIKE ? ESCAPE '\\' OR file_path LIKE ? ESCAPE '\\')")
		args = append(args, pattern, pattern, pattern)
	}
	if filter.Archived == nil {
		where = append(where, "archived=0")
	} else if *filter.Archived {
		where = append(where, "archived=1")
	} else {
		where = append(where, "archived=0")
	}
	sqlstr := "SELECT COUNT(*) FROM recordings"
	if len(where) > 0 {
		sqlstr += " WHERE " + strings.Join(where, " AND ")
	}
	var count int
	err := d.db.QueryRowContext(ctx, sqlstr, args...).Scan(&count)
	return count, err
}

// GetRecordingsByPathSet returns a set of file paths that exist in the recordings table.
// Used for orphan file reconciliation to determine which files are already registered.
func (d *DB) GetRecordingsByPathSet(ctx context.Context, paths []string) (map[string]bool, error) {
	result := make(map[string]bool)
	if len(paths) == 0 {
		return result, nil
	}
	placeholders := make([]string, len(paths))
	args := make([]interface{}, len(paths))
	for i, p := range paths {
		placeholders[i] = "?"
		args[i] = p
	}
	q := "SELECT file_path FROM recordings WHERE file_path IN (" + strings.Join(placeholders, ",") + ")"
	rows, err := d.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err == nil {
			result[p] = true
		}
	}
	return result, nil
}

// InsertOrphanRecordings batch-inserts orphan recording metadata using INSERT OR IGNORE.
// Returns the number of actually inserted rows (skips duplicates).
func (d *DB) InsertOrphanRecordings(ctx context.Context, recordings []*model.Recording) (int, error) {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	q := `INSERT OR IGNORE INTO recordings(id, camera_id, file_path, format, started_at, ended_at, duration, file_size, frame_count, merged, merge_status) VALUES(?,?,?,?,?,?,?,?,?,?,?);`
	inserted := 0
	for _, r := range recordings {
		result, err := tx.ExecContext(ctx, q, r.ID, r.CameraID, r.FilePath, r.Format, timeToDB(r.StartedAt), timeToDB(r.EndedAt), r.Duration, r.FileSize, r.FrameCount, r.Merged, mergeStatusFromBool(r.Merged))
		if err != nil {
			return 0, err
		}
		n, _ := result.RowsAffected()
		inserted += int(n)
	}
	return inserted, tx.Commit()
}

func (d *DB) DeleteRecording(ctx context.Context, id string) error {
	_, err := d.db.ExecContext(ctx, `DELETE FROM recordings WHERE id=?;`, id)
	return err
}

// DeleteRecordingsBatch deletes multiple recordings by ID using a transaction.
// Returns a slice of IDs that were successfully deleted.
func (d *DB) DeleteRecordingsBatch(ctx context.Context, ids []string) ([]string, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	deleted := []string{}
	for _, id := range ids {
		res, err := tx.ExecContext(ctx, `DELETE FROM recordings WHERE id=?;`, id)
		if err != nil {
			logger.Warn("batch delete: failed to delete recording", "id", id, "error", err)
			continue
		}
		n, _ := res.RowsAffected()
		if n > 0 {
			deleted = append(deleted, id)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return deleted, nil
}

func (d *DB) SetMerged(ctx context.Context, id string, merged bool) error {
	val := 0
	if merged {
		val = 1
	}
	mergeStatus := mergeStatusFromBool(merged)
	_, err := d.db.ExecContext(ctx, `UPDATE recordings SET merged=?, merge_status=? WHERE id=?;`, val, mergeStatus, id)
	return err
}

func (d *DB) CleanupIncomplete(ctx context.Context) error {
	_, err := d.db.ExecContext(ctx, `DELETE FROM recordings WHERE ended_at IS NULL;`)
	return err
}

func (d *DB) ListExpiredRecordings(ctx context.Context, retentionDays int) ([]model.Recording, error) {
	sqlstr := `SELECT id, camera_id, file_path, format, started_at, ended_at, duration, file_size, frame_count, merged, merge_status, archived FROM recordings WHERE ended_at IS NOT NULL AND archived=0 AND ended_at < datetime('now', '-' || ? || ' days') ORDER BY ended_at ASC;`
	rows, err := d.db.QueryContext(ctx, sqlstr, retentionDays)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var res []model.Recording
	for rows.Next() {
		var r model.Recording
		var startedAtStr, endedAtStr, mergeStatusStr sql.NullString
		if err := rows.Scan(&r.ID, &r.CameraID, &r.FilePath, &r.Format, &startedAtStr, &endedAtStr, &r.Duration, &r.FileSize, &r.FrameCount, &r.Merged, &mergeStatusStr, &r.Archived); err != nil {
			return nil, err
		}
		r.MergeStatus = mergeStatusFromBool(r.Merged)
		if mergeStatusStr.Valid && mergeStatusStr.String != "" {
			r.MergeStatus = mergeStatusStr.String
		}
		r.StartedAt = scanTime(startedAtStr)
		r.EndedAt = scanTime(endedAtStr)
		res = append(res, r)
	}
	return res, nil
}

// ListExpiredRecordingsByCamera returns expired recordings for a specific camera
func (d *DB) ListExpiredRecordingsByCamera(ctx context.Context, cameraID string, retentionDays int) ([]model.Recording, error) {
	sqlstr := `SELECT id, camera_id, file_path, format, started_at, ended_at, duration, file_size, frame_count, merged, merge_status, archived FROM recordings WHERE ended_at IS NOT NULL AND archived=0 AND camera_id=? AND ended_at < datetime('now', '-' || ? || ' days') ORDER BY ended_at ASC;`
	rows, err := d.db.QueryContext(ctx, sqlstr, cameraID, retentionDays)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var res []model.Recording
	for rows.Next() {
		var r model.Recording
		var startedAtStr, endedAtStr, mergeStatusStr sql.NullString
		if err := rows.Scan(&r.ID, &r.CameraID, &r.FilePath, &r.Format, &startedAtStr, &endedAtStr, &r.Duration, &r.FileSize, &r.FrameCount, &r.Merged, &mergeStatusStr, &r.Archived); err != nil {
			return nil, err
		}
		scanRecording(&r, startedAtStr, endedAtStr, mergeStatusStr)
		res = append(res, r)
	}
	return res, nil
}

// ListExpiredArchivedRecordingsByCamera returns expired archived recordings for a specific camera.
func (d *DB) ListExpiredArchivedRecordingsByCamera(ctx context.Context, cameraID string, retentionDays int) ([]model.Recording, error) {
	sqlstr := `SELECT id, camera_id, file_path, format, started_at, ended_at, duration, file_size, frame_count, merged, merge_status, archived FROM recordings WHERE ended_at IS NOT NULL AND archived=1 AND camera_id=? AND ended_at < datetime('now', '-' || ? || ' days') ORDER BY ended_at ASC;`
	rows, err := d.db.QueryContext(ctx, sqlstr, cameraID, retentionDays)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var res []model.Recording
	for rows.Next() {
		var r model.Recording
		var startedAtStr, endedAtStr, mergeStatusStr sql.NullString
		if err := rows.Scan(&r.ID, &r.CameraID, &r.FilePath, &r.Format, &startedAtStr, &endedAtStr, &r.Duration, &r.FileSize, &r.FrameCount, &r.Merged, &mergeStatusStr, &r.Archived); err != nil {
			return nil, err
		}
		scanRecording(&r, startedAtStr, endedAtStr, mergeStatusStr)
		res = append(res, r)
	}
	return res, nil
}

func (d *DB) ListOldestRecordings(ctx context.Context, limit int) ([]model.Recording, error) {
	sqlstr := `SELECT id, camera_id, file_path, format, started_at, ended_at, duration, file_size, frame_count, merged, merge_status, archived FROM recordings WHERE ended_at IS NOT NULL AND archived=0 ORDER BY ended_at ASC LIMIT ?;`
	rows, err := d.db.QueryContext(ctx, sqlstr, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var res []model.Recording
	for rows.Next() {
		var r model.Recording
		var startedAtStr, endedAtStr, mergeStatusStr sql.NullString
		if err := rows.Scan(&r.ID, &r.CameraID, &r.FilePath, &r.Format, &startedAtStr, &endedAtStr, &r.Duration, &r.FileSize, &r.FrameCount, &r.Merged, &mergeStatusStr, &r.Archived); err != nil {
			return nil, err
		}
		scanRecording(&r, startedAtStr, endedAtStr, mergeStatusStr)
		res = append(res, r)
	}
	return res, nil
}

// ListRecordingPathsByCamera returns the basenames of all file_path values for a camera's recordings.
func (d *DB) ListRecordingPathsByCamera(ctx context.Context, cameraID string) (map[string]bool, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT file_path FROM recordings WHERE camera_id=?`, cameraID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[string]bool)
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			continue
		}
		result[filepath.Base(p)] = true
	}
	return result, nil
}
