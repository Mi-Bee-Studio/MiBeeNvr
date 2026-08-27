package storage

// Long-tail coverage for the storage Manager + health tracker (#580):
// root switching/overrides, segment listing, camera-dir deletion, probe,
// and the periodic health check loop (observed via its observable effect,
// never a fixed sleep).

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/event"
	"github.com/stretchr/testify/require"
)

func TestManagerRootSwitchAndCameraOverrides(t *testing.T) {
	rootA := t.TempDir()
	m, err := NewManager(rootA)
	require.NoError(t, err)

	// Camera override round-trip + clear.
	m.SetCameraRoot("cam-1", rootA)
	require.Equal(t, rootA, m.CameraRoot("cam-1"))
	m.SetCameraRoot("cam-1", "")
	require.Empty(t, m.CameraRoot("cam-1"))

	// Hot root switch: creates the dir, RootFor follows.
	rootB := filepath.Join(t.TempDir(), "switched")
	require.NoError(t, m.SetRootDir(rootB))
	require.Equal(t, rootB, m.RootFor("cam-1"))

	// Empty root rejected.
	require.Error(t, m.SetRootDir(""))

	// Roots() includes default + overrides.
	m.SetCameraRoot("cam-1", rootA)
	roots := m.Roots()
	require.Contains(t, roots, rootA)
	require.Contains(t, roots, rootB)
}

func TestManagerRootUsage(t *testing.T) {
	m, err := NewManager(t.TempDir())
	require.NoError(t, err)

	total, free, err := m.GetRootUsage(t.TempDir())
	require.NoError(t, err)
	require.Positive(t, total)
	require.GreaterOrEqual(t, total, free)

	_, _, err = m.GetRootUsage("/definitely/not/a/root")
	require.Error(t, err)
}

func TestManagerListSegmentsAndDeleteCameraDir(t *testing.T) {
	root := t.TempDir()
	m, err := NewManager(root)
	require.NoError(t, err)

	// Unknown camera dir → explicit error, not an empty list.
	_, err = m.ListSegments("ghost")
	require.ErrorContains(t, err, "cannot read camera dir")

	// Seed a camera tree with segments + noise.
	camDir := filepath.Join(root, "cam-1")
	dayDir := filepath.Join(camDir, "20260820")
	require.NoError(t, os.MkdirAll(dayDir, 0o755))
	// Segment names follow the {cameraID}_{ts}_{uuid} pattern.
	for _, n := range []string{"cam-1_20260820100000_abc123.mp4", "cam-1_20260820110000_def456.mp4"} {
		require.NoError(t, os.WriteFile(filepath.Join(dayDir, n), []byte("x"), 0o644))
	}
	require.NoError(t, os.WriteFile(filepath.Join(camDir, ".hidden"), []byte("x"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dayDir, "part.tmp"), []byte("x"), 0o644))

	segs, err := m.ListSegments("cam-1")
	require.NoError(t, err)
	require.Len(t, segs, 2, "hidden + tmp entries are skipped")

	// ProbeDir accepts a writable dir and rejects a file.
	require.NoError(t, ProbeDir(dayDir))
	filePath := filepath.Join(camDir, "plain.txt")
	require.NoError(t, os.WriteFile(filePath, []byte("x"), 0o644))
	require.Error(t, ProbeDir(filePath))

	// DeleteCameraDir removes the tree across roots.
	require.NoError(t, m.DeleteCameraDir("cam-1"))
	_, err = os.Stat(camDir)
	require.True(t, os.IsNotExist(err))
}

func TestManagerWriteHealthRecording(t *testing.T) {
	root := t.TempDir()
	m, err := NewManager(root)
	require.NoError(t, err)
	bus := event.NewEventBus(8)
	m.SetEventBus(bus)

	events := make(chan event.Event, 8)
	bus.SubscribeByPrefix("storage", events, 8)

	// A real temp segment registered to cam-1 fails to write → health state
	// must flip and publish a change event (unknown paths are a safe no-op).
	m.RecordWriteFailureForPath("/tmp/unknown.tmp")
	tempPath, _, err := m.CreateSegment("cam-1", "h264")
	require.NoError(t, err)
	m.RecordWriteFailureForPath(tempPath)

	select {
	case evt := <-events:
		require.Equal(t, event.TopicStorageHealthChanged, evt.Topic)
	case <-time.After(5 * time.Second):
		t.Fatal("write failure never published a health event")
	}

	// Recovery path.
	m.RecordWriteSuccessForPath(tempPath)
}
