package timelapse

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
)

// RecordingLister is the interface for listing recordings from the database.
type RecordingLister interface {
	ListRecordings(ctx context.Context, filter model.RecordingFilter) ([]model.Recording, error)
}

// DailyMergeManager handles daily merge operations for timelapse recordings.
type DailyMergeManager struct {
	store   RecordingLister
	updater MergeStatusUpdater
	merger  TimelapseMerger
	fps     int
	dataDir string
}

// NewDailyMergeManager creates a new DailyMergeManager.
func NewDailyMergeManager(store RecordingLister, updater MergeStatusUpdater, merger TimelapseMerger, fps int, dataDir string) *DailyMergeManager {
	return &DailyMergeManager{
		store:   store,
		updater: updater,
		merger:  merger,
		fps:     fps,
		dataDir: dataDir,
	}
}

// Run executes the daily merge pipeline for the given camera on the given date.
// date format: "2006-01-02"
func (m *DailyMergeManager) Run(ctx context.Context, cameraID string, date string) error {
	startOfDay, endOfDay, err := parseDayRange(date)
	if err != nil {
		return fmt.Errorf("daily merge: invalid date %q: %w", date, err)
	}

	// 1. Query DB for merged timelapse segments in the date range.
	merged := true
	recordings, err := m.store.ListRecordings(ctx, model.RecordingFilter{
		CameraID:  cameraID,
		Format:    model.FormatTimelapse,
		Merged:    &merged,
		StartTime: startOfDay,
		EndTime:   endOfDay,
	})
	if err != nil {
		return fmt.Errorf("daily merge: list recordings: %w", err)
	}

	// Filter to only include segments with merge_status='merged' (not already daily_merged).
	segments := filterMergedSegments(recordings)

	// 2. Handle no segments.
	if len(segments) == 0 {
		slog.Warn("daily merge: no segments found for date",
			"camera_id", cameraID,
			"date", date,
		)
		return nil
	}

	// 3. Build output path.
	dailyFilename := fmt.Sprintf("daily_%s.mp4", date)
	dailyPath := filepath.Join(m.dataDir, cameraID, dailyFilename)

	// 4. Handle single segment — just copy.
	if len(segments) == 1 {
		return m.handleSingleSegment(ctx, segments[0], dailyPath)
	}

	// 5. Check segment compatibility.
	compatible, err := checkSegmentCompatibility(ctx, segments)
	if err != nil {
		slog.Warn("daily merge: compatibility check failed, using Go fallback",
			"camera_id", cameraID,
			"date", date,
			"error", err,
		)
		// Fall through to Go merge.
	}

	// 6. Attempt FFmpeg concat merge if compatible.
	if compatible && m.merger != nil && m.merger.CanMerge() {
		err = m.ffmpegConcatMerge(ctx, segments, dailyPath)
		if err == nil {
			return m.finalizeMerge(ctx, segments, dailyPath)
		}
		slog.Warn("daily merge: FFmpeg merge failed, falling back to Go merge",
			"camera_id", cameraID,
			"date", date,
			"error", err,
		)
		_ = m.markMergeFailed(ctx, segments, err)
		return err
	}

	// 7. Fall back to Go merge.
	err = m.goMergeSegments(ctx, segments, dailyPath)
	if err != nil {
		_ = m.markMergeFailed(ctx, segments, err)
		return fmt.Errorf("daily merge: Go merge failed: %w", err)
	}

	return m.finalizeMerge(ctx, segments, dailyPath)
}

// parseDayRange returns the start and end of a day for the given date string.
func parseDayRange(date string) (time.Time, time.Time, error) {
	t, err := time.Parse("2006-01-02", date)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	startOfDay := t
	endOfDay := t.Add(24 * time.Hour)
	return startOfDay, endOfDay, nil
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

// handleSingleSegment copies a single segment to the daily output path.
func (m *DailyMergeManager) handleSingleSegment(ctx context.Context, seg model.Recording, dailyPath string) error {
	if err := os.MkdirAll(filepath.Dir(dailyPath), 0755); err != nil {
		return fmt.Errorf("daily merge: create output dir: %w", err)
	}

	input, err := os.ReadFile(seg.FilePath)
	if err != nil {
		return fmt.Errorf("daily merge: read segment %s: %w", seg.ID, err)
	}
	if err := os.WriteFile(dailyPath, input, 0644); err != nil {
		return fmt.Errorf("daily merge: write daily file: %w", err)
	}

	if m.updater != nil {
		if err := m.updater.SetMergeStatus(ctx, []string{seg.ID}, "daily_merged"); err != nil {
			slog.Warn("daily merge: failed to update merge status",
				"recording_id", seg.ID,
				"error", err,
			)
		}
	}

	slog.Info("daily merge: single segment processed",
		"camera_id", seg.CameraID,
		"segment_id", seg.ID,
		"daily_path", dailyPath,
	)
	return nil
}

// ffmpegConcatMerge merges segments using FFmpeg concat demuxer.
func (m *DailyMergeManager) ffmpegConcatMerge(ctx context.Context, segments []model.Recording, outputPath string) error {
	if err := os.MkdirAll(filepath.Dir(outputPath), 0755); err != nil {
		return fmt.Errorf("daily merge: create output dir: %w", err)
	}

	listFile, err := os.CreateTemp("", "daily_merge_*.txt")
	if err != nil {
		return fmt.Errorf("daily merge: create list file: %w", err)
	}
	defer os.Remove(listFile.Name())

	listWriter := bufio.NewWriter(listFile)
	for _, seg := range segments {
		absPath, err := filepath.Abs(seg.FilePath)
		if err != nil {
			listWriter.Flush()
			listFile.Close()
			return fmt.Errorf("daily merge: resolve path %s: %w", seg.FilePath, err)
		}
		escapedPath := strings.ReplaceAll(absPath, "'", "'\\''")
		fmt.Fprintf(listWriter, "file '%s'\n", escapedPath)
	}
	if err := listWriter.Flush(); err != nil {
		listFile.Close()
		return fmt.Errorf("daily merge: flush list file: %w", err)
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

	slog.Debug("daily merge: running ffmpeg concat",
		"path", ffmpegPath,
		"args", args,
		"segments", len(segments),
	)

	cmd := exec.CommandContext(ctx, ffmpegPath, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("daily merge: stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("daily merge: start ffmpeg: %w", err)
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
func (m *DailyMergeManager) goMergeSegments(ctx context.Context, segments []model.Recording, outputPath string) error {
	if m.merger == nil {
		return fmt.Errorf("daily merge: no merger available")
	}

	if err := os.MkdirAll(filepath.Dir(outputPath), 0755); err != nil {
		return fmt.Errorf("daily merge: create output dir: %w", err)
	}

	tmpDir, err := os.MkdirTemp("", "daily_go_merge_*")
	if err != nil {
		return fmt.Errorf("daily merge: create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	frameIndex := 0
	for _, seg := range segments {
		entries, err := os.ReadDir(seg.FilePath)
		if err != nil {
			return fmt.Errorf("daily merge: read segment dir %s: %w", seg.ID, err)
		}

		for _, entry := range entries {
			if entry.IsDir() || (!strings.HasSuffix(entry.Name(), ".jpg") && !strings.HasSuffix(entry.Name(), ".jpeg")) {
				continue
			}

			data, err := os.ReadFile(filepath.Join(seg.FilePath, entry.Name()))
			if err != nil {
				return fmt.Errorf("daily merge: read frame %s: %w", entry.Name(), err)
			}

			frameIndex++
			frameName := fmt.Sprintf("frame_%06d.jpg", frameIndex)
			if err := os.WriteFile(filepath.Join(tmpDir, frameName), data, 0644); err != nil {
				return fmt.Errorf("daily merge: write frame %s: %w", frameName, err)
			}

			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}
		}
	}

	if frameIndex == 0 {
		return fmt.Errorf("daily merge: no frames found in segments")
	}

	result, err := m.merger.Merge(ctx, tmpDir, outputPath, m.fps)
	if err != nil {
		return fmt.Errorf("daily merge: merge failed: %w", err)
	}

	slog.Info("daily merge: Go merge completed",
		"output_path", result.OutputPath,
		"frames_merged", result.FramesMerged,
		"duration", result.Duration,
	)

	return nil
}

// finalizeMerge updates segment statuses to daily_merged after a successful merge.
func (m *DailyMergeManager) finalizeMerge(ctx context.Context, segments []model.Recording, dailyPath string) error {
	ids := make([]string, len(segments))
	for i, seg := range segments {
		ids[i] = seg.ID
	}

	if m.updater != nil {
		if err := m.updater.SetMergeStatus(ctx, ids, "daily_merged"); err != nil {
			slog.Warn("daily merge: failed to update merge statuses",
				"count", len(ids),
				"error", err,
			)
		}
	}

	slog.Info("daily merge: completed successfully",
		"segments", len(segments),
		"daily_path", dailyPath,
	)
	return nil
}

// markMergeFailed updates segment statuses to failed.
func (m *DailyMergeManager) markMergeFailed(ctx context.Context, segments []model.Recording, mergeErr error) error {
	ids := make([]string, len(segments))
	for i, seg := range segments {
		ids[i] = seg.ID
	}

	if m.updater != nil {
		if err := m.updater.SetMergeStatus(ctx, ids, model.MergeStatusFailed); err != nil {
			slog.Warn("daily merge: failed to set merge status to failed",
				"count", len(ids),
				"error", err,
			)
			return err
		}
	}

	slog.Error("daily merge: failed",
		"segments", len(segments),
		"error", mergeErr,
	)
	return nil
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
			slog.Warn("daily merge: segment resolution mismatch",
				"segment_id", segments[i].ID,
				"expected", fmt.Sprintf("%dx%d", refWidth, refHeight),
				"got", fmt.Sprintf("%dx%d", width, height),
			)
			return false, nil
		}

		if codec != refCodec {
			slog.Warn("daily merge: segment codec mismatch",
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