package merge

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/stretchr/testify/require"
)

// BackfillCamera reset-path serialization (#788): the includeFailed reset
// (ResetFailedMergeStatus + dropBuckets) used to run WITHOUT the per-camera
// merge lock — the only merge entry point that didn't hold it. dropBuckets'
// finalize accounting reads retained-bucket fields (segmentCount/createdAt/
// lastAppend) while a concurrent mergeOneSegment writes them under bucket.mu,
// with no lock in common between the two goroutines. This stress interleaves
// live segment merges with manual backfill resets on one camera; run with
// -race (CI's test job does) — pre-fix the detector fires on the bucket
// fields, post-fix the reset serializes behind the live merges.
func TestBackfillCameraResetConcurrentWithLiveMerge(t *testing.T) {
	cfg := config.MergeConfig{
		RollingEnabled: boolPtr(true), RollingFragmentHoldS: intPtr(0), // backfill semantics test: batching off for determinism
		RollingDebounce:      "10ms",
		RollingWindow:        "1h",
		RollingBucketRetain:  2,
		RollingBucketIdleTTL: "1h",
	}
	r, env, bus := retentionTestEnv(t, cfg, nil)

	cameraID := "cam-788"
	ctx := context.Background()
	base := time.Now().UTC().Truncate(time.Hour).Add(5 * time.Minute)

	for i := range 60 {
		// A fresh failed segment each round so every BackfillCamera call
		// actually walks the reset path (failed→pending + dropBuckets) — the
		// previous round's failed row was merged away by its own backfill.
		failedID := fmt.Sprintf("rec-failed-%d", i)
		at := base.Add(time.Duration(i)*2*time.Second + time.Second)
		createAndInsertSegment(t, env, failedID, cameraID, at)
		require.NoError(t, env.db.SetMergeStatus(ctx, []string{failedID}, model.MergeStatusFailed))

		liveID := fmt.Sprintf("rec-live-%d", i)
		live := createAndInsertSegment(t, env, liveID, cameraID, at.Add(time.Second))

		// Fire the async live merge (mergeSegments → mergeOneSegment holds the
		// per-camera merge lock and writes bucket fields under bucket.mu)…
		publishSegmentCompleted(t, bus, cameraID, liveID, live, "h264", at.Add(time.Second))
		// …wait past the debounce so the merge is genuinely in-flight (parse +
		// append + bucket bookkeeping), then run the manual reset path that
		// used to skip the lock. Jitter varies the overlap phase across
		// rounds. Errors are fine (busy camera post-fix); the race detector
		// is the assertion.
		time.Sleep(time.Duration(10+i%20) * time.Millisecond)
		_, _ = r.BackfillCamera(ctx, cameraID, true)
	}
}
