package merge

import (
	"context"
	"encoding/json"
	"errors"
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

// foldAppendBucket folds one video-only run via the sequential-append bucket
// (#853): O(segment) bytes at the mdat tail + in-place table patches, never a
// full-bucket rewrite. Handles both bucket creation (fresh append-bucket
// file) and appends to an existing one; classicFellBack=true means the run
// must take the classic MergeMP4Segments path instead (not an append bucket,
// or capacity exhausted — the compact rewrite is that path).
//
// Caller holds bucket.mu. The append bucket file is opened per fold (fresh
// mirror from the tables — cheap moov-only reads) and closed after; folds are
// minutes apart, keeping no FDs between them.
func (r *RollingMergeCoordinator) foldAppendBucket(
	ctx context.Context,
	run []*foldSegment,
	bucket *bucketInfo,
) (outputPath, mergedRecID string, classicFellBack bool, err error) {
	first, last := run[0].seg, run[len(run)-1].seg
	cameraID := first.cameraID
	cfg := r.resolveRollingConfig(cameraID)

	var ab *AppendBucket
	// finalPath carries the create-branch's final segment name out of the
	// branch scope: the row accounting below runs after the branch, and the
	// merged row must reference it (not the create-time temp name, #912).
	// Empty on the append branch — there ab.Path() IS the final path.
	var finalPath string
	if bucket.mergedFilePath == "" {
		tempPath, fp, derr := r.store.CreateSegment(cameraID, first.format)
		if derr != nil {
			return "", "", false, fmt.Errorf("create append bucket output: %w", derr)
		}
		finalPath = fp
		nab, cerr := CreateAppendBucket(tempPath, run[0].info, AppendBucketConfig{Window: cfg.Window})
		if cerr != nil {
			os.Remove(tempPath)
			return "", "", false, fmt.Errorf("create append bucket: %w", cerr)
		}
		ab = nab
		// Defer the temp→final rename until the batch landed; on any error
		// below the temp file is removed and the bucket stays un-created.
		defer func() {
			if err != nil {
				ab.Close()
				os.Remove(tempPath)
				return
			}
			if rerr := ab.Close(); rerr != nil {
				err = fmt.Errorf("close append bucket: %w", rerr)
				os.Remove(finalPath)
				return
			}
			if rerr := r.store.CloseSegmentMerged(tempPath, finalPath); rerr != nil {
				err = fmt.Errorf("finalize append bucket: %w", rerr)
				os.Remove(finalPath)
				return
			}
			outputPath = finalPath
		}()
	} else {
		nab, oerr := OpenAppendBucket(bucket.mergedFilePath)
		if oerr != nil {
			if errors.Is(oerr, ErrNotAppendBucket) {
				return "", "", true, nil // classic bucket — classic path
			}
			return "", "", false, fmt.Errorf("open append bucket: %w", oerr)
		}
		ab = nab
		defer ab.Close()
	}

	srcs := make([]AppendSource, 0, len(run))
	for _, fs := range run {
		srcs = append(srcs, AppendSource{Path: fs.seg.filePath, Samples: fs.info.Samples})
	}
	ast, aerr := ab.AppendBatch(r.resolveTimelapseCadence(cameraID), r.resolveTimelapseGap(cameraID), srcs)
	if aerr != nil {
		if errors.Is(aerr, ErrAppendCapacity) && bucket.mergedFilePath != "" {
			// 容量耗尽：压紧 = 经典全量重写路径（该桶此后保持经典格式）。
			rollingLogger.Info("append bucket capacity exhausted — compacting via classic rewrite",
				"camera_id", cameraID, "bucket", bucket.mergedFilePath)
			return "", "", true, nil
		}
		return "", "", false, fmt.Errorf("append batch: %w", aerr)
	}

	// --- 行/桶账务：镜像 appendToBucket 的 #496 墙钟口径 ---
	ts := float64(ab.timescale)
	if ts <= 0 {
		ts = 1000
	}
	deltaWall := float64(ast.WallTicks) / ts
	deltaFile := float64(ast.FileTicks) / ts

	if bucket.mergedFilePath == "" {
		wallSec := last.endedAt.Sub(first.startedAt).Seconds()
		mergedRecID = strconv.FormatInt(time.Now().UnixNano(), 10)
		bucket.wallDurSec = wallSec
		bucket.fileDurSec = deltaFile
		bucket.lastEnded = last.endedAt
		bucket.rowStart = first.startedAt
		bucket.wallFile = [][2]float64{{0, 0}, {wallSec, deltaFile}}
		totalFrames := 0
		for _, fs := range run {
			totalFrames += fs.info.SampleCount
		}
		fi, ferr := os.Stat(outputPathOf(ab))
		if ferr != nil {
			return "", "", false, fmt.Errorf("stat append bucket: %w", ferr)
		}
		mergedRec := &model.Recording{
			ID:       mergedRecID,
			CameraID: cameraID,
			// The row must reference the FINAL path (#912): the temp→final
			// rename happens in the deferred block AFTER this insert, and
			// nothing updates the row afterwards — a row stamped with the
			// .tmp path stayed a permanent 404 until the next append fold
			// happened to overwrite it, and a restart in that window left
			// the orphan behind for the startup sweep. Size is measured on
			// the temp file (rename doesn't change content).
			FilePath:    finalPath,
			Format:      model.Format(first.format),
			StartedAt:   first.startedAt,
			EndedAt:     last.endedAt,
			Duration:    wallSec,
			FileSize:    fi.Size(),
			FrameCount:  totalFrames,
			MergeStatus: model.MergeStatusMerged,
			TimelineMap: mapJSON(bucket.wallFile),
		}
		recIDs := make([]string, len(run))
		for i, fs := range run {
			recIDs[i] = fs.seg.recordingID
		}
		if uerr := storage.RetryOnBusy(ctx, func() error {
			return r.db.RollingReplaceRecordings(ctx, mergedRec, "", recIDs)
		}); uerr != nil {
			return "", "", false, fmt.Errorf("db replace (append create): %w", uerr)
		}
		r.deleteRunSources(ctx, segsOf(run), "append-bucket-create")
		return outputPathOf(ab), mergedRecID, false, nil
	}

	// 追加：gap/span 钳制 + #698 乱序守卫 + I1 钳制（同 appendToBucket）。
	if !bucket.lastEnded.IsZero() {
		if gapWall := last.endedAt.Sub(bucket.lastEnded).Seconds(); gapWall > deltaWall {
			deltaWall = gapWall
		}
	}
	if spanWall := last.endedAt.Sub(first.startedAt).Seconds(); deltaWall < spanWall {
		deltaWall = spanWall
	}
	if last.endedAt.After(bucket.lastEnded) {
		bucket.lastEnded = last.endedAt
	}
	if !bucket.rowStart.IsZero() && first.startedAt.Before(bucket.rowStart) {
		bucket.rowStart = first.startedAt
	}
	bucket.wallDurSec += deltaWall
	bucket.fileDurSec += deltaFile
	if !bucket.rowStart.IsZero() {
		if span := bucket.lastEnded.Sub(bucket.rowStart).Seconds(); span > bucket.wallDurSec {
			bucket.wallDurSec = span
		}
	}
	if len(bucket.wallFile) == 0 {
		bucket.wallFile = [][2]float64{{0, 0}}
	}
	bucket.wallFile = append(bucket.wallFile, [2]float64{bucket.wallDurSec, bucket.fileDurSec})

	fi, ferr := os.Stat(ab.Path())
	if ferr != nil {
		return "", "", false, fmt.Errorf("stat append bucket: %w", ferr)
	}
	mergedRecID = bucket.mergedRecID
	mergedRec := &model.Recording{
		ID:          mergedRecID,
		CameraID:    cameraID,
		FilePath:    ab.Path(),
		Format:      model.Format(first.format),
		StartedAt:   bucket.rowStart,
		EndedAt:     bucket.lastEnded,
		Duration:    bucket.wallDurSec,
		FileSize:    fi.Size(),
		FrameCount:  int(ab.SampleCount()),
		MergeStatus: model.MergeStatusMerged,
		TimelineMap: mapJSON(bucket.wallFile),
	}
	recIDs := make([]string, len(run))
	for i, fs := range run {
		recIDs[i] = fs.seg.recordingID
	}
	if uerr := storage.RetryOnBusy(ctx, func() error {
		return r.db.RollingReplaceRecordings(ctx, mergedRec, bucket.mergedRecID, recIDs)
	}); uerr != nil {
		rollingLogger.Error("db update failed after append-bucket fold — data may be inconsistent",
			"camera_id", cameraID, "merged_path", ab.Path(), "error", uerr)
		return "", "", false, fmt.Errorf("db replace (append): %w", uerr)
	}
	r.deleteRunSources(ctx, segsOf(run), "append-bucket")
	return ab.Path(), mergedRecID, false, nil
}

func segsOf(run []*foldSegment) []pendingSegmentInfo {
	out := make([]pendingSegmentInfo, len(run))
	for i, fs := range run {
		out[i] = fs.seg
	}
	return out
}

// outputPathOf 提取追加桶的目标路径（创建期在 defer 里才可知，经由桶自身携带）。
func outputPathOf(ab *AppendBucket) string { return ab.Path() }
