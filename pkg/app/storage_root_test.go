package app

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
)

// uncreatablePath returns a path that os.MkdirAll can never create for ANY
// uid (a directory component is a regular file → ENOTDIR), keeping the tests
// deterministic on dev machines and root-capable CI runners alike.
func uncreatablePath(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	require.NoError(t, os.WriteFile(blocker, []byte("file"), 0o644))
	return filepath.Join(blocker, "nested", "root")
}

func TestEnsureStorageRoot_CreatesMissingDir(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfg := &config.Config{}
	cfg.Storage.RootDir = filepath.Join(dir, "recordings")

	require.NoError(t, ensureStorageRoot(cfg, ""))
	info, err := os.Stat(cfg.Storage.RootDir)
	require.NoError(t, err)
	require.True(t, info.IsDir())
}

func TestEnsureStorageRoot_FatalOutsideContainer(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{}
	poisoned := uncreatablePath(t)
	cfg.Storage.RootDir = poisoned

	// No container fallback → same fatal error as before the #434 fix, and
	// the config is left untouched.
	err := ensureStorageRoot(cfg, "")
	require.ErrorContains(t, err, "create storage dir")
	require.Equal(t, poisoned, cfg.Storage.RootDir)
}

func TestEnsureStorageRoot_FatalWhenFallbackEqualsRoot(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{}
	cfg.Storage.RootDir = uncreatablePath(t)
	fallback := cfg.Storage.RootDir

	// Falling back to the identical path is pointless → stay fatal.
	require.Error(t, ensureStorageRoot(cfg, fallback))
	require.Equal(t, fallback, cfg.Storage.RootDir)
}

func TestEnsureStorageRoot_FallsBackInContainer(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{}
	cfg.Storage.RootDir = uncreatablePath(t)
	vol := t.TempDir() // stands in for the container data volume

	// #434: a stale host path must not abort startup into a restart loop —
	// the app continues on the data volume with the misconfiguration logged.
	require.NoError(t, ensureStorageRoot(cfg, vol))
	require.Equal(t, vol, cfg.Storage.RootDir)
	info, err := os.Stat(vol)
	require.NoError(t, err)
	require.True(t, info.IsDir())
}

func TestEnsureStorageRoot_FatalWhenFallbackUnusable(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{}
	cfg.Storage.RootDir = uncreatablePath(t)

	// The fallback itself cannot be created → still fatal (real I/O problem).
	require.Error(t, ensureStorageRoot(cfg, uncreatablePath(t)))
}
