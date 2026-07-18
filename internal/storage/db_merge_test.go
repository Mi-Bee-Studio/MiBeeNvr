package storage

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/stretchr/testify/require"
)

// TestListRecordingsByMergeStatus verifies the query used by `repair fragments`
// to find segments the merge engine gave up on. Covers status filtering, camera
// filtering, limit, and the empty-input no-op.
func TestListRecordingsByMergeStatus(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test_merge.db")
	db, err := New(dbPath)
	require.NoError(t, err)
	ctx := context.Background()
	require.NoError(t, db.Init(ctx))
	defer db.Close()

	now := time.Now().UTC()
	// Insert 5 recordings across 2 cameras; we'll set distinct merge_status values.
	recs := []*model.Recording{
		{ID: "incompat-a", CameraID: "camA", FilePath: "/a1.mp4", Format: model.FormatH264, StartedAt: now, Duration: 11, FileSize: 100},
		{ID: "incompat-b", CameraID: "camA", FilePath: "/a2.mp4", Format: model.FormatH264, StartedAt: now.Add(time.Second), Duration: 9, FileSize: 100},
		{ID: "failed-a", CameraID: "camA", FilePath: "/a3.mp4", Format: model.FormatH264, StartedAt: now.Add(2 * time.Second), Duration: 7, FileSize: 100},
		{ID: "incompat-c", CameraID: "camB", FilePath: "/b1.mp4", Format: model.FormatH264, StartedAt: now.Add(3 * time.Second), Duration: 13, FileSize: 100},
		{ID: "pending-a", CameraID: "camA", FilePath: "/a4.mp4", Format: model.FormatH264, StartedAt: now.Add(4 * time.Second), Duration: 31, FileSize: 100},
	}
	for _, r := range recs {
		require.NoError(t, db.InsertRecording(ctx, r))
	}
	// pending-a stays pending (InsertRecording default). Set the others.
	require.NoError(t, db.SetMergeStatus(ctx, []string{"incompat-a", "incompat-b", "incompat-c"}, model.MergeStatusIncompatible))
	require.NoError(t, db.SetMergeStatus(ctx, []string{"failed-a"}, model.MergeStatusFailed))

	// 1. Filter by incompatible only — should return 3, ordered by started_at ASC.
	t.Run("incompatible only", func(t *testing.T) {
		got, err := db.ListRecordingsByMergeStatus(ctx, []string{model.MergeStatusIncompatible}, "", 0)
		require.NoError(t, err)
		require.Len(t, got, 3)
		// ASC ordering: incompat-a (now) < incompat-b (+1s) < incompat-c (+3s)
		require.Equal(t, "incompat-a", got[0].ID)
		require.Equal(t, "incompat-b", got[1].ID)
		require.Equal(t, "incompat-c", got[2].ID)
		// Full row populated (FilePath needed by --force-delete to remove files).
		require.Equal(t, "/a1.mp4", got[0].FilePath)
		require.Equal(t, model.FormatH264, got[0].Format)
	})

	// 2. Multiple statuses: incompatible + failed = 4 (excludes pending-a).
	t.Run("incompatible+failed", func(t *testing.T) {
		got, err := db.ListRecordingsByMergeStatus(ctx, []string{model.MergeStatusIncompatible, model.MergeStatusFailed}, "", 0)
		require.NoError(t, err)
		require.Len(t, got, 4)
	})

	// 3. Camera filter: camA incompatible = 2 (a, b); camB = 1 (c).
	t.Run("camera filter", func(t *testing.T) {
		got, err := db.ListRecordingsByMergeStatus(ctx, []string{model.MergeStatusIncompatible}, "camA", 0)
		require.NoError(t, err)
		require.Len(t, got, 2)
		for _, r := range got {
			require.Equal(t, "camA", r.CameraID)
		}
	})

	// 4. Limit caps the result.
	t.Run("limit", func(t *testing.T) {
		got, err := db.ListRecordingsByMergeStatus(ctx, []string{model.MergeStatusIncompatible}, "", 2)
		require.NoError(t, err)
		require.Len(t, got, 2)
	})

	// 5. Pending is never returned by an incompatible query (the CLI relies on
	//    this to avoid touching live segments the merge engine is still working on).
	t.Run("excludes pending", func(t *testing.T) {
		got, err := db.ListRecordingsByMergeStatus(ctx, []string{model.MergeStatusIncompatible}, "", 0)
		require.NoError(t, err)
		for _, r := range got {
			require.NotEqual(t, "pending-a", r.ID)
		}
	})

	// 6. Empty status slice is a no-op (returns nil, nil — no SQL executed).
	t.Run("empty status no-op", func(t *testing.T) {
		got, err := db.ListRecordingsByMergeStatus(ctx, nil, "", 0)
		require.NoError(t, err)
		require.Nil(t, got)
	})

	// 7. Non-matching status returns empty.
	t.Run("no match", func(t *testing.T) {
		got, err := db.ListRecordingsByMergeStatus(ctx, []string{model.MergeStatusDark}, "", 0)
		require.NoError(t, err)
		require.Empty(t, got)
	})
}
