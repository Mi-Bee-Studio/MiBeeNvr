package merge

import (
	"context"
	"fmt"
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
	require.NoError(t, env.db.UpsertCamera(ctx, cameraID, "Test", "rtsp", "", "rtsp://localhost/test", "", "", true, "", "", ""))

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
	require.NoError(t, env.db.UpsertCamera(ctx, cameraID, "Test", "rtsp", "", "rtsp://localhost/test", "", "", true, "", "", ""))

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
	require.NoError(t, env.db.UpsertCamera(ctx, cameraID, "Test", "rtsp", "", "rtsp://localhost/test", "", "", true, "", "", ""))

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
	require.NoError(t, env.db.UpsertCamera(ctx, cameraID, "Test", "rtsp", "", "rtsp://localhost/test", "", "", false, "", "", ""))

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
	require.NoError(t, env.db.UpsertCamera(ctx, cameraID, "Test", "rtsp", "", "rtsp://localhost/test", "", "", true, "", "", ""))

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
	require.NoError(t, env.db.UpsertCamera(ctx, cameraID, "Test", "rtsp", "", "rtsp://localhost/test", "", "", true, "", "", ""))

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
	require.NoError(t, env.db.UpsertCamera(ctx, cameraID, "Test", "rtsp", "", "rtsp://localhost/test", "", "", true, "", "", ""))

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
	require.NoError(t, env.db.UpsertCamera(ctx, cameraID, "Test", "rtsp", "", "rtsp://localhost/test", "", "", true, "", "", ""))

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

// insertMergeableMJPEGRecording creates a MJPEG segment directory with fake JPEG files and inserts a recording into the DB.
// frameStart offsets the frame numbering to avoid filename collisions across segments.
func (e *mergeTestEnv) insertMergeableMJPEGRecording(t *testing.T, id string, cameraID string, startedAt, endedAt time.Time, frameCount, frameStart int) string {
	t.Helper()
	ctx := context.Background()

	// Create a temp MJPEG segment directory via the store.
	tempPath, finalPath, err := e.store.CreateSegment(cameraID, string(model.FormatMJPEG))
	require.NoError(t, err)

	// Create fake JPEG files in the temp directory.
	for i := 0; i < frameCount; i++ {
		filename := fmt.Sprintf("frame%03d.jpg", frameStart+i)
		require.NoError(t, os.WriteFile(filepath.Join(tempPath, filename), []byte("fake-jpeg-data"), 0644))
	}

	// Close segment (atomic rename from temp to final).
	require.NoError(t, e.store.CloseSegment(tempPath, finalPath))

	// Calculate total file size.
	var totalSize int64
	for i := 0; i < frameCount; i++ {
		filename := fmt.Sprintf("frame%03d.jpg", frameStart+i)
		fi, err := os.Stat(filepath.Join(finalPath, filename))
		require.NoError(t, err)
		totalSize += fi.Size()
	}

	rec := &model.Recording{
		ID:         id,
		CameraID:   cameraID,
		FilePath:   finalPath,
		Format:     model.FormatMJPEG,
		StartedAt:  startedAt,
		EndedAt:    endedAt,
		Duration:   endedAt.Sub(startedAt).Seconds(),
		FileSize:   totalSize,
		FrameCount: frameCount,
		Merged:     false,
	}
	require.NoError(t, e.db.InsertRecording(ctx, rec))

	return finalPath
}

func TestRunOnce_MJPEGIntegration(t *testing.T) {
	env := newMergeTestEnv(t)
	defer env.close(t)

	cameraID := "cam1"
	ctx := context.Background()
	require.NoError(t, env.db.UpsertCamera(ctx, cameraID, "Test", "rtsp", "mjpeg", "rtsp://localhost/test", "", "", true, "", "", ""))

	// Insert MJPEG recordings old enough to pass min_age.
	now := time.Now()
	oldTime := now.Add(-2 * time.Hour)
	src1 := env.insertMergeableMJPEGRecording(t, "rec1", cameraID, oldTime, oldTime.Add(30*time.Second), 2, 0)
	src2 := env.insertMergeableMJPEGRecording(t, "rec2", cameraID, oldTime.Add(30*time.Second), oldTime.Add(60*time.Second), 1, 2)

	// Count recordings before merge.
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

	// After merge: old recordings should be deleted, new merged recording should exist.
	recsAfter, err := env.db.ListRecordings(ctx, model.RecordingFilter{CameraID: cameraID})
	require.NoError(t, err)
	require.Len(t, recsAfter, 1)

	merged := recsAfter[0]
	require.Equal(t, cameraID, merged.CameraID)
	require.Equal(t, model.FormatMJPEG, merged.Format)
	require.True(t, merged.Merged)
	require.False(t, merged.StartedAt.IsZero())
	require.False(t, merged.EndedAt.IsZero())
	require.Greater(t, merged.FileSize, int64(0))
	require.Equal(t, 3, merged.FrameCount)

	// Verify merged directory exists and has all 3 JPEG files.
	entries, err := os.ReadDir(merged.FilePath)
	require.NoError(t, err)
	require.Len(t, entries, 3)

	// Verify source directories are removed.
	_, err = os.Stat(src1)
	require.True(t, os.IsNotExist(err), "source dir should be deleted: %s", src1)
	_, err = os.Stat(src2)
	require.True(t, os.IsNotExist(err), "source dir should be deleted: %s", src2)
}

func TestSetMergeStatus(t *testing.T) {
	testSetMergeStatus(t, nil)
}

func testSetMergeStatus(t *testing.T, testDB *storage.DB) {
	t.Helper()
	var db *storage.DB
	var closeDB func()
	if testDB != nil {
		db = testDB
		closeDB = func() {}
	} else {
		dir := t.TempDir()
		dbPath := filepath.Join(dir, "test.db")
		var err error
		db, err = storage.New(dbPath)
		require.NoError(t, err)
		require.NoError(t, db.Init(context.Background()))
		closeDB = func() { db.Close() }
	}
	defer closeDB()
	ctx := context.Background()

	// Insert two recordings.
	for _, id := range []string{"s1", "s2"} {
		require.NoError(t, db.InsertRecording(ctx, &model.Recording{
			ID: id, CameraID: "cam1", FilePath: "/fake.mp4", Format: model.FormatH264,
			StartedAt: time.Now(), EndedAt: time.Now().Add(time.Minute), Duration: 60, FileSize: 100, FrameCount: 30,
		}))
	}

	// Mark both as failed.
	require.NoError(t, db.SetMergeStatus(ctx, []string{"s1", "s2"}, model.MergeStatusFailed))

	// Verify.
	r1, err := db.GetRecording(ctx, "s1")
	require.NoError(t, err)
	require.Equal(t, model.MergeStatusFailed, r1.MergeStatus)
	r2, err := db.GetRecording(ctx, "s2")
	require.NoError(t, err)
	require.Equal(t, model.MergeStatusFailed, r2.MergeStatus)

	// Empty slice is no-op.
	require.NoError(t, db.SetMergeStatus(ctx, nil, model.MergeStatusMerged))
}

// insertBrokenRecording inserts a recording whose file_path points to a non-existent file,
// so ParseSegment will fail.
func (e *mergeTestEnv) insertBrokenRecording(t *testing.T, id, cameraID string, startedAt, endedAt time.Time) {
	t.Helper()
	ctx := context.Background()
	rec := &model.Recording{
		ID:         id,
		CameraID:   cameraID,
		FilePath:   filepath.Join(e.dir, "nonexistent", id+".mp4"),
		Format:     model.FormatH264,
		StartedAt:  startedAt,
		EndedAt:    endedAt,
		Duration:   endedAt.Sub(startedAt).Seconds(),
		FileSize:   100,
		FrameCount: 2,
		Merged:     false,
	}
	require.NoError(t, e.db.InsertRecording(ctx, rec))
}

// insertMergeableH264WithCustomParams creates an H.264 MP4 with the given SPS/PPS and inserts a recording.
func (e *mergeTestEnv) insertMergeableH264WithCustomParams(t *testing.T, id, cameraID string, startedAt, endedAt time.Time, sps, pps []byte) string {
	t.Helper()
	ctx := context.Background()

	tempPath, finalPath, err := e.store.CreateSegment(cameraID, "h264")
	require.NoError(t, err)

	segDir := filepath.Dir(tempPath)
	segFile := createTestH264SegmentWithParams(t, segDir, sps, pps)

	data, err := os.ReadFile(segFile)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(tempPath, data, 0644))
	os.Remove(segFile)

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

func TestRunOnce_ParseFailedMarkedAsFailed(t *testing.T) {
	env := newMergeTestEnv(t)
	defer env.close(t)

	cameraID := "cam1"
	ctx := context.Background()
	require.NoError(t, env.db.UpsertCamera(ctx, cameraID, "Test", "rtsp", "", "rtsp://localhost/test", "", "", true, "", "", ""))

	now := time.Now()
	oldTime := now.Add(-2 * time.Hour)
	// Insert one valid + one broken (parse will fail).
	env.insertMergeableRecording(t, "rec1", cameraID, oldTime, oldTime.Add(30*time.Second))
	env.insertBrokenRecording(t, "rec2", cameraID, oldTime.Add(30*time.Second), oldTime.Add(60*time.Second))

	cfg := config.MergeConfig{
		Enabled:            true,
		CheckInterval:      "1h",
		MinSegmentAge:      "1m",
		BatchLimit:         100,
		MinSegmentsToMerge: 2,
	}
	mgr := newTestMergeManager(env.db, env.store, cfg, []config.CameraConfig{{ID: cameraID, Enabled: true}})

	// First pass: rec2 should be marked failed.
	require.NoError(t, mgr.RunOnce(ctx))

	rec2, err := env.db.GetRecording(ctx, "rec2")
	require.NoError(t, err)
	require.NotNil(t, rec2)
	require.Equal(t, model.MergeStatusFailed, rec2.MergeStatus)

	// Second pass: rec2 should NOT appear in mergeable segments.
	recs, err := env.db.ListMergeableSegments(ctx, cameraID, oldTime.Add(-time.Hour), now.Add(time.Hour))
	require.NoError(t, err)
	for _, r := range recs {
		require.NotEqual(t, "rec2", r.ID, "failed segment should not be mergeable")
	}
}

func TestRunOnce_UndersizedGroupMarkedAsFailed(t *testing.T) {
	env := newMergeTestEnv(t)
	defer env.close(t)

	cameraID := "cam1"
	ctx := context.Background()
	require.NoError(t, env.db.UpsertCamera(ctx, cameraID, "Test", "rtsp", "", "rtsp://localhost/test", "", "", true, "", "", ""))

	now := time.Now()
	oldTime := now.Add(-2 * time.Hour)
	// Insert 2 valid H.264 segments with different SPS/PPS.
	// With MinSegmentsToMerge=2, each SPS/PPS group has only 1 segment → undersized.
	env.insertMergeableH264WithCustomParams(t, "rec1", cameraID, oldTime, oldTime.Add(30*time.Second),
		[]byte{0x67, 0x42, 0x00, 0x0a, 0xe2, 0x40, 0x40, 0x04}, []byte{0x68, 0xce, 0x38, 0x80})
	env.insertMergeableH264WithCustomParams(t, "rec2", cameraID, oldTime.Add(30*time.Second), oldTime.Add(60*time.Second),
		[]byte{0x67, 0x42, 0x00, 0x0a, 0xff, 0x00, 0x40, 0x04}, []byte{0x68, 0xaa, 0x38, 0x80})

	cfg := config.MergeConfig{
		Enabled:            true,
		CheckInterval:      "1h",
		MinSegmentAge:      "1m",
		BatchLimit:         100,
		MinSegmentsToMerge: 2,
	}
	mgr := newTestMergeManager(env.db, env.store, cfg, []config.CameraConfig{{ID: cameraID, Enabled: true}})

	// First pass: both should be marked failed (undersized SPS/PPS groups).
	require.NoError(t, mgr.RunOnce(ctx))

	for _, id := range []string{"rec1", "rec2"} {
		rec, err := env.db.GetRecording(ctx, id)
		require.NoError(t, err)
		require.NotNil(t, rec)
		require.Equal(t, model.MergeStatusFailed, rec.MergeStatus, "segment %s should be marked failed", id)
	}

	// Second pass: none should be mergeable.
	recs, err := env.db.ListMergeableSegments(ctx, cameraID, oldTime.Add(-time.Hour), now.Add(time.Hour))
	require.NoError(t, err)
	require.Empty(t, recs, "failed segments should not be mergeable")
}
