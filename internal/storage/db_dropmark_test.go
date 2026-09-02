package storage

// Tests for queue-drop marking (#671): recordings reported dropped by the AI
// consumer get ai_status='skipped' — never overwriting terminal statuses —
// and are excluded from offline-compensation repush.

import (
	"context"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/stretchr/testify/require"
)

func seedDropRecording(t *testing.T, db *DB, id, cameraID, aiStatus string, startedAt time.Time) {
	t.Helper()
	r := &model.Recording{
		ID: id, CameraID: cameraID, FilePath: "/data/" + id + ".mp4",
		Format: model.Format("mp4"), StartedAt: startedAt, EndedAt: startedAt.Add(time.Minute),
		FileSize: 100, FrameCount: 10,
	}
	require.NoError(t, db.InsertRecording(context.Background(), r))
	if aiStatus != "" {
		require.NoError(t, db.UpdateRecordingAIStatus(context.Background(), id, aiStatus, ""))
	}
}

func TestMarkRecordingsSkippedByIDs(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	base := time.Date(2026, 9, 2, 4, 0, 0, 0, time.UTC)

	seedDropRecording(t, db, "drop-blank", "cam-1", "", base)
	seedDropRecording(t, db, "drop-pending", "cam-1", "pending", base.Add(time.Minute))
	seedDropRecording(t, db, "drop-processing", "cam-1", "processing", base.Add(2*time.Minute))
	seedDropRecording(t, db, "keep-completed", "cam-1", "completed", base.Add(3*time.Minute))
	seedDropRecording(t, db, "keep-failed", "cam-1", "failed", base.Add(4*time.Minute))
	seedDropRecording(t, db, "keep-skipped-old", "cam-1", "skipped", base.Add(5*time.Minute))

	marked, err := db.MarkRecordingsSkippedByIDs(ctx,
		[]string{"drop-blank", "drop-pending", "drop-processing", "keep-completed", "keep-failed", "keep-skipped-old", "ghost-id"},
		"vision drop:queue_full")
	require.NoError(t, err)
	require.Equal(t, int64(3), marked, "only non-terminal rows are marked")

	for _, id := range []string{"drop-blank", "drop-pending", "drop-processing"} {
		rec, err := db.GetRecording(ctx, id)
		require.NoError(t, err)
		require.Equal(t, "skipped", rec.AIStatus, id)
		require.Equal(t, "vision drop:queue_full", rec.AIError, id)
		require.NotNil(t, rec.AIProcessedAt, "skipped stamps ai_processed_at (%s)", id)
	}
	for _, id := range []string{"keep-completed", "keep-failed"} {
		rec, err := db.GetRecording(ctx, id)
		require.NoError(t, err)
		require.Equal(t, id == "keep-completed", rec.AIStatus == "completed")
		require.Equal(t, id == "keep-failed", rec.AIStatus == "failed")
	}
}

func TestMarkRecordingsSkippedByRange(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	base := time.Date(2026, 9, 2, 4, 0, 0, 0, time.UTC)

	seedDropRecording(t, db, "in-win-cam1", "cam-1", "", base.Add(10*time.Minute))
	seedDropRecording(t, db, "in-win-cam2", "cam-2", "", base.Add(11*time.Minute))
	seedDropRecording(t, db, "out-win-cam1", "cam-1", "", base.Add(5*time.Hour))
	seedDropRecording(t, db, "in-win-done", "cam-1", "completed", base.Add(12*time.Minute))

	marked, err := db.MarkRecordingsSkippedByRange(ctx, "cam-1",
		base.Add(5*time.Minute), base.Add(3*time.Hour), "vision drop:ttl_expired")
	require.NoError(t, err)
	require.Equal(t, int64(1), marked, "camera + time window + non-terminal filter")

	rec, err := db.GetRecording(ctx, "in-win-cam1")
	require.NoError(t, err)
	require.Equal(t, "skipped", rec.AIStatus)
	require.Equal(t, "vision drop:ttl_expired", rec.AIError)

	rec, _ = db.GetRecording(ctx, "in-win-cam2")
	require.Equal(t, "", rec.AIStatus, "other cameras untouched")
	rec, _ = db.GetRecording(ctx, "out-win-cam1")
	require.Equal(t, "", rec.AIStatus, "outside the time window untouched")
	rec, _ = db.GetRecording(ctx, "in-win-done")
	require.Equal(t, "completed", rec.AIStatus, "terminal status untouched")
}

func TestRepushExcludesSkipped(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()

	seedDropRecording(t, db, "repush-eligible", "cam-1", "", now.Add(-20*time.Minute))
	seedDropRecording(t, db, "repush-skipped", "cam-1", "", now.Add(-18*time.Minute))
	// Mark one as skipped via the drop path (not the generic status updater).
	_, err := db.MarkRecordingsSkippedByIDs(ctx, []string{"repush-skipped"}, "vision drop:queue_full")
	require.NoError(t, err)

	recs, err := db.ListRecordingsForVisionRepush(ctx, now.Add(-2*time.Hour), now, 500)
	require.NoError(t, err)
	ids := make([]string, 0, len(recs))
	for _, r := range recs {
		ids = append(ids, r.ID)
	}
	require.ElementsMatch(t, []string{"repush-eligible"}, ids,
		"dropped-and-marked recordings must not be re-pushed during offline compensation")
}
