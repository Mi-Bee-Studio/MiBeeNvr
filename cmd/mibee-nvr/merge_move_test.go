package main

// merge_move_test.go — moveFile's cross-device fallback (#891): os.Rename
// cannot cross a filesystem boundary (Linux EXDEV when merging recordings
// across two disks; Windows ERROR_NOT_SAME_DEVICE for cross-volume — and
// drive-relative — paths). The fallback must copy + delete, preserving
// content and permissions.

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMoveFileSameDevice(t *testing.T) {
	// Sequential: the cross-device tests below mutate the package-level
	// osRename seam, so none of the moveFile tests may run concurrently.

	dir := t.TempDir()
	src := filepath.Join(dir, "a.mp4")
	dst := filepath.Join(dir, "b.mp4")
	require.NoError(t, os.WriteFile(src, []byte("payload"), 0o644))

	require.NoError(t, moveFile(src, dst))
	data, err := os.ReadFile(dst)
	require.NoError(t, err)
	require.Equal(t, "payload", string(data))
	_, err = os.Stat(src)
	require.True(t, os.IsNotExist(err), "source must be gone after a same-device move")
}

func TestMoveFileCrossDeviceFallsBackToCopy(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "a.mp4")
	dst := filepath.Join(dir, "sub", "b.mp4")
	require.NoError(t, os.WriteFile(src, []byte("cross-volume-payload"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Dir(dst), 0o755))

	// Force the rename failure a two-disk merge produces: EXDEV on Linux,
	// ERROR_NOT_SAME_DEVICE (also errno 17) on Windows.
	orig := osRename
	osRename = func(string, string) error { return &os.LinkError{Op: "rename", Err: syscall.Errno(17)} }
	t.Cleanup(func() { osRename = orig })

	require.NoError(t, moveFile(src, dst))
	data, err := os.ReadFile(dst)
	require.NoError(t, err)
	require.Equal(t, "cross-volume-payload", string(data))
	_, err = os.Stat(src)
	require.True(t, os.IsNotExist(err), "source must be gone after the copy-fallback move")

	info, err := os.Stat(dst)
	require.NoError(t, err)
	require.False(t, info.IsDir())
}

func TestMoveFileSurfacesOtherRenameErrors(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "missing.mp4")

	// A genuine ENOENT is not a cross-device condition — it must surface.
	require.Error(t, moveFile(src, filepath.Join(dir, "dst.mp4")))
}

// TestMergeDiskDirectoriesMovesAcrossDevices pins the wiring: the merge walk
// uses moveFile, so a two-disk source→destination merge completes instead of
// failing on the first file.
func TestMergeDiskDirectoriesMovesAcrossDevices(t *testing.T) {
	srcDir := t.TempDir()
	dstDir := t.TempDir()
	src := filepath.Join(srcDir, "cam-old_20260923_120000_1.mp4")
	require.NoError(t, os.WriteFile(src, []byte("segment"), 0o644))

	orig := osRename
	osRename = func(string, string) error { return &os.LinkError{Op: "rename", Err: syscall.Errno(17)} }
	t.Cleanup(func() { osRename = orig })

	moved, manifest, err := mergeDiskDirectories(context.Background(), srcDir, dstDir, "", "cam-old", "cam-new")
	require.NoError(t, err)
	require.Equal(t, 1, moved)
	require.Len(t, manifest, 1)
	data, err := os.ReadFile(filepath.Join(dstDir, "cam-new_20260923_120000_1.mp4"))
	require.NoError(t, err)
	require.Equal(t, "segment", string(data))
}
