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
	db, store := setupPTZTestDB(t)
	ctx := context.Background()
	require.NoError(t, db.UpsertCamera(ctx, "onvif-cam", "ONVIF Camera", "onvif", "onvif://host/stream", "admin", "pass", true))

	h := TestHandler(db, store)
	body := `{"mode": "continuous", "pan": 0.5, "tilt": 0.0, "zoom": 0.0}`
	req := httptest.NewRequest(http.MethodPost, "/api/cameras/onvif-cam/ptz/move", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var resp map[string]string
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	require.Equal(t, "ok", resp["status"])
}

func TestPTZMoveNonOnvifRejected(t *testing.T) {
	db, store := setupPTZTestDB(t)
	ctx := context.Background()
	require.NoError(t, db.UpsertCamera(ctx, "rtsp-cam", "RTSP Camera", "rtsp_h264", "rtsp://host/stream", "", "", true))

	h := TestHandler(db, store)
	body := `{"mode": "continuous", "pan": 0.5, "tilt": 0.0, "zoom": 0.0}`
	req := httptest.NewRequest(http.MethodPost, "/api/cameras/rtsp-cam/ptz/move", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
}

func TestPTZMoveCameraNotFound(t *testing.T) {
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
	db, store := setupPTZTestDB(t)
	ctx := context.Background()
	require.NoError(t, db.UpsertCamera(ctx, "onvif-cam", "ONVIF Camera", "onvif", "onvif://host/stream", "", "", true))

	h := TestHandler(db, store)
	body := `{"mode": "invalid", "pan": 0.5}`
	req := httptest.NewRequest(http.MethodPost, "/api/cameras/onvif-cam/ptz/move", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
}

func TestPTZStopEndpoint(t *testing.T) {
	db, store := setupPTZTestDB(t)
	ctx := context.Background()
	require.NoError(t, db.UpsertCamera(ctx, "onvif-cam", "ONVIF Camera", "onvif", "onvif://host/stream", "", "", true))

	h := TestHandler(db, store)
	req := httptest.NewRequest(http.MethodPost, "/api/cameras/onvif-cam/ptz/stop", nil)

	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var resp map[string]string
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	require.Equal(t, "stopped", resp["status"])
}

func TestPTZStatusEndpoint(t *testing.T) {
	db, store := setupPTZTestDB(t)
	ctx := context.Background()
	require.NoError(t, db.UpsertCamera(ctx, "onvif-cam", "ONVIF Camera", "onvif", "onvif://host/stream", "", "", true))

	h := TestHandler(db, store)
	req := httptest.NewRequest(http.MethodGet, "/api/cameras/onvif-cam/ptz/status", nil)

	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var resp map[string]interface{}
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	_, hasPan := resp["pan"]
	_, hasMoving := resp["moving"]
	require.True(t, hasPan, "should have pan field")
	require.True(t, hasMoving, "should have moving field")
}
