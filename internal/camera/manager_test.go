package camera

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/metrics"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/onvif"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/recorder"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
)


func testConfig() *config.Config {
	return &config.Config{
		Storage: config.StorageConfig{
			RootDir:         "/tmp/mibee-nvr-test-camera",
			SegmentDuration: "1m",
		},
		Cameras: []config.CameraConfig{
			{
				ID:       "cam-h264",
				Name:     "H264 Camera",
				Protocol: "rtsp",
				Encoding: "h264",
				URL:      "rtsp://127.0.0.1:1/stream",
				Enabled:  true,
			},
			{
				ID:       "cam-mjpeg",
				Name:     "MJPEG Camera",
				Protocol: "rtsp",
				Encoding: "mjpeg",
				URL:      "rtsp://127.0.0.1:1/stream",
				Enabled:  true,
			},
			{
				ID:       "cam-disabled",
				Name:     "Disabled Camera",
				Protocol: "rtsp",
				Encoding: "h264",
				URL:      "rtsp://127.0.0.1:1/stream",
				Enabled:  false,
			},
			{
				ID:       "cam-jpeg",
				Name:     "JPEG Camera",
				Protocol: "http",
				Encoding: "jpeg",
				URL:      "http://192.168.1.13/jpg",
				Enabled:  true,
			},
		},
	}
}

func newTestManager(t *testing.T) (*CameraManager, *storage.Manager, *storage.DB, string) {
	t.Helper()
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	cfg := testConfig()
	cfg.Storage.RootDir = filepath.Join(tmpDir, "storage")
	require.NoError(t, os.MkdirAll(cfg.Storage.RootDir, 0o755))
	require.NoError(t, config.Save(configPath, cfg))

	dbPath := filepath.Join(tmpDir, "test.db")
	db, err := storage.New(dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })

	ctx := context.Background()
	require.NoError(t, db.Init(ctx))

	store, err := storage.NewManager(cfg.Storage.RootDir)
	require.NoError(t, err)
	t.Cleanup(func() { store.CleanupTempFiles() })

	mgr := NewCameraManager(cfg, store, db, configPath)
	return mgr, store, db, configPath
}

func TestNewCameraManager(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := testConfig()
	cfg.Storage.RootDir = filepath.Join(tmpDir, "storage")
	require.NoError(t, os.MkdirAll(cfg.Storage.RootDir, 0o755))

	store, err := storage.NewManager(cfg.Storage.RootDir)
	require.NoError(t, err)
	defer store.CleanupTempFiles()

	mgr := NewCameraManager(cfg, store, nil, "")
	assert.NotNil(t, mgr)
	assert.Equal(t, 0, mgr.RecorderCount())
}

func TestStart_EnabledCameras(t *testing.T) {
	mgr, _, _, _ := newTestManager(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := mgr.Start(ctx)
	require.NoError(t, err)

	// Should have created recorders for h264, mjpeg, and http_jpeg cameras
	// (disabled camera is skipped)
	assert.Equal(t, 3, mgr.RecorderCount())

	statuses := mgr.Status()
	assert.Len(t, statuses, 3)
	_, hasH264 := statuses["cam-h264"]
	_, hasMJPEG := statuses["cam-mjpeg"]
	assert.True(t, hasH264, "should have h264 recorder")
	assert.True(t, hasMJPEG, "should have mjpeg recorder")
	_, hasDisabled := statuses["cam-disabled"]
	assert.False(t, hasDisabled, "should not have disabled recorder")
	_, hasJPEG := statuses["cam-jpeg"]
	assert.True(t, hasJPEG, "should have http_jpeg recorder")
}

func TestStart_DisabledCamerasSkipped(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := &config.Config{
		Storage: config.StorageConfig{
			RootDir:         filepath.Join(tmpDir, "storage"),
			SegmentDuration: "1m",
		},
		Cameras: []config.CameraConfig{
			{
				ID:       "cam-1",
				Protocol: "rtsp",
				Encoding: "h264",
				URL:      "rtsp://192.168.1.10:554/stream",
				Enabled:  false,
			},
		},
	}
	require.NoError(t, os.MkdirAll(cfg.Storage.RootDir, 0o755))

	dbPath := filepath.Join(tmpDir, "test.db")
	db, err := storage.New(dbPath)
	require.NoError(t, err)
	defer db.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	_ = db.Init(ctx)

	store, err := storage.NewManager(cfg.Storage.RootDir)
	require.NoError(t, err)
	defer store.CleanupTempFiles()

	mgr := NewCameraManager(cfg, store, db, "")
	err = mgr.Start(ctx)
	require.NoError(t, err)
	assert.Equal(t, 0, mgr.RecorderCount())
}

func TestStart_HTTPJPEGRecorderCreated(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := &config.Config{
		Storage: config.StorageConfig{
			RootDir:         filepath.Join(tmpDir, "storage"),
			SegmentDuration: "1m",
		},
		Cameras: []config.CameraConfig{
			{
				ID:       "cam-1",
				Protocol: "http",
				Encoding: "jpeg",
				Enabled:  true,
			},
		},
	}
	require.NoError(t, os.MkdirAll(cfg.Storage.RootDir, 0o755))

	dbPath := filepath.Join(tmpDir, "test.db")
	db, err := storage.New(dbPath)
	require.NoError(t, err)
	defer db.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	_ = db.Init(ctx)

	store, err := storage.NewManager(cfg.Storage.RootDir)
	require.NoError(t, err)
	defer store.CleanupTempFiles()

	mgr := NewCameraManager(cfg, store, db, "")
	err = mgr.Start(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, mgr.RecorderCount())
	_, hasJPEG := mgr.Status()["cam-1"]
	assert.True(t, hasJPEG, "should have http_jpeg recorder")
}

func TestStart_InvalidSegmentDuration(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := &config.Config{
		Storage: config.StorageConfig{
			RootDir:         filepath.Join(tmpDir, "storage"),
			SegmentDuration: "not-a-duration",
		},
		Cameras: []config.CameraConfig{
			{
				ID:       "cam-1",
				Protocol: "rtsp",
				Encoding: "h264",
				Enabled:  true,
			},
		},
	}
	require.NoError(t, os.MkdirAll(cfg.Storage.RootDir, 0o755))

	dbPath := filepath.Join(tmpDir, "test.db")
	db, err := storage.New(dbPath)
	require.NoError(t, err)
	defer db.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	_ = db.Init(ctx)

	store, err := storage.NewManager(cfg.Storage.RootDir)
	require.NoError(t, err)
	defer store.CleanupTempFiles()

	mgr := NewCameraManager(cfg, store, db, "")
	err = mgr.Start(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid segment duration")
}

func TestStop(t *testing.T) {
	mgr, _, _, _ := newTestManager(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := mgr.Start(ctx)
	require.NoError(t, err)
	assert.Equal(t, 3, mgr.RecorderCount())

	// Give recorders a moment to start their goroutines
	time.Sleep(100 * time.Millisecond)

	err = mgr.Stop()
	require.NoError(t, err)

	// After stop, recorders should still be in the map (not removed)
	assert.Equal(t, 3, mgr.RecorderCount())

	// Status should be stopped
	statuses := mgr.Status()
	for _, s := range statuses {
		assert.Equal(t, model.StatusStopped, s)
	}

	time.Sleep(100 * time.Millisecond)

	err = mgr.Stop()
	require.NoError(t, err)

	// After stop, recorders should still be in the map (not removed)
	assert.Equal(t, 3, mgr.RecorderCount())

	// Status should be stopped
}

func TestStop_EmptyManager(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := testConfig()
	cfg.Storage.RootDir = filepath.Join(tmpDir, "storage")
	require.NoError(t, os.MkdirAll(cfg.Storage.RootDir, 0o755))

	store, err := storage.NewManager(cfg.Storage.RootDir)
	require.NoError(t, err)
	defer store.CleanupTempFiles()

	mgr := NewCameraManager(cfg, store, nil, "")
	err = mgr.Stop()
	require.NoError(t, err)
}

func TestStatus_EmptyManager(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := testConfig()
	cfg.Storage.RootDir = filepath.Join(tmpDir, "storage")
	require.NoError(t, os.MkdirAll(cfg.Storage.RootDir, 0o755))

	store, err := storage.NewManager(cfg.Storage.RootDir)
	require.NoError(t, err)
	defer store.CleanupTempFiles()

	mgr := NewCameraManager(cfg, store, nil, "")
	statuses := mgr.Status()
	assert.NotNil(t, statuses)
	assert.Empty(t, statuses)
}

func TestCameraStatus_Unknown(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := testConfig()
	cfg.Storage.RootDir = filepath.Join(tmpDir, "storage")
	require.NoError(t, os.MkdirAll(cfg.Storage.RootDir, 0o755))

	store, err := storage.NewManager(cfg.Storage.RootDir)
	require.NoError(t, err)
	defer store.CleanupTempFiles()

	mgr := NewCameraManager(cfg, store, nil, "")
	status := mgr.CameraStatus("nonexistent")
	assert.Equal(t, model.StatusError, status)
}

func TestCameraStatus_Known(t *testing.T) {
	mgr, _, _, _ := newTestManager(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := mgr.Start(ctx)
	require.NoError(t, err)

	status := mgr.CameraStatus("cam-h264")
	// Status will be recording or reconnecting (since no real RTSP server)
	assert.Contains(t, []model.RecorderStatus{
		model.StatusRecording,
		model.StatusReconnecting,
	}, status)
}

func TestGracefulShutdown(t *testing.T) {
	mgr, _, _, _ := newTestManager(t)

	ctx, cancel := context.WithCancel(context.Background())
	err := mgr.Start(ctx)
	require.NoError(t, err)
	assert.Equal(t, 3, mgr.RecorderCount())

	// Let recorders run briefly
	time.Sleep(100 * time.Millisecond)

	// Cancel context to signal shutdown
	cancel()

	// Stop should complete promptly
	done := make(chan error, 1)
	go func() {
		done <- mgr.Stop()
	}()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("Stop() did not complete in time")
	}

	statuses := mgr.Status()
	for _, s := range statuses {
		assert.Equal(t, model.StatusStopped, s)
	}
}

func TestStart_UnknownProtocol(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := &config.Config{
		Storage: config.StorageConfig{
			RootDir:         filepath.Join(tmpDir, "storage"),
			SegmentDuration: "1m",
		},
		Cameras: []config.CameraConfig{
			{
				ID:       "cam-1",
				Protocol: "onvif",
				URL:      "rtsp://192.168.1.10:554/stream",
				Enabled:  true,
			},
		},
	}
	require.NoError(t, os.MkdirAll(cfg.Storage.RootDir, 0o755))

	dbPath := filepath.Join(tmpDir, "test.db")
	db, err := storage.New(dbPath)
	require.NoError(t, err)
	defer db.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	_ = db.Init(ctx)

	store, err := storage.NewManager(cfg.Storage.RootDir)
	require.NoError(t, err)
	defer store.CleanupTempFiles()

	mgr := NewCameraManager(cfg, store, db, "")
	err = mgr.Start(ctx)
	require.NoError(t, err) // should not fail, just skip unknown protocol
	assert.Equal(t, 0, mgr.RecorderCount())
}

func TestStart_InsertsCameraRecords(t *testing.T) {
	mgr, _, db, _ := newTestManager(t)

	// Initialize database
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start camera manager
	err := mgr.Start(ctx)
	require.NoError(t, err)

	// Check that enabled cameras are in the database
	cameras, err := db.ListCameras(ctx)
	require.NoError(t, err)
	require.Len(t, cameras, 4, "should have 4 cameras in database (including disabled)")

	// Verify camera records exist and have correct data
	cameraMap := make(map[string]storage.CameraRow)
	for _, cam := range cameras {
		cameraMap[cam.ID] = cam
	}

	// Check H264 camera
	h264Cam, exists := cameraMap["cam-h264"]
	require.True(t, exists, "H264 camera should be in database")
	assert.Equal(t, "H264 Camera", h264Cam.Name)
	assert.Equal(t, "rtsp", h264Cam.Protocol)
	assert.True(t, h264Cam.Enabled)

	// Check MJPEG camera
	mjpegCam, exists := cameraMap["cam-mjpeg"]
	require.True(t, exists, "MJPEG camera should be in database")
	assert.Equal(t, "MJPEG Camera", mjpegCam.Name)
	assert.Equal(t, "rtsp", mjpegCam.Protocol)
	assert.True(t, mjpegCam.Enabled)

	// Verify disabled camera IS in database (all cameras are inserted)
	_, exists = cameraMap["cam-disabled"]
	require.True(t, exists, "Disabled camera should be in database")

	// Verify JPEG camera IS in database (all cameras are inserted)
	_, exists = cameraMap["cam-jpeg"]
	require.True(t, exists, "JPEG camera should be in database")
}

// --- CRUD lifecycle tests ---

func TestAddCamera_EnabledH264(t *testing.T) {
	mgr, _, _, _ := newTestManager(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	id, err := mgr.AddCamera(ctx, config.CameraConfig{
		ID:       "cam-new-h264",
		Name:     "New H264 Camera",
		Protocol: "rtsp",
			Encoding: "h264",
		Enabled:  true,
	})
	require.NoError(t, err)
	assert.Equal(t, "cam-new-h264", id)

	// Recorder should be created
	_, ok := mgr.recorders["cam-new-h264"]
	assert.True(t, ok, "recorder should be created for enabled h264 camera")
	assert.Equal(t, 1, mgr.RecorderCount())

	// Camera should be in config
	assert.Len(t, mgr.cfg.Cameras, 5) // 4 original + 1 new
}

func TestAddCamera_Disabled(t *testing.T) {
	mgr, _, _, _ := newTestManager(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	id, err := mgr.AddCamera(ctx, config.CameraConfig{
		ID:       "cam-new-disabled",
		Name:     "Disabled Camera",
			Protocol: "rtsp",
				Encoding: "h264",
		Enabled:  false,
	})
	require.NoError(t, err)
	assert.Equal(t, "cam-new-disabled", id)

	// No recorder should be created
	assert.Equal(t, 0, mgr.RecorderCount())
}

func TestAddCamera_HTTPJPEG(t *testing.T) {
	mgr, _, _, _ := newTestManager(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	id, err := mgr.AddCamera(ctx, config.CameraConfig{
		ID:       "cam-new-jpeg",
		Name:     "JPEG Camera",
			Protocol: "http",
				Encoding: "jpeg",
		Enabled:  true,
	})
	require.NoError(t, err)
	assert.Equal(t, "cam-new-jpeg", id)

	// Recorder should be created for http_jpeg
	_, ok := mgr.recorders["cam-new-jpeg"]
	assert.True(t, ok, "recorder should be created for http_jpeg camera")
	assert.Equal(t, 1, mgr.RecorderCount())
}

func TestAddCamera_DuplicateID(t *testing.T) {
	mgr, _, _, _ := newTestManager(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	_, err := mgr.AddCamera(ctx, config.CameraConfig{
		ID:       "cam-h264", // duplicate
		Name:     "Dup Camera",
		Protocol: "rtsp",
				Encoding: "h264",
		Enabled:  true,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")
}

func TestAddCamera_AutoID(t *testing.T) {
	mgr, _, _, _ := newTestManager(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	id, err := mgr.AddCamera(ctx, config.CameraConfig{
		ID:       "", // empty → auto-generate
		Name:     "Auto ID Camera",
			Protocol: "rtsp",
				Encoding: "h264",
		Enabled:  false,
	})
	require.NoError(t, err)
	assert.NotEmpty(t, id)
	assert.True(t, len(id) > 4, "auto-generated ID should have cam- prefix")
}

func TestAddCamera_Persists(t *testing.T) {
	mgr, _, _, configPath := newTestManager(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	_, err := mgr.AddCamera(ctx, config.CameraConfig{
		ID:       "cam-persist",
		Name:     "Persist Camera",
			Protocol: "rtsp",
				Encoding: "h264",
		Enabled:  false,
	})
	require.NoError(t, err)

	// Reload config from disk
	loaded, err := config.Load(configPath)
	require.NoError(t, err)
	found := false
	for _, cam := range loaded.Cameras {
		if cam.ID == "cam-persist" {
			found = true
			assert.Equal(t, "Persist Camera", cam.Name)
			break
		}
	}
	assert.True(t, found, "camera should be persisted to config file")
}

func TestRemoveCamera_WithRecorder(t *testing.T) {
	mgr, _, _, _ := newTestManager(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start the manager to create recorders
	err := mgr.Start(ctx)
	require.NoError(t, err)
	assert.Equal(t, 3, mgr.RecorderCount())

	// Remove a camera that has a recorder
	err = mgr.RemoveCamera(ctx, "cam-h264")
	require.NoError(t, err)

	// Recorder should be removed
	assert.Equal(t, 2, mgr.RecorderCount())
	_, ok := mgr.recorders["cam-h264"]
	assert.False(t, ok)

	// Camera should be removed from config
	assert.Len(t, mgr.cfg.Cameras, 3)
}

func TestRemoveCamera_NotFound(t *testing.T) {
	mgr, _, _, _ := newTestManager(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := mgr.RemoveCamera(ctx, "nonexistent")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestRemoveCamera_Persists(t *testing.T) {
	mgr, _, _, configPath := newTestManager(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := mgr.RemoveCamera(ctx, "cam-jpeg")
	require.NoError(t, err)

	// Reload config from disk
	loaded, err := config.Load(configPath)
	require.NoError(t, err)
	for _, cam := range loaded.Cameras {
		assert.NotEqual(t, "cam-jpeg", cam.ID, "removed camera should not be in config")
	}
}

func TestUpdateCamera_Name(t *testing.T) {
	mgr, _, _, _ := newTestManager(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	newName := "Updated H264 Camera"
	updated, err := mgr.UpdateCamera(ctx, "cam-h264", CameraUpdate{Name: &newName})
	require.NoError(t, err)
	assert.Equal(t, newName, updated.Name)

	// Recorder count should not change (no restart needed)
	assert.Equal(t, 0, mgr.RecorderCount())
}

func TestUpdateCamera_URL(t *testing.T) {
	mgr, _, _, _ := newTestManager(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start to create recorders
	err := mgr.Start(ctx)
	require.NoError(t, err)
	assert.Equal(t, 3, mgr.RecorderCount())

	newURL := "rtsp://127.0.0.1:2/new-stream"
	updated, err := mgr.UpdateCamera(ctx, "cam-h264", CameraUpdate{URL: &newURL})
	require.NoError(t, err)
	assert.Equal(t, newURL, updated.URL)

	// Recorder should still exist (restarted)
	assert.Equal(t, 3, mgr.RecorderCount())
}

func TestUpdateCamera_Disable(t *testing.T) {
	mgr, _, _, _ := newTestManager(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start to create recorders
	err := mgr.Start(ctx)
	require.NoError(t, err)
	assert.Equal(t, 3, mgr.RecorderCount())

	disabled := false
	updated, err := mgr.UpdateCamera(ctx, "cam-h264", CameraUpdate{Enabled: &disabled})
	require.NoError(t, err)
	assert.False(t, updated.Enabled)

	// Recorder should be stopped and removed
	assert.Equal(t, 2, mgr.RecorderCount())
	_, ok := mgr.recorders["cam-h264"]
	assert.False(t, ok)
}

func TestUpdateCamera_Enable(t *testing.T) {
	mgr, _, _, _ := newTestManager(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// cam-disabled has no recorder initially
	assert.Equal(t, 0, mgr.RecorderCount())

	enabled := true
	updated, err := mgr.UpdateCamera(ctx, "cam-disabled", CameraUpdate{Enabled: &enabled})
	require.NoError(t, err)
	assert.True(t, updated.Enabled)

	// Recorder should be created
	assert.Equal(t, 1, mgr.RecorderCount())
	_, ok := mgr.recorders["cam-disabled"]
	assert.True(t, ok)
}

func TestUpdateCamera_NotFound(t *testing.T) {
	mgr, _, _, _ := newTestManager(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	name := "test"
	_, err := mgr.UpdateCamera(ctx, "nonexistent", CameraUpdate{Name: &name})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestRestartRecorder(t *testing.T) {
	mgr, _, _, _ := newTestManager(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start to create recorders
	err := mgr.Start(ctx)
	require.NoError(t, err)
	assert.Equal(t, 3, mgr.RecorderCount())

	// Restart a recorder
	err = mgr.RestartRecorder(ctx, "cam-h264")
	require.NoError(t, err)

	// Recorder should still be there
	assert.Equal(t, 3, mgr.RecorderCount())
	_, ok := mgr.recorders["cam-h264"]
	assert.True(t, ok)
}

func TestRestartRecorder_Disabled(t *testing.T) {
	mgr, _, _, _ := newTestManager(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := mgr.RestartRecorder(ctx, "cam-disabled")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "disabled")
}

func TestCreateRecorder_ONVIF(t *testing.T) {
	t.Helper()
	tmpDir := t.TempDir()
	cfg := &config.Config{
		Storage: config.StorageConfig{
			RootDir:         filepath.Join(tmpDir, "storage"),
			SegmentDuration: "1m",
		},
	}
	require.NoError(t, os.MkdirAll(cfg.Storage.RootDir, 0o755))

	store, err := storage.NewManager(cfg.Storage.RootDir)
	require.NoError(t, err)
	defer store.CleanupTempFiles()

	mgr := NewCameraManager(cfg, store, nil, "")

	cam := config.CameraConfig{
		ID:       "cam-onvif",
		Name:     "ONVIF Camera",
		Protocol: "onvif",
		URL:      "http://192.168.1.100/onvif/device_service",
		Username: "admin",
		Password: "pass",
		Enabled:  true,
	}
	segDur, err := time.ParseDuration(cfg.Storage.SegmentDuration)
	require.NoError(t, err)

	rec := mgr.createRecorder(cam, segDur)
	require.NotNil(t, rec, "ONVIF protocol should create a recorder")
	// Verify it's an ONVIFRecorder
	status := rec.Status()
	require.Equal(t, model.StatusStopped, status)
}

func TestCreateRecorder_ONVIF_WithEndpoint(t *testing.T) {
	t.Helper()
	tmpDir := t.TempDir()
	cfg := &config.Config{
		Storage: config.StorageConfig{
			RootDir:         filepath.Join(tmpDir, "storage"),
			SegmentDuration: "30s",
		},
	}
	require.NoError(t, os.MkdirAll(cfg.Storage.RootDir, 0o755))

	store, err := storage.NewManager(cfg.Storage.RootDir)
	require.NoError(t, err)
	defer store.CleanupTempFiles()

	mgr := NewCameraManager(cfg, store, nil, "")

	cam := config.CameraConfig{
		ID:            "cam-onvif-endpoint",
		Name:          "ONVIF Camera",
		Protocol:      "onvif",
		URL:           "http://192.168.1.100/stream",
		ONVIFEndpoint: "http://192.168.1.100:8080/onvif/device_service",
		Username:      "admin",
		Password:      "pass",
	}
	segDur, err := time.ParseDuration(cfg.Storage.SegmentDuration)
	require.NoError(t, err)

	rec := mgr.createRecorder(cam, segDur)
	require.NotNil(t, rec, "ONVIF protocol with endpoint should create a recorder")
}

func TestGetONVIFPTZController_NotFound(t *testing.T) {
	t.Helper()
	tmpDir := t.TempDir()
	cfg := &config.Config{
		Storage: config.StorageConfig{
			RootDir:         filepath.Join(tmpDir, "storage"),
			SegmentDuration: "1m",
		},
	}
	require.NoError(t, os.MkdirAll(cfg.Storage.RootDir, 0o755))

	store, err := storage.NewManager(cfg.Storage.RootDir)
	require.NoError(t, err)
	defer store.CleanupTempFiles()

	mgr := NewCameraManager(cfg, store, nil, "")

	ctx := context.Background()
	_, err = mgr.GetONVIFPTZController(ctx, "nonexistent")
	require.Error(t, err)
	require.Contains(t, err.Error(), "not found")
}

func TestGetONVIFPTZController_NotONVIF(t *testing.T) {
	t.Helper()
	tmpDir := t.TempDir()
	cfg := &config.Config{
		Storage: config.StorageConfig{
			RootDir:         filepath.Join(tmpDir, "storage"),
			SegmentDuration: "1m",
		},
		Cameras: []config.CameraConfig{{
			ID:       "cam-h264",
			Name:     "H264 Camera",
			Protocol: "rtsp",
				Encoding: "h264",
			Enabled:  true,
		}},
	}
	require.NoError(t, os.MkdirAll(cfg.Storage.RootDir, 0o755))

	store, err := storage.NewManager(cfg.Storage.RootDir)
	require.NoError(t, err)
	defer store.CleanupTempFiles()

	mgr := NewCameraManager(cfg, store, nil, "")

	ctx := context.Background()
	_, err = mgr.GetONVIFPTZController(ctx, "cam-h264")
	require.Error(t, err)
	require.Contains(t, err.Error(), "not an ONVIF device")
}


func TestCreateRecorder_FallbackToBuiltIn(t *testing.T) {
	t.Helper()

	tmpDir := t.TempDir()
	cfg := &config.Config{
		Storage: config.StorageConfig{
			RootDir:         filepath.Join(tmpDir, "storage"),
			SegmentDuration: "1m",
		},
	}
	require.NoError(t, os.MkdirAll(cfg.Storage.RootDir, 0o755))

	store, err := storage.NewManager(cfg.Storage.RootDir)
	require.NoError(t, err)
	t.Cleanup(func() { store.CleanupTempFiles() })

	mgr := NewCameraManager(cfg, store, nil, "")

	// Built-in rtsp+h264 should still work (no plugin registered for "rtsp")
	cam := config.CameraConfig{
		ID:       "cam-rtsp-h264",
		Protocol: "rtsp",
		Encoding: "h264",
		URL:      "rtsp://127.0.0.1:1/stream",
		Enabled:  true,
	}
	segDur, err := time.ParseDuration(cfg.Storage.SegmentDuration)
	require.NoError(t, err)

	rec := mgr.createRecorder(cam, segDur)
	require.NotNil(t, rec, "built-in rtsp+h264 should still create a recorder")
}


func TestNewCameraManager_WithMetrics(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := &config.Config{
		Storage: config.StorageConfig{
			RootDir:         filepath.Join(tmpDir, "storage"),
			SegmentDuration: "30s",
		},
	}
	require.NoError(t, os.MkdirAll(cfg.Storage.RootDir, 0o755))

	store, err := storage.NewManager(cfg.Storage.RootDir)
	require.NoError(t, err)
	t.Cleanup(func() { store.CleanupTempFiles() })

	mm := metrics.NewMetrics()

	mgr := NewCameraManager(cfg, store, nil, "", mm)
	assert.NotNil(t, mgr)
	assert.Equal(t, mm, mgr.metrics)
}

func TestNewCameraManager_BackwardCompatOpts(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := &config.Config{
		Storage: config.StorageConfig{
			RootDir:         filepath.Join(tmpDir, "storage"),
			SegmentDuration: "30s",
		},
	}
	require.NoError(t, os.MkdirAll(cfg.Storage.RootDir, 0o755))

	store, err := storage.NewManager(cfg.Storage.RootDir)
	require.NoError(t, err)
	t.Cleanup(func() { store.CleanupTempFiles() })

	// Old style: just metrics as variadic arg
	mgr := NewCameraManager(cfg, store, nil, "", metrics.NewMetrics())
	assert.NotNil(t, mgr)
	assert.NotNil(t, mgr.metrics)
	assert.Nil(t, mgr.mergeMgr)

	// Old style: no opts at all
	mgr2 := NewCameraManager(cfg, store, nil, "")
	assert.NotNil(t, mgr2)
	assert.Nil(t, mgr2.metrics)
	assert.Nil(t, mgr2.mergeMgr)
}

func TestGetOrCreateONVIFClient_CacheHit(t *testing.T) {
	mgr, _, _, _ := newTestManager(t)
	assert.NotNil(t, mgr.onvifClients)
	assert.Empty(t, mgr.onvifClients)

	// Seed the cache with a mock client directly
	mockClient := onvif.NewClient("http://192.168.1.100/onvif/device_service", "admin", "pass")
	mgr.onvifClients["test-cam"] = mockClient

	// The getOrCreateONVIFClient can't actually Connect() without a real device,
	// so test the cache lookup path by pre-seeding and verifying CloseONVIFClient removes it.
	assert.Contains(t, mgr.onvifClients, "test-cam")
	mgr.CloseONVIFClient("test-cam")
	assert.NotContains(t, mgr.onvifClients, "test-cam")
}

func TestGetOrCreateONVIFClient_CameraNotFound(t *testing.T) {
	mgr, _, _, _ := newTestManager(t)

	ctx := context.Background()
	_, err := mgr.getOrCreateONVIFClient(ctx, "nonexistent-camera")
	assert.Error(t, err)
	var notFound *model.CameraNotFoundError
	assert.ErrorAs(t, err, &notFound)
	assert.Equal(t, "nonexistent-camera", notFound.CameraID)
}

func TestGetOrCreateONVIFClient_NonONVIFCamera(t *testing.T) {
	mgr, _, _, _ := newTestManager(t)

	ctx := context.Background()
	_, err := mgr.getOrCreateONVIFClient(ctx, "cam-h264")
	assert.Error(t, err)
	var notONVIF *model.ONVIFNotCameraError
	assert.ErrorAs(t, err, &notONVIF)
	assert.Equal(t, "cam-h264", notONVIF.CameraID)
}

func TestCloseONVIFClient(t *testing.T) {
	mgr, _, _, _ := newTestManager(t)
	assert.Empty(t, mgr.onvifClients)

	mockClient := onvif.NewClient("http://192.168.1.100/onvif/device_service", "admin", "pass")
	mgr.onvifClients["cam-to-close"] = mockClient
	assert.Len(t, mgr.onvifClients, 1)

	mgr.CloseONVIFClient("cam-to-close")
	assert.Empty(t, mgr.onvifClients)

	// Closing a non-existent key is a no-op
	mgr.CloseONVIFClient("does-not-exist")
	assert.Empty(t, mgr.onvifClients)
}

func TestCloseAllONVIFClients(t *testing.T) {
	mgr, _, _, _ := newTestManager(t)
	assert.Empty(t, mgr.onvifClients)

	mockClient1 := onvif.NewClient("http://192.168.1.100/onvif/device_service", "admin", "pass")
	mockClient2 := onvif.NewClient("http://192.168.1.101/onvif/device_service", "admin", "pass")
	mgr.onvifClients["cam-1"] = mockClient1
	mgr.onvifClients["cam-2"] = mockClient2
	assert.Len(t, mgr.onvifClients, 2)

	mgr.closeAllONVIFClients()
	assert.Empty(t, mgr.onvifClients)
}

func TestClassifyError(t *testing.T) {
	t.Helper()
	require.Equal(t, "unknown", classifyError(nil))
	require.Equal(t, "unknown", classifyError(fmt.Errorf("some random error")))
	require.Equal(t, "timeout", classifyError(fmt.Errorf("connection timeout after 10s")))
	require.Equal(t, "timeout", classifyError(fmt.Errorf("deadline exceeded")))
	require.Equal(t, "auth", classifyError(fmt.Errorf("401 unauthorized")))
	require.Equal(t, "auth", classifyError(fmt.Errorf("authentication failed")))
	require.Equal(t, "network", classifyError(fmt.Errorf("connection refused")))
	require.Equal(t, "network", classifyError(fmt.Errorf("dial tcp: no such host")))
}

func TestCameraConnectionErrorMetrics(t *testing.T) {
	t.Helper()
	m := metrics.NewMetrics()
	mgr, store, _, configPath := newTestManager(t)
	defer store.CleanupTempFiles()
	_ = mgr

	// Create a new manager with metrics and a camera that will fail to start
	tmpDir := t.TempDir()
	cfg := testConfig()
	cfg.Storage.RootDir = filepath.Join(tmpDir, "storage")
	// Use an unknown protocol so createRecorder returns nil → startRecorder returns error
	cfg.Cameras[0].Protocol = "unknown_proto"
	cfg.Cameras[0].Enabled = true
	require.NoError(t, os.MkdirAll(cfg.Storage.RootDir, 0o755))
	require.NoError(t, config.Save(configPath, cfg))

	mgr2 := NewCameraManager(cfg, store, nil, configPath, m)
	// Call startRecorder directly to trigger the error metric
	segDur, _ := time.ParseDuration(cfg.Storage.SegmentDuration)
	err := mgr2.startRecorder(context.Background(), cfg.Cameras[0], segDur)
	require.Error(t, err)
	require.Contains(t, err.Error(), "does not support recording")

	// Verify no metric was recorded (createRecorder returns nil, not a connection error)
	families, _ := m.Registry.Gather()
	for _, f := range families {
		require.NotEqual(t, "nvr_camera_connection_errors_total", f.GetName(), "unknown protocol should not record connection error")
	}
}

func TestFrameProcessingDuration_1in100Sampling(t *testing.T) {
	m := metrics.NewMetrics()
	mgr := NewCameraManager(testConfig(), nil, nil, "", m)

	segDur, err := time.ParseDuration("1m")
	require.NoError(t, err)

	cfg := testConfig()
	rec := mgr.createRecorder(cfg.Cameras[0], segDur)
	require.NotNil(t, rec)

	// Type-assert to H264Recorder to access the Hub
	h264Rec, ok := rec.(*recorder.H264Recorder)
	require.True(t, ok, "expected H264Recorder")
	hub := h264Rec.Hub
	require.NotNil(t, hub)

	// Simulate 500 frames — expect ~5 histogram samples (1/100 sampling)
	for i := 0; i < 500; i++ {
		hub.Broadcast(int64(i), [][]byte{{byte(i)}}, i == 0)
	}

	// Gather metrics and verify sample count
	families, err := m.Registry.Gather()
	require.NoError(t, err)

	var samples int
	for _, f := range families {
		if f.GetName() == "nvr_frame_processing_duration_seconds" {
			for _, metric := range f.GetMetric() {
				samples += int(metric.GetHistogram().GetSampleCount())
			}
		}
	}

	// 500 frames / 100 = 5 samples, allow ±1 for edge cases
	require.InDelta(t, 5, samples, 1, "expected ~5 histogram samples for 500 frames")
}

// --- autoPopulateSnapshotURL tests ---

func TestAutoPopulateSnapshotURL_CameraNotFound(t *testing.T) {
	// Should log warning and return, not panic
	mgr, _, _, _ := newTestManager(t)
	mgr.autoPopulateSnapshotURL(context.Background(), "nonexistent-camera")
}

func TestAutoPopulateSnapshotURL_NonONVIFCamera(t *testing.T) {
	// Should log warning and return
	mgr, _, _, _ := newTestManager(t)
	mgr.autoPopulateSnapshotURL(context.Background(), "cam-h264")
}

func TestAutoPopulateSnapshotURL_PreservesExistingURL(t *testing.T) {
	mgr, _, _, _ := newTestManager(t)

	// Add an ONVIF camera with existing SnapshotURL to config
	mgr.cfg.Cameras = append(mgr.cfg.Cameras, config.CameraConfig{
		ID:          "cam-onvif",
		Name:        "ONVIF Camera",
		Protocol:    "onvif",
		URL:         "http://192.168.1.100/onvif/device_service",
		SnapshotURL: "http://existing-snapshot.jpg",
	})

	// autoPopulateSnapshotURL will fail early (no ONVIF client connectable)
	// but the important thing is it doesn't overwrite the existing URL
	mgr.autoPopulateSnapshotURL(context.Background(), "cam-onvif")

	// Verify SnapshotURL is still the original value
	camCfg := mgr.GetCameraConfig("cam-onvif")
	require.NotNil(t, camCfg)
	assert.Equal(t, "http://existing-snapshot.jpg", camCfg.SnapshotURL)
}

func TestAddCamera_ONVIF_PreservesExistingSnapshotURL(t *testing.T) {
	mgr, _, db, configPath := newTestManager(t)

	cam := config.CameraConfig{
		ID:           "new-onvif-cam",
		Name:         "New ONVIF Camera",
		Protocol:     "onvif",
		URL:          "http://192.168.1.100/onvif/device_service",
		Username:     "admin",
		Password:     "pass",
		Enabled:      true,
		SnapshotURL:  "http://custom-snapshot.jpg",
	}

	ctx := context.Background()
	id, err := mgr.AddCamera(ctx, cam)
	require.NoError(t, err)
	require.Equal(t, "new-onvif-cam", id)

	// Give goroutine a moment (it should NOT fire since SnapshotURL is already set)
	time.Sleep(50 * time.Millisecond)

	// Verify SnapshotURL is preserved
	camCfg := mgr.GetCameraConfig("new-onvif-cam")
	require.NotNil(t, camCfg)
	assert.Equal(t, "http://custom-snapshot.jpg", camCfg.SnapshotURL)

	// Cleanup
	mgr.mu.Lock()
	defer mgr.mu.Unlock()
	for i, c := range mgr.cfg.Cameras {
		if c.ID == "new-onvif-cam" {
			mgr.cfg.Cameras = append(mgr.cfg.Cameras[:i], mgr.cfg.Cameras[i+1:]...)
			break
		}
	}
	config.Save(configPath, mgr.cfg)
	db.Close()
}

func TestUpdateCamera_ONVIFEndpointChange_ClosesClient(t *testing.T) {
	mgr, _, _, _ := newTestManager(t)

	// Add an ONVIF camera to config
	mgr.cfg.Cameras = append(mgr.cfg.Cameras, config.CameraConfig{
		ID:       "cam-onvif",
		Name:     "ONVIF Camera",
		Protocol: "onvif",
		URL:      "http://192.168.1.100/onvif/device_service",
		Enabled:  false,
	})

	// Pre-seed ONVIF client cache
	mockClient := onvif.NewClient("http://192.168.1.100/onvif/device_service", "admin", "pass")
	mgr.onvifClients["cam-onvif"] = mockClient
	assert.Contains(t, mgr.onvifClients, "cam-onvif")

	// Update ONVIF endpoint
	newEndpoint := "http://192.168.1.100:8080/onvif/device_service"
	_, err := mgr.UpdateCamera(context.Background(), "cam-onvif", CameraUpdate{
		ONVIFEndpoint: &newEndpoint,
	})
	require.NoError(t, err)

	// Cached client should be closed
	assert.NotContains(t, mgr.onvifClients, "cam-onvif")

	// Endpoint should be updated
	camCfg := mgr.GetCameraConfig("cam-onvif")
	require.NotNil(t, camCfg)
	assert.Equal(t, newEndpoint, camCfg.ONVIFEndpoint)
}

// --- Dual-mode integration tests ---

func TestDualModeIntegration(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := &config.Config{
		Storage: config.StorageConfig{
			RootDir:         filepath.Join(tmpDir, "storage"),
			SegmentDuration: "30s",
		},
		Cameras: []config.CameraConfig{
			{
				ID:       "cam-dual",
				Name:     "Dual Mode H265",
				Protocol: "rtsp",
				Encoding: "h265",
				URL:      "rtsp://127.0.0.1:1/stream",
				Enabled:  true,
				Timelapse: &config.CameraTimelapseConfig{
					Enabled:     true,
					FrameSource: "rtsp_keyframe",
					Interval:    "1s",
				},
			},
		},
	}

	require.NoError(t, os.MkdirAll(cfg.Storage.RootDir, 0o755))

	dbPath := filepath.Join(tmpDir, "test.db")
	db, err := storage.New(dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })

	ctx := context.Background()
	require.NoError(t, db.Init(ctx))

	store, err := storage.NewManager(cfg.Storage.RootDir)
	require.NoError(t, err)
	t.Cleanup(func() { store.CleanupTempFiles() })

	mgr := NewCameraManager(cfg, store, db, "")

	// Record baseline goroutine count
	baselineGoroutines := runtime.NumGoroutine()

	// ---- Start ----
	err = mgr.Start(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, mgr.RecorderCount())

	// Verify KeyframeExtractor is registered and running
	ext, ok := mgr.keyframeExtractors["cam-dual"]
	require.True(t, ok, "keyframe extractor should be registered for dual-mode camera")
	require.True(t, ext.IsRunning(), "keyframe extractor should be running")

	// Verify the recorder's StreamHub exists
	rec := mgr.recorders["cam-dual"]
	require.NotNil(t, rec)
	hub := getRecorderHub(rec)
	require.NotNil(t, hub, "recorder should have a StreamHub")

	// Subscribe a test consumer to verify frame delivery via hub
	frameCh := make(chan int64, 100)
	err = hub.Subscribe("test-consumer-dual", func(pts int64, au [][]byte) {
		frameCh <- pts
	})
	require.NoError(t, err)
	defer hub.Unsubscribe("test-consumer-dual")

	// ---- Push H.265 NAL Units ----
	// H.265 NAL type encoding: type = (nalu[0] >> 1) & 0x3F
	//   IDR_W_RADL (type 19): nalu[0] = 19 << 1 = 38 = 0x26
	//   TRAIL_N   (type 1):  nalu[0] = 1 << 1 = 2 = 0x02
	//   VPS       (type 32): nalu[0] = 32 << 1 = 64 = 0x40
	//   SPS       (type 33): nalu[0] = 33 << 1 = 66 = 0x42
	//   PPS       (type 34): nalu[0] = 34 << 1 = 68 = 0x44
	idrNalu := []byte{0x26, 0x01, 0x02, 0x03}
	nonIDRNalu := []byte{0x02, 0x01, 0x02, 0x03}
	vpsNalu := []byte{0x40, 0x01, 0x02}
	spsNalu := []byte{0x42, 0x01, 0x02}
	ppsNalu := []byte{0x44, 0x01, 0x02}

	// Push IDR access unit (VPS+SPS+PPS+IDR = keyframe)
	hub.Broadcast(1000, [][]byte{vpsNalu, spsNalu, ppsNalu, idrNalu}, true)
	// Push non-IDR frame
	hub.Broadcast(1001, [][]byte{nonIDRNalu}, false)
	// Push another IDR access unit
	hub.Broadcast(1002, [][]byte{vpsNalu, spsNalu, ppsNalu, idrNalu}, true)

	// Verify frames delivered to test consumer
	require.Eventually(t, func() bool {
		return len(frameCh) >= 3
	}, 3*time.Second, 10*time.Millisecond, "should receive 3 broadcast frames")

	// Verify KFE still running after frame delivery
	require.True(t, ext.IsRunning(), "KFE should still be running after frame delivery")

	// Wait for KFE capture loop to capture at least one frame (interval=1s)
	time.Sleep(1500 * time.Millisecond)

	// Check that KFE created segment files on disk
	cameraDir := filepath.Join(cfg.Storage.RootDir, "cam-dual")
	entries, segErr := os.ReadDir(cameraDir)
	if segErr == nil {
		t.Logf("KFE segment directory entries after capture: %d", len(entries))
		for _, e := range entries {
			t.Logf("  - %s (dir=%v)", e.Name(), e.IsDir())
		}
	}

	// ---- Stop ----
	err = mgr.Stop()
	require.NoError(t, err)

	// Verify KFE stopped
	require.False(t, ext.IsRunning(), "KFE should be stopped after manager Stop")

	// Verify all recorders stopped
	for id, status := range mgr.Status() {
		assert.Equal(t, model.StatusStopped, status, "recorder %s should be stopped", id)
	}

	// Verify no goroutine leaks (allow overhead)
	finalGoroutines := runtime.NumGoroutine()
	leakBudget := 5
	t.Logf("goroutines: baseline=%d, final=%d", baselineGoroutines, finalGoroutines)
	assert.LessOrEqual(t, finalGoroutines, baselineGoroutines+leakBudget,
		"possible goroutine leak: %d extra goroutines", finalGoroutines-baselineGoroutines)
}

func TestDualMode_H264Timelapse_CreatesKeyframeExtractor(t *testing.T) {
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
	t.Cleanup(func() { store.CleanupTempFiles() })

	mgr := NewCameraManager(cfg, store, nil, "")

	trueVal := true
	cam := config.CameraConfig{
		ID:       "cam-dual-h264",
		Name:     "Dual-Mode H264",
		Protocol: "rtsp",
		Encoding: "h264",
		URL:      "rtsp://192.168.1.100/stream",
		Enabled:  true,
		Timelapse: &config.CameraTimelapseConfig{
			Enabled:     trueVal,
			FrameSource: "rtsp_keyframe",
			Interval:    "10s",
		},
	}
	segDur, err := time.ParseDuration(cfg.Storage.SegmentDuration)
	require.NoError(t, err)

	// createRecorder should produce H264Recorder with a StreamHub
	rec := mgr.createRecorder(cam, segDur)
	require.NotNil(t, rec, "H264 camera with timelapse should create H264Recorder")
	assert.IsType(t, &recorder.H264Recorder{}, rec, "should be an H264Recorder")

	hub := getRecorderHub(rec)
	require.NotNil(t, hub, "H264Recorder should have a StreamHub")

	// Start keyframe extractor with the hub
	err = mgr.startTimelapseKeyframeExtractor(cam.ID, cam, hub)
	require.NoError(t, err, "should start keyframe extractor for H264 dual-mode")

	// Verify KFE is registered and running
	mgr.mu.RLock()
	ext, exists := mgr.keyframeExtractors[cam.ID]
	mgr.mu.RUnlock()
	assert.True(t, exists, "keyframe extractor should be registered")
	assert.NotNil(t, ext)
	assert.True(t, ext.IsRunning(), "keyframe extractor should be running")

	// Cleanup
	mgr.stopTimelapseKeyframeExtractor(cam.ID)
}

func TestDualMode_ONVIFTimelapse_H265_CreatesKeyframeExtractor(t *testing.T) {
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
	t.Cleanup(func() { store.CleanupTempFiles() })

	mgr := NewCameraManager(cfg, store, nil, "")

	trueVal := true
	cam := config.CameraConfig{
		ID:             "cam-dual-onvif-h265",
		Name:           "Dual-Mode ONVIF H265",
		Protocol:       "onvif",
		URL:            "http://192.168.1.100/onvif/device_service",
		StreamEncoding: "H265",
		Username:       "admin",
		Password:       "pass",
		Enabled:        true,
		Timelapse: &config.CameraTimelapseConfig{
			Enabled:     trueVal,
			FrameSource: "rtsp_keyframe",
			Interval:    "10s",
		},
	}
	segDur, err := time.ParseDuration(cfg.Storage.SegmentDuration)
	require.NoError(t, err)

	// createRecorder should produce ONVIFRecorder with a StreamHub
	rec := mgr.createRecorder(cam, segDur)
	require.NotNil(t, rec, "ONVIF camera with timelapse should create ONVIFRecorder")

	hub := getRecorderHub(rec)
	require.NotNil(t, hub, "ONVIFRecorder should have a StreamHub")

	// Start keyframe extractor with the hub
	err = mgr.startTimelapseKeyframeExtractor(cam.ID, cam, hub)
	require.NoError(t, err, "should start keyframe extractor for ONVIF dual-mode")

	// Verify KFE is registered and running
	mgr.mu.RLock()
	ext, exists := mgr.keyframeExtractors[cam.ID]
	mgr.mu.RUnlock()
	assert.True(t, exists, "keyframe extractor should be registered")
	assert.True(t, ext.IsRunning(), "keyframe extractor should be running")

	// Cleanup
	mgr.stopTimelapseKeyframeExtractor(cam.ID)
}

func TestDualMode_ONVIFTimelapse_H264_CreatesKeyframeExtractor(t *testing.T) {
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
	t.Cleanup(func() { store.CleanupTempFiles() })

	mgr := NewCameraManager(cfg, store, nil, "")

	trueVal := true
	cam := config.CameraConfig{
		ID:       "cam-dual-onvif-h264",
		Name:     "Dual-Mode ONVIF H264",
		Protocol: "onvif",
		URL:      "http://192.168.1.100/onvif/device_service",
		Username: "admin",
		Password: "pass",
		Enabled:  true,
		Timelapse: &config.CameraTimelapseConfig{
			Enabled:     trueVal,
			FrameSource: "rtsp_keyframe",
			Interval:    "10s",
		},
	}
	segDur, err := time.ParseDuration(cfg.Storage.SegmentDuration)
	require.NoError(t, err)

	rec := mgr.createRecorder(cam, segDur)
	require.NotNil(t, rec, "ONVIF camera with timelapse should create ONVIFRecorder")

	hub := getRecorderHub(rec)
	require.NotNil(t, hub)

	err = mgr.startTimelapseKeyframeExtractor(cam.ID, cam, hub)
	require.NoError(t, err, "should start keyframe extractor for ONVIF H264 dual-mode")

	mgr.mu.RLock()
	ext, exists := mgr.keyframeExtractors[cam.ID]
	mgr.mu.RUnlock()
	assert.True(t, exists, "keyframe extractor should be registered")
	assert.True(t, ext.IsRunning(), "keyframe extractor should be running")

	mgr.stopTimelapseKeyframeExtractor(cam.ID)
}

func TestDualMode_TimelapseDisabled_NoKeyframeExtractor(t *testing.T) {
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
	t.Cleanup(func() { store.CleanupTempFiles() })

	mgr := NewCameraManager(cfg, store, nil, "")

	t.Run("no timelapse config", func(t *testing.T) {
		cam := config.CameraConfig{
			ID:       "cam-no-tl",
			Protocol: "rtsp",
			Encoding: "h264",
			URL:      "rtsp://192.168.1.100/stream",
			Enabled:  true,
		}
		err := mgr.startTimelapseKeyframeExtractor(cam.ID, cam, nil)
		assert.NoError(t, err, "no timelapse config should not error")
		mgr.mu.RLock()
		_, exists := mgr.keyframeExtractors[cam.ID]
		mgr.mu.RUnlock()
		assert.False(t, exists, "should not create KFE without timelapse config")
	})

	t.Run("timelapse disabled", func(t *testing.T) {
		cam := config.CameraConfig{
			ID:       "cam-tl-disabled",
			Protocol: "rtsp",
			Encoding: "h264",
			URL:      "rtsp://192.168.1.100/stream",
			Enabled:  true,
			Timelapse: &config.CameraTimelapseConfig{
				Enabled:     false,
				FrameSource: "rtsp_keyframe",
			},
		}
		err := mgr.startTimelapseKeyframeExtractor(cam.ID, cam, nil)
		assert.NoError(t, err, "disabled timelapse should not error")
		mgr.mu.RLock()
		_, exists := mgr.keyframeExtractors[cam.ID]
		mgr.mu.RUnlock()
		assert.False(t, exists, "should not create KFE when timelapse disabled")
	})

	t.Run("wrong frame source", func(t *testing.T) {
		trueVal := true
		cam := config.CameraConfig{
			ID:       "cam-tl-snapshot",
			Protocol: "rtsp",
			Encoding: "h264",
			URL:      "rtsp://192.168.1.100/stream",
			Enabled:  true,
			Timelapse: &config.CameraTimelapseConfig{
				Enabled:     trueVal,
				FrameSource: "snapshot",
				Interval:    "10s",
			},
		}
		err := mgr.startTimelapseKeyframeExtractor(cam.ID, cam, nil)
		assert.NoError(t, err, "wrong frame source should not error")
		mgr.mu.RLock()
		_, exists := mgr.keyframeExtractors[cam.ID]
		mgr.mu.RUnlock()
		assert.False(t, exists, "should not create KFE with non-rtsp_keyframe source")
	})
}

func TestDualMode_KeyframeExtractorStopsOnCameraStop(t *testing.T) {
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
	t.Cleanup(func() { store.CleanupTempFiles() })

	mgr := NewCameraManager(cfg, store, nil, "")

	trueVal := true
	cam := config.CameraConfig{
		ID:       "cam-dual-stop",
		Name:     "Dual-Mode Stop",
		Protocol: "rtsp",
		Encoding: "h264",
		URL:      "rtsp://192.168.1.100/stream",
		Enabled:  true,
		Timelapse: &config.CameraTimelapseConfig{
			Enabled:     trueVal,
			FrameSource: "rtsp_keyframe",
			Interval:    "10s",
		},
	}

	// Add camera to config so StopCamera can find it
	mgr.cfg.Cameras = append(mgr.cfg.Cameras, cam)

	segDur, err := time.ParseDuration(cfg.Storage.SegmentDuration)
	require.NoError(t, err)

	// Create recorder to get hub
	rec := mgr.createRecorder(cam, segDur)
	require.NotNil(t, rec)
	hub := getRecorderHub(rec)
	require.NotNil(t, hub)

	// Register recorder manually (startRecorder would fail on rec.Start for fake URL)
	mgr.mu.Lock()
	mgr.recorders[cam.ID] = rec
	mgr.mu.Unlock()

	// Start KFE
	err = mgr.startTimelapseKeyframeExtractor(cam.ID, cam, hub)
	require.NoError(t, err)

	// Verify KFE is registered and running
	mgr.mu.RLock()
	ext, exists := mgr.keyframeExtractors[cam.ID]
	mgr.mu.RUnlock()
	require.True(t, exists)
	require.True(t, ext.IsRunning())

	// Stop camera
	ctx := context.Background()
	err = mgr.StopCamera(ctx, cam.ID)
	require.NoError(t, err)

	// Verify KFE is removed from map
	mgr.mu.RLock()
	_, exists = mgr.keyframeExtractors[cam.ID]
	mgr.mu.RUnlock()
	assert.False(t, exists, "keyframe extractor should be removed after StopCamera")

	// Verify KFE is no longer running
	assert.False(t, ext.IsRunning(), "keyframe extractor should be stopped")
}

func TestDualMode_StandaloneTimelapseRTSPKeyframe_GetsValidRecorder(t *testing.T) {
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
	t.Cleanup(func() { store.CleanupTempFiles() })

	mgr := NewCameraManager(cfg, store, nil, "")

	trueVal := true
	cam := config.CameraConfig{
		ID:       "cam-tl-standalone",
		Name:     "Timelapse Standalone",
		Protocol: "timelapse",
		Encoding: "h264",
		URL:      "rtsp://192.168.1.100/stream",
		Enabled:  true,
		Timelapse: &config.CameraTimelapseConfig{
			Enabled:     trueVal,
			FrameSource: "rtsp_keyframe",
			Interval:    "10s",
		},
	}
	segDur, err := time.ParseDuration(cfg.Storage.SegmentDuration)
	require.NoError(t, err)

	// createRecorder should return H264Recorder (not nil, not StubRecorder)
	rec := mgr.createRecorder(cam, segDur)
	require.NotNil(t, rec, "standalone timelapse with rtsp_keyframe should create H264Recorder")
	assert.IsType(t, &recorder.H264Recorder{}, rec, "should be H264Recorder, not StubRecorder")

	// Recorder should have a working StreamHub
	hub := getRecorderHub(rec)
	require.NotNil(t, hub, "recorder should have StreamHub from initStreamHub")

	// KFE should be startable with this hub
	err = mgr.startTimelapseKeyframeExtractor(cam.ID, cam, hub)
	require.NoError(t, err, "should start KFE with standalone timelapse recorder hub")

	mgr.mu.RLock()
	ext, exists := mgr.keyframeExtractors[cam.ID]
	mgr.mu.RUnlock()
	assert.True(t, exists, "keyframe extractor should be registered")
	assert.True(t, ext.IsRunning(), "keyframe extractor should be running")

	mgr.stopTimelapseKeyframeExtractor(cam.ID)
}
