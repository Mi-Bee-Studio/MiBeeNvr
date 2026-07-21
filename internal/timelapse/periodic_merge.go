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

// TimelapseMergeStore is the interface for persisting periodic-merge output
// metadata to the timelapse_merges table. Implementations must be safe for
// concurrent use (one Run per camera, but API-triggered merges may race with
// scheduled ones).
type TimelapseMergeStore interface {
	InsertTimelapseMerge(ctx context.Context, m *model.TimelapseMerge) (int64, error)
	UpdateTimelapseMergeStatus(ctx context.Context, id int64, status, errMsg string) error
	CompleteTimelapseMerge(ctx context.Context, id int64, outputPath string, fileSize int64, frameCount int, codec, sourceSegmentIDs string) error
	FindTimelapseMergeByWindow(ctx context.Context, cameraID string, windowStart time.Time, durationLabel string) (*model.TimelapseMerge, error)
}

// IntermediateMP4Pruner is implemented by storage.DB to let the periodic
// merger clear the recordings.merge_path pointer for source segments whose
// intermediate .mp4 output has been pruned. Kept as a separate interface so
// the shared MergeStatusUpdater (used by the rolling merger too) does not
// need to grow a periodic-merge-specific method.
type IntermediateMP4Pruner interface {
	ClearMergePathBatch(ctx context.Context, ids []string) error
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

	// recordingEnabledProvider reports whether a camera has recording_enabled=true.
	// When set and returns true, Run extracts frames from video recordings in the
	// merge window and includes them alongside existing timelapse recordings.
	recordingEnabledProvider func(cameraID string) bool

	// mergeStore, when set, persists one row per periodic-merge output to the
	// timelapse_merges table so the frontend can discover / play / delete the
	// long-window videos. nil = legacy behavior (file on disk, no DB record).
	mergeStore TimelapseMergeStore

	// retainIntermediateMP4, when false (the default), makes finalizeMerge
	// delete each source segment's rolling-merge .mp4 output (the file at
	// recordings.merge_path) after the periodic merge has folded those
	// segments into a long-window output. This reclaims disk without losing
	// data — the raw frame directories (frame_*.h264/.h265/.jpg) are kept so
	// the periodic output can be regenerated if needed.
	retainIntermediateMP4 bool

	// pruner, when set AND retainIntermediateMP4 is false, clears the
	// recordings.merge_path DB pointer for source segments whose intermediate
	// .mp4 has been pruned. nil = skip DB cleanup (legacy behavior).
	pruner IntermediateMP4Pruner

	// durationLabel is the original config string (e.g. "24h", "natural-day",
	// "8h") preserved so DB rows record what the user configured, not the
	// parsed duration. Empty when the manager was constructed without a label
	// (legacy callers / tests) — in that case the row uses duration.String().
	durationLabel string

	// runCtx holds per-Run metadata (camera/window/label) that finalizeMerge
	// needs to write the timelapse_merges DB row. Set at the top of Run under
	// runCtxMu. Empty runCtx.cameraID signals "no DB recording" (legacy mode
	// when mergeStore is nil).
	runCtxMu sync.Mutex
	runCtx   periodicRunContext
}

// periodicRunContext is the per-Run metadata stashed on the manager so that
// deep callees (finalizeMerge) can write a timelapse_merges DB row without
// threading extra arguments through every function signature.
type periodicRunContext struct {
	cameraID      string
	startTime     time.Time
	endTime       time.Time
	durationLabel string
}

// Option configures PeriodicMergeManager behavior.
type Option func(*PeriodicMergeManager)

// WithRecordingEnabledProvider sets a function that reports if a camera has
// recording_enabled=true. When true, Run will extract frames from video
// recordings in the merge window and include them in the timelapse output.
// The provider is called once per Run invocation with the camera ID.
// Use functional options pattern so existing call sites need no changes.
func WithRecordingEnabledProvider(p func(cameraID string) bool) Option {
	return func(m *PeriodicMergeManager) {
		m.recordingEnabledProvider = p
	}
}

// WithMergeStore enables persistence of periodic-merge outputs to the
// timelapse_merges table. When set, Run inserts a 'merging' row at start,
// completes it on success (with output path, file size, frame count, codec,
// source segment ids), or marks it failed on error. nil opts out (legacy
// behavior: file on disk, no DB record).
func WithMergeStore(s TimelapseMergeStore) Option {
	return func(m *PeriodicMergeManager) {
		m.mergeStore = s
	}
}

// WithDurationLabel records the original config string (e.g. "natural-day",
// "8h", "7d") so DB rows reflect what the user configured rather than the
// parsed Go duration. Optional — when unset, Run falls back to duration.String().
func WithDurationLabel(label string) Option {
	return func(m *PeriodicMergeManager) {
		m.durationLabel = label
	}
}

// WithRetainIntermediateMP4 controls whether per-segment rolling-merge .mp4
// outputs are kept after a periodic merge folds them into a long-window
// output. Pass true to retain (debugging / re-merge safety); pass false (the
// default) to clean them up and reclaim disk. The raw frame directories are
// always preserved regardless.
func WithRetainIntermediateMP4(retain bool) Option {
	return func(m *PeriodicMergeManager) {
		m.retainIntermediateMP4 = retain
	}
}

// WithIntermediateMP4Pruner wires the storage layer used to clear
// recordings.merge_path after intermediate .mp4 files are pruned. Optional —
// when nil, finalizeMerge prunes the files but cannot update the DB pointer
// (the row would still point at a now-deleted path). Production wiring passes
// the *storage.DB here.
func WithIntermediateMP4Pruner(p IntermediateMP4Pruner) Option {
	return func(m *PeriodicMergeManager) {
		m.pruner = p
	}
}

// NewPeriodicMergeManager creates a new PeriodicMergeManager with the given merge duration.
// If loc is nil, UTC is used for window alignment.
// Variadic opts enable optional behavior without breaking existing call sites.
func NewPeriodicMergeManager(store RecordingLister, updater MergeStatusUpdater, merger TimelapseMerger, fps int, dataDir string, duration time.Duration, loc *time.Location, opts ...Option) *PeriodicMergeManager {
	if loc == nil {
		loc = time.UTC
	}
	m := &PeriodicMergeManager{
		store:       store,
		updater:     updater,
		merger:      merger,
		fps:         fps,
		dataDir:     dataDir,
		duration:    duration,
		loc:         loc,
		retryCounts: make(map[string]retryInfo),
	}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

// Duration returns the configured merge duration.
func (m *PeriodicMergeManager) Duration() time.Duration {
	return m.duration
}

// Run executes the merge pipeline for the given camera for the merge window
// containing the reference time t.
//
// When recording is enabled (recordingEnabledProvider returns true), the method
// also queries video-format recordings (H264, H265, AVI, MJPEG) in the same
// window, extracts frames via RecordingFrameExtractor, and merges them alongside
// existing timelapse recordings. Extracted frames are organized into per-codec
// temporary directories and cleaned up after merge completion.
func (m *PeriodicMergeManager) Run(ctx context.Context, cameraID string, t time.Time) error {
	startTime, endTime := parseMergeRange(t, m.duration, m.loc)
	windowLabel := startTime.Format("2006-01-02_150405")

	// Stash per-Run context so deep callees (finalizeMerge) can write a
	// timelapse_merges DB row. cameraID == "" signals legacy no-DB mode.
	durationLabel := m.durationLabel
	if durationLabel == "" {
		durationLabel = m.duration.String()
	}
	m.runCtxMu.Lock()
	m.runCtx = periodicRunContext{
		cameraID:      cameraID,
		startTime:     startTime,
		endTime:       endTime,
		durationLabel: durationLabel,
	}
	m.runCtxMu.Unlock()
	defer func() {
		// Clear context on exit so a stale Run doesn't leak into the next one.
		m.runCtxMu.Lock()
		m.runCtx = periodicRunContext{}
		m.runCtxMu.Unlock()
	}()

	// Query ALL timelapse segments in the date range — both merged (rolling
	// merge produced .mp4) and unmerged (raw frame directories, when
	// merge_enabled=false skips rolling merge). The pipeline tiers handle both.
	recordings, err := m.store.ListRecordings(ctx, model.RecordingFilter{
		CameraID:  cameraID,
		Format:    model.FormatTimelapse,
		StartTime: startTime,
		EndTime:   endTime,
	})
	if err != nil {
		return fmt.Errorf("periodic merge: list recordings: %w", err)
	}

	// Filter to only include eligible segments (merged, or unmerged raw dirs,
	// or retryable failed segments).
	segments := m.filterEligibleSegments(recordings)

	// Deferred cleanup for extracted frame temporary directories (when
	// recording is enabled and recordings are found).
	var tmpDirs []string
	defer func() {
		for _, d := range tmpDirs {
			if err := os.RemoveAll(d); err != nil {
				slog.Warn("periodic merge: failed to clean up temp dir",
					"dir", d, "error", err)
			}
		}
	}()

	// When recording is enabled, extract frames from video recordings in the
	// merge window and include them alongside existing timelapse segments.
	// This supports:
	//   - H264 ✅ (keyframe-sync IDR samples from MP4)
	//   - H265 ✅ (IRAP NAL type 19/20 sync samples from MP4)
	//   - AVI ✅  (MJPEG JPEG frames via internal/avi demuxer)
	//   - MJPEG ✅ (same JPEG extraction as AVI)
	//   - MPEG-TS ✗ (no moov/stss boxes, too expensive to probe)
	recordingEnabled := m.recordingEnabledProvider != nil && m.recordingEnabledProvider(cameraID)
	if recordingEnabled {
		videoSegs, dirs, err := m.extractRecordingFrames(ctx, cameraID, startTime, endTime)
		if err != nil {
			slog.Warn("periodic merge: recording frame extraction failed",
				"camera_id", cameraID, "error", err)
		}
		tmpDirs = append(tmpDirs, dirs...)
		segments = append(segments, videoSegs...)
	}

	// 2. Handle no segments.
	if len(segments) == 0 {
		slog.Warn(
			"periodic merge: no segments found for window",
			"camera_id", cameraID,
			"window", windowLabel,
		)
		return nil
	}

	if recordingEnabled {
		// 3b. Per-codec merge: group segments by codec type and run separate
		// pipelines to avoid mixing incompatible codecs (e.g. H264+H265)
		// in a single merge output. Temporary directories are cleaned up
		// via the deferred function above.
		return m.runPerCodecMerge(ctx, segments, cameraID, windowLabel)
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

	// Check if any segment is an unmerged raw frame directory (no .mp4 from
	// rolling merge). These must go through Go keyframe merge (Tier 4) directly,
	// since they're directories, not MP4 files.
	if hasUnmergedRawSegments(segments) {
		slog.Info("periodic merge: detected unmerged raw segments, using Go keyframe merge")
		err := m.goMergeSegments(ctx, segments, outputPath)
		if err != nil {
			_ = m.markMergeFailed(ctx, segments, err)
			return fmt.Errorf("periodic merge: Go merge failed: %w", err)
		}
		return m.finalizeMerge(ctx, segments, outputPath)
	}

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

	// Duration-based alignment: align time-of-day to the largest multiple of
	// dur that is ≤ the time-of-day, starting from midnight local.
	//   - 24h: midnight local
	//   - 12h: midnight or noon local
	//   - 8h:  00:00, 08:00, 16:00 local
	//   - sub-hour (e.g. 45m, 30m): aligned by wall-clock seconds, supports
	//     fractional-hour durations that don't divide 24 evenly.
	durHours := int(dur.Hours())
	if durHours > 0 && 24%durHours == 0 {
		// Whole-hour duration that divides 24: integer-hour alignment.
		hour := t.Hour()
		alignedHour := (hour / durHours) * durHours
		start := time.Date(year, month, day, alignedHour, 0, 0, 0, loc)
		end := start.Add(dur)
		return start, end
	}
	// General case: align by wall-clock nanoseconds since midnight. Works for
	// any positive duration (sub-hour, non-divisor-of-24, etc.). The window
	// may straddle midnight if dur does not divide 24h evenly.
	secOfDay := t.Hour()*3600 + t.Minute()*60 + t.Second()
	durSec := int(dur / time.Second)
	if durSec <= 0 {
		durSec = 1
	}
	alignedSec := (secOfDay / durSec) * durSec
	start := time.Date(year, month, day, 0, 0, 0, 0, loc).Add(time.Duration(alignedSec) * time.Second)
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
		return fmt.Errorf("go concat merge: %w", err)
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

// extractRecordingFrames queries video-format recordings in the merge window
// and extracts frames into per-codec temporary directories. Returns synthetic
// Recording entries for each codec group and the list of temp dirs to clean up.
//
// Supported formats for frame extraction:
//   - H264 ✅ (keyframe-sync H.264 IDR samples from MP4)
//   - H265 ✅ (IRAP NAL type 19/20 sync samples from MP4)
//   - AVI ✅  (MJPEG JPEG frames via internal/avi demuxer)
//   - MJPEG ✅ (same JPEG extraction as AVI)
//   - MPEG-TS ✗ (no moov/stss boxes, too expensive to probe)
func (m *PeriodicMergeManager) extractRecordingFrames(ctx context.Context, cameraID string, startTime, endTime time.Time) ([]model.Recording, []string, error) {
	videoFormats := []model.Format{model.FormatH264, model.FormatH265, model.FormatAVI, model.FormatMJPEG}
	recs, err := m.store.ListRecordings(ctx, model.RecordingFilter{
		CameraID:  cameraID,
		Formats:   videoFormats,
		StartTime: startTime,
		EndTime:   endTime,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("list video recordings: %w", err)
	}
	if len(recs) == 0 {
		return nil, nil, nil
	}

	// Map recording format to extraction codec (grouping key).
	// MJPEG recordings produce JPEG frames like AVI.
	codecKey := func(f model.Format) model.Format {
		if f == model.FormatMJPEG {
			return model.FormatAVI // MJPEG → JPEG extraction like AVI
		}
		return f
	}

	// Group recordings by codec for per-codec extraction.
	codecRecordings := make(map[model.Format][]model.Recording)
	for _, r := range recs {
		ck := codecKey(r.Format)
		codecRecordings[ck] = append(codecRecordings[ck], r)
	}

	extractor := NewRecordingFrameExtractor()
	// Determine extraction interval based on FPS. At least 1 frame per recording.
	interval := time.Second / time.Duration(m.fps)
	if interval <= 0 {
		interval = 100 * time.Millisecond // default 10fps
	}

	var segments []model.Recording
	var tmpDirs []string

	for codec, recs := range codecRecordings {
		tmpDir, err := os.MkdirTemp("", fmt.Sprintf("periodic_extract_%s_*", codec))
		if err != nil {
			// Clean up previously created dirs on error.
			for _, d := range tmpDirs {
				os.RemoveAll(d)
			}
			return nil, nil, fmt.Errorf("create temp dir for %s: %w", codec, err)
		}
		tmpDirs = append(tmpDirs, tmpDir)

		for _, rec := range recs {
			n, err := extractor.ExtractFrames(rec.FilePath, codec, interval, tmpDir)
			if err != nil {
				slog.Warn("periodic merge: frame extraction failed, skipping recording",
					"recording_id", rec.ID, "format", rec.Format, "error", err)
				continue
			}
			slog.Debug("periodic merge: extracted frames from recording",
				"recording_id", rec.ID, "format", rec.Format, "frames", n, "dir", tmpDir)
		}

		// Check if any frames were actually extracted.
		entries, err := os.ReadDir(tmpDir)
		if err != nil || len(entries) == 0 {
			slog.Warn("periodic merge: no frames extracted for codec, skipping",
				"codec", codec, "camera_id", cameraID)
			continue
		}

		// Create a synthetic Recording entry for this codec's extracted frame directory.
		// MergeStatus is empty (unmerged), so runMergePipeline's hasUnmergedRawSegments
		// check will route through Go keyframe merge path (Tier 4) as a raw frame dir.
		segments = append(segments, model.Recording{
			ID:          fmt.Sprintf("extracted_%s_%s", cameraID, string(codec)),
			CameraID:    cameraID,
			FilePath:    tmpDir,
			Format:      codec,
			MergeStatus: "", // unmerged raw segment
		})
	}

	return segments, tmpDirs, nil
}

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
