package api

// Tests for the per-camera frame-trace sampling endpoint (#482):
// GET/POST/DELETE /api/cameras/{id}/trace.

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/frametrace"
	"github.com/stretchr/testify/require"
)

func newTraceTestHandler(t *testing.T) *Handler {
	t.Helper()
	h, _ := newFlowTestHandler(t)
	require.NoError(t, h.db.UpsertCamera(t.Context(), "trace-cam", "Trace Cam", "rtsp", "h264", "", "", "", "", "", "", ""))
	return h
}

func TestHandleCameraTrace_Lifecycle(t *testing.T) {
	h := newTraceTestHandler(t)

	// Before: inactive.
	rr := doRequest(t, h.Routes(), http.MethodGet, "/api/cameras/trace-cam/trace", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	var get struct {
		Active bool `json:"active"`
	}
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&get))
	require.False(t, get.Active)

	// POST starts the window.
	rr = doRequest(t, h.Routes(), http.MethodPost, "/api/cameras/trace-cam/trace?duration=45s", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	var post struct {
		Active      bool   `json:"active"`
		ActiveUntil string `json:"active_until"`
	}
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&post))
	require.True(t, post.Active)
	require.NotEmpty(t, post.ActiveUntil)
	require.True(t, frametrace.Active("trace-cam"))

	// DELETE stops it.
	rr = doRequest(t, h.Routes(), http.MethodDelete, "/api/cameras/trace-cam/trace", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	require.False(t, frametrace.Active("trace-cam"))
}

func TestHandleCameraTrace_DefaultDuration(t *testing.T) {
	h := newTraceTestHandler(t)
	t.Cleanup(func() { frametrace.Disable("trace-cam") })

	rr := doRequest(t, h.Routes(), http.MethodPost, "/api/cameras/trace-cam/trace", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	require.True(t, frametrace.Active("trace-cam"), "no duration → default window")
}

func TestHandleCameraTrace_BadDuration(t *testing.T) {
	h := newTraceTestHandler(t)

	rr := doRequest(t, h.Routes(), http.MethodPost, "/api/cameras/trace-cam/trace?duration=abc", nil, "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestHandleCameraTrace_NotFound(t *testing.T) {
	h := newTraceTestHandler(t)

	rr := doRequest(t, h.Routes(), http.MethodPost, "/api/cameras/nope/trace", nil, "", "")
	require.Equal(t, http.StatusNotFound, rr.Code)
}
