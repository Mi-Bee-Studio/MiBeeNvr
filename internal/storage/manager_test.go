package storage

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/stretchr/testify/require"
)

// --- NewManager() ---

func TestNew_CreatesRootDir(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "nvr")

	m, err := NewManager(root)
	if err != nil {
		t.Fatalf("New(%q) returned error: %v", root, err)
	}
	if m == nil {
		t.Fatal("New returned nil manager")
	}

	info, err := os.Stat(root)
	if err != nil {
		t.Fatalf("root dir not created: %v", err)
	}
	if !info.IsDir() {
		t.Fatal("root path is not a directory")
	}
}

func TestNew_AcceptsExistingDir(t *testing.T) {
	root := t.TempDir()

	m, err := NewManager(root)
	if err != nil {
		t.Fatalf("New(%q) returned error: %v", root, err)
	}
	if m == nil {
		t.Fatal("New returned nil manager")
	}
}

func TestNew_EmptyPath(t *testing.T) {
	_, err := NewManager("")
	if err == nil {
		t.Fatal("expected error for empty path, got nil")
	}
}

// --- EnsureCameraDir ---

func TestEnsureCameraDir(t *testing.T) {
	dir := t.TempDir()
	m, _ := NewManager(dir)

	cameraID := "cam-01"
	err := m.EnsureCameraDir(cameraID)
	if err != nil {
		t.Fatalf("EnsureCameraDir(%q) error: %v", cameraID, err)
	}

	expected := filepath.Join(dir, cameraID)
	info, err := os.Stat(expected)
	if err != nil {
		t.Fatalf("camera dir not created: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("expected directory, got file")
	}
}

func TestEnsureCameraDir_Idempotent(t *testing.T) {
	dir := t.TempDir()
	m, _ := NewManager(dir)

	cameraID := "cam-01"
	if err := m.EnsureCameraDir(cameraID); err != nil {
		t.Fatal(err)
	}
	if err := m.EnsureCameraDir(cameraID); err != nil {
		t.Fatal("second call should not error:", err)
	}
}

// --- CreateSegment + CloseSegment (Atomic Write) ---

func TestCreateSegment_H264(t *testing.T) {
	dir := t.TempDir()
	m, _ := NewManager(dir)
	m.EnsureCameraDir("cam-01")

	tempPath, finalPath, err := m.CreateSegment("cam-01", "h264")
	if err != nil {
		t.Fatalf("CreateSegment error: %v", err)
	}

	// temp file must exist
	if _, err := os.Stat(tempPath); err != nil {
		t.Fatalf("temp file not created: %v", err)
	}

	// temp file must end with .tmp
	if !strings.HasSuffix(tempPath, ".tmp") {
		t.Fatalf("temp path must end with .tmp, got: %s", tempPath)
	}

	// final path must end with .mp4
	if !strings.HasSuffix(finalPath, ".mp4") {
		t.Fatalf("final path must end with .mp4, got: %s", finalPath)
	}

	// final path must NOT exist yet (atomic write guarantee)
	if _, err := os.Stat(finalPath); err == nil {
		t.Fatal("final path must not exist before CloseSegment")
	}

	// Write some data
	data := []byte("fake-h264-data")
	n, err := m.WriteFrame(tempPath, data)
	if err != nil {
		t.Fatalf("WriteFrame error: %v", err)
	}
	if n != len(data) {
		t.Fatalf("WriteFrame wrote %d bytes, want %d", n, len(data))
	}

	// Close segment — atomic rename
	if err := m.CloseSegment(tempPath, finalPath); err != nil {
		t.Fatalf("CloseSegment error: %v", err)
	}

	// final path must now exist
	if _, err := os.Stat(finalPath); err != nil {
		t.Fatalf("final file not created after CloseSegment: %v", err)
	}

	// temp path must no longer exist
	if _, err := os.Stat(tempPath); err == nil {
		t.Fatal("temp file still exists after CloseSegment")
	}

	// Verify content
	content, err := os.ReadFile(finalPath)
	if err != nil {
		t.Fatalf("cannot read final file: %v", err)
	}
	if string(content) != string(data) {
		t.Fatalf("content mismatch: got %q, want %q", content, data)
	}
}

func TestCreateSegment_AVI(t *testing.T) {
	dir := t.TempDir()
	m, _ := NewManager(dir)
	m.EnsureCameraDir("cam-03")

	tempPath, finalPath, err := m.CreateSegment("cam-03", "avi")
	if err != nil {
		t.Fatalf("CreateSegment error: %v", err)
	}

	// temp file must exist
	if _, err := os.Stat(tempPath); err != nil {
		t.Fatalf("temp file not created: %v", err)
	}

	// temp file must end with .tmp
	if !strings.HasSuffix(tempPath, ".tmp") {
		t.Fatalf("temp path must end with .tmp, got: %s", tempPath)
	}

	// final path must end with .avi
	if !strings.HasSuffix(finalPath, ".avi") {
		t.Fatalf("final path must end with .avi, got: %s", finalPath)
	}

	// final path must NOT exist yet (atomic write guarantee)
	if _, err := os.Stat(finalPath); err == nil {
		t.Fatal("final path must not exist before CloseSegment")
	}

	// Write some data
	data := []byte("fake-avi-data")
	n, err := m.WriteFrame(tempPath, data)
	if err != nil {
		t.Fatalf("WriteFrame error: %v", err)
	}
	if n != len(data) {
		t.Fatalf("WriteFrame wrote %d bytes, want %d", n, len(data))
	}

	// Close segment — atomic rename
	if err := m.CloseSegment(tempPath, finalPath); err != nil {
		t.Fatalf("CloseSegment error: %v", err)
	}

	// final path must now exist
	if _, err := os.Stat(finalPath); err != nil {
		t.Fatalf("final file not created after CloseSegment: %v", err)
	}

	// temp path must no longer exist
	if _, err := os.Stat(tempPath); err == nil {
		t.Fatal("temp file still exists after CloseSegment")
	}

	// Verify content
	content, err := os.ReadFile(finalPath)
	if err != nil {
		t.Fatalf("cannot read final file: %v", err)
	}
	if string(content) != string(data) {
		t.Fatalf("content mismatch: got %q, want %q", content, data)
	}
}

func TestCreateSegment_MJPEG(t *testing.T) {
	dir := t.TempDir()
	m, _ := NewManager(dir)
	m.EnsureCameraDir("cam-02")

	tempPath, finalPath, err := m.CreateSegment("cam-02", "mjpeg")
	if err != nil {
		t.Fatalf("CreateSegment error: %v", err)
	}

	// temp must be a directory
	info, err := os.Stat(tempPath)
	if err != nil {
		t.Fatalf("temp dir not created: %v", err)
	}
	if !info.IsDir() {
		t.Fatal("temp path must be a directory for MJPEG")
	}

	// final path must NOT exist
	if _, err := os.Stat(finalPath); err == nil {
		t.Fatal("final dir must not exist before CloseSegment")
	}

	// Write frames (individual JPEG files)
	frame1 := []byte("fake-jpeg-1")
	frame2 := []byte("fake-jpeg-2")

	if _, err := m.WriteFrame(tempPath, frame1); err != nil {
		t.Fatalf("WriteFrame 1 error: %v", err)
	}
	time.Sleep(10 * time.Millisecond) // ensure different timestamps
	if _, err := m.WriteFrame(tempPath, frame2); err != nil {
		t.Fatalf("WriteFrame 2 error: %v", err)
	}

	// Close segment
	if err := m.CloseSegment(tempPath, finalPath); err != nil {
		t.Fatalf("CloseSegment error: %v", err)
	}

	// final path must exist as directory
	info, err = os.Stat(finalPath)
	if err != nil {
		t.Fatalf("final dir not created: %v", err)
	}
	if !info.IsDir() {
		t.Fatal("final path must be a directory for MJPEG")
	}

	// temp dir must no longer exist
	if _, err := os.Stat(tempPath); err == nil {
		t.Fatal("temp dir still exists after CloseSegment")
	}

	// Check files inside final dir
	entries, err := os.ReadDir(finalPath)
	if err != nil {
		t.Fatalf("cannot read final dir: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 frames, got %d", len(entries))
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".jpg") {
			t.Fatalf("frame file must end with .jpg, got: %s", e.Name())
		}
	}
}

func TestAtomicWrite_FileNotVisibleBeforeClose(t *testing.T) {
	dir := t.TempDir()
	m, _ := NewManager(dir)
	m.EnsureCameraDir("cam-03")

	tempPath, finalPath, err := m.CreateSegment("cam-03", "h264")
	if err != nil {
		t.Fatal(err)
	}

	// Write data
	m.WriteFrame(tempPath, []byte("data"))

	// List files before close — final should NOT appear
	files, _ := m.ListFiles("cam-03")
	for _, f := range files {
		if strings.Contains(f, filepath.Base(finalPath)) {
			t.Fatal("final file visible in listing before CloseSegment")
		}
	}

	m.CloseSegment(tempPath, finalPath)

	// List files after close — final SHOULD appear
	files, err = m.ListFiles("cam-03")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, f := range files {
		if strings.Contains(f, filepath.Base(finalPath)) {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("final file not found in listing after CloseSegment")
	}
}

// --- Multiple segments don't collide ---

func TestMultipleSegments_NoCollision(t *testing.T) {
	dir := t.TempDir()
	m, _ := NewManager(dir)
	m.EnsureCameraDir("cam-04")

	temps := make([]string, 0)
	finals := make([]string, 0)

	for i := range 5 {
		temp, final, err := m.CreateSegment("cam-04", "h264")
		if err != nil {
			t.Fatalf("segment %d error: %v", i, err)
		}
		temps = append(temps, temp)
		finals = append(finals, final)
	}

	// All temp paths must be unique
	seen := make(map[string]bool)
	for _, p := range temps {
		if seen[p] {
			t.Fatalf("duplicate temp path: %s", p)
		}
		seen[p] = true
	}

	// All final paths must be unique
	for _, p := range finals {
		if seen[p] {
			t.Fatalf("duplicate final path: %s", p)
		}
		seen[p] = true
	}

	// Clean up
	for i := range temps {
		m.CloseSegment(temps[i], finals[i])
	}
}

// --- ListFiles ---

func TestListFiles(t *testing.T) {
	dir := t.TempDir()
	m, _ := NewManager(dir)
	m.EnsureCameraDir("cam-05")

	// Create a couple of segments
	temp1, final1, _ := m.CreateSegment("cam-05", "h264")
	m.WriteFrame(temp1, []byte("data1"))
	m.CloseSegment(temp1, final1)

	time.Sleep(time.Second) // ensure different final path timestamps
	temp2, final2, _ := m.CreateSegment("cam-05", "h264")
	m.WriteFrame(temp2, []byte("data2"))
	m.CloseSegment(temp2, final2)

	files, err := m.ListFiles("cam-05")
	if err != nil {
		t.Fatalf("ListFiles error: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("expected 2 files, got %d: %v", len(files), files)
	}
}

func TestListFiles_EmptyCamera(t *testing.T) {
	dir := t.TempDir()
	m, _ := NewManager(dir)
	m.EnsureCameraDir("cam-06")

	files, err := m.ListFiles("cam-06")
	if err != nil {
		t.Fatalf("ListFiles error: %v", err)
	}
	if len(files) != 0 {
		t.Fatalf("expected 0 files, got %d", len(files))
	}
}

func TestListFiles_CameraNotExist(t *testing.T) {
	dir := t.TempDir()
	m, _ := NewManager(dir)

	_, err := m.ListFiles("nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent camera")
	}
}

// --- GetFileSize ---

func TestGetFileSize(t *testing.T) {
	dir := t.TempDir()
	m, _ := NewManager(dir)
	m.EnsureCameraDir("cam-07")

	temp, final, _ := m.CreateSegment("cam-07", "h264")
	data := []byte("test-file-content")
	m.WriteFrame(temp, data)
	m.CloseSegment(temp, final)

	size, err := m.GetFileSize(final)
	if err != nil {
		t.Fatalf("GetFileSize error: %v", err)
	}
	if size != int64(len(data)) {
		t.Fatalf("size mismatch: got %d, want %d", size, len(data))
	}
}

func TestGetFileSize_NotExist(t *testing.T) {
	dir := t.TempDir()
	m, _ := NewManager(dir)

	_, err := m.GetFileSize(filepath.Join(dir, "nonexistent.mp4"))
	if err == nil {
		t.Fatal("expected error for nonexistent file")
	}
}

// --- DeleteFile ---

func TestDeleteFile(t *testing.T) {
	dir := t.TempDir()
	m, _ := NewManager(dir)
	m.EnsureCameraDir("cam-08")

	temp, final, _ := m.CreateSegment("cam-08", "h264")
	m.WriteFrame(temp, []byte("data"))
	m.CloseSegment(temp, final)

	if err := m.DeleteFile(final); err != nil {
		t.Fatalf("DeleteFile error: %v", err)
	}

	if _, err := os.Stat(final); err == nil {
		t.Fatal("file still exists after delete")
	}
}

func TestDeleteFile_NotExist(t *testing.T) {
	dir := t.TempDir()
	m, _ := NewManager(dir)

	err := m.DeleteFile(filepath.Join(dir, "nonexistent.mp4"))
	if err == nil {
		t.Fatal("expected error for nonexistent file")
	}
}

// --- CleanupTempFiles ---

func TestCleanupTempFiles(t *testing.T) {
	dir := t.TempDir()
	m, _ := NewManager(dir)
	m.EnsureCameraDir("cam-09")

	// Create some orphaned .tmp files
	tmpFile1 := filepath.Join(dir, "cam-09", "orphan1.tmp")
	tmpFile2 := filepath.Join(dir, "cam-09", "orphan2.tmp")
	os.WriteFile(tmpFile1, []byte("orphan"), 0o644)
	os.WriteFile(tmpFile2, []byte("orphan"), 0o644)

	// Create a normal file that should NOT be cleaned up
	temp, final, _ := m.CreateSegment("cam-09", "h264")
	m.WriteFrame(temp, []byte("keep"))
	m.CloseSegment(temp, final)

	if err := m.CleanupTempFiles(); err != nil {
		t.Fatalf("CleanupTempFiles error: %v", err)
	}

	// Orphaned .tmp files should be gone
	if _, err := os.Stat(tmpFile1); err == nil {
		t.Fatal("orphan1.tmp still exists after cleanup")
	}
	if _, err := os.Stat(tmpFile2); err == nil {
		t.Fatal("orphan2.tmp still exists after cleanup")
	}

	// Normal file should still exist
	if _, err := os.Stat(final); err != nil {
		t.Fatal("normal file was deleted by cleanup")
	}
}

func TestCleanupTempFiles_NoTempFiles(t *testing.T) {
	dir := t.TempDir()
	m, _ := NewManager(dir)

	// Should not error when no temp files exist
	if err := m.CleanupTempFiles(); err != nil {
		t.Fatalf("CleanupTempFiles should not error on empty dir: %v", err)
	}
}

// TestCleanupTempFiles_ScopesToCameraDirs verifies the scan is bounded to
// cam-* subtrees. The original implementation walked the entire tree, which on
// a 100k+ file production tree blocked startup for 20+ seconds. .tmp segment
// residue only ever appears under <root>/cam-xxx/YYYY/MM/DD/HH/ (see
// CreateSegment), so hls/, recordings/, bin/, certs/, and top-level files are
// out of scope and must be left untouched even if they happen to have a .tmp
// suffix.
func TestCleanupTempFiles_ScopesToCameraDirs(t *testing.T) {
	dir := t.TempDir()
	m, _ := NewManager(dir)

	// Camera subtree with an orphan .tmp that MUST be removed.
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "cam-test", "2026", "07", "20", "10"), 0o755))
	camTmp := filepath.Join(dir, "cam-test", "2026", "07", "20", "10", "orphan.tmp")
	require.NoError(t, os.WriteFile(camTmp, []byte("x"), 0o644))

	// Out-of-scope locations that must NOT be touched even with .tmp suffix.
	// These mimic real layout: hls shards, recordings dir, bin, certs, db files,
	// and a top-level .tmp config backup.
	outOfScope := []string{
		filepath.Join(dir, "hls", "cam-test", "segment.tmp"),
		filepath.Join(dir, "recordings", "stale.tmp"),
		filepath.Join(dir, "bin", "mibee-nvr.tmp"),
		filepath.Join(dir, "certs", "selfsigned.crt.tmp"),
		filepath.Join(dir, "config.yaml.tmp"), // top-level file with .tmp suffix
	}
	for _, p := range outOfScope {
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte("keep"), 0o644))
	}

	require.NoError(t, m.CleanupTempFiles())

	// In-scope: removed.
	_, err := os.Stat(camTmp)
	require.Error(t, err, "camera .tmp file should be removed")

	// Out-of-scope: all preserved.
	for _, p := range outOfScope {
		_, err := os.Stat(p)
		require.NoError(t, err, "out-of-scope .tmp file should NOT be removed: %s", p)
	}
}

// TestCleanupTempFiles_HandlesNonExistentRoot guards against a missing root
// (e.g. misconfigured storage.root_dir or an unmounted multi-volume root).
// Multi-root semantics: non-existent roots are skipped, not fatal — cleanup
// must neither panic nor fail because one root vanished.
func TestCleanupTempFiles_HandlesNonExistentRoot(t *testing.T) {
	m, _ := NewManager(t.TempDir())
	m.rootDir = filepath.Join(t.TempDir(), "does-not-exist")

	err := m.CleanupTempFiles()
	require.NoError(t, err, "missing root dir must be skipped, not an error")
}

// TestCleanupTempFiles_PreservesNormalFilesUnderCamera is a regression guard:
// the walk removes ONLY .tmp entries — normal .mp4 segments and MJPEG frame
// directories under cam-* must survive.
func TestCleanupTempFiles_PreservesNormalFilesUnderCamera(t *testing.T) {
	dir := t.TempDir()
	m, _ := NewManager(dir)
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "cam-1", "2026", "07", "20", "10"), 0o755))

	hourDir := filepath.Join(dir, "cam-1", "2026", "07", "20", "10")
	mp4Path := filepath.Join(hourDir, "cam-1_20260720_100000_123.mp4")
	mjpegDir := filepath.Join(hourDir, "cam-1_20260720_100001_124")
	require.NoError(t, os.WriteFile(mp4Path, []byte("mp4"), 0o644))
	require.NoError(t, os.MkdirAll(mjpegDir, 0o755))

	require.NoError(t, m.CleanupTempFiles())

	_, err := os.Stat(mp4Path)
	require.NoError(t, err, "normal .mp4 segment must survive cleanup")
	_, err = os.Stat(mjpegDir)
	require.NoError(t, err, "normal MJPEG frame dir must survive cleanup")
}

// --- IsAvailable ---

func TestIsAvailable_DirExists(t *testing.T) {
	dir := t.TempDir()
	m, _ := NewManager(dir)

	if !m.IsAvailable() {
		t.Fatal("IsAvailable should return true for existing dir")
	}
}

func TestIsAvailable_DirNotExist(t *testing.T) {
	m := &Manager{rootDir: "/tmp/nonexistent_mibee-nvr_dir_xyz"}

	if m.IsAvailable() {
		t.Fatal("IsAvailable should return false for nonexistent dir")
	}
}

func TestIsAvailable_AfterDelete(t *testing.T) {
	dir := t.TempDir()
	m, _ := NewManager(dir)

	// Remove the dir
	os.RemoveAll(dir)

	if m.IsAvailable() {
		t.Fatal("IsAvailable should return false after dir is removed")
	}
}

// --- GetDiskUsage ---

func TestGetDiskUsage(t *testing.T) {
	dir := t.TempDir()
	m, _ := NewManager(dir)

	total, used, err := m.GetDiskUsage()
	if err != nil {
		t.Fatalf("GetDiskUsage error: %v", err)
	}
	if total <= 0 {
		t.Fatalf("total disk space should be positive, got %d", total)
	}
	if used < 0 {
		t.Fatalf("used disk space should be non-negative, got %d", used)
	}
	if used > total {
		t.Fatalf("used (%d) should not exceed total (%d)", used, total)
	}
}

func TestGetDiskUsage_InvalidDir(t *testing.T) {
	m := &Manager{rootDir: "/tmp/nonexistent_mibee-nvr_disk_xyz"}

	_, _, err := m.GetDiskUsage()
	if err == nil {
		t.Fatal("expected error for nonexistent dir")
	}
}

// --- WriteFrame edge cases ---

func TestWriteFrame_AppendData(t *testing.T) {
	dir := t.TempDir()
	m, _ := NewManager(dir)
	m.EnsureCameraDir("cam-10")

	temp, final, _ := m.CreateSegment("cam-10", "h264")

	// Write multiple frames
	m.WriteFrame(temp, []byte("frame1"))
	m.WriteFrame(temp, []byte("frame2"))
	m.WriteFrame(temp, []byte("frame3"))

	m.CloseSegment(temp, final)

	content, err := os.ReadFile(final)
	if err != nil {
		t.Fatal(err)
	}
	expected := "frame1frame2frame3"
	if string(content) != expected {
		t.Fatalf("content mismatch: got %q, want %q", content, expected)
	}
}

func TestWriteFrame_AfterClose(t *testing.T) {
	dir := t.TempDir()
	m, _ := NewManager(dir)
	m.EnsureCameraDir("cam-11")

	temp, final, _ := m.CreateSegment("cam-11", "h264")
	m.WriteFrame(temp, []byte("data"))
	m.CloseSegment(temp, final)

	_, err := m.WriteFrame(temp, []byte("more"))
	if err == nil {
		t.Fatal("expected error writing to closed segment")
	}
}

// --- RootDir accessor ---

func TestManager_RootDir(t *testing.T) {
	dir := t.TempDir()
	m, _ := NewManager(dir)

	if m.RootDir() != dir {
		t.Fatalf("RootDir() = %q, want %q", m.RootDir(), dir)
	}
}

func TestReconcileOrphanedFiles_Basic(t *testing.T) {
	dir := t.TempDir()
	storeDir := filepath.Join(dir, "store")
	m, err := NewManager(storeDir)
	require.NoError(t, err)

	dbPath := filepath.Join(dir, "reconcile.db")
	db, err := New(dbPath)
	require.NoError(t, err)
	ctx := context.Background()
	require.NoError(t, db.Init(ctx))
	defer db.Close()

	// Insert camera into DB
	require.NoError(t, db.UpsertCamera(ctx, "test-cam-1", "Test Cam", "rtsp", "h264", "rtsp://host/stream", "", "", "", "", "", ""))

	// Create camera directory and MP4 files with correct naming pattern
	camDir := filepath.Join(storeDir, "test-cam-1")
	require.NoError(t, os.MkdirAll(camDir, 0o755))

	files := []string{
		"test-cam-1_20260514_120000_1234567890123456789.mp4",
		"test-cam-1_20260514_120100_1234567890123456790.mp4",
		"test-cam-1_20260514_120200_1234567890123456791.mp4",
	}
	for _, f := range files {
		require.NoError(t, os.WriteFile(filepath.Join(camDir, f), []byte("fake-mp4-data-123456"), 0o644))
	}

	cameraIDs := map[string]bool{"test-cam-1": true}
	count, err := m.ReconcileOrphanedFiles(ctx, db, cameraIDs)
	require.NoError(t, err)
	require.Equal(t, 3, count)

	// Verify recordings are in DB
	for _, f := range files {
		nanoStr := strings.TrimSuffix(f, ".mp4")
		parts := strings.SplitN(nanoStr, "_", 4)
		recID := parts[3]
		got, err := db.GetRecording(ctx, recID)
		require.NoError(t, err)
		require.NotNil(t, got, "recording for file %s should exist", f)
		require.Equal(t, "test-cam-1", got.CameraID)
	}
}

func TestReconcileOrphanedFiles_SkipsUnknownCamera(t *testing.T) {
	dir := t.TempDir()
	storeDir := filepath.Join(dir, "store")
	m, err := NewManager(storeDir)
	require.NoError(t, err)

	dbPath := filepath.Join(dir, "reconcile_unknown.db")
	db, err := New(dbPath)
	require.NoError(t, err)
	ctx := context.Background()
	require.NoError(t, db.Init(ctx))
	defer db.Close()

	// Do NOT insert camera into DB — it's unknown
	camDir := filepath.Join(storeDir, "unknown-cam")
	require.NoError(t, os.MkdirAll(camDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(camDir, "unknown-cam_20260514_120000_1234567890123456789.mp4"), []byte("data"), 0o644))

	cameraIDs := map[string]bool{} // empty — unknown-cam not recognized
	count, err := m.ReconcileOrphanedFiles(ctx, db, cameraIDs)
	require.NoError(t, err)
	require.Equal(t, 0, count)
}

func TestReconcileOrphanedFiles_SkipsNonMatching(t *testing.T) {
	dir := t.TempDir()
	storeDir := filepath.Join(dir, "store")
	m, err := NewManager(storeDir)
	require.NoError(t, err)

	dbPath := filepath.Join(dir, "reconcile_nomatch.db")
	db, err := New(dbPath)
	require.NoError(t, err)
	ctx := context.Background()
	require.NoError(t, db.Init(ctx))
	defer db.Close()

	require.NoError(t, db.UpsertCamera(ctx, "test-cam-1", "Test Cam", "rtsp", "h264", "rtsp://host/stream", "", "", "", "", "", ""))

	camDir := filepath.Join(storeDir, "test-cam-1")
	require.NoError(t, os.MkdirAll(camDir, 0o755))

	// Files with wrong pattern
	require.NoError(t, os.WriteFile(filepath.Join(camDir, "random_file.mp4"), []byte("data"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(camDir, "incomplete_.mp4"), []byte("data"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(camDir, "test-cam-1_onlydate.mp4"), []byte("data"), 0o644))

	cameraIDs := map[string]bool{"test-cam-1": true}
	count, err := m.ReconcileOrphanedFiles(ctx, db, cameraIDs)
	require.NoError(t, err)
	require.Equal(t, 0, count)
}

func TestReconcileOrphanedFiles_SkipsZeroByte(t *testing.T) {
	dir := t.TempDir()
	storeDir := filepath.Join(dir, "store")
	m, err := NewManager(storeDir)
	require.NoError(t, err)

	dbPath := filepath.Join(dir, "reconcile_zerobyte.db")
	db, err := New(dbPath)
	require.NoError(t, err)
	ctx := context.Background()
	require.NoError(t, db.Init(ctx))
	defer db.Close()

	require.NoError(t, db.UpsertCamera(ctx, "test-cam-1", "Test Cam", "rtsp", "h264", "rtsp://host/stream", "", "", "", "", "", ""))

	camDir := filepath.Join(storeDir, "test-cam-1")
	require.NoError(t, os.MkdirAll(camDir, 0o755))

	// Zero-byte file with correct naming
	require.NoError(t, os.WriteFile(filepath.Join(camDir, "test-cam-1_20260514_120000_1234567890123456789.mp4"), nil, 0o644))

	cameraIDs := map[string]bool{"test-cam-1": true}
	count, err := m.ReconcileOrphanedFiles(ctx, db, cameraIDs)
	require.NoError(t, err)
	require.Equal(t, 0, count)
}

func TestReconcileOrphanedFiles_Idempotent(t *testing.T) {
	dir := t.TempDir()
	storeDir := filepath.Join(dir, "store")
	m, err := NewManager(storeDir)
	require.NoError(t, err)

	dbPath := filepath.Join(dir, "reconcile_idem.db")
	db, err := New(dbPath)
	require.NoError(t, err)
	ctx := context.Background()
	require.NoError(t, db.Init(ctx))
	defer db.Close()

	require.NoError(t, db.UpsertCamera(ctx, "test-cam-1", "Test Cam", "rtsp", "h264", "rtsp://host/stream", "", "", "", "", "", ""))

	camDir := filepath.Join(storeDir, "test-cam-1")
	require.NoError(t, os.MkdirAll(camDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(camDir, "test-cam-1_20260514_120000_1234567890123456789.mp4"), []byte("fake-data-here-1234"), 0o644))

	cameraIDs := map[string]bool{"test-cam-1": true}

	// First run: should reconcile 1 file
	count1, err := m.ReconcileOrphanedFiles(ctx, db, cameraIDs)
	require.NoError(t, err)
	require.Equal(t, 1, count1)

	// Second run: same files, should reconcile 0 (already registered)
	count2, err := m.ReconcileOrphanedFiles(ctx, db, cameraIDs)
	require.NoError(t, err)
	require.Equal(t, 0, count2)
}

func TestReconcileOrphanedFiles_MJPEGDirs(t *testing.T) {
	dir := t.TempDir()
	storeDir := filepath.Join(dir, "store")
	m, err := NewManager(storeDir)
	require.NoError(t, err)

	dbPath := filepath.Join(dir, "reconcile_mjpeg.db")
	db, err := New(dbPath)
	require.NoError(t, err)
	ctx := context.Background()
	require.NoError(t, db.Init(ctx))
	defer db.Close()

	// Insert camera into DB
	require.NoError(t, db.UpsertCamera(ctx, "mjpeg-cam", "MJPEG Cam", "rtsp", "mjpeg", "rtsp://host/stream", "", "", "", "", "", ""))

	// Create camera directory and MJPEG segment dirs with correct naming
	camDir := filepath.Join(storeDir, "mjpeg-cam")
	require.NoError(t, os.MkdirAll(camDir, 0o755))

	mjpegDirs := []string{
		"mjpeg-cam_20260514_120000_1749897600000000001",
		"mjpeg-cam_20260514_120100_1749897660000000002",
	}
	for _, d := range mjpegDirs {
		segDir := filepath.Join(camDir, d)
		require.NoError(t, os.MkdirAll(segDir, 0o755))
		// Create JPEG frame files inside
		require.NoError(t, os.WriteFile(filepath.Join(segDir, "frame001.jpg"), []byte("fake-jpeg-data-12345"), 0o644))
		require.NoError(t, os.WriteFile(filepath.Join(segDir, "frame002.jpg"), []byte("fake-jpeg-data-67890"), 0o644))
		require.NoError(t, os.WriteFile(filepath.Join(segDir, "frame003.jpg"), []byte("fake-jpeg-data-11111"), 0o644))
	}

	cameraIDs := map[string]bool{"mjpeg-cam": true}
	count, err := m.ReconcileOrphanedFiles(ctx, db, cameraIDs)
	require.NoError(t, err)
	require.Equal(t, 2, count)

	// Verify recordings are in DB with correct MJPEG metadata
	for _, d := range mjpegDirs {
		parts := strings.SplitN(d, "_", 4)
		recID := parts[3]
		got, err := db.GetRecording(ctx, recID)
		require.NoError(t, err)
		require.NotNil(t, got, "recording for dir %s should exist", d)
		require.Equal(t, "mjpeg-cam", got.CameraID)
		require.Equal(t, model.FormatMJPEG, got.Format)
		require.Equal(t, 3, got.FrameCount)
		require.Equal(t, int64(60), got.FileSize) // 3 * 20 bytes per frame
		require.NotEqual(t, model.MergeStatusMerged, got.MergeStatus)
	}
}

func TestReconcileOrphanedFiles_MixedMP4AndMJPEG(t *testing.T) {
	dir := t.TempDir()
	storeDir := filepath.Join(dir, "store")
	m, err := NewManager(storeDir)
	require.NoError(t, err)

	dbPath := filepath.Join(dir, "reconcile_mixed.db")
	db, err := New(dbPath)
	require.NoError(t, err)
	ctx := context.Background()
	require.NoError(t, db.Init(ctx))
	defer db.Close()

	require.NoError(t, db.UpsertCamera(ctx, "cam-mix", "Mix Cam", "rtsp", "h264", "rtsp://host/stream", "", "", "", "", "", ""))

	camDir := filepath.Join(storeDir, "cam-mix")
	require.NoError(t, os.MkdirAll(camDir, 0o755))

	// Create an MP4 file
	require.NoError(t, os.WriteFile(filepath.Join(camDir, "cam-mix_20260514_120000_1234567890123456789.mp4"), []byte("fake-mp4-data-12345"), 0o644))

	// Create an MJPEG dir
	mjpegDir := filepath.Join(camDir, "cam-mix_20260514_120100_1234567890123456790")
	require.NoError(t, os.MkdirAll(mjpegDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(mjpegDir, "frame001.jpg"), []byte("jpeg-data-20b"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(mjpegDir, "frame002.jpg"), []byte("jpeg-data-20b"), 0o644))

	cameraIDs := map[string]bool{"cam-mix": true}
	count, err := m.ReconcileOrphanedFiles(ctx, db, cameraIDs)
	require.NoError(t, err)
	require.Equal(t, 2, count) // 1 MP4 + 1 MJPEG

	// Verify MP4 recording
	mp4Rec, err := db.GetRecording(ctx, "1234567890123456789")
	require.NoError(t, err)
	require.NotNil(t, mp4Rec)
	require.Equal(t, model.FormatH264, mp4Rec.Format)
	require.Equal(t, "cam-mix", mp4Rec.CameraID)

	// Verify MJPEG recording
	mjpegRec, err := db.GetRecording(ctx, "1234567890123456790")
	require.NoError(t, err)
	require.NotNil(t, mjpegRec)
	require.Equal(t, model.FormatMJPEG, mjpegRec.Format)
	require.Equal(t, "cam-mix", mjpegRec.CameraID)
	require.Equal(t, 2, mjpegRec.FrameCount)
	require.Equal(t, int64(26), mjpegRec.FileSize) // 2 * 13 bytes per frame
}

func TestReconcileOrphanedFiles_MJPEGEmptyDir(t *testing.T) {
	// Empty MJPEG dirs (no JPEG files) should be skipped
	dir := t.TempDir()
	storeDir := filepath.Join(dir, "store")
	m, err := NewManager(storeDir)
	require.NoError(t, err)

	dbPath := filepath.Join(dir, "reconcile_mjpeg_empty.db")
	db, err := New(dbPath)
	require.NoError(t, err)
	ctx := context.Background()
	require.NoError(t, db.Init(ctx))
	defer db.Close()

	require.NoError(t, db.UpsertCamera(ctx, "mjpeg-cam", "MJPEG Cam", "rtsp", "mjpeg", "rtsp://host/stream", "", "", "", "", "", ""))

	camDir := filepath.Join(storeDir, "mjpeg-cam")
	require.NoError(t, os.MkdirAll(camDir, 0o755))

	// Create an MJPEG dir with no JPEG files inside
	emptyDir := filepath.Join(camDir, "mjpeg-cam_20260514_120000_1749897600000000001")
	require.NoError(t, os.MkdirAll(emptyDir, 0o755))

	cameraIDs := map[string]bool{"mjpeg-cam": true}
	count, err := m.ReconcileOrphanedFiles(ctx, db, cameraIDs)
	require.NoError(t, err)
	require.Equal(t, 0, count)
}

func TestReconcileOrphanedFiles_MJPEGIdempotent(t *testing.T) {
	dir := t.TempDir()
	storeDir := filepath.Join(dir, "store")
	m, err := NewManager(storeDir)
	require.NoError(t, err)

	dbPath := filepath.Join(dir, "reconcile_mjpeg_idem.db")
	db, err := New(dbPath)
	require.NoError(t, err)
	ctx := context.Background()
	require.NoError(t, db.Init(ctx))
	defer db.Close()

	require.NoError(t, db.UpsertCamera(ctx, "mjpeg-cam", "MJPEG Cam", "rtsp", "mjpeg", "rtsp://host/stream", "", "", "", "", "", ""))

	camDir := filepath.Join(storeDir, "mjpeg-cam")
	require.NoError(t, os.MkdirAll(camDir, 0o755))

	mjpegDir := filepath.Join(camDir, "mjpeg-cam_20260514_120000_1749897600000000001")
	require.NoError(t, os.MkdirAll(mjpegDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(mjpegDir, "frame001.jpg"), []byte("jpeg-data"), 0o644))

	cameraIDs := map[string]bool{"mjpeg-cam": true}

	// First run
	count1, err := m.ReconcileOrphanedFiles(ctx, db, cameraIDs)
	require.NoError(t, err)
	require.Equal(t, 1, count1)

	// Second run: already registered
	count2, err := m.ReconcileOrphanedFiles(ctx, db, cameraIDs)
	require.NoError(t, err)
	require.Equal(t, 0, count2)
}

func TestReconcileOrphanedFiles_MJPEGSkipsRandomDirs(t *testing.T) {
	// Non-MJPEG dirs (e.g., .tmp dirs, system dirs) should be skipped
	dir := t.TempDir()
	storeDir := filepath.Join(dir, "store")
	m, err := NewManager(storeDir)
	require.NoError(t, err)

	dbPath := filepath.Join(dir, "reconcile_mjpeg_skip.db")
	db, err := New(dbPath)
	require.NoError(t, err)
	ctx := context.Background()
	require.NoError(t, db.Init(ctx))
	defer db.Close()

	require.NoError(t, db.UpsertCamera(ctx, "mjpeg-cam", "MJPEG Cam", "rtsp", "mjpeg", "rtsp://host/stream", "", "", "", "", "", ""))

	camDir := filepath.Join(storeDir, "mjpeg-cam")
	require.NoError(t, os.MkdirAll(camDir, 0o755))

	// Create dirs that should NOT be treated as MJPEG segments
	require.NoError(t, os.MkdirAll(filepath.Join(camDir, "some-random-dir"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(camDir, "mjpeg-cam_onlydate"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(camDir, "mjpeg-cam_20260514_120000"), 0o755)) // missing nano part
	require.NoError(t, os.MkdirAll(filepath.Join(camDir, "1234567890.tmp"), 0o755))            // has .tmp extension

	cameraIDs := map[string]bool{"mjpeg-cam": true}
	count, err := m.ReconcileOrphanedFiles(ctx, db, cameraIDs)
	require.NoError(t, err)
	require.Equal(t, 0, count)
}

// TestParseRecordingName is the authoritative regression test for the
// date-bucket-dir misclassification bug. The bug: reconcileCameraDir treated
// ANY extension-less directory under cam-* as an MJPEG recording dir and
// fully walked it. The segment writer's date bucket dirs ("202607/", "20/",
// "07/") are also extension-less, so each was walked recursively — every
// historical mp4 (540k+ files on production) got stat'd once per enclosing
// date dir, causing an 8.7GB / 6.5-minute startup IO storm.
//
// Fix: parseRecordingName is the cheap name-shape gate. Only names matching
// "<camID>_YYYYMMDD_HHMMSS_<nano>" (optionally + ".mp4") return ok=true.
// Date buckets and any other non-conforming names return ok=false WITHOUT
// touching the disk, so reconcileCameraDir never walks them.
//
// This test exercises the pure function directly — no IO, no timing, no
// flakiness. It is the reliable regression guard; the production deploy
// measurement (reconcile duration + read_bytes) is the authoritative
// end-to-end verification.
func TestParseRecordingName(t *testing.T) {
	const camID = "cam-4aeeef41-e379-4d93-b289-c3aedbe5d729"

	valid := []struct {
		name    string
		wantMP4 bool
	}{
		{camID + "_20260629_063758_1782686278819104131.mp4", true},
		{camID + "_20260629_063758_1782686278819104131", false}, // MJPEG dir
		{camID + "_20260720_100000_1111111111111111111.mp4", true},
	}
	for _, tc := range valid {
		got, ok := parseRecordingName(tc.name, camID)
		require.True(t, ok, "valid name rejected: %s", tc.name)
		require.Equal(t, camID, got.cameraID)
		require.Equal(t, tc.wantMP4, got.isMP4File)
		require.False(t, got.startedAt.IsZero(), "startedAt should parse")
		require.NotEmpty(t, got.nanoID)
	}

	invalid := []string{
		// Date bucket dirs from the segment writer layout — the primary bug.
		"202607",
		"20",
		"07",
		"202606",
		// MJPEG dirs missing the nano segment (only 3 parts).
		camID + "_20260629_063758",
		// Wrong camera ID.
		"cam-other_20260629_063758_1782686278819104131.mp4",
		// Bad date format.
		camID + "_notadate_063758_1782686278819104131.mp4",
		camID + "_20261329_063758_1782686278819104131.mp4", // month 13
		// Non-mp4 file with extension.
		camID + "_20260629_063758_1782686278819104131.avi",
		"config.yaml",
		"orphan.tmp",
		// Random junk.
		"",
		"foo",
		"a_b_c",
	}
	for _, name := range invalid {
		_, ok := parseRecordingName(name, camID)
		require.False(t, ok, "invalid name accepted (would cause spurious stat/walk): %q", name)
	}

	// cameraIDHint="" should skip the camera-ID gate but still validate shape.
	_, ok := parseRecordingName("cam-other_20260629_063758_1782686278819104131.mp4", "")
	require.True(t, ok, "empty cameraIDHint should accept any camera prefix")
	_, ok = parseRecordingName("202607", "")
	require.False(t, ok, "date dir must be rejected even with empty cameraIDHint")
}

// TestReconcileOrphanedFiles_SkipsDateBucketDirs is the integration-level
// counterpart to TestParseRecordingName: it seeds a realistic nested layout
// (date bucket dirs with mp4 files inside, plus legit flat orphans) and
// asserts only the legit orphans are reconciled. It does NOT assert timing —
// that's the pure-function test's job — but verifies the end-to-end behavior.
func TestReconcileOrphanedFiles_SkipsDateBucketDirs(t *testing.T) {
	dir := t.TempDir()
	storeDir := filepath.Join(dir, "store")
	m, err := NewManager(storeDir)
	require.NoError(t, err)

	dbPath := filepath.Join(dir, "reconcile_dates.db")
	db, err := New(dbPath)
	require.NoError(t, err)
	ctx := context.Background()
	require.NoError(t, db.Init(ctx))
	defer db.Close()

	require.NoError(t, db.UpsertCamera(ctx, "cam-dates", "Date Cam", "rtsp", "h264", "rtsp://x", "", "", "", "", "", ""))
	cameraIDs := map[string]bool{"cam-dates": true}

	camDir := filepath.Join(storeDir, "cam-dates")
	require.NoError(t, os.MkdirAll(camDir, 0o755))

	// (A) Nested current-layout mp4 files under a date bucket. Must NOT
	// reconcile — date dirs are out of scope (registered via CloseSegment).
	nestedHour := filepath.Join(camDir, "202607", "20", "10")
	require.NoError(t, os.MkdirAll(nestedHour, 0o755))
	nestedNames := []string{
		"cam-dates_20260720_100000_1111111111111111111.mp4",
		"cam-dates_20260720_100100_2222222222222222222.mp4",
		"cam-dates_20260720_100200_3333333333333333333.mp4",
	}
	for _, n := range nestedNames {
		require.NoError(t, os.WriteFile(filepath.Join(nestedHour, n), []byte("x"), 0o644))
	}

	// (B) A legit legacy-layout flat mp4 at cam dir top level. SHOULD reconcile.
	flatName := "cam-dates_20260629_100000_4444444444444444444.mp4"
	require.NoError(t, os.WriteFile(filepath.Join(camDir, flatName), []byte("flat-mp4"), 0o644))

	// (C) A legit MJPEG recording dir at cam dir top level. SHOULD reconcile.
	mjpegDir := "cam-dates_20260629_100100_5555555555555555555"
	require.NoError(t, os.MkdirAll(filepath.Join(camDir, mjpegDir), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(camDir, mjpegDir, "frame_0000.jpg"), []byte("jpg"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(camDir, mjpegDir, "frame_0001.jpg"), []byte("jpg"), 0o644))

	count, err := m.ReconcileOrphanedFiles(ctx, db, cameraIDs)
	require.NoError(t, err)
	// Exactly 2: the flat mp4 + the legit MJPEG dir. The 3 nested mp4 inside
	// date buckets must NOT be counted.
	require.Equal(t, 2, count, "nested date-bucket mp4 files must not be reconciled")

	// Spot-check: the nested mp4s are NOT in DB.
	for _, n := range nestedNames {
		nano := strings.SplitN(strings.TrimSuffix(n, ".mp4"), "_", 4)[3]
		got, err := db.GetRecording(ctx, nano)
		require.NoError(t, err)
		require.Nil(t, got, "nested date-bucket file %q must not be reconciled into DB", n)
	}
}

// TestReconcileIncrementalCommit verifies that ReconcileOrphanedFiles processes each camera
// directory independently with per-camera commits, allowing partial progress if interrupted.
func TestReconcileIncrementalCommit(t *testing.T) {
	dir := t.TempDir()
	storeDir := filepath.Join(dir, "store")
	m, err := NewManager(storeDir)
	require.NoError(t, err)

	dbPath := filepath.Join(dir, "reconcile_incr.db")
	db, err := New(dbPath)
	require.NoError(t, err)
	ctx := context.Background()
	require.NoError(t, db.Init(ctx))
	defer db.Close()

	// Create 3 cameras with 10 files each
	cameraIDs := map[string]bool{}
	fileCount := 10
	expectedTotal := 0
	for camIdx := 1; camIdx <= 3; camIdx++ {
		camID := fmt.Sprintf("cam-%d", camIdx)
		cameraIDs[camID] = true
		require.NoError(t, db.UpsertCamera(ctx, camID, "Cam "+camID, "rtsp", "h264", "rtsp://host/stream", "", "", "", "", "", ""))

		camDir := filepath.Join(storeDir, camID)
		require.NoError(t, os.MkdirAll(camDir, 0o755))

		for fileIdx := range fileCount {
			// Format: cameraID_20260514_120000_<nanotimestamp>.mp4
			// Use unique timestamps so each file gets a unique ID
			dateTime := fmt.Sprintf("20260514_%02d%02d%02d", 9+camIdx, 0, fileIdx)
			nano := fmt.Sprintf("%019d", int64(camIdx)*1000000000000000000+int64(fileIdx))
			fileName := fmt.Sprintf("%s_%s_%s.mp4", camID, dateTime, nano)
			require.NoError(t, os.WriteFile(filepath.Join(camDir, fileName), []byte("fake-mp4-data"), 0o644))
			expectedTotal++
		}
	}

	// First run: all files should be reconciled
	count, err := m.ReconcileOrphanedFiles(ctx, db, cameraIDs)
	require.NoError(t, err)
	require.Equal(t, expectedTotal, count, "all files should be reconciled on first run")

	// Verify all recordings exist in DB
	for camID := range cameraIDs {
		list, err := db.ListRecordings(ctx, model.RecordingFilter{CameraID: camID})
		require.NoError(t, err)
		require.Len(t, list, fileCount, "camera %q should have %d recordings", camID, fileCount)
	}

	// Second run: idempotent — should reconcile 0 new files
	count, err = m.ReconcileOrphanedFiles(ctx, db, cameraIDs)
	require.NoError(t, err)
	require.Equal(t, 0, count, "second reconcile should find no orphans")
}

// TestReconcilePerCameraCommit verifies that ReconcileOrphanedFiles commits per camera directory,
// so a partial reconcile (limited camera IDs) can be completed incrementally.
func TestReconcilePerCameraCommit(t *testing.T) {
	dir := t.TempDir()
	storeDir := filepath.Join(dir, "store")
	m, err := NewManager(storeDir)
	require.NoError(t, err)

	dbPath := filepath.Join(dir, "reconcile_partial.db")
	db, err := New(dbPath)
	require.NoError(t, err)
	ctx := context.Background()
	require.NoError(t, db.Init(ctx))
	defer db.Close()

	// Create 3 cameras with 5 files each
	for camIdx := 1; camIdx <= 3; camIdx++ {
		camID := fmt.Sprintf("cam-%d", camIdx)
		require.NoError(t, db.UpsertCamera(ctx, camID, "Cam "+camID, "rtsp", "h264", "rtsp://host/stream", "", "", "", "", "", ""))

		camDir := filepath.Join(storeDir, camID)
		require.NoError(t, os.MkdirAll(camDir, 0o755))

		for fileIdx := range 5 {
			dateTime := fmt.Sprintf("20260514_%02d%02d%02d", 9+camIdx, 0, fileIdx)
			nano := fmt.Sprintf("%019d", int64(camIdx)*1000000000000000000+int64(fileIdx))
			fileName := fmt.Sprintf("%s_%s_%s.mp4", camID, dateTime, nano)
			require.NoError(t, os.WriteFile(filepath.Join(camDir, fileName), []byte("fake-mp4-data"), 0o644))
		}
	}

	// Step 1: reconcile only camera 1
	count, err := m.ReconcileOrphanedFiles(ctx, db, map[string]bool{"cam-1": true})
	require.NoError(t, err)
	require.Equal(t, 5, count, "camera 1 should have 5 files reconciled")

	// Step 2: reconcile all 3 cameras — should only pick up cameras 2 and 3
	count, err = m.ReconcileOrphanedFiles(ctx, db, map[string]bool{"cam-1": true, "cam-2": true, "cam-3": true})
	require.NoError(t, err)
	require.Equal(t, 10, count, "cameras 2 and 3 should have 10 files reconciled total")

	// Verify all recordings exist
	for _, camID := range []string{"cam-1", "cam-2", "cam-3"} {
		list, err := db.ListRecordings(ctx, model.RecordingFilter{CameraID: camID})
		require.NoError(t, err)
		require.Len(t, list, 5, "camera %q should have 5 recordings", camID)
	}

	// Step 3: third reconcile should find nothing
	count, err = m.ReconcileOrphanedFiles(ctx, db, map[string]bool{"cam-1": true, "cam-2": true, "cam-3": true})
	require.NoError(t, err)
	require.Equal(t, 0, count, "third reconcile should be idempotent")
}

// TestInsertOrphanRecordingsBatching verifies that InsertOrphanRecordings commits in batches
// of orphanBatchSize and returns the correct inserted count.
func TestInsertOrphanRecordingsBatching(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "orphan_batch.db")
	db, err := New(dbPath)
	require.NoError(t, err)
	ctx := context.Background()
	require.NoError(t, db.Init(ctx))
	defer db.Close()

	// Insert a camera so foreign key constraint works (if any)
	require.NoError(t, db.UpsertCamera(ctx, "batch-cam", "Batch Cam", "rtsp", "h264", "rtsp://host/stream", "", "", "", "", "", ""))

	// Create 1200 orphan recordings (2 full batches of 500 + 1 partial batch of 200)
	// orphanBatchSize = 500, so: 500 + 500 + 200 = 1200
	var recs []*model.Recording
	for i := range 1200 {
		recs = append(recs, &model.Recording{
			ID:          fmt.Sprintf("batch-rec-%d", i),
			CameraID:    "batch-cam",
			FilePath:    fmt.Sprintf("/path/file_%d.mp4", i),
			Format:      model.FormatH264,
			StartedAt:   time.Now(),
			EndedAt:     time.Now().Add(time.Minute),
			Duration:    60,
			FileSize:    1024,
			FrameCount:  30,
			MergeStatus: model.MergeStatusPending,
		})
	}

	inserted, err := db.InsertOrphanRecordings(ctx, recs)
	require.NoError(t, err)
	require.Equal(t, 1200, inserted, "all 1200 orphan recordings should be inserted")

	// Verify all are in the DB
	list, err := db.ListRecordings(ctx, model.RecordingFilter{CameraID: "batch-cam"})
	require.NoError(t, err)
	require.Len(t, list, 1200, "all 1200 recordings should exist in DB")

	// Second insert should be idempotent (INSERT OR IGNORE)
	inserted, err = db.InsertOrphanRecordings(ctx, recs)
	require.NoError(t, err)
	require.Equal(t, 0, inserted, "second insert should find no new records")
}

// A vanished temp segment (removed by the stale-segment cleanup or lost in a
// restart window) must NOT count toward storage health. Counting it drove a
// production deadlock: three vanished-tmp closes during restart ramp-up
// escalated a healthy camera to Failed, the recorders' skip-before-write
// behavior then blocked the successful write that resets the state, and the
// camera stopped recording until the next restart.
func TestCloseSegment_VanishedTempDoesNotTripHealth(t *testing.T) {
	dir := t.TempDir()
	m, _ := NewManager(dir)
	m.EnsureCameraDir("cam-latch")

	// Create and register a real segment, then delete the temp file out from
	// under the closer — the observed production scenario.
	tempPath, finalPath, err := m.CreateSegment("cam-latch", "h264")
	if err != nil {
		t.Fatalf("CreateSegment error: %v", err)
	}
	if err := os.Remove(tempPath); err != nil {
		t.Fatalf("Remove temp: %v", err)
	}

	if err := m.CloseSegment(tempPath, finalPath); err == nil {
		t.Fatal("CloseSegment must still report the lost segment")
	}

	// Repeat far past the failure threshold: health must stay clean.
	for range maxConsecutiveFailures * 2 {
		_ = m.CloseSegment(tempPath, finalPath)
	}
	if m.StorageFailedLegacy() {
		t.Fatal("vanished temp segments must not trip storage health")
	}
	if m.StorageHealth("cam-latch") != HealthHealthy {
		t.Fatal("camera health must remain healthy after vanished-tmp closes")
	}
}

// WriteFrame on a vanished temp must not feed the health tracker either —
// the recorder-side reconnect loop used to hammer the missing tmp until the
// camera escalated to Failed (#413).
func TestWriteFrame_VanishedTempDoesNotTripHealth(t *testing.T) {
	dir := t.TempDir()
	m, _ := NewManager(dir)
	m.EnsureCameraDir("cam-wf")

	tempPath, _, err := m.CreateSegment("cam-wf", "h264")
	if err != nil {
		t.Fatalf("CreateSegment error: %v", err)
	}
	if err := os.Remove(tempPath); err != nil {
		t.Fatalf("Remove temp: %v", err)
	}

	for range maxConsecutiveFailures * 2 {
		if _, err := m.WriteFrame(tempPath, []byte("x")); err == nil {
			t.Fatal("WriteFrame must still report the missing temp")
		}
	}
	if m.StorageFailedLegacy() {
		t.Fatal("vanished temp segments must not trip storage health on write")
	}
	if m.StorageHealth("cam-wf") != HealthHealthy {
		t.Fatal("camera health must remain healthy after vanished-tmp writes")
	}
}
