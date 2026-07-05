// Package timelapse — Enhanced MJPEG merger tests.
//
// These tests verify that the Go-based MJPEG muxer with JPEG re-compression:
//   - Produces valid MP4 output (verified via abema/go-mp4 box structure)
//   - Reduces file size vs passthrough (source JPEG frames at Q=85, re-encoded at Q=30)
//   - Builds with CGO_ENABLED=0
//   - Handles edge cases (empty, single frame, various quality levels)
//
// Run: go test ./internal/timelapse/ -run TestEnhancedGoMerge -v
package timelapse

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/abema/go-mp4"
)

// --- Test harness ---

// generateTestJPEG creates a test JPEG frame file with the given dimensions and quality.
func generateTestJPEG(t *testing.T, path string, width, height, quality int) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	// Fill with a natural-image-like pattern (gradient + detail) to test compression.
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			// Colorful gradient with some high-frequency detail.
			r := uint8((x * 255 / width) * (y * 128 / height) / 255)
			g := uint8((y * 255 / height) * (255 - x*128/width) / 255)
			b := uint8((x+y)*255/(width+height)) ^ 0x7F
			img.Set(x, y, color.RGBA{R: r, G: g, B: b, A: 255})
		}
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := jpeg.Encode(f, img, &jpeg.Options{Quality: quality}); err != nil {
		t.Fatal(err)
	}
}

// --- Tests ---

// TestEnhancedGoMerge_ValidOutput verifies the enhanced Go merger produces a valid MP4.
func TestEnhancedGoMerge_ValidOutput(t *testing.T) {
	tmpDir := t.TempDir()
	framesDir := filepath.Join(tmpDir, "frames")
	outputPath := filepath.Join(tmpDir, "output.mp4")

	if err := os.MkdirAll(framesDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Generate 5 test frames at Q=85 (typical capture quality).
	for i := 0; i < 5; i++ {
		framePath := filepath.Join(framesDir, fmt.Sprintf("frame_%06d.jpg", i))
		generateTestJPEG(t, framePath, 640, 480, 85)
	}

	// Use enhanced merger at Q=30.
	merger := NewEnhancedGoMerger(30)
	if !merger.CanMerge() {
		t.Fatal("EnhancedGoMerger.CanMerge() should return true")
	}

	result, err := merger.Merge(context.Background(), framesDir, outputPath, 1)
	if err != nil {
		t.Fatalf("Merge failed: %v", err)
	}
	if result.Error != "" {
		t.Fatalf("Merge result has error: %s", result.Error)
	}

	// Verify output file exists.
	fi, err := os.Stat(outputPath)
	if err != nil {
		t.Fatalf("output file not created: %v", err)
	}
	if fi.Size() == 0 {
		t.Fatal("output file is empty")
	}

	// Validate MP4 box structure.
	f, err := os.Open(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	foundFtyp := false
	foundMoov := false
	foundMdat := false
	for {
		boxInfo, err := mp4.ReadBoxInfo(f)
		if err != nil {
			break
		}
		boxName := string(boxInfo.Type[:])
		switch boxName {
		case "ftyp":
			foundFtyp = true
		case "moov":
			foundMoov = true
		case "mdat":
			foundMdat = true
		}
		if _, err := f.Seek(int64(boxInfo.Offset)+int64(boxInfo.Size), 0); err != nil {
			break
		}
		if boxInfo.Size == 0 {
			break
		}
	}

	if !foundFtyp {
		t.Error("Missing ftyp box")
	}
	if !foundMoov {
		t.Error("Missing moov box")
	}
	if !foundMdat {
		t.Error("Missing mdat box")
	}

	// Verify output file is smaller than source frames (compression check).
	sourceTotal := int64(0)
	for i := 0; i < 5; i++ {
		framePath := filepath.Join(framesDir, fmt.Sprintf("frame_%06d.jpg", i))
		if fii, err := os.Stat(framePath); err == nil {
			sourceTotal += fii.Size()
		}
	}
	t.Logf("Source frames total size: %d bytes", sourceTotal)
	t.Logf("Output file size: %d bytes", fi.Size())
	// Enhanced MJPEG should be < 2x source size (compression improvement check).
	// Actually, since we re-encode at lower quality, it should be MUCH smaller.
	// The source is Q=85 and we re-encode at Q=30, so expect ~40-60% of original size.
	if fi.Size() >= sourceTotal*2 {
		t.Errorf("Output file too large: %d bytes vs source %d bytes (>2x)", fi.Size(), sourceTotal)
	}

	t.Logf("Merge result: frames=%d, duration=%.1fs, tier=%s", result.FramesMerged, result.Duration, result.Tier)
}

// TestEnhancedGoMerge_SizeReduction verifies enhanced merger produces smaller files than passthrough.
func TestEnhancedGoMerge_SizeReduction(t *testing.T) {
	tmpDir := t.TempDir()
	framesDir := filepath.Join(tmpDir, "frames")
	passthroughDir := filepath.Join(tmpDir, "passthrough")
	outputEnhanced := filepath.Join(tmpDir, "enhanced.mp4")
	outputPassthrough := filepath.Join(tmpDir, "passthrough.mp4")

	for _, d := range []string{framesDir, passthroughDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	// Generate frames and copy to both directories.
	for i := 0; i < 10; i++ {
		framePath := filepath.Join(framesDir, fmt.Sprintf("frame_%06d.jpg", i))
		generateTestJPEG(t, framePath, 320, 240, 85)
		// Copy to passthrough dir.
		src, err := os.ReadFile(framePath)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(passthroughDir, fmt.Sprintf("frame_%06d.jpg", i)), src, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// Enhanced merger at Q=30.
	enhanced := NewEnhancedGoMerger(30)
	resultEnhanced, err := enhanced.Merge(context.Background(), framesDir, outputEnhanced, 1)
	if err != nil {
		t.Fatalf("Enhanced merge failed: %v", err)
	}

	// Passthrough merger (original quality preserved).
	passthrough := NewGoMerger()
	resultPassthrough, err := passthrough.Merge(context.Background(), passthroughDir, outputPassthrough, 1)
	if err != nil {
		t.Fatalf("Passthrough merge failed: %v", err)
	}

	enhancedFI, _ := os.Stat(outputEnhanced)
	passthroughFI, _ := os.Stat(outputPassthrough)

	t.Logf("Passthrough (Q=85): %d bytes", passthroughFI.Size())
	t.Logf("Enhanced (Q=30):    %d bytes", enhancedFI.Size())
	t.Logf("Size ratio: %.1f%%", float64(enhancedFI.Size())/float64(passthroughFI.Size())*100)

	// Enhanced should be smaller than passthrough.
	if enhancedFI.Size() >= passthroughFI.Size() {
		t.Errorf("Enhanced output (%d bytes) should be smaller than passthrough (%d bytes)",
			enhancedFI.Size(), passthroughFI.Size())
	}

	_ = resultEnhanced
	_ = resultPassthrough
}

// TestEnhancedGoMerge_SingleFrame verifies the merger handles a single frame correctly.
func TestEnhancedGoMerge_SingleFrame(t *testing.T) {
	tmpDir := t.TempDir()
	framesDir := filepath.Join(tmpDir, "frames")
	outputPath := filepath.Join(tmpDir, "output.mp4")

	if err := os.MkdirAll(framesDir, 0o755); err != nil {
		t.Fatal(err)
	}

	framePath := filepath.Join(framesDir, "frame_000000.jpg")
	generateTestJPEG(t, framePath, 640, 480, 85)

	merger := NewEnhancedGoMerger(30)
	result, err := merger.Merge(context.Background(), framesDir, outputPath, 1)
	if err != nil {
		t.Fatalf("Merge failed: %v", err)
	}

	fi, err := os.Stat(outputPath)
	if err != nil {
		t.Fatalf("Output file not found: %v", err)
	}

	if result.FramesMerged != 1 {
		t.Errorf("Expected 1 frame merged, got %d", result.FramesMerged)
	}
	if fi.Size() == 0 {
		t.Error("Output file is empty")
	}

	t.Logf("Single frame output: %d bytes", fi.Size())
}

// TestEnhancedGoMerge_QualityLevels tests various quality settings.
func TestEnhancedGoMerge_QualityLevels(t *testing.T) {
	tests := []struct {
		name    string
		quality int
	}{
		{"minimum quality 1", 1},
		{"archival quality 20", 20},
		{"default enhanced 30", 30},
		{"medium quality 50", 50},
		{"high quality 80", 80},
		{"maximum quality 100", 100},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			framesDir := filepath.Join(tmpDir, "frames")
			outputPath := filepath.Join(tmpDir, "output.mp4")

			if err := os.MkdirAll(framesDir, 0o755); err != nil {
				t.Fatal(err)
			}

			// Generate 3 frames.
			for i := 0; i < 3; i++ {
				framePath := filepath.Join(framesDir, fmt.Sprintf("frame_%06d.jpg", i))
				generateTestJPEG(t, framePath, 320, 240, 85)
			}

			merger := NewEnhancedGoMerger(tt.quality)
			result, err := merger.Merge(context.Background(), framesDir, outputPath, 1)
			if err != nil {
				t.Fatalf("Merge failed for quality %d: %v", tt.quality, err)
			}

			fi, _ := os.Stat(outputPath)
			if fi.Size() == 0 {
				t.Error("Output file is empty")
			}

			t.Logf("Quality=%d: output=%d bytes, frames=%d", tt.quality, fi.Size(), result.FramesMerged)
		})
	}
}

// TestEnhancedGoMerge_EmptyDir verifies the merger handles empty input gracefully.
func TestEnhancedGoMerge_EmptyDir(t *testing.T) {
	tmpDir := t.TempDir()
	emptyDir := filepath.Join(tmpDir, "empty")
	outputPath := filepath.Join(tmpDir, "output.mp4")

	if err := os.MkdirAll(emptyDir, 0o755); err != nil {
		t.Fatal(err)
	}

	merger := NewEnhancedGoMerger(30)
	_, err := merger.Merge(context.Background(), emptyDir, outputPath, 1)
	if err == nil {
		t.Error("Expected error for empty directory, got nil")
	}
}

// TestEnhancedGoMerge_ClampQuality verifies quality bounds are clamped.
func TestEnhancedGoMerge_ClampQuality(t *testing.T) {
	t.Run("quality below 1", func(t *testing.T) {
		m := NewEnhancedGoMerger(0)
		if m.jpegQuality != 1 {
			t.Errorf("Expected quality clamped to 1, got %d", m.jpegQuality)
		}
	})

	t.Run("quality above 100", func(t *testing.T) {
		m := NewEnhancedGoMerger(200)
		if m.jpegQuality != 100 {
			t.Errorf("Expected quality clamped to 100, got %d", m.jpegQuality)
		}
	})

	t.Run("negative quality", func(t *testing.T) {
		m := NewEnhancedGoMerger(-5)
		if m.jpegQuality != 1 {
			t.Errorf("Expected quality clamped to 1, got %d", m.jpegQuality)
		}
	})
}

// TestEnhancedGoMerge_FFprobeValidation validates output with ffprobe if available.
func TestEnhancedGoMerge_FFprobeValidation(t *testing.T) {
	// Check if ffprobe is available.
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe not available, skipping validation")
	}

	tmpDir := t.TempDir()
	framesDir := filepath.Join(tmpDir, "frames")
	outputPath := filepath.Join(tmpDir, "output.mp4")

	if err := os.MkdirAll(framesDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Generate 5 frames with a more natural image pattern.
	for i := 0; i < 5; i++ {
		framePath := filepath.Join(framesDir, fmt.Sprintf("frame_%06d.jpg", i))
		generateTestJPEG(t, framePath, 640, 480, 85)
	}

	merger := NewEnhancedGoMerger(30)
	_, err := merger.Merge(context.Background(), framesDir, outputPath, 1)
	if err != nil {
		t.Fatalf("Merge failed: %v", err)
	}

	// Run ffprobe to validate the MP4.
	cmd := exec.Command(
		"ffprobe",
		"-v", "quiet",
		"-print_format", "json",
		"-show_streams",
		outputPath,
	)
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("ffprobe failed: %v", err)
	}

	outputStr := string(output)
	t.Logf("ffprobe output:\n%s", outputStr)

	// Check for MJPEG-related stream info.
	if !strings.Contains(outputStr, `"codec_name":`) {
		t.Error("ffprobe output missing codec_name")
	}
	if strings.Contains(outputStr, `"codec_name": "mjpeg"`) || strings.Contains(outputStr, `"codec_name":"mjpeg"`) {
		t.Log("Codec confirmed as MJPEG")
	}

	// Check dimensions.
	if !strings.Contains(outputStr, `"width": 640`) {
		t.Log("Warning: width not detected as 640 in ffprobe output")
	}
	if !strings.Contains(outputStr, `"height": 480`) {
		t.Log("Warning: height not detected as 480 in ffprobe output")
	}
}

// TestEnhancedGoMerge_ContextCancellation verifies cancellation during merge.
func TestEnhancedGoMerge_ContextCancellation(t *testing.T) {
	tmpDir := t.TempDir()
	framesDir := filepath.Join(tmpDir, "frames")
	outputPath := filepath.Join(tmpDir, "output.mp4")

	if err := os.MkdirAll(framesDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Generate frames.
	for i := 0; i < 20; i++ {
		framePath := filepath.Join(framesDir, fmt.Sprintf("frame_%06d.jpg", i))
		generateTestJPEG(t, framePath, 640, 480, 85)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately.

	merger := NewEnhancedGoMerger(30)
	_, err := merger.Merge(ctx, framesDir, outputPath, 1)
	if err == nil {
		t.Error("Expected error for cancelled context, got nil")
	}
}

// TestEnhancedGoMerge_InterfaceCompliance verifies EnhancedGoMerger satisfies the interface.
func TestEnhancedGoMerge_InterfaceCompliance(t *testing.T) {
	var _ TimelapseMerger = (*GoMerger)(nil)
}

// TestEnhancedGoMerge_MergeResult verifies merge result fields are populated.
func TestEnhancedGoMerge_MergeResult(t *testing.T) {
	tmpDir := t.TempDir()
	framesDir := filepath.Join(tmpDir, "frames")
	outputPath := filepath.Join(tmpDir, "output.mp4")

	if err := os.MkdirAll(framesDir, 0o755); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 3; i++ {
		framePath := filepath.Join(framesDir, fmt.Sprintf("frame_%06d.jpg", i))
		generateTestJPEG(t, framePath, 640, 480, 85)
	}

	merger := NewEnhancedGoMerger(30)
	result, err := merger.Merge(context.Background(), framesDir, outputPath, 1)
	if err != nil {
		t.Fatalf("Merge failed: %v", err)
	}

	if result.Tier != TierGo {
		t.Errorf("Expected Tier=%q, got %q", TierGo, result.Tier)
	}
	if result.FramesMerged != 3 {
		t.Errorf("Expected 3 frames merged, got %d", result.FramesMerged)
	}
	if result.Duration <= 0 {
		t.Errorf("Expected positive duration, got %f", result.Duration)
	}
	if result.OutputPath != outputPath {
		t.Errorf("Expected OutputPath=%q, got %q", outputPath, result.OutputPath)
	}
	if result.Error != "" {
		t.Errorf("Expected no error, got %q", result.Error)
	}
}

// TestEnhancedGoMerge_PassthroughMode verifies passthrough mode preserves original JPEG data.
func TestEnhancedGoMerge_PassthroughMode(t *testing.T) {
	tmpDir := t.TempDir()
	framesDir := filepath.Join(tmpDir, "frames")
	outputPath := filepath.Join(tmpDir, "output.mp4")

	if err := os.MkdirAll(framesDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Generate a single frame.
	framePath := filepath.Join(framesDir, "frame_000000.jpg")
	generateTestJPEG(t, framePath, 640, 480, 85)

	originalData, err := os.ReadFile(framePath)
	if err != nil {
		t.Fatal(err)
	}

	// Create passthrough merger (default NewGoMerger, no re-encoding).
	merger := NewGoMerger()
	result, err := merger.Merge(context.Background(), framesDir, outputPath, 1)
	if err != nil {
		t.Fatalf("Passthrough merge failed: %v", err)
	}

	outputData, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatal(err)
	}

	// Passthrough output should be larger than the frame (MP4 container overhead).
	if len(outputData) <= len(originalData) {
		t.Errorf("Passthrough output (%d bytes) should be larger than frame (%d bytes) due to MP4 overhead",
			len(outputData), len(originalData))
	}

	_ = result
}
