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
