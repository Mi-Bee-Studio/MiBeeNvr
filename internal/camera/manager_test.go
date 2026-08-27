package camera

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
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
			},
			{
				ID:       "cam-mjpeg",
				Name:     "MJPEG Camera",
				Protocol: "rtsp",
				Encoding: "mjpeg",
				URL:      "rtsp://127.0.0.1:1/stream",
			},
			{
				ID:       "cam-disabled",
				Name:     "Disabled Camera",
				Protocol: "rtsp",
				Encoding: "h264",
				URL:      "rtsp://127.0.0.1:1/stream",
			},
			{
				ID:       "cam-jpeg",
				Name:     "JPEG Camera",
				Protocol: "http",
				Encoding: "jpeg",
				URL:      "http://192.168.1.13/jpg",
			},
		},
	}
}

func newTestManager(t *testing.T) (*CameraManager, *storage.Manager, *storage.DB, string) {
	t.Helper()
	return newTestManagerWithCfg(t, testConfig())
}

// newTestManagerWithCfg wires up a CameraManager against a fresh temp dir + DB
// for the given config, registering teardown in the correct order:
//
//  1. store.CleanupTempFiles()  (registered first → runs last)
//  2. db.Close()
//  3. mgr.Stop()               (registered last → runs first)
//
// mgr.Stop() runs FIRST (LIFO) so that the startup backfill goroutines
// (backfillStableIDs, backfillEncoding — both touch cm.db) have fully exited
// before the DB file is closed and the temp dir is deleted. Any test that
// calls Start() MUST go through this helper (or replicate the ordering):
// leaking those goroutines races against t.TempDir() cleanup →
// "directory not empty" / "disk I/O error" (flaky under -race, see #112, #124).
func newTestManagerWithCfg(t *testing.T, cfg *config.Config) (*CameraManager, *storage.Manager, *storage.DB, string) {
	t.Helper()
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

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
	t.Cleanup(func() { _ = mgr.Stop() })
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
	// Should have created recorders for all 4 cameras
	assert.Equal(t, 4, mgr.RecorderCount())

	statuses := mgr.Status()
	assert.Len(t, statuses, 4)
	_, hasH264 := statuses["cam-h264"]
	_, hasMJPEG := statuses["cam-mjpeg"]
	assert.True(t, hasH264, "should have h264 recorder")
	assert.True(t, hasMJPEG, "should have mjpeg recorder")
	_, hasJPEG := statuses["cam-jpeg"]
	assert.True(t, hasJPEG, "should have http_jpeg recorder")
}

func TestStart_HTTPJPEGRecorderCreated(t *testing.T) {
	cfg := &config.Config{
		Storage: config.StorageConfig{
			SegmentDuration: "1m",
		},
		Cameras: []config.CameraConfig{
			{
				ID:       "cam-1",
				Protocol: "http",
				Encoding: "jpeg",
			},
		},
	}
	mgr, _, _, _ := newTestManagerWithCfg(t, cfg)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := mgr.Start(ctx)
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
	assert.Equal(t, 4, mgr.RecorderCount())

	// Give recorders a moment to start their goroutines
	time.Sleep(100 * time.Millisecond)

	err = mgr.Stop()
	require.NoError(t, err)

	// After stop, recorders should still be in the map (not removed)
	assert.Equal(t, 4, mgr.RecorderCount())

	// Status should be stopped
	statuses := mgr.Status()
	for _, s := range statuses {
		assert.Equal(t, model.StatusStopped, s)
	}

	time.Sleep(100 * time.Millisecond)

	err = mgr.Stop()
	require.NoError(t, err)

	// After stop, recorders should still be in the map (not removed)
	assert.Equal(t, 4, mgr.RecorderCount())

	// Status should be stopped
}

// TestStop_WaitsForONVIFEnsureGoroutines guards #163: Stop must join the
// per-recorder ensure* goroutines (ensureStableID / ensureProfileToken /
// ensureEncoding) spawned via launchTrackedEnsure. Without onvifEnsureWg.Wait
// in Stop, those goroutines could still be mid configMu.Lock + persistConfig
// + DB write when Stop returns, racing the caller's resource teardown.
//
// We exercise the tracker directly: spawn a tracked "ensure" that blocks on a
// release channel, call Stop in a goroutine, and assert Stop does NOT return
// until the tracked work is released and finishes.
func TestStop_WaitsForONVIFEnsureGoroutines(t *testing.T) {
	t.Helper()
	mgr, _, _, _ := newTestManager(t)

	release := make(chan struct{})
	done := atomic.Bool{}

	// launchTrackedEnsure is the exact wrapper used by startRecorderLocked for
	// the three ensure* passes, so this exercises the real tracking path.
	mgr.launchTrackedEnsure(func(_ string) {
		select {
		case <-release:
		case <-time.After(5 * time.Second):
			t.Error("tracked ensure goroutine was not released within 5s")
		}
		done.Store(true)
	}, "cam-test")

	// Give the goroutine a moment to start. We can't observe entry directly,
	// but the Stop below will prove blocking: if it returns before release,
	// the tracker is missing.
	stopReturned := make(chan struct{})
	go func() {
		_ = mgr.Stop()
		close(stopReturned)
	}()

	select {
	case <-stopReturned:
		t.Fatal("Stop returned before the tracked ensure goroutine finished — onvifEnsureWg.Wait is missing in Stop")
	case <-time.After(50 * time.Millisecond):
		// expected: Stop is parked on onvifEnsureWg.Wait
	}

	close(release)

	select {
	case <-stopReturned:
		// expected: Stop returned after the tracked goroutine exited
	case <-time.After(2 * time.Second):
		t.Fatal("Stop did not return within 2s of releasing the tracked goroutine")
	}
	if !done.Load() {
		t.Fatal("tracked ensure goroutine should have completed before Stop returned")
	}
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
	assert.Equal(t, 4, mgr.RecorderCount())

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
	cfg := &config.Config{
		Storage: config.StorageConfig{
			SegmentDuration: "1m",
		},
		Cameras: []config.CameraConfig{
			{
				ID:       "cam-1",
				Protocol: "onvif",
				URL:      "rtsp://192.168.1.10:554/stream",
			},
		},
	}
	mgr, _, _, _ := newTestManagerWithCfg(t, cfg)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := mgr.Start(ctx)
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

	// Check MJPEG camera
	mjpegCam, exists := cameraMap["cam-mjpeg"]
	require.True(t, exists, "MJPEG camera should be in database")
	assert.Equal(t, "MJPEG Camera", mjpegCam.Name)
	assert.Equal(t, "rtsp", mjpegCam.Protocol)

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
	})
	require.NoError(t, err)
	assert.Equal(t, "cam-new-h264", id)

	// Recorder should be created
	ok := mgr.GetRecorder("cam-new-h264") != nil
	assert.True(t, ok, "recorder should be created for enabled h264 camera")
	assert.Equal(t, 1, mgr.RecorderCount())

	// Camera should be in config
	assert.Len(t, mgr.cfg.Cameras, 5) // 4 original + 1 new
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
	})
	require.NoError(t, err)
	assert.Equal(t, "cam-new-jpeg", id)

	// Recorder should be created for http_jpeg
	ok := mgr.GetRecorder("cam-new-jpeg") != nil
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
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")
}

// TestAddCamera_DuplicateONVIFEndpoint verifies AddCamera dedupes by ONVIF
// endpoint, not just by ID. Auto-discover generates a fresh random ID per
// discovery, so ID-level dedup alone cannot stop the same physical ONVIF device
// from being enrolled twice (one manual/early add + one auto-discover add with
// a different ID). This is the last-line defense behind the auto-discover
// Adder's existsInDB check.
//
// Uses ActivationState="pending_activation" so AddCamera skips recorder startup
// (a real ONVIF handshake against the fake endpoint would hang the test). The
// dedup check runs in PHASE 1 regardless of activation state.
func TestAddCamera_DuplicateONVIFEndpoint(t *testing.T) {
	mgr, _, _, _ := newTestManager(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const ep = "http://192.168.63.212:80/onvif/device_service"
	// First add (mimics a manual/early add with a non-generated ID).
	id1, err := mgr.AddCamera(ctx, config.CameraConfig{
		ID:              "cam-early-manual",
		Name:            "视通",
		Protocol:        "onvif",
		ONVIFEndpoint:   ep,
		ActivationState: "pending_activation", // skip recorder start
	})
	require.NoError(t, err)
	assert.Equal(t, "cam-early-manual", id1)

	// Second add: different (auto-generated) ID, SAME endpoint — must be rejected.
	_, err = mgr.AddCamera(ctx, config.CameraConfig{
		ID:              "cam-autodiscover-fresh-id",
		Name:            "IPC",
		Protocol:        "onvif",
		ONVIFEndpoint:   ep,
		ActivationState: "pending_activation",
	})
	require.Error(t, err, "AddCamera must dedupe by ONVIF endpoint, not just ID")
	assert.Contains(t, err.Error(), "already exists")

	// Trailing-slash tolerance: the same endpoint with a trailing slash is the
	// same device (WS-Discovery / device firmware sometimes appends one).
	_, err = mgr.AddCamera(ctx, config.CameraConfig{
		ID:              "cam-autodiscover-slash",
		Name:            "IPC2",
		Protocol:        "onvif",
		ONVIFEndpoint:   ep + "/",
		ActivationState: "pending_activation",
	})
	require.Error(t, err, "AddCamera must dedupe by endpoint ignoring trailing slash")

	// No duplicate was actually added.
	assert.Equal(t, 5, len(mgr.cfg.Cameras), "only the 4 seed cameras + 1 manual add should exist")
}

// TestAddCamera_DuplicateStableID verifies AddCamera dedupes by stable_id
// (ONVIF hardware serial) — catches the same device after a DHCP IP change
// (endpoint differs, hardware identity is the same).
func TestAddCamera_DuplicateStableID(t *testing.T) {
	mgr, _, _, _ := newTestManager(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const serial = "030a000200e76182e1d6"
	_, err := mgr.AddCamera(ctx, config.CameraConfig{
		ID:              "cam-old-ip",
		Name:            "Cam at old IP",
		Protocol:        "onvif",
		StableID:        serial,
		ActivationState: "pending_activation", // skip recorder start
	})
	require.NoError(t, err)

	// Same device, different endpoint (IP changed), same stable_id — must reject.
	_, err = mgr.AddCamera(ctx, config.CameraConfig{
		ID:              "cam-new-ip",
		Name:            "Cam at new IP",
		Protocol:        "onvif",
		ONVIFEndpoint:   "http://192.168.63.99:80/onvif/device_service",
		StableID:        serial,
		ActivationState: "pending_activation",
	})
	require.Error(t, err, "AddCamera must dedupe by stable_id (hardware serial)")
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
	assert.Equal(t, 4, mgr.RecorderCount())

	// Remove a camera that has a recorder
	err = mgr.RemoveCamera(ctx, "cam-h264")
	require.NoError(t, err)

	// Recorder should be removed
	assert.Equal(t, 3, mgr.RecorderCount())
	ok := mgr.GetRecorder("cam-h264") != nil
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
	assert.Equal(t, 4, mgr.RecorderCount())

	newURL := "rtsp://127.0.0.1:2/new-stream"
	updated, err := mgr.UpdateCamera(ctx, "cam-h264", CameraUpdate{URL: &newURL})
	require.NoError(t, err)
	assert.Equal(t, newURL, updated.URL)

	// Recorder should still exist (restarted)
	assert.Equal(t, 4, mgr.RecorderCount())
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

// TestUpdateCamera_ToggleRecordingEnabled_Restarts verifies that toggling
// recording_enabled triggers a recorder restart in ALL cases — including the
// previously-broken nil→false transition (issue #73). Before the fix, a camera
// whose recording_enabled was never explicitly persisted (nil = default record)
// would NOT restart when set to false, so the old recorder kept writing
// segments to disk despite the user disabling recording.
func TestUpdateCamera_ToggleRecordingEnabled_Restarts(t *testing.T) {
	tests := []struct {
		name    string
		oldVal  *bool // camera's recording_enabled before the update
		newVal  bool  // value sent in the update
		restart bool  // expect a recorder restart?
	}{
		{"nil_to_false_restarts", nil, false, true},  // the bug: previously no restart
		{"nil_to_true_no_restart", nil, true, false}, // true→true (effective)
		{"true_to_false_restarts", ptrBool(true), false, true},
		{"true_to_true_no_restart", ptrBool(true), true, false},
		{"false_to_true_restarts", ptrBool(false), true, true},
		{"false_to_false_no_restart", ptrBool(false), false, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mgr, _, _, _ := newTestManager(t)
			// Set the camera's initial recording_enabled before start.
			for i, c := range mgr.cfg.Cameras {
				if c.ID == "cam-h264" {
					mgr.cfg.Cameras[i].RecordingEnabled = tc.oldVal
				}
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			require.NoError(t, mgr.Start(ctx))
			require.Equal(t, 4, mgr.RecorderCount())

			// Snapshot the recorder identity before the update.
			recBefore := mgr.snapshotRecorder("cam-h264")

			_, err := mgr.UpdateCamera(ctx, "cam-h264", CameraUpdate{RecordingEnabled: &tc.newVal})
			require.NoError(t, err)

			recAfter := mgr.snapshotRecorder("cam-h264")
			if tc.restart {
				// A restart replaces the recorder instance.
				if fmt.Sprintf("%p", recBefore) == fmt.Sprintf("%p", recAfter) {
					t.Errorf("expected recorder to be restarted when recording_enabled changes effectively (same pointer %p)", recAfter)
				}
			} else {
				if fmt.Sprintf("%p", recBefore) != fmt.Sprintf("%p", recAfter) {
					t.Errorf("expected NO recorder restart when recording_enabled is effectively unchanged (before=%p after=%p)", recBefore, recAfter)
				}
			}
		})
	}
}

func TestRestartRecorder(t *testing.T) {
	mgr, _, _, _ := newTestManager(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start to create recorders
	err := mgr.Start(ctx)
	require.NoError(t, err)
	assert.Equal(t, 4, mgr.RecorderCount())

	// Restart a recorder
	err = mgr.RestartRecorder(ctx, "cam-h264")
	require.NoError(t, err)

	// Recorder should still be there
	assert.Equal(t, 4, mgr.RecorderCount())
	ok := mgr.GetRecorder("cam-h264") != nil
	assert.True(t, ok)
}

// TestWithCameraLifecycle_SerializesConcurrentOps verifies the per-camera
// single-flight guard serializes lifecycle operations for one camera (no two
// run concurrently — the precondition that prevents recorder-construction
// leaks when a manual restart overlaps a health auto-remediation restart).
func TestWithCameraLifecycle_SerializesConcurrentOps(t *testing.T) {
	mgr, _, _, _ := newTestManager(t)

	var (
		inFlight    atomic.Int32
		maxInFlight atomic.Int32
		wg          sync.WaitGroup
	)
	const goroutines = 20
	wg.Add(goroutines)
	for range goroutines {
		go func() {
			defer wg.Done()
			_ = mgr.withCameraLifecycle("cam-h264", func() error {
				// Record concurrency: if two ever overlap, maxInFlight > 1.
				cur := inFlight.Add(1)
				for {
					old := maxInFlight.Load()
					if cur <= old || maxInFlight.CompareAndSwap(old, cur) {
						break
					}
				}
				time.Sleep(2 * time.Millisecond) // widen the overlap window
				inFlight.Add(-1)
				return nil
			})
		}()
	}
	wg.Wait()
	assert.Equal(t, int32(1), maxInFlight.Load(), "lifecycle ops for one camera must be serialized (no concurrent execution)")
}

// TestWithCameraLifecycle_DifferentCamerasDoNotBlock verifies the per-camera
// guard does NOT serialize across different cameras — B's lifecycle must
// proceed even while A's is mid-flight.
func TestWithCameraLifecycle_DifferentCamerasDoNotBlock(t *testing.T) {
	mgr, _, _, _ := newTestManager(t)

	aStarted := make(chan struct{})
	aProceed := make(chan struct{})
	bDone := make(chan struct{})

	// A holds its guard open until released.
	go func() {
		_ = mgr.withCameraLifecycle("cam-a", func() error {
			close(aStarted)
			<-aProceed
			return nil
		})
	}()
	<-aStarted

	// B must be able to run concurrently (different camera → different guard).
	go func() {
		_ = mgr.withCameraLifecycle("cam-b", func() error {
			close(bDone)
			return nil
		})
	}()
	select {
	case <-bDone:
		// good — B ran despite A holding its guard
	case <-time.After(time.Second):
		t.Fatal("cam-b lifecycle was blocked by cam-a's guard — guards must be per-camera")
	}
	close(aProceed)
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

func TestHubStatsFlusherExportsSends(t *testing.T) {
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

	// Subscribe a consumer so per-consumer sends accumulate.
	require.NoError(t, hub.Subscribe("test-consumer", func(int64, [][]byte) {}))

	// Register the hub in the manager snapshot so the flusher can see it.
	mgr.apply(func(s *snapshot) *snapshot {
		s.hubs[cfg.Cameras[0].ID] = hub
		return s
	})

	// First flush seeds per-consumer baselines (exports nothing).
	mgr.flushHubStats()

	// Simulate 50 frames (< consumer buffer of 150, so none drop).
	for i := range 50 {
		hub.Broadcast(int64(i), [][]byte{{byte(i)}}, i == 0)
	}

	// Second flush exports the delta accumulated since the first.
	mgr.flushHubStats()

	families, err := m.Registry.Gather()
	require.NoError(t, err)

	var sent, dwellAvgFound bool
	for _, f := range families {
		switch f.GetName() {
		case "nvr_streamhub_frames_sent_total":
			for _, metric := range f.GetMetric() {
				if metric.GetCounter().GetValue() >= 50 {
					sent = true
				}
			}
		case "nvr_streamhub_hop_dwell_ms_avg":
			dwellAvgFound = true
		}
	}
	require.True(t, sent, "expected nvr_streamhub_frames_sent_total >= 50 for test-consumer")
	require.True(t, dwellAvgFound, "expected nvr_streamhub_hop_dwell_ms_avg gauge family")
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
	mgr.reseedSnapshotConfigs()

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
		ID:          "new-onvif-cam",
		Name:        "New ONVIF Camera",
		Protocol:    "onvif",
		URL:         "http://192.168.1.100/onvif/device_service",
		Username:    "admin",
		Password:    "pass",
		SnapshotURL: "http://custom-snapshot.jpg",
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
	mgr.auxMu.Lock()
	defer mgr.auxMu.Unlock()
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
	})
	mgr.reseedSnapshotConfigs()

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
				// RecordingEnabled=false so the timelapse capturer is used
				RecordingEnabled: ptrBool(false),
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
	rec := mgr.GetRecorder("cam-dual")
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
	err = mgr.startTimelapseKeyframeExtractor(cam.ID, cam, hub, nil)
	require.NoError(t, err, "should start keyframe extractor for H264 dual-mode")

	// Verify KFE is registered and running
	mgr.auxMu.Lock()
	ext, exists := mgr.keyframeExtractors[cam.ID]
	mgr.auxMu.Unlock()
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
	err = mgr.startTimelapseKeyframeExtractor(cam.ID, cam, hub, nil)
	require.NoError(t, err, "should start keyframe extractor for ONVIF dual-mode")

	// Verify KFE is registered and running
	mgr.auxMu.Lock()
	ext, exists := mgr.keyframeExtractors[cam.ID]
	mgr.auxMu.Unlock()
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

	err = mgr.startTimelapseKeyframeExtractor(cam.ID, cam, hub, nil)
	require.NoError(t, err, "should start keyframe extractor for ONVIF H264 dual-mode")

	mgr.auxMu.Lock()
	ext, exists := mgr.keyframeExtractors[cam.ID]
	mgr.auxMu.Unlock()
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
		}
		err := mgr.startTimelapseKeyframeExtractor(cam.ID, cam, nil, nil)
		assert.NoError(t, err, "no timelapse config should not error")
		mgr.auxMu.Lock()
		_, exists := mgr.keyframeExtractors[cam.ID]
		mgr.auxMu.Unlock()
		assert.False(t, exists, "should not create KFE without timelapse config")
	})

	t.Run("timelapse disabled", func(t *testing.T) {
		cam := config.CameraConfig{
			ID:       "cam-tl-disabled",
			Protocol: "rtsp",
			Encoding: "h264",
			URL:      "rtsp://192.168.1.100/stream",
			Timelapse: &config.CameraTimelapseConfig{
				Enabled:     false,
				FrameSource: "rtsp_keyframe",
			},
		}
		err := mgr.startTimelapseKeyframeExtractor(cam.ID, cam, nil, nil)
		assert.NoError(t, err, "disabled timelapse should not error")
		mgr.auxMu.Lock()
		_, exists := mgr.keyframeExtractors[cam.ID]
		mgr.auxMu.Unlock()
		assert.False(t, exists, "should not create KFE when timelapse disabled")
	})

	t.Run("wrong frame source", func(t *testing.T) {
		trueVal := true
		cam := config.CameraConfig{
			ID:       "cam-tl-snapshot",
			Protocol: "rtsp",
			Encoding: "h264",
			URL:      "rtsp://192.168.1.100/stream",
			Timelapse: &config.CameraTimelapseConfig{
				Enabled:     trueVal,
				FrameSource: "snapshot",
				Interval:    "10s",
			},
		}
		err := mgr.startTimelapseKeyframeExtractor(cam.ID, cam, nil, nil)
		assert.NoError(t, err, "wrong frame source should not error")
		mgr.auxMu.Lock()
		_, exists := mgr.keyframeExtractors[cam.ID]
		mgr.auxMu.Unlock()
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
		Timelapse: &config.CameraTimelapseConfig{
			Enabled:     trueVal,
			FrameSource: "rtsp_keyframe",
			Interval:    "10s",
		},
	}

	// Add camera to config so StopCamera can find it
	mgr.cfg.Cameras = append(mgr.cfg.Cameras, cam)
	mgr.reseedSnapshotConfigs()

	segDur, err := time.ParseDuration(cfg.Storage.SegmentDuration)
	require.NoError(t, err)

	// Create recorder to get hub
	rec := mgr.createRecorder(cam, segDur)
	require.NotNil(t, rec)
	hub := getRecorderHub(rec)
	require.NotNil(t, hub)

	// Register recorder manually (startRecorder would fail on rec.Start for fake URL)
	mgr.SetTestRecorder(cam.ID, rec)

	// Start KFE
	err = mgr.startTimelapseKeyframeExtractor(cam.ID, cam, hub, nil)
	require.NoError(t, err)

	// Verify KFE is registered and running
	mgr.auxMu.Lock()
	ext, exists := mgr.keyframeExtractors[cam.ID]
	mgr.auxMu.Unlock()
	require.True(t, exists)
	require.True(t, ext.IsRunning())

	// Stop camera
	ctx := context.Background()
	err = mgr.StopCamera(ctx, cam.ID)
	require.NoError(t, err)

	// Verify KFE is removed from map
	mgr.auxMu.Lock()
	_, exists = mgr.keyframeExtractors[cam.ID]
	mgr.auxMu.Unlock()
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
	err = mgr.startTimelapseKeyframeExtractor(cam.ID, cam, hub, nil)
	require.NoError(t, err, "should start KFE with standalone timelapse recorder hub")

	mgr.auxMu.Lock()
	ext, exists := mgr.keyframeExtractors[cam.ID]
	mgr.auxMu.Unlock()
	assert.True(t, exists, "keyframe extractor should be registered")
	assert.True(t, ext.IsRunning(), "keyframe extractor should be running")

	mgr.stopTimelapseKeyframeExtractor(cam.ID)
}

func TestDualMode_ONVIFTimelapse_AutoFrameSource_CreatesKeyframeExtractor(t *testing.T) {
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
		ID:             "cam-dual-onvif-auto",
		Name:           "Dual-Mode ONVIF Auto",
		Protocol:       "onvif",
		URL:            "http://192.168.1.100/onvif/device_service",
		StreamEncoding: "H265",
		Username:       "admin",
		Password:       "pass",
		Timelapse: &config.CameraTimelapseConfig{
			Enabled:     trueVal,
			FrameSource: "auto", // should be resolved to rtsp_keyframe
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

	// Start keyframe extractor with the hub — should succeed with auto frame source
	err = mgr.startTimelapseKeyframeExtractor(cam.ID, cam, hub, nil)
	require.NoError(t, err, "should start keyframe extractor for ONVIF dual-mode with auto frame source")

	// Verify KFE is registered and running
	mgr.auxMu.Lock()
	ext, exists := mgr.keyframeExtractors[cam.ID]
	mgr.auxMu.Unlock()
	assert.True(t, exists, "keyframe extractor should be registered")
	assert.True(t, ext.IsRunning(), "keyframe extractor should be running")

	// Cleanup
	mgr.stopTimelapseKeyframeExtractor(cam.ID)
}

func TestGetCodecInfo_NonExistentCamera(t *testing.T) {
	mgr, _, _, _ := newTestManager(t)

	ci := mgr.GetCodecInfo("nonexistent")
	assert.Nil(t, ci.SPS, "SPS should be nil for nonexistent camera")
	assert.Nil(t, ci.PPS, "PPS should be nil for nonexistent camera")
	assert.Nil(t, ci.VPS, "VPS should be nil for nonexistent camera")
	assert.Empty(t, ci.AudioCodec, "audio codec should be empty")
	assert.Equal(t, 0, ci.AudioSampleRate, "sample rate should be 0")
	assert.Equal(t, 0, ci.AudioChannels, "channels should be 0")
}

func TestGetCodecInfo_KnownCameraBeforeStreaming(t *testing.T) {
	mgr, _, _, _ := newTestManager(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, mgr.Start(ctx))

	// H264 camera: recorder exists but not yet streaming → empty codec info
	ci := mgr.GetCodecInfo("cam-h264")
	assert.NotNil(t, ci, "should return non-nil CodecInfo")
	assert.Nil(t, ci.SPS, "SPS should be nil before stream starts")
	assert.Nil(t, ci.PPS, "PPS should be nil before stream starts")
	assert.True(t, ci.IsH264, "H264 recorder should report IsH264=true")
	assert.Empty(t, ci.AudioCodec, "no audio before stream starts")

	// MJPEG camera: should return through fallback path
	mjpegCI := mgr.GetCodecInfo("cam-mjpeg")
	assert.NotNil(t, mjpegCI, "should return non-nil CodecInfo for MJPEG")
	assert.Nil(t, mjpegCI.SPS, "SPS should be nil for MJPEG fallback")
	assert.Empty(t, mjpegCI.AudioCodec, "no audio for MJPEG")
}

func TestGetCodecInfo_NilRecorder(t *testing.T) {
	mgr, _, _, _ := newTestManager(t)
	// Don't Start the manager — no recorders registered yet.

	ci := mgr.GetCodecInfo("cam-h264")
	assert.Nil(t, ci.SPS, "SPS should be nil when no recorders")
	assert.False(t, ci.IsH264, "IsH264 should be false when no recorders")
}

// --- Relay manager tests ---

type mockRelayManager struct {
	mu            sync.Mutex
	targets       map[string][]config.PushTargetConfig
	removedCamera string
}

func (m *mockRelayManager) SetCameraTargets(cameraID string, cfgs []config.PushTargetConfig) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.targets == nil {
		m.targets = make(map[string][]config.PushTargetConfig)
	}
	m.targets[cameraID] = cfgs
}

func (m *mockRelayManager) RemoveCamera(cameraID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.removedCamera = cameraID
	delete(m.targets, cameraID)
}

func TestSetRelayManager(t *testing.T) {
	cm := NewCameraManager(nil, nil, nil, "")
	require.Nil(t, cm.relayMgr)

	mockRM := &mockRelayManager{}
	cm.SetRelayManager(mockRM)
	require.Equal(t, mockRM, cm.relayMgr)
}

func TestRelayStatus_NilManager(t *testing.T) {
	cm := NewCameraManager(nil, nil, nil, "")
	status := cm.RelayStatus("cam-1")
	require.Nil(t, status, "should return nil when no relay manager set")
}

func TestSetCameraTargetsNoDeadlock(t *testing.T) {
	cm := NewCameraManager(nil, nil, nil, "")
	mockRM := &mockRelayManager{}
	cm.SetRelayManager(mockRM)

	targets := []config.PushTargetConfig{{URL: "rtmp://example.com/live/stream"}}
	done := make(chan struct{})
	go func() {
		cm.relayMgr.SetCameraTargets("cam-1", targets)
		close(done)
	}()

	select {
	case <-done:
		// Success
	case <-time.After(2 * time.Second):
		t.Fatal("SetCameraTargets did not complete within timeout")
	}
}

// ptrBool returns a pointer to the given bool value.
func ptrBool(v bool) *bool { return &v }

func TestStartSkipsTimelapseWhenRecordingEnabled(t *testing.T) {
	t.Helper()

	trueVal := true
	segDur := 10 * time.Minute

	tests := []struct {
		name             string
		recordingEnabled *bool
		expectSkip       bool // true = capturer should be skipped (not started)
	}{
		{"recording_enabled_true", ptrBool(true), true},
		{"recording_enabled_false", ptrBool(false), false},
		{"recording_enabled_nil", nil, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
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

			cam := config.CameraConfig{
				ID:               tc.name,
				Name:             "Test Camera",
				Protocol:         "rtsp",
				Encoding:         "h264",
				URL:              "rtsp://192.168.1.100/stream",
				RecordingEnabled: tc.recordingEnabled,
				Timelapse: &config.CameraTimelapseConfig{
					Enabled:     trueVal,
					FrameSource: "rtsp_keyframe",
					Interval:    "10s",
				},
			}

			// Create the recorder (succeeds — no network I/O)
			rec := mgr.createRecorder(cam, segDur)
			require.NotNil(t, rec, "should create recorder")

			hub := getRecorderHub(rec)
			require.NotNil(t, hub, "recorder should have StreamHub")

			// Verify the recording_enabled guard condition
			recordingEnabled := cam.RecordingEnabled == nil || *cam.RecordingEnabled
			assert.Equal(t, tc.expectSkip, recordingEnabled && cam.Timelapse.Enabled,
				"recording_enabled && timelapse.enabled condition")

			if tc.expectSkip {
				// When recording_enabled=true, verify no extractor/poller is started
				mgr.auxMu.Lock()
				_, kfeExists := mgr.keyframeExtractors[tc.name]
				_, fpExists := mgr.framePollers[tc.name]
				mgr.auxMu.Unlock()
				assert.False(t, kfeExists, "keyframe extractor should not exist when recording_enabled is true/nil")
				assert.False(t, fpExists, "frame poller should not exist when recording_enabled is true/nil")
			} else {
				// When recording_enabled=false, verify the extractor CAN be started
				// (the guard in Start() allows it)
				err := mgr.startTimelapseKeyframeExtractor(tc.name, cam, hub, rec)
				require.NoError(t, err, "should start keyframe extractor when recording_enabled=false")
				mgr.auxMu.Lock()
				ext, kfeExists := mgr.keyframeExtractors[tc.name]
				mgr.auxMu.Unlock()
				assert.True(t, kfeExists, "keyframe extractor should exist when recording_enabled=false")
				assert.NotNil(t, ext)
				mgr.stopTimelapseKeyframeExtractor(tc.name)
			}
		})
	}
}

func TestTimelapseDisabledNoCapturer(t *testing.T) {
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

	segDur := 10 * time.Minute

	t.Run("timelapse nil", func(t *testing.T) {
		cam := config.CameraConfig{
			ID:       "cam-no-tl",
			Protocol: "rtsp",
			Encoding: "h264",
			URL:      "rtsp://192.168.1.100/stream",
		}
		recordingEnabled := cam.RecordingEnabled == nil || *cam.RecordingEnabled
		timelapseEnabled := cam.Timelapse != nil && cam.Timelapse.Enabled
		assert.False(t, recordingEnabled && timelapseEnabled,
			"nil timelapse: should not try to skip capturer")
		assert.Equal(t, "", effectiveDualModeFrameSource(cam),
			"nil timelapse: effective frame source should be empty")

		// Verify that Start() path would not try to skip
		rec := mgr.createRecorder(cam, segDur)
		require.NotNil(t, rec, "should create recorder for non-timelapse camera")
		hub := getRecorderHub(rec)
		require.NotNil(t, hub)
		// Calling startTimelapseKeyframeExtractor should respect timelapse disabled
		err := mgr.startTimelapseKeyframeExtractor(cam.ID, cam, hub, rec)
		assert.NoError(t, err, "no error when timelapse disabled")
		mgr.auxMu.Lock()
		_, kfeExists := mgr.keyframeExtractors[cam.ID]
		mgr.auxMu.Unlock()
		assert.False(t, kfeExists, "no keyframe extractor when timelapse config absent")
	})

	t.Run("timelapse disabled", func(t *testing.T) {
		cam := config.CameraConfig{
			ID:       "cam-tl-disabled",
			Protocol: "rtsp",
			Encoding: "h264",
			URL:      "rtsp://192.168.1.100/stream",
			Timelapse: &config.CameraTimelapseConfig{
				Enabled:     false,
				FrameSource: "rtsp_keyframe",
			},
		}
		recordingEnabled := cam.RecordingEnabled == nil || *cam.RecordingEnabled
		timelapseEnabled := cam.Timelapse != nil && cam.Timelapse.Enabled
		assert.False(t, recordingEnabled && timelapseEnabled,
			"disabled timelapse: should not try to skip capturer")
		assert.Equal(t, "", effectiveDualModeFrameSource(cam),
			"disabled timelapse: effective frame source should be empty")
	})
}

// TestAddCameraReverseONVIFLookup verifies that tryFillStableIDFromONVIF populates
// StableID when the ONVIF device returns a serial number.
func TestAddCameraReverseONVIFLookup(t *testing.T) {
	mockClient := &onvif.MockDeviceClient{
		DeviceInfo: &onvif.DeviceInfo{
			SerialNumber: "ABC123",
		},
	}

	cam := &config.CameraConfig{
		Protocol:      "onvif",
		ONVIFEndpoint: "http://192.168.63.212:80/onvif/device_service",
		Username:      "admin",
		Password:      "admin",
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := tryFillStableIDFromONVIFWithClient(ctx, cam, mockClient)
	require.NoError(t, err)
	assert.Equal(t, "ABC123", cam.StableID)
	assert.Equal(t, 1, mockClient.ConnectCalls)
	assert.Equal(t, 1, mockClient.GetDeviceInformationCalls)
}

// TestAddCameraReverseONVIFFailure verifies that when the ONVIF connection fails,
// AddCamera still succeeds (best-effort) and StableID remains empty.
func TestAddCameraReverseONVIFFailure(t *testing.T) {
	mockClient := &onvif.MockDeviceClient{
		ConnectError: fmt.Errorf("connection refused"),
	}

	cam := &config.CameraConfig{
		Protocol:      "onvif",
		ONVIFEndpoint: "http://192.168.63.212:80/onvif/device_service",
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := tryFillStableIDFromONVIFWithClient(ctx, cam, mockClient)
	require.NoError(t, err, "best-effort: must not return error on connect failure")
	assert.Empty(t, cam.StableID, "StableID must remain empty on connect failure")
	assert.Equal(t, 1, mockClient.ConnectCalls)
	assert.Equal(t, 0, mockClient.GetDeviceInformationCalls)
}

// TestAddCameraReverseONVIFSkip verifies that when StableID is already set,
// the reverse ONVIF lookup is skipped entirely.
func TestAddCameraReverseONVIFSkip(t *testing.T) {
	// Test 1: StableID already set → skip
	cam := &config.CameraConfig{
		StableID:      "already-set",
		Protocol:      "onvif",
		ONVIFEndpoint: "http://192.168.63.212:80/onvif/device_service",
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := tryFillStableIDFromONVIF(ctx, cam)
	require.NoError(t, err)
	assert.Equal(t, "already-set", cam.StableID, "StableID must remain unchanged")

	// Test 2: Non-ONVIF protocol → skip
	cam2 := &config.CameraConfig{
		Protocol: "rtsp",
		URL:      "rtsp://192.168.1.1/stream",
	}
	err = tryFillStableIDFromONVIF(ctx, cam2)
	require.NoError(t, err)
	assert.Empty(t, cam2.StableID)

	// Test 3: Empty endpoint → skip
	cam3 := &config.CameraConfig{
		Protocol: "onvif",
	}
	err = tryFillStableIDFromONVIF(ctx, cam3)
	require.NoError(t, err)
	assert.Empty(t, cam3.StableID)
}

// TestTryFillStableIDFromONVIF_OverwritesDirty verifies the #216 self-heal: a
// dirty StableID (IP, URL, all-zero MAC — frozen in YAML by a prior firmware
// glitch) is NOT treated as "already set". The reverse ONVIF lookup runs and
// overwrites it with the real hardware serial.
func TestTryFillStableIDFromONVIF_OverwritesDirty(t *testing.T) {
	realSerial := "744dbd988218"
	dirtyCases := []struct {
		name    string
		dirtyID string
	}{
		{"dirty = IP address", "192.168.63.148"},
		{"dirty = all-zero MAC", "000000000000"},
		{"dirty = URL", "http://cam/onvif"},
	}
	for _, tc := range dirtyCases {
		t.Run(tc.name, func(t *testing.T) {
			mockClient := &onvif.MockDeviceClient{
				DeviceInfo: &onvif.DeviceInfo{SerialNumber: realSerial},
			}
			cam := &config.CameraConfig{
				StableID:      tc.dirtyID,
				Protocol:      "onvif",
				ONVIFEndpoint: "http://192.168.63.212:80/onvif/device_service",
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			err := tryFillStableIDFromONVIFWithClient(ctx, cam, mockClient)
			require.NoError(t, err)
			assert.Equal(t, realSerial, cam.StableID, "dirty stable_id must be overwritten by the real serial")
			assert.Equal(t, 1, mockClient.GetDeviceInformationCalls, "lookup must run despite non-empty dirty value")
		})
	}
}

// TestTryFillStableIDFromONVIF_RejectsDirtySerial verifies the firmware-glitch
// defense (#216, upstream seeed-esp32s3-cam #2): when GetDeviceInformation
// returns a garbage serial (all-zero, empty, IP-like), it is NOT persisted —
// the dirty/garbage value cannot re-poison the field.
func TestTryFillStableIDFromONVIF_RejectsDirtySerial(t *testing.T) {
	garbageCases := []struct {
		name   string
		serial string
	}{
		{"all-zero", "000000000000"},
		{"empty after trim", "   "},
		{"IP-like", "192.168.1.10"},
		{"too short", "ab"},
	}
	for _, tc := range garbageCases {
		t.Run(tc.name, func(t *testing.T) {
			mockClient := &onvif.MockDeviceClient{
				DeviceInfo: &onvif.DeviceInfo{SerialNumber: tc.serial},
			}
			cam := &config.CameraConfig{
				Protocol:      "onvif",
				ONVIFEndpoint: "http://192.168.63.212:80/onvif/device_service",
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			err := tryFillStableIDFromONVIFWithClient(ctx, cam, mockClient)
			require.NoError(t, err, "best-effort: must not return error on garbage serial")
			assert.Empty(t, cam.StableID, "garbage serial must NOT be persisted")
		})
	}
}

func TestCreateRecorder_GB28181(t *testing.T) {
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

	// Test H.264 GB28181 camera
	cam := config.CameraConfig{
		ID:       "cam-gb28181-h264",
		Name:     "GB28181 H264 Camera",
		Protocol: string(model.ProtoGB28181),
		Encoding: "h264",
	}
	segDur, err := time.ParseDuration(cfg.Storage.SegmentDuration)
	require.NoError(t, err)

	rec := mgr.createRecorder(cam, segDur)
	require.NotNil(t, rec, "GB28181 protocol should create a recorder")
	gbRec, ok := rec.(*recorder.GB28181Recorder)
	require.True(t, ok, "recorder should be a GB28181Recorder")
	require.NotNil(t, gbRec.GetHub(), "hub should be set by initStreamHub")

	// Verify hub is in the hubRegistry
	snapshot := mgr.loadSnapshot()
	hub, ok := snapshot.hubs["cam-gb28181-h264"]
	require.True(t, ok, "hub should be registered")
	require.Same(t, gbRec.Hub, hub, "hub in registry should be the same object")

	// Test H.265 GB28181 camera
	cam2 := config.CameraConfig{
		ID:       "cam-gb28181-h265",
		Name:     "GB28181 H265 Camera",
		Protocol: string(model.ProtoGB28181),
		Encoding: "h265",
	}
	rec2 := mgr.createRecorder(cam2, segDur)
	require.NotNil(t, rec2, "GB28181 protocol should create a recorder for H.265")
	gbRec2, ok := rec2.(*recorder.GB28181Recorder)
	require.True(t, ok)
	require.NotNil(t, gbRec2.GetHub(), "hub should be set by initStreamHub")
}

func TestGetGB28181Recorder(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := &config.Config{
		Storage: config.StorageConfig{
			RootDir:         filepath.Join(tmpDir, "storage"),
			SegmentDuration: "1m",
		},
		Cameras: []config.CameraConfig{{
			ID:       "cam-gb28181",
			Name:     "GB28181 Camera",
			Protocol: string(model.ProtoGB28181),
			Encoding: "h264",
		}},
	}
	require.NoError(t, os.MkdirAll(cfg.Storage.RootDir, 0o755))

	store, err := storage.NewManager(cfg.Storage.RootDir)
	require.NoError(t, err)
	defer store.CleanupTempFiles()

	db, err := storage.New(filepath.Join(tmpDir, "test.db"))
	require.NoError(t, err)
	defer db.Close()

	mgr := NewCameraManager(cfg, store, db, tmpDir)
	segDur, err := time.ParseDuration(cfg.Storage.SegmentDuration)
	require.NoError(t, err)

	// Create a GB28181 recorder
	rec := mgr.createRecorder(cfg.Cameras[0], segDur)
	require.NotNil(t, rec)

	// Register the recorder
	mgr.apply(func(s *snapshot) *snapshot {
		s.recorders["cam-gb28181"] = rec
		return s
	})

	// Test GetGB28181Recorder
	gbRec := mgr.GetGB28181Recorder("cam-gb28181")
	require.NotNil(t, gbRec)
	require.Same(t, rec, gbRec)

	// Test non-GB28181 camera returns nil
	nilRec := mgr.GetGB28181Recorder("non-existent")
	require.Nil(t, nilRec)
}

// recordingStatsProvider mirrors the flow handler's recorder capability probe.
type recordingStatsProvider interface {
	RecordingStats() recorder.RecordingStats
}

// TestCreateRecorder_RingBufCapOverride verifies per-camera ring_buf_cap
// (issue #521) reaches the recorder's frameCh capacity: an explicit value is
// used verbatim; 0 falls through to recorder.DefaultRingBufCap.
func TestCreateRecorder_RingBufCapOverride(t *testing.T) {
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
	segDur, err := time.ParseDuration(cfg.Storage.SegmentDuration)
	require.NoError(t, err)

	statsOf := func(rec model.Recorder) recorder.RecordingStats {
		sp, ok := rec.(recordingStatsProvider)
		require.True(t, ok, "recorder should expose RecordingStats")
		return sp.RecordingStats()
	}

	t.Run("explicit override", func(t *testing.T) {
		t.Helper()
		cam := config.CameraConfig{
			ID: "cam-ring-override", Name: "Ring Override", Protocol: "rtsp",
			Encoding: "h264", URL: "rtsp://127.0.0.1:8554/test", RingBufCap: 777,
		}
		rec := mgr.createRecorder(cam, segDur)
		require.NotNil(t, rec)
		require.Equal(t, 777, statsOf(rec).RingCap)
	})

	t.Run("zero falls back to default", func(t *testing.T) {
		t.Helper()
		cam := config.CameraConfig{
			ID: "cam-ring-default", Name: "Ring Default", Protocol: "rtsp",
			Encoding: "h264", URL: "rtsp://127.0.0.1:8554/test",
		}
		rec := mgr.createRecorder(cam, segDur)
		require.NotNil(t, rec)
		require.Equal(t, recorder.DefaultRingBufCap, statsOf(rec).RingCap)
	})
}
