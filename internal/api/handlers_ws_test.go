package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/camera"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/middleware"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/wsstream"
	"github.com/stretchr/testify/require"
)

// --- WebSocket stream endpoint tests ---

func TestStreamWS_AuthRequired(t *testing.T) {
	t.Helper()
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()

	authMW, _ := middleware.NewAuthMiddleware(middleware.AuthProvider{
		GetUsername: func() string { return "admin" },
		GetHash:     func() string { return "a$dummyhashdummyhashdummyhashdum" },
	}, "", middleware.AuthRateLimitConfig{})
	h := NewHandler(db, store, authMW, nil, nil, nil, "", nil, nil, nil, nil, nil)

	r := h.Routes()
	req := httptest.NewRequest(http.MethodGet, "/api/cameras/test-cam/stream/ws", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	require.Equal(t, http.StatusUnauthorized, rr.Code)
}

func TestStreamWS_NoManager(t *testing.T) {
	t.Helper()
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()

	h := NewHandler(db, store, noopAuthMW(), nil, nil, nil, "", nil, nil, nil, nil, nil)

	rr := doRequest(t, h.Routes(), "GET", "/api/cameras/test-cam/stream/ws", nil, "admin", "pass")

	require.Equal(t, http.StatusServiceUnavailable, rr.Code)
}

func TestStreamWS_CameraNotFound(t *testing.T) {
	t.Helper()
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()

	wsMgr := wsstream.NewManager()
	h := NewHandler(db, store, noopAuthMW(), nil, nil, nil, "", nil, nil, nil, nil, nil)
	h.SetWSManager(wsMgr)

	rr := doRequest(t, h.Routes(), "GET", "/api/cameras/nonexistent/stream/ws", nil, "admin", "pass")

	require.Equal(t, http.StatusNotFound, rr.Code)
}

func TestStreamWS_NoCamMgr(t *testing.T) {
	t.Helper()
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()

	seedCameraWithEncoding(t, db, "cam1", "h264")

	wsMgr := wsstream.NewManager()
	// Create handler with no camMgr (nil camera manager)
	h := NewHandler(db, store, noopAuthMW(), nil, nil, nil, "", nil, nil, nil, nil, nil)
	h.SetWSManager(wsMgr)

	// Stream not active and camMgr is nil -> 404
	rr := doRequest(t, h.Routes(), "GET", "/api/cameras/cam1/stream/ws", nil, "admin", "pass")
	require.Equal(t, http.StatusNotFound, rr.Code)
}

func TestStreamWS_RecorderNotRunning(t *testing.T) {
	t.Helper()
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()

	// Create handler with camMgr using same db/store
	camMgr := camera.NewCameraManager(&config.Config{
		Storage: config.StorageConfig{RootDir: store.RootDir(), SegmentDuration: "30s"},
		Cleanup: config.CleanupConfig{RetentionDays: 30, CheckInterval: "1h", DiskThresholdPercent: 95},
		Cameras: []config.CameraConfig{},
	}, store, db, "")
	h := NewHandler(db, store, noopAuthMW(), nil, camMgr, nil, "", nil, nil, nil, nil, nil)

	seedCameraWithEncoding(t, db, "cam1", "h264")

	wsMgr := wsstream.NewManager()
	h.SetWSManager(wsMgr)

	rr := doRequest(t, h.Routes(), "GET", "/api/cameras/cam1/stream/ws", nil, "admin", "pass")

	// Camera recorder not running (not started)
	require.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestStreamWS_RequiresAuth(t *testing.T) {
	t.Helper()
	db, store := setupTestDB(t)
	defer db.Close()

	hash, err := middleware.HashPassword("secret")
	require.NoError(t, err)

	h := testHandlerWithAuth(db, store, "admin", hash)
	wsMgr := wsstream.NewManager()
	h.SetWSManager(wsMgr)

	// Without auth
	rr := doRequest(t, h.Routes(), "GET", "/api/cameras/test-cam/stream/ws", nil, "", "")
	require.Equal(t, http.StatusUnauthorized, rr.Code)

	// With valid auth (camera "test-cam" not in DB)
	rr = doRequest(t, h.Routes(), "GET", "/api/cameras/test-cam/stream/ws", nil, "admin", "secret")
	require.Equal(t, http.StatusNotFound, rr.Code)

	// With wrong auth
	rr = doRequest(t, h.Routes(), "GET", "/api/cameras/test-cam/stream/ws", nil, "admin", "wrong")
	require.Equal(t, http.StatusUnauthorized, rr.Code)
}
