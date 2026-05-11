package merge

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
	"github.com/stretchr/testify/require"
)

// mergeTestEnv holds test dependencies for merge manager tests.
type mergeTestEnv struct {
	db    *storage.DB
	store *storage.Manager
	dir   string
}

func newMergeTestEnv(t *testing.T) *mergeTestEnv {
	t.Helper()
	dir := t.TempDir()

	dbPath := filepath.Join(dir, "test.db")
	db, err := storage.New(dbPath)
	require.NoError(t, err)
	ctx := context.Background()
	require.NoError(t, db.Init(ctx))

	storeDir := filepath.Join(dir, "store")
	store, err := storage.NewManager(storeDir)
	require.NoError(t, err)

	return &mergeTestEnv{db: db, store: store, dir: dir}
}

func (e *mergeTestEnv) close(t *testing.T) {
	t.Helper()
	e.db.Close()
}

// insertMergeableRecording creates a real MP4 file and inserts a recording into the DB.
func (e *mergeTestEnv) insertMergeableRecording(t *testing.T, id string, cameraID string, startedAt, endedAt time.Time) string {
	t.Helper()
	ctx := context.Background()

	// Create a real H.264 MP4 file via the store
	tempPath, finalPath, err := e.store.CreateSegment(cameraID, "h264")
	require.NoError(t, err)

	// Create a valid H.264 segment at the temp path, then rename it
	segDir := filepath.Dir(tempPath)
	segFile := createTestH264Segment(t, segDir)

	// Move the created segment to the temp path
	data, err := os.ReadFile(segFile)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(tempPath, data, 0644))
	os.Remove(segFile)

	// Close segment (atomic rename)
	require.NoError(t, e.store.CloseSegment(tempPath, finalPath))

	fi, err := os.Stat(finalPath)
	require.NoError(t, err)

	rec := &model.Recording{
		ID:         id,
		CameraID:   cameraID,
		FilePath:   finalPath,
		Format:     model.FormatH264,
		StartedAt:  startedAt,
		EndedAt:    endedAt,
		Duration:   endedAt.Sub(startedAt).Seconds(),
		FileSize:   fi.Size(),
		FrameCount: 2,
		Merged:     false,
	}
	require.NoError(t, e.db.InsertRecording(ctx, rec))

	return finalPath
}
// newTestMergeManager creates a MergeManager with the given config for testing.
func newTestMergeManager(db *storage.DB, store *storage.Manager, cfg config.MergeConfig, cameras []config.CameraConfig) *MergeManager {
	return NewMergeManager(db, store, func() config.MergeConfig { return cfg }, func(string) *config.MergeConfig { return nil }, func() []config.CameraConfig { return cameras })
}

func TestRunOnce_NoCameras(t *testing.T) {
	env := newMergeTestEnv(t)
	defer env.close(t)

	cfg := config.MergeConfig{
		Enabled:            true,
		CheckInterval:      "1h",
		MinSegmentAge:      "1m",
		BatchLimit:         100,
		MinSegmentsToMerge: 2,
	}

	mgr := newTestMergeManager(env.db, env.store, cfg, nil)

	err := mgr.RunOnce(context.Background())
	require.NoError(t, err)
}

func TestRunOnce_MergeDisabled(t *testing.T) {
	env := newMergeTestEnv(t)
	defer env.close(t)

	cfg := config.MergeConfig{
		Enabled:            false,
		CheckInterval:      "1h",
		MinSegmentAge:      "1m",
		BatchLimit:         100,
		MinSegmentsToMerge: 2,
	}

	cameraID := "cam1"
	ctx := context.Background()
	require.NoError(t, env.db.UpsertCamera(ctx, cameraID, "Test", "rtsp", "rtsp://localhost/test", "", "", true, "", "", ""))

	now := time.Now()
	env.insertMergeableRecording(t, "rec1", cameraID, now.Add(-2*time.Hour), now.Add(-time.Hour))
	env.insertMergeableRecording(t, "rec2", cameraID, now.Add(-time.Hour), now)

	mgr := newTestMergeManager(env.db, env.store, cfg, []config.CameraConfig{{ID: cameraID, Enabled: true}})

	err := mgr.RunOnce(context.Background())
	require.NoError(t, err)

	// When merge is disabled, RunOnce still returns nil (no error) but should not merge.
	// The original recordings should still exist.
	rec, err := env.db.GetRecording(ctx, "rec1")
	require.NoError(t, err)
	require.NotNil(t, rec)
}

func TestRunOnce_Integration(t *testing.T) {
	env := newMergeTestEnv(t)
	defer env.close(t)

	cameraID := "cam1"
	ctx := context.Background()
	require.NoError(t, env.db.UpsertCamera(ctx, cameraID, "Test", "rtsp", "rtsp://localhost/test", "", "", true, "", "", ""))

	// Insert recordings old enough to pass min_age
	now := time.Now()
	oldTime := now.Add(-2 * time.Hour)
	env.insertMergeableRecording(t, "rec1", cameraID, oldTime, oldTime.Add(30*time.Second))
	env.insertMergeableRecording(t, "rec2", cameraID, oldTime.Add(30*time.Second), oldTime.Add(60*time.Second))

	// Count recordings before merge
	recsBefore, err := env.db.ListRecordings(ctx, model.RecordingFilter{CameraID: cameraID})
	require.NoError(t, err)
	require.Len(t, recsBefore, 2)

	cfg := config.MergeConfig{
		Enabled:            true,
		CheckInterval:      "1h",
		MinSegmentAge:      "1m",
		BatchLimit:         100,
		MinSegmentsToMerge: 2,
	}

	mgr := newTestMergeManager(env.db, env.store, cfg, []config.CameraConfig{{ID: cameraID, Enabled: true}})

	err = mgr.RunOnce(context.Background())
	require.NoError(t, err)

	// After merge: old recordings should be deleted, new merged recording should exist
	recsAfter, err := env.db.ListRecordings(ctx, model.RecordingFilter{CameraID: cameraID})
	require.NoError(t, err)
	// Old recordings deleted, new merged recording added
	require.Len(t, recsAfter, 1)

	merged := recsAfter[0]
	require.Equal(t, cameraID, merged.CameraID)
	require.Equal(t, model.FormatH264, merged.Format)
	require.False(t, merged.StartedAt.IsZero())
	require.False(t, merged.EndedAt.IsZero())
	require.Greater(t, merged.FileSize, int64(0))
	require.Greater(t, merged.FrameCount, 0)

	// Verify merged file exists on disk
	_, err = os.Stat(merged.FilePath)
	require.NoError(t, err)
}

func TestRunOnce_NotEnoughSegments(t *testing.T) {
	env := newMergeTestEnv(t)
	defer env.close(t)

	cameraID := "cam1"
	ctx := context.Background()
	require.NoError(t, env.db.UpsertCamera(ctx, cameraID, "Test", "rtsp", "rtsp://localhost/test", "", "", true, "", "", ""))

	// Only insert 1 recording (below MinSegmentsToMerge=2)
	now := time.Now()
	oldTime := now.Add(-2 * time.Hour)
	env.insertMergeableRecording(t, "rec1", cameraID, oldTime, oldTime.Add(30*time.Second))

	cfg := config.MergeConfig{
		Enabled:            true,
		CheckInterval:      "1h",
		MinSegmentAge:      "1m",
		BatchLimit:         100,
		MinSegmentsToMerge: 2,
	}

	mgr := newTestMergeManager(env.db, env.store, cfg, []config.CameraConfig{{ID: cameraID, Enabled: true}})

	err := mgr.RunOnce(context.Background())
	require.NoError(t, err)

	// Recording should still exist (not enough to merge)
	rec, err := env.db.GetRecording(ctx, "rec1")
	require.NoError(t, err)
	require.NotNil(t, rec)
}

func TestRunOnce_DisabledCamera(t *testing.T) {
	env := newMergeTestEnv(t)
	defer env.close(t)

	cameraID := "cam1"
	ctx := context.Background()
	require.NoError(t, env.db.UpsertCamera(ctx, cameraID, "Test", "rtsp", "rtsp://localhost/test", "", "", false, "", "", ""))

	now := time.Now()
	oldTime := now.Add(-2 * time.Hour)
	env.insertMergeableRecording(t, "rec1", cameraID, oldTime, oldTime.Add(30*time.Second))
	env.insertMergeableRecording(t, "rec2", cameraID, oldTime.Add(30*time.Second), oldTime.Add(60*time.Second))

	cfg := config.MergeConfig{
		Enabled:            true,
		CheckInterval:      "1h",
		MinSegmentAge:      "1m",
		BatchLimit:         100,
		MinSegmentsToMerge: 2,
	}

	mgr := newTestMergeManager(env.db, env.store, cfg, []config.CameraConfig{{ID: cameraID, Enabled: false}})

	err := mgr.RunOnce(context.Background())
	require.NoError(t, err)

	// Recordings should still exist (camera disabled)
	recs, err := env.db.ListRecordings(ctx, model.RecordingFilter{CameraID: cameraID})
	require.NoError(t, err)
	require.Len(t, recs, 2)
}

func TestRunOnce_ContextCancelled(t *testing.T) {
	env := newMergeTestEnv(t)
	defer env.close(t)

	cfg := config.MergeConfig{
		Enabled:            true,
		CheckInterval:      "1h",
		MinSegmentAge:      "1m",
		BatchLimit:         100,
		MinSegmentsToMerge: 2,
	}

	mgr := newTestMergeManager(env.db, env.store, cfg, []config.CameraConfig{{ID: "cam1", Enabled: true}})

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	err := mgr.RunOnce(ctx)
	require.NoError(t, err)
}

func TestStatus_Initial(t *testing.T) {
	env := newMergeTestEnv(t)
	defer env.close(t)

	cfg := config.MergeConfig{
		Enabled:            true,
		CheckInterval:      "1h",
		MinSegmentAge:      "1m",
		BatchLimit:         100,
		MinSegmentsToMerge: 2,
	}

	mgr := newTestMergeManager(env.db, env.store, cfg, nil)

	status := mgr.Status()
	require.True(t, status.LastRunTime.IsZero())
	require.Equal(t, 0, status.SegmentsMerged)
	require.Equal(t, 0, status.FilesCreated)
	require.Equal(t, 0, status.ErrorCount)
}

func TestStatus_AfterRunOnce(t *testing.T) {
	env := newMergeTestEnv(t)
	defer env.close(t)

	cameraID := "cam1"
	ctx := context.Background()
	require.NoError(t, env.db.UpsertCamera(ctx, cameraID, "Test", "rtsp", "rtsp://localhost/test", "", "", true, "", "", ""))

	now := time.Now()
	oldTime := now.Add(-2 * time.Hour)
	env.insertMergeableRecording(t, "rec1", cameraID, oldTime, oldTime.Add(30*time.Second))
	env.insertMergeableRecording(t, "rec2", cameraID, oldTime.Add(30*time.Second), oldTime.Add(60*time.Second))

	cfg := config.MergeConfig{
		Enabled:            true,
		CheckInterval:      "1h",
		MinSegmentAge:      "1m",
		BatchLimit:         100,
		MinSegmentsToMerge: 2,
	}

	mgr := newTestMergeManager(env.db, env.store, cfg, []config.CameraConfig{{ID: cameraID, Enabled: true}})
	require.NoError(t, mgr.RunOnce(ctx))

	status := mgr.Status()
	require.False(t, status.LastRunTime.IsZero())
	require.Equal(t, 2, status.SegmentsMerged)
	require.Equal(t, 1, status.FilesCreated)
	require.Equal(t, 0, status.ErrorCount)
}

func TestPendingCounts(t *testing.T) {
	env := newMergeTestEnv(t)
	defer env.close(t)

	cameraID := "cam1"
	ctx := context.Background()
	require.NoError(t, env.db.UpsertCamera(ctx, cameraID, "Test", "rtsp", "rtsp://localhost/test", "", "", true, "", "", ""))

	now := time.Now()
	oldTime := now.Add(-2 * time.Hour)
	env.insertMergeableRecording(t, "rec1", cameraID, oldTime, oldTime.Add(30*time.Second))
	env.insertMergeableRecording(t, "rec2", cameraID, oldTime.Add(30*time.Second), oldTime.Add(60*time.Second))

	cfg := config.MergeConfig{
		Enabled:            true,
		CheckInterval:      "1h",
		MinSegmentAge:      "1m",
		BatchLimit:         100,
		MinSegmentsToMerge: 2,
	}

	mgr := newTestMergeManager(env.db, env.store, cfg, []config.CameraConfig{{ID: cameraID, Enabled: true}})

	counts := mgr.PendingCounts(ctx)
	require.Equal(t, 2, counts[cameraID])
}

func TestPendingCounts_MergeDisabled(t *testing.T) {
	env := newMergeTestEnv(t)
	defer env.close(t)

	cameraID := "cam1"
	ctx := context.Background()
	require.NoError(t, env.db.UpsertCamera(ctx, cameraID, "Test", "rtsp", "rtsp://localhost/test", "", "", true, "", "", ""))

	now := time.Now()
	oldTime := now.Add(-2 * time.Hour)
	env.insertMergeableRecording(t, "rec1", cameraID, oldTime, oldTime.Add(30*time.Second))
	env.insertMergeableRecording(t, "rec2", cameraID, oldTime.Add(30*time.Second), oldTime.Add(60*time.Second))

	cfg := config.MergeConfig{
		Enabled:            false,
		CheckInterval:      "1h",
		MinSegmentAge:      "1m",
		BatchLimit:         100,
		MinSegmentsToMerge: 2,
	}

	mgr := newTestMergeManager(env.db, env.store, cfg, []config.CameraConfig{{ID: cameraID, Enabled: true}})

	counts := mgr.PendingCounts(ctx)
	// Merge disabled — camera should not appear in counts.
	_, ok := counts[cameraID]
	require.False(t, ok)
}

func TestHotReload_PerCameraConfig(t *testing.T) {
	env := newMergeTestEnv(t)
	defer env.close(t)

	cameraID := "cam1"
	ctx := context.Background()
	require.NoError(t, env.db.UpsertCamera(ctx, cameraID, "Test", "rtsp", "rtsp://localhost/test", "", "", true, "", "", ""))

	now := time.Now()
	oldTime := now.Add(-2 * time.Hour)
	env.insertMergeableRecording(t, "rec1", cameraID, oldTime, oldTime.Add(30*time.Second))
	env.insertMergeableRecording(t, "rec2", cameraID, oldTime.Add(30*time.Second), oldTime.Add(60*time.Second))

	// Start with merge disabled globally.
	cfg := config.MergeConfig{
		Enabled:            false,
		CheckInterval:      "1h",
		MinSegmentAge:      "1m",
		BatchLimit:         100,
		MinSegmentsToMerge: 2,
	}
	perCamCfg := &config.MergeConfig{Enabled: true}

	mgr := NewMergeManager(env.db, env.store,
		func() config.MergeConfig { return cfg },
		func(cid string) *config.MergeConfig {
			if cid == cameraID {
				return perCamCfg
			}
			return nil
		},
		func() []config.CameraConfig { return []config.CameraConfig{{ID: cameraID, Enabled: true}} },
	)

	// Per-camera override enables merge even when global is disabled.
	err := mgr.RunOnce(ctx)
	require.NoError(t, err)

	// After merge: old recordings should be deleted, new merged recording should exist.
	recsAfter, err := env.db.ListRecordings(ctx, model.RecordingFilter{CameraID: cameraID})
	require.NoError(t, err)
	require.Len(t, recsAfter, 1)
	require.True(t, recsAfter[0].Merged)
}
