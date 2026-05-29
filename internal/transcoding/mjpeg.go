package transcoding

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// TranscodeMJPEG converts a directory of JPEG frames into a single H.264 MP4 file.
//
// The directory must contain JPEG files with timestamp-based names matching the
// format used by MJPEG recording: "20060102_150405.000.jpg".
//
// If fps is 0 or negative, the framerate is inferred from the timestamps encoded
// in the filenames. If inference fails (e.g. only one frame), a default of 10 FPS
// is used.
//
// After transcoding, the format changes from "mjpeg" (directory) to "h264" (file).
func TranscodeMJPEG(
	ctx context.Context,
	inputDir string,
	outputPath string,
	fps int,
	caps HardwareCapabilities,
	progressCB func(float64),
) error {
	// 1. Scan directory for JPEG files.
	files, err := scanJPEGFiles(inputDir)
	if err != nil {
		return fmt.Errorf("scan directory: %w", err)
	}
	if len(files) == 0 {
		return fmt.Errorf("no JPEG files found in %s", inputDir)
	}

	// 2. Infer framerate if not specified.
	if fps <= 0 {
		fps = inferFramerate(files)
		if fps <= 0 {
			fps = 10 // safe default
		}
	}

	// 3. Build options and execute via engine.
	opts := TranscodeOptions{
		InputPath:   inputDir,
		OutputPath:  outputPath,
		InputCodec:  "mjpeg",
		OutputCodec: "h264",
		Framerate:   fps,
	}

	engine := NewTranscodeEngine(caps.FFmpegPath, "")
	return engine.Transcode(ctx, opts, caps, progressCB)
}

// scanJPEGFiles returns a sorted list of .jpg file paths in the given directory.
// Files are sorted lexicographically by name, which matches chronological order
// for the timestamp-based filename format "20060102_150405.000.jpg".
func scanJPEGFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read directory %s: %w", dir, err)
	}

	var files []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasSuffix(strings.ToLower(name), ".jpg") {
			files = append(files, filepath.Join(dir, name))
		}
	}

	sort.Strings(files)
	return files, nil
}

// inferFramerate estimates the frame rate from the timestamp intervals encoded
// in JPEG filenames. The expected filename format is "20060102_150405.000.jpg".
//
// It parses timestamps from all filenames, computes the average interval between
// consecutive frames, and returns 1/avgInterval rounded to the nearest integer.
// Returns 0 if fewer than 2 frames or if timestamps cannot be parsed.
func inferFramerate(files []string) int {
	if len(files) < 2 {
		return 0
	}

	// Parse timestamps from filenames.
	timestamps := make([]time.Time, 0, len(files))
	for _, f := range files {
		ts, err := parseFrameTimestamp(filepath.Base(f))
		if err != nil {
			continue
		}
		timestamps = append(timestamps, ts)
	}

	if len(timestamps) < 2 {
		return 0
	}

	// Sort timestamps (they should already be sorted, but be safe).
	sort.Slice(timestamps, func(i, j int) bool {
		return timestamps[i].Before(timestamps[j])
	})

	// Compute total duration spanned and derive FPS.
	totalDuration := timestamps[len(timestamps)-1].Sub(timestamps[0])
	if totalDuration < time.Millisecond {
		return 0
	}

	// Number of intervals = number of timestamps - 1.
	intervals := len(timestamps) - 1
	avgInterval := totalDuration / time.Duration(intervals)
	if avgInterval < time.Millisecond {
		return 0
	}

	fps := int(time.Second / avgInterval)
	if fps < 1 {
		fps = 1
	}
	if fps > 120 {
		fps = 120
	}

	return fps
}

// parseFrameTimestamp extracts the timestamp from a JPEG frame filename.
// Expected format: "20060102_150405.000.jpg" or "2006-01-02_15.04.05.000.jpg".
// Returns an error if the filename cannot be parsed.
func parseFrameTimestamp(filename string) (time.Time, error) {
	// Strip .jpg extension.
	name := strings.TrimSuffix(filename, ".jpg")
	name = strings.TrimSuffix(name, ".JPEG")

	// Try format from storage/manager.go WriteFrame: "20060102_150405.000"
	if t, err := time.Parse("20060102_150405.000", name); err == nil {
		return t, nil
	}

	// Try format with nanosecond precision: "20060102_150405.000000000"
	if t, err := time.Parse("20060102_150405.000000000", name); err == nil {
		return t, nil
	}

	// Try alternative format with dashes/dots: "2006-01-02_15.04.05.000"
	if t, err := time.Parse("2006-01-02_15.04.05.000", name); err == nil {
		return t, nil
	}

	// Try without milliseconds: "20060102_150405"
	if t, err := time.Parse("20060102_150405", name); err == nil {
		return t, nil
	}

	// Fallback: try to extract a Unix timestamp from the filename.
	// Some naming schemes use numeric prefixes.
	parts := strings.SplitN(name, "_", 2)
	if len(parts) >= 1 {
		if unix, err := strconv.ParseInt(parts[0], 10, 64); err == nil && unix > 0 {
			return time.Unix(unix, 0), nil
		}
	}

	return time.Time{}, fmt.Errorf("cannot parse timestamp from %q", filename)
}
