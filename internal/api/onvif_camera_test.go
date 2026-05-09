package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestONVIFCameraProfilesEndpoint(t *testing.T) {
	h := TestHandler(nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/cameras/test-cam/onvif/profiles", nil)

	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	var resp map[string]interface{}
	err := json.NewDecoder(w.Body).Decode(&resp)
	require.NoError(t, err)
	_, hasProfiles := resp["profiles"]
	require.True(t, hasProfiles, "response should contain 'profiles' field")
}

func TestONVIFCameraProfilesCapabilities(t *testing.T) {
	h := TestHandler(nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/cameras/test-cam/onvif/profiles", nil)

	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	var resp map[string]interface{}
	err := json.NewDecoder(w.Body).Decode(&resp)
	require.NoError(t, err)

	caps, ok := resp["capabilities"].(map[string]interface{})
	require.True(t, ok, "response should contain 'capabilities' object")
	_, hasPTZ := caps["ptz"]
	_, hasStreaming := caps["streaming"]
	require.True(t, hasPTZ, "capabilities should contain 'ptz'")
	require.True(t, hasStreaming, "capabilities should contain 'streaming'")
}

func TestCreateONVIFCameraMissingEndpoint(t *testing.T) {
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
	h := TestHandler(nil, nil)
	body := `{"name": "Test ONVIF", "protocol": "onvif", "onvif_endpoint": "http://192.168.1.100:8080/onvif/device_service"}`
	req := httptest.NewRequest(http.MethodPost, "/api/cameras", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)

	// camMgr is nil in TestHandler(nil, nil), so expect 500
	require.Equal(t, http.StatusInternalServerError, w.Code)
}
