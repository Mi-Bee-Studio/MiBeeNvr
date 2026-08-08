package cleanup

// This file holds the database self-repair strategies:
//   - staleRecordCleanup: MJPEG recordings stuck in merge_status='pending'
//     whose directory no longer exists → marked 'failed'.
//   - repairZeroDurationRecordings: recordings with duration=0 are re-probed
//     (pure-Go mediaprobe, ffprobe fallback) and updated with the real duration.
//
// Both run once per RunOnce cycle.
//
// Extracted from cleanup.go (#227).

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/mediaprobe"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
)

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
