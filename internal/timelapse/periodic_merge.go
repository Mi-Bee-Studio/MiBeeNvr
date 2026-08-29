package timelapse

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
)

// frameProgressRegex matches frame=N in ffmpeg stderr progress output.
var frameProgressRegex = regexp.MustCompile(`frame=\s*(\d+)`)

// retryInfo tracks the retry count and timestamp for a segment merge attempt.

// retryInfo tracks the retry count and timestamp for a segment merge attempt.
type retryInfo struct {
	count     int
	timestamp time.Time
}

// TimelapseMergeStore is the interface for persisting periodic-merge output
// metadata to the timelapse_merges table. Implementations must be safe for
// concurrent use (one Run per camera, but API-triggered merges may race with
// scheduled ones).

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

// Option configures PeriodicMergeManager behavior.
type Option func(*PeriodicMergeManager)

// WithRecordingEnabledProvider sets a function that reports if a camera has
// recording_enabled=true. When true, Run will extract frames from video
// recordings in the merge window and include them in the timelapse output.
// The provider is called once per Run invocation with the camera ID.
// Use functional options pattern so existing call sites need no changes.

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
