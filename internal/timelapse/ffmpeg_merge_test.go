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
		})
		if !m.CanMerge() {
			t.Error("expected CanMerge() == true with FFmpegPath set")
		}
	})

	t.Run("FFmpeg not available", func(t *testing.T) {
		m := NewFFmpegMerger(&transcoding.HardwareCapabilities{
			FFmpegPath: "",
		})
		if m.CanMerge() {
			t.Error("expected CanMerge() == false with empty FFmpegPath")
		}
	})

	t.Run("nil caps", func(t *testing.T) {
		m := NewFFmpegMerger(nil)
		if m.CanMerge() {
			t.Error("expected CanMerge() == false with nil caps")
		}
	})
}

func TestFFmpegMerge_Tier(t *testing.T) {
	m := NewFFmpegMerger(nil)
	if m.Tier() != TierFFmpeg {
		t.Errorf("expected TierFFmpeg, got %q", m.Tier())
	}
}

func TestFFmpegMerge_Command(t *testing.T) {
	m := NewFFmpegMerger(&transcoding.HardwareCapabilities{
		FFmpegPath: "/usr/bin/ffmpeg",
	})

	args := m.buildArgs("/tmp/frames", "/tmp/output.mp4", 10)
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
		})

		args := m.buildArgs("/tmp/frames", "/tmp/output.mp4", 10)
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
		})

		args := m.buildArgs("/tmp/frames", "/tmp/output.mp4", 10)
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
		})

		args := m.buildArgs("/tmp/frames", "/tmp/output.mp4", 10)
		cmdStr := strings.Join(args, " ")

		if !strings.Contains(cmdStr, "libx264") {
			t.Errorf("expected libx264 fallback, got: %s", cmdStr)
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

func TestFFmpegMerge_Cancel(t *testing.T) {
	tmpDir := t.TempDir()
	ffmpegPath := filepath.Join(tmpDir, "ffmpeg")
	script := `#!/bin/sh
# Simulate FFmpeg: sleep long enough to test cancellation
sleep 10
`
	if err := os.WriteFile(ffmpegPath, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}

	m := NewFFmpegMerger(&transcoding.HardwareCapabilities{
		FFmpegPath: ffmpegPath,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	framesDir := filepath.Join(tmpDir, "frames")
	if err := os.MkdirAll(framesDir, 0755); err != nil {
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
		if err := os.WriteFile(fname, []byte("fake-jpeg"), 0644); err != nil {
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

	m := NewFFmpegMerger(caps)

	if !m.CanMerge() {
		t.Fatal("expected CanMerge() == true with real FFmpeg")
	}

	tmpDir := t.TempDir()
	framesDir := filepath.Join(tmpDir, "frames")
	if err := os.MkdirAll(framesDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create a minimal valid JPEG (1x1 pixel) for each frame.
	// Minimal JPEG EOI marker + SOI is enough for FFmpeg to process.
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
		0x00,                         // precision 0, table 0
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
		0x01,       // number of components
		0x01, 0x11, 0x00, // component 1
		0xFF, 0xC4, // DHT
		0x00, 0x1F, // length
		0x00,                         // table class 0, table id 0
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
		if err := os.WriteFile(fname, jpegData, 0644); err != nil {
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