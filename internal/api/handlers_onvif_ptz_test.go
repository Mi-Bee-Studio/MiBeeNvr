package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/camera"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/onvif"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/xiaomi"
	"github.com/mickeyzzc/gb28181-go/platform"
	"github.com/stretchr/testify/require"
)

// --- Pure vector→protocol mappers ---

func TestXiaomiPTZFromVector(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name            string
		pan, tilt, zoom float64
		wantDir         string
		wantSpeed       int
		wantErr         bool
	}{
		{"pan left", -0.5, 0, 0, "left", 5, false},
		{"pan right fast", 1.0, 0, 0, "right", 10, false},
		{"pan right slow", 0.25, 0, 0, "right", 3, false},
		{"tiny magnitude clamps to 1", -0.05, 0, 0, "left", 1, false},
		{"tilt up", 0, 0.6, 0, "up", 6, false},
		{"tilt down", 0, -0.4, 0, "down", 4, false},
		{"tilt dominates pan", 0.2, 0.9, 0, "up", 9, false},
		{"zoom rejected", 0, 0, 0.5, "", 0, true},
		{"zero vector stops", 0, 0, 0, "stop", 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir, speed, err := xiaomiPTZFromVector(tc.pan, tc.tilt, tc.zoom)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.wantDir, dir)
			require.Equal(t, tc.wantSpeed, speed)
		})
	}
}

func TestGBPTZFromVector(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name            string
		pan, tilt, zoom float64
		wantDir         string
		wantSpeed       byte
	}{
		{"zoom in mid", 0, 0, 0.5, platform.DirZoomIn, 128},
		{"zoom out full", 0, 0, -1.0, platform.DirZoomOut, 255},
		{"pan left full", -1.0, 0, 0, platform.DirLeft, 255},
		{"tilt up quarter", 0, 0.25, 0, platform.DirUp, 64},
		{"tilt down dominates pan", 0.5, -0.9, 0, platform.DirDown, 230},
		{"zoom dominates tilt", 0, 0.9, -0.3, platform.DirZoomOut, 77},
		{"zero vector stops", 0, 0, 0, platform.DirStop, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir, speed := gbPTZFromVector(tc.pan, tc.tilt, tc.zoom)
			require.Equal(t, tc.wantDir, dir)
			require.Equal(t, tc.wantSpeed, speed)
		})
	}
}

// --- Unified /ptz endpoints routing for Xiaomi cameras ---

// newXiaomiPTZTestHandler builds a handler with one Xiaomi camera backed by a
// mock MISS connection, mirroring handlers_xiaomi_ptz_test.go's harness.
func newXiaomiPTZTestHandler(t *testing.T) (*Handler, *testMISSConn) {
	t.Helper()
	db, store := setupTestDB(t)
	ctx := t.Context()
	require.NoError(t, db.UpsertCamera(ctx, "xiaomi-cam", "Xiaomi Camera", "xiaomi", "", "xiaomi://12345", "", "", "", "", "", ""))

	cfg := &config.Config{
		Storage: config.StorageConfig{RootDir: store.RootDir(), SegmentDuration: "30s"},
		Cleanup: config.CleanupConfig{RetentionDays: 30, CheckInterval: "1h"},
		Cameras: []config.CameraConfig{},
	}
	camMgr := camera.NewCameraManager(cfg, store, db, "")
	h := NewHandler(db, store, noopAuthMW(), cfg, camMgr, nil, "", nil, nil, nil, nil, nil, nil)

	rec := xiaomi.NewXiaomiRecorder(xiaomi.XiaomiRecorderConfig{
		CameraID: "xiaomi-cam",
		DID:      "12345",
	}, store)
	mockConn := newTestMISSConn()
	rec.SetMISSClientForTest(xiaomi.NewTestMISSClient(mockConn))
	camMgr.SetTestRecorder("xiaomi-cam", rec)
	return h, mockConn
}

func postPTZ(t *testing.T, h *Handler, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)
	return w
}

func TestPTZMove_XiaomiUnifiedRoute(t *testing.T) {
	t.Parallel()
	h, mockConn := newXiaomiPTZTestHandler(t)

	w := postPTZ(t, h, "/api/cameras/xiaomi-cam/ptz/move", `{"mode":"continuous","pan":-0.5,"tilt":0,"zoom":0}`)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var resp map[string]string
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	require.Equal(t, "ok", resp["status"])
	require.True(t, mockConn.writeCalled.Load(), "motor command should reach the MISS wire")
}

func TestPTZMove_XiaomiZoomUnsupported(t *testing.T) {
	t.Parallel()
	h, _ := newXiaomiPTZTestHandler(t)

	w := postPTZ(t, h, "/api/cameras/xiaomi-cam/ptz/move", `{"mode":"continuous","pan":0,"tilt":0,"zoom":0.5}`)
	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Contains(t, w.Body.String(), "zoom")
}

func TestPTZStop_XiaomiUnifiedRoute(t *testing.T) {
	t.Parallel()
	h, mockConn := newXiaomiPTZTestHandler(t)

	w := postPTZ(t, h, "/api/cameras/xiaomi-cam/ptz/stop", "")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.True(t, mockConn.writeCalled.Load(), "stop command should reach the MISS wire")
}

func TestPTZMove_XiaomiNotConnected(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	ctx := t.Context()
	require.NoError(t, db.UpsertCamera(ctx, "xiaomi-cam", "Xiaomi Camera", "xiaomi", "", "xiaomi://12345", "", "", "", "", "", ""))
	cfg := &config.Config{
		Storage: config.StorageConfig{RootDir: store.RootDir(), SegmentDuration: "30s"},
		Cleanup: config.CleanupConfig{RetentionDays: 30, CheckInterval: "1h"},
	}
	camMgr := camera.NewCameraManager(cfg, store, db, "")
	h := NewHandler(db, store, noopAuthMW(), cfg, camMgr, nil, "", nil, nil, nil, nil, nil, nil)

	w := postPTZ(t, h, "/api/cameras/xiaomi-cam/ptz/move", `{"mode":"continuous","pan":0.5,"tilt":0,"zoom":0}`)
	require.Equal(t, http.StatusServiceUnavailable, w.Code, w.Body.String())
}

// --- PTZ suppression windows ---

func TestPTZSuppressWindow_MoveVsStop(t *testing.T) {
	t.Parallel()
	h, _ := newXiaomiPTZTestHandler(t)

	type suppressCall struct {
		cameraID string
		window   time.Duration
	}
	calls := make(chan suppressCall, 4)
	h.SetPTZSuppressor(func(cameraID string, window time.Duration) {
		select {
		case calls <- suppressCall{cameraID, window}:
		default:
		}
	})

	w := postPTZ(t, h, "/api/cameras/xiaomi-cam/ptz/move", `{"mode":"continuous","pan":-0.5,"tilt":0,"zoom":0}`)
	require.Equal(t, http.StatusOK, w.Code)

	select {
	case c := <-calls:
		require.Equal(t, "xiaomi-cam", c.cameraID)
		require.Equal(t, ptzSuppressWindowMove, c.window)
		require.Greater(t, c.window, ptzSuppressWindowShort, "move must suppress longer than stop (long-press guard)")
	case <-time.After(2 * time.Second):
		t.Fatal("move did not fire the PTZ suppressor")
	}

	w = postPTZ(t, h, "/api/cameras/xiaomi-cam/ptz/stop", "")
	require.Equal(t, http.StatusOK, w.Code)

	select {
	case c := <-calls:
		require.Equal(t, "xiaomi-cam", c.cameraID)
		require.Equal(t, ptzSuppressWindowShort, c.window)
	case <-time.After(2 * time.Second):
		t.Fatal("stop did not fire the PTZ suppressor")
	}
}

// --- Unified /ptz endpoints for ONVIF cameras (mocked controller) ---

// newONVIFPTZTestHandler builds a handler with one ONVIF camera whose PTZ
// controller is a recording mock (no SOAP server needed).
func newONVIFPTZTestHandler(t *testing.T) (*Handler, *onvif.MockPTZController) {
	t.Helper()
	db, store := setupTestDB(t)
	ctx := t.Context()
	require.NoError(t, db.UpsertCamera(ctx, "onvif-cam", "ONVIF Camera", "onvif", "", "onvif://sn-1", "", "", "", "", "", ""))

	cfg := &config.Config{
		Storage: config.StorageConfig{RootDir: store.RootDir(), SegmentDuration: "30s"},
		Cleanup: config.CleanupConfig{RetentionDays: 30, CheckInterval: "1h"},
		Cameras: []config.CameraConfig{},
	}
	camMgr := camera.NewCameraManager(cfg, store, db, "")
	h := NewHandler(db, store, noopAuthMW(), cfg, camMgr, nil, "", nil, nil, nil, nil, nil, nil)

	mock := &onvif.MockPTZController{
		Position: onvif.PTZVector{Pan: -0.25, Tilt: 0.5, Zoom: 0.1},
		Moving:   true,
	}
	camMgr.SetTestONVIFPTZController("onvif-cam", mock)
	return h, mock
}

func TestPTZStatus_ONVIF(t *testing.T) {
	t.Parallel()
	h, mock := newONVIFPTZTestHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/api/cameras/onvif-cam/ptz/status", nil)
	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var resp struct {
		Moving bool    `json:"moving"`
		Pan    float64 `json:"pan"`
		Tilt   float64 `json:"tilt"`
		Zoom   float64 `json:"zoom"`
	}
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	require.True(t, resp.Moving)
	require.InDelta(t, -0.25, resp.Pan, 1e-9)
	require.InDelta(t, 0.5, resp.Tilt, 1e-9)
	require.InDelta(t, 0.1, resp.Zoom, 1e-9)
	require.Equal(t, 1, mock.GetStatusCalls)
}

func TestPTZStop_ONVIF(t *testing.T) {
	t.Parallel()
	h, mock := newONVIFPTZTestHandler(t)

	w := postPTZ(t, h, "/api/cameras/onvif-cam/ptz/stop", "")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, 1, mock.StopCalls)
}

func TestPTZMove_ONVIF(t *testing.T) {
	t.Parallel()
	h, mock := newONVIFPTZTestHandler(t)

	w := postPTZ(t, h, "/api/cameras/onvif-cam/ptz/move", `{"mode":"continuous","pan":0.25,"tilt":0,"zoom":0}`)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, 1, mock.ContinuousMoveCalls)
	require.Len(t, mock.MoveHistory, 1)
	require.InDelta(t, 0.25, mock.MoveHistory[0].Pan, 1e-9)
}
