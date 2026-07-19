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

// TestListFakeMergedRecordings verifies the query behind `repair fragments
// --reset-fake-merged`: recordings marked merged but with an empty merge_path
// (never actually merged by the singleton fast-path bug).
func TestListFakeMergedRecordings(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test_fake.db")
	db, err := New(dbPath)
	require.NoError(t, err)
	ctx := context.Background()
	require.NoError(t, db.Init(ctx))
	defer db.Close()

	now := time.Now().UTC()
	// Three recordings: a real merge (has merge_path), a fake merge (no path),
	// and a pending one. Only the fake merge should be returned.
	recs := []*model.Recording{
		{ID: "real-merged", CameraID: "camA", FilePath: "/src1.mp4", Format: model.FormatH264, StartedAt: now, Duration: 30, FileSize: 100},
		{ID: "fake-merged", CameraID: "camA", FilePath: "/src2.mp4", Format: model.FormatH264, StartedAt: now.Add(time.Second), Duration: 30, FileSize: 100},
		{ID: "pending-one", CameraID: "camA", FilePath: "/src3.mp4", Format: model.FormatH264, StartedAt: now.Add(2 * time.Second), Duration: 30, FileSize: 100},
	}
	for _, r := range recs {
		require.NoError(t, db.InsertRecording(ctx, r))
	}
	// real-merged: mark merged WITH a merge_path (real merge output).
	require.NoError(t, db.SetMergeResult(ctx, "real-merged", "/merged/output.mp4", "go"))
	// fake-merged: mark merged but SetMergeStatus leaves merge_path empty (the bug).
	require.NoError(t, db.SetMergeStatus(ctx, []string{"fake-merged"}, model.MergeStatusMerged))
	// pending-one stays pending.

	// 1. Only fake-merged is returned.
	got, err := db.ListFakeMergedRecordings(ctx, "", 0, 0)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, "fake-merged", got[0].ID)
	require.Equal(t, "", got[0].MergePath, "fake-merged has empty merge_path")

	// 2. Camera filter works.
	t.Run("camera filter", func(t *testing.T) {
		got, err := db.ListFakeMergedRecordings(ctx, "camA", 0, 0)
		require.NoError(t, err)
		require.Len(t, got, 1)
	})

	// 3. Non-matching camera returns empty.
	t.Run("other camera", func(t *testing.T) {
		got, err := db.ListFakeMergedRecordings(ctx, "camB", 0, 0)
		require.NoError(t, err)
		require.Empty(t, got)
	})

	// 4. maxDuration filter: only returns fragments with duration <= the threshold.
	// Useful for targeting singleton debris while leaving long already-merged
	// recordings (which legitimately have an empty merge_path) untouched.
	t.Run("maxDuration filter", func(t *testing.T) {
		// fake-merged has duration=30s. A 60s threshold includes it; a 10s
		// threshold (below 30s) excludes it.
		got, err := db.ListFakeMergedRecordings(ctx, "", 0, 60)
		require.NoError(t, err)
		require.Len(t, got, 1, "30s fragment passes a 60s threshold")
		got, err = db.ListFakeMergedRecordings(ctx, "", 0, 10)
		require.NoError(t, err)
		require.Empty(t, got, "30s fragment excluded by a 10s threshold")
	})

	// 5. After resetting fake-merged to pending, it's no longer returned.
	t.Run("after reset to pending", func(t *testing.T) {
		require.NoError(t, db.SetMergeStatus(ctx, []string{"fake-merged"}, model.MergeStatusPending))
		got, err := db.ListFakeMergedRecordings(ctx, "", 0, 0)
		require.NoError(t, err)
		require.Empty(t, got, "once reset to pending, it's no longer fake-merged")
	})
}
