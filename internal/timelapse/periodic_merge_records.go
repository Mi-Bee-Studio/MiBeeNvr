package timelapse

import (
	"context"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/mediaprobe"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
)

// finalizeMerge updates segment statuses to daily_merged after a successful merge.
// When a TimelapseMergeStore is configured, it also upserts a row in the
// timelapse_merges table for this output so the frontend can discover and
// play the long-window timelapse video.
func (m *PeriodicMergeManager) finalizeMerge(ctx context.Context, segments []model.Recording, outputPath string) error {
	ids := make([]string, len(segments))
	for i, seg := range segments {
		ids[i] = seg.ID
	}

	// Clean up retry counts on successful merge.
	m.retryMu.Lock()
	for _, seg := range segments {
		delete(m.retryCounts, seg.ID)
	}
	m.retryMu.Unlock()

	// Update progress to 100 for completed merge.
	m.updateProgressBatch(ctx, segments, 100)

	if m.updater != nil {
		if err := m.updater.SetMergeStatus(ctx, ids, model.MergeStatusDailyMerged); err != nil {
			slog.Warn(
				"periodic merge: failed to update merge statuses",
				"count", len(ids),
				"error", err,
			)
		}
	}

	// Record this periodic-merge output in the timelapse_merges table (if a
	// store is configured). Failures here are non-fatal — the merge itself
	// succeeded; only the DB bookkeeping failed.
	m.recordMergeRow(ctx, segments, outputPath)

	// Prune intermediate rolling-merge .mp4 outputs (the files at
	// recordings.merge_path) now that the segments have been folded into the
	// long-window periodic output. The raw frame directories are preserved.
	if !m.retainIntermediateMP4 {
		m.pruneIntermediateMP4s(ctx, segments)
	}

	slog.Info(
		"periodic merge: completed successfully",
		"segments", len(segments),
		"output_path", outputPath,
	)
	return nil
}

// recordMergeRow upserts (insert-or-complete) a timelapse_merges row for the
// given output. Each finalizeMerge invocation corresponds to exactly one
// output file (the per-codec path calls finalizeMerge once per codec group),
// so one row is written per call. Failures are logged but do NOT fail the
// merge — the MP4 on disk is the source of truth.
//
// Codec and frame count are read from the output via mediaprobe (pure-Go MP4
// box parsing, no ffprobe). File size comes from os.Stat.

// recordMergeRow upserts (insert-or-complete) a timelapse_merges row for the
// given output. Each finalizeMerge invocation corresponds to exactly one
// output file (the per-codec path calls finalizeMerge once per codec group),
// so one row is written per call. Failures are logged but do NOT fail the
// merge — the MP4 on disk is the source of truth.
//
// Codec and frame count are read from the output via mediaprobe (pure-Go MP4
// box parsing, no ffprobe). File size comes from os.Stat.
func (m *PeriodicMergeManager) recordMergeRow(ctx context.Context, segments []model.Recording, outputPath string) {
	if m.mergeStore == nil {
		return // legacy mode: no DB recording
	}
	m.runCtxMu.Lock()
	rc := m.runCtx
	m.runCtxMu.Unlock()
	if rc.cameraID == "" {
		return // no Run context (shouldn't happen, but defend against misuse)
	}

	// Best-effort metadata probes. Failures degrade the row but don't skip it.
	var fileSize int64
	if st, err := os.Stat(outputPath); err == nil {
		fileSize = st.Size()
	}
	frameCount := 0
	codec := ""
	if info, err := mediaprobe.ProbeMP4(outputPath); err == nil {
		frameCount = info.FrameCount
		// mediaprobe reports Codec as "h264" or "h265" (internal names), matching
		// model.TimelapseMergeCodec* values directly. For mjpa outputs the probe
		// returns the raw codec string — normalize empty/unknown to "mjpeg".
		switch info.Codec {
		case model.TimelapseMergeCodecH264, model.TimelapseMergeCodecH265:
			codec = info.Codec
		default:
			codec = model.TimelapseMergeCodecMJPEG
		}
	}

	sourceIDs := sourceSegmentIDsJSON(segments)

	// Find-or-create: if a row already exists for this (camera, window,
	// duration_label), complete it in place (re-merge of the same window);
	// otherwise insert then complete. This handles both first-run and
	// re-trigger (manual merge API) cleanly.
	existing, err := m.mergeStore.FindTimelapseMergeByWindow(ctx, rc.cameraID, rc.startTime, rc.durationLabel)
	if err != nil {
		slog.Warn("periodic merge: find existing merge row failed",
			"camera_id", rc.cameraID, "error", err)
		return
	}
	if existing != nil {
		if err := m.mergeStore.CompleteTimelapseMerge(ctx, existing.ID, outputPath, fileSize, frameCount, codec, sourceIDs); err != nil {
			slog.Warn("periodic merge: complete existing merge row failed",
				"merge_id", existing.ID, "error", err)
		}
		return
	}
	row := &model.TimelapseMerge{
		CameraID:         rc.cameraID,
		WindowStart:      rc.startTime,
		WindowEnd:        rc.endTime,
		DurationLabel:    rc.durationLabel,
		OutputPath:       outputPath,
		FileSize:         fileSize,
		FrameCount:       frameCount,
		Codec:            codec,
		FPS:              m.fps,
		SourceSegmentIDs: sourceIDs,
		Status:           model.TimelapseMergeStatusCompleted,
		CompletedAt:      time.Now().UTC(),
	}
	if _, err := m.mergeStore.InsertTimelapseMerge(ctx, row); err != nil {
		slog.Warn("periodic merge: insert merge row failed",
			"camera_id", rc.cameraID, "output_path", outputPath, "error", err)
	}
}

// pruneIntermediateMP4s removes the per-segment rolling-merge .mp4 files (at
// recordings.merge_path) for the given source segments and clears the DB
// pointer. The raw frame directories (recordings.file_path) are preserved so
// the periodic output can be regenerated.
//
// Failures are best-effort and logged — a missing file or DB error does NOT
// fail the overall merge, which has already produced its output.

// pruneIntermediateMP4s removes the per-segment rolling-merge .mp4 files (at
// recordings.merge_path) for the given source segments and clears the DB
// pointer. The raw frame directories (recordings.file_path) are preserved so
// the periodic output can be regenerated.
//
// Failures are best-effort and logged — a missing file or DB error does NOT
// fail the overall merge, which has already produced its output.
func (m *PeriodicMergeManager) pruneIntermediateMP4s(ctx context.Context, segments []model.Recording) {
	var clearedIDs []string
	prunedCount := 0
	for _, seg := range segments {
		if seg.MergePath == "" {
			continue
		}
		if err := os.Remove(seg.MergePath); err != nil {
			if !os.IsNotExist(err) {
				slog.Warn("periodic merge: prune intermediate mp4 failed",
					"recording_id", seg.ID, "merge_path", seg.MergePath, "error", err)
			}
			// Even on error, clear the DB pointer — the file is either gone or
			// unwritable; either way we don't want playback pointing at it.
		} else {
			prunedCount++
		}
		clearedIDs = append(clearedIDs, seg.ID)
	}
	if len(clearedIDs) == 0 {
		return
	}
	if m.pruner != nil {
		if err := m.pruner.ClearMergePathBatch(ctx, clearedIDs); err != nil {
			slog.Warn("periodic merge: clear merge_path DB pointer failed",
				"count", len(clearedIDs), "error", err)
		}
	}
	if prunedCount > 0 {
		slog.Info("periodic merge: pruned intermediate mp4 outputs",
			"pruned", prunedCount, "cleared_db_pointers", len(clearedIDs))
	}
}

// sourceSegmentIDsJSON renders the list of source recording IDs as a JSON
// array string for the timelapse_merges.source_segment_ids column.

// sourceSegmentIDsJSON renders the list of source recording IDs as a JSON
// array string for the timelapse_merges.source_segment_ids column.
func sourceSegmentIDsJSON(segments []model.Recording) string {
	if len(segments) == 0 {
		return "[]"
	}
	var b strings.Builder
	b.WriteByte('[')
	for i, s := range segments {
		if i > 0 {
			b.WriteByte(',')
		}
		// strconv.Quote handles escaping.
		b.WriteString(strconv.Quote(s.ID))
	}
	b.WriteByte(']')
	return b.String()
}

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
