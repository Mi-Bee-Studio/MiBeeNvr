package cleanup

// This file holds the orphan-file cleanup strategy: scans each configured
// camera's directory for files/subdirectories not tracked in the recordings
// table (leftover .mp4 segments, abandoned MJPEG/.tmp dirs) and removes them.
// Items younger than 1h are skipped to avoid racing an in-progress write.
// Runs once per RunOnce cycle.
//
// Extracted from cleanup.go (#227).

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"
)

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
