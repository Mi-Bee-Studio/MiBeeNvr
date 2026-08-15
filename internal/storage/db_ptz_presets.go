package storage

import (
	"context"
	"database/sql"
	"strconv"
	"time"
)

// PTZPreset is a locally-managed preset row (camera_ptz_presets). Used by
// protocols with no device-side preset query — notably GB28181, where the
// platform picks the preset number and devices only understand set/call/del
// commands (GB/T 28181-2016 § A.3.4).
type PTZPreset struct {
	CameraID  string
	Token     string // preset number, "1".."255"
	Name      string
	CreatedAt time.Time
}

// InsertPTZPreset adds a preset for a camera (INSERT OR IGNORE — an existing
// token keeps its name).
func (d *DB) InsertPTZPreset(ctx context.Context, p PTZPreset) error {
	_, err := d.db.ExecContext(ctx, `INSERT OR IGNORE INTO camera_ptz_presets
		(camera_id, token, name, created_at) VALUES (?, ?, ?, ?);`,
		p.CameraID, p.Token, p.Name, timeToDB(p.CreatedAt))
	return err
}

// UpsertPTZPreset adds or renames a preset for a camera.
func (d *DB) UpsertPTZPreset(ctx context.Context, p PTZPreset) error {
	_, err := d.db.ExecContext(ctx, `INSERT INTO camera_ptz_presets
		(camera_id, token, name, created_at) VALUES (?, ?, ?, ?)
		ON CONFLICT(camera_id, token) DO UPDATE SET name=excluded.name;`,
		p.CameraID, p.Token, p.Name, timeToDB(p.CreatedAt))
	return err
}

// DeletePTZPreset removes a preset. Idempotent (0 rows affected is fine).
func (d *DB) DeletePTZPreset(ctx context.Context, cameraID, token string) error {
	_, err := d.db.ExecContext(ctx, `DELETE FROM camera_ptz_presets WHERE camera_id=? AND token=?;`,
		cameraID, token)
	return err
}

// ListPTZPresets returns a camera's presets ordered by numeric token.
func (d *DB) ListPTZPresets(ctx context.Context, cameraID string) ([]PTZPreset, error) {
	rows, err := d.readConn().QueryContext(ctx, `SELECT camera_id, token, name, created_at
		FROM camera_ptz_presets WHERE camera_id=? ORDER BY CAST(token AS INTEGER);`, cameraID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var res []PTZPreset
	for rows.Next() {
		var p PTZPreset
		var createdAt string
		if err := rows.Scan(&p.CameraID, &p.Token, &p.Name, &createdAt); err != nil {
			return nil, err
		}
		p.CreatedAt = scanTime(sql.NullString{String: createdAt, Valid: createdAt != ""})
		res = append(res, p)
	}
	return res, rows.Err()
}

// NextPTZPresetToken returns the lowest unused preset number (1-255) for a
// camera, or false when all 255 slots are taken.
func (d *DB) NextPTZPresetToken(ctx context.Context, cameraID string) (string, bool) {
	rows, err := d.readConn().QueryContext(ctx, `SELECT token FROM camera_ptz_presets WHERE camera_id=?;`, cameraID)
	if err != nil {
		return "1", true // fall back to 1 on error — INSERT OR IGNORE dedups
	}
	defer rows.Close()
	used := make(map[string]bool)
	for rows.Next() {
		var token string
		if err := rows.Scan(&token); err == nil {
			used[token] = true
		}
	}
	for n := 1; n <= 255; n++ {
		t := strconv.Itoa(n)
		if !used[t] {
			return t, true
		}
	}
	return "", false
}
