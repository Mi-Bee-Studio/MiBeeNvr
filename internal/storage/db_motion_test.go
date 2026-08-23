package storage

// Motion-score storage tests (issue #435): column migration, score updates,
// list filters, and the boring-first ordering used by disk-threshold cleanup.

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/stretchr/testify/require"
)

func newMotionTestDB(t *testing.T) *DB {
	t.Helper()
	db, err := New(filepath.Join(t.TempDir(), "motion.db"))
	require.NoError(t, err)
	require.NoError(t, db.Init(context.Background()))
	t.Cleanup(func() { db.Close() })
	return db
}

func insertMotionTestRecording(t *testing.T, db *DB, id string, endedAgo time.Duration) {
	t.Helper()
	now := time.Now().UTC()
	ended := now.Add(-endedAgo)
	r := &model.Recording{
		ID:          id,
		CameraID:    "cam-1",
		FilePath:    "/x/" + id + ".mp4",
		Format:      model.FormatH264,
		StartedAt:   ended.Add(-30 * time.Second),
		EndedAt:     ended,
		Duration:    30,
		FileSize:    1000,
		FrameCount:  600,
		MergeStatus: model.MergeStatusPending,
	}
	require.NoError(t, db.InsertRecording(context.Background(), r))
}

func TestMotionColumnsExistAfterInit(t *testing.T) {
	db := newMotionTestDB(t)
	var colCount int
	err := db.db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM pragma_table_info('recordings') WHERE name IN ('motion_score','activity_flags')`,
	).Scan(&colCount)
	require.NoError(t, err)
	require.Equal(t, 2, colCount, "motion_score + activity_flags columns must exist")
}

func TestUpdateRecordingMotionScore(t *testing.T) {
	db := newMotionTestDB(t)
	insertMotionTestRecording(t, db, "rec-1", 0)

	require.NoError(t, db.UpdateRecordingMotionScore(context.Background(), "rec-1", 0.42, "motion,scene_cut"))

	got, err := db.GetRecording(context.Background(), "rec-1")
	require.NoError(t, err)
	require.InDelta(t, 0.42, got.MotionScore, 1e-9)
	require.Equal(t, "motion,scene_cut", got.ActivityFlags)
}

func TestListRecordingsMinMotionScoreFilter(t *testing.T) {
	db := newMotionTestDB(t)
	insertMotionTestRecording(t, db, "rec-static", 0)
	insertMotionTestRecording(t, db, "rec-active", time.Minute)
	require.NoError(t, db.UpdateRecordingMotionScore(context.Background(), "rec-static", 0.0, "static"))
	require.NoError(t, db.UpdateRecordingMotionScore(context.Background(), "rec-active", 0.8, "motion"))

	minScore := 0.3
	recs, err := db.ListRecordings(context.Background(), model.RecordingFilter{MinMotionScore: &minScore})
	require.NoError(t, err)
	require.Len(t, recs, 1)
	require.Equal(t, "rec-active", recs[0].ID)

	// Unanalyzed (-1) never passes a min-score filter.
	insertMotionTestRecording(t, db, "rec-unanalyzed", 2*time.Minute)
	recs, err = db.ListRecordings(context.Background(), model.RecordingFilter{MinMotionScore: &minScore})
	require.NoError(t, err)
	require.Len(t, recs, 1)
}

func TestListRecordingsActivityFilter(t *testing.T) {
	db := newMotionTestDB(t)
	insertMotionTestRecording(t, db, "rec-a", 0)
	insertMotionTestRecording(t, db, "rec-b", time.Minute)
	require.NoError(t, db.UpdateRecordingMotionScore(context.Background(), "rec-a", 0.0, "static"))
	require.NoError(t, db.UpdateRecordingMotionScore(context.Background(), "rec-b", 0.9, "motion,scene_cut"))

	recs, err := db.ListRecordings(context.Background(), model.RecordingFilter{Activity: "static"})
	require.NoError(t, err)
	require.Len(t, recs, 1)
	require.Equal(t, "rec-a", recs[0].ID)

	recs, err = db.ListRecordings(context.Background(), model.RecordingFilter{Activity: "scene_cut"})
	require.NoError(t, err)
	require.Len(t, recs, 1)
	require.Equal(t, "rec-b", recs[0].ID)
}

func TestListOldestRecordingsMotionAware_BoringFirst(t *testing.T) {
	db := newMotionTestDB(t)
	// Three recordings, oldest is the most active, newest is the most static:
	// plain age ordering deletes "old-active" first; motion-aware ordering
	// must delete "new-static" first and "old-active" last.
	insertMotionTestRecording(t, db, "old-active", 3*time.Hour)
	insertMotionTestRecording(t, db, "mid-unanalyzed", 2*time.Hour)
	insertMotionTestRecording(t, db, "new-static", time.Hour)
	require.NoError(t, db.UpdateRecordingMotionScore(context.Background(), "old-active", 0.9, "motion"))
	// mid-unanalyzed stays -1 (neutral rank 0.5)
	require.NoError(t, db.UpdateRecordingMotionScore(context.Background(), "new-static", 0.0, "static"))

	recs, err := db.ListOldestRecordingsMotionAware(context.Background(), 3)
	require.NoError(t, err)
	require.Len(t, recs, 3)
	require.Equal(t, "new-static", recs[0].ID, "most static must be evicted first")
	require.Equal(t, "mid-unanalyzed", recs[1].ID, "unanalyzed ranks neutrally")
	require.Equal(t, "old-active", recs[2].ID, "most active must be evicted last")

	// Legacy ordering is untouched (age only).
	legacy, err := db.ListOldestRecordings(context.Background(), 3)
	require.NoError(t, err)
	require.Equal(t, "old-active", legacy[0].ID)
}

func TestTimelineSegmentsCarryMotionScore(t *testing.T) {
	db := newMotionTestDB(t)
	insertMotionTestRecording(t, db, "rec-tl", 0)
	require.NoError(t, db.UpdateRecordingMotionScore(context.Background(), "rec-tl", 0.66, "motion"))

	segs, total, err := db.ListRecordingTimelineSegments(context.Background(), model.RecordingFilter{})
	require.NoError(t, err)
	require.Len(t, segs, 1)
	require.Equal(t, 1, total)
	require.InDelta(t, 0.66, segs[0].MotionScore, 1e-9)
}

// Merge/rolling-merge must propagate motion info into the merged row before
// the source rows are deleted (#458): duration-weighted score + flag union.
// All-unanalyzed sources keep the merged row unanalyzed (-1) so motion-aware
// cleanup does not mislabel it as static.
func TestMergePropagatesMotionScore(t *testing.T) {
	ctx := context.Background()
	db := newMotionTestDB(t)

	// Two analyzed sources: 30s @ 0.2 static + 60s @ 0.8 motion.
	insertMotionTestRecording(t, db, "src-a", 2*time.Minute)
	require.NoError(t, db.UpdateRecordingMotionScore(ctx, "src-a", 0.2, "static"))
	r, err := db.GetRecording(ctx, "src-a")
	require.NoError(t, err)
	r.Duration = 30
	require.NoError(t, db.UpdateRecording(ctx, r))

	insertMotionTestRecording(t, db, "src-b", time.Minute)
	require.NoError(t, db.UpdateRecordingMotionScore(ctx, "src-b", 0.8, "motion,scene_cut"))
	r, err = db.GetRecording(ctx, "src-b")
	require.NoError(t, err)
	r.Duration = 60
	require.NoError(t, db.UpdateRecording(ctx, r))

	merged := &model.Recording{
		ID: "merged-1", CameraID: "cam-1", FilePath: "/x/merged-1.mp4",
		Format: model.FormatH264, StartedAt: time.Now().UTC().Add(-3 * time.Minute),
		EndedAt: time.Now().UTC(), Duration: 90, FileSize: 3000, FrameCount: 1800,
	}
	require.NoError(t, db.MergeAndReplaceRecordings(ctx, merged, []string{"src-a", "src-b"}))

	got, err := db.GetRecording(ctx, "merged-1")
	require.NoError(t, err)
	// (0.2*30 + 0.8*60) / 90 = 0.6
	require.InDelta(t, 0.6, got.MotionScore, 1e-9)
	require.Contains(t, got.ActivityFlags, "static")
	require.Contains(t, got.ActivityFlags, "motion")
	require.Contains(t, got.ActivityFlags, "scene_cut")
}

func TestRollingMergePropagatesMotionScore(t *testing.T) {
	ctx := context.Background()
	db := newMotionTestDB(t)

	// Bucket create from one analyzed + one unanalyzed source.
	insertMotionTestRecording(t, db, "rs-1", 2*time.Minute)
	require.NoError(t, db.UpdateRecordingMotionScore(ctx, "rs-1", 0.6, "motion"))
	r, _ := db.GetRecording(ctx, "rs-1")
	r.Duration = 30
	require.NoError(t, db.UpdateRecording(ctx, r))
	insertMotionTestRecording(t, db, "rs-2", time.Minute) // stays unanalyzed (-1)

	merged := &model.Recording{
		ID: "roll-1", CameraID: "cam-1", FilePath: "/x/roll-1.mp4",
		Format: model.FormatH264, StartedAt: time.Now().UTC().Add(-3 * time.Minute),
		EndedAt: time.Now().UTC(), Duration: 60, FileSize: 2000, FrameCount: 1200,
	}
	require.NoError(t, db.RollingReplaceRecordings(ctx, merged, "", []string{"rs-1", "rs-2"}))

	got, err := db.GetRecording(ctx, "roll-1")
	require.NoError(t, err)
	// Only the analyzed source weights the average: 0.6.
	require.InDelta(t, 0.6, got.MotionScore, 1e-9)
	require.Equal(t, "motion", got.ActivityFlags)

	// Append: 60s of 0.6 existing + 30s of 0.3 new → (0.6*60+0.3*30)/90 = 0.5.
	insertMotionTestRecording(t, db, "rs-3", 0)
	require.NoError(t, db.UpdateRecordingMotionScore(ctx, "rs-3", 0.3, "static"))
	r, _ = db.GetRecording(ctx, "rs-3")
	r.Duration = 30
	require.NoError(t, db.UpdateRecording(ctx, r))

	merged2 := &model.Recording{
		ID: "roll-1", CameraID: "cam-1", FilePath: "/x/roll-1.mp4",
		Format: model.FormatH264, StartedAt: got.StartedAt, EndedAt: time.Now().UTC(),
		Duration: 90, FileSize: 3000, FrameCount: 1800,
	}
	require.NoError(t, db.RollingReplaceRecordings(ctx, merged2, "roll-1", []string{"rs-3"}))

	got, err = db.GetRecording(ctx, "roll-1")
	require.NoError(t, err)
	require.InDelta(t, 0.5, got.MotionScore, 1e-9)
	require.Contains(t, got.ActivityFlags, "motion")
	require.Contains(t, got.ActivityFlags, "static")
}

func TestMergeAllUnanalyzedStaysUnanalyzed(t *testing.T) {
	ctx := context.Background()
	db := newMotionTestDB(t)

	insertMotionTestRecording(t, db, "un-1", 2*time.Minute)
	insertMotionTestRecording(t, db, "un-2", time.Minute)

	merged := &model.Recording{
		ID: "merged-un", CameraID: "cam-1", FilePath: "/x/merged-un.mp4",
		Format: model.FormatH264, StartedAt: time.Now().UTC().Add(-3 * time.Minute),
		EndedAt: time.Now().UTC(), Duration: 60, FileSize: 2000, FrameCount: 1200,
	}
	require.NoError(t, db.MergeAndReplaceRecordings(ctx, merged, []string{"un-1", "un-2"}))

	got, err := db.GetRecording(ctx, "merged-un")
	require.NoError(t, err)
	require.Equal(t, model.MotionScoreUnanalyzed, got.MotionScore)
	require.Empty(t, got.ActivityFlags)
}
