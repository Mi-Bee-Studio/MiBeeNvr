package transcoding

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
	"syscall"
)

// TranscodeEngine handles a single transcode job by wrapping FFmpeg execution.
// It provides disk space checking, progress parsing via stderr, and context-based cancellation.
type TranscodeEngine struct {
	ffmpegPath  string
	ffprobePath string
}

// NewTranscodeEngine creates a new engine with the given FFmpeg and ffprobe binary paths.
func NewTranscodeEngine(ffmpegPath, ffprobePath string) *TranscodeEngine {
	return &TranscodeEngine{
		ffmpegPath:  ffmpegPath,
		ffprobePath: ffprobePath,
	}
}

// Transcode executes a single transcode job from input to output.
//
// Supported conversions: H.264→H.265, H.265→H.264, MJPEG→H.264.
// H.265→H.264 uses software decode (libx264) as RPi 3B lacks hardware HEVC decoder.
//
// Steps:
//  1. Build FFmpeg command from opts + caps.
//  2. Check available disk space (2x input file size safety margin).
//  3. Run FFmpeg with progress parsing via stderr.
//  4. Validate output with ffprobe.
//
// On context cancellation the FFmpeg process group is killed and the partial
// output file is removed.
//
// progressCB is called with monotonically increasing values in [0, 1].
// It may be nil if progress tracking is not needed.
func (e *TranscodeEngine) Transcode(
	ctx context.Context,
	opts TranscodeOptions,
	caps HardwareCapabilities,
	progressCB func(float64),
) error {
	// 1. Build FFmpeg command.
	args, err := BuildFFmpegCommand(opts, caps)
	if err != nil {
		return fmt.Errorf("build command: %w", err)
	}

	// 2. Check available disk space.
	if err := checkDiskSpace(opts.OutputPath, opts.InputPath); err != nil {
		return fmt.Errorf("disk space: %w", err)
	}

	// 3. Execute FFmpeg.
	cmd := exec.CommandContext(ctx, e.ffmpegPath, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("stderr pipe: %w", err)
	}

	// Get input duration for progress calculation.
	totalSeconds := 0.0
	if info, probeErr := GetMediaInfo(e.ffprobePath, opts.InputPath); probeErr == nil && info != nil {
		totalSeconds = info.Duration
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start ffmpeg: %w", err)
	}

	// Parse stderr for progress in background goroutine.
	progressDone := make(chan struct{})
	go func() {
		defer close(progressDone)
		parseProgress(stderr, totalSeconds, progressCB)
	}()

	// Wait for FFmpeg to finish.
	waitErr := cmd.Wait()
	<-progressDone

	if waitErr != nil {
		// Context was cancelled — kill process group and clean up.
		if ctx.Err() != nil {
			killProcessGroup(cmd)
			os.Remove(opts.OutputPath)
			return ctx.Err()
		}
		return fmt.Errorf("ffmpeg failed: %w", waitErr)
	}

	// 4. Validate output.
	if err := ValidateOutput(e.ffprobePath, opts.OutputPath); err != nil {
		os.Remove(opts.OutputPath)
		return fmt.Errorf("output validation failed: %w", err)
	}

	return nil
}

// --- Disk space ---

// checkDiskSpace verifies that the filesystem containing outputPath has at least
// 2x the input file size free. The 2x multiplier is a safety margin since the
// transcoded output may be larger than the input (especially H.264→H.265 at low CRF).
func checkDiskSpace(outputPath, inputPath string) error {
	inputSize, err := getFileSize(inputPath)
	if err != nil {
		// Input may not exist yet (e.g. MJPEG directory) — skip check.
		return nil
	}

	var stat syscall.Statfs_t

	// Resolve to an existing directory for Statfs — the output file may not exist yet.
	dir := outputPath
	for {
		fi, statErr := os.Stat(dir)
		if statErr == nil {
			if !fi.IsDir() {
				dir = filepath.Dir(dir)
				continue
			}
			break
		}
		if os.IsNotExist(statErr) {
			dir = filepath.Dir(dir)
			if dir == "/" || dir == "." {
				// Can't resolve — skip check.
				return nil
			}
			continue
		}
		return fmt.Errorf("statfs %s: %w", dir, statErr)
	}
	if err := syscall.Statfs(dir, &stat); err != nil {
		return fmt.Errorf("statfs %s: %w", dir, err)
	}

	freeBytes := stat.Bavail * uint64(stat.Bsize)
	required := uint64(inputSize) * 2

	if freeBytes < required {
		return fmt.Errorf("insufficient disk space: need %d bytes, have %d bytes free", required, freeBytes)
	}

	return nil
}

// getFileSize returns the file size, or an error if the file cannot be stat'd.
func getFileSize(path string) (int64, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	return fi.Size(), nil
}

// --- Progress parsing ---

// progressRegex matches FFmpeg's standard stderr progress line:
//
//	frame=  120 fps= 25 q=28.0 size=    512kB time=00:00:04.80 bitrate= 872.1kbits/s speed=   1x
var progressRegex = regexp.MustCompile(`time=(\d+):(\d+):(\d+\.\d+)`)

// parseProgress reads FFmpeg stderr lines and calls cb with progress [0, 1].
// Values are clamped to [0, 1] and guaranteed monotonically increasing.
// If totalSeconds is 0 or cb is nil, parsing still consumes stderr but no callbacks are issued.
func parseProgress(stderr io.Reader, totalSeconds float64, cb func(float64)) {
	if cb == nil || totalSeconds <= 0 {
		// Drain stderr to prevent blocking.
		io.Copy(io.Discard, stderr)
		return
	}

	var lastProgress float64
	scanner := bufio.NewScanner(stderr)
	for scanner.Scan() {
		line := scanner.Text()
		matches := progressRegex.FindStringSubmatch(line)
		if len(matches) != 4 {
			continue
		}

		h, _ := strconv.ParseFloat(matches[1], 64)
		m, _ := strconv.ParseFloat(matches[2], 64)
		s, _ := strconv.ParseFloat(matches[3], 64)
		current := h*3600 + m*60 + s

		progress := current / totalSeconds
		if progress > 1.0 {
			progress = 1.0
		}

		// Guarantee monotonic increase.
		if progress > lastProgress {
			cb(progress)
			lastProgress = progress
		}
	}
}

// --- Process management ---

// killProcessGroup sends SIGKILL to the entire process group to ensure
// FFmpeg and any child processes are terminated.
func killProcessGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
		slog.Warn("failed to kill ffmpeg process group", "pid", cmd.Process.Pid, "error", err)
	}
}
