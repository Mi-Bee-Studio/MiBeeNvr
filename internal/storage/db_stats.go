package storage

import (
	"context"
	"database/sql"
	"sort"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
)

func (d *DB) CountRecordings(ctx context.Context) (int, error) {
	var count int
	err := d.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM recordings;`).Scan(&count)
	return count, err
}

// CountRecordingsByCamera returns the number of recordings for a specific camera.
func (d *DB) CountRecordingsByCamera(ctx context.Context, cameraID string) (int, error) {
	var count int
	err := d.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM recordings WHERE camera_id=?", cameraID).Scan(&count)
	return count, err
}

// GetRecordingTrends returns daily aggregated recording statistics.
// Days defaults to 7, clamped to [1, 30].
func (d *DB) GetRecordingTrends(ctx context.Context, days int, loc *time.Location) ([]model.DailyStats, error) {
	if days <= 0 {
		days = 7
	}
	if days > 30 {
		days = 30
	}
	if loc == nil {
		loc = time.UTC
	}
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

	rows, err := d.db.QueryContext(ctx, query, offset, formatTime(cutoff))
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
	return result, nil
}

// GetLastRecordingTime returns the most recent ended_at for a camera.
func (d *DB) GetLastRecordingTime(ctx context.Context, cameraID string) (*time.Time, error) {
	var endedAtStr sql.NullString
	err := d.db.QueryRowContext(ctx, "SELECT MAX(ended_at) FROM recordings WHERE camera_id=? AND ended_at IS NOT NULL", cameraID).Scan(&endedAtStr)
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
	rows, err := d.db.QueryContext(ctx,
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
