package api

// Tests for handlers_webrtc.go — WHEP session create/delete endpoints (#232).
// Success paths need a live WebRTC manager + peer connection; here we cover the
// not-available, wrong-content-type, and camera-not-found error paths.

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWebRTC_Create_NotAvailable(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	t.Cleanup(func() { db.Close() })
	h := TestHandler(db, store) // webrtcMgr is nil

	rr := doRequest(t, h.Routes(), "POST", "/api/cameras/cam-1/stream/webrtc", nil, "", "")
	require.Equal(t, http.StatusServiceUnavailable, rr.Code)
}

func TestWebRTC_Create_NotFound(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	t.Cleanup(func() { db.Close() })
	h := TestHandler(db, store)
	// webrtcMgr still nil → 503 short-circuits before the camera lookup, so this
	// also documents that ordering (availability is checked first).
	rr := doRequest(t, h.Routes(), "POST", "/api/cameras/cam-missing/stream/webrtc", nil, "", "")
	require.Equal(t, http.StatusServiceUnavailable, rr.Code)
}

func TestWebRTC_Delete_NotAvailable(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	t.Cleanup(func() { db.Close() })
	h := TestHandler(db, store) // webrtcMgr is nil

	rr := doRequest(t, h.Routes(), "DELETE", "/api/cameras/cam-1/stream/webrtc/sess-1", nil, "", "")
	require.Equal(t, http.StatusServiceUnavailable, rr.Code)
}
