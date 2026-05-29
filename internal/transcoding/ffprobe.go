package transcoding

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"time"
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

// ValidateOutput verifies a transcoded file is valid using ffprobe.
// It checks that the output file contains at least one video stream with a non-empty codec name.
func ValidateOutput(ffprobePath, outputPath string) error {
	if ffprobePath == "" {
		ffprobePath = "ffprobe"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, ffprobePath,
		"-v", "error",
		"-select_streams", "v:0",
		"-show_entries", "stream=codec_name,duration,width,height",
		"-of", "json",
		outputPath,
	)
	output, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("ffprobe validation failed: %w", err)
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
		return fmt.Errorf("failed to parse ffprobe output: %w", err)
	}

	if len(result.Streams) == 0 {
		return fmt.Errorf("no video stream found in output file")
	}

	stream := result.Streams[0]
	if stream.CodecName == "" {
		return fmt.Errorf("empty codec name in output file")
	}

	return nil
}

// GetMediaInfo extracts codec, duration, and resolution from a media file using ffprobe.
func GetMediaInfo(ffprobePath, filePath string) (*MediaInfo, error) {
	if ffprobePath == "" {
		ffprobePath = "ffprobe"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, ffprobePath,
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
