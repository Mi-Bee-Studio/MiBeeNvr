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
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/event"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/metrics"
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
