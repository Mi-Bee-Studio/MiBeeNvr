package storage

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
)

// batchUpdateChunkSize limits the number of bind parameters per statement to stay safely
// under SQLite's SQLITE_MAX_VARIABLE_NUMBER (default 999; some builds 32766). Using 500
// matches the established orphanBatchSize convention and keeps each UPDATE cheap.
const batchUpdateChunkSize = 500

// chunkIDs splits ids into slices of at most size, without allocating when len(ids)<=size.
func chunkIDs(ids []string, size int) [][]string {
	if size <= 0 || len(ids) == 0 {
		return nil
	}
	if len(ids) <= size {
		return [][]string{ids}
	}
	var chunks [][]string
	for i := 0; i < len(ids); i += size {
		end := i + size
		if end > len(ids) {
			end = len(ids)
		}
		chunks = append(chunks, ids[i:end])
	}
	return chunks
}

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

	// Batch delete old recordings with a single IN clause
	placeholders := make([]string, len(oldIDs))
	args := make([]interface{}, len(oldIDs))
	for i, id := range oldIDs {
		placeholders[i] = "?"
		args[i] = id
	}
	delQ := "DELETE FROM recordings WHERE id IN (" + strings.Join(placeholders, ",") + ")"
	_, err = tx.ExecContext(ctx, delQ, args...)
	if err != nil {
		return err
	}

	return tx.Commit()
}

// ListMergeableSegments returns recordings for a camera within a time window,
// excluding merged and incomplete segments.
func (d *DB) ListMergeableSegments(ctx context.Context, cameraID string, windowStart, windowEnd time.Time) ([]*model.Recording, error) {
	rows, err := d.readConn().QueryContext(ctx,
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
		scanRecording(&r, startedAtStr, endedAtStr, mergeStatusStr)
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
	rows, err := d.readConn().QueryContext(ctx, query, cameraID, cutoff)
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

// SetMergeStatus updates merge_status for the given recording IDs in a single batched
// UPDATE (with chunking to stay under SQLite's variable limit). Empty ids slice is a no-op.
// Replaces the former per-row ExecContext loop (N IDs = N round-trips) with at most
// ceil(len(ids)/batchUpdateChunkSize) statements, all in one transaction.
func (d *DB) SetMergeStatus(ctx context.Context, ids []string, status string) error {
	if len(ids) == 0 {
		return nil
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	q := `UPDATE recordings SET merge_status = ? WHERE id IN (%s);`
	for _, chunk := range chunkIDs(ids, batchUpdateChunkSize) {
		placeholders := make([]string, len(chunk))
		args := make([]any, 0, len(chunk)+1)
		args = append(args, status)
		for i, id := range chunk {
			placeholders[i] = "?"
			args = append(args, id)
		}
		stmt := fmt.Sprintf(q, strings.Join(placeholders, ","))
		if _, err := tx.ExecContext(ctx, stmt, args...); err != nil {
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

// SetMergeError updates merge_status to 'failed' and sets merge_error for the given IDs
// in a single batched UPDATE (chunked to stay under SQLite's variable limit). Empty ids is a no-op.
func (d *DB) SetMergeError(ctx context.Context, ids []string, mergeError string) error {
	if len(ids) == 0 {
		return nil
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	q := `UPDATE recordings SET merge_status=?, merge_error=?, merge_progress=? WHERE id IN (%s);`
	for _, chunk := range chunkIDs(ids, batchUpdateChunkSize) {
		placeholders := make([]string, len(chunk))
		args := make([]any, 0, len(chunk)+3)
		args = append(args, model.MergeStatusFailed, mergeError, 0)
		for i, id := range chunk {
			placeholders[i] = "?"
			args = append(args, id)
		}
		stmt := fmt.Sprintf(q, strings.Join(placeholders, ","))
		if _, err := tx.ExecContext(ctx, stmt, args...); err != nil {
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

// UpdateMergeProgressBatch is the multi-ID equivalent of UpdateMergeProgress, issuing a
// single chunked UPDATE per batch instead of one statement per ID. Empty ids is a no-op.
// This is the hot path during FFmpeg/Go merge progress parsing, where it was previously
// called once per segment per progress tick (N segments × M ticks = N×M statements).
func (d *DB) UpdateMergeProgressBatch(ctx context.Context, ids []string, progress int) error {
	if len(ids) == 0 {
		return nil
	}
	status := model.MergeStatusPending
	if progress >= 100 {
		status = model.MergeStatusMerged
	} else if progress > 0 {
		status = model.MergeStatusMerging
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	q := `UPDATE recordings SET merge_progress=?, merge_status=? WHERE id IN (%s);`
	for _, chunk := range chunkIDs(ids, batchUpdateChunkSize) {
		placeholders := make([]string, len(chunk))
		args := make([]any, 0, len(chunk)+2)
		args = append(args, progress, string(status))
		for i, id := range chunk {
			placeholders[i] = "?"
			args = append(args, id)
		}
		stmt := fmt.Sprintf(q, strings.Join(placeholders, ","))
		if _, err := tx.ExecContext(ctx, stmt, args...); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ListSingletonPendingRecordings returns pending recordings for a camera that are
// older than minAge but are NOT part of any multi-segment merge window.
// These are hour-boundary orphans that will never be merged.
func (d *DB) ListSingletonPendingRecordings(ctx context.Context, cameraID string, minAge time.Duration) ([]*model.Recording, error) {
	cutoff := time.Now().Add(-minAge).Format(sqliteTimeFormat)
	query := `
		WITH hour_buckets AS (
			SELECT id, camera_id, file_path, format, started_at, ended_at, duration, file_size, frame_count, merged, merge_status, archived,
				COUNT(*) OVER (
					PARTITION BY camera_id, strftime('%Y-%m-%d %H', started_at), format
				) as cnt
			FROM recordings
			WHERE camera_id = ? AND merge_status = 'pending' AND ended_at IS NOT NULL AND ended_at < ?
		)
		SELECT id, camera_id, file_path, format, started_at, ended_at, duration, file_size, frame_count, merged, merge_status, archived
		FROM hour_buckets WHERE cnt = 1
		`
	rows, err := d.readConn().QueryContext(ctx, query, cameraID, cutoff)
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
		scanRecording(&r, startedAtStr, endedAtStr, mergeStatusStr)
		res = append(res, &r)
	}
	return res, nil
}
