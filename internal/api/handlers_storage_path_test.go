package api

// handlers_storage_path_test.go — OS-portable path semantics of the storage
// migrate/candidate handlers (#891): absolute-path checks must accept Windows
// drive paths on Windows (the desktop build runs there), trailing-separator
// trimming must handle "\" as well as "/", and root containment must compare
// path components instead of raw string prefixes. The helpers are pure so
// their contracts pin cross-OS; the Windows-only acceptance of `C:\...`
// absolutes is guarded by runtime.GOOS.

import (
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCleanStoragePath(t *testing.T) {
	t.Parallel()

	// Absolute in, trailing separators of both flavors trimmed out.
	got, ok := cleanStoragePath("/mnt/data/nvr/")
	require.True(t, ok)
	require.Equal(t, "/mnt/data/nvr", got)

	got, ok = cleanStoragePath("  /mnt/data/nvr/\\")
	require.True(t, ok)
	require.Equal(t, "/mnt/data/nvr", got)

	// Windows drive paths are absolute when the server runs on Windows —
	// the desktop SPA round-trips them unchanged.
	if runtime.GOOS == "windows" {
		got, ok = cleanStoragePath(`C:\Recordings\nvr\`)
		require.True(t, ok)
		require.Equal(t, `C:\Recordings\nvr`, got)
	} else {
		_, ok = cleanStoragePath(`C:\Recordings\nvr`)
		require.False(t, ok, "drive paths are not absolute on this OS")
	}

	// Relative paths are rejected everywhere.
	_, ok = cleanStoragePath("data/recordings")
	require.False(t, ok)

	// Blank (after trim) is rejected.
	_, ok = cleanStoragePath("   ")
	require.False(t, ok)
}

func TestPathWithin(t *testing.T) {
	t.Parallel()

	require.True(t, pathWithin("/data/nvr", "/data/nvr"), "identical paths")
	require.True(t, pathWithin("/data/nvr", "/data/nvr/cam-1"), "child inside parent")
	require.False(t, pathWithin("/data/nvr/cam-1", "/data/nvr"), "single-direction: reversed args are outside")

	// The string-prefix traps the old HasPrefix check fell into.
	require.False(t, pathWithin("/data/nvr", "/data/nvr-other"), "sibling sharing a string prefix")
	require.False(t, pathWithin("/data/nvr", "/data/nvr2/cam-1"), "prefix-then-digit is not containment")

	// Windows separators compare correctly on every OS (component compare,
	// not byte-prefix): mixed-flavor containment holds, cross-volume does not.
	require.True(t, pathWithin(`C:\Recordings`, `C:\Recordings\cam-1`))
	require.True(t, pathWithin(`C:\Recordings`, `C:/Recordings/cam-1`))
	require.False(t, pathWithin(`C:\Recordings`, `D:\Recordings\cam-1`), "different volumes")
}
