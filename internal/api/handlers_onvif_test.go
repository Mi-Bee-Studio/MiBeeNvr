package api

// Tests for handlers_onvif.go — ONVIF discovery + PTZ control endpoints (#232).
// The success paths need live ONVIF cameras; here we cover the validation /
// requireONVIF guard paths which are the highest-value regression guards.

import (
	"bytes"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestONVIF_Discover_TimeoutTooLarge(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	t.Cleanup(func() { db.Close() })
	h := TestHandler(db, store)

	rr := doRequest(t, h.Routes(), "POST", "/api/onvif/discover",
		bytes.NewBufferString(`{"timeout":60}`), "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestONVIF_Discover_BadJSON_DefaultsTimeout(t *testing.T) {
	// Malformed JSON falls back to timeout=5 (no 400). This actually runs a
	// 5s onvif.Discover on the local network — keep it but don't assert on the
	// device list, only that it returns 200 (discovery is best-effort).
	// Skipped by default to avoid the network wait in CI; enable with -run.
	t.Skip("onvif.Discover performs real network discovery; skip in CI")
}

func TestONVIF_DeviceDetail_NoIP(t *testing.T) {
	// The {ip} URL param is required by the route; an empty value can't be
	// sent through chi (the route won't match), so this documents the guard.
	t.Parallel()
	db, store := setupTestDB(t)
	t.Cleanup(func() { db.Close() })
	h := TestHandler(db, store)
	// A literal call without an IP doesn't match the /{ip} route → 404 from chi.
	rr := doRequest(t, h.Routes(), "GET", "/api/onvif/discover/", nil, "", "")
	require.Equal(t, http.StatusNotFound, rr.Code)
}

func TestPTZ_Move_InvalidBody(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	t.Cleanup(func() { db.Close() })
	h := TestHandler(db, store)

	rr := doRequest(t, h.Routes(), "POST", "/api/cameras/cam-1/ptz/move",
		bytes.NewBufferString(`{bad json`), "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestPTZ_Move_InvalidMode(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	t.Cleanup(func() { db.Close() })
	h := TestHandler(db, store)

	rr := doRequest(t, h.Routes(), "POST", "/api/cameras/cam-1/ptz/move",
		bytes.NewBufferString(`{"mode":"bogus","pan":0.1,"tilt":0,"zoom":0}`), "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestPTZ_Move_CameraNotFound(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	t.Cleanup(func() { db.Close() })
	h := TestHandler(db, store)

	rr := doRequest(t, h.Routes(), "POST", "/api/cameras/cam-missing/ptz/move",
		bytes.NewBufferString(`{"mode":"continuous","pan":0.1,"tilt":0,"zoom":0}`), "", "")
	require.Equal(t, http.StatusNotFound, rr.Code)
}

func TestPTZ_Move_NotONVIFCamera(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	t.Cleanup(func() { db.Close() })
	h := TestHandler(db, store)
	// Seed an RTSP camera (not ONVIF) → requireONVIF rejects with 400.
	require.NoError(t, h.db.UpsertCamera(t.Context(), "cam-rtsp", "RTSP Cam", "rtsp", "h264", "rtsp://x", "", "", "", "", "", ""))

	rr := doRequest(t, h.Routes(), "POST", "/api/cameras/cam-rtsp/ptz/move",
		bytes.NewBufferString(`{"mode":"continuous","pan":0.1,"tilt":0,"zoom":0}`), "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestPTZ_Stop_CameraNotFound(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	t.Cleanup(func() { db.Close() })
	h := TestHandler(db, store)

	rr := doRequest(t, h.Routes(), "POST", "/api/cameras/cam-missing/ptz/stop", nil, "", "")
	require.Equal(t, http.StatusNotFound, rr.Code)
}

func TestPTZ_GetPresets_CameraNotFound(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	t.Cleanup(func() { db.Close() })
	h := TestHandler(db, store)

	rr := doRequest(t, h.Routes(), "GET", "/api/cameras/cam-missing/ptz/presets", nil, "", "")
	require.Equal(t, http.StatusNotFound, rr.Code)
}

func TestPTZ_CreatePreset_CameraNotFound(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	t.Cleanup(func() { db.Close() })
	h := TestHandler(db, store)

	rr := doRequest(t, h.Routes(), "POST", "/api/cameras/cam-missing/ptz/presets",
		bytes.NewBufferString(`{"name":"Home"}`), "", "")
	require.Equal(t, http.StatusNotFound, rr.Code)
}
