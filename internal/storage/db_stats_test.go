package storage

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
)

// TestGetRecordingTrends_Timezone verifies that timezone-aware GROUP BY works correctly.
// This tests the SQL GROUP BY rewrite that aggregates in SQL instead of loading all rows.
func TestGetRecordingTrends_Timezone(t *testing.T) {
	t.Helper()

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	db, err := New(dbPath)
	require.NoError(t, err)

	ctx := context.Background()
	err = db.Init(ctx)
	require.NoError(t, err)
	defer db.Close()

	// Insert test cameras
	_, err = db.db.ExecContext(ctx,
		"INSERT INTO cameras (id, name, protocol, url) VALUES (?, ?, ?, ?)",
		"cam1", "Front Door", "rtsp", "rtsp://192.168.1.10/stream")
	require.NoError(t, err)

	_, err = db.db.ExecContext(ctx,
		"INSERT INTO cameras (id, name, protocol, url) VALUES (?, ?, ?, ?)",
		"cam2", "Back Yard", "rtsp", "rtsp://192.168.1.11/stream")
	require.NoError(t, err)

	now := time.Now().UTC()
	baseTime := now.Truncate(24 * time.Hour).Add(12 * time.Hour) // Noon UTC

	testCases := []struct {
		name     string
		loc      *time.Location
		offset   int // expected offset in seconds
		recTimes []time.Time
		wantDate string // expected date string in the timezone
	}{
		{
			name:     "UTC - offset 0",
			loc:      time.UTC,
			offset:   0,
			recTimes: []time.Time{baseTime, baseTime.Add(2 * time.Hour)},
			wantDate: baseTime.Format("2006-01-02"),
		},
		{
			name:     "UTC+8 - offset 28800",
			loc:      time.FixedZone("UTC+8", 8*3600),
			offset:   28800,
			recTimes: []time.Time{baseTime, baseTime.Add(2 * time.Hour)},
			wantDate: baseTime.Add(8 * time.Hour).Format("2006-01-02"),
		},
		{
			name:     "UTC-5 - offset -18000",
			loc:      time.FixedZone("UTC-5", -5*3600),
			offset:   -18000,
			recTimes: []time.Time{baseTime, baseTime.Add(2 * time.Hour)},
			wantDate: baseTime.Add(-5 * time.Hour).Format("2006-01-02"),
		},
		{
			name:     "UTC midnight with negative offset - belongs to previous day",
			loc:      time.FixedZone("UTC-5", -5*3600),
			offset:   -18000,
			recTimes: []time.Time{baseTime.Truncate(24 * time.Hour)}, // Midnight UTC
			wantDate: baseTime.Truncate(24 * time.Hour).Add(-5 * time.Hour).Format("2006-01-02"),
		},
	}

	for i, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Helper()

			// Clear recordings table
			_, err := db.db.ExecContext(ctx, "DELETE FROM recordings")
			require.NoError(t, err)
			db.InvalidateStatsCache() // raw SQL bypasses the cache; clear it so the next query re-fetches

			// Insert test recordings
			for i, recTime := range tc.recTimes {
				_, err := db.db.ExecContext(ctx,
					`INSERT INTO recordings (id, camera_id, file_path, format, started_at, ended_at, duration, file_size, frame_count)
					 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
					"rec-"+tc.name+"-"+string(rune('0'+i)), "cam1", "/test.mp4", "h264",
					formatTime(recTime), formatTime(recTime.Add(60*time.Second)), 60.0, int64(1024*(i+1)), 60)
				require.NoError(t, err)
			}

			// Get trends — use a distinct days value per case so the cache key
			// (days:tzOffset) doesn't collide between cases with the same timezone.
			queryDays := 7 + i
			trends, err := db.GetRecordingTrends(ctx, queryDays, tc.loc)
			require.NoError(t, err)
			require.NotEmpty(t, trends)

			// Verify we got the expected date
			found := false
			for _, trend := range trends {
				if trend.Date == tc.wantDate {
					found = true
					// Verify aggregation
					require.Equal(t, len(tc.recTimes), trend.Recordings, "recordings count mismatch")
					require.Equal(t, int64(1024*(1+len(tc.recTimes))*len(tc.recTimes)/2), trend.TotalSize, "total size mismatch")
					require.Contains(t, trend.CameraCounts, "Front Door")
					break
				}
			}
			require.True(t, found, "expected date %s not found in trends, got %+v", tc.wantDate, trends)
		})
	}
}

// TestGetRecordingTrends_MultipleCameras verifies aggregation across multiple cameras
// with different timezones.
func TestGetRecordingTrends_MultipleCameras(t *testing.T) {
	t.Helper()

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	db, err := New(dbPath)
	require.NoError(t, err)

	ctx := context.Background()
	err = db.Init(ctx)
	require.NoError(t, err)
	defer db.Close()

	// Insert test cameras
	_, err = db.db.ExecContext(ctx,
		"INSERT INTO cameras (id, name, protocol, url) VALUES (?, ?, ?, ?)",
		"cam1", "Front Door", "rtsp", "rtsp://192.168.1.10/stream")
	require.NoError(t, err)

	_, err = db.db.ExecContext(ctx,
		"INSERT INTO cameras (id, name, protocol, url) VALUES (?, ?, ?, ?)",
		"cam2", "Back Yard", "rtsp", "rtsp://192.168.1.11/stream")
	require.NoError(t, err)

	now := time.Now().UTC()
	baseTime := now.Truncate(24 * time.Hour).Add(12 * time.Hour)

	// Insert recordings for both cameras on the same day
	for i := range 3 {
		recTime := baseTime.Add(time.Duration(i) * time.Hour)
		_, err := db.db.ExecContext(ctx,
			`INSERT INTO recordings (id, camera_id, file_path, format, started_at, ended_at, duration, file_size, frame_count)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			"rec-cam1-"+string(rune('0'+i)), "cam1", "/test.mp4", "h264",
			formatTime(recTime), formatTime(recTime.Add(60*time.Second)), 60.0, int64(1024*(i+1)), 60)
		require.NoError(t, err)
	}

	for i := range 2 {
		recTime := baseTime.Add(time.Duration(i) * time.Hour)
		_, err := db.db.ExecContext(ctx,
			`INSERT INTO recordings (id, camera_id, file_path, format, started_at, ended_at, duration, file_size, frame_count)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			"rec-cam2-"+string(rune('0'+i)), "cam2", "/test.mp4", "h264",
			formatTime(recTime), formatTime(recTime.Add(60*time.Second)), 60.0, int64(2048*(i+1)), 60)
		require.NoError(t, err)
	}

	loc := time.UTC
	trends, err := db.GetRecordingTrends(ctx, 7, loc)
	require.NoError(t, err)
	require.NotEmpty(t, trends)

	// Find the trend for today
	today := baseTime.Format("2006-01-02")
	var todayTrend *model.DailyStats
	for i := range trends {
		if trends[i].Date == today {
			todayTrend = &trends[i]
			break
		}
	}
	require.NotNil(t, todayTrend)

	// Verify total counts
	require.Equal(t, 5, todayTrend.Recordings)           // 3 + 2 recordings
	require.Equal(t, int64(12288), todayTrend.TotalSize) // cam1: 1024+2048+3072=6144, cam2: 2048+4096=6144
	require.Len(t, todayTrend.CameraCounts, 2)
	require.Equal(t, 3, todayTrend.CameraCounts["Front Door"])
	require.Equal(t, 2, todayTrend.CameraCounts["Back Yard"])
}

// TestGetRecordingTrends_EmptyResult verifies behavior with no recordings.
func TestGetRecordingTrends_EmptyResult(t *testing.T) {
	t.Helper()

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	db, err := New(dbPath)
	require.NoError(t, err)

	ctx := context.Background()
	err = db.Init(ctx)
	require.NoError(t, err)
	defer db.Close()

	trends, err := db.GetRecordingTrends(ctx, 7, time.UTC)
	require.NoError(t, err)
	require.NotNil(t, trends)
	require.Empty(t, trends)
}

// TestGetRecordingTrends_DaysClamping verifies the days parameter is clamped to [1, 30].
func TestGetRecordingTrends_DaysClamping(t *testing.T) {
	t.Helper()

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	db, err := New(dbPath)
	require.NoError(t, err)

	ctx := context.Background()
	err = db.Init(ctx)
	require.NoError(t, err)
	defer db.Close()

	_, err = db.db.ExecContext(ctx,
		"INSERT INTO cameras (id, name, protocol, url) VALUES (?, ?, ?, ?)",
		"cam1", "Front Door", "rtsp", "rtsp://192.168.1.10/stream")
	require.NoError(t, err)

	now := time.Now().UTC()
	// Insert a recording 100 days ago
	oldTime := now.AddDate(0, 0, -100).Truncate(24 * time.Hour).Add(12 * time.Hour)
	_, err = db.db.ExecContext(ctx,
		`INSERT INTO recordings (id, camera_id, file_path, format, started_at, ended_at, duration, file_size, frame_count)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"rec-old", "cam1", "/test.mp4", "h264",
		formatTime(oldTime), formatTime(oldTime.Add(60*time.Second)), 60.0, int64(1024), 60)
	require.NoError(t, err)

	// With days=0, should default to 7 (so no recordings returned)
	trends, err := db.GetRecordingTrends(ctx, 0, time.UTC)
	require.NoError(t, err)
	require.Empty(t, trends)

	// With days=30, should still not find the 100-day-old recording
	trends, err = db.GetRecordingTrends(ctx, 30, time.UTC)
	require.NoError(t, err)
	require.Empty(t, trends)

	// With days=100, should clamp to 30 and still not find it
	trends, err = db.GetRecordingTrends(ctx, 100, time.UTC)
	require.NoError(t, err)
	require.Empty(t, trends)

	// Insert a recent recording (within 30 days)
	recentTime := now.Add(-12 * time.Hour)
	_, err = db.db.ExecContext(ctx,
		`INSERT INTO recordings (id, camera_id, file_path, format, started_at, ended_at, duration, file_size, frame_count)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"rec-recent", "cam1", "/test.mp4", "h264",
		formatTime(recentTime), formatTime(recentTime.Add(60*time.Second)), 60.0, int64(1024), 60)
	require.NoError(t, err)

	// With days=1, should find the recent recording
	trends, err = db.GetRecordingTrends(ctx, 1, time.UTC)
	require.NoError(t, err)
	require.Len(t, trends, 1)
}

// TestGetRecordingTrends_NilLocation defaults to UTC.
func TestGetRecordingTrends_NilLocation(t *testing.T) {
	t.Helper()

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	db, err := New(dbPath)
	require.NoError(t, err)

	ctx := context.Background()
	err = db.Init(ctx)
	require.NoError(t, err)
	defer db.Close()

	_, err = db.db.ExecContext(ctx,
		"INSERT INTO cameras (id, name, protocol, url) VALUES (?, ?, ?, ?)",
		"cam1", "Front Door", "rtsp", "rtsp://192.168.1.10/stream")
	require.NoError(t, err)

	now := time.Now().UTC()
	baseTime := now.Truncate(24 * time.Hour).Add(12 * time.Hour)
	_, err = db.db.ExecContext(ctx,
		`INSERT INTO recordings (id, camera_id, file_path, format, started_at, ended_at, duration, file_size, frame_count)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"rec1", "cam1", "/test.mp4", "h264",
		formatTime(baseTime), formatTime(baseTime.Add(60*time.Second)), 60.0, int64(1024), 60)
	require.NoError(t, err)

	// With nil location, should default to UTC
	trends, err := db.GetRecordingTrends(ctx, 7, nil)
	require.NoError(t, err)
	require.NotEmpty(t, trends)

	// Compare with explicit UTC
	trendsUTC, err := db.GetRecordingTrends(ctx, 7, time.UTC)
	require.NoError(t, err)
	require.Equal(t, trends, trendsUTC)
}

// TestGetCameraStorageStats verifies per-camera aggregation, archived flag
// propagation, name fallback for cameras without a cameras row, and descending
// size ordering.
func TestGetCameraStorageStats(t *testing.T) {
	t.Helper()

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	db, err := New(dbPath)
	require.NoError(t, err)

	ctx := context.Background()
	err = db.Init(ctx)
	require.NoError(t, err)
	defer db.Close()

	_, err = db.db.ExecContext(ctx,
		"INSERT INTO cameras (id, name, protocol, url) VALUES (?, ?, ?, ?)",
		"cam1", "Front Door", "rtsp", "rtsp://192.168.1.10/stream")
	require.NoError(t, err)

	_, err = db.db.ExecContext(ctx,
		"INSERT INTO cameras (id, name, protocol, url, archived) VALUES (?, ?, ?, ?, 1)",
		"cam2", "Back Yard", "rtsp", "rtsp://192.168.1.11/stream")
	require.NoError(t, err)
	// Note: "cam3" has recordings but NO cameras row — name falls back to the id.

	now := time.Now().UTC()
	insertRec := func(id, camID string, size int64) {
		t.Helper()
		_, err := db.db.ExecContext(ctx,
			`INSERT INTO recordings (id, camera_id, file_path, format, started_at, ended_at, duration, file_size, frame_count)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, camID, "/test.mp4", "h264",
			formatTime(now), formatTime(now.Add(60*time.Second)), 60.0, size, 60)
		require.NoError(t, err)
	}
	insertRec("rec-1", "cam1", 1024)
	insertRec("rec-2", "cam1", 2048)
	insertRec("rec-3", "cam2", 8192)
	insertRec("rec-4", "cam3", 512)

	stats, err := db.GetCameraStorageStats(ctx)
	require.NoError(t, err)
	require.Len(t, stats, 3)

	// Largest consumer first.
	require.Equal(t, "cam2", stats[0].CameraID)
	require.Equal(t, "Back Yard", stats[0].CameraName)
	require.True(t, stats[0].Archived)
	require.Equal(t, 1, stats[0].Recordings)
	require.Equal(t, int64(8192), stats[0].TotalBytes)

	require.Equal(t, "cam1", stats[1].CameraID)
	require.Equal(t, "Front Door", stats[1].CameraName)
	require.False(t, stats[1].Archived)
	require.Equal(t, 2, stats[1].Recordings)
	require.Equal(t, int64(3072), stats[1].TotalBytes)

	// No cameras row → id as name, not archived.
	require.Equal(t, "cam3", stats[2].CameraID)
	require.Equal(t, "cam3", stats[2].CameraName)
	require.False(t, stats[2].Archived)
}

// TestGetCameraStorageStats_Cache verifies the TTL cache: inserts after the
// first call are invisible until InvalidateStatsCache.
func TestGetCameraStorageStats_Cache(t *testing.T) {
	t.Helper()

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	db, err := New(dbPath)
	require.NoError(t, err)

	ctx := context.Background()
	err = db.Init(ctx)
	require.NoError(t, err)
	defer db.Close()

	_, err = db.db.ExecContext(ctx,
		`INSERT INTO recordings (id, camera_id, file_path, format, started_at, ended_at, duration, file_size, frame_count)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"rec-1", "cam1", "/test.mp4", "h264",
		formatTime(time.Now()), formatTime(time.Now().Add(60*time.Second)), 60.0, 1024, 60)
	require.NoError(t, err)

	stats, err := db.GetCameraStorageStats(ctx)
	require.NoError(t, err)
	require.Len(t, stats, 1)

	_, err = db.db.ExecContext(ctx,
		`INSERT INTO recordings (id, camera_id, file_path, format, started_at, ended_at, duration, file_size, frame_count)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"rec-2", "cam2", "/test.mp4", "h264",
		formatTime(time.Now()), formatTime(time.Now().Add(60*time.Second)), 60.0, 4096, 60)
	require.NoError(t, err)

	// Cached — the new camera is invisible within the TTL.
	stats, err = db.GetCameraStorageStats(ctx)
	require.NoError(t, err)
	require.Len(t, stats, 1, "cached result should not reflect insert within TTL")

	db.InvalidateStatsCache()
	stats, err = db.GetCameraStorageStats(ctx)
	require.NoError(t, err)
	require.Len(t, stats, 2)
	require.Equal(t, "cam2", stats[0].CameraID, "largest consumer first after invalidation")
}

// TestGetRecordingTrends_CrossDate verifies recordings spanning multiple dates
// are correctly grouped.
func TestGetRecordingTrends_CrossDate(t *testing.T) {
	t.Helper()

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	db, err := New(dbPath)
	require.NoError(t, err)

	ctx := context.Background()
	err = db.Init(ctx)
	require.NoError(t, err)
	defer db.Close()

	_, err = db.db.ExecContext(ctx,
		"INSERT INTO cameras (id, name, protocol, url) VALUES (?, ?, ?, ?)",
		"cam1", "Front Door", "rtsp", "rtsp://192.168.1.10/stream")
	require.NoError(t, err)

	now := time.Now().UTC()
	midnight := now.Truncate(24 * time.Hour)

	// Insert 3 recordings: yesterday, today, tomorrow
	yesterday := midnight.Add(-12 * time.Hour)
	today := midnight.Add(12 * time.Hour)
	tomorrow := midnight.Add(36 * time.Hour)

	for i, recTime := range []time.Time{yesterday, today, tomorrow} {
		_, err := db.db.ExecContext(ctx,
			`INSERT INTO recordings (id, camera_id, file_path, format, started_at, ended_at, duration, file_size, frame_count)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			"rec-"+string(rune('0'+i)), "cam1", "/test.mp4", "h264",
			formatTime(recTime), formatTime(recTime.Add(60*time.Second)), 60.0, int64(1024), 60)
		require.NoError(t, err)
	}

	loc := time.UTC
	trends, err := db.GetRecordingTrends(ctx, 3, loc)
	require.NoError(t, err)
	require.Len(t, trends, 3)

	// Verify each date has exactly 1 recording
	for _, trend := range trends {
		require.Equal(t, 1, trend.Recordings)
		require.Equal(t, int64(1024), trend.TotalSize)
		require.Equal(t, 1, trend.CameraCounts["Front Door"])
	}
}

// TestCountRecordings_Cache verifies the 15s TTL cache: a second call within
// the TTL returns the cached value without hitting the DB (the count reflects
// the first call, not a concurrent insert).
func TestCountRecordings_Cache(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	db, err := New(dbPath)
	require.NoError(t, err)
	ctx := context.Background()
	err = db.Init(ctx)
	require.NoError(t, err)
	defer db.Close()

	// Insert one recording.
	_, err = db.db.ExecContext(ctx,
		`INSERT INTO recordings (id, camera_id, file_path, format, started_at, ended_at, duration, file_size, frame_count)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"rec-cache-1", "cam1", "/test.mp4", "h264",
		formatTime(time.Now()), formatTime(time.Now().Add(60*time.Second)), 60.0, 1024, 60)
	require.NoError(t, err)

	// First call — populates cache.
	count1, err := db.CountRecordings(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, count1)

	// Insert another recording — the cache should NOT reflect it yet (TTL not expired).
	_, err = db.db.ExecContext(ctx,
		`INSERT INTO recordings (id, camera_id, file_path, format, started_at, ended_at, duration, file_size, frame_count)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"rec-cache-2", "cam1", "/test.mp4", "h264",
		formatTime(time.Now()), formatTime(time.Now().Add(60*time.Second)), 60.0, 1024, 60)
	require.NoError(t, err)

	// Second call — should return cached value (1), not the actual DB count (2).
	count2, err := db.CountRecordings(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, count2, "cached count should not reflect insert within TTL")
}
