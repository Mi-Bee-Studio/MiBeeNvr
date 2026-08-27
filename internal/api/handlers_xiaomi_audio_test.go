package api

// Tests for handlers_xiaomi_audio.go — two-way audio REST guards +
// processAudioUpstreamMessage encode matrix (#578). Uses the same mock
// MISS client pattern as handlers_xiaomi_ptz_test.go: no camera, no
// network. The mock conn reports the CS2 protocol, which makes
// StartSpeaker fail with "requires TUTK transport" — that exercises the
// handler's error branch deterministically.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/camera"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/wsstream"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/xiaomi"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
)

// stubRecorder is any non-Xiaomi recorder: exercises the type-assert
// guard (400 "does not support two-way audio").
type stubRecorder struct{}

func (stubRecorder) Start(ctx context.Context) error { return nil }
func (stubRecorder) Stop() error                     { return nil }
func (stubRecorder) Status() model.RecorderStatus    { return "" }

func xiaomiAudioEnv(t *testing.T, rec model.Recorder, cameraID string) http.Handler {
	t.Helper()
	db, store := setupTestDB(t)
	t.Cleanup(func() { db.Close() })
	require.NoError(t, db.UpsertCamera(t.Context(), cameraID, "Cam", "xiaomi", "", "xiaomi://1", "", "", "", "", "", ""))

	cfg := &config.Config{
		Storage: config.StorageConfig{RootDir: store.RootDir(), SegmentDuration: "30s"},
		Cameras: []config.CameraConfig{},
	}
	camMgr := camera.NewCameraManager(cfg, store, db, "")
	if rec != nil {
		camMgr.SetTestRecorder(cameraID, rec)
	}
	h := NewHandler(db, store, noopAuthMW(), cfg, camMgr, nil, "", nil, nil, nil, nil, nil)
	return h.Routes()
}

func newXiaomiRecorderWithMock(t *testing.T) (*xiaomi.XiaomiRecorder, *testMISSConn) {
	t.Helper()
	db, store := setupTestDB(t)
	t.Cleanup(func() { db.Close() })
	rec := xiaomi.NewXiaomiRecorder(xiaomi.XiaomiRecorderConfig{CameraID: "xiaomi-cam", DID: "12345"}, store)
	conn := newTestMISSConn()
	conn.protocol = "tutk" // CS2 transport refuses two-way audio writes
	rec.SetMISSClientForTest(xiaomi.NewTestMISSClient(conn))
	return rec, conn
}

func TestTwoWayAudio_Guards(t *testing.T) {
	t.Parallel()

	// Unknown camera → 404.
	routes := xiaomiAudioEnv(t, nil, "ghost")
	rr := doRequest(t, routes, http.MethodPost, "/api/cameras/ghost/xiaomi/two-way-audio/start", nil, "", "")
	require.Equal(t, http.StatusNotFound, rr.Code)

	// Non-Xiaomi recorder → 400 on all three endpoints.
	routes = xiaomiAudioEnv(t, stubRecorder{}, "plain-cam")
	rr = doRequest(t, routes, http.MethodPost, "/api/cameras/plain-cam/xiaomi/two-way-audio/start", nil, "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)
	rr = doRequest(t, routes, http.MethodPost, "/api/cameras/plain-cam/xiaomi/two-way-audio/stop", nil, "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)
	req := httptest.NewRequest(http.MethodGet, "/api/ws/camera/plain-cam/audio-upstream", nil)
	w := httptest.NewRecorder()
	routes.ServeHTTP(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code)

	// Xiaomi recorder over the CS2 mock: start fails (TUTK-only) → 400,
	// and stop is a plain packet write → 200.
	db2, store2 := setupTestDB(t)
	t.Cleanup(func() { db2.Close() })
	rec := xiaomi.NewXiaomiRecorder(xiaomi.XiaomiRecorderConfig{CameraID: "xiaomi-cam", DID: "1"}, store2)
	rec.SetMISSClientForTest(xiaomi.NewTestMISSClient(newTestMISSConn())) // cs2
	routes = xiaomiAudioEnv(t, rec, "xiaomi-cam")
	rr = doRequest(t, routes, http.MethodPost, "/api/cameras/xiaomi-cam/xiaomi/two-way-audio/start", nil, "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)
	require.Contains(t, rr.Body.String(), "TUTK")

	// Stop succeeds over the mock (plain packet write) → 200.
	rr = doRequest(t, routes, http.MethodPost, "/api/cameras/xiaomi-cam/xiaomi/two-way-audio/stop", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	require.Contains(t, rr.Body.String(), "stopped")
}

func TestTwoWayAudio_StartWithoutClient(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	t.Cleanup(func() { db.Close() })
	require.NoError(t, db.UpsertCamera(t.Context(), "xiaomi-cam", "Cam", "xiaomi", "", "xiaomi://1", "", "", "", "", "", ""))

	cfg := &config.Config{Storage: config.StorageConfig{RootDir: store.RootDir()}, Cameras: []config.CameraConfig{}}
	camMgr := camera.NewCameraManager(cfg, store, db, "")
	// No SetMISSClientForTest → missClient nil → "not connected" → 400.
	rec := xiaomi.NewXiaomiRecorder(xiaomi.XiaomiRecorderConfig{CameraID: "xiaomi-cam", DID: "1"}, store)
	camMgr.SetTestRecorder("xiaomi-cam", rec)
	h := NewHandler(db, store, noopAuthMW(), cfg, camMgr, nil, "", nil, nil, nil, nil, nil)

	rr := doRequest(t, h.Routes(), http.MethodPost, "/api/cameras/xiaomi-cam/xiaomi/two-way-audio/start", nil, "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)
	require.Contains(t, rr.Body.String(), "not connected")

	rr = doRequest(t, h.Routes(), http.MethodPost, "/api/cameras/xiaomi-cam/xiaomi/two-way-audio/stop", nil, "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestProcessAudioUpstreamMessage(t *testing.T) {
	t.Parallel()

	pcmMsg := make([]byte, 641) // reserved byte + 640 PCM bytes
	for i := range pcmMsg {
		pcmMsg[i] = byte(i % 251)
	}

	t.Run("undersized frame is skipped", func(t *testing.T) {
		t.Parallel()
		rec, _ := newXiaomiRecorderWithMock(t)
		require.NoError(t, processAudioUpstreamMessage(rec, missCodecPCMU, pcmMsg[:100]))
	})

	t.Run("pcm passthrough", func(t *testing.T) {
		t.Parallel()
		rec, _ := newXiaomiRecorderWithMock(t)
		require.NoError(t, processAudioUpstreamMessage(rec, missCodecPCM, pcmMsg))
	})

	t.Run("g711 encode", func(t *testing.T) {
		t.Parallel()
		rec, _ := newXiaomiRecorderWithMock(t)
		require.NoError(t, processAudioUpstreamMessage(rec, missCodecPCMU, pcmMsg))
		require.NoError(t, processAudioUpstreamMessage(rec, missCodecPCMA, pcmMsg))
	})

	t.Run("unsupported codec is skipped", func(t *testing.T) {
		t.Parallel()
		rec, _ := newXiaomiRecorderWithMock(t)
		require.NoError(t, processAudioUpstreamMessage(rec, 9999, pcmMsg))
	})

	t.Run("nil client surfaces write error", func(t *testing.T) {
		t.Parallel()
		db, store := setupTestDB(t)
		t.Cleanup(func() { db.Close() })
		rec := xiaomi.NewXiaomiRecorder(xiaomi.XiaomiRecorderConfig{CameraID: "c", DID: "1"}, store)
		require.Error(t, processAudioUpstreamMessage(rec, missCodecPCM, pcmMsg))
	})
}

func TestAudioUpstreamWS_RoundTrip(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	t.Cleanup(func() { db.Close() })
	require.NoError(t, db.UpsertCamera(t.Context(), "xiaomi-cam", "Cam", "xiaomi", "", "xiaomi://1", "", "", "", "", "", ""))

	cfg := &config.Config{Storage: config.StorageConfig{RootDir: store.RootDir()}, Cameras: []config.CameraConfig{}}
	camMgr := camera.NewCameraManager(cfg, store, db, "")
	rec := xiaomi.NewXiaomiRecorder(xiaomi.XiaomiRecorderConfig{CameraID: "xiaomi-cam", DID: "1"}, store)
	conn := newTestMISSConn()
	conn.protocol = "tutk" // CS2 transport refuses two-way audio
	rec.SetMISSClientForTest(xiaomi.NewTestMISSClient(conn))
	camMgr.SetTestRecorder("xiaomi-cam", rec)

	h := NewHandler(db, store, noopAuthMW(), cfg, camMgr, nil, "", nil, nil, nil, nil, nil)
	h.SetWSManager(wsstream.NewManager())

	srv := httptest.NewServer(h.Routes())
	defer srv.Close()
	url := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/ws/camera/xiaomi-cam/audio-upstream"

	ws, _, err := websocket.DefaultDialer.Dial(url, nil)
	require.NoError(t, err)

	msg := make([]byte, 641)
	for i := range msg {
		msg[i] = byte(i % 251)
	}
	require.NoError(t, ws.WriteMessage(websocket.BinaryMessage, msg))
	// The server loops on read until the client goes away — closing here
	// ends that loop; no server→client message is ever expected.
	require.NoError(t, ws.Close())

	// The PCM message must have been written to the camera: SpeakerCodec
	// defaults to 0 → handler substitutes PCM → WriteAudio → WritePacket.
	require.Eventually(t, func() bool { return conn.packetWrites.Load() > 0 },
		5*time.Second, 50*time.Millisecond, "audio packet never reached the mock camera")
}
