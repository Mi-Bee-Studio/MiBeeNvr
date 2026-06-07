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
	"strings"
	"syscall"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
)

// PeriodicMergeManager handles merge operations for timelapse recordings
// with configurable merge intervals (8h, 12h, 24h, 7d, 30d).
type PeriodicMergeManager struct {
	store    RecordingLister
	updater  MergeStatusUpdater
	merger   TimelapseMerger
	fps      int
	dataDir  string
	duration time.Duration
}

// NewPeriodicMergeManager creates a new PeriodicMergeManager with the given merge duration.
func NewPeriodicMergeManager(store RecordingLister, updater MergeStatusUpdater, merger TimelapseMerger, fps int, dataDir string, duration time.Duration) *PeriodicMergeManager {
	return &PeriodicMergeManager{
		store:    store,
		updater:  updater,
		merger:   merger,
		fps:      fps,
		dataDir:  dataDir,
		duration: duration,
	}
}

// Duration returns the configured merge duration.
func (m *PeriodicMergeManager) Duration() time.Duration {
	return m.duration
}

// Run executes the merge pipeline for the given camera for the merge window
// containing the reference time t.
func (m *PeriodicMergeManager) Run(ctx context.Context, cameraID string, t time.Time) error {
	startTime, endTime := parseMergeRange(t, m.duration)
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

	// Filter to only include segments with merge_status='merged'.
	segments := filterMergedSegments(recordings)

	// 2. Handle no segments.
	if len(segments) == 0 {
		slog.Warn("periodic merge: no segments found for window",
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
	if err := os.MkdirAll(filepath.Dir(outputPath), 0755); err != nil {
		return fmt.Errorf("periodic merge: create output dir: %w", err)
	}

	// Handle single segment — just copy.
	if len(segments) == 1 {
		return m.handleSingleSegment(ctx, segments[0], outputPath)
	}

	// Check segment compatibility.
	compatible, err := checkSegmentCompatibility(ctx, segments)
	if err != nil {
		slog.Warn("periodic merge: compatibility check failed, using Go fallback",
			"error", err,
		)
	}

	// Attempt FFmpeg concat merge if compatible.
	if compatible && m.merger != nil && m.merger.CanMerge() {
		err = m.ffmpegConcatMerge(ctx, segments, outputPath)
		if err == nil {
			return m.finalizeMerge(ctx, segments, outputPath)
		}
		slog.Warn("periodic merge: FFmpeg merge failed, falling back to Go merge",
			"error", err,
		)
		_ = m.markMergeFailed(ctx, segments, err)
		return err
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
// aligned to the given duration boundary in UTC.
//
// Supported durations and their alignment rules:
//   - 8h:  aligned to 00:00, 08:00, 16:00 UTC
//   - 12h: aligned to 00:00, 12:00 UTC
//   - 24h: aligned to 00:00 UTC daily
//   - 7d:  aligned to Monday 00:00 UTC
//   - 30d: aligned to 1st of month 00:00 UTC
func parseMergeRange(t time.Time, dur time.Duration) (time.Time, time.Time) {
	t = t.UTC()

	// Calendar-month alignment for 30d duration.
	if dur == 30*24*time.Hour {
		year, month, _ := t.Date()
		start := time.Date(year, month, 1, 0, 0, 0, 0, time.UTC)
		end := time.Date(year, month+1, 1, 0, 0, 0, 0, time.UTC)
		return start, end
	}

	// Duration-based alignment using Truncate.
	// Go zero time (0001-01-01 00:00:00 UTC) is a Monday in the proleptic
	// Gregorian calendar, so Truncate(7*24*time.Hour) naturally aligns to
	// Monday 00:00 UTC for weekly windows.
	start := t.Truncate(dur)
	end := start.Add(dur)
	return start, end
}

// handleSingleSegment copies a single segment to the output path.
func (m *PeriodicMergeManager) handleSingleSegment(ctx context.Context, seg model.Recording, outputPath string) error {
	if err := os.MkdirAll(filepath.Dir(outputPath), 0755); err != nil {
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

	if m.updater != nil {
		if err := m.updater.SetMergeStatus(ctx, []string{seg.ID}, "daily_merged"); err != nil {
			slog.Warn("periodic merge: failed to update merge status",
				"recording_id", seg.ID,
				"error", err,
			)
		}
	}

	slog.Info("periodic merge: single segment processed",
		"camera_id", seg.CameraID,
		"segment_id", seg.ID,
		"output_path", outputPath,
	)
	return nil
}

// ffmpegConcatMerge merges segments using FFmpeg concat demuxer.
func (m *PeriodicMergeManager) ffmpegConcatMerge(ctx context.Context, segments []model.Recording, outputPath string) error {
	if err := os.MkdirAll(filepath.Dir(outputPath), 0755); err != nil {
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

	slog.Debug("periodic merge: running ffmpeg concat",
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

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("periodic merge: start ffmpeg: %w", err)
	}

	errOutput := consumeStderr(stderr)

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

	if err := os.MkdirAll(filepath.Dir(outputPath), 0755); err != nil {
		return fmt.Errorf("periodic merge: create output dir: %w", err)
	}

	tmpDir, err := os.MkdirTemp("", "periodic_go_merge_*")
	if err != nil {
		return fmt.Errorf("periodic merge: create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	frameIndex := 0
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

	slog.Info("periodic merge: Go merge completed",
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

	if m.updater != nil {
		if err := m.updater.SetMergeStatus(ctx, ids, "daily_merged"); err != nil {
			slog.Warn("periodic merge: failed to update merge statuses",
				"count", len(ids),
				"error", err,
			)
		}
	}

	slog.Info("periodic merge: completed successfully",
		"segments", len(segments),
		"output_path", outputPath,
	)
	return nil
}

// markMergeFailed updates segment statuses to failed.
func (m *PeriodicMergeManager) markMergeFailed(ctx context.Context, segments []model.Recording, mergeErr error) error {
	ids := make([]string, len(segments))
	for i, seg := range segments {
		ids[i] = seg.ID
	}

	if m.updater != nil {
		if err := m.updater.SetMergeStatus(ctx, ids, model.MergeStatusFailed); err != nil {
			slog.Warn("periodic merge: failed to set merge status to failed",
				"count", len(ids),
				"error", err,
			)
			return err
		}
	}

	slog.Error("periodic merge: failed",
		"segments", len(segments),
		"error", mergeErr,
	)
	return nil
}

// filterMergedSegments filters recordings to only those with merge_status='merged'.
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
// Uses ffprobe to read metadata from each segment.
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
			slog.Warn("merge: segment resolution mismatch",
				"segment_id", segments[i].ID,
				"expected", fmt.Sprintf("%dx%d", refWidth, refHeight),
				"got", fmt.Sprintf("%dx%d", width, height),
			)
			return false, nil
		}

		if codec != refCodec {
			slog.Warn("merge: segment codec mismatch",
				"segment_id", segments[i].ID,
				"expected", refCodec,
				"got", codec,
			)
			return false, nil
		}
	}

	return true, nil
}

// probeSegmentMetadata uses ffprobe to extract video resolution and codec from a file.
func probeSegmentMetadata(ctx context.Context, filePath string) (width, height int, codec string, err error) {
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
