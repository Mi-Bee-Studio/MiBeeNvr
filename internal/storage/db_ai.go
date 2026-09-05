package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// AIEvent represents a single AI detection event written by MiBeeVision.
type AIEvent struct {
	ID             int64   `json:"id"`
	CameraID       string  `json:"camera_id"`
	RecordingID    string  `json:"recording_id,omitempty"`
	EventType      string  `json:"event_type"`
	Severity       string  `json:"severity"`
	ZoneName       string  `json:"zone_name,omitempty"`
	ClassName      string  `json:"class_name,omitempty"`
	Confidence     float64 `json:"confidence"`
	FrameIdx       int     `json:"frame_idx,omitempty"`
	FrameTimestamp string  `json:"frame_timestamp,omitempty"`
	BBox           string  `json:"bbox,omitempty"` // JSON array [x1,y1,x2,y2] normalized
	SnapshotPath   string  `json:"snapshot_path,omitempty"`
	Metadata       string  `json:"metadata,omitempty"` // JSON
	Source         string  `json:"source,omitempty"`   // 写入方 API Key 名(多 Vision 实例归因;空=旧数据/匿名)
	CreatedAt      string  `json:"created_at"`
}

// InsertAIEvent stores a new AI event from MiBeeVision.
func (d *DB) InsertAIEvent(ctx context.Context, e *AIEvent) (int64, error) {
	q := `INSERT INTO ai_events (camera_id, recording_id, event_type, severity, zone_name, class_name, confidence, frame_idx, frame_timestamp, bbox, snapshot_path, metadata, source)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);`
	result, err := d.db.ExecContext(
		ctx, q,
		e.CameraID, e.RecordingID, e.EventType, e.Severity,
		e.ZoneName, e.ClassName, e.Confidence, e.FrameIdx,
		e.FrameTimestamp, e.BBox, e.SnapshotPath, e.Metadata, e.Source,
	)
	if err != nil {
		return 0, fmt.Errorf("insert ai event: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return 0, err
	}
	e.ID = id
	return id, nil
}

// AIEventFilter holds query parameters for listing AI events.
type AIEventFilter struct {
	CameraID    string
	RecordingID string // filter by recording_id (used by merge migration tests + NVR UI)
	EventType   string
	Source      string     // filter by writer instance (API key name, v35)
	StartTime   *time.Time // inclusive lower bound on created_at
	EndTime     *time.Time // inclusive upper bound on created_at
	AscOrder    bool       // order by created_at ASC (for timeline overlay)
	Limit       int
	Offset      int
}

// ListAIEvents returns AI events matching the filter, ordered by created_at DESC
// (or ASC if f.AscOrder is true).
//
// NOTE: unlike ListRecordings/ListHealthEvents (where Limit=0 means "no limit"),
// a zero/negative Limit here defaults to 50. AI events can number in the tens of
// thousands per day for high-frequency detectors, so an unguarded "no limit" would
// risk pulling huge result sets on the default list page. Callers that need more
// (e.g. TimelineBar overlay, which fetches a full day) MUST pass an explicit Limit.
func (d *DB) ListAIEvents(ctx context.Context, f AIEventFilter) ([]AIEvent, int, error) {
	if f.Limit <= 0 {
		f.Limit = 50
	}

	var where []string
	var args []interface{}
	if f.CameraID != "" {
		where = append(where, "camera_id = ?")
		args = append(args, f.CameraID)
	}
	if f.RecordingID != "" {
		where = append(where, "recording_id = ?")
		args = append(args, f.RecordingID)
	}
	if f.EventType != "" {
		where = append(where, "event_type = ?")
		args = append(args, f.EventType)
	}
	if f.Source != "" {
		where = append(where, "source = ?")
		args = append(args, f.Source)
	}
	if f.StartTime != nil {
		where = append(where, "created_at >= ?")
		args = append(args, f.StartTime.Format("2006-01-02 15:04:05.999999999"))
	}
	if f.EndTime != nil {
		where = append(where, "created_at <= ?")
		args = append(args, f.EndTime.Format("2006-01-02 15:04:05.999999999"))
	}

	whereClause := ""
	if len(where) > 0 {
		whereClause = " WHERE " + strings.Join(where, " AND ")
	}

	// Count
	var total int
	countQ := `SELECT COUNT(*) FROM ai_events` + whereClause
	if err := d.readConn().QueryRowContext(ctx, countQ, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	orderClause := " ORDER BY created_at DESC, id DESC"
	if f.AscOrder {
		orderClause = " ORDER BY created_at ASC, id ASC"
	}

	// Data
	dataQ := `SELECT id, camera_id, recording_id, event_type, severity, zone_name, class_name, confidence, frame_idx, frame_timestamp, bbox, snapshot_path, metadata, source, created_at
		FROM ai_events` + whereClause + orderClause + ` LIMIT ? OFFSET ?`
	rows, err := d.readConn().QueryContext(ctx, dataQ, append(args, f.Limit, f.Offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var events []AIEvent
	for rows.Next() {
		var e AIEvent
		var recordingID, zoneName, className, frameTS, bbox, snapshotPath, metadata, source sql.NullString
		if err := rows.Scan(&e.ID, &e.CameraID, &recordingID, &e.EventType, &e.Severity,
			&zoneName, &className, &e.Confidence, &e.FrameIdx, &frameTS,
			&bbox, &snapshotPath, &metadata, &source, &e.CreatedAt); err != nil {
			return nil, 0, err
		}
		e.RecordingID = recordingID.String
		e.ZoneName = zoneName.String
		e.ClassName = className.String
		e.FrameTimestamp = frameTS.String
		e.BBox = bbox.String
		e.SnapshotPath = snapshotPath.String
		e.Metadata = metadata.String
		e.Source = source.String
		e.CreatedAt = aiCreatedAtToRFC3339(e.CreatedAt)
		events = append(events, e)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	return events, total, nil
}

// aiCreatedAtToRFC3339 normalizes the zoneless UTC timestamps SQLite's
// datetime('now') writes ("2006-01-02 15:04:05") to RFC3339. A bare
// "YYYY-MM-DD HH:MM:SS" is ambiguous — JS Date.parse and most ISO parsers
// treat it as LOCAL time, which shifted event display and event→recording
// deep links by the server's UTC offset (8h on a CST deployment).
func aiCreatedAtToRFC3339(s string) string {
	if s == "" {
		return s
	}
	for _, layout := range []string{"2006-01-02 15:04:05.999999999", "2006-01-02 15:04:05", time.RFC3339Nano, time.RFC3339} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC().Format(time.RFC3339Nano)
		}
	}
	return s
}

// GetAIEvent returns a single AI event by ID.
func (d *DB) GetAIEvent(ctx context.Context, id int64) (*AIEvent, error) {
	q := `SELECT id, camera_id, recording_id, event_type, severity, zone_name, class_name, confidence, frame_idx, frame_timestamp, bbox, snapshot_path, metadata, source, created_at
		FROM ai_events WHERE id = ?`
	row := d.readConn().QueryRowContext(ctx, q, id)
	var e AIEvent
	var recordingID, zoneName, className, frameTS, bbox, snapshotPath, metadata, source sql.NullString
	err := row.Scan(&e.ID, &e.CameraID, &recordingID, &e.EventType, &e.Severity,
		&zoneName, &className, &e.Confidence, &e.FrameIdx, &frameTS,
		&bbox, &snapshotPath, &metadata, &source, &e.CreatedAt)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	e.RecordingID = recordingID.String
	e.ZoneName = zoneName.String
	e.ClassName = className.String
	e.FrameTimestamp = frameTS.String
	e.BBox = bbox.String
	e.SnapshotPath = snapshotPath.String
	e.Metadata = metadata.String
	e.Source = source.String
	e.CreatedAt = aiCreatedAtToRFC3339(e.CreatedAt)
	return &e, nil
}

// DeleteAIEventsByRecording deletes all AI events associated with a recording
// (used when a recording is deleted and events are orphaned).
func (d *DB) DeleteAIEventsByRecording(ctx context.Context, recordingID string) (int64, error) {
	result, err := d.db.ExecContext(ctx, `DELETE FROM ai_events WHERE recording_id = ?`, recordingID)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// AIEventStats holds aggregated statistics for AI events.
type AIEventStats struct {
	EventType string `json:"event_type"`
	Count     int    `json:"count"`
}

// GetAIEventStats returns event type counts within a time period. When cameraID
// is non-empty, stats are scoped to that camera; when empty, stats aggregate
// across ALL cameras (global view, #213). The query groups by event_type only,
// so the result shape is identical in both modes.
func (d *DB) GetAIEventStats(ctx context.Context, cameraID string, since time.Time) ([]AIEventStats, error) {
	var where []string
	var args []interface{}
	if cameraID != "" {
		where = append(where, "camera_id = ?")
		args = append(args, cameraID)
	}
	where = append(where, "created_at >= ?")
	args = append(args, since.Format("2006-01-02 15:04:05"))

	q := `SELECT event_type, COUNT(*) as cnt FROM ai_events WHERE ` +
		strings.Join(where, " AND ") +
		` GROUP BY event_type ORDER BY cnt DESC`
	rows, err := d.readConn().QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var stats []AIEventStats
	for rows.Next() {
		var s AIEventStats
		if err := rows.Scan(&s.EventType, &s.Count); err != nil {
			return nil, err
		}
		stats = append(stats, s)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return stats, nil
}

// MarshalBBox converts a [4]float64 to JSON string for storage.
func MarshalBBox(bbox [4]float64) string {
	b, err := json.Marshal(bbox)
	if err != nil {
		return ""
	}
	return string(b)
}
