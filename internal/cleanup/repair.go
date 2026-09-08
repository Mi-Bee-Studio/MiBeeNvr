package cleanup

// This file holds the database self-repair strategies:
//   - staleRecordCleanup: recordings stuck in merge_status='pending' whose
//     file no longer exists (and never did — ghost rows) → deleted, once
//     older than ghostPendingGrace.
//   - repairZeroDurationRecordings: recordings with duration=0 are re-probed
//     (pure-Go mediaprobe, ffprobe fallback) and updated with the real duration.
//
// Both run once per RunOnce cycle.
//
// Extracted from cleanup.go (#227); ghost-row sweep generalized from the
// MJPEG-only 'failed' marking (2026-09-08 incident: the startup temp-cleanup
// scan deleted an in-flight segment temp; the recorder still inserted its DB
// row and the entry became a permanent 404 in the recordings list).

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/mediaprobe"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
)

// ghostPendingGrace is how long a pending row with a missing file survives
// before the sweep deletes it. The guard window tolerates temporarily
// unmounted storage (per-camera root override hiccup): files come back on
// remount and must not take their metadata with them.
const ghostPendingGrace = 24 * time.Hour

// staleRecordCleanup deletes ghost rows: merge_status='pending' recordings
// whose file does not exist on disk and are older than ghostPendingGrace.
// Such rows can never merge or play — they are permanent 404 entries and
// endless retry fuel for the merge pipeline.
func (cm *CleanupManager) staleRecordCleanup(ctx context.Context) {
	cutoff := time.Now().Add(-ghostPendingGrace)
	recordings, err := cm.db.ListStalePendingRecordings(ctx, cutoff, 500)
	if err != nil {
		logger.Warn("stale record cleanup: failed to list pending recordings", "error", err)
		return
	}
	var ghostIDs []string
	for i := range recordings {
		if !cm.recordingFileMissing(&recordings[i]) {
			continue
		}
		ghostIDs = append(ghostIDs, recordings[i].ID)
	}
	if len(ghostIDs) == 0 {
		return
	}
	if _, err := cm.db.DeleteRecordingsBatch(ctx, ghostIDs); err != nil {
		logger.Warn("stale record cleanup: failed to delete ghost rows", "error", err)
		return
	}
	logger.Info("deleted ghost pending recordings (file missing)", "count", len(ghostIDs))
}

// recordingFileMissing reports whether a recording's file is absent. Main
// recorders store absolute paths; tierrec sub-layer rows store paths
// relative to the storage root. Stat errors other than NotExist are treated
// as present (conservative — never delete metadata on a stat failure).
func (cm *CleanupManager) recordingFileMissing(rec *model.Recording) bool {
	path := rec.FilePath
	if path == "" {
		return true
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(cm.store.RootDir(), path)
	}
	_, err := os.Stat(path)
	return err != nil && os.IsNotExist(err)
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
