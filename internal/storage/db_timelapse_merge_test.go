package storage

import (
	"context"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/stretchr/testify/require"
)

// newTimelapseMerge is a tiny test factory producing a valid TimelapseMerge
// row with sensible defaults so each test only overrides what it asserts on.
func newTimelapseMerge(cameraID string, windowStart time.Time, label string) *model.TimelapseMerge {
	return &model.TimelapseMerge{
		CameraID:         cameraID,
		WindowStart:      windowStart,
		WindowEnd:        windowStart.Add(24 * time.Hour),
		DurationLabel:    label,
		OutputPath:       "/tmp/periodic-merge/" + cameraID + "/periodic_" + windowStart.Format("2006-01-02_150405") + ".mp4",
		FrameCount:       2880,
		Codec:            model.TimelapseMergeCodecH265,
		FPS:              30,
		SourceSegmentIDs: `["1784636406191031930","1784636277274361781"]`,
	}
}

func TestInsertAndGetTimelapseMerge(t *testing.T) {
	t.Helper()
	db := newTestDB(t)
	ctx := context.Background()

	windowStart := time.Date(2026, 7, 21, 0, 0, 0, 0, time.UTC)
	m := newTimelapseMerge("cam-test", windowStart, "natural-day")
	id, err := db.InsertTimelapseMerge(ctx, m)
	require.NoError(t, err)
	require.NotZero(t, id)
	require.Equal(t, id, m.ID)
	require.Equal(t, model.TimelapseMergeStatusPending, m.Status, "InsertTimelapseMerge should default status to pending")
	require.False(t, m.CreatedAt.IsZero(), "CreatedAt should be set to now when zero")

	got, err := db.GetTimelapseMerge(ctx, id)
	require.NoError(t, err)
	require.Equal(t, m.CameraID, got.CameraID)
	require.True(t, got.WindowStart.Equal(windowStart))
	require.Equal(t, "natural-day", got.DurationLabel)
	require.Equal(t, model.TimelapseMergeCodecH265, got.Codec)
	require.Equal(t, 2880, got.FrameCount)
	require.Equal(t, 30, got.FPS)
	require.Equal(t, model.TimelapseMergeStatusPending, got.Status)
	require.False(t, got.CreatedAt.IsZero())
	require.True(t, got.CompletedAt.IsZero(), "CompletedAt must be zero before completion")
}

func TestCompleteTimelapseMerge(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	windowStart := time.Date(2026, 7, 21, 0, 0, 0, 0, time.UTC)
	m := newTimelapseMerge("cam-test", windowStart, "24h")
	id, err := db.InsertTimelapseMerge(ctx, m)
	require.NoError(t, err)

	err = db.CompleteTimelapseMerge(ctx, id, "/tmp/out.mp4", 1234567, 3000, model.TimelapseMergeCodecH264, `["a","b","c"]`)
	require.NoError(t, err)

	got, err := db.GetTimelapseMerge(ctx, id)
	require.NoError(t, err)
	require.Equal(t, model.TimelapseMergeStatusCompleted, got.Status)
	require.Equal(t, "/tmp/out.mp4", got.OutputPath)
	require.Equal(t, int64(1234567), got.FileSize)
	require.Equal(t, 3000, got.FrameCount)
	require.Equal(t, model.TimelapseMergeCodecH264, got.Codec)
	require.Equal(t, `["a","b","c"]`, got.SourceSegmentIDs)
	require.Equal(t, "", got.Error, "Completion should clear any stale error")
	require.False(t, got.CompletedAt.IsZero(), "CompletedAt should be set on completion")
}

func TestUpdateTimelapseMergeStatus_Failed(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	m := newTimelapseMerge("cam-test", time.Now().UTC(), "8h")
	id, err := db.InsertTimelapseMerge(ctx, m)
	require.NoError(t, err)

	err = db.UpdateTimelapseMergeStatus(ctx, id, model.TimelapseMergeStatusFailed, "boom: out of memory")
	require.NoError(t, err)

	got, err := db.GetTimelapseMerge(ctx, id)
	require.NoError(t, err)
	require.Equal(t, model.TimelapseMergeStatusFailed, got.Status)
	require.Equal(t, "boom: out of memory", got.Error)
	require.True(t, got.CompletedAt.IsZero(), "Failed merge must not set CompletedAt")
}

func TestFindTimelapseMergeByWindow(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	windowStart := time.Date(2026, 7, 21, 0, 0, 0, 0, time.UTC)
	m := newTimelapseMerge("cam-find", windowStart, "natural-day")
	_, err := db.InsertTimelapseMerge(ctx, m)
	require.NoError(t, err)

	// Hit
	got, err := db.FindTimelapseMergeByWindow(ctx, "cam-find", windowStart, "natural-day")
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, windowStart.Unix(), got.WindowStart.Unix())

	// Miss — different camera
	got, err = db.FindTimelapseMergeByWindow(ctx, "cam-other", windowStart, "natural-day")
	require.NoError(t, err)
	require.Nil(t, got, "FindTimelapseMergeByWindow must return (nil, nil) when no match")

	// Miss — different duration label
	got, err = db.FindTimelapseMergeByWindow(ctx, "cam-find", windowStart, "8h")
	require.NoError(t, err)
	require.Nil(t, got)
}

func TestListAndCountTimelapseMerges(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	cameraA := "cam-list-a"
	cameraB := "cam-list-b"
	base := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)

	// 3 merges for cameraA across 3 days + 1 merge for cameraB.
	for i := range 3 {
		m := newTimelapseMerge(cameraA, base.AddDate(0, 0, i), "natural-day")
		_, err := db.InsertTimelapseMerge(ctx, m)
		require.NoError(t, err)
	}
	mB := newTimelapseMerge(cameraB, base, "natural-day")
	_, err := db.InsertTimelapseMerge(ctx, mB)
	require.NoError(t, err)

	// List all
	all, err := db.ListTimelapseMerges(ctx, TimelapseMergeFilter{Limit: 100})
	require.NoError(t, err)
	require.Len(t, all, 4)

	// Filter by camera
	aOnly, err := db.ListTimelapseMerges(ctx, TimelapseMergeFilter{CameraID: cameraA, Limit: 100})
	require.NoError(t, err)
	require.Len(t, aOnly, 3)
	for _, m := range aOnly {
		require.Equal(t, cameraA, m.CameraID)
	}

	// Filter by time range (only the 2nd and 3rd of July for cameraA)
	rangeStart := time.Date(2026, 7, 2, 0, 0, 0, 0, time.UTC)
	rangeEnd := time.Date(2026, 7, 3, 0, 0, 0, 0, time.UTC)
	inRange, err := db.ListTimelapseMerges(ctx, TimelapseMergeFilter{
		CameraID:  cameraA,
		StartTime: rangeStart,
		EndTime:   rangeEnd,
		Limit:     100,
	})
	require.NoError(t, err)
	require.Len(t, inRange, 2, "range filter should match Jul 2 and Jul 3 windows")

	// Count
	total, err := db.CountTimelapseMerges(ctx, TimelapseMergeFilter{})
	require.NoError(t, err)
	require.Equal(t, 4, total)

	totalA, err := db.CountTimelapseMerges(ctx, TimelapseMergeFilter{CameraID: cameraA})
	require.NoError(t, err)
	require.Equal(t, 3, totalA)

	// DESC ordering — most recent first
	require.True(t, aOnly[0].WindowStart.After(aOnly[2].WindowStart), "ListTimelapseMerges should return DESC by window_start")
}

func TestListTimelapseMerges_StatusFilter(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	m1 := newTimelapseMerge("cam-status", time.Now().UTC(), "24h")
	id1, err := db.InsertTimelapseMerge(ctx, m1)
	require.NoError(t, err)
	require.NoError(t, db.CompleteTimelapseMerge(ctx, id1, "/p1.mp4", 100, 10, model.TimelapseMergeCodecH264, "[]"))

	m2 := newTimelapseMerge("cam-status", time.Now().Add(time.Hour), "24h")
	id2, err := db.InsertTimelapseMerge(ctx, m2)
	require.NoError(t, err)
	require.NoError(t, db.UpdateTimelapseMergeStatus(ctx, id2, model.TimelapseMergeStatusFailed, "err"))

	// Only completed
	completed, err := db.ListTimelapseMerges(ctx, TimelapseMergeFilter{Status: model.TimelapseMergeStatusCompleted, Limit: 100})
	require.NoError(t, err)
	require.Len(t, completed, 1)
	require.Equal(t, model.TimelapseMergeStatusCompleted, completed[0].Status)

	// Only failed
	failed, err := db.ListTimelapseMerges(ctx, TimelapseMergeFilter{Status: model.TimelapseMergeStatusFailed, Limit: 100})
	require.NoError(t, err)
	require.Len(t, failed, 1)
	require.Equal(t, model.TimelapseMergeStatusFailed, failed[0].Status)
}

func TestDeleteTimelapseMerge(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	m := newTimelapseMerge("cam-del", time.Now().UTC(), "24h")
	id, err := db.InsertTimelapseMerge(ctx, m)
	require.NoError(t, err)

	require.NoError(t, db.DeleteTimelapseMerge(ctx, id))

	got, err := db.GetTimelapseMerge(ctx, id)
	require.Error(t, err, "GetTimelapseMerge must error after delete")
	require.Nil(t, got)
}
