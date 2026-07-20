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

// setupReassignTest creates a fresh DB and inserts source + target cameras.
func setupReassignTest(t *testing.T) (*DB, context.Context) {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "reassign_test.db")
	db, err := New(dbPath)
	require.NoError(t, err)
	ctx := context.Background()
	require.NoError(t, db.Init(ctx))

	// Create source camera
	err = db.UpsertCamera(ctx, "cam-source", "Source", "rtsp", "h264", "rtsp://source", "", "", "", "", "", "")
	require.NoError(t, err)

	// Create target camera
	err = db.UpsertCamera(ctx, "cam-target", "Target", "rtsp", "h264", "rtsp://target", "", "", "", "", "", "")
	require.NoError(t, err)

	return db, ctx
}

// countCameraRows is a test helper that returns the number of rows in a table
// where camera_id matches the given value.
func countCameraRows(t *testing.T, db *DB, ctx context.Context, table, cameraID string) int {
	t.Helper()
	var count int
	q := fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE camera_id=?", table)
	err := db.readConn().QueryRowContext(ctx, q, cameraID).Scan(&count)
	require.NoError(t, err)
	return count
}

func TestGetCameraByID(t *testing.T) {
	db, ctx := setupReassignTest(t)
	defer db.Close()

	// Existing camera
	cam, err := db.GetCameraByID(ctx, "cam-source")
	require.NoError(t, err)
	require.NotNil(t, cam)
	require.Equal(t, "cam-source", cam.ID)
	require.Equal(t, "Source", cam.Name)

	// Non-existent camera
	cam, err = db.GetCameraByID(ctx, "nonexistent")
	require.NoError(t, err)
	require.Nil(t, cam)
}

func TestDeleteCameraRow(t *testing.T) {
	db, ctx := setupReassignTest(t)
	defer db.Close()

	// Delete existing camera
	err := db.DeleteCameraRow(ctx, "cam-source")
	require.NoError(t, err)

	// Verify it's gone
	cam, err := db.GetCameraByID(ctx, "cam-source")
	require.NoError(t, err)
	require.Nil(t, cam)

	// Delete non-existent camera — should not error
	err = db.DeleteCameraRow(ctx, "nonexistent")
	require.NoError(t, err)
}

func TestReassignCameraData(t *testing.T) {
	db, ctx := setupReassignTest(t)
	defer db.Close()

	// ── Insert test data ──
	// 10 recordings for source
	now := time.Now().UTC()
	for i := range 10 {
		rec := &model.Recording{
			ID:        fmt.Sprintf("rec-%d", i),
			CameraID:  "cam-source",
			FilePath:  fmt.Sprintf("/data/cam-source/2026/01/01/rec-%d.mp4", i),
			Format:    model.FormatH264,
			StartedAt: now,
			EndedAt:   now.Add(30 * time.Second),
			Duration:  30.0,
			FileSize:  1024,
		}
		err := db.InsertRecording(ctx, rec)
		require.NoError(t, err)
	}

	// 5 health events for source
	for i := range 5 {
		event := model.HealthEvent{
			CameraID:  "cam-source",
			EventType: "connectivity",
			Status:    "ok",
			Message:   fmt.Sprintf("health-%d", i),
			CreatedAt: now,
		}
		err := db.InsertHealthEvent(ctx, event)
		require.NoError(t, err)
	}

	// 3 AI events for source
	for range 3 {
		e := &AIEvent{
			CameraID:  "cam-source",
			EventType: "motion",
			Severity:  "info",
			CreatedAt: now.Format("2006-01-02 15:04:05.999999999"),
		}
		_, err := db.InsertAIEvent(ctx, e)
		require.NoError(t, err)
	}

	for i := range 2 {
		task := &TranscodeTask{
			CameraID:     "cam-source",
			RecordingID:  fmt.Sprintf("rec-%d", i),
			InputPath:    fmt.Sprintf("/input/%d.mp4", i),
			InputFormat:  "h264",
			OutputPath:   fmt.Sprintf("/output/%d.mp4", i),
			OutputFormat: "h265",
			CreatedAt:    now.Format("2006-01-02 15:04:05.999999999"),
		}
		err := db.EnqueueTask(ctx, task)
		require.NoError(t, err)
	}

	// ── Verify initial state ──
	require.Equal(t, 10, countCameraRows(t, db, ctx, "recordings", "cam-source"))
	require.Equal(t, 0, countCameraRows(t, db, ctx, "recordings", "cam-target"))
	require.Equal(t, 5, countCameraRows(t, db, ctx, "camera_health_events", "cam-source"))
	require.Equal(t, 0, countCameraRows(t, db, ctx, "camera_health_events", "cam-target"))
	require.Equal(t, 3, countCameraRows(t, db, ctx, "ai_events", "cam-source"))
	require.Equal(t, 0, countCameraRows(t, db, ctx, "ai_events", "cam-target"))
	require.Equal(t, 2, countCameraRows(t, db, ctx, "transcoding_tasks", "cam-source"))
	require.Equal(t, 0, countCameraRows(t, db, ctx, "transcoding_tasks", "cam-target"))

	// ── Execute reassign ──
	err := db.ReassignCameraData(ctx, "cam-source", "cam-target")
	require.NoError(t, err)

	// ── Verify all data moved to target ──
	require.Equal(t, 0, countCameraRows(t, db, ctx, "recordings", "cam-source"))
	require.Equal(t, 10, countCameraRows(t, db, ctx, "recordings", "cam-target"))
	require.Equal(t, 0, countCameraRows(t, db, ctx, "camera_health_events", "cam-source"))
	require.Equal(t, 5, countCameraRows(t, db, ctx, "camera_health_events", "cam-target"))
	require.Equal(t, 0, countCameraRows(t, db, ctx, "ai_events", "cam-source"))
	require.Equal(t, 3, countCameraRows(t, db, ctx, "ai_events", "cam-target"))
	require.Equal(t, 0, countCameraRows(t, db, ctx, "transcoding_tasks", "cam-source"))
	require.Equal(t, 2, countCameraRows(t, db, ctx, "transcoding_tasks", "cam-target"))

	// ── Verify file_path was rewritten to point at target dir ──
	recs, _, err := db.ListRecordingsWithTotal(ctx, model.RecordingFilter{CameraID: "cam-target"})
	require.NoError(t, err)
	require.Len(t, recs, 10, "all 10 recordings should be re-tagged to target")
	for _, r := range recs {
		require.Contains(t, r.FilePath, "cam-target", "file_path should be rewritten to target ID: %s", r.FilePath)
		require.NotContains(t, r.FilePath, "cam-source", "file_path should no longer contain source ID: %s", r.FilePath)
	}

	// ── Source camera row still exists ──
	cam, err := db.GetCameraByID(ctx, "cam-source")
	require.NoError(t, err)
	require.NotNil(t, cam)
	require.Equal(t, "cam-source", cam.ID)
}

func TestReassignCameraData_TargetNotFound(t *testing.T) {
	db, ctx := setupReassignTest(t)
	defer db.Close()

	// Insert one recording for source
	now := time.Now().UTC()
	rec := &model.Recording{
		ID:        "rec-test",
		CameraID:  "cam-source",
		FilePath:  "/path/test.mp4",
		Format:    model.FormatH264,
		StartedAt: now,
	}
	err := db.InsertRecording(ctx, rec)
	require.NoError(t, err)

	// Target does not exist
	err = db.ReassignCameraData(ctx, "cam-source", "cam-nonexistent")
	require.Error(t, err)
	require.Contains(t, err.Error(), "not found")

	// Verify no data moved — source still has the recording
	require.Equal(t, 1, countCameraRows(t, db, ctx, "recordings", "cam-source"))
	require.Equal(t, 0, countCameraRows(t, db, ctx, "recordings", "cam-nonexistent"))
}

func TestReassignCameraData_Atomicity(t *testing.T) {
	db, ctx := setupReassignTest(t)
	defer db.Close()

	// Insert one recording and one health event for source
	now := time.Now().UTC()
	rec := &model.Recording{
		ID:        "rec-atom",
		CameraID:  "cam-source",
		FilePath:  "/path/atom.mp4",
		Format:    model.FormatH264,
		StartedAt: now,
	}
	err := db.InsertRecording(ctx, rec)
	require.NoError(t, err)

	event := model.HealthEvent{
		CameraID:  "cam-source",
		EventType: "connectivity",
		Status:    "ok",
		CreatedAt: now,
	}
	err = db.InsertHealthEvent(ctx, event)
	require.NoError(t, err)

	// Use a cancelled context — the first query (SELECT 1) should fail,
	// causing the function to return an error with no updates applied.
	ctxCancelled, cancel := context.WithCancel(context.Background())
	cancel()

	err = db.ReassignCameraData(ctxCancelled, "cam-source", "cam-target")
	require.Error(t, err)

	// Verify no data was moved — all rows still belong to source
	require.Equal(t, 1, countCameraRows(t, db, ctx, "recordings", "cam-source"))
	require.Equal(t, 0, countCameraRows(t, db, ctx, "recordings", "cam-target"))
	require.Equal(t, 1, countCameraRows(t, db, ctx, "camera_health_events", "cam-source"))
	require.Equal(t, 0, countCameraRows(t, db, ctx, "camera_health_events", "cam-target"))
}
