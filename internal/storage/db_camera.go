package storage

import (
	"context"
	"database/sql"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
)

// CameraRow represents a camera record from the SQLite database.
// Shared fields with config.CameraConfig: ID, Name, Protocol, Encoding, URL, Username,
// ONVIFEndpoint, ProfileToken, StreamEncoding.
// CameraRow adds DB-only fields: Description, Location, Brand, Model, SerialNumber,
// RetentionDays, Status, LastSeen, HasPassword, merge config, archive fields.
type CameraRow struct {
	ID           string               `json:"id"`
	Name         string               `json:"name"`
	Protocol     string               `json:"protocol"`
	Encoding     string               `json:"encoding"`
	URL          string               `json:"url"`
	Description  string               `json:"description"`
	Location     string               `json:"location"`
	Brand        string               `json:"brand"`
	Model        string               `json:"model"`
	SerialNumber string               `json:"serial_number"`
	RetentionDays int                 `json:"retention_days"`
	Status       model.RecorderStatus `json:"status"`
	ErrorType    *string              `json:"error_type"`
	ErrorDetail  *string              `json:"error_detail"`
	LastSeen     *time.Time           `json:"last_seen,omitempty"`
	Username     string               `json:"username"`
	HasPassword  bool                 `json:"has_password"`
	// Per-camera merge config (nil = use global)
	MergeEnabled            *bool   `json:"merge_enabled,omitempty"`
	MergeCheckInterval      *string `json:"merge_check_interval,omitempty"`
	MergeWindowSize         *string `json:"merge_window_size,omitempty"`
	MergeBatchLimit         *int    `json:"merge_batch_limit,omitempty"`
	MergeMinSegmentAge      *string `json:"merge_min_segment_age,omitempty"`
	MergeMinSegmentsToMerge *int    `json:"merge_min_segments_to_merge,omitempty"`
	ONVIFEndpoint           string  `json:"onvif_endpoint"`
	ProfileToken            string  `json:"profile_token"`
	StreamEncoding          string  `json:"stream_encoding"`
	Archived             bool                         `json:"archived"`
	ArchivedAt           *time.Time                    `json:"archived_at,omitempty"`
	ArchiveRetentionDays int                           `json:"archive_retention_days"`
	// Transcoding config injected from YAML at API response time
	Transcoding          *config.CameraTranscodingConfig `json:"transcoding,omitempty"`
	// Channel injected from YAML at API response time (Xiaomi dual-lens)
	Channel              string `json:"channel,omitempty"`
}

func (d *DB) ListCameras(ctx context.Context) ([]CameraRow, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT id, name, protocol, encoding, url, description, location, brand, model, serial_number, retention_days, username, CASE WHEN password IS NOT NULL AND password != '' THEN 1 ELSE 0 END as has_password,
		merge_enabled, merge_check_interval, merge_window_size, merge_batch_limit, merge_min_segment_age, merge_min_segments_to_merge,
		onvif_endpoint, profile_token, stream_encoding,
		archived, archived_at, archive_retention_days
		FROM cameras WHERE archived=0 ORDER BY id;`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var res []CameraRow
	for rows.Next() {
		var c CameraRow
		var mergeEnabled sql.NullBool
		var mergeCheckInterval, mergeWindowSize, mergeMinSegmentAge sql.NullString
		var mergeBatchLimit, mergeMinSegmentsToMerge sql.NullInt64
		var archivedAtStr sql.NullString
		if err := rows.Scan(&c.ID, &c.Name, &c.Protocol, &c.Encoding, &c.URL, &c.Description, &c.Location, &c.Brand, &c.Model, &c.SerialNumber, &c.RetentionDays, &c.Username, &c.HasPassword,
			&mergeEnabled, &mergeCheckInterval, &mergeWindowSize, &mergeBatchLimit, &mergeMinSegmentAge, &mergeMinSegmentsToMerge,
			&c.ONVIFEndpoint, &c.ProfileToken, &c.StreamEncoding,
			&c.Archived, &archivedAtStr, &c.ArchiveRetentionDays); err != nil {
			return nil, err
		}
		c.MergeEnabled = nullBoolToPtr(mergeEnabled)
		c.MergeCheckInterval = nullStringToPtr(mergeCheckInterval)
		c.MergeWindowSize = nullStringToPtr(mergeWindowSize)
		c.MergeBatchLimit = nullInt64ToPtr(mergeBatchLimit)
		c.MergeMinSegmentAge = nullStringToPtr(mergeMinSegmentAge)
		c.MergeMinSegmentsToMerge = nullInt64ToPtr(mergeMinSegmentsToMerge)
		if archivedAtStr.Valid && archivedAtStr.String != "" {
			t := scanTime(archivedAtStr)
			c.ArchivedAt = &t
		}
		res = append(res, c)
	}
	return res, nil
}

// ListArchivedCameras returns only cameras marked as archived.
func (d *DB) ListArchivedCameras(ctx context.Context) ([]CameraRow, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT id, name, protocol, encoding, url, description, location, brand, model, serial_number, retention_days, username, CASE WHEN password IS NOT NULL AND password != '' THEN 1 ELSE 0 END as has_password,
		merge_enabled, merge_check_interval, merge_window_size, merge_batch_limit, merge_min_segment_age, merge_min_segments_to_merge,
		onvif_endpoint, profile_token, stream_encoding,
		archived, archived_at, archive_retention_days
		FROM cameras WHERE archived=1 ORDER BY id;`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var res []CameraRow
	for rows.Next() {
		var c CameraRow
		var mergeEnabled sql.NullBool
		var mergeCheckInterval, mergeWindowSize, mergeMinSegmentAge sql.NullString
		var mergeBatchLimit, mergeMinSegmentsToMerge sql.NullInt64
		var archivedAtStr sql.NullString
		if err := rows.Scan(&c.ID, &c.Name, &c.Protocol, &c.Encoding, &c.URL, &c.Description, &c.Location, &c.Brand, &c.Model, &c.SerialNumber, &c.RetentionDays, &c.Username, &c.HasPassword,
			&mergeEnabled, &mergeCheckInterval, &mergeWindowSize, &mergeBatchLimit, &mergeMinSegmentAge, &mergeMinSegmentsToMerge,
			&c.ONVIFEndpoint, &c.ProfileToken, &c.StreamEncoding,
			&c.Archived, &archivedAtStr, &c.ArchiveRetentionDays); err != nil {
			return nil, err
		}
		c.MergeEnabled = nullBoolToPtr(mergeEnabled)
		c.MergeCheckInterval = nullStringToPtr(mergeCheckInterval)
		c.MergeWindowSize = nullStringToPtr(mergeWindowSize)
		c.MergeBatchLimit = nullInt64ToPtr(mergeBatchLimit)
		c.MergeMinSegmentAge = nullStringToPtr(mergeMinSegmentAge)
		c.MergeMinSegmentsToMerge = nullInt64ToPtr(mergeMinSegmentsToMerge)
		if archivedAtStr.Valid && archivedAtStr.String != "" {
			t := scanTime(archivedAtStr)
			c.ArchivedAt = &t
		}
		res = append(res, c)
	}
	return res, nil
}

// UpsertCamera inserts or updates a camera record in the database
func (d *DB) UpsertCamera(ctx context.Context, id, name, protocol, encoding, url, username, password string, onvifEndpoint, profileToken, streamEncoding string) error {

	q := `INSERT INTO cameras(id, name, protocol, encoding, url, username, password, onvif_endpoint, profile_token, stream_encoding) VALUES(?,?,?,?,?,?,?,?,?,?)

		 ON CONFLICT(id) DO UPDATE SET name=excluded.name, protocol=excluded.protocol, encoding=excluded.encoding, url=excluded.url, username=excluded.username, password=excluded.password, onvif_endpoint=excluded.onvif_endpoint, profile_token=excluded.profile_token, stream_encoding=excluded.stream_encoding;`

	_, err := d.db.ExecContext(ctx, q, id, name, protocol, encoding, url, username, password, onvifEndpoint, profileToken, streamEncoding)

	return err
}

func (d *DB) GetCamera(ctx context.Context, cameraID string) (*CameraRow, error) {
	var c CameraRow
	var mergeEnabled sql.NullBool
	var mergeCheckInterval, mergeWindowSize, mergeMinSegmentAge sql.NullString
	var mergeBatchLimit, mergeMinSegmentsToMerge sql.NullInt64
	var archivedAtStr sql.NullString
	err := d.db.QueryRowContext(ctx, `SELECT id, name, protocol, encoding, url, description, location, brand, model, serial_number, retention_days, username, CASE WHEN password IS NOT NULL AND password != '' THEN 1 ELSE 0 END as has_password,
		merge_enabled, merge_check_interval, merge_window_size, merge_batch_limit, merge_min_segment_age, merge_min_segments_to_merge,
		onvif_endpoint, profile_token, stream_encoding,
		archived, archived_at, archive_retention_days
		FROM cameras WHERE id = ?`, cameraID).Scan(
		&c.ID, &c.Name, &c.Protocol, &c.Encoding, &c.URL, &c.Description, &c.Location, &c.Brand, &c.Model, &c.SerialNumber, &c.RetentionDays, &c.Username, &c.HasPassword,
		&mergeEnabled, &mergeCheckInterval, &mergeWindowSize, &mergeBatchLimit, &mergeMinSegmentAge, &mergeMinSegmentsToMerge,
		&c.ONVIFEndpoint, &c.ProfileToken, &c.StreamEncoding,
		&c.Archived, &archivedAtStr, &c.ArchiveRetentionDays)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	c.MergeEnabled = nullBoolToPtr(mergeEnabled)
	c.MergeCheckInterval = nullStringToPtr(mergeCheckInterval)
	c.MergeWindowSize = nullStringToPtr(mergeWindowSize)
	c.MergeBatchLimit = nullInt64ToPtr(mergeBatchLimit)
	c.MergeMinSegmentAge = nullStringToPtr(mergeMinSegmentAge)
	c.MergeMinSegmentsToMerge = nullInt64ToPtr(mergeMinSegmentsToMerge)
	if archivedAtStr.Valid && archivedAtStr.String != "" {
		t := scanTime(archivedAtStr)
		c.ArchivedAt = &t
	}
	return &c, nil
}

// DeleteCamera removes a camera record from the database.
// Returns an error if the camera does not exist.
func (d *DB) DeleteCamera(ctx context.Context, cameraID string) error {
	res, err := d.db.ExecContext(ctx, `DELETE FROM cameras WHERE id = ?;`, cameraID)
	if err != nil {
		return err
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// UpdateCameraMetadata updates DB-only metadata fields for a camera.
func (d *DB) UpdateCameraMetadata(ctx context.Context, id, description, location, brand, model, serialNumber string, retentionDays int) error {
	q := `UPDATE cameras SET description=?, location=?, brand=?, model=?, serial_number=?, retention_days=? WHERE id=?;`
	_, err := d.db.ExecContext(ctx, q, description, location, brand, model, serialNumber, retentionDays, id)
	return err
}
