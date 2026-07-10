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
func scanRecording(r *model.Recording, startedAtStr, endedAtStr, mergeStatusStr sql.NullString) {
	r.StartedAt = scanTime(startedAtStr)
	r.EndedAt = scanTime(endedAtStr)
	r.MergeStatus = mergeStatusFromBool(r.Merged)
	if mergeStatusStr.Valid && mergeStatusStr.String != "" {
		r.MergeStatus = mergeStatusStr.String
	}
}

func (d *DB) InsertRecording(ctx context.Context, r *model.Recording) error {
	defer d.observeQuery("InsertRecording", time.Now())
	q := `INSERT INTO recordings(id, camera_id, file_path, format, started_at, ended_at, duration, file_size, frame_count, merged, merge_status, merge_tier) VALUES(?,?,?,?,?,?,?,?,?,?,?,?);`
	mergeStatus := mergeStatusFromBool(r.Merged)
	_, err := d.db.ExecContext(ctx, q, r.ID, r.CameraID, r.FilePath, r.Format, timeToDB(r.StartedAt), timeToDB(r.EndedAt), r.Duration, r.FileSize, r.FrameCount, r.Merged, mergeStatus, r.MergeTier)
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
	q := `UPDATE recordings SET camera_id=?, file_path=?, format=?, started_at=?, ended_at=?, duration=?, file_size=?, frame_count=?, merged=?, merge_status=?, merge_tier=? WHERE id=?;`
	_, err := d.db.ExecContext(ctx, q, r.CameraID, r.FilePath, r.Format, timeToDB(r.StartedAt), timeToDB(r.EndedAt), r.Duration, r.FileSize, r.FrameCount, r.Merged, r.MergeStatus, r.MergeTier, r.ID)
	return err
}

func (d *DB) GetRecording(ctx context.Context, id string) (*model.Recording, error) {
	row := d.readConn().QueryRowContext(ctx, `SELECT id, camera_id, file_path, format, started_at, ended_at, duration, file_size, frame_count, merged, merge_status, merge_path, merge_tier, merge_progress, merge_error, archived FROM recordings WHERE id=?;`, id)
	var r model.Recording
	var startedAtStr, endedAtStr, mergePathStr, mergeTierStr, mergeErrorStr sql.NullString
	var mergeProgress sql.NullInt64
	if err := row.Scan(&r.ID, &r.CameraID, &r.FilePath, &r.Format, &startedAtStr, &endedAtStr, &r.Duration, &r.FileSize, &r.FrameCount, &r.Merged, &r.MergeStatus, &mergePathStr, &mergeTierStr, &mergeProgress, &mergeErrorStr, &r.Archived); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
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
	if r.MergeStatus == "" {
		r.MergeStatus = mergeStatusFromBool(r.Merged)
	}
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
	q := "SELECT id, camera_id, file_path, format, started_at, ended_at, duration, file_size, frame_count, merged, merge_status, merge_path, merge_tier, merge_progress, merge_error, archived FROM recordings WHERE id IN (" + strings.Join(placeholders, ",") + ")"
	rows, err := d.readConn().QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var res []model.Recording
	for rows.Next() {
		var r model.Recording
		var startedAtStr, endedAtStr, mergePathStr, mergeTierStr, mergeErrorStr sql.NullString
		var mergeProgress sql.NullInt64
		if err := rows.Scan(&r.ID, &r.CameraID, &r.FilePath, &r.Format, &startedAtStr, &endedAtStr, &r.Duration, &r.FileSize, &r.FrameCount, &r.Merged, &r.MergeStatus, &mergePathStr, &mergeTierStr, &mergeProgress, &mergeErrorStr, &r.Archived); err != nil {
			return nil, err
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
		if r.MergeStatus == "" {
			r.MergeStatus = mergeStatusFromBool(r.Merged)
		}
		res = append(res, r)
	}
	return res, nil
}

func (d *DB) ListRecordings(ctx context.Context, filter model.RecordingFilter) ([]model.Recording, error) {
	defer d.observeQuery("ListRecordings", time.Now())
	where, args := recordingsFilterWhere(filter)
	sqlstr := "SELECT id, camera_id, file_path, format, started_at, ended_at, duration, file_size, frame_count, merged, merge_status, merge_path, merge_tier, merge_progress, merge_error, archived FROM recordings"
	if len(where) > 0 {
		sqlstr += " WHERE " + strings.Join(where, " AND ")
	}
	sqlstr += recordingsOrderByClause(filter)
	if filter.Limit > 0 {
		sqlstr += fmt.Sprintf(" LIMIT %d", filter.Limit)
	}
	if filter.Offset > 0 {
		sqlstr += fmt.Sprintf(" OFFSET %d", filter.Offset)
	}
	sqlstr += ";"
	rows, err := d.readConn().QueryContext(ctx, sqlstr, args...)
	if err != nil {
		return nil, err
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
		var startedAtStr, endedAtStr, mergeStatusStr, mergePathStr, mergeTierStr, mergeErrorStr sql.NullString
		var mergeProgress sql.NullInt64
		if err := rows.Scan(&r.ID, &r.CameraID, &r.FilePath, &r.Format, &startedAtStr, &endedAtStr, &r.Duration, &r.FileSize, &r.FrameCount, &r.Merged, &mergeStatusStr, &mergePathStr, &mergeTierStr, &mergeProgress, &mergeErrorStr, &r.Archived); err != nil {
			return nil, err
		}
		r.MergeStatus = mergeStatusFromBool(r.Merged)
		if mergeStatusStr.Valid && mergeStatusStr.String != "" {
			r.MergeStatus = mergeStatusStr.String
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

// DailyRecordingSummary returns per-day recording counts and format categories for the
// given filter, grouped by local date. tzOffsetMinutes is the client's signed UTC offset
// in minutes (e.g. 480 for UTC+8, -300 for UTC-5); 0 groups by UTC date. The result is
// bounded by the number of days in the date range (max 31 for a month), so no LIMIT is needed.
func (d *DB) DailyRecordingSummary(ctx context.Context, filter model.RecordingFilter, tzOffsetMinutes int) ([]model.RecordingDaySummary, error) {
	// The tz modifier is the first bound parameter (positionally before any WHERE args).
	modifier := fmt.Sprintf("%d minutes", tzOffsetMinutes)
	args := []any{modifier}
	where := []string{}
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
	if len(filter.Formats) > 0 {
		placeholders := make([]string, len(filter.Formats))
		for i, f := range filter.Formats {
			placeholders[i] = "?"
			args = append(args, string(f))
		}
		where = append(where, "format IN ("+strings.Join(placeholders, ",")+")")
	} else if filter.Format != "" {
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
		return nil, err
	}
	defer rows.Close()

	var res []model.RecordingDaySummary
	for rows.Next() {
		var date string
		var count int
		var hasVideo, hasTimelapse, hasMjpeg int
		if err := rows.Scan(&date, &count, &hasVideo, &hasTimelapse, &hasMjpeg); err != nil {
			return nil, err
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
	rows, err := d.readConn().QueryContext(ctx, q, args...)
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

	q := `INSERT OR IGNORE INTO recordings(id, camera_id, file_path, format, started_at, ended_at, duration, file_size, frame_count, merged, merge_status) VALUES(?,?,?,?,?,?,?,?,?,?,?);`
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
			result, err := tx.ExecContext(ctx, q, r.ID, r.CameraID, r.FilePath, r.Format, timeToDB(r.StartedAt), timeToDB(r.EndedAt), r.Duration, r.FileSize, r.FrameCount, r.Merged, mergeStatusFromBool(r.Merged), r.MergeTier)
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
		return nil, err
	}
	n, _ := res.RowsAffected()
	if n > 0 {
		return ids, nil
	}
	return nil, nil
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
	rows, err := d.readConn().QueryContext(ctx, sqlstr, retentionDays)
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
	rows, err := d.readConn().QueryContext(ctx, sqlstr, cameraID, retentionDays)
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
	rows, err := d.readConn().QueryContext(ctx, sqlstr, cameraID, retentionDays)
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
	rows, err := d.readConn().QueryContext(ctx, sqlstr, limit)
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
	rows, err := d.readConn().QueryContext(ctx, `SELECT file_path FROM recordings WHERE camera_id=?`, cameraID)
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

// ListPendingMJPEGRecordings returns recordings for a camera where format IN ('mjpeg','jpeg')
// AND merge_status='pending' AND ended_at IS NOT NULL.
func (d *DB) ListPendingMJPEGRecordings(ctx context.Context, cameraID string) ([]model.Recording, error) {
	sqlstr := `SELECT id, camera_id, file_path, format, started_at, ended_at, duration, file_size, frame_count, merged, merge_status, archived FROM recordings WHERE camera_id = ? AND format IN ('mjpeg','jpeg') AND merge_status = 'pending' AND ended_at IS NOT NULL;`
	rows, err := d.readConn().QueryContext(ctx, sqlstr, cameraID)
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

// ListRecordingsWithoutTranscode returns recordings for a camera that have ended
// but have no corresponding transcoding task, and are not archived.
func (d *DB) ListRecordingsWithoutTranscode(ctx context.Context, cameraID string) ([]model.Recording, error) {
	sqlstr := `SELECT id, camera_id, file_path, format, started_at, ended_at, duration, file_size, frame_count, merged, merge_status, archived FROM recordings WHERE camera_id = ? AND ended_at IS NOT NULL AND archived = 0 AND NOT EXISTS (SELECT 1 FROM transcoding_tasks WHERE recording_id = recordings.id) ORDER BY started_at DESC;`
	rows, err := d.readConn().QueryContext(ctx, sqlstr, cameraID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	res := make([]model.Recording, 0)
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

// RepairZeroDurationRecordings returns recordings where duration=0 but the file is
// non-trivial in size, non-MJPEG, has ended_at set, and merge_status=pending.
// These are candidates for duration repair via ffprobe.
func (d *DB) RepairZeroDurationRecordings(ctx context.Context) ([]model.Recording, error) {
	sqlstr := `SELECT id, camera_id, file_path, format, started_at, ended_at, duration, file_size, frame_count, merged, merge_status, archived FROM recordings WHERE duration = 0 AND file_size > 1048576 AND format != 'mjpeg' AND ended_at IS NOT NULL AND merge_status = 'pending';`
	rows, err := d.readConn().QueryContext(ctx, sqlstr)
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

// UpdateRecordingDuration updates the duration and ended_at for a recording.
func (d *DB) UpdateRecordingDuration(ctx context.Context, id string, duration float64, endedAt time.Time) error {
	_, err := d.db.ExecContext(ctx, `UPDATE recordings SET duration=?, ended_at=? WHERE id=?;`, duration, timeToDB(endedAt), id)
	return err
}

// UpdateRecordingAIStatus sets the AI processing status for a recording.
// status: "pending", "processing", "done", "failed", "skipped"
// errMsg: optional error description (for "failed" status)
func (d *DB) UpdateRecordingAIStatus(ctx context.Context, id, status, errMsg string) error {
	now := time.Now().UTC().Format("2006-01-02 15:04:05.999999999")
	_, err := d.db.ExecContext(ctx,
		`UPDATE recordings SET ai_status=?, ai_error=?, ai_processed_at=CASE WHEN ? IN ('done','failed','skipped') THEN ? ELSE ai_processed_at END WHERE id=?;`,
		status, errMsg, status, now, id)
	return err
}

// GetRecordingAIStatus returns the AI processing status of a recording.
func (d *DB) GetRecordingAIStatus(ctx context.Context, id string) (status string, err error) {
	err = d.readConn().QueryRowContext(ctx, `SELECT COALESCE(ai_status, '') FROM recordings WHERE id=?`, id).Scan(&status)
	return
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
		return nil, err
	}
	defer rows.Close()
	result := make(map[string]string, len(ids))
	for rows.Next() {
		var id, status string
		if err := rows.Scan(&id, &status); err != nil {
			return nil, err
		}
		result[id] = status
	}
	return result, rows.Err()
}
