package merge

import (
	"context"
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

	// Backfill with includeFailed=true should reset and merge it.
	merged, err := r.BackfillCamera(context.Background(), cameraID, true)
	require.NoError(t, err)
	require.Equal(t, 1, merged, "should merge the previously-failed segment")

	// Verify it's now merged.
	recs, _, err := env.db.ListRecordingsWithTotal(context.Background(), model.RecordingFilter{CameraID: cameraID, Limit: 100})
	require.NoError(t, err)
	require.Len(t, recs, 1)
	require.Equal(t, model.MergeStatusMerged, recs[0].MergeStatus, "should be marked merged")
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

	// Backfill should skip the missing file and merge the good one.
	merged, err := r.BackfillCamera(context.Background(), cameraID, false)
	require.NoError(t, err)
	require.Equal(t, 1, merged, "should merge only the good segment")

	// Verify: good segment merged, missing segment still pending (or gone).
	recs, _, err := env.db.ListRecordingsWithTotal(context.Background(), model.RecordingFilter{CameraID: cameraID, Limit: 100})
	require.NoError(t, err)
	// The good segment should be marked merged (status), the missing one should NOT be merged.
	hasGoodMerged := false
	hasPendingMissing := false
	for _, rec := range recs {
		if rec.ID == "good-rec" && rec.MergeStatus == model.MergeStatusMerged {
			hasGoodMerged = true
		}
		if rec.ID == "missing-rec" && rec.MergeStatus != model.MergeStatusMerged {
			hasPendingMissing = true
		}
	}
	require.True(t, hasGoodMerged, "good segment should be marked merged")
	require.True(t, hasPendingMissing, "missing-file segment should NOT be merged")
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
