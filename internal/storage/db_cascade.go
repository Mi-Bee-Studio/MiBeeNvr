package storage

import (
	"context"
	"database/sql"
	"time"
)

// CascadeChannel is the persisted camera → GB channel-ID allocation of the
// cascade client: stable across restarts so the upper platform's bindings
// never break (#364).
type CascadeChannel struct {
	CameraID    string
	GBChannelID string
	Name        string
	UpdatedAt   time.Time
}

func (d *DB) UpsertCascadeChannel(ctx context.Context, ch CascadeChannel) error {
	_, err := d.db.ExecContext(ctx, `INSERT INTO cascade_channels
		(camera_id, gb_channel_id, name, updated_at) VALUES (?, ?, ?, ?)
		ON CONFLICT(camera_id) DO UPDATE SET
			gb_channel_id = excluded.gb_channel_id,
			name = excluded.name,
			updated_at = excluded.updated_at`,
		ch.CameraID, ch.GBChannelID, ch.Name, timeToDB(ch.UpdatedAt))
	return err
}

func (d *DB) ListCascadeChannels(ctx context.Context) ([]CascadeChannel, error) {
	rows, err := d.db.QueryContext(ctx,
		`SELECT camera_id, gb_channel_id, name, updated_at FROM cascade_channels`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []CascadeChannel
	for rows.Next() {
		var ch CascadeChannel
		var updated sql.NullString
		if err := rows.Scan(&ch.CameraID, &ch.GBChannelID, &ch.Name, &updated); err != nil {
			return nil, err
		}
		ch.UpdatedAt = scanTime(updated)
		out = append(out, ch)
	}
	return out, rows.Err()
}
