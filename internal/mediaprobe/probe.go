// Package mediaprobe provides pure-Go media file metadata probing.
//
// It is the ffprobe-free equivalent of internal/transcoding/ffprobe.go's
// GetMediaInfo: it reads MP4 box metadata (moov/mdhd/stsz/avcC/hvcC) and
// parses SPS for resolution — never decoding pixel data. This makes it
// dramatically faster than shelling out to ffprobe (no process spawn,
// no full-file scan for frame counting) and removes the ffmpeg binary
// dependency from non-transcoding code paths.
//
// Callers that still want an ffprobe fallback (e.g. for non-MP4 inputs)
// should use ProbeMP4 first and fall back to ffprobe on error.
package mediaprobe

import (
	"fmt"
	"strings"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/merge"
)

// MediaInfo holds the metadata extracted from a media file.
// Field names mirror ffprobe's JSON output for drop-in compatibility with
// internal/transcoding.MediaInfo consumers.
type MediaInfo struct {
	CodecName  string  // ffprobe-compatible: "h264", "hevc", "mjpeg", ...
	Duration   float64 // seconds
	Width      int
	Height     int
	FrameCount int    // number of video samples (frames); ffprobe needs -count_frames to get this
	Codec      string // internal name: "h264" or "h265"
}

// ProbeMP4 reads an MP4 file and extracts codec, duration, resolution, and
// frame count using only box-structure parsing — no pixel decoding, no
// external processes. It is the pure-Go replacement for ffprobe on MP4 inputs.
//
// Returns an error if the file is not a parseable MP4 or has no video track.
func ProbeMP4(filePath string) (*MediaInfo, error) {
	seg, err := merge.ParseSegment(filePath)
	if err != nil {
		return nil, fmt.Errorf("mediaprobe: parse MP4: %w", err)
	}

	info := &MediaInfo{
		Duration:   seg.TotalDuration.Seconds(),
		FrameCount: seg.SampleCount,
		Codec:      seg.Codec,
	}

	// Map internal codec name to ffprobe-compatible codec_name.
	switch seg.Codec {
	case "h264":
		info.CodecName = "h264"
	case "h265":
		info.CodecName = "hevc"
	default:
		info.CodecName = seg.Codec
	}

	// Resolve resolution from SPS.
	if w, h, err := resolutionFromSPS(seg.Codec, seg.SPS); err == nil {
		info.Width = w
		info.Height = h
	} else {
		// SPS parse failure is non-fatal — some MJPEG-in-MP4 segments lack SPS.
		// Consumers that need resolution should fall back to ffprobe if Width==0.
		info.Width, info.Height = 0, 0
	}

	return info, nil
}

// ProbeDuration reads only the duration (in seconds) from an MP4 file.
// Cheaper than ProbeMP4 when only duration is needed, though ParseSegment
// already avoids reading mdat so the difference is negligible.
func ProbeDuration(filePath string) (float64, error) {
	seg, err := merge.ParseSegment(filePath)
	if err != nil {
		return 0, fmt.Errorf("mediaprobe: parse duration: %w", err)
	}
	return seg.TotalDuration.Seconds(), nil
}

// resolutionFromSPS dispatches to the codec-appropriate SPS parser.
func resolutionFromSPS(codec string, sps []byte) (width, height int, err error) {
	switch codec {
	case "h264":
		return merge.ParseSPSResolution(sps)
	case "h265":
		return merge.ParseHEVCSPSResolution(sps)
	default:
		return 0, 0, fmt.Errorf("mediaprobe: no SPS parser for codec %q", codec)
	}
}

// IsLikelyMP4 returns true if the path has an MP4-ish extension.
// Used by callers to decide whether to try ProbeMP4 before ffprobe.
func IsLikelyMP4(path string) bool {
	p := strings.ToLower(path)
	return strings.HasSuffix(p, ".mp4") ||
		strings.HasSuffix(p, ".m4v") ||
		strings.HasSuffix(p, ".mov") ||
		strings.HasSuffix(p, ".transcoded.mp4")
}

// FormatDuration mirrors time.Duration formatting for logging.
func (m *MediaInfo) FormatDuration() string {
	return time.Duration(m.Duration * float64(time.Second)).Round(time.Millisecond).String()
}
