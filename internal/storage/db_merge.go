package storage

import (
	"context"
	"database/sql"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
)

// MergeWindow represents a group of consecutive recordings eligible for merging.
type MergeWindow struct {
	StartTime    time.Time `json:"start_time"`
	EndTime      time.Time `json:"end_time"`
	SegmentCount int       `json:"segment_count"`
	Format       string    `json:"format"`
}

// MergeAndReplaceRecordings atomically inserts a merged recording and deletes old recordings in a single transaction.
// This reduces SQLITE_BUSY contention compared to separate INSERT + SetMerged + DeleteBatch calls.
func (d *DB) MergeAndReplaceRecordings(ctx context.Context, merged *model.Recording, oldIDs []string) error {
	if len(oldIDs) == 0 {
		return d.InsertRecording(ctx, merged)
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	q := `INSERT INTO recordings(id, camera_id, file_path, format, started_at, ended_at, duration, file_size, frame_count, merged, merge_status, merge_tier, merge_progress) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?);`
	_, err = tx.ExecContext(ctx, q, merged.ID, merged.CameraID, merged.FilePath, merged.Format, timeToDB(merged.StartedAt), timeToDB(merged.EndedAt), merged.Duration, merged.FileSize, merged.FrameCount, true, model.MergeStatusMerged, merged.MergeTier, 100)
	if err != nil {
		return err
	}

	for _, id := range oldIDs {
		_, err = tx.ExecContext(ctx, `DELETE FROM recordings WHERE id = ?;`, id)
		if err != nil {
			return err
		}
	}

	return tx.Commit()
}

// ListMergeableSegments returns recordings for a camera within a time window,
// excluding merged and incomplete segments.
func (d *DB) ListMergeableSegments(ctx context.Context, cameraID string, windowStart, windowEnd time.Time) ([]*model.Recording, error) {
	rows, err := d.db.QueryContext(ctx,
		`SELECT id, camera_id, file_path, format, started_at, ended_at, duration, file_size, frame_count, merged, merge_status, archived FROM recordings WHERE camera_id = ? AND merge_status = 'pending' AND ended_at IS NOT NULL AND started_at >= ? AND started_at < ? ORDER BY started_at ASC;`,
		cameraID, formatTime(windowStart), formatTime(windowEnd))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var res []*model.Recording
	for rows.Next() {
		var r model.Recording
		var startedAtStr, endedAtStr, mergeStatusStr sql.NullString
		if err := rows.Scan(&r.ID, &r.CameraID, &r.FilePath, &r.Format, &startedAtStr, &endedAtStr, &r.Duration, &r.FileSize, &r.FrameCount, &r.Merged, &mergeStatusStr, &r.Archived); err != nil {
			return nil, err
		}
		scanRecording(&r, startedAtStr, endedAtStr, mergeStatusStr) //nolint:errcheck // TODO(#51): error is already in row.Scan
		res = append(res, &r)
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return res, nil
}

// ListCameraMergeWindows returns hourly merge windows for a camera with 2+ segments.
// Only includes recordings older than minAge.
func (d *DB) ListCameraMergeWindows(ctx context.Context, cameraID string, minAge time.Duration) ([]MergeWindow, error) {
	cutoff := time.Now().Add(-minAge).Format(sqliteTimeFormat)
	query := `SELECT strftime('%Y-%m-%d %H', started_at) as hour, MIN(started_at), MAX(ended_at), COUNT(*), format FROM recordings WHERE camera_id = ? AND merge_status = 'pending' AND ended_at IS NOT NULL AND ended_at < ? GROUP BY hour, format HAVING COUNT(*) >= 2 ORDER BY hour ASC;`
	rows, err := d.db.QueryContext(ctx, query, cameraID, cutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var res []MergeWindow
	for rows.Next() {
		var w MergeWindow
		var hourStr, minStart, maxEnd sql.NullString
		if err := rows.Scan(&hourStr, &minStart, &maxEnd, &w.SegmentCount, &w.Format); err != nil {
			return nil, err
		}
		w.StartTime = scanTime(minStart)
		w.EndTime = scanTime(maxEnd)
		res = append(res, w)
	}
	return res, nil
}

// UpsertCameraMerge writes per-camera merge config columns.
// Pass nil pointers to leave fields unchanged (keep existing values).
func (d *DB) UpsertCameraMerge(ctx context.Context, cameraID string, mergeEnabled *bool, mergeCheckInterval, mergeWindowSize, mergeMinSegmentAge *string, mergeBatchLimit, mergeMinSegmentsToMerge *int) error {
	q := `UPDATE cameras SET
		merge_enabled = COALESCE(?, merge_enabled),
		merge_check_interval = COALESCE(?, merge_check_interval),
		merge_window_size = COALESCE(?, merge_window_size),
		merge_batch_limit = COALESCE(?, merge_batch_limit),
		merge_min_segment_age = COALESCE(?, merge_min_segment_age),
		merge_min_segments_to_merge = COALESCE(?, merge_min_segments_to_merge)
		WHERE id = ?;`
	_, err := d.db.ExecContext(ctx, q,
		ptrToNullBool(mergeEnabled),
		ptrToNullString(mergeCheckInterval),
		ptrToNullString(mergeWindowSize),
		ptrToNullInt64(mergeBatchLimit),
		ptrToNullString(mergeMinSegmentAge),
		ptrToNullInt64(mergeMinSegmentsToMerge),
		cameraID)
	return err
}

// SetMergeStatus updates merge_status for the given recording IDs in a transaction.
// Empty ids slice is a no-op.
func (d *DB) SetMergeStatus(ctx context.Context, ids []string, status string) error {
	if len(ids) == 0 {
		return nil
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	q := `UPDATE recordings SET merge_status = ? WHERE id = ?;`
	for _, id := range ids {
		if _, err := tx.ExecContext(ctx, q, status, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// SetMergeResult updates merge_status to 'merged' and sets merge_path, merge_tier, and merge_progress for a recording.
func (d *DB) SetMergeResult(ctx context.Context, id string, mergePath, mergeTier string) error {
	_, err := d.db.ExecContext(ctx,
		`UPDATE recordings SET merge_status=?, merge_path=?, merge_tier=?, merge_progress=? WHERE id=?;`,
		model.MergeStatusMerged, mergePath, mergeTier, 100, id)
	return err
}

// SetMergeError updates merge_status to 'failed' and sets merge_error for the given IDs.
func (d *DB) SetMergeError(ctx context.Context, ids []string, mergeError string) error {
	if len(ids) == 0 {
		return nil
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	q := `UPDATE recordings SET merge_status=?, merge_error=?, merge_progress=? WHERE id=?;`
	for _, id := range ids {
		if _, err := tx.ExecContext(ctx, q, model.MergeStatusFailed, mergeError, 0, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// UpdateMergeProgress updates merge_progress and automatically sets merge_status for a recording.
//   - progress=0: sets merge_status='pending'
//   - progress>0 and progress<100: sets merge_status='merging'
//   - progress=100: sets merge_status='merged'
func (d *DB) UpdateMergeProgress(ctx context.Context, id string, progress int) error {
	status := model.MergeStatusPending
	if progress >= 100 {
		status = model.MergeStatusMerged
	} else if progress > 0 {
		status = model.MergeStatusMerging
	}
	_, err := d.db.ExecContext(ctx,
		`UPDATE recordings SET merge_progress=?, merge_status=? WHERE id=?;`,
		progress, string(status), id)
	return err
}

// ListSingletonPendingRecordings returns pending recordings for a camera that are
// older than minAge but are NOT part of any multi-segment merge window.
// These are hour-boundary orphans that will never be merged.
func (d *DB) ListSingletonPendingRecordings(ctx context.Context, cameraID string, minAge time.Duration) ([]*model.Recording, error) {
	cutoff := time.Now().Add(-minAge).Format(sqliteTimeFormat)
	query := `
		SELECT r.id, r.camera_id, r.file_path, r.format, r.started_at, r.ended_at, r.duration, r.file_size, r.frame_count, r.merged, r.merge_status, r.archived
		FROM recordings r
		WHERE r.camera_id = ?
			AND r.merge_status = 'pending'
			AND r.ended_at IS NOT NULL
			AND r.ended_at < ?
			AND (
				SELECT COUNT(*)
				FROM recordings r2
				WHERE r2.camera_id = r.camera_id
					AND r2.merge_status = 'pending'
					AND r2.ended_at IS NOT NULL
					AND strftime('%Y-%m-%d %H', r2.started_at) = strftime('%Y-%m-%d %H', r.started_at)
					AND r2.format = r.format
			) = 1;
		`
	rows, err := d.db.QueryContext(ctx, query, cameraID, cutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var res []*model.Recording
	for rows.Next() {
		var r model.Recording
		var startedAtStr, endedAtStr, mergeStatusStr sql.NullString
		if err := rows.Scan(&r.ID, &r.CameraID, &r.FilePath, &r.Format, &startedAtStr, &endedAtStr, &r.Duration, &r.FileSize, &r.FrameCount, &r.Merged, &mergeStatusStr, &r.Archived); err != nil {
			return nil, err
		}
		scanRecording(&r, startedAtStr, endedAtStr, mergeStatusStr) //nolint:errcheck // TODO(#51): error is already in row.Scan
		res = append(res, &r)
	}
	return res, nil
}
