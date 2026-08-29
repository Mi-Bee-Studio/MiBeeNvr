package merge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"os"
	"strconv"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
)

// Uses the per-batch lock release pattern to avoid blocking live events.
func (r *RollingMergeCoordinator) backfillMP4(ctx context.Context, cameraID string, recs []*model.Recording) (int, error) {
	const backfillBatchSize = 20 // small batches to yield lock to real-time events
	batchPause := adaptiveBatchPause(len(recs), r.diskFreePercent())

	// Group recordings by natural-hour window for batch merging.
	// Segments in different hours go into separate merge batches.
	type windowBatch struct {
		start time.Time
		recs  []*model.Recording
	}
	var windows []windowBatch
	for _, rec := range recs {
		windowStart := rec.StartedAt.UTC().Truncate(time.Hour)
		if len(windows) == 0 || !windows[len(windows)-1].start.Equal(windowStart) {
			windows = append(windows, windowBatch{start: windowStart})
		}
		windows[len(windows)-1].recs = append(windows[len(windows)-1].recs, rec)
	}

	merged := 0
	for _, win := range windows {
		if ctx.Err() != nil {
			break
		}

		// Process each window in sub-batches to limit lock hold time.
		for i := 0; i < len(win.recs); i += backfillBatchSize {
			if ctx.Err() != nil {
				break
			}
			endIdx := i + backfillBatchSize
			if endIdx > len(win.recs) {
				endIdx = len(win.recs)
			}
			batch := win.recs[i:endIdx]

			// Filter missing files.
			var valid []*model.Recording
			for _, rec := range batch {
				if _, err := os.Stat(rec.FilePath); os.IsNotExist(err) {
					continue
				}
				valid = append(valid, rec)
			}
			if len(valid) < 2 {
				// Not enough segments to merge in this batch. See the long
				// comment in backfillBatchFormat for why we do NOT mark these
				// merged unconditionally — doing so would eject recent segments
				// from the queue before their neighbors arrive.
				//
				// BUT: historical singletons (older than
				// singletonPurgeAge) are different. A segment that has sat
				// pending alone in its window for this long will essentially
				// never gain a neighbor — the camera has long since moved on
				// to other hours. Leaving them pending forever is what caused
				// the 8500-segment backlog observed in production: backfill
				// always queries the oldest pending first, these singletons
				// always fail the >=2 check, and the queue never drains. Mark
				// them merged so the next cycle progresses past them.
				if r.shouldPurgeSingleton(valid) {
					n := r.markSingletonsMerged(ctx, cameraID, valid)
					merged += n
				}
				continue
			}

			release, ok := r.acquireMergeLock(cameraID)
			if !ok {
				select {
				case <-time.After(batchPause):
				case <-ctx.Done():
				}
				continue
			}

			// Disk-space admission: abort this batch (leave segments pending) if
			// there isn't room for ~1.1× the source size. Prevents backfill from
			// filling the disk on RPi 3B.
			var estSize int64
			for _, rec := range valid {
				estSize += rec.FileSize
			}
			if !checkDiskSpaceForMerge(r.store, estSize) {
				release()
				rollingLogger.Warn("backfill MP4: disk full, deferring remaining segments",
					"camera_id", cameraID, "processed", merged, "remaining", len(recs)-merged)
				return merged, nil
			}

			n, err := r.mergeBatchMP4(ctx, cameraID, valid)
			release()
			if err != nil {
				rollingLogger.Warn("backfill MP4: batch merge failed",
					"camera_id", cameraID, "error", err)
			}
			// Backfill batches also bypass the in-memory bucket state (see
			// mergeSegments) — drop any stale bucket to avoid double coverage.
			r.buckets.Delete(cameraID)
			merged += n

			rollingLogger.Info("backfill MP4 progress",
				"camera_id", cameraID, "merged", merged, "total", len(recs),
				"percent", merged*100/len(recs))
			select {
			case <-time.After(batchPause):
			case <-ctx.Done():
			}
		}
	}
	return merged, nil //nolint:nilerr // TODO(#storage-overhaul): intentional — report partial merge count on ctx cancellation rather than failing the whole backfill.
}

// mergeBatchMP4 merges a batch of MP4 segments into output file(s).
// Uses ParseSegment + MergeMP4Segments (the same as the periodic MergeManager).
// The batch is first split into consecutive merge-compatible runs (codec +
// SPS/PPS/VPS + audio): a batch that straddles a parameter-set or audio toggle
// boundary must not merge across it — a codec/SPS change hard-fails the merge
// and an audio change trips the mixed-audio policy that silently drops the
// audio from the whole output.
// Returns the number of segments successfully merged.

// mergeBatchMP4 merges a batch of MP4 segments into output file(s).
// Uses ParseSegment + MergeMP4Segments (the same as the periodic MergeManager).
// The batch is first split into consecutive merge-compatible runs (codec +
// SPS/PPS/VPS + audio): a batch that straddles a parameter-set or audio toggle
// boundary must not merge across it — a codec/SPS change hard-fails the merge
// and an audio change trips the mixed-audio policy that silently drops the
// audio from the whole output.
// Returns the number of segments successfully merged.
func (r *RollingMergeCoordinator) mergeBatchMP4(ctx context.Context, cameraID string, recs []*model.Recording) (int, error) {
	// Parse all segments.
	infos := make([]*SegmentInfo, 0, len(recs))
	parsedRecs := make([]*model.Recording, 0, len(recs))
	for _, rec := range recs {
		info, err := ParseSegment(rec.FilePath)
		if err != nil {
			rollingLogger.Warn("backfill MP4: parse failed, skipping",
				"camera_id", cameraID, "recording_id", rec.ID, "path", rec.FilePath, "error", err)
			continue
		}
		infos = append(infos, info)
		parsedRecs = append(parsedRecs, rec)
	}

	if len(infos) < 2 {
		// Not enough parseable segments — mark only the valid ones as merged.
		for _, rec := range parsedRecs {
			if err := storage.RetryOnBusy(ctx, func() error {
				return r.db.SetMergeStatus(ctx, []string{rec.ID}, model.MergeStatusMerged)
			}); err != nil {
				rollingLogger.Warn("backfill MP4: failed to mark singleton",
					"camera_id", cameraID, "recording_id", rec.ID, "error", err)
			}
		}
		return len(parsedRecs), nil
	}

	merged := 0
	for _, run := range splitRunsByCompatKey(parsedRecs, infos) {
		if len(run.infos) < 2 {
			// Lone segment in its audio run — no merge partner within the run.
			// Mark it merged; the file stays as a standalone recording.
			if err := storage.RetryOnBusy(ctx, func() error {
				return r.db.SetMergeStatus(ctx, []string{run.recs[0].ID}, model.MergeStatusMerged)
			}); err != nil {
				rollingLogger.Warn("backfill MP4: failed to mark singleton run",
					"camera_id", cameraID, "recording_id", run.recs[0].ID, "error", err)
			}
			merged++
			continue
		}
		n, err := r.mergeAudioRun(ctx, cameraID, run.recs, run.infos)
		if err != nil {
			rollingLogger.Warn("backfill MP4: run merge failed",
				"camera_id", cameraID, "segments", len(run.recs), "error", err)
			continue
		}
		merged += n
	}
	return merged, nil
}

// segmentRun is a consecutive, merge-compatible slice of a parsed batch.

// segmentRun is a consecutive, merge-compatible slice of a parsed batch.
type segmentRun struct {
	recs   []*model.Recording
	infos  []*SegmentInfo
	keyStr string
}

// segmentCompatKey is the merge-compatibility key for a parsed segment:
// codec + parameter sets + audio state. Segments with different keys cannot
// be merged into one MP4 — a codec/SPS change mid-run made the old
// audio-only split hard-fail the WHOLE run inside MergeMP4Segments and
// permanently mark it incompatible (#488 follow-up).

// segmentCompatKey is the merge-compatibility key for a parsed segment:
// codec + parameter sets + audio state. Segments with different keys cannot
// be merged into one MP4 — a codec/SPS change mid-run made the old
// audio-only split hard-fail the WHOLE run inside MergeMP4Segments and
// permanently mark it incompatible (#488 follow-up).
func segmentCompatKey(info *SegmentInfo) string {
	h := sha256.New()
	h.Write([]byte(info.Codec))
	h.Write(info.SPS)
	h.Write(info.PPS)
	h.Write(info.VPS)
	return hex.EncodeToString(h.Sum(nil)) + "|" + segmentAudioKey(info)
}

// splitRunsByCompatKey splits a parsed batch into consecutive runs sharing the
// same segmentCompatKey (codec + SPS/PPS/VPS + audio). recs and infos are
// parallel slices (same length).

// splitRunsByCompatKey splits a parsed batch into consecutive runs sharing the
// same segmentCompatKey (codec + SPS/PPS/VPS + audio). recs and infos are
// parallel slices (same length).
func splitRunsByCompatKey(recs []*model.Recording, infos []*SegmentInfo) []segmentRun {
	var runs []segmentRun
	for i, info := range infos {
		key := segmentCompatKey(info)
		if len(runs) > 0 && runs[len(runs)-1].keyStr == key {
			last := &runs[len(runs)-1]
			last.recs = append(last.recs, recs[i])
			last.infos = append(last.infos, info)
			continue
		}
		runs = append(runs, segmentRun{
			recs:   []*model.Recording{recs[i]},
			infos:  []*SegmentInfo{info},
			keyStr: key,
		})
	}
	return runs
}

// mergeAudioRun merges one audio-homogeneous run into a single output file and
// updates the DB atomically. Returns the number of source segments merged.

// mergeAudioRun merges one audio-homogeneous run into a single output file and
// updates the DB atomically. Returns the number of source segments merged.
func (r *RollingMergeCoordinator) mergeAudioRun(ctx context.Context, cameraID string, recs []*model.Recording, infos []*SegmentInfo) (int, error) {
	sourcePaths := make([]string, 0, len(recs))
	for _, rec := range recs {
		sourcePaths = append(sourcePaths, rec.FilePath)
	}

	// Create output file.
	tempPath, finalPath, err := r.store.CreateSegment(cameraID, string(recs[0].Format))
	if err != nil {
		return 0, fmt.Errorf("create output: %w", err)
	}

	// Merge.
	stats, err := MergeMP4Segments(ctx, infos, tempPath, r.resolveTimelapseCadence(cameraID))
	if err != nil {
		os.Remove(tempPath)
		// Mark these as incompatible so we don't retry forever.
		ids := make([]string, len(recs))
		for i, rec := range recs {
			ids[i] = rec.ID
		}
		_ = storage.RetryOnBusy(ctx, func() error {
			return r.db.SetMergeStatus(ctx, ids, model.MergeStatusIncompatible)
		})
		return 0, fmt.Errorf("merge: %w", err)
	}
	// Keyframe-less segments are not in the output: mark them incompatible and
	// exclude them from the DB replacement + source deletion below (#488).
	if len(stats.SkippedNoKeyframe) > 0 {
		skipIDs := make([]string, 0, len(stats.SkippedNoKeyframe))
		for _, idx := range stats.SkippedNoKeyframe {
			skipIDs = append(skipIDs, recs[idx].ID)
		}
		_ = storage.RetryOnBusy(ctx, func() error {
			return r.db.SetMergeStatus(ctx, skipIDs, model.MergeStatusIncompatible)
		})
		rollingLogger.Warn("audio run merge skipped keyframe-less segments",
			"camera_id", cameraID, "skipped", len(skipIDs))
	}
	if len(stats.Included) != len(recs) {
		keepRecs := make([]*model.Recording, 0, len(stats.Included))
		keepInfos := make([]*SegmentInfo, 0, len(stats.Included))
		keepPaths := make([]string, 0, len(stats.Included))
		for _, idx := range stats.Included {
			keepRecs = append(keepRecs, recs[idx])
			keepInfos = append(keepInfos, infos[idx])
			keepPaths = append(keepPaths, recs[idx].FilePath)
		}
		recs, infos, sourcePaths = keepRecs, keepInfos, keepPaths
	}
	if len(recs) == 0 {
		os.Remove(tempPath)
		return 0, ErrNoKeyframe
	}

	fi, err := os.Stat(tempPath)
	if err != nil || fi.Size() == 0 {
		os.Remove(tempPath)
		return 0, fmt.Errorf("output empty")
	}

	if err := r.store.CloseSegment(tempPath, finalPath); err != nil {
		os.Remove(tempPath)
		return 0, fmt.Errorf("finalize: %w", err)
	}

	// Compute merged metadata.
	var totalDur time.Duration
	totalFrames := 0
	for _, info := range infos {
		totalDur += info.TotalDuration
		totalFrames += info.SampleCount
	}
	durSec := math.Round(totalDur.Seconds()*1000) / 1000

	mergedRec := &model.Recording{
		ID:           strconv.FormatInt(time.Now().UnixNano(), 10),
		CameraID:     cameraID,
		FilePath:     finalPath,
		Format:       recs[0].Format,
		StartedAt:    recs[0].StartedAt,
		EndedAt:      recs[len(recs)-1].EndedAt,
		Duration:     durSec,
		FileSize:     fi.Size(),
		FrameCount:   totalFrames,
		MergeStatus:  model.MergeStatusMerged,
		MergeQuality: ComputeMergeQuality(recs[0].StartedAt, recs[len(recs)-1].EndedAt, durSec, r.resolveRollingConfig(cameraID).MinDuration.Seconds()),
		TimelineMap:  stats.TimelineMapJSON(),
	}

	ids := make([]string, len(recs))
	for i, rec := range recs {
		ids[i] = rec.ID
	}
	if err := storage.RetryOnBusy(ctx, func() error {
		return r.db.MergeAndReplaceRecordings(ctx, mergedRec, ids)
	}); err != nil {
		os.Remove(finalPath)
		return 0, fmt.Errorf("db replace: %w", err)
	}

	// Delete source files.
	for _, path := range sourcePaths {
		r.store.DeleteFile(path)
	}

	return len(recs), nil
}

// backfillBatchFormat handles AVI and MJPEG formats via window-bucketed batch merge.
// Unlike MP4 (per-segment rolling append), AVI/MJPEG use the existing batch merge
// functions (MergeAVISegments / MergeMJPEGSegments) which merge all segments in a
// window at once. This is simpler and avoids format-specific append complexities.

// backfillBatchFormat handles AVI and MJPEG formats via window-bucketed batch merge.
// Unlike MP4 (per-segment rolling append), AVI/MJPEG use the existing batch merge
// functions (MergeAVISegments / MergeMJPEGSegments) which merge all segments in a
// window at once. This is simpler and avoids format-specific append complexities.
func (r *RollingMergeCoordinator) backfillBatchFormat(ctx context.Context, cameraID string, recs []*model.Recording, format string) (int, error) {
	const batchBatchSize = 20 // small batches to yield lock to real-time events
	batchPause := adaptiveBatchPause(len(recs), r.diskFreePercent())

	merged := 0
	for startIdx := 0; startIdx < len(recs); startIdx += batchBatchSize {
		if ctx.Err() != nil {
			break
		}
		endIdx := startIdx + batchBatchSize
		if endIdx > len(recs) {
			endIdx = len(recs)
		}
		batch := recs[startIdx:endIdx]

		// Filter out missing files.
		var valid []*model.Recording
		for _, rec := range batch {
			if _, err := os.Stat(rec.FilePath); os.IsNotExist(err) {
				continue
			}
			valid = append(valid, rec)
		}
		if len(valid) < 2 {
			// Not enough segments to merge in this batch. Leave them pending —
			// do NOT mark as merged. Marking singletons merged (the old behavior)
			// permanently ejected them from the merge queue: backfill only selects
			// pending segments, so a segment marked merged here would never be
			// retried even when adjacent segments arrive later. This produced
			// thousands of "fake merged" 30s fragments that cluttered the timeline
			// (merge_status=merged but merge_path empty — never actually merged).
			// Keeping them pending means the next backfill/periodic merge will
			// reconsider them alongside any new neighbors.
			rollingLogger.Debug("backfill: batch has <2 valid segments, leaving pending",
				"camera_id", cameraID, "format", format,
				"valid", len(valid), "missing", len(batch)-len(valid))
			continue
		}

		release, ok := r.acquireMergeLock(cameraID)
		if !ok {
			select {
			case <-time.After(batchPause):
			case <-ctx.Done():
			}
			continue
		}

		// Disk-space admission (same rationale as backfillMP4).
		var estSize int64
		for _, rec := range valid {
			estSize += rec.FileSize
		}
		if !checkDiskSpaceForMerge(r.store, estSize) {
			release()
			rollingLogger.Warn("backfill batch: disk full, deferring remaining segments",
				"camera_id", cameraID, "format", format, "processed", merged, "remaining", len(recs)-merged)
			return merged, nil
		}

		n, err := r.mergeBatchSegments(ctx, cameraID, valid, format)
		release()
		if err != nil {
			rollingLogger.Warn("backfill: batch merge failed",
				"camera_id", cameraID, "format", format, "error", err)
		}
		merged += n

		rollingLogger.Info("backfill batch progress",
			"camera_id", cameraID, "format", format,
			"merged", merged, "total", len(recs),
			"percent", merged*100/len(recs))
		select {
		case <-time.After(batchPause):
		case <-ctx.Done():
		}
	}
	return merged, nil //nolint:nilerr // TODO(#storage-overhaul): intentional — report partial merge count on ctx cancellation rather than failing the whole backfill.
}

// mergeBatchSegments delegates to the format-specific batch merge function and
// updates the DB atomically. Returns the number of source segments merged.

// mergeBatchSegments delegates to the format-specific batch merge function and
// updates the DB atomically. Returns the number of source segments merged.
func (r *RollingMergeCoordinator) mergeBatchSegments(ctx context.Context, cameraID string, recs []*model.Recording, format string) (int, error) {
	var mergedRec *model.Recording
	var sourcePaths []string
	var err error

	switch model.Format(format) {
	case model.FormatAVI:
		mergedRec, sourcePaths, err = MergeAVISegments(ctx, recs, r.store, cameraID)
	case model.FormatMJPEG:
		mergedRec, sourcePaths, err = MergeMJPEGSegments(ctx, recs, r.store, cameraID)
	default:
		return 0, fmt.Errorf("unsupported batch format: %s", format)
	}
	if err != nil {
		return 0, fmt.Errorf("batch merge %s: %w", format, err)
	}

	mergedRec.MergeStatus = model.MergeStatusMerged

	// Atomic DB replace: insert merged + delete sources.
	ids := make([]string, len(recs))
	for i, rec := range recs {
		ids[i] = rec.ID
	}
	if err := storage.RetryOnBusy(ctx, func() error {
		return r.db.MergeAndReplaceRecordings(ctx, mergedRec, ids)
	}); err != nil {
		// DB failed — clean up merged file, keep sources.
		if format == string(model.FormatMJPEG) {
			os.RemoveAll(mergedRec.FilePath)
		} else {
			os.Remove(mergedRec.FilePath)
		}
		return 0, fmt.Errorf("db replace %s: %w", format, err)
	}

	// Delete source files/dirs AFTER successful DB commit.
	for _, path := range sourcePaths {
		if format == string(model.FormatMJPEG) {
			os.RemoveAll(path)
		} else {
			r.store.DeleteFile(path)
		}
	}

	rollingLogger.Info("batch merge complete",
		"camera_id", cameraID, "format", format,
		"segments", len(recs), "duration_s", mergedRec.Duration,
		"size_bytes", mergedRec.FileSize)

	return len(recs), nil
}

// Stop unsubscribes, signals all goroutines to exit via ctx cancellation, and
// WAITS for them to fully return before returning itself. This honors the
// App.Service contract ("must release all goroutines") and prevents the
// TempDir cleanup race (#143/#125 class) where outlived goroutines write files
// after the caller (e.g. a test's t.TempDir) begins cleanup.
