package merge

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
)

// createBucket creates the initial merged file for a new window bucket.
// It merges the single segment into a new output file and creates a DB row.
// The source segment's DB row is then deleted (replaced by the merged row).
//
// bucket receives the initial wall/file axis state (see bucketInfo) — the
// caller holds bucket.mu.
func (r *RollingMergeCoordinator) createBucket(
	ctx context.Context,
	seg pendingSegmentInfo,
	info *SegmentInfo,
	bucket *bucketInfo,
) (outputPath, mergedRecID string, err error) {
	// Create output file via store.
	tempPath, finalPath, derr := r.store.CreateSegment(seg.cameraID, seg.format)
	if derr != nil {
		return "", "", fmt.Errorf("create bucket output: %w", derr)
	}

	// Merge single segment into the bucket file (pre-aligned by mergeOneSegment).
	stats, err := MergeMP4Segments(ctx, []*SegmentInfo{info}, tempPath, r.resolveTimelapseCadence(seg.cameraID), r.resolveTimelapseGap(seg.cameraID))
	if err != nil {
		os.Remove(tempPath)
		return "", "", fmt.Errorf("merge initial segment: %w", err)
	}

	// Verify output.
	fi, err := os.Stat(tempPath)
	if err != nil || fi.Size() == 0 {
		os.Remove(tempPath)
		return "", "", fmt.Errorf("bucket output is empty or missing")
	}

	// Atomic rename.
	if err := r.store.CloseSegment(tempPath, finalPath); err != nil {
		os.Remove(tempPath)
		return "", "", fmt.Errorf("finalize bucket: %w", err)
	}

	// Create merged recording row and delete the source segment row.
	mergedRecID = strconv.FormatInt(time.Now().UnixNano(), 10)
	// Wall axis is row-level truth (#496 append-fix): started_at..ended_at,
	// never the parsed file duration — a timelapse-compressed bucket must keep
	// reporting real time so the UI's wall-clock math stays correct.
	wallSec := seg.endedAt.Sub(seg.startedAt).Seconds()
	fileSec := stats.FileDurationSec()
	if fileSec <= 0 {
		fileSec = info.TotalDuration.Seconds()
	}
	bucket.wallDurSec = wallSec
	bucket.fileDurSec = fileSec
	bucket.lastEnded = seg.endedAt
	bucket.wallFile = stats.WallToFile
	if len(bucket.wallFile) == 0 {
		bucket.wallFile = [][2]float64{{0, 0}, {wallSec, fileSec}}
	}
	mergedRec := &model.Recording{
		ID:        mergedRecID,
		CameraID:  seg.cameraID,
		FilePath:  finalPath,
		Format:    model.Format(seg.format),
		StartedAt: seg.startedAt,
		EndedAt:   seg.endedAt,
		// Wall-clock span (#496): the file timeline may be timelapse-compressed;
		// the row keeps reporting real time so storage accounting and the UI's
		// wall-clock math stay on the real axis.
		Duration:    wallSec,
		FileSize:    fi.Size(),
		FrameCount:  info.SampleCount,
		MergeStatus: model.MergeStatusMerged,
		TimelineMap: mapJSON(bucket.wallFile),
	}

	if err := storage.RetryOnBusy(ctx, func() error {
		return r.db.RollingReplaceRecordings(ctx, mergedRec, "", []string{seg.recordingID})
	}); err != nil {
		os.Remove(finalPath)
		return "", "", fmt.Errorf("db replace (create): %w", err)
	}

	// Delete the source segment file (DB already committed).
	r.store.DeleteFile(seg.filePath)
	os.Remove(seg.filePath + ".g711") // ambient archive sidecar (#496)

	return finalPath, mergedRecID, nil
}

// appendToBucket merges a new segment into the existing bucket file.
// It produces a new output file (temp→rename) and UPDATEs the merged DB row,
// then deletes the source segment's row + file.

// appendToBucket merges a new segment into the existing bucket file.
// It produces a new output file (temp→rename) and UPDATEs the merged DB row,
// then deletes the source segment's row + file.
func (r *RollingMergeCoordinator) appendToBucket(
	ctx context.Context,
	seg pendingSegmentInfo,
	newInfo *SegmentInfo,
	bucket *bucketInfo,
) (outputPath, mergedRecID string, err error) {
	// Parse the existing bucket file.
	bucketInfo, err := ParseSegment(bucket.mergedFilePath)
	if err != nil {
		return "", "", fmt.Errorf("parse existing bucket: %w", err)
	}

	// Validate codec compatibility (defensive — should have been caught earlier).
	if bucketInfo.Codec != newInfo.Codec ||
		!bytesEqual(bucketInfo.SPS, newInfo.SPS) ||
		!bytesEqual(bucketInfo.PPS, newInfo.PPS) {
		return "", "", fmt.Errorf("bucket/segment codec mismatch during append")
	}
	// Same for audio: a mismatch here would silently drop the audio track for
	// the whole bucket (and every future append — see segmentAudioKey).
	if segmentAudioKey(bucketInfo) != segmentAudioKey(newInfo) {
		return "", "", fmt.Errorf("bucket/segment audio mismatch during append (%s vs %s)",
			segmentAudioKey(bucketInfo), segmentAudioKey(newInfo))
	}

	// Create new output file.
	tempPath, finalPath, derr := r.store.CreateSegment(seg.cameraID, seg.format)
	if derr != nil {
		return "", "", fmt.Errorf("create append output: %w", derr)
	}

	// Merge [bucket + newSegment]. The new segment was pre-aligned by
	// mergeOneSegment; the bucket head is re-aligned inside the merge
	// (self-heals pre-fix buckets whose first sample was a P-frame). A bucket
	// with NO keyframe-bearing sample at all (legacy corrupt data) aborts the
	// append with a distinct error so mergeOneSegment can rebuild the bucket.
	stats, mergeErr := MergeMP4Segments(ctx, []*SegmentInfo{bucketInfo, newInfo}, tempPath, r.resolveTimelapseCadence(seg.cameraID), r.resolveTimelapseGap(seg.cameraID))
	if mergeErr != nil {
		os.Remove(tempPath)
		return "", "", fmt.Errorf("merge append: %w", mergeErr)
	}
	if len(stats.Included) != 2 {
		os.Remove(tempPath)
		return "", "", fmt.Errorf("append dropped a segment (bucket keyframe-less): included=%d/2", len(stats.Included))
	}

	fi, err := os.Stat(tempPath)
	if err != nil || fi.Size() == 0 {
		os.Remove(tempPath)
		return "", "", fmt.Errorf("append output is empty or missing")
	}

	// Atomic rename — overwrites the old bucket file.
	// CloseSegment does temp→rename; but we need to replace an existing final path.
	// Use direct rename to overwrite (the old bucket file will be replaced).
	if err := os.Rename(tempPath, finalPath); err != nil {
		// Fallback: if rename fails (cross-device?), use the store's CloseSegment
		// which may handle it differently. For safety, clean up temp.
		os.Remove(tempPath)
		return "", "", fmt.Errorf("finalize append (rename): %w", err)
	}

	// Calculate updated metadata (#496 append-fix): the bucket file's parsed
	// durations are ALREADY compressed — per-merge stats would read them as
	// wall and silently drop every previously compressed TL dwell. Instead,
	// this append's wall/file contribution is the delta across the NEW input's
	// boundary in the merge stats (entries [1]→[2] for [bucket, new]), taken
	// while the new segment's durations are still original; both axes then
	// accumulate on the in-memory bucket state and the row keeps the wall
	// duration plus the monotonically grown wall→file map.
	deltaWall, deltaFile := appendDeltas(stats, newInfo)
	// 墙钟推进以行为准:lastEnded→ended 覆盖段间间隙(断连/TL 停顿在时间轴上
	// 保持可见,与行 started_at..ended_at 跨度一致),并钳到不低于本段自身
	// 跨度(防时钟回拨/重叠段倒缩)。
	if !bucket.lastEnded.IsZero() {
		if gapWall := seg.endedAt.Sub(bucket.lastEnded).Seconds(); gapWall > deltaWall {
			deltaWall = gapWall
		}
	}
	if segWall := seg.endedAt.Sub(seg.startedAt).Seconds(); deltaWall < segWall {
		deltaWall = segWall
	}
	bucket.lastEnded = seg.endedAt
	bucket.wallDurSec += deltaWall
	bucket.fileDurSec += deltaFile
	if len(bucket.wallFile) == 0 {
		bucket.wallFile = [][2]float64{{0, 0}}
	}
	bucket.wallFile = append(bucket.wallFile, [2]float64{bucket.wallDurSec, bucket.fileDurSec})

	totalFrames := bucketInfo.SampleCount + newInfo.SampleCount

	mergedRecID = bucket.mergedRecID
	mergedRec := &model.Recording{
		ID:          mergedRecID,
		CameraID:    seg.cameraID,
		FilePath:    finalPath,
		Format:      model.Format(seg.format),
		StartedAt:   bucket.windowStart,
		EndedAt:     seg.endedAt,
		Duration:    bucket.wallDurSec,
		FileSize:    fi.Size(),
		FrameCount:  totalFrames,
		MergeStatus: model.MergeStatusMerged,
		TimelineMap: mapJSON(bucket.wallFile),
	}

	// UPDATE the merged row + DELETE the source segment row, in one transaction.
	if err := storage.RetryOnBusy(ctx, func() error {
		return r.db.RollingReplaceRecordings(ctx, mergedRec, bucket.mergedRecID, []string{seg.recordingID})
	}); err != nil {
		// DB failed — the file is already renamed. This is a data inconsistency risk.
		// The merged file is valid but the DB row may be stale. Log prominently.
		rollingLogger.Error("db update failed after file merge — data may be inconsistent",
			"camera_id", seg.cameraID,
			"merged_path", finalPath,
			"error", err)
		return "", "", fmt.Errorf("db replace (append): %w", err)
	}

	// Delete the source segment file.
	r.store.DeleteFile(seg.filePath)
	os.Remove(seg.filePath + ".g711") // ambient archive sidecar (#496)

	// Delete the PREVIOUS bucket file (each append creates a new file via
	// store.CreateSegment, so the old bucket path is now orphaned). Only
	// delete if it differs from the new finalPath (it always should, since
	// CreateSegment generates unique timestamps).
	if bucket.mergedFilePath != "" && bucket.mergedFilePath != finalPath {
		r.store.DeleteFile(bucket.mergedFilePath)
		os.Remove(bucket.mergedFilePath + ".g711")
	}

	return finalPath, mergedRecID, nil
}

// computeWindow returns the [start, end) time window for a timestamp.
// Window boundaries are aligned to epoch start at windowDur intervals.
// For windowDur=1h, this produces natural-hour boundaries (00:00, 01:00, ...).

// appendDeltas extracts THIS append's wall/file contribution from the merge
// stats: for inputs [bucket, new] the boundary entries are [1] (after the
// bucket) and [2] (after the new input). The delta across [1]→[2] is measured
// while the new segment's sample durations are still original — the only
// moment its true (possibly dwell-heavy) wall span exists, because the bucket
// side is already compressed and the new side becomes compressed in the
// output. Falls back to the new input's parsed duration when the stats map is
// missing (degenerate parse).
func appendDeltas(stats MergeStats, newInfo *SegmentInfo) (wall, file float64) {
	if n := len(stats.WallToFile); n >= 3 {
		wall = stats.WallToFile[n-1][0] - stats.WallToFile[n-2][0]
		file = stats.WallToFile[n-1][1] - stats.WallToFile[n-2][1]
		if wall > 0 {
			return wall, file
		}
	}
	d := newInfo.TotalDuration.Seconds()
	if d <= 0 {
		d = 0
	}
	return d, d
}

// mapJSON renders the accumulated wall→file map for the row's timeline_map
// column (same compact format as MergeStats.TimelineMapJSON).
func mapJSON(pairs [][2]float64) string {
	if len(pairs) == 0 {
		return ""
	}
	b, err := json.Marshal(pairs)
	if err != nil {
		return ""
	}
	return string(b)
}
