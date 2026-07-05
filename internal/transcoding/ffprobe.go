package transcoding

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os/exec"
	"strconv"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/mediaprobe"
)

// parseFloatFlexible parses a JSON number that may be a string or numeric value.
// ffprobe returns duration as string "29.860000" in some cases and as number 29.86 in others.
func parseFloatFlexible(raw json.RawMessage) (float64, error) {
	if len(raw) == 0 {
		return 0, nil
	}
	// Try numeric first (fast path)
	var f float64
	if err := json.Unmarshal(raw, &f); err == nil {
		return f, nil
	}
	// Try string
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return strconv.ParseFloat(s, 64)
	}
	return 0, fmt.Errorf("cannot parse %s as float", string(raw))
}

// GetMediaInfo extracts codec, duration, and resolution from a media file.
//
// It prefers the pure-Go mediaprobe path (no external process, reads only MP4
// box metadata) for MP4 files, and falls back to ffprobe when mediaprobe fails
// or the input is not MP4. This removes the hard ffprobe dependency for the
// common case while keeping ffprobe as a fallback for edge cases (non-MP4
// containers, corrupted moov, etc.).
//
// When ffprobePath is empty, ffprobe is looked up on PATH; if it is not
// available the ffprobe fallback is skipped and only the mediaprobe result is
// returned (which may be an error for non-MP4 inputs).
func GetMediaInfo(ffprobePath, filePath string) (*MediaInfo, error) {
	// Fast path: pure-Go probe for MP4 files — avoids spawning ffprobe entirely.
	if mediaprobe.IsLikelyMP4(filePath) {
		if info, err := mediaprobe.ProbeMP4(filePath); err == nil {
			return &MediaInfo{
				CodecName: info.CodecName,
				Duration:  info.Duration,
				Width:     info.Width,
				Height:    info.Height,
			}, nil
		} else {
			slog.Debug("mediaprobe failed, falling back to ffprobe",
				"file", filePath, "error", err)
		}
	}

	// Fallback: ffprobe subprocess.
	return getMediaInfoFFprobe(ffprobePath, filePath)
}

// getMediaInfoFFprobe extracts media info via the ffprobe binary.
func getMediaInfoFFprobe(ffprobePath, filePath string) (*MediaInfo, error) {
	if ffprobePath == "" {
		ffprobePath = "ffprobe"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(
		ctx, ffprobePath,
		"-v", "error",
		"-select_streams", "v:0",
		"-show_entries", "stream=codec_name,duration,width,height",
		"-of", "json",
		filePath,
	)
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("ffprobe failed: %w", err)
	}

	var result struct {
		Streams []struct {
			CodecName string          `json:"codec_name"`
			Duration  json.RawMessage `json:"duration"`
			Width     int             `json:"width"`
			Height    int             `json:"height"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(output, &result); err != nil {
		return nil, fmt.Errorf("failed to parse ffprobe output: %w", err)
	}

	if len(result.Streams) == 0 {
		return nil, fmt.Errorf("no video stream found")
	}

	stream := result.Streams[0]
	duration, _ := parseFloatFlexible(stream.Duration)

	return &MediaInfo{
		CodecName: stream.CodecName,
		Duration:  duration,
		Width:     stream.Width,
		Height:    stream.Height,
	}, nil
}
