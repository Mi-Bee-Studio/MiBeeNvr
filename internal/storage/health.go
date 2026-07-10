package storage

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
)

// HealthEventsFilter defines query parameters for listing health events.
type HealthEventsFilter struct {
	CameraID  string
	EventType string
	Since     string // time.Time formatted as UTC string, filters created_at >= since
	Limit     int
	Offset    int
}

// InsertHealthEvent inserts a new camera health event.
func (d *DB) InsertHealthEvent(ctx context.Context, event model.HealthEvent) error {
	q := `INSERT INTO camera_health_events(camera_id, event_type, status, message, metadata, created_at) VALUES(?,?,?,?,?,?);`
	_, err := d.db.ExecContext(ctx, q, event.CameraID, event.EventType, event.Status, event.Message, event.Metadata, formatTime(event.CreatedAt))
	return err
}

// ListHealthEvents returns health events matching the filter, total count, and error.
// Results are ordered by created_at DESC.
func (d *DB) ListHealthEvents(ctx context.Context, filter HealthEventsFilter) ([]model.HealthEvent, int, error) {
	where := []string{}
	args := []any{}

	if filter.CameraID != "" {
		where = append(where, "camera_id=?")
		args = append(args, filter.CameraID)
	}
	if filter.Since != "" {
		where = append(where, "created_at>=?")
		args = append(args, filter.Since)
	}
	if filter.EventType != "" {
		where = append(where, "event_type=?")
		args = append(args, filter.EventType)
	}

	whereClause := ""
	if len(where) > 0 {
		whereClause = " WHERE " + strings.Join(where, " AND ")
	}

	// Count query
	countSQL := "SELECT COUNT(*) FROM camera_health_events" + whereClause
	var total int
	if err := d.readConn().QueryRowContext(ctx, countSQL, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	// Data query
	dataSQL := "SELECT id, camera_id, event_type, status, message, metadata, created_at FROM camera_health_events" + whereClause
	dataSQL += " ORDER BY created_at DESC"
	if filter.Limit > 0 {
		dataSQL += fmt.Sprintf(" LIMIT %d", filter.Limit)
	}
	if filter.Offset > 0 {
		dataSQL += fmt.Sprintf(" OFFSET %d", filter.Offset)
	}
	dataSQL += ";"

	rows, err := d.readConn().QueryContext(ctx, dataSQL, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var res []model.HealthEvent
	for rows.Next() {
		var e model.HealthEvent
		var createdAtStr sql.NullString
		if err := rows.Scan(&e.ID, &e.CameraID, &e.EventType, &e.Status, &e.Message, &e.Metadata, &createdAtStr); err != nil {
			return nil, 0, err
		}
		e.CreatedAt = scanTime(createdAtStr)
		res = append(res, e)
	}

	return res, total, nil
}

// GetLatestCameraHealth returns the most recent health event for a camera, or nil if none exist.
func (d *DB) GetLatestCameraHealth(ctx context.Context, cameraID string) (*model.HealthEvent, error) {
	q := `SELECT id, camera_id, event_type, status, message, metadata, created_at FROM camera_health_events WHERE camera_id=? ORDER BY created_at DESC LIMIT 1;`
	row := d.readConn().QueryRowContext(ctx, q, cameraID)

	var e model.HealthEvent
	var createdAtStr sql.NullString
	if err := row.Scan(&e.ID, &e.CameraID, &e.EventType, &e.Status, &e.Message, &e.Metadata, &createdAtStr); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	e.CreatedAt = scanTime(createdAtStr)
	return &e, nil
}

// DeleteHealthEventsBefore deletes health events older than the given time.
// Returns the number of rows deleted.
func (d *DB) DeleteHealthEventsBefore(ctx context.Context, before time.Time) (int64, error) {
	q := `DELETE FROM camera_health_events WHERE created_at < ?;`
	result, err := d.db.ExecContext(ctx, q, formatTime(before))
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// DeleteHealthEventsByType deletes health events of a given type older than the given time.
// Returns the number of rows deleted.
func (d *DB) DeleteHealthEventsByType(ctx context.Context, eventType string, before time.Time) (int64, error) {
	q := `DELETE FROM camera_health_events WHERE event_type = ? AND created_at < ?;`
	result, err := d.db.ExecContext(ctx, q, eventType, formatTime(before))
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// GetCameraHealthSummary returns the latest health status per camera.
// Returns a map of cameraID -> CameraHealth.
func (d *DB) GetCameraHealthSummary(ctx context.Context) (map[string]*model.CameraHealth, error) {
	q := `SELECT h.camera_id, h.event_type, h.status, h.message, h.created_at FROM camera_health_events h INNER JOIN (SELECT camera_id, MAX(created_at) AS max_created_at FROM camera_health_events GROUP BY camera_id) latest ON h.camera_id = latest.camera_id AND h.created_at = latest.max_created_at ORDER BY h.camera_id;`
	rows, err := d.readConn().QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[string]*model.CameraHealth)
	for rows.Next() {
		var ch model.CameraHealth
		var createdAtStr sql.NullString
		if err := rows.Scan(&ch.CameraID, &ch.LatestEvent, &ch.LatestStatus, &ch.LatestMessage, &createdAtStr); err != nil {
			return nil, err
		}
		ch.LastEventAt = scanTime(createdAtStr)
		result[ch.CameraID] = &ch
	}
	return result, nil
}
