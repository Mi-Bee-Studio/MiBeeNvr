package storage

import (
	"context"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
)

// helperV26DB creates a database with schema version 26 (before the v27 stable_id
// migration). Used to test the upgrade path.
func helperV26DB(t *testing.T, ctx context.Context) (*DB, func(t *testing.T)) {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "v26.db")
	db, err := New(dbPath)
	require.NoError(t, err)

	// Create base tables manually (v26 schema — no stable_id column)
	_, err = db.db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS cameras (
			id TEXT PRIMARY KEY, name TEXT NOT NULL, protocol TEXT NOT NULL,
			encoding TEXT NOT NULL DEFAULT '', url TEXT NOT NULL,
			username TEXT DEFAULT '', password TEXT DEFAULT '', enabled INTEGER DEFAULT 1,
			description TEXT DEFAULT '', location TEXT DEFAULT '', brand TEXT DEFAULT '',
			model TEXT DEFAULT '', serial_number TEXT DEFAULT '',
			onvif_endpoint TEXT DEFAULT '', profile_token TEXT DEFAULT '',
			archived INTEGER DEFAULT 0, archived_at DATETIME DEFAULT NULL,
			archive_retention_days INTEGER DEFAULT 0,
			merge_enabled INTEGER, merge_check_interval TEXT,
			merge_window_size TEXT, merge_batch_limit INTEGER,
			merge_min_segment_age TEXT, merge_min_segments_to_merge INTEGER,
			stream_encoding TEXT DEFAULT '',
			merge_duration TEXT DEFAULT 'natural-day',
			stream_key TEXT DEFAULT '', srt_passphrase TEXT DEFAULT '',
			srt_stream_id TEXT DEFAULT '', activation_state TEXT DEFAULT 'active',
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		);
	`)
	require.NoError(t, err)
	// Create recordings table for FK
	_, err = db.db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS recordings (
			id TEXT PRIMARY KEY, camera_id TEXT NOT NULL, file_path TEXT NOT NULL,
			format TEXT NOT NULL, started_at DATETIME NOT NULL, ended_at DATETIME,
			duration REAL, file_size INTEGER DEFAULT 0, frame_count INTEGER DEFAULT 0,
			merged INTEGER DEFAULT 0, archived INTEGER DEFAULT 0,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			FOREIGN KEY (camera_id) REFERENCES cameras(id)
		);
	`)
	require.NoError(t, err)
	_, err = db.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);`)
	require.NoError(t, err)

	// Set schema version to 26
	_, err = db.db.ExecContext(ctx, `INSERT OR IGNORE INTO schema_meta (key, value) VALUES ('schema_version', '26');`)
	require.NoError(t, err)

	// Insert a camera for FK constraint
	_, err = db.db.ExecContext(ctx, `INSERT INTO cameras (id, name, protocol, url) VALUES ('cam-pre-v27', 'Pre-V27 Cam', 'rtsp', 'rtsp://host/stream');`)
	require.NoError(t, err)

	return db, func(t *testing.T) {
		t.Helper()
		db.Close()
	}
}

// TestMigrationV27_FreshInstall verifies that stable_id column exists on a
// freshly initialized database (clean install path).
func TestMigrationV27_FreshInstall(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "fresh_v27.db")
	db, err := New(dbPath)
	require.NoError(t, err)
	defer db.Close()

	err = db.Init(ctx)
	require.NoError(t, err)

	// Verify stable_id column exists
	var colExists int
	err = db.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('cameras') WHERE name='stable_id'`).Scan(&colExists)
	require.NoError(t, err)
	require.Equal(t, 1, colExists, "stable_id column must exist after fresh Init")

	// Verify schema version >= 27
	var version string
	err = db.db.QueryRowContext(ctx, "SELECT value FROM schema_meta WHERE key='schema_version'").Scan(&version)
	require.NoError(t, err)
	n, err := strconv.Atoi(version)
	require.NoError(t, err)
	require.GreaterOrEqual(t, n, 27, "schema_version must be >= 27 after migration")
}

// TestMigrationV27_UpgradePath verifies that stable_id column is added when
// migrating from an existing v26 database (upgrade path).
func TestMigrationV27_UpgradePath(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, cleanup := helperV26DB(t, ctx)
	defer cleanup(t)

	// Verify stable_id column does NOT exist before migration
	var colExists int
	_ = db.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('cameras') WHERE name='stable_id'`).Scan(&colExists)
	require.Equal(t, 0, colExists, "stable_id column should NOT exist before migration")

	// Verify schema version is 26 before migration
	var preVersion string
	err := db.db.QueryRowContext(ctx, "SELECT value FROM schema_meta WHERE key='schema_version'").Scan(&preVersion)
	require.NoError(t, err)
	require.Equal(t, "26", preVersion, "schema_version should be 26 before v27 migration")

	// Run Init — should apply v26→v27 migration
	err = db.Init(ctx)
	require.NoError(t, err)

	// Verify stable_id column now exists
	_ = db.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('cameras') WHERE name='stable_id'`).Scan(&colExists)
	require.Equal(t, 1, colExists, "stable_id column should exist after migration")

	// Verify schema version >= 27
	var version string
	err = db.db.QueryRowContext(ctx, "SELECT value FROM schema_meta WHERE key='schema_version'").Scan(&version)
	require.NoError(t, err)
	n, err := strconv.Atoi(version)
	require.NoError(t, err)
	require.GreaterOrEqual(t, n, 27, "schema_version must be >= 27 after migration")
}

// TestCameraExistsByStableID verifies CameraExistsByStableID works for
// existence, non-existence, and empty stableID cases.
func TestCameraExistsByStableID(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test_stable_id_exists.db")
	db, err := New(dbPath)
	require.NoError(t, err)
	defer db.Close()

	err = db.Init(ctx)
	require.NoError(t, err)

	// Insert cameras with known stable_ids
	_, err = db.db.ExecContext(ctx, `INSERT INTO cameras (id, name, protocol, url, stable_id) VALUES ('cam-1', 'Cam 1', 'rtsp', 'rtsp://host/1', 'SERIAL-001')`)
	require.NoError(t, err)
	_, err = db.db.ExecContext(ctx, `INSERT INTO cameras (id, name, protocol, url, stable_id) VALUES ('cam-2', 'Cam 2', 'rtsp', 'rtsp://host/2', 'SERIAL-002')`)
	require.NoError(t, err)
	_, err = db.db.ExecContext(ctx, `INSERT INTO cameras (id, name, protocol, url, stable_id) VALUES ('cam-3', 'Cam 3', 'onvif', 'rtsp://host/3', '')`)
	require.NoError(t, err)

	// Test: exists for known stable_id
	exists, err := db.CameraExistsByStableID(ctx, "SERIAL-001")
	require.NoError(t, err)
	require.True(t, exists, "CameraExistsByStableID should return true for existing stable_id")

	// Test: exists for second known stable_id
	exists, err = db.CameraExistsByStableID(ctx, "SERIAL-002")
	require.NoError(t, err)
	require.True(t, exists, "CameraExistsByStableID should return true for existing stable_id")

	// Test: does not exist for unknown stable_id
	exists, err = db.CameraExistsByStableID(ctx, "SERIAL-999")
	require.NoError(t, err)
	require.False(t, exists, "CameraExistsByStableID should return false for unknown stable_id")

	// Test: empty stableID returns false
	exists, err = db.CameraExistsByStableID(ctx, "")
	require.NoError(t, err)
	require.False(t, exists, "CameraExistsByStableID should return false for empty stableID")
}

// TestUpdateCameraStableID verifies UpdateCameraStableID sets and overwrites
// the stable_id column correctly.
func TestUpdateCameraStableID(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test_update_stable_id.db")
	db, err := New(dbPath)
	require.NoError(t, err)
	defer db.Close()

	err = db.Init(ctx)
	require.NoError(t, err)

	// Insert a camera
	_, err = db.db.ExecContext(ctx, `INSERT INTO cameras (id, name, protocol, url, stable_id) VALUES ('cam-1', 'Cam 1', 'rtsp', 'rtsp://host/1', '')`)
	require.NoError(t, err)

	// Verify stable_id is empty initially
	var stableID string
	err = db.db.QueryRowContext(ctx, `SELECT COALESCE(stable_id, '') FROM cameras WHERE id='cam-1'`).Scan(&stableID)
	require.NoError(t, err)
	require.Equal(t, "", stableID, "stable_id should be empty initially")

	// Update stable_id
	err = db.UpdateCameraStableID(ctx, "cam-1", "SERIAL-001")
	require.NoError(t, err)

	// Verify updated
	err = db.db.QueryRowContext(ctx, `SELECT COALESCE(stable_id, '') FROM cameras WHERE id='cam-1'`).Scan(&stableID)
	require.NoError(t, err)
	require.Equal(t, "SERIAL-001", stableID, "stable_id should be updated")

	// Overwrite with a different value
	err = db.UpdateCameraStableID(ctx, "cam-1", "SERIAL-002")
	require.NoError(t, err)

	err = db.db.QueryRowContext(ctx, `SELECT COALESCE(stable_id, '') FROM cameras WHERE id='cam-1'`).Scan(&stableID)
	require.NoError(t, err)
	require.Equal(t, "SERIAL-002", stableID, "stable_id should be overwritten")
}

// TestGetCameraStableID verifies GetCameraStableID retrieves the stable_id
// correctly for existing and non-existent cameras.
func TestGetCameraStableID(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test_get_stable_id.db")
	db, err := New(dbPath)
	require.NoError(t, err)
	defer db.Close()

	err = db.Init(ctx)
	require.NoError(t, err)

	// Insert cameras with and without stable_id
	_, err = db.db.ExecContext(ctx, `INSERT INTO cameras (id, name, protocol, url, stable_id) VALUES ('cam-1', 'Cam 1', 'rtsp', 'rtsp://host/1', 'SERIAL-001')`)
	require.NoError(t, err)
	_, err = db.db.ExecContext(ctx, `INSERT INTO cameras (id, name, protocol, url, stable_id) VALUES ('cam-2', 'Cam 2', 'rtsp', 'rtsp://host/2', '')`)
	require.NoError(t, err)

	// Test: get stable_id for camera with one set
	sid, err := db.GetCameraStableID(ctx, "cam-1")
	require.NoError(t, err)
	require.Equal(t, "SERIAL-001", sid, "GetCameraStableID should return the stable_id")

	// Test: get stable_id for camera with empty value
	sid, err = db.GetCameraStableID(ctx, "cam-2")
	require.NoError(t, err)
	require.Equal(t, "", sid, "GetCameraStableID should return empty for unset stable_id")

	// Test: get stable_id for non-existent camera
	sid, err = db.GetCameraStableID(ctx, "cam-nonexistent")
	require.NoError(t, err)
	require.Equal(t, "", sid, "GetCameraStableID should return empty for non-existent camera")
}
