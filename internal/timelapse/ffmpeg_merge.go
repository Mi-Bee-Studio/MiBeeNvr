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
	"strings"
	"syscall"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/transcoding"
)

// FFmpegMerger implements TimelapseMerger using FFmpeg for JPEG→MP4 transcoding.
// It supports hardware-accelerated encoding with software libx264 fallback,
// configurable CRF/bitrate, and automatic codec detection via ffprobe.
type FFmpegMerger struct {
	caps   *transcoding.HardwareCapabilities
	config *MergeConfig
}

// Interface compliance check.
var _ TimelapseMerger = (*FFmpegMerger)(nil)

// NewFFmpegMerger creates a new FFmpegMerger with the given hardware capabilities
// and optional merge configuration.
func NewFFmpegMerger(caps *transcoding.HardwareCapabilities, config *MergeConfig) *FFmpegMerger {
	return &FFmpegMerger{caps: caps, config: config}
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
// It implements the fallback chain:
//  1. FFmpeg with hardware encoder (v4l2m2m/VAAPI)
//  2. FFmpeg with software libx264
//
// After a successful merge, it detects the output codec via ffprobe.
func (m *FFmpegMerger) Merge(ctx context.Context, framesDir, outputPath string, fps int) (*MergeResult, error) {
	if !m.CanMerge() {
		return &MergeResult{
			Tier:  TierFFmpeg,
			Error: "FFmpeg not available",
		}, fmt.Errorf("FFmpeg not available")
	}

	// Build the fallback chain: hardware encoder first, then software libx264.
	encoders := m.encoderFallbackChain()

	var lastErr error
	for _, encoder := range encoders {
		args := m.buildArgs(framesDir, outputPath, fps, encoder)

		slog.Debug(
			"running ffmpeg merge",
			"path", m.caps.FFmpegPath,
			"args", args,
			"framesDir", framesDir,
			"outputPath", outputPath,
			"fps", fps,
			"encoder", encoder,
		)

		err := m.runFFmpeg(ctx, args, outputPath)
		if err == nil {
			// Success — detect output codec.
			codec := m.detectCodec(outputPath)
			framesMerged := countFramesInDir(framesDir)

			slog.Debug(
				"ffmpeg merge completed",
				"encoder", encoder,
				"codec", codec,
				"frames", framesMerged,
				"path", outputPath,
			)

			return &MergeResult{
				Tier:         TierFFmpeg,
				OutputPath:   outputPath,
				FramesMerged: framesMerged,
				Duration:     float64(framesMerged) / float64(fps),
				Codec:        codec,
			}, nil
		}

		slog.Warn(
			"ffmpeg merge attempt failed, trying next encoder",
			"encoder", encoder,
			"error", err,
		)
		lastErr = err
	}

	// All encoders failed.
	return &MergeResult{
		Tier:  TierFFmpeg,
		Error: lastErr.Error(),
	}, lastErr
}

// encoderFallbackChain returns the ordered list of encoder names to try.
// If the preferred encoder is already libx264 (no hw available), returns single entry.
func (m *FFmpegMerger) encoderFallbackChain() []string {
	preferred := selectMergeEncoder(m.caps)
	if preferred == "libx264" {
		return []string{"libx264"}
	}
	return []string{preferred, "libx264"}
}

// runFFmpeg executes the FFmpeg command with the given args and returns an error on failure.
func (m *FFmpegMerger) runFFmpeg(ctx context.Context, args []string, outputPath string) error {
	cmd := exec.CommandContext(ctx, m.caps.FFmpegPath, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start ffmpeg: %w", err)
	}

	// Consume stderr to prevent blocking; capture last lines for error diagnostics.
	errOutput := consumeStderr(stderr)

	waitErr := cmd.Wait()
	if waitErr != nil {
		// Context was cancelled — kill process group and clean up partial output.
		if ctx.Err() != nil {
			killMergeProcess(cmd)
			os.Remove(outputPath)
			return ctx.Err()
		}

		// FFmpeg failed — include stderr output in error message.
		errMsg := fmt.Sprintf("ffmpeg failed: %v", waitErr)
		if errOutput != "" {
			errMsg = fmt.Sprintf("%s\nstderr: %s", errMsg, errOutput)
		}
		return fmt.Errorf("%s", errMsg)
	}

	return nil
}

// buildArgs constructs the FFmpeg argument list for JPEG→MP4 transcoding.
// It uses the provided encoder name and respects MergeConfig CRF/bitrate settings.
func (m *FFmpegMerger) buildArgs(framesDir, outputPath string, fps int, encoder string) []string {
	var args []string

	// Input: image sequence with framerate.
	args = append(args, "-framerate", fmt.Sprintf("%d", fps))
	args = append(args, "-i", filepath.Join(framesDir, "frame_%06d.jpg"))

	// Encoder selection.
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
		args = append(args, "-preset", "fast")
		// Use config bitrate if set (overrides CRF).
		if m.config != nil && m.config.Bitrate != "" {
			args = append(args, "-b:v", m.config.Bitrate)
		} else {
			crf := 23
			if m.config != nil && m.config.CRF > 0 {
				crf = m.config.CRF
			}
			args = append(args, "-crf", fmt.Sprintf("%d", crf))
		}
	}

	// Overwrite output without asking.
	args = append(args, "-y", outputPath)

	return args
}

// detectCodec runs ffprobe on the output file to detect the video codec.
// Returns empty string if detection fails.
func (m *FFmpegMerger) detectCodec(outputPath string) string {
	info, err := transcoding.GetMediaInfo("", outputPath)
	if err != nil {
		slog.Warn("failed to detect output codec", "path", outputPath, "error", err)
		return ""
	}
	return info.CodecName
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
