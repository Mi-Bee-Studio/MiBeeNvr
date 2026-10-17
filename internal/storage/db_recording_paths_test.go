package storage

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/stretchr/testify/require"
)

// newPathTestDB builds an initialized DB with one camera and no rows.
func newPathTestDB(t *testing.T) *DB {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := New(dbPath)
	require.NoError(t, err)
	require.NoError(t, db.Init(context.Background()))
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func insertPathRow(t *testing.T, db *DB, id, cameraID, filePath, mergePath string) {
	t.Helper()
	r := &model.Recording{
		ID:        id,
		CameraID:  cameraID,
		FilePath:  filePath,
		Format:    model.FormatH264,
		StartedAt: time.Now().Add(-time.Minute),
		EndedAt:   time.Now(),
		Duration:  30,
	}
	require.NoError(t, db.InsertRecording(context.Background(), r))
	if mergePath != "" {
		require.NoError(t, db.SetMergeResult(context.Background(), id, mergePath, "daily"))
	}
}

func TestDeleteRecordingRowsByPath_ExactAndTree(t *testing.T) {
	db := newPathTestDB(t)
	ctx := context.Background()
	root := "/srv/nvr"

	insertPathRow(t, db, "1", "cam", root+"/cam/cam_1.mp4", root+"/nested/2026/merged_1.mp4")
	insertPathRow(t, db, "2", "cam", root+"/cam/cam_2.mp4", "")
	insertPathRow(t, db, "3", "cam", root+"/cam-2/cam_3.mp4", "") // sibling prefix — must survive

	n, err := db.DeleteRecordingRowsByPath(ctx, root+"/cam")
	require.NoError(t, err)
	// Row 1 (file_path + merge_path under the tree) and row 2; row 3 survives.
	require.Equal(t, int64(2), n)

	paths, err := db.GetRecordingPathsByCamera(ctx, "cam")
	require.NoError(t, err)
	require.Contains(t, paths, root+"/cam-2/cam_3.mp4")
	require.NotContains(t, paths, root+"/cam/cam_1.mp4")
}

func TestDeleteRecordingRowsByPath_MergedOutputOnly(t *testing.T) {
	db := newPathTestDB(t)
	ctx := context.Background()
	merged := "/srv/nvr/nested/2026/10/17/merged.mp4"
	insertPathRow(t, db, "1", "cam", "/srv/nvr/cam/cam_1.mp4", merged)

	n, err := db.DeleteRecordingRowsByPath(ctx, merged)
	require.NoError(t, err)
	require.Equal(t, int64(1), n, "a row referenced only via merge_path must drop")
}

func TestRenameRecordingPaths_RewritesExactAndTree(t *testing.T) {
	db := newPathTestDB(t)
	ctx := context.Background()
	oldDir := "/srv/nvr/cam"
	newDir := "/srv/nvr/cam-renamed"

	insertPathRow(t, db, "1", "cam", oldDir+"/cam_1.mp4", oldDir+"/nested/merged_1.mp4")
	insertPathRow(t, db, "2", "cam", oldDir+"/sub/cam_2.mp4", "")
	insertPathRow(t, db, "3", "cam", "/srv/nvr/cam-2/cam_3.mp4", "")

	n, err := db.RenameRecordingPaths(ctx, oldDir, newDir)
	require.NoError(t, err)
	require.Equal(t, int64(2), n)

	paths, err := db.GetRecordingPathsByCamera(ctx, "cam")
	require.NoError(t, err)
	require.Contains(t, paths, newDir+"/cam_1.mp4")
	require.Contains(t, paths, newDir+"/sub/cam_2.mp4")
	require.Contains(t, paths, "/srv/nvr/cam-2/cam_3.mp4", "sibling prefix untouched")
}
