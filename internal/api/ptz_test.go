package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPTZMoveEndpoint(t *testing.T) {
	h := TestHandler(nil, nil)
	body := `{"mode": "continuous", "pan": 0.5, "tilt": 0.0, "zoom": 0.0}`
	req := httptest.NewRequest(http.MethodPost, "/api/cameras/test-cam/ptz/move", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var resp map[string]string
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	require.Equal(t, "ok", resp["status"])
}

func TestPTZMoveInvalidMode(t *testing.T) {
	h := TestHandler(nil, nil)
	body := `{"mode": "invalid", "pan": 0.5}`
	req := httptest.NewRequest(http.MethodPost, "/api/cameras/test-cam/ptz/move", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
}

func TestPTZStopEndpoint(t *testing.T) {
	h := TestHandler(nil, nil)
	req := httptest.NewRequest(http.MethodPost, "/api/cameras/test-cam/ptz/stop", nil)

	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var resp map[string]string
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	require.Equal(t, "stopped", resp["status"])
}

func TestPTZStatusEndpoint(t *testing.T) {
	h := TestHandler(nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/cameras/test-cam/ptz/status", nil)

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
