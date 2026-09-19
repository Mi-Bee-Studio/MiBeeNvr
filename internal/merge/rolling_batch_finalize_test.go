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

// Batch-path finalize accounting (#764 follow-up, reshaped by #852): the
// batch merge paths that still produce STANDALONE outputs (the backfill
// sweep's mergeBatchMP4) drop the camera's whole retained bucket set — that
// drop must account every bucket under reason="batch_reset" (in production
// the periodic backfill runs every 10m and is the DOMINANT eviction path, so
// nvr_rolling_merge_bucket_finalized_total would undercount to uselessness
// without it). The LIVE 2+-in-one-debounce dispatch no longer takes the batch
// path at all (#852 true batch fold): it folds into the retained bucket, so
// the second half of this test pins that no reset happens there.
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

	// 2) LIVE: two segments inside one debounce window now fold into the SAME
	// retained bucket (#852) — no standalone batch product, no bucket-set drop.
	p2 := createAndInsertSegment(t, env, "rec-2", cameraID, base.Add(2*time.Minute))
	publishSegmentCompleted(t, bus, cameraID, "rec-2", p2, "h264", base.Add(2*time.Minute))
	p3 := createAndInsertSegment(t, env, "rec-3", cameraID, base.Add(3*time.Minute))
	publishSegmentCompleted(t, bus, cameraID, "rec-3", p3, "h264", base.Add(3*time.Minute))
	waitForRecordingGone(t, env, cameraID, "rec-2")
	waitForRecordingGone(t, env, cameraID, "rec-3")
	waitForBucketStable(t, r, cameraID, 3, 5*time.Second)
	require.Equal(t, 0.0, testutil.ToFloat64(mt.RollingBucketFinalizedTotal.WithLabelValues("batch_reset")),
		"live dispatches fold into the retained bucket — no batch_reset drop (#852)")

	// 3) BACKFILL: two OLD pre-existing pending segments (an earlier window,
	// ended well before the #852 fragment-hold rail's horizon) take the
	// standalone batch path (mergeBatchMP4) + bucket-set drop — which must
	// account the finalize.
	for i := range 2 {
		recID := "rec-bf-" + string(rune('0'+i))
		startedAt := base.Add(time.Duration(-50+i) * time.Minute)
		createAndInsertSegment(t, env, recID, cameraID, startedAt)
	}
	_, err := r.BackfillCamera(context.Background(), cameraID, false)
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		return testutil.ToFloat64(mt.RollingBucketFinalizedTotal.WithLabelValues("batch_reset")) >= 1.0
	}, 5*time.Second, 20*time.Millisecond,
		"the backfill batch path's bucket-set drop must account finalizes (reason=batch_reset)")
}
