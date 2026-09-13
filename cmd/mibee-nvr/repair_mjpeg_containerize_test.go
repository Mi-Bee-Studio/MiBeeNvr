package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/avi"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
)

// --- fixtures ---

func newContainerizeDB(t *testing.T) *storage.DB {
	t.Helper()
	db, err := storage.New(filepath.Join(t.TempDir(), "c.db"))
	require.NoError(t, err)
	require.NoError(t, db.Init(context.Background()))
	t.Cleanup(func() { db.Close() })
	return db
}

// seedMJPEGDir creates a directory-form MJPEG segment with n frames named in
// the storage timestamp layout, plus its DB row. Returns the recording row.
func seedMJPEGDir(t *testing.T, db *storage.DB, cameraID, recID string, n int, mergeStatus string) *model.Recording {
	t.Helper()
	ctx := context.Background()
	segDir := filepath.Join(t.TempDir(), cameraID+"_"+recID)
	require.NoError(t, os.MkdirAll(segDir, 0o755))
	for i := range n {
		name := fmt.Sprintf("20260913_12%02d00.%03d.jpg", i%60, i)
		require.NoError(t, os.WriteFile(filepath.Join(segDir, name), testJPEGBytes(), 0o644))
	}
	base := time.Date(2026, 9, 13, 12, 0, 0, 0, time.Local)
	rec := &model.Recording{
		ID: recID, CameraID: cameraID, FilePath: segDir, Format: model.FormatMJPEG,
		StartedAt: base, EndedAt: base.Add(time.Minute),
		Duration: 60, FrameCount: n, MergeStatus: mergeStatus,
	}
	require.NoError(t, db.InsertRecording(ctx, rec))
	return rec
}

// testJPEGBytes synthesizes a minimal JPEG (SOI + SOF0 32x24 + EOI) — enough
// for dimension detection; the AVI muxer stores frame bytes verbatim.
func testJPEGBytes() []byte {
	return []byte{
		0xFF, 0xD8, 0xFF, 0xC0, 0x00, 0x11, 0x08, 0x00, 0x18, 0x00, 0x20,
		0x03, 0x01, 0x11, 0x00, 0x02, 0x11, 0x01, 0x03, 0x11, 0x01, 0xFF, 0xD9,
	}
}

// --- tests ---

// TestContainerizeMJPEGDirs_Execute converts a directory-form MJPEG segment
// into a verified single-file AVI next to it, flips the DB row, and removes
// the source directory (#761 migration).
func TestContainerizeMJPEGDirs_Execute(t *testing.T) {
	t.Helper()
	db := newContainerizeDB(t)
	rec := seedMJPEGDir(t, db, "cam-c1", "111", 3, model.MergeStatusPending)
	srcDir := rec.FilePath // containerizeOne mutates rec.FilePath in place

	rep := containerizeMJPEGDirs(context.Background(), db, []*model.Recording{rec},
		containerizeOptions{dryRun: false}, os.Stdout)
	require.Equal(t, 1, rep.converted)
	require.Equal(t, 0, rep.failed)

	// Source dir gone; .avi sibling exists and demuxes to 3 frames.
	_, err := os.Stat(srcDir)
	require.True(t, os.IsNotExist(err), "source dir should be removed after verify")
	aviPath := srcDir + ".avi"
	fi, err := os.Stat(aviPath)
	require.NoError(t, err)
	require.Greater(t, fi.Size(), int64(0))

	fh, err := os.Open(aviPath)
	require.NoError(t, err)
	defer fh.Close()
	d, err := avi.NewDemuxer(fh)
	require.NoError(t, err)
	frames, err := d.VideoFrameIndex()
	require.NoError(t, err)
	require.Len(t, frames, 3)

	// DB row now points at the container.
	got, err := db.GetRecording(context.Background(), rec.ID)
	require.NoError(t, err)
	require.Equal(t, aviPath, got.FilePath)
	require.Equal(t, model.FormatAVI, got.Format)
	require.Equal(t, 3, got.FrameCount)
	require.Equal(t, fi.Size(), got.FileSize)
}

// TestContainerizeMJPEGDirs_DryRun plans without touching disk or DB.
func TestContainerizeMJPEGDirs_DryRun(t *testing.T) {
	t.Helper()
	db := newContainerizeDB(t)
	rec := seedMJPEGDir(t, db, "cam-c1", "222", 2, model.MergeStatusPending)

	rep := containerizeMJPEGDirs(context.Background(), db, []*model.Recording{rec},
		containerizeOptions{dryRun: true}, os.Stdout)
	require.Equal(t, 1, rep.planned)
	require.Equal(t, 0, rep.converted)

	_, err := os.Stat(rec.FilePath)
	require.NoError(t, err, "dry run must not remove the source dir")
	_, err = os.Stat(rec.FilePath + ".avi")
	require.True(t, os.IsNotExist(err), "dry run must not write the container")

	got, err := db.GetRecording(context.Background(), rec.ID)
	require.NoError(t, err)
	require.Equal(t, model.FormatMJPEG, got.Format)
	require.Equal(t, rec.FilePath, got.FilePath)
}

// TestContainerizeMJPEGDirs_KeepOld retains the source directory on request.
func TestContainerizeMJPEGDirs_KeepOld(t *testing.T) {
	t.Helper()
	db := newContainerizeDB(t)
	rec := seedMJPEGDir(t, db, "cam-c1", "333", 2, model.MergeStatusPending)
	srcDir := rec.FilePath

	rep := containerizeMJPEGDirs(context.Background(), db, []*model.Recording{rec},
		containerizeOptions{dryRun: false, keepOld: true}, os.Stdout)
	require.Equal(t, 1, rep.converted)
	_, err := os.Stat(srcDir)
	require.NoError(t, err, "--keep-old must retain the source dir")
}

// TestContainerizeMJPEGDirs_Idempotent: a second pass over the already
// converted row (now .avi) plans nothing.
func TestContainerizeMJPEGDirs_Idempotent(t *testing.T) {
	t.Helper()
	db := newContainerizeDB(t)
	rec := seedMJPEGDir(t, db, "cam-c1", "444", 2, model.MergeStatusPending)
	rep := containerizeMJPEGDirs(context.Background(), db, []*model.Recording{rec},
		containerizeOptions{}, os.Stdout)
	require.Equal(t, 1, rep.converted)

	// Refresh the row from DB and re-run: file is now .avi → not a dir → skip.
	got, err := db.GetRecording(context.Background(), rec.ID)
	require.NoError(t, err)
	rep2 := containerizeMJPEGDirs(context.Background(), db, []*model.Recording{got},
		containerizeOptions{}, os.Stdout)
	require.Equal(t, 0, rep2.planned)
	require.Equal(t, 0, rep2.converted)
}

// TestContainerizeMJPEGDirs_SkipsDarkAndEmpty: dark rows are cleanup-bound
// (converting wastes IO); empty dirs are reported skipped, not failed.
func TestContainerizeMJPEGDirs_SkipsDarkAndEmpty(t *testing.T) {
	t.Helper()
	db := newContainerizeDB(t)
	dark := seedMJPEGDir(t, db, "cam-c1", "555", 2, model.MergeStatusDark)
	empty := seedMJPEGDir(t, db, "cam-c1", "666", 0, model.MergeStatusPending)

	rep := containerizeMJPEGDirs(context.Background(), db, []*model.Recording{dark, empty},
		containerizeOptions{}, os.Stdout)
	require.Equal(t, 0, rep.converted)
	require.Equal(t, 0, rep.failed)
	require.Equal(t, 2, rep.skipped)

	// Dark row untouched.
	got, err := db.GetRecording(context.Background(), dark.ID)
	require.NoError(t, err)
	require.Equal(t, model.FormatMJPEG, got.Format)
	_, err = os.Stat(dark.FilePath)
	require.NoError(t, err)
}
