package cleanup

// This file holds the dark-segment cleanup strategy: recordings detected as
// too dark (merge_status='dark', e.g. night without IR) are deleted promptly
// (with a 1h grace period so they're briefly visible before removal). Keeping
// them provides no playback value. Runs once per RunOnce cycle.
//
// Extracted from cleanup.go (#227).

import (
	"context"
	"time"
)

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
