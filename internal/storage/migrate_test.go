package storage

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// createTestJPEG generates a minimal valid JPEG with the given dimensions.
// The output starts with FF D8 (SOI), has a SOF0 marker with w/h, and ends
// with FF D9 (EOI).
func createTestJPEG(width, height int) []byte {
	b := make([]byte, 0, 23)
	b = append(b, 0xFF, 0xD8) // SOI
	b = append(b, 0xFF, 0xC0) // SOF0
	b = append(b, 0, 11)      // length
	b = append(b, 8)          // precision
	b = append(b, byte(height>>8), byte(height&0xFF))
	b = append(b, byte(width>>8), byte(width&0xFF))
	b = append(b, 1) // components
	b = append(b, 1, 0x11, 0)
	b = append(b, 0xFF, 0xD9) // EOI
	return b
}

// createTestMJPEGDir creates a temp directory containing numbered .jpg frames.
// Returns the directory path.
func createTestMJPEGDir(t *testing.T, parentDir, baseName string, frameCount int) string {
	t.Helper()
	dir := filepath.Join(parentDir, baseName)
	require.NoError(t, os.MkdirAll(dir, 0o755))

	for i := range frameCount {
		name := filepath.Join(dir, "20060102_150405."+threeDigit(i)+".jpg")
		require.NoError(t, os.WriteFile(name, createTestJPEG(640, 480), 0o644))
	}
	return dir
}

func threeDigit(i int) string {
	s := fmt.Sprintf("%03d", i)
	return s
}

// insertMJPEGRecording inserts a legacy MJPEG recording into the test DB.
func insertMJPEGRecording(t *testing.T, db *DB, ctx context.Context, id, cameraID, filePath string, startedAt time.Time) {
	t.Helper()
	err := db.InsertRecording(ctx, &model.Recording{
		ID:        id,
		CameraID:  cameraID,
		FilePath:  filePath,
		Format:    model.FormatMJPEG,
		StartedAt: startedAt,
		EndedAt:   startedAt.Add(30 * time.Second),
		Duration:  30,
		FileSize:  0,
	})
	require.NoError(t, err)
}

// setupMigrateTest creates a DB + Manager + a single MJPEG recording for testing.
func setupMigrateTest(t *testing.T) (*DB, *Manager, context.Context, string) {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	db, err := New(dbPath)
	require.NoError(t, err)
	ctx := context.Background()
	require.NoError(t, db.Init(ctx))

	store, err := NewManager(dir)
	require.NoError(t, err)

	return db, store, ctx, dir
}

// ---------------------------------------------------------------------------
// Tests: verifyAVI
// ---------------------------------------------------------------------------

func TestValidJPEG(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		want bool
	}{
		{"valid minimal", []byte{0xFF, 0xD8, 0x01, 0x02, 0xFF, 0xD9}, true},
		{"too short", []byte{0xFF, 0xD8, 0xFF}, false},
		{"no SOI", []byte{0x00, 0x00, 0xFF, 0xD9}, false},
		{"no EOI", []byte{0xFF, 0xD8, 0x00, 0x00}, false},
		{"empty", []byte{}, false},
		{"test JPEG", createTestJPEG(320, 240), true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := validJPEG(tt.data)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestJPEGDimensions(t *testing.T) {
	w, h, ok := jpegDimensions(createTestJPEG(640, 480))
	require.True(t, ok)
	require.Equal(t, 640, w)
	require.Equal(t, 480, h)

	_, _, ok = jpegDimensions([]byte{0xFF, 0xD8, 0xFF, 0xD9})
	require.False(t, ok)
}

// ---------------------------------------------------------------------------
// Tests: migrateOneRecording  (integration with real avi muxer/demuxer)
// ---------------------------------------------------------------------------

func TestMigrateOneRecording_Success(t *testing.T) {
	db, store, ctx, dir := setupMigrateTest(t)
	defer db.Close()

	// Create an MJPEG directory with 10 frames.
	mjpegDir := createTestMJPEGDir(t, dir, "cam1_rec_001", 10)

	rec := model.Recording{
		ID:        "rec-001",
		CameraID:  "cam1",
		FilePath:  mjpegDir,
		Format:    model.FormatMJPEG,
		StartedAt: time.Now(),
	}

	// Insert the recording into DB before migration.
	err := db.InsertRecording(ctx, &rec)
	require.NoError(t, err)

	log := logger
	err = migrateOneRecording(ctx, db, store, rec, log)
	require.NoError(t, err)

	// Verify the .avi file was created.
	aviPath := mjpegDir + ".avi"
	info, err := os.Stat(aviPath)
	require.NoError(t, err)
	require.Greater(t, info.Size(), int64(0))

	// Verify the original MJPEG directory was removed.
	_, err = os.Stat(mjpegDir)
	require.True(t, os.IsNotExist(err))

	// Verify DB was updated.
	updated, err := db.GetRecording(ctx, "rec-001")
	require.NoError(t, err)
	require.NotNil(t, updated)
	require.Equal(t, model.FormatAVI, updated.Format)
	require.Equal(t, aviPath, updated.FilePath)
	require.Greater(t, updated.FileSize, int64(0))
}

func TestMigrateOneRecording_EmptyDir(t *testing.T) {
	db, store, ctx, dir := setupMigrateTest(t)
	defer db.Close()

	// Create an empty directory.
	emptyDir := filepath.Join(dir, "empty")
	require.NoError(t, os.MkdirAll(emptyDir, 0o755))

	rec := model.Recording{
		ID:       "rec-empty",
		CameraID: "cam1",
		FilePath: emptyDir,
		Format:   model.FormatMJPEG,
	}
	err := migrateOneRecording(ctx, db, store, rec, logger)
	require.ErrorIs(t, err, errSkipped)
}

func TestMigrateOneRecording_MissingDir(t *testing.T) {
	db, store, ctx, dir := setupMigrateTest(t)
	defer db.Close()

	rec := model.Recording{
		ID:       "rec-missing",
		CameraID: "cam1",
		FilePath: filepath.Join(dir, "nonexistent"),
		Format:   model.FormatMJPEG,
	}
	err := migrateOneRecording(ctx, db, store, rec, logger)
	require.ErrorIs(t, err, errSkipped)
}

// ---------------------------------------------------------------------------
// Tests: MigrateMJPEGToAVI end-to-end
// ---------------------------------------------------------------------------

func TestMigrateMJPEGToAVI_HappyPath(t *testing.T) {
	db, store, ctx, dir := setupMigrateTest(t)
	defer db.Close()

	// Create a single MJPEG recording (within cutoff = recent).
	mjpegDir := createTestMJPEGDir(t, dir, "cam1/rec_001", 5)
	insertMJPEGRecording(t, db, ctx, "r1", "cam1", mjpegDir, time.Now())

	opts := MigrateOptions{
		Cutoff:      72 * time.Hour,
		Concurrency: 1,
		DryRun:      false,
		PurgeOld:    false,
	}

	err := MigrateMJPEGToAVI(ctx, db, store, opts)
	require.NoError(t, err)

	// Verify the recording was migrated.
	updated, err := db.GetRecording(ctx, "r1")
	require.NoError(t, err)
	require.NotNil(t, updated)
	require.Equal(t, model.FormatAVI, updated.Format)
}

func TestMigrateMJPEGToAVI_DryRun(t *testing.T) {
	db, store, ctx, dir := setupMigrateTest(t)
	defer db.Close()

	mjpegDir := createTestMJPEGDir(t, dir, "cam1/rec_dry", 3)
	insertMJPEGRecording(t, db, ctx, "r-dry", "cam1", mjpegDir, time.Now())

	opts := MigrateOptions{
		Cutoff: 72 * time.Hour,
		DryRun: true,
	}

	err := MigrateMJPEGToAVI(ctx, db, store, opts)
	require.NoError(t, err)

	// Dry-run should NOT change anything.
	rec, err := db.GetRecording(ctx, "r-dry")
	require.NoError(t, err)
	require.NotNil(t, rec)
	require.Equal(t, model.FormatMJPEG, rec.Format)

	// Original directory should still exist.
	_, err = os.Stat(mjpegDir)
	require.NoError(t, err)
}

func TestMigrateMJPEGToAVI_PurgeOld(t *testing.T) {
	db, store, ctx, dir := setupMigrateTest(t)
	defer db.Close()

	// Create an "old" recording (older than cutoff).
	oldDir := createTestMJPEGDir(t, dir, "cam1/rec_old", 3)
	insertMJPEGRecording(t, db, ctx, "r-old", "cam1", oldDir, time.Now().Add(-96*time.Hour))

	// Create a "recent" recording (within cutoff).
	recentDir := createTestMJPEGDir(t, dir, "cam1/rec_recent", 3)
	insertMJPEGRecording(t, db, ctx, "r-recent", "cam1", recentDir, time.Now())

	opts := MigrateOptions{
		Cutoff:      72 * time.Hour,
		Concurrency: 1,
		DryRun:      false,
		PurgeOld:    true,
	}

	err := MigrateMJPEGToAVI(ctx, db, store, opts)
	require.NoError(t, err)

	// Old recording should be deleted from DB.
	oldRec, err := db.GetRecording(ctx, "r-old")
	require.NoError(t, err)
	require.Nil(t, oldRec)

	// Old directory should be gone.
	_, err = os.Stat(oldDir)
	require.True(t, os.IsNotExist(err))

	// Recent recording should be migrated.
	recentRec, err := db.GetRecording(ctx, "r-recent")
	require.NoError(t, err)
	require.NotNil(t, recentRec)
	require.Equal(t, model.FormatAVI, recentRec.Format)
}

func TestMigrateMJPEGToAVI_SkipOldWithoutPurge(t *testing.T) {
	db, store, ctx, dir := setupMigrateTest(t)
	defer db.Close()

	// Old recording (older than cutoff).
	oldDir := createTestMJPEGDir(t, dir, "cam1/rec_old_skip", 3)
	insertMJPEGRecording(t, db, ctx, "r-old-skip", "cam1", oldDir, time.Now().Add(-96*time.Hour))

	opts := MigrateOptions{
		Cutoff:      72 * time.Hour,
		Concurrency: 1,
		DryRun:      false,
		PurgeOld:    false,
	}

	err := MigrateMJPEGToAVI(ctx, db, store, opts)
	require.NoError(t, err)

	// Old recording should still be MJPEG (not migrated, not purged).
	rec, err := db.GetRecording(ctx, "r-old-skip")
	require.NoError(t, err)
	require.NotNil(t, rec)
	require.Equal(t, model.FormatMJPEG, rec.Format)

	// Original dir should still exist.
	_, err = os.Stat(oldDir)
	require.NoError(t, err)
}

func TestMigrateMJPEGToAVI_ResumeOrphanCleanup(t *testing.T) {
	db, store, ctx, dir := setupMigrateTest(t)
	defer db.Close()

	// Create an orphan .avi file (not in DB).
	orphanDir := filepath.Join(dir, "cam1")
	require.NoError(t, os.MkdirAll(orphanDir, 0o755))
	orphanAVI := filepath.Join(orphanDir, "orphan.avi")
	require.NoError(t, os.WriteFile(orphanAVI, []byte("not a real avi"), 0o644))

	// Create a legitimate .avi.tmp leftover.
	leftoverTmp := filepath.Join(orphanDir, "leftover.avi.tmp")
	require.NoError(t, os.WriteFile(leftoverTmp, []byte("leftover"), 0o644))

	opts := MigrateOptions{
		Cutoff: 72 * time.Hour,
		Resume: true,
		DryRun: false,
	}

	err := MigrateMJPEGToAVI(ctx, db, store, opts)
	require.NoError(t, err)

	// Orphan .avi should be removed.
	_, err = os.Stat(orphanAVI)
	require.True(t, os.IsNotExist(err))

	// Leftover .avi.tmp should be removed.
	_, err = os.Stat(leftoverTmp)
	require.True(t, os.IsNotExist(err))
}

func TestMigrateMJPEGToAVI_CameraFilter(t *testing.T) {
	db, store, ctx, dir := setupMigrateTest(t)
	defer db.Close()

	// Camera A: MJPEG recording (within cutoff).
	aDir := createTestMJPEGDir(t, dir, "camA/rec_a", 3)
	insertMJPEGRecording(t, db, ctx, "r-a", "camA", aDir, time.Now())

	// Camera B: MJPEG recording.
	bDir := createTestMJPEGDir(t, dir, "camB/rec_b", 3)
	insertMJPEGRecording(t, db, ctx, "r-b", "camB", bDir, time.Now())

	opts := MigrateOptions{
		CameraID:    "camA",
		Cutoff:      72 * time.Hour,
		Concurrency: 1,
		DryRun:      false,
	}

	err := MigrateMJPEGToAVI(ctx, db, store, opts)
	require.NoError(t, err)

	// Camera A recording should be migrated.
	aRec, err := db.GetRecording(ctx, "r-a")
	require.NoError(t, err)
	require.Equal(t, model.FormatAVI, aRec.Format)

	// Camera B recording should still be MJPEG.
	bRec, err := db.GetRecording(ctx, "r-b")
	require.NoError(t, err)
	require.Equal(t, model.FormatMJPEG, bRec.Format)
}

// Test that a recording with no .jpg files is skipped.
func TestMigrateOneRecording_NoJPGs(t *testing.T) {
	db, store, ctx, dir := setupMigrateTest(t)
	defer db.Close()

	// Create a directory with non-jpg files.
	noJPGDir := filepath.Join(dir, "cam1/nojpg")
	require.NoError(t, os.MkdirAll(noJPGDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(noJPGDir, "frame.bmp"), []byte("not jpg"), 0o644))

	rec := model.Recording{
		ID:       "rec-nojpg",
		CameraID: "cam1",
		FilePath: noJPGDir,
		Format:   model.FormatMJPEG,
	}
	err := migrateOneRecording(ctx, db, store, rec, logger)
	require.ErrorIs(t, err, errSkipped)
}

// TestMigrateMJPEGToAVI_OverwritesStaleBackup verifies that a pre-existing
// .migrate-backup file (left over from a prior interrupted run) does not block
// the migration. Before the fix, VACUUM INTO failed with "output file already
// exists" on every re-run, requiring manual cleanup.
func TestMigrateMJPEGToAVI_OverwritesStaleBackup(t *testing.T) {
	db, store, ctx, dir := setupMigrateTest(t)
	defer db.Close()

	// Simulate a stale backup from a previous run by pre-creating the file
	// the migration will try to write to.
	backupPath := db.path + ".migrate-backup"
	require.NoError(t, os.WriteFile(backupPath, []byte("stale backup from prior run"), 0o644))
	info, err := os.Stat(backupPath)
	require.NoError(t, err)
	require.Greater(t, info.Size(), int64(0), "precondition: stale backup exists")

	// Seed one eligible recording so the migration does real work.
	mjpegDir := createTestMJPEGDir(t, dir, "cam1/rec_stale", 3)
	insertMJPEGRecording(t, db, ctx, "r-stale", "cam1", mjpegDir, time.Now())

	opts := MigrateOptions{
		Cutoff:      72 * time.Hour,
		Concurrency: 1,
		DryRun:      false,
	}
	err = MigrateMJPEGToAVI(ctx, db, store, opts)
	require.NoError(t, err, "migration must succeed despite stale backup file")

	// The backup should now be a valid VACUUM INTO output (larger than the
	// placeholder we wrote, since it contains the full DB).
	newInfo, err := os.Stat(backupPath)
	require.NoError(t, err)
	require.Greater(t, newInfo.Size(), info.Size(),
		"backup should be overwritten with a real VACUUM INTO output")

	// And the recording should have been migrated.
	updated, err := db.GetRecording(ctx, "r-stale")
	require.NoError(t, err)
	require.Equal(t, model.FormatAVI, updated.Format)
}

// TestMigrateOneRecording_TooManyFramesSkipped verifies the OOM protection:
// a recording with more than maxFramesPerRecording frames is skipped rather
// than loaded into the AVI muxer's in-RAM buffer (which would OOM on
// memory-constrained hosts). Regression test for a production incident where
// a 24751-frame recording drove the migration process to 1.4 GB RSS and got
// OOM-killed on a 4 GB host.
func TestMigrateOneRecording_TooManyFramesSkipped(t *testing.T) {
	db, store, ctx, dir := setupMigrateTest(t)
	defer db.Close()

	// Create a recording with maxFramesPerRecording + 1 frames — one over the
	// limit. Each frame is a tiny valid JPEG (content doesn't matter; the count
	// is what triggers the guard).
	overLimit := maxFramesPerRecording + 1
	mjpegDir := createTestMJPEGDir(t, dir, "cam1/rec_huge", overLimit)

	rec := model.Recording{
		ID:        "r-huge",
		CameraID:  "cam1",
		FilePath:  mjpegDir,
		Format:    model.FormatMJPEG,
		StartedAt: time.Now(),
	}
	require.NoError(t, db.InsertRecording(ctx, &rec))

	err := migrateOneRecording(ctx, db, store, rec, logger)
	require.ErrorIs(t, err, errSkipped,
		"recording with %d frames (> limit %d) must be skipped, not migrated",
		overLimit, maxFramesPerRecording)

	// Recording must remain format=mjpeg (unchanged).
	unchanged, err := db.GetRecording(ctx, "r-huge")
	require.NoError(t, err)
	require.NotNil(t, unchanged)
	require.Equal(t, model.FormatMJPEG, unchanged.Format,
		"skipped recording must retain its original format")

	// And the source directory must still exist (not deleted).
	_, err = os.Stat(mjpegDir)
	require.NoError(t, err, "source directory must be preserved when skipped")
}
