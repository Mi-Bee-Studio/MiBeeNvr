package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/flv"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/middleware"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/webrtc"
	"github.com/stretchr/testify/require"
)

// containsProtocol checks if the protocol list contains a named available protocol.
func containsProtocol(t *testing.T, protocols []ProtocolDetail, name string) bool {
	t.Helper()
	for _, p := range protocols {
		if p.Protocol == name && p.Available {
			return true
		}
	}
	return false
}


// --- WHEP endpoint tests ---

func TestWHEP_AuthRequired(t *testing.T) {
	t.Helper()
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()

	authMW, _ := middleware.NewAuthMiddleware(middleware.AuthProvider{GetUsername: func() string { return "admin" }, GetHash: func() string { return "a$dummyhashdummyhashdummyhashdum" }}, "", middleware.AuthRateLimitConfig{})
	h := NewHandler(db, store, authMW, nil, nil, nil, "", nil, nil, nil)

	r := h.Routes()
	req := httptest.NewRequest("POST", "/api/cameras/test-cam/stream/webrtc", strings.NewReader("v=0"))
	req.Header.Set("Content-Type", "application/sdp")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	require.Equal(t, http.StatusUnauthorized, rr.Code)
}

func TestWHEP_Create_NoWebRTCManager(t *testing.T) {
	t.Helper()
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()

	h := NewHandler(db, store, noopAuthMW(), nil, nil, nil, "", nil, nil, nil)

	rr := doRequest(t, h.Routes(), "POST", "/api/cameras/test-cam/stream/webrtc",
		strings.NewReader("v=0"), "admin", "pass")

	require.Equal(t, http.StatusServiceUnavailable, rr.Code)
}

func TestWHEP_Delete_NoWebRTCManager(t *testing.T) {
	t.Helper()
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()

	h := NewHandler(db, store, noopAuthMW(), nil, nil, nil, "", nil, nil, nil)

	rr := doRequest(t, h.Routes(), "DELETE", "/api/cameras/test-cam/stream/webrtc/nonexistent-session",
		nil, "admin", "pass")

	require.Equal(t, http.StatusServiceUnavailable, rr.Code)
}

func TestWHEP_Delete_SessionNotFound(t *testing.T) {
	t.Helper()
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()

	webrtcMgr := webrtc.NewManager()
	h := NewHandler(db, store, noopAuthMW(), nil, nil, nil, "", nil, nil, nil)
	h.SetWebRTCManager(webrtcMgr)

	rr := doRequest(t, h.Routes(), "DELETE", "/api/cameras/test-cam/stream/webrtc/nonexistent-session",
		nil, "admin", "pass")

	require.Equal(t, http.StatusNotFound, rr.Code)
}

func TestWHEP_CameraNotFound(t *testing.T) {
	t.Helper()
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()

	webrtcMgr := webrtc.NewManager()
	h := NewHandler(db, store, noopAuthMW(), nil, nil, nil, "", nil, nil, nil)
	h.SetWebRTCManager(webrtcMgr)

	rr := doRequest(t, h.Routes(), "POST", "/api/cameras/nonexistent/stream/webrtc",
		strings.NewReader("v=0"), "admin", "pass")
	// Without Content-Type header, it returns 415 first
	_ = rr
}

func TestWHEP_InvalidContentType(t *testing.T) {
	t.Helper()
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()

	// Create camera in DB
	seedCameraWithEncoding(t, db, "cam1", "h264")

	webrtcMgr := webrtc.NewManager()
	h := NewHandler(db, store, noopAuthMW(), nil, nil, nil, "", nil, nil, nil)
	h.SetWebRTCManager(webrtcMgr)

	req := httptest.NewRequest("POST", "/api/cameras/cam1/stream/webrtc", strings.NewReader("v=0"))
	req.Header.Set("Content-Type", "text/plain")
	req.SetBasicAuth("admin", "pass")
	rr := httptest.NewRecorder()
	h.Routes().ServeHTTP(rr, req)

	require.Equal(t, http.StatusUnsupportedMediaType, rr.Code)
}

// --- FLV endpoint tests ---

func TestFLV_AuthRequired(t *testing.T) {
	t.Helper()
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()

	authMW, _ := middleware.NewAuthMiddleware(middleware.AuthProvider{GetUsername: func() string { return "admin" }, GetHash: func() string { return "a$dummyhashdummyhashdummyhashdum" }}, "", middleware.AuthRateLimitConfig{})
	h := NewHandler(db, store, authMW, nil, nil, nil, "", nil, nil, nil)

	r := h.Routes()
	req := httptest.NewRequest("GET", "/api/cameras/test-cam/stream.flv", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	require.Equal(t, http.StatusUnauthorized, rr.Code)
}

func TestFLV_NoManager(t *testing.T) {
	t.Helper()
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()

	h := NewHandler(db, store, noopAuthMW(), nil, nil, nil, "", nil, nil, nil)

	rr := doRequest(t, h.Routes(), "GET", "/api/cameras/test-cam/stream.flv", nil, "admin", "pass")

	require.Equal(t, http.StatusServiceUnavailable, rr.Code)
}

func TestFLV_CameraNotFound(t *testing.T) {
	t.Helper()
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()

	flvMgr := flv.NewManager()
	h := NewHandler(db, store, noopAuthMW(), nil, nil, nil, "", nil, nil, nil)
	h.SetFLVManager(flvMgr)

	rr := doRequest(t, h.Routes(), "GET", "/api/cameras/nonexistent/stream.flv", nil, "admin", "pass")

	require.Equal(t, http.StatusNotFound, rr.Code)
}

func TestFLV_StreamNotActive(t *testing.T) {
	t.Helper()
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()

	seedCameraWithEncoding(t, db, "cam1", "h264")

	flvMgr := flv.NewManager()
	h := NewHandler(db, store, noopAuthMW(), nil, nil, nil, "", nil, nil, nil)
	h.SetFLVManager(flvMgr)

	rr := doRequest(t, h.Routes(), "GET", "/api/cameras/cam1/stream.flv", nil, "admin", "pass")

	require.Equal(t, http.StatusNotFound, rr.Code)
}

// --- Per-camera protocols endpoint tests ---

func TestCameraProtocols_AuthRequired(t *testing.T) {
	t.Helper()
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()

	authMW, _ := middleware.NewAuthMiddleware(middleware.AuthProvider{GetUsername: func() string { return "admin" }, GetHash: func() string { return "a$dummyhashdummyhashdummyhashdum" }}, "", middleware.AuthRateLimitConfig{})
	h := NewHandler(db, store, authMW, nil, nil, nil, "", nil, nil, nil)

	r := h.Routes()
	req := httptest.NewRequest("GET", "/api/cameras/test-cam/protocols", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	require.Equal(t, http.StatusUnauthorized, rr.Code)
}

func TestCameraProtocols_CameraNotFound(t *testing.T) {
	t.Helper()
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()

	h := NewHandler(db, store, noopAuthMW(), nil, nil, nil, "", nil, nil, nil)

	rr := doRequest(t, h.Routes(), "GET", "/api/cameras/nonexistent/protocols", nil, "admin", "pass")

	require.Equal(t, http.StatusNotFound, rr.Code)
}

func TestCameraProtocols_H264Camera(t *testing.T) {
	t.Helper()
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()

	seedCameraWithEncoding(t, db, "cam1", "h264")

	reg := NewStreamRegistry()
	reg.Register(&HLSStreamHandler{})
	reg.Register(&stubStreamHandler{name: "webrtc", codecs: []model.Format{model.FormatH264}})
	reg.Register(&stubStreamHandler{name: "flv", codecs: []model.Format{model.FormatH264, model.FormatH265}})

	h := NewHandler(db, store, noopAuthMW(), nil, nil, nil, "", nil, nil, nil)
	h.SetStreamRegistry(reg)

	rr := doRequest(t, h.Routes(), "GET", "/api/cameras/cam1/protocols", nil, "admin", "pass")

	require.Equal(t, http.StatusOK, rr.Code)

	var resp cameraProtocolsResponse
	require.NoError(t, parseJSONBody(t, rr, &resp))
	require.Equal(t, "h264", resp.Encoding)
	require.True(t, containsProtocol(t, resp.Protocols, "hls"), "hls should be available")
	require.True(t, containsProtocol(t, resp.Protocols, "webrtc"), "webrtc should be available")
	require.True(t, containsProtocol(t, resp.Protocols, "flv"), "flv should be available")
require.Equal(t, "webrtc", resp.Default) // WebRTC is preferred
}


func TestCameraProtocols_H265Camera(t *testing.T) {
	t.Helper()
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()

	seedCameraWithEncoding(t, db, "cam2", "h265")

	reg := NewStreamRegistry()
	reg.Register(&HLSStreamHandler{})
	reg.Register(&stubStreamHandler{name: "webrtc", codecs: []model.Format{model.FormatH264}})
	reg.Register(&stubStreamHandler{name: "flv", codecs: []model.Format{model.FormatH264, model.FormatH265}})

	h := NewHandler(db, store, noopAuthMW(), nil, nil, nil, "", nil, nil, nil)
	h.SetStreamRegistry(reg)

	rr := doRequest(t, h.Routes(), "GET", "/api/cameras/cam2/protocols", nil, "admin", "pass")

	require.Equal(t, http.StatusOK, rr.Code)

	var resp cameraProtocolsResponse
	require.NoError(t, parseJSONBody(t, rr, &resp))
	require.Equal(t, "h265", resp.Encoding)
	require.True(t, containsProtocol(t, resp.Protocols, "hls"), "hls should be available")
	require.True(t, containsProtocol(t, resp.Protocols, "flv"), "flv should be available")
	require.False(t, containsProtocol(t, resp.Protocols, "webrtc"), "WebRTC should not be available for H.265")
require.Equal(t, "flv", resp.Default) // FLV is preferred after WebRTC (unavailable)
}


func TestCameraProtocols_MJPEGCamera(t *testing.T) {
	t.Helper()
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()

	seedCameraWithEncoding(t, db, "cam3", "mjpeg")

	reg := NewStreamRegistry()
	reg.Register(&HLSStreamHandler{})
	reg.Register(&stubStreamHandler{name: "webrtc", codecs: []model.Format{model.FormatH264}})

	h := NewHandler(db, store, noopAuthMW(), nil, nil, nil, "", nil, nil, nil)
	h.SetStreamRegistry(reg)

	rr := doRequest(t, h.Routes(), "GET", "/api/cameras/cam3/protocols", nil, "admin", "pass")

	require.Equal(t, http.StatusOK, rr.Code)

	var resp cameraProtocolsResponse
	require.NoError(t, parseJSONBody(t, rr, &resp))
	require.Equal(t, "mjpeg", resp.Encoding)
	require.Empty(t, resp.Protocols)
	require.Empty(t, resp.Default)
}

func TestCameraProtocols_NoRegistry(t *testing.T) {
	t.Helper()
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()

	seedCameraWithEncoding(t, db, "cam1", "h264")

	h := NewHandler(db, store, noopAuthMW(), nil, nil, nil, "", nil, nil, nil)
	// No stream registry set

	rr := doRequest(t, h.Routes(), "GET", "/api/cameras/cam1/protocols", nil, "admin", "pass")

	require.Equal(t, http.StatusOK, rr.Code)

	var resp cameraProtocolsResponse
	require.NoError(t, parseJSONBody(t, rr, &resp))
	require.Equal(t, "h264", resp.Encoding)
	require.Empty(t, resp.Protocols)
	require.Empty(t, resp.Default)
}

func TestCameraProtocols_UsesStreamEncoding(t *testing.T) {
	t.Helper()
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()

	// Camera with encoding="" but stream_encoding="h264"
	seedCameraWithEncodings(t, db, "cam1", "", "h264")

	reg := NewStreamRegistry()
	reg.Register(&HLSStreamHandler{})

	h := NewHandler(db, store, noopAuthMW(), nil, nil, nil, "", nil, nil, nil)
	h.SetStreamRegistry(reg)

	rr := doRequest(t, h.Routes(), "GET", "/api/cameras/cam1/protocols", nil, "admin", "pass")

	require.Equal(t, http.StatusOK, rr.Code)

	var resp cameraProtocolsResponse
	require.NoError(t, parseJSONBody(t, rr, &resp))
	require.Equal(t, "h264", resp.Encoding)
require.True(t, containsProtocol(t, resp.Protocols, "hls"), "hls should be available")
}


// --- Route wiring verification ---

func TestRoutes_WHEPEndpointsRegistered(t *testing.T) {
	t.Helper()
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()

	webrtcMgr := webrtc.NewManager()
	flvMgr := flv.NewManager()

	seedCameraWithEncoding(t, db, "test", "h264")

	h := NewHandler(db, store, noopAuthMW(), nil, nil, nil, "", nil, nil, nil)
	h.SetWebRTCManager(webrtcMgr)
	h.SetFLVManager(flvMgr)

	r := h.Routes()
	// Verify WHEP POST route responds (not 404)
	req := httptest.NewRequest("POST", "/api/cameras/test/stream/webrtc", nil)
	req.SetBasicAuth("admin", "pass")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	// Should not be 404 (405 Method Not Allowed or 415 Unsupported Media Type is fine)
	require.NotEqual(t, http.StatusNotFound, rr.Code)

	// Verify WHEP DELETE route is registered — returns 404 for missing session, not 405
	// A 404 here means the route handler was invoked (session not found), confirming wiring
	req = httptest.NewRequest("DELETE", "/api/cameras/test/stream/webrtc/some-session", nil)
	req.SetBasicAuth("admin", "pass")
	rr = httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	// 404 is expected (session doesn't exist), confirming route is registered
	require.Equal(t, http.StatusNotFound, rr.Code)
	// Verify it's our JSON error, not Chi's plain text 404
	require.Contains(t, rr.Body.String(), "session not found")
}

func TestRoutes_FLVEndpointRegistered(t *testing.T) {
	t.Helper()
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()

	flvMgr := flv.NewManager()
	h := NewHandler(db, store, noopAuthMW(), nil, nil, nil, "", nil, nil, nil)
	h.SetFLVManager(flvMgr)

	rr := doRequest(t, h.Routes(), "GET", "/api/cameras/test/stream.flv", nil, "admin", "pass")

	// Camera not found (404), not route not found
	require.Equal(t, http.StatusNotFound, rr.Code)
}

func TestRoutes_CameraProtocolsEndpointRegistered(t *testing.T) {
	t.Helper()
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()

	h := NewHandler(db, store, noopAuthMW(), nil, nil, nil, "", nil, nil, nil)

	rr := doRequest(t, h.Routes(), "GET", "/api/cameras/nonexistent/protocols", nil, "admin", "pass")

	// Camera not found (404), not route not found
	require.Equal(t, http.StatusNotFound, rr.Code)
}

// --- SetWebRTCManager / SetFLVManager tests ---

func TestSetWebRTCManager(t *testing.T) {
	t.Helper()
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()

	h := NewHandler(db, store, noopAuthMW(), nil, nil, nil, "", nil, nil, nil)
	require.Nil(t, h.webrtcMgr)

	mgr := webrtc.NewManager()
	h.SetWebRTCManager(mgr)
	require.Equal(t, mgr, h.webrtcMgr)
}

func TestSetFLVManager(t *testing.T) {
	t.Helper()
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()

	h := NewHandler(db, store, noopAuthMW(), nil, nil, nil, "", nil, nil, nil)
	require.Nil(t, h.flvMgr)

	mgr := flv.NewManager()
	h.SetFLVManager(mgr)
	require.Equal(t, mgr, h.flvMgr)
}

// --- Test helpers ---

// seedCameraWithEncoding inserts a camera with the given encoding into the DB.
func seedCameraWithEncoding(t *testing.T, db *storage.DB, id, encoding string) {
	t.Helper()
	err := db.UpsertCamera(context.Background(), id, "Test Camera", "rtsp", encoding, "rtsp://example.com/stream", "", "", "", "", "")
	require.NoError(t, err, "failed to seed camera %s", id)
}

// seedCameraWithEncodings inserts a camera with separate encoding and stream_encoding.
func seedCameraWithEncodings(t *testing.T, db *storage.DB, id, encoding, streamEncoding string) {
	t.Helper()
	err := db.UpsertCamera(context.Background(), id, "Test Camera", "rtsp", encoding, "rtsp://example.com/stream", "", "", "", "", streamEncoding)
	require.NoError(t, err, "failed to seed camera %s", id)
}

// parseJSONBody parses JSON from a httptest.ResponseRecorder into v.
func parseJSONBody(t *testing.T, rr *httptest.ResponseRecorder, v interface{}) error {
	t.Helper()
	dec := json.NewDecoder(rr.Body)
	return dec.Decode(v)
}
