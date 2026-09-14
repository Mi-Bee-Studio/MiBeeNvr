package merge

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

// Batch-path finalize accounting (#764 follow-up): the batch merge paths
// (2+ segments in one debounce dispatch, backfill batches) drop the camera's
// whole retained bucket set via r.buckets.Delete — silently, without the
// finalize metric. In production the periodic backfill runs every 10m and is
// the DOMINANT eviction path, so nvr_rolling_merge_bucket_finalized_total
// undercounts to the point of uselessness (observed: zero series for hours
// on M5 while buckets rolled hourly). Dropping the set must account every
// bucket under reason="batch_reset".
func TestRollingMerge_BatchPathAccountsFinalize(t *testing.T) {
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
	require.NoError(t, r.Start(context.Background()))
	defer r.Stop()

	cameraID := "cam-batch"
	base := time.Now().UTC().Truncate(time.Hour).Add(5 * time.Minute)

	// 1) One segment alone → rolling bucket created (retained set size 1).
	p1 := createAndInsertSegment(t, env, "rec-1", cameraID, base)
	publishSegmentCompleted(t, bus, cameraID, "rec-1", p1, "h264", base)
	waitForRecordingGone(t, env, cameraID, "rec-1")

	// 2) Two segments inside one debounce window → the batch path
	// (mergeBatchMP4) + bucket-set drop.
	p2 := createAndInsertSegment(t, env, "rec-2", cameraID, base.Add(2*time.Minute))
	publishSegmentCompleted(t, bus, cameraID, "rec-2", p2, "h264", base.Add(2*time.Minute))
	p3 := createAndInsertSegment(t, env, "rec-3", cameraID, base.Add(3*time.Minute))
	publishSegmentCompleted(t, bus, cameraID, "rec-3", p3, "h264", base.Add(3*time.Minute))
	waitForRecordingGone(t, env, cameraID, "rec-2")
	waitForRecordingGone(t, env, cameraID, "rec-3")

	// The metric increments in dropBuckets AFTER mergeBatchMP4 has deleted
	// the source rows — waitForRecordingGone can land inside that window
	// (observed as a CI flake 2026-09-14). Poll the metric itself rather
	// than asserting instantly behind the leading effect (#571 rule).
	require.Eventually(t, func() bool {
		return testutil.ToFloat64(mt.RollingBucketFinalizedTotal.WithLabelValues("batch_reset")) >= 1.0
	}, 5*time.Second, 20*time.Millisecond,
		"the batch path's bucket-set drop must account finalizes (reason=batch_reset)")
}
