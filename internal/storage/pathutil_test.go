// SPDX-License-Identifier: MIT

package storage

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidatePath_ValidRelative(t *testing.T) {
	tmpDir := t.TempDir()

	path, err := ValidatePath(tmpDir, "cam01/video.mp4")
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(tmpDir, "cam01", "video.mp4"), path)
}

func TestValidatePath_ValidAbsolute(t *testing.T) {
	tmpDir := t.TempDir()
	subDir := filepath.Join(tmpDir, "cam01")
	require.NoError(t, os.MkdirAll(subDir, 0755))

	path, err := ValidatePath(tmpDir, subDir)
	require.NoError(t, err)
	assert.Equal(t, subDir, path)
}

func TestValidatePath_ValidRoot(t *testing.T) {
	tmpDir := t.TempDir()

	path, err := ValidatePath(tmpDir, ".")
	require.NoError(t, err)
	assert.Equal(t, tmpDir, path)
}

func TestValidatePath_ValidEmptyString(t *testing.T) {
	tmpDir := t.TempDir()

	path, err := ValidatePath(tmpDir, "")
	require.NoError(t, err)
	assert.Equal(t, tmpDir, path)
}

func TestValidatePath_TraversalRelativeDotDot(t *testing.T) {
	tmpDir := t.TempDir()

	_, err := ValidatePath(tmpDir, "../etc/passwd")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "outside storage root")
}

func TestValidatePath_TraversalDeepRelative(t *testing.T) {
	tmpDir := t.TempDir()

	_, err := ValidatePath(tmpDir, "cam01/../../../../etc/passwd")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "outside storage root")
}

func TestValidatePath_TraversalAbsolute(t *testing.T) {
	tmpDir := t.TempDir()

	_, err := ValidatePath(tmpDir, "/etc/passwd")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "outside storage root")
}

func TestValidatePath_TraversalMixedDotDot(t *testing.T) {
	tmpDir := t.TempDir()

	_, err := ValidatePath(tmpDir, "cam01/../../../etc/passwd")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "outside storage root")
}

func TestValidatePath_TraversalWindowsBackslash(t *testing.T) {
	// On Unix, backslash is a valid filename char, so this is actually a valid
	// relative path pointing to a file named "..\\..\\etc\\passwd" inside cam01.
	// Only accept this test on Windows. On Unix it should succeed (not traverse).
	tmpDir := t.TempDir()

	// This doesn't traverse on Unix — backslash is part of the filename.
	path, err := ValidatePath(tmpDir, "..\\..\\etc\\passwd")
	// On Unix, this is a valid filename, not a traversal
	if err == nil {
		assert.Equal(t, filepath.Join(tmpDir, "..\\..\\etc\\passwd"), path)
	} else {
		assert.Contains(t, err.Error(), "outside storage root")
	}
}

func TestValidatePath_SymlinkNoTraversal(t *testing.T) {
	tmpDir := t.TempDir()
	realDir := filepath.Join(tmpDir, "real")
	linkDir := filepath.Join(tmpDir, "link")
	require.NoError(t, os.MkdirAll(realDir, 0755))

	// Create a symlink inside rootDir to another directory inside rootDir
	require.NoError(t, os.Symlink("real", linkDir))

	// Accessing through the symlink is fine — it resolves inside rootDir
	path, err := ValidatePath(tmpDir, "link/../real/file.txt")
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(tmpDir, "real", "file.txt"), path)
}

// Test that deeply nested valid paths still resolve correctly
func TestValidatePath_DeepNesting(t *testing.T) {
	tmpDir := t.TempDir()
	deepDir := filepath.Join(tmpDir, "a", "b", "c", "d")
	require.NoError(t, os.MkdirAll(deepDir, 0755))

	path, err := ValidatePath(tmpDir, "a/b/c/d/file.mp4")
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(deepDir, "file.mp4"), path)
}

// Test that an actual symlink to outside the root is blocked (if the caller resolves it)
func TestValidatePath_RelativePathWithDotDot(t *testing.T) {
	tmpDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, "sub"), 0755))

	// Valid path that uses .. within bounds
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "sub", "file.txt"), []byte("data"), 0644))
	path, err := ValidatePath(tmpDir, "sub/../sub/file.txt")
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(tmpDir, "sub", "file.txt"), path)
}

func TestValidatePath_SymlinkTraversal(t *testing.T) {
	t.Helper()
	tmpDir := t.TempDir()
	realDir := filepath.Join(tmpDir, "real")
	outsideDir := filepath.Join(tmpDir, "outside")
	require.NoError(t, os.MkdirAll(realDir, 0755))
	require.NoError(t, os.MkdirAll(outsideDir, 0755))

	// Create a file outside the intended base, then symlink to it from inside
	outsideFile := filepath.Join(outsideDir, "secret.txt")
	require.NoError(t, os.WriteFile(outsideFile, []byte("secret"), 0644))

	// Create a symlink inside realDir that points to outsideDir
	linkPath := filepath.Join(realDir, "escape")
	require.NoError(t, os.Symlink(outsideDir, linkPath))

	// Attempt traversal via the symlink
	_, err := ValidatePath(realDir, "escape/secret.txt")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "outside storage root")
}
