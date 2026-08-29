package merge

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
)

// backfillConcurrency bounds how many cameras backfillHistorical merges in
// parallel. Each camera has its own merge lock (r.mergeLocks) so per-camera
// appends stay serialized; this bounds total disk IO across cameras.
//
// The effective value comes from merge.rolling_backfill_concurrency (config),
// which defaults to 1 on ≤2GB-RAM hosts (RPi 3B) and 3 on larger hosts — see
// config.go ApplyDefaults. Caller passes it in via getGlobalCfg(); we keep a
// package-level fallback only for tests that don't run ApplyDefaults.
const defaultBackfillConcurrency = 3

// backfillHistorical processes one batch of pending segments across all
// rolling-enabled cameras. Unlike backfillOnStartup, it has NO max_age filter
// (so it digests old backlogs beyond 72h) and processes at most
// rolling_backfill_batch segments per cycle to bound IO.
//
// Cameras are processed CONCURRENTLY (up to backfillConcurrency at once) rather
// than serially. Each camera has its own merge lock (r.mergeLocks) so appends
// are naturally serialized per-camera; running multiple cameras in parallel
// multiplies throughput by overlapping DB query + parse of one camera with the
// file IO of another.

// backfillHistorical processes one batch of pending segments across all
// rolling-enabled cameras. Unlike backfillOnStartup, it has NO max_age filter
// (so it digests old backlogs beyond 72h) and processes at most
// rolling_backfill_batch segments per cycle to bound IO.
//
// Cameras are processed CONCURRENTLY (up to backfillConcurrency at once) rather
// than serially. Each camera has its own merge lock (r.mergeLocks) so appends
// are naturally serialized per-camera; running multiple cameras in parallel
// multiplies throughput by overlapping DB query + parse of one camera with the
// file IO of another.
func (r *RollingMergeCoordinator) backfillHistorical(ctx context.Context) {
	global := r.getGlobalCfg()
	totalLimit := global.RollingBackfillBatch
	if totalLimit <= 0 {
		totalLimit = 500
	}

	// Enumerate rolling-enabled cameras and divide the per-cycle limit evenly
	// across them. The previous implementation queried pending segments across
	// ALL cameras in a single SELECT with `ORDER BY camera_id, started_at ASC
	// LIMIT N`. That ordering meant a single camera with a large backlog
	// (production: cam-fa049182 with ~4000 pending) starved every camera that
	// sorted after it — backfill never reached them at all. Per-camera queries
	// with fair share guarantee every camera gets a slice of each cycle.
	rollingCameras := make([]string, 0)
	for _, cam := range r.cameras() {
		if r.resolveRollingConfig(cam.ID).Enabled {
			rollingCameras = append(rollingCameras, cam.ID)
		}
	}
	if len(rollingCameras) == 0 {
		return
	}
	// Fair share: at least 50 per camera, more if total budget allows.
	perCamera := totalLimit / len(rollingCameras)
	if perCamera < 50 {
		perCamera = 50
	}

	// Process cameras concurrently with a bounded semaphore. Per-camera merge
	// locks (r.acquireMergeLock inside backfillMP4) guarantee no two goroutines
	// merge the same camera at once; the semaphore bounds total disk IO.
	concurrency := global.RollingBackfillConcurrency
	if concurrency <= 0 {
		// Tests may bypass ApplyDefaults; fall back to the conservative default
		// rather than panic on divide-by-zero below.
		concurrency = defaultBackfillConcurrency
	}
	if concurrency > len(rollingCameras) {
		concurrency = len(rollingCameras)
	}
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	var totalMerged atomic.Int64
	var camerasTouched atomic.Int64

	for _, cameraID := range rollingCameras {
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		go func(camID string) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-sem }()

			// No max_age filter — the periodic sweep's job is to eventually
			// clear ALL historical pending, including pre-72h backlogs the
			// startup scan deferred.
			recs, err := r.db.ListPendingSegmentsForRolling(ctx, camID, false, perCamera, time.Time{})
			if err != nil {
				rollingLogger.Warn("backfill loop: failed to list pending segments",
					"camera_id", camID, "error", err)
				return
			}
			if len(recs) == 0 {
				return
			}
			merged, err := r.backfillCameraRecordings(ctx, camID, recs)
			if err != nil {
				rollingLogger.Warn("backfill loop: failed for camera",
					"camera_id", camID, "error", err)
			}
			if merged > 0 {
				totalMerged.Add(int64(merged))
				camerasTouched.Add(1)
			}
		}(cameraID)
	}
	wg.Wait()

	merged := int(totalMerged.Load())
	touched := int(camerasTouched.Load())
	if merged > 0 || touched > 0 {
		rollingLogger.Info("backfill loop: processed historical segments",
			"segments_merged", merged, "cameras_touched", touched,
			"rolling_cameras", len(rollingCameras), "per_camera_limit", perCamera,
			"concurrency", concurrency)
	}
}

// BackfillCamera triggers an immediate rolling merge backfill for a single camera.
// Processes all pending (and optionally failed) MP4 segments. This is the handler
// for POST /api/cameras/{id}/merge/backfill.
//
// Returns the number of segments successfully merged.

// BackfillCamera triggers an immediate rolling merge backfill for a single camera.
// Processes all pending (and optionally failed) MP4 segments. This is the handler
// for POST /api/cameras/{id}/merge/backfill.
//
// Returns the number of segments successfully merged.
func (r *RollingMergeCoordinator) BackfillCamera(ctx context.Context, cameraID string, includeFailed bool) (int, error) {
	// Manual API trigger — no throttling (the user explicitly asked for it).
	recs, err := r.db.ListPendingSegmentsForRolling(ctx, cameraID, includeFailed, 0, time.Time{})
	if err != nil {
		return 0, fmt.Errorf("list pending segments: %w", err)
	}
	if len(recs) == 0 {
		return 0, nil
	}

	// If includeFailed, reset failed segments to pending first so the merge path
	// accepts them.
	if includeFailed {
		var failedIDs []string
		for _, rec := range recs {
			if rec.MergeStatus == model.MergeStatusFailed || rec.MergeStatus == model.MergeStatusIncompatible {
				failedIDs = append(failedIDs, rec.ID)
			}
		}
		if len(failedIDs) > 0 {
			if err := storage.RetryOnBusy(ctx, func() error {
				_, err := r.db.ResetFailedMergeStatus(ctx, failedIDs)
				return err
			}); err != nil {
				return 0, fmt.Errorf("reset failed status: %w", err)
			}
			// Clear the in-memory bucket state for this camera. The bucket may
			// reference recordings that were just reset (failed→pending), and
			// appending to a stale bucket would UPDATE a row that no longer
			// represents the current merged file. Starting fresh forces
			// createBucket, which is always safe.
			if old, ok := r.buckets.LoadAndDelete(cameraID); ok {
				bi := old.(*bucketInfo)
				bi.mu.Lock()
				bi.mergedFilePath = ""
				bi.mergedRecID = ""
				bi.segmentCount = 0
				bi.mu.Unlock()
			}
		}
	}

	return r.backfillCameraRecordings(ctx, cameraID, recs)
}

// ConsolidateShortRecord finds merge_quality='short' recordings for a camera
// and attempts to merge them with adjacent recordings to reach the minimum
// duration threshold.

// ConsolidateShortRecord finds merge_quality='short' recordings for a camera
// and attempts to merge them with adjacent recordings to reach the minimum
// duration threshold.
func (r *RollingMergeCoordinator) ConsolidateShortRecord(ctx context.Context, cameraID string, minDuration time.Duration) (int, error) {
	recs, err := r.db.ListShortMergedRecordings(ctx, cameraID, minDuration.Seconds())
	if err != nil {
		return 0, fmt.Errorf("list short recordings: %w", err)
	}
	if len(recs) < 2 {
		return 0, nil
	}

	// Convert to model.Recording slice and use backfillCameraRecordings.
	merged, err := r.backfillCameraRecordings(ctx, cameraID, recs)
	if err != nil {
		return 0, fmt.Errorf("consolidate: %w", err)
	}
	return merged, nil
}

// backfillCameraRecordings merges a list of recordings for a camera sequentially.
// Segments are processed in started_at order so they land in the correct window buckets.
//
// For large backlogs (thousands of segments), a short sleep is inserted every
// backfillBatchSize segments to avoid IO starvation on the recording hot path.
// This is critical for production deployments with months of unmerged history.

// backfillCameraRecordings merges a list of recordings for a camera sequentially.
// Segments are processed in started_at order so they land in the correct window buckets.
//
// For large backlogs (thousands of segments), a short sleep is inserted every
// backfillBatchSize segments to avoid IO starvation on the recording hot path.
// This is critical for production deployments with months of unmerged history.
func (r *RollingMergeCoordinator) backfillCameraRecordings(ctx context.Context, cameraID string, recs []*model.Recording) (int, error) {
	// Group recordings by format — each format uses a different merge strategy.
	mp4Recs, aviRecs, mjpegRecs := splitByFormat(recs)

	totalMerged := 0

	// Process MP4 (H.264/H.265) via per-segment rolling append.
	if len(mp4Recs) > 0 {
		n, err := r.backfillMP4(ctx, cameraID, mp4Recs)
		if err != nil {
			rollingLogger.Warn("backfill: MP4 batch failed", "camera_id", cameraID, "error", err)
		}
		totalMerged += n
	}

	// Process AVI via batch merge (window-bucketed).
	if len(aviRecs) > 0 {
		n, err := r.backfillBatchFormat(ctx, cameraID, aviRecs, "avi")
		if err != nil {
			rollingLogger.Warn("backfill: AVI batch failed", "camera_id", cameraID, "error", err)
		}
		totalMerged += n
	}

	// Process MJPEG via batch merge (window-bucketed).
	if len(mjpegRecs) > 0 {
		n, err := r.backfillBatchFormat(ctx, cameraID, mjpegRecs, "mjpeg")
		if err != nil {
			rollingLogger.Warn("backfill: MJPEG batch failed", "camera_id", cameraID, "error", err)
		}
		totalMerged += n
	}

	return totalMerged, nil
}

// splitByFormat partitions recordings into MP4 (h264/h265), AVI, and MJPEG groups.

// splitByFormat partitions recordings into MP4 (h264/h265), AVI, and MJPEG groups.
func splitByFormat(recs []*model.Recording) (mp4, avi, mjpeg []*model.Recording) {
	for _, rec := range recs {
		switch rec.Format {
		case model.FormatH264, model.FormatH265:
			mp4 = append(mp4, rec)
		case model.FormatAVI:
			avi = append(avi, rec)
		case model.FormatMJPEG:
			mjpeg = append(mjpeg, rec)
		}
	}
	return
}

// backfillMP4 processes H.264/H.265 segments via batch merge.
//
// For backfill (historical backlog), batch merge is dramatically faster than
// per-segment rolling append — it merges all segments in a window at once via
// MergeMP4Segments, instead of appending one-at-a-time to an ever-growing bucket
// (which gets slower as the bucket grows). Benchmark: batch-merge of 50 segments
// is ~10x faster than 50 sequential appends.
//
// singletonPurgeAge bounds how long a lone segment in its hour window stays
// pending before backfill gives up waiting for a neighbor and marks it merged.
//
// Background: backfill queries the oldest pending segments first
// (ORDER BY started_at ASC). Sparse historical recordings — e.g. a camera
// that only recorded a single 5-minute clip in some hour weeks ago — produce
// hour windows with only one pending segment. The >=2 batch requirement
// means these singletons can never be merged, and because they're the oldest,
// they block backfill from ever reaching the dense recent windows behind them.
// On a production tree this caused ~8500 segments stuck pending forever.
//
// Once a singleton is older than this threshold, it is effectively permanent:
// no new segment will arrive to join its window (the camera has long since
// moved on). Marking it merged lets backfill drain past it. The segment file
// is untouched — it remains a fully playable standalone recording.

// backfillMP4 processes H.264/H.265 segments via batch merge.
//
// For backfill (historical backlog), batch merge is dramatically faster than
// per-segment rolling append — it merges all segments in a window at once via
// MergeMP4Segments, instead of appending one-at-a-time to an ever-growing bucket
// (which gets slower as the bucket grows). Benchmark: batch-merge of 50 segments
// is ~10x faster than 50 sequential appends.
//
// singletonPurgeAge bounds how long a lone segment in its hour window stays
// pending before backfill gives up waiting for a neighbor and marks it merged.
//
// Background: backfill queries the oldest pending segments first
// (ORDER BY started_at ASC). Sparse historical recordings — e.g. a camera
// that only recorded a single 5-minute clip in some hour weeks ago — produce
// hour windows with only one pending segment. The >=2 batch requirement
// means these singletons can never be merged, and because they're the oldest,
// they block backfill from ever reaching the dense recent windows behind them.
// On a production tree this caused ~8500 segments stuck pending forever.
//
// Once a singleton is older than this threshold, it is effectively permanent:
// no new segment will arrive to join its window (the camera has long since
// moved on). Marking it merged lets backfill drain past it. The segment file
// is untouched — it remains a fully playable standalone recording.
const singletonPurgeAge = 7 * 24 * time.Hour

// shouldPurgeSingleton reports whether a batch of valid (on-disk) segments
// that failed the >=2 merge threshold should be marked merged and retired
// from the pending queue. Returns true only when the NEWEST segment in the
// batch is older than singletonPurgeAge — recent singletons stay pending in
// case a neighbor arrives.

// shouldPurgeSingleton reports whether a batch of valid (on-disk) segments
// that failed the >=2 merge threshold should be marked merged and retired
// from the pending queue. Returns true only when the NEWEST segment in the
// batch is older than singletonPurgeAge — recent singletons stay pending in
// case a neighbor arrives.
func (r *RollingMergeCoordinator) shouldPurgeSingleton(valid []*model.Recording) bool {
	if len(valid) == 0 {
		return false
	}
	// valid comes from recs which are ORDER BY started_at ASC, so the last
	// entry is the newest in the batch.
	newest := valid[len(valid)-1].StartedAt
	return time.Since(newest) > singletonPurgeAge
}

// markSingletonsMerged marks the given recordings as merged (without actually
// producing a merged file) and returns the count marked. Used to retire
// historical lone segments that will never gain a merge partner. Each segment
// keeps its original file_path — playback still works, it just no longer
// shows as "pending merge" in the UI.

// markSingletonsMerged marks the given recordings as merged (without actually
// producing a merged file) and returns the count marked. Used to retire
// historical lone segments that will never gain a merge partner. Each segment
// keeps its original file_path — playback still works, it just no longer
// shows as "pending merge" in the UI.
func (r *RollingMergeCoordinator) markSingletonsMerged(ctx context.Context, cameraID string, recs []*model.Recording) int {
	ids := make([]string, 0, len(recs))
	for _, rec := range recs {
		ids = append(ids, rec.ID)
	}
	if err := storage.RetryOnBusy(ctx, func() error {
		return r.db.SetMergeStatus(ctx, ids, model.MergeStatusMerged)
	}); err != nil {
		rollingLogger.Warn("backfill MP4: failed to retire historical singletons",
			"camera_id", cameraID, "count", len(ids), "error", err)
		return 0
	}
	rollingLogger.Info("backfill MP4: retired historical singletons (no neighbor arrived within purge age)",
		"camera_id", cameraID, "count", len(ids), "purge_age", singletonPurgeAge)
	return len(ids)
}

// Uses the per-batch lock release pattern to avoid blocking live events.
