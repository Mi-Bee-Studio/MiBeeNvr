package cleanup

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/stretchr/testify/require"
)

// testEnv holds a temporary DB + storage manager for tests.
type testEnv struct {
	db    *storage.DB
	store *storage.Manager
	dir   string
}

func newTestEnv(t *testing.T) *testEnv {
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

	// Insert default test camera so per-camera cleanup can find it
	require.NoError(t, db.UpsertCamera(ctx, "cam1", "Test Camera", "rtsp", "", "rtsp://localhost/test", "", "", true, "", "", ""))

	return &testEnv{db: db, store: store, dir: dir}
}

func (e *testEnv) close(t *testing.T) {
	t.Helper()
	e.db.Close()
}

// insertTestRecording inserts a recording with full control over fields.
func (e *testEnv) insertTestRecording(t *testing.T, id string, cameraID string, filePath string, endedAt time.Time, merged bool) {
	t.Helper()
	ctx := context.Background()
	fullPath := filepath.Join(e.store.RootDir(), filePath)
	rec := &model.Recording{
		ID:        id,
		CameraID:  cameraID,
		FilePath:  fullPath,
		Format:    model.FormatH264,
		StartedAt: endedAt.Add(-time.Hour),
		EndedAt:   endedAt,
		Duration:  3600.0,
		FileSize:  1024,
		Merged:    merged,
	}
	err := e.db.InsertRecording(ctx, rec)
	require.NoError(t, err)

	// Create the actual file on disk so DeleteFile works
	require.NoError(t, os.MkdirAll(filepath.Dir(fullPath), 0755))
	require.NoError(t, os.WriteFile(fullPath, []byte("fake-data"), 0644))
}

// insertRecordingWithNullEnded inserts a recording where ended_at is NULL (still recording).
func (e *testEnv) insertRecordingWithNullEnded(t *testing.T, id string) {
	t.Helper()
	ctx := context.Background()
	fullPath := filepath.Join(e.store.RootDir(), "still_recording.mp4")
	_, err := e.db.DB().ExecContext(ctx,
	`INSERT INTO recordings(id, camera_id, file_path, format, started_at, ended_at, duration, file_size, frame_count, merged) VALUES(?,?,?,?,?,NULL,?,?,?,?);`,
		id, "cam1", fullPath, model.FormatH264, time.Now(), 0, 0, 0, false,
	)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(fullPath, []byte("still-recording-data"), 0644))
}

func defaultCleanupConfig() config.CleanupConfig {
	return config.CleanupConfig{
		RetentionDays:        30,
		CheckInterval:        "1h",
		DiskThresholdPercent: 95,
	}
}

// --- Tests ---

func TestNewCleanupManager(t *testing.T) {
	env := newTestEnv(t)
	defer env.close(t)

	cfg := defaultCleanupConfig()
	cm, err := NewCleanupManager(env.db, env.store, cfg)
	require.NoError(t, err)
	require.NotNil(t, cm)
	require.Equal(t, 30*time.Hour*24, cm.retention)
	require.Equal(t, 95, cm.diskThreshold)
	require.Equal(t, time.Hour, cm.interval)
}

func TestRunOnce_TimeBasedCleanup(t *testing.T) {
	env := newTestEnv(t)
	defer env.close(t)

	// Use 1 day retention so only recordings older than 1 day are cleaned
	cfg := defaultCleanupConfig()
	cfg.RetentionDays = 1
	cm, err := NewCleanupManager(env.db, env.store, cfg)
	require.NoError(t, err)

	now := time.Now()

	// Old recording → should be deleted
	env.insertTestRecording(t, "old-rec", "cam1", "/old_rec.mp4", now.Add(-48*time.Hour), false)

	// Old merged recording → should ALSO be deleted (merged does NOT protect)
	env.insertTestRecording(t, "old-merged", "cam1", "/old_merged.mp4", now.Add(-48*time.Hour), true)

	// Recent recording → should be KEPT
	env.insertTestRecording(t, "recent-rec", "cam1", "/recent_rec.mp4", now.Add(-1*time.Hour), false)

	// Still recording (ended_at IS NULL) → should be KEPT
	env.insertRecordingWithNullEnded(t, "still-recording")

	err = cm.RunOnce(context.Background())
	require.NoError(t, err)

	// Verify: old-rec deleted
	got, err := env.db.GetRecording(context.Background(), "old-rec")
	require.NoError(t, err)
	require.Nil(t, got)

	// Verify: old-merged also deleted (merged doesn't protect)
	got, err = env.db.GetRecording(context.Background(), "old-merged")
	require.NoError(t, err)
	require.Nil(t, got)

	// Verify: recent-rec kept
	got, err = env.db.GetRecording(context.Background(), "recent-rec")
	require.NoError(t, err)
	require.NotNil(t, got)

	// Verify: still-recording kept
	got, err = env.db.GetRecording(context.Background(), "still-recording")
	require.NoError(t, err)
	require.NotNil(t, got)

	// Verify: file deleted for old-rec
	_, err = os.Stat(filepath.Join(env.store.RootDir(), "/old_rec.mp4"))
	require.True(t, os.IsNotExist(err))

	// Verify: file also deleted for old-merged
	_, err = os.Stat(filepath.Join(env.store.RootDir(), "/old_merged.mp4"))
	require.True(t, os.IsNotExist(err))
}

func TestRunOnce_WithRetentionDays(t *testing.T) {
	env := newTestEnv(t)
	defer env.close(t)

	cfg := defaultCleanupConfig()
	cfg.RetentionDays = 7
	cm, err := NewCleanupManager(env.db, env.store, cfg)
	require.NoError(t, err)

	now := time.Now()

	// 10 days old → expired (> 7 days)
	env.insertTestRecording(t, "expired-10d", "cam1", "/expired_10d.mp4", now.Add(-10*24*time.Hour), false)

	// 5 days old → within retention
	env.insertTestRecording(t, "within-5d", "cam1", "/within_5d.mp4", now.Add(-5*24*time.Hour), false)

	err = cm.RunOnce(context.Background())
	require.NoError(t, err)

	// expired-10d should be deleted
	got, err := env.db.GetRecording(context.Background(), "expired-10d")
	require.NoError(t, err)
	require.Nil(t, got)

	// within-5d should be kept
	got, err = env.db.GetRecording(context.Background(), "within-5d")
	require.NoError(t, err)
	require.NotNil(t, got)
}

func TestRunOnce_TimeBasedCleanup_Ordering(t *testing.T) {
	env := newTestEnv(t)
	defer env.close(t)

	cfg := defaultCleanupConfig()
	cfg.RetentionDays = 1
	cm, err := NewCleanupManager(env.db, env.store, cfg)
	require.NoError(t, err)

	now := time.Now()

	// Insert multiple expired recordings
	env.insertTestRecording(t, "exp-1", "cam1", "/exp1.mp4", now.Add(-72*time.Hour), false)
	env.insertTestRecording(t, "exp-2", "cam1", "/exp2.mp4", now.Add(-48*time.Hour), false)
	env.insertTestRecording(t, "exp-3", "cam1", "/exp3.mp4", now.Add(-25*time.Hour), false)

	err = cm.RunOnce(context.Background())
	require.NoError(t, err)

	// All expired should be deleted
	for _, id := range []string{"exp-1", "exp-2", "exp-3"} {
		got, err := env.db.GetRecording(context.Background(), id)
		require.NoError(t, err)
		require.Nil(t, got, "expected %s to be deleted", id)
	}
}

func TestRunOnce_DiskThresholdCleanup(t *testing.T) {
	env := newTestEnv(t)
	defer env.close(t)

	// Set a very low retention so time-based doesn't interfere
	cfg := defaultCleanupConfig()
	cfg.RetentionDays = 365 // keep everything by time
	cfg.DiskThresholdPercent = 0 // trigger disk cleanup at 0% (always)
	cm, err := NewCleanupManager(env.db, env.store, cfg)
	require.NoError(t, err)

	now := time.Now()

	// Insert several recordings; oldest should be deleted first
	env.insertTestRecording(t, "disk-oldest", "cam1", "/disk_oldest.mp4", now.Add(-100*time.Hour), false)
	env.insertTestRecording(t, "disk-middle", "cam1", "/disk_middle.mp4", now.Add(-50*time.Hour), false)
	env.insertTestRecording(t, "disk-newest", "cam1", "/disk_newest.mp4", now.Add(-1*time.Hour), false)
	env.insertTestRecording(t, "disk-merged", "cam1", "/disk_merged.mp4", now.Add(-200*time.Hour), true) // merged, NOT protected

	err = cm.RunOnce(context.Background())
	require.NoError(t, err)

	// Merged recording is NOT protected from disk cleanup anymore
	got, err := env.db.GetRecording(context.Background(), "disk-merged")
	require.NoError(t, err)
	require.Nil(t, got, "merged recording should be deletable by disk cleanup")

	// Since threshold is 0%, disk cleanup should delete oldest recordings
	// At least the oldest one should be gone (disk usage check will stop when below threshold,
	// but with 0% it will keep trying until all are gone or it can't go lower)
	// We verify the ordering: disk-oldest should be gone, disk-newest might survive.
	got, err = env.db.GetRecording(context.Background(), "disk-oldest")
	require.NoError(t, err)
	require.Nil(t, got, "oldest recording should be deleted by disk cleanup")
}

func TestRunOnce_NoExpiredRecordings(t *testing.T) {
	env := newTestEnv(t)
	defer env.close(t)

	cfg := defaultCleanupConfig()
	cfg.RetentionDays = 7
	cm, err := NewCleanupManager(env.db, env.store, cfg)
	require.NoError(t, err)

	now := time.Now()

	// Only recent recordings
	env.insertTestRecording(t, "recent-1", "cam1", "/recent1.mp4", now.Add(-1*time.Hour), false)
	env.insertTestRecording(t, "recent-2", "cam1", "/recent2.mp4", now.Add(-2*time.Hour), false)

	err = cm.RunOnce(context.Background())
	require.NoError(t, err)

	// Both should be kept
	for _, id := range []string{"recent-1", "recent-2"} {
		got, err := env.db.GetRecording(context.Background(), id)
		require.NoError(t, err)
		require.NotNil(t, got)
	}
}

func TestRunOnce_EmptyDatabase(t *testing.T) {
	env := newTestEnv(t)
	defer env.close(t)

	cfg := defaultCleanupConfig()
	cm, err := NewCleanupManager(env.db, env.store, cfg)
	require.NoError(t, err)

	err = cm.RunOnce(context.Background())
	require.NoError(t, err)
}

func TestRunOnce_FileMissingFromDisk(t *testing.T) {
	env := newTestEnv(t)
	defer env.close(t)

	cfg := defaultCleanupConfig()
	cfg.RetentionDays = 1
	cm, err := NewCleanupManager(env.db, env.store, cfg)
	require.NoError(t, err)

	ctx := context.Background()
	now := time.Now()

	// Insert a recording in DB but don't create the file
	rec := &model.Recording{
		ID:        "no-file",
		CameraID:  "cam1",
		FilePath:  "/nonexistent.mp4",
		Format:    model.FormatH264,
		StartedAt: now.Add(-48 * time.Hour),
		EndedAt:   now.Add(-47 * time.Hour),
		Duration:  3600.0,
		FileSize:  1024,
		Merged:    false,
	}
	require.NoError(t, env.db.InsertRecording(ctx, rec))

	// Should not error even though file doesn't exist
	err = cm.RunOnce(ctx)
	require.NoError(t, err)

	// DB record should still be deleted
	got, err := env.db.GetRecording(ctx, "no-file")
	require.NoError(t, err)
	require.Nil(t, got)
}

func TestRun_ContextCancellation(t *testing.T) {
	env := newTestEnv(t)
	defer env.close(t)

	cfg := defaultCleanupConfig()
	cfg.CheckInterval = "100ms"
	cm, err := NewCleanupManager(env.db, env.store, cfg)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())

	// Insert a recording so first RunOnce does work
	env.insertTestRecording(t, "cancel-test", "cam1", "/cancel.mp4", time.Now().Add(-48*time.Hour), false)

	done := make(chan struct{})
	go func() {
		cm.Run(ctx)
		close(done)
	}()

	// Let it run at least one cycle
	time.Sleep(200 * time.Millisecond)
	cancel()

	select {
	case <-done:
		// Good, it stopped
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop after context cancellation")
	}
}

func TestRunOnce_HealthRetentionCleanup(t *testing.T) {
	env := newTestEnv(t)
	defer env.close(t)

	cfg := defaultCleanupConfig()
	cm, err := NewCleanupManager(env.db, env.store, cfg)
	require.NoError(t, err)

	// Enable health with 1 hour retention
	cm.SetHealthConfig(true, time.Hour)

	ctx := context.Background()
	now := time.Now().UTC()

	// Insert old health events (2 hours ago - past retention)
	for i := 0; i < 3; i++ {
		event := model.HealthEvent{
			CameraID: "cam1",
			EventType: "offline",
			Status: "critical",
			Message: "camera disconnected",
			CreatedAt: now.Add(-2 * time.Hour),
		}
		require.NoError(t, env.db.InsertHealthEvent(ctx, event))
	}

	// Insert recent health events (30 min ago - within retention)
	for i := 0; i < 2; i++ {
		event := model.HealthEvent{
			CameraID: "cam1",
			EventType: "online",
			Status: "ok",
			Message: "camera reconnected",
			CreatedAt: now.Add(-30 * time.Minute),
		}
		require.NoError(t, env.db.InsertHealthEvent(ctx, event))
	}

	// Run cleanup
	err = cm.RunOnce(ctx)
	require.NoError(t, err)

	// Verify: only recent events remain
	filter := storage.HealthEventsFilter{CameraID: "cam1"}
	events, total, err := env.db.ListHealthEvents(ctx, filter)
	require.NoError(t, err)
	require.Equal(t, 2, total, "expected 2 recent events to remain")
	require.Len(t, events, 2)
	for _, e := range events {
		require.Equal(t, "online", e.EventType, "remaining events should be recent online events")
	}
}

func TestRunOnce_HealthRetentionCleanup_Disabled(t *testing.T) {
	env := newTestEnv(t)
	defer env.close(t)

	cfg := defaultCleanupConfig()
	cm, err := NewCleanupManager(env.db, env.store, cfg)
	require.NoError(t, err)

	// Health disabled (default)
	ctx := context.Background()
	now := time.Now().UTC()

	// Insert old health events
	for i := 0; i < 3; i++ {
		event := model.HealthEvent{
			CameraID: "cam1",
			EventType: "offline",
			Status: "critical",
			Message: "camera disconnected",
			CreatedAt: now.Add(-48 * time.Hour),
		}
		require.NoError(t, env.db.InsertHealthEvent(ctx, event))
	}

	// Run cleanup
	err = cm.RunOnce(ctx)
	require.NoError(t, err)

	// Verify: all events still exist (health cleanup skipped)
	filter := storage.HealthEventsFilter{CameraID: "cam1"}
	_, total, err := env.db.ListHealthEvents(ctx, filter)
	require.NoError(t, err)
	require.Equal(t, 3, total, "expected all events to remain when health cleanup disabled")
}

