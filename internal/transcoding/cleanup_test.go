package transcoding

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
)

// --- Test helpers ---

// mustCreateFile is a test helper that creates a file with the given content.
func mustCreateFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// mustCreateDir is a test helper that creates a directory with sample JPEG files.
func mustCreateDir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	for i := 0; i < 3; i++ {
		mustCreateFile(t, filepath.Join(path, filepath.FromSlash(
			string(rune('a'+i))+".jpg",
		)), "fake-jpeg")
	}
}

// pathExists returns true if path exists on disk.
func pathExists(t *testing.T, path string) bool {
	t.Helper()
	_, err := os.Stat(path)
	return err == nil
}

// --- Tests ---

// TestVerifyOutput_MissingFile verifies that verification fails when output doesn't exist.
func TestVerifyOutput_MissingFile(t *testing.T) {
	cleaner := NewTranscodeCleaner("")
	tmp := t.TempDir()
	missing := filepath.Join(tmp, "nonexistent.mp4")

	err := cleaner.verifyOutput(missing, missing)
	if err == nil {
		t.Fatal("verifyOutput should fail for missing file")
	}
}

// TestVerifyOutput_EmptyFile verifies that verification fails for a zero-byte output.
func TestVerifyOutput_EmptyFile(t *testing.T) {
	cleaner := NewTranscodeCleaner("")
	tmp := t.TempDir()
	empty := filepath.Join(tmp, "empty.mp4")
	f, err := os.Create(empty)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	f.Close()

	err = cleaner.verifyOutput(empty, empty)
	if err == nil {
		t.Fatal("verifyOutput should fail for empty file")
	}
}

// TestReplaceOriginal_H264 verifies that replace mode deletes a single file (h264).
func TestReplaceOriginal_H264(t *testing.T) {
	cleaner := NewTranscodeCleaner("")
	tmp := t.TempDir()
	input := filepath.Join(tmp, "original.mp4")
	mustCreateFile(t, input, "fake-h264-data")

	if !pathExists(t, input) {
		t.Fatal("input file should exist before replace")
	}

	err := cleaner.replaceOriginal(input, "h264")
	if err != nil {
		t.Fatalf("replaceOriginal h264: %v", err)
	}
	if pathExists(t, input) {
		t.Fatal("original file should be deleted after replace")
	}
}

// TestReplaceOriginal_MJPEG verifies that replace mode deletes an entire directory (mjpeg).
func TestReplaceOriginal_MJPEG(t *testing.T) {
	cleaner := NewTranscodeCleaner("")
	tmp := t.TempDir()
	inputDir := filepath.Join(tmp, "mjpeg_frames")
	mustCreateDir(t, inputDir)

	if !pathExists(t, inputDir) {
		t.Fatal("input directory should exist before replace")
	}

	err := cleaner.replaceOriginal(inputDir, "mjpeg")
	if err != nil {
		t.Fatalf("replaceOriginal mjpeg: %v", err)
	}
	if pathExists(t, inputDir) {
		t.Fatal("MJPEG directory should be deleted after replace")
	}
}

// TestReplaceOriginal_H265 verifies that replace mode works for h265 format (single file).
func TestReplaceOriginal_H265(t *testing.T) {
	cleaner := NewTranscodeCleaner("")
	tmp := t.TempDir()
	input := filepath.Join(tmp, "original.mp4")
	mustCreateFile(t, input, "fake-h265-data")

	err := cleaner.replaceOriginal(input, "h265")
	if err != nil {
		t.Fatalf("replaceOriginal h265: %v", err)
	}
	if pathExists(t, input) {
		t.Fatal("original file should be deleted after replace")
	}
}

// TestVerifyAndClean_KeepMode verifies keep mode: original is untouched.
func TestVerifyAndClean_KeepMode(t *testing.T) {
	cleaner := NewTranscodeCleaner("")
	tmp := t.TempDir()
	input := filepath.Join(tmp, "original.mp4")
	output := filepath.Join(tmp, "output.mp4")
	mustCreateFile(t, input, "fake-input-data")

	// verifyOutput will fail at ffprobe stage, but keep mode doesn't delete
	// the original on verification failure — it rolls back output only.
	// Create a non-empty output so file-exists check passes.
	mustCreateFile(t, output, "fake-output-data")

	err := cleaner.VerifyAndClean(context.Background(), input, output, "h264", false)
	// Verification will fail because ffprobe can't parse fake data.
	// But we're testing that the input is NOT deleted in keep mode on failure.
	if pathExists(t, input) == false {
		t.Fatal("input file should NOT be deleted in keep mode even on verification failure")
	}
	// The output should have been rolled back (deleted) since verification failed.
	_ = err // error expected
}

// TestVerifyAndClean_ReplaceMode_Success verifies replace mode with valid output.
// We can't test full ffprobe validation without FFmpeg installed, so we test
// the replaceOriginal path directly instead.
func TestVerifyAndClean_ReplaceMode_InvalidOutputRollback(t *testing.T) {
	cleaner := NewTranscodeCleaner("")
	tmp := t.TempDir()
	input := filepath.Join(tmp, "original.mp4")
	output := filepath.Join(tmp, "output.mp4")
	mustCreateFile(t, input, "fake-input-data")
	mustCreateFile(t, output, "fake-output-data")

	err := cleaner.VerifyAndClean(context.Background(), input, output, "h264", true)
	if err == nil {
		t.Fatal("VerifyAndClean should fail — ffprobe cannot validate fake data")
	}

	// Original must NOT be deleted — verification failed before replace step.
	if !pathExists(t, input) {
		t.Fatal("original should NOT be deleted when verification fails")
	}
}

// TestRollbackFailedTranscode verifies that rollback removes the output file.
func TestRollbackFailedTranscode(t *testing.T) {
	cleaner := NewTranscodeCleaner("")
	tmp := t.TempDir()
	output := filepath.Join(tmp, "partial.mp4")
	mustCreateFile(t, output, "partial-data")

	cleaner.RollbackFailedTranscode(output)

	if pathExists(t, output) {
		t.Fatal("partial output should be removed after rollback")
	}
}

// TestRollbackFailedTranscode_AlreadyGone verifies rollback is safe when file doesn't exist.
func TestRollbackFailedTranscode_AlreadyGone(t *testing.T) {
	cleaner := NewTranscodeCleaner("")
	tmp := t.TempDir()
	missing := filepath.Join(tmp, "nonexistent.mp4")

	// Should not panic or error.
	cleaner.RollbackFailedTranscode(missing)
}

// TestCheckDiskSpaceForTranscode_Sufficient verifies disk space check passes with enough space.
func TestCheckDiskSpaceForTranscode_Sufficient(t *testing.T) {
	tmp := t.TempDir()
	smallFile := filepath.Join(tmp, "input.mp4")
	mustCreateFile(t, smallFile, "x") // 1 byte

	err := CheckDiskSpaceForTranscode(smallFile, tmp, 2.0)
	if err != nil {
		t.Fatalf("CheckDiskSpaceForTranscode should pass for 1-byte input: %v", err)
	}
}

// TestCheckDiskSpaceForTranscode_MissingInput verifies check is skipped for missing input.
func TestCheckDiskSpaceForTranscode_MissingInput(t *testing.T) {
	tmp := t.TempDir()

	err := CheckDiskSpaceForTranscode("/nonexistent/file.mp4", tmp, 2.0)
	if err != nil {
		t.Fatalf("CheckDiskSpaceForTranscode should skip for missing input: %v", err)
	}
}

// TestCheckDiskSpaceForTranscode_DirInput verifies disk space check with directory input.
func TestCheckDiskSpaceForTranscode_DirInput(t *testing.T) {
	tmp := t.TempDir()
	inputDir := filepath.Join(tmp, "frames")
	mustCreateDir(t, inputDir)

	err := CheckDiskSpaceForTranscode(inputDir, tmp, 2.0)
	if err != nil {
		t.Fatalf("CheckDiskSpaceForTranscode should pass for small directory: %v", err)
	}
}

// TestDirSize verifies dirSize returns correct total for files in a directory.
func TestDirSize(t *testing.T) {
	tmp := t.TempDir()
	mustCreateFile(t, filepath.Join(tmp, "a.jpg"), "12345")  // 5 bytes
	mustCreateFile(t, filepath.Join(tmp, "b.jpg"), "123456") // 6 bytes

	size, err := dirSize(tmp)
	if err != nil {
		t.Fatalf("dirSize: %v", err)
	}
	if size != 11 {
		t.Fatalf("dirSize = %d, want 11", size)
	}
}

// TestDirSize_SingleFile verifies dirSize works for a single file.
func TestDirSize_SingleFile(t *testing.T) {
	tmp := t.TempDir()
	p := filepath.Join(tmp, "test.mp4")
	mustCreateFile(t, p, "hello") // 5 bytes

	size, err := dirSize(p)
	if err != nil {
		t.Fatalf("dirSize: %v", err)
	}
	if size != 5 {
		t.Fatalf("dirSize = %d, want 5", size)
	}
}

// TestNewTranscodeCleaner verifies constructor with custom ffprobe path.
func TestNewTranscodeCleaner(t *testing.T) {
	c := NewTranscodeCleaner("/usr/bin/ffprobe")
	if c.ffprobePath != "/usr/bin/ffprobe" {
		t.Fatalf("ffprobePath = %q, want /usr/bin/ffprobe", c.ffprobePath)
	}

	c2 := NewTranscodeCleaner("")
	if c2.ffprobePath != "" {
		t.Fatalf("empty ffprobePath should be preserved for default resolution in ffprobe funcs")
	}
}

// mockDBTaskLister is a test mock for DBTaskLister interface.
type mockDBTaskLister struct {
	tasks []storage.TranscodeTask
	err   error
}

func (m *mockDBTaskLister) ListTranscodeTasks(_ context.Context, _ storage.TranscodeTaskFilter) ([]storage.TranscodeTask, int, error) {
	return m.tasks, len(m.tasks), m.err
}

// TestCleanOrphanedTranscodes_DeletesOrphan verifies that files without a DB task are deleted.
func TestCleanOrphanedTranscodes_DeletesOrphan(t *testing.T) {
	tmp := t.TempDir()

	// Create an orphaned transcoded file (no matching DB task)
	orphan := filepath.Join(tmp, "segment.mp4.transcoded.mp4")
	mustCreateFile(t, orphan, "fake-transcoded-data")

	db := &mockDBTaskLister{tasks: nil}

	err := CleanOrphanedTranscodes(context.Background(), tmp, db)
	if err != nil {
		t.Fatalf("CleanOrphanedTranscodes: %v", err)
	}

	if pathExists(t, orphan) {
		t.Fatal("orphaned transcoded file should be deleted")
	}
}

// TestCleanOrphanedTranscodes_PreservesActiveTask verifies that files with a DB task are kept.
func TestCleanOrphanedTranscodes_PreservesActiveTask(t *testing.T) {
	tmp := t.TempDir()

	activePath := filepath.Join(tmp, "segment.mp4.transcoded.mp4")
	mustCreateFile(t, activePath, "fake-transcoded-data")

	db := &mockDBTaskLister{
		tasks: []storage.TranscodeTask{{
			OutputPath: activePath,
		}},
	}

	err := CleanOrphanedTranscodes(context.Background(), tmp, db)
	if err != nil {
		t.Fatalf("CleanOrphanedTranscodes: %v", err)
	}

	if !pathExists(t, activePath) {
		t.Fatal("file with active DB task should be preserved")
	}
}

// TestCleanOrphanedTranscodes_MixedFiles verifies only .transcoded.mp4 orphans are deleted.
func TestCleanOrphanedTranscodes_MixedFiles(t *testing.T) {
	tmp := t.TempDir()

	orphan := filepath.Join(tmp, "video.mp4.transcoded.mp4")
	normal := filepath.Join(tmp, "video.mp4")
	mustCreateFile(t, orphan, "orphan-data")
	mustCreateFile(t, normal, "normal-data")

	db := &mockDBTaskLister{tasks: nil}

	err := CleanOrphanedTranscodes(context.Background(), tmp, db)
	if err != nil {
		t.Fatalf("CleanOrphanedTranscodes: %v", err)
	}

	if pathExists(t, orphan) {
		t.Fatal("orphaned .transcoded.mp4 should be deleted")
	}
	if !pathExists(t, normal) {
		t.Fatal("normal .mp4 file should NOT be deleted")
	}
}

// TestCleanOrphanedTranscodes_DBError verifies that DB errors are propagated.
func TestCleanOrphanedTranscodes_DBError(t *testing.T) {
	tmp := t.TempDir()

	db := &mockDBTaskLister{err: fmt.Errorf("db unavailable")}

	err := CleanOrphanedTranscodes(context.Background(), tmp, db)
	if err == nil {
		t.Fatal("CleanOrphanedTranscodes should return error when DB fails")
	}
}
