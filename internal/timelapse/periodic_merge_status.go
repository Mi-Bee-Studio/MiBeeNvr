package timelapse

import (
	"context"
	"log/slog"
	"os"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
)

// markMergeFailed updates retry counts and marks segments as failed.
// Segments are retried up to 3 times before being permanently marked as failed.
//
// Only segments that do NOT already have a usable merged .mp4 file are marked
// failed. Segments that were previously merged (have a non-empty merge_path
// pointing to an existing, non-empty file) are left untouched — the periodic
// merge failure is in the aggregation step, not in the per-segment merge, so
// those segments remain individually playable. Without this guard, a single
// bad segment in a window would poison all the good ones to "failed",
// creating a vicious cycle where the next periodic merge retry excludes them
// via filterEligibleSegments (which only admits "failed" segments under the
// retry-count cap, reset on process restart).
func (m *PeriodicMergeManager) markMergeFailed(ctx context.Context, segments []model.Recording, mergeErr error) error {
	// Partition: only mark segments that lack a usable merged output.
	var ids []string
	var failedSegments []model.Recording
	for _, seg := range segments {
		if seg.MergePath != "" {
			if info, err := os.Stat(seg.MergePath); err == nil && info.Size() > 0 {
				// This segment has a good .mp4 — leave its status alone.
				continue
			}
		}
		ids = append(ids, seg.ID)
		failedSegments = append(failedSegments, seg)
	}

	if len(ids) == 0 {
		// All segments already have usable outputs — nothing to mark failed.
		slog.Warn(
			"periodic merge: aggregation failed but all segments have usable merged output; leaving statuses unchanged",
			"segments", len(segments),
			"error", mergeErr,
		)
		return nil
	}

	// Increment retry counts and check if any segment has exhausted retries.
	m.retryMu.Lock()
	maxRetriesReached := false
	now := time.Now()
	for _, seg := range failedSegments {
		info := m.retryCounts[seg.ID]
		info.count++
		info.timestamp = now
		m.retryCounts[seg.ID] = info
		if info.count >= 3 {
			maxRetriesReached = true
		}
	}
	m.retryMu.Unlock()

	// Update progress to 0 for failed merge.
	m.updateProgressBatch(ctx, failedSegments, 0)

	if m.updater != nil {
		if err := m.updater.SetMergeStatus(ctx, ids, model.MergeStatusFailed); err != nil {
			slog.Warn(
				"periodic merge: failed to set merge status to failed",
				"count", len(ids),
				"error", err,
			)
			return err
		}
	}

	if maxRetriesReached {
		slog.Error(
			"periodic merge: permanently failed after 3 retries",
			"segments", len(failedSegments),
			"error", mergeErr,
		)
	} else {
		slog.Warn(
			"periodic merge: failed, will retry on next cycle",
			"segments", len(failedSegments),
			"retry_count", func() int {
				m.retryMu.Lock()
				defer m.retryMu.Unlock()
				if len(failedSegments) > 0 {
					return m.retryCounts[failedSegments[0].ID].count
				}
				return 0
			}(),
			"error", mergeErr,
		)
	}

	return nil
}

// updateProgressBatch updates merge progress for a batch of segments in a single
// chunked UPDATE rather than one statement per segment. This is the hot path during
// FFmpeg/Go merge progress parsing, previously issuing N statements per progress tick.

// updateProgressBatch updates merge progress for a batch of segments in a single
// chunked UPDATE rather than one statement per segment. This is the hot path during
// FFmpeg/Go merge progress parsing, previously issuing N statements per progress tick.
func (m *PeriodicMergeManager) updateProgressBatch(ctx context.Context, segments []model.Recording, progress int) {
	if m.updater == nil || len(segments) == 0 {
		return
	}
	ids := make([]string, len(segments))
	for i, seg := range segments {
		ids[i] = seg.ID
	}
	if err := m.updater.UpdateMergeProgressBatch(ctx, ids, progress); err != nil {
		slog.Warn(
			"periodic merge: failed to update merge progress (batch)",
			"segment_count", len(ids),
			"progress", progress,
			"error", err,
		)
	}
}

// filterEligibleSegments filters recordings to include merged segments
// and failed segments with remaining retry attempts (< 3).

// filterEligibleSegments filters recordings to include merged segments
// and failed segments with remaining retry attempts (< 3).
func (m *PeriodicMergeManager) filterEligibleSegments(recordings []model.Recording) []model.Recording {
	var segments []model.Recording
	m.retryMu.Lock()
	defer m.retryMu.Unlock()
	for _, r := range recordings {
		switch r.MergeStatus {
		case model.MergeStatusMerged:
			// Rolling-merged segment (has .mp4 output) — eligible for concat.
			segments = append(segments, r)
		case model.MergeStatusFailed:
			// Retryable failed segment.
			if info, ok := m.retryCounts[r.ID]; ok && info.count < 3 {
				segments = append(segments, r)
			}
		case "", model.MergeStatusPending:
			// Unmerged raw frame directory (merge_enabled=false skipped rolling
			// merge, or segment just inserted) — eligible for Go keyframe merge (Tier 4).
			segments = append(segments, r)
		}
	}

	// Clean stale retryCounts entries: entries not in current recordings and older than 24h.
	validIDs := make(map[string]struct{}, len(recordings))
	for _, r := range recordings {
		validIDs[r.ID] = struct{}{}
	}
	cutoff := time.Now().Add(-24 * time.Hour)
	for id, info := range m.retryCounts {
		if _, exists := validIDs[id]; !exists && info.timestamp.Before(cutoff) {
			delete(m.retryCounts, id)
		}
	}
	return segments
}

// filterMergedSegments filters recordings to only those with merge_status='merged'.
// This is a standalone helper used by daily.go (legacy compat).

// filterMergedSegments filters recordings to only those with merge_status='merged'.
// This is a standalone helper used by daily.go (legacy compat).
func filterMergedSegments(recordings []model.Recording) []model.Recording {
	var segments []model.Recording
	for _, r := range recordings {
		if r.MergeStatus == model.MergeStatusMerged {
			segments = append(segments, r)
		}
	}
	return segments
}

// hasUnmergedRawSegments returns true if any segment is a directory of raw
// frames (JPEG/H.264/H.265) rather than a rolling-merged .mp4 file. This
// happens when merge_enabled=false skips rolling merge — the segments are
// frame directories that must go through Go keyframe merge (Tier 4) directly.

// hasUnmergedRawSegments returns true if any segment is a directory of raw
// frames (JPEG/H.264/H.265) rather than a rolling-merged .mp4 file. This
// happens when merge_enabled=false skips rolling merge — the segments are
// frame directories that must go through Go keyframe merge (Tier 4) directly.
func hasUnmergedRawSegments(segments []model.Recording) bool {
	for _, seg := range segments {
		if seg.MergeStatus != model.MergeStatusMerged {
			return true
		}
	}
	return false
}

// checkSegmentCompatibility checks if all segments have compatible resolution and codec.
// Uses the pure-Go mediaprobe by default, falling back to ffprobe when needed.
