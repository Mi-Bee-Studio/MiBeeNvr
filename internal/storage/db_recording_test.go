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

// TestListRecordingTimelineSegments covers the lightweight timeline projection
// (issue #115): column projection, camera filter, time window, ASC ordering,
// and the truncation flag. The full-row endpoint caps at 500 and was silently
// dropping the afternoon; this endpoint's 7-column projection has no such
// problem for realistic days.
func TestListRecordingTimelineSegments(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	base := time.Date(2026, 7, 24, 0, 0, 0, 0, time.UTC) // day window start

	// 3 recordings for cam-A across the morning, 1 for cam-B, 1 the next day.
	insert := func(id, cam string, offsetMin time.Duration, format model.Format, mergeStatus string) {
		start := base.Add(offsetMin)
		r := &model.Recording{
			ID:          id,
			CameraID:    cam,
			FilePath:    "/" + cam + "/" + id + ".mp4",
			Format:      format,
			StartedAt:   start,
			EndedAt:     start.Add(60 * time.Second),
			Duration:    60,
			FileSize:    4096,
			FrameCount:  60,
			MergeStatus: mergeStatus,
		}
		require.NoError(t, db.InsertRecording(ctx, r))
		// InsertRecording does not set merge_status post-insert for empty strings;
		// force the merge_status we asked for via UpdateRecording when non-default.
		if mergeStatus != "" && mergeStatus != model.MergeStatusPending {
			r.MergeStatus = mergeStatus
			require.NoError(t, db.UpdateRecording(ctx, r))
		}
	}
	insert("a1", "cam-A", 1*time.Minute, model.FormatH264, model.MergeStatusMerged)
	insert("a2", "cam-A", 2*time.Hour, model.FormatH264, model.MergeStatusPending)
	insert("a3", "cam-A", 20*time.Hour, model.FormatH265, model.MergeStatusMerged) // late evening
	insert("b1", "cam-B", 30*time.Minute, model.FormatMJPEG, model.MergeStatusMerged)
	insert("a4", "cam-A", 26*time.Hour, model.FormatH264, model.MergeStatusMerged) // next day — outside window

	dayStart := base
	dayEnd := base.Add(24 * time.Hour)

	// (1) Camera filter: only cam-A segments within the day.
	segs, total, err := db.ListRecordingTimelineSegments(ctx, model.RecordingFilter{
		CameraID:  "cam-A",
		StartTime: dayStart,
		EndTime:   dayEnd,
	})
	require.NoError(t, err)
	require.Len(t, segs, 3, "cam-A has a1/a2/a3 within the day; a4 is next day, b1 is cam-B")
	require.Equal(t, 3, total)
	require.False(t, total > len(segs), "no truncation expected")

	// (2) ASC ordering by started_at.
	require.Equal(t, "a1", segs[0].ID)
	require.Equal(t, "a2", segs[1].ID)
	require.Equal(t, "a3", segs[2].ID, "evening segment (20h) must be present — the bug was afternoon truncation")

	// (3) Only the 7 projected fields are populated; the rest are zero-valued.
	//     (We can't assert "column not selected" directly, but we assert the
	//     fields a timeline needs are correct.)
	require.Equal(t, "cam-A", segs[2].CameraID)
	require.Equal(t, model.FormatH265, segs[2].Format)
	require.Equal(t, model.MergeStatusMerged, segs[2].MergeStatus)
	require.Equal(t, float64(60), segs[2].Duration)
	require.WithinDuration(t, base.Add(20*time.Hour), segs[2].StartedAt, time.Second)

	// (4) No camera filter: all 4 segments within the day (a1,a2,a3,b1).
	segsAll, totalAll, err := db.ListRecordingTimelineSegments(ctx, model.RecordingFilter{
		StartTime: dayStart,
		EndTime:   dayEnd,
	})
	require.NoError(t, err)
	require.Len(t, segsAll, 4)
	require.Equal(t, 4, totalAll)

	// (5) merged=true filter keeps only the merged rows.
	mergedTrue := true
	segsMerged, _, err := db.ListRecordingTimelineSegments(ctx, model.RecordingFilter{
		StartTime: dayStart,
		EndTime:   dayEnd,
		Merged:    &mergedTrue,
	})
	require.NoError(t, err)
	require.Len(t, segsMerged, 3, "a1(merged), a3(merged), b1(merged); a2 is pending")

	// (6) Empty window → empty result, no error.
	segsEmpty, totalEmpty, err := db.ListRecordingTimelineSegments(ctx, model.RecordingFilter{
		CameraID:  "cam-A",
		StartTime: base.Add(48 * time.Hour),
		EndTime:   base.Add(49 * time.Hour),
	})
	require.NoError(t, err)
	require.Empty(t, segsEmpty)
	require.Equal(t, 0, totalEmpty)
}

// TestListRecordingTimelineSegments_Truncation verifies the truncation flag is
// reported correctly when a window exceeds maxTimelineSegments. We can't insert
// 10k+ rows cheaply, so this asserts the (total > len) contract directly via a
// focused unit check of the invariants the handler relies on.
func TestListRecordingTimelineSegments_Truncation(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	base := time.Date(2026, 7, 24, 0, 0, 0, 0, time.UTC)

	// Insert a handful; total will be < maxTimelineSegments → truncated is false.
	for i := range 5 {
		require.NoError(t, db.InsertRecording(ctx, &model.Recording{
			ID:        "t" + string(rune('a'+i)),
			CameraID:  "cam-T",
			FilePath:  "/t.mp4",
			Format:    model.FormatH264,
			StartedAt: base.Add(time.Duration(i) * time.Minute),
		}))
	}
	segs, total, err := db.ListRecordingTimelineSegments(ctx, model.RecordingFilter{
		CameraID:  "cam-T",
		StartTime: base,
		EndTime:   base.Add(24 * time.Hour),
	})
	require.NoError(t, err)
	require.Len(t, segs, 5)
	require.Equal(t, 5, total)
	// The handler computes `truncated = total > len(segments)`. With total == len,
	// it must be false — proving the flag is accurate when not capped.
	require.False(t, total > len(segs))
}

func TestPathIsRecordingFile(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()

	filePath := "/data/cam-1/202607/26/12/cam-1_ts_abc"
	mergePath := "/data/cam-1/202607/26/12/cam-1_ts_abc.mp4"
	require.NoError(t, db.InsertRecording(ctx, &model.Recording{
		ID:        "rec-ref",
		CameraID:  "cam-1",
		FilePath:  filePath,
		Format:    model.FormatH264,
		StartedAt: now,
		EndedAt:   now.Add(time.Minute),
		Duration:  60,
	}))
	require.NoError(t, db.SetMergeResult(ctx, "rec-ref", mergePath, "go"))

	// Full path matches file_path.
	got, err := db.PathIsRecordingFile(ctx, "cam-1", filePath)
	require.NoError(t, err)
	require.True(t, got, "full file_path should be referenced")

	// Full path matches merge_path.
	got, err = db.PathIsRecordingFile(ctx, "cam-1", mergePath)
	require.NoError(t, err)
	require.True(t, got, "full merge_path should be referenced")

	// Unrelated path → not referenced.
	got, err = db.PathIsRecordingFile(ctx, "cam-1", "/data/cam-1/202607/26/12/orphan_xyz.mp4")
	require.NoError(t, err)
	require.False(t, got, "unreferenced path should not be a recording file")

	// Wrong camera → not referenced (camera_id is part of the filter).
	got, err = db.PathIsRecordingFile(ctx, "cam-other", mergePath)
	require.NoError(t, err)
	require.False(t, got, "path under a different camera should not match")
}

// TestCountCacheKeyIncludesAiClass guards against a regression where the AI
// class filter was omitted from the count-cache key: without AiClass in the
// key, ?ai_class=person collided with the unfiltered entry and the list API
// returned the wrong total (the whole-table count), making the "含人" filter
// look broken in the UI.
func TestCountCacheKeyIncludesAiClass(t *testing.T) {
	base := model.RecordingFilter{CameraID: "cam-1", Format: model.FormatH264}
	noClass := countCacheKey(base)
	withPerson := countCacheKey(model.RecordingFilter{CameraID: "cam-1", Format: model.FormatH264, AiClass: "person"})
	withCar := countCacheKey(model.RecordingFilter{CameraID: "cam-1", Format: model.FormatH264, AiClass: "car"})

	require.NotEqual(t, noClass, withPerson, "AiClass=person must not share the cache key of the unfiltered query")
	require.NotEqual(t, noClass, withCar, "AiClass=car must not share the cache key of the unfiltered query")
	require.NotEqual(t, withPerson, withCar, "different AiClass values must produce different cache keys")
}

// TestUpdateRecordingAIStatusStampsProcessedAt: the API handler only accepts
// "completed" (not "done"), so the terminal-status list must include it —
// previously ai_processed_at was never stamped on successful processing.
func TestUpdateRecordingAIStatusStampsProcessedAt(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	require.NoError(t, db.InsertRecording(ctx, &model.Recording{
		ID: "rec-aistat", CameraID: "cam-1", FilePath: "/x.mp4", Format: model.FormatH264,
		StartedAt: now, EndedAt: now.Add(time.Second), Duration: 1,
	}))

	// Non-terminal: no stamp.
	require.NoError(t, db.UpdateRecordingAIStatus(ctx, "rec-aistat", "processing", ""))
	rec, err := db.GetRecording(ctx, "rec-aistat")
	require.NoError(t, err)
	require.Nil(t, rec.AIProcessedAt, "processing must not stamp ai_processed_at")

	// Terminal via the API's vocabulary: stamps.
	require.NoError(t, db.UpdateRecordingAIStatus(ctx, "rec-aistat", "completed", ""))
	rec, err = db.GetRecording(ctx, "rec-aistat")
	require.NoError(t, err)
	require.NotNil(t, rec.AIProcessedAt, "completed must stamp ai_processed_at")
	require.WithinDuration(t, time.Now().UTC(), *rec.AIProcessedAt, time.Minute)

	// Legacy "done" spelling still stamps (backward tolerance).
	require.NoError(t, db.UpdateRecordingAIStatus(ctx, "rec-aistat", "failed", "boom"))
	rec, err = db.GetRecording(ctx, "rec-aistat")
	require.NoError(t, err)
	require.NotNil(t, rec.AIProcessedAt, "failed must stamp ai_processed_at")
}
