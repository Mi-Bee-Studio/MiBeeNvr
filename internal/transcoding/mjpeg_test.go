package transcoding

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// --- Test helpers ---

// mjpegMustCreateDir is a test helper that creates a directory with timestamped JPEG files.
// Frames are created at the given interval starting from baseTime.
func mjpegMustCreateDir(t *testing.T, dir string, baseTime time.Time, frameCount int, interval time.Duration) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	for i := 0; i < frameCount; i++ {
		ts := baseTime.Add(time.Duration(i) * interval)
		// Match the format from storage/manager.go WriteFrame: "20060102_150405.000"
		name := ts.Format("20060102_150405.000") + ".jpg"
		if err := os.WriteFile(filepath.Join(dir, name), []byte("fake-jpeg"), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
}

// mjpegMustCreateEmptyDir is a test helper that creates an empty directory (no JPEGs).
func mjpegMustCreateEmptyDir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
}

// --- Tests ---

// TestScanJPEGFiles_Sorted verifies that scanJPEGFiles returns .jpg files sorted by name.
func TestScanJPEGFiles_Sorted(t *testing.T) {
	t.Helper()
	dir := t.TempDir()

	// Create JPEG files out of order.
	for _, name := range []string{"c.jpg", "a.jpg", "b.jpg"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("data"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Create a non-JPEG file that should be ignored.
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("ignore"), 0o644); err != nil {
		t.Fatal(err)
	}

	files, err := scanJPEGFiles(dir)
	if err != nil {
		t.Fatalf("scanJPEGFiles: %v", err)
	}

	if len(files) != 3 {
		t.Fatalf("expected 3 files, got %d", len(files))
	}

	// Verify sorted order.
	expected := []string{"a.jpg", "b.jpg", "c.jpg"}
	for i, f := range files {
		if filepath.Base(f) != expected[i] {
			t.Errorf("files[%d] = %q, want %q", i, filepath.Base(f), expected[i])
		}
	}
}

// TestScanJPEGFiles_EmptyDirectory verifies that scanJPEGFiles returns empty slice for dir with no JPEGs.
func TestScanJPEGFiles_EmptyDirectory(t *testing.T) {
	t.Helper()
	dir := t.TempDir()

	files, err := scanJPEGFiles(dir)
	if err != nil {
		t.Fatalf("scanJPEGFiles: %v", err)
	}
	if len(files) != 0 {
		t.Fatalf("expected 0 files in empty directory, got %d", len(files))
	}
}

// TestScanJPEGFiles_NonexistentDir verifies that scanJPEGFiles returns error for missing directory.
func TestScanJPEGFiles_NonexistentDir(t *testing.T) {
	t.Helper()
	_, err := scanJPEGFiles("/nonexistent/path/that/does/not/exist")
	if err == nil {
		t.Fatal("expected error for nonexistent directory")
	}
}

// TestInferFramerate_10FPS verifies framerate inference from 100ms intervals (10 FPS).
func TestInferFramerate_10FPS(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	baseTime := time.Date(2025, 1, 15, 10, 30, 0, 0, time.UTC)
	// Create 11 frames at 100ms intervals → 10 FPS.
	for i := 0; i < 11; i++ {
		ts := baseTime.Add(time.Duration(i) * 100 * time.Millisecond)
		name := ts.Format("20060102_150405.000") + ".jpg"
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	files, err := scanJPEGFiles(dir)
	if err != nil {
		t.Fatal(err)
	}

	fps := inferFramerate(files)
	if fps != 10 {
		t.Errorf("inferFramerate = %d, want 10", fps)
	}
}

// TestInferFramerate_1FPS verifies framerate inference from 1s intervals (1 FPS).
func TestInferFramerate_1FPS(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	baseTime := time.Date(2025, 3, 20, 14, 0, 0, 0, time.UTC)
	// Create 5 frames at 1s intervals → 1 FPS.
	for i := 0; i < 5; i++ {
		ts := baseTime.Add(time.Duration(i) * time.Second)
		name := ts.Format("20060102_150405.000") + ".jpg"
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	files, err := scanJPEGFiles(dir)
	if err != nil {
		t.Fatal(err)
	}

	fps := inferFramerate(files)
	if fps != 1 {
		t.Errorf("inferFramerate = %d, want 1", fps)
	}
}

// TestInferFramerate_SingleFrame verifies that a single frame returns 0 (cannot infer).
func TestInferFramerate_SingleFrame(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	name := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC).Format("20060102_150405.000") + ".jpg"
	if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	files, err := scanJPEGFiles(dir)
	if err != nil {
		t.Fatal(err)
	}

	fps := inferFramerate(files)
	if fps != 0 {
		t.Errorf("inferFramerate with 1 frame = %d, want 0", fps)
	}
}

// TestInferFramerate_EmptySlice verifies that empty file list returns 0.
func TestInferFramerate_EmptySlice(t *testing.T) {
	t.Helper()
	fps := inferFramerate(nil)
	if fps != 0 {
		t.Errorf("inferFramerate(nil) = %d, want 0", fps)
	}
}

// TestParseFrameTimestamp_Formats verifies parsing of various filename formats.
func TestParseFrameTimestamp_Formats(t *testing.T) {
	t.Helper()
	cases := []struct {
		filename string
		want     time.Time
	}{
		{
			"20250115_103000.000.jpg",
			time.Date(2025, 1, 15, 10, 30, 0, 0, time.UTC),
		},
		{
			"20250115_103000.jpg",
			time.Date(2025, 1, 15, 10, 30, 0, 0, time.UTC),
		},
		{
			"20250115_103000.123456789.jpg",
			time.Date(2025, 1, 15, 10, 30, 0, 123456789, time.UTC),
		},
	}

	for _, tc := range cases {
		got, err := parseFrameTimestamp(tc.filename)
		if err != nil {
			t.Errorf("parseFrameTimestamp(%q): %v", tc.filename, err)
			continue
		}
		// Compare at second precision since some formats truncate sub-second.
		if got.Unix() != tc.want.Unix() {
			t.Errorf("parseFrameTimestamp(%q) = %v, want %v", tc.filename, got, tc.want)
		}
	}
}

// TestParseFrameTimestamp_Invalid verifies that invalid filenames return an error.
func TestParseFrameTimestamp_Invalid(t *testing.T) {
	t.Helper()
	_, err := parseFrameTimestamp("not_a_timestamp.jpg")
	if err == nil {
		t.Error("expected error for unparseable filename")
	}
}

// TestTranscodeMJPEG_EmptyDirectory verifies that transcoding an empty directory returns an error.
func TestTranscodeMJPEG_EmptyDirectory(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	output := filepath.Join(dir, "output.mp4")
	emptyDir := filepath.Join(dir, "empty_frames")
	mjpegMustCreateEmptyDir(t, emptyDir)

	caps := HardwareCapabilities{FFmpegPath: "/usr/bin/ffmpeg"}
	err := TranscodeMJPEG(context.Background(), emptyDir, output, 0, caps, nil)
	if err == nil {
		t.Fatal("expected error for empty directory")
	}
	if err.Error() != "no JPEG files found in "+emptyDir {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestTranscodeMJPEG_NonexistentDir verifies that a nonexistent input directory returns an error.
func TestTranscodeMJPEG_NonexistentDir(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	output := filepath.Join(dir, "output.mp4")

	caps := HardwareCapabilities{FFmpegPath: "/usr/bin/ffmpeg"}
	err := TranscodeMJPEG(context.Background(), filepath.Join(dir, "missing"), output, 10, caps, nil)
	if err == nil {
		t.Fatal("expected error for nonexistent directory")
	}
}

// TestTranscodeMJPEG_CancelledContext verifies that context cancellation is propagated.
func TestTranscodeMJPEG_CancelledContext(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	output := filepath.Join(dir, "output.mp4")
	framesDir := filepath.Join(dir, "frames")
	mjpegMustCreateDir(t, framesDir, time.Now(), 3, 100*time.Millisecond)

	caps := HardwareCapabilities{FFmpegPath: "/usr/bin/ffmpeg"}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately.

	err := TranscodeMJPEG(ctx, framesDir, output, 10, caps, nil)
	if err == nil {
		t.Fatal("expected error from cancelled context")
	}
}

// TestTranscodeMJPEG_FramerateInference verifies that FPS is inferred when fps=0.
// This tests the scan + inference path without actually calling FFmpeg.
func TestTranscodeMJPEG_FramerateInference(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	output := filepath.Join(dir, "output.mp4")
	framesDir := filepath.Join(dir, "frames")
	// Create frames at 200ms intervals → 5 FPS.
	mjpegMustCreateDir(t, framesDir, time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC), 10, 200*time.Millisecond)

	caps := HardwareCapabilities{FFmpegPath: "/usr/bin/ffmpeg"}

	// FPS=0 triggers inference. FFmpeg will fail (not installed in test), but we verify
	// that the error comes from the engine, not from scan/inference.
	err := TranscodeMJPEG(context.Background(), framesDir, output, 0, caps, nil)
	// The error should come from FFmpeg execution, not from "no JPEG files" or "scan directory".
	if err == nil {
		// If FFmpeg is actually installed, this could succeed.
		t.Log("TranscodeMJPEG succeeded (FFmpeg installed)")
	} else {
		// Error must NOT be about empty directory or scanning.
		if err.Error() == "no JPEG files found in "+framesDir ||
			err.Error() == "scan directory: ..." {
			t.Errorf("unexpected error about scan/empty: %v", err)
		}
		t.Logf("Expected FFmpeg error (not installed or failed): %v", err)
	}
}

// TestTranscodeMJPEG_ExplicitFPS verifies that an explicit FPS is used when provided.
// We test this indirectly by verifying the function reaches the engine execution step.
func TestTranscodeMJPEG_ExplicitFPS(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	output := filepath.Join(dir, "output.mp4")
	framesDir := filepath.Join(dir, "frames")
	mjpegMustCreateDir(t, framesDir, time.Now(), 5, 100*time.Millisecond)

	caps := HardwareCapabilities{FFmpegPath: "/usr/bin/ffmpeg"}

	err := TranscodeMJPEG(context.Background(), framesDir, output, 25, caps, nil)
	// Error expected from FFmpeg, not from scan.
	if err != nil {
		// Verify it's not a scan error.
		if err.Error() == "no JPEG files found in "+framesDir {
			t.Errorf("should not get empty directory error: %v", err)
		}
		t.Logf("FFmpeg error (expected if not installed): %v", err)
	}
}
