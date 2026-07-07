package timelapse

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/transcoding"
)

func TestFFmpegMerge_CanMerge(t *testing.T) {
	t.Run("FFmpeg available", func(t *testing.T) {
		m := NewFFmpegMerger(&transcoding.HardwareCapabilities{
			FFmpegPath: "/usr/bin/ffmpeg",
		}, nil)
		if !m.CanMerge() {
			t.Error("expected CanMerge() == true with FFmpegPath set")
		}
	})

	t.Run("FFmpeg not available", func(t *testing.T) {
		m := NewFFmpegMerger(&transcoding.HardwareCapabilities{
			FFmpegPath: "",
		}, nil)
		if m.CanMerge() {
			t.Error("expected CanMerge() == false with empty FFmpegPath")
		}
	})

	t.Run("nil caps", func(t *testing.T) {
		m := NewFFmpegMerger(nil, nil)
		if m.CanMerge() {
			t.Error("expected CanMerge() == false with nil caps")
		}
	})
}

func TestFFmpegMerge_Tier(t *testing.T) {
	m := NewFFmpegMerger(nil, nil)
	if m.Tier() != TierFFmpeg {
		t.Errorf("expected TierFFmpeg, got %q", m.Tier())
	}
}

func TestFFmpegMerge_Command(t *testing.T) {
	m := NewFFmpegMerger(&transcoding.HardwareCapabilities{
		FFmpegPath: "/usr/bin/ffmpeg",
	}, nil)

	args := m.buildArgs("/tmp/frames", "/tmp/output.mp4", 10, "libx264")
	cmdStr := strings.Join(args, " ")

	expectedParts := []string{
		"-framerate", "10",
		"-i", "/tmp/frames/frame_%06d.jpg",
		"-c:v", "libx264",
		"-pix_fmt", "yuv420p",
		"-preset", "fast",
		"-crf", "23",
		"-y",
		"/tmp/output.mp4",
	}

	for _, part := range expectedParts {
		if !strings.Contains(cmdStr, part) {
			t.Errorf("expected command to contain %q, got: %s", part, cmdStr)
		}
	}
}

func TestFFmpegMerge_Command_HardwareEncoder(t *testing.T) {
	t.Run("v4l2m2m encoder", func(t *testing.T) {
		m := NewFFmpegMerger(&transcoding.HardwareCapabilities{
			FFmpegPath:      "/usr/bin/ffmpeg",
			H264Encoder:     "h264_v4l2m2m",
			H264EncoderType: transcoding.EncoderV4L2M2M,
		}, nil)

		args := m.buildArgs("/tmp/frames", "/tmp/output.mp4", 10, "h264_v4l2m2m")
		cmdStr := strings.Join(args, " ")

		if !strings.Contains(cmdStr, "h264_v4l2m2m") {
			t.Errorf("expected v4l2m2m encoder, got: %s", cmdStr)
		}
		if !strings.Contains(cmdStr, "-g 50") {
			t.Errorf("expected GOP size flag, got: %s", cmdStr)
		}
		if !strings.Contains(cmdStr, "-bf 0") {
			t.Errorf("expected no B-frames flag, got: %s", cmdStr)
		}
	})

	t.Run("vaapi encoder", func(t *testing.T) {
		m := NewFFmpegMerger(&transcoding.HardwareCapabilities{
			FFmpegPath:      "/usr/bin/ffmpeg",
			H264Encoder:     "h264_vaapi",
			H264EncoderType: transcoding.EncoderVAAPI,
		}, nil)

		args := m.buildArgs("/tmp/frames", "/tmp/output.mp4", 10, "h264_vaapi")
		cmdStr := strings.Join(args, " ")

		if !strings.Contains(cmdStr, "h264_vaapi") {
			t.Errorf("expected vaapi encoder, got: %s", cmdStr)
		}
		if !strings.Contains(cmdStr, "-hwaccel vaapi") {
			t.Errorf("expected vaapi hwaccel flags, got: %s", cmdStr)
		}
	})

	t.Run("software fallback when no hardware encoder", func(t *testing.T) {
		m := NewFFmpegMerger(&transcoding.HardwareCapabilities{
			FFmpegPath:      "/usr/bin/ffmpeg",
			H264Encoder:     "libx264",
			H264EncoderType: transcoding.EncoderSoftware,
		}, nil)

		args := m.buildArgs("/tmp/frames", "/tmp/output.mp4", 10, "libx264")
		cmdStr := strings.Join(args, " ")

		if !strings.Contains(cmdStr, "libx264") {
			t.Errorf("expected libx264 fallback, got: %s", cmdStr)
		}
	})
}

func TestFFmpegMerge_Command_CRFConfig(t *testing.T) {
	m := NewFFmpegMerger(&transcoding.HardwareCapabilities{
		FFmpegPath: "/usr/bin/ffmpeg",
	}, &MergeConfig{CRF: 28})

	args := m.buildArgs("/tmp/frames", "/tmp/output.mp4", 10, "libx264")
	cmdStr := strings.Join(args, " ")

	if !strings.Contains(cmdStr, "-crf 28") {
		t.Errorf("expected CRF 28 from config, got: %s", cmdStr)
	}
	if !strings.Contains(cmdStr, "-preset fast") {
		t.Errorf("expected preset fast, got: %s", cmdStr)
	}
}

func TestFFmpegMerge_Command_BitrateConfig(t *testing.T) {
	t.Run("bitrate overrides CRF", func(t *testing.T) {
		m := NewFFmpegMerger(&transcoding.HardwareCapabilities{
			FFmpegPath: "/usr/bin/ffmpeg",
		}, &MergeConfig{CRF: 28, Bitrate: "2M"})

		args := m.buildArgs("/tmp/frames", "/tmp/output.mp4", 10, "libx264")
		cmdStr := strings.Join(args, " ")

		if !strings.Contains(cmdStr, "-b:v 2M") {
			t.Errorf("expected bitrate flag, got: %s", cmdStr)
		}
		if strings.Contains(cmdStr, "-crf") {
			t.Errorf("expected CRF to be omitted when bitrate is set, got: %s", cmdStr)
		}
	})

	t.Run("bitrate with hw encoder ignores bitrate", func(t *testing.T) {
		m := NewFFmpegMerger(&transcoding.HardwareCapabilities{
			FFmpegPath:      "/usr/bin/ffmpeg",
			H264Encoder:     "h264_v4l2m2m",
			H264EncoderType: transcoding.EncoderV4L2M2M,
		}, &MergeConfig{Bitrate: "2M"})

		args := m.buildArgs("/tmp/frames", "/tmp/output.mp4", 10, "h264_v4l2m2m")
		cmdStr := strings.Join(args, " ")

		// V4L2 should not get bitrate or crf flags
		if strings.Contains(cmdStr, "-b:v") {
			t.Errorf("expected no bitrate flag for v4l2m2m, got: %s", cmdStr)
		}
	})
}

func TestFFmpegMerge_SelectEncoder(t *testing.T) {
	t.Run("hardware encoder preferred", func(t *testing.T) {
		caps := &transcoding.HardwareCapabilities{
			H264Encoder:     "h264_v4l2m2m",
			H264EncoderType: transcoding.EncoderV4L2M2M,
		}
		enc := selectMergeEncoder(caps)
		if enc != "h264_v4l2m2m" {
			t.Errorf("expected h264_v4l2m2m, got %s", enc)
		}
	})

	t.Run("software fallback", func(t *testing.T) {
		caps := &transcoding.HardwareCapabilities{
			H264Encoder:     "libx264",
			H264EncoderType: transcoding.EncoderSoftware,
		}
		enc := selectMergeEncoder(caps)
		if enc != "libx264" {
			t.Errorf("expected libx264, got %s", enc)
		}
	})

	t.Run("nil caps fallback", func(t *testing.T) {
		enc := selectMergeEncoder(nil)
		if enc != "libx264" {
			t.Errorf("expected libx264, got %s", enc)
		}
	})
}

func TestFFmpegMerge_EncoderFallbackChain(t *testing.T) {
	t.Run("hardware encoder first, libx264 second", func(t *testing.T) {
		m := NewFFmpegMerger(&transcoding.HardwareCapabilities{
			H264Encoder:     "h264_v4l2m2m",
			H264EncoderType: transcoding.EncoderV4L2M2M,
		}, nil)
		chain := m.encoderFallbackChain()
		expected := []string{"h264_v4l2m2m", "libx264"}
		if len(chain) != len(expected) {
			t.Fatalf("expected %d encoders, got %d: %v", len(expected), len(chain), chain)
		}
		for i, enc := range expected {
			if chain[i] != enc {
				t.Errorf("chain[%d] = %q, want %q", i, chain[i], enc)
			}
		}
	})

	t.Run("libx264 only when no hardware", func(t *testing.T) {
		m := NewFFmpegMerger(&transcoding.HardwareCapabilities{
			H264Encoder:     "libx264",
			H264EncoderType: transcoding.EncoderSoftware,
		}, nil)
		chain := m.encoderFallbackChain()
		if len(chain) != 1 || chain[0] != "libx264" {
			t.Errorf("expected single libx264, got: %v", chain)
		}
	})

	t.Run("nil caps returns single libx264", func(t *testing.T) {
		m := NewFFmpegMerger(nil, nil)
		chain := m.encoderFallbackChain()
		if len(chain) != 1 || chain[0] != "libx264" {
			t.Errorf("expected single libx264, got: %v", chain)
		}
	})
}

func TestFFmpegMerge_CodecDetection(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a fake ffprobe that returns known JSON output.
	ffprobePath := filepath.Join(tmpDir, "ffprobe")
	ffprobeScript := `#!/bin/sh
# Mock ffprobe that returns h264 codec for any input
cat << 'FFPROBE_EOF'
{"streams":[{"codec_name":"h264","duration":"10.000000","width":640,"height":480}]}
FFPROBE_EOF
`
	if err := os.WriteFile(ffprobePath, []byte(ffprobeScript), 0o755); err != nil {
		t.Fatal(err)
	}

	// Override PATH so the fake ffprobe is found.
	t.Setenv("PATH", tmpDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	// Create a fake ffmpeg that creates a valid output file.
	ffmpegPath := filepath.Join(tmpDir, "ffmpeg")
	ffmpegScript := `#!/bin/sh
# Mock FFmpeg: create minimal output file
touch "$(echo "$@" | grep -oE '/[^ ]+\.mp4' | tail -1)"
`
	if err := os.WriteFile(ffmpegPath, []byte(ffmpegScript), 0o755); err != nil {
		t.Fatal(err)
	}

	m := NewFFmpegMerger(&transcoding.HardwareCapabilities{
		FFmpegPath: ffmpegPath,
	}, nil)

	framesDir := filepath.Join(tmpDir, "frames")
	if err := os.MkdirAll(framesDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Create minimal frame files.
	for i := 1; i <= 3; i++ {
		fname := filepath.Join(framesDir, fmt.Sprintf("frame_%06d.jpg", i))
		if err := os.WriteFile(fname, []byte("fake-jpeg"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	outputPath := filepath.Join(tmpDir, "merged.mp4")
	ctx := context.Background()
	result, err := m.Merge(ctx, framesDir, outputPath, 10)
	if err != nil {
		t.Fatalf("Merge failed: %v", err)
	}

	if result.Codec != "h264" {
		t.Errorf("expected codec 'h264', got %q", result.Codec)
	}
	if result.Tier != TierFFmpeg {
		t.Errorf("expected TierFFmpeg, got %q", result.Tier)
	}
	if result.FramesMerged != 3 {
		t.Errorf("expected 3 frames, got %d", result.FramesMerged)
	}
}

func TestFFmpegMerge_CodecDetection_FallbackToEmpty(t *testing.T) {
	// When ffprobe is not available, codec should be empty but merge still succeeds.
	tmpDir := t.TempDir()

	// Create a fake ffmpeg only (no ffprobe).
	ffmpegPath := filepath.Join(tmpDir, "ffmpeg")
	ffmpegScript := `#!/bin/sh
touch "$(echo "$@" | grep -oE '/[^ ]+\.mp4' | tail -1)"
`
	if err := os.WriteFile(ffmpegPath, []byte(ffmpegScript), 0o755); err != nil {
		t.Fatal(err)
	}

	m := NewFFmpegMerger(&transcoding.HardwareCapabilities{
		FFmpegPath: ffmpegPath,
	}, nil)

	framesDir := filepath.Join(tmpDir, "frames")
	if err := os.MkdirAll(framesDir, 0o755); err != nil {
		t.Fatal(err)
	}

	for i := 1; i <= 2; i++ {
		fname := filepath.Join(framesDir, fmt.Sprintf("frame_%06d.jpg", i))
		if err := os.WriteFile(fname, []byte("fake-jpeg"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	outputPath := filepath.Join(tmpDir, "merged.mp4")
	ctx := context.Background()
	result, err := m.Merge(ctx, framesDir, outputPath, 10)
	if err != nil {
		t.Fatalf("Merge failed: %v", err)
	}

	if result.Codec != "" {
		t.Errorf("expected empty codec when ffprobe unavailable, got %q", result.Codec)
	}
	if result.FramesMerged != 2 {
		t.Errorf("expected 2 frames, got %d", result.FramesMerged)
	}
}

func TestFFmpegMerge_Fallback_ToSoftware(t *testing.T) {
	// Simulate hardware encoder failure with fallback to libx264.
	tmpDir := t.TempDir()

	// Create a fake ffprobe for codec detection.
	ffprobePath := filepath.Join(tmpDir, "ffprobe")
	ffprobeScript := `#!/bin/sh
cat << 'FFPROBE_EOF'
{"streams":[{"codec_name":"h264","duration":"10.000000","width":640,"height":480}]}
FFPROBE_EOF
`
	if err := os.WriteFile(ffprobePath, []byte(ffprobeScript), 0o755); err != nil {
		t.Fatal(err)
	}

	// Create a fake ffmpeg that fails for v4l2m2m but succeeds for libx264.
	ffmpegPath := filepath.Join(tmpDir, "ffmpeg")
	ffmpegScript := `#!/bin/sh
# Fail if h264_v4l2m2m is requested, succeed otherwise
if echo "$@" | grep -q "h264_v4l2m2m"; then
  echo "Hardware encoder failed" >&2
  exit 1
fi
touch "$(echo "$@" | grep -oE '/[^ ]+\.mp4' | tail -1)"
`
	if err := os.WriteFile(ffmpegPath, []byte(ffmpegScript), 0o755); err != nil {
		t.Fatal(err)
	}

	t.Setenv("PATH", tmpDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	m := NewFFmpegMerger(&transcoding.HardwareCapabilities{
		FFmpegPath:      ffmpegPath,
		H264Encoder:     "h264_v4l2m2m",
		H264EncoderType: transcoding.EncoderV4L2M2M,
	}, nil)

	chain := m.encoderFallbackChain()
	if len(chain) != 2 || chain[0] != "h264_v4l2m2m" || chain[1] != "libx264" {
		t.Fatalf("expected fallback chain [h264_v4l2m2m, libx264], got: %v", chain)
	}

	framesDir := filepath.Join(tmpDir, "frames")
	if err := os.MkdirAll(framesDir, 0o755); err != nil {
		t.Fatal(err)
	}

	for i := 1; i <= 3; i++ {
		fname := filepath.Join(framesDir, fmt.Sprintf("frame_%06d.jpg", i))
		if err := os.WriteFile(fname, []byte("fake-jpeg"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	outputPath := filepath.Join(tmpDir, "merged.mp4")
	ctx := context.Background()
	result, err := m.Merge(ctx, framesDir, outputPath, 10)
	if err != nil {
		t.Fatalf("Merge failed after fallback: %v", err)
	}

	if result.Tier != TierFFmpeg {
		t.Errorf("expected TierFFmpeg, got %q", result.Tier)
	}
	if result.FramesMerged != 3 {
		t.Errorf("expected 3 frames, got %d", result.FramesMerged)
	}
}

func TestFFmpegMerge_Fallback_BaseCase(t *testing.T) {
	// When preferred encoder is already libx264, no fallback is attempted.
	tmpDir := t.TempDir()

	ffmpegPath := filepath.Join(tmpDir, "ffmpeg")
	ffmpegScript := `#!/bin/sh
exit 1
`
	if err := os.WriteFile(ffmpegPath, []byte(ffmpegScript), 0o755); err != nil {
		t.Fatal(err)
	}

	m := NewFFmpegMerger(&transcoding.HardwareCapabilities{
		FFmpegPath:      ffmpegPath,
		H264Encoder:     "libx264",
		H264EncoderType: transcoding.EncoderSoftware,
	}, nil)

	chain := m.encoderFallbackChain()
	if len(chain) != 1 || chain[0] != "libx264" {
		t.Fatalf("expected single libx264, got: %v", chain)
	}

	framesDir := filepath.Join(tmpDir, "frames")
	if err := os.MkdirAll(framesDir, 0o755); err != nil {
		t.Fatal(err)
	}

	for i := 1; i <= 2; i++ {
		fname := filepath.Join(framesDir, fmt.Sprintf("frame_%06d.jpg", i))
		if err := os.WriteFile(fname, []byte("fake-jpeg"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	outputPath := filepath.Join(tmpDir, "merged.mp4")
	ctx := context.Background()
	_, err := m.Merge(ctx, framesDir, outputPath, 10)
	if err == nil {
		t.Fatal("expected error when ffmpeg always fails")
	}
}

func TestFFmpegMerge_Cancel(t *testing.T) {
	tmpDir := t.TempDir()
	ffmpegPath := filepath.Join(tmpDir, "ffmpeg")
	script := `#!/bin/sh
# Simulate FFmpeg: sleep long enough to test cancellation
sleep 10
`
	if err := os.WriteFile(ffmpegPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	m := NewFFmpegMerger(&transcoding.HardwareCapabilities{
		FFmpegPath: ffmpegPath,
	}, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	framesDir := filepath.Join(tmpDir, "frames")
	if err := os.MkdirAll(framesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	outputPath := filepath.Join(tmpDir, "output.mp4")

	_, err := m.Merge(ctx, framesDir, outputPath, 10)
	if err == nil {
		t.Fatal("expected error from cancelled context, got nil")
	}
	if !strings.Contains(err.Error(), "context deadline exceeded") &&
		!strings.Contains(err.Error(), "canceled") &&
		!strings.Contains(err.Error(), "killed") {
		t.Errorf("expected context/cancellation error, got: %v", err)
	}

	// Verify partial output is cleaned up.
	if _, err := os.Stat(outputPath); err == nil {
		t.Error("expected partial output file to be removed after cancellation")
	}
}

func TestFFmpegMerge_CountFrames(t *testing.T) {
	tmpDir := t.TempDir()

	for i := 1; i <= 5; i++ {
		fname := filepath.Join(tmpDir, fmt.Sprintf("frame_%06d.jpg", i))
		if err := os.WriteFile(fname, []byte("fake-jpeg"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	count := countFramesInDir(tmpDir)
	if count != 5 {
		t.Errorf("expected 5 frames, got %d", count)
	}
}

func TestFFmpegMerge_Integration(t *testing.T) {
	caps := transcoding.ProbeHardwareCapabilities("")
	if caps.FFmpegPath == "" {
		t.Skip("FFmpeg not available — skipping integration test")
	}

	m := NewFFmpegMerger(caps, nil)

	if !m.CanMerge() {
		t.Fatal("expected CanMerge() == true with real FFmpeg")
	}

	tmpDir := t.TempDir()
	framesDir := filepath.Join(tmpDir, "frames")
	if err := os.MkdirAll(framesDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Create a minimal valid JPEG (1x1 pixel) for each frame.
	jpegData := []byte{
		0xFF, 0xD8, // SOI
		0xFF, 0xE0, // APP0
		0x00, 0x10, // length
		0x4A, 0x46, 0x49, 0x46, 0x00, // "JFIF\0"
		0x01, 0x01, // version
		0x00,       // units
		0x00, 0x01, // X density
		0x00, 0x01, // Y density
		0x00, 0x00, // thumbnail
		0xFF, 0xDB, // DQT
		0x00, 0x43, // length
		0x00, // precision 0, table 0
		0x08, 0x06, 0x06, 0x07, 0x06, 0x05, 0x08, 0x07,
		0x07, 0x07, 0x09, 0x09, 0x08, 0x0A, 0x0C, 0x14,
		0x0D, 0x0C, 0x0B, 0x0B, 0x0C, 0x19, 0x12, 0x13,
		0x0F, 0x14, 0x1D, 0x1A, 0x1F, 0x1E, 0x1D, 0x1A,
		0x1C, 0x1C, 0x20, 0x24, 0x2E, 0x27, 0x20, 0x22,
		0x2C, 0x23, 0x1C, 0x1C, 0x28, 0x37, 0x29, 0x2C,
		0x30, 0x31, 0x34, 0x34, 0x34, 0x1F, 0x27, 0x39,
		0x3D, 0x38, 0x32, 0x3C, 0x2E, 0x33, 0x34, 0x32,
		0xFF, 0xC0, // SOF0
		0x00, 0x0B, // length
		0x01,       // precision
		0x00, 0x01, // height
		0x00, 0x01, // width
		0x01,             // number of components
		0x01, 0x11, 0x00, // component 1
		0xFF, 0xC4, // DHT
		0x00, 0x1F, // length
		0x00, // table class 0, table id 0
		0x00, 0x01, 0x05, 0x01, 0x01, 0x01, 0x01, 0x01,
		0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x00, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07,
		0x08, 0x09, 0x0A, 0x0B,
		0xFF, 0xDA, // SOS
		0x00, 0x08, // length
		0x01, 0x00, // component
		0x00,       // spectral selection
		0x3F,       // successive approximation
		0x00,       // fill bytes
		0x7F,       // entropy-coded data
		0xFF, 0xD9, // EOI
	}

	for i := 1; i <= 3; i++ {
		fname := filepath.Join(framesDir, fmt.Sprintf("frame_%06d.jpg", i))
		if err := os.WriteFile(fname, jpegData, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	outputPath := filepath.Join(tmpDir, "merged.mp4")

	ctx := context.Background()
	result, err := m.Merge(ctx, framesDir, outputPath, 10)
	if err != nil {
		t.Fatalf("Merge failed: %v", err)
	}

	if result.Tier != TierFFmpeg {
		t.Errorf("expected TierFFmpeg, got %q", result.Tier)
	}
	if result.OutputPath != outputPath {
		t.Errorf("expected output path %q, got %q", outputPath, result.OutputPath)
	}
	if result.FramesMerged != 3 {
		t.Errorf("expected 3 frames merged, got %d", result.FramesMerged)
	}
	if result.Duration != 0.3 {
		t.Errorf("expected 0.3s duration (3 frames at 10fps), got %f", result.Duration)
	}
	if result.Error != "" {
		t.Errorf("expected no error, got %q", result.Error)
	}

	// Verify output file exists and is non-empty.
	info, err := os.Stat(outputPath)
	if err != nil {
		t.Fatalf("output file not found: %v", err)
	}
	if info.Size() == 0 {
		t.Error("expected non-empty output file")
	}
}
