package cleanup

import (
	"context"
	"log"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
)

// CleanupManager handles periodic cleanup of old recordings.
// It supports two cleanup strategies:
//   - Time-based: delete recordings older than retention period
//   - Disk-threshold: delete oldest unpinned recordings when disk usage exceeds threshold
type CleanupManager struct {
	db            *storage.DB
	store         *storage.Manager
	retention     time.Duration
	diskThreshold int // percent
	interval      time.Duration
}

// NewCleanupManager creates a new CleanupManager with the given config.
func NewCleanupManager(db *storage.DB, store *storage.Manager, cfg config.CleanupConfig) (*CleanupManager, error) {
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

// RunOnce performs a single cleanup pass: time-based then disk-threshold.
func (cm *CleanupManager) RunOnce(ctx context.Context) error {
	if err := cm.timeBasedCleanup(ctx); err != nil {
		log.Printf("[cleanup] time-based cleanup error: %v", err)
	}
	if err := cm.diskThresholdCleanup(ctx); err != nil {
		log.Printf("[cleanup] disk-threshold cleanup error: %v", err)
	}
	return nil
}

// timeBasedCleanup deletes recordings where:
// - pinned = false
// - ended_at < NOW() - retention
func (cm *CleanupManager) timeBasedCleanup(ctx context.Context) error {
	retentionDays := int(cm.retention.Hours() / 24)
	recordings, err := cm.db.ListExpiredRecordings(ctx, retentionDays)
	if err != nil {
		return err
	}

	for _, rec := range recordings {
		if err := cm.deleteRecording(ctx, &rec); err != nil {
			log.Printf("[cleanup] failed to delete recording %s: %v", rec.ID, err)
			continue
		}
		log.Printf("[cleanup] time-based: deleted recording %s", rec.ID)
	}
	return nil
}

// diskThresholdCleanup deletes oldest unpinned recordings when disk usage exceeds threshold.
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

	log.Printf("[cleanup] disk usage %d%% exceeds threshold %d%%, starting cleanup", usagePercent, cm.diskThreshold)

	// Fetch recordings in batches until usage drops below threshold
	for {
		recordings, err := cm.db.ListOldestUnpinnedRecordings(ctx, 50)
		if err != nil {
			return err
		}
		if len(recordings) == 0 {
			break
		}

		deleted := false
		for _, rec := range recordings {
			if err := cm.deleteRecording(ctx, &rec); err != nil {
				log.Printf("[cleanup] failed to delete recording %s: %v", rec.ID, err)
				continue
			}
			log.Printf("[cleanup] disk-threshold: deleted recording %s", rec.ID)
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
		log.Printf("[cleanup] failed to delete file %s: %v", rec.FilePath, err)
	}
	return nil
}
