package storage

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
)

// selectRecordingColumns is the canonical "lite" column list for the many
// list/expired/repair queries that don't need merge-detail (merge_path/tier/
// progress/error/quality) or AI columns. Pair every SELECT that uses it with
// scanRecordingRow; both must stay in sync. (#224 — mirrors the
// selectTimelapseMergeColumns / scanTimelapseMerge pattern in db_timelapse_merge.go.)
const selectRecordingColumns = `SELECT id, camera_id, file_path, format, started_at, ended_at, duration, file_size, frame_count, merge_status, archived, motion_score, motion_confidence, activity_flags, COALESCE(layer,0) FROM recordings`

// scanRecordingRow scans one row of selectRecordingColumns (14 cols) via the
// shared scanner interface (satisfied by *sql.Row and *sql.Rows), then applies
// the time/merge_status defaulting. It replaces the per-call-site
// rows.Scan(...) + scanRecording(...) duplication across ~10 list paths (#224).
func scanRecordingRow(s scanner) (*model.Recording, error) {
	var r model.Recording
	var startedAtStr, endedAtStr, mergeStatusStr sql.NullString
	if err := s.Scan(&r.ID, &r.CameraID, &r.FilePath, &r.Format, &startedAtStr, &endedAtStr, &r.Duration, &r.FileSize, &r.FrameCount, &mergeStatusStr, &r.Archived, &r.MotionScore, &r.MotionConfidence, &r.ActivityFlags, &r.Layer); err != nil {
		return nil, err
	}
	scanRecording(&r, startedAtStr, endedAtStr, mergeStatusStr)
	return &r, nil
}

// scanRecording scans the standard recording columns from a row. It handles the
// time fields and merge_status defaulting that are common across all SELECT paths.
// AI fields (ai_status, ai_processed_at, ai_error) are scanned separately by each
// caller that includes them in the SELECT column list.
func scanRecording(r *model.Recording, startedAtStr, endedAtStr, mergeStatusStr sql.NullString) {
	r.StartedAt = scanTime(startedAtStr)
	r.EndedAt = scanTime(endedAtStr)
	if mergeStatusStr.Valid && mergeStatusStr.String != "" {
		r.MergeStatus = mergeStatusStr.String
	} else if r.MergeStatus == "" {
		r.MergeStatus = model.MergeStatusPending
	}
}

// scanAIFields scans the AI processing columns into a Recording. Called after
// the main Scan for SELECTs that include ai_status/ai_processed_at/ai_error.
func scanAIFields(r *model.Recording, aiStatus, aiProcessedAt, aiError sql.NullString) {
	if aiStatus.Valid {
		r.AIStatus = aiStatus.String
	}
	// NULL stays a nil pointer: a zero time.Time would serialize as
	// "0001-01-01T00:00:00Z" (omitempty cannot omit structs), which clients
	// have mistaken for a real processing timestamp.
	if aiProcessedAt.Valid && aiProcessedAt.String != "" {
		if t, err := parseTime(aiProcessedAt.String); err == nil {
			r.AIProcessedAt = &t
		} else {
			logger.Warn("scanAIFields: failed to parse ai_processed_at", "value", aiProcessedAt.String, "error", err)
		}
	}
	if aiError.Valid {
		r.AIError = aiError.String
	}
}

func (d *DB) InsertRecording(ctx context.Context, r *model.Recording) error {
	defer d.observeQuery("InsertRecording", time.Now())
	q := `INSERT INTO recordings(id, camera_id, file_path, format, started_at, ended_at, duration, file_size, frame_count, merge_status, merge_tier, layer) VALUES(?,?,?,?,?,?,?,?,?,?,?,?);`
	mergeStatus := r.MergeStatus
	if mergeStatus == "" {
		mergeStatus = model.MergeStatusPending
	}
	_, err := d.db.ExecContext(ctx, q, r.ID, r.CameraID, r.FilePath, r.Format, timeToDB(r.StartedAt), timeToDB(r.EndedAt), r.Duration, r.FileSize, r.FrameCount, mergeStatus, r.MergeTier, r.Layer)
	return err
}

// InsertRecordingWithRetry wraps InsertRecording with retry logic for SQLITE_BUSY errors.
// It retries up to maxRetries attempts with a fixed backoff between retries.
// Non-SQLITE_BUSY errors are returned immediately without retry.
func (d *DB) InsertRecordingWithRetry(ctx context.Context, r *model.Recording, maxRetries int, backoff time.Duration) error {
	var lastErr error
	for attempt := range maxRetries {
		if attempt > 0 {
			logger.Warn(
				"insert recording: database busy, retrying",
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
	logger.Error(
		"insert recording: exhausted retries",
		"camera_id", r.CameraID,
		"file_path", r.FilePath,
		"max_retries", maxRetries,
		"error", lastErr,
	)
	return fmt.Errorf("insert recording failed after %d attempts: %w", maxRetries, lastErr)
}

func (d *DB) UpdateRecording(ctx context.Context, r *model.Recording) error {
	q := `UPDATE recordings SET camera_id=?, file_path=?, format=?, started_at=?, ended_at=?, duration=?, file_size=?, frame_count=?, merge_status=?, merge_tier=? WHERE id=?;`
	_, err := d.db.ExecContext(ctx, q, r.CameraID, r.FilePath, r.Format, timeToDB(r.StartedAt), timeToDB(r.EndedAt), r.Duration, r.FileSize, r.FrameCount, r.MergeStatus, r.MergeTier, r.ID)
	return err
}

func (d *DB) GetRecording(ctx context.Context, id string) (*model.Recording, error) {
	row := d.readConn().QueryRowContext(ctx, `SELECT id, camera_id, file_path, format, started_at, ended_at, duration, file_size, frame_count, merge_status, merge_path, merge_tier, merge_progress, merge_error, merge_quality, archived, ai_status, ai_processed_at, ai_error, motion_score, motion_confidence, activity_flags, timeline_map, COALESCE(layer,0) FROM recordings WHERE id=?;`, id)
	var r model.Recording
	var startedAtStr, endedAtStr, mergePathStr, mergeTierStr, mergeErrorStr, mergeQualityStr sql.NullString
	var mergeProgress sql.NullInt64
	var aiStatus, aiProcessedAt, aiError, timelineMap sql.NullString
	if err := row.Scan(&r.ID, &r.CameraID, &r.FilePath, &r.Format, &startedAtStr, &endedAtStr, &r.Duration, &r.FileSize, &r.FrameCount, &r.MergeStatus, &mergePathStr, &mergeTierStr, &mergeProgress, &mergeErrorStr, &mergeQualityStr, &r.Archived, &aiStatus, &aiProcessedAt, &aiError, &r.MotionScore, &r.MotionConfidence, &r.ActivityFlags, &timelineMap, &r.Layer); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	r.StartedAt = scanTime(startedAtStr)
	r.EndedAt = scanTime(endedAtStr)
	if timelineMap.Valid {
		r.TimelineMap = timelineMap.String
	}
	if mergePathStr.Valid {
		r.MergePath = mergePathStr.String
	}
	if mergeTierStr.Valid {
		r.MergeTier = mergeTierStr.String
	}
	if mergeProgress.Valid {
		r.MergeProgress = int(mergeProgress.Int64)
	}
	if mergeErrorStr.Valid {
		r.MergeError = mergeErrorStr.String
	}
	if mergeQualityStr.Valid && mergeQualityStr.String != "" {
		r.MergeQuality = mergeQualityStr.String
	} else {
		r.MergeQuality = model.MergeQualityComplete
	}
	if r.MergeStatus == "" {
		r.MergeStatus = model.MergeStatusPending
	}
	scanAIFields(&r, aiStatus, aiProcessedAt, aiError)
	return &r, nil
}

func (d *DB) GetRecordingsByIDBatch(ctx context.Context, ids []string) ([]model.Recording, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	placeholders := make([]string, len(ids))
	args := make([]interface{}, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	q := "SELECT id, camera_id, file_path, format, started_at, ended_at, duration, file_size, frame_count, merge_status, merge_path, merge_tier, merge_progress, merge_error, merge_quality, archived FROM recordings WHERE id IN (" + strings.Join(placeholders, ",") + ")"
	rows, err := d.readConn().QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("get recordings by id batch: %w", err)
	}
	defer rows.Close()
	var res []model.Recording
	for rows.Next() {
		var r model.Recording
		var startedAtStr, endedAtStr, mergePathStr, mergeTierStr, mergeErrorStr, mergeQualityStr sql.NullString
		var mergeProgress sql.NullInt64
		if err := rows.Scan(&r.ID, &r.CameraID, &r.FilePath, &r.Format, &startedAtStr, &endedAtStr, &r.Duration, &r.FileSize, &r.FrameCount, &r.MergeStatus, &mergePathStr, &mergeTierStr, &mergeProgress, &mergeErrorStr, &mergeQualityStr, &r.Archived); err != nil {
			return nil, fmt.Errorf("get recordings by id batch: %w", err)
		}
		r.StartedAt = scanTime(startedAtStr)
		r.EndedAt = scanTime(endedAtStr)
		if mergePathStr.Valid {
			r.MergePath = mergePathStr.String
		}
		if mergeTierStr.Valid {
			r.MergeTier = mergeTierStr.String
		}
		if mergeProgress.Valid {
			r.MergeProgress = int(mergeProgress.Int64)
		}
		if mergeErrorStr.Valid {
			r.MergeError = mergeErrorStr.String
		}
		if mergeQualityStr.Valid && mergeQualityStr.String != "" {
			r.MergeQuality = mergeQualityStr.String
		} else {
			r.MergeQuality = model.MergeQualityComplete
		}
		if r.MergeStatus == "" {
			r.MergeStatus = model.MergeStatusPending
		}
		res = append(res, r)
	}
	return res, nil
}

func (d *DB) ListRecordings(ctx context.Context, filter model.RecordingFilter) ([]model.Recording, error) {
	defer d.observeQuery("ListRecordings", time.Now())
	where, args := recordingsFilterWhere(filter)

	// Keyset (cursor) pagination: when a cursor is provided and the sort is the default
	// (started_at DESC), inject "started_at < ?" instead of using OFFSET. This is O(1)
	// regardless of page depth — OFFSET N must scan+skip N rows, making "page 200" take
	// seconds at scale. The cursor is the started_at of the last row on the previous page.
	useKeyset := false
	if filter.Cursor != "" {
		// Keyset only applies to the default sort direction. For ASC the seek predicate
		// is "started_at > ?" (not implemented — DESC is the UI default).
		sortBy := filter.SortBy
		if sortBy == "" {
			sortBy = "started_at"
		}
		sortOrder := filter.SortOrder
		if sortOrder == "" {
			sortOrder = "desc"
		}
		if sortBy == "started_at" && strings.EqualFold(sortOrder, "desc") {
			where = append(where, "started_at < ?")
			args = append(args, filter.Cursor)
			useKeyset = true
		}
	}

	sqlstr := "SELECT id, camera_id, file_path, format, started_at, ended_at, duration, file_size, frame_count, merge_status, merge_path, merge_tier, merge_progress, merge_error, merge_quality, archived, motion_score, motion_confidence, activity_flags, COALESCE(layer,0) FROM recordings"
	if len(where) > 0 {
		sqlstr += " WHERE " + strings.Join(where, " AND ")
	}
	sqlstr += recordingsOrderByClause(filter)
	if filter.Limit > 0 {
		sqlstr += fmt.Sprintf(" LIMIT %d", filter.Limit)
	}
	// OFFSET is only used for legacy (non-cursor) pagination.
	if !useKeyset && filter.Offset > 0 {
		sqlstr += fmt.Sprintf(" OFFSET %d", filter.Offset)
	}
	sqlstr += ";"
	rows, err := d.readConn().QueryContext(ctx, sqlstr, args...)
	if err != nil {
		return nil, fmt.Errorf("list recordings: %w", err)
	}
	defer rows.Close()
	return scanRecordingRows(rows)
}

// ListRecordingsForVisionRepush returns up to limit recordings whose segments
// completed within [since, until] and that Vision has not finished processing —
// the offline-compensation window for the vision push coordinator (#329).
// The window is keyed on ended_at (completion): segments that started before
// the pause but landed while offline must still be compensated; a small grace
// on since covers ordering slack between segment completion and the push
// attempt. Excludes timelapse segments (never pushed live) and merged segments
// (files are typically reclaimed by the merge pipeline), and drops anything
// already in a terminal ai_status (completed/failed).
func (d *DB) ListRecordingsForVisionRepush(ctx context.Context, since, until time.Time, limit int) ([]model.Recording, error) {
	defer d.observeQuery("ListRecordingsForVisionRepush", time.Now())
	rows, err := d.readConn().QueryContext(ctx, `
		SELECT id, camera_id, file_path, format, started_at, ended_at, duration, file_size, frame_count, merge_status, merge_path, merge_tier, merge_progress, merge_error, merge_quality, archived, motion_score, motion_confidence, activity_flags, COALESCE(layer,0)
		FROM recordings
		WHERE ended_at>=? AND ended_at<=?
			AND format != 'timelapse'
			AND COALESCE(merge_status,'') NOT IN ('merged','daily_merged')
			AND COALESCE(layer,0)=0
			AND COALESCE(ai_status,'') IN ('','pending','processing')
		ORDER BY started_at ASC
		LIMIT ?;`,
		timeToDB(since.Add(-time.Minute)), timeToDB(until), limit)
	if err != nil {
		return nil, fmt.Errorf("list recordings for vision repush: %w", err)
	}
	defer rows.Close()
	return scanRecordingRows(rows)
}

// ListRecordingsWithTotal returns a page of recordings plus the total count matching the
// filter. It runs ListRecordings (covering index, no sort) for the page, and a cached
// CountRecordingsWithFilter for the total — collapsing repeated counts for the same
// filter within countCacheTTL (e.g. rapid pagination clicks, gallery+list views).
//
// This deliberately does NOT use COUNT(*) OVER(): production EXPLAIN QUERY PLAN on 86K
// rows showed the window function forces a full index scan + TEMP B-TREE FOR ORDER BY
// (the planner can't satisfy ORDER BY from the index when OVER() materializes the set),
// taking ~3.9s vs ~5ms for the separated queries. See storage-optimization.md §4.
func (d *DB) ListRecordingsWithTotal(ctx context.Context, filter model.RecordingFilter) ([]model.Recording, int, error) {
	recs, err := d.ListRecordings(ctx, filter)
	if err != nil {
		return nil, 0, err
	}
	total, err := d.countRecordingsCached(ctx, filter)
	if err != nil {
		//nolint:nilerr // intentional: count is best-effort; a failed COUNT must not fail the list request
		return recs, 0, nil
	}
	return recs, total, nil
}

// countRecordingsCached wraps CountRecordingsWithFilter with a short-TTL in-memory cache
// keyed by the filter signature. Paginated UIs hit ListRecordingsWithTotal on every page
// navigation; without caching, each page re-runs a full COUNT over the filtered set.
// The cache key omits Limit/Offset/Sort (pagination params don't affect the total).
func (d *DB) countRecordingsCached(ctx context.Context, filter model.RecordingFilter) (int, error) {
	key := countCacheKey(filter)
	now := time.Now()
	d.countMu.Lock()
	if d.countCache == nil {
		d.countCache = make(map[string]*countCacheEntry)
	}
	if entry, ok := d.countCache[key]; ok && now.Before(entry.expiryAt) {
		d.countMu.Unlock()
		return entry.value, nil
	}
	// Opportunistic eviction of expired entries to keep the map bounded.
	for k, e := range d.countCache {
		if !now.Before(e.expiryAt) {
			delete(d.countCache, k)
		}
	}
	d.countMu.Unlock()

	count, err := d.CountRecordingsWithFilter(ctx, filter)
	if err != nil {
		return 0, err
	}
	d.countMu.Lock()
	d.countCache[key] = &countCacheEntry{value: count, expiryAt: now.Add(countCacheTTL)}
	d.countMu.Unlock()
	return count, nil
}

// countCacheKey builds a stable string signature of the count-relevant filter fields.
// Limit/Offset/SortBy/SortOrder are excluded — they don't change the total match count.
func countCacheKey(f model.RecordingFilter) string {
	merged := ""
	if f.Merged != nil {
		merged = "t"
		if !*f.Merged {
			merged = "f"
		}
	}
	archived := "d" // default (nil) = archived=0
	if f.Archived != nil {
		if *f.Archived {
			archived = "t"
		} else {
			archived = "f"
		}
	}
	var b strings.Builder
	b.Grow(128)
	b.WriteString(f.CameraID)
	b.WriteByte('|')
	b.WriteString(string(f.Format))
	b.WriteByte('|')
	for _, fo := range f.Formats {
		b.WriteString(string(fo))
		b.WriteByte(',')
	}
	b.WriteByte('|')
	b.WriteString(f.Search)
	b.WriteByte('|')
	b.WriteString(f.StartTime.Format("2006-01-02T15:04:05.999"))
	b.WriteByte('|')
	b.WriteString(f.EndTime.Format("2006-01-02T15:04:05.999"))
	b.WriteByte('|')
	b.WriteString(merged)
	b.WriteByte('|')
	b.WriteString(archived)
	b.WriteByte('|')
	// AiClass must be part of the key: without it, ?ai_class=person collides
	// with the unfiltered count cache entry and returns the wrong total.
	b.WriteString(f.AiClass)
	b.WriteByte('|')
	// Motion filters (issue #435) participate in the key for the same reason.
	if f.MinMotionScore != nil {
		b.WriteString(strconv.FormatFloat(*f.MinMotionScore, 'g', -1, 64))
	}
	b.WriteByte('|')
	b.WriteString(f.Activity)
	return b.String()
}

// recordingsFilterWhere builds the WHERE clause fragments and bind args shared by
// ListRecordings, ListRecordingsWithTotal, and CountRecordingsWithFilter.
func recordingsFilterWhere(filter model.RecordingFilter) ([]string, []any) {
	where := []string{}
	args := []any{}
	if filter.CameraID != "" {
		where = append(where, "camera_id=?")
		args = append(args, filter.CameraID)
	}
	// Tier filter (#637): default (nil) hides the continuous sub-stream tier
	// from lists/timeline; an explicit layer selects exactly that tier.
	if filter.Layer != nil {
		where = append(where, "COALESCE(layer,0)=?")
		args = append(args, *filter.Layer)
	} else {
		where = append(where, "COALESCE(layer,0)=0")
	}
	if filter.Merged != nil {
		if *filter.Merged {
			where = append(where, "merge_status IN ('merged','daily_merged')")
		} else {
			where = append(where, "merge_status NOT IN ('merged','daily_merged')")
		}
	}
	if !filter.StartTime.IsZero() {
		where = append(where, "started_at>=?")
		args = append(args, formatTime(filter.StartTime))
	}
	if !filter.EndTime.IsZero() {
		where = append(where, "started_at<=?")
		args = append(args, formatTime(filter.EndTime))
	}
	if len(filter.Formats) > 0 {
		placeholders := make([]string, len(filter.Formats))
		for i, f := range filter.Formats {
			placeholders[i] = "?"
			args = append(args, string(f))
		}
		where = append(where, "format IN ("+strings.Join(placeholders, ",")+")")
	} else if filter.Format != "" {
		where = append(where, "format=?")
		args = append(args, string(filter.Format))
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
	// AI class filter: keep only recordings that have ≥1 AI event with the given
	// class_name. Uses a correlated EXISTS against ai_events joined on recording_id
	// (both TEXT snowflake IDs). Leverages idx_ai_events_class_recording.
	if filter.AiClass != "" {
		where = append(where, "EXISTS(SELECT 1 FROM ai_events WHERE ai_events.recording_id = recordings.id AND ai_events.class_name = ?)")
		args = append(args, filter.AiClass)
	}
	// Motion filters (issue #435). Unanalyzed recordings (motion_score < 0)
	// never pass a MinMotionScore filter; the activity LIKE matches the
	// comma-separated flags vocabulary (static/motion/scene_cut).
	if filter.MinMotionScore != nil {
		where = append(where, "motion_score >= ?")
		args = append(args, *filter.MinMotionScore)
	}
	if filter.Activity != "" {
		// ESCAPE '\' pairs with escapeLike — flag values contain underscores
		// (scene_cut) that must match literally, not as the single-char wildcard.
		where = append(where, `activity_flags LIKE ? ESCAPE '\'`)
		args = append(args, "%"+escapeLike(filter.Activity)+"%")
	}
	return where, args
}

// recordingsOrderByClause returns the ORDER BY clause for a filter, restricted to a
// whitelist of sortable columns. Defaults to started_at DESC.
func recordingsOrderByClause(filter model.RecordingFilter) string {
	allowedSortFields := map[string]bool{"started_at": true, "duration": true, "file_size": true, "camera_id": true}
	sortBy := "started_at"
	if filter.SortBy != "" && allowedSortFields[filter.SortBy] {
		sortBy = filter.SortBy
	}
	sortOrder := "DESC"
	if strings.EqualFold(filter.SortOrder, "asc") {
		sortOrder = "ASC"
	}
	return " ORDER BY " + sortBy + " " + sortOrder
}

// scanRecordingRows scans a standard recordings SELECT (no OVER() column) into a slice.
func scanRecordingRows(rows *sql.Rows) ([]model.Recording, error) {
	var res []model.Recording
	for rows.Next() {
		var r model.Recording
		var startedAtStr, endedAtStr, mergeStatusStr, mergePathStr, mergeTierStr, mergeErrorStr, mergeQualityStr sql.NullString
		var mergeProgress sql.NullInt64
		if err := rows.Scan(&r.ID, &r.CameraID, &r.FilePath, &r.Format, &startedAtStr, &endedAtStr, &r.Duration, &r.FileSize, &r.FrameCount, &mergeStatusStr, &mergePathStr, &mergeTierStr, &mergeProgress, &mergeErrorStr, &mergeQualityStr, &r.Archived, &r.MotionScore, &r.MotionConfidence, &r.ActivityFlags, &r.Layer); err != nil {
			return nil, fmt.Errorf("scan recordings: %w", err)
		}
		if mergeStatusStr.Valid && mergeStatusStr.String != "" {
			r.MergeStatus = mergeStatusStr.String
		} else if r.MergeStatus == "" {
			r.MergeStatus = model.MergeStatusPending
		}
		if mergePathStr.Valid {
			r.MergePath = mergePathStr.String
		}
		if mergeTierStr.Valid {
			r.MergeTier = mergeTierStr.String
		}
		if mergeProgress.Valid {
			r.MergeProgress = int(mergeProgress.Int64)
		}
		if mergeErrorStr.Valid {
			r.MergeError = mergeErrorStr.String
		}
		if mergeQualityStr.Valid && mergeQualityStr.String != "" {
			r.MergeQuality = mergeQualityStr.String
		} else {
			r.MergeQuality = model.MergeQualityComplete
		}
		r.StartedAt = scanTime(startedAtStr)
		r.EndedAt = scanTime(endedAtStr)
		res = append(res, r)
	}
	return res, rows.Err()
}

func (d *DB) CountRecordingsWithFilter(ctx context.Context, filter model.RecordingFilter) (int, error) {
	ctx, cancel := withHeavyQueryTimeout(ctx)
	defer cancel()
	defer d.observeQuery("CountRecordingsWithFilter", time.Now())
	where, args := recordingsFilterWhere(filter)
	sqlstr := "SELECT COUNT(*) FROM recordings"
	if len(where) > 0 {
		sqlstr += " WHERE " + strings.Join(where, " AND ")
	}
	var count int
	err := d.readConn().QueryRowContext(ctx, sqlstr, args...).Scan(&count)
	return count, err
}

// maxTimelineSegments is the row ceiling for ListRecordingTimelineSegments. It
// is deliberately much higher than the 500-row cap on handleListRecordings
// because each timeline row carries only 7 small columns (~10x smaller than a
// full Recording), so shipping a whole fragmented day in one response is cheap
// and avoids the silent afternoon-truncation bug (issue #115). 10k covers the
// worst observed fragmentation (Xiaomi reconnect storms ~5k/day) with headroom.
const maxTimelineSegments = 10000

// ListRecordingTimelineSegments returns the lightweight timeline projection of
// recordings matching the filter. It reuses recordingsFilterWhere (so camera_id
// / start / end / format / merged / archived all work) but selects only the 7
// columns a timeline needs, forces ORDER BY started_at ASC (timelines always
// render left-to-right), and caps at maxTimelineSegments.
//
// Returns (segments, total) where total is the unfiltered-by-limit match count
// (via the cached CountRecordingsWithFilter) so the caller can set a truncated
// flag. The cache key already omits Limit/Offset/Sort, so the count is correct
// regardless of the 10k cap.
func (d *DB) ListRecordingTimelineSegments(ctx context.Context, filter model.RecordingFilter) ([]model.TimelineSegment, int, error) {
	defer d.observeQuery("ListRecordingTimelineSegments", time.Now())
	where, args := recordingsFilterWhere(filter)

	// 8-column projection — omit file_path/merge_*/file_size/frame_count/etc.
	// motion_score lets the timeline render an activity heat strip (issue #435);
	// motion_confidence discounts bitrate-starved segments whose relative score
	// is noise (issue #634).
	sqlstr := "SELECT id, camera_id, started_at, ended_at, duration, format, merge_status, motion_score, motion_confidence FROM recordings"
	if len(where) > 0 {
		sqlstr += " WHERE " + strings.Join(where, " AND ")
	}
	// Timeline always renders left→right; ignore caller SortBy/SortOrder.
	sqlstr += " ORDER BY started_at ASC"
	sqlstr += fmt.Sprintf(" LIMIT %d", maxTimelineSegments)
	sqlstr += ";"

	rows, err := d.readConn().QueryContext(ctx, sqlstr, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	res := make([]model.TimelineSegment, 0, 64)
	for rows.Next() {
		var s model.TimelineSegment
		var startedAtStr, endedAtStr, mergeStatusStr sql.NullString
		if err := rows.Scan(&s.ID, &s.CameraID, &startedAtStr, &endedAtStr, &s.Duration, &s.Format, &mergeStatusStr, &s.MotionScore, &s.MotionConfidence); err != nil {
			return nil, 0, err
		}
		s.StartedAt = scanTime(startedAtStr)
		s.EndedAt = scanTime(endedAtStr)
		if mergeStatusStr.Valid && mergeStatusStr.String != "" {
			s.MergeStatus = mergeStatusStr.String
		} else {
			s.MergeStatus = model.MergeStatusPending
		}
		res = append(res, s)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}

	// Total (uncapped) so the handler can flag truncation. Best-effort: a failed
	// count must not fail the timeline render — mirror ListRecordingsWithTotal.
	total, err := d.countRecordingsCached(ctx, filter)
	if err != nil {
		return res, len(res), nil
	}
	return res, total, nil
}

// DailyRecordingSummary returns per-day recording counts and format categories for the
// given filter, grouped by local date. tzOffsetMinutes is the client's signed UTC offset
// in minutes (e.g. 480 for UTC+8, -300 for UTC-5); 0 groups by UTC date. The result is
// bounded by the number of days in the date range (max 31 for a month), so no LIMIT is needed.
func (d *DB) DailyRecordingSummary(ctx context.Context, filter model.RecordingFilter, tzOffsetMinutes int) ([]model.RecordingDaySummary, error) {
	// The tz modifier is the first bound parameter (positionally before any WHERE
	// args) because the SELECT uses date(started_at, ?). Reuse the shared
	// recordingsFilterWhere (#235) and prepend the modifier to keep arg order.
	modifier := fmt.Sprintf("%d minutes", tzOffsetMinutes)
	where, whereArgs := recordingsFilterWhere(filter)
	args := append([]any{modifier}, whereArgs...)

	// Conditional aggregation (version-safe — no GROUP_CONCAT(DISTINCT) dependency).
	// MAX(expr) over a group returns 1 if any row satisfies the condition, 0 otherwise.
	sqlstr := `SELECT date(started_at, ?) AS d, COUNT(*) AS cnt, ` +
		`MAX(format IN ('h264','h265','avi')) AS has_video, ` +
		`MAX(format='timelapse') AS has_timelapse, ` +
		`MAX(format='mjpeg') AS has_mjpeg ` +
		`FROM recordings`
	if len(where) > 0 {
		sqlstr += " WHERE " + strings.Join(where, " AND ")
	}
	sqlstr += " GROUP BY d ORDER BY d;"

	rows, err := d.readConn().QueryContext(ctx, sqlstr, args...)
	if err != nil {
		return nil, fmt.Errorf("daily recording summary: %w", err)
	}
	defer rows.Close()

	var res []model.RecordingDaySummary
	for rows.Next() {
		var date string
		var count int
		var hasVideo, hasTimelapse, hasMjpeg int
		if err := rows.Scan(&date, &count, &hasVideo, &hasTimelapse, &hasMjpeg); err != nil {
			return nil, fmt.Errorf("daily recording summary: %w", err)
		}
		formats := []string{}
		if hasVideo > 0 {
			formats = append(formats, "video")
		}
		if hasTimelapse > 0 {
			formats = append(formats, "timelapse")
		}
		if hasMjpeg > 0 {
			formats = append(formats, "mjpeg")
		}
		res = append(res, model.RecordingDaySummary{Date: date, Count: count, Formats: formats})
	}
	return res, nil
}

// GetRecordingsByPathSet returns a set of file paths that exist in the recordings table.
// Used for orphan file reconciliation to determine which files are already registered.
//
// Note: this query does NOT scope by camera_id and relies on a full scan of the
// recordings table (file_path is not indexed). For orphan reconciliation where
// the caller already knows the camera_id, prefer GetRecordingPathsByCamera which
// uses the idx_recordings_camera_time index for a bounded range scan.
func (d *DB) GetRecordingsByPathSet(ctx context.Context, paths []string) (map[string]bool, error) {
	result := make(map[string]bool)
	if len(paths) == 0 {
		return result, nil
	}
	// SQLite's SQLITE_MAX_VARIABLE_NUMBER defaults to 999 (older) or 32766
	// (3.32+). modernc.org/sqlite sets 32766, but to stay portable and avoid
	// pathological planner behavior on huge IN lists, chunk at 500.
	const chunkSize = 500
	for start := 0; start < len(paths); start += chunkSize {
		end := start + chunkSize
		if end > len(paths) {
			end = len(paths)
		}
		chunk := paths[start:end]
		placeholders := make([]string, len(chunk))
		args := make([]interface{}, len(chunk))
		for i, p := range chunk {
			placeholders[i] = "?"
			args[i] = p
		}
		q := "SELECT file_path FROM recordings WHERE file_path IN (" + strings.Join(placeholders, ",") + ")"
		if err := func() error {
			rows, err := d.readConn().QueryContext(ctx, q, args...)
			if err != nil {
				return fmt.Errorf("get recordings by path set: %w", err)
			}
			defer rows.Close()
			for rows.Next() {
				var p string
				if err := rows.Scan(&p); err == nil {
					result[p] = true
				}
			}
			return rows.Err()
		}(); err != nil {
			return nil, err
		}
	}
	return result, nil
}

// GetRecordingPathsByCamera returns the set of file_path values stored for a
// single camera. Used by orphan reconciliation as a much cheaper replacement
// for GetRecordingsByPathSet when the caller knows the camera_id: it uses the
// idx_recordings_camera_time(camera_id, ...) index for a bounded range scan
// (only that camera's rows) instead of scanning the entire recordings table.
//
// The returned set is the complete path list for the camera; callers intersect
// it with their disk scan locally (O(n) hashset lookup) rather than via a
// potentially huge IN (?, ?, ...) list. On a production tree with ~15k
// recordings, this reduces per-camera reconciliation IO from a full table scan
// to an index range read of just that camera's rows.
func (d *DB) GetRecordingPathsByCamera(ctx context.Context, cameraID string) (map[string]bool, error) {
	result := make(map[string]bool)
	rows, err := d.readConn().QueryContext(ctx,
		`SELECT file_path FROM recordings WHERE camera_id = ?`, cameraID)
	if err != nil {
		return nil, fmt.Errorf("get recording paths by camera: %w", err)
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

// orphanBatchSize controls how many orphans are inserted per transaction.
// Larger batches reduce transaction count but increase lock duration.
// 500 strikes a balance between throughput and write lock hold time.
const orphanBatchSize = 500

// InsertOrphanRecordings batch-inserts orphan recording metadata using INSERT OR IGNORE.
// Returns the number of actually inserted rows (skips duplicates).
// Inserts are performed in batches of orphanBatchSize to avoid holding the
// write lock for too long. Context timeout is checked between batches.
func (d *DB) InsertOrphanRecordings(ctx context.Context, recordings []*model.Recording) (int, error) {
	if len(recordings) == 0 {
		return 0, nil
	}
	if ctx.Err() != nil {
		return 0, ctx.Err()
	}

	q := `INSERT OR IGNORE INTO recordings(id, camera_id, file_path, format, started_at, ended_at, duration, file_size, frame_count, merge_status) VALUES(?,?,?,?,?,?,?,?,?,?);`
	totalInserted := 0

	for i := 0; i < len(recordings); i += orphanBatchSize {
		if ctx.Err() != nil {
			return totalInserted, ctx.Err()
		}

		end := i + orphanBatchSize
		if end > len(recordings) {
			end = len(recordings)
		}

		tx, err := d.db.BeginTx(ctx, nil)
		if err != nil {
			return totalInserted, err
		}

		batchInserted := 0
		for _, r := range recordings[i:end] {
			result, err := tx.ExecContext(ctx, q, r.ID, r.CameraID, r.FilePath, r.Format, timeToDB(r.StartedAt), timeToDB(r.EndedAt), r.Duration, r.FileSize, r.FrameCount, r.MergeStatus)
			if err != nil {
				tx.Rollback()
				return totalInserted, err
			}
			n, _ := result.RowsAffected()
			batchInserted += int(n)
		}

		if err := tx.Commit(); err != nil {
			return totalInserted, err
		}
		totalInserted += batchInserted
	}

	return totalInserted, nil
}

func (d *DB) DeleteRecording(ctx context.Context, id string) error {
	_, err := d.db.ExecContext(ctx, `DELETE FROM recordings WHERE id=?;`, id)
	return err
}

// DeleteRecordingsBatch deletes multiple recordings by ID using a single batch DELETE.
// Returns the slice of IDs requested for deletion on success (nil on failure).
// Uses a single IN clause to minimize transaction duration and SQLITE_BUSY contention.
func (d *DB) DeleteRecordingsBatch(ctx context.Context, ids []string) ([]string, error) {
	if len(ids) == 0 {
		return nil, nil
	}

	placeholders := make([]string, len(ids))
	args := make([]interface{}, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	q := "DELETE FROM recordings WHERE id IN (" + strings.Join(placeholders, ",") + ")"
	res, err := d.db.ExecContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("delete recordings batch: %w", err)
	}
	n, _ := res.RowsAffected()
	if n > 0 {
		return ids, nil
	}
	return nil, nil
}

func (d *DB) SetMerged(ctx context.Context, id string, merged bool) error {
	status := model.MergeStatusPending
	if merged {
		status = model.MergeStatusMerged
	}
	_, err := d.db.ExecContext(ctx, `UPDATE recordings SET merge_status=? WHERE id=?;`, status, id)
	return err
}

func (d *DB) CleanupIncomplete(ctx context.Context) error {
	_, err := d.db.ExecContext(ctx, `DELETE FROM recordings WHERE ended_at IS NULL;`)
	return err
}

// listExpiredRecordings is the shared core of the three ListExpired* methods
// below (#237). cameraID=="" means "all cameras"; archived selects the
// archived=0/1 partition. retentionDays is always applied.
func (d *DB) listExpiredRecordings(ctx context.Context, cameraID string, archived, retentionDays int) ([]model.Recording, error) {
	sqlstr := selectRecordingColumns + ` WHERE ended_at IS NOT NULL AND archived=?`
	args := []any{archived}
	if cameraID != "" {
		sqlstr += ` AND camera_id=?`
		args = append(args, cameraID)
	}
	sqlstr += ` AND ended_at < datetime('now', '-' || ? || ' days') ORDER BY ended_at ASC;`
	args = append(args, retentionDays)

	rows, err := d.readConn().QueryContext(ctx, sqlstr, args...)
	if err != nil {
		return nil, fmt.Errorf("list expired recordings: %w", err)
	}
	defer rows.Close()
	var res []model.Recording
	for rows.Next() {
		r, err := scanRecordingRow(rows)
		if err != nil {
			return nil, fmt.Errorf("list expired recordings: %w", err)
		}
		res = append(res, *r)
	}
	return res, nil
}

func (d *DB) ListExpiredRecordings(ctx context.Context, retentionDays int) ([]model.Recording, error) {
	return d.listExpiredRecordings(ctx, "", 0, retentionDays)
}

// ListExpiredRecordingsByCamera returns expired recordings for a specific camera
func (d *DB) ListExpiredRecordingsByCamera(ctx context.Context, cameraID string, retentionDays int) ([]model.Recording, error) {
	return d.listExpiredRecordings(ctx, cameraID, 0, retentionDays)
}

// ListExpiredArchivedRecordingsByCamera returns expired archived recordings for a specific camera.
func (d *DB) ListExpiredArchivedRecordingsByCamera(ctx context.Context, cameraID string, retentionDays int) ([]model.Recording, error) {
	return d.listExpiredRecordings(ctx, cameraID, 1, retentionDays)
}

func (d *DB) ListOldestRecordings(ctx context.Context, limit int) ([]model.Recording, error) {
	sqlstr := selectRecordingColumns + ` WHERE ended_at IS NOT NULL AND archived=0 ORDER BY ended_at ASC LIMIT ?;`
	rows, err := d.readConn().QueryContext(ctx, sqlstr, limit)
	if err != nil {
		return nil, fmt.Errorf("list oldest recordings: %w", err)
	}
	defer rows.Close()
	var res []model.Recording
	for rows.Next() {
		r, err := scanRecordingRow(rows)
		if err != nil {
			return nil, fmt.Errorf("list oldest recordings: %w", err)
		}
		res = append(res, *r)
	}
	return res, nil
}

// ListOldestRecordingsMotionAware is ListOldestRecordings with boring-first
// ordering for the disk-threshold cleanup path (issue #435): static segments
// (motion_score≈0) are deleted first, active segments (score→1) last, and
// unanalyzed segments (-1) rank neutrally in between at 0.5. Age remains the
// tiebreaker within the same score band. Issue #634: the score is discounted
// by motion_confidence — a bitrate-starved segment's relative score is
// rate-control jitter, not activity, so it must not be protected from
// eviction. Rows analyzed before the confidence column (-1) keep full weight.
func (d *DB) ListOldestRecordingsMotionAware(ctx context.Context, limit int) ([]model.Recording, error) {
	sqlstr := selectRecordingColumns + ` WHERE ended_at IS NOT NULL AND archived=0
		ORDER BY (CASE WHEN motion_score < 0 THEN 0.5
			ELSE motion_score * (CASE WHEN motion_confidence < 0 THEN 1.0 ELSE motion_confidence END) END) ASC, ended_at ASC LIMIT ?;`
	rows, err := d.readConn().QueryContext(ctx, sqlstr, limit)
	if err != nil {
		return nil, fmt.Errorf("list oldest recordings motion-aware: %w", err)
	}
	defer rows.Close()
	var res []model.Recording
	for rows.Next() {
		r, err := scanRecordingRow(rows)
		if err != nil {
			return nil, fmt.Errorf("list oldest recordings motion-aware: %w", err)
		}
		res = append(res, *r)
	}
	return res, nil
}

// ListRecordingPathsByCamera returns the basenames of all file_path values for a camera's recordings.
func (d *DB) ListRecordingPathsByCamera(ctx context.Context, cameraID string) (map[string]bool, error) {
	rows, err := d.readConn().QueryContext(ctx, `SELECT file_path FROM recordings WHERE camera_id=?`, cameraID)
	if err != nil {
		return nil, fmt.Errorf("list recording paths by camera: %w", err)
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

// PathIsRecordingFile reports whether the given on-disk path is still
// referenced by any recording row for the camera — either as the source
// file_path or the merged-output merge_path. The comparison is on the full
// path, which is how both columns are stored (absolute paths under the storage
// root). Used by the orphan scanner (repair reclaim-orphan-merges) to avoid
// reclaiming files that still belong to a live recording.
func (d *DB) PathIsRecordingFile(ctx context.Context, cameraID, fullpath string) (bool, error) {
	var exists int
	err := d.readConn().QueryRowContext(ctx, `
		SELECT 1 FROM recordings
		WHERE camera_id=?
		  AND (file_path=? OR merge_path=?)
		LIMIT 1;`, cameraID, fullpath, fullpath).Scan(&exists)
	if err != nil {
		if err == sql.ErrNoRows {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// ListPendingMJPEGRecordings returns recordings for a camera where format IN ('mjpeg','jpeg')
// AND merge_status='pending' AND ended_at IS NOT NULL.
func (d *DB) ListPendingMJPEGRecordings(ctx context.Context, cameraID string) ([]model.Recording, error) {
	sqlstr := selectRecordingColumns + ` WHERE camera_id = ? AND format IN ('mjpeg','jpeg') AND merge_status = 'pending' AND ended_at IS NOT NULL;`
	rows, err := d.readConn().QueryContext(ctx, sqlstr, cameraID)
	if err != nil {
		return nil, fmt.Errorf("list pending mjpeg recordings: %w", err)
	}
	defer rows.Close()
	var res []model.Recording
	for rows.Next() {
		r, err := scanRecordingRow(rows)
		if err != nil {
			return nil, fmt.Errorf("list pending mjpeg recordings: %w", err)
		}
		res = append(res, *r)
	}
	return res, nil
}

// ListRecordingsWithoutTranscode returns recordings for a camera that have ended
// but have no corresponding transcoding task, and are not archived.
func (d *DB) ListRecordingsWithoutTranscode(ctx context.Context, cameraID string) ([]model.Recording, error) {
	sqlstr := selectRecordingColumns + ` WHERE camera_id = ? AND ended_at IS NOT NULL AND archived = 0 AND NOT EXISTS (SELECT 1 FROM transcoding_tasks WHERE recording_id = recordings.id) ORDER BY started_at DESC;`
	rows, err := d.readConn().QueryContext(ctx, sqlstr, cameraID)
	if err != nil {
		return nil, fmt.Errorf("list recordings without transcode: %w", err)
	}
	defer rows.Close()
	res := make([]model.Recording, 0)
	for rows.Next() {
		r, err := scanRecordingRow(rows)
		if err != nil {
			return nil, fmt.Errorf("list recordings without transcode: %w", err)
		}
		res = append(res, *r)
	}
	return res, nil
}

// RepairZeroDurationRecordings returns recordings where duration=0 but the file is
// non-trivial in size, non-MJPEG, has ended_at set, and merge_status=pending.
// These are candidates for duration repair via ffprobe.
func (d *DB) RepairZeroDurationRecordings(ctx context.Context) ([]model.Recording, error) {
	sqlstr := selectRecordingColumns + ` WHERE duration = 0 AND file_size > 1048576 AND format != 'mjpeg' AND ended_at IS NOT NULL AND merge_status = 'pending';`
	rows, err := d.readConn().QueryContext(ctx, sqlstr)
	if err != nil {
		return nil, fmt.Errorf("repair zero duration recordings: %w", err)
	}
	defer rows.Close()
	var res []model.Recording
	for rows.Next() {
		r, err := scanRecordingRow(rows)
		if err != nil {
			return nil, fmt.Errorf("repair zero duration recordings: %w", err)
		}
		res = append(res, *r)
	}
	return res, nil
}

// UpdateRecordingDuration updates the duration and ended_at for a recording.
func (d *DB) UpdateRecordingDuration(ctx context.Context, id string, duration float64, endedAt time.Time) error {
	_, err := d.db.ExecContext(ctx, `UPDATE recordings SET duration=?, ended_at=? WHERE id=?;`, duration, timeToDB(endedAt), id)
	return err
}

// ListZeroDurationRecordings returns recordings whose duration is 0 — these are
// corrupted segments where the recorder failed to compute ended_at at close time
// (a bug fixed in a prior release; historical rows remain). The video files are
// typically intact, so a CLI repair tool can re-probe the file duration and call
// UpdateRecordingDuration to restore correct metadata.
//
// cameraID="" scans all cameras. limit caps the result (0 = unlimited).
func (d *DB) ListZeroDurationRecordings(ctx context.Context, cameraID string, limit int) ([]*model.Recording, error) {
	query := selectRecordingColumns + ` WHERE duration = 0 AND ended_at IS NOT NULL`
	args := []interface{}{}
	if cameraID != "" {
		query += " AND camera_id = ?"
		args = append(args, cameraID)
	}
	query += " ORDER BY camera_id, started_at ASC"
	if limit > 0 {
		query += " LIMIT ?"
		args = append(args, limit)
	}
	query += ";"

	rows, err := d.readConn().QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list zero duration recordings: %w", err)
	}
	defer rows.Close()
	var res []*model.Recording
	for rows.Next() {
		r, err := scanRecordingRow(rows)
		if err != nil {
			return nil, fmt.Errorf("list zero duration recordings: %w", err)
		}
		res = append(res, r)
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("list zero duration recordings: %w", err)
		}
	}
	return res, nil
}

// UpdateRecordingAIStatus sets the AI processing status for a recording.
// Terminal statuses (completed — what the API handler validates and external
// consumers send — plus the legacy "done"/"skipped" spellings) stamp
// ai_processed_at. The list previously held only "done", which the handler
// rejects, so ai_processed_at was NEVER stamped on successful processing.
// Non-terminal statuses (pending/processing) leave it untouched.
//
// Multi-instance aggregate guard: with several vision consumers processing
// the same recording, the single ai_status column must never regress —
// writes are ranked (completed > skipped > failed > pending/processing/”)
// and only land when their rank EXCEEDS the current one. A consumer retry
// flow (failed → processing → completed) therefore shows "failed" until the
// final "completed" lands, and one instance's completed result is never
// clobbered by another's failed/skipped report.
func (d *DB) UpdateRecordingAIStatus(ctx context.Context, id, status, errMsg string) error {
	now := time.Now().UTC().Format("2006-01-02 15:04:05.999999999")
	rank := aiStatusRank(status)
	const currentRank = `CASE COALESCE(ai_status,'') WHEN 'completed' THEN 3 WHEN 'skipped' THEN 2 WHEN 'failed' THEN 1 ELSE 0 END`
	_, err := d.db.ExecContext(ctx,
		`UPDATE recordings SET
			ai_status = CASE WHEN ? >= (`+currentRank+`) THEN ? ELSE ai_status END,
			ai_error = CASE WHEN ? >= (`+currentRank+`) THEN ? ELSE ai_error END,
			ai_processed_at = CASE WHEN ? >= (`+currentRank+`) AND ? IN ('completed','done','failed','skipped') THEN ? ELSE ai_processed_at END
		WHERE id=?;`,
		rank, status,
		rank, errMsg,
		rank, status, now, id)
	return err
}

// aiStatusRank maps an ai_status value to its aggregate precedence.
func aiStatusRank(status string) int {
	switch status {
	case "completed":
		return 3
	case "skipped":
		return 2
	case "failed":
		return 1
	default: // pending / processing / ''
		return 0
	}
}

// GetRecordingAIStatus returns the AI processing status of a recording.
func (d *DB) GetRecordingAIStatus(ctx context.Context, id string) (status string, err error) {
	err = d.readConn().QueryRowContext(ctx, `SELECT COALESCE(ai_status, '') FROM recordings WHERE id=?`, id).Scan(&status)
	return
}

// MarkRecordingsSkippedByIDs marks the given recordings ai_status='skipped'
// with the drop reason in ai_error (#671 — consumer-reported queue drops).
// Only rows still in a non-terminal AI state (”, pending, processing) are
// touched; completed/failed/skipped stay as-is. Returns rows marked.
func (d *DB) MarkRecordingsSkippedByIDs(ctx context.Context, ids []string, aiErr string) (int64, error) {
	defer d.observeQuery("MarkRecordingsSkippedByIDs", time.Now())
	if len(ids) == 0 {
		return 0, nil
	}
	var sb strings.Builder
	sb.WriteString(`UPDATE recordings SET ai_status='skipped', ai_error=?, ai_processed_at=? WHERE id IN (`)
	for range ids {
		sb.WriteString("?,")
	}
	sbString := sb.String()
	sbString = sbString[:len(sbString)-1] + `) AND COALESCE(ai_status,'') IN ('','pending','processing');`

	args := make([]any, 0, len(ids)+2)
	args = append(args, aiErr, time.Now().UTC().Format("2006-01-02 15:04:05.999999999"))
	for _, id := range ids {
		args = append(args, id)
	}
	res, err := d.db.ExecContext(ctx, sbString, args...)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// MarkRecordingsSkippedByRange is the id-less fallback of
// MarkRecordingsSkippedByIDs: mark every non-terminal recording of the given
// camera whose started_at falls inside [from, to] (#671). Returns rows marked.
func (d *DB) MarkRecordingsSkippedByRange(ctx context.Context, cameraID string, from, to time.Time, aiErr string) (int64, error) {
	defer d.observeQuery("MarkRecordingsSkippedByRange", time.Now())
	if cameraID == "" || to.Before(from) {
		return 0, nil
	}
	res, err := d.db.ExecContext(ctx,
		`UPDATE recordings SET ai_status='skipped', ai_error=?, ai_processed_at=? WHERE camera_id=? AND started_at >= ? AND started_at <= ? AND COALESCE(ai_status,'') IN ('','pending','processing');`,
		aiErr, time.Now().UTC().Format("2006-01-02 15:04:05.999999999"), cameraID, timeToDB(from), timeToDB(to))
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// UpdateRecordingMotionScore persists the compressed-domain activity score,
// absolute-size confidence (issue #634) and activity flags for a recording
// (issue #435). Written by the offline motion analyzer after a segment
// completes. Confidence < 0 means "unknown" (pre-v34 rows).
func (d *DB) UpdateRecordingMotionScore(ctx context.Context, id string, score, confidence float64, flags string) error {
	_, err := d.db.ExecContext(ctx,
		`UPDATE recordings SET motion_score=?, motion_confidence=?, activity_flags=? WHERE id=?`,
		score, confidence, flags, id)
	return err
}

// BatchGetRecordingAIStatus returns a map of recording ID to AI status for a batch of IDs.
// Eliminates N+1 by fetching all statuses in a single query.
func (d *DB) BatchGetRecordingAIStatus(ctx context.Context, ids []string) (map[string]string, error) {
	if len(ids) == 0 {
		return map[string]string{}, nil
	}
	placeholders := make([]string, len(ids))
	args := make([]interface{}, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	q := "SELECT id, COALESCE(ai_status, '') FROM recordings WHERE id IN (" + strings.Join(placeholders, ",") + ")"
	rows, err := d.readConn().QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("batch get recording ai status: %w", err)
	}
	defer rows.Close()
	result := make(map[string]string, len(ids))
	for rows.Next() {
		var id, status string
		if err := rows.Scan(&id, &status); err != nil {
			return nil, fmt.Errorf("batch get recording ai status: %w", err)
		}
		result[id] = status
	}
	return result, rows.Err()
}

// ListDarkRecordings returns recordings with merge_status='dark' older than
// the given grace period (from ended_at). These are night/no-IR segments
// excluded from merge and pending cleanup.
func (d *DB) ListDarkRecordings(ctx context.Context, gracePeriod time.Duration) ([]model.Recording, error) {
	// UTC is mandatory here: ended_at is stored UTC (timeToDB), so a local-time
	// cutoff would sit hours in the future on non-UTC hosts and delete fresh
	// dark segments immediately, voiding the grace period (found by #565 tests).
	cutoff := time.Now().UTC().Add(-gracePeriod).Format("2006-01-02 15:04:05.999999999")
	q := selectRecordingColumns + ` WHERE merge_status = 'dark' AND ended_at IS NOT NULL AND ended_at < ? ORDER BY ended_at ASC LIMIT 500;`
	rows, err := d.readConn().QueryContext(ctx, q, cutoff)
	if err != nil {
		return nil, fmt.Errorf("list dark recordings: %w", err)
	}
	defer rows.Close()
	var res []model.Recording
	for rows.Next() {
		r, err := scanRecordingRow(rows)
		if err != nil {
			return nil, fmt.Errorf("list dark recordings: %w", err)
		}
		res = append(res, *r)
	}
	return res, rows.Err()
}
