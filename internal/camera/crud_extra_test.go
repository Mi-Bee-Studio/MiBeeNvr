package camera

// Long-tail coverage for camera CRUD + PTZ forwarding (#583): the
// publishCameraAdded event path (real event bus), UpdateCamera field
// matrix, ArchiveCamera teardown, and ForwardPTZ dispatch for the
// Xiaomi/guard/default branches.

import (
	"context"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/event"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/onvif"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
	"github.com/stretchr/testify/require"
)

// newManagerWithBus mirrors newTestManagerWithCfg but injects a real event
// bus through the variadic opts (same wiring as pkg/app).
func newManagerWithBus(t *testing.T, cfg *config.Config) (*CameraManager, *event.EventBus) {
	t.Helper()
	bus := event.NewEventBus(8)
	tmpDir := t.TempDir()
	cfg.Storage.RootDir = tmpDir + "/storage"
	require.NoError(t, config.Save(tmpDir+"/config.yaml", cfg))
	db, err := storage.New(tmpDir + "/test.db")
	require.NoError(t, err)
	require.NoError(t, db.Init(context.Background()))
	store, err := storage.NewManager(cfg.Storage.RootDir)
	require.NoError(t, err)
	mgr := NewCameraManager(cfg, store, db, tmpDir+"/config.yaml", bus)
	t.Cleanup(func() { mgr.Stop() })
	t.Cleanup(func() { db.Close() })
	return mgr, bus
}

func TestPublishCameraAddedEvent(t *testing.T) {
	t.Parallel()
	cfg := testConfig()
	cfg.Cameras = nil
	mgr, bus := newManagerWithBus(t, cfg)

	events := make(chan event.Event, 4)
	require.NoError(t, bus.SubscribeByPrefix(event.TopicCameraAdded, events, 4))

	id, err := mgr.AddCamera(context.Background(), config.CameraConfig{
		ID: "added-cam", Name: "Added", Protocol: "http", Encoding: "jpeg",
		URL: "http://127.0.0.1:1/frame",
	})
	require.NoError(t, err)
	require.Equal(t, "added-cam", id)

	select {
	case evt := <-events:
		require.Equal(t, event.TopicCameraAdded, evt.Topic)
		data, ok := evt.Data.(map[string]any)
		require.True(t, ok)
		require.Equal(t, "added-cam", data["camera_id"])
		require.Equal(t, "manual", data["source"])
	case <-time.After(5 * time.Second):
		t.Fatal("camera.added event never published")
	}
}

func TestUpdateCameraFieldMatrix(t *testing.T) {
	t.Parallel()
	cfg := testConfig()
	cfg.Cameras = []config.CameraConfig{
		{ID: "cam-h264", Name: "H264 Camera", Protocol: "rtsp", Encoding: "h264", URL: "rtsp://127.0.0.1:1/stream"},
	}
	mgr, _, _, _ := newTestManagerWithCfg(t, cfg)

	newName := "Renamed"
	audio := true
	updated, err := mgr.UpdateCamera(context.Background(), "cam-h264", CameraUpdate{
		Name:         &newName,
		AudioEnabled: &audio,
	})
	require.NoError(t, err)
	require.Equal(t, "Renamed", updated.Name)
	require.True(t, updated.AudioEnabled)

	got := mgr.GetCameraConfig("cam-h264")
	require.NotNil(t, got)
	require.Equal(t, "Renamed", got.Name)
	require.True(t, got.AudioEnabled)

	// Unknown camera → error.
	_, err = mgr.UpdateCamera(context.Background(), "ghost", CameraUpdate{Name: &newName})
	require.Error(t, err)
}

func TestArchiveCameraRemovesFromConfig(t *testing.T) {
	t.Parallel()
	cfg := testConfig()
	cfg.Cameras = []config.CameraConfig{
		{ID: "to-archive", Name: "Old", Protocol: "rtsp", Encoding: "h264", URL: "rtsp://127.0.0.1:1/stream"},
		{ID: "keep-me", Name: "Keep", Protocol: "rtsp", Encoding: "h264", URL: "rtsp://127.0.0.1:1/stream"},
	}
	mgr, _, db, _ := newTestManagerWithCfg(t, cfg)
	require.NoError(t, db.UpsertCamera(context.Background(), "to-archive", "Old", "rtsp", "h264", "", "", "", "", "", "", ""))

	require.NoError(t, mgr.ArchiveCamera(context.Background(), "to-archive"))
	require.Nil(t, mgr.GetCameraConfig("to-archive"), "archived camera leaves the active config")
	require.NotNil(t, mgr.GetCameraConfig("keep-me"))
}

// --- ForwardPTZ dispatch matrix ---

type motorStub struct {
	statusStub
	lastDir  string
	lastSpd  int
	failWith error
}

func (m *motorStub) MotorControl(direction string, speed int) error {
	m.lastDir, m.lastSpd = direction, speed
	return m.failWith
}

func TestForwardPTZ_XiaomiMotor(t *testing.T) {
	t.Parallel()
	cfg := testConfig()
	cfg.Cameras = []config.CameraConfig{
		{ID: "mi-cam", Name: "Mi", Protocol: "xiaomi", Encoding: "h264"},
	}
	mgr, _, _, _ := newTestManagerWithCfg(t, cfg)

	// Disconnected (stub without motor) → explicit error.
	mgr.SetTestRecorder("mi-cam", &statusStub{})
	require.Error(t, ForwardPTZ(context.Background(), mgr, nil, "mi-cam", "left", 5))

	motor := &motorStub{}
	mgr.SetTestRecorder("mi-cam", motor)

	// Direction with clamping (0 → 1, 200 → 100).
	require.NoError(t, ForwardPTZ(context.Background(), mgr, nil, "mi-cam", "left", 0))
	require.Equal(t, 1, motor.lastSpd)
	require.NoError(t, ForwardPTZ(context.Background(), mgr, nil, "mi-cam", "right", 200))
	require.Equal(t, 100, motor.lastSpd)

	// Stop passes through at speed 0.
	require.NoError(t, ForwardPTZ(context.Background(), mgr, nil, "mi-cam", "stop", 0))
	require.Equal(t, "stop", motor.lastDir)

	// Unsupported direction → error.
	require.Error(t, ForwardPTZ(context.Background(), mgr, nil, "mi-cam", "zoom-in", 5))

	// Motor failure surfaces.
	motor.failWith = context.Canceled
	require.ErrorIs(t, ForwardPTZ(context.Background(), mgr, nil, "mi-cam", "left", 5), context.Canceled)
}

func TestForwardPTZ_GBGuards(t *testing.T) {
	t.Parallel()
	cfg := testConfig()
	cfg.Cameras = []config.CameraConfig{
		{ID: "gb-nil", Protocol: "gb28181", GB28181: config.GB28181ChannelConfig{ChannelID: "ch-1"}},
		{ID: "gb-noch", Protocol: "gb28181", GB28181: config.GB28181ChannelConfig{}},
	}
	mgr, _, _, _ := newTestManagerWithCfg(t, cfg)

	// No GB sender wired.
	require.Error(t, ForwardPTZ(context.Background(), mgr, nil, "gb-nil", "up", 5))
	// No channel binding.
	require.Error(t, ForwardPTZ(context.Background(), mgr, func(string, string, byte) error { return nil }, "gb-noch", "up", 5))
	// Happy path routes to the sender with the bound channel.
	sent := ""
	require.NoError(t, ForwardPTZ(context.Background(), mgr, func(ch, dir string, _ byte) error {
		sent = ch + "/" + dir
		return nil
	}, "gb-nil", "down", 9))
	require.Equal(t, "ch-1/down", sent)
}

func TestPTZVectorMatrix(t *testing.T) {
	t.Parallel()
	// Zero speed falls back to the default magnitude.
	v := ptzVectorFor("up", 0)
	require.InDelta(t, 0.5, v.Tilt, 0.001)
	v = ptzVectorFor("down", 255)
	require.InDelta(t, -1.0, v.Tilt, 0.001)
	v = ptzVectorFor("up-left", 128)
	require.InDelta(t, -0.5, v.Pan, 0.01)
	require.InDelta(t, 0.5, v.Tilt, 0.01)
	v = ptzVectorFor("zoom-out", 128)
	require.InDelta(t, -0.5, v.Zoom, 0.01)
	require.Equal(t, onvif.PTZVector{}, ptzVectorFor("spin", 10), "unknown direction → zero vector")
}
