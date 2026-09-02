package merge

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"runtime"
	"sync"
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
//   - SPS/PPS change (camera reconnect with new params) or audio config change
//     (audio_enabled toggled, codec/config renegotiated) → close current bucket,
//     start a new one.
//
// Benchmark data (see mp4merge_bench_test.go BenchmarkRollingMergeSimulation):
//   - Worst-case append (1h bucket + new 30s segment): ~586ms on dev machine.
//   - USB HDD throughput: 67-230 MB/s depending on NAL sizes.
//   - Target <10s latency: comfortably achievable.

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
//   - SPS/PPS change (camera reconnect with new params) or audio config change
//     (audio_enabled toggled, codec/config renegotiated) → close current bucket,
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
	// getAdaptiveCfg resolves the per-camera adaptive config (the compressed
	// timeline cadence lives there); nil = package default.
	getAdaptiveCfg func(cameraID string) *config.AdaptiveRecordingConfig
	cameras        func() []config.CameraConfig
	metrics        *metrics.Metrics

	mergeLocks sync.Map // map[string]*mergeLock — per-camera non-blocking mutex

	// bucketState tracks the current open bucket per camera for the active window.
	// Key = cameraID, Value = *bucketInfo. Cleared on window rollover or SPS/PPS change.
	buckets sync.Map // map[string]*bucketInfo

	eventBus  *event.EventBus
	eventCh   chan event.Event
	cancelSub context.CancelFunc

	// wg tracks ALL goroutines spawned by Start (eventLoop, backfillOnStartup,
	// backfillLoop) AND the per-camera mergeSegments goroutines fan-out by
	// eventLoop. Stop waits on wg so that, once Stop returns, no goroutine is
	// still writing to the storage tree — required by the App.Service contract
	// ("must release all goroutines ... resources") and prevents the TempDir
	// cleanup race (issue #143 / #125 class) where outlived goroutines write
	// files after t.TempDir() RemoveAll begins.
	wg sync.WaitGroup
}

// bucketInfo tracks the accumulated merge state for a camera's current window.

// bucketInfo tracks the accumulated merge state for a camera's current window.
type bucketInfo struct {
	mu             sync.Mutex
	mergedFilePath string // path to the accumulating merged file (empty = not yet created)
	mergedRecID    string // DB recording ID of the merged row (for UPDATE on next append)
	spsKey         string // SHA-256(SPS+PPS+VPS) for compatibility checking
	audioKey       string // segmentAudioKey for audio compatibility checking
	windowStart    time.Time
	windowEnd      time.Time
	segmentCount   int   // how many segments have been merged into this bucket
	mergedFileSize int64 // current byte size of mergedFilePath (0 if no bucket yet)

	// Wall/file axis accumulation (#496 append-fix, 2026-09-01): every append
	// re-parses the bucket FILE, whose TL dwell samples are already compressed
	// to the file cadence — per-merge stats therefore cannot recover the wall
	// span (the compressed dwell time reads as wall and is silently lost, so
	// the row's duration collapses onto the file axis and the day-timeline
	// seek desyncs). Both axes are accumulated in memory instead; the row
	// always carries the wall duration plus a monotonically grown wall→file
	// map. Survives arbitrary camera frame pacing (smart-codec decimation,
	// day/night fps switching) because each input's own contribution is taken
	// from the merge stats of THAT append, before it is ever re-parsed.
	wallDurSec float64
	fileDurSec float64
	wallFile   [][2]float64
	// lastEnded is the wall time of the last input segment's end. The next
	// append's wall contribution is lastEnded→ended — inter-segment GAPS
	// (disconnects, TL pauses) stay visible on the wall axis, matching the
	// row's started_at..ended_at span instead of silently shrinking it.
	lastEnded time.Time
}

// segmentAudioKey derives the audio compatibility key for a parsed segment.
// Segments with different keys cannot be merged into one MP4 without dropping
// the audio track (MergeMP4Segments' mixed-audio policy), so the rolling bucket
// must break at every key change — otherwise the first mixed append produces a
// video-only bucket whose subsequent appends keep mismatching forever (sticky
// audio loss: the degraded bucket is the input to every later merge).

// segmentAudioKey derives the audio compatibility key for a parsed segment.
// Segments with different keys cannot be merged into one MP4 without dropping
// the audio track (MergeMP4Segments' mixed-audio policy), so the rolling bucket
// must break at every key change — otherwise the first mixed append produces a
// video-only bucket whose subsequent appends keep mismatching forever (sticky
// audio loss: the degraded bucket is the input to every later merge).
func segmentAudioKey(info *SegmentInfo) string {
	if !info.HasAudio {
		return "none"
	}
	return fmt.Sprintf("%s:mulaw=%v:ts=%d:cfg=%x",
		info.AudioCodec, info.G711MULaw, info.AudioTimescale, info.AudioConfig)
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

// NewRollingMergeCoordinator creates a new coordinator.
// It does NOT start subscribing until Start() is called.
func NewRollingMergeCoordinator(
	db *storage.DB,
	store *storage.Manager,
	getGlobalCfg func() config.MergeConfig,
	getCameraCfg func(cameraID string) *config.MergeConfig,
	getAdaptiveCfg func(cameraID string) *config.AdaptiveRecordingConfig,
	cameras func() []config.CameraConfig,
	m *metrics.Metrics,
	eventBus *event.EventBus,
) *RollingMergeCoordinator {
	return &RollingMergeCoordinator{
		db:             db,
		store:          store,
		getGlobalCfg:   getGlobalCfg,
		getCameraCfg:   getCameraCfg,
		getAdaptiveCfg: getAdaptiveCfg,
		cameras:        cameras,
		metrics:        m,
		eventBus:       eventBus,
		eventCh:        make(chan event.Event, 128), // buffered: bursts of segment closes
	}
}

// resolveRollingConfig returns the effective rolling config for a camera.
// resolveTimelapseCadence returns the per-camera compressed-timeline frame
// duration (adaptive.timelapse_frame_ms presets 100/300/500ms); zero = the
// merge package default.

// resolveRollingConfig returns the effective rolling config for a camera.
// resolveTimelapseCadence returns the per-camera compressed-timeline frame
// duration (adaptive.timelapse_frame_ms presets 100/300/500ms); zero = the
// merge package default.
func (r *RollingMergeCoordinator) resolveTimelapseCadence(cameraID string) time.Duration {
	if r.getAdaptiveCfg == nil {
		return 0
	}
	a := r.getAdaptiveCfg(cameraID)
	if a == nil || a.TimelapseFrameMs == 0 {
		return 0
	}
	return time.Duration(a.TimelapseFrameMs) * time.Millisecond
}

// resolveTimelapseGap returns this camera's dwell-compression gap threshold:
// max(package default, half the timelapse interval). A TL dwell is ~one
// interval long by construction, while a slow/smart-codec camera's NORMAL
// inter-frame gap (e.g. 2.1s at 0.5fps static decimation) never approaches
// its interval — the fixed 2s default mis-read such cameras' full-rate
// footage as timelapse and fast-forwarded it (wall-axis suite S3, 2026-09-01).
func (r *RollingMergeCoordinator) resolveTimelapseGap(cameraID string) time.Duration {
	interval := 30 * time.Second
	if r.getAdaptiveCfg != nil {
		if a := r.getAdaptiveCfg(cameraID); a != nil && a.TimelapseInterval != "" {
			if d, err := time.ParseDuration(a.TimelapseInterval); err == nil && d > 0 {
				interval = d
			}
		}
	}
	half := interval / 2
	if half < TimelapseGapThreshold {
		return TimelapseGapThreshold
	}
	return half
}

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

	// wg.Add BEFORE the `go` statement (not inside the goroutine) is required by
	// sync.WaitGroup's contract: "Calls with a positive delta that start when the
	// counter is zero must happen before a Wait." Otherwise a fast Stop's Wait
	// could observe counter==0 and return before the goroutine's Add runs.
	r.wg.Add(3)
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

// backfillOnStartup scans for historical pending MP4 segments across all rolling-enabled
// cameras and merges them into window buckets. This ensures that recordings that existed
// before rolling merge was enabled get the same quasi-real-time treatment retroactively.
func (r *RollingMergeCoordinator) backfillOnStartup(ctx context.Context) {
	defer r.wg.Done() // paired with r.wg.Add(3) in Start
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

// backfillLoop runs a periodic sweep of historical pending segments, in
// addition to the one-shot startup backfill. The startup backfill is throttled
// (max_segments/max_age) and runs once; without a periodic sweep, historical
// pending accumulates whenever the startup backfill can't keep up (thousands of
// 30s H265 fragments/day). This closes the gap: every `rolling_backfill_interval`
// (default 10m) it processes up to `rolling_backfill_batch` (default 500) pending
// segments, using try-locks to yield to real-time events.
func (r *RollingMergeCoordinator) backfillLoop(ctx context.Context) {
	defer r.wg.Done() // paired with r.wg.Add(3) in Start
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

// Stop unsubscribes, signals all goroutines to exit via ctx cancellation, and
// WAITS for them to fully return before returning itself. This honors the
// App.Service contract ("must release all goroutines") and prevents the
// TempDir cleanup race (#143/#125 class) where outlived goroutines write files
// after the caller (e.g. a test's t.TempDir) begins cleanup.
func (r *RollingMergeCoordinator) Stop() {
	if r.cancelSub != nil {
		r.cancelSub()
	}
	if r.eventBus != nil {
		r.eventBus.Unsubscribe(event.TopicSegmentCompleted, r.eventCh)
	}
	// Wait for eventLoop + backfillOnStartup + backfillLoop + any in-flight
	// mergeSegments goroutines fan-out by eventLoop to fully exit.
	r.wg.Wait()
}

// eventLoop drains SegmentCompleted events and dispatches per-camera merge goroutines.
// It applies debounce per camera: rapid segment closes (e.g. short segment dur with
// reconnects) get batched into a single merge.
//
// All wg.Add calls for the mergeSegments fan-out happen inside this goroutine (in the
// fireCh select case), so once ctx is cancelled the loop exits via ctx.Done and no
// further Add can race with a concurrent Stop's Wait (which would violate sync.WaitGroup's
// "Add with positive delta must happen before Wait when counter is zero" rule).
// The debounce timers only send a non-blocking signal on fireCh; they never touch wg.

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

// statsWallDuration picks the product's wall-clock span from the merge stats
// (sum of input sample durations — unaffected by #496 timelapse dwell
// compression), falling back to the caller's parsed-duration estimate when no
// map was collected.

// statsWallDuration picks the product's wall-clock span from the merge stats
// (sum of input sample durations — unaffected by #496 timelapse dwell
// compression), falling back to the caller's parsed-duration estimate when no
// map was collected.
func statsWallDuration(stats MergeStats, fallback float64) float64 {
	if w := stats.WallDurationSec(); w > 0 {
		return math.Round(w*1000) / 1000
	}
	return fallback
}

// totalDurFallbackSec is the pre-stats duration estimate for a bucket append:
// the parsed bucket file plus the new input. Correct on the real-time axis
// (no compression yet), and only used when stats carry no wall map.

// totalDurFallbackSec is the pre-stats duration estimate for a bucket append:
// the parsed bucket file plus the new input. Correct on the real-time axis
// (no compression yet), and only used when stats carry no wall map.
func totalDurFallbackSec(bucketInfo, newInfo *SegmentInfo) float64 {
	return math.Round((bucketInfo.TotalDuration+newInfo.TotalDuration).Seconds()*1000) / 1000
}
