package merge

// #810: both the debounce batch path AND the per-segment bucket append fold
// and delete their source file within one debounce (~500ms), racing the
// sequential transcode queue that enqueues per segment — on a flapping H.265
// camera with a backed-up queue, every source vanished before its task ran
// (M5 production: 54 consecutive exit-254s). Segments with a pending/running
// transcode task must be DEFERRED from the dispatch entirely; the backfill
// sweep (10m cadence, min_segment_age rail) merges them once their tasks
// have finished.

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/event"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/metrics"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

func TestRollingMerge_BatchHoldsPendingTranscodeSegments(t *testing.T) {
	env := newMergeTestEnv(t)
	defer env.close(t)

	bus := event.NewEventBus(16)
	cfg := config.MergeConfig{
		RollingEnabled:  boolPtr(true),
		RollingDebounce: "50ms",
		RollingWindow:   "1h",
		TranscodeGrace:  "90s",
	}
	mt := metrics.NewMetrics()
	r := NewRollingMergeCoordinator(
		env.db, env.store,
		func() config.MergeConfig { return cfg },
		func(string) *config.MergeConfig { return nil },
		nil,
		func() []config.CameraConfig { return nil },
		mt,
		bus,
	)

	cameraID := "cam-hold"
	base := time.Now().UTC().Truncate(time.Hour).Add(5 * time.Minute)
	p1 := createAndInsertSegment(t, env, "rec-h1", cameraID, base)
	p2 := createAndInsertSegment(t, env, "rec-h2", cameraID, base.Add(2*time.Minute))

	r.pendingTranscodePaths = func(ctx context.Context, camID string) map[string]bool {
		require.Equal(t, cameraID, camID)
		return map[string]bool{p2: true}
	}

	require.NoError(t, r.Start(context.Background()))
	defer r.Stop()

	publishSegmentCompleted(t, bus, cameraID, "rec-h1", p1, "h264", base)
	publishSegmentCompleted(t, bus, cameraID, "rec-h2", p2, "h264", base.Add(2*time.Minute))

	// The unheld segment takes the per-segment bucket path (a retained bucket
	// appears); the held segment is deferred untouched — no batch, no fold.
	require.Eventually(t, func() bool {
		_, ok := r.buckets.Load(cameraID)
		return ok
	}, 10*time.Second, 100*time.Millisecond, "unheld segment must land in the bucket path")
	require.Equal(t, 0.0,
		testutil.ToFloat64(mt.RollingBucketFinalizedTotal.WithLabelValues("batch_reset")),
		"no batch merge may run while a segment's transcode task is pending")
	require.FileExists(t, p2, "deferred segment must keep its source file for the transcode task")
}

// Control side of #810: with no pending transcode tasks the same
// two-segment window must still batch-merge — the deferral must not disable
// the frequent-disconnect batch path.
func TestRollingMerge_BatchStillMergesWithoutPendingTranscode(t *testing.T) {
	env := newMergeTestEnv(t)
	defer env.close(t)

	bus := event.NewEventBus(16)
	cfg := config.MergeConfig{
		RollingEnabled:  boolPtr(true),
		RollingDebounce: "50ms",
		RollingWindow:   "1h",
	}
	mt := metrics.NewMetrics()
	r := NewRollingMergeCoordinator(
		env.db, env.store,
		func() config.MergeConfig { return cfg },
		func(string) *config.MergeConfig { return nil },
		nil,
		func() []config.CameraConfig { return nil },
		mt,
		bus,
	)
	r.pendingTranscodePaths = func(context.Context, string) map[string]bool { return nil }

	require.NoError(t, r.Start(context.Background()))
	defer r.Stop()

	cameraID := "cam-free"
	base := time.Now().UTC().Truncate(time.Hour).Add(5 * time.Minute)
	p1 := createAndInsertSegment(t, env, "rec-c1", cameraID, base)
	p2 := createAndInsertSegment(t, env, "rec-c2", cameraID, base.Add(2*time.Minute))
	publishSegmentCompleted(t, bus, cameraID, "rec-c1", p1, "h264", base)
	publishSegmentCompleted(t, bus, cameraID, "rec-c2", p2, "h264", base.Add(2*time.Minute))

	// Batch merge consumed both rows (frequent-disconnect path intact).
	waitForRecordingGone(t, env, cameraID, "rec-c1")
	waitForRecordingGone(t, env, cameraID, "rec-c2")
}

// Backfill side of #810 (review follow-up): backfillMP4's valid filter only
// dropped missing files — a deferred segment whose transcode task is still
// queued would be folded by the 10-minute sweep anyway (a deep backlog
// drains in minutes-to-tens-of-minutes, far beyond min_segment_age). The
// hold must reach the backfill path too.
func TestBackfillCamera_HoldsPendingTranscodeSegments(t *testing.T) {
	env := newMergeTestEnv(t)
	defer env.close(t)

	bus := event.NewEventBus(16)
	cfg := config.MergeConfig{RollingEnabled: boolPtr(true)}
	cameraID := "backfill-hold"
	cameras := []config.CameraConfig{{ID: cameraID}}
	r := newTestRollingCoordinatorWithCameras(env, cfg, bus, cameras)

	baseTime := time.Now().UTC().Truncate(time.Hour).Add(15 * time.Minute)
	paths := make([]string, 3)
	for i := range 3 {
		recID := "hold-" + string(rune('a'+i))
		startedAt := baseTime.Add(time.Duration(i) * 30 * time.Second)
		paths[i] = createAndInsertSegment(t, env, recID, cameraID, startedAt)
	}
	held := paths[1]

	r.pendingTranscodePaths = func(ctx context.Context, camID string) map[string]bool {
		require.Equal(t, cameraID, camID)
		return map[string]bool{held: true}
	}

	merged, err := r.BackfillCamera(context.Background(), cameraID, false)
	require.NoError(t, err)

	// The free pair merged; the held segment must survive untouched for its
	// transcode task, and its row must stay unmerged for the next sweep.
	require.FileExists(t, held, "backfill must not fold a segment whose transcode task is pending")
	recs, _, err := env.db.ListRecordingsWithTotal(context.Background(), model.RecordingFilter{CameraID: cameraID, Limit: 100})
	require.NoError(t, err)
	for _, rec := range recs {
		if rec.FilePath == held {
			require.NotEqual(t, model.MergeStatusMerged, rec.MergeStatus,
				"held segment must stay pending for the next sweep")
		}
	}
	require.GreaterOrEqual(t, merged, 1, "unheld segments must still backfill-merge")
}

// TestRollingMerge_HoldsYoungSegmentsOnTranscodeCameras pins the #810 TOCTOU
// follow-up observed on production M5 (2026-09-15 evening, cam-3bbed0e1):
// segment completed 21:57:25 → merge debounce folded it at 21:57:26 → the
// transcode task row only landed at 21:57:30 → queue found the input gone at
// 21:57:42 (cancelled, H.264 version lost). The pending-task hold cannot see
// a task that does not exist yet, so on transcode-enabled cameras segments
// younger than transcodeGraceWindow defer by AGE, independent of task
// visibility — deterministic, no ordering dependency between the two
// segment-completed subscribers.
func TestRollingMerge_HoldsYoungSegmentsOnTranscodeCameras(t *testing.T) {
	env := newMergeTestEnv(t)
	defer env.close(t)

	bus := event.NewEventBus(16)
	cfg := config.MergeConfig{
		RollingEnabled:  boolPtr(true),
		RollingDebounce: "50ms",
		RollingWindow:   "1h",
	}
	mt := metrics.NewMetrics()
	r := NewRollingMergeCoordinator(
		env.db, env.store,
		func() config.MergeConfig { return cfg },
		func(string) *config.MergeConfig { return nil },
		nil,
		func() []config.CameraConfig { return nil },
		mt,
		bus,
	)
	cameraID := "cam-young"
	r.cameraTranscodeEnabled = func(string) bool { return true }
	// The race window: NO task rows exist yet — the empty hold set is exactly
	// what the production interleaving presents to the merge.
	r.pendingTranscodePaths = func(context.Context, string) map[string]bool {
		return map[string]bool{}
	}

	now := time.Now().UTC()
	p1 := createAndInsertSegment(t, env, "rec-y1", cameraID, now.Add(-35*time.Second))
	p2 := createAndInsertSegment(t, env, "rec-y2", cameraID, now.Add(-32*time.Second))

	require.NoError(t, r.Start(context.Background()))
	defer r.Stop()

	publishSegmentCompleted(t, bus, cameraID, "rec-y1", p1, "h264", now.Add(-35*time.Second))
	publishSegmentCompleted(t, bus, cameraID, "rec-y2", p2, "h264", now.Add(-32*time.Second))

	// Young segments on a transcode camera: nothing may fold them — no batch,
	// no bucket append. The control below proves the rail EXPIRES.
	require.Never(t, func() bool {
		_, ok := r.buckets.Load(cameraID)
		return ok
	}, 5*time.Second, 100*time.Millisecond, "young segment must not reach the bucket path")
	require.FileExists(t, p1, "young segment must survive for its (not-yet-created) transcode task")
	require.FileExists(t, p2, "young segment must survive for its (not-yet-created) transcode task")
	require.Equal(t, 0.0,
		testutil.ToFloat64(mt.RollingBucketFinalizedTotal.WithLabelValues("batch_reset")),
		"no batch merge may run inside the grace window")
}

// TestRollingMerge_GraceWindowExpiresOnTranscodeCameras is the expiry
// control: segments older than the grace window fold normally on the SAME
// transcode-enabled camera — the rail buys the task-creation window, not a
// permanent hold.
func TestRollingMerge_GraceWindowExpiresOnTranscodeCameras(t *testing.T) {
	env := newMergeTestEnv(t)
	defer env.close(t)

	bus := event.NewEventBus(16)
	cfg := config.MergeConfig{
		RollingEnabled:  boolPtr(true),
		RollingDebounce: "50ms",
		RollingWindow:   "1h",
		TranscodeGrace:  "90s",
	}
	r := NewRollingMergeCoordinator(
		env.db, env.store,
		func() config.MergeConfig { return cfg },
		func(string) *config.MergeConfig { return nil },
		nil,
		func() []config.CameraConfig { return nil },
		metrics.NewMetrics(),
		bus,
	)
	cameraID := "cam-grace-old"
	r.cameraTranscodeEnabled = func(string) bool { return true }
	r.pendingTranscodePaths = func(context.Context, string) map[string]bool {
		return map[string]bool{}
	}

	// Anchor mid-PREVIOUS-hour (~45min old, same natural-hour window for
	// both): a trunc(hour)+5m anchor is young again when the test runs in
	// the first minutes of an hour (~12%/run flake, #815 review ②).
	base := time.Now().UTC().Truncate(time.Hour).Add(-45 * time.Minute)
	p1 := createAndInsertSegment(t, env, "rec-g1", cameraID, base)
	p2 := createAndInsertSegment(t, env, "rec-g2", cameraID, base.Add(2*time.Minute))

	require.NoError(t, r.Start(context.Background()))
	defer r.Stop()

	publishSegmentCompletedAt(t, bus, cameraID, "rec-g1", p1, "h264", base, base.Add(30*time.Second))
	publishSegmentCompletedAt(t, bus, cameraID, "rec-g2", p2, "h264", base.Add(2*time.Minute), base.Add(2*time.Minute+30*time.Second))

	// Old segments (fixtures end minutes ago) on the same enabled camera must
	// still batch-merge.
	require.Eventually(t, func() bool {
		return !fileExists(p1) && !fileExists(p2)
	}, 10*time.Second, 100*time.Millisecond, "expired segments must fold normally")
}

// TestBackfillCamera_HoldsYoungSegmentsOnTranscodeCameras: the backfill
// sweep has the same TOCTOU (its hold query can run before the task row
// lands) and no minimum-age filter in ListPendingSegmentsForRolling — young
// segments on transcode-enabled cameras must defer by age there too, while
// old segments on the same camera fold (rail expiry inside the same sweep).
func TestBackfillCamera_HoldsYoungSegmentsOnTranscodeCameras(t *testing.T) {
	env := newMergeTestEnv(t)
	defer env.close(t)

	bus := event.NewEventBus(16)
	cfg := config.MergeConfig{RollingEnabled: boolPtr(true)}
	cameraID := "backfill-young"
	cameras := []config.CameraConfig{{ID: cameraID}}
	r := newTestRollingCoordinatorWithCameras(env, cfg, bus, cameras)
	r.cameraTranscodeEnabled = func(string) bool { return true }
	r.pendingTranscodePaths = func(context.Context, string) map[string]bool {
		return map[string]bool{}
	}

	// Two young segments (ended ~5s ago) + two old controls anchored
	// mid-PREVIOUS-hour (same natural-hour window; the trunc(hour)+10m
	// anchor is young again early in the hour — ~21%/run flake, #815
	// review ②).
	young1 := createAndInsertSegment(t, env, "rec-by1", cameraID, time.Now().UTC().Add(-35*time.Second))
	young2 := createAndInsertSegment(t, env, "rec-by2", cameraID, time.Now().UTC().Add(-34*time.Second))
	oldBase := time.Now().UTC().Truncate(time.Hour).Add(-45 * time.Minute)
	old1 := createAndInsertSegment(t, env, "rec-bo1", cameraID, oldBase)
	old2 := createAndInsertSegment(t, env, "rec-bo2", cameraID, oldBase.Add(30*time.Second))

	_, err := r.BackfillCamera(context.Background(), cameraID, false)
	require.NoError(t, err)

	require.FileExists(t, young1, "young segment must survive the backfill sweep")
	require.FileExists(t, young2, "young segment must survive the backfill sweep")
	require.True(t, !fileExists(old1) && !fileExists(old2),
		"old segments on the same camera must fold — the rail expires")
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// TestRollingMerge_TranscodeGraceOffFoldsImmediately pins the operator
// exit (#815 review ①): merge.transcode_grace "0s"/"off" disables the age
// rail entirely — a transcode camera folds fresh segments right away,
// exactly the pre-#811 behavior, for operators who prefer it.
func TestRollingMerge_TranscodeGraceOffFoldsImmediately(t *testing.T) {
	env := newMergeTestEnv(t)
	defer env.close(t)

	bus := event.NewEventBus(16)
	cfg := config.MergeConfig{
		RollingEnabled:  boolPtr(true),
		RollingDebounce: "50ms",
		RollingWindow:   "1h",
		TranscodeGrace:  "off",
	}
	mt := metrics.NewMetrics()
	r := NewRollingMergeCoordinator(
		env.db, env.store,
		func() config.MergeConfig { return cfg },
		func(string) *config.MergeConfig { return nil },
		nil,
		func() []config.CameraConfig { return nil },
		mt,
		bus,
	)
	cameraID := "cam-grace-off"
	r.cameraTranscodeEnabled = func(string) bool { return true }
	r.pendingTranscodePaths = func(context.Context, string) map[string]bool {
		return map[string]bool{}
	}

	now := time.Now().UTC()
	p1 := createAndInsertSegment(t, env, "rec-o1", cameraID, now.Add(-35*time.Second))
	p2 := createAndInsertSegment(t, env, "rec-o2", cameraID, now.Add(-32*time.Second))

	require.NoError(t, r.Start(context.Background()))
	defer r.Stop()

	publishSegmentCompleted(t, bus, cameraID, "rec-o1", p1, "h264", now.Add(-35*time.Second))
	publishSegmentCompleted(t, bus, cameraID, "rec-o2", p2, "h264", now.Add(-32*time.Second))

	// Rail off: young segments fold straight into the batch.
	require.Eventually(t, func() bool {
		return !fileExists(p1) && !fileExists(p2)
	}, 10*time.Second, 100*time.Millisecond, "transcode_grace off must fold young segments immediately")
}
