package merge

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/event"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/stretchr/testify/require"
)

// newTestRollingCoordinator creates a RollingMergeCoordinator with the given config.
func newTestRollingCoordinator(env *mergeTestEnv, cfg config.MergeConfig, bus *event.EventBus) *RollingMergeCoordinator {
	return NewRollingMergeCoordinator(
		env.db, env.store,
		func() config.MergeConfig { return cfg },
		func(string) *config.MergeConfig { return nil },
		func() []config.CameraConfig { return nil },
		nil,
		bus,
	)
}

// boolPtr is a test helper for setting *bool config fields (RollingEnabled).
func boolPtr(b bool) *bool { return &b }

// newTestRollingCoordinatorWithCameras creates a coordinator with a camera list
// (needed for backfill tests that rely on the cameras() callback).
func newTestRollingCoordinatorWithCameras(env *mergeTestEnv, cfg config.MergeConfig, bus *event.EventBus, cameras []config.CameraConfig) *RollingMergeCoordinator {
	return NewRollingMergeCoordinator(
		env.db, env.store,
		func() config.MergeConfig { return cfg },
		func(string) *config.MergeConfig { return nil },
		func() []config.CameraConfig { return cameras },
		nil,
		bus,
	)
}

// publishSegmentCompleted simulates a recorder closing a segment.
func publishSegmentCompleted(t *testing.T, bus *event.EventBus, cameraID, recordingID, filePath, format string, startedAt time.Time) {
	t.Helper()
	bus.Publish(context.Background(), event.TopicSegmentCompleted, event.SegmentCompleted{
		CameraID:    cameraID,
		FilePath:    filePath,
		Format:      format,
		Encoding:    format,
		StartedAt:   startedAt.Format(time.RFC3339Nano),
		EndedAt:     time.Now().Format(time.RFC3339Nano),
		FileSize:    0,
		RecordingID: recordingID,
	})
}

// createAndInsertSegment creates a real MP4 file via the store and inserts a recording row.
// Returns the final file path.
func createAndInsertSegment(t *testing.T, env *mergeTestEnv, recordingID, cameraID string, startedAt time.Time) string {
	t.Helper()
	ctx := context.Background()

	tempPath, finalPath, err := env.store.CreateSegment(cameraID, "h264")
	require.NoError(t, err)

	// Create a valid H.264 segment at the temp path.
	segDir := filepath.Dir(tempPath)
	segFile := createTestH264Segment(t, segDir)
	data, err := os.ReadFile(segFile)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(tempPath, data, 0o644))
	os.Remove(segFile)
	require.NoError(t, env.store.CloseSegment(tempPath, finalPath))

	fi, err := os.Stat(finalPath)
	require.NoError(t, err)

	rec := &model.Recording{
		ID:         recordingID,
		CameraID:   cameraID,
		FilePath:   finalPath,
		Format:     model.FormatH264,
		StartedAt:  startedAt,
		EndedAt:    startedAt.Add(30 * time.Second),
		Duration:   30.0,
		FileSize:   fi.Size(),
		FrameCount: 2,
	}
	require.NoError(t, env.db.InsertRecording(ctx, rec))
	return finalPath
}

// waitForBucketStable polls until the coordinator's bucket for a camera has the
// expected segment count, or times out (merge is async).
func waitForBucketStable(t *testing.T, r *RollingMergeCoordinator, cameraID string, expectedCount int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		bucketAny, ok := r.buckets.Load(cameraID)
		if ok {
			bi := bucketAny.(*bucketInfo)
			bi.mu.Lock()
			count := bi.segmentCount
			bi.mu.Unlock()
			if count == expectedCount {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for bucket segmentCount=%d for camera %s", expectedCount, cameraID)
}

// ---------------------------------------------------------------------------
// TestRollingMerge_SingleSegment — first segment in a bucket creates the file.
// ---------------------------------------------------------------------------

func TestRollingMerge_SingleSegment(t *testing.T) {
	env := newMergeTestEnv(t)
	defer env.close(t)

	bus := event.NewEventBus(16)
	cfg := config.MergeConfig{
		RollingEnabled:  boolPtr(true),
		RollingDebounce: "50ms",
		RollingWindow:   "1h",
	}
	r := newTestRollingCoordinator(env, cfg, bus)
	require.NoError(t, r.Start(context.Background()))
	defer r.Stop()

	cameraID := "cam1"
	now := time.Now().UTC().Truncate(time.Hour).Add(5 * time.Minute)

	filePath := createAndInsertSegment(t, env, "rec1", cameraID, now)
	// Publish the SegmentCompleted event.
	publishSegmentCompleted(t, bus, cameraID, "rec1", filePath, "h264", now)

	// Wait for the async merge to create the bucket.
	waitForBucketStable(t, r, cameraID, 1, 5*time.Second)

	// Verify: the source recording should be deleted from DB, and a merged recording exists.
	recs, _, err := env.db.ListRecordingsWithTotal(context.Background(), model.RecordingFilter{CameraID: cameraID, Limit: 100})
	require.NoError(t, err)
	require.Len(t, recs, 1, "should have exactly 1 merged recording")
	require.True(t, recs[0].Merged, "the recording should be marked merged")
	require.NotEqual(t, "rec1", recs[0].ID, "should be a NEW merged recording ID, not the original")

	// Verify the merged file exists and is parseable.
	mergedPath := recs[0].FilePath
	_, err = os.Stat(mergedPath)
	require.NoError(t, err, "merged file should exist")
	info, err := ParseSegment(mergedPath)
	require.NoError(t, err)
	require.Equal(t, "h264", info.Codec)
}

// ---------------------------------------------------------------------------
// TestRollingMerge_AppendMultiple — multiple segments accumulate in one bucket.
// Verifies the rolling merge produces the same result as a batch merge.
// ---------------------------------------------------------------------------

func TestRollingMerge_AppendMultiple(t *testing.T) {
	env := newMergeTestEnv(t)
	defer env.close(t)

	bus := event.NewEventBus(16)
	cfg := config.MergeConfig{
		RollingEnabled:  boolPtr(true),
		RollingDebounce: "50ms",
		RollingWindow:   "1h",
	}
	r := newTestRollingCoordinator(env, cfg, bus)
	require.NoError(t, r.Start(context.Background()))
	defer r.Stop()

	cameraID := "cam2"
	baseTime := time.Now().UTC().Truncate(time.Hour).Add(10 * time.Minute)

	// Create and publish 3 segments within the same hour window.
	for i := range 3 {
		recID := "rec-" + string(rune('a'+i))
		startedAt := baseTime.Add(time.Duration(i) * 30 * time.Second)
		filePath := createAndInsertSegment(t, env, recID, cameraID, startedAt)
		publishSegmentCompleted(t, bus, cameraID, recID, filePath, "h264", startedAt)
		// Small delay between publishes so the debounce timer processes each.
		time.Sleep(100 * time.Millisecond)
	}

	// Wait for all 3 to be merged into the bucket.
	waitForBucketStable(t, r, cameraID, 3, 10*time.Second)

	// Verify: should have exactly 1 merged recording.
	recs, _, err := env.db.ListRecordingsWithTotal(context.Background(), model.RecordingFilter{CameraID: cameraID, Limit: 100})
	require.NoError(t, err)
	require.Len(t, recs, 1, "should have exactly 1 merged recording after 3 appends")
	require.True(t, recs[0].Merged)

	// Verify the merged file has 3x the samples of a single segment (2 each → 6 total).
	info, err := ParseSegment(recs[0].FilePath)
	require.NoError(t, err)
	require.Equal(t, 6, info.SampleCount, "merged file should contain all 3 segments' samples")
}

// ---------------------------------------------------------------------------
// TestRollingMerge_DisabledByDefault — when RollingEnabled=false, no merge happens.
// ---------------------------------------------------------------------------

func TestRollingMerge_DisabledByDefault(t *testing.T) {
	env := newMergeTestEnv(t)
	defer env.close(t)

	bus := event.NewEventBus(16)
	cfg := config.MergeConfig{
		RollingEnabled: boolPtr(false), // disabled
	}
	r := newTestRollingCoordinator(env, cfg, bus)
	require.NoError(t, r.Start(context.Background()))
	defer r.Stop()

	cameraID := "cam3"
	now := time.Now()
	createAndInsertSegment(t, env, "rec1", cameraID, now)
	publishSegmentCompleted(t, bus, cameraID, "rec1", "", "h264", now)

	// Give the coordinator time to (not) process.
	time.Sleep(200 * time.Millisecond)

	// Verify: original recording should still be pending (not merged).
	recs, _, err := env.db.ListRecordingsWithTotal(context.Background(), model.RecordingFilter{CameraID: cameraID, Limit: 100})
	require.NoError(t, err)
	require.Len(t, recs, 1, "original recording should still exist")
	require.False(t, recs[0].Merged, "should not be merged")
	require.Equal(t, "rec1", recs[0].ID)

	// Verify: no bucket was created.
	_, ok := r.buckets.Load(cameraID)
	require.False(t, ok, "no bucket should be created when disabled")
}

// ---------------------------------------------------------------------------
// TestRollingMerge_NonMP4FormatIgnored — MJPEG/timelapse events are skipped.
// ---------------------------------------------------------------------------

func TestRollingMerge_NonMP4FormatIgnored(t *testing.T) {
	env := newMergeTestEnv(t)
	defer env.close(t)

	bus := event.NewEventBus(16)
	cfg := config.MergeConfig{
		RollingEnabled:  boolPtr(true),
		RollingDebounce: "50ms",
	}
	r := newTestRollingCoordinator(env, cfg, bus)
	require.NoError(t, r.Start(context.Background()))
	defer r.Stop()

	cameraID := "cam4"
	now := time.Now()
	// Publish a "mjpeg" format event — should be ignored.
	publishSegmentCompleted(t, bus, cameraID, "rec1", "/some/path", "mjpeg", now)

	time.Sleep(200 * time.Millisecond)

	// Verify: no bucket was created.
	_, ok := r.buckets.Load(cameraID)
	require.False(t, ok, "no bucket should be created for MJPEG format")
}

// ---------------------------------------------------------------------------
// TestComputeWindow — window alignment produces correct boundaries.
// ---------------------------------------------------------------------------

func TestComputeWindow(t *testing.T) {
	cases := []struct {
		name     string
		t        time.Time
		window   time.Duration
		wantStar string // RFC3339 of start
		wantEnd  string
	}{
		{
			name:     "1h_window_aligned",
			t:        time.Date(2026, 7, 10, 14, 30, 0, 0, time.UTC),
			window:   time.Hour,
			wantStar: "2026-07-10T14:00:00Z",
			wantEnd:  "2026-07-10T15:00:00Z",
		},
		{
			name:     "1h_window_boundary",
			t:        time.Date(2026, 7, 10, 14, 0, 0, 0, time.UTC),
			window:   time.Hour,
			wantStar: "2026-07-10T14:00:00Z",
			wantEnd:  "2026-07-10T15:00:00Z",
		},
		{
			name:     "30m_window",
			t:        time.Date(2026, 7, 10, 14, 45, 0, 0, time.UTC),
			window:   30 * time.Minute,
			wantStar: "2026-07-10T14:30:00Z",
			wantEnd:  "2026-07-10T15:00:00Z",
		},
		{
			name:     "default_zero_window_falls_back_to_1h",
			t:        time.Date(2026, 7, 10, 14, 30, 0, 0, time.UTC),
			window:   0,
			wantStar: "2026-07-10T14:00:00Z",
			wantEnd:  "2026-07-10T15:00:00Z",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			start, end := computeWindow(tc.t, tc.window)
			require.Equal(t, tc.wantStar, start.Format(time.RFC3339))
			require.Equal(t, tc.wantEnd, end.Format(time.RFC3339))
		})
	}
}

// ---------------------------------------------------------------------------
// TestBackfillCamera_HistoricalSegments — backfill merges pre-existing pending
// segments that were never processed by the event-driven rolling merge.
// ---------------------------------------------------------------------------

func TestBackfillCamera_HistoricalSegments(t *testing.T) {
	env := newMergeTestEnv(t)
	defer env.close(t)

	bus := event.NewEventBus(16)
	cfg := config.MergeConfig{
		RollingEnabled:  boolPtr(true),
		RollingDebounce: "50ms",
		RollingWindow:   "1h",
	}
	cameraID := "backfill-cam1"
	cameras := []config.CameraConfig{{ID: cameraID}}
	r := newTestRollingCoordinatorWithCameras(env, cfg, bus, cameras)

	// Create 3 historical segments WITHOUT publishing SegmentCompleted events
	// (simulating recordings that existed before rolling merge was enabled).
	baseTime := time.Now().UTC().Truncate(time.Hour).Add(15 * time.Minute)
	for i := range 3 {
		recID := "hist-" + string(rune('a'+i))
		startedAt := baseTime.Add(time.Duration(i) * 30 * time.Second)
		createAndInsertSegment(t, env, recID, cameraID, startedAt)
	}

	// Verify all 3 are pending before backfill.
	recs, _, err := env.db.ListRecordingsWithTotal(context.Background(), model.RecordingFilter{CameraID: cameraID, Limit: 100})
	require.NoError(t, err)
	require.Len(t, recs, 3, "should have 3 pending segments before backfill")

	// Trigger backfill.
	merged, err := r.BackfillCamera(context.Background(), cameraID, false)
	require.NoError(t, err)
	require.Equal(t, 3, merged, "should merge all 3 segments")

	// Verify: should have 1 merged recording.
	recs, _, err = env.db.ListRecordingsWithTotal(context.Background(), model.RecordingFilter{CameraID: cameraID, Limit: 100})
	require.NoError(t, err)
	require.Len(t, recs, 1, "should have exactly 1 merged recording after backfill")
	require.True(t, recs[0].Merged)

	// Verify the merged file has all samples (2 per segment × 3 = 6).
	info, err := ParseSegment(recs[0].FilePath)
	require.NoError(t, err)
	require.Equal(t, 6, info.SampleCount, "merged file should contain all 3 segments' samples")
}

// ---------------------------------------------------------------------------
// TestBackfillCamera_NoPendingSegments — backfill with empty backlog is a no-op.
// ---------------------------------------------------------------------------

func TestBackfillCamera_NoPendingSegments(t *testing.T) {
	env := newMergeTestEnv(t)
	defer env.close(t)

	bus := event.NewEventBus(16)
	cfg := config.MergeConfig{RollingEnabled: boolPtr(true)}
	cameras := []config.CameraConfig{{ID: "empty-cam"}}
	r := newTestRollingCoordinatorWithCameras(env, cfg, bus, cameras)

	merged, err := r.BackfillCamera(context.Background(), "empty-cam", false)
	require.NoError(t, err)
	require.Equal(t, 0, merged, "should merge 0 segments when none are pending")
}

// ---------------------------------------------------------------------------
// TestBackfillCamera_MultipleWindows — segments spanning multiple hour windows
// land in separate buckets.
// ---------------------------------------------------------------------------

func TestBackfillCamera_MultipleWindows(t *testing.T) {
	env := newMergeTestEnv(t)
	defer env.close(t)

	bus := event.NewEventBus(16)
	cfg := config.MergeConfig{
		RollingEnabled:  boolPtr(true),
		RollingDebounce: "50ms",
		RollingWindow:   "1h",
	}
	cameraID := "multi-window-cam"
	cameras := []config.CameraConfig{{ID: cameraID}}
	r := newTestRollingCoordinatorWithCameras(env, cfg, bus, cameras)

	// Create segments in two different hours.
	hour1Base := time.Now().UTC().Truncate(time.Hour).Add(-2 * time.Hour).Add(10 * time.Minute)
	hour2Base := time.Now().UTC().Truncate(time.Hour).Add(-1 * time.Hour).Add(10 * time.Minute)

	// 2 segments in hour 1.
	for i := range 2 {
		createAndInsertSegment(t, env, "h1-"+string(rune('a'+i)), cameraID, hour1Base.Add(time.Duration(i)*30*time.Second))
	}
	// 2 segments in hour 2.
	for i := range 2 {
		createAndInsertSegment(t, env, "h2-"+string(rune('a'+i)), cameraID, hour2Base.Add(time.Duration(i)*30*time.Second))
	}

	// Backfill all.
	merged, err := r.BackfillCamera(context.Background(), cameraID, false)
	require.NoError(t, err)
	require.Equal(t, 4, merged, "should merge all 4 segments")

	// Should have 2 merged recordings (one per hour window).
	recs, _, err := env.db.ListRecordingsWithTotal(context.Background(), model.RecordingFilter{CameraID: cameraID, Limit: 100})
	require.NoError(t, err)
	require.Len(t, recs, 2, "should have 2 merged recordings (one per hour)")
	for _, rec := range recs {
		require.True(t, rec.Merged, "all recordings should be merged")
		// Each merged file should have 4 samples (2 segments × 2 samples each).
		info, err := ParseSegment(rec.FilePath)
		require.NoError(t, err)
		require.Equal(t, 4, info.SampleCount)
	}
}

// ---------------------------------------------------------------------------
// TestBackfillCamera_IncludeFailed — backfill with include_failed=true reprocesses
// previously failed segments.
// ---------------------------------------------------------------------------

func TestBackfillCamera_IncludeFailed(t *testing.T) {
	env := newMergeTestEnv(t)
	defer env.close(t)

	bus := event.NewEventBus(16)
	cfg := config.MergeConfig{
		RollingEnabled:  boolPtr(true),
		RollingDebounce: "50ms",
		RollingWindow:   "1h",
	}
	cameraID := "failed-cam"
	cameras := []config.CameraConfig{{ID: cameraID}}
	r := newTestRollingCoordinatorWithCameras(env, cfg, bus, cameras)

	// Create a segment and manually mark it as failed.
	now := time.Now().UTC().Truncate(time.Hour).Add(20 * time.Minute)
	createAndInsertSegment(t, env, "failed-rec", cameraID, now)

	// Mark it as failed.
	require.NoError(t, env.db.SetMergeStatus(context.Background(), []string{"failed-rec"}, model.MergeStatusFailed))

	// Verify it's not returned by normal pending query.
	pending, err := env.db.ListPendingSegmentsForRolling(context.Background(), cameraID, false, 0, time.Time{})
	require.NoError(t, err)
	require.Len(t, pending, 0, "failed segment should not be in pending list")

	// But it IS returned with includeFailed=true.
	withFailed, err := env.db.ListPendingSegmentsForRolling(context.Background(), cameraID, true, 0, time.Time{})
	require.NoError(t, err)
	require.Len(t, withFailed, 1, "failed segment should be included with includeFailed=true")

	// Backfill with includeFailed=true resets it to pending. With only 1 segment
	// there's nothing to merge — it's left pending (singleton fast-path removed:
	// a lone segment is not falsely marked merged). It will merge when a neighbor
	// arrives in a future backfill.
	merged, err := r.BackfillCamera(context.Background(), cameraID, true)
	require.NoError(t, err)
	require.Equal(t, 0, merged, "single segment is not merged (left pending for retry with future neighbors)")

	// Verify it's now pending (reset from failed), NOT falsely merged.
	recs, _, err := env.db.ListRecordingsWithTotal(context.Background(), model.RecordingFilter{CameraID: cameraID, Limit: 100})
	require.NoError(t, err)
	require.Len(t, recs, 1)
	require.Equal(t, model.MergeStatusPending, recs[0].MergeStatus, "should be reset to pending, not falsely merged")
}

// ---------------------------------------------------------------------------
// TestBackfillCamera_MissingFileSkipped — backfill skips segments whose files
// have been deleted (e.g. by retention cleanup) without error.
// ---------------------------------------------------------------------------

func TestBackfillCamera_MissingFileSkipped(t *testing.T) {
	env := newMergeTestEnv(t)
	defer env.close(t)

	bus := event.NewEventBus(16)
	cfg := config.MergeConfig{RollingEnabled: boolPtr(true), RollingWindow: "1h"}
	cameraID := "missing-cam"
	cameras := []config.CameraConfig{{ID: cameraID}}
	r := newTestRollingCoordinatorWithCameras(env, cfg, bus, cameras)

	// Create one good segment + one with a missing file.
	now := time.Now().UTC().Truncate(time.Hour).Add(25 * time.Minute)
	goodPath := createAndInsertSegment(t, env, "good-rec", cameraID, now)
	_ = goodPath
	// Insert a recording row pointing to a nonexistent file.
	missingRec := &model.Recording{
		ID:         "missing-rec",
		CameraID:   cameraID,
		FilePath:   "/nonexistent/path/missing.mp4",
		Format:     model.FormatH264,
		StartedAt:  now.Add(30 * time.Second),
		EndedAt:    now.Add(60 * time.Second),
		Duration:   30.0,
		FileSize:   1024,
		FrameCount: 2,
	}
	require.NoError(t, env.db.InsertRecording(context.Background(), missingRec))

	// Backfill should skip the missing file. With only 1 valid segment left,
	// there's nothing to merge — the singleton is left pending (NOT marked
	// merged, which would permanently eject it from the merge queue — see the
	// fake-merged bug fix in backfillMP4/backfillBatchFormat).
	merged, err := r.BackfillCamera(context.Background(), cameraID, false)
	require.NoError(t, err)
	require.Equal(t, 0, merged, "single valid segment is not merged (left pending for retry)")

	// Verify: both segments remain pending — neither is falsely marked merged.
	recs, _, err := env.db.ListRecordingsWithTotal(context.Background(), model.RecordingFilter{CameraID: cameraID, Limit: 100})
	require.NoError(t, err)
	for _, rec := range recs {
		require.NotEqual(t, model.MergeStatusMerged, rec.MergeStatus,
			"segment %s must not be marked merged (singleton fast-path removed)", rec.ID)
	}
}

// ---------------------------------------------------------------------------
// TestListPendingSegmentsForRolling — DB query returns correct segments.
// ---------------------------------------------------------------------------

func TestListPendingSegmentsForRolling(t *testing.T) {
	env := newMergeTestEnv(t)
	defer env.close(t)

	ctx := context.Background()
	cameraID := "list-cam"

	// Create 2 H.264 segments + 1 MJPEG + 1 timelapse (timelapse should be excluded).
	now := time.Now().UTC().Truncate(time.Hour).Add(30 * time.Minute)
	for i := range 2 {
		createAndInsertSegment(t, env, "list-"+string(rune('a'+i)), cameraID, now.Add(time.Duration(i)*30*time.Second))
	}
	// Insert a MJPEG recording (should be returned — all formats except timelapse).
	require.NoError(t, env.db.InsertRecording(ctx, &model.Recording{
		ID: "mjpeg-rec", CameraID: cameraID, FilePath: "/tmp/x", Format: model.FormatMJPEG,
		StartedAt: now, EndedAt: now.Add(30 * time.Second), Duration: 30, FrameCount: 1,
	}))
	// Insert a timelapse recording (should NOT be returned).
	require.NoError(t, env.db.InsertRecording(ctx, &model.Recording{
		ID: "tl-rec", CameraID: cameraID, FilePath: "/tmp/tl", Format: model.FormatTimelapse,
		StartedAt: now, EndedAt: now.Add(30 * time.Second), Duration: 30, FrameCount: 1,
	}))

	// Query all cameras — should return 3 (2 H.264 + 1 MJPEG), NOT timelapse.
	all, err := env.db.ListPendingSegmentsForRolling(ctx, "", false, 0, time.Time{})
	require.NoError(t, err)
	count := 0
	for _, rec := range all {
		if rec.CameraID == cameraID {
			require.NotEqual(t, model.FormatTimelapse, rec.Format,
				"timelapse should never be returned")
			count++
		}
	}
	require.Equal(t, 3, count, "should return 2 H.264 + 1 MJPEG, not timelapse")

	// Query single camera.
	single, err := env.db.ListPendingSegmentsForRolling(ctx, cameraID, false, 0, time.Time{})
	require.NoError(t, err)
	require.Len(t, single, 3)

	// Mark one as failed, verify includeFailed behavior.
	require.NoError(t, env.db.SetMergeStatus(ctx, []string{"list-a"}, model.MergeStatusFailed))
	normalPending, err := env.db.ListPendingSegmentsForRolling(ctx, cameraID, false, 0, time.Time{})
	require.NoError(t, err)
	require.Len(t, normalPending, 2, "should not include failed in normal query (1 H.264 + 1 MJPEG)")

	withFailed, err := env.db.ListPendingSegmentsForRolling(ctx, cameraID, true, 0, time.Time{})
	require.NoError(t, err)
	require.Len(t, withFailed, 3, "should include failed with includeFailed=true")

	// Test ResetFailedMergeStatus.
	affected, err := env.db.ResetFailedMergeStatus(ctx, []string{"list-a"})
	require.NoError(t, err)
	require.Equal(t, int64(1), affected, "should reset 1 failed segment")
	afterReset, err := env.db.ListPendingSegmentsForRolling(ctx, cameraID, false, 0, time.Time{})
	require.NoError(t, err)
	require.Len(t, afterReset, 3, "all 3 segments should be pending after reset")
}

// TestListPendingSegmentsForRolling_Limit verifies the startup-backfill LIMIT
// parameter caps the number of rows returned. This is the RPi-3B IO-storm guard.
func TestListPendingSegmentsForRolling_Limit(t *testing.T) {
	env := newMergeTestEnv(t)
	defer env.close(t)

	ctx := context.Background()
	cameraID := "limit-cam"
	now := time.Now().UTC().Truncate(time.Hour)

	// Create 5 segments.
	for i := range 5 {
		createAndInsertSegment(t, env, "lim-"+string(rune('a'+i)), cameraID, now.Add(time.Duration(i)*30*time.Second))
	}

	// limit=0 → all 5.
	all, err := env.db.ListPendingSegmentsForRolling(ctx, cameraID, false, 0, time.Time{})
	require.NoError(t, err)
	require.Len(t, all, 5)

	// limit=3 → only 3, and they are the oldest (ORDER BY started_at ASC).
	limited, err := env.db.ListPendingSegmentsForRolling(ctx, cameraID, false, 3, time.Time{})
	require.NoError(t, err)
	require.Len(t, limited, 3, "LIMIT should cap rows")
	require.Equal(t, "lim-a", limited[0].ID, "oldest first (ASC)")
	require.Equal(t, "lim-c", limited[2].ID)
}

// TestListPendingSegmentsForRolling_AgeFilter verifies the since parameter
// excludes segments older than the cutoff. This bounds startup backfill to
// recent segments so months of historical fragments go to the periodic merger.
func TestListPendingSegmentsForRolling_AgeFilter(t *testing.T) {
	env := newMergeTestEnv(t)
	defer env.close(t)

	ctx := context.Background()
	cameraID := "age-cam"
	now := time.Now().UTC().Truncate(time.Hour)

	// Create one recent segment and one old (5 days ago) segment.
	createAndInsertSegment(t, env, "age-recent", cameraID, now.Add(-10*time.Minute))
	createAndInsertSegment(t, env, "age-old", cameraID, now.Add(5*24*time.Hour*-1))

	// since = 1h ago → only the recent segment.
	cutoff := now.Add(-1 * time.Hour)
	recent, err := env.db.ListPendingSegmentsForRolling(ctx, cameraID, false, 0, cutoff)
	require.NoError(t, err)
	require.Len(t, recent, 1, "age filter should exclude old segments")
	require.Equal(t, "age-recent", recent[0].ID)

	// since = 7 days ago → both.
	weekAgo := now.Add(-7 * 24 * time.Hour)
	both, err := env.db.ListPendingSegmentsForRolling(ctx, cameraID, false, 0, weekAgo)
	require.NoError(t, err)
	require.Len(t, both, 2, "wide age filter includes all")

	// Combine limit + age: since=1h, limit=10 → 1 (recent only).
	combined, err := env.db.ListPendingSegmentsForRolling(ctx, cameraID, false, 10, cutoff)
	require.NoError(t, err)
	require.Len(t, combined, 1)
}

// ---------------------------------------------------------------------------
// TestBackfillMP4_HistoricalSingletonPurged — regression test for the
// "backfill loop stuck on historical singletons" production bug.
//
// Backfill queries the oldest pending segments first (ORDER BY started_at ASC).
// A lone historical segment in its own hour window can never reach the >=2
// batch threshold, so backfill kept re-querying the same stuck segments every
// cycle and never drained the queue (~8500 stuck pending in production).
//
// Fix: backfillMP4 marks singletons older than singletonPurgeAge as merged,
// retiring them from the queue. Recent singletons stay pending in case a
// neighbor arrives.
// ---------------------------------------------------------------------------

func TestBackfillMP4_HistoricalSingletonPurged(t *testing.T) {
	env := newMergeTestEnv(t)
	defer env.close(t)

	bus := event.NewEventBus(16)
	cfg := config.MergeConfig{
		RollingEnabled:  boolPtr(true),
		RollingDebounce: "50ms",
		RollingWindow:   "1h",
	}
	cameraID := "singleton-cam"
	cameras := []config.CameraConfig{{ID: cameraID}}
	r := newTestRollingCoordinatorWithCameras(env, cfg, bus, cameras)

	// A lone segment from 10 days ago — older than singletonPurgeAge (7d).
	// It lives in its own hour window with no neighbors.
	old := time.Now().UTC().Add(-10 * 24 * time.Hour).Truncate(time.Hour).Add(23 * time.Minute)
	createAndInsertSegment(t, env, "old-singleton", cameraID, old)

	// Run the rolling backfill path (same as backfillHistorical →
	// backfillCameraRecordings → backfillMP4).
	merged, err := r.BackfillCamera(context.Background(), cameraID, false)
	require.NoError(t, err)
	require.Equal(t, 1, merged, "historical singleton should be retired (counted as merged)")

	// Verify the segment is now retired from the pending queue.
	// (Merged boolean stays false — only merge_status flips to "merged",
	// since no actual merge happened. The original file is untouched.)
	recs, _, err := env.db.ListRecordingsWithTotal(context.Background(),
		model.RecordingFilter{CameraID: cameraID, Limit: 100})
	require.NoError(t, err)
	require.Len(t, recs, 1)
	require.Equal(t, model.MergeStatusMerged, recs[0].MergeStatus,
		"historical singleton should be retired (merge_status=merged)")
	require.False(t, recs[0].Merged, "singleton purge does not set Merged=true (no real merge)")

	// And it must be gone from the pending queue.
	pending, err := env.db.ListPendingSegmentsForRolling(context.Background(), cameraID, false, 0, time.Time{})
	require.NoError(t, err)
	require.Empty(t, pending, "retired singleton must leave the pending queue")
}

// ---------------------------------------------------------------------------
// TestBackfillMP4_RecentSingletonStaysPending — the counterpart guard: a
// lone segment that is NEWER than singletonPurgeAge must stay pending so a
// late-arriving neighbor can still be merged with it.
// ---------------------------------------------------------------------------

func TestBackfillMP4_RecentSingletonStaysPending(t *testing.T) {
	env := newMergeTestEnv(t)
	defer env.close(t)

	bus := event.NewEventBus(16)
	cfg := config.MergeConfig{
		RollingEnabled:  boolPtr(true),
		RollingDebounce: "50ms",
		RollingWindow:   "1h",
	}
	cameraID := "recent-singleton-cam"
	cameras := []config.CameraConfig{{ID: cameraID}}
	r := newTestRollingCoordinatorWithCameras(env, cfg, bus, cameras)

	// A lone segment from 1 hour ago — well within singletonPurgeAge (7d).
	recent := time.Now().UTC().Add(-1 * time.Hour).Truncate(time.Hour).Add(13 * time.Minute)
	createAndInsertSegment(t, env, "recent-singleton", cameraID, recent)

	merged, err := r.BackfillCamera(context.Background(), cameraID, false)
	require.NoError(t, err)
	require.Equal(t, 0, merged, "recent singleton must NOT be retired — wait for a neighbor")

	recs, _, err := env.db.ListRecordingsWithTotal(context.Background(),
		model.RecordingFilter{CameraID: cameraID, Limit: 100})
	require.NoError(t, err)
	require.Len(t, recs, 1)
	require.False(t, recs[0].Merged, "recent singleton must stay pending")
}

// ---------------------------------------------------------------------------
// TestBackfillMP4_DenseWindowStillMerges — make sure the singleton purge
// didn't break the normal multi-segment case: a dense historical window
// (>=2 segments, older than singletonPurgeAge) must still be actually merged
// into a single file, not just "retired".
// ---------------------------------------------------------------------------

func TestBackfillMP4_DenseWindowStillMerges(t *testing.T) {
	env := newMergeTestEnv(t)
	defer env.close(t)

	bus := event.NewEventBus(16)
	cfg := config.MergeConfig{
		RollingEnabled:  boolPtr(true),
		RollingDebounce: "50ms",
		RollingWindow:   "1h",
	}
	cameraID := "dense-old-cam"
	cameras := []config.CameraConfig{{ID: cameraID}}
	r := newTestRollingCoordinatorWithCameras(env, cfg, bus, cameras)

	// 3 segments in the same hour, all 10 days old.
	base := time.Now().UTC().Add(-10 * 24 * time.Hour).Truncate(time.Hour).Add(7 * time.Minute)
	for i := range 3 {
		createAndInsertSegment(t, env, "dense-"+string(rune('a'+i)), cameraID,
			base.Add(time.Duration(i)*30*time.Second))
	}

	merged, err := r.BackfillCamera(context.Background(), cameraID, false)
	require.NoError(t, err)
	require.Equal(t, 3, merged, "all 3 segments should be merged")

	// Should collapse to 1 merged recording with a real merged file.
	recs, _, err := env.db.ListRecordingsWithTotal(context.Background(),
		model.RecordingFilter{CameraID: cameraID, Limit: 100})
	require.NoError(t, err)
	require.Len(t, recs, 1, "3 segments should collapse to 1 merged recording")
	require.True(t, recs[0].Merged)
	require.NotEmpty(t, recs[0].FilePath, "dense window must produce a real merged file, not just status flip")

	// Verify the merged file actually contains the samples (2 per segment × 3).
	info, err := ParseSegment(recs[0].FilePath)
	require.NoError(t, err)
	require.Equal(t, 6, info.SampleCount)
}

// ---------------------------------------------------------------------------
// TestShouldPurgeSingleton_AgeThreshold — unit test for the age gate itself,
// so the boundary is explicit and doesn't depend on the full backfill path.
// ---------------------------------------------------------------------------

func TestShouldPurgeSingleton_AgeThreshold(t *testing.T) {
	env := newMergeTestEnv(t)
	defer env.close(t)

	bus := event.NewEventBus(16)
	cfg := config.MergeConfig{RollingEnabled: boolPtr(true)}
	r := newTestRollingCoordinatorWithCameras(env, cfg, bus, []config.CameraConfig{{ID: "c"}})

	// Empty → false (nothing to retire).
	require.False(t, r.shouldPurgeSingleton(nil))

	// Newest segment 8 days old → purge (older than 7d).
	old := []*model.Recording{{
		ID:        "x",
		StartedAt: time.Now().Add(-8 * 24 * time.Hour),
	}}
	require.True(t, r.shouldPurgeSingleton(old))

	// Newest segment 6 days old → keep (within 7d).
	recent := []*model.Recording{{
		ID:        "x",
		StartedAt: time.Now().Add(-6 * 24 * time.Hour),
	}}
	require.False(t, r.shouldPurgeSingleton(recent))

	// Mixed batch: take the NEWEST (last in ASC-sorted slice). If the newest
	// is recent, keep the whole batch even if older entries exist.
	mixed := []*model.Recording{
		{ID: "old", StartedAt: time.Now().Add(-30 * 24 * time.Hour)},
		{ID: "new", StartedAt: time.Now().Add(-1 * time.Hour)},
	}
	require.False(t, r.shouldPurgeSingleton(mixed),
		"batch with a recent newest segment must stay pending")
}

// ---------------------------------------------------------------------------
// TestBackfillHistorical_FairAcrossCameras — regression test for the backfill
// starvation bug. Old impl queried pending segments across ALL cameras in one
// SELECT with `ORDER BY camera_id, started_at ASC LIMIT N`. A camera with a
// large backlog that sorted early (e.g. cam-5xxx before cam-fxxx) consumed the
// whole N-segment budget every cycle, so cameras sorting later were never
// reached — production: cam-fa049182 (3969 pending) got zero backfill across
// 3 weeks of operation while earlier-sorting cameras stayed stuck too.
//
// Fix: backfillHistorical now enumerates rolling-enabled cameras and queries
// each camera's pending independently with a fair-share limit. This test
// seeds two cameras where the alphabetically-first camera has a HUGE backlog
// (enough to saturate any global LIMIT) and verifies the second camera STILL
// gets its segments processed in a single backfillHistorical call.
// ---------------------------------------------------------------------------

func TestBackfillHistorical_FairAcrossCameras(t *testing.T) {
	env := newMergeTestEnv(t)
	defer env.close(t)

	bus := event.NewEventBus(16)
	cfg := config.MergeConfig{
		RollingEnabled:       boolPtr(true),
		RollingDebounce:      "50ms",
		RollingWindow:        "1h",
		RollingBackfillBatch: 20, // small global budget per cycle
	}
	// Two cameras: "aaa-hog" sorts before "zzz-starved" alphabetically.
	cameras := []config.CameraConfig{{ID: "aaa-hog"}, {ID: "zzz-starved"}}
	r := newTestRollingCoordinatorWithCameras(env, cfg, bus, cameras)

	// aaa-hog: seed MANY old singletons (10 days old, each in its own hour
	// window) — far more than RollingBackfillBatch=20. The old global-LIMIT
	// impl spent its entire budget on these and never reached zzz-starved.
	old := time.Now().UTC().Add(-10 * 24 * time.Hour).Truncate(time.Hour)
	for i := range 50 {
		// Spread across distinct hours so each is a singleton window.
		createAndInsertSegment(t, env, fmt.Sprintf("hog-%d", i), "aaa-hog",
			old.Add(time.Duration(i)*time.Hour))
	}

	// zzz-starved: seed 3 segments in ONE hour window (a real mergeable batch).
	// Under the old impl this camera was never reached. Under the fix its
	// batch must be processed in the same cycle.
	starvedBase := time.Now().UTC().Add(-10 * 24 * time.Hour).Truncate(time.Hour).Add(5 * time.Minute)
	for i := range 3 {
		createAndInsertSegment(t, env, fmt.Sprintf("starved-%d", i), "zzz-starved",
			starvedBase.Add(time.Duration(i)*30*time.Second))
	}

	// Run one backfillHistorical cycle (the periodic sweep path).
	r.backfillHistorical(context.Background())

	// aaa-hog's singletons should all be retired (old + alone → purged).
	hogRecs, _, err := env.db.ListRecordingsWithTotal(context.Background(),
		model.RecordingFilter{CameraID: "aaa-hog", Limit: 100})
	require.NoError(t, err)
	require.Len(t, hogRecs, 50, "aaa-hog segments preserved (retired in place)")
	for _, rec := range hogRecs {
		require.Equal(t, model.MergeStatusMerged, rec.MergeStatus,
			"aaa-hog historical singletons must be retired")
	}

	// The critical assertion: zzz-starved's 3 segments were ACTUALLY MERGED
	// into 1 file, even though aaa-hog has 30 segments that would have
	// saturated a global LIMIT under the old impl.
	starvedRecs, _, err := env.db.ListRecordingsWithTotal(context.Background(),
		model.RecordingFilter{CameraID: "zzz-starved", Limit: 100})
	require.NoError(t, err)
	require.Len(t, starvedRecs, 1,
		"zzz-starved must be merged in the same cycle — proves fair scheduling "+
			"(old impl would have starved this camera)")
	require.True(t, starvedRecs[0].Merged)
	require.NotEmpty(t, starvedRecs[0].FilePath)
}

// ---------------------------------------------------------------------------
// TestRollingMerge_BucketSizeLimit — when an accumulating bucket approaches
// the 4 GiB MP4 mdat hard limit, the next segment should start a fresh bucket
// within the same window (rather than failing with "mdat box size exceeds
// MaxUint32" and dropping the segment from the merge queue).
//
// High-bitrate cameras (2K云台 ~1.7MB/s) hit this within ~40 min of recording
// in one window. The fix: bucketInfo now tracks mergedFileSize and mergeOneSegment
// rolls a new bucket when mergedFileSize + incoming segment > bucketSizeLimit.
// ---------------------------------------------------------------------------

func TestRollingMerge_BucketSizeLimit(t *testing.T) {
	env := newMergeTestEnv(t)
	defer env.close(t)

	bus := event.NewEventBus(16)
	cfg := config.MergeConfig{
		RollingEnabled:  boolPtr(true),
		RollingDebounce: "50ms",
		RollingWindow:   "1h",
	}
	cameraID := "size-cam"
	cameras := []config.CameraConfig{{ID: cameraID}}
	r := newTestRollingCoordinatorWithCameras(env, cfg, bus, cameras)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, r.Start(ctx))
	defer r.Stop()

	// Publish one segment to create the bucket.
	now := time.Now().UTC().Truncate(time.Hour).Add(5 * time.Minute)
	filePath1 := createAndInsertSegment(t, env, "seg-1", cameraID, now)
	publishSegmentCompleted(t, bus, cameraID, "seg-1", filePath1, "h264", now)
	waitForBucketStable(t, r, cameraID, 1, 5*time.Second)

	// Simulate the bucket having grown near the 3 GiB limit. We can't actually
	// write 3 GiB in a test, so directly set the tracked size on the bucket
	// state — this is the same field mergeOneSegment checks.
	bucketAny, ok := r.buckets.Load(cameraID)
	require.True(t, ok, "bucket should exist after first segment")
	bucket := bucketAny.(*bucketInfo)
	bucket.mu.Lock()
	bucket.mergedFileSize = bucketSizeLimit + 1 // over the limit
	bucket.mu.Unlock()

	// Publish a second segment in the SAME window. The size check should roll
	// a new bucket: segmentCount resets to 1 (the new segment alone), rather
	// than incrementing to 2 (appending to the oversized bucket).
	seg2Time := now.Add(30 * time.Second)
	filePath2 := createAndInsertSegment(t, env, "seg-2", cameraID, seg2Time)
	publishSegmentCompleted(t, bus, cameraID, "seg-2", filePath2, "h264", seg2Time)

	// Wait for the second segment to be processed. With the size-limit fix,
	// the bucket rolls and segmentCount ends at 1. Without the fix, the bucket
	// appends and segmentCount ends at 2. Poll until stable (count stops
	// changing) — we can't use waitForBucketStable(count=1) because count=1
	// is also the state BEFORE seg-2 is processed.
	deadline := time.Now().Add(5 * time.Second)
	var finalCount, finalSize int64
	for time.Now().Before(deadline) {
		bucket.mu.Lock()
		c := bucket.segmentCount
		s := bucket.mergedFileSize
		bucket.mu.Unlock()
		// seg-2 processed → either count=1 (rolled) or count=2 (appended).
		// Also wait for size to be updated from the fake 3GiB value (stat
		// after merge resets it to the real small file size).
		if (c == 1 || c == 2) && s != bucketSizeLimit+1 {
			finalCount = int64(c)
			finalSize = s
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	require.Greater(t, finalCount, int64(0), "seg-2 should have been processed")
	require.Equal(t, int64(1), finalCount,
		"oversized bucket should roll to a new bucket (segmentCount=1), not append (segmentCount=2)")
	require.Less(t, finalSize, int64(bucketSizeLimit),
		"new bucket should be well under the size limit")
}

// TestAdaptiveBatchPause locks in the disk-free% + backlog-driven pause
// scheduling. The function is the main IO-backpressure knob between the merge
// backfill and the recording pipeline; regressions here directly cause frame
// drops during backlog clearing on USB HDD.
func TestAdaptiveBatchPause(t *testing.T) {
	base := backfillBatchPauseForArch()

	cases := []struct {
		name       string
		pending    int
		diskFree   int
		wantFactor float64 // want == base * factor (fractional factors use /2 or *3/2)
	}{
		{"disk critical (<10%) overrides everything", 5000, 5, 2.0},
		{"disk critical (<10%) even with no backlog", 0, 9, 2.0},
		{"disk tight (10-20%) gentle slowdown", 100, 15, 1.5},
		{"disk tight boundary (19%)", 100, 19, 1.5},
		{"backlog large + disk ample → speed up", 3000, 50, 0.5},
		{"backlog large but disk only 31% → speed up", 3000, 31, 0.5},
		{"backlog large but disk borderline 30% → baseline", 3000, 30, 1.0},
		{"backlog small + disk ample → baseline", 100, 50, 1.0},
		{"no backlog + ample disk → baseline", 0, 80, 1.0},
		{"backlog near threshold (2000) → baseline (not >2000)", 2000, 50, 1.0},
		{"backlog just over threshold (2001) + ample → speed up", 2001, 50, 0.5},
		{"disk exactly 20% → baseline (not <20%)", 100, 20, 1.0},
		{"disk exactly 10% → tight slowdown (is <20%)", 100, 10, 1.5},
		{"disk exactly 9% → critical (is <10%)", 100, 9, 2.0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := adaptiveBatchPause(tc.pending, tc.diskFree)
			want := time.Duration(float64(base) * tc.wantFactor)
			require.Equal(t, want, got,
				"adaptiveBatchPause(pending=%d, diskFree=%d%%): got %v, want %v (base=%v × %v)",
				tc.pending, tc.diskFree, got, want, base, tc.wantFactor)
		})
	}
}
