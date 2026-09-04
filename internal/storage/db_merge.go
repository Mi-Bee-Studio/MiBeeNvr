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

// migrateAIEventsInTx reparents AI events from old recording IDs to a new merged
// recording ID within the given transaction. This prevents AI events from becoming
// orphaned when source segments are deleted during merge — without this, events
// pointing at deleted segment IDs would no longer join to any recording, and the
// merged recording would appear to have no AI data even though its content was analyzed.
func migrateAIEventsInTx(ctx context.Context, tx *sql.Tx, newRecordingID string, oldIDs []string) error {
	if len(oldIDs) == 0 {
		return nil
	}
	for _, chunk := range chunkIDs(oldIDs, batchUpdateChunkSize) {
		placeholders := make([]string, len(chunk))
		args := make([]interface{}, 0, len(chunk)+1)
		args = append(args, newRecordingID)
		for i, id := range chunk {
			placeholders[i] = "?"
			args = append(args, id)
		}
		q := "UPDATE ai_events SET recording_id = ? WHERE recording_id IN (" + strings.Join(placeholders, ",") + ")"
		if _, err := tx.ExecContext(ctx, q, args...); err != nil {
			return fmt.Errorf("migrate ai_events to merged recording %s: %w", newRecordingID, err)
		}
	}
	return nil
}

// motionAgg accumulates motion info across recordings being merged so the
// merged row inherits a duration-weighted motion_score (+ confidence, #634)
// and the union of activity flags (#458). Unanalyzed sources (score < 0)
// contribute their flags but not the average; a set of only-unanalyzed
// sources keeps the merged row unanalyzed (-1) — treating them as 0 would
// mislabel a merged hour as "static" and make motion-aware cleanup evict it
// first.
type motionAgg struct {
	weightedSum  float64
	confSum      float64 // duration-weighted confidence accumulator
	totalDur     float64
	analyzed     int
	simpleSum    float64
	simpleConf   float64
	flags        map[string]struct{}
	flagsOrdered []string
}

func (a *motionAgg) add(score, conf float64, flags string, dur float64) {
	if a.flags == nil {
		a.flags = make(map[string]struct{})
	}
	for _, f := range strings.Split(flags, ",") {
		if f = strings.TrimSpace(f); f != "" {
			if _, dup := a.flags[f]; !dup {
				a.flags[f] = struct{}{}
				a.flagsOrdered = append(a.flagsOrdered, f)
			}
		}
	}
	if score < 0 {
		return
	}
	if dur < 0 {
		dur = 0
	}
	a.weightedSum += score * dur
	a.totalDur += dur
	a.simpleSum += score
	if conf < 0 {
		conf = 1 // pre-v634 sources: keep full weight
	}
	a.confSum += conf * dur
	a.simpleConf += conf
	a.analyzed++
}

// result returns the aggregated (score, confidence, flags). Score falls back
// to a simple average when every analyzed source has zero duration.
func (a *motionAgg) result() (float64, float64, string) {
	if a.analyzed == 0 {
		return model.MotionScoreUnanalyzed, model.MotionConfidenceUnanalyzed, ""
	}
	var score, conf float64
	if a.totalDur > 0 {
		score = a.weightedSum / a.totalDur
		conf = a.confSum / a.totalDur
	} else {
		score = a.simpleSum / float64(a.analyzed)
		conf = a.simpleConf / float64(a.analyzed)
	}
	return score, conf, strings.Join(a.flagsOrdered, ",")
}

// aggregateSourceMotion loads the motion columns of the given recording IDs
// within tx and folds them into agg.
func aggregateSourceMotion(ctx context.Context, tx *sql.Tx, ids []string, agg *motionAgg) error {
	for _, chunk := range chunkIDs(ids, batchUpdateChunkSize) {
		if err := aggregateSourceMotionChunk(ctx, tx, chunk, agg); err != nil {
			return err
		}
	}
	return nil
}

func aggregateSourceMotionChunk(ctx context.Context, tx *sql.Tx, chunk []string, agg *motionAgg) error {
	placeholders := make([]string, len(chunk))
	args := make([]interface{}, 0, len(chunk))
	for i, id := range chunk {
		placeholders[i] = "?"
		args = append(args, id)
	}
	q := `SELECT motion_score, COALESCE(motion_confidence, -1), COALESCE(activity_flags, ''), COALESCE(duration, 0) FROM recordings WHERE id IN (` + strings.Join(placeholders, ",") + `)`
	rows, err := tx.QueryContext(ctx, q, args...)
	if err != nil {
		return fmt.Errorf("load source motion columns: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var score, conf, dur float64
		var flags string
		if err := rows.Scan(&score, &conf, &flags, &dur); err != nil {
			return fmt.Errorf("scan source motion columns: %w", err)
		}
		agg.add(score, conf, flags, dur)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate source motion columns: %w", err)
	}
	return nil
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

	// Propagate motion info from the source segments into the merged row
	// (duration-weighted score + flag union) before they are deleted (#458).
	var agg motionAgg
	if err := aggregateSourceMotion(ctx, tx, oldIDs, &agg); err != nil {
		return err
	}
	motionScore, motionConf, motionFlags := agg.result()

	q := `INSERT INTO recordings(id, camera_id, file_path, format, started_at, ended_at, duration, file_size, frame_count, merge_status, merge_tier, merge_progress, merge_quality, motion_score, motion_confidence, activity_flags, timeline_map) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?);`
	_, err = tx.ExecContext(ctx, q, merged.ID, merged.CameraID, merged.FilePath, merged.Format, timeToDB(merged.StartedAt), timeToDB(merged.EndedAt), merged.Duration, merged.FileSize, merged.FrameCount, model.MergeStatusMerged, merged.MergeTier, 100, merged.MergeQuality, motionScore, motionConf, motionFlags, merged.TimelineMap)
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

	// Migrate AI events from deleted segment IDs to the new merged recording.
	if err := migrateAIEventsInTx(ctx, tx, merged.ID, oldIDs); err != nil {
		return err
	}

	return tx.Commit()
}

// ListShortMergedRecordings returns merged recordings shorter than minDurationSec
// for a camera (or all cameras if cameraID is empty). These are candidates for
// further consolidation — merging with adjacent recordings to reach the minimum
// duration threshold.
func (d *DB) ListShortMergedRecordings(ctx context.Context, cameraID string, minDurationSec float64) ([]*model.Recording, error) {
	query := `SELECT id, camera_id, file_path, format, started_at, ended_at, duration, file_size, frame_count, merge_status, archived FROM recordings WHERE merge_status='merged' AND merge_quality='short' AND duration < ? AND ended_at IS NOT NULL`
	args := []interface{}{minDurationSec}
	if cameraID != "" {
		query += " AND camera_id = ?"
		args = append(args, cameraID)
	}
	query += " ORDER BY camera_id, started_at ASC;"

	rows, err := d.readConn().QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var res []*model.Recording
	for rows.Next() {
		var r model.Recording
		var startedAtStr, endedAtStr, mergeStatusStr sql.NullString
		if err := rows.Scan(&r.ID, &r.CameraID, &r.FilePath, &r.Format, &startedAtStr, &endedAtStr, &r.Duration, &r.FileSize, &r.FrameCount, &mergeStatusStr, &r.Archived); err != nil {
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

// RollingReplaceRecordings atomically updates or creates a merged recording and
// deletes the source segment(s) in a single transaction.
//
// This is the DB operation backing the RollingMergeCoordinator's quasi-real-time
// merge pipeline. Two cases:
//
//  1. Create (existingMergedID == ""): INSERT the new merged recording + DELETE
//     the source segment IDs. Used when starting a new merge window bucket.
//
//  2. Append (existingMergedID != ""): UPDATE the existing merged recording's
//     metadata (duration, file_size, frame_count, ended_at) + DELETE the source
//     segment ID. Used when appending a segment to an existing bucket.
//
// The merged recording is marked merge_status='merged' so retention cleanup
// treats it as a finalized file.
func (d *DB) RollingReplaceRecordings(ctx context.Context, merged *model.Recording, existingMergedID string, sourceIDs []string) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Propagate motion info into the merged row (#458):
	// - create: fold the source segments' scores (duration-weighted) + flags
	// - append: fold the EXISTING merged row's aggregate together with the
	//   newly-appended sources, so a growing bucket keeps a representative
	//   score for its whole content rather than only the latest append.
	var agg motionAgg
	if existingMergedID != "" {
		if err := aggregateSourceMotion(ctx, tx, []string{existingMergedID}, &agg); err != nil {
			return err
		}
	}
	if err := aggregateSourceMotion(ctx, tx, sourceIDs, &agg); err != nil {
		return err
	}
	motionScore, motionConf, motionFlags := agg.result()

	if existingMergedID == "" {
		// Case 1: Create — INSERT new merged recording.
		q := `INSERT INTO recordings(id, camera_id, file_path, format, started_at, ended_at, duration, file_size, frame_count, merge_status, merge_tier, merge_progress, merge_quality, motion_score, motion_confidence, activity_flags, timeline_map) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?);`
		_, err = tx.ExecContext(ctx, q, merged.ID, merged.CameraID, merged.FilePath, merged.Format,
			timeToDB(merged.StartedAt), timeToDB(merged.EndedAt), merged.Duration, merged.FileSize,
			merged.FrameCount, model.MergeStatusMerged, "rolling", 100, merged.MergeQuality, motionScore, motionConf, motionFlags, merged.TimelineMap)
		if err != nil {
			return err
		}
	} else {
		// Case 2: Append — UPDATE existing merged recording. started_at/ended_at
		// use SQL min()/max(): appends may extend the row backward when an
		// out-of-order earlier segment arrives (#698), and the endpoints must
		// never invert or shrink the covered range. The canonical time format
		// (big-endian, zero-padded) makes lexicographic min/max time-correct.
		q := `UPDATE recordings SET file_path = ?, started_at = min(started_at, ?), ended_at = max(ended_at, ?), duration = ?, file_size = ?, frame_count = ?, merge_quality = ?, motion_score = ?, motion_confidence = ?, activity_flags = ?, timeline_map = ? WHERE id = ?;`
		_, err = tx.ExecContext(ctx, q, merged.FilePath, timeToDB(merged.StartedAt), timeToDB(merged.EndedAt), merged.Duration,
			merged.FileSize, merged.FrameCount, merged.MergeQuality, motionScore, motionConf, motionFlags, merged.TimelineMap, existingMergedID)
		if err != nil {
			return err
		}
	}

	// Delete source segment IDs.
	if len(sourceIDs) > 0 {
		placeholders := make([]string, len(sourceIDs))
		args := make([]interface{}, len(sourceIDs))
		for i, id := range sourceIDs {
			placeholders[i] = "?"
			args[i] = id
		}
		delQ := "DELETE FROM recordings WHERE id IN (" + strings.Join(placeholders, ",") + ")"
		_, err = tx.ExecContext(ctx, delQ, args...)
		if err != nil {
			return err
		}

		// Migrate AI events from source segment IDs to the merged recording.
		// Target is existingMergedID (append) or merged.ID (create new bucket).
		targetID := existingMergedID
		if targetID == "" {
			targetID = merged.ID
		}
		if err := migrateAIEventsInTx(ctx, tx, targetID, sourceIDs); err != nil {
			return err
		}
	}

	return tx.Commit()
}

// ListMergeableSegments returns recordings for a camera within a time window,
// excluding merged and incomplete segments.
func (d *DB) ListMergeableSegments(ctx context.Context, cameraID string, windowStart, windowEnd time.Time) ([]*model.Recording, error) {
	rows, err := d.readConn().QueryContext(ctx,
		`SELECT id, camera_id, file_path, format, started_at, ended_at, duration, file_size, frame_count, merge_status, archived FROM recordings WHERE camera_id = ? AND merge_status = 'pending' AND COALESCE(layer,0)=0 AND ended_at IS NOT NULL AND started_at >= ? AND started_at < ? ORDER BY started_at ASC;`,
		cameraID, formatTime(windowStart), formatTime(windowEnd))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var res []*model.Recording
	for rows.Next() {
		var r model.Recording
		var startedAtStr, endedAtStr, mergeStatusStr sql.NullString
		if err := rows.Scan(&r.ID, &r.CameraID, &r.FilePath, &r.Format, &startedAtStr, &endedAtStr, &r.Duration, &r.FileSize, &r.FrameCount, &mergeStatusStr, &r.Archived); err != nil {
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

// ListPendingSegmentsForRolling returns pending recordings for a camera, ordered
// by started_at ascending. Used by the rolling merge backfill to drain historical
// backlog on startup or via manual API trigger.
//
// All mergeable formats are returned (h264, h265, avi, mjpeg). The timelapse format
// is excluded — it has its own merge pipeline (timelapse package).
// If cameraID is empty, returns pending segments for ALL cameras.
// Optionally reincludes 'failed' segments (for forced backfill via API).
//
// limit caps the number of rows returned (0 = unlimited). since, when non-zero,
// filters to segments whose started_at >= since — this bounds startup backfill to
// recent segments on resource-constrained hosts (RPi 3B) so months of historical
// fragments are left for the periodic MergeManager to digest gradually instead of
// triggering an IO storm on the first boot after enabling rolling merge.
func (d *DB) ListPendingSegmentsForRolling(ctx context.Context, cameraID string, includeFailed bool, limit int, since time.Time) ([]*model.Recording, error) {
	query := `SELECT id, camera_id, file_path, format, started_at, ended_at, duration, file_size, frame_count, merge_status, archived FROM recordings WHERE merge_status = 'pending' AND COALESCE(layer,0)=0 AND ended_at IS NOT NULL AND format != 'timelapse'`
	args := []interface{}{}
	if includeFailed {
		query = strings.Replace(query, "merge_status = 'pending'", "merge_status IN ('pending', 'failed')", 1)
	}
	if cameraID != "" {
		query += " AND camera_id = ?"
		args = append(args, cameraID)
	}
	if !since.IsZero() {
		query += " AND started_at >= ?"
		args = append(args, since.UTC().Format("2006-01-02 15:04:05.999999999"))
	}
	query += " ORDER BY camera_id, started_at ASC"
	if limit > 0 {
		query += " LIMIT ?"
		args = append(args, limit)
	}
	query += ";"

	rows, err := d.readConn().QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var res []*model.Recording
	for rows.Next() {
		var r model.Recording
		var startedAtStr, endedAtStr, mergeStatusStr sql.NullString
		if err := rows.Scan(&r.ID, &r.CameraID, &r.FilePath, &r.Format, &startedAtStr, &endedAtStr, &r.Duration, &r.FileSize, &r.FrameCount, &mergeStatusStr, &r.Archived); err != nil {
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

// ResetFailedMergeStatus resets merge_status from 'failed' or 'incompatible' back to 'pending'
// for the given recording IDs, allowing them to be re-processed by rolling/periodic merge.
// Returns the number of rows affected.
func (d *DB) ResetFailedMergeStatus(ctx context.Context, ids []string) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	placeholders := make([]string, len(ids))
	args := make([]interface{}, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	q := "UPDATE recordings SET merge_status = 'pending', merge_error = '' WHERE id IN (" + strings.Join(placeholders, ",") + ") AND merge_status IN ('failed', 'incompatible');"
	result, err := d.db.ExecContext(ctx, q, args...)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// ListCameraMergeWindows returns hourly merge windows for a camera with 2+ segments.
// Only includes recordings older than minAge.
func (d *DB) ListCameraMergeWindows(ctx context.Context, cameraID string, minAge time.Duration) ([]MergeWindow, error) {
	// UTC is mandatory: ended_at is stored UTC (timeToDB), so a local-time
	// cutoff would sit hours in the future of the intended instant on any
	// non-UTC host and loosen the minAge gate (same class as the
	// ListDarkRecordings bug, #565).
	cutoff := time.Now().UTC().Add(-minAge).Format(sqliteTimeFormat)
	query := `SELECT strftime('%Y-%m-%d %H', started_at) as hour, MIN(started_at), MAX(ended_at), COUNT(*), format FROM recordings WHERE camera_id = ? AND merge_status = 'pending' AND COALESCE(layer,0)=0 AND ended_at IS NOT NULL AND ended_at < ? GROUP BY hour, format HAVING COUNT(*) >= 2 ORDER BY hour ASC;`
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

// ClearCameraMerge resets ALL per-camera merge-config overrides to NULL so the
// camera falls back to the global defaults. Unlike UpsertCameraMerge(nil...),
// which COALESCEs and keeps existing values, this actually clears them.
// Required by the "Use global default" UI action (issue #68-3): the previous
// implementation called UpsertCameraMerge with all-nil args, which was a no-op,
// so the per-camera override persisted and the editor kept showing "(customized)"
// every time the user reopened it.
func (d *DB) ClearCameraMerge(ctx context.Context, cameraID string) error {
	_, err := d.db.ExecContext(ctx,
		`UPDATE cameras SET
			merge_enabled = NULL,
			merge_check_interval = NULL,
			merge_window_size = NULL,
			merge_batch_limit = NULL,
			merge_min_segment_age = NULL,
			merge_min_segments_to_merge = NULL
		WHERE id = ?;`, cameraID)
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

// ClearMergePathBatch empties merge_path and resets merge_progress for the
// given recording IDs. Used by the periodic timelapse merge after it has
// folded segments into a long-window output: the intermediate rolling-merge
// .mp4 at recordings.merge_path is now redundant, so we clear the DB pointer
// (and the caller removes the file on disk). merge_status is left unchanged
// — the segment is still considered merged-into-the-window (daily_merged).
// Empty ids slice is a no-op.
func (d *DB) ClearMergePathBatch(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	q := `UPDATE recordings SET merge_path='', merge_progress=0 WHERE id IN (%s);`
	for _, chunk := range chunkIDs(ids, batchUpdateChunkSize) {
		placeholders := make([]string, len(chunk))
		args := make([]any, 0, len(chunk))
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

// ListMergedRecordingsForValidation returns all recordings that have a non-empty
// merge_path and merge_status='merged'. Used at startup to verify that the merged
// output files actually exist on disk — stale DB entries from deleted/missing files
// are reset so playback can fall back to original frames.
func (d *DB) ListMergedRecordingsForValidation(ctx context.Context) ([]*model.Recording, error) {
	rows, err := d.db.QueryContext(ctx,
		`SELECT id, camera_id, file_path, merge_path FROM recordings WHERE merge_status='merged' AND merge_path != '' AND merge_path IS NOT NULL;`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []*model.Recording
	for rows.Next() {
		var r model.Recording
		if err := rows.Scan(&r.ID, &r.CameraID, &r.FilePath, &r.MergePath); err != nil {
			return nil, err
		}
		result = append(result, &r)
	}
	return result, rows.Err()
}

// ListRecordingsByMergeStatus returns recordings matching one of the given
// merge_status values (e.g. "incompatible", "failed", "dark"). Used by the
// `repair fragments` CLI to surface segments the merge engine permanently gave
// up on — those accumulate as un-mergeable debris that rolling merge will never
// retry (rolling.go marks them "incompatible so we don't retry forever").
//
// cameraID == "" matches all cameras; limit <= 0 means no limit.
// Results are ordered by started_at ASC so dry-run output reads chronologically.
func (d *DB) ListRecordingsByMergeStatus(ctx context.Context, statuses []string, cameraID string, limit int) ([]*model.Recording, error) {
	if len(statuses) == 0 {
		return nil, nil
	}
	placeholders := make([]string, len(statuses))
	args := make([]any, 0, len(statuses)+2)
	for i, s := range statuses {
		placeholders[i] = "?"
		args = append(args, s)
	}
	q := "SELECT id, camera_id, file_path, format, started_at, ended_at, duration, file_size, frame_count, merge_status, merge_path, merge_tier, merge_progress, merge_error, merge_quality, archived, motion_score, motion_confidence, activity_flags, COALESCE(layer,0) FROM recordings WHERE merge_status IN (" +
		strings.Join(placeholders, ",") + ")"
	if cameraID != "" {
		q += " AND camera_id=?"
		args = append(args, cameraID)
	}
	q += " ORDER BY started_at ASC"
	if limit > 0 {
		q += fmt.Sprintf(" LIMIT %d", limit)
	}
	rows, err := d.readConn().QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	recs, err := scanRecordingRows(rows)
	if err != nil {
		return nil, err
	}
	result := make([]*model.Recording, len(recs))
	for i := range recs {
		result[i] = &recs[i]
	}
	return result, nil
}

// ListFakeMergedRecordings returns recordings marked merge_status='merged' but
// with an empty merge_path — i.e. segments that were marked "merged" by the
// rolling merge singleton fast-path (when a batch had <2 valid segments) but
// were never actually merged. These permanently fell out of the merge queue
// (backfill only selects pending), accumulating as thousands of un-merged
// short fragments that clutter the timeline.
//
// cameraID == "" matches all cameras; limit <= 0 means no limit.
// maxDurationSec > 0 restricts to recordings with duration <= that value (used
// to target singleton fragments while leaving long already-merged recordings
// alone — some merged outputs legitimately have an empty merge_path).
// Ordered by started_at ASC for chronological dry-run output.
//
// Reset these to pending (via SetMergeStatus) to re-queue them for merging.
func (d *DB) ListFakeMergedRecordings(ctx context.Context, cameraID string, limit int, maxDurationSec float64) ([]*model.Recording, error) {
	q := "SELECT id, camera_id, file_path, format, started_at, ended_at, duration, file_size, frame_count, merge_status, merge_path, merge_tier, merge_progress, merge_error, merge_quality, archived, motion_score, motion_confidence, activity_flags, COALESCE(layer,0) FROM recordings WHERE merge_status='merged' AND (merge_path IS NULL OR merge_path='')"
	args := []any{}
	if cameraID != "" {
		q += " AND camera_id=?"
		args = append(args, cameraID)
	}
	if maxDurationSec > 0 {
		q += " AND duration <= ?"
		args = append(args, maxDurationSec)
	}
	q += " ORDER BY started_at ASC"
	if limit > 0 {
		q += fmt.Sprintf(" LIMIT %d", limit)
	}
	rows, err := d.readConn().QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	recs, err := scanRecordingRows(rows)
	if err != nil {
		return nil, err
	}
	result := make([]*model.Recording, len(recs))
	for i := range recs {
		result[i] = &recs[i]
	}
	return result, nil
}

// ResetMergeStatus clears merge_status and merge_path for a recording, reverting
// it to its unmerged state so playback falls back to original frames.
func (d *DB) ResetMergeStatus(ctx context.Context, id string) error {
	_, err := d.db.ExecContext(ctx,
		`UPDATE recordings SET merge_status='', merge_path='', merge_tier='', merge_progress=0 WHERE id=?;`, id)
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
	// UTC is mandatory: ended_at is stored UTC (timeToDB), so a local-time
	// cutoff would sit hours in the future of the intended instant on any
	// non-UTC host and loosen the minAge gate (same class as the
	// ListDarkRecordings bug, #565).
	cutoff := time.Now().UTC().Add(-minAge).Format(sqliteTimeFormat)
	query := `
		WITH hour_buckets AS (
			SELECT id, camera_id, file_path, format, started_at, ended_at, duration, file_size, frame_count, merge_status, archived,
				COUNT(*) OVER (
					PARTITION BY camera_id, strftime('%Y-%m-%d %H', started_at), format
				) as cnt
			FROM recordings
			WHERE camera_id = ? AND merge_status = 'pending' AND COALESCE(layer,0)=0 AND ended_at IS NOT NULL AND ended_at < ?
		)
		SELECT id, camera_id, file_path, format, started_at, ended_at, duration, file_size, frame_count, merge_status, archived
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
		if err := rows.Scan(&r.ID, &r.CameraID, &r.FilePath, &r.Format, &startedAtStr, &endedAtStr, &r.Duration, &r.FileSize, &r.FrameCount, &mergeStatusStr, &r.Archived); err != nil {
			return nil, err
		}
		scanRecording(&r, startedAtStr, endedAtStr, mergeStatusStr)
		res = append(res, &r)
	}
	return res, nil
}
