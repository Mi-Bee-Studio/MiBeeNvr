package storage

import (
	"context"
	"database/sql"
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
func (d *DB) GetRecordingTrends(ctx context.Context, days int) ([]model.DailyStats, error) {
	if days <= 0 {
		days = 7
	}
	if days > 30 {
		days = 30
	}
	cutoff := time.Now().AddDate(0, 0, -days).UTC()
	
	query := `SELECT DATE(r.started_at) as date, COUNT(*) as recordings, SUM(r.file_size) as total_size, r.camera_id, COALESCE(c.name, r.camera_id) as camera_name
		FROM recordings r LEFT JOIN cameras c ON r.camera_id = c.id
		WHERE r.started_at >= ?
		GROUP BY DATE(r.started_at), r.camera_id
		ORDER BY date`
	
	rows, err := d.db.QueryContext(ctx, query, formatTime(cutoff))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	
	// Aggregate per-camera rows into per-date stats
	dateIndex := make(map[string]int) // date -> index into result slice
	var result []model.DailyStats
	
	for rows.Next() {
		var date string
		var count int
		var totalSize int64
		var cameraID, cameraName string
		if err := rows.Scan(&date, &count, &totalSize, &cameraID, &cameraName); err != nil {
			return nil, err
		}
		idx, ok := dateIndex[date]
		if !ok {
			idx = len(result)
			dateIndex[date] = idx
			result = append(result, model.DailyStats{
				Date:         date,
				CameraCounts: make(map[string]int),
			})
		}
		result[idx].Recordings += count
		result[idx].TotalSize += totalSize
		result[idx].CameraCounts[cameraName] += count
	}
	if err := rows.Err(); err != nil {
		return nil, err
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
