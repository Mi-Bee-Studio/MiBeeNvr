package camera

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/metrics"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/plugin"
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
	require.Contains(t, err.Error(), "not an ONVIF camera")
}

// --- Plugin registry tests ---

func TestCreateRecorder_PluginLookup(t *testing.T) {
	t.Helper()

	// Register a mock plugin for a custom protocol
	const testProto = "test-plugin-proto"
	mockRec := &mockRecorder{status: model.StatusStopped}
	plugin.Register(&mockPlugin{
		name:     "test-plugin",
		protocols: []string{testProto},
		recorder: mockRec,
	})

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

	cam := config.CameraConfig{
		ID:       "cam-plugin",
		Name:     "Plugin Camera",
		Protocol: testProto,
		Enabled:  true,
	}
	segDur, err := time.ParseDuration(cfg.Storage.SegmentDuration)
	require.NoError(t, err)

	rec := mgr.createRecorder(cam, segDur)
	require.NotNil(t, rec, "plugin-registered protocol should create a recorder via plugin")
	require.Equal(t, model.StatusStopped, rec.Status())
}

// mockRecorder satisfies model.Recorder for testing.
type mockRecorder struct{ status model.RecorderStatus }

func (m *mockRecorder) Start(_ context.Context) error  { return nil }
func (m *mockRecorder) Stop() error                     { return nil }
func (m *mockRecorder) Status() model.RecorderStatus    { return m.status }

// mockPlugin satisfies plugin.RecorderPlugin for testing.
type mockPlugin struct {
	name      string
	protocols []string
	recorder  model.Recorder
}

func (p *mockPlugin) Name() string                          { return p.name }
func (p *mockPlugin) Protocols() []string                    { return p.protocols }
func (p *mockPlugin) NewRecorder(_ config.CameraConfig, _ *storage.Manager, _ *storage.DB, _ ...*metrics.Metrics) model.Recorder {
	return p.recorder
}
func (p *mockPlugin) RegisterRoutes(_ chi.Router)            {}
func (p *mockPlugin) ConfigSchema() interface{}              { return nil }

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

// --- gRPC Plugin Manager dispatch tests ---


func TestCreateRecorder_NilPluginMgr(t *testing.T) {
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
	t.Cleanup(func() { store.CleanupTempFiles() })

	// nil pluginMgr — should use built-in recorders
	mgr := NewCameraManager(cfg, store, nil, "")

	cam := config.CameraConfig{
		ID:       "cam-h264",
		Protocol: "rtsp",
		Encoding: "h264",
		URL:      "rtsp://127.0.0.1:1/stream",
	}
	segDur, err := time.ParseDuration(cfg.Storage.SegmentDuration)
	require.NoError(t, err)

	rec := mgr.createRecorder(cam, segDur)
require.NotNil(t, rec, "built-in recorder should work with nil pluginMgr")
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

func TestStop_NoPlugins(t *testing.T) {
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

	mgr := NewCameraManager(cfg, store, nil, "")

	// Stop should not panic with no plugins
	err = mgr.Stop()
	require.NoError(t, err)
}
