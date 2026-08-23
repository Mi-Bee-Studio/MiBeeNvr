package cleanup

// This file holds the disk-threshold cleanup strategy: when disk usage
// exceeds the configured percentage, delete the oldest recordings in batches
// until usage drops back below the threshold. Runs once per RunOnce cycle.
//
// Extracted from cleanup.go (#227).

import (
	"context"
)

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
	// With motion-aware ordering (issue #435) the eviction prefers boring
	// segments: static first, unanalyzed neutral, active last — age still
	// breaks ties, so within one score band behavior matches the legacy path.
	fetch := cm.db.ListOldestRecordings
	if cm.motionAwareDisk {
		fetch = cm.db.ListOldestRecordingsMotionAware
	}
	for {
		batch, err := fetch(ctx, 50)
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
