package cleanup

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/event"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/mediaprobe"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/metrics"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
)

var logger = slog.Default().With("component", "cleanup")

// CleanupManager handles periodic cleanup of old recordings.
// It supports two cleanup strategies:
//   - Time-based: delete recordings older than retention period
//   - Disk-threshold: delete oldest recordings when disk usage exceeds threshold
type CleanupManager struct {
	db                         *storage.DB
	store                      *storage.Manager
	retention                  time.Duration
	diskThreshold              int // percent
	interval                   time.Duration
	metrics                    *metrics.Metrics
	healthEnabled              bool
	healthRetention            time.Duration
	transcodeOrphanFn          func(ctx context.Context) error
	transcodeHistoryRetention  time.Duration // 0 = disabled
	// activeCameraProvider returns the live set of cameras the user has
	// configured (cfg.Cameras, the yaml source of truth). When set,
	// directory-scanning cleanup (orphanFileCleanup, staleRecordCleanup)
	// iterates only these IDs, skipping orphan dirs from cameras removed
	// from yaml but still present on disk / in the DB cache. nil = legacy
	// behaviour (fall back to db.ListCameras). Injected from pkg/app/run.go
	// — mirrors the provider pattern used by the merge coordinators.
	activeCameraProvider func() []config.CameraConfig
	ffprobePath                string        // optional ffprobe fallback for probeDuration; empty = pure-Go mediaprobe only
	eventBus                   *event.EventBus
	consecutivePassiveFailures int // tracks consecutive PASSIVE checkpoint failures for escalation to TRUNCATE
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
		db:            db,
		store:         store,
		retention:     time.Duration(cfg.RetentionDays) * 24 * time.Hour,
		diskThreshold: cfg.DiskThresholdPercent,
		interval:      interval,
		metrics:       m,
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

// darkSegmentCleanup deletes recordings marked merge_status='dark'.
// These are segments detected as too dark (night without IR) that were
// excluded from merge. They are cleaned up immediately to save space —
// keeping them provides no playback value. A short grace period (1h) allows
// the recording to be visible briefly before deletion.
func (cm *CleanupManager) darkSegmentCleanup(ctx context.Context) {
	recordings, err := cm.db.ListDarkRecordings(ctx, time.Hour)
	if err != nil {
		logger.Warn("dark segment cleanup: failed to list", "error", err)
		return
	}
	if len(recordings) == 0 {
		return
	}

	deleted, err := cm.BatchDeleteRecordingsWithFiles(ctx, recordings, "dark_segment")
	if err != nil {
		logger.Error("dark segment cleanup: batch delete failed", "error", err, "count", len(recordings))
		return
	}

	var totalSize int64
	for _, rec := range recordings {
		totalSize += rec.FileSize
	}
	logger.Info("cleaned up dark segments",
		"count", len(deleted),
		"freed_bytes", totalSize)
}

// timeBasedCleanup deletes expired recordings using a single batch query.
// It fetches all expired recordings with the minimum effective retention, groups by camera,
// filters by each camera's actual retention_days, and batch-deletes per camera.
// This eliminates N+1 queries (one per camera) from the original per-camera loop.
func (cm *CleanupManager) timeBasedCleanup(ctx context.Context) error {
	globalRetentionDays := int(cm.retention.Hours() / 24)
	if globalRetentionDays <= 0 {
		return nil
	}

	cameras, err := cm.db.ListCameras(ctx)
	if err != nil {
		return err
	}
	if len(cameras) == 0 {
		return nil
	}

	// Build per-camera retention map and compute minimum effective retention
	// to get the widest superset in a single query.
	cameraRetention := make(map[string]int, len(cameras))
	minRetention := globalRetentionDays
	for _, cam := range cameras {
		retentionDays := cam.RetentionDays
		if retentionDays <= 0 {
			retentionDays = globalRetentionDays
		}
		cameraRetention[cam.ID] = retentionDays
		if retentionDays < minRetention {
			minRetention = retentionDays
		}
	}

	// Single query with minimum retention gets the widest set — recordings that
	// are expired for ANY camera. Per-camera filtering below narrows it.
	allExpired, err := cm.db.ListExpiredRecordings(ctx, minRetention)
	if err != nil {
		return err
	}
	if len(allExpired) == 0 {
		return nil
	}

	// Group by camera_id for per-camera filtering
	byCamera := make(map[string][]model.Recording, len(cameras))
	for _, rec := range allExpired {
		byCamera[rec.CameraID] = append(byCamera[rec.CameraID], rec)
	}

	now := time.Now().UTC()
	for _, cam := range cameras {
		recs, ok := byCamera[cam.ID]
		if !ok {
			continue
		}

		retentionDays := cameraRetention[cam.ID]
		retentionDur := time.Duration(retentionDays) * 24 * time.Hour

		// Filter recordings that are actually expired for this camera's retention
		var expired []model.Recording
		for _, rec := range recs {
			if rec.EndedAt.IsZero() {
				continue
			}
			if now.Sub(rec.EndedAt) >= retentionDur {
				expired = append(expired, rec)
			}
		}

		if len(expired) == 0 {
			continue
		}

		deleted, err := cm.BatchDeleteRecordingsWithFiles(ctx, expired, "retention_expired")
		if err != nil {
			logger.Warn("batch delete failed for camera", "camera_id", cam.ID, "count", len(expired), "error", err)
			continue
		}

		for range deleted {
			logger.Info("deleted recording (time-based)", "camera_id", cam.ID)
			if cm.metrics != nil {
				cm.metrics.CleanupDeleted.WithLabelValues("retention").Add(1)
			}
		}
	}
	return nil
}

// diskThresholdCleanup deletes oldest recordings in batches when disk usage exceeds threshold.
func (cm *CleanupManager) diskThresholdCleanup(ctx context.Context) error {
	total, used, err := cm.store.GetDiskUsage()
	if err != nil {
		return err
	}

	if total == 0 {
		return nil
	}

	usagePercent := int(float64(used) / float64(total) * 100)
	if usagePercent <= cm.diskThreshold {
		return nil
	}

	logger.Info("disk usage exceeds threshold, starting cleanup", "usage_percent", usagePercent, "threshold_percent", cm.diskThreshold)

	// Fetch recordings in batches of 50 until usage drops below threshold.
	// Uses BatchDeleteRecordingsWithFiles instead of row-by-row deleteRecording.
	for {
		batch, err := cm.db.ListOldestRecordings(ctx, 50)
		if err != nil {
			return err
		}
		if len(batch) == 0 {
			break
		}

		deleted, err := cm.BatchDeleteRecordingsWithFiles(ctx, batch, "disk_threshold")
		if err != nil {
			logger.Warn("disk threshold batch delete failed", "error", err)
			continue
		}

		for range deleted {
			logger.Info("deleted recording (disk-threshold)")
			if cm.metrics != nil {
				cm.metrics.CleanupDeleted.WithLabelValues("disk_threshold").Add(1)
			}
		}

		// Recheck disk usage
		_, used, err = cm.store.GetDiskUsage()
		if err != nil {
			return err
		}
		usagePercent = int(float64(used) / float64(total) * 100)
		if usagePercent <= cm.diskThreshold {
			break
		}
	}

	return nil
}

// SetEventBus injects the event bus for publishing segment.deleted events.
func (cm *CleanupManager) SetEventBus(bus *event.EventBus) {
	if cm == nil {
		return
	}
	cm.eventBus = bus
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

	// 5. Delete files for successfully deleted recordings (best-effort)
	for _, rec := range toDelete {
		if !deletedSet[rec.ID] {
			continue
		}
		if err := cm.store.DeleteFile(rec.FilePath); err != nil {
			logger.Warn("failed to delete file", "file_path", rec.FilePath, "error", err)
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

// archivedRetentionCleanup deletes expired archived recordings and cleans up empty archive groups.
// Uses BatchDeleteRecordingsWithFiles to avoid N+1 delete pattern.
func (cm *CleanupManager) archivedRetentionCleanup(ctx context.Context) {
	archivedCameras, err := cm.db.ListArchivedCameras(ctx)
	if err != nil {
		logger.Error("failed to list archived cameras", "error", err)
		return
	}

	for _, cam := range archivedCameras {
		// retention_days=0 means keep forever
		if cam.ArchiveRetentionDays <= 0 {
			continue
		}

		batch, err := cm.db.ListExpiredArchivedRecordingsByCamera(ctx, cam.ID, cam.ArchiveRetentionDays)
		if err != nil {
			logger.Warn("failed to list expired archived recordings", "camera_id", cam.ID, "error", err)
			continue
		}

		if len(batch) == 0 {
			continue
		}

		deleted, err := cm.BatchDeleteRecordingsWithFiles(ctx, batch, "retention_expired")
		if err != nil {
			logger.Warn("batch delete failed for archived recordings", "camera_id", cam.ID, "count", len(batch), "error", err)
			continue
		}

		for range deleted {
			logger.Info("deleted archived recording (retention)", "camera_id", cam.ID)
			if cm.metrics != nil {
				cm.metrics.CleanupDeleted.WithLabelValues("archive_retention").Add(1)
			}
		}

		// Check if this archived camera has any recordings left
		remaining, err := cm.db.CountRecordingsByCamera(ctx, cam.ID)
		if err != nil {
			logger.Warn("failed to count recordings for archived camera", "camera_id", cam.ID, "error", err)
			continue
		}
		if remaining == 0 {
			if err := cm.store.DeleteCameraDir(cam.ID); err != nil {
				logger.Warn("failed to delete camera directory", "camera_id", cam.ID, "error", err)
			}
			if err := cm.db.DeleteCamera(ctx, cam.ID); err != nil {
				logger.Warn("failed to delete archived camera", "camera_id", cam.ID, "error", err)
				continue
			}
			logger.Info("cleaned up empty archive group", "camera_id", cam.ID)
		}
	}
}

// healthRetentionCleanup deletes expired health events older than the retention period.
func (cm *CleanupManager) healthRetentionCleanup(ctx context.Context) {
	if !cm.healthEnabled {
		return
	}
	if cm.healthRetention <= 0 {
		return
	}
	cutoff := time.Now().UTC().Add(-cm.healthRetention)
	deleted, err := cm.db.DeleteHealthEventsBefore(ctx, cutoff)
	if err != nil {
		logger.Warn("health event retention cleanup failed", "error", err)
		return
	}
	if deleted > 0 {
		logger.Info("health events cleaned up", "deleted", deleted)
	}
}

// transcodeHistoryCleanup deletes expired transcode task history older than the retention period.
func (cm *CleanupManager) transcodeHistoryCleanup(ctx context.Context) {
	if cm.transcodeHistoryRetention <= 0 {
		return
	}
	deleted, err := cm.db.DeleteCompletedTasks(ctx, cm.transcodeHistoryRetention)
	if err != nil {
		logger.Warn("transcode history cleanup failed", "error", err)
		return
	}
	if deleted > 0 {
		logger.Info("transcode history cleaned up", "deleted", deleted)
	}
}

// orphanFileCleanup scans camera directories for files/directories not tracked
// in the recordings table and removes them.
func (cm *CleanupManager) orphanFileCleanup(ctx context.Context) {
	cameras, err := cm.activeCameraIDs(ctx)
	if err != nil {
		logger.Warn("orphan cleanup: failed to list cameras", "error", err)
		return
	}
	var totalDeleted int
	for _, cam := range cameras {
		totalDeleted += cm.cleanOrphansForCamera(ctx, cam)
	}
	if totalDeleted > 0 {
		logger.Info("orphan files cleaned up", "deleted", totalDeleted)
	}
}

// cleanOrphansForCamera scans a single camera directory for orphans.
func (cm *CleanupManager) cleanOrphansForCamera(ctx context.Context, cameraID string) int {
	dbBasenames, err := cm.db.ListRecordingPathsByCamera(ctx, cameraID)
	if err != nil {
		logger.Warn("orphan cleanup: failed to list recording paths", "camera_id", cameraID, "error", err)
		return 0
	}
	entries, err := cm.store.ListCameraDirEntries(cameraID)
	if err != nil {
		return 0 // directory may not exist
	}
	var deleted int
	for _, entry := range entries {
		name := entry.Name()
		info, err := entry.Info()
		if err != nil {
			continue
		}
		// Skip items younger than 1 hour
		if time.Since(info.ModTime()) < time.Hour {
			continue
		}
		// Skip known recordings
		if dbBasenames[name] {
			continue
		}
		fullPath := filepath.Join(cm.store.RootDir(), cameraID, name)
		if info.IsDir() {
			// MJPEG directories or .tmp directories
			if strings.HasPrefix(name, cameraID+"_") || strings.HasSuffix(name, ".tmp") {
				if err := os.RemoveAll(fullPath); err != nil {
					logger.Warn("orphan cleanup: failed to remove dir", "path", fullPath, "error", err)
					continue
				}
				logger.Info("deleted orphan directory", "camera_id", cameraID, "dir", name)
				if cm.metrics != nil {
					cm.metrics.CleanupDeleted.WithLabelValues("orphan").Add(1)
				}
				deleted++
			}
		} else if strings.HasSuffix(name, ".mp4") && strings.HasPrefix(name, cameraID+"_") {
			if err := os.Remove(fullPath); err != nil {
				logger.Warn("orphan cleanup: failed to delete file", "path", fullPath, "error", err)
				continue
			}
			logger.Info("deleted orphan file", "camera_id", cameraID, "file", name)
			if cm.metrics != nil {
				cm.metrics.CleanupDeleted.WithLabelValues("orphan").Add(1)
			}
			deleted++
		}
	}
	return deleted
}

// staleRecordCleanup scans DB for MJPEG recordings with merge_status='pending'
// whose directory on disk no longer exists, and marks them as merge_status='failed'.
func (cm *CleanupManager) staleRecordCleanup(ctx context.Context) {
	cameras, err := cm.activeCameraIDs(ctx)
	if err != nil {
		logger.Warn("stale record cleanup: failed to list cameras", "error", err)
		return
	}
	var totalFixed int
	for _, cam := range cameras {
		totalFixed += cm.fixStaleMJPEGRecords(ctx, cam)
	}
	if totalFixed > 0 {
		logger.Info("stale MJPEG records marked as failed", "fixed", totalFixed)
	}
}

// fixStaleMJPEGRecords checks pending MJPEG recordings for a camera and marks
// those with missing directories as failed. Returns count of fixed records.
func (cm *CleanupManager) fixStaleMJPEGRecords(ctx context.Context, cameraID string) int {
	recordings, err := cm.db.ListPendingMJPEGRecordings(ctx, cameraID)
	if err != nil {
		logger.Warn("stale record cleanup: failed to list pending MJPEG recordings",
			"camera_id", cameraID, "error", err)
		return 0
	}
	var staleIDs []string
	for _, rec := range recordings {
		if _, err := os.Stat(rec.FilePath); os.IsNotExist(err) {
			staleIDs = append(staleIDs, rec.ID)
		}
	}
	if len(staleIDs) == 0 {
		return 0
	}
	if err := cm.db.SetMergeStatus(ctx, staleIDs, model.MergeStatusFailed); err != nil {
		logger.Warn("stale record cleanup: failed to update merge status",
			"camera_id", cameraID, "error", err)
		return 0
	}
	logger.Info("stale MJPEG records marked failed", "camera_id", cameraID, "count", len(staleIDs))
	return len(staleIDs)
}

// repairZeroDurationRecordings fixes recordings with duration=0 by probing actual
// media files. Uses the pure-Go mediaprobe by default (no external binary);
// falls back to ffprobe when configured (cm.ffprobePath non-empty and available)
// for non-MP4 inputs or when mediaprobe fails.
func (cm *CleanupManager) repairZeroDurationRecordings(ctx context.Context) {
	recordings, err := cm.db.RepairZeroDurationRecordings(ctx)
	if err != nil {
		logger.Warn("zero-duration repair: failed to query recordings", "error", err)
		return
	}
	if len(recordings) == 0 {
		return
	}
	logger.Info("zero-duration repair: found recordings to repair", "count", len(recordings))
	var repaired int
	for _, rec := range recordings {
		// Check file exists on disk
		if _, err := os.Stat(rec.FilePath); err != nil {
			continue
		}
		duration := cm.probeDuration(ctx, rec.FilePath)
		// < 1ms in seconds — skip sub-ms values that truncate to zero
		// when converted to time.Duration (anti-pattern: avoid duration <= 0)
		if duration < 0.001 {
			continue
		}
		// Calculate corrected ended_at = started_at + probed duration
		endedAt := rec.StartedAt.Add(time.Duration(duration * float64(time.Second)))
		if err := cm.db.UpdateRecordingDuration(ctx, rec.ID, duration, endedAt); err != nil {
			logger.Warn("zero-duration repair: failed to update recording",
				"id", rec.ID, "error", err)
			continue
		}
		logger.Info("zero-duration repair: fixed recording",
			"id", rec.ID, "camera_id", rec.CameraID, "duration", duration)
		repaired++
	}
	if repaired > 0 {
		logger.Info("zero-duration repair: completed", "repaired", repaired, "total", len(recordings))
	}
}

// probeDuration returns the duration (in seconds) of a media file.
//
// It tries the pure-Go mediaprobe first (reads MP4 box metadata only — no
// external process, ~10-100x faster than ffprobe). If that fails or the file
// is not MP4, it falls back to ffprobe when cm.ffprobePath is configured and
// available. Returns 0 on any error (best-effort, never fatal).
func (cm *CleanupManager) probeDuration(ctx context.Context, filePath string) float64 {
	// Fast path: pure-Go probe — works without any external binary.
	if d, err := mediaprobe.ProbeDuration(filePath); err == nil && d > 0 {
		return d
	}

	// Fallback: ffprobe subprocess (only when explicitly configured).
	if cm.ffprobePath == "" {
		return 0
	}
	cmd := exec.CommandContext(
		ctx, cm.ffprobePath,
		"-v", "quiet",
		"-show_entries", "format=duration",
		"-of", "default=noprint_wrappers=1:nokey=1",
		filePath,
	)
	out, err := cmd.Output()
	if err != nil {
		logger.Warn("zero-duration repair: ffprobe failed", "path", filePath, "error", err)
		return 0
	}
	// Parse float from output (e.g. "32.400000\n")
	trimmed := strings.TrimSpace(string(out))
	d, err := strconv.ParseFloat(trimmed, 64)
	if err != nil {
		logger.Warn("zero-duration repair: failed to parse ffprobe output", "path", filePath, "output", trimmed, "error", err)
		return 0
	}
	return d
}

// performDatabaseMaintenance handles WAL checkpoint scheduling and incremental vacuum.
// Called after cleanup + PRAGMA optimize in each RunOnce cycle.
//
// WAL checkpoint strategy:
// - PASSIVE is the default (non-blocking).
// - If PASSIVE returns busy=1 for 3 consecutive cycles, escalate to TRUNCATE.
// - TRUNCATE resets the counter.
//
// Incremental vacuum:
// - If fragmentation > 20%, reclaim up to 1000 free pages stepwise.
// - NOT full VACUUM — does not require exclusive lock.

func (cm *CleanupManager) performDatabaseMaintenance(ctx context.Context) {
	// Step 1: WAL checkpoint (skip if WAL is under 10MB)
	walSize, err := cm.db.GetWALSize()
	if err != nil {
		logger.Warn("DB maintenance: failed to get WAL size", "error", err)
	} else if walSize > 10*1024*1024 {
		logger.Info("DB maintenance: large WAL file, attempting checkpoint", "size_bytes", walSize)
		busy, _, _, err := cm.db.CheckpointWAL(ctx, "PASSIVE")
		if err != nil {
			logger.Warn("DB maintenance: PASSIVE checkpoint failed", "error", err)
		} else if busy == 1 {
			cm.consecutivePassiveFailures++
			logger.Warn("DB maintenance: PASSIVE checkpoint busy",
				"consecutive_failures", cm.consecutivePassiveFailures)
			if cm.consecutivePassiveFailures >= 3 {
				logger.Info("DB maintenance: escalating to TRUNCATE checkpoint")
				busy2, logFrames, ckptFrames, err2 := cm.db.CheckpointWAL(ctx, "TRUNCATE")
				if err2 != nil {
					logger.Warn("DB maintenance: TRUNCATE checkpoint failed", "error", err2)
				} else {
					logger.Info("DB maintenance: TRUNCATE checkpoint completed",
						"busy", busy2, "log_frames", logFrames, "checkpointed_frames", ckptFrames)
					cm.consecutivePassiveFailures = 0
				}
			}
		} else {
			// PASSIVE succeeded, reset counter
			cm.consecutivePassiveFailures = 0
		}
	}

	// Step 2: Incremental vacuum (only if fragmentation > 20%)
	frac, err := cm.db.GetFragmentationRatio(ctx)
	if err != nil {
		logger.Warn("DB maintenance: failed to get fragmentation ratio", "error", err)
	} else if frac > 0.50 {
		// Severe fragmentation (>50%): incremental_vacuum is too slow (1000 pages/cycle)
		// and is a no-op on DBs created before auto_vacuum was enabled (auto_vacuum=0).
		// Do a full online compaction via VACUUM INTO — non-blocking, swaps files atomically.
		logger.Info("DB maintenance: severe fragmentation, running online compaction", "fragmentation_ratio", fmt.Sprintf("%.1f%%", frac*100))
		saved, compErr := cm.db.CompactOnline(ctx)
		if compErr != nil {
			logger.Warn("DB maintenance: online compaction failed", "error", compErr)
		} else {
			logger.Info("DB maintenance: online compaction succeeded", "saved_bytes", saved)
		}
	} else if frac > 0.20 {
		// Moderate fragmentation (20-50%): reclaim free pages incrementally.
		// Use a larger batch (5000) when fragmentation is high for faster reclamation.
		pages := 1000
		if frac > 0.35 {
			pages = 5000
		}
		logger.Info("DB maintenance: high fragmentation detected", "fragmentation_ratio", fmt.Sprintf("%.1f%%", frac*100), "vacuum_pages", pages)
		if err := cm.db.IncrementalVacuum(ctx, pages); err != nil {
			logger.Warn("DB maintenance: incremental vacuum failed", "error", err)
		}
	}
}

// updateSQLiteMetrics updates all SQLite database health metrics.
// Called at the end of each cleanup cycle after performDatabaseMaintenance.
func (cm *CleanupManager) updateSQLiteMetrics(ctx context.Context) {
	if cm.metrics == nil {
		return
	}

	// Update WAL size
	if walSize, err := cm.db.GetWALSize(); err == nil {
		cm.metrics.SQLiteWALSizeBytes.Set(float64(walSize))
	}

	// Update DB file size (use actual DB path, not hardcoded filename)
	dbPath := cm.db.Path()
	if info, err := os.Stat(dbPath); err == nil {
		cm.metrics.SQLiteDBSizeBytes.Set(float64(info.Size()))
	}

	// Update fragmentation ratio
	if frac, err := cm.db.GetFragmentationRatio(ctx); err == nil {
		cm.metrics.SQLiteFragmentationRatio.Set(frac)
	}

	// Update connection pool stats (writer pool — the single serialized connection)
	if db := cm.db.DB(); db != nil {
		stats := db.Stats()
		cm.metrics.SQLiteOpenConnections.Set(float64(stats.OpenConnections))
		cm.metrics.SQLiteInUseConnections.Set(float64(stats.InUse))
	}
	// Update read pool stats (separate pool for SELECTs — not visible via DB().Stats()).
	// WaitCount/WaitDuration reveal whether the pool is undersized: nonzero sustained
	// growth means callers are blocking for a connection and SetReadPoolSize should rise.
	if rstats, ok := cm.db.ReadPoolStats(); ok {
		cm.metrics.SQLiteReadOpenConnections.Set(float64(rstats.OpenConnections))
		cm.metrics.SQLiteReadInUseConnections.Set(float64(rstats.InUse))
		cm.metrics.SQLiteReadWaitCount.Add(float64(rstats.WaitCount))
		cm.metrics.SQLiteReadWaitDuration.Set(rstats.WaitDuration.Seconds())
	}
}
