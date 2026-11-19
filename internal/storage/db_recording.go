package storage

import (
	"context"
	"database/sql"
	"fmt"
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
	defer d.observeTxn(ctx, "recording_insert", time.Now())
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
	defer d.observeTxn(ctx, "recording_insert", time.Now())
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
			// Ctx-aware sleep: retrying a cancelled context is a guaranteed
			// failure, so bail out instead of burning the backoff slots.
			select {
			case <-ctx.Done():
				return fmt.Errorf("insert recording: context cancelled during busy backoff: %w", ctx.Err())
			case <-time.After(backoff):
			}
		}
		err := d.InsertRecording(ctx, r)
		if err == nil {
			return nil
		}
		if !IsBusyError(err) {
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
	defer d.observeTxn(ctx, "api_write", time.Now())
	q := `UPDATE recordings SET camera_id=?, file_path=?, format=?, started_at=?, ended_at=?, duration=?, file_size=?, frame_count=?, merge_status=?, merge_tier=? WHERE id=?;`
	_, err := d.db.ExecContext(ctx, q, r.CameraID, r.FilePath, r.Format, timeToDB(r.StartedAt), timeToDB(r.EndedAt), r.Duration, r.FileSize, r.FrameCount, r.MergeStatus, r.MergeTier, r.ID)
	return err
}

// FindMergedCoveringIDs returns the IDs of merged rows whose
// [started_at, ended_at] span contains at (#903 detail fallback). Recording
// IDs are UnixNano creation stamps and a row's creation moment always falls
// inside its own span, so a stale source-segment ID (deleted after rolling
// merge consumed it) resolves to the merged product that swallowed it.
// cameraID may be empty to search all cameras.
func (d *DB) FindMergedCoveringIDs(ctx context.Context, at time.Time, cameraID string) ([]string, error) {
	defer d.observeQuery("FindMergedCoveringIDs", time.Now())
	q := "SELECT id FROM recordings WHERE merge_status='merged' AND started_at<=? AND ended_at>=?"
	args := []any{formatTime(at), formatTime(at)}
	if cameraID != "" {
		q += " AND camera_id=?"
		args = append(args, cameraID)
	}
	q += " ORDER BY started_at LIMIT 4"
	rows, err := d.readConn().QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("find merged covering: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("find merged covering: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
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
			// The API hands cursors out as RFC3339Nano but started_at is stored in
			// TimeLayout (space-separated). Bound raw, SQLite compares TEXT
			// lexicographically and ' ' (0x20) sorts below 'T' (0x54) at offset 10 —
			// every stored row compared "less than" every cursor, so each page
			// silently re-served page 1 (#704). Re-format through the storage format
			// like the start/end filters do; an unparseable cursor falls back to
			// OFFSET paging instead of matching nothing.
			if t, err := time.Parse(time.RFC3339, filter.Cursor); err == nil {
				where = append(where, "started_at < ?")
				args = append(args, formatTime(t))
				useKeyset = true
			}
		}
	}

	sqlstr := "SELECT id, camera_id, file_path, format, started_at, ended_at, duration, file_size, frame_count, merge_status, merge_path, merge_tier, merge_progress, merge_error, merge_quality, archived, motion_score, motion_confidence, activity_flags, COALESCE(layer,0) FROM recordings"
	if len(where) > 0 {
		sqlstr += " WHERE " + strings.Join(where, " AND ")
	}
	sqlstr += recordingsOrderByClause(filter)
	// Bind LIMIT/OFFSET as parameters (#875 L1): interpolated values make a
	// distinct SQL text per page, defeating sqlite's prepared-statement cache
	// so every page pays a fresh prepare. Placeholders keep one cached plan.
	if filter.Limit > 0 {
		sqlstr += " LIMIT ?"
		args = append(args, filter.Limit)
	}
	// OFFSET is only used for legacy (non-cursor) pagination.
	if !useKeyset && filter.Offset > 0 {
		sqlstr += " OFFSET ?"
		args = append(args, filter.Offset)
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
	sqlstr += " LIMIT ?"
	args = append(args, maxTimelineSegments)
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
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("daily recording summary: %w", err)
	}
	return res, nil
}
