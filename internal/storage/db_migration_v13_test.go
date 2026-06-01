package storage

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/stretchr/testify/require"
)

// mustParseTime parses a time string for use in tests. Calls t.Helper().
func mustParseTime(t *testing.T, s string) time.Time {
	t.Helper()
	tt, err := parseTime(s)
	require.NoError(t, err)
	return tt
}

// helperV12DB creates a fresh DB with v12 schema (no merge_status column),
// inserts test recordings with known merged values, and returns the DB.
// The caller is responsible for closing the DB.
func helperV12DB(t *testing.T, ctx context.Context) (*DB, func(t *testing.T)) {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "v12.db")
	db, err := New(dbPath)
	require.NoError(t, err)

	// Create base tables manually (v12 schema — no merge_status column)
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
			merged INTEGER DEFAULT 0, archived INTEGER DEFAULT 0,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			FOREIGN KEY (camera_id) REFERENCES cameras(id)
		);
	`)
	require.NoError(t, err)
	_, err = db.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);`)
	require.NoError(t, err)

	// Set schema version to 12
	_, err = db.db.ExecContext(ctx, `INSERT OR IGNORE INTO schema_meta (key, value) VALUES ('schema_version', '12');`)
	require.NoError(t, err)

	// Insert a camera for FK constraint
	_, err = db.db.ExecContext(ctx, `INSERT INTO cameras (id, name, protocol, url) VALUES ('cam1', 'Test Cam', 'rtsp', 'rtsp://host/stream');`)
	require.NoError(t, err)

	// Insert recordings: 3 unmerged (merged=0) + 2 merged (merged=1)
	for _, rec := range []struct {
		id     string
		merged int
	}{
		{"v12-pending-1", 0},
		{"v12-pending-2", 0},
		{"v12-pending-3", 0},
		{"v12-merged-1", 1},
		{"v12-merged-2", 1},
	} {
		_, err = db.db.ExecContext(ctx,
			`INSERT INTO recordings (id, camera_id, file_path, format, started_at, ended_at, duration, file_size, frame_count, merged, archived) VALUES (?,?,?,?,?,?,?,?,?,?,0);`,
			rec.id, "cam1", "/path/"+rec.id+".mp4", "h264", "2026-06-01 10:00:00", "2026-06-01 10:01:00", 60.0, 1024, 60, rec.merged,
		)
		require.NoError(t, err)
	}

	return db, func(t *testing.T) {
		t.Helper()
		db.Close()
	}
}

func TestMigrationV13_AddMergeStatusColumn(t *testing.T) {
	ctx := context.Background()
	db, cleanup := helperV12DB(t, ctx)
	defer cleanup(t)

	// Verify merge_status column does NOT exist before migration
	var colExists int
	_ = db.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('recordings') WHERE name='merge_status'`).Scan(&colExists)
	require.Equal(t, 0, colExists, "merge_status column should NOT exist before migration")

	// Run Init — should apply v12→v13 migration
	err := db.Init(ctx)
	require.NoError(t, err)

	// Verify merge_status column now exists
	_ = db.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('recordings') WHERE name='merge_status'`).Scan(&colExists)
	require.Equal(t, 1, colExists, "merge_status column should exist after migration")

	// Verify schema version is now 13
	var version string
	err = db.db.QueryRowContext(ctx, "SELECT value FROM schema_meta WHERE key='schema_version'").Scan(&version)
	require.NoError(t, err)
	require.Equal(t, "14", version)
}

func TestMigrationV13_BackfillFromMerged(t *testing.T) {
	ctx := context.Background()
	db, cleanup := helperV12DB(t, ctx)
	defer cleanup(t)

	// Run migration
	err := db.Init(ctx)
	require.NoError(t, err)

	// Verify backfill: merged=0 → merge_status='pending', merged=1 → merge_status='merged'
	type row struct {
		id          string
		merged      int
		mergeStatus string
	}
	rows := []row{}
	rs, err := db.db.QueryContext(ctx, `SELECT id, merged, merge_status FROM recordings ORDER BY id`)
	require.NoError(t, err)
	defer rs.Close()
	for rs.Next() {
		var r row
		require.NoError(t, rs.Scan(&r.id, &r.merged, &r.mergeStatus))
		rows = append(rows, r)
	}
	// Build a map for lookup by id
	byID := map[string]row{}
	for _, r := range rows {
		byID[r.id] = r
	}

	// Unmerged recordings should have merge_status='pending'
	for _, id := range []string{"v12-pending-1", "v12-pending-2", "v12-pending-3"} {
		r, ok := byID[id]
		require.True(t, ok, "expected row %s to exist", id)
		require.Equal(t, 0, r.merged, "row %s should have merged=0", id)
		require.Equal(t, model.MergeStatusPending, r.mergeStatus, "row %s should have merge_status='pending'", id)
	}

	// Merged recordings should have merge_status='merged'
	for _, id := range []string{"v12-merged-1", "v12-merged-2"} {
		r, ok := byID[id]
		require.True(t, ok, "expected row %s to exist", id)
		require.Equal(t, 1, r.merged, "row %s should have merged=1", id)
		require.Equal(t, model.MergeStatusMerged, r.mergeStatus, "row %s should have merge_status='merged'", id)
	}
}

func TestMigrationV13_NewInsertionsGetPending(t *testing.T) {
	ctx := context.Background()
	db, cleanup := helperV12DB(t, ctx)
	defer cleanup(t)

	// Run migration
	err := db.Init(ctx)
	require.NoError(t, err)

	// Insert a new recording via InsertRecording
	rec := &model.Recording{
		ID:         "new-pending-1",
		CameraID:   "cam1",
		FilePath:   "/path/new.mp4",
		Format:     model.FormatH264,
		StartedAt:  mustParseTime(t, "2026-06-01 12:00:00"),
		EndedAt:    mustParseTime(t, "2026-06-01 12:01:00"),
		Duration:   60.0,
		FileSize:   2048,
		FrameCount: 120,
	}
	err = db.InsertRecording(ctx, rec)
	require.NoError(t, err)

	// Verify merge_status='pending' for new recording
	var mergeStatus string
	err = db.db.QueryRowContext(ctx, `SELECT merge_status FROM recordings WHERE id='new-pending-1'`).Scan(&mergeStatus)
	require.NoError(t, err)
	require.Equal(t, model.MergeStatusPending, mergeStatus)

	// Verify the Recording model also gets MergeStatus populated
	got, err := db.GetRecording(ctx, "new-pending-1")
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, model.MergeStatusPending, got.MergeStatus)
	require.False(t, got.Merged)
}

func TestMigrationV13_MergeStatusInQueries(t *testing.T) {
	ctx := context.Background()
	db, cleanup := helperV12DB(t, ctx)
	defer cleanup(t)

	// Run migration
	err := db.Init(ctx)
	require.NoError(t, err)

	// Test ListMergeableSegments — should only return merge_status='pending' rows
	segments, err := db.ListMergeableSegments(ctx, "cam1",
		mustParseTime(t, "2026-01-01 00:00:00"),
		mustParseTime(t, "2030-01-01 00:00:00"))
	require.NoError(t, err)
	require.Len(t, segments, 3, "should return 3 unmerged (pending) segments")
	for _, seg := range segments {
		require.Equal(t, model.MergeStatusPending, seg.MergeStatus)
		require.False(t, seg.Merged)
	}

	// Test ListRecordings with Merged filter
	merged := true
	list, err := db.ListRecordings(ctx, model.RecordingFilter{Merged: &merged})
	require.NoError(t, err)
	require.Len(t, list, 2, "should return 2 merged recordings")
	for _, r := range list {
		require.Equal(t, model.MergeStatusMerged, r.MergeStatus)
		require.True(t, r.Merged)
	}

	pending := false
	list2, err := db.ListRecordings(ctx, model.RecordingFilter{Merged: &pending})
	require.NoError(t, err)
	require.Len(t, list2, 3, "should return 3 pending recordings")
	for _, r := range list2 {
		require.Equal(t, model.MergeStatusPending, r.MergeStatus)
		require.False(t, r.Merged)
	}
}

func TestMigrationV13_MergeAndReplaceUsesStatus(t *testing.T) {
	ctx := context.Background()
	db, cleanup := helperV12DB(t, ctx)
	defer cleanup(t)

	// Run migration
	err := db.Init(ctx)
	require.NoError(t, err)

	// Merge some recordings
	merged := &model.Recording{
		ID:        "merged-v13",
		CameraID:  "cam1",
		FilePath:  "/path/merged-v13.mp4",
		Format:    model.FormatH264,
		StartedAt: mustParseTime(t, "2026-06-01 10:00:00"),
		EndedAt:   mustParseTime(t, "2026-06-01 10:03:00"),
		Duration:  180.0,
		FileSize:  3072,
		FrameCount: 180,
		Merged:    true,
	}
	oldIDs := []string{"v12-pending-1", "v12-pending-2"}
	err = db.MergeAndReplaceRecordings(ctx, merged, oldIDs)
	require.NoError(t, err)

	// Verify merged recording has merge_status='merged'
	var mergeStatus string
	err = db.db.QueryRowContext(ctx, `SELECT merge_status FROM recordings WHERE id='merged-v13'`).Scan(&mergeStatus)
	require.NoError(t, err)
	require.Equal(t, model.MergeStatusMerged, mergeStatus)

	// Old recordings should be deleted
	for _, id := range oldIDs {
		got, err := db.GetRecording(ctx, id)
		require.NoError(t, err)
		require.Nil(t, got, "old recording %s should be deleted", id)
	}
}

func TestMigrationV13_Idempotent(t *testing.T) {
	ctx := context.Background()
	db, cleanup := helperV12DB(t, ctx)
	defer cleanup(t)

	// Run Init twice — migration should be idempotent
	require.NoError(t, db.Init(ctx))
	require.NoError(t, db.Init(ctx))

	// Verify schema version
	var version string
	err := db.db.QueryRowContext(ctx, "SELECT value FROM schema_meta WHERE key='schema_version'").Scan(&version)
	require.NoError(t, err)
	require.Equal(t, "14", version)

	// Data should be intact
	var count int
	err = db.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM recordings").Scan(&count)
	require.NoError(t, err)
	require.Equal(t, 5, count)
}
