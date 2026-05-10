package storage

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/stretchr/testify/require"
)

func TestNewDB(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	db, err := New(dbPath)
	require.NoError(t, err)
	require.NotNil(t, db)

	ctx := context.Background()
	err = db.Init(ctx)
	require.NoError(t, err)

	err = db.Close()
	require.NoError(t, err)
}

func TestInitCreatesTables_Idempotent(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test2.db")
	db, err := New(dbPath)
	require.NoError(t, err)

	ctx := context.Background()
	require.NoError(t, db.Init(ctx))
	require.NoError(t, db.Init(ctx))

	require.NoError(t, db.Close())
}

func TestInsertAndGetRecording(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test3.db")
	db, _ := New(dbPath)
	ctx := context.Background()
	_ = db.Init(ctx)

	started := time.Now()
	rec := &model.Recording{
		ID:         "rec-001",
		CameraID:   "cam1",
		FilePath:   "/path/file.mp4",
		Format:     model.FormatH264,
		StartedAt:  started,
		EndedAt:    started.Add(time.Minute),
		Duration:   60.0,
		FileSize:   1024,
		FrameCount: 60,
		Pinned:     false,
	}
	err := db.InsertRecording(ctx, rec)
	require.NoError(t, err)

	got, err := db.GetRecording(ctx, rec.ID)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, "cam1", got.CameraID)
	require.Equal(t, "/path/file.mp4", got.FilePath)
	require.Equal(t, model.FormatH264, got.Format)
	require.Equal(t, int64(1024), got.FileSize)
}

func TestGetRecordingNotFound(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test4.db")
	db, _ := New(dbPath)
	ctx := context.Background()
	_ = db.Init(ctx)

	got, err := db.GetRecording(ctx, "nonexistent")
	require.NoError(t, err)
	require.Nil(t, got)
}

func TestListRecordingsWithFilter(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test5.db")
	db, _ := New(dbPath)
	ctx := context.Background()
	_ = db.Init(ctx)

	r1 := &model.Recording{ID: "r1", CameraID: "camA", FilePath: "/a1.mp4", Format: model.FormatH264, StartedAt: time.Now()}
	_ = db.InsertRecording(ctx, r1)
	r2 := &model.Recording{ID: "r2", CameraID: "camB", FilePath: "/b1.mp4", Format: model.FormatH264, StartedAt: time.Now()}
	_ = db.InsertRecording(ctx, r2)

	list, err := db.ListRecordings(ctx, model.RecordingFilter{CameraID: "camA"})
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.Equal(t, "r1", list[0].ID)

	pinned := true
	list2, err := db.ListRecordings(ctx, model.RecordingFilter{Pinned: &pinned})
	require.NoError(t, err)
	require.Len(t, list2, 0)
}

func TestDeleteRecording(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test6.db")
	db, _ := New(dbPath)
	ctx := context.Background()
	_ = db.Init(ctx)

	rec := &model.Recording{ID: "del-1", CameraID: "camX", FilePath: "/del.mp4", Format: model.FormatH264, StartedAt: time.Now()}
	_ = db.InsertRecording(ctx, rec)

	err := db.DeleteRecording(ctx, rec.ID)
	require.NoError(t, err)

	got, err := db.GetRecording(ctx, rec.ID)
	require.NoError(t, err)
	require.Nil(t, got)
}

func TestSetPinned(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test7.db")
	db, _ := New(dbPath)
	ctx := context.Background()
	_ = db.Init(ctx)

	rec := &model.Recording{ID: "pin-1", CameraID: "camP", FilePath: "/p.mp4", Format: model.FormatH264, StartedAt: time.Now()}
	_ = db.InsertRecording(ctx, rec)

	err := db.SetPinned(ctx, rec.ID, true)
	require.NoError(t, err)

	got, err := db.GetRecording(ctx, rec.ID)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.True(t, got.Pinned)

	err = db.SetPinned(ctx, rec.ID, false)
	require.NoError(t, err)
	got2, err := db.GetRecording(ctx, rec.ID)
	require.NoError(t, err)
	require.False(t, got2.Pinned)
}

func TestCleanupIncomplete(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test8.db")
	db, _ := New(dbPath)
	ctx := context.Background()
	_ = db.Init(ctx)

	// Insert directly with NULL ended_at to test cleanup (InsertRecording serializes zero time as 0001-01-01, not NULL)
	_, err := db.db.ExecContext(ctx,
		`INSERT INTO recordings(id, camera_id, file_path, format, started_at, ended_at, duration, file_size, frame_count, pinned) VALUES(?,?,?,?,NULL,?,?,?,?);`,
		"inc-1", "camC", "/c.mp4", model.FormatH264, time.Now(), 0, 0, 0, false,
	)
	err = db.CleanupIncomplete(ctx)
	require.NoError(t, err)

	got, err := db.GetRecording(ctx, "inc-1")
	require.NoError(t, err)
	require.Nil(t, got)
}

func TestCloseAndReopen(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test9.db")
	db, _ := New(dbPath)
	ctx := context.Background()
	_ = db.Init(ctx)

	rec := &model.Recording{ID: "pers-1", CameraID: "camZ", FilePath: "/z.mp4", Format: model.FormatH264, StartedAt: time.Now()}
	_ = db.InsertRecording(ctx, rec)
	require.NoError(t, db.Close())

	db2, err := New(dbPath)
	require.NoError(t, err)
	_ = db2.Init(ctx)

	got, err := db2.GetRecording(ctx, rec.ID)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, "camZ", got.CameraID)
	require.NoError(t, db2.Close())
}


func TestUpsertCamera(t *testing.T) {

	dir := t.TempDir()

	dbPath := filepath.Join(dir, "test10.db")

	db, _ := New(dbPath)

	ctx := context.Background()

	_ = db.Init(ctx)



	// Test insert new camera

	err := db.UpsertCamera(ctx, "cam1", "Camera 1", "rtsp_h264", "rtsp://localhost:554/stream", "user", "pass", true)

	require.NoError(t, err)



	// Verify camera was inserted

	cameras, err := db.ListCameras(ctx)

	require.NoError(t, err)

	require.Len(t, cameras, 1)

	require.Equal(t, "cam1", cameras[0].ID)

	require.Equal(t, "Camera 1", cameras[0].Name)

	require.Equal(t, "rtsp_h264", cameras[0].Protocol)

	require.Equal(t, "rtsp://localhost:554/stream", cameras[0].URL)

	require.True(t, cameras[0].Enabled)



	// Test update existing camera

	err = db.UpsertCamera(ctx, "cam1", "Updated Camera 1", "rtsp_mjpeg", "rtsp://localhost:555/stream", "newuser", "newpass", false)

	require.NoError(t, err)



	// Verify camera was updated

	cameras2, err := db.ListCameras(ctx)

	require.NoError(t, err)

	require.Len(t, cameras2, 1)

	require.Equal(t, "cam1", cameras2[0].ID)

	require.Equal(t, "Updated Camera 1", cameras2[0].Name)

	require.Equal(t, "rtsp_mjpeg", cameras2[0].Protocol)

	require.Equal(t, "rtsp://localhost:555/stream", cameras2[0].URL)

	require.False(t, cameras2[0].Enabled)



	require.NoError(t, db.Close())

}

func TestGetCamera(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test_getcam.db")
	db, _ := New(dbPath)
	ctx := context.Background()
	_ = db.Init(ctx)
	defer db.Close()

	// Insert a camera first
	err := db.UpsertCamera(ctx, "cam1", "Camera 1", "rtsp_h264", "rtsp://localhost:554/stream", "user", "pass", true)
	require.NoError(t, err)

	// Get the camera
	cam, err := db.GetCamera(ctx, "cam1")
	require.NoError(t, err)
	require.NotNil(t, cam)
	require.Equal(t, "cam1", cam.ID)
	require.Equal(t, "Camera 1", cam.Name)
	require.Equal(t, "rtsp_h264", cam.Protocol)
	require.Equal(t, "rtsp://localhost:554/stream", cam.URL)
	require.True(t, cam.Enabled)
}

func TestGetCamera_NotFound(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test_getcam_nf.db")
	db, _ := New(dbPath)
	ctx := context.Background()
	_ = db.Init(ctx)
	defer db.Close()

	cam, err := db.GetCamera(ctx, "nonexistent")
	require.NoError(t, err)
	require.Nil(t, cam)
}

func TestTimestampRoundTrip(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test_rt.db")
	db, _ := New(dbPath)
	ctx := context.Background()
	_ = db.Init(ctx)
	defer db.Close()

	started := time.Date(2026, 4, 30, 15, 30, 0, 123456789, time.UTC)
	ended := started.Add(time.Minute)
	rec := &model.Recording{
		ID:         "rt-1",
		CameraID:   "camRT",
		FilePath:   "/rt.mp4",
		Format:     model.FormatH264,
		StartedAt:  started,
		EndedAt:    ended,
		Duration:   60.0,
		FileSize:   1024,
		FrameCount: 60,
		Pinned:     false,
	}
	err := db.InsertRecording(ctx, rec)
	require.NoError(t, err)

	got, err := db.GetRecording(ctx, rec.ID)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.True(t, got.StartedAt.Equal(started), "StartedAt mismatch: got %v, want %v", got.StartedAt, started)
	require.True(t, got.EndedAt.Equal(ended), "EndedAt mismatch: got %v, want %v", got.EndedAt, ended)
}

func TestTimestampStoredInSQLiteFormat(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test_fmt.db")
	db, _ := New(dbPath)
	ctx := context.Background()
	_ = db.Init(ctx)
	defer db.Close()

	// Verify timeToDB produces SQLite-compatible format
	ts := time.Date(2026, 4, 30, 15, 30, 0, 123456789, time.UTC)
	require.Equal(t, "2026-04-30 15:30:00.123456789", formatTime(ts))
	require.Equal(t, "", formatTime(time.Time{}))

	// Verify round-trip: insert and get back
	rec := &model.Recording{
		ID:        "fmt-1",
		CameraID:  "camFmt",
		FilePath:  "/fmt.mp4",
		Format:    model.FormatH264,
		StartedAt: ts,
		EndedAt:   ts.Add(time.Minute),
	}
	require.NoError(t, db.InsertRecording(ctx, rec))
	got, err := db.GetRecording(ctx, "fmt-1")
	require.NoError(t, err)
	require.True(t, got.StartedAt.Equal(ts))
	require.True(t, got.EndedAt.Equal(ts.Add(time.Minute)))
}

func TestListExpiredRecordingsSameDayEdgeCase(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test_sameday.db")
	db, _ := New(dbPath)
	ctx := context.Background()
	_ = db.Init(ctx)
	defer db.Close()

	now := time.Now().UTC()
	// Recording ended 31 days ago (just past the 30-day cutoff)
	oldEnded := now.Add(-31 * 24 * time.Hour)
	oldRec := &model.Recording{
		ID:        "edge-old",
		CameraID:  "cam1",
		FilePath:  "/old.mp4",
		Format:    model.FormatH264,
		StartedAt: oldEnded.Add(-time.Hour),
		EndedAt:   oldEnded,
	}
	require.NoError(t, db.InsertRecording(ctx, oldRec))

	// Recording ended 29 days ago (just inside the 30-day cutoff)
	recentEnded := now.Add(-29 * 24 * time.Hour)
	recentRec := &model.Recording{
		ID:        "edge-recent",
		CameraID:  "cam1",
		FilePath:  "/recent.mp4",
		Format:    model.FormatH264,
		StartedAt: recentEnded.Add(-time.Hour),
		EndedAt:   recentEnded,
	}
	require.NoError(t, db.InsertRecording(ctx, recentRec))

	expired, err := db.ListExpiredRecordings(ctx, 30)
	require.NoError(t, err)
	require.Len(t, expired, 1, "only the 31-day-old recording should be expired")
	require.Equal(t, "edge-old", expired[0].ID)
}

func TestListExpiredRecordings(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test_expired.db")
	db, _ := New(dbPath)
	ctx := context.Background()
	_ = db.Init(ctx)
	defer db.Close()

	now := time.Now().UTC()

	// 1. Old recording (40 days ago) — should be found as expired
	oldEnded := now.Add(-40 * 24 * time.Hour)
	oldRec := &model.Recording{
		ID:        "exp-old",
		CameraID:  "cam1",
		FilePath:  "/old.mp4",
		Format:    model.FormatH264,
		StartedAt: oldEnded.Add(-time.Hour),
		EndedAt:   oldEnded,
	}
	require.NoError(t, db.InsertRecording(ctx, oldRec))

	// 2. Recent recording (1 day ago) — should NOT be found as expired
	recentEnded := now.Add(-24 * time.Hour)
	recentRec := &model.Recording{
		ID:        "exp-recent",
		CameraID:  "cam1",
		FilePath:  "/recent.mp4",
		Format:    model.FormatH264,
		StartedAt: recentEnded.Add(-time.Hour),
		EndedAt:   recentEnded,
	}
	require.NoError(t, db.InsertRecording(ctx, recentRec))

	// 3. Pinned old recording — should NOT be found even if old
	pinnedRec := &model.Recording{
		ID:        "exp-pinned",
		CameraID:  "cam1",
		FilePath:  "/pinned.mp4",
		Format:    model.FormatH264,
		StartedAt: oldEnded.Add(-time.Hour),
		EndedAt:   oldEnded,
		Pinned:    true,
	}
	require.NoError(t, db.InsertRecording(ctx, pinnedRec))

	// Query with 30-day retention
	expired, err := db.ListExpiredRecordings(ctx, 30)
	require.NoError(t, err)

	// Only the old unpinned recording should be found
	require.Len(t, expired, 1)
	require.Equal(t, "exp-old", expired[0].ID)

}
func TestParseTimeLegacyFormat(t *testing.T) {
	// Verify parseTime handles the old time.Time.String() format with monotonic clock
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{"canonical format", "2026-04-30 15:30:00.123456789", false},
		{"without fractional", "2026-04-30 15:30:00", false},
		{"RFC3339", "2026-04-30T15:30:00Z", false},
		{"RFC3339 with offset", "2026-04-30T15:30:00+08:00", false},
		{"legacy Go format", "2026-04-30 22:52:10.109803985 +0800 CST m=+32.026969936", false},
		{"legacy without mono", "2026-04-30 22:52:10.109803985 +0800 CST", false},
		{"empty string", "", false},
		{"garbage", "not a time", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseTime(tt.input)
			if tt.wantErr {
				require.Error(t, err)
				require.True(t, got.IsZero())
			} else {
				require.NoError(t, err)
				if tt.input != "" {
					require.False(t, got.IsZero(), "expected non-zero time for input %q", tt.input)
				}
			}
		})
	}
}

func TestListRecordings_SearchByCameraID(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test_search_camid.db")
	db, _ := New(dbPath)
	ctx := context.Background()
	_ = db.Init(ctx)

	r1 := &model.Recording{ID: "r1", CameraID: "camAlpha", FilePath: "/a1.mp4", Format: model.FormatH264, StartedAt: time.Now()}
	_ = db.InsertRecording(ctx, r1)
	r2 := &model.Recording{ID: "r2", CameraID: "camBeta", FilePath: "/b1.mp4", Format: model.FormatH264, StartedAt: time.Now()}
	_ = db.InsertRecording(ctx, r2)
	r3 := &model.Recording{ID: "r3", CameraID: "other", FilePath: "/c1.mp4", Format: model.FormatH264, StartedAt: time.Now()}
	_ = db.InsertRecording(ctx, r3)

	list, err := db.ListRecordings(ctx, model.RecordingFilter{Search: "cam"})
	require.NoError(t, err)
	require.Len(t, list, 2)
	ids := map[string]bool{list[0].ID: true, list[1].ID: true}
	require.True(t, ids["r1"])
	require.True(t, ids["r2"])
}

func TestListRecordings_SearchByFormat(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test_search_format.db")
	db, _ := New(dbPath)
	ctx := context.Background()
	_ = db.Init(ctx)

	r1 := &model.Recording{ID: "r1", CameraID: "cam1", FilePath: "/a1.mp4", Format: model.FormatH264, StartedAt: time.Now()}
	_ = db.InsertRecording(ctx, r1)
	r2 := &model.Recording{ID: "r2", CameraID: "cam2", FilePath: "/b1.jpg", Format: model.FormatMJPEG, StartedAt: time.Now()}
	_ = db.InsertRecording(ctx, r2)

	list, err := db.ListRecordings(ctx, model.RecordingFilter{Search: "h264"})
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.Equal(t, "r1", list[0].ID)
}

func TestListRecordings_SearchByFilePath(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test_search_path.db")
	db, _ := New(dbPath)
	ctx := context.Background()
	_ = db.Init(ctx)

	r1 := &model.Recording{ID: "r1", CameraID: "cam1", FilePath: "/recordings/cam1/seg1.mp4", Format: model.FormatH264, StartedAt: time.Now()}
	_ = db.InsertRecording(ctx, r1)
	r2 := &model.Recording{ID: "r2", CameraID: "cam2", FilePath: "/recordings/cam2/seg1.mp4", Format: model.FormatH264, StartedAt: time.Now()}
	_ = db.InsertRecording(ctx, r2)

	list, err := db.ListRecordings(ctx, model.RecordingFilter{Search: "cam1/seg"})
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.Equal(t, "r1", list[0].ID)
}

func TestListRecordings_SearchEmpty(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test_search_empty.db")
	db, _ := New(dbPath)
	ctx := context.Background()
	_ = db.Init(ctx)

	r1 := &model.Recording{ID: "r1", CameraID: "camA", FilePath: "/a1.mp4", Format: model.FormatH264, StartedAt: time.Now()}
	_ = db.InsertRecording(ctx, r1)
	r2 := &model.Recording{ID: "r2", CameraID: "camB", FilePath: "/b1.mp4", Format: model.FormatH264, StartedAt: time.Now()}
	_ = db.InsertRecording(ctx, r2)

	list, err := db.ListRecordings(ctx, model.RecordingFilter{Search: ""})
	require.NoError(t, err)
	require.Len(t, list, 2)
}

func TestListRecordings_SearchWithOtherFilters(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test_search_combo.db")
	db, _ := New(dbPath)
	ctx := context.Background()
	_ = db.Init(ctx)

	r1 := &model.Recording{ID: "r1", CameraID: "cam1", FilePath: "/a1.mp4", Format: model.FormatH264, StartedAt: time.Now()}
	_ = db.InsertRecording(ctx, r1)
	r2 := &model.Recording{ID: "r2", CameraID: "cam2", FilePath: "/cam1_b.mp4", Format: model.FormatH264, StartedAt: time.Now()}
	_ = db.InsertRecording(ctx, r2)
	r3 := &model.Recording{ID: "r3", CameraID: "cam1", FilePath: "/c1.mp4", Format: model.FormatMJPEG, StartedAt: time.Now()}
	_ = db.InsertRecording(ctx, r3)

	// Search "cam1" AND camera_id="cam1" — only r1 and r3 match camera_id, and both match search
	list, err := db.ListRecordings(ctx, model.RecordingFilter{Search: "cam1", CameraID: "cam1"})
	require.NoError(t, err)
	require.Len(t, list, 2)
	ids := map[string]bool{list[0].ID: true, list[1].ID: true}
	require.True(t, ids["r1"])
	require.True(t, ids["r3"])
}

func TestListRecordings_SearchLikeWildcardEscape(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test_search_wildcard.db")
	db, _ := New(dbPath)
	ctx := context.Background()
	_ = db.Init(ctx)

	r1 := &model.Recording{ID: "r1", CameraID: "cam_percent%", FilePath: "/a1.mp4", Format: model.FormatH264, StartedAt: time.Now()}
	_ = db.InsertRecording(ctx, r1)
	r2 := &model.Recording{ID: "r2", CameraID: "cam_normal", FilePath: "/b1.mp4", Format: model.FormatH264, StartedAt: time.Now()}
	_ = db.InsertRecording(ctx, r2)
	r3 := &model.Recording{ID: "r3", CameraID: "cam_other", FilePath: "/c1.mp4", Format: model.FormatH264, StartedAt: time.Now()}
	_ = db.InsertRecording(ctx, r3)

	// Searching for literal "%" should only match r1 (which contains the literal %)
	list, err := db.ListRecordings(ctx, model.RecordingFilter{Search: "%"})
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.Equal(t, "r1", list[0].ID)
}
