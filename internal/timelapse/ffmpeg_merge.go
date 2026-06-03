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
	"strings"
	"syscall"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/transcoding"
)

// FFmpegMerger implements TimelapseMerger using FFmpeg for JPEG→MP4 conversion.
type FFmpegMerger struct {
	caps *transcoding.HardwareCapabilities
}

// NewFFmpegMerger creates a new FFmpegMerger with the given hardware capabilities.
func NewFFmpegMerger(caps *transcoding.HardwareCapabilities) *FFmpegMerger {
	return &FFmpegMerger{caps: caps}
}

// CanMerge reports whether FFmpeg is available on this system.
func (m *FFmpegMerger) CanMerge() bool {
	return m.caps != nil && m.caps.FFmpegPath != ""
}

// Tier returns the merge tier identifier.
func (m *FFmpegMerger) Tier() MergeTier {
	return TierFFmpeg
}

// Merge performs the merge of JPEG frame files from framesDir into outputPath at the given fps.
// It builds an FFmpeg command, executes it with context-based cancellation, and returns
// the merge result with frame count and duration.
func (m *FFmpegMerger) Merge(ctx context.Context, framesDir, outputPath string, fps int) (*MergeResult, error) {
	if !m.CanMerge() {
		return &MergeResult{
			Tier:  TierFFmpeg,
			Error: "FFmpeg not available",
		}, fmt.Errorf("FFmpeg not available")
	}

	args := m.buildArgs(framesDir, outputPath, fps)

	slog.Debug("running ffmpeg merge",
		"path", m.caps.FFmpegPath,
		"args", args,
		"framesDir", framesDir,
		"outputPath", outputPath,
		"fps", fps,
	)

	cmd := exec.CommandContext(ctx, m.caps.FFmpegPath, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		return &MergeResult{
			Tier:  TierFFmpeg,
			Error: fmt.Sprintf("stderr pipe: %v", err),
		}, fmt.Errorf("stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return &MergeResult{
			Tier:  TierFFmpeg,
			Error: fmt.Sprintf("start ffmpeg: %v", err),
		}, fmt.Errorf("start ffmpeg: %w", err)
	}

	// Consume stderr to prevent blocking; capture last lines for error diagnostics.
	errOutput := consumeStderr(stderr)

	waitErr := cmd.Wait()

	if waitErr != nil {
		// Context was cancelled — kill process group and clean up partial output.
		if ctx.Err() != nil {
			killMergeProcess(cmd)
			os.Remove(outputPath)
			return &MergeResult{
				Tier:  TierFFmpeg,
				Error: ctx.Err().Error(),
			}, ctx.Err()
		}

		// FFmpeg failed — include stderr output in error message.
		errMsg := fmt.Sprintf("ffmpeg failed: %v", waitErr)
		if errOutput != "" {
			errMsg = fmt.Sprintf("%s\nstderr: %s", errMsg, errOutput)
		}
		return &MergeResult{
			Tier:  TierFFmpeg,
			Error: errMsg,
		}, fmt.Errorf("%s", errMsg)
	}

	// Count successfully merged frames.
	framesMerged := countFramesInDir(framesDir)

	return &MergeResult{
		Tier:         TierFFmpeg,
		OutputPath:   outputPath,
		FramesMerged: framesMerged,
		Duration:     float64(framesMerged) / float64(fps),
	}, nil
}

// buildArgs constructs the FFmpeg argument list for JPEG→MP4 merge.
func (m *FFmpegMerger) buildArgs(framesDir, outputPath string, fps int) []string {
	var args []string

	// Input: image sequence with framerate.
	args = append(args, "-framerate", fmt.Sprintf("%d", fps))
	args = append(args, "-i", filepath.Join(framesDir, "frame_%06d.jpg"))

	// Encoder selection — prefer hardware when available.
	encoder := selectMergeEncoder(m.caps)
	args = append(args, "-c:v", encoder)

	// Pixel format for maximum compatibility.
	args = append(args, "-pix_fmt", "yuv420p")

	// Encoder-specific flags.
	switch {
	case strings.Contains(encoder, "v4l2m2m"):
		// V4L2 M2M requires explicit GOP and no B-frames.
		args = append(args, "-g", "50", "-bf", "0")
	case strings.Contains(encoder, "vaapi"):
		// VAAPI needs hwaccel init flags.
		args = append(args, "-hwaccel", "vaapi", "-hwaccel_output_format", "vaapi")
	case encoder == "libx264":
		args = append(args, "-preset", "fast", "-crf", "23")
	}

	// Overwrite output without asking.
	args = append(args, "-y", outputPath)

	return args
}

// selectMergeEncoder picks the best available H.264 encoder for JPEG→MP4 merge.
// Unlike the transcoding subsystem (which forces software for MJPEG input because
// v4l2m2m can hang on MJPEG stream decode), the image2 demuxer decodes JPEG frames
// individually in software, so hardware encoders work correctly here.
func selectMergeEncoder(caps *transcoding.HardwareCapabilities) string {
	if caps != nil && caps.H264EncoderType != transcoding.EncoderSoftware && caps.H264Encoder != "" {
		return caps.H264Encoder
	}
	return "libx264"
}

// mergeProgressRegex matches FFmpeg's standard stderr progress line.
var mergeProgressRegex = regexp.MustCompile(`time=(\d+):(\d+):(\d+\.\d+)`)

// consumeStderr reads FFmpeg stderr and returns the last meaningful output for diagnostics.
// It fully consumes stderr to prevent the FFmpeg process from blocking on stderr writes.
func consumeStderr(stderr io.Reader) string {
	var lastLines []string
	scanner := bufio.NewScanner(stderr)
	for scanner.Scan() {
		line := scanner.Text()
		lastLines = append(lastLines, line)
		if len(lastLines) > 10 {
			lastLines = lastLines[1:]
		}
	}
	return strings.Join(lastLines, "\n")
}

// killMergeProcess sends SIGKILL to the entire process group to ensure
// FFmpeg and any child processes are terminated.
func killMergeProcess(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
		slog.Warn("failed to kill ffmpeg merge process group", "pid", cmd.Process.Pid, "error", err)
	}
}

// countFramesInDir counts JPEG frame files in the given directory.
func countFramesInDir(dir string) int {
	matches, err := filepath.Glob(filepath.Join(dir, "frame_*.jpg"))
	if err != nil {
		return 0
	}
	return len(matches)
}