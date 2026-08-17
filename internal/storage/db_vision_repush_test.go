package storage

import (
	"context"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/stretchr/testify/require"
)

func TestListRecordingsForVisionRepush(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	now := time.Now().UTC()
	insert := func(id string, format model.Format, startedAt, endedAt time.Time, mergeStatus, aiStatus string) {
		t.Helper()
		r := &model.Recording{
			ID: id, CameraID: "cam1", FilePath: "/data/" + id + ".mp4",
			Format: format, StartedAt: startedAt, EndedAt: endedAt,
			FileSize: 100, FrameCount: 10,
		}
		if mergeStatus != "" {
			r.MergeStatus = mergeStatus
		}
		require.NoError(t, db.InsertRecording(ctx, r))
		if aiStatus != "" {
			require.NoError(t, db.UpdateRecordingAIStatus(ctx, id, aiStatus, ""))
		}
	}

	insert("repush-pending", "mp4", now.Add(-20*time.Minute), now.Add(-19*time.Minute), "", "")
	insert("repush-processing", "mp4", now.Add(-18*time.Minute), now.Add(-17*time.Minute), "", "processing")
	insert("skip-completed", "mp4", now.Add(-16*time.Minute), now.Add(-15*time.Minute), "", "completed")
	insert("skip-failed", "mp4", now.Add(-14*time.Minute), now.Add(-13*time.Minute), "", "failed")
	insert("skip-timelapse", "timelapse", now.Add(-12*time.Minute), now.Add(-11*time.Minute), "", "")
	insert("skip-merged", "mp4", now.Add(-10*time.Minute), now.Add(-9*time.Minute), "merged", "")
	// Completed before the offline window (ended 3h ago) — outside bounds.
	insert("skip-old", "mp4", now.Add(-3*time.Hour), now.Add(-3*time.Hour+time.Minute), "", "")

	recs, err := db.ListRecordingsForVisionRepush(ctx, now.Add(-2*time.Hour), now, 500)
	require.NoError(t, err)

	ids := make([]string, 0, len(recs))
	for _, r := range recs {
		ids = append(ids, r.ID)
	}
	require.ElementsMatch(t, []string{"repush-pending", "repush-processing"}, ids,
		"only non-terminal, non-timelapse, non-merged segments inside the window qualify")

	// Limit is honored (ascending start order).
	capped, err := db.ListRecordingsForVisionRepush(ctx, now.Add(-2*time.Hour), now, 1)
	require.NoError(t, err)
	require.Len(t, capped, 1)
	require.Equal(t, "repush-pending", capped[0].ID)
}
