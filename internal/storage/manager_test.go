package storage

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
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

	for i := 0; i < 5; i++ {
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
	os.WriteFile(tmpFile1, []byte("orphan"), 0644)
	os.WriteFile(tmpFile2, []byte("orphan"), 0644)

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
	require.NoError(t, db.UpsertCamera(ctx, "test-cam-1", "Test Cam", "rtsp", "h264", "rtsp://host/stream", "", "", "", "", ""))

	// Create camera directory and MP4 files with correct naming pattern
	camDir := filepath.Join(storeDir, "test-cam-1")
	require.NoError(t, os.MkdirAll(camDir, 0755))

	files := []string{
		"test-cam-1_20260514_120000_1234567890123456789.mp4",
		"test-cam-1_20260514_120100_1234567890123456790.mp4",
		"test-cam-1_20260514_120200_1234567890123456791.mp4",
	}
	for _, f := range files {
		require.NoError(t, os.WriteFile(filepath.Join(camDir, f), []byte("fake-mp4-data-123456"), 0644))
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
	require.NoError(t, os.MkdirAll(camDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(camDir, "unknown-cam_20260514_120000_1234567890123456789.mp4"), []byte("data"), 0644))

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

	require.NoError(t, db.UpsertCamera(ctx, "test-cam-1", "Test Cam", "rtsp", "h264", "rtsp://host/stream", "", "", "", "", ""))

	camDir := filepath.Join(storeDir, "test-cam-1")
	require.NoError(t, os.MkdirAll(camDir, 0755))

	// Files with wrong pattern
	require.NoError(t, os.WriteFile(filepath.Join(camDir, "random_file.mp4"), []byte("data"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(camDir, "incomplete_.mp4"), []byte("data"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(camDir, "test-cam-1_onlydate.mp4"), []byte("data"), 0644))

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

	require.NoError(t, db.UpsertCamera(ctx, "test-cam-1", "Test Cam", "rtsp", "h264", "rtsp://host/stream", "", "", "", "", ""))

	camDir := filepath.Join(storeDir, "test-cam-1")
	require.NoError(t, os.MkdirAll(camDir, 0755))

	// Zero-byte file with correct naming
	require.NoError(t, os.WriteFile(filepath.Join(camDir, "test-cam-1_20260514_120000_1234567890123456789.mp4"), nil, 0644))

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

	require.NoError(t, db.UpsertCamera(ctx, "test-cam-1", "Test Cam", "rtsp", "h264", "rtsp://host/stream", "", "", "", "", ""))

	camDir := filepath.Join(storeDir, "test-cam-1")
	require.NoError(t, os.MkdirAll(camDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(camDir, "test-cam-1_20260514_120000_1234567890123456789.mp4"), []byte("fake-data-here-1234"), 0644))

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
	require.NoError(t, db.UpsertCamera(ctx, "mjpeg-cam", "MJPEG Cam", "rtsp", "mjpeg", "rtsp://host/stream", "", "", "", "", ""))

	// Create camera directory and MJPEG segment dirs with correct naming
	camDir := filepath.Join(storeDir, "mjpeg-cam")
	require.NoError(t, os.MkdirAll(camDir, 0755))

	mjpegDirs := []string{
		"mjpeg-cam_20260514_120000_1749897600000000001",
		"mjpeg-cam_20260514_120100_1749897660000000002",
	}
	for _, d := range mjpegDirs {
		segDir := filepath.Join(camDir, d)
		require.NoError(t, os.MkdirAll(segDir, 0755))
		// Create JPEG frame files inside
		require.NoError(t, os.WriteFile(filepath.Join(segDir, "frame001.jpg"), []byte("fake-jpeg-data-12345"), 0644))
		require.NoError(t, os.WriteFile(filepath.Join(segDir, "frame002.jpg"), []byte("fake-jpeg-data-67890"), 0644))
		require.NoError(t, os.WriteFile(filepath.Join(segDir, "frame003.jpg"), []byte("fake-jpeg-data-11111"), 0644))
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
		require.Equal(t, false, got.Merged)
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

	require.NoError(t, db.UpsertCamera(ctx, "cam-mix", "Mix Cam", "rtsp", "h264", "rtsp://host/stream", "", "", "", "", ""))

	camDir := filepath.Join(storeDir, "cam-mix")
	require.NoError(t, os.MkdirAll(camDir, 0755))

	// Create an MP4 file
	require.NoError(t, os.WriteFile(filepath.Join(camDir, "cam-mix_20260514_120000_1234567890123456789.mp4"), []byte("fake-mp4-data-12345"), 0644))

	// Create an MJPEG dir
	mjpegDir := filepath.Join(camDir, "cam-mix_20260514_120100_1234567890123456790")
	require.NoError(t, os.MkdirAll(mjpegDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(mjpegDir, "frame001.jpg"), []byte("jpeg-data-20b"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(mjpegDir, "frame002.jpg"), []byte("jpeg-data-20b"), 0644))

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

	require.NoError(t, db.UpsertCamera(ctx, "mjpeg-cam", "MJPEG Cam", "rtsp", "mjpeg", "rtsp://host/stream", "", "", "", "", ""))

	camDir := filepath.Join(storeDir, "mjpeg-cam")
	require.NoError(t, os.MkdirAll(camDir, 0755))

	// Create an MJPEG dir with no JPEG files inside
	emptyDir := filepath.Join(camDir, "mjpeg-cam_20260514_120000_1749897600000000001")
	require.NoError(t, os.MkdirAll(emptyDir, 0755))

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

	require.NoError(t, db.UpsertCamera(ctx, "mjpeg-cam", "MJPEG Cam", "rtsp", "mjpeg", "rtsp://host/stream", "", "", "", "", ""))

	camDir := filepath.Join(storeDir, "mjpeg-cam")
	require.NoError(t, os.MkdirAll(camDir, 0755))

	mjpegDir := filepath.Join(camDir, "mjpeg-cam_20260514_120000_1749897600000000001")
	require.NoError(t, os.MkdirAll(mjpegDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(mjpegDir, "frame001.jpg"), []byte("jpeg-data"), 0644))

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

	require.NoError(t, db.UpsertCamera(ctx, "mjpeg-cam", "MJPEG Cam", "rtsp", "mjpeg", "rtsp://host/stream", "", "", "", "", ""))

	camDir := filepath.Join(storeDir, "mjpeg-cam")
	require.NoError(t, os.MkdirAll(camDir, 0755))

	// Create dirs that should NOT be treated as MJPEG segments
	require.NoError(t, os.MkdirAll(filepath.Join(camDir, "some-random-dir"), 0755))
	require.NoError(t, os.MkdirAll(filepath.Join(camDir, "mjpeg-cam_onlydate"), 0755))
	require.NoError(t, os.MkdirAll(filepath.Join(camDir, "mjpeg-cam_20260514_120000"), 0755)) // missing nano part
	require.NoError(t, os.MkdirAll(filepath.Join(camDir, "1234567890.tmp"), 0755)) // has .tmp extension

	cameraIDs := map[string]bool{"mjpeg-cam": true}
	count, err := m.ReconcileOrphanedFiles(ctx, db, cameraIDs)
	require.NoError(t, err)
	require.Equal(t, 0, count)
}
