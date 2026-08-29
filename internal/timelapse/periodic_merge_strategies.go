package timelapse

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/mediaprobe"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/merge"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
)

// goConcatMerge merges MP4 segments losslessly using the pure-Go merge package
// (equivalent to `ffmpeg -f concat -c copy`). Requires all segments to share
// the same codec and SPS/PPS (H.264) or VPS/SPS/PPS (H.265) — enforced by
// merge.MergeMP4Segments. No external process, no pixel decoding.
//
// Returns an error (caller falls back to FFmpeg concat or Go keyframe merge)
// if segments are not MP4, fail to parse, or have mismatched params.
func (m *PeriodicMergeManager) goConcatMerge(ctx context.Context, segments []model.Recording, outputPath string) error {
	if err := os.MkdirAll(filepath.Dir(outputPath), 0o755); err != nil {
		return fmt.Errorf("periodic merge: create output dir: %w", err)
	}

	// Parse each segment into a SegmentInfo for the merge package.
	// Only MP4 files are supported; non-MP4 inputs cause a fallback to FFmpeg.
	segInfos := make([]*merge.SegmentInfo, 0, len(segments))
	totalFrames := 0
	for _, seg := range segments {
		if !mediaprobe.IsLikelyMP4(seg.FilePath) {
			return fmt.Errorf("segment %s is not MP4 (Go concat requires MP4)", seg.ID)
		}
		info, err := merge.ParseSegment(seg.FilePath)
		if err != nil {
			return fmt.Errorf("parse segment %s: %w", seg.ID, err)
		}
		segInfos = append(segInfos, info)
		totalFrames += info.SampleCount
	}

	slog.Debug(
		"periodic merge: running Go concat",
		"segments", len(segInfos),
		"total_frames", totalFrames,
	)

	// merge.MergeMP4Segments validates codec/SPS/PPS/VPS/audio consistency and
	// keyframe-aligns every segment head (#488); keyframe-less segments are
	// skipped and reported in stats.
	stats, err := merge.MergeMP4Segments(ctx, segInfos, outputPath)
	if err != nil {
		_ = os.Remove(outputPath)
		return fmt.Errorf("go concat merge: %w", err)
	}
	if len(stats.SkippedNoKeyframe) > 0 {
		slog.Warn("periodic merge: skipped keyframe-less segments",
			"skipped", len(stats.SkippedNoKeyframe), "total", len(segInfos))
	}

	// Report 100% progress — Go concat is fast (streaming copy), no granular progress.
	if totalFrames > 0 {
		m.updateProgressBatch(ctx, segments, 100)
	}
	return nil
}

// ffmpegConcatMerge merges segments using FFmpeg concat demuxer.

// ffmpegConcatMerge merges segments using FFmpeg concat demuxer.
func (m *PeriodicMergeManager) ffmpegConcatMerge(ctx context.Context, segments []model.Recording, outputPath string) error {
	if err := os.MkdirAll(filepath.Dir(outputPath), 0o755); err != nil {
		return fmt.Errorf("periodic merge: create output dir: %w", err)
	}

	listFile, err := os.CreateTemp("", "periodic_merge_*.txt")
	if err != nil {
		return fmt.Errorf("periodic merge: create list file: %w", err)
	}
	defer os.Remove(listFile.Name())

	listWriter := bufio.NewWriter(listFile)
	for _, seg := range segments {
		absPath, err := filepath.Abs(seg.FilePath)
		if err != nil {
			listWriter.Flush()
			listFile.Close()
			return fmt.Errorf("periodic merge: resolve path %s: %w", seg.FilePath, err)
		}
		escapedPath := strings.ReplaceAll(absPath, "'", "'\\''")
		fmt.Fprintf(listWriter, "file '%s'\n", escapedPath)
	}
	if err := listWriter.Flush(); err != nil {
		listFile.Close()
		return fmt.Errorf("periodic merge: flush list file: %w", err)
	}
	listFile.Close()

	ffmpegPath := "ffmpeg"
	if m.merger != nil {
		if fm, ok := m.merger.(*FFmpegMerger); ok && fm.caps != nil && fm.caps.FFmpegPath != "" {
			ffmpegPath = fm.caps.FFmpegPath
		}
	}

	args := []string{
		"-f", "concat",
		"-safe", "0",
		"-i", listFile.Name(),
		"-c", "copy",
		"-y",
		outputPath,
	}

	slog.Debug(
		"periodic merge: running ffmpeg concat",
		"path", ffmpegPath,
		"args", args,
		"segments", len(segments),
	)

	cmd := exec.CommandContext(ctx, ffmpegPath, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("periodic merge: stderr pipe: %w", err)
	}

	// Count total frames for progress estimation (best-effort).
	totalFrames := 0
	for _, seg := range segments {
		frames, err := probeVideoFrameCount(ctx, seg.FilePath)
		if err != nil {
			slog.Warn("periodic merge: failed to probe frame count for progress",
				"segment_id", seg.ID, "error", err)
		}
		totalFrames += frames
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("periodic merge: start ffmpeg: %w", err)
	}

	// Read stderr for progress and error capture.
	var lastLines []string
	scanner := bufio.NewScanner(stderr)
	for scanner.Scan() {
		line := scanner.Text()
		lastLines = append(lastLines, line)
		if len(lastLines) > 10 {
			lastLines = lastLines[1:]
		}

		// Parse frame count from ffmpeg progress output for progress tracking.
		if totalFrames > 0 {
			if match := frameProgressRegex.FindStringSubmatch(line); len(match) > 1 {
				if frame, err := strconv.Atoi(match[1]); err == nil && frame > 0 {
					pct := frame * 100 / totalFrames
					if pct > 99 {
						pct = 99
					}
					m.updateProgressBatch(ctx, segments, pct)
				}
			}
		}
	}
	errOutput := strings.Join(lastLines, "\n")

	if waitErr := cmd.Wait(); waitErr != nil {
		if ctx.Err() != nil {
			killMergeProcess(cmd)
			os.Remove(outputPath)
			return ctx.Err()
		}
		errMsg := fmt.Sprintf("ffmpeg concat failed: %v", waitErr)
		if errOutput != "" {
			errMsg = fmt.Sprintf("%s\nstderr: %s", errMsg, errOutput)
		}
		return fmt.Errorf("%s", errMsg)
	}

	return nil
}

// goMergeSegments merges segments using the Go merger.
//
// This is codec-aware: it collects frame files of ANY extension produced by
// the keyframe extractor (.jpg/.jpeg for snapshot cameras, .h264 for H.264
// cameras, .h265 for H.265 cameras) and preserves the original extension
// when copying into the temp dir. The actual merger is chosen by
// AutoDetectMerger based on the frame extension — H265GoMerger for .h265,
// H264GoMerger for .h264, GoMerger (JPEG) otherwise.
//
// This fixes a bug where H.265 timelapse cameras could never produce a
// periodic merge: the old code hardcoded .jpg collection + JPEG merger, so
// H.265 frame dirs were always reported as "no frames found in segments".

// goMergeSegments merges segments using the Go merger.
//
// This is codec-aware: it collects frame files of ANY extension produced by
// the keyframe extractor (.jpg/.jpeg for snapshot cameras, .h264 for H.264
// cameras, .h265 for H.265 cameras) and preserves the original extension
// when copying into the temp dir. The actual merger is chosen by
// AutoDetectMerger based on the frame extension — H265GoMerger for .h265,
// H264GoMerger for .h264, GoMerger (JPEG) otherwise.
//
// This fixes a bug where H.265 timelapse cameras could never produce a
// periodic merge: the old code hardcoded .jpg collection + JPEG merger, so
// H.265 frame dirs were always reported as "no frames found in segments".
func (m *PeriodicMergeManager) goMergeSegments(ctx context.Context, segments []model.Recording, outputPath string) error {
	if err := os.MkdirAll(filepath.Dir(outputPath), 0o755); err != nil {
		return fmt.Errorf("periodic merge: create output dir: %w", err)
	}

	tmpDir, err := os.MkdirTemp("", "periodic_go_merge_*")
	if err != nil {
		return fmt.Errorf("periodic merge: create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	// isFrameFile reports whether a directory entry is a timelapse frame.
	// Frame files are named frame_NNNNNN.{jpg,jpeg,h264,h265} by the keyframe
	// extractor / snapshot capturer. We match on the "frame_" prefix to avoid
	// picking up non-frame files (README, .DS_Store, etc.) that might live in
	// the segment dir, while accepting any codec extension.
	isFrameFile := func(name string) bool {
		if !strings.HasPrefix(name, "frame_") {
			return false
		}
		lower := strings.ToLower(name)
		return strings.HasSuffix(lower, ".jpg") ||
			strings.HasSuffix(lower, ".jpeg") ||
			strings.HasSuffix(lower, ".h264") ||
			strings.HasSuffix(lower, ".h265")
	}

	// Count total frames for progress estimation.
	totalFrames := 0
	for _, seg := range segments {
		entries, err := os.ReadDir(seg.FilePath)
		if err != nil {
			return fmt.Errorf("periodic merge: read segment dir %s: %w", seg.ID, err)
		}
		for _, entry := range entries {
			if !entry.IsDir() && isFrameFile(entry.Name()) {
				totalFrames++
			}
		}
	}

	// Copy frames into tmpDir with sequential numbering, preserving the
	// original extension so AutoDetectMerger can pick the right codec path.
	// All frames in a periodic window come from the same camera, so they
	// share one extension — but we preserve per-file just in case.
	frameIndex := 0
	framesCopied := 0
	for _, seg := range segments {
		entries, err := os.ReadDir(seg.FilePath)
		if err != nil {
			return fmt.Errorf("periodic merge: read segment dir %s: %w", seg.ID, err)
		}

		// Sort entries for deterministic frame ordering. ReadDir returns
		// entries in directory order which is usually creation order on
		// ext4, but not guaranteed.
		sort.Slice(entries, func(i, j int) bool {
			return entries[i].Name() < entries[j].Name()
		})

		for _, entry := range entries {
			if entry.IsDir() || !isFrameFile(entry.Name()) {
				continue
			}

			src, err := os.Open(filepath.Join(seg.FilePath, entry.Name()))
			if err != nil {
				return fmt.Errorf("periodic merge: open frame %s: %w", entry.Name(), err)
			}

			frameIndex++
			framesCopied++
			// Preserve the original extension (e.g. .h265, .h264, .jpg).
			ext := filepath.Ext(entry.Name())
			frameName := fmt.Sprintf("frame_%06d%s", frameIndex, ext)
			dst, err := os.Create(filepath.Join(tmpDir, frameName))
			if err != nil {
				src.Close()
				return fmt.Errorf("periodic merge: create frame %s: %w", frameName, err)
			}

			_, err = io.Copy(dst, src)
			src.Close()
			if cerr := dst.Close(); cerr != nil && err == nil {
				err = cerr
			}
			if err != nil {
				return fmt.Errorf("periodic merge: copy frame %s: %w", frameName, err)
			}

			// Report copy progress.
			if totalFrames > 0 && (framesCopied%10 == 0 || framesCopied == totalFrames) {
				pct := framesCopied * 100 / totalFrames
				m.updateProgressBatch(ctx, segments, pct)
			}

			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}
		}
	}

	if frameIndex == 0 {
		return fmt.Errorf("periodic merge: no frames found in segments")
	}

	// Use AutoDetectMerger so H.265/H.264/JPEG frames are dispatched to the
	// matching codec merger. The injected m.merger may be JPEG-only
	// (NewGoMerger) in production wiring — AutoDetectMerger wraps all three
	// and picks based on the frame extension in tmpDir.
	merger := NewAutoDetectMerger()
	result, err := merger.Merge(ctx, tmpDir, outputPath, m.fps)
	if err != nil {
		return fmt.Errorf("periodic merge: merge failed: %w", err)
	}

	slog.Info(
		"periodic merge: Go merge completed",
		"output_path", result.OutputPath,
		"frames_merged", result.FramesMerged,
		"duration", result.Duration,
	)

	return nil
}

// finalizeMerge updates segment statuses to daily_merged after a successful merge.
// When a TimelapseMergeStore is configured, it also upserts a row in the
// timelapse_merges table for this output so the frontend can discover and
// play the long-window timelapse video.

// runPerCodecMerge groups segments by codec type and runs a separate merge
// pipeline for each group. This prevents mixing incompatible codecs (e.g.
// H264 + H265) in a single merge output.
//
// Codec grouping:
//   - Timelapse + AVI/MJPEG extracted frames → "jpeg" group
//   - H264 extracted frames → "h264" group
//   - H265 extracted frames → "h265" group
//
// Output naming:
//   - JPEG group: periodic_WINDOW.mp4
//   - H264 group: periodic_WINDOW_h264.mp4
//   - H265 group: periodic_WINDOW_h265.mp4
func (m *PeriodicMergeManager) runPerCodecMerge(ctx context.Context, segments []model.Recording, cameraID, windowLabel string) error {
	// Map each segment to its codec group.
	// Timelapse and MJPEG/AVI extracted frames are JPEG-based and can be grouped.
	groups := make(map[string][]model.Recording)
	for _, seg := range segments {
		codec := string(seg.Format)
		if codec == string(model.FormatTimelapse) || codec == "" {
			codec = "jpeg" // timelapse JPEG frames
		}
		groups[codec] = append(groups[codec], seg)
	}

	var lastErr error
	for codec, segs := range groups {
		suffix := ""
		if codec != "jpeg" {
			suffix = "_" + codec
		}
		outputFilename := fmt.Sprintf("periodic_%s%s.mp4", windowLabel, suffix)
		outputPath := filepath.Join(m.dataDir, cameraID, outputFilename)

		if err := m.runMergePipeline(ctx, segs, outputPath); err != nil {
			slog.Warn("periodic merge: per-codec merge failed",
				"codec", codec, "camera_id", cameraID, "error", err)
			lastErr = err
		}
	}
	return lastErr
}
