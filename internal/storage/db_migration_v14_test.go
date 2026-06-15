package storage

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/stretchr/testify/require"
)

// helperV14DB creates a fresh DB with v14 schema (no merge_path/merge_error columns),
// inserts test recordings, and returns the DB.
// The caller is responsible for closing the DB.
func helperV14DB(t *testing.T, ctx context.Context) (*DB, func(t *testing.T)) {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "v14.db")
	db, err := New(dbPath)
	require.NoError(t, err)

	// Create base tables manually (v14 schema — no merge_path/merge_error columns)
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
			stream_encoding TEXT DEFAULT '', retention_days INTEGER DEFAULT 0,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		);
	`)
	require.NoError(t, err)
	_, err = db.db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS recordings (
			id TEXT PRIMARY KEY, camera_id TEXT NOT NULL, file_path TEXT NOT NULL,
			format TEXT NOT NULL, started_at DATETIME NOT NULL, ended_at DATETIME,
			duration REAL, file_size INTEGER DEFAULT 0, frame_count INTEGER DEFAULT 0,
			merged INTEGER DEFAULT 0, merge_status TEXT NOT NULL DEFAULT 'pending',
			archived INTEGER DEFAULT 0,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			FOREIGN KEY (camera_id) REFERENCES cameras(id)
		);
	`)
	require.NoError(t, err)
	_, err = db.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);`)
	require.NoError(t, err)

	// Set schema version to 14
	_, err = db.db.ExecContext(ctx, `INSERT OR IGNORE INTO schema_meta (key, value) VALUES ('schema_version', '14');`)
	require.NoError(t, err)

	// Insert a camera for FK constraint
	_, err = db.db.ExecContext(ctx, `INSERT INTO cameras (id, name, protocol, url) VALUES ('cam1', 'Test Cam', 'rtsp', 'rtsp://host/stream');`)
	require.NoError(t, err)

	// Insert 3 recordings
	for _, rec := range []struct {
		id          string
		mergeStatus string
	}{
		{"v14-rec-1", model.MergeStatusPending},
		{"v14-rec-2", model.MergeStatusMerged},
		{"v14-rec-3", model.MergeStatusFailed},
	} {
		_, err = db.db.ExecContext(ctx,
			`INSERT INTO recordings (id, camera_id, file_path, format, started_at, ended_at, duration, file_size, frame_count, merged, merge_status, archived) VALUES (?,?,?,?,?,?,?,?,?,?,?,0);`,
			rec.id, "cam1", "/path/"+rec.id+".mp4", "h264",
			"2026-06-01 10:00:00", "2026-06-01 10:01:00",
			60.0, 1024, 60, 0, rec.mergeStatus,
		)
		require.NoError(t, err)
	}

	return db, func(t *testing.T) {
		t.Helper()
		db.Close()
	}
}

// TestMergeColumnsExist verifies that merge_path and merge_error columns
// are added during migration v14→v15 on a fresh DB.
func TestMergeColumnsExist(t *testing.T) {
	ctx := context.Background()

	// Test 1: Fresh DB — columns should exist after Init
	t.Run("fresh_db_has_columns", func(t *testing.T) {
		dir := t.TempDir()
		dbPath := filepath.Join(dir, "fresh.db")
		db, err := New(dbPath)
		require.NoError(t, err)
		defer db.Close()

		err = db.Init(ctx)
		require.NoError(t, err)

		// Verify merge_path column exists
		var colExists int
		_ = db.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('recordings') WHERE name='merge_path'`).Scan(&colExists)
		require.Equal(t, 1, colExists, "merge_path column should exist after Init")

		// Verify merge_error column exists
		_ = db.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('recordings') WHERE name='merge_error'`).Scan(&colExists)
		require.Equal(t, 1, colExists, "merge_error column should exist after Init")

		// Verify merge_status column still exists (regression check)
		_ = db.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('recordings') WHERE name='merge_status'`).Scan(&colExists)
		require.Equal(t, 1, colExists, "merge_status column should still exist")

		// Verify schema version is 16
		var version string
		err = db.db.QueryRowContext(ctx, "SELECT value FROM schema_meta WHERE key='schema_version'").Scan(&version)
		require.NoError(t, err)
		require.Equal(t, "21", version)
	})

	// Test 2: Existing DB at v14 — columns added via migration
	t.Run("migration_from_v14", func(t *testing.T) {
		db, cleanup := helperV14DB(t, ctx)
		defer cleanup(t)

		// Verify merge_path/merge_error do NOT exist before migration
		var colExists int
		_ = db.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('recordings') WHERE name='merge_path'`).Scan(&colExists)
		require.Equal(t, 0, colExists, "merge_path should not exist before migration")
		_ = db.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('recordings') WHERE name='merge_error'`).Scan(&colExists)
		require.Equal(t, 0, colExists, "merge_error should not exist before migration")

		// Run Init — should apply v14→v15 migration
		err := db.Init(ctx)
		require.NoError(t, err)

		// Verify columns now exist
		_ = db.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('recordings') WHERE name='merge_path'`).Scan(&colExists)
		require.Equal(t, 1, colExists, "merge_path should exist after migration")
		_ = db.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('recordings') WHERE name='merge_error'`).Scan(&colExists)
		require.Equal(t, 1, colExists, "merge_error should exist after migration")

		// Verify existing data is preserved
		var count int
		err = db.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM recordings").Scan(&count)
		require.NoError(t, err)
		require.Equal(t, 3, count, "all 3 existing recordings should be preserved")

		// Verify merge_status values preserved
		var mergeStatus string
		err = db.db.QueryRowContext(ctx, `SELECT merge_status FROM recordings WHERE id='v14-rec-1'`).Scan(&mergeStatus)
		require.NoError(t, err)
		require.Equal(t, model.MergeStatusPending, mergeStatus)
		err = db.db.QueryRowContext(ctx, `SELECT merge_status FROM recordings WHERE id='v14-rec-2'`).Scan(&mergeStatus)
		require.NoError(t, err)
		require.Equal(t, model.MergeStatusMerged, mergeStatus)
		err = db.db.QueryRowContext(ctx, `SELECT merge_status FROM recordings WHERE id='v14-rec-3'`).Scan(&mergeStatus)
		require.NoError(t, err)
		require.Equal(t, model.MergeStatusFailed, mergeStatus)

		// Verify new columns have default values
		var mergePath, mergeError string
		err = db.db.QueryRowContext(ctx, `SELECT merge_path, merge_error FROM recordings WHERE id='v14-rec-1'`).Scan(&mergePath, &mergeError)
		require.NoError(t, err)
		require.Equal(t, "", mergePath, "merge_path should default to empty string")
		require.Equal(t, "", mergeError, "merge_error should default to empty string")
	})

	// Test 3: New insertions get default merge_path/merge_error
	t.Run("new_insertions_get_defaults", func(t *testing.T) {
		dir := t.TempDir()
		dbPath := filepath.Join(dir, "fresh2.db")
		db, err := New(dbPath)
		require.NoError(t, err)
		defer db.Close()

		err = db.Init(ctx)
		require.NoError(t, err)

		// Insert a camera first (for FK)
		_, err = db.db.ExecContext(ctx, `INSERT INTO cameras (id, name, protocol, url) VALUES ('cam2', 'Cam 2', 'rtsp', 'rtsp://host/cam2');`)
		require.NoError(t, err)

		// Insert a recording via InsertRecording (no merge_path/merge_error in INSERT)
		rec := &model.Recording{
			ID:          "new-rec-1",
			CameraID:    "cam2",
			FilePath:    "/path/new.mp4",
			Format:      model.FormatH264,
			StartedAt:   mustParseTime(t, "2026-06-01 12:00:00"),
			EndedAt:     mustParseTime(t, "2026-06-01 12:01:00"),
			Duration:    60.0,
			FileSize:    2048,
			FrameCount:  120,
			MergeStatus: model.MergeStatusPending,
		}
		err = db.InsertRecording(ctx, rec)
		require.NoError(t, err)

		// Verify merge_path and merge_error are empty for the new recording
		var mergePath, mergeError string
		err = db.db.QueryRowContext(ctx, `SELECT merge_path, merge_error FROM recordings WHERE id='new-rec-1'`).Scan(&mergePath, &mergeError)
		require.NoError(t, err)
		require.Equal(t, "", mergePath)
		require.Equal(t, "", mergeError)

		// Verify merge_status is preserved
		var mergeStatus string
		err = db.db.QueryRowContext(ctx, `SELECT merge_status FROM recordings WHERE id='new-rec-1'`).Scan(&mergeStatus)
		require.NoError(t, err)
		require.Equal(t, model.MergeStatusPending, mergeStatus)
	})

	// Test 4: Idempotent — running Init twice should be safe
	t.Run("idempotent", func(t *testing.T) {
		db, cleanup := helperV14DB(t, ctx)
		defer cleanup(t)

		require.NoError(t, db.Init(ctx))
		require.NoError(t, db.Init(ctx))

		// Verify schema version
		var version string
		err := db.db.QueryRowContext(ctx, "SELECT value FROM schema_meta WHERE key='schema_version'").Scan(&version)
		require.NoError(t, err)
	require.Equal(t, "21", version)

		// Data intact
		var count int
		err = db.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM recordings").Scan(&count)
		require.NoError(t, err)
		require.Equal(t, 3, count)
	})
}
