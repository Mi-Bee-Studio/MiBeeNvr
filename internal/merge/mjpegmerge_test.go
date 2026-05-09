package merge

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
	"github.com/stretchr/testify/require"
)

func TestMergeMJPEGSegments_MultipleSources(t *testing.T) {
	dir := t.TempDir()
	storeDir := filepath.Join(dir, "store")
	store, err := storage.NewManager(storeDir)
	require.NoError(t, err)

	cameraID := "cam1"

	// Create source segment directories with JPEG files
	srcDir1 := filepath.Join(storeDir, cameraID, "src1")
	require.NoError(t, os.MkdirAll(srcDir1, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(srcDir1, "frame001.jpg"), []byte("fake-jpeg-1"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(srcDir1, "frame002.jpg"), []byte("fake-jpeg-2"), 0644))

	srcDir2 := filepath.Join(storeDir, cameraID, "src2")
	require.NoError(t, os.MkdirAll(srcDir2, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(srcDir2, "frame003.jpg"), []byte("fake-jpeg-3"), 0644))

	segments := []*model.Recording{
		{
			ID:        "seg1",
			CameraID:  cameraID,
			FilePath:  srcDir1,
			Format:    model.FormatMJPEG,
			StartedAt: time.Now().Add(-2 * time.Hour),
			EndedAt:   time.Now().Add(-time.Hour),
			Duration:  3600.0,
			FileSize:  24,
			FrameCount: 2,
		},
		{
			ID:        "seg2",
			CameraID:  cameraID,
			FilePath:  srcDir2,
			Format:    model.FormatMJPEG,
			StartedAt: time.Now().Add(-time.Hour),
			EndedAt:   time.Now(),
			Duration:  3600.0,
			FileSize:  12,
			FrameCount: 1,
		},
	}

	merged, err := MergeMJPEGSegments(context.Background(), segments, store, cameraID)
	require.NoError(t, err)
	require.NotNil(t, merged)
	require.Equal(t, model.FormatMJPEG, merged.Format)
	require.Equal(t, cameraID, merged.CameraID)
	require.Equal(t, 7200.0, merged.Duration)
	require.Equal(t, 3, merged.FrameCount)
	require.Greater(t, merged.FileSize, int64(0))

	// Verify merged directory exists and has correct number of files
	entries, err := os.ReadDir(merged.FilePath)
	require.NoError(t, err)
	require.Len(t, entries, 3)

	// Verify source directories are removed
	_, err = os.Stat(srcDir1)
	require.True(t, os.IsNotExist(err))
	_, err = os.Stat(srcDir2)
	require.True(t, os.IsNotExist(err))
}

func TestMergeMJPEGSegments_EmptyList(t *testing.T) {
	dir := t.TempDir()
	storeDir := filepath.Join(dir, "store")
	store, err := storage.NewManager(storeDir)
	require.NoError(t, err)

	_, err = MergeMJPEGSegments(context.Background(), nil, store, "cam1")
	require.Error(t, err)
	require.Contains(t, err.Error(), "no segments")
}

func TestMergeMJPEGSegments_SingleSource(t *testing.T) {
	dir := t.TempDir()
	storeDir := filepath.Join(dir, "store")
	store, err := storage.NewManager(storeDir)
	require.NoError(t, err)

	cameraID := "cam1"
	srcDir := filepath.Join(storeDir, cameraID, "src_single")
	require.NoError(t, os.MkdirAll(srcDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(srcDir, "frame001.jpg"), []byte("fake-jpeg-data"), 0644))

	segments := []*model.Recording{
		{
			ID:        "seg1",
			CameraID:  cameraID,
			FilePath:  srcDir,
			Format:    model.FormatMJPEG,
			StartedAt: time.Now().Add(-time.Hour),
			EndedAt:   time.Now(),
			Duration:  3600.0,
			FileSize:  15,
			FrameCount: 1,
		},
	}

	merged, err := MergeMJPEGSegments(context.Background(), segments, store, cameraID)
	require.NoError(t, err)
	require.NotNil(t, merged)
	require.Equal(t, 1, merged.FrameCount)

	entries, err := os.ReadDir(merged.FilePath)
	require.NoError(t, err)
	require.Len(t, entries, 1)
}
