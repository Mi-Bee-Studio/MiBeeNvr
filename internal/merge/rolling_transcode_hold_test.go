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
	"errors"
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

	r.pendingTranscodePaths = func(ctx context.Context, camID string) (map[string]bool, error) {
		require.Equal(t, cameraID, camID)
		return map[string]bool{p2: true}, nil
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
	r.pendingTranscodePaths = func(context.Context, string) (map[string]bool, error) {
		return nil, nil
	}

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
	// #852: batching off — this test exercises backfill semantics; default-on
	// fragment holding would defer its fresh-timestamp rows by hour position.
	cfg := config.MergeConfig{RollingEnabled: boolPtr(true), RollingFragmentHoldS: intPtr(0)}
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

	r.pendingTranscodePaths = func(ctx context.Context, camID string) (map[string]bool, error) {
		require.Equal(t, cameraID, camID)
		return map[string]bool{held: true}, nil
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
	r.pendingTranscodePaths = func(context.Context, string) (map[string]bool, error) {
		return map[string]bool{}, nil
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
	r.pendingTranscodePaths = func(context.Context, string) (map[string]bool, error) {
		return map[string]bool{}, nil
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
	r.pendingTranscodePaths = func(context.Context, string) (map[string]bool, error) {
		return map[string]bool{}, nil
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
	r.pendingTranscodePaths = func(context.Context, string) (map[string]bool, error) {
		return map[string]bool{}, nil
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

// --- #810 residual follow-up (M5 production 2026-09-16): fold-site re-check ---
//
// Production evidence (cam-3bbed0e1, flapping Xiaomi H.265): task 8538 sat
// pending in the DB from 06:45:51 while its input segment folded at 06:47:40
// (bucket-create site) and the queue cancelled the claim at 06:57:22 ("input
// vanished"). Two seconds after the fold, a neighboring dispatch's hold query
// DID see the pending set — the fold executed against a stale/empty hold
// snapshot upstream of the deletion. Whatever the upstream miss (filter query
// failure failing open, snapshot age, rail expiry mid-merge), the hold must be
// re-asserted at the fold site itself, inside the merge lock, immediately
// before the source file is deleted: never fold a path whose transcode task
// is pending/running in the DB at fold time.

func TestMergeOneSegment_FoldSiteRecheckHoldsSegment(t *testing.T) {
	env := newMergeTestEnv(t)
	defer env.close(t)

	bus := event.NewEventBus(16)
	cfg := config.MergeConfig{RollingEnabled: boolPtr(true), RollingWindow: "1h"}
	cameraID := "cam-recheck"
	r := newTestRollingCoordinatorWithCameras(env, cfg, bus, []config.CameraConfig{{ID: cameraID}})
	r.cameraTranscodeEnabled = func(string) bool { return true }

	base := time.Now().UTC().Truncate(time.Hour).Add(5 * time.Minute)
	path := createAndInsertSegment(t, env, "rec-r1", cameraID, base)
	// The dispatch filter missed the task (any upstream cause) — the fold site
	// must still see it: the task is pending in the DB RIGHT NOW.
	r.pendingTranscodePaths = func(context.Context, string) (map[string]bool, error) {
		return map[string]bool{path: true}, nil
	}

	err := r.mergeOneSegment(context.Background(), pendingSegmentInfo{
		recordingID: "rec-r1",
		filePath:    path,
		format:      "h264",
		cameraID:    cameraID,
		startedAt:   base,
		endedAt:     base.Add(30 * time.Second),
		fileSize:    1024,
	})
	require.NoError(t, err)
	require.FileExists(t, path,
		"fold site must not delete a segment whose transcode task is pending/running at fold time")
}

// A hold-query failure on a transcode-enabled camera must fail CLOSED: skip
// the fold, leave the segment for the next sweep. The pre-fix behavior failed
// open (nil hold set) and folded silently — unrecoverable for the queued task.
func TestMergeOneSegment_HoldQueryErrorFailsClosedOnTranscodeCamera(t *testing.T) {
	env := newMergeTestEnv(t)
	defer env.close(t)

	bus := event.NewEventBus(16)
	cfg := config.MergeConfig{RollingEnabled: boolPtr(true), RollingWindow: "1h"}
	cameraID := "cam-failclosed"
	r := newTestRollingCoordinatorWithCameras(env, cfg, bus, []config.CameraConfig{{ID: cameraID}})
	r.cameraTranscodeEnabled = func(string) bool { return true }

	base := time.Now().UTC().Truncate(time.Hour).Add(5 * time.Minute)
	path := createAndInsertSegment(t, env, "rec-f1", cameraID, base)
	r.pendingTranscodePaths = func(context.Context, string) (map[string]bool, error) {
		return nil, errors.New("sqlite: database is locked")
	}

	err := r.mergeOneSegment(context.Background(), pendingSegmentInfo{
		recordingID: "rec-f1",
		filePath:    path,
		format:      "h264",
		cameraID:    cameraID,
		startedAt:   base,
		endedAt:     base.Add(30 * time.Second),
		fileSize:    1024,
	})
	require.NoError(t, err)
	require.FileExists(t, path,
		"hold-query failure on a transcode camera must defer the fold (fail-closed)")
}

// Control: the same query failure on a transcode-DISABLED camera ALSO defers
// the fold — fail-closed is deliberately unconditional (the camera toggle is
// a config-snapshot derivative and must not gate the safe default). The
// segment folds on the next round once the query succeeds again.
func TestMergeOneSegment_HoldQueryErrorDefersOnDisabledCameraToo(t *testing.T) {
	env := newMergeTestEnv(t)
	defer env.close(t)

	bus := event.NewEventBus(16)
	cfg := config.MergeConfig{RollingEnabled: boolPtr(true), RollingWindow: "1h"}
	cameraID := "cam-failopen-ok"
	r := newTestRollingCoordinatorWithCameras(env, cfg, bus, []config.CameraConfig{{ID: cameraID}})
	r.cameraTranscodeEnabled = func(string) bool { return false }

	base := time.Now().UTC().Truncate(time.Hour).Add(5 * time.Minute)
	path := createAndInsertSegment(t, env, "rec-f2", cameraID, base)
	r.pendingTranscodePaths = func(context.Context, string) (map[string]bool, error) {
		return nil, errors.New("sqlite: database is locked")
	}

	err := r.mergeOneSegment(context.Background(), pendingSegmentInfo{
		recordingID: "rec-f2",
		filePath:    path,
		format:      "h264",
		cameraID:    cameraID,
		startedAt:   base,
		endedAt:     base.Add(30 * time.Second),
		fileSize:    1024,
	})
	require.NoError(t, err)
	require.FileExists(t, path,
		"fail-closed is unconditional: any camera defers the fold on a hold-query error")
}

// Batch path: the fold site re-check drops held segments from the batch — the
// held file survives, its row stays untouched, the free segments still merge.
func TestMergeBatchMP4_FoldSiteRecheckDropsHeldSegments(t *testing.T) {
	env := newMergeTestEnv(t)
	defer env.close(t)

	bus := event.NewEventBus(16)
	cfg := config.MergeConfig{RollingEnabled: boolPtr(true), RollingWindow: "1h"}
	cameraID := "cam-batch-recheck"
	r := newTestRollingCoordinatorWithCameras(env, cfg, bus, []config.CameraConfig{{ID: cameraID}})
	r.cameraTranscodeEnabled = func(string) bool { return true }

	base := time.Now().UTC().Truncate(time.Hour).Add(5 * time.Minute)
	paths := make([]string, 3)
	recs := make([]*model.Recording, 3)
	for i := range 3 {
		recID := "rec-b" + string(rune('0'+i))
		startedAt := base.Add(time.Duration(i) * 30 * time.Second)
		paths[i] = createAndInsertSegment(t, env, recID, cameraID, startedAt)
		recs[i] = &model.Recording{
			ID:        recID,
			CameraID:  cameraID,
			FilePath:  paths[i],
			Format:    model.FormatH264,
			StartedAt: startedAt,
			EndedAt:   startedAt.Add(30 * time.Second),
			Duration:  30,
		}
	}
	r.pendingTranscodePaths = func(context.Context, string) (map[string]bool, error) {
		return map[string]bool{paths[1]: true}, nil
	}

	n, err := r.mergeBatchMP4(context.Background(), cameraID, recs)
	require.NoError(t, err)
	require.Equal(t, 2, n, "the two unheld segments must merge")
	require.FileExists(t, paths[1], "held segment must survive the batch fold")
	require.True(t, !fileExists(paths[0]) && !fileExists(paths[2]),
		"unheld segments must fold normally")
}

// Wired dispatch path: a persistent hold-query failure on a transcode camera
// must stop the live fold entirely (fail-closed) — the segments stay for the
// next sweep instead of racing the transcode queue blind.
func TestRollingMerge_HoldQueryErrorDefersFold(t *testing.T) {
	env := newMergeTestEnv(t)
	defer env.close(t)

	bus := event.NewEventBus(16)
	cfg := config.MergeConfig{
		RollingEnabled:  boolPtr(true),
		RollingDebounce: "50ms",
		RollingWindow:   "1h",
		TranscodeGrace:  "off",
	}
	cameraID := "cam-dispatch-failclosed"
	r := newTestRollingCoordinatorWithCameras(env, cfg, bus, []config.CameraConfig{{ID: cameraID}})
	r.cameraTranscodeEnabled = func(string) bool { return true }
	r.pendingTranscodePaths = func(context.Context, string) (map[string]bool, error) {
		return nil, errors.New("sqlite: database is locked")
	}

	// Old segments (ended minutes ago, grace off) — every rail is expired;
	// only the fail-closed query error can save them.
	base := time.Now().UTC().Truncate(time.Hour).Add(-45 * time.Minute)
	p1 := createAndInsertSegment(t, env, "rec-d1", cameraID, base)
	p2 := createAndInsertSegment(t, env, "rec-d2", cameraID, base.Add(2*time.Minute))

	require.NoError(t, r.Start(context.Background()))
	defer r.Stop()

	publishSegmentCompleted(t, bus, cameraID, "rec-d1", p1, "h264", base)
	publishSegmentCompleted(t, bus, cameraID, "rec-d2", p2, "h264", base.Add(2*time.Minute))

	require.Never(t, func() bool {
		return !fileExists(p1) || !fileExists(p2)
	}, 5*time.Second, 100*time.Millisecond,
		"hold-query failure must defer the fold on transcode cameras (fail-closed)")
}

// --- #817 follow-up (M5 production 2026-09-16, round 2): two residual gaps ---
//
// cam-3bbed0e1 is DB-managed (absent from the yaml camera list): the task
// creator resolves it against GLOBAL transcoding (enabled) and keeps creating
// tasks, while the coordinator's per-camera-block-only lookup said "disabled"
// and turned the age rail OFF — and the fold-site re-check ran at
// mergeOneSegment ENTRY, with the merge+replace+delete taking seconds AFTER
// it (production: task pending 10:46:00.4, source deleted 10:46:04.2, the
// re-check itself ran ~2s BEFORE the task landed). Two fixes: (a) the rail
// follows the task creator's resolution via builders wiring, (b) the hold is
// re-asserted at the DELETION moment — a task landing mid-merge keeps its
// source file (the bucket output is already committed; the retained file is
// reclaimed by the orphan sweep after the task finishes).

// (b) entry re-check misses a task that lands DURING the merge — the
// deletion moment must re-assert the hold and keep the source file.
func TestMergeOneSegment_DeletionMomentRecheckKeepsFileForLateTask(t *testing.T) {
	env := newMergeTestEnv(t)
	defer env.close(t)

	bus := event.NewEventBus(16)
	cfg := config.MergeConfig{RollingEnabled: boolPtr(true), RollingWindow: "1h"}
	cameraID := "cam-late-task"
	r := newTestRollingCoordinatorWithCameras(env, cfg, bus, []config.CameraConfig{{ID: cameraID}})
	r.cameraTranscodeEnabled = func(string) bool { return true }

	base := time.Now().UTC().Truncate(time.Hour).Add(5 * time.Minute)
	path := createAndInsertSegment(t, env, "rec-l1", cameraID, base)
	// Call #1 (entry re-check): no task yet. Call #2 (deletion moment): the
	// task landed while the merge was running — exactly the production race.
	calls := 0
	r.pendingTranscodePaths = func(context.Context, string) (map[string]bool, error) {
		calls++
		if calls >= 2 {
			return map[string]bool{path: true}, nil
		}
		return map[string]bool{}, nil
	}

	err := r.mergeOneSegment(context.Background(), pendingSegmentInfo{
		recordingID: "rec-l1",
		filePath:    path,
		format:      "h264",
		cameraID:    cameraID,
		startedAt:   base,
		endedAt:     base.Add(30 * time.Second),
		fileSize:    1024,
	})
	require.NoError(t, err)
	require.FileExists(t, path,
		"a task landing mid-merge must keep its source file at the deletion moment")
}

// (a) rail semantics for DB-managed cameras: a camera absent from the yaml
// snapshot with GLOBAL transcoding enabled must defer young segments — pinned
// via the same resolution the task creator uses (builders wires
// cfg.ResolveTranscodingConfig; this test drives it through the exported
// setter).
func TestRollingMerge_HoldsYoungSegmentsOnGlobalOnlyTranscodeCameras(t *testing.T) {
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
		func() []config.CameraConfig { return nil }, // DB-managed camera: not in yaml
		mt,
		bus,
	)
	cameraID := "cam-global-only"
	// Same shape builders wires: the task creator's resolution (global
	// fallback for cameras absent from the yaml list).
	r.SetCameraTranscodeEnabled(func(string) bool { return true })
	r.pendingTranscodePaths = func(context.Context, string) (map[string]bool, error) {
		return map[string]bool{}, nil
	}

	now := time.Now().UTC()
	p1 := createAndInsertSegment(t, env, "rec-g1", cameraID, now.Add(-35*time.Second))
	p2 := createAndInsertSegment(t, env, "rec-g2", cameraID, now.Add(-32*time.Second))

	require.NoError(t, r.Start(context.Background()))
	defer r.Stop()

	publishSegmentCompleted(t, bus, cameraID, "rec-g1", p1, "h264", now.Add(-35*time.Second))
	publishSegmentCompleted(t, bus, cameraID, "rec-g2", p2, "h264", now.Add(-32*time.Second))

	require.Never(t, func() bool {
		return !fileExists(p1) || !fileExists(p2)
	}, 5*time.Second, 100*time.Millisecond,
		"young segments on a global-only transcode camera must defer (rail follows the task creator's resolution)")
}

// --- cross-engine race (M5 production 2026-09-16, 3×/6h) ---
//
// The legacy MergeManager pass and the rolling engine race over the same
// pending pool: the legacy pass merged+deleted sources and ONE SECOND later
// the rolling backfill's 20-segment run failed wholesale with ENOENT — the
// file existed at ParseSegment time and vanished before the actual merge.
// A vanished source means another engine already folded it: drop it and
// merge the survivors instead of failing the run.
func TestMergeAudioRun_ToleratesVanishedSource(t *testing.T) {
	env := newMergeTestEnv(t)
	defer env.close(t)

	bus := event.NewEventBus(16)
	cfg := config.MergeConfig{RollingEnabled: boolPtr(true), RollingWindow: "1h"}
	cameraID := "cam-vanish"
	r := newTestRollingCoordinatorWithCameras(env, cfg, bus, []config.CameraConfig{{ID: cameraID}})

	base := time.Now().UTC().Truncate(time.Hour).Add(5 * time.Minute)
	paths := make([]string, 3)
	recs := make([]*model.Recording, 3)
	infos := make([]*SegmentInfo, 3)
	for i := range 3 {
		recID := "rec-v" + string(rune('0'+i))
		startedAt := base.Add(time.Duration(i) * 30 * time.Second)
		paths[i] = createAndInsertSegment(t, env, recID, cameraID, startedAt)
		recs[i] = &model.Recording{
			ID:        recID,
			CameraID:  cameraID,
			FilePath:  paths[i],
			Format:    model.FormatH264,
			StartedAt: startedAt,
			EndedAt:   startedAt.Add(30 * time.Second),
			Duration:  30,
		}
		info, err := ParseSegment(paths[i])
		require.NoError(t, err)
		infos[i] = info
	}
	// The production race: file vanishes between ParseSegment and the merge.
	require.NoError(t, os.Remove(paths[1]))

	n, err := r.mergeAudioRun(context.Background(), cameraID, recs, infos)
	require.NoError(t, err, "a vanished source must fail the run")
	require.Equal(t, 2, n, "the two surviving segments must merge")
	require.True(t, !fileExists(paths[0]) && !fileExists(paths[2]),
		"surviving segments fold normally")
}
