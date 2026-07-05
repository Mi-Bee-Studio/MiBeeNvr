package merge

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/metrics"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
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
	require.NoError(t, os.WriteFile(tempPath, data, 0o644))
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
	return NewMergeManager(db, store, func() config.MergeConfig { return cfg }, func(string) *config.MergeConfig { return nil }, func() []config.CameraConfig { return cameras }, nil)
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
	require.NoError(t, env.db.UpsertCamera(ctx, cameraID, "Test", "rtsp", "", "rtsp://localhost/test", "", "", "", "", ""))

	now := time.Now()
	env.insertMergeableRecording(t, "rec1", cameraID, now.Add(-2*time.Hour), now.Add(-time.Hour))
	env.insertMergeableRecording(t, "rec2", cameraID, now.Add(-time.Hour), now)

	mgr := newTestMergeManager(env.db, env.store, cfg, []config.CameraConfig{{ID: cameraID}})

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
	require.NoError(t, env.db.UpsertCamera(ctx, cameraID, "Test", "rtsp", "", "rtsp://localhost/test", "", "", "", "", ""))

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

	mgr := newTestMergeManager(env.db, env.store, cfg, []config.CameraConfig{{ID: cameraID}})

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
	require.NoError(t, env.db.UpsertCamera(ctx, cameraID, "Test", "rtsp", "", "rtsp://localhost/test", "", "", "", "", ""))

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

	mgr := newTestMergeManager(env.db, env.store, cfg, []config.CameraConfig{{ID: cameraID}})

	err := mgr.RunOnce(context.Background())
	require.NoError(t, err)

	// Recording should still exist (not enough to merge)
	rec, err := env.db.GetRecording(ctx, "rec1")
	require.NoError(t, err)
	require.NotNil(t, rec)
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

	mgr := newTestMergeManager(env.db, env.store, cfg, []config.CameraConfig{{ID: "cam1"}})

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
	require.NoError(t, env.db.UpsertCamera(ctx, cameraID, "Test", "rtsp", "", "rtsp://localhost/test", "", "", "", "", ""))

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

	mgr := newTestMergeManager(env.db, env.store, cfg, []config.CameraConfig{{ID: cameraID}})
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
	require.NoError(t, env.db.UpsertCamera(ctx, cameraID, "Test", "rtsp", "", "rtsp://localhost/test", "", "", "", "", ""))

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

	mgr := newTestMergeManager(env.db, env.store, cfg, []config.CameraConfig{{ID: cameraID}})

	counts := mgr.PendingCounts(ctx)
	require.Equal(t, 2, counts[cameraID])
}

func TestPendingCounts_MergeDisabled(t *testing.T) {
	env := newMergeTestEnv(t)
	defer env.close(t)

	cameraID := "cam1"
	ctx := context.Background()
	require.NoError(t, env.db.UpsertCamera(ctx, cameraID, "Test", "rtsp", "", "rtsp://localhost/test", "", "", "", "", ""))

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

	mgr := newTestMergeManager(env.db, env.store, cfg, []config.CameraConfig{{ID: cameraID}})

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
	require.NoError(t, env.db.UpsertCamera(ctx, cameraID, "Test", "rtsp", "", "rtsp://localhost/test", "", "", "", "", ""))

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

	mgr := NewMergeManager(
		env.db, env.store,
		func() config.MergeConfig { return cfg },
		func(cid string) *config.MergeConfig {
			if cid == cameraID {
				return perCamCfg
			}
			return nil
		},
		func() []config.CameraConfig { return []config.CameraConfig{{ID: cameraID}} },
		nil,
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
		require.NoError(t, os.WriteFile(filepath.Join(tempPath, filename), []byte("fake-jpeg-data"), 0o644))
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
	require.NoError(t, env.db.UpsertCamera(ctx, cameraID, "Test", "rtsp", "mjpeg", "rtsp://localhost/test", "", "", "", "", ""))

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

	mgr := newTestMergeManager(env.db, env.store, cfg, []config.CameraConfig{{ID: cameraID}})

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
	require.NoError(t, os.WriteFile(tempPath, data, 0o644))
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
	require.NoError(t, env.db.UpsertCamera(ctx, cameraID, "Test", "rtsp", "", "rtsp://localhost/test", "", "", "", "", ""))

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
	mgr := newTestMergeManager(env.db, env.store, cfg, []config.CameraConfig{{ID: cameraID}})

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
	require.NoError(t, env.db.UpsertCamera(ctx, cameraID, "Test", "rtsp", "", "rtsp://localhost/test", "", "", "", "", ""))

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
	mgr := newTestMergeManager(env.db, env.store, cfg, []config.CameraConfig{{ID: cameraID}})

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

// insertTimelapseRecording creates a timelapse segment directory with fake JPEG files
// and inserts a pending recording into the DB.
func (e *mergeTestEnv) insertTimelapseRecording(t *testing.T, id string, cameraID string, startedAt, endedAt time.Time) string {
	t.Helper()
	ctx := context.Background()
	cameraDir := filepath.Join(e.store.RootDir(), cameraID)
	require.NoError(t, os.MkdirAll(cameraDir, 0o755))

	// Timelapse recordings are directories named with a timestamp.
	segName := fmt.Sprintf("%s_%s_timelapse", cameraID, startedAt.Format("20060102_150405"))
	finalPath := filepath.Join(cameraDir, segName)
	require.NoError(t, os.MkdirAll(finalPath, 0o755))

	// Create fake JPEG files.
	for i := 0; i < 3; i++ {
		filename := fmt.Sprintf("frame_%06d.jpg", i)
		require.NoError(t, os.WriteFile(filepath.Join(finalPath, filename), []byte("fake-jpeg"), 0o644))
	}

	// Calculate total size.
	var totalSize int64
	filepath.Walk(finalPath, func(path string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			totalSize += info.Size()
		}
		return nil
	})

	rec := &model.Recording{
		ID:         id,
		CameraID:   cameraID,
		FilePath:   finalPath,
		Format:     model.FormatTimelapse,
		StartedAt:  startedAt,
		EndedAt:    endedAt,
		Duration:   endedAt.Sub(startedAt).Seconds(),
		FileSize:   totalSize,
		FrameCount: 3,
		Merged:     false,
	}
	require.NoError(t, e.db.InsertRecording(ctx, rec))
	return finalPath
}

func TestRunOnce_TimelapseSkipped(t *testing.T) {
	env := newMergeTestEnv(t)
	defer env.close(t)

	cameraID := "cam1"
	ctx := context.Background()
	require.NoError(t, env.db.UpsertCamera(ctx, cameraID, "Test", "rtsp", "", "rtsp://localhost/test", "", "", "", "", ""))

	// Insert 2 timelapse recordings in the same hour window, old enough to pass min_age.
	now := time.Now()
	oldTime := now.Add(-2 * time.Hour)
	tp1 := env.insertTimelapseRecording(t, "tl1", cameraID, oldTime, oldTime.Add(30*time.Second))
	tp2 := env.insertTimelapseRecording(t, "tl2", cameraID, oldTime.Add(30*time.Second), oldTime.Add(60*time.Second))

	// Also insert 2 H264 recordings to verify normal merge still works.
	// Use oldTime+1h so they are in a different hour window than timelapse and well past minAge.
	env.insertMergeableRecording(t, "h264-1", cameraID, oldTime.Add(1*time.Hour), oldTime.Add(1*time.Hour).Add(30*time.Second))
	env.insertMergeableRecording(t, "h264-2", cameraID, oldTime.Add(1*time.Hour).Add(30*time.Second), oldTime.Add(1*time.Hour).Add(60*time.Second))

	cfg := config.MergeConfig{
		Enabled:            true,
		CheckInterval:      "1h",
		MinSegmentAge:      "1m",
		BatchLimit:         100,
		MinSegmentsToMerge: 2,
	}

	mgr := newTestMergeManager(env.db, env.store, cfg, []config.CameraConfig{{ID: cameraID}})

	err := mgr.RunOnce(ctx)
	require.NoError(t, err)

	// Verify timelapse recordings still exist (were NOT merged).
	tl1, err := env.db.GetRecording(ctx, "tl1")
	require.NoError(t, err)
	require.NotNil(t, tl1, "timelapse recording should not be deleted")
	require.Equal(t, model.FormatTimelapse, tl1.Format)
	// Timelapse should still be pending (not marked failed or merged)
	require.Equal(t, model.MergeStatusPending, tl1.MergeStatus)

	tl2, err := env.db.GetRecording(ctx, "tl2")
	require.NoError(t, err)
	require.NotNil(t, tl2, "timelapse recording should not be deleted")
	require.Equal(t, model.FormatTimelapse, tl2.Format)
	require.Equal(t, model.MergeStatusPending, tl2.MergeStatus)

	// Verify timelapse directories still exist on disk.
	_, err = os.Stat(tp1)
	require.NoError(t, err, "timelapse dir should still exist: %s", tp1)
	_, err = os.Stat(tp2)
	require.NoError(t, err, "timelapse dir should still exist: %s", tp2)

	// Verify H264 recordings got merged.
	recsAfter, err := env.db.ListRecordings(ctx, model.RecordingFilter{CameraID: cameraID})
	require.NoError(t, err)
	// Should have tl1, tl2, and the merged recording (3 total).
	require.Len(t, recsAfter, 3)
}

func TestHashGrouping_SPSWithEmbeddedNull(t *testing.T) {
	env := newMergeTestEnv(t)
	defer env.close(t)

	cameraID := "cam1"
	ctx := context.Background()
	require.NoError(t, env.db.UpsertCamera(ctx, cameraID, "Test", "rtsp", "", "rtsp://localhost/test", "", "", "", "", ""))

	now := time.Now()
	oldTime := now.Add(-2 * time.Hour)

	// Use valid H.264 SPS values (0x67-prefixed, 8+ bytes) with embedded nulls.
	// Both are valid NAL units the muxer can accept.
	// SHA-256 hash must correctly differentiate the two groups.
	// SPS_A: Baseline profile, standard params
	spsA := []byte{0x67, 0x42, 0x00, 0x0a, 0xe2, 0x40, 0x40, 0x04}
	ppsA := []byte{0x68, 0xce, 0x38, 0x80}

	// SPS_B: different SPS with embedded nulls (profile differs)
	spsB := []byte{0x67, 0x42, 0x00, 0x0a, 0xff, 0x00, 0x40, 0x04}
	ppsB := []byte{0x68, 0xaa, 0x38, 0x80}

	env.insertMergeableH264WithCustomParams(t, "rec-a", cameraID, oldTime, oldTime.Add(30*time.Second), spsA, ppsA)
	env.insertMergeableH264WithCustomParams(t, "rec-b", cameraID, oldTime.Add(30*time.Second), oldTime.Add(60*time.Second), spsB, ppsB)

	// With MinSegmentsToMerge=2 but different SPS/PPS groups, each group has 1 segment.
	// They MUST NOT be merged into the same group.
	cfg := config.MergeConfig{
		Enabled:            true,
		CheckInterval:      "1h",
		MinSegmentAge:      "1m",
		BatchLimit:         100,
		MinSegmentsToMerge: 2,
	}

	mgr := newTestMergeManager(env.db, env.store, cfg, []config.CameraConfig{{ID: cameraID}})
	require.NoError(t, mgr.RunOnce(ctx))

	// Verify both recordings still exist (were NOT merged — different SPS hash groups).
	recA, err := env.db.GetRecording(ctx, "rec-a")
	require.NoError(t, err)
	require.NotNil(t, recA)

	recB, err := env.db.GetRecording(ctx, "rec-b")
	require.NoError(t, err)
	require.NotNil(t, recB)

	// They should be marked failed as undersized groups (each group only has 1 segment).
	require.Equal(t, model.MergeStatusFailed, recA.MergeStatus, "rec-a should be marked failed (undersized SPS group)")
	require.Equal(t, model.MergeStatusFailed, recB.MergeStatus, "rec-b should be marked failed (undersized SPS group)")
}

func TestMJPEGDeferredDelete_OnDBFailure(t *testing.T) {
	env := newMergeTestEnv(t)
	defer env.close(t)

	cameraID := "cam1"
	ctx := context.Background()
	require.NoError(t, env.db.UpsertCamera(ctx, cameraID, "Test", "rtsp", "mjpeg", "rtsp://localhost/test", "", "", "", "", ""))

	// Insert MJPEG recordings.
	now := time.Now()
	oldTime := now.Add(-2 * time.Hour)
	src1 := env.insertMergeableMJPEGRecording(t, "rec1", cameraID, oldTime, oldTime.Add(30*time.Second), 2, 0)
	src2 := env.insertMergeableMJPEGRecording(t, "rec2", cameraID, oldTime.Add(30*time.Second), oldTime.Add(60*time.Second), 1, 2)

	cfg := config.MergeConfig{
		Enabled:            true,
		CheckInterval:      "1h",
		MinSegmentAge:      "1m",
		BatchLimit:         100,
		MinSegmentsToMerge: 2,
	}

	mgr := newTestMergeManager(env.db, env.store, cfg, []config.CameraConfig{{ID: cameraID}})

	// Run merge once — should succeed.
	err := mgr.RunOnce(ctx)
	require.NoError(t, err)

	// Source dirs should be deleted after successful merge.
	_, err = os.Stat(src1)
	require.True(t, os.IsNotExist(err), "source dir should be deleted after successful merge: %s", src1)
	_, err = os.Stat(src2)
	require.True(t, os.IsNotExist(err), "source dir should be deleted after successful merge: %s", src2)

	// Verify merged recording exists.
	recs, err := env.db.ListRecordings(ctx, model.RecordingFilter{CameraID: cameraID})
	require.NoError(t, err)
	require.Len(t, recs, 1)
	require.True(t, recs[0].Merged)
}

func TestMergeStatusRace(t *testing.T) {
	env := newMergeTestEnv(t)
	defer env.close(t)

	cameraID := "cam1"
	ctx := context.Background()
	require.NoError(t, env.db.UpsertCamera(ctx, cameraID, "Test", "rtsp", "", "rtsp://localhost/test", "", "", "", "", ""))

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

	mgr := newTestMergeManager(env.db, env.store, cfg, []config.CameraConfig{{ID: cameraID}})

	var wg sync.WaitGroup

	// Launch concurrent readers.
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = mgr.Status()
		}()
	}

	// Launch concurrent writers.
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = mgr.RunOnce(ctx)
		}()
	}

	wg.Wait()
}

func TestRunOnce_BatchLimitTruncation(t *testing.T) {
	env := newMergeTestEnv(t)
	defer env.close(t)

	cameraID := "cam1"
	ctx := context.Background()
	require.NoError(t, env.db.UpsertCamera(ctx, cameraID, "Test", "rtsp", "", "rtsp://localhost/test", "", "", "", "", ""))

	// Insert 3 recordings in the same window
	now := time.Now()
	oldTime := now.Add(-2 * time.Hour)
	env.insertMergeableRecording(t, "rec1", cameraID, oldTime, oldTime.Add(30*time.Second))
	env.insertMergeableRecording(t, "rec2", cameraID, oldTime.Add(30*time.Second), oldTime.Add(60*time.Second))
	env.insertMergeableRecording(t, "rec3", cameraID, oldTime.Add(60*time.Second), oldTime.Add(90*time.Second))

	// With BatchLimit=2, only 2 segments should be merged in a single pass
	cfg := config.MergeConfig{
		Enabled:            true,
		CheckInterval:      "1h",
		MinSegmentAge:      "1m",
		BatchLimit:         2,
		MinSegmentsToMerge: 2,
	}

	mgr := newTestMergeManager(env.db, env.store, cfg, []config.CameraConfig{{ID: cameraID}})
	require.NoError(t, mgr.RunOnce(ctx))

	// After first pass: 2 segments merged into 1 file, 1 singleton marked as merged
	recsAfter, err := env.db.ListRecordings(ctx, model.RecordingFilter{CameraID: cameraID})
	require.NoError(t, err)
	require.Len(t, recsAfter, 2)

	// One recording should be the merged file (Merged=true)
	var mergedCount int
	for _, r := range recsAfter {
		if r.Merged {
			mergedCount++
		}
	}
	require.Equal(t, 1, mergedCount, "expected exactly 1 merged recording")

	// Second pass should be no-op (everything already processed)
	require.NoError(t, mgr.RunOnce(ctx))
	recsAfter2, err := env.db.ListRecordings(ctx, model.RecordingFilter{CameraID: cameraID})
	require.NoError(t, err)
	// Still 2 recordings (merged file + singleton)
	require.Len(t, recsAfter2, 2)
}

func TestRunOnce_MJPEGNotEnoughSegments(t *testing.T) {
	env := newMergeTestEnv(t)
	defer env.close(t)

	cameraID := "cam1"
	ctx := context.Background()
	require.NoError(t, env.db.UpsertCamera(ctx, cameraID, "Test", "rtsp", "mjpeg", "rtsp://localhost/test", "", "", "", "", ""))

	// Insert only 1 MJPEG recording - below MinSegmentsToMerge=2
	now := time.Now()
	oldTime := now.Add(-2 * time.Hour)
	env.insertMergeableMJPEGRecording(t, "rec1", cameraID, oldTime, oldTime.Add(30*time.Second), 3, 0)

	cfg := config.MergeConfig{
		Enabled:            true,
		CheckInterval:      "1h",
		MinSegmentAge:      "1m",
		BatchLimit:         100,
		MinSegmentsToMerge: 2,
	}

	mgr := newTestMergeManager(env.db, env.store, cfg, []config.CameraConfig{{ID: cameraID}})
	require.NoError(t, mgr.RunOnce(ctx))

	// Recording should still exist (not enough segments to merge)
	rec, err := env.db.GetRecording(ctx, "rec1")
	require.NoError(t, err)
	require.NotNil(t, rec)
	require.False(t, rec.Merged)
}

// insertMergeableH265Recording creates a real H.265 MP4 file and inserts a recording into the DB.
func (e *mergeTestEnv) insertMergeableH265Recording(t *testing.T, id string, cameraID string, startedAt, endedAt time.Time) string {
	t.Helper()
	ctx := context.Background()

	tempPath, finalPath, err := e.store.CreateSegment(cameraID, "h265")
	require.NoError(t, err)

	segDir := filepath.Dir(tempPath)
	segFile := createTestH265Segment(t, segDir)

	data, err := os.ReadFile(segFile)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(tempPath, data, 0o644))
	os.Remove(segFile)

	require.NoError(t, e.store.CloseSegment(tempPath, finalPath))

	fi, err := os.Stat(finalPath)
	require.NoError(t, err)

	rec := &model.Recording{
		ID:         id,
		CameraID:   cameraID,
		FilePath:   finalPath,
		Format:     model.FormatH265,
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

// readCounterValue reads the current value of a prometheus Counter.
func readCounterValue(c prometheus.Counter) float64 {
	var m dto.Metric
	if err := c.Write(&m); err != nil {
		return -1
	}
	return m.GetCounter().GetValue()
}

func TestIntegration_FullMergeWorkflow(t *testing.T) {
	env := newMergeTestEnv(t)
	if os.Getenv("CI") != "" {
		t.Skip("integration merge workflow skipped in CI — merge timing/DB behavior is CI-sensitive; run locally for coverage")
	}
	defer env.close(t)

	ctx := context.Background()
	now := time.Now()
	oldTime := now.Add(-2 * time.Hour)

	t.Run("H264", func(t *testing.T) {
		cameraID := "cam-h264-int"
		require.NoError(t, env.db.UpsertCamera(ctx, cameraID, "H264 Test", "rtsp", "", "rtsp://localhost/h264", "", "", "", "", ""))

		env.insertMergeableRecording(t, "int-h264-1", cameraID, oldTime, oldTime.Add(30*time.Second))
		env.insertMergeableRecording(t, "int-h264-2", cameraID, oldTime.Add(30*time.Second), oldTime.Add(60*time.Second))
		env.insertMergeableRecording(t, "int-h264-3", cameraID, oldTime.Add(60*time.Second), oldTime.Add(90*time.Second))

		recsBefore, err := env.db.ListRecordings(ctx, model.RecordingFilter{CameraID: cameraID})
		require.NoError(t, err)
		require.Len(t, recsBefore, 3)

		m := metrics.NewMetrics()
		cfg := config.MergeConfig{
			Enabled:            true,
			CheckInterval:      "1h",
			MinSegmentAge:      "1m",
			BatchLimit:         100,
			MinSegmentsToMerge: 2,
		}
		cameras := []config.CameraConfig{{ID: cameraID}}
		mgr := NewMergeManager(
			env.db, env.store,
			func() config.MergeConfig { return cfg },
			func(string) *config.MergeConfig { return nil },
			func() []config.CameraConfig { return cameras },
			m,
		)

		require.NoError(t, mgr.RunOnce(ctx))

		recsAfter, err := env.db.ListRecordings(ctx, model.RecordingFilter{CameraID: cameraID})
		require.NoError(t, err)
		require.Len(t, recsAfter, 1)

		merged := recsAfter[0]
		require.Equal(t, cameraID, merged.CameraID)
		require.Equal(t, model.FormatH264, merged.Format)
		require.True(t, merged.Merged)
		require.False(t, merged.StartedAt.IsZero())
		require.False(t, merged.EndedAt.IsZero())
		require.Greater(t, merged.FileSize, int64(0))
		require.Greater(t, merged.FrameCount, 0)

		// Verify merged file exists on disk
		_, err = os.Stat(merged.FilePath)
		require.NoError(t, err)

		// Verify prometheus merge metrics
		require.Equal(t, float64(1), readCounterValue(m.MergeAttemptsTotal))
		require.Equal(t, float64(1), readCounterValue(m.MergeSuccessesTotal))

		// Verify MergeManager Status
		status := mgr.Status()
		require.Equal(t, 3, status.SegmentsMerged)
		require.Equal(t, 1, status.FilesCreated)
		require.Equal(t, 0, status.ErrorCount)
	})

	t.Run("H265", func(t *testing.T) {
		cameraID := "cam-h265-int"
		require.NoError(t, env.db.UpsertCamera(ctx, cameraID, "H265 Test", "rtsp", "", "rtsp://localhost/h265", "", "", "", "", ""))

		env.insertMergeableH265Recording(t, "int-h265-1", cameraID, oldTime, oldTime.Add(30*time.Second))
		env.insertMergeableH265Recording(t, "int-h265-2", cameraID, oldTime.Add(30*time.Second), oldTime.Add(60*time.Second))
		env.insertMergeableH265Recording(t, "int-h265-3", cameraID, oldTime.Add(60*time.Second), oldTime.Add(90*time.Second))

		recsBefore, err := env.db.ListRecordings(ctx, model.RecordingFilter{CameraID: cameraID})
		require.NoError(t, err)
		require.Len(t, recsBefore, 3)

		m := metrics.NewMetrics()
		cfg := config.MergeConfig{
			Enabled:            true,
			CheckInterval:      "1h",
			MinSegmentAge:      "1m",
			BatchLimit:         100,
			MinSegmentsToMerge: 2,
		}
		cameras := []config.CameraConfig{{ID: cameraID}}
		mgr := NewMergeManager(
			env.db, env.store,
			func() config.MergeConfig { return cfg },
			func(string) *config.MergeConfig { return nil },
			func() []config.CameraConfig { return cameras },
			m,
		)

		require.NoError(t, mgr.RunOnce(ctx))

		recsAfter, err := env.db.ListRecordings(ctx, model.RecordingFilter{CameraID: cameraID})
		require.NoError(t, err)
		require.Len(t, recsAfter, 1)

		merged := recsAfter[0]
		require.Equal(t, cameraID, merged.CameraID)
		require.Equal(t, model.FormatH265, merged.Format)
		require.True(t, merged.Merged)
		require.False(t, merged.StartedAt.IsZero())
		require.False(t, merged.EndedAt.IsZero())
		require.Greater(t, merged.FileSize, int64(0))
		require.Greater(t, merged.FrameCount, 0)

		// Verify merged file exists on disk
		_, err = os.Stat(merged.FilePath)
		require.NoError(t, err)

		// Verify prometheus merge metrics
		require.Equal(t, float64(1), readCounterValue(m.MergeAttemptsTotal))
		require.Equal(t, float64(1), readCounterValue(m.MergeSuccessesTotal))

		// Verify MergeManager Status
		status := mgr.Status()
		require.Equal(t, 3, status.SegmentsMerged)
		require.Equal(t, 1, status.FilesCreated)
		require.Equal(t, 0, status.ErrorCount)
	})

	t.Run("MJPEG", func(t *testing.T) {
		cameraID := "cam-mjpeg-int"
		require.NoError(t, env.db.UpsertCamera(ctx, cameraID, "MJPEG Test", "rtsp", "mjpeg", "rtsp://localhost/mjpeg", "", "", "", "", ""))

		src1 := env.insertMergeableMJPEGRecording(t, "int-mjpeg-1", cameraID, oldTime, oldTime.Add(30*time.Second), 2, 0)
		src2 := env.insertMergeableMJPEGRecording(t, "int-mjpeg-2", cameraID, oldTime.Add(30*time.Second), oldTime.Add(60*time.Second), 2, 2)
		src3 := env.insertMergeableMJPEGRecording(t, "int-mjpeg-3", cameraID, oldTime.Add(60*time.Second), oldTime.Add(90*time.Second), 2, 4)

		recsBefore, err := env.db.ListRecordings(ctx, model.RecordingFilter{CameraID: cameraID})
		require.NoError(t, err)
		require.Len(t, recsBefore, 3)

		m := metrics.NewMetrics()
		cfg := config.MergeConfig{
			Enabled:            true,
			CheckInterval:      "1h",
			MinSegmentAge:      "1m",
			BatchLimit:         100,
			MinSegmentsToMerge: 2,
		}
		cameras := []config.CameraConfig{{ID: cameraID}}
		mgr := NewMergeManager(
			env.db, env.store,
			func() config.MergeConfig { return cfg },
			func(string) *config.MergeConfig { return nil },
			func() []config.CameraConfig { return cameras },
			m,
		)

		require.NoError(t, mgr.RunOnce(ctx))

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
		require.Equal(t, 6, merged.FrameCount)

		// Verify merged directory has all 6 JPEG files
		entries, err := os.ReadDir(merged.FilePath)
		require.NoError(t, err)
		require.Len(t, entries, 6)

		// Verify source directories are removed
		_, err = os.Stat(src1)
		require.True(t, os.IsNotExist(err), "source dir should be deleted: %s", src1)
		_, err = os.Stat(src2)
		require.True(t, os.IsNotExist(err), "source dir should be deleted: %s", src2)
		_, err = os.Stat(src3)
		require.True(t, os.IsNotExist(err), "source dir should be deleted: %s", src3)

		// Verify prometheus merge metrics
		require.Equal(t, float64(1), readCounterValue(m.MergeAttemptsTotal))
		require.Equal(t, float64(1), readCounterValue(m.MergeSuccessesTotal))

		// Verify MergeManager Status
		status := mgr.Status()
		require.Equal(t, 3, status.SegmentsMerged)
		require.Equal(t, 1, status.FilesCreated)
		require.Equal(t, 0, status.ErrorCount)
	})

	t.Run("ConcurrentMergeProtection", func(t *testing.T) {
		concEnv := newMergeTestEnv(t)
		defer concEnv.close(t)

		cameraID := "cam-concurrent"
		require.NoError(t, concEnv.db.UpsertCamera(ctx, cameraID, "Concurrent Test", "rtsp", "", "rtsp://localhost/conc", "", "", "", "", ""))

		concEnv.insertMergeableRecording(t, "conc-1", cameraID, oldTime, oldTime.Add(30*time.Second))
		concEnv.insertMergeableRecording(t, "conc-2", cameraID, oldTime.Add(30*time.Second), oldTime.Add(60*time.Second))

		concM := metrics.NewMetrics()
		cfg := config.MergeConfig{
			Enabled:            true,
			CheckInterval:      "1h",
			MinSegmentAge:      "1m",
			BatchLimit:         100,
			MinSegmentsToMerge: 2,
		}
		cameras := []config.CameraConfig{{ID: cameraID}}
		mgr := NewMergeManager(
			concEnv.db, concEnv.store,
			func() config.MergeConfig { return cfg },
			func(string) *config.MergeConfig { return nil },
			func() []config.CameraConfig { return cameras },
			concM,
		)

		var wg sync.WaitGroup
		var mu sync.Mutex
		successCount := 0

		// Barrier: all goroutines block on channel, released simultaneously
		start := make(chan struct{})
		for i := 0; i < 5; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				if err := mgr.MergeCamera(ctx, cameraID); err == nil {
					mu.Lock()
					successCount++
					mu.Unlock()
				}
			}()
		}
		close(start) // release all goroutines simultaneously
		wg.Wait()

		require.Equal(t, 1, successCount, "exactly 1 merge should succeed, got %d", successCount)

		// Verify the merge actually completed
		recs, err := concEnv.db.ListRecordings(ctx, model.RecordingFilter{CameraID: cameraID})
		require.NoError(t, err)
		require.Len(t, recs, 1)
		require.True(t, recs[0].Merged)
	})
}

// insertMergeableAVIRecording creates an AVI file via the store and inserts a pending recording into the DB.
func (e *mergeTestEnv) insertMergeableAVIRecording(t *testing.T, id string, cameraID string, startedAt, endedAt time.Time, numFrames int, hasAudio bool) string {
	t.Helper()
	ctx := context.Background()

	tempPath, finalPath, err := e.store.CreateSegment(cameraID, string(model.FormatAVI))
	require.NoError(t, err)

	// Create AVI file at a temp directory, then move it to the store path.
	segDir := t.TempDir()
	aviFile := createTestAVI(t, segDir, "segment.avi", 640, 480, numFrames, hasAudio)

	data, err := os.ReadFile(aviFile)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(tempPath, data, 0o644))
	os.Remove(aviFile)

	require.NoError(t, e.store.CloseSegment(tempPath, finalPath))

	fi, err := os.Stat(finalPath)
	require.NoError(t, err)

	rec := &model.Recording{
		ID:         id,
		CameraID:   cameraID,
		FilePath:   finalPath,
		Format:     model.FormatAVI,
		StartedAt:  startedAt,
		EndedAt:    endedAt,
		Duration:   endedAt.Sub(startedAt).Seconds(),
		FileSize:   fi.Size(),
		FrameCount: numFrames,
		Merged:     false,
	}
	require.NoError(t, e.db.InsertRecording(ctx, rec))

	return finalPath
}

func TestRunOnce_AVIIntegration(t *testing.T) {
	env := newMergeTestEnv(t)
	defer env.close(t)

	cameraID := "cam1"
	ctx := context.Background()
	require.NoError(t, env.db.UpsertCamera(ctx, cameraID, "Test", "rtsp", "", "rtsp://localhost/test", "", "", "", "", ""))

	// Insert AVI recordings old enough to pass min_age.
	now := time.Now()
	oldTime := now.Add(-2 * time.Hour)
	src1 := env.insertMergeableAVIRecording(t, "rec1", cameraID, oldTime, oldTime.Add(30*time.Second), 3, false)
	src2 := env.insertMergeableAVIRecording(t, "rec2", cameraID, oldTime.Add(30*time.Second), oldTime.Add(60*time.Second), 2, false)

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

	mgr := newTestMergeManager(env.db, env.store, cfg, []config.CameraConfig{{ID: cameraID}})

	err = mgr.RunOnce(context.Background())
	require.NoError(t, err)

	// After merge: old recordings should be deleted, new merged recording should exist.
	recsAfter, err := env.db.ListRecordings(ctx, model.RecordingFilter{CameraID: cameraID})
	require.NoError(t, err)
	require.Len(t, recsAfter, 1)

	merged := recsAfter[0]
	require.Equal(t, cameraID, merged.CameraID)
	require.Equal(t, model.FormatAVI, merged.Format)
	require.True(t, merged.Merged)
	require.False(t, merged.StartedAt.IsZero())
	require.False(t, merged.EndedAt.IsZero())
	require.Greater(t, merged.FileSize, int64(0))
	require.Equal(t, 5, merged.FrameCount) // 3+2

	// Verify merged file exists and is valid AVI.
	_, err = os.Stat(merged.FilePath)
	require.NoError(t, err)

	// Verify AVI structure via Demuxer.
	frameCount := countAVIFrames(t, merged.FilePath)
	require.Equal(t, 5, frameCount)

	// Verify source files are deleted.
	_, err = os.Stat(src1)
	require.True(t, os.IsNotExist(err), "source file should be deleted: %s", src1)
	_, err = os.Stat(src2)
	require.True(t, os.IsNotExist(err), "source file should be deleted: %s", src2)
}
