package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestONVIFCameraProfilesEndpoint(t *testing.T) {
	t.Parallel()
	db, store := setupPTZTestDB(t)
	ctx := context.Background()
	require.NoError(t, db.UpsertCamera(ctx, "onvif-cam", "ONVIF Camera", "onvif", "", "onvif://host/stream", "admin", "pass", true, "", "", ""))

	h := TestHandler(db, store)
	req := httptest.NewRequest(http.MethodGet, "/api/cameras/onvif-cam/onvif/profiles", nil)

	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)

	// camMgr is nil, so expect 500
	require.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestONVIFCameraProfilesCapabilities(t *testing.T) {
	t.Parallel()
	// Test the capabilities endpoint exists and returns correct error when camMgr nil
	db, store := setupPTZTestDB(t)
	ctx := context.Background()
	require.NoError(t, db.UpsertCamera(ctx, "onvif-cam", "ONVIF Camera", "onvif", "", "onvif://host/stream", "admin", "pass", true, "", "", ""))

	h := TestHandler(db, store)
	req := httptest.NewRequest(http.MethodGet, "/api/cameras/onvif-cam/onvif/capabilities", nil)

	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)

	// camMgr is nil, so expect 500
	require.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestCreateONVIFCameraMissingEndpoint(t *testing.T) {
	t.Parallel()
	h := TestHandler(nil, nil)
	body := `{"name": "Test ONVIF", "protocol": "onvif"}`
	req := httptest.NewRequest(http.MethodPost, "/api/cameras", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)

	// Should reject without onvif_endpoint
	require.Equal(t, http.StatusBadRequest, w.Code)

	var resp map[string]string
	err := json.NewDecoder(w.Body).Decode(&resp)
	require.NoError(t, err)
	require.Contains(t, resp["error"], "onvif_endpoint")
}

func TestCreateONVIFCameraWithEndpoint(t *testing.T) {
	t.Parallel()
	h := TestHandler(nil, nil)
	body := `{"name": "Test ONVIF", "protocol": "onvif", "onvif_endpoint": "http://192.168.1.100:8080/onvif/device_service"}`
	req := httptest.NewRequest(http.MethodPost, "/api/cameras", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)

	// camMgr is nil in TestHandler(nil, nil), so expect 500
	require.Equal(t, http.StatusInternalServerError, w.Code)
}
