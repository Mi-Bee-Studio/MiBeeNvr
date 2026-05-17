package cleanup

import (
	"context"
	"log/slog"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
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
	db            *storage.DB
	store         *storage.Manager
	retention     time.Duration
	diskThreshold int // percent
	interval      time.Duration
	metrics        *metrics.Metrics
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

// RunOnce performs a single cleanup pass: time-based, archived, then disk-threshold.
func (cm *CleanupManager) RunOnce(ctx context.Context) error {
	if err := cm.timeBasedCleanup(ctx); err != nil {
		logger.Error("time-based cleanup error", "error", err)
	}
	cm.archivedRetentionCleanup(ctx)
	if err := cm.diskThresholdCleanup(ctx); err != nil {
		logger.Error("disk-threshold cleanup error", "error", err)
	}
	return nil
}

// timeBasedCleanup deletes recordings per-camera where:
// - ended_at < NOW() - retention
// - ended_at < NOW() - retention
// Each camera uses its own retention_days (0 = fallback to global).
func (cm *CleanupManager) timeBasedCleanup(ctx context.Context) error {
	globalRetentionDays := int(cm.retention.Hours() / 24)

	cameras, err := cm.db.ListCameras(ctx)
	if err != nil {
		return err
	}

	for _, cam := range cameras {
		retentionDays := cam.RetentionDays
		if retentionDays <= 0 {
			retentionDays = globalRetentionDays
		}
		if retentionDays <= 0 {
			continue
		}

		recordings, err := cm.db.ListExpiredRecordingsByCamera(ctx, cam.ID, retentionDays)
		if err != nil {
			logger.Warn("failed to list expired recordings for camera", "camera_id", cam.ID, "error", err)
			continue
		}

		for _, rec := range recordings {
			if err := cm.deleteRecording(ctx, &rec); err != nil {
				logger.Warn("failed to delete recording", "recording_id", rec.ID, "error", err)
				continue
			}
			logger.Info("deleted recording (time-based)", "recording_id", rec.ID, "camera_id", cam.ID)
			if cm.metrics != nil {
				cm.metrics.CleanupDeleted.WithLabelValues("retention").Add(1)
			}
		}
	}
	return nil
}

// diskThresholdCleanup deletes oldest recordings when disk usage exceeds threshold.
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

	// Fetch recordings in batches until usage drops below threshold
	for {
		recordings, err := cm.db.ListOldestRecordings(ctx, 50)
		if err != nil {
			return err
		}
		if len(recordings) == 0 {
			break
		}

		deleted := false
		for _, rec := range recordings {
			if err := cm.deleteRecording(ctx, &rec); err != nil {
				logger.Warn("failed to delete recording", "recording_id", rec.ID, "error", err)
				continue
			}
			logger.Info("deleted recording (disk-threshold)", "recording_id", rec.ID)
			if cm.metrics != nil {
				cm.metrics.CleanupDeleted.WithLabelValues("disk_threshold").Add(1)
			}
			deleted = true
		}

		if !deleted {
			break
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

// deleteRecording deletes the DB record first, then the file from disk.
// File deletion errors are logged but not returned (orphaned files are acceptable).
func (cm *CleanupManager) deleteRecording(ctx context.Context, rec *model.Recording) error {
	if err := cm.db.DeleteRecording(ctx, rec.ID); err != nil {
		return err
	}
	if err := cm.store.DeleteFile(rec.FilePath); err != nil {
		logger.Warn("failed to delete file", "file_path", rec.FilePath, "error", err)
	}
	return nil
}

// archivedRetentionCleanup deletes expired archived recordings and cleans up empty archive groups.
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

		recordings, err := cm.db.ListExpiredArchivedRecordingsByCamera(ctx, cam.ID, cam.ArchiveRetentionDays)
		if err != nil {
			logger.Warn("failed to list expired archived recordings", "camera_id", cam.ID, "error", err)
			continue
		}

		for _, rec := range recordings {
			if err := cm.deleteRecording(ctx, &rec); err != nil {
				logger.Warn("failed to delete archived recording", "recording_id", rec.ID, "error", err)
				continue
			}
			logger.Info("deleted archived recording (retention)", "recording_id", rec.ID, "camera_id", cam.ID)
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
