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

	// Discovery is not yet implemented, expect 500
	require.Equal(t, http.StatusInternalServerError, w.Code)

	var resp map[string]string
	err := json.NewDecoder(w.Body).Decode(&resp)
	require.NoError(t, err)
	require.Contains(t, resp["error"], "discovery failed")
}

func TestONVIFDiscoverDefaultTimeout(t *testing.T) {
	h := TestHandler(nil, nil)
	body := `{}`
	req := httptest.NewRequest(http.MethodPost, "/api/onvif/discover", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)

	// Default timeout = 5, but discovery returns error
	require.Equal(t, http.StatusInternalServerError, w.Code)
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

	// Negative timeout defaults to 5, but discovery returns error
	require.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestONVIFDeviceDetailEndpoint(t *testing.T) {
	h := TestHandler(nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/onvif/discover/192.168.1.100", nil)

	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)

	// Not yet implemented
	require.Equal(t, http.StatusNotImplemented, w.Code)

	var resp map[string]string
	err := json.NewDecoder(w.Body).Decode(&resp)
	require.NoError(t, err)
	require.Contains(t, resp["error"], "not yet implemented")
}
