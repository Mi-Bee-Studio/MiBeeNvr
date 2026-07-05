// Package timelapse provides types and interfaces for timelapse recording and segment merging.
package timelapse

import (
	"context"
	"os"
	"strings"
)

// MergeMode represents the merge output mode for timelapse recordings.
type MergeMode string

const (
	// MergeModeAuto auto-detects the best merge mode based on input format.
	MergeModeAuto MergeMode = "auto"
	// MergeModeMP4 merges frames into an MP4 video file.
	MergeModeMP4 MergeMode = "mp4"
	// MergeModeJPEG merges frames into a single JPEG file (e.g., montage).
	MergeModeJPEG MergeMode = "jpeg"
)

// String returns the string representation of the MergeMode.
func (m MergeMode) String() string { return string(m) }

// MergeTier represents the available merge implementation tier.
type MergeTier string

const (
	// TierFFmpeg uses FFmpeg for merging (requires external binary).
	TierFFmpeg MergeTier = "ffmpeg"
	// TierGo uses native Go implementation for merging.
	TierGo MergeTier = "go"
	// TierJPEG uses native Go JPEG processing for merging.
	TierJPEG MergeTier = "jpeg"
)

// String returns the string representation of the MergeTier.
func (t MergeTier) String() string { return string(t) }

// FrameSource represents the source of frames for timelapse recording.
type FrameSource string

const (
	// FrameSourceAuto auto-detects the best frame source based on camera capabilities.
	FrameSourceAuto FrameSource = "auto"
	// FrameSourceSnapshot uses HTTP snapshot endpoint for frame capture.
	FrameSourceSnapshot FrameSource = "snapshot"
	// FrameSourceRTSPKeyframe extracts keyframes from RTSP stream for frame capture.
	FrameSourceRTSPKeyframe FrameSource = "rtsp_keyframe"
	// FrameSourceMJPEG uses MJPEG stream for frame capture.
	FrameSourceMJPEG FrameSource = "mjpeg"
)

// MergeStatus represents the merge process status for a timelapse recording.
type MergeStatus string

const (
	// MergeStatusNone indicates no merge has been attempted.
	MergeStatusNone MergeStatus = "none"
	// MergeStatusMerging indicates a merge is in progress.
	MergeStatusMerging MergeStatus = "merging"
	// MergeStatusMerged indicates the merge completed successfully.
	MergeStatusMerged MergeStatus = "merged"
	// MergeStatusFailed indicates the merge failed.
	MergeStatusFailed MergeStatus = "failed"
)

// String returns the string representation of the MergeStatus.
func (s MergeStatus) String() string { return string(s) }

// MergeConfig holds merge configuration for timelapse recordings.
type MergeConfig struct {
	// Enabled controls whether merging is active for this camera or globally.
	Enabled bool `json:"enabled" yaml:"enabled"`
	// Mode selects the merge output mode (auto, mp4, jpeg).
	Mode MergeMode `json:"mode" yaml:"mode"`
	// OutputFPS controls the output frame rate for merged video.
	// Repurposed from the deprecated TimelapseRecorderConfig.OutputFPS.
	OutputFPS int `json:"output_fps" yaml:"output_fps"`
	// DeleteOriginal removes the source frame directories after a successful merge.
	DeleteOriginal bool `json:"delete_original" yaml:"delete_original"`
	// DailyMerge groups frames by day for daily merged output files.
	DailyMerge bool `json:"daily_merge" yaml:"daily_merge"`
	// CRF controls the Constant Rate Factor for x264/x265 encoding (0-51, default 23).
	// Only applies when using software libx264/libx265 encoder.
	CRF int `json:"crf" yaml:"crf"`
	// Bitrate sets a target bitrate for encoding (e.g. "2M", "500k").
	// Overrides CRF when set; only applies to software libx264/libx265 encoder.
	Bitrate string `json:"bitrate" yaml:"bitrate"`
}

// MergeResult holds the outcome of a merge operation.
type MergeResult struct {
	// Tier identifies which merge implementation produced this result.
	Tier MergeTier `json:"tier"`
	// OutputPath is the path to the merged output file.
	OutputPath string `json:"output_path"`
	// Error contains the error message if the merge failed.
	Error string `json:"error,omitempty"`
	// FramesMerged is the number of frames successfully merged.
	FramesMerged int `json:"frames_merged"`
	// Duration is the total duration of the merged output in seconds.
	Duration float64 `json:"duration"`
	// Codec is the detected output codec (e.g. "h264", "hevc") from ffprobe.
	Codec string `json:"codec,omitempty"`
}

// TimelapseMerger is the interface for merging timelapse frame sequences into a single output file.
type TimelapseMerger interface {
	// CanMerge reports whether this merge tier is available (e.g., binary present, codec supported).
	CanMerge() bool
	// Merge performs the merge of frame files from framesDir into outputPath at the given fps.
	Merge(ctx context.Context, framesDir, outputPath string, fps int) (*MergeResult, error)
	// Tier returns the merge tier identifier.
	Tier() MergeTier
}

// AutoDetectMerger wraps multiple mergers and picks the right one based on
// frame file types in the directory. It checks for .h265 files first
// (uses H265GoMerger), then .h264 files (uses H264GoMerger), then falls back
// to GoMerger for JPEG frames.
type AutoDetectMerger struct {
	jpegMerger *GoMerger
	h264Merger *H264GoMerger
	h265Merger *H265GoMerger
}

// NewAutoDetectMerger creates a merger that auto-detects frame types.
func NewAutoDetectMerger() *AutoDetectMerger {
	return &AutoDetectMerger{
		jpegMerger: NewGoMerger(),
		h264Merger: &H264GoMerger{},
		h265Merger: &H265GoMerger{},
	}
}

func (m *AutoDetectMerger) CanMerge() bool  { return true }
func (m *AutoDetectMerger) Tier() MergeTier { return TierGo }

func (m *AutoDetectMerger) Merge(ctx context.Context, framesDir, outputPath string, fps int) (*MergeResult, error) {
	if hasH265Frames(framesDir) {
		return m.h265Merger.Merge(ctx, framesDir, outputPath, fps)
	}
	if hasH264Frames(framesDir) {
		return m.h264Merger.Merge(ctx, framesDir, outputPath, fps)
	}
	return m.jpegMerger.Merge(ctx, framesDir, outputPath, fps)
}

// hasH264Frames checks if a directory contains .h264 frame files.
func hasH264Frames(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := strings.ToLower(e.Name())
		if strings.HasSuffix(name, ".h264") {
			return true
		}
	}
	return false
}

// hasH265Frames checks if a directory contains .h265 frame files.
func hasH265Frames(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := strings.ToLower(e.Name())
		if strings.HasSuffix(name, ".h265") {
			return true
		}
	}
	return false
}
