package cleanup

// This file holds the time-based retention strategies: per-camera recording
// retention, archived-recording retention (per-camera archive_retention_days),
// health-event retention, and transcode-task-history retention. Each runs once
// per RunOnce cycle.
//
// Extracted from cleanup.go (#227).

import (
	"context"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
)

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
