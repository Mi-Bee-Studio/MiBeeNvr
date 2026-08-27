package cleanup

// Lifecycle and wiring tests: ArchiveDeleter Start/Stop loop, event-bus
// publication on deletion, active-camera provider, adaptive batch sleep,
// and SQLite metrics updates. See #565.

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/event"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/metrics"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

func TestArchiveDeleter_Lifecycle(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t)
	defer env.close(t)

	// A second camera with recordings + files + directory, plus a pending task.
	require.NoError(t, env.db.UpsertCamera(ctx, "cam2", "Doomed Cam", "rtsp", "", "rtsp://localhost/x", "", "", "", "", "", ""))
	env.insertTestRecording(t, "arch-1", "cam2", "cam2/seg1.mp4", time.Now().UTC(), false)
	env.insertTestRecording(t, "arch-2", "cam2", "cam2/seg2.mp4", time.Now().UTC(), false)
	createPendingTask(t, env.db, "cam2")

	d := NewArchiveDeleter(env.db, env.store)
	require.Equal(t, "archive-deleter", d.Name())

	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, d.Start(runCtx))

	// The loop polls every 3s — poll the observable end state (task 'done')
	// instead of sleeping a fixed amount. See #571 anti-flake rules.
	require.Eventually(t, func() bool {
		recent, err := env.db.ListRecentArchiveCleanupTasks(context.Background(), time.Now().Add(-time.Hour))
		if err != nil {
			return false
		}
		for _, task := range recent {
			if task.CameraID == "cam2" && task.Status == "done" {
				return true
			}
		}
		return false
	}, 15*time.Second, 100*time.Millisecond, "task should be processed to done within one poll cycle")

	require.NoError(t, d.Stop())

	require.False(t, recordingExists(t, env, "arch-1"))
	require.False(t, recordingExists(t, env, "arch-2"))
	_, err := os.Stat(filepath.Join(env.store.RootDir(), "cam2"))
	require.True(t, os.IsNotExist(err), "camera directory should be removed")
	cams, err := env.db.ListCameras(ctx)
	require.NoError(t, err)
	for _, cam := range cams {
		require.NotEqual(t, "cam2", cam.ID, "camera row should be removed")
	}
}

func TestArchiveDeleter_MaybePrune(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t)
	defer env.close(t)

	createPendingTask(t, env.db, "prune-old")
	require.NoError(t, env.db.UpdateArchiveCleanupTaskStatus(ctx, "prune-old", "done", ""))
	// Backdate completion past the 1h prune cutoff (deterministic).
	_, err := env.db.DB().ExecContext(ctx,
		`UPDATE archive_cleanup_tasks SET completed_at=? WHERE camera_id=?;`,
		time.Now().UTC().Add(-2*time.Hour), "prune-old")
	require.NoError(t, err)

	createPendingTask(t, env.db, "prune-fresh")
	require.NoError(t, env.db.UpdateArchiveCleanupTaskStatus(ctx, "prune-fresh", "done", ""))

	d := NewArchiveDeleter(env.db, env.store)
	d.maybePrune(ctx)

	tasks, err := env.db.ListRecentArchiveCleanupTasks(ctx, time.Now().Add(-3*time.Hour))
	require.NoError(t, err)
	ids := map[string]bool{}
	for _, task := range tasks {
		ids[task.CameraID] = true
	}
	require.False(t, ids["prune-old"], "done task older than 1h should be pruned")
	require.True(t, ids["prune-fresh"], "recently completed task must survive pruning")
}

func TestRunOnce_TranscodeOrphanHookCalled(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t)
	defer env.close(t)

	called := 0
	cfg := defaultCleanupConfig()
	cm, err := NewCleanupManager(env.db, env.store, cfg)
	require.NoError(t, err)
	cm.SetTranscodeOrphanCleanup(func(ctx context.Context) error {
		called++
		return nil
	})
	require.NoError(t, cm.RunOnce(ctx))
	require.Equal(t, 1, called, "transcode orphan hook should run exactly once per cycle")
}

func TestBatchDelete_PublishesSegmentDeletedEvents(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t)
	defer env.close(t)

	// One expired recording owned by cam1.
	env.insertTestRecording(t, "evt-1", "cam1", "cam1/evt1.mp4", time.Now().UTC(), false)

	bus := event.NewEventBus(16)
	ch := make(chan event.Event, 16)
	require.NoError(t, bus.Subscribe(event.TopicSegmentDeleted, ch, 16))

	cfg := defaultCleanupConfig()
	cm, err := NewCleanupManager(env.db, env.store, cfg)
	require.NoError(t, err)
	cm.SetEventBus(bus)

	rec, err := env.db.GetRecording(ctx, "evt-1")
	require.NoError(t, err)
	require.NotNil(t, rec)

	deleted, err := cm.BatchDeleteRecordingsWithFiles(ctx, []model.Recording{*rec}, "disk_threshold")
	require.NoError(t, err)
	require.Equal(t, []string{"evt-1"}, deleted)

	// Publish is a synchronous non-blocking send — the event is already in the
	// channel buffer when the delete call returns.
	select {
	case evt := <-ch:
		require.Equal(t, event.TopicSegmentDeleted, evt.Topic)
		sd, ok := evt.Data.(event.SegmentDeleted)
		require.True(t, ok, "event payload should be event.SegmentDeleted")
		require.Equal(t, "evt-1", sd.RecordingID)
		require.Equal(t, "cam1", sd.CameraID)
		require.Equal(t, "disk_threshold", sd.Reason)
	default:
		t.Fatal("segment.deleted event should have been published for the deleted recording")
	}
	require.False(t, recordingExists(t, env, "evt-1"))
}

func TestAdaptiveBatchSleep_ContextCancellation(t *testing.T) {
	env := newTestEnv(t)
	defer env.close(t)

	cfg := defaultCleanupConfig()
	cm, err := NewCleanupManager(env.db, env.store, cfg)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	cm.adaptiveBatchSleep(ctx) // must return immediately on cancelled ctx
	require.Less(t, time.Since(start), 500*time.Millisecond, "cancelled context must skip the sleep")

	cm.adaptiveBatchSleep(context.Background()) // fresh DB → WAL < 5MB → ~10ms sleep
}

func TestActiveCameraIDs_ProviderPath(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t)
	defer env.close(t)

	cfg := defaultCleanupConfig()
	cm, err := NewCleanupManager(env.db, env.store, cfg)
	require.NoError(t, err)

	cm.SetActiveCameraProvider(func() []config.CameraConfig {
		return []config.CameraConfig{
			{ID: "cam1"},
			{ID: "  "}, // blank entries must be filtered
			{ID: " cam9 "},
		}
	})
	ids, err := cm.activeCameraIDs(ctx)
	require.NoError(t, err)
	require.Equal(t, []string{"cam1", "cam9"}, ids, "provider IDs should be trimmed and blanks dropped")

	// nil provider → legacy DB fallback (cam1 from the fixture).
	cm.SetActiveCameraProvider(nil)
	ids, err = cm.activeCameraIDs(ctx)
	require.NoError(t, err)
	require.Equal(t, []string{"cam1"}, ids)
}

func TestSetters_NilReceiverSafe(t *testing.T) {
	var cm *CleanupManager
	cm.SetActiveCameraProvider(func() []config.CameraConfig { return nil })
	cm.SetEventBus(nil)
}

func TestUpdateSQLiteMetrics_WithMetricsWired(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t)
	defer env.close(t)

	m := metrics.NewMetrics()
	cfg := defaultCleanupConfig()
	cm, err := NewCleanupManager(env.db, env.store, cfg, m)
	require.NoError(t, err)

	require.NoError(t, cm.RunOnce(ctx))

	require.Greater(t, testutil.ToFloat64(m.SQLiteDBSizeBytes), float64(0),
		"DB size metric should be populated after a cleanup cycle")
	require.GreaterOrEqual(t, testutil.ToFloat64(m.SQLiteOpenConnections), float64(0))
}
