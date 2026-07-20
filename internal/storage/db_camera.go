package storage

import (
	"context"
	"database/sql"
	"fmt"
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
	ID            string               `json:"id"`
	Name          string               `json:"name"`
	Protocol      string               `json:"protocol"`
	Encoding      string               `json:"encoding"`
	URL           string               `json:"url"`
	Description   string               `json:"description"`
	Location      string               `json:"location"`
	Brand         string               `json:"brand"`
	Model         string               `json:"model"`
	SerialNumber  string               `json:"serial_number"`
	RetentionDays int                  `json:"retention_days"`
	Status        model.RecorderStatus `json:"status"`
	ErrorType     *string              `json:"error_type"`
	ErrorDetail   *string              `json:"error_detail"`
	LastSeen      *time.Time           `json:"last_seen,omitempty"`
	Username      string               `json:"username"`
	HasPassword   bool                 `json:"has_password"`
	// Per-camera merge config (nil = use global)
	MergeEnabled            *bool      `json:"merge_enabled,omitempty"`
	MergeCheckInterval      *string    `json:"merge_check_interval,omitempty"`
	MergeWindowSize         *string    `json:"merge_window_size,omitempty"`
	MergeBatchLimit         *int       `json:"merge_batch_limit,omitempty"`
	MergeMinSegmentAge      *string    `json:"merge_min_segment_age,omitempty"`
	MergeMinSegmentsToMerge *int       `json:"merge_min_segments_to_merge,omitempty"`
	ONVIFEndpoint           string     `json:"onvif_endpoint"`
	ProfileToken            string     `json:"profile_token"`
	StreamEncoding          string     `json:"stream_encoding"`
	Archived                bool       `json:"archived"`
	ArchivedAt              *time.Time `json:"archived_at,omitempty"`
	ArchiveRetentionDays    int        `json:"archive_retention_days"`
	// Transcoding config injected from YAML at API response time
	Transcoding *config.CameraTranscodingConfig `json:"transcoding,omitempty"`
	// Channel injected from YAML at API response time (Xiaomi dual-lens)
	Channel string `json:"channel,omitempty"`
	// AudioEnabled injected from YAML at API response time
	AudioEnabled bool `json:"audio_enabled"`
	// Push/ingest fields injected from YAML at API response time (SRT/RTMP).
	// Persisted via UpsertCameraIngest (separate from UpsertCamera).
	StreamKey     string `json:"stream_key,omitempty"`
	SRTPassphrase string `json:"srt_passphrase,omitempty"`
	SRTStreamID   string `json:"srt_stream_id,omitempty"`
	// Push-out relay targets + retention, injected from YAML at API response time.
	PushTargets       []config.PushTargetConfig `json:"push_targets,omitempty"`
	PushRetentionDays *int                      `json:"push_retention_days,omitempty"`
	// StableID is the ONVIF serial number used for IP self-healing.
	// Persisted in DB via UpsertCamera / UpdateCameraStableID.
	StableID    string   `json:"stable_id,omitempty"`
	SubnetHints []string `json:"subnet_hints,omitempty"`
	// Dark frame filtering (injected from YAML at API response time)
	DarkFrameFilterEnabled bool `json:"dark_frame_filter_enabled"`
	DarkFrameThreshold     int  `json:"dark_frame_threshold"`
	// Recording gate (injected from YAML at API response time). nil = record;
	// pointer to false = live-only (no segments written to disk).
	RecordingEnabled *bool `json:"recording_enabled,omitempty"`
	// Recording schedule (injected from YAML at API response time)
	RecordingSchedule *config.ScheduleConfig `json:"recording_schedule,omitempty"`
	// ActivationState: "active" (recorder runs) or "pending_activation"
	// (persisted + visible but recorder not started — awaiting credentials).
	// "" is treated as "active". Set by auto-discover for authenticated devices.
	ActivationState string `json:"activation_state,omitempty"`
}

func (d *DB) ListCameras(ctx context.Context) ([]CameraRow, error) {
	rows, err := d.readConn().QueryContext(ctx, `SELECT id, name, protocol, encoding, url, description, location, brand, model, serial_number, stable_id, retention_days, username, CASE WHEN password IS NOT NULL AND password != '' THEN 1 ELSE 0 END as has_password,
		merge_enabled, merge_check_interval, merge_window_size, merge_batch_limit, merge_min_segment_age, merge_min_segments_to_merge,
		onvif_endpoint, profile_token, stream_encoding,
		archived, archived_at, archive_retention_days,
		COALESCE(activation_state, 'active')
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
		if err := rows.Scan(&c.ID, &c.Name, &c.Protocol, &c.Encoding, &c.URL, &c.Description, &c.Location, &c.Brand, &c.Model, &c.SerialNumber, &c.StableID, &c.RetentionDays, &c.Username, &c.HasPassword,
			&mergeEnabled, &mergeCheckInterval, &mergeWindowSize, &mergeBatchLimit, &mergeMinSegmentAge, &mergeMinSegmentsToMerge,
			&c.ONVIFEndpoint, &c.ProfileToken, &c.StreamEncoding,
			&c.Archived, &archivedAtStr, &c.ArchiveRetentionDays,
			&c.ActivationState); err != nil {
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
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return res, nil
}

// ListArchivedCameras returns only cameras marked as archived.
func (d *DB) ListArchivedCameras(ctx context.Context) ([]CameraRow, error) {
	rows, err := d.readConn().QueryContext(ctx, `SELECT id, name, protocol, encoding, url, description, location, brand, model, serial_number, stable_id, retention_days, username, CASE WHEN password IS NOT NULL AND password != '' THEN 1 ELSE 0 END as has_password,
		merge_enabled, merge_check_interval, merge_window_size, merge_batch_limit, merge_min_segment_age, merge_min_segments_to_merge,
		onvif_endpoint, profile_token, stream_encoding,
		archived, archived_at, archive_retention_days,
		COALESCE(activation_state, 'active')
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
		if err := rows.Scan(&c.ID, &c.Name, &c.Protocol, &c.Encoding, &c.URL, &c.Description, &c.Location, &c.Brand, &c.Model, &c.SerialNumber, &c.StableID, &c.RetentionDays, &c.Username, &c.HasPassword,
			&mergeEnabled, &mergeCheckInterval, &mergeWindowSize, &mergeBatchLimit, &mergeMinSegmentAge, &mergeMinSegmentsToMerge,
			&c.ONVIFEndpoint, &c.ProfileToken, &c.StreamEncoding,
			&c.Archived, &archivedAtStr, &c.ArchiveRetentionDays,
			&c.ActivationState); err != nil {
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
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return res, nil
}

// UpsertCamera inserts or updates a camera record in the database
func (d *DB) UpsertCamera(ctx context.Context, id, name, protocol, encoding, url, username, password string, onvifEndpoint, profileToken, streamEncoding, stableID string) error {
	q := `INSERT INTO cameras(id, name, protocol, encoding, url, username, password, onvif_endpoint, profile_token, stream_encoding, stable_id) VALUES(?,?,?,?,?,?,?,?,?,?,?)

		 ON CONFLICT(id) DO UPDATE SET name=excluded.name, protocol=excluded.protocol, encoding=excluded.encoding, url=excluded.url, username=excluded.username, password=excluded.password, onvif_endpoint=excluded.onvif_endpoint, profile_token=excluded.profile_token, stream_encoding=excluded.stream_encoding, stable_id=excluded.stable_id;`

	_, err := d.db.ExecContext(ctx, q, id, name, protocol, encoding, url, username, password, onvifEndpoint, profileToken, streamEncoding, stableID)

	return err
}

// UpsertCameraIngest writes the push/ingest columns (stream_key, srt_passphrase,
// srt_stream_id) for a camera. Kept separate from UpsertCamera (which ~60 call
// sites use) to avoid a sweeping signature change — mirrors the
// UpsertCameraMerge pattern. Only relevant for srt/rtmp protocol cameras.
func (d *DB) UpsertCameraIngest(ctx context.Context, cameraID, streamKey, srtPassphrase, srtStreamID string) error {
	q := `UPDATE cameras SET stream_key=?, srt_passphrase=?, srt_stream_id=? WHERE id=?;`
	_, err := d.db.ExecContext(ctx, q, streamKey, srtPassphrase, srtStreamID, cameraID)
	return err
}

// UpdateCameraActivationState sets the activation_state column for a camera.
// Empty state is normalized to "active". Used by auto-discover (pending->active
// on credential activation) and by the AddCamera path to persist the state.
func (d *DB) UpdateCameraActivationState(ctx context.Context, cameraID, state string) error {
	if state == "" {
		state = "active"
	}
	_, err := d.db.ExecContext(ctx, `UPDATE cameras SET activation_state=? WHERE id=?;`, state, cameraID)
	return err
}

// CameraExistsByOnvifEndpoint reports whether ANY camera row — including
// ARCHIVED ones — already references the given onvif_endpoint or serial_number.
// Used by auto-discover dedup: ListCameras (the usual dedup source) only
// returns archived=0 rows, so without this an archived camera would be
// invisible to dedup and auto-discover would immediately re-enroll the same
// physical device the user just archived. Querying the whole table (no archived
// filter) closes that gap.
//
// serial is matched against serial_number + stable_id (stable_id is now
// DB-persisted via the v27 migration).
func (d *DB) CameraExistsByOnvifEndpoint(ctx context.Context, onvifEndpoint, serial string) (bool, error) {
	if onvifEndpoint == "" && serial == "" {
		return false, nil
	}
	if onvifEndpoint != "" {
		var c int
		if err := d.readConn().QueryRowContext(ctx, `SELECT COUNT(*) FROM cameras WHERE onvif_endpoint=? LIMIT 1`, onvifEndpoint).Scan(&c); err != nil {
			return false, err
		}
		if c > 0 {
			return true, nil
		}
	}
	if serial != "" {
		var c int
		// Check both serial_number and stable_id
		if err := d.readConn().QueryRowContext(ctx, `SELECT COUNT(*) FROM cameras WHERE serial_number=? OR stable_id=? LIMIT 1`, serial, serial).Scan(&c); err != nil {
			return false, err
		}
		if c > 0 {
			return true, nil
		}
	}
	return false, nil
}

func (d *DB) GetCamera(ctx context.Context, cameraID string) (*CameraRow, error) {
	var c CameraRow
	var mergeEnabled sql.NullBool
	var mergeCheckInterval, mergeWindowSize, mergeMinSegmentAge sql.NullString
	var mergeBatchLimit, mergeMinSegmentsToMerge sql.NullInt64
	var archivedAtStr sql.NullString
	err := d.readConn().QueryRowContext(ctx, `SELECT id, name, protocol, encoding, url, description, location, brand, model, serial_number, stable_id, retention_days, username, CASE WHEN password IS NOT NULL AND password != '' THEN 1 ELSE 0 END as has_password,
		merge_enabled, merge_check_interval, merge_window_size, merge_batch_limit, merge_min_segment_age, merge_min_segments_to_merge,
		onvif_endpoint, profile_token, stream_encoding,
		archived, archived_at, archive_retention_days,
		COALESCE(activation_state, 'active')
		FROM cameras WHERE id = ?`, cameraID).Scan(
		&c.ID, &c.Name, &c.Protocol, &c.Encoding, &c.URL, &c.Description, &c.Location, &c.Brand, &c.Model, &c.SerialNumber, &c.StableID, &c.RetentionDays, &c.Username, &c.HasPassword,
		&mergeEnabled, &mergeCheckInterval, &mergeWindowSize, &mergeBatchLimit, &mergeMinSegmentAge, &mergeMinSegmentsToMerge,
		&c.ONVIFEndpoint, &c.ProfileToken, &c.StreamEncoding,
		&c.Archived, &archivedAtStr, &c.ArchiveRetentionDays,
		&c.ActivationState,
	)
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

// GetCameraByID retrieves a single camera record by its ID.
// Returns nil if the camera does not exist.
func (d *DB) GetCameraByID(ctx context.Context, cameraID string) (*CameraRow, error) {
	return d.GetCamera(ctx, cameraID)
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

// DeleteCameraRow removes a camera record from the database.
// Unlike DeleteCamera, this does NOT return an error if the camera does not exist.
func (d *DB) DeleteCameraRow(ctx context.Context, cameraID string) error {
	_, err := d.db.ExecContext(ctx, `DELETE FROM cameras WHERE id = ?;`, cameraID)
	return err
}

// UpdateCameraMetadata updates DB-only metadata fields for a camera.
func (d *DB) UpdateCameraMetadata(ctx context.Context, id, description, location, brand, model, serialNumber string, retentionDays int) error {
	q := `UPDATE cameras SET description=?, location=?, brand=?, model=?, serial_number=?, retention_days=? WHERE id=?;`
	_, err := d.db.ExecContext(ctx, q, description, location, brand, model, serialNumber, retentionDays, id)
	return err
}

// UpdateCameraStableID updates the stable_id column for a camera.
// stable_id is the ONVIF serial number used for IP self-healing.
// Idempotent: does nothing if cameraID does not exist (0 rows affected).
func (d *DB) UpdateCameraStableID(ctx context.Context, cameraID, stableID string) error {
	_, err := d.db.ExecContext(ctx, `UPDATE cameras SET stable_id=? WHERE id=?;`, stableID, cameraID)
	return err
}

// GetCameraStableID retrieves the stable_id for a camera.
// Returns empty string if camera not found or stable_id is not set.
func (d *DB) GetCameraStableID(ctx context.Context, cameraID string) (string, error) {
	var stableID string
	err := d.readConn().QueryRowContext(ctx, `SELECT COALESCE(stable_id, '') FROM cameras WHERE id=? LIMIT 1`, cameraID).Scan(&stableID)
	if err != nil {
		if err == sql.ErrNoRows {
			return "", nil
		}
		return "", err
	}
	return stableID, nil
}

// CameraExistsByStableID reports whether any camera row (including archived)
// has the given stable_id. Used for dedup when enrolling a device by its
// ONVIF serial number.
func (d *DB) CameraExistsByStableID(ctx context.Context, stableID string) (bool, error) {
	if stableID == "" {
		return false, nil
	}
	var c int
	if err := d.readConn().QueryRowContext(ctx, `SELECT COUNT(*) FROM cameras WHERE stable_id=? LIMIT 1`, stableID).Scan(&c); err != nil {
		return false, err
	}
	return c > 0, nil
}

// ReassignCameraStableID moves a stable_id from one camera to another.
// Previously used to atomically transfer the identity when merging duplicate
// camera records. Sets the old camera's stable_id to empty string.
func (d *DB) ReassignCameraStableID(ctx context.Context, fromCameraID, toCameraID string) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("reassign stable_id begin tx: %w", err)
	}
	defer tx.Rollback() // no-op if committed

	// Read current stable_id from source
	var stableID string
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(stable_id, '') FROM cameras WHERE id=? LIMIT 1`, fromCameraID).Scan(&stableID); err != nil {
		if err == sql.ErrNoRows {
			return nil // source camera gone, nothing to reassign
		}
		return fmt.Errorf("reassign stable_id read source: %w", err)
	}
	if stableID == "" {
		return nil // nothing to reassign
	}

	// Clear source
	if _, err := tx.ExecContext(ctx, `UPDATE cameras SET stable_id='' WHERE id=?`, fromCameraID); err != nil {
		return fmt.Errorf("reassign stable_id clear source: %w", err)
	}

	// Set destination
	if _, err := tx.ExecContext(ctx, `UPDATE cameras SET stable_id=? WHERE id=?`, stableID, toCameraID); err != nil {
		return fmt.Errorf("reassign stable_id set dest: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("reassign stable_id commit: %w", err)
	}
	return nil
}

// ReassignCameraData atomically re-tags all related data rows from sourceCameraID
// to targetCameraID in a single transaction. Updates recordings, camera_health_events,
// ai_events, and transcoding_tasks tables.
// Returns an error if the target camera does not exist.
// Does NOT delete the source camera row — call DeleteCameraRow separately.
func (d *DB) ReassignCameraData(ctx context.Context, sourceCameraID, targetCameraID string) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("reassign data begin tx: %w", err)
	}
	defer tx.Rollback() // no-op if committed

	// Pre-check: target camera must exist
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM cameras WHERE id=? LIMIT 1`, targetCameraID).Scan(&exists); err != nil {
		if err == sql.ErrNoRows {
			return fmt.Errorf("target camera %q not found", targetCameraID)
		}
		return fmt.Errorf("reassign data check target: %w", err)
	}

	// Re-tag recordings — also rewrite path columns (file_path, merge_path, thumbnail_path)
	// so they point to the target camera's directory instead of the source camera's.
	// Without this, the disk step (which moves files to target dir) would leave the DB
	// pointing to the now-empty source dir, breaking playback and orphaning every row.
	if _, err := tx.ExecContext(ctx,
		`UPDATE recordings SET camera_id=? WHERE camera_id=?`, targetCameraID, sourceCameraID); err != nil {
		return fmt.Errorf("reassign recordings: %w", err)
	}

	// Rewrite path columns: file_path may be NULL or empty; only rewrite when it contains sourceID.
	// Use REPLACE() to swap the source camera ID prefix in the directory and filename components.
	if _, err := tx.ExecContext(ctx,
		`UPDATE recordings SET file_path = REPLACE(file_path, ?, ?) WHERE camera_id=? AND file_path LIKE ?`,
		sourceCameraID, targetCameraID, targetCameraID, "%"+sourceCameraID+"%"); err != nil {
		return fmt.Errorf("rewrite file_path: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE recordings SET merge_path = REPLACE(merge_path, ?, ?) WHERE camera_id=? AND merge_path LIKE ?`,
		sourceCameraID, targetCameraID, targetCameraID, "%"+sourceCameraID+"%"); err != nil {
		return fmt.Errorf("rewrite merge_path: %w", err)
	}
	// Re-tag camera_health_events
	if _, err := tx.ExecContext(ctx, `UPDATE camera_health_events SET camera_id=? WHERE camera_id=?`, targetCameraID, sourceCameraID); err != nil {
		return fmt.Errorf("reassign health events: %w", err)
	}

	// Re-tag ai_events
	if _, err := tx.ExecContext(ctx, `UPDATE ai_events SET camera_id=? WHERE camera_id=?`, targetCameraID, sourceCameraID); err != nil {
		return fmt.Errorf("reassign ai events: %w", err)
	}

	// Re-tag transcoding_tasks
	if _, err := tx.ExecContext(ctx, `UPDATE transcoding_tasks SET camera_id=? WHERE camera_id=?`, targetCameraID, sourceCameraID); err != nil {
		return fmt.Errorf("reassign transcoding tasks: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("reassign data commit: %w", err)
	}
	return nil
}
