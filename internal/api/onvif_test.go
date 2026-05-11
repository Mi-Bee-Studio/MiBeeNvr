package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestONVIFDiscoverEndpoint(t *testing.T) {
	h := TestHandler(nil, nil)
	req := httptest.NewRequest(http.MethodPost, "/api/onvif/discover", nil)
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)

	// Discovery now works — returns 200 with empty devices list
	require.Equal(t, http.StatusOK, w.Code)

	var resp map[string]interface{}
	err := json.NewDecoder(w.Body).Decode(&resp)
	require.NoError(t, err)
	devices, ok := resp["devices"].([]interface{})
	require.True(t, ok, "response should have 'devices' field")
	require.Equal(t, 0, len(devices), "no ONVIF devices in test environment")
}

func TestONVIFDiscoverDefaultTimeout(t *testing.T) {
	h := TestHandler(nil, nil)
	body := `{}`
	req := httptest.NewRequest(http.MethodPost, "/api/onvif/discover", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)

	// Discovery succeeds — returns 200 with empty devices
	require.Equal(t, http.StatusOK, w.Code)
}

func TestONVIFDiscoverTimeoutTooLarge(t *testing.T) {
	h := TestHandler(nil, nil)
	body := `{"timeout": 100}`
	req := httptest.NewRequest(http.MethodPost, "/api/onvif/discover", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)

	var resp map[string]string
	err := json.NewDecoder(w.Body).Decode(&resp)
	require.NoError(t, err)
	require.Contains(t, resp["error"], "timeout")
}

func TestONVIFDiscoverNegativeTimeout(t *testing.T) {
	h := TestHandler(nil, nil)
	body := `{"timeout": -1}`
	req := httptest.NewRequest(http.MethodPost, "/api/onvif/discover", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)

	// Negative timeout defaults to 5s, discovery runs and returns
	require.Equal(t, http.StatusOK, w.Code)
}

func TestONVIFDeviceDetailEndpoint(t *testing.T) {
	h := TestHandler(nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/onvif/discover/192.168.1.100", nil)

	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)

	// Device detail now actually tries to connect to the ONVIF device.
	// In test environment with no real device, this returns 502 BadGateway.
	require.Equal(t, http.StatusBadGateway, w.Code)
	var resp map[string]string
	err := json.NewDecoder(w.Body).Decode(&resp)
	require.NoError(t, err)
	require.Contains(t, resp["error"], "failed to connect")
}

func TestONVIFDeviceDetail_MissingIP(t *testing.T) {
	h := TestHandler(nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/onvif/discover/", nil)

	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)

	// chi requires at least one char for {ip} param, so /api/onvif/discover/ returns 404
	require.Equal(t, http.StatusNotFound, w.Code)
}

func TestPTZMove_InvalidMode(t *testing.T) {
	h := TestHandler(nil, nil)
	body := `{"mode": "invalid"}`
	req := httptest.NewRequest(http.MethodPost, "/api/cameras/test-cam/ptz/move", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
}

func TestPTZMove_InvalidBody(t *testing.T) {
	h := TestHandler(nil, nil)
	req := httptest.NewRequest(http.MethodPost, "/api/cameras/test-cam/ptz/move", strings.NewReader("not json"))
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
}

func TestPTZStop_NoCamMgr(t *testing.T) {
	h := TestHandler(nil, nil)
	req := httptest.NewRequest(http.MethodPost, "/api/cameras/test-cam/ptz/stop", nil)

	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)

	// No DB means requireONVIF returns 404 (camera not found)
	require.Equal(t, http.StatusNotFound, w.Code)
}

func TestPTZStatus_NoCamMgr(t *testing.T) {
	h := TestHandler(nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/cameras/test-cam/ptz/status", nil)

	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)

	// No DB means requireONVIF returns 404 (camera not found)
	require.Equal(t, http.StatusNotFound, w.Code)
}
