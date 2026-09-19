package merge

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
)

// createBucket creates the initial merged file for a new window bucket from a
// run of one or more pre-classified segments (single segment = the classic
// path; 2+ = a fragment batch flushed by the hold queue, #852). The source
// segments' DB rows are replaced by the merged row, and their files deleted.
//
// bucket receives the initial wall/file axis state (see bucketInfo) — the
// caller holds bucket.mu. segs/infos are parallel, sorted by startedAt, and
// share spsKey/audioKey/window (the run-grouping invariant).
func (r *RollingMergeCoordinator) createBucket(
	ctx context.Context,
	segs []pendingSegmentInfo,
	infos []*SegmentInfo,
	bucket *bucketInfo,
) (outputPath, mergedRecID string, err error) {
	first, last := segs[0], segs[len(segs)-1]

	// Create output file via store.
	tempPath, finalPath, derr := r.store.CreateSegment(first.cameraID, first.format)
	if derr != nil {
		return "", "", fmt.Errorf("create bucket output: %w", derr)
	}

	// Merge the run into the bucket file (pre-aligned by the run classifier).
	stats, err := MergeMP4Segments(ctx, infos, tempPath, r.resolveTimelapseCadence(first.cameraID), r.resolveTimelapseGap(first.cameraID))
	if err != nil {
		os.Remove(tempPath)
		return "", "", fmt.Errorf("merge initial segment: %w", err)
	}
	if len(stats.Included) != len(infos) {
		os.Remove(tempPath)
		return "", "", fmt.Errorf("initial merge dropped a segment: included=%d/%d", len(stats.Included), len(infos))
	}

	// Verify output.
	fi, err := os.Stat(tempPath)
	if err != nil || fi.Size() == 0 {
		os.Remove(tempPath)
		return "", "", fmt.Errorf("bucket output is empty or missing")
	}

	// Atomic rename.
	if err := r.store.CloseSegmentMerged(tempPath, finalPath); err != nil {
		os.Remove(tempPath)
		return "", "", fmt.Errorf("finalize bucket: %w", err)
	}

	// Create merged recording row and delete the source segment rows.
	mergedRecID = strconv.FormatInt(time.Now().UnixNano(), 10)
	// Wall axis is row-level truth (#496 append-fix): started_at..ended_at,
	// never the parsed file duration — a timelapse-compressed bucket must keep
	// reporting real time so the UI's wall-clock math stays correct.
	wallSec := last.endedAt.Sub(first.startedAt).Seconds()
	fileSec := stats.FileDurationSec()
	if fileSec <= 0 {
		for _, info := range infos {
			fileSec += info.TotalDuration.Seconds()
		}
	}
	bucket.wallDurSec = wallSec
	bucket.fileDurSec = fileSec
	bucket.lastEnded = last.endedAt
	// The row's started_at anchors at the run's first segment; later appends
	// may move it backward (out-of-order arrival, #698) but never forward.
	bucket.rowStart = first.startedAt
	bucket.wallFile = stats.WallToFile
	if len(bucket.wallFile) == 0 {
		bucket.wallFile = [][2]float64{{0, 0}, {wallSec, fileSec}}
	}
	totalFrames := 0
	for _, info := range infos {
		totalFrames += info.SampleCount
	}
	mergedRec := &model.Recording{
		ID:        mergedRecID,
		CameraID:  first.cameraID,
		FilePath:  finalPath,
		Format:    model.Format(first.format),
		StartedAt: first.startedAt,
		EndedAt:   last.endedAt,
		// Wall-clock span (#496): the file timeline may be timelapse-compressed;
		// the row keeps reporting real time so storage accounting and the UI's
		// wall-clock math stay on the real axis.
		Duration:    wallSec,
		FileSize:    fi.Size(),
		FrameCount:  totalFrames,
		MergeStatus: model.MergeStatusMerged,
		TimelineMap: mapJSON(bucket.wallFile),
	}

	recIDs := make([]string, len(segs))
	for i, seg := range segs {
		recIDs[i] = seg.recordingID
	}
	if err := storage.RetryOnBusy(ctx, func() error {
		return r.db.RollingReplaceRecordings(ctx, mergedRec, "", recIDs)
	}); err != nil {
		os.Remove(finalPath)
		return "", "", fmt.Errorf("db replace (create): %w", err)
	}

	// Deletion-moment re-check (#817 follow-up): the entry re-check ran
	// seconds ago and a transcode task can land while the merge itself runs
	// (production: task pending 3.8s before this delete). The bucket output
	// is already committed — keep the source file for the task; the orphan
	// sweep reclaims it after the task finishes.
	r.deleteRunSources(ctx, segs, "bucket-create")
	return finalPath, mergedRecID, nil
}

// appendToBucket merges a run of one or more segments into the existing bucket
// file in ONE MergeMP4Segments pass (#852 true batch fold: the fragment storm
// previously rewrote the whole bucket per fragment). It produces a new output
// file (temp→rename), UPDATEs the merged DB row, then deletes the source
// segments' rows + files and the previous bucket file.
//
// segs/infos are parallel, sorted by startedAt, share spsKey/audioKey/window
// and were pre-aligned by the run classifier. The caller holds bucket.mu.
func (r *RollingMergeCoordinator) appendToBucket(
	ctx context.Context,
	segs []pendingSegmentInfo,
	infos []*SegmentInfo,
	bucket *bucketInfo,
) (outputPath, mergedRecID string, err error) {
	first, last := segs[0], segs[len(segs)-1]

	// Parse the existing bucket file.
	bucketParsed, err := ParseSegment(bucket.mergedFilePath)
	if err != nil {
		return "", "", fmt.Errorf("parse existing bucket: %w", err)
	}

	// Validate codec compatibility (defensive — should have been caught earlier).
	for i, newInfo := range infos {
		if bucketParsed.Codec != newInfo.Codec ||
			!bytesEqual(bucketParsed.SPS, newInfo.SPS) ||
			!bytesEqual(bucketParsed.PPS, newInfo.PPS) {
			return "", "", fmt.Errorf("bucket/segment codec mismatch during append (input %d)", i)
		}
		// Same for audio: a mismatch here would silently drop the audio track
		// for the whole bucket (and every future append — see segmentAudioKey).
		if segmentAudioKey(bucketParsed) != segmentAudioKey(newInfo) {
			return "", "", fmt.Errorf("bucket/segment audio mismatch during append (input %d: %s vs %s)",
				i, segmentAudioKey(bucketParsed), segmentAudioKey(newInfo))
		}
	}

	// Create new output file.
	tempPath, finalPath, derr := r.store.CreateSegment(first.cameraID, first.format)
	if derr != nil {
		return "", "", fmt.Errorf("create append output: %w", derr)
	}

	// Merge [bucket + run]. The run was pre-aligned by the classifier; the
	// bucket head is re-aligned inside the merge (self-heals pre-fix buckets
	// whose first sample was a P-frame). A bucket with NO keyframe-bearing
	// sample at all (legacy corrupt data) aborts the append with a distinct
	// error so the caller can rebuild the bucket.
	inputs := make([]*SegmentInfo, 0, len(infos)+1)
	inputs = append(inputs, bucketParsed)
	inputs = append(inputs, infos...)
	stats, mergeErr := MergeMP4Segments(ctx, inputs, tempPath, r.resolveTimelapseCadence(first.cameraID), r.resolveTimelapseGap(first.cameraID))
	if mergeErr != nil {
		os.Remove(tempPath)
		return "", "", fmt.Errorf("merge append: %w", mergeErr)
	}
	if len(stats.Included) != len(inputs) {
		os.Remove(tempPath)
		return "", "", fmt.Errorf("append dropped a segment (bucket keyframe-less): included=%d/%d", len(stats.Included), len(inputs))
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
	// this append's wall/file contribution is the delta across the RUN's
	// boundary in the merge stats (entries [1]→[1+K] for [bucket, run...]),
	// taken while the run's durations are still original; both axes then
	// accumulate on the in-memory bucket state and the row keeps the wall
	// duration plus the monotonically grown wall→file map.
	deltaWall, deltaFile := appendDeltas(stats, infos)
	// 墙钟推进以行为准:lastEnded→run 末段 ended 覆盖段间间隙(断连/TL 停顿在
	// 时间轴上保持可见,与行 started_at..ended_at 跨度一致),并钳到不低于整段
	// run 自身跨度(批内碎段间的断连间隙 + 防时钟回拨/重叠段倒缩)。
	if !bucket.lastEnded.IsZero() {
		if gapWall := last.endedAt.Sub(bucket.lastEnded).Seconds(); gapWall > deltaWall {
			deltaWall = gapWall
		}
	}
	if spanWall := last.endedAt.Sub(first.startedAt).Seconds(); deltaWall < spanWall {
		deltaWall = spanWall
	}
	// Out-of-order guard (#698): lastEnded only ever advances, and an earlier
	// segment extends the row's start backward. Before this, a swapped merge
	// order (later segment first) wrote ended_at < started_at — an inverted
	// row that broke every timeline invariant downstream.
	if last.endedAt.After(bucket.lastEnded) {
		bucket.lastEnded = last.endedAt
	}
	if !bucket.rowStart.IsZero() && first.startedAt.Before(bucket.rowStart) {
		bucket.rowStart = first.startedAt
	}
	bucket.wallDurSec += deltaWall
	bucket.fileDurSec += deltaFile
	// I1 (duration == wall span): when the backward extension outpaces the
	// accumulated deltas (an earlier segment arrives late and its gap was
	// never counted), clamp the wall axis up to the full row span. No-op in
	// the ordered flow, where the gap rule already keeps them equal.
	if !bucket.rowStart.IsZero() {
		if span := bucket.lastEnded.Sub(bucket.rowStart).Seconds(); span > bucket.wallDurSec {
			bucket.wallDurSec = span
		}
	}
	if len(bucket.wallFile) == 0 {
		bucket.wallFile = [][2]float64{{0, 0}}
	}
	bucket.wallFile = append(bucket.wallFile, [2]float64{bucket.wallDurSec, bucket.fileDurSec})

	totalFrames := bucketParsed.SampleCount
	for _, info := range infos {
		totalFrames += info.SampleCount
	}

	rowStart := bucket.rowStart
	if rowStart.IsZero() {
		// Legacy in-flight bucket (created before rowStart was tracked): the
		// row's true start is unknown here; pass the segment-relative value —
		// the DB update applies min(started_at, ?) so the stored start can
		// only move backward, never shrink coverage.
		rowStart = first.startedAt
	}
	mergedRecID = bucket.mergedRecID
	mergedRec := &model.Recording{
		ID:          mergedRecID,
		CameraID:    first.cameraID,
		FilePath:    finalPath,
		Format:      model.Format(first.format),
		StartedAt:   rowStart,
		EndedAt:     bucket.lastEnded,
		Duration:    bucket.wallDurSec,
		FileSize:    fi.Size(),
		FrameCount:  totalFrames,
		MergeStatus: model.MergeStatusMerged,
		TimelineMap: mapJSON(bucket.wallFile),
	}

	// UPDATE the merged row + DELETE the source segment rows, in one transaction.
	recIDs := make([]string, len(segs))
	for i, seg := range segs {
		recIDs[i] = seg.recordingID
	}
	if err := storage.RetryOnBusy(ctx, func() error {
		return r.db.RollingReplaceRecordings(ctx, mergedRec, bucket.mergedRecID, recIDs)
	}); err != nil {
		// DB failed — the file is already renamed. This is a data inconsistency risk.
		// The merged file is valid but the DB row may be stale. Log prominently.
		rollingLogger.Error("db update failed after file merge — data may be inconsistent",
			"camera_id", first.cameraID,
			"merged_path", finalPath,
			"error", err)
		return "", "", fmt.Errorf("db replace (append): %w", err)
	}

	// Deletion-moment re-check (#817 follow-up): same race as bucket-create —
	// a task landing mid-merge keeps its source file.
	r.deleteRunSources(ctx, segs, "bucket-append")

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

// deleteRunSources removes the run's source segment files after the merged
// output is committed, honoring the deletion-moment transcode re-check
// (#817 follow-up): a task that landed while the merge ran keeps its input
// file — the orphan sweep reclaims it after the task finishes.
func (r *RollingMergeCoordinator) deleteRunSources(ctx context.Context, segs []pendingSegmentInfo, site string) {
	held, ok := r.queryTranscodeHold(ctx, segs[0].cameraID)
	for _, seg := range segs {
		if !ok || held[seg.filePath] {
			rollingLogger.Info("fold re-check: transcode task landed during merge — keeping source file",
				"camera_id", seg.cameraID, "recording_id", seg.recordingID, "site", site)
			continue
		}
		rollingLogger.Info("fold deleted source", "site", site,
			"camera_id", seg.cameraID, "recording_id", seg.recordingID, "file", filepath.Base(seg.filePath))
		r.store.DeleteFile(seg.filePath)
		os.Remove(seg.filePath + ".g711") // ambient archive sidecar (#496)
	}
}

// computeWindow returns the [start, end) time window for a timestamp.
// Window boundaries are aligned to epoch start at windowDur intervals.
// For windowDur=1h, this produces natural-hour boundaries (00:00, 01:00, ...).

// appendDeltas extracts THIS append's wall/file contribution from the merge
// stats: for inputs [bucket, run...] the boundary entries are [1] (after the
// bucket) and [1+len(run)] (after the last run input). The delta across them
// is measured while the run's sample durations are still original — the only
// moment their true (possibly dwell-heavy) wall span exists, because the
// bucket side is already compressed and the run side becomes compressed in
// the output. Falls back to the run's parsed durations when the stats map is
// missing (degenerate parse).
func appendDeltas(stats MergeStats, infos []*SegmentInfo) (wall, file float64) {
	if n := len(stats.WallToFile); n >= len(infos)+2 {
		last := stats.WallToFile[len(infos)+1]
		first := stats.WallToFile[1]
		wall = last[0] - first[0]
		file = last[1] - first[1]
		if wall > 0 {
			return wall, file
		}
	}
	for _, info := range infos {
		d := info.TotalDuration.Seconds()
		if d > 0 {
			wall += d
			file += d
		}
	}
	return wall, file
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
