package merge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"math"
	"os"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/event"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/metrics"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
)

// rollingLogger is the slog handle for the rolling merge coordinator.
var rollingLogger = slog.Default().With("component", "rolling-merge")

// backfillBatchPauseForArch returns the inter-batch pause for backfill merges.
// On ARM (RPi 3B: 4× Cortex-A53 @ 1.2GHz, USB-bound IO) we slow down to avoid
// contending with the recorder hot path; on faster hosts the default 200ms is fine.
func backfillBatchPauseForArch() time.Duration {
	if runtime.GOARCH == "arm" || runtime.GOARCH == "arm64" {
		return 500 * time.Millisecond
	}
	return 200 * time.Millisecond
}

// adaptiveBatchPause scales the inter-batch pause by the current backlog size
// and disk free space. The scheduling priority is:
//
//  1. Disk near-full (<10% free): 2× pause — protect the recording write path.
//     Recording drops frames first when the HDD head is saturated; merge must
//     yield hard. This is the regime where cleanup should also be reclaiming
//     space.
//  2. Disk getting tight (10-20% free): 1.5× pause — gentle slowdown. On USB
//     HDD, free space below ~20% correlates with increased fragmentation and
//     seek contention as the allocator scatters new segments. Slowing merge
//     here prevents the recording pipeline from being starved before the hard
//     <10% cliff is hit.
//  3. Backlog large (>2000 pending) AND disk ample (>30% free): 0.5× pause —
//     digest fragments faster. This is the "drain the backlog" regime: there's
//     plenty of headroom for merge IO to overlap with recording without
//     blocking it.
//  4. Otherwise: architecture baseline (500ms ARM / 200ms other).
//
// pendingCount is the total pending segment count (across all cameras) at the
// start of this backfill cycle; diskFree is the current free-space percentage.
func adaptiveBatchPause(pendingCount int, diskFreePercent int) time.Duration {
	base := backfillBatchPauseForArch()
	if diskFreePercent < 10 {
		return base * 2 // disk tight: slow down hard to protect writes
	}
	if diskFreePercent < 20 {
		return base * 3 / 2 // disk getting tight: gentle slowdown
	}
	if pendingCount > 2000 && diskFreePercent > 30 {
		return base / 2 // backlog large + disk ample: speed up
	}
	return base
}

// diskFreePercent returns the percentage (0-100) of free space on the storage
// volume. Returns 100 on error (fail-open: don't throttle on unknown disk state).
func (r *RollingMergeCoordinator) diskFreePercent() int {
	total, used, err := r.store.GetDiskUsage()
	if err != nil || total == 0 {
		return 100
	}
	free := total - used
	return int(free * 100 / total)
}

// checkDiskSpaceForMerge returns true if there is at least 1.1× required bytes free
// on the storage volume. Mirrors the admission check in MergeManager.processCamera
// (manager.go) so backfill cannot fill the disk. required is the estimated merged
// output size (sum of source segment sizes).
func checkDiskSpaceForMerge(store *storage.Manager, required int64) bool {
	total, used, err := store.GetDiskUsage()
	if err != nil {
		rollingLogger.Warn("backfill: cannot determine disk usage, proceeding cautiously", "error", err)
		return true
	}
	freeSpace := total - used
	need := required * 11 / 10 // 1.1× safety margin
	if freeSpace < need {
		rollingLogger.Warn("backfill: insufficient disk space, deferring batch",
			"needed", need, "free", freeSpace)
		return false
	}
	return true
}

// RollingMergeConfig holds the effective rolling merge configuration for a camera.
type RollingMergeConfig struct {
	Enabled     bool
	Debounce    time.Duration // delay after segment close before merging (batches rapid segments)
	Window      time.Duration // bucket size (default 1h = natural-hour alignment)
	MinDuration time.Duration // target minimum merged duration (default 5m); shorter → merge_quality='short'
}

// RollingMergeCoordinator is an event-driven rolling merge manager.
//
// It subscribes to TopicSegmentCompleted and merges each newly-closed segment
// into a per-camera, per-window "current bucket" file. This achieves quasi-real-time
// merge (<10s latency on USB HDD) without requiring fragmented MP4 — each merge
// operation is MergeMP4Segments([existingBucket, newSegment]) → overwrite bucket.
//
// Design:
//   - Window = natural-hour bucket (configurable). Each camera+hour gets one file.
//   - On SegmentCompleted: debounce → parse new segment → find/create bucket →
//     MergeMP4Segments([bucketOrCreate, newSegment]) → RollingReplaceRecordings.
//   - Per-camera non-blocking lock: one merge per camera at a time (same pattern
//     as MergeManager.acquireMergeLock). Never blocks the recorder.
//   - SPS/PPS change (camera reconnect with new params) → close current bucket,
//     start a new one.
//
// Benchmark data (see mp4merge_bench_test.go BenchmarkRollingMergeSimulation):
//   - Worst-case append (1h bucket + new 30s segment): ~586ms on dev machine.
//   - USB HDD throughput: 67-230 MB/s depending on NAL sizes.
//   - Target <10s latency: comfortably achievable.
type RollingMergeCoordinator struct {
	db           *storage.DB
	store        *storage.Manager
	getGlobalCfg func() config.MergeConfig
	getCameraCfg func(cameraID string) *config.MergeConfig
	cameras      func() []config.CameraConfig
	metrics      *metrics.Metrics

	mergeLocks sync.Map // map[string]*mergeLock — per-camera non-blocking mutex

	// bucketState tracks the current open bucket per camera for the active window.
	// Key = cameraID, Value = *bucketInfo. Cleared on window rollover or SPS/PPS change.
	buckets sync.Map // map[string]*bucketInfo

	eventBus  *event.EventBus
	eventCh   chan event.Event
	cancelSub context.CancelFunc
}

// bucketInfo tracks the accumulated merge state for a camera's current window.
type bucketInfo struct {
	mu             sync.Mutex
	mergedFilePath string // path to the accumulating merged file (empty = not yet created)
	mergedRecID    string // DB recording ID of the merged row (for UPDATE on next append)
	spsKey         string // SHA-256(SPS+PPS+VPS) for compatibility checking
	windowStart    time.Time
	windowEnd      time.Time
	segmentCount   int   // how many segments have been merged into this bucket
	mergedFileSize int64 // current byte size of mergedFilePath (0 if no bucket yet)
}

// bucketSizeLimit caps how large an accumulating rolling-merge bucket file can
// grow before a new bucket is rolled. MP4 mdat box size is a uint32, so once a
// bucket exceeds ~4 GiB the next append fails with "mdat box size exceeds
// MaxUint32" and the segment is lost from the merge queue. High-bitrate cameras
// (e.g. 2K云台 at ~1.7 MB/s) hit this within ~40 minutes of recording in one
// window. The 3 GiB threshold leaves headroom for one more append before the
// hard 4 GiB limit. When triggered, the current bucket is finalized and a new
// bucket starts within the same window — playback sees multiple merged files
// per hour instead of one, which is fine (the timeline UI groups by hour).
const bucketSizeLimit = 3 << 30 // 3 GiB

// NewRollingMergeCoordinator creates a new coordinator.
// It does NOT start subscribing until Start() is called.
func NewRollingMergeCoordinator(
	db *storage.DB,
	store *storage.Manager,
	getGlobalCfg func() config.MergeConfig,
	getCameraCfg func(cameraID string) *config.MergeConfig,
	cameras func() []config.CameraConfig,
	m *metrics.Metrics,
	eventBus *event.EventBus,
) *RollingMergeCoordinator {
	return &RollingMergeCoordinator{
		db:           db,
		store:        store,
		getGlobalCfg: getGlobalCfg,
		getCameraCfg: getCameraCfg,
		cameras:      cameras,
		metrics:      m,
		eventBus:     eventBus,
		eventCh:      make(chan event.Event, 128), // buffered: bursts of segment closes
	}
}

// resolveRollingConfig returns the effective rolling config for a camera.
func (r *RollingMergeCoordinator) resolveRollingConfig(cameraID string) RollingMergeConfig {
	global := r.getGlobalCfg()
	perCamera := r.getCameraCfg(cameraID)
	effective := config.ResolveMergeConfig(global, perCamera)

	cfg := RollingMergeConfig{
		Enabled:  effective.RollingEnabledValue(),
		Debounce: 5 * time.Second, // default: batches frequent disconnect segments
		Window:   time.Hour,       // default: natural-hour bucket
	}
	if effective.RollingDebounce != "" {
		if d, err := time.ParseDuration(effective.RollingDebounce); err == nil && d > 0 {
			cfg.Debounce = d
		}
	}
	if effective.RollingWindow != "" {
		if w, err := time.ParseDuration(effective.RollingWindow); err == nil && w > 0 {
			cfg.Window = w
		}
	}
	if effective.RollingMinDuration != "" {
		if d, err := time.ParseDuration(effective.RollingMinDuration); err == nil && d > 0 {
			cfg.MinDuration = d
		}
	}
	return cfg
}

// Start subscribes to SegmentCompleted events and launches the merge loop.
// Also triggers a one-shot backfill of historical pending segments for cameras
// with rolling merge enabled. Idempotent — safe to call multiple times.
func (r *RollingMergeCoordinator) Start(ctx context.Context) error {
	if r.eventBus == nil {
		return nil
	}

	ctx, r.cancelSub = context.WithCancel(ctx)
	if err := r.eventBus.Subscribe(event.TopicSegmentCompleted, r.eventCh, 0); err != nil {
		return fmt.Errorf("rolling merge: subscribe to segment.completed: %w", err)
	}

	go r.eventLoop(ctx)

	// One-shot backfill: drain historical pending segments for rolling-enabled cameras.
	// Runs asynchronously so it never blocks service startup.
	go r.backfillOnStartup(ctx)

	// Periodic backfill sweep: continues digesting historical pending that the
	// one-shot startup backfill couldn't finish (it's throttled to max_segments/
	// max_age). Without this, pending accumulates whenever fragment production
	// outpaces the startup scan + event-driven merge.
	go r.backfillLoop(ctx)

	rollingLogger.Info("rolling merge coordinator started")
	return nil
}

// backfillOnStartup scans for historical pending MP4 segments across all rolling-enabled
// cameras and merges them into window buckets. This ensures that recordings that existed
// before rolling merge was enabled get the same quasi-real-time treatment retroactively.
func (r *RollingMergeCoordinator) backfillOnStartup(ctx context.Context) {
	// Check if any camera has rolling enabled — skip entirely if none.
	hasRolling := false
	for _, cam := range r.cameras() {
		if r.resolveRollingConfig(cam.ID).Enabled {
			hasRolling = true
			break
		}
	}
	if !hasRolling {
		return
	}

	// Resolve startup backfill throttling from the global config. This bounds
	// the first-boot merge workload so an upgrade to default-on rolling merge
	// cannot trigger an IO storm on RPi 3B. Older segments beyond maxAge are
	// left for the periodic MergeManager to digest gradually.
	global := r.getGlobalCfg()
	limit := global.RollingBackfillMaxSegments
	var since time.Time
	if global.RollingBackfillMaxAge != "" {
		if d, err := time.ParseDuration(global.RollingBackfillMaxAge); err == nil && d > 0 {
			since = time.Now().UTC().Add(-d)
		}
	}

	// First, count the true total (unthrottled) for an accurate log message.
	totalRecs, err := r.db.ListPendingSegmentsForRolling(ctx, "", false, 0, time.Time{})
	if err != nil {
		rollingLogger.Warn("backfill: failed to count pending segments", "error", err)
		return
	}
	totalPending := len(totalRecs)
	if totalPending == 0 {
		return
	}

	// Query pending segments, now with throttling applied.
	recs, err := r.db.ListPendingSegmentsForRolling(ctx, "", false, limit, since)
	if err != nil {
		rollingLogger.Warn("backfill: failed to list pending segments", "error", err)
		return
	}
	if len(recs) == 0 {
		rollingLogger.Info("backfill: pending segments exist but all exceed max_age; left for periodic merge",
			"total_pending", totalPending, "max_age", global.RollingBackfillMaxAge)
		return
	}

	if throttled := totalPending - len(recs); throttled > 0 {
		rollingLogger.Info("backfill: startup scan throttled to protect disk IO",
			"processing", len(recs), "total_pending", totalPending, "deferred", throttled,
			"max_segments", limit, "max_age", global.RollingBackfillMaxAge,
			"note", "deferred segments will be merged by the periodic MergeManager")
	} else {
		rollingLogger.Info("backfill: startup scan found pending segments",
			"total", len(recs))
	}

	// Group by camera.
	byCamera := make(map[string][]*model.Recording)
	for _, rec := range recs {
		byCamera[rec.CameraID] = append(byCamera[rec.CameraID], rec)
	}

	totalMerged := 0
	for cameraID, camRecs := range byCamera {
		if ctx.Err() != nil {
			break
		}
		// Only backfill cameras with rolling enabled.
		if !r.resolveRollingConfig(cameraID).Enabled {
			continue
		}

		rollingLogger.Info("backfill: processing historical segments",
			"camera_id", cameraID, "pending_count", len(camRecs))

		merged, err := r.backfillCameraRecordings(ctx, cameraID, camRecs)
		if err != nil {
			rollingLogger.Warn("backfill: failed for camera",
				"camera_id", cameraID, "error", err)
		}
		totalMerged += merged
	}

	if totalMerged > 0 {
		rollingLogger.Info("backfill complete", "segments_merged", totalMerged)
	}
}

// backfillLoop runs a periodic sweep of historical pending segments, in
// addition to the one-shot startup backfill. The startup backfill is throttled
// (max_segments/max_age) and runs once; without a periodic sweep, historical
// pending accumulates whenever the startup backfill can't keep up (thousands of
// 30s H265 fragments/day). This closes the gap: every `rolling_backfill_interval`
// (default 10m) it processes up to `rolling_backfill_batch` (default 500) pending
// segments, using try-locks to yield to real-time events.
func (r *RollingMergeCoordinator) backfillLoop(ctx context.Context) {
	global := r.getGlobalCfg()
	interval := time.Duration(0)
	if global.RollingBackfillInterval != "" && global.RollingBackfillInterval != "0" {
		if d, err := time.ParseDuration(global.RollingBackfillInterval); err == nil && d > 0 {
			interval = d
		}
	}
	if interval <= 0 {
		rollingLogger.Info("backfill loop disabled (rolling_backfill_interval=0)")
		return
	}
	rollingLogger.Info("backfill loop started", "interval", interval)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.backfillHistorical(ctx)
		}
	}
}

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
const singletonPurgeAge = 7 * 24 * time.Hour

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
func (r *RollingMergeCoordinator) backfillMP4(ctx context.Context, cameraID string, recs []*model.Recording) (int, error) {
	const backfillBatchSize = 20 // small batches to yield lock to real-time events
	batchPause := adaptiveBatchPause(len(recs), r.diskFreePercent())

	// Group recordings by natural-hour window for batch merging.
	// Segments in different hours go into separate merge batches.
	type windowBatch struct {
		start time.Time
		recs  []*model.Recording
	}
	var windows []windowBatch
	for _, rec := range recs {
		windowStart := rec.StartedAt.UTC().Truncate(time.Hour)
		if len(windows) == 0 || !windows[len(windows)-1].start.Equal(windowStart) {
			windows = append(windows, windowBatch{start: windowStart})
		}
		windows[len(windows)-1].recs = append(windows[len(windows)-1].recs, rec)
	}

	merged := 0
	for _, win := range windows {
		if ctx.Err() != nil {
			break
		}

		// Process each window in sub-batches to limit lock hold time.
		for i := 0; i < len(win.recs); i += backfillBatchSize {
			if ctx.Err() != nil {
				break
			}
			endIdx := i + backfillBatchSize
			if endIdx > len(win.recs) {
				endIdx = len(win.recs)
			}
			batch := win.recs[i:endIdx]

			// Filter missing files.
			var valid []*model.Recording
			for _, rec := range batch {
				if _, err := os.Stat(rec.FilePath); os.IsNotExist(err) {
					continue
				}
				valid = append(valid, rec)
			}
			if len(valid) < 2 {
				// Not enough segments to merge in this batch. See the long
				// comment in backfillBatchFormat for why we do NOT mark these
				// merged unconditionally — doing so would eject recent segments
				// from the queue before their neighbors arrive.
				//
				// BUT: historical singletons (older than
				// singletonPurgeAge) are different. A segment that has sat
				// pending alone in its window for this long will essentially
				// never gain a neighbor — the camera has long since moved on
				// to other hours. Leaving them pending forever is what caused
				// the 8500-segment backlog observed in production: backfill
				// always queries the oldest pending first, these singletons
				// always fail the >=2 check, and the queue never drains. Mark
				// them merged so the next cycle progresses past them.
				if r.shouldPurgeSingleton(valid) {
					n := r.markSingletonsMerged(ctx, cameraID, valid)
					merged += n
				}
				continue
			}

			release, ok := r.acquireMergeLock(cameraID)
			if !ok {
				select {
				case <-time.After(batchPause):
				case <-ctx.Done():
				}
				continue
			}

			// Disk-space admission: abort this batch (leave segments pending) if
			// there isn't room for ~1.1× the source size. Prevents backfill from
			// filling the disk on RPi 3B.
			var estSize int64
			for _, rec := range valid {
				estSize += rec.FileSize
			}
			if !checkDiskSpaceForMerge(r.store, estSize) {
				release()
				rollingLogger.Warn("backfill MP4: disk full, deferring remaining segments",
					"camera_id", cameraID, "processed", merged, "remaining", len(recs)-merged)
				return merged, nil
			}

			n, err := r.mergeBatchMP4(ctx, cameraID, valid)
			release()
			if err != nil {
				rollingLogger.Warn("backfill MP4: batch merge failed",
					"camera_id", cameraID, "error", err)
			}
			merged += n

			rollingLogger.Info("backfill MP4 progress",
				"camera_id", cameraID, "merged", merged, "total", len(recs),
				"percent", merged*100/len(recs))
			select {
			case <-time.After(batchPause):
			case <-ctx.Done():
			}
		}
	}
	return merged, nil //nolint:nilerr // TODO(#storage-overhaul): intentional — report partial merge count on ctx cancellation rather than failing the whole backfill.
}

// mergeBatchMP4 merges a batch of MP4 segments into a single output file.
// Uses ParseSegment + MergeMP4Segments (the same as the periodic MergeManager).
// Returns the number of segments successfully merged.
func (r *RollingMergeCoordinator) mergeBatchMP4(ctx context.Context, cameraID string, recs []*model.Recording) (int, error) {
	// Parse all segments.
	infos := make([]*SegmentInfo, 0, len(recs))
	sourcePaths := make([]string, 0, len(recs))
	for _, rec := range recs {
		info, err := ParseSegment(rec.FilePath)
		if err != nil {
			rollingLogger.Warn("backfill MP4: parse failed, skipping",
				"camera_id", cameraID, "recording_id", rec.ID, "path", rec.FilePath, "error", err)
			continue
		}
		infos = append(infos, info)
		sourcePaths = append(sourcePaths, rec.FilePath)
	}
	if len(infos) < 2 {
		// Not enough parseable segments — mark only the valid ones as merged.
		for _, rec := range recs {
			if _, err := os.Stat(rec.FilePath); os.IsNotExist(err) {
				continue // skip missing files, don't mark them
			}
			if err := storage.RetryOnBusy(ctx, func() error {
				return r.db.SetMergeStatus(ctx, []string{rec.ID}, model.MergeStatusMerged)
			}); err != nil {
				rollingLogger.Warn("backfill MP4: failed to mark singleton",
					"camera_id", cameraID, "recording_id", rec.ID, "error", err)
			}
		}
		return len(recs), nil
	}

	// Create output file.
	tempPath, finalPath, err := r.store.CreateSegment(cameraID, string(recs[0].Format))
	if err != nil {
		return 0, fmt.Errorf("create output: %w", err)
	}

	// Merge.
	if err := MergeMP4Segments(ctx, infos, tempPath); err != nil {
		os.Remove(tempPath)
		// Mark these as incompatible so we don't retry forever.
		ids := make([]string, len(recs))
		for i, rec := range recs {
			ids[i] = rec.ID
		}
		_ = storage.RetryOnBusy(ctx, func() error {
			return r.db.SetMergeStatus(ctx, ids, model.MergeStatusIncompatible)
		})
		return 0, fmt.Errorf("merge: %w", err)
	}

	fi, err := os.Stat(tempPath)
	if err != nil || fi.Size() == 0 {
		os.Remove(tempPath)
		return 0, fmt.Errorf("output empty")
	}

	if err := r.store.CloseSegment(tempPath, finalPath); err != nil {
		os.Remove(tempPath)
		return 0, fmt.Errorf("finalize: %w", err)
	}

	// Compute merged metadata.
	var totalDur time.Duration
	totalFrames := 0
	for _, info := range infos {
		totalDur += info.TotalDuration
		totalFrames += info.SampleCount
	}
	durSec := math.Round(totalDur.Seconds()*1000) / 1000

	mergedRec := &model.Recording{
		ID:           strconv.FormatInt(time.Now().UnixNano(), 10),
		CameraID:     cameraID,
		FilePath:     finalPath,
		Format:       recs[0].Format,
		StartedAt:    recs[0].StartedAt,
		EndedAt:      recs[len(recs)-1].EndedAt,
		Duration:     durSec,
		FileSize:     fi.Size(),
		FrameCount:   totalFrames,
		MergeStatus:  model.MergeStatusMerged,
		MergeQuality: ComputeMergeQuality(recs[0].StartedAt, recs[len(recs)-1].EndedAt, durSec, r.resolveRollingConfig(cameraID).MinDuration.Seconds()),
	}

	ids := make([]string, len(recs))
	for i, rec := range recs {
		ids[i] = rec.ID
	}
	if err := storage.RetryOnBusy(ctx, func() error {
		return r.db.MergeAndReplaceRecordings(ctx, mergedRec, ids)
	}); err != nil {
		os.Remove(finalPath)
		return 0, fmt.Errorf("db replace: %w", err)
	}

	// Delete source files.
	for _, path := range sourcePaths {
		r.store.DeleteFile(path)
	}

	return len(recs), nil
}

// backfillBatchFormat handles AVI and MJPEG formats via window-bucketed batch merge.
// Unlike MP4 (per-segment rolling append), AVI/MJPEG use the existing batch merge
// functions (MergeAVISegments / MergeMJPEGSegments) which merge all segments in a
// window at once. This is simpler and avoids format-specific append complexities.
func (r *RollingMergeCoordinator) backfillBatchFormat(ctx context.Context, cameraID string, recs []*model.Recording, format string) (int, error) {
	const batchBatchSize = 20 // small batches to yield lock to real-time events
	batchPause := adaptiveBatchPause(len(recs), r.diskFreePercent())

	merged := 0
	for startIdx := 0; startIdx < len(recs); startIdx += batchBatchSize {
		if ctx.Err() != nil {
			break
		}
		endIdx := startIdx + batchBatchSize
		if endIdx > len(recs) {
			endIdx = len(recs)
		}
		batch := recs[startIdx:endIdx]

		// Filter out missing files.
		var valid []*model.Recording
		for _, rec := range batch {
			if _, err := os.Stat(rec.FilePath); os.IsNotExist(err) {
				continue
			}
			valid = append(valid, rec)
		}
		if len(valid) < 2 {
			// Not enough segments to merge in this batch. Leave them pending —
			// do NOT mark as merged. Marking singletons merged (the old behavior)
			// permanently ejected them from the merge queue: backfill only selects
			// pending segments, so a segment marked merged here would never be
			// retried even when adjacent segments arrive later. This produced
			// thousands of "fake merged" 30s fragments that cluttered the timeline
			// (merge_status=merged but merge_path empty — never actually merged).
			// Keeping them pending means the next backfill/periodic merge will
			// reconsider them alongside any new neighbors.
			rollingLogger.Debug("backfill: batch has <2 valid segments, leaving pending",
				"camera_id", cameraID, "format", format,
				"valid", len(valid), "missing", len(batch)-len(valid))
			continue
		}

		release, ok := r.acquireMergeLock(cameraID)
		if !ok {
			select {
			case <-time.After(batchPause):
			case <-ctx.Done():
			}
			continue
		}

		// Disk-space admission (same rationale as backfillMP4).
		var estSize int64
		for _, rec := range valid {
			estSize += rec.FileSize
		}
		if !checkDiskSpaceForMerge(r.store, estSize) {
			release()
			rollingLogger.Warn("backfill batch: disk full, deferring remaining segments",
				"camera_id", cameraID, "format", format, "processed", merged, "remaining", len(recs)-merged)
			return merged, nil
		}

		n, err := r.mergeBatchSegments(ctx, cameraID, valid, format)
		release()
		if err != nil {
			rollingLogger.Warn("backfill: batch merge failed",
				"camera_id", cameraID, "format", format, "error", err)
		}
		merged += n

		rollingLogger.Info("backfill batch progress",
			"camera_id", cameraID, "format", format,
			"merged", merged, "total", len(recs),
			"percent", merged*100/len(recs))
		select {
		case <-time.After(batchPause):
		case <-ctx.Done():
		}
	}
	return merged, nil //nolint:nilerr // TODO(#storage-overhaul): intentional — report partial merge count on ctx cancellation rather than failing the whole backfill.
}

// mergeBatchSegments delegates to the format-specific batch merge function and
// updates the DB atomically. Returns the number of source segments merged.
func (r *RollingMergeCoordinator) mergeBatchSegments(ctx context.Context, cameraID string, recs []*model.Recording, format string) (int, error) {
	var mergedRec *model.Recording
	var sourcePaths []string
	var err error

	switch model.Format(format) {
	case model.FormatAVI:
		mergedRec, sourcePaths, err = MergeAVISegments(ctx, recs, r.store, cameraID)
	case model.FormatMJPEG:
		mergedRec, sourcePaths, err = MergeMJPEGSegments(ctx, recs, r.store, cameraID)
	default:
		return 0, fmt.Errorf("unsupported batch format: %s", format)
	}
	if err != nil {
		return 0, fmt.Errorf("batch merge %s: %w", format, err)
	}

	mergedRec.MergeStatus = model.MergeStatusMerged

	// Atomic DB replace: insert merged + delete sources.
	ids := make([]string, len(recs))
	for i, rec := range recs {
		ids[i] = rec.ID
	}
	if err := storage.RetryOnBusy(ctx, func() error {
		return r.db.MergeAndReplaceRecordings(ctx, mergedRec, ids)
	}); err != nil {
		// DB failed — clean up merged file, keep sources.
		if format == string(model.FormatMJPEG) {
			os.RemoveAll(mergedRec.FilePath)
		} else {
			os.Remove(mergedRec.FilePath)
		}
		return 0, fmt.Errorf("db replace %s: %w", format, err)
	}

	// Delete source files/dirs AFTER successful DB commit.
	for _, path := range sourcePaths {
		if format == string(model.FormatMJPEG) {
			os.RemoveAll(path)
		} else {
			r.store.DeleteFile(path)
		}
	}

	rollingLogger.Info("batch merge complete",
		"camera_id", cameraID, "format", format,
		"segments", len(recs), "duration_s", mergedRec.Duration,
		"size_bytes", mergedRec.FileSize)

	return len(recs), nil
}

// Stop unsubscribes and signals the event loop to exit.
func (r *RollingMergeCoordinator) Stop() {
	if r.cancelSub != nil {
		r.cancelSub()
	}
	if r.eventBus != nil {
		r.eventBus.Unsubscribe(event.TopicSegmentCompleted, r.eventCh)
	}
}

// eventLoop drains SegmentCompleted events and dispatches per-camera merge goroutines.
// It applies debounce per camera: rapid segment closes (e.g. short segment dur with
// reconnects) get batched into a single merge.
func (r *RollingMergeCoordinator) eventLoop(ctx context.Context) {
	pendingMu := sync.Mutex{}
	pending := make(map[string][]pendingSegmentInfo) // cameraID → queued segments
	timers := make(map[string]*time.Timer)

	processCamera := func(cameraID string) {
		pendingMu.Lock()
		segs := pending[cameraID]
		delete(pending, cameraID)
		delete(timers, cameraID)
		pendingMu.Unlock()

		if len(segs) == 0 {
			return
		}

		// Launch async merge — never block the event loop.
		go r.mergeSegments(ctx, cameraID, segs)
	}

	for {
		select {
		case <-ctx.Done():
			pendingMu.Lock()
			for _, t := range timers {
				t.Stop()
			}
			pendingMu.Unlock()
			return

		case evt := <-r.eventCh:
			sc, ok := evt.Data.(event.SegmentCompleted)
			if !ok {
				continue
			}

			// Check if rolling merge is enabled for this camera.
			cfg := r.resolveRollingConfig(sc.CameraID)
			if !cfg.Enabled {
				continue
			}

			// Skip timelapse format — it has its own merge pipeline (timelapse package).
			// All other formats (h264, h265, avi, mjpeg) are handled by rolling merge.
			if sc.Format == string(model.FormatTimelapse) {
				continue
			}

			// Parse the segment's started_at time for window calculation.
			startedAt, err := time.Parse(time.RFC3339Nano, sc.StartedAt)
			if err != nil {
				// Fallback: use now.
				startedAt = time.Now()
			}

			seg := pendingSegmentInfo{
				recordingID: sc.RecordingID,
				filePath:    sc.FilePath,
				format:      sc.Format,
				cameraID:    sc.CameraID,
				startedAt:   startedAt,
				endedAt:     time.Now(),
				fileSize:    sc.FileSize,
			}

			pendingMu.Lock()
			pending[sc.CameraID] = append(pending[sc.CameraID], seg)

			// Reset or create the debounce timer.
			if existing, ok := timers[sc.CameraID]; ok {
				existing.Stop()
			}
			t := time.AfterFunc(cfg.Debounce, func() {
				processCamera(sc.CameraID)
			})
			timers[sc.CameraID] = t
			pendingMu.Unlock()
		}
	}
}

// pendingSegmentInfo carries segment metadata from the event loop to the merge worker.
type pendingSegmentInfo struct {
	recordingID string
	filePath    string
	format      string
	cameraID    string
	startedAt   time.Time
	endedAt     time.Time
	fileSize    int64
}

// mergeSegments performs the rolling merge for a batch of segments on one camera.
// It acquires a per-camera non-blocking lock; if a merge is already in progress,
// the segments are left for the next periodic merge pass (MergeManager) as a fallback.
func (r *RollingMergeCoordinator) mergeSegments(ctx context.Context, cameraID string, segs []pendingSegmentInfo) {
	// For real-time events, use BLOCKING lock acquisition instead of try-lock.
	// The backfill path uses try-lock (skips if busy), so real-time events
	// will eventually get the lock. We wait with a timeout (2 min) to avoid
	// infinite blocking if something goes wrong.
	lockDeadline := time.Now().Add(2 * time.Minute)
	var release func()
	for {
		var ok bool
		release, ok = r.acquireMergeLock(cameraID)
		if ok {
			break
		}
		if time.Now().After(lockDeadline) {
			// This is expected when periodic merge holds the lock for the same camera.
			// The rolling merge will retry on the next segment close. Demoted from WARN
			// to DEBUG to avoid log noise (was ~80/hour in production with no ill effect).
			rollingLogger.Debug("rolling merge timed out waiting for lock",
				"camera_id", cameraID, "segments", len(segs))
			return
		}
		select {
		case <-time.After(500 * time.Millisecond):
		case <-ctx.Done():
			return
		}
	}
	defer release()

	// Separate MP4 segments from AVI/MJPEG.
	var mp4Segs []pendingSegmentInfo
	var batchRecs []*model.Recording
	batchFormat := ""

	for _, seg := range segs {
		if seg.format == string(model.FormatH264) || seg.format == string(model.FormatH265) {
			mp4Segs = append(mp4Segs, seg)
		} else if seg.format == string(model.FormatAVI) || seg.format == string(model.FormatMJPEG) {
			batchFormat = seg.format
			batchRecs = append(batchRecs, &model.Recording{
				ID:        seg.recordingID,
				CameraID:  seg.cameraID,
				FilePath:  seg.filePath,
				Format:    model.Format(seg.format),
				StartedAt: seg.startedAt,
				EndedAt:   seg.endedAt,
				Duration:  seg.endedAt.Sub(seg.startedAt).Seconds(),
				FileSize:  seg.fileSize,
			})
		}
	}

	// Process MP4 segments.
	// When 2+ segments accumulated (frequent disconnect scenario), use batch merge
	// to produce ONE merged file instead of multiple tiny rolling files.
	// Single segment → per-segment append to rolling bucket (low latency).
	if len(mp4Segs) >= 2 {
		mp4Recs := make([]*model.Recording, len(mp4Segs))
		for i, seg := range mp4Segs {
			mp4Recs[i] = &model.Recording{
				ID:        seg.recordingID,
				CameraID:  seg.cameraID,
				FilePath:  seg.filePath,
				Format:    model.Format(seg.format),
				StartedAt: seg.startedAt,
				EndedAt:   seg.endedAt,
				Duration:  seg.endedAt.Sub(seg.startedAt).Seconds(),
				FileSize:  seg.fileSize,
			}
		}
		if ctx.Err() != nil {
			return
		}
		if n, err := r.mergeBatchMP4(ctx, cameraID, mp4Recs); err != nil {
			rollingLogger.Warn("rolling MP4 batch merge failed",
				"camera_id", cameraID, "segments", len(mp4Recs), "error", err)
			if r.metrics != nil {
				r.metrics.RecordMergeFailure("rolling_error")
			}
		} else {
			rollingLogger.Info("rolling MP4 batch merged",
				"camera_id", cameraID, "segments", n)
		}
	} else {
		for _, seg := range mp4Segs {
			if ctx.Err() != nil {
				return
			}
			if err := r.mergeOneSegment(ctx, seg); err != nil {
				rollingLogger.Warn("rolling merge failed for segment",
					"camera_id", cameraID, "recording_id", seg.recordingID, "error", err)
				if r.metrics != nil {
					r.metrics.RecordMergeFailure("rolling_error")
				}
			}
		}
	}

	// Process AVI/MJPEG segments via batch merge.
	if len(batchRecs) >= 2 && batchFormat != "" {
		if ctx.Err() != nil {
			return
		}
		if _, err := r.mergeBatchSegments(ctx, cameraID, batchRecs, batchFormat); err != nil {
			rollingLogger.Warn("rolling batch merge failed",
				"camera_id", cameraID, "format", batchFormat, "error", err)
			if r.metrics != nil {
				r.metrics.RecordMergeFailure("rolling_error")
			}
		}
	} else if len(batchRecs) == 1 && batchFormat != "" {
		// Singleton — just mark as merged (no merge needed).
		if err := storage.RetryOnBusy(ctx, func() error {
			return r.db.SetMergeStatus(ctx, []string{batchRecs[0].ID}, model.MergeStatusMerged)
		}); err != nil {
			rollingLogger.Warn("failed to mark singleton as merged",
				"camera_id", cameraID, "recording_id", batchRecs[0].ID, "error", err)
		}
	}
}

// acquireMergeLock attempts a non-blocking per-camera lock.
// Returns a release func and true on success, nil/false if locked.
func (r *RollingMergeCoordinator) acquireMergeLock(cameraID string) (release func(), ok bool) {
	lock := &mergeLock{}
	actual, _ := r.mergeLocks.LoadOrStore(cameraID, lock)
	l := actual.(*mergeLock)
	if !l.mu.TryLock() {
		return nil, false
	}
	return func() { l.mu.Unlock() }, true
}

// mergeOneSegment merges a single segment into the camera's current window bucket.
func (r *RollingMergeCoordinator) mergeOneSegment(ctx context.Context, seg pendingSegmentInfo) error {
	mergeStart := time.Now()

	// Parse the new segment (moov-only, skips mdat).
	newInfo, err := ParseSegment(seg.filePath)
	if err != nil {
		return fmt.Errorf("parse new segment: %w", err)
	}

	// Compute SPS/PPS compatibility key.
	h := sha256.New()
	h.Write(newInfo.SPS)
	h.Write(newInfo.PPS)
	h.Write(newInfo.VPS)
	spsKey := hex.EncodeToString(h.Sum(nil))

	// Compute the window for this segment (natural-hour or configured window).
	cfg := r.resolveRollingConfig(seg.cameraID)
	windowStart, windowEnd := computeWindow(seg.startedAt, cfg.Window)

	// Get or create the bucket for this camera.
	bucketAny, _ := r.buckets.LoadOrStore(seg.cameraID, &bucketInfo{
		windowStart: windowStart,
		windowEnd:   windowEnd,
	})
	bucket := bucketAny.(*bucketInfo)

	bucket.mu.Lock()
	defer bucket.mu.Unlock()

	// Check for window rollover, SPS/PPS change, or size limit → close old
	// bucket, start new.
	needNewBucket := false
	if bucket.mergedFilePath != "" {
		if !seg.startedAt.Before(bucket.windowEnd) && !seg.startedAt.Equal(bucket.windowStart) {
			// Segment is in a new window — the old bucket is complete.
			rollingLogger.Debug("window rollover, finalizing bucket",
				"camera_id", seg.cameraID,
				"old_window_end", bucket.windowEnd,
				"new_window_start", windowStart)
			needNewBucket = true
		} else if bucket.spsKey != spsKey {
			// Codec params changed (camera reconnect) — incompatible merge.
			rollingLogger.Info("SPS/PPS changed, starting new bucket",
				"camera_id", seg.cameraID,
				"old_key", bucket.spsKey[:8],
				"new_key", spsKey[:8])
			needNewBucket = true
		} else if bucket.mergedFileSize > 0 && bucket.mergedFileSize+newInfo.MdatSize > bucketSizeLimit {
			// Bucket approaching the 4 GiB MP4 mdat hard limit. Finalize it
			// and start a fresh bucket within the same window. Without this,
			// high-bitrate cameras (2K云台 ~1.7MB/s → 6GB/hour) accumulate
			// until MergeMP4Segments returns "mdat box size exceeds MaxUint32"
			// and the segment is lost from the merge queue.
			rollingLogger.Info("bucket size limit reached, starting new bucket",
				"camera_id", seg.cameraID,
				"bucket_size_mb", bucket.mergedFileSize>>20,
				"new_segment_mb", newInfo.MdatSize>>20,
				"limit_mb", bucketSizeLimit>>20,
				"segment_count", bucket.segmentCount)
			needNewBucket = true
		}
	}

	if needNewBucket {
		// The old bucket file is already finalized in the DB (each append updates it).
		// Just reset the in-memory state to start fresh.
		bucket.mergedFilePath = ""
		bucket.mergedRecID = ""
		bucket.spsKey = ""
		bucket.segmentCount = 0
		bucket.mergedFileSize = 0
		bucket.windowStart = windowStart
		bucket.windowEnd = windowEnd
	}

	// Perform the merge.
	var outputPath string
	var mergedRecID string

	if bucket.mergedFilePath == "" {
		// First segment in this bucket — create the bucket file by merging
		// the single segment (this normalizes it into the bucket format and
		// creates the DB row that future appends will UPDATE).
		outputPath, mergedRecID, err = r.createBucket(ctx, seg, newInfo)
	} else {
		// Append to existing bucket: merge [bucketFile + newSegment].
		outputPath, mergedRecID, err = r.appendToBucket(ctx, seg, newInfo, bucket)
	}

	if err != nil {
		return err
	}

	// Update bucket state.
	bucket.mergedFilePath = outputPath
	bucket.mergedRecID = mergedRecID
	bucket.spsKey = spsKey
	bucket.segmentCount++
	// Track merged file size for the bucketSizeLimit check on the next append.
	// One stat per merged segment is cheap (the file was just written and its
	// inode is hot in cache), and avoids the need to thread size through every
	// create/append return path.
	if fi, statErr := os.Stat(outputPath); statErr == nil {
		bucket.mergedFileSize = fi.Size()
	} else {
		bucket.mergedFileSize = 0 // unknown — size check will be skipped next cycle
	}

	// Record metrics.
	if r.metrics != nil {
		r.metrics.RecordMergeSuccess(time.Since(mergeStart), newInfo.MdatSize)
		// Rolling-specific metrics: latency from segment close to merge complete.
		latency := time.Since(seg.endedAt)
		r.metrics.RecordRollingMergeLatency(seg.cameraID, latency)
		r.metrics.UpdateRollingMergeBucketSegments(seg.cameraID, bucket.segmentCount)
	}

	rollingLogger.Debug("rolling merge complete",
		"camera_id", seg.cameraID,
		"recording_id", seg.recordingID,
		"bucket_segments", bucket.segmentCount,
		"duration_ms", time.Since(mergeStart).Milliseconds())

	return nil
}

// createBucket creates the initial merged file for a new window bucket.
// It merges the single segment into a new output file and creates a DB row.
// The source segment's DB row is then deleted (replaced by the merged row).
func (r *RollingMergeCoordinator) createBucket(
	ctx context.Context,
	seg pendingSegmentInfo,
	info *SegmentInfo,
) (outputPath, mergedRecID string, err error) {
	// Create output file via store.
	tempPath, finalPath, derr := r.store.CreateSegment(seg.cameraID, seg.format)
	if derr != nil {
		return "", "", fmt.Errorf("create bucket output: %w", derr)
	}

	// Merge single segment into the bucket file.
	if err := MergeMP4Segments(ctx, []*SegmentInfo{info}, tempPath); err != nil {
		os.Remove(tempPath)
		return "", "", fmt.Errorf("merge initial segment: %w", err)
	}

	// Verify output.
	fi, err := os.Stat(tempPath)
	if err != nil || fi.Size() == 0 {
		os.Remove(tempPath)
		return "", "", fmt.Errorf("bucket output is empty or missing")
	}

	// Atomic rename.
	if err := r.store.CloseSegment(tempPath, finalPath); err != nil {
		os.Remove(tempPath)
		return "", "", fmt.Errorf("finalize bucket: %w", err)
	}

	// Create merged recording row and delete the source segment row.
	mergedRecID = strconv.FormatInt(time.Now().UnixNano(), 10)
	mergedRec := &model.Recording{
		ID:          mergedRecID,
		CameraID:    seg.cameraID,
		FilePath:    finalPath,
		Format:      model.Format(seg.format),
		StartedAt:   seg.startedAt,
		EndedAt:     seg.endedAt,
		Duration:    info.TotalDuration.Seconds(),
		FileSize:    fi.Size(),
		FrameCount:  info.SampleCount,
		MergeStatus: model.MergeStatusMerged,
	}

	if err := storage.RetryOnBusy(ctx, func() error {
		return r.db.RollingReplaceRecordings(ctx, mergedRec, "", []string{seg.recordingID})
	}); err != nil {
		os.Remove(finalPath)
		return "", "", fmt.Errorf("db replace (create): %w", err)
	}

	// Delete the source segment file (DB already committed).
	r.store.DeleteFile(seg.filePath)

	return finalPath, mergedRecID, nil
}

// appendToBucket merges a new segment into the existing bucket file.
// It produces a new output file (temp→rename) and UPDATEs the merged DB row,
// then deletes the source segment's row + file.
func (r *RollingMergeCoordinator) appendToBucket(
	ctx context.Context,
	seg pendingSegmentInfo,
	newInfo *SegmentInfo,
	bucket *bucketInfo,
) (outputPath, mergedRecID string, err error) {
	// Parse the existing bucket file.
	bucketInfo, err := ParseSegment(bucket.mergedFilePath)
	if err != nil {
		return "", "", fmt.Errorf("parse existing bucket: %w", err)
	}

	// Validate codec compatibility (defensive — should have been caught earlier).
	if bucketInfo.Codec != newInfo.Codec ||
		!bytesEqual(bucketInfo.SPS, newInfo.SPS) ||
		!bytesEqual(bucketInfo.PPS, newInfo.PPS) {
		return "", "", fmt.Errorf("bucket/segment codec mismatch during append")
	}

	// Create new output file.
	tempPath, finalPath, derr := r.store.CreateSegment(seg.cameraID, seg.format)
	if derr != nil {
		return "", "", fmt.Errorf("create append output: %w", derr)
	}

	// Merge [bucket + newSegment].
	if err := MergeMP4Segments(ctx, []*SegmentInfo{bucketInfo, newInfo}, tempPath); err != nil {
		os.Remove(tempPath)
		return "", "", fmt.Errorf("merge append: %w", err)
	}

	fi, err := os.Stat(tempPath)
	if err != nil || fi.Size() == 0 {
		os.Remove(tempPath)
		return "", "", fmt.Errorf("append output is empty or missing")
	}

	// Atomic rename — overwrites the old bucket file.
	// CloseSegment does temp→rename; but we need to replace an existing final path.
	// Use direct rename to overwrite (the old bucket file will be replaced).
	if err := os.Rename(tempPath, finalPath); err != nil {
		// Fallback: if rename fails (cross-device?), use the store's CloseSegment
		// which may handle it differently. For safety, clean up temp.
		os.Remove(tempPath)
		return "", "", fmt.Errorf("finalize append (rename): %w", err)
	}

	// Calculate updated metadata.
	totalDur := bucketInfo.TotalDuration + newInfo.TotalDuration
	totalFrames := bucketInfo.SampleCount + newInfo.SampleCount
	totalDurSec := math.Round(totalDur.Seconds()*1000) / 1000

	mergedRecID = bucket.mergedRecID
	mergedRec := &model.Recording{
		ID:          mergedRecID,
		CameraID:    seg.cameraID,
		FilePath:    finalPath,
		Format:      model.Format(seg.format),
		StartedAt:   bucket.windowStart,
		EndedAt:     seg.endedAt,
		Duration:    totalDurSec,
		FileSize:    fi.Size(),
		FrameCount:  totalFrames,
		MergeStatus: model.MergeStatusMerged,
	}

	// UPDATE the merged row + DELETE the source segment row, in one transaction.
	if err := storage.RetryOnBusy(ctx, func() error {
		return r.db.RollingReplaceRecordings(ctx, mergedRec, bucket.mergedRecID, []string{seg.recordingID})
	}); err != nil {
		// DB failed — the file is already renamed. This is a data inconsistency risk.
		// The merged file is valid but the DB row may be stale. Log prominently.
		rollingLogger.Error("db update failed after file merge — data may be inconsistent",
			"camera_id", seg.cameraID,
			"merged_path", finalPath,
			"error", err)
		return "", "", fmt.Errorf("db replace (append): %w", err)
	}

	// Delete the source segment file.
	r.store.DeleteFile(seg.filePath)

	// Delete the PREVIOUS bucket file (each append creates a new file via
	// store.CreateSegment, so the old bucket path is now orphaned). Only
	// delete if it differs from the new finalPath (it always should, since
	// CreateSegment generates unique timestamps).
	if bucket.mergedFilePath != "" && bucket.mergedFilePath != finalPath {
		r.store.DeleteFile(bucket.mergedFilePath)
	}

	return finalPath, mergedRecID, nil
}

// computeWindow returns the [start, end) time window for a timestamp.
// Window boundaries are aligned to epoch start at windowDur intervals.
// For windowDur=1h, this produces natural-hour boundaries (00:00, 01:00, ...).
func computeWindow(t time.Time, windowDur time.Duration) (start, end time.Time) {
	if windowDur <= 0 {
		windowDur = time.Hour
	}
	// Truncate to window boundary in UTC (DB stores UTC).
	utc := t.UTC()
	epoch := time.Unix(0, 0).UTC()
	elapsed := utc.Sub(epoch)
	buckets := int64(elapsed / windowDur)
	start = epoch.Add(time.Duration(buckets) * windowDur)
	end = start.Add(windowDur)
	return start, end
}

// bytesEqual is a small helper to avoid importing bytes just for Equal.
func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
