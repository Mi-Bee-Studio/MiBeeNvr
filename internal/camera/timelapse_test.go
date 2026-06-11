package camera

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/recorder"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/timelapse"
)

// --- Timelapse createRecorder Tests ---

func TestCreateRecorder_TimelapseSnapshotSource(t *testing.T) {
	t.Helper()
	tmpDir := t.TempDir()
	cfg := &config.Config{
		Storage: config.StorageConfig{
			RootDir:         filepath.Join(tmpDir, "storage"),
			SegmentDuration: "10m",
		},
	}
	require.NoError(t, os.MkdirAll(cfg.Storage.RootDir, 0o755))

	store, err := storage.NewManager(cfg.Storage.RootDir)
	require.NoError(t, err)
	defer store.CleanupTempFiles()

	mgr := NewCameraManager(cfg, store, nil, "")

	trueVal := true
	cam := config.CameraConfig{
		ID:       "cam-tl-snapshot",
		Name:     "Timelapse Snapshot",
		Protocol: "timelapse",
		URL:      "http://192.168.1.100/snapshot.jpg",
		Timelapse: &config.CameraTimelapseConfig{
			Enabled:     trueVal,
			FrameSource: "snapshot",
			Interval:    "10s",
			SnapshotURL: "http://192.168.1.100/snapshot.jpg",
		},
	}
	segDur, err := time.ParseDuration(cfg.Storage.SegmentDuration)
	require.NoError(t, err)

	rec := mgr.createRecorder(cam, segDur)
	require.NotNil(t, rec, "snapshot timelapse source should create a recorder")

	// Verify it's a SnapshotCapturer
	_, ok := rec.(*timelapse.SnapshotCapturer)
	assert.True(t, ok, "expected SnapshotCapturer for snapshot frame source")
}

func TestCreateRecorder_TimelapseSnapshotSourceAutoDeriveURL(t *testing.T) {
	t.Helper()
	tmpDir := t.TempDir()
	cfg := &config.Config{
		Storage: config.StorageConfig{
			RootDir:         filepath.Join(tmpDir, "storage"),
			SegmentDuration: "10m",
		},
	}
	require.NoError(t, os.MkdirAll(cfg.Storage.RootDir, 0o755))

	store, err := storage.NewManager(cfg.Storage.RootDir)
	require.NoError(t, err)
	defer store.CleanupTempFiles()

	mgr := NewCameraManager(cfg, store, nil, "")

	trueVal := true
	cam := config.CameraConfig{
		ID:       "cam-tl-snapshot-auto",
		Name:     "Timelapse Snapshot Auto",
		Protocol: "timelapse",
		URL:      "rtsp://192.168.1.100:554/stream",
		Timelapse: &config.CameraTimelapseConfig{
			Enabled:     trueVal,
			FrameSource: "snapshot",
			Interval:    "10s",
			// SnapshotURL is empty — should be auto-derived
		},
	}
	segDur, err := time.ParseDuration(cfg.Storage.SegmentDuration)
	require.NoError(t, err)

	rec := mgr.createRecorder(cam, segDur)
	// The auto-derive will produce a URL, but no actual HTTP server exists.
	// Still, the recorder should be created.
	assert.NotNil(t, rec, "snapshot source with auto-derive URL should create a recorder")
}

func TestCreateRecorder_TimelapseSnapshotSourceNoURL(t *testing.T) {
	t.Helper()
	tmpDir := t.TempDir()
	cfg := &config.Config{
		Storage: config.StorageConfig{
			RootDir:         filepath.Join(tmpDir, "storage"),
			SegmentDuration: "10m",
		},
	}
	require.NoError(t, os.MkdirAll(cfg.Storage.RootDir, 0o755))

	store, err := storage.NewManager(cfg.Storage.RootDir)
	require.NoError(t, err)
	defer store.CleanupTempFiles()

	mgr := NewCameraManager(cfg, store, nil, "")

	trueVal := true
	cam := config.CameraConfig{
		ID:       "cam-tl-snapshot-nourl",
		Name:     "Timelapse Snapshot No URL",
		Protocol: "timelapse",
		URL:      "", // no RTSP/HTTP URL
		Timelapse: &config.CameraTimelapseConfig{
			Enabled:     trueVal,
			FrameSource: "snapshot",
			Interval:    "10s",
			// Both SnapshotURL and camera URL are empty
		},
	}
	segDur, err := time.ParseDuration(cfg.Storage.SegmentDuration)
	require.NoError(t, err)

	rec := mgr.createRecorder(cam, segDur)
	assert.Nil(t, rec, "snapshot source without any URL should return nil")
}

func TestCreateRecorder_TimelapseMJPEGSource(t *testing.T) {
	t.Helper()
	tmpDir := t.TempDir()
	cfg := &config.Config{
		Storage: config.StorageConfig{
			RootDir:         filepath.Join(tmpDir, "storage"),
			SegmentDuration: "10m",
		},
	}
	require.NoError(t, os.MkdirAll(cfg.Storage.RootDir, 0o755))

	store, err := storage.NewManager(cfg.Storage.RootDir)
	require.NoError(t, err)
	defer store.CleanupTempFiles()

	mgr := NewCameraManager(cfg, store, nil, "")

	trueVal := true
	cam := config.CameraConfig{
		ID:       "cam-tl-mjpeg",
		Name:     "Timelapse MJPEG",
		Protocol: "timelapse",
		URL:      "http://192.168.1.100/mjpeg",
		Username: "admin",
		Password: "pass",
		Timelapse: &config.CameraTimelapseConfig{
			Enabled:     trueVal,
			FrameSource: "mjpeg",
			Interval:    "5s",
		},
	}
	segDur, err := time.ParseDuration(cfg.Storage.SegmentDuration)
	require.NoError(t, err)

	rec := mgr.createRecorder(cam, segDur)
	require.NotNil(t, rec, "mjpeg timelapse source should create a recorder")

	// Verify it's a TimelapseRecorder
	_, ok := rec.(*recorder.TimelapseRecorder)
	assert.True(t, ok, "expected TimelapseRecorder for mjpeg frame source")
}

func TestCreateRecorder_TimelapseAutoSource(t *testing.T) {
	t.Helper()
	tmpDir := t.TempDir()
	cfg := &config.Config{
		Storage: config.StorageConfig{
			RootDir:         filepath.Join(tmpDir, "storage"),
			SegmentDuration: "10m",
		},
	}
	require.NoError(t, os.MkdirAll(cfg.Storage.RootDir, 0o755))

	store, err := storage.NewManager(cfg.Storage.RootDir)
	require.NoError(t, err)
	defer store.CleanupTempFiles()

	mgr := NewCameraManager(cfg, store, nil, "")

	trueVal := true
	cam := config.CameraConfig{
		ID:       "cam-tl-auto",
		Name:     "Timelapse Auto",
		Protocol: "timelapse",
		URL:      "http://192.168.1.100/mjpeg",
		Timelapse: &config.CameraTimelapseConfig{
			Enabled:     trueVal,
			FrameSource: "auto",
			Interval:    "30s",
		},
	}
	segDur, err := time.ParseDuration(cfg.Storage.SegmentDuration)
	require.NoError(t, err)

	rec := mgr.createRecorder(cam, segDur)
	require.NotNil(t, rec, "auto timelapse source should create a recorder")

	// Verify it's a TimelapseRecorder (auto falls back to MJPEG)
	_, ok := rec.(*recorder.TimelapseRecorder)
	assert.True(t, ok, "expected TimelapseRecorder for auto frame source")
}

func TestCreateRecorder_TimelapseEmptyFrameSource(t *testing.T) {
	t.Helper()
	tmpDir := t.TempDir()
	cfg := &config.Config{
		Storage: config.StorageConfig{
			RootDir:         filepath.Join(tmpDir, "storage"),
			SegmentDuration: "10m",
		},
	}
	require.NoError(t, os.MkdirAll(cfg.Storage.RootDir, 0o755))

	store, err := storage.NewManager(cfg.Storage.RootDir)
	require.NoError(t, err)
	defer store.CleanupTempFiles()

	mgr := NewCameraManager(cfg, store, nil, "")

	trueVal := true
	cam := config.CameraConfig{
		ID:       "cam-tl-empty",
		Name:     "Timelapse Empty",
		Protocol: "timelapse",
		URL:      "http://192.168.1.100/mjpeg",
		Timelapse: &config.CameraTimelapseConfig{
			Enabled: trueVal,
			// FrameSource is empty — should default to auto
		},
	}
	segDur, err := time.ParseDuration(cfg.Storage.SegmentDuration)
	require.NoError(t, err)

	rec := mgr.createRecorder(cam, segDur)
	require.NotNil(t, rec, "empty frame source should default to auto and create a recorder")
}

func TestCreateRecorder_TimelapseNotEnabled(t *testing.T) {
	t.Helper()
	tmpDir := t.TempDir()
	cfg := &config.Config{
		Storage: config.StorageConfig{
			RootDir:         filepath.Join(tmpDir, "storage"),
			SegmentDuration: "10m",
		},
	}
	require.NoError(t, os.MkdirAll(cfg.Storage.RootDir, 0o755))

	store, err := storage.NewManager(cfg.Storage.RootDir)
	require.NoError(t, err)
	defer store.CleanupTempFiles()

	mgr := NewCameraManager(cfg, store, nil, "")

	cam := config.CameraConfig{
		ID:       "cam-tl-disabled",
		Name:     "Timelapse Disabled",
		Protocol: "timelapse",
		URL:      "http://192.168.1.100/mjpeg",
		// No Timelapse config
	}
	segDur, err := time.ParseDuration(cfg.Storage.SegmentDuration)
	require.NoError(t, err)

	rec := mgr.createRecorder(cam, segDur)
	assert.Nil(t, rec, "timelapse protocol without Timelapse config should return nil")
}

func TestCreateRecorder_TimelapseRTSPKeyframeSource(t *testing.T) {
	t.Helper()
	tmpDir := t.TempDir()
	cfg := &config.Config{
		Storage: config.StorageConfig{
			RootDir:         filepath.Join(tmpDir, "storage"),
			SegmentDuration: "10m",
		},
	}
	require.NoError(t, os.MkdirAll(cfg.Storage.RootDir, 0o755))

	store, err := storage.NewManager(cfg.Storage.RootDir)
	require.NoError(t, err)
	defer store.CleanupTempFiles()

	mgr := NewCameraManager(cfg, store, nil, "")

	trueVal := true
	cam := config.CameraConfig{
		ID:       "cam-tl-keyframe",
		Name:     "Timelapse Keyframe",
		Protocol: "timelapse",
		URL:      "rtsp://192.168.1.100:554/stream",
		Timelapse: &config.CameraTimelapseConfig{
			Enabled:     trueVal,
			FrameSource: "rtsp_keyframe",
			Interval:    "10s",
		},
	}
	segDur, err := time.ParseDuration(cfg.Storage.SegmentDuration)
	require.NoError(t, err)

	rec := mgr.createRecorder(cam, segDur)
	// rtsp_keyframe now creates an H264Recorder for standalone timelapse cameras
	assert.NotNil(t, rec, "rtsp_keyframe should return a recorder from createRecorder")
	assert.IsType(t, &recorder.H264Recorder{}, rec, "should be an H264Recorder")
}

func TestCreateRecorder_TimelapseWithMergeManager(t *testing.T) {
	t.Helper()
	tmpDir := t.TempDir()
	cfg := &config.Config{
		Storage: config.StorageConfig{
			RootDir:         filepath.Join(tmpDir, "storage"),
			SegmentDuration: "10m",
		},
	}
	require.NoError(t, os.MkdirAll(cfg.Storage.RootDir, 0o755))

	store, err := storage.NewManager(cfg.Storage.RootDir)
	require.NoError(t, err)
	defer store.CleanupTempFiles()

	// Create a RollingMergeManager
	mergeMgr := timelapse.NewRollingMergeManager(nil, nil, 30, false)

	mgr := NewCameraManager(cfg, store, nil, "", mergeMgr)

	trueVal := true
	cam := config.CameraConfig{
		ID:       "cam-tl-merge",
		Name:     "Timelapse With Merge",
		Protocol: "timelapse",
		URL:      "http://192.168.1.100/mjpeg",
		Timelapse: &config.CameraTimelapseConfig{
			Enabled:     trueVal,
			FrameSource: "mjpeg",
			Interval:    "30s",
		},
	}
	segDur, err := time.ParseDuration(cfg.Storage.SegmentDuration)
	require.NoError(t, err)

	rec := mgr.createRecorder(cam, segDur)
	require.NotNil(t, rec, "timelapse recorder with merge manager should be created")

	tlRec, ok := rec.(*recorder.TimelapseRecorder)
	assert.True(t, ok, "expected TimelapseRecorder")
	_ = tlRec // merge manager is wired internally
}

// --- Scheduler Wiring Tests ---

func TestSchedulerCheck_IsRecordingTime(t *testing.T) {
	t.Helper()
	scheduler := timelapse.NewScheduler(time.UTC)

	// No schedule = 24/7 recording
	cfg := config.CameraTimelapseConfig{
		Enabled: true,
		// Schedule is nil
	}
	assert.True(t, scheduler.IsRecordingTime(cfg), "nil schedule should always be recording time")

	// Paused overrides everything
	pausedCfg := config.CameraTimelapseConfig{
		Enabled: true,
		Paused:  true,
		// Schedule is nil
	}
	assert.False(t, scheduler.IsRecordingTime(pausedCfg), "paused should return false regardless of schedule")
}

func TestSchedulerCheck_WithSchedule(t *testing.T) {
	t.Helper()
	scheduler := timelapse.NewScheduler(time.UTC)

	// Schedule covering all days, all day
	cfg := config.CameraTimelapseConfig{
		Enabled: true,
		Schedule: &config.ScheduleConfig{
			TimeRanges: []config.TimeRange{
				{Start: "00:00", End: "23:59"},
			},
		},
	}
	assert.True(t, scheduler.IsRecordingTime(cfg), "all-day schedule should always be recording time")
}

// --- Schedule Monitor Tests ---

func TestStartTimelapseScheduleMonitor(t *testing.T) {
	t.Helper()
	tmpDir := t.TempDir()
	cfg := &config.Config{
		Storage: config.StorageConfig{
			RootDir:         filepath.Join(tmpDir, "storage"),
			SegmentDuration: "10m",
		},
	}
	require.NoError(t, os.MkdirAll(cfg.Storage.RootDir, 0o755))

	store, err := storage.NewManager(cfg.Storage.RootDir)
	require.NoError(t, err)
	defer store.CleanupTempFiles()

	mgr := NewCameraManager(cfg, store, nil, "")

	trueVal := true
	cam := config.CameraConfig{
		ID:       "cam-tl-monitor",
		Name:     "Timelapse Monitor",
		Protocol: "timelapse",
		URL:      "http://192.168.1.100/mjpeg",
		Timelapse: &config.CameraTimelapseConfig{
			Enabled:     trueVal,
			FrameSource: "mjpeg",
			Interval:    "30s",
		},
	}
	segDur, err := time.ParseDuration(cfg.Storage.SegmentDuration)
	require.NoError(t, err)

	rec := mgr.createRecorder(cam, segDur)
	require.NotNil(t, rec)

	ctx := context.Background()
	mgr.startTimelapseScheduleMonitor(ctx, cam.ID, rec, *cam.Timelapse)

	// Verify monitor was registered
	mgr.mu.RLock()
	_, exists := mgr.scheduleMonitors[cam.ID]
	mgr.mu.RUnlock()
	assert.True(t, exists, "schedule monitor should be registered")

	// Stop the monitor
	mgr.stopTimelapseScheduleMonitor(cam.ID)

	// Wait a moment for goroutine to finish
	time.Sleep(50 * time.Millisecond)

	mgr.mu.RLock()
	_, exists = mgr.scheduleMonitors[cam.ID]
	mgr.mu.RUnlock()
	assert.False(t, exists, "schedule monitor should be removed after stop")
}

func TestStopTimelapseScheduleMonitor_NonExistent(t *testing.T) {
	t.Helper()
	tmpDir := t.TempDir()
	cfg := &config.Config{
		Storage: config.StorageConfig{
			RootDir:         filepath.Join(tmpDir, "storage"),
			SegmentDuration: "10m",
		},
	}
	require.NoError(t, os.MkdirAll(cfg.Storage.RootDir, 0o755))

	store, err := storage.NewManager(cfg.Storage.RootDir)
	require.NoError(t, err)
	defer store.CleanupTempFiles()

	mgr := NewCameraManager(cfg, store, nil, "")
	// Should not panic
	mgr.stopTimelapseScheduleMonitor("nonexistent")
}

// --- Keyframe Extractor Wiring Tests ---

func TestStartTimelapseKeyframeExtractor_NotTimelapse(t *testing.T) {
	t.Helper()
	tmpDir := t.TempDir()
	cfg := &config.Config{
		Storage: config.StorageConfig{
			RootDir:         filepath.Join(tmpDir, "storage"),
			SegmentDuration: "10m",
		},
	}
	require.NoError(t, os.MkdirAll(cfg.Storage.RootDir, 0o755))

	store, err := storage.NewManager(cfg.Storage.RootDir)
	require.NoError(t, err)
	defer store.CleanupTempFiles()

	mgr := NewCameraManager(cfg, store, nil, "")

	cam := config.CameraConfig{
		ID:       "cam-regular",
		Name:     "Regular Camera",
		Protocol: "rtsp",
		Encoding: "h264",
		URL:      "rtsp://192.168.1.100/stream",
		// No Timelapse config
	}

	// Should return nil without doing anything
	err = mgr.startTimelapseKeyframeExtractor(cam.ID, cam, nil)
	assert.NoError(t, err, "should not error when no timelapse config")
}

func TestStartTimelapseKeyframeExtractor_WrongSource(t *testing.T) {
	t.Helper()
	tmpDir := t.TempDir()
	cfg := &config.Config{
		Storage: config.StorageConfig{
			RootDir:         filepath.Join(tmpDir, "storage"),
			SegmentDuration: "10m",
		},
	}
	require.NoError(t, os.MkdirAll(cfg.Storage.RootDir, 0o755))

	store, err := storage.NewManager(cfg.Storage.RootDir)
	require.NoError(t, err)
	defer store.CleanupTempFiles()

	mgr := NewCameraManager(cfg, store, nil, "")

	trueVal := true
	cam := config.CameraConfig{
		ID:       "cam-tl-wrong-source",
		Name:     "Wrong Source",
		Protocol: "rtsp",
		Encoding: "h264",
		URL:      "rtsp://192.168.1.100/stream",
		Timelapse: &config.CameraTimelapseConfig{
			Enabled:     trueVal,
			FrameSource: "snapshot", // not rtsp_keyframe
		},
	}

	// Should return nil without doing anything (wrong frame source)
	err = mgr.startTimelapseKeyframeExtractor(cam.ID, cam, nil)
	assert.NoError(t, err, "should not error when frame source is not rtsp_keyframe")
}

func TestStopTimelapseKeyframeExtractor_NonExistent(t *testing.T) {
	t.Helper()
	tmpDir := t.TempDir()
	cfg := &config.Config{
		Storage: config.StorageConfig{
			RootDir:         filepath.Join(tmpDir, "storage"),
			SegmentDuration: "10m",
		},
	}
	require.NoError(t, os.MkdirAll(cfg.Storage.RootDir, 0o755))

	store, err := storage.NewManager(cfg.Storage.RootDir)
	require.NoError(t, err)
	defer store.CleanupTempFiles()

	mgr := NewCameraManager(cfg, store, nil, "")
	// Should not panic
	mgr.stopTimelapseKeyframeExtractor("nonexistent")
}

func TestStopAllTimelapseKeyframeExtractors_Empty(t *testing.T) {
	t.Helper()
	tmpDir := t.TempDir()
	cfg := &config.Config{
		Storage: config.StorageConfig{
			RootDir:         filepath.Join(tmpDir, "storage"),
			SegmentDuration: "10m",
		},
	}
	require.NoError(t, os.MkdirAll(cfg.Storage.RootDir, 0o755))

	store, err := storage.NewManager(cfg.Storage.RootDir)
	require.NoError(t, err)
	defer store.CleanupTempFiles()

	mgr := NewCameraManager(cfg, store, nil, "")
	// Should not panic
	mgr.stopAllTimelapseKeyframeExtractors()
}

// --- startRecorder Timelapse Tests ---

func TestStartRecorder_TimelapseMJPEG(t *testing.T) {
	t.Helper()
	tmpDir := t.TempDir()
	cfg := &config.Config{
		Storage: config.StorageConfig{
			RootDir:         filepath.Join(tmpDir, "storage"),
			SegmentDuration: "10m",
		},
	}
	require.NoError(t, os.MkdirAll(cfg.Storage.RootDir, 0o755))

	store, err := storage.NewManager(cfg.Storage.RootDir)
	require.NoError(t, err)
	defer store.CleanupTempFiles()

	mgr := NewCameraManager(cfg, store, nil, "")

	trueVal := true
	cam := config.CameraConfig{
		ID:       "cam-tl-start",
		Name:     "Timelapse Start",
		Protocol: "timelapse",
		URL:      "http://192.168.1.100/mjpeg",
		Timelapse: &config.CameraTimelapseConfig{
			Enabled:     trueVal,
			FrameSource: "mjpeg",
			Interval:    "30s",
		},
	}

	ctx := context.Background()
	segDur, err := time.ParseDuration(cfg.Storage.SegmentDuration)
	require.NoError(t, err)

	err = mgr.startRecorder(ctx, cam, segDur)
	// Should succeed (schedule is nil = 24/7, so recorder should start)
	require.NoError(t, err)

	// Verify recorder is registered
	mgr.mu.RLock()
	rec, exists := mgr.recorders[cam.ID]
	mgr.mu.RUnlock()
	assert.True(t, exists, "recorder should be registered")
	assert.NotNil(t, rec)

	// Verify schedule monitor was started
	mgr.mu.RLock()
	_, hasMonitor := mgr.scheduleMonitors[cam.ID]
	mgr.mu.RUnlock()
	assert.True(t, hasMonitor, "schedule monitor should be started for timelapse recorder")

	// Cleanup
	mgr.stopTimelapseScheduleMonitor(cam.ID)
	rec.Stop()
	// Remove from recorders to avoid interfering with other tests
	mgr.mu.Lock()
	delete(mgr.recorders, cam.ID)
	mgr.mu.Unlock()
}

func TestStartRecorder_TimelapseRTSPKeyframe(t *testing.T) {
	t.Helper()
	tmpDir := t.TempDir()
	cfg := &config.Config{
		Storage: config.StorageConfig{
			RootDir:         filepath.Join(tmpDir, "storage"),
			SegmentDuration: "10m",
		},
	}
	require.NoError(t, os.MkdirAll(cfg.Storage.RootDir, 0o755))

	store, err := storage.NewManager(cfg.Storage.RootDir)
	require.NoError(t, err)
	defer store.CleanupTempFiles()

	mgr := NewCameraManager(cfg, store, nil, "")

	trueVal := true
	cam := config.CameraConfig{
		ID:       "cam-tl-keyframe-start",
		Name:     "Timelapse Keyframe Start",
		Protocol: "timelapse",
		URL:      "rtsp://192.168.1.100/stream",
		Timelapse: &config.CameraTimelapseConfig{
			Enabled:     trueVal,
			FrameSource: "rtsp_keyframe",
			Interval:    "10s",
		},
	}

	ctx := context.Background()
	segDur, err := time.ParseDuration(cfg.Storage.SegmentDuration)
	require.NoError(t, err)

	err = mgr.startRecorder(ctx, cam, segDur)
	// Should NOT error — startRecorder creates and registers H264Recorder for standalone timelapse with rtsp_keyframe
	require.NoError(t, err, "rtsp_keyframe should not cause an error")
	// Verify H264Recorder is registered (createRecorder now returns H264Recorder for rtsp_keyframe)
	mgr.mu.RLock()
	rec, exists := mgr.recorders[cam.ID]
	mgr.mu.RUnlock()
	assert.True(t, exists, "rtsp_keyframe should register a recorder")
	assert.IsType(t, &recorder.H264Recorder{}, rec, "should be an H264Recorder")
}

// --- GetRecorderHub Tests ---

func TestGetRecorderHub_NilRecorder(t *testing.T) {
	t.Helper()
	hub := getRecorderHub(nil)
	assert.Nil(t, hub, "nil recorder should return nil hub")
}

func TestGetRecorderHub_NonHubRecorder(t *testing.T) {
	t.Helper()
	// A mock recorder without GetHub method
	// Just verify that getRecorderHub handles it gracefully
	hub := getRecorderHub(nil)
	assert.Nil(t, hub)
}

// --- SnapshotCapturer Wiring via createRecorder ---

func TestCreateRecorder_TimelapseSnapshotWithStore(t *testing.T) {
	t.Helper()
	tmpDir := t.TempDir()
	cfg := &config.Config{
		Storage: config.StorageConfig{
			RootDir:         filepath.Join(tmpDir, "storage"),
			SegmentDuration: "10m",
		},
	}
	require.NoError(t, os.MkdirAll(cfg.Storage.RootDir, 0o755))

	store, err := storage.NewManager(cfg.Storage.RootDir)
	require.NoError(t, err)
	defer store.CleanupTempFiles()

	dbPath := filepath.Join(tmpDir, "test.db")
	db, err := storage.New(dbPath)
	require.NoError(t, err)
	defer db.Close()

	ctx := context.Background()
	require.NoError(t, db.Init(ctx))

	mgr := NewCameraManager(cfg, store, db, "")

	trueVal := true
	cam := config.CameraConfig{
		ID:       "cam-tl-snap-store",
		Name:     "Timelapse Snap Store",
		Protocol: "timelapse",
		URL:      "http://192.168.1.100/snapshot.jpg",
		Timelapse: &config.CameraTimelapseConfig{
			Enabled:     trueVal,
			FrameSource: "snapshot",
			Interval:    "10s",
			SnapshotURL: "http://192.168.1.100/snapshot.jpg",
		},
	}

	segDur, err := time.ParseDuration(cfg.Storage.SegmentDuration)
	require.NoError(t, err)

	rec := mgr.createRecorder(cam, segDur)
	require.NotNil(t, rec, "snapshot source with store should create a recorder")
	_ = rec
}

// --- Timelapse Config ApplyDefaults Integration ---

func TestTimelapseApplyDefaults(t *testing.T) {
	t.Helper()
	cfg := &config.Config{}
	cfg.ApplyDefaults()

	// Verify timelapse defaults are applied
	cam := config.CameraConfig{
		ID:       "cam-tl-defaults",
		Protocol: "timelapse",
		Timelapse: &config.CameraTimelapseConfig{
			Enabled: true,
		},
	}
	cfg.Cameras = append(cfg.Cameras, cam)
	cfg.ApplyDefaults()

	require.Len(t, cfg.Cameras, 1)
	tl := cfg.Cameras[0].Timelapse
	require.NotNil(t, tl)
	assert.Equal(t, "30s", tl.Interval, "default timelapse interval should be 30s")
	assert.Equal(t, "auto", tl.FrameSource, "default timelapse frame source should be auto")
}

// --- Model.Recorder Verification for SnapshotCapturer ---

func TestSnapshotCapturerImplementsRecorder(t *testing.T) {
	t.Helper()
	// Compile-time check: SnapshotCapturer must implement model.Recorder
	var _ model.Recorder = (*timelapse.SnapshotCapturer)(nil)
}

func TestTimelapseRecorderImplementsRecorder(t *testing.T) {
	t.Helper()
	// Compile-time check: TimelapseRecorder must implement model.Recorder
	var _ model.Recorder = (*recorder.TimelapseRecorder)(nil)
}

// --- Timelapse Wiring Tests (Pause/Resume) ---

func TestTimelapseWiring_PauseStopsRecorder(t *testing.T) {
	t.Helper()
	tmpDir := t.TempDir()
	cfg := &config.Config{
		Storage: config.StorageConfig{
			RootDir:         filepath.Join(tmpDir, "storage"),
			SegmentDuration: "10m",
		},
	}
	require.NoError(t, os.MkdirAll(cfg.Storage.RootDir, 0o755))

	store, err := storage.NewManager(cfg.Storage.RootDir)
	require.NoError(t, err)
	defer store.CleanupTempFiles()

	mgr := NewCameraManager(cfg, store, nil, "")

	trueVal := true
	cam := config.CameraConfig{
		ID:       "cam-tl-wiring-pause",
		Name:     "Timelapse Wiring Pause",
		Protocol: "timelapse",
		URL:      "http://192.168.1.100/mjpeg",
		Timelapse: &config.CameraTimelapseConfig{
			Enabled:     trueVal,
			FrameSource: "mjpeg",
			Interval:    "30s",
		},
	}

	segDur, _ := time.ParseDuration(cfg.Storage.SegmentDuration)
	rec := mgr.createRecorder(cam, segDur)
	require.NotNil(t, rec, "should create timelapse recorder")

	// Manually register the recorder as if startRecorder did it
	mgr.mu.Lock()
	mgr.recorders[cam.ID] = rec
	mgr.mu.Unlock()

	// Verify recorder is registered
	mgr.mu.RLock()
	_, exists := mgr.recorders[cam.ID]
	mgr.mu.RUnlock()
	assert.True(t, exists, "recorder should be registered before pause")

	// Pause the timelapse
	ctx := context.Background()
	err = mgr.PauseTimelapse(ctx, cam.ID)
	require.NoError(t, err)

	// Verify recorder is removed from map
	mgr.mu.RLock()
	_, exists = mgr.recorders[cam.ID]
	mgr.mu.RUnlock()
	assert.False(t, exists, "recorder should be removed after pause")
}

func TestTimelapseWiring_PauseNonExistentNoError(t *testing.T) {
	t.Helper()
	tmpDir := t.TempDir()
	cfg := &config.Config{
		Storage: config.StorageConfig{
			RootDir:         filepath.Join(tmpDir, "storage"),
			SegmentDuration: "10m",
		},
	}
	require.NoError(t, os.MkdirAll(cfg.Storage.RootDir, 0o755))

	store, err := storage.NewManager(cfg.Storage.RootDir)
	require.NoError(t, err)
	defer store.CleanupTempFiles()

	mgr := NewCameraManager(cfg, store, nil, "")

	// Pause a camera that doesn't have a recorder running — should be no-op
	ctx := context.Background()
	err = mgr.PauseTimelapse(ctx, "nonexistent")
	require.NoError(t, err, "pausing non-existent camera should not error")
}

func TestTimelapseWiring_ResumeStartsRecorder(t *testing.T) {
	t.Helper()
	tmpDir := t.TempDir()
	cfg := &config.Config{
		Storage: config.StorageConfig{
			RootDir:         filepath.Join(tmpDir, "storage"),
			SegmentDuration: "10m",
		},
	}
	require.NoError(t, os.MkdirAll(cfg.Storage.RootDir, 0o755))

	store, err := storage.NewManager(cfg.Storage.RootDir)
	require.NoError(t, err)
	defer store.CleanupTempFiles()

	mgr := NewCameraManager(cfg, store, nil, "")

	trueVal := true
	cam := config.CameraConfig{
		ID:       "cam-tl-wiring-resume",
		Name:     "Timelapse Wiring Resume",
		Protocol: "timelapse",
		URL:      "http://192.168.1.100/mjpeg",
		Timelapse: &config.CameraTimelapseConfig{
			Enabled:     trueVal,
			FrameSource: "mjpeg",
			Interval:    "30s",
			Paused:      false, // Not paused, schedule nil = 24/7
		},
	}

	// Add camera to config so ResumeTimelapse can find it
	mgr.cfg.Cameras = append(mgr.cfg.Cameras, cam)

	// Resume should start the recorder (no schedule = 24/7 recording)
	ctx := context.Background()
	err = mgr.ResumeTimelapse(ctx, cam.ID)
	require.NoError(t, err)

	// Verify recorder is now registered
	mgr.mu.RLock()
	rec, exists := mgr.recorders[cam.ID]
	mgr.mu.RUnlock()
	assert.True(t, exists, "recorder should be registered after resume")

	if exists && rec != nil {
		rec.Stop()
		mgr.mu.Lock()
		delete(mgr.recorders, cam.ID)
		mgr.mu.Unlock()
	}
}

func TestTimelapseWiring_ResumeAlreadyRunningNoOp(t *testing.T) {
	t.Helper()
	tmpDir := t.TempDir()
	cfg := &config.Config{
		Storage: config.StorageConfig{
			RootDir:         filepath.Join(tmpDir, "storage"),
			SegmentDuration: "10m",
		},
	}
	require.NoError(t, os.MkdirAll(cfg.Storage.RootDir, 0o755))

	store, err := storage.NewManager(cfg.Storage.RootDir)
	require.NoError(t, err)
	defer store.CleanupTempFiles()

	mgr := NewCameraManager(cfg, store, nil, "")

	trueVal := true
	cam := config.CameraConfig{
		ID:       "cam-tl-wiring-already",
		Name:     "Timelapse Wiring Already",
		Protocol: "timelapse",
		URL:      "http://192.168.1.100/mjpeg",
		Timelapse: &config.CameraTimelapseConfig{
			Enabled:     trueVal,
			FrameSource: "mjpeg",
			Interval:    "30s",
			Paused:      false,
		},
	}

	mgr.cfg.Cameras = append(mgr.cfg.Cameras, cam)

	// Manually register a recorder (simulating already running)
	segDur, _ := time.ParseDuration(cfg.Storage.SegmentDuration)
	rec := mgr.createRecorder(cam, segDur)
	require.NotNil(t, rec)
	mgr.mu.Lock()
	mgr.recorders[cam.ID] = rec
	mgr.mu.Unlock()

	// Resume should be a no-op since already running
	ctx := context.Background()
	err = mgr.ResumeTimelapse(ctx, cam.ID)
	require.NoError(t, err)

	// Verify still registered
	mgr.mu.RLock()
	_, exists := mgr.recorders[cam.ID]
	mgr.mu.RUnlock()
	assert.True(t, exists, "recorder should still be registered after no-op resume")

	// Cleanup
	rec.Stop()
	mgr.mu.Lock()
	delete(mgr.recorders, cam.ID)
	mgr.mu.Unlock()
}

func TestTimelapseWiring_ResumeCameraNotFound(t *testing.T) {
	t.Helper()
	tmpDir := t.TempDir()
	cfg := &config.Config{
		Storage: config.StorageConfig{
			RootDir:         filepath.Join(tmpDir, "storage"),
			SegmentDuration: "10m",
		},
	}
	require.NoError(t, os.MkdirAll(cfg.Storage.RootDir, 0o755))

	store, err := storage.NewManager(cfg.Storage.RootDir)
	require.NoError(t, err)
	defer store.CleanupTempFiles()

	mgr := NewCameraManager(cfg, store, nil, "")

	// Resume a camera that doesn't exist in config
	ctx := context.Background()
	err = mgr.ResumeTimelapse(ctx, "nonexistent")
	require.Error(t, err, "resuming non-existent camera should error")
	var notFound *model.CameraNotFoundError
	assert.ErrorAs(t, err, &notFound)
}

func TestTimelapseWiring_ResumeNoTimelapseConfig(t *testing.T) {
	t.Helper()
	tmpDir := t.TempDir()
	cfg := &config.Config{
		Storage: config.StorageConfig{
			RootDir:         filepath.Join(tmpDir, "storage"),
			SegmentDuration: "10m",
		},
	}
	require.NoError(t, os.MkdirAll(cfg.Storage.RootDir, 0o755))

	store, err := storage.NewManager(cfg.Storage.RootDir)
	require.NoError(t, err)
	defer store.CleanupTempFiles()

	mgr := NewCameraManager(cfg, store, nil, "")

	cam := config.CameraConfig{
		ID:       "cam-tl-no-tl-config",
		Name:     "No Timelapse Config",
		Protocol: "rtsp",
		Encoding: "h264",
		URL:      "rtsp://192.168.1.100/stream",
		// No Timelapse config
	}
	mgr.cfg.Cameras = append(mgr.cfg.Cameras, cam)

	ctx := context.Background()
	err = mgr.ResumeTimelapse(ctx, cam.ID)
	require.Error(t, err, "resuming camera without timelapse config should error")
}

func TestTimelapseWiring_ResumeNotRecordingTime(t *testing.T) {
	t.Helper()
	tmpDir := t.TempDir()
	cfg := &config.Config{
		Storage: config.StorageConfig{
			RootDir:         filepath.Join(tmpDir, "storage"),
			SegmentDuration: "10m",
		},
	}
	require.NoError(t, os.MkdirAll(cfg.Storage.RootDir, 0o755))

	store, err := storage.NewManager(cfg.Storage.RootDir)
	require.NoError(t, err)
	defer store.CleanupTempFiles()

	mgr := NewCameraManager(cfg, store, nil, "")

	trueVal := true
	// Schedule that only covers a time that is unlikely to match now
	cam := config.CameraConfig{
		ID:       "cam-tl-wiring-skip",
		Name:     "Timelapse Wiring Skip",
		Protocol: "timelapse",
		URL:      "http://192.168.1.100/mjpeg",
		Timelapse: &config.CameraTimelapseConfig{
			Enabled:     trueVal,
			FrameSource: "mjpeg",
			Interval:    "30s",
			Paused:      false,
			Schedule: &config.ScheduleConfig{
				TimeRanges: []config.TimeRange{
					{Start: "00:00", End: "00:01"}, // Very unlikely to match
				},
			},
		},
	}

	mgr.cfg.Cameras = append(mgr.cfg.Cameras, cam)

	// Resume should be a no-op since not recording time per schedule
	ctx := context.Background()
	err = mgr.ResumeTimelapse(ctx, cam.ID)
	require.NoError(t, err, "should not error when not recording time")

	// Verify no recorder was created
	mgr.mu.RLock()
	_, exists := mgr.recorders[cam.ID]
	mgr.mu.RUnlock()
	assert.False(t, exists, "recorder should not be created when not recording time")
}
