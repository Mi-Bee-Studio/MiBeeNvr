package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
	"github.com/stretchr/testify/require"
)

func setupPTZTestDB(t *testing.T) (*storage.DB, *storage.Manager) {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	db, err := storage.New(dbPath)
	require.NoError(t, err)
	ctx := context.Background()
	require.NoError(t, db.Init(ctx))
	store, err := storage.NewManager(filepath.Join(dir, "storage"))
	require.NoError(t, err)
	return db, store
}

func TestPTZMoveEndpoint(t *testing.T) {
	t.Parallel()
	db, store := setupPTZTestDB(t)
	ctx := context.Background()
	require.NoError(t, db.UpsertCamera(ctx, "onvif-cam", "ONVIF Camera", "onvif", "", "onvif://host/stream", "admin", "pass", "", "", "", ""))

	h := TestHandler(db, store)
	body := `{"mode": "continuous", "pan": 0.5, "tilt": 0.0, "zoom": 0.0}`
	req := httptest.NewRequest(http.MethodPost, "/api/cameras/onvif-cam/ptz/move", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)

	// camMgr is nil in TestHandler — returns 500
	require.Equal(t, http.StatusInternalServerError, w.Code)
	var resp map[string]string
	err := json.NewDecoder(w.Body).Decode(&resp)
	require.NoError(t, err)
	require.Contains(t, resp["error"], "camera manager not available")
}

func TestPTZMoveNonOnvifRejected(t *testing.T) {
	t.Parallel()
	db, store := setupPTZTestDB(t)
	ctx := context.Background()
	require.NoError(t, db.UpsertCamera(ctx, "rtsp-cam", "RTSP Camera", "rtsp_h264", "", "rtsp://host/stream", "", "", "", "", "", ""))

	h := TestHandler(db, store)
	body := `{"mode": "continuous", "pan": 0.5, "tilt": 0.0, "zoom": 0.0}`
	req := httptest.NewRequest(http.MethodPost, "/api/cameras/rtsp-cam/ptz/move", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
}

func TestPTZMoveCameraNotFound(t *testing.T) {
	t.Parallel()
	db, store := setupPTZTestDB(t)

	h := TestHandler(db, store)
	body := `{"mode": "continuous", "pan": 0.5, "tilt": 0.0, "zoom": 0.0}`
	req := httptest.NewRequest(http.MethodPost, "/api/cameras/nonexistent/ptz/move", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)

	require.Equal(t, http.StatusNotFound, w.Code)
}

func TestPTZMoveInvalidMode(t *testing.T) {
	t.Parallel()
	db, store := setupPTZTestDB(t)
	ctx := context.Background()
	require.NoError(t, db.UpsertCamera(ctx, "onvif-cam", "ONVIF Camera", "onvif", "", "onvif://host/stream", "", "", "", "", "", ""))

	h := TestHandler(db, store)
	body := `{"mode": "invalid", "pan": 0.5}`
	req := httptest.NewRequest(http.MethodPost, "/api/cameras/onvif-cam/ptz/move", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
}

func TestPTZStopEndpoint(t *testing.T) {
	t.Parallel()
	db, store := setupPTZTestDB(t)
	ctx := context.Background()
	require.NoError(t, db.UpsertCamera(ctx, "onvif-cam", "ONVIF Camera", "onvif", "", "onvif://host/stream", "", "", "", "", "", ""))

	h := TestHandler(db, store)
	req := httptest.NewRequest(http.MethodPost, "/api/cameras/onvif-cam/ptz/stop", nil)

	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)

	// camMgr is nil in TestHandler — requireONVIF passes (camera is ONVIF) but camMgr is nil
	require.Equal(t, http.StatusInternalServerError, w.Code)
	var resp map[string]string
	err := json.NewDecoder(w.Body).Decode(&resp)
	require.NoError(t, err)
	require.Contains(t, resp["error"], "camera manager not available")
}

func TestPTZStatusEndpoint(t *testing.T) {
	t.Parallel()
	db, store := setupPTZTestDB(t)
	ctx := context.Background()
	require.NoError(t, db.UpsertCamera(ctx, "onvif-cam", "ONVIF Camera", "onvif", "", "onvif://host/stream", "", "", "", "", "", ""))

	h := TestHandler(db, store)
	req := httptest.NewRequest(http.MethodGet, "/api/cameras/onvif-cam/ptz/status", nil)

	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)

	// camMgr is nil in TestHandler — requireONVIF passes but camMgr is nil
	require.Equal(t, http.StatusInternalServerError, w.Code)
}
