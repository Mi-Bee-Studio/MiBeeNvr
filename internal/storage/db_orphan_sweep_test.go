package storage

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/stretchr/testify/require"
)

// TestSweepOrphanRecordingRows: rows whose suffix file still exists are kept
// (a live writer may own them); rows whose file vanished are deleted;
// archived rows are untouched by design (their file_path semantics belong to
// the offload layer).
func TestSweepOrphanRecordingRows(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	dir := t.TempDir()

	liveTmp := filepath.Join(dir, "live.tmp")
	require.NoError(t, os.WriteFile(liveTmp, []byte("x"), 0o644))

	now := time.Now().UTC()
	recs := []*model.Recording{
		{ID: "orphan-1", CameraID: "camA", FilePath: filepath.Join(dir, "gone1.tmp"), Format: model.FormatH264, StartedAt: now, Duration: 60, FileSize: 100},
		{ID: "orphan-2", CameraID: "camA", FilePath: filepath.Join(dir, "gone2.tmp"), Format: model.FormatH264, StartedAt: now, Duration: 60, FileSize: 100},
		{ID: "live-1", CameraID: "camA", FilePath: liveTmp, Format: model.FormatH264, StartedAt: now, Duration: 60, FileSize: 100},
		{ID: "plain-1", CameraID: "camA", FilePath: filepath.Join(dir, "plain.mp4"), Format: model.FormatH264, StartedAt: now, Duration: 60, FileSize: 100},
	}
	for _, r := range recs {
		require.NoError(t, db.InsertRecording(ctx, r))
	}
	require.NoError(t, db.SetMergeStatus(ctx, []string{"orphan-1", "orphan-2", "live-1"}, model.MergeStatusMerged))

	// Archived row pointing at a vanished .tmp — must survive (out of scope).
	archived := &model.Recording{ID: "archived-1", CameraID: "camA", FilePath: filepath.Join(dir, "gone3.tmp"), Format: model.FormatH264, StartedAt: now, Duration: 60, FileSize: 100}
	require.NoError(t, db.InsertRecording(ctx, archived))
	_, err := db.db.ExecContext(ctx, `UPDATE recordings SET archived = 1 WHERE id = ?`, "archived-1")
	require.NoError(t, err)

	n, err := db.SweepOrphanRecordingRows(ctx, ".tmp")
	require.NoError(t, err)
	require.Equal(t, int64(2), n)

	for _, id := range []string{"live-1", "archived-1", "plain-1"} {
		rec, err := db.GetRecording(ctx, id)
		require.NoError(t, err, id)
		require.NotNil(t, rec, "row %s should survive the sweep", id)
	}
	for _, id := range []string{"orphan-1", "orphan-2"} {
		rec, err := db.GetRecording(ctx, id)
		require.NoError(t, err, id)
		require.Nil(t, rec, "row %s should have been swept", id)
	}

	// A suffix without the leading dot must be rejected before touching rows.
	_, err = db.SweepOrphanRecordingRows(ctx, "tmp")
	require.Error(t, err)
}
