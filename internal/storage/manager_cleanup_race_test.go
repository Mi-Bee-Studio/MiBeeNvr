package storage

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// The startup temp-cleanup scan (CleanupTempFiles) races with recorders that
// start writing immediately after process start — on large trees the scan
// runs for minutes. An ACTIVE segment (created by CreateSegment, not yet
// closed) must never be deleted by the scan.
//
// Production incident 2026-09-08 (Banana Pi M5): a 120s h265 segment's .tmp
// was deleted mid-write by the startup scan; CloseSegment failed with "temp
// path not found", the final .mp4 never existed, and the DB row became a
// permanent 404 entry in the recordings list.

func TestCleanupTempFiles_SkipsActiveSegment(t *testing.T) {
	m, err := NewManager(t.TempDir())
	require.NoError(t, err)

	tempPath, finalPath, err := m.CreateSegment("cam-race", "h264")
	require.NoError(t, err)
	_, err = m.WriteFrame(tempPath, []byte("payload"))
	require.NoError(t, err)

	require.NoError(t, m.CleanupTempFiles())

	_, err = os.Stat(tempPath)
	require.NoError(t, err, "active segment temp must survive the cleanup scan")

	// A clean close after the scan still produces the final file — the
	// protection must not leak into normal finalization.
	require.NoError(t, m.CloseSegment(tempPath, finalPath))
	_, err = os.Stat(finalPath)
	require.NoError(t, err)
}

func TestCleanupTempFiles_SkipsActiveSegmentDir(t *testing.T) {
	m, err := NewManager(t.TempDir())
	require.NoError(t, err)

	tempPath, _, err := m.CreateSegment("cam-race", "mjpeg")
	require.NoError(t, err)
	_, err = m.WriteFrame(tempPath, []byte("jpeg-bytes"))
	require.NoError(t, err)

	require.NoError(t, m.CleanupTempFiles())

	info, err := os.Stat(tempPath)
	require.NoError(t, err, "active MJPEG temp dir must survive the cleanup scan")
	require.True(t, info.IsDir())
}

// Orphaned temps from a previous crash must still be removed even when an
// active segment exists in the same tree — protection is per-path, exact.
func TestCleanupTempFiles_StillRemovesOrphansBesideActiveSegment(t *testing.T) {
	root := t.TempDir()
	m, err := NewManager(root)
	require.NoError(t, err)

	activeTemp, activeFinal, err := m.CreateSegment("cam-race", "h264")
	require.NoError(t, err)
	orphan := filepath.Join(filepath.Dir(activeTemp), "999999999999999999.tmp")
	require.NoError(t, os.WriteFile(orphan, []byte("crash-leftover"), 0o644))

	require.NoError(t, m.CleanupTempFiles())

	_, err = os.Stat(activeTemp)
	require.NoError(t, err, "active segment must be skipped")
	_, err = os.Stat(orphan)
	require.True(t, os.IsNotExist(err), "orphaned temp must still be removed")

	require.NoError(t, m.CloseSegment(activeTemp, activeFinal))
}

// tierrec writes segment temps under cam-*/ trees WITHOUT going through
// CreateSegment; it registers them via RegisterActiveTemp instead. A
// registered external temp must be protected until unregistered.
func TestCleanupTempFiles_SkipsRegisteredExternalTemp(t *testing.T) {
	root := t.TempDir()
	m, err := NewManager(root)
	require.NoError(t, err)

	camDir := filepath.Join(root, "cam-ext")
	require.NoError(t, os.MkdirAll(camDir, 0o755))
	extTemp := filepath.Join(camDir, "1788877062013791340.tmp")
	require.NoError(t, os.WriteFile(extTemp, []byte("tierrec-segment"), 0o644))

	m.RegisterActiveTemp(extTemp, "cam-ext")
	require.NoError(t, m.CleanupTempFiles())
	_, err = os.Stat(extTemp)
	require.NoError(t, err, "registered external temp must survive the scan")

	m.UnregisterActiveTemp(extTemp)
	require.NoError(t, m.CleanupTempFiles())
	_, err = os.Stat(extTemp)
	require.True(t, os.IsNotExist(err), "unregistered temp is an orphan again and must be removed")
}
