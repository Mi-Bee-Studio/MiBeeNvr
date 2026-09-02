package merge

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
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
		nil, // no per-camera adaptive config in test
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
		nil, // no per-camera adaptive config in test
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
	require.True(t, recs[0].MergeStatus == model.MergeStatusMerged, "the recording should be marked merged")
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
	require.True(t, recs[0].MergeStatus == model.MergeStatusMerged)

	// Verify the merged file has 3x the samples of a single segment (2 each → 6 total).
	info, err := ParseSegment(recs[0].FilePath)
	require.NoError(t, err)
	require.Equal(t, 6, info.SampleCount, "merged file should contain all 3 segments' samples")
}

// waitForBucketAudio polls until the camera's bucket reaches the expected
// segment count AND audio key (the audio key disambiguates a reset bucket from
// the previous one — count alone can match stale state after a bucket break).
func waitForBucketAudio(t *testing.T, r *RollingMergeCoordinator, cameraID string, expectedCount int, expectedAudioKey string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		bucketAny, ok := r.buckets.Load(cameraID)
		if ok {
			bi := bucketAny.(*bucketInfo)
			bi.mu.Lock()
			count, key := bi.segmentCount, bi.audioKey
			bi.mu.Unlock()
			if count == expectedCount && key == expectedAudioKey {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for bucket segmentCount=%d audioKey=%q for camera %s", expectedCount, expectedAudioKey, cameraID)
}

// ---------------------------------------------------------------------------
// TestRollingMerge_AudioConfigChangeBreaksBucket — audio presence/config change
// across segments (audio_enabled toggled mid-stream) must finalize the current
// bucket and start a new one, NOT merge across the boundary. Merging across it
// trips MergeMP4Segments' mixed-audio drop policy, and the resulting video-only
// bucket poisons every subsequent append (sticky audio loss — observed live:
// "audio presence/config mismatch across segments" warning repeating every
// segment close long after all new segments carried audio).
// ---------------------------------------------------------------------------

// createAndInsertSegmentWithG711 is createAndInsertSegment with a G.711 μ-law
// audio track (2 samples) muxed in.
func createAndInsertSegmentWithG711(t *testing.T, env *mergeTestEnv, recordingID, cameraID string, startedAt time.Time) string {
	t.Helper()
	ctx := context.Background()

	tempPath, finalPath, err := env.store.CreateSegment(cameraID, "h264")
	require.NoError(t, err)

	segDir := filepath.Dir(tempPath)
	segFile := createH264SegmentWithG711Audio(t, segDir)
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

func TestRollingMerge_AudioConfigChangeBreaksBucket(t *testing.T) {
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

	cameraID := "cam-audio-boundary"
	baseTime := time.Now().UTC().Truncate(time.Hour).Add(10 * time.Minute)

	// Segment 1: video-only. Creates the first bucket.
	p1 := createAndInsertSegment(t, env, "rec-plain", cameraID, baseTime)
	publishSegmentCompleted(t, bus, cameraID, "rec-plain", p1, "h264", baseTime)
	waitForBucketAudio(t, r, cameraID, 1, "none", 5*time.Second)

	// Segment 2: audio arrives (audio_enabled toggled / codec negotiated).
	// The audio key differs from the bucket's — a new bucket must start here.
	p2 := createAndInsertSegmentWithG711(t, env, "rec-audio1", cameraID, baseTime.Add(30*time.Second))
	publishSegmentCompleted(t, bus, cameraID, "rec-audio1", p2, "h264", baseTime.Add(30*time.Second))
	audioInfo, err := ParseSegment(p2)
	require.NoError(t, err)
	audioKey := segmentAudioKey(audioInfo)
	require.NotEqual(t, "none", audioKey)
	waitForBucketAudio(t, r, cameraID, 1, audioKey, 5*time.Second)

	// Segment 3: audio again. Must append to the SECOND bucket (audio intact),
	// not trip the mixed-audio drop.
	p3 := createAndInsertSegmentWithG711(t, env, "rec-audio2", cameraID, baseTime.Add(60*time.Second))
	publishSegmentCompleted(t, bus, cameraID, "rec-audio2", p3, "h264", baseTime.Add(60*time.Second))
	waitForBucketAudio(t, r, cameraID, 2, audioKey, 5*time.Second)

	// Two merged recordings: the video-only bucket + the audio bucket.
	recs, _, err := env.db.ListRecordingsWithTotal(context.Background(), model.RecordingFilter{CameraID: cameraID, Limit: 100})
	require.NoError(t, err)
	require.Len(t, recs, 2, "audio boundary must produce two merged recordings, one per audio state")

	var plainRec, audioRec *model.Recording
	for i := range recs {
		info, err := ParseSegment(recs[i].FilePath)
		require.NoError(t, err)
		switch {
		case !info.HasAudio:
			require.Nil(t, plainRec, "more than one video-only merged recording")
			plainRec = &recs[i]
		case info.HasAudio:
			require.Nil(t, audioRec, "more than one audio merged recording")
			audioRec = &recs[i]
			// 2 G.711 samples per segment × 2 segments — audio survived the
			// rolling merge instead of being dropped at the boundary.
			require.Equal(t, 4, info.AudioSampleCount, "audio bucket must retain all audio samples")
			require.Equal(t, "g711", info.AudioCodec)
			require.True(t, info.G711MULaw, "audio bucket must retain the μ-law config")
			require.Equal(t, 4, info.SampleCount, "audio bucket must retain all video samples")
		}
	}
	require.NotNil(t, plainRec, "video-only bucket missing")
	require.NotNil(t, audioRec, "audio bucket missing")
}

// ---------------------------------------------------------------------------
// TestBackfillMP4_MixedAudioBatchSplitsRuns — a backfill batch straddling an
// audio boundary must split into audio-homogeneous runs instead of tripping
// the mixed-audio drop policy (which would strip audio from the whole output).
// ---------------------------------------------------------------------------

func TestBackfillMP4_MixedAudioBatchSplitsRuns(t *testing.T) {
	env := newMergeTestEnv(t)
	defer env.close(t)

	bus := event.NewEventBus(16)
	cfg := config.MergeConfig{RollingEnabled: boolPtr(true)}
	r := newTestRollingCoordinator(env, cfg, bus)

	cameraID := "cam-mixed-batch"
	baseTime := time.Now().UTC().Truncate(time.Hour).Add(10 * time.Minute)

	// A batch spanning the boundary: plain → audio → audio.
	p1 := createAndInsertSegment(t, env, "rec-plain", cameraID, baseTime)
	p2 := createAndInsertSegmentWithG711(t, env, "rec-a1", cameraID, baseTime.Add(30*time.Second))
	p3 := createAndInsertSegmentWithG711(t, env, "rec-a2", cameraID, baseTime.Add(60*time.Second))

	recs := []*model.Recording{
		{ID: "rec-plain", CameraID: cameraID, FilePath: p1, Format: model.FormatH264, StartedAt: baseTime, EndedAt: baseTime.Add(30 * time.Second), Duration: 30, FileSize: 1},
		{ID: "rec-a1", CameraID: cameraID, FilePath: p2, Format: model.FormatH264, StartedAt: baseTime.Add(30 * time.Second), EndedAt: baseTime.Add(60 * time.Second), Duration: 30, FileSize: 1},
		{ID: "rec-a2", CameraID: cameraID, FilePath: p3, Format: model.FormatH264, StartedAt: baseTime.Add(60 * time.Second), EndedAt: baseTime.Add(90 * time.Second), Duration: 30, FileSize: 1},
	}
	// Recording rows were already inserted by the create helpers above.

	merged, err := r.backfillCameraRecordings(context.Background(), cameraID, recs)
	require.NoError(t, err)
	require.Equal(t, 3, merged)

	// The plain segment stays standalone; the two audio segments merge together.
	dbRecs, _, err := env.db.ListRecordingsWithTotal(context.Background(), model.RecordingFilter{CameraID: cameraID, Limit: 100})
	require.NoError(t, err)
	require.Len(t, dbRecs, 2, "expected standalone plain + merged audio recordings")

	var sawPlain, sawAudio bool
	for i := range dbRecs {
		info, err := ParseSegment(dbRecs[i].FilePath)
		require.NoError(t, err)
		if info.HasAudio {
			sawAudio = true
			require.Equal(t, 4, info.AudioSampleCount, "merged audio run must keep all audio samples")
			require.Equal(t, 4, info.SampleCount)
		} else {
			sawPlain = true
			require.Equal(t, 2, info.SampleCount, "plain segment must stay standalone")
		}
	}
	require.True(t, sawPlain, "plain standalone recording missing")
	require.True(t, sawAudio, "audio merged recording missing")
}

// ---------------------------------------------------------------------------
// TestSplitRunsByCompatKey — unit test for the run splitter.
// ---------------------------------------------------------------------------

func TestSplitRunsByCompatKey(t *testing.T) {
	none := &SegmentInfo{HasAudio: false}
	g711u := &SegmentInfo{HasAudio: true, AudioCodec: "g711", G711MULaw: true, AudioTimescale: 8000}
	g711a := &SegmentInfo{HasAudio: true, AudioCodec: "g711", G711MULaw: false, AudioTimescale: 8000}
	aac := &SegmentInfo{HasAudio: true, AudioCodec: "aac", AudioTimescale: 44100, AudioConfig: []byte{0x12, 0x10}}

	mk := func(n int, f func(i int) *SegmentInfo) ([]*model.Recording, []*SegmentInfo) {
		recs := make([]*model.Recording, n)
		infos := make([]*SegmentInfo, n)
		for i := range n {
			recs[i] = &model.Recording{ID: fmt.Sprintf("r%d", i)}
			infos[i] = f(i)
		}
		return recs, infos
	}

	// none none | g711u g711u | none | aac  → 4 runs
	recs, infos := mk(6, func(i int) *SegmentInfo {
		switch i {
		case 0, 1:
			return none
		case 2, 3:
			return g711u
		case 4:
			return none
		default:
			return aac
		}
	})
	runs := splitRunsByCompatKey(recs, infos)
	require.Len(t, runs, 4)
	require.Equal(t, []int{2, 2, 1, 1}, []int{len(runs[0].infos), len(runs[1].infos), len(runs[2].infos), len(runs[3].infos)})
	require.Equal(t, segmentCompatKey(none), runs[0].keyStr)
	require.Equal(t, segmentCompatKey(g711u), runs[1].keyStr)
	require.Equal(t, segmentCompatKey(none), runs[2].keyStr)
	require.Equal(t, segmentCompatKey(aac), runs[3].keyStr)
	// A-law vs μ-law must be different keys (config bytes differ).
	require.NotEqual(t, segmentAudioKey(g711u), segmentAudioKey(g711a))

	// All-homogeneous batch → single run.
	recs, infos = mk(3, func(int) *SegmentInfo { return g711u })
	runs = splitRunsByCompatKey(recs, infos)
	require.Len(t, runs, 1)
	require.Len(t, runs[0].infos, 3)
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
	require.NotEqual(t, model.MergeStatusMerged, recs[0].MergeStatus, "should not be merged")
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
	require.True(t, recs[0].MergeStatus == model.MergeStatusMerged)

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
		require.True(t, rec.MergeStatus == model.MergeStatusMerged, "all recordings should be merged")
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
	require.Empty(t, recs[0].MergePath, "singleton purge does not produce a merge file (no merge_path)")

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
	require.NotEqual(t, model.MergeStatusMerged, recs[0].MergeStatus, "recent singleton must stay pending")
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
	require.True(t, recs[0].MergeStatus == model.MergeStatusMerged)
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
	require.True(t, starvedRecs[0].MergeStatus == model.MergeStatusMerged)
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

// TestRollingMerge_StopJoinsGoroutines is a regression test for #143:
// RollingMergeCoordinator.Start spawns 3 goroutines (eventLoop,
// backfillOnStartup, backfillLoop) and eventLoop fans out a mergeSegments
// goroutine per debounced camera. Stop must join ALL of them (via r.wg.Wait)
// so none outlive the caller and keep writing the storage tree — that was the
// root cause of the TempDir "directory not empty" flake (#143 / #125 class).
//
// We assert deterministically by inspecting goroutine stacks: after Stop, no
// live goroutine should have a frame rooted in RollingMergeCoordinator. Counting
// NumGoroutine is unreliable under -race (detector injects helper goroutines).
func TestRollingMerge_StopJoinsGoroutines(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping goroutine-leak test in short mode")
	}

	env := newMergeTestEnv(t)
	defer env.close(t)
	bus := event.NewEventBus(16)
	cfg := config.MergeConfig{
		RollingEnabled:  boolPtr(true),
		RollingDebounce: "10ms",
	}
	r := newTestRollingCoordinator(env, cfg, bus)
	require.NoError(t, r.Start(context.Background()))

	// Exercise the eventLoop + a fan-out mergeSegments goroutine by publishing
	// a segment-completed event for a (synthetic) MP4 segment. The merge will
	// fail to find the file, but the goroutine still launches — that's what we
	// want to verify gets joined.
	now := time.Now()
	publishSegmentCompleted(t, bus, "leak-cam", "rec1", "/nonexistent/path.mp4", "h264", now)
	// Let the debounce timer fire and the mergeSegments goroutine launch.
	time.Sleep(80 * time.Millisecond)

	r.Stop()

	// Poll: after Stop, no goroutine should be running our coordinator code.
	leakMarker := "merge.(*RollingMergeCoordinator)"
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
		buf := make([]byte, 1<<16)
		n := runtime.Stack(buf, true)
		if !strings.Contains(string(buf[:n]), leakMarker) {
			return // pass
		}
	}

	buf := make([]byte, 1<<18)
	n := runtime.Stack(buf, true)
	t.Errorf("goroutine leak after Stop: RollingMergeCoordinator goroutine still alive. Stacks:\n%s", buf[:n])
}

// ---------------------------------------------------------------------------
// TestRollingMerge_IgnoresSubLayerSegments — tierrec's 60s sub-layer archives
// (Layer=LayerSub) must never enter the live merge pipeline. Field bug
// 2026-09-01: the live event dispatch only filtered the timelapse format, so
// every sub segment was consumed into the main-layer bucket — splicing 480p
// frames into 2.5K merged output and deleting the sub rows/files.
// ---------------------------------------------------------------------------

func TestRollingMerge_IgnoresSubLayerSegments(t *testing.T) {
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

	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Hour).Add(5 * time.Minute)

	// A tierrec sub-layer segment: real file + row carrying Layer=LayerSub.
	subCam := "cam-sub"
	tempPath, finalPath, err := env.store.CreateSegment(subCam, "h264")
	require.NoError(t, err)
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
		ID:         "rec-sub-1",
		CameraID:   subCam,
		FilePath:   finalPath,
		Format:     model.FormatH264,
		StartedAt:  now,
		EndedAt:    now.Add(60 * time.Second),
		Duration:   60,
		FileSize:   fi.Size(),
		FrameCount: 2,
		Layer:      model.LayerSub,
	}
	require.NoError(t, env.db.InsertRecording(ctx, rec))

	// The exact event shape tierrec publishes (Layer=LayerSub).
	bus.Publish(ctx, event.TopicSegmentCompleted, event.SegmentCompleted{
		CameraID:    subCam,
		FilePath:    finalPath,
		Format:      "h264",
		Encoding:    "h264",
		StartedAt:   now.Format(time.RFC3339Nano),
		EndedAt:     time.Now().Format(time.RFC3339Nano),
		RecordingID: "rec-sub-1",
		Layer:       model.LayerSub,
	})

	// Positive control: a main-layer segment on another camera must merge —
	// proves the event loop is live, so the sub-layer silence below is the
	// filter working, not a dead loop.
	mainCam := "cam-main"
	mainPath := createAndInsertSegment(t, env, "rec-main-1", mainCam, now)
	publishSegmentCompleted(t, bus, mainCam, "rec-main-1", mainPath, "h264", now)
	waitForBucketStable(t, r, mainCam, 1, 5*time.Second)

	// Ample time past the 50ms debounce for a (wrong) sub-layer pickup.
	time.Sleep(300 * time.Millisecond)
	if _, ok := r.buckets.Load(subCam); ok {
		t.Fatal("live rolling merge must never form a bucket from sub-layer segments")
	}
	got, err := env.db.GetRecording(ctx, "rec-sub-1")
	require.NoError(t, err)
	require.Equal(t, model.MergeStatusPending, got.MergeStatus, "sub-layer row must stay pending")
	require.Equal(t, model.LayerSub, got.Layer, "sub-layer row must keep its layer")
	_, err = os.Stat(finalPath)
	require.NoError(t, err, "sub-layer file must not be consumed")
}

// ---------------------------------------------------------------------------
// TestRollingMerge_WallAxisSurvivesRepeatedAppends — 2026-09-01 field bug:
// every append re-parses the bucket file whose TL dwells are ALREADY
// compressed, so the per-merge stats read the compressed durations as wall
// and the row's duration collapsed onto the file axis (day-timeline seek
// desync, "TL 播放" broken). The bucket must accumulate the wall axis in
// memory: after N appends of dwell-heavy sparse segments, the row's duration
// keeps the full wall span while the file is compressed, and the timeline
// map's last point maps wall→file monotonically.
// ---------------------------------------------------------------------------
func TestRollingMerge_WallAxisSurvivesRepeatedAppends(t *testing.T) {
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

	cameraID := "cam-wall"
	baseTime := time.Now().UTC().Truncate(time.Hour).Add(5 * time.Minute)

	sps := []byte{0x67, 0x42, 0x00, 0x0a, 0xe2, 0x40, 0x40, 0x04, 0x00, 0x00, 0x00, 0x04, 0x00, 0x00, 0x00, 0xc8, 0x40}
	pps := []byte{0x68, 0xce, 0x38, 0x80}
	idr := []byte{0x65, 0x88, 0x80, 0x40}

	origFrame := TimelapseFrameDur
	TimelapseFrameDur = 100 * time.Millisecond
	t.Cleanup(func() { TimelapseFrameDur = origFrame })

	// 三个稀疏源段: 每段 2 个 IDR、样本时长 30s 驻留 → 段墙钟 60s、压缩后文件 ~0.2s。
	publishSparse := func(i int) {
		t.Helper()
		startedAt := baseTime.Add(time.Duration(i) * 61 * time.Second)
		// 写到 store 管理的段路径:借 createAndInsertSegment 的目录习惯,直接落 env 目录。
		path := createH264SegmentWithDurations(t, filepath.Dir(mustTempPath(t)), fmt.Sprintf("sparse-%d.mp4", i), sps, pps,
			[][]byte{idr, idr},
			[]time.Duration{30 * time.Second, 30 * time.Second})
		fi, err := os.Stat(path)
		require.NoError(t, err)
		rec := &model.Recording{
			ID:         fmt.Sprintf("rec-sparse-%d", i),
			CameraID:   cameraID,
			FilePath:   path,
			Format:     model.FormatH264,
			StartedAt:  startedAt,
			EndedAt:    startedAt.Add(60 * time.Second),
			Duration:   60,
			FileSize:   fi.Size(),
			FrameCount: 2,
		}
		require.NoError(t, env.db.InsertRecording(context.Background(), rec))
		bus.Publish(context.Background(), event.TopicSegmentCompleted, event.SegmentCompleted{
			CameraID:    cameraID,
			FilePath:    path,
			Format:      "h264",
			Encoding:    "h264",
			StartedAt:   startedAt.Format(time.RFC3339Nano),
			EndedAt:     startedAt.Add(60 * time.Second).Format(time.RFC3339Nano),
			FileSize:    fi.Size(),
			RecordingID: rec.ID,
		})
	}
	for i := range 3 {
		publishSparse(i)
		time.Sleep(300 * time.Millisecond)
	}
	waitForBucketStable(t, r, cameraID, 3, 10*time.Second)

	recs, _, err := env.db.ListRecordingsWithTotal(context.Background(), model.RecordingFilter{CameraID: cameraID, Limit: 10})
	require.NoError(t, err)
	require.Len(t, recs, 1, "one merged product")
	got := recs[0]
	full, err := env.db.GetRecording(context.Background(), got.ID)
	require.NoError(t, err)
	got = *full
	// 墙钟轴: 3 段 × 60s = 180s(允许 keyframe 对齐的小损耗)。
	t.Logf("DURATION=%v MAP=%s FILESIZE=%d FRAMES=%d", got.Duration, got.TimelineMap, got.FileSize, got.FrameCount)
	require.InDelta(t, 180.0, got.Duration, 5.0,
		"row duration must stay on the wall axis across appends, got %v", got.Duration)
	// 文件轴被压缩 ≪ 墙钟,且 map 的末点把 180s 墙钟映射到文件时长。
	var pairs [][2]float64
	require.NoError(t, json.Unmarshal([]byte(got.TimelineMap), &pairs))
	require.GreaterOrEqual(t, len(pairs), 4, "map must have per-append points: %+v", pairs)
	last := pairs[len(pairs)-1]
	require.InDelta(t, 180.0, last[0], 5.0, "map wall endpoint = row wall span: %+v", last)
	require.Less(t, last[1], 30.0, "file axis must be compressed: %+v", last)
	// 单调性。
	for i := 1; i < len(pairs); i++ {
		require.GreaterOrEqual(t, pairs[i][0], pairs[i-1][0])
		require.GreaterOrEqual(t, pairs[i][1], pairs[i-1][1])
	}
}

// mustTempPath gives a scratch directory for fixture files.
func mustTempPath(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "x")
	require.NoError(t, os.MkdirAll(filepath.Dir(dir), 0o755))
	return dir
}
