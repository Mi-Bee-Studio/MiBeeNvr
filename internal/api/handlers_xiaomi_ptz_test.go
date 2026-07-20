package api

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/camera"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/xiaomi"
	"github.com/stretchr/testify/require"
)

func TestXiaomiPTZMove(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	ctx := t.Context()
	require.NoError(t, db.UpsertCamera(ctx, "xiaomi-cam", "Xiaomi Camera", "xiaomi", "", "xiaomi://12345", "", "", "", "", "", ""))

	cfg := &config.Config{
		Storage: config.StorageConfig{RootDir: store.RootDir(), SegmentDuration: "30s"},
		Cleanup: config.CleanupConfig{RetentionDays: 30, CheckInterval: "1h", DiskThresholdPercent: 95},
		Cameras: []config.CameraConfig{},
	}
	camMgr := camera.NewCameraManager(cfg, store, db, "")
	h := NewHandler(db, store, noopAuthMW(), cfg, camMgr, nil, "", nil, nil, nil)

	rec := xiaomi.NewXiaomiRecorder(xiaomi.XiaomiRecorderConfig{
		CameraID: "xiaomi-cam",
		DID:      "12345",
	}, store)
	mockConn := newTestMISSConn()
	mockClient := xiaomi.NewTestMISSClient(mockConn)
	rec.SetMISSClientForTest(mockClient)
	camMgr.SetTestRecorder("xiaomi-cam", rec)

	body := `{"direction":"left","speed":5}`
	req := httptest.NewRequest(http.MethodPost, "/api/cameras/xiaomi-cam/xiaomi/ptz/move", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var resp map[string]string
	err := json.NewDecoder(w.Body).Decode(&resp)
	require.NoError(t, err)
	require.Equal(t, "ok", resp["status"])
	require.True(t, mockConn.writeCalled, "WriteCommand should have been called")
}

func TestXiaomiPTZStop(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	ctx := t.Context()
	require.NoError(t, db.UpsertCamera(ctx, "xiaomi-cam", "Xiaomi Camera", "xiaomi", "", "xiaomi://12345", "", "", "", "", "", ""))

	cfg := &config.Config{
		Storage: config.StorageConfig{RootDir: store.RootDir(), SegmentDuration: "30s"},
		Cleanup: config.CleanupConfig{RetentionDays: 30, CheckInterval: "1h", DiskThresholdPercent: 95},
		Cameras: []config.CameraConfig{},
	}
	camMgr := camera.NewCameraManager(cfg, store, db, "")
	h := NewHandler(db, store, noopAuthMW(), cfg, camMgr, nil, "", nil, nil, nil)

	rec := xiaomi.NewXiaomiRecorder(xiaomi.XiaomiRecorderConfig{
		CameraID: "xiaomi-cam",
		DID:      "12345",
	}, store)
	mockConn := newTestMISSConn()
	mockClient := xiaomi.NewTestMISSClient(mockConn)
	rec.SetMISSClientForTest(mockClient)
	camMgr.SetTestRecorder("xiaomi-cam", rec)

	req := httptest.NewRequest(http.MethodPost, "/api/cameras/xiaomi-cam/xiaomi/ptz/stop", nil)

	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var resp map[string]string
	err := json.NewDecoder(w.Body).Decode(&resp)
	require.NoError(t, err)
	require.Equal(t, "ok", resp["status"])
	require.True(t, mockConn.writeCalled, "WriteCommand should have been called")
}

func TestXiaomiDeviceInfo(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	ctx := t.Context()
	require.NoError(t, db.UpsertCamera(ctx, "xiaomi-cam", "Xiaomi Camera", "xiaomi", "", "xiaomi://12345", "", "", "", "", "", ""))

	cfg := &config.Config{
		Storage: config.StorageConfig{RootDir: store.RootDir(), SegmentDuration: "30s"},
		Cleanup: config.CleanupConfig{RetentionDays: 30, CheckInterval: "1h", DiskThresholdPercent: 95},
		Cameras: []config.CameraConfig{},
	}
	camMgr := camera.NewCameraManager(cfg, store, db, "")
	h := NewHandler(db, store, noopAuthMW(), cfg, camMgr, nil, "", nil, nil, nil)

	rec := xiaomi.NewXiaomiRecorder(xiaomi.XiaomiRecorderConfig{
		CameraID: "xiaomi-cam",
		DID:      "12345",
	}, store)
	mockConn := newTestMISSConn()
	key := make([]byte, 32)
	// Encrypt mock device info response with the zero key.
	plaintext := []byte(`{"firmware_version":"1.2.3","hardware_version":"v2","result":"ok"}`)
	encrypted, err := xiaomi.Encode(plaintext, key)
	require.NoError(t, err)
	mockConn.readCmdData = encrypted
	mockClient := xiaomi.NewTestMISSClient(mockConn)
	rec.SetMISSClientForTest(mockClient)
	camMgr.SetTestRecorder("xiaomi-cam", rec)

	req := httptest.NewRequest(http.MethodGet, "/api/cameras/xiaomi-cam/xiaomi/device-info", nil)

	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var resp map[string]interface{}
	err = json.NewDecoder(w.Body).Decode(&resp)
	require.NoError(t, err)
	require.Equal(t, "1.2.3", resp["firmware_version"])
	require.Equal(t, "v2", resp["hardware_version"])
}

func TestXiaomiPTZNonXiaomi(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	ctx := t.Context()
	require.NoError(t, db.UpsertCamera(ctx, "rtsp-cam", "RTSP Camera", "rtsp_h264", "", "rtsp://host/stream", "", "", "", "", "", ""))

	h := TestHandler(db, store)
	body := `{"direction":"left","speed":5}`
	req := httptest.NewRequest(http.MethodPost, "/api/cameras/rtsp-cam/xiaomi/ptz/move", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
}

func TestXiaomiDeviceInfoNotConnected(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	ctx := t.Context()
	require.NoError(t, db.UpsertCamera(ctx, "xiaomi-cam", "Xiaomi Camera", "xiaomi", "", "xiaomi://12345", "", "", "", "", "", ""))

	cfg := &config.Config{
		Storage: config.StorageConfig{RootDir: store.RootDir(), SegmentDuration: "30s"},
		Cleanup: config.CleanupConfig{RetentionDays: 30, CheckInterval: "1h", DiskThresholdPercent: 95},
		Cameras: []config.CameraConfig{},
	}
	camMgr := camera.NewCameraManager(cfg, store, db, "")
	h := NewHandler(db, store, noopAuthMW(), cfg, camMgr, nil, "", nil, nil, nil)

	rec := xiaomi.NewXiaomiRecorder(xiaomi.XiaomiRecorderConfig{
		CameraID: "xiaomi-cam",
		DID:      "12345",
	}, store)
	camMgr.SetTestRecorder("xiaomi-cam", rec)

	req := httptest.NewRequest(http.MethodGet, "/api/cameras/xiaomi-cam/xiaomi/device-info", nil)

	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)

	require.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestXiaomiPTZCameraNotFound(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)

	h := TestHandler(db, store)
	body := `{"direction":"left","speed":5}`
	req := httptest.NewRequest(http.MethodPost, "/api/cameras/nonexistent/xiaomi/ptz/move", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)

	require.Equal(t, http.StatusNotFound, w.Code)
}

func TestXiaomiPTZInvalidInput(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	ctx := t.Context()
	require.NoError(t, db.UpsertCamera(ctx, "xiaomi-cam", "Xiaomi Camera", "xiaomi", "", "xiaomi://12345", "", "", "", "", "", ""))

	h := TestHandler(db, store)

	// Empty direction
	body := `{"direction":"","speed":5}`
	req := httptest.NewRequest(http.MethodPost, "/api/cameras/xiaomi-cam/xiaomi/ptz/move", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code)

	// Speed too low
	body = `{"direction":"left","speed":0}`
	req = httptest.NewRequest(http.MethodPost, "/api/cameras/xiaomi-cam/xiaomi/ptz/move", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code)

	// Speed too high
	body = `{"direction":"left","speed":101}`
	req = httptest.NewRequest(http.MethodPost, "/api/cameras/xiaomi-cam/xiaomi/ptz/move", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code)

	// Invalid JSON
	body = `not-json`
	req = httptest.NewRequest(http.MethodPost, "/api/cameras/xiaomi-cam/xiaomi/ptz/move", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code)
}

// testMISSConn implements xiaomi.MISSConn for testing MotorControl and GetDeviceInfo.
type testMISSConn struct {
	writeCalled bool
	readCmdData []byte // data returned by ReadCommand for GetDeviceInfo
}

func newTestMISSConn() *testMISSConn {
	return &testMISSConn{}
}

func (m *testMISSConn) Protocol() string                     { return "cs2" }
func (m *testMISSConn) Version() string                      { return "test" }
func (m *testMISSConn) ReadCommand() (uint32, []byte, error) { return 0, m.readCmdData, nil }
func (m *testMISSConn) WriteCommand(cmd uint32, data []byte) error {
	m.writeCalled = true
	_ = cmd
	_ = data
	return nil
}
func (m *testMISSConn) ReadPacket() ([]byte, []byte, error)   { return nil, nil, nil }
func (m *testMISSConn) WritePacket(hdr, payload []byte) error { return nil }
func (m *testMISSConn) RemoteAddr() net.Addr                  { return &testMISSAddr{} }
func (m *testMISSConn) SetDeadline(t time.Time) error         { return nil }
func (m *testMISSConn) Close() error                          { return nil }

// testMISSAddr implements net.Addr for the mock MISS conn.
type testMISSAddr struct{}

func (a *testMISSAddr) Network() string { return "tcp" }
func (a *testMISSAddr) String() string  { return "127.0.0.1:1234" }
