package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/onvif"
	"github.com/stretchr/testify/require"
)

// stubDiscoverEmpty makes onvif.Discover deterministic for tests by returning
// an empty device list without performing real UDP multicast WS-Discovery,
// which is network-dependent (issue #221: a CI runner sharing a LAN with a
// real ONVIF camera caused the old "len(devices)==0" assertion to fail). The
// restore func is returned for the caller to defer.
func stubDiscoverEmpty(t *testing.T) {
	t.Helper()
	restore := onvif.SetDiscoverFuncForTest(func(ctx context.Context, timeout time.Duration, enrich bool) *onvif.DiscoveryResult {
		return &onvif.DiscoveryResult{Devices: []onvif.DiscoveredDevice{}}
	})
	t.Cleanup(restore)
}

func TestONVIFDiscoverEndpoint(t *testing.T) {
	t.Parallel()
	stubDiscoverEmpty(t)
	h := TestHandler(nil, nil)
	req := httptest.NewRequest(http.MethodPost, "/api/onvif/discover", nil)
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)

	// Discovery is stubbed — returns 200 with an empty devices list.
	require.Equal(t, http.StatusOK, w.Code)

	var resp map[string]interface{}
	err := json.NewDecoder(w.Body).Decode(&resp)
	require.NoError(t, err)
	devices, ok := resp["devices"].([]interface{})
	require.True(t, ok, "response should have 'devices' field")
	require.Equal(t, 0, len(devices), "stubbed discovery returns no devices")
}

func TestONVIFDiscoverDefaultTimeout(t *testing.T) {
	t.Parallel()
	stubDiscoverEmpty(t)
	h := TestHandler(nil, nil)
	body := `{}`
	req := httptest.NewRequest(http.MethodPost, "/api/onvif/discover", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)

	// Discovery is stubbed — returns 200 with empty devices.
	require.Equal(t, http.StatusOK, w.Code)
}

func TestONVIFDiscoverTimeoutTooLarge(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
	stubDiscoverEmpty(t)
	h := TestHandler(nil, nil)
	body := `{"timeout": -1}`
	req := httptest.NewRequest(http.MethodPost, "/api/onvif/discover", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)

	// Negative timeout defaults to 5s; with discovery stubbed it returns 200.
	require.Equal(t, http.StatusOK, w.Code)
}

func TestONVIFDeviceDetailEndpoint(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
	h := TestHandler(nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/onvif/discover/", nil)

	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)

	// chi requires at least one char for {ip} param, so /api/onvif/discover/ returns 404
	require.Equal(t, http.StatusNotFound, w.Code)
}

func TestPTZMove_InvalidMode(t *testing.T) {
	t.Parallel()
	h := TestHandler(nil, nil)
	body := `{"mode": "invalid"}`
	req := httptest.NewRequest(http.MethodPost, "/api/cameras/test-cam/ptz/move", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
}

func TestPTZMove_InvalidBody(t *testing.T) {
	t.Parallel()
	h := TestHandler(nil, nil)
	req := httptest.NewRequest(http.MethodPost, "/api/cameras/test-cam/ptz/move", strings.NewReader("not json"))
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
}

func TestPTZStop_NoCamMgr(t *testing.T) {
	t.Parallel()
	h := TestHandler(nil, nil)
	req := httptest.NewRequest(http.MethodPost, "/api/cameras/test-cam/ptz/stop", nil)

	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)

	// No DB means requireONVIF returns 404 (camera not found)
	require.Equal(t, http.StatusNotFound, w.Code)
}

func TestPTZStatus_NoCamMgr(t *testing.T) {
	t.Parallel()
	h := TestHandler(nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/cameras/test-cam/ptz/status", nil)

	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)

	// No DB means requireONVIF returns 404 (camera not found)
	require.Equal(t, http.StatusNotFound, w.Code)
}

func TestONVIFDeviceDetail_InvalidIP(t *testing.T) {
	t.Parallel()
	h := TestHandler(nil, nil)

	invalidIPs := []string{
		"/api/onvif/discover/notanip",
		"/api/onvif/discover/256.256.256.256",
		"/api/onvif/discover/abc.def.ghi.jkl",
		"/api/onvif/discover/127.0.0.999",
	}
	for _, path := range invalidIPs {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			w := httptest.NewRecorder()
			h.Routes().ServeHTTP(w, req)
			require.Equal(t, http.StatusBadRequest, w.Code)
			var resp map[string]string
			err := json.NewDecoder(w.Body).Decode(&resp)
			require.NoError(t, err)
			require.Equal(t, "invalid IP address format", resp["error"])
		})
	}
}

func TestONVIFProbeEndpoint(t *testing.T) {
	t.Parallel()
	h := TestHandler(nil, nil)
	body := `{"host": "192.168.1.100", "port": 80}`
	req := httptest.NewRequest(http.MethodPost, "/api/onvif/probe", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)

	// In test env with no real device at that IP, probe fails → 502 BadGateway
	require.Equal(t, http.StatusBadGateway, w.Code)
	require.Equal(t, "application/json", w.Header().Get("Content-Type"))

	var resp map[string]string
	err := json.NewDecoder(w.Body).Decode(&resp)
	require.NoError(t, err)
	require.Contains(t, resp["error"], "probe failed")
}

func TestONVIFProbe_DefaultsApplied(t *testing.T) {
	t.Parallel()
	h := TestHandler(nil, nil)
	// Empty body — defaults applied, tries to probe but no host → 400
	req := httptest.NewRequest(http.MethodPost, "/api/onvif/probe", strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
	var resp map[string]string
	err := json.NewDecoder(w.Body).Decode(&resp)
	require.NoError(t, err)
	require.Contains(t, resp["error"], "host is required")
}

func TestONVIFProbe_InvalidHost(t *testing.T) {
	t.Parallel()
	h := TestHandler(nil, nil)
	invalidHosts := []string{
		`{"host": "notanip"}`,
		`{"host": "256.256.256.256"}`,
		`{"host": "abc.def.ghi.jkl"}`,
	}
	for _, body := range invalidHosts {
		t.Run(body, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/onvif/probe", strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			h.Routes().ServeHTTP(w, req)
			require.Equal(t, http.StatusBadRequest, w.Code)
			var resp map[string]string
			err := json.NewDecoder(w.Body).Decode(&resp)
			require.NoError(t, err)
			require.Equal(t, "invalid IP address format", resp["error"])
		})
	}
}

func TestONVIFProbe_TimeoutTooLarge(t *testing.T) {
	t.Parallel()
	h := TestHandler(nil, nil)
	body := `{"host": "192.168.1.1", "timeout": 100}`
	req := httptest.NewRequest(http.MethodPost, "/api/onvif/probe", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
	var resp map[string]string
	err := json.NewDecoder(w.Body).Decode(&resp)
	require.NoError(t, err)
	require.Contains(t, resp["error"], "timeout")
}
