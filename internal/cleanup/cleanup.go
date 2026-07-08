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
	ffprobePath               string        // optional ffprobe fallback for probeDuration; empty = pure-Go mediaprobe only
	eventBus                  *event.EventBus
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

// Run starts the periodic cleanup loop. It blocks until ctx is cancelled.
func (cm *CleanupManager) Run(ctx context.Context) {
	ticker := time.NewTicker(cm.interval)
	defer ticker.Stop()

	// Run once immediately
	cm.RunOnce(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cm.RunOnce(ctx)
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

// deleteRecording deletes the DB record first, then the file from disk.
// File deletion errors are logged but not returned (orphaned files are acceptable).
// Publishes a segment.deleted event so MiBeeVision can cancel in-progress processing.
// Skips deletion if the recording is currently being processed by MiBeeVision
// (ai_status = "processing") to prevent losing in-flight AI analysis.
func (cm *CleanupManager) deleteRecording(ctx context.Context, rec *model.Recording) error {
	// Protect recordings being processed by MiBeeVision
	if status, err := cm.db.GetRecordingAIStatus(ctx, rec.ID); err == nil && status == "processing" {
		logger.Debug("skipping deletion of recording being processed by MiBeeVision",
			"recording_id", rec.ID, "ai_status", status)
		return nil
	}

	if err := cm.db.DeleteRecording(ctx, rec.ID); err != nil {
		return err
	}
	if err := cm.store.DeleteFile(rec.FilePath); err != nil {
		logger.Warn("failed to delete file", "file_path", rec.FilePath, "error", err)
	}
	// Publish segment.deleted event for MiBeeVision cancellation
	if cm.eventBus != nil {
		cm.eventBus.Publish(ctx, event.TopicSegmentDeleted, event.SegmentDeleted{
			RecordingID: rec.ID,
			CameraID:    rec.CameraID,
			FilePath:    rec.FilePath,
			Reason:      "retention_expired",
		})
	}
	return nil
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
	cameras, err := cm.db.ListCameras(ctx)
	if err != nil {
		logger.Warn("orphan cleanup: failed to list cameras", "error", err)
		return
	}
	var totalDeleted int
	for _, cam := range cameras {
		totalDeleted += cm.cleanOrphansForCamera(ctx, cam.ID)
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
	cameras, err := cm.db.ListCameras(ctx)
	if err != nil {
		logger.Warn("stale record cleanup: failed to list cameras", "error", err)
		return
	}
	var totalFixed int
	for _, cam := range cameras {
		totalFixed += cm.fixStaleMJPEGRecords(ctx, cam.ID)
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
	} else if frac > 0.20 {
		logger.Info("DB maintenance: high fragmentation detected", "fragmentation_ratio", frac)
		if err := cm.db.IncrementalVacuum(ctx, 1000); err != nil {
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

	// Update DB file size (construct path from store root dir)
	dbPath := filepath.Join(cm.store.RootDir(), "recordings.db")
	if info, err := os.Stat(dbPath); err == nil {
		cm.metrics.SQLiteDBSizeBytes.Set(float64(info.Size()))
	}

	// Update fragmentation ratio
	if frac, err := cm.db.GetFragmentationRatio(ctx); err == nil {
		cm.metrics.SQLiteFragmentationRatio.Set(frac)
	}

	// Update connection pool stats
	if db := cm.db.DB(); db != nil {
		stats := db.Stats()
		cm.metrics.SQLiteOpenConnections.Set(float64(stats.OpenConnections))
		cm.metrics.SQLiteInUseConnections.Set(float64(stats.InUse))
	}
}
