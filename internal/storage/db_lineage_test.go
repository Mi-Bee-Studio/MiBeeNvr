package storage

import (
	"context"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/stretchr/testify/require"
)

// TestMergeLineage_RecordedAndRebound covers the provenance lifecycle:
// MergeAndReplaceRecordings maps each consumed source to the merged row,
// folding that merged row further re-targets the original sources to the
// surviving row, and deleting the survivor purges its lineage.
func TestMergeLineage_RecordedAndRebound(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()

	srcA := &model.Recording{ID: "src-a", CameraID: "cam1", FilePath: "/x/a.mp4", Format: model.FormatH264, StartedAt: now, Duration: 60, FileSize: 1}
	srcB := &model.Recording{ID: "src-b", CameraID: "cam1", FilePath: "/x/b.mp4", Format: model.FormatH264, StartedAt: now.Add(time.Minute), Duration: 60, FileSize: 1}
	require.NoError(t, db.InsertRecording(ctx, srcA))
	require.NoError(t, db.InsertRecording(ctx, srcB))

	bucket1 := &model.Recording{ID: "bucket-1", CameraID: "cam1", FilePath: "/x/b1.mp4", Format: model.FormatH264,
		StartedAt: now, EndedAt: now.Add(2 * time.Minute), Duration: 120, FileSize: 2, MergeStatus: model.MergeStatusMerged}
	require.NoError(t, db.MergeAndReplaceRecordings(ctx, bucket1, []string{"src-a", "src-b"}))

	for _, id := range []string{"src-a", "src-b"} {
		target, err := db.FindLineageTarget(ctx, id)
		require.NoError(t, err)
		require.Equal(t, "bucket-1", target, "lineage for %s", id)
	}

	// Fold bucket-1 (itself a merged row) into bucket-2: the sources' lineage
	// must follow the surviving row, and bucket-1 gets its own entry.
	bucket2 := &model.Recording{ID: "bucket-2", CameraID: "cam1", FilePath: "/x/b2.mp4", Format: model.FormatH264,
		StartedAt: now, EndedAt: now.Add(2 * time.Minute), Duration: 120, FileSize: 2, MergeStatus: model.MergeStatusMerged}
	require.NoError(t, db.MergeAndReplaceRecordings(ctx, bucket2, []string{"bucket-1"}))

	for _, id := range []string{"src-a", "src-b", "bucket-1"} {
		target, err := db.FindLineageTarget(ctx, id)
		require.NoError(t, err)
		require.Equal(t, "bucket-2", target, "lineage for %s must re-target to the surviving row", id)
	}

	// Deleting the surviving row purges the lineage that pointed at it.
	_, err := db.DeleteRecordingsBatch(ctx, []string{"bucket-2"})
	require.NoError(t, err)
	for _, id := range []string{"src-a", "src-b", "bucket-1"} {
		target, err := db.FindLineageTarget(ctx, id)
		require.NoError(t, err)
		require.Empty(t, target, "lineage for %s must be purged with its target", id)
	}
}

// TestRollingReplaceLineage: the rolling append/create transaction records
// lineage for the source segments it consumes, targeting the bucket row in
// both the create and the append case.
func TestRollingReplaceLineage(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()

	seg1 := &model.Recording{ID: "seg-1", CameraID: "cam1", FilePath: "/x/s1.mp4", Format: model.FormatH264, StartedAt: now, Duration: 45, FileSize: 1}
	seg2 := &model.Recording{ID: "seg-2", CameraID: "cam1", FilePath: "/x/s2.mp4", Format: model.FormatH264, StartedAt: now.Add(time.Minute), Duration: 45, FileSize: 1}
	require.NoError(t, db.InsertRecording(ctx, seg1))
	require.NoError(t, db.InsertRecording(ctx, seg2))

	bucket := &model.Recording{ID: "rb-1", CameraID: "cam1", FilePath: "/x/rb.mp4", Format: model.FormatH264,
		StartedAt: now, EndedAt: now.Add(time.Minute), Duration: 60, FileSize: 2, MergeStatus: model.MergeStatusMerged}
	require.NoError(t, db.RollingReplaceRecordings(ctx, bucket, "", []string{"seg-1"}))

	target, err := db.FindLineageTarget(ctx, "seg-1")
	require.NoError(t, err)
	require.Equal(t, "rb-1", target)

	// Append into the existing bucket: new sources target the same row.
	bucket2 := &model.Recording{ID: "rb-1", CameraID: "cam1", FilePath: "/x/rb2.mp4", Format: model.FormatH264,
		StartedAt: now, EndedAt: now.Add(2 * time.Minute), Duration: 120, FileSize: 3, MergeStatus: model.MergeStatusMerged}
	require.NoError(t, db.RollingReplaceRecordings(ctx, bucket2, "rb-1", []string{"seg-2"}))

	for _, id := range []string{"seg-1", "seg-2"} {
		target, err := db.FindLineageTarget(ctx, id)
		require.NoError(t, err)
		require.Equal(t, "rb-1", target, "lineage for %s", id)
	}
}

func TestFindLineageTarget_Missing(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	target, err := db.FindLineageTarget(ctx, "never-consumed")
	require.NoError(t, err)
	require.Empty(t, target)
}
