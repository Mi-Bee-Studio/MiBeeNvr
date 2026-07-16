package storage

import (
	"context"
	"database/sql"
	"sort"
	"strconv"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
)

// totalRecordingsCacheTTL bounds how long COUNT(*) FROM recordings is reused.
// The Dashboard polls every 30s; the count drifts by at most one segment interval
// (~30s of new recordings) during this TTL, which is imperceptible.
const totalRecordingsCacheTTL = 15 * time.Second

func (d *DB) CountRecordings(ctx context.Context) (int, error) {
	// Fast path: return cached count if fresh. COUNT(*) FROM recordings is a full
	// table scan in SQLite (no index helps) — on a busy NVR with tens of thousands
	// of recordings this is the single most expensive stat query, and the Dashboard
	// polls it every 30s. A 15s TTL means at most one scan per 15s instead of per
	// request, with negligible staleness.
	d.totalRecordingsMu.RLock()
	if time.Since(d.totalRecordingsCachedAt) < totalRecordingsCacheTTL {
		count := d.totalRecordingsCached
		d.totalRecordingsMu.RUnlock()
		return count, nil
	}
	d.totalRecordingsMu.RUnlock()

	var count int
	err := d.readConn().QueryRowContext(ctx, `SELECT COUNT(*) FROM recordings;`).Scan(&count)
	if err != nil {
		return 0, err
	}

	d.totalRecordingsMu.Lock()
	d.totalRecordingsCached = count
	d.totalRecordingsCachedAt = time.Now()
	d.totalRecordingsMu.Unlock()
	return count, nil
}

// CountRecordingsByCamera returns the number of recordings for a specific camera.
func (d *DB) CountRecordingsByCamera(ctx context.Context, cameraID string) (int, error) {
	var count int
	err := d.readConn().QueryRowContext(ctx, "SELECT COUNT(*) FROM recordings WHERE camera_id=?", cameraID).Scan(&count)
	return count, err
}

// GetRecordingTrends returns daily aggregated recording statistics.
// Days defaults to 7, clamped to [1, 30].
// heavyQueryTimeout bounds the wall-clock duration of analytic queries that scan
// large portions of the recordings table (GetRecordingTrends, CountRecordingsWithFilter).
// It only applies when the caller's context has no deadline, so explicit caller
// timeouts always take precedence. Protects the (small) read pool from a single slow
// query monopolizing a connection.
const heavyQueryTimeout = 10 * time.Second

// withHeavyQueryTimeout returns a context bounded to heavyQueryTimeout unless ctx
// already has a sooner deadline. The cancel func is always non-nil and safe to defer.
func withHeavyQueryTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Deadline(); ok {
		// Caller already set a deadline; respect it verbatim.
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, heavyQueryTimeout)
}

// InvalidateStatsCache clears the cached CountRecordings and GetRecordingTrends
// results. Call after bulk data changes (tests, migrations) that bypass the
// normal insert path. Normal recording inserts/deletes rely on the short TTL
// for natural expiry.
func (d *DB) InvalidateStatsCache() {
	d.totalRecordingsMu.Lock()
	d.totalRecordingsCachedAt = time.Time{}
	d.totalRecordingsMu.Unlock()

	d.trendsMu.Lock()
	d.trendsCache = nil
	d.trendsMu.Unlock()
}

// trendsCacheTTL bounds how long GetRecordingTrends results are reused. Daily
// aggregates change at most once per day (per-recording), so a 2-minute cache is
// effectively always fresh while eliminating the GROUP BY scan on every 30s poll.
const trendsCacheTTL = 2 * time.Minute

func (d *DB) GetRecordingTrends(ctx context.Context, days int, loc *time.Location) ([]model.DailyStats, error) {
	if days <= 0 {
		days = 7
	}
	if days > 30 {
		days = 30
	}

	// Fast path: check cache. The cache key includes the timezone offset so that
	// callers in different timezones get distinct cache entries.
	if loc == nil {
		loc = time.UTC
	}
	_, tzOffset := time.Now().In(loc).Zone()
	cacheKey := strconv.Itoa(days) + ":" + strconv.Itoa(tzOffset)
	d.trendsMu.Lock()
	if d.trendsCache == nil {
		d.trendsCache = make(map[string]*trendsCacheEntry)
	}
	if entry, ok := d.trendsCache[cacheKey]; ok && time.Now().Before(entry.expiryAt) {
		result := entry.value
		d.trendsMu.Unlock()
		return result, nil
	}
	d.trendsMu.Unlock()

	ctx, cancel := withHeavyQueryTimeout(ctx)
	defer cancel()
	defer d.observeQuery("GetRecordingTrends", time.Now())

	cutoff := time.Now().AddDate(0, 0, -days).UTC()

	// Calculate UTC offset in seconds for timezone-aware GROUP BY
	now := time.Now().In(loc)
	_, offset := now.Zone()

	// Group by timezone-correct date in SQL to avoid loading all rows into Go memory
	query := `SELECT date(datetime(r.started_at, ? || ' seconds')) as d,
		r.camera_id,
		COALESCE(c.name, r.camera_id) as camera_name,
		COUNT(*) as cnt,
		COALESCE(SUM(r.file_size), 0) as total_size
	FROM recordings r LEFT JOIN cameras c ON r.camera_id = c.id
	WHERE r.started_at >= ?
	GROUP BY d, r.camera_id
	ORDER BY d`

	rows, err := d.readConn().QueryContext(ctx, query, offset, formatTime(cutoff))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	// Aggregate SQL results into per-date stats (merging cameras for the same date)
	type dailyKey struct {
		date  string
		camID string
	}
	agg := make(map[dailyKey]*model.DailyStats)

	for rows.Next() {
		var dateStr, cameraID, cameraName string
		var cnt int
		var totalSize int64
		if err := rows.Scan(&dateStr, &cameraID, &cameraName, &cnt, &totalSize); err != nil {
			return nil, err
		}

		key := dailyKey{date: dateStr, camID: cameraID}
		entry, ok := agg[key]
		if !ok {
			entry = &model.DailyStats{
				Date:         dateStr,
				CameraCounts: make(map[string]int),
			}
			agg[key] = entry
		}
		entry.Recordings += cnt
		entry.TotalSize += totalSize
		entry.CameraCounts[cameraName] += cnt
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Sort by date ascending and merge per-camera entries for the same date
	dates := make(map[string]bool)
	for _, entry := range agg {
		dates[entry.Date] = true
	}
	sorted := make([]string, 0, len(dates))
	for d := range dates {
		sorted = append(sorted, d)
	}
	sort.Strings(sorted)

	result := make([]model.DailyStats, 0, len(sorted))
	for _, d := range sorted {
		merged := model.DailyStats{
			Date:         d,
			CameraCounts: make(map[string]int),
		}
		for _, entry := range agg {
			if entry.Date == d {
				merged.Recordings += entry.Recordings
				merged.TotalSize += entry.TotalSize
				for camName, count := range entry.CameraCounts {
					merged.CameraCounts[camName] += count
				}
			}
		}
		result = append(result, merged)
	}

	if result == nil {
		result = []model.DailyStats{}
	}

	// Cache the result. Daily aggregates change slowly; a 2-minute TTL eliminates
	// the GROUP BY scan on the Dashboard's 30s poll cycle.
	d.trendsMu.Lock()
	d.trendsCache[cacheKey] = &trendsCacheEntry{
		value:    result,
		expiryAt: time.Now().Add(trendsCacheTTL),
	}
	d.trendsMu.Unlock()

	return result, nil
}

// GetLastRecordingTime returns the most recent ended_at for a camera.
func (d *DB) GetLastRecordingTime(ctx context.Context, cameraID string) (*time.Time, error) {
	var endedAtStr sql.NullString
	err := d.readConn().QueryRowContext(ctx, "SELECT MAX(ended_at) FROM recordings WHERE camera_id=? AND ended_at IS NOT NULL", cameraID).Scan(&endedAtStr)
	if err != nil {
		return nil, err
	}
	if !endedAtStr.Valid || endedAtStr.String == "" {
		return nil, nil
	}
	t := scanTime(endedAtStr)
	return &t, nil
}

// GetAllLastRecordingTimes returns the last recording time for each camera.
func (d *DB) GetAllLastRecordingTimes(ctx context.Context) (map[string]*time.Time, error) {
	rows, err := d.readConn().QueryContext(ctx,
		`SELECT camera_id, MAX(ended_at) as last_ended FROM recordings WHERE ended_at IS NOT NULL GROUP BY camera_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[string]*time.Time)
	for rows.Next() {
		var cameraID string
		var endedAtStr sql.NullString
		if err := rows.Scan(&cameraID, &endedAtStr); err != nil {
			return nil, err
		}
		if endedAtStr.Valid && endedAtStr.String != "" {
			t := scanTime(endedAtStr)
			result[cameraID] = &t
		}
	}
	return result, nil
}
