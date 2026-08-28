package merge

// Long-tail coverage (#586): exported SPS wrappers, ParseSegmentNoProbe /
// ParseSegmentDurationOnly, both resolveTimelapseCadence resolvers,
// MergeManager.Run's cancel path and merge-lock semantics, and
// ConsolidateShortRecord over real MP4 fixtures (muxer-built, hermetic).

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/metrics"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
	"github.com/stretchr/testify/require"
)

// ppsFixture matches h264SPS1920 (same stream).
var ppsFixture = []byte{0x68, 0xce, 0x38, 0x80}

func TestExportedSPSResolutionWrappers(t *testing.T) {
	t.Parallel()

	w, h, err := ParseSPSResolution(h264SPS1920)
	require.NoError(t, err)
	require.Equal(t, 1920, w)
	require.Equal(t, 1080, h)

	w, h, err = ParseHEVCSPSResolution(hevcSPS1920)
	require.NoError(t, err)
	require.Equal(t, 1920, w)
	require.Equal(t, 1080, h)

	// Codec-dispatching shim.
	w, _, err = SPSResolution("h264", h264SPS1920)
	require.NoError(t, err)
	require.Equal(t, 1920, w)
	_, h, err = SPSResolution("h265", hevcSPS1920)
	require.NoError(t, err)
	require.Equal(t, 1080, h)

	// Garbage input errors through every entry point.
	for _, fn := range []func([]byte) (int, int, error){ParseSPSResolution, ParseHEVCSPSResolution} {
		_, _, err = fn([]byte{0x01})
		require.Error(t, err)
	}
}

func TestParseSegmentNoProbeAndDurationOnly(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := createH264SegmentWithSamples(t, dir, "seg.mp4", h264SPS1920, ppsFixture, [][]byte{
		{0x65, 0x88, 0x80, 0x40}, {0x41, 0x10, 0x00, 0x0c},
	})

	info, err := ParseSegmentNoProbe(path)
	require.NoError(t, err)
	require.Equal(t, "h264", info.Codec)
	require.Equal(t, h264SPS1920, info.SPS)

	dur, err := ParseSegmentDurationOnly(path)
	require.NoError(t, err)
	require.Positive(t, dur)

	// Missing file errors on both.
	_, err = ParseSegmentNoProbe(filepath.Join(dir, "missing.mp4"))
	require.Error(t, err)
	_, err = ParseSegmentDurationOnly(filepath.Join(dir, "missing.mp4"))
	require.Error(t, err)
}

// mergeTestEnv wires a MergeManager + RollingMergeCoordinator against a
// per-test SQLite instance (#571).
func coordinatorTestEnv(t *testing.T) (*MergeManager, *RollingMergeCoordinator, *storage.DB, *storage.Manager) {
	t.Helper()
	dir := t.TempDir()
	db, err := storage.New(filepath.Join(dir, "test.db"))
	require.NoError(t, err)
	require.NoError(t, db.Init(context.Background()))
	t.Cleanup(func() { db.Close() })
	store, err := storage.NewManager(filepath.Join(dir, "storage"))
	require.NoError(t, err)

	getAdaptive := func(cameraID string) *config.AdaptiveRecordingConfig { return nil }

	mgr := NewMergeManager(db, store,
		func() config.MergeConfig { return config.MergeConfig{} },
		func(string) *config.MergeConfig { return nil },
		getAdaptive,
		func() []config.CameraConfig { return nil },
		metrics.NewMetrics())
	rolling := NewRollingMergeCoordinator(db, store,
		func() config.MergeConfig { return config.MergeConfig{} },
		func(string) *config.MergeConfig { return nil },
		getAdaptive,
		func() []config.CameraConfig { return nil },
		metrics.NewMetrics(), nil)
	return mgr, rolling, db, store
}

func TestResolveTimelapseCadence(t *testing.T) {
	t.Parallel()
	mgr, rolling, _, _ := coordinatorTestEnv(t)

	// No adaptive provider wired → zero.
	require.Equal(t, time.Duration(0), mgr.resolveTimelapseCadence("c"))
	require.Equal(t, time.Duration(0), rolling.resolveTimelapseCadence("c"))

	// With a provider that has no entry / zero cadence → zero.
	// (NewMergeManager wires the provider; both resolvers consult it.)
	cfg := &config.AdaptiveRecordingConfig{TimelapseFrameMs: 300}
	mgr2 := NewMergeManager(nil, nil, nil, nil, func(string) *config.AdaptiveRecordingConfig { return cfg }, nil, nil)
	require.Equal(t, 300*time.Millisecond, mgr2.resolveTimelapseCadence("c"))

	off := NewMergeManager(nil, nil, nil, nil, func(string) *config.AdaptiveRecordingConfig { return nil }, nil, nil)
	require.Equal(t, time.Duration(0), off.resolveTimelapseCadence("c"))

	zero := NewMergeManager(nil, nil, nil, nil, func(string) *config.AdaptiveRecordingConfig { return &config.AdaptiveRecordingConfig{} }, nil, nil)
	require.Equal(t, time.Duration(0), zero.resolveTimelapseCadence("c"))
}

func TestMergeManagerRunCancelledContext(t *testing.T) {
	t.Parallel()
	mgr, _, _, _ := coordinatorTestEnv(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// Run must return promptly on a cancelled context (immediate RunOnce
	// observes the cancellation; the loop then exits).
	done := make(chan struct{})
	go func() { mgr.Run(ctx); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not exit on cancelled context")
	}
}

func TestMergeLockTrySemantics(t *testing.T) {
	t.Parallel()
	mgr, _, _, _ := coordinatorTestEnv(t)

	release, ok := mgr.acquireMergeLock("cam-1")
	require.True(t, ok)

	// A second acquisition for the same camera fails (non-blocking).
	_, ok = mgr.acquireMergeLock("cam-1")
	require.False(t, ok)

	// A different camera still acquires.
	release2, ok := mgr.acquireMergeLock("cam-2")
	require.True(t, ok)

	release()
	release2()

	// After release the lock is re-acquirable.
	release3, ok := mgr.acquireMergeLock("cam-1")
	require.True(t, ok)
	release3()
}

func TestConsolidateShortRecord(t *testing.T) {
	t.Parallel()
	_, rolling, db, store := coordinatorTestEnv(t)

	// Two real MP4s with identical SPS/PPS, registered as short-merged rows
	// via RollingReplaceRecordings (the one insert path that persists
	// merge_quality).
	camDir := filepath.Join(store.RootDir(), "cam-1")
	require.NoError(t, os.MkdirAll(camDir, 0o755))

	// Anchor mid-hour so both recordings share one natural-hour merge window
	// (same lesson as mergeTestNow: an HH:59 wall clock straddles windows).
	base := time.Now().UTC().Truncate(time.Hour).Add(-2 * time.Hour).Add(50 * time.Minute)
	for i, id := range []string{"sh-1", "sh-2"} {
		path := createH264SegmentWithSamples(t, camDir, id+".mp4", h264SPS1920, ppsFixture, [][]byte{
			{0x65, 0x88, 0x80, 0x40}, {0x41, 0x10, 0x00, 0x0c},
		})
		start := base.Add(time.Duration(i) * time.Minute)
		require.NoError(t, db.RollingReplaceRecordings(context.Background(), &model.Recording{
			ID: id, CameraID: "cam-1", FilePath: path,
			Format: model.FormatH264, StartedAt: start, EndedAt: start.Add(5 * time.Minute),
			Duration: 300, MergeQuality: "short", FileSize: 100,
		}, "", nil))
	}

	// Unknown camera / fewer than two shorts → no-op.
	n, err := rolling.ConsolidateShortRecord(context.Background(), "ghost", time.Minute)
	require.NoError(t, err)
	require.Equal(t, 0, n)

	// Two shorts merge into one consolidated output.
	n, err = rolling.ConsolidateShortRecord(context.Background(), "cam-1", time.Hour)
	require.NoError(t, err)
	require.GreaterOrEqual(t, n, 1)
}
