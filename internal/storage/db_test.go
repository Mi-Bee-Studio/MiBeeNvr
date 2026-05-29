package storage

import (
	"context"
	"fmt"
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
		Merged:     false,
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

	merged := true
	list2, err := db.ListRecordings(ctx, model.RecordingFilter{Merged: &merged})
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

func TestSetMerged(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test7.db")
	db, _ := New(dbPath)
	ctx := context.Background()
	_ = db.Init(ctx)

	rec := &model.Recording{ID: "merge-1", CameraID: "camM", FilePath: "/m.mp4", Format: model.FormatH264, StartedAt: time.Now()}
	_ = db.InsertRecording(ctx, rec)

	err := db.SetMerged(ctx, rec.ID, true)
	require.NoError(t, err)

	got, err := db.GetRecording(ctx, rec.ID)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.True(t, got.Merged)

	err = db.SetMerged(ctx, rec.ID, false)
	require.NoError(t, err)
	got2, err := db.GetRecording(ctx, rec.ID)
	require.NoError(t, err)
	require.False(t, got2.Merged)
}

func TestCleanupIncomplete(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test8.db")
	db, _ := New(dbPath)
	ctx := context.Background()
	_ = db.Init(ctx)

	// Insert directly with NULL ended_at to test cleanup (InsertRecording serializes zero time as 0001-01-01, not NULL)
	_, err := db.db.ExecContext(ctx,
	`INSERT INTO recordings(id, camera_id, file_path, format, started_at, ended_at, duration, file_size, frame_count, merged) VALUES(?,?,?,?,NULL,?,?,?,?);`,
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

	err := db.UpsertCamera(ctx, "cam1", "Camera 1", "rtsp_h264", "", "rtsp://localhost:554/stream", "user", "pass", true, "", "", "")

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
	require.Equal(t, "user", cameras[0].Username)
	require.True(t, cameras[0].HasPassword)



	// Test update existing camera

	err = db.UpsertCamera(ctx, "cam1", "Updated Camera 1", "rtsp_mjpeg", "", "rtsp://localhost:555/stream", "newuser", "newpass", false, "", "", "")

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
	require.Equal(t, "newuser", cameras2[0].Username)
	require.True(t, cameras2[0].HasPassword)



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
	err := db.UpsertCamera(ctx, "cam1", "Camera 1", "rtsp_h264", "", "rtsp://localhost:554/stream", "user", "pass", true, "", "", "")
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
	require.Equal(t, "user", cam.Username)
	require.True(t, cam.HasPassword)
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

func TestListCameras_CredentialMetadata(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test_cred_meta.db")
	db, _ := New(dbPath)
	ctx := context.Background()
	_ = db.Init(ctx)
	defer db.Close()

	// Camera with username and password
	require.NoError(t, db.UpsertCamera(ctx, "cam1", "With Creds", "rtsp_h264", "", "rtsp://host/stream", "admin", "secret", true, "", "", ""))
	// Camera with username only (no password)
	require.NoError(t, db.UpsertCamera(ctx, "cam2", "No Password", "rtsp_h264", "", "rtsp://host/stream2", "viewer", "", true, "", "", ""))
	// Camera with no credentials
	require.NoError(t, db.UpsertCamera(ctx, "cam3", "No Creds", "rtsp_h264", "", "rtsp://host/stream3", "", "", true, "", "", ""))

	cameras, err := db.ListCameras(ctx)
	require.NoError(t, err)
	require.Len(t, cameras, 3)

	// cam1: has both username and password
	require.Equal(t, "admin", cameras[0].Username)
	require.True(t, cameras[0].HasPassword)

	// cam2: has username but no password
	require.Equal(t, "viewer", cameras[1].Username)
	require.False(t, cameras[1].HasPassword)

	// cam3: no credentials
	require.Equal(t, "", cameras[2].Username)
	require.False(t, cameras[2].HasPassword)
}

func TestGetCamera_CredentialMetadata(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test_get_cred.db")
	db, _ := New(dbPath)
	ctx := context.Background()
	_ = db.Init(ctx)
	defer db.Close()

	// Camera with credentials
	require.NoError(t, db.UpsertCamera(ctx, "cam1", "Cred Cam", "rtsp_h264", "", "rtsp://host/stream", "user1", "pass1", true, "", "", ""))

	cam, err := db.GetCamera(ctx, "cam1")
	require.NoError(t, err)
	require.NotNil(t, cam)
	require.Equal(t, "user1", cam.Username)
	require.True(t, cam.HasPassword)

	// Camera without password
	require.NoError(t, db.UpsertCamera(ctx, "cam2", "No Pass", "rtsp_h264", "", "rtsp://host/stream2", "", "", true, "", "", ""))

	cam2, err := db.GetCamera(ctx, "cam2")
	require.NoError(t, err)
	require.NotNil(t, cam2)
	require.Equal(t, "", cam2.Username)
	require.False(t, cam2.HasPassword)
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
		Merged:     false,
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

	// 3. Old recording (no special protection now — merged doesn't protect from cleanup)
	oldRec2 := &model.Recording{
		ID:        "exp-old2",
		CameraID:  "cam1",
		FilePath:  "/old2.mp4",
		Format:    model.FormatH264,
		StartedAt: oldEnded.Add(-time.Hour),
		EndedAt:   oldEnded,
		Merged:    true,
	}
	require.NoError(t, db.InsertRecording(ctx, oldRec2))

	// Query with 30-day retention
	expired, err := db.ListExpiredRecordings(ctx, 30)
	require.NoError(t, err)

	// Both old recordings should be found (merged does NOT protect from cleanup)
	require.Len(t, expired, 2)
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

func TestUpsertCameraMerge_RoundTrip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test_merge_config.db")
	db, _ := New(dbPath)
	ctx := context.Background()
	_ = db.Init(ctx)
	defer db.Close()

	// Insert a camera first
	require.NoError(t, db.UpsertCamera(ctx, "cam1", "Merge Cam", "rtsp_h264", "", "rtsp://host/stream", "", "", true, "", "", ""))

	// Set per-camera merge config
	mergeEnabled := true
	checkInterval := "30m"
	windowSize := "2h"
	batchLimit := 50
	minSegmentAge := "5m"
	minSegments := 5
	err := db.UpsertCameraMerge(ctx, "cam1",
		&mergeEnabled, &checkInterval, &windowSize, &minSegmentAge, &batchLimit, &minSegments)
	require.NoError(t, err)

	// Read back and verify
	cam, err := db.GetCamera(ctx, "cam1")
	require.NoError(t, err)
	require.NotNil(t, cam)
	require.NotNil(t, cam.MergeEnabled)
	require.True(t, *cam.MergeEnabled)
	require.NotNil(t, cam.MergeCheckInterval)
	require.Equal(t, "30m", *cam.MergeCheckInterval)
	require.NotNil(t, cam.MergeWindowSize)
	require.Equal(t, "2h", *cam.MergeWindowSize)
	require.NotNil(t, cam.MergeBatchLimit)
	require.Equal(t, 50, *cam.MergeBatchLimit)
	require.NotNil(t, cam.MergeMinSegmentAge)
	require.Equal(t, "5m", *cam.MergeMinSegmentAge)
	require.NotNil(t, cam.MergeMinSegmentsToMerge)
	require.Equal(t, 5, *cam.MergeMinSegmentsToMerge)
}

func TestUpsertCameraMerge_NilKeepsExisting(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test_merge_nil.db")
	db, _ := New(dbPath)
	ctx := context.Background()
	_ = db.Init(ctx)
	defer db.Close()

	require.NoError(t, db.UpsertCamera(ctx, "cam1", "Nil Cam", "rtsp_h264", "", "rtsp://host/stream", "", "", true, "", "", ""))

	// Set merge config
	mergeEnabled := false
	checkInterval := "15m"
	err := db.UpsertCameraMerge(ctx, "cam1", &mergeEnabled, &checkInterval, nil, nil, nil, nil)
	require.NoError(t, err)

	// Update with all nil — should keep existing values
	err = db.UpsertCameraMerge(ctx, "cam1", nil, nil, nil, nil, nil, nil)
	require.NoError(t, err)

	cam, err := db.GetCamera(ctx, "cam1")
	require.NoError(t, err)
	require.NotNil(t, cam)
	require.NotNil(t, cam.MergeEnabled)
	require.False(t, *cam.MergeEnabled)
	require.NotNil(t, cam.MergeCheckInterval)
	require.Equal(t, "15m", *cam.MergeCheckInterval)
	// Fields not set remain nil
	require.Nil(t, cam.MergeWindowSize)
	require.Nil(t, cam.MergeBatchLimit)
}

func TestListCameras_WithMergeConfig(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test_list_merge.db")
	db, _ := New(dbPath)
	ctx := context.Background()
	_ = db.Init(ctx)
	defer db.Close()

	// Camera with no merge config
	require.NoError(t, db.UpsertCamera(ctx, "cam1", "No Merge", "rtsp_h264", "", "rtsp://host/stream", "", "", true, "", "", ""))
	// Camera with merge config
	require.NoError(t, db.UpsertCamera(ctx, "cam2", "With Merge", "rtsp_h264", "", "rtsp://host/stream2", "", "", true, "", "", ""))
	mergeEnabled := true
	batchLimit := 100
	require.NoError(t, db.UpsertCameraMerge(ctx, "cam2", &mergeEnabled, nil, nil, nil, &batchLimit, nil))

	cameras, err := db.ListCameras(ctx)
	require.NoError(t, err)
	require.Len(t, cameras, 2)

	// cam1: all merge fields nil
	require.Nil(t, cameras[0].MergeEnabled)
	require.Nil(t, cameras[0].MergeBatchLimit)

	// cam2: merge fields set
	require.NotNil(t, cameras[1].MergeEnabled)
	require.True(t, *cameras[1].MergeEnabled)
	require.NotNil(t, cameras[1].MergeBatchLimit)
	require.Equal(t, 100, *cameras[1].MergeBatchLimit)
}

func TestUpsertCameraMerge_AllFalseValues(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test_merge_false.db")
	db, _ := New(dbPath)
	ctx := context.Background()
	_ = db.Init(ctx)
	defer db.Close()

	require.NoError(t, db.UpsertCamera(ctx, "cam1", "False Cam", "rtsp_h264", "", "rtsp://host/stream", "", "", true, "", "", ""))

	// Set merge_enabled to false — must not be confused with nil
	mergeEnabled := false
	err := db.UpsertCameraMerge(ctx, "cam1", &mergeEnabled, nil, nil, nil, nil, nil)
	require.NoError(t, err)

	cam, err := db.GetCamera(ctx, "cam1")
	require.NoError(t, err)
	require.NotNil(t, cam.MergeEnabled)
	require.False(t, *cam.MergeEnabled)
}

func TestUpsertCamera_OnvifFields(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test_onvif.db")
	db, _ := New(dbPath)
	ctx := context.Background()
	_ = db.Init(ctx)
	defer db.Close()

	// Insert camera with ONVIF fields
	err := db.UpsertCamera(ctx, "cam1", "ONVIF Cam", "onvif", "", "rtsp://host/stream", "admin", "pass", true, "http://host/onvif/device_service", "profile_1", "")
	require.NoError(t, err)

	// Verify via GetCamera
	cam, err := db.GetCamera(ctx, "cam1")
	require.NoError(t, err)
	require.NotNil(t, cam)
	require.Equal(t, "http://host/onvif/device_service", cam.ONVIFEndpoint)
	require.Equal(t, "profile_1", cam.ProfileToken)

	// Verify via ListCameras
	cameras, err := db.ListCameras(ctx)
	require.NoError(t, err)
	require.Len(t, cameras, 1)
	require.Equal(t, "http://host/onvif/device_service", cameras[0].ONVIFEndpoint)
	require.Equal(t, "profile_1", cameras[0].ProfileToken)
}

func TestUpsertCamera_OnvifFieldsEmptyDefaults(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test_onvif_empty.db")
	db, _ := New(dbPath)
	ctx := context.Background()
	_ = db.Init(ctx)
	defer db.Close()

	// Insert camera without ONVIF fields (backward compat)
	err := db.UpsertCamera(ctx, "cam1", "No ONVIF", "rtsp_h264", "", "rtsp://host/stream", "", "", true, "", "", "")
	require.NoError(t, err)

	cam, err := db.GetCamera(ctx, "cam1")
	require.NoError(t, err)
	require.NotNil(t, cam)
	require.Equal(t, "", cam.ONVIFEndpoint)
	require.Equal(t, "", cam.ProfileToken)
}

func TestUpsertCamera_OnvifUpdateExisting(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test_onvif_update.db")
	db, _ := New(dbPath)
	ctx := context.Background()
	_ = db.Init(ctx)
	defer db.Close()

	// Insert without ONVIF
	require.NoError(t, db.UpsertCamera(ctx, "cam1", "Cam", "rtsp_h264", "", "rtsp://host/stream", "", "", true, "", "", ""))

	// Update with ONVIF fields
	err := db.UpsertCamera(ctx, "cam1", "Cam Updated", "onvif", "", "rtsp://host/stream2", "admin", "pass", true, "http://host/onvif", "prof_2", "")
	require.NoError(t, err)

	cam, err := db.GetCamera(ctx, "cam1")
	require.NoError(t, err)
	require.NotNil(t, cam)
	require.Equal(t, "http://host/onvif", cam.ONVIFEndpoint)
	require.Equal(t, "prof_2", cam.ProfileToken)
	require.Equal(t, "Cam Updated", cam.Name)
}

func TestMigrationV5ToV6_OnvifColumns(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test_migrate_v6.db")
	db, _ := New(dbPath)
	ctx := context.Background()
	_ = db.Init(ctx)
	defer db.Close()

	// Simulate v5 schema: manually set version and verify columns exist after Init
	// On fresh DB, Init creates all tables with current schema. Verify ONVIF columns exist.
	var onvifEndpointExists, profileTokenExists int
	_ = db.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('cameras') WHERE name='onvif_endpoint'`).Scan(&onvifEndpointExists)
	_ = db.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('cameras') WHERE name='profile_token'`).Scan(&profileTokenExists)
	require.Equal(t, 1, onvifEndpointExists, "onvif_endpoint column must exist after Init")
	require.Equal(t, 1, profileTokenExists, "profile_token column must exist after Init")

	// Verify schema version is at least 11 (current version with transcoding_tasks)
	var version string
	err := db.db.QueryRowContext(ctx, "SELECT value FROM schema_meta WHERE key='schema_version'").Scan(&version)
	require.NoError(t, err)
	require.Equal(t, "12", version)
}

func TestInsertRecordingWithRetry_Success(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test_retry.db")
	db, err := New(dbPath)
	require.NoError(t, err)
	ctx := context.Background()
	require.NoError(t, db.Init(ctx))
	defer db.Close()

	started := time.Now()
	rec := &model.Recording{
		ID:         "retry-001",
		CameraID:   "cam1",
		FilePath:   "/path/retry.mp4",
		Format:     model.FormatH264,
		StartedAt:  started,
		EndedAt:    started.Add(time.Minute),
		Duration:   60.0,
		FileSize:   1024,
		FrameCount: 60,
		Merged:     false,
	}
	err = db.InsertRecordingWithRetry(ctx, rec, 3, 10*time.Millisecond)
	require.NoError(t, err)

	got, err := db.GetRecording(ctx, rec.ID)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, "cam1", got.CameraID)
	require.Equal(t, "/path/retry.mp4", got.FilePath)
}

func TestMergeAndReplaceRecordings(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test_merge_replace.db")
	db, err := New(dbPath)
	require.NoError(t, err)
	ctx := context.Background()
	require.NoError(t, db.Init(ctx))
	defer db.Close()

	now := time.Now()
	// Insert 5 source recordings
	oldIDs := make([]string, 5)
	for i := 0; i < 5; i++ {
		id := fmt.Sprintf("old-%d", i)
		oldIDs[i] = id
		rec := &model.Recording{
			ID:        id,
			CameraID:  "camMerge",
			FilePath:  fmt.Sprintf("/merge/seg%d.mp4", i),
			Format:    model.FormatH264,
			StartedAt: now.Add(time.Duration(i) * time.Minute),
			EndedAt:   now.Add(time.Duration(i+1) * time.Minute),
			Duration:  60.0,
			FileSize:  1024,
		}
		require.NoError(t, db.InsertRecording(ctx, rec))
	}

	// Merge and replace
	merged := &model.Recording{
		ID:        "merged-001",
		CameraID:  "camMerge",
		FilePath:  "/merge/merged.mp4",
		Format:    model.FormatH264,
		StartedAt: now,
		EndedAt:   now.Add(5 * time.Minute),
		Duration:  300.0,
		FileSize:  5120,
		Merged:    true,
	}
	err = db.MergeAndReplaceRecordings(ctx, merged, oldIDs)
	require.NoError(t, err)

	// Old recordings should be deleted
	for _, id := range oldIDs {
		got, err := db.GetRecording(ctx, id)
		require.NoError(t, err)
		require.Nil(t, got, "old recording %s should be deleted", id)
	}

	// Merged recording should exist with Merged=true
	got, err := db.GetRecording(ctx, merged.ID)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.True(t, got.Merged)
	require.Equal(t, "/merge/merged.mp4", got.FilePath)
}

func TestMergeAndReplaceRecordings_EmptyOldIDs(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test_merge_empty.db")
	db, err := New(dbPath)
	require.NoError(t, err)
	ctx := context.Background()
	require.NoError(t, db.Init(ctx))
	defer db.Close()

	merged := &model.Recording{
		ID:        "merge-no-old",
		CameraID:  "cam1",
		FilePath:  "/merge/single.mp4",
		Format:    model.FormatH264,
		StartedAt: time.Now(),
		Duration:  60.0,
		FileSize:  1024,
	}
	err = db.MergeAndReplaceRecordings(ctx, merged, nil)
	require.NoError(t, err)

	got, err := db.GetRecording(ctx, merged.ID)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, "merge-no-old", got.ID)
}

func TestGetRecordingsByPathSet(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test_pathset.db")
	db, err := New(dbPath)
	require.NoError(t, err)
	ctx := context.Background()
	require.NoError(t, db.Init(ctx))
	defer db.Close()

	now := time.Now()
	paths := []string{"/a/seg1.mp4", "/a/seg2.mp4", "/a/seg3.mp4"}
	for i, p := range paths {
		rec := &model.Recording{
			ID:        fmt.Sprintf("ps-%d", i),
			CameraID:  "camPS",
			FilePath:  p,
			Format:    model.FormatH264,
			StartedAt: now.Add(time.Duration(i) * time.Minute),
		}
		require.NoError(t, db.InsertRecording(ctx, rec))
	}

	// Query with all 3 paths + 1 non-existent
	result, err := db.GetRecordingsByPathSet(ctx, append(paths, "/a/nonexistent.mp4"))
	require.NoError(t, err)
	require.Len(t, result, 3)
	for _, p := range paths {
		require.True(t, result[p], "path %s should be in result set", p)
	}
}

func TestGetRecordingsByPathSet_Empty(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test_pathset_empty.db")
	db, err := New(dbPath)
	require.NoError(t, err)
	ctx := context.Background()
	require.NoError(t, db.Init(ctx))
	defer db.Close()

	result, err := db.GetRecordingsByPathSet(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Len(t, result, 0)
}

func TestInsertOrphanRecordings(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test_orphan.db")
	db, err := New(dbPath)
	require.NoError(t, err)
	ctx := context.Background()
	require.NoError(t, db.Init(ctx))
	defer db.Close()

	now := time.Now()
	recordings := []*model.Recording{
		{ID: "orph-1", CameraID: "camO", FilePath: "/o/seg1.mp4", Format: model.FormatH264, StartedAt: now, FileSize: 100},
		{ID: "orph-2", CameraID: "camO", FilePath: "/o/seg2.mp4", Format: model.FormatH264, StartedAt: now.Add(time.Minute), FileSize: 200},
	}

	count, err := db.InsertOrphanRecordings(ctx, recordings)
	require.NoError(t, err)
	require.Equal(t, 2, count)

	// Verify they exist
	got, err := db.GetRecording(ctx, "orph-1")
	require.NoError(t, err)
	require.NotNil(t, got)

	// Insert same data again — should be ignored (OR IGNORE)
	count2, err := db.InsertOrphanRecordings(ctx, recordings)
	require.NoError(t, err)
	require.Equal(t, 0, count2, "duplicate inserts should be ignored")
}

func TestListCamerasExcludesArchived(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test_list_cams.db")
	db, err := New(dbPath)
	require.NoError(t, err)
	ctx := context.Background()
	require.NoError(t, db.Init(ctx))
	defer db.Close()

	require.NoError(t, db.UpsertCamera(ctx, "cam-active", "Active Cam", "rtsp_h264", "", "rtsp://host/stream1", "", "", true, "", "", ""))
	require.NoError(t, db.UpsertCamera(ctx, "cam-archived", "Archived Cam", "rtsp_h264", "", "rtsp://host/stream2", "", "", true, "", "", ""))
	// Mark one as archived
	_, err = db.db.ExecContext(ctx, "UPDATE cameras SET archived=1 WHERE id=?", "cam-archived")
	require.NoError(t, err)

	cameras, err := db.ListCameras(ctx)
	require.NoError(t, err)
	require.Len(t, cameras, 1)
	require.Equal(t, "cam-active", cameras[0].ID)
}

func TestListArchivedCameras(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test_list_archived_cams.db")
	db, err := New(dbPath)
	require.NoError(t, err)
	ctx := context.Background()
	require.NoError(t, db.Init(ctx))
	defer db.Close()

	require.NoError(t, db.UpsertCamera(ctx, "cam-active", "Active Cam", "rtsp_h264", "", "rtsp://host/stream1", "", "", true, "", "", ""))
	require.NoError(t, db.UpsertCamera(ctx, "cam-archived", "Archived Cam", "rtsp_h264", "", "rtsp://host/stream2", "", "", true, "", "", ""))
	_, err = db.db.ExecContext(ctx, "UPDATE cameras SET archived=1 WHERE id=?", "cam-archived")
	require.NoError(t, err)

	cameras, err := db.ListArchivedCameras(ctx)
	require.NoError(t, err)
	require.Len(t, cameras, 1)
	require.Equal(t, "cam-archived", cameras[0].ID)
}

func TestListRecordingsArchivedFilter(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test_rec_archived.db")
	db, err := New(dbPath)
	require.NoError(t, err)
	ctx := context.Background()
	require.NoError(t, db.Init(ctx))
	defer db.Close()

	now := time.Now()
	// Insert non-archived recording
	require.NoError(t, db.InsertRecording(ctx, &model.Recording{
		ID: "rec-normal", CameraID: "cam1", FilePath: "/f/normal.mp4", Format: model.FormatH264,
		StartedAt: now, EndedAt: now.Add(time.Minute), Duration: 60, FileSize: 100, Merged: false,
	}))
	// Insert archived recording
	require.NoError(t, db.InsertRecording(ctx, &model.Recording{
		ID: "rec-archived", CameraID: "cam1", FilePath: "/f/archived.mp4", Format: model.FormatH264,
		StartedAt: now.Add(time.Minute), EndedAt: now.Add(2 * time.Minute), Duration: 60, FileSize: 200, Merged: false,
	}))
	_, err = db.db.ExecContext(ctx, "UPDATE recordings SET archived=1 WHERE id=?", "rec-archived")
	require.NoError(t, err)

	// Default (Archived=nil) → exclude archived
	recs, err := db.ListRecordings(ctx, model.RecordingFilter{})
	require.NoError(t, err)
	require.Len(t, recs, 1)
	require.Equal(t, "rec-normal", recs[0].ID)

	// Archived=false → exclude archived
	archivedFalse := false
	recs, err = db.ListRecordings(ctx, model.RecordingFilter{Archived: &archivedFalse})
	require.NoError(t, err)
	require.Len(t, recs, 1)
	require.Equal(t, "rec-normal", recs[0].ID)

	// Archived=true → only archived
	archivedTrue := true
	recs, err = db.ListRecordings(ctx, model.RecordingFilter{Archived: &archivedTrue})
	require.NoError(t, err)
	require.Len(t, recs, 1)
	require.Equal(t, "rec-archived", recs[0].ID)
}

func TestListOldestRecordingsExcludesArchived(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test_oldest_archived.db")
	db, err := New(dbPath)
	require.NoError(t, err)
	ctx := context.Background()
	require.NoError(t, db.Init(ctx))
	defer db.Close()

	now := time.Now()
	// Insert archived (oldest)
	require.NoError(t, db.InsertRecording(ctx, &model.Recording{
		ID: "rec-old-archived", CameraID: "cam1", FilePath: "/f/old.mp4", Format: model.FormatH264,
		StartedAt: now.Add(-2 * time.Hour), EndedAt: now.Add(-2*time.Hour + time.Minute), Duration: 60, FileSize: 100, Merged: false,
	}))
	_, err = db.db.ExecContext(ctx, "UPDATE recordings SET archived=1 WHERE id=?", "rec-old-archived")
	require.NoError(t, err)
	// Insert non-archived (newer)
	require.NoError(t, db.InsertRecording(ctx, &model.Recording{
		ID: "rec-new", CameraID: "cam1", FilePath: "/f/new.mp4", Format: model.FormatH264,
		StartedAt: now.Add(-time.Hour), EndedAt: now.Add(-time.Hour + time.Minute), Duration: 60, FileSize: 200, Merged: false,
	}))

	recs, err := db.ListOldestRecordings(ctx, 10)
	require.NoError(t, err)
	require.Len(t, recs, 1)
	require.Equal(t, "rec-new", recs[0].ID)
}

func TestEscapeLike(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "plain text", input: "hello", want: "hello"},
		{name: "percent sign", input: "100%", want: "100\\%"},
		{name: "underscore", input: "test_", want: "test\\_"},
		{name: "backslash", input: "path\\to", want: "path\\\\to"},
		{name: "all special chars", input: "%_\\", want: "\\%\\_\\\\"},
		{name: "empty string", input: "", want: ""},
		{name: "percent in middle", input: "90%done", want: "90\\%done"},
		{name: "multiple underscores", input: "a_b_c", want: "a\\_b\\_c"},
		{name: "mixed", input: "%completed_\\test", want: "\\%completed\\_\\\\test"},
		{name: "SQL injection attempt", input: "' OR 1=1 --", want: "' OR 1=1 --"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := escapeLike(tt.input)
			require.Equal(t, tt.want, got)
		})
	}
}
