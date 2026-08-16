package storage

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// GB28181Device is the persisted form of a registered GB28181 device
// (e.g. a Hikvision NVR). Times are stored as UTC via timeToDB/scanTime.
// The in-memory counterpart (internal/gb28181.Device) keeps liveness in
// atomics (UnixNano); conversion to these time.Time fields happens here.
type GB28181Device struct {
	ID            string
	Name          string
	Manufacturer  string
	Model         string
	Status        string // "online" | "offline"
	LastKeepalive time.Time
	RegisteredAt  time.Time
}

// GB28181Channel is the persisted form of a channel in a GB28181 device's
// catalog. CameraID links the channel to a MiBee camera once bound.
type GB28181Channel struct {
	ID           string
	DeviceID     string
	Name         string
	Manufacturer string
	Parental     int
	Status       string // "idle" | "inviting" | "playing"
	CameraID     string
	UpdatedAt    time.Time
}

// GB28181Fingerprint is the ONVIF serial probed from a GB28181 device's SIP
// source address — the cross-protocol identity link for dedup (dual-protocol
// cameras carry the same serial on every network interface, so the fingerprint
// matches regardless of which interface IP each protocol used).
type GB28181Fingerprint struct {
	DeviceID string
	Serial   string
	SourceIP string
	ProbedAt time.Time
}

// UpsertGB28181Fingerprint caches a successfully probed device serial.
func (d *DB) UpsertGB28181Fingerprint(ctx context.Context, fp GB28181Fingerprint) error {
	_, err := d.db.ExecContext(ctx, `INSERT INTO gb28181_fingerprints
		(device_id, serial, source_ip, probed_at) VALUES (?, ?, ?, ?)
		ON CONFLICT(device_id) DO UPDATE SET
			serial = excluded.serial,
			source_ip = excluded.source_ip,
			probed_at = excluded.probed_at`,
		fp.DeviceID, fp.Serial, fp.SourceIP, timeToDB(fp.ProbedAt))
	return err
}

// GetGB28181Fingerprint returns the cached serial for a device (nil when none).
func (d *DB) GetGB28181Fingerprint(ctx context.Context, deviceID string) (*GB28181Fingerprint, error) {
	row := d.db.QueryRowContext(ctx,
		`SELECT device_id, serial, source_ip, probed_at FROM gb28181_fingerprints WHERE device_id = ?`, deviceID)
	var fp GB28181Fingerprint
	var probed sql.NullString
	err := row.Scan(&fp.DeviceID, &fp.Serial, &fp.SourceIP, &probed)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	fp.ProbedAt = scanTime(probed)
	return &fp, nil
}

// ListGB28181Fingerprints returns all cached device serials (serial → device
// correlation for the camera-create dedup path).
func (d *DB) ListGB28181Fingerprints(ctx context.Context) ([]GB28181Fingerprint, error) {
	rows, err := d.db.QueryContext(ctx,
		`SELECT device_id, serial, source_ip, probed_at FROM gb28181_fingerprints`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []GB28181Fingerprint
	for rows.Next() {
		var fp GB28181Fingerprint
		var probed sql.NullString
		if err := rows.Scan(&fp.DeviceID, &fp.Serial, &fp.SourceIP, &probed); err != nil {
			return nil, err
		}
		fp.ProbedAt = scanTime(probed)
		out = append(out, fp)
	}
	return out, rows.Err()
}

// UpsertGB28181Device inserts a device row, or updates the existing one
// while PRESERVING non-empty metadata fields. Callers upsert from multiple
// paths with partial payloads (REGISTER passes status only, keepalive passes
// status+timestamp, DeviceInfo passes name/manufacturer/model) — a blind
// INSERT OR REPLACE from the keepalive path wiped the DeviceInfo metadata
// ~20s after it landed.
func (d *DB) UpsertGB28181Device(ctx context.Context, device GB28181Device) error {
	_, err := d.db.ExecContext(ctx, `INSERT INTO gb28181_devices
		(id, name, manufacturer, model, status, last_keepalive, registered_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name = COALESCE(NULLIF(excluded.name, ''), name),
			manufacturer = COALESCE(NULLIF(excluded.manufacturer, ''), manufacturer),
			model = COALESCE(NULLIF(excluded.model, ''), model),
			status = excluded.status,
			last_keepalive = excluded.last_keepalive,
			registered_at = COALESCE(NULLIF(excluded.registered_at, ''), registered_at);`,
		device.ID, device.Name, device.Manufacturer, device.Model, device.Status,
		timeToDB(device.LastKeepalive), timeToDB(device.RegisteredAt))
	return err
}

// UpsertGB28181Channel inserts or replaces a channel row (INSERT OR REPLACE).
func (d *DB) UpsertGB28181Channel(ctx context.Context, channel GB28181Channel) error {
	_, err := d.db.ExecContext(ctx, `INSERT OR REPLACE INTO gb28181_channels
		(id, device_id, name, manufacturer, parental, status, camera_id, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?);`,
		channel.ID, channel.DeviceID, channel.Name, channel.Manufacturer,
		channel.Parental, channel.Status, channel.CameraID, timeToDB(channel.UpdatedAt))
	return err
}

// ListGB28181Devices returns all registered devices, sorted by ID.
func (d *DB) ListGB28181Devices(ctx context.Context) ([]GB28181Device, error) {
	rows, err := d.readConn().QueryContext(ctx, `SELECT id, name, manufacturer, model, status, last_keepalive, registered_at
		FROM gb28181_devices ORDER BY id;`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var res []GB28181Device
	for rows.Next() {
		var dev GB28181Device
		var lastKeepalive, registeredAt sql.NullString
		if err := rows.Scan(&dev.ID, &dev.Name, &dev.Manufacturer, &dev.Model, &dev.Status,
			&lastKeepalive, &registeredAt); err != nil {
			return nil, err
		}
		dev.LastKeepalive = scanTime(lastKeepalive)
		dev.RegisteredAt = scanTime(registeredAt)
		res = append(res, dev)
	}
	return res, rows.Err()
}

// ListGB28181Channels returns the channels of deviceID, sorted by channel ID.
func (d *DB) ListGB28181Channels(ctx context.Context, deviceID string) ([]GB28181Channel, error) {
	rows, err := d.readConn().QueryContext(ctx, `SELECT id, device_id, name, manufacturer, parental, status, camera_id, updated_at
		FROM gb28181_channels WHERE device_id=? ORDER BY id;`, deviceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var res []GB28181Channel
	for rows.Next() {
		var ch GB28181Channel
		var cameraID, updatedAt sql.NullString
		if err := rows.Scan(&ch.ID, &ch.DeviceID, &ch.Name, &ch.Manufacturer, &ch.Parental,
			&ch.Status, &cameraID, &updatedAt); err != nil {
			return nil, err
		}
		ch.CameraID = cameraID.String
		ch.UpdatedAt = scanTime(updatedAt)
		res = append(res, ch)
	}
	return res, rows.Err()
}

// MarkDeviceOffline sets a device's status to "offline". Idempotent: a no-op
// (0 rows affected) when the device does not exist.
func (d *DB) MarkDeviceOffline(ctx context.Context, id string) error {
	_, err := d.db.ExecContext(ctx, `UPDATE gb28181_devices SET status='offline' WHERE id=?;`, id)
	return err
}

// BindChannelCamera links a channel to a MiBee camera by setting camera_id.
// Idempotent: a no-op (0 rows affected) when the channel does not exist.
func (d *DB) BindChannelCamera(ctx context.Context, channelID, cameraID string) error {
	_, err := d.db.ExecContext(ctx, `UPDATE gb28181_channels SET camera_id=? WHERE id=?;`, cameraID, channelID)
	return err
}

// DeleteGB28181Device removes a device and its channels in one transaction.
// The tables have no FK constraints (SQLite default) — the cascade is
// explicit here so a device delete can never orphan its channel rows.
func (d *DB) DeleteGB28181Device(ctx context.Context, id string) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("delete gb28181 device begin tx: %w", err)
	}
	defer tx.Rollback() // no-op if committed

	if _, err := tx.ExecContext(ctx, `DELETE FROM gb28181_channels WHERE device_id=?;`, id); err != nil {
		return fmt.Errorf("delete gb28181 channels: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM gb28181_devices WHERE id=?;`, id); err != nil {
		return fmt.Errorf("delete gb28181 device: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("delete gb28181 device commit: %w", err)
	}
	return nil
}

// GetGB28181Device returns a single GB28181 device by ID.
func (d *DB) GetGB28181Device(ctx context.Context, id string) (*GB28181Device, error) {
	row := d.db.QueryRowContext(ctx, `SELECT id, name, manufacturer, model, status, last_keepalive, registered_at
		FROM gb28181_devices WHERE id=?;`, id)
	var dev GB28181Device
	var lastKeepalive, registeredAt sql.NullString
	if err := row.Scan(&dev.ID, &dev.Name, &dev.Manufacturer, &dev.Model, &dev.Status,
		&lastKeepalive, &registeredAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	dev.LastKeepalive = scanTime(lastKeepalive)
	dev.RegisteredAt = scanTime(registeredAt)
	return &dev, nil
}
