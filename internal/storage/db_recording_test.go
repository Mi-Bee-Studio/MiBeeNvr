package storage

import (
	"context"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/stretchr/testify/require"
)

func TestListRecordingsWithoutTranscode(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()

	// Insert 3 recordings for camera 'cam-1', all with ended_at set and not archived
	recs := []*model.Recording{
		{ID: "rec-001", CameraID: "cam-1", FilePath: "/cam-1/001.mp4", Format: model.FormatH264, StartedAt: now, EndedAt: now.Add(30 * time.Second), Duration: 30, FileSize: 1024, FrameCount: 30},
		{ID: "rec-002", CameraID: "cam-1", FilePath: "/cam-1/002.mp4", Format: model.FormatH264, StartedAt: now.Add(1 * time.Minute), EndedAt: now.Add(1*time.Minute + 30*time.Second), Duration: 30, FileSize: 2048, FrameCount: 30},
		{ID: "rec-003", CameraID: "cam-1", FilePath: "/cam-1/003.mp4", Format: model.FormatH264, StartedAt: now.Add(2 * time.Minute), EndedAt: now.Add(2*time.Minute + 30*time.Second), Duration: 30, FileSize: 4096, FrameCount: 30},
	}
	for _, rec := range recs {
		require.NoError(t, db.InsertRecording(ctx, rec))
	}

	// Create a transcoding task referencing rec-002
	task := &TranscodeTask{
		CameraID:     "cam-1",
		RecordingID:  "rec-002",
		InputPath:    "/cam-1/002.mp4",
		InputFormat:  "h264",
		OutputPath:   "/cam-1/002_hevc.mp4",
		OutputFormat: "hevc",
		CreatedAt:    formatTime(now),
	}
	require.NoError(t, db.EnqueueTask(ctx, task))

	// Test: 3 recordings for 'cam-1', 1 has a transcoding task → expect 2 returned
	result, err := db.ListRecordingsWithoutTranscode(ctx, "cam-1")
	require.NoError(t, err)
	require.Len(t, result, 2)

	// Verify rec-002 (with transcoding task) is excluded
	ids := make(map[string]bool)
	for _, r := range result {
		ids[r.ID] = true
	}
	require.True(t, ids["rec-001"], "rec-001 should be in results")
	require.True(t, ids["rec-003"], "rec-003 should be in results")
	require.False(t, ids["rec-002"], "rec-002 (with transcoding task) should not be in results")

	// Test: Empty result for camera with no recordings
	result, err = db.ListRecordingsWithoutTranscode(ctx, "cam-nonexistent")
	require.NoError(t, err)
	require.Len(t, result, 0)
}
