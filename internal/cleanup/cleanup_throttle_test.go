// Directory-form deletion pacing tests (#748): MJPEG/timelapse frame-tree
// recordings must be unlinked in paced chunks so the ext4 journal (jbd2)
// keeps up, while plain-file recordings keep the legacy unpaced path.
package cleanup

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
)

// TestBatchDeleteRecordingsWithFiles_DirThrottlePacing deletes an MJPEG
// frame directory with pacing enabled and verifies the recording is fully
// removed and the chunk sleeps actually happened.
func TestBatchDeleteRecordingsWithFiles_DirThrottlePacing(t *testing.T) {
	env := newTestEnv(t)
	defer env.close(t)

	cm, err := NewCleanupManager(env.db, env.store, defaultCleanupConfig())
	if err != nil {
		t.Fatalf("NewCleanupManager: %v", err)
	}
	cm.SetDirectoryDeleteThrottle(3, 25*time.Millisecond)

	dir := filepath.Join(env.store.RootDir(), "cam1", "frames")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for i := range 10 {
		p := filepath.Join(dir, fmt.Sprintf("frame_%06d.jpg", i))
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	rec := &model.Recording{
		ID: "mjpeg-1", CameraID: "cam1", FilePath: dir, Format: model.FormatMJPEG,
		StartedAt: now.Add(-2 * time.Hour), EndedAt: now.Add(-time.Hour),
		MergeStatus: model.MergeStatusPending,
	}
	if err := env.db.InsertRecording(context.Background(), rec); err != nil {
		t.Fatalf("InsertRecording: %v", err)
	}

	start := time.Now()
	deleted, err := cm.BatchDeleteRecordingsWithFiles(context.Background(), []model.Recording{*rec}, "timelapse_source_merged")
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("BatchDeleteRecordingsWithFiles: %v", err)
	}
	if len(deleted) != 1 || deleted[0] != "mjpeg-1" {
		t.Fatalf("deleted = %v, want [mjpeg-1]", deleted)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("frame dir still exists after paced deletion")
	}
	// 10 files with chunk 3 → sleeps after files 3, 6 and 9 → ≥ 3 × 25ms.
	if elapsed < 60*time.Millisecond {
		t.Errorf("paced deletion finished in %v — chunk sleeps did not happen", elapsed)
	}
}

// TestDeleteRecordingFile_ThrottleDisabledKeepsLegacyPath verifies the
// unpaced fallback: with no throttle configured a directory goes through the
// storage manager's RemoveAll (fast, no pacing).
func TestDeleteRecordingFile_ThrottleDisabledKeepsLegacyPath(t *testing.T) {
	env := newTestEnv(t)
	defer env.close(t)

	cm, err := NewCleanupManager(env.db, env.store, defaultCleanupConfig())
	if err != nil {
		t.Fatalf("NewCleanupManager: %v", err)
	}

	dir := filepath.Join(env.store.RootDir(), "cam1", "frames")
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sub", "frame.jpg"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := cm.deleteRecordingFile(context.Background(), dir); err != nil {
		t.Fatalf("deleteRecordingFile: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("dir still exists after unpaced deletion")
	}
}

// TestDeleteRecordingFile_PlainFileWithThrottleEnabled verifies plain files
// bypass the paced walk (storage-manager semantics, incl. sidecars).
func TestDeleteRecordingFile_PlainFileWithThrottleEnabled(t *testing.T) {
	env := newTestEnv(t)
	defer env.close(t)

	cm, err := NewCleanupManager(env.db, env.store, defaultCleanupConfig())
	if err != nil {
		t.Fatalf("NewCleanupManager: %v", err)
	}
	cm.SetDirectoryDeleteThrottle(3, 25*time.Millisecond)

	file := filepath.Join(env.store.RootDir(), "plain.mp4")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := cm.deleteRecordingFile(context.Background(), file); err != nil {
		t.Fatalf("deleteRecordingFile: %v", err)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Errorf("plain file still exists")
	}
}

// TestDeleteRecordingFile_PacedCtxCancel verifies a cancelled context stops
// the paced walk (partial tree left for the orphan scanner) instead of
// spinning forever.
func TestDeleteRecordingFile_PacedCtxCancel(t *testing.T) {
	env := newTestEnv(t)
	defer env.close(t)

	cm, err := NewCleanupManager(env.db, env.store, defaultCleanupConfig())
	if err != nil {
		t.Fatalf("NewCleanupManager: %v", err)
	}
	cm.SetDirectoryDeleteThrottle(2, 50*time.Millisecond)

	dir := filepath.Join(env.store.RootDir(), "cam1", "frames")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for i := range 6 {
		p := filepath.Join(dir, fmt.Sprintf("frame_%06d.jpg", i))
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := cm.deleteRecordingFile(ctx, dir); err == nil {
		t.Fatal("expected error from cancelled paced walk")
	}
}
