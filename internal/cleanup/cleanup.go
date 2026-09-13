package cleanup

// This file defines CleanupManager: its struct, constructor, config setters,
// the periodic Run loop, the RunOnce dispatcher (which fans out to the
// per-strategy methods in time_retention.go / disk_threshold.go /
// dark_segments.go / orphans.go / repair.go), and the shared batch-delete +
// database-maintenance helpers.
//
// Cleanup strategies were split out by responsibility (#227).

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/slogx"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/event"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/iobudget"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/metrics"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
)

var logger = slogx.Component("cleanup")

// CleanupManager handles periodic cleanup of old recordings.
// It supports two cleanup strategies:
//   - Time-based: delete recordings older than retention period
//   - Disk-threshold: delete oldest recordings when disk usage exceeds threshold
type CleanupManager struct {
	db                        *storage.DB
	store                     *storage.Manager
	retention                 time.Duration
	diskThreshold             int // percent
	interval                  time.Duration
	metrics                   *metrics.Metrics
	healthEnabled             bool
	healthRetention           time.Duration
	transcodeOrphanFn         func(ctx context.Context) error
	transcodeHistoryRetention time.Duration // 0 = disabled
	// deepOrphanLast timestamps the latest deep (nested-tree) orphan scan;
	// the recursive walk is throttled to once per deepOrphanInterval.
	deepOrphanLast time.Time
	// activeCameraProvider returns the live set of cameras the user has
	// configured (cfg.Cameras, the yaml source of truth). When set,
	// directory-scanning cleanup (orphanFileCleanup) iterates only these
	// IDs, skipping orphan dirs from cameras removed from yaml but still
	// present on disk / in the DB cache. nil = legacy behaviour (fall back
	// to db.ListCameras). Injected from pkg/app/run.go — mirrors the
	// provider pattern used by the merge coordinators. (staleRecordCleanup
	// no longer uses it: the ghost-row sweep queries pending rows directly.)
	activeCameraProvider       func() []config.CameraConfig
	ffprobePath                string // optional ffprobe fallback for probeDuration; empty = pure-Go mediaprobe only
	eventBus                   *event.EventBus
	consecutivePassiveFailures int  // tracks consecutive PASSIVE checkpoint failures for escalation to TRUNCATE
	motionAwareDisk            bool // disk-threshold path deletes boring-first (issue #435); default true

	// Directory-form deletion pacing (#748): MJPEG/timelapse sources are frame
	// directories holding up to hundreds of thousands of small files (a 1s
	// natural-day window ≈ 600K). Unthrottled recursive removal saturates the
	// ext4 journal (jbd2 D-state) and starves online recording IO for minutes
	// even after the deleting process exits. When paced, every dirDeleteChunk
	// unlinked files are followed by a ctx-aware dirDeleteSleep pause.
	dirDeleteChunk int
	dirDeleteSleep time.Duration

	// budget paces deletion/reclaim I/O against the process-wide background
	// budget (#751). nil = budgeting off (default) — deletion runs with the
	// time-slice pacing above only. Set once via SetIOBudget at startup.
	budget iobudget.Limiter
	// unlinkBudget is the metadata-storm guardrail (#755): a per-FILE rate
	// limit for recursive frame-tree deletion. When set it REPLACES the fixed
	// dirDeleteChunk/dirDeleteSleep time-slice pacing (bytes are billed
	// separately via budget). nil = legacy time-slice pacing.
	unlinkBudget iobudget.Limiter
}

// NewCleanupManager creates a new CleanupManager with the given config.
func NewCleanupManager(db *storage.DB, store *storage.Manager, cfg config.CleanupConfig, opts ...*metrics.Metrics) (*CleanupManager, error) {
	var m *metrics.Metrics
	if len(opts) > 0 {
		m = opts[0]
	}
	interval, err := time.ParseDuration(cfg.CheckInterval)
	if err != nil {
		return nil, err
	}
	if interval <= 0 {
		interval = time.Hour
	}

	return &CleanupManager{
		db:              db,
		store:           store,
		retention:       time.Duration(cfg.RetentionDays) * 24 * time.Hour,
		diskThreshold:   cfg.DiskThresholdPercent,
		interval:        interval,
		metrics:         m,
		motionAwareDisk: cfg.MotionAwareDiskCleanupEnabled(),
	}, nil
}

// SetHealthConfig enables or disables health event retention cleanup.
func (cm *CleanupManager) SetHealthConfig(enabled bool, retention time.Duration) {
	cm.healthEnabled = enabled
	cm.healthRetention = retention
}

// SetTranscodeOrphanCleanup registers a function to clean up orphaned transcoded files.
// The function is called once per cleanup cycle (typically daily).
func (cm *CleanupManager) SetTranscodeOrphanCleanup(fn func(ctx context.Context) error) {
	cm.transcodeOrphanFn = fn
}

// SetTranscodeHistoryRetention sets the retention period for completed transcode task history.
func (cm *CleanupManager) SetTranscodeHistoryRetention(retention time.Duration) {
	cm.transcodeHistoryRetention = retention
}

// SetActiveCameraProvider registers a function returning the currently
// configured cameras (yaml cfg.Cameras). When set, orphanFileCleanup and
// staleRecordCleanup iterate only these camera IDs, avoiding O(N) stat
// scans over directories belonging to cameras that have been removed from
// the config but whose rows/files remain. nil provider = fall back to
// db.ListCameras() (legacy behaviour). Mirrors the provider injection
// pattern already used by the merge coordinators (see pkg/app/run.go).
func (cm *CleanupManager) SetActiveCameraProvider(fn func() []config.CameraConfig) {
	if cm == nil {
		return
	}
	cm.activeCameraProvider = fn
}

// activeCameraIDs returns the set of camera IDs that periodic
// directory-scanning cleanup should visit. When an active-camera provider
// is wired (yaml cfg.Cameras), it is the source of truth — the DB cameras
// table can desync from yaml (a removed camera's rows may linger), which
// previously caused orphan dirs to be stat-scanned every cycle. When no
// provider is set, fall back to db.ListCameras() for backward
// compatibility. timeBasedCleanup / diskThresholdCleanup intentionally do
// NOT use this — they must keep cleaning up recordings of removed cameras
// via SQL, which does no per-file stat.
func (cm *CleanupManager) activeCameraIDs(ctx context.Context) ([]string, error) {
	if cm.activeCameraProvider != nil {
		cams := cm.activeCameraProvider()
		ids := make([]string, 0, len(cams))
		for i := range cams {
			if id := strings.TrimSpace(cams[i].ID); id != "" {
				ids = append(ids, id)
			}
		}
		return ids, nil
	}
	// Legacy fallback for unwired callers / tests.
	cams, err := cm.db.ListCameras(ctx)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(cams))
	for _, c := range cams {
		ids = append(ids, c.ID)
	}
	return ids, nil
}

// Run starts the periodic cleanup loop. It blocks until ctx is cancelled.
func (cm *CleanupManager) Run(ctx context.Context) {
	ticker := time.NewTicker(cm.interval)
	defer ticker.Stop()

	// SQLite health metrics (WAL size, DB size, fragmentation, pool stats) are updated
	// on a faster cadence than the (typically hourly) cleanup cycle so they stay
	// near-real-time for monitoring. This ticker is cheap: a few PRAGMAs + an os.Stat.
	const sqliteMetricsInterval = 60 * time.Second
	metricsTicker := time.NewTicker(sqliteMetricsInterval)
	defer metricsTicker.Stop()

	// Run once immediately
	cm.RunOnce(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cm.RunOnce(ctx)
		case <-metricsTicker.C:
			// Only update metrics if wired; non-fatal on error.
			if cm.metrics != nil {
				cm.updateSQLiteMetrics(ctx)
			}
		}
	}
}

// RunOnce performs a single cleanup pass: time-based, archived, disk-threshold,
// health retention, orphan files, stale records, zero-duration repair, then database
// optimize (PRAGMA optimize) and WAL checkpoint.
func (cm *CleanupManager) RunOnce(ctx context.Context) error {
	start := time.Now()

	if err := cm.timeBasedCleanup(ctx); err != nil {
		logger.Error("time-based cleanup error", "error", err)
	}
	cm.archivedRetentionCleanup(ctx)
	cm.darkSegmentCleanup(ctx)
	if err := cm.diskThresholdCleanup(ctx); err != nil {
		logger.Error("disk-threshold cleanup error", "error", err)
	}
	cm.healthRetentionCleanup(ctx)
	if cm.transcodeOrphanFn != nil {
		if err := cm.transcodeOrphanFn(ctx); err != nil {
			logger.Error("transcode orphan cleanup error", "error", err)
		}
	}
	cm.transcodeHistoryCleanup(ctx)
	if cm.metrics != nil {
		if count, err := cm.db.CountRecordings(ctx); err == nil {
			cm.metrics.RecordingCount.Set(float64(count))
		}
	}
	cm.orphanFileCleanup(ctx)
	cm.staleRecordCleanup(ctx)
	cm.repairZeroDurationRecordings(ctx)
	// Refresh query planner stats after cleanup. PRAGMA optimize is cheap —
	// incremental ANALYZE only where needed (tables/indexes that changed
	// significantly since last ANALYZE). With analysis_limit=1000 pragma,
	// each ANALYZE scans at most 1000 rows per index.
	if err := cm.db.Optimize(ctx); err != nil {
		logger.Warn("database optimize failed", "error", err)
	}
	// Database maintenance: WAL checkpoint and incremental vacuum.
	cm.performDatabaseMaintenance(ctx)

	// Update cleanup duration metric
	if cm.metrics != nil {
		cm.metrics.CleanupDurationSeconds.Observe(time.Since(start).Seconds())
	}

	// Update SQLite database health metrics
	cm.updateSQLiteMetrics(ctx)

	return nil
}

// SetEventBus injects the event bus for publishing segment.deleted events.
func (cm *CleanupManager) SetEventBus(bus *event.EventBus) {
	if cm == nil {
		return
	}
	cm.eventBus = bus
}

// SetDirectoryDeleteThrottle paces directory-form recording deletion (#748):
// after every chunk files removed inside a frame directory (MJPEG/timelapse
// trees with up to hundreds of thousands of small files), the deletion loop
// sleeps for d so the ext4 journal (jbd2) can drain and online recording IO
// is not starved. chunk <= 0 or d <= 0 disables pacing (legacy behavior).
func (cm *CleanupManager) SetDirectoryDeleteThrottle(chunk int, d time.Duration) {
	if cm == nil {
		return
	}
	cm.dirDeleteChunk, cm.dirDeleteSleep = chunk, d
}

// SetIOBudget installs the shared background I/O budget (#751). Reclaim I/O
// (file removals in batch deletes) is billed by estimated byte size so
// deletion yields to foreground work on busy media. nil disables billing.
func (cm *CleanupManager) SetIOBudget(l iobudget.Limiter) {
	if cm == nil {
		return
	}
	cm.budget = l
}

// HasIOBudget returns the installed limiter (nil when budgeting is off) —
// wiring-regression observability.
func (cm *CleanupManager) HasIOBudget() iobudget.Limiter {
	if cm == nil {
		return nil
	}
	return cm.budget
}

// SetUnlinkBudget installs the per-file unlink guardrail for directory-form
// deletion (#755). When set, it replaces the fixed time-slice pacing
// (SetDirectoryDeleteThrottle) — the medium tells us when it is busy instead
// of a fixed sleep guessing. nil restores legacy pacing.
func (cm *CleanupManager) SetUnlinkBudget(l iobudget.Limiter) {
	if cm == nil {
		return
	}
	cm.unlinkBudget = l
}

// HasUnlinkBudget returns the installed guardrail (nil when off).
func (cm *CleanupManager) HasUnlinkBudget() iobudget.Limiter {
	if cm == nil {
		return nil
	}
	return cm.unlinkBudget
}

// estimatePathBytes sums the on-disk size of path (recursive for
// directories). Unreadable entries contribute zero — an estimate is pacing
// input, not accounting truth.
func estimatePathBytes(path string) int64 {
	if path == "" {
		return 0
	}
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	if !info.IsDir() {
		return info.Size()
	}
	var total int64
	_ = filepath.WalkDir(path, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil //nolint:nilerr // unreadable entries just don't count
		}
		if fi, err := d.Info(); err == nil {
			total += fi.Size()
		}
		return nil
	})
	return total
}

// deleteRecordingFile deletes one recording path, pacing directory-form
// (frame-tree) deletions when throttling is configured. Plain files keep the
// storage-manager semantics (including the .vodidx sidecar); paced directories
// are unlinked file-by-file in chunks and the remaining empty tree is swept by
// a final RemoveAll. A ctx cancellation mid-walk leaves a partially-deleted
// tree — the orphan scanner reclaims it later.
//
// Pacing strategy (#755): when the per-file unlink guardrail is installed it
// REPLACES the fixed chunk/sleep time-slice (one guardrail Wait per file —
// fast media sprints, busy media backs off); without it the legacy
// SetDirectoryDeleteThrottle pacing applies unchanged.
func (cm *CleanupManager) deleteRecordingFile(ctx context.Context, path string) error {
	if cm.unlinkBudget != nil {
		return cm.deleteRecordingFileGuarded(ctx, path)
	}
	if cm.dirDeleteChunk <= 0 || cm.dirDeleteSleep <= 0 {
		return cm.store.DeleteFile(path)
	}
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return cm.store.DeleteFile(path)
	}
	n := 0
	if err := filepath.WalkDir(path, func(p string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		if d.IsDir() {
			return nil
		}
		if err := os.Remove(p); err != nil {
			return err
		}
		n++
		if n%cm.dirDeleteChunk == 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(cm.dirDeleteSleep):
			}
		}
		return nil
	}); err != nil {
		return fmt.Errorf("paced delete %q: %w", path, err)
	}
	return os.RemoveAll(path)
}

// deleteRecordingFileGuarded is the #755 guardrail variant of
// deleteRecordingFile: every file in the tree bills ONE unit to the unlink
// budget before its unlink, replacing the fixed chunk/sleep pacing entirely.
// The billing is ctx-cancellable (abort leaves a partial tree for the orphan
// scanner — identical cancellation semantics to the legacy pacing).
func (cm *CleanupManager) deleteRecordingFileGuarded(ctx context.Context, path string) error {
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return cm.store.DeleteFile(path)
	}
	if err := filepath.WalkDir(path, func(p string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		if d.IsDir() {
			return nil
		}
		if err := cm.unlinkBudget.Wait(ctx, iobudget.ConsumerCleanup, 1); err != nil {
			return err
		}
		return os.Remove(p)
	}); err != nil {
		return fmt.Errorf("guarded delete %q: %w", path, err)
	}
	return os.RemoveAll(path)
}

// adaptiveBatchSleep sleeps between batch delete operations, adapting the sleep
// duration based on the current WAL file size. Larger WAL = longer sleep to give
// the checkpoint process time to catch up.
func (cm *CleanupManager) adaptiveBatchSleep(ctx context.Context) {
	walSize, err := cm.db.GetWALSize()
	if err != nil {
		// Cannot determine WAL size - use default minimum sleep
		select {
		case <-ctx.Done():
			return
		case <-time.After(10 * time.Millisecond):
		}
		return
	}

	// sleep = 10ms + max(0, (walSize - 5MB)) / 1MB * 5ms, capped at 200ms
	extraMs := int64(0)
	const fiveMB int64 = 5 * 1024 * 1024
	const oneMB int64 = 1024 * 1024
	if walSize > fiveMB {
		extraMs = (walSize - fiveMB) / oneMB * 5
	}
	sleepMs := 10 + extraMs
	if sleepMs > 200 {
		sleepMs = 200
	}

	select {
	case <-ctx.Done():
	case <-time.After(time.Duration(sleepMs) * time.Millisecond):
	}
}

// BatchDeleteRecordingsWithFiles batch-deletes recordings with a single DB query,
// then deletes their files and publishes segment.deleted events.
//
// Flow: batch-fetch AI status, filter processing, collect event payloads,
// batch-delete DB in chunks of 200 (with adaptive sleep), delete files, publish events.
//
// The reason parameter is used as the event Reason field
// (e.g. "retention_expired", "disk_threshold").
// Returns the list of successfully deleted recording IDs.
func (cm *CleanupManager) BatchDeleteRecordingsWithFiles(ctx context.Context, recordings []model.Recording, reason string) ([]string, error) {
	if len(recordings) == 0 {
		return nil, nil
	}
	// Attribute this batch's DB writes to cleanup (#759 txn sources).
	ctx = storage.WithTxnSource(ctx, "cleanup")

	// 1. Batch-fetch AI status (eliminates N+1)
	ids := make([]string, len(recordings))
	for i, rec := range recordings {
		ids[i] = rec.ID
	}

	aiStatuses, err := cm.db.BatchGetRecordingAIStatus(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("batch get AI status: %w", err)
	}

	// 2. Filter out recordings being processed by MiBeeVision
	var toDelete []model.Recording
	for _, rec := range recordings {
		if aiStatuses[rec.ID] == "processing" {
			logger.Debug("skipping deletion of recording being processed by MiBeeVision",
				"recording_id", rec.ID, "ai_status", "processing")
			continue
		}
		toDelete = append(toDelete, rec)
	}

	if len(toDelete) == 0 {
		return nil, nil
	}

	// 3. Collect event payloads (publish after successful deletion)
	deleteIDs := make([]string, len(toDelete))
	events := make([]event.SegmentDeleted, len(toDelete))
	for i, rec := range toDelete {
		deleteIDs[i] = rec.ID
		events[i] = event.SegmentDeleted{
			RecordingID: rec.ID,
			CameraID:    rec.CameraID,
			FilePath:    rec.FilePath,
			Reason:      reason,
		}
	}

	// 4. Batch-delete DB records in chunks of 200
	const batchSize = 200
	var successfullyDeleted []string
	for i := 0; i < len(deleteIDs); i += batchSize {
		end := i + batchSize
		if end > len(deleteIDs) {
			end = len(deleteIDs)
		}
		batch := deleteIDs[i:end]
		if _, err := cm.db.DeleteRecordingsBatch(ctx, batch); err != nil {
			logger.Warn("batch delete failed, skipping batch", "count", len(batch), "error", err)
			continue
		}
		successfullyDeleted = append(successfullyDeleted, batch...)

		// Adaptive sleep between batches to let WAL checkpoint catch up
		if end < len(deleteIDs) {
			cm.adaptiveBatchSleep(ctx)
		}
	}

	if len(successfullyDeleted) == 0 {
		return nil, nil
	}

	// Build set of successfully deleted IDs for filtering files and events
	deletedSet := make(map[string]bool, len(successfullyDeleted))
	for _, id := range successfullyDeleted {
		deletedSet[id] = true
	}

	// Directory-locality grouping (#755): reclaim same-directory recordings
	// back-to-back so ext4 directory-entry metadata stays hot in cache and
	// the inode/jbd2 changes coalesce into fewer journal transactions.
	// Input order is temporal (retention-ordered) and uncorrelated with
	// layout; sorting by dir does not change WHICH files die, only the
	// visit order.
	sorted := make([]model.Recording, 0, len(toDelete))
	for _, rec := range toDelete {
		if deletedSet[rec.ID] {
			sorted = append(sorted, rec)
		}
	}
	sort.Slice(sorted, func(i, j int) bool {
		return filepath.Dir(sorted[i].FilePath) < filepath.Dir(sorted[j].FilePath)
	})

	// 5. Delete files for successfully deleted recordings (best-effort).
	// Reclaim the merged MP4 (merge_path) too — it is the largest artifact and
	// the one playback loads; without this it leaks permanently because the
	// orphan scanner never reaches the nested YYYYMM/DD/HH/ tree. Mirrors
	// handleDeleteRecording / handleTimelapseDelete.
	budgetAborted := false
	for _, rec := range sorted {
		// Bill the reclaim to the shared budget first (#751): the biggest
		// deletes (multi-GB merged MP4s, 600K-file frame trees) are exactly
		// the ones that must yield to foreground I/O. On a budget wait error
		// (shutdown ctx cancelled) billing stops but removal continues
		// best-effort — pacing never changes deletion semantics.
		if cm.budget != nil && !budgetAborted {
			est := estimatePathBytes(rec.FilePath) + estimatePathBytes(rec.MergePath)
			if err := cm.budget.Wait(ctx, iobudget.ConsumerCleanup, est); err != nil {
				budgetAborted = true
				logger.Warn("io budget wait aborted; continuing file reclaim unthrottled", "error", err)
			}
		}
		if rec.MergePath != "" {
			if err := os.RemoveAll(rec.MergePath); err != nil {
				logger.Warn("failed to delete merged file", "merge_path", rec.MergePath, "error", err)
			}
		}
		if err := cm.deleteRecordingFile(ctx, rec.FilePath); err != nil {
			logger.Warn("failed to delete file", "file_path", rec.FilePath, "error", err)
		}
		// Ambient archive sidecar shares the recording's lifetime (#496).
		if err := os.Remove(rec.FilePath + ".g711"); err != nil && !os.IsNotExist(err) {
			logger.Warn("failed to delete ambient sidecar", "file_path", rec.FilePath+".g711", "error", err)
		}
	}

	// 6. Publish segment.deleted events for successfully deleted recordings
	if cm.eventBus != nil {
		for _, evt := range events {
			if deletedSet[evt.RecordingID] {
				cm.eventBus.Publish(ctx, event.TopicSegmentDeleted, evt)
			}
		}
	}

	return successfullyDeleted, nil
}
