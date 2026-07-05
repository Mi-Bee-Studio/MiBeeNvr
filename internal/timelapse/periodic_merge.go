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
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/mediaprobe"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/merge"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
)

// frameProgressRegex matches frame=N in ffmpeg stderr progress output.
var frameProgressRegex = regexp.MustCompile(`frame=\s*(\d+)`)

// retryInfo tracks the retry count and timestamp for a segment merge attempt.
type retryInfo struct {
	count     int
	timestamp time.Time
}

// PeriodicMergeManager handles merge operations for timelapse recordings
// with configurable merge intervals (8h, 12h, 24h, 7d, 30d).
type PeriodicMergeManager struct {
	store    RecordingLister
	updater  MergeStatusUpdater
	merger   TimelapseMerger
	fps      int
	dataDir  string
	duration time.Duration
	loc      *time.Location

	retryCounts map[string]retryInfo
	retryMu     sync.Mutex
}

// NewPeriodicMergeManager creates a new PeriodicMergeManager with the given merge duration.
// If loc is nil, UTC is used for window alignment.
func NewPeriodicMergeManager(store RecordingLister, updater MergeStatusUpdater, merger TimelapseMerger, fps int, dataDir string, duration time.Duration, loc *time.Location) *PeriodicMergeManager {
	if loc == nil {
		loc = time.UTC
	}
	return &PeriodicMergeManager{
		store:       store,
		updater:     updater,
		merger:      merger,
		fps:         fps,
		dataDir:     dataDir,
		duration:    duration,
		loc:         loc,
		retryCounts: make(map[string]retryInfo),
	}
}

// Duration returns the configured merge duration.
func (m *PeriodicMergeManager) Duration() time.Duration {
	return m.duration
}

// Run executes the merge pipeline for the given camera for the merge window
// containing the reference time t.
func (m *PeriodicMergeManager) Run(ctx context.Context, cameraID string, t time.Time) error {
	startTime, endTime := parseMergeRange(t, m.duration, m.loc)
	windowLabel := startTime.Format("2006-01-02_150405")

	// 1. Query DB for merged timelapse segments in the date range.
	merged := true
	recordings, err := m.store.ListRecordings(ctx, model.RecordingFilter{
		CameraID:  cameraID,
		Format:    model.FormatTimelapse,
		Merged:    &merged,
		StartTime: startTime,
		EndTime:   endTime,
	})
	if err != nil {
		return fmt.Errorf("periodic merge: list recordings: %w", err)
	}

	// Filter to only include merged or retryable segments.
	segments := m.filterEligibleSegments(recordings)

	// 2. Handle no segments.
	if len(segments) == 0 {
		slog.Warn(
			"periodic merge: no segments found for window",
			"camera_id", cameraID,
			"window", windowLabel,
		)
		return nil
	}

	// 3. Build output path.
	outputFilename := fmt.Sprintf("periodic_%s.mp4", windowLabel)
	outputPath := filepath.Join(m.dataDir, cameraID, outputFilename)

	// 4. Run the merge pipeline.
	return m.runMergePipeline(ctx, segments, outputPath)
}

// runMergePipeline runs the core merge logic on the given segments.
func (m *PeriodicMergeManager) runMergePipeline(ctx context.Context, segments []model.Recording, outputPath string) error {
	if err := os.MkdirAll(filepath.Dir(outputPath), 0o755); err != nil {
		return fmt.Errorf("periodic merge: create output dir: %w", err)
	}
	// Set initial merge progress to 0 for all segments.
	m.updateProgressBatch(ctx, segments, 0)
	// Handle single segment — just copy.
	if len(segments) == 1 {
		return m.handleSingleSegment(ctx, segments[0], outputPath)
	}

	// Check segment compatibility.
	compatible, err := checkSegmentCompatibility(ctx, segments)
	if err != nil {
		slog.Warn(
			"periodic merge: compatibility check failed, using Go fallback",
			"error", err,
		)
	}

	// Attempt Go concat merge (pure-Go, lossless -c copy equivalent) if compatible.
	// Falls back to FFmpeg concat, then to Go keyframe merge.
	if compatible {
		// Prefer pure-Go concat (merge.MergeMP4Segments) — no external process.
		if err := m.goConcatMerge(ctx, segments, outputPath); err == nil {
			return m.finalizeMerge(ctx, segments, outputPath)
		} else if m.merger != nil && m.merger.CanMerge() {
			// Go concat failed (e.g. SPS/PPS mismatch that merge rejects) — try FFmpeg concat.
			slog.Warn("periodic merge: Go concat failed, trying FFmpeg concat",
				"error", err)
			_ = os.Remove(outputPath)
			if err := m.ffmpegConcatMerge(ctx, segments, outputPath); err == nil {
				return m.finalizeMerge(ctx, segments, outputPath)
			} else {
				slog.Warn("periodic merge: FFmpeg concat failed, falling back to Go merge",
					"error", err)
				_ = os.Remove(outputPath)
				_ = m.markMergeFailed(ctx, segments, err)
				return err
			}
		} else {
			_ = m.markMergeFailed(ctx, segments, err)
			return err
		}
	}

	// Fall back to Go merge.
	err = m.goMergeSegments(ctx, segments, outputPath)
	if err != nil {
		_ = m.markMergeFailed(ctx, segments, err)
		return fmt.Errorf("periodic merge: Go merge failed: %w", err)
	}

	return m.finalizeMerge(ctx, segments, outputPath)
}

// parseMergeRange returns the start and end of the merge window containing t,
// aligned to the given duration boundary in the provided timezone.
//
// Supported durations and their alignment rules:
//   - 8h:  aligned to 00:00, 08:00, 16:00 local time
//   - 12h: aligned to 00:00, 12:00 local time
//   - 24h: aligned to 00:00 local time
//   - 7d:  aligned to Monday 00:00 local time
//   - 30d: aligned to 1st of month 00:00 local time
func parseMergeRange(t time.Time, dur time.Duration, loc *time.Location) (time.Time, time.Time) {
	if loc == nil {
		loc = time.UTC
	}
	t = t.In(loc)
	year, month, day := t.Date()

	// Calendar-month alignment for 30d duration.
	if dur == 30*24*time.Hour {
		start := time.Date(year, month, 1, 0, 0, 0, 0, loc)
		end := time.Date(year, month+1, 1, 0, 0, 0, 0, loc)
		return start, end
	}

	// Weekly alignment (7d): align to Monday 00:00 local time.
	if dur == 7*24*time.Hour {
		weekday := t.Weekday()
		// weekday: Sunday=0, Monday=1, ..., Saturday=6
		// days since last Monday: (weekday - 1 + 7) % 7
		daysSinceMonday := (int(weekday) - 1 + 7) % 7
		monday := t.AddDate(0, 0, -daysSinceMonday)
		y, m, d := monday.Date()
		start := time.Date(y, m, d, 0, 0, 0, 0, loc)
		end := start.Add(dur)
		return start, end
	}

	// Duration-based alignment: align time-of-day to nearest multiple of dur from midnight local.
	// For 24h: midnight local
	// For 12h: midnight or noon local
	// For 8h:  00:00, 08:00, 16:00 local
	durHours := int(dur.Hours())
	hour := t.Hour()
	alignedHour := (hour / durHours) * durHours
	start := time.Date(year, month, day, alignedHour, 0, 0, 0, loc)
	end := start.Add(dur)
	return start, end
}

// handleSingleSegment copies a single segment to the output path.
func (m *PeriodicMergeManager) handleSingleSegment(ctx context.Context, seg model.Recording, outputPath string) error {
	if err := os.MkdirAll(filepath.Dir(outputPath), 0o755); err != nil {
		return fmt.Errorf("periodic merge: create output dir: %w", err)
	}

	src, err := os.Open(seg.FilePath)
	if err != nil {
		return fmt.Errorf("periodic merge: open segment %s: %w", seg.ID, err)
	}
	defer src.Close()

	dst, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("periodic merge: create output file: %w", err)
	}
	defer dst.Close()

	if _, err := io.Copy(dst, src); err != nil {
		return fmt.Errorf("periodic merge: copy segment %s: %w", seg.ID, err)
	}

	// Set progress to 100 for completed single segment merge.
	if m.updater != nil {
		if err := m.updater.UpdateMergeProgress(ctx, seg.ID, 100); err != nil {
			slog.Warn(
				"periodic merge: failed to update merge progress",
				"recording_id", seg.ID,
				"error", err,
			)
		}
	}

	if m.updater != nil {
		if err := m.updater.SetMergeStatus(ctx, []string{seg.ID}, "daily_merged"); err != nil {
			slog.Warn(
				"periodic merge: failed to update merge status",
				"recording_id", seg.ID,
				"error", err,
			)
		}
	}

	slog.Info(
		"periodic merge: single segment processed",
		"camera_id", seg.CameraID,
		"segment_id", seg.ID,
		"output_path", outputPath,
	)
	return nil
}

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

	// merge.MergeMP4Segments validates codec/SPS/PPS/VPS/audio consistency.
	if err := merge.MergeMP4Segments(ctx, segInfos, outputPath); err != nil {
		_ = os.Remove(outputPath)
		return fmt.Errorf("Go concat merge: %w", err)
	}

	// Report 100% progress — Go concat is fast (streaming copy), no granular progress.
	if totalFrames > 0 {
		m.updateProgressBatch(ctx, segments, 100)
	}
	return nil
}

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
func (m *PeriodicMergeManager) goMergeSegments(ctx context.Context, segments []model.Recording, outputPath string) error {
	if m.merger == nil {
		return fmt.Errorf("periodic merge: no merger available")
	}

	if err := os.MkdirAll(filepath.Dir(outputPath), 0o755); err != nil {
		return fmt.Errorf("periodic merge: create output dir: %w", err)
	}

	tmpDir, err := os.MkdirTemp("", "periodic_go_merge_*")
	if err != nil {
		return fmt.Errorf("periodic merge: create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	// Count total frames for progress estimation.
	totalFrames := 0
	for _, seg := range segments {
		entries, err := os.ReadDir(seg.FilePath)
		if err != nil {
			return fmt.Errorf("periodic merge: read segment dir %s: %w", seg.ID, err)
		}
		for _, entry := range entries {
			if !entry.IsDir() && (strings.HasSuffix(entry.Name(), ".jpg") || strings.HasSuffix(entry.Name(), ".jpeg")) {
				totalFrames++
			}
		}
	}

	frameIndex := 0
	framesCopied := 0
	for _, seg := range segments {
		entries, err := os.ReadDir(seg.FilePath)
		if err != nil {
			return fmt.Errorf("periodic merge: read segment dir %s: %w", seg.ID, err)
		}

		for _, entry := range entries {
			if entry.IsDir() || (!strings.HasSuffix(entry.Name(), ".jpg") && !strings.HasSuffix(entry.Name(), ".jpeg")) {
				continue
			}

			src, err := os.Open(filepath.Join(seg.FilePath, entry.Name()))
			if err != nil {
				return fmt.Errorf("periodic merge: open frame %s: %w", entry.Name(), err)
			}

			frameIndex++
			framesCopied++
			frameName := fmt.Sprintf("frame_%06d.jpg", frameIndex)
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

	result, err := m.merger.Merge(ctx, tmpDir, outputPath, m.fps)
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
		if err := m.updater.SetMergeStatus(ctx, ids, "daily_merged"); err != nil {
			slog.Warn(
				"periodic merge: failed to update merge statuses",
				"count", len(ids),
				"error", err,
			)
		}
	}

	slog.Info(
		"periodic merge: completed successfully",
		"segments", len(segments),
		"output_path", outputPath,
	)
	return nil
}

// markMergeFailed updates retry counts and marks segments as failed.
// Segments are retried up to 3 times before being permanently marked as failed.
func (m *PeriodicMergeManager) markMergeFailed(ctx context.Context, segments []model.Recording, mergeErr error) error {
	ids := make([]string, len(segments))
	for i, seg := range segments {
		ids[i] = seg.ID
	}

	// Increment retry counts and check if any segment has exhausted retries.
	m.retryMu.Lock()
	maxRetriesReached := false
	now := time.Now()
	for _, seg := range segments {
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
	m.updateProgressBatch(ctx, segments, 0)

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
			"segments", len(segments),
			"error", mergeErr,
		)
	} else {
		slog.Warn(
			"periodic merge: failed, will retry on next cycle",
			"segments", len(segments),
			"retry_count", func() int {
				m.retryMu.Lock()
				defer m.retryMu.Unlock()
				if len(segments) > 0 {
					return m.retryCounts[segments[0].ID].count
				}
				return 0
			}(),
			"error", mergeErr,
		)
	}

	return nil
}

// updateProgressBatch updates merge progress for a batch of segments.
func (m *PeriodicMergeManager) updateProgressBatch(ctx context.Context, segments []model.Recording, progress int) {
	if m.updater == nil {
		return
	}
	for _, seg := range segments {
		if err := m.updater.UpdateMergeProgress(ctx, seg.ID, progress); err != nil {
			slog.Warn(
				"periodic merge: failed to update merge progress",
				"recording_id", seg.ID,
				"progress", progress,
				"error", err,
			)
		}
	}
}

// filterEligibleSegments filters recordings to include merged segments
// and failed segments with remaining retry attempts (< 3).
func (m *PeriodicMergeManager) filterEligibleSegments(recordings []model.Recording) []model.Recording {
	var segments []model.Recording
	m.retryMu.Lock()
	defer m.retryMu.Unlock()
	for _, r := range recordings {
		if r.MergeStatus == model.MergeStatusMerged {
			segments = append(segments, r)
			continue
		}
		if r.MergeStatus == model.MergeStatusFailed {
			if info, ok := m.retryCounts[r.ID]; ok && info.count < 3 {
				segments = append(segments, r)
			}
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
func filterMergedSegments(recordings []model.Recording) []model.Recording {
	var segments []model.Recording
	for _, r := range recordings {
		if r.MergeStatus == model.MergeStatusMerged {
			segments = append(segments, r)
		}
	}
	return segments
}

// checkSegmentCompatibility checks if all segments have compatible resolution and codec.
// Uses the pure-Go mediaprobe by default, falling back to ffprobe when needed.
func checkSegmentCompatibility(ctx context.Context, segments []model.Recording) (bool, error) {
	if len(segments) < 2 {
		return true, nil
	}

	refWidth, refHeight, refCodec, err := probeSegmentMetadata(ctx, segments[0].FilePath)
	if err != nil {
		return false, fmt.Errorf("probe reference segment %s: %w", segments[0].ID, err)
	}

	for i := 1; i < len(segments); i++ {
		width, height, codec, err := probeSegmentMetadata(ctx, segments[i].FilePath)
		if err != nil {
			return false, fmt.Errorf("probe segment %s: %w", segments[i].ID, err)
		}

		if width != refWidth || height != refHeight {
			slog.Warn(
				"merge: segment resolution mismatch",
				"segment_id", segments[i].ID,
				"expected", fmt.Sprintf("%dx%d", refWidth, refHeight),
				"got", fmt.Sprintf("%dx%d", width, height),
			)
			return false, nil
		}

		if codec != refCodec {
			slog.Warn(
				"merge: segment codec mismatch",
				"segment_id", segments[i].ID,
				"expected", refCodec,
				"got", codec,
			)
			return false, nil
		}
	}

	return true, nil
}

// probeSegmentMetadata extracts video resolution and codec from a file.
//
// It prefers the pure-Go mediaprobe (no external process) and falls back to
// ffprobe when mediaprobe fails or the file is not MP4. The returned codec
// uses ffprobe-compatible names ("h264", "hevc") so compatibility comparisons
// behave identically to the previous ffprobe-only implementation.
func probeSegmentMetadata(ctx context.Context, filePath string) (width, height int, codec string, err error) {
	// Fast path: pure-Go probe.
	if mediaprobe.IsLikelyMP4(filePath) {
		if info, e := mediaprobe.ProbeMP4(filePath); e == nil {
			return info.Width, info.Height, info.CodecName, nil
		}
	}

	// Fallback: ffprobe subprocess (requires ffprobe on PATH).
	args := []string{
		"-v", "quiet",
		"-print_format", "json",
		"-show_streams",
		filePath,
	}

	cmd := exec.CommandContext(ctx, "ffprobe", args...)
	output, err := cmd.Output()
	if err != nil {
		return 0, 0, "", fmt.Errorf("ffprobe failed: %w", err)
	}

	lines := strings.Split(string(output), "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, `"codec_name":`) {
			codec = strings.Trim(strings.TrimPrefix(trimmed, `"codec_name":`), ` ",`)
		} else if strings.HasPrefix(trimmed, `"width":`) {
			fmt.Sscanf(trimmed, `"width": %d`, &width)
		} else if strings.HasPrefix(trimmed, `"height":`) {
			fmt.Sscanf(trimmed, `"height": %d`, &height)
		}
	}

	if width == 0 || height == 0 || codec == "" {
		return 0, 0, "", fmt.Errorf("incomplete metadata from ffprobe")
	}

	return width, height, codec, nil
}

// probeVideoFrameCount returns the total number of video frames in a file.
//
// Uses the pure-Go mediaprobe (reads stsz box, no decoding) and falls back to
// ffprobe -count_frames when mediaprobe fails or the file is not MP4. The
// mediaprobe path is dramatically faster since ffprobe -count_frames must
// decode the entire file to count frames.
func probeVideoFrameCount(ctx context.Context, filePath string) (int, error) {
	// Fast path: pure-Go probe — frame count comes from stsz.SampleCount.
	if mediaprobe.IsLikelyMP4(filePath) {
		if info, err := mediaprobe.ProbeMP4(filePath); err == nil {
			return info.FrameCount, nil
		}
	}

	// Fallback: ffprobe subprocess.
	cmd := exec.CommandContext(
		ctx, "ffprobe",
		"-v", "error",
		"-count_frames",
		"-select_streams", "v:0",
		"-show_entries", "stream=nb_read_frames",
		"-of", "csv=p=0",
		filePath,
	)
	output, err := cmd.Output()
	if err != nil {
		return 0, fmt.Errorf("ffprobe frame count failed: %w", err)
	}
	s := strings.TrimSpace(string(output))
	if s == "" {
		return 0, fmt.Errorf("ffprobe returned empty frame count")
	}
	count, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("ffprobe frame count parse failed: %w", err)
	}
	return count, nil
}
