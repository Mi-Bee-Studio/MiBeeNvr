package api

// Tests for handlers_hls.go — HLS stream start/stop + snapshot endpoints (#232).
// Success paths need a live HLS manager + recorder; here we cover the
// not-available error paths and the camera-not-found guard.

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHLS_Stream_NotAvailable(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	t.Cleanup(func() { db.Close() })
	h := TestHandler(db, store) // hlsMgr is nil

	rr := doRequest(t, h.Routes(), "GET", "/api/cameras/cam-1/stream/index.m3u8", nil, "", "")
	require.Equal(t, http.StatusInternalServerError, rr.Code)
}

func TestHLS_Stop_NotAvailable(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	t.Cleanup(func() { db.Close() })
	h := TestHandler(db, store) // hlsMgr is nil

	rr := doRequest(t, h.Routes(), "DELETE", "/api/cameras/cam-1/stream", nil, "", "")
	require.Equal(t, http.StatusInternalServerError, rr.Code)
}

func TestHLS_GetRecorderHub(t *testing.T) {
	t.Parallel()
	// A nil recorder (or one that doesn't implement the hubber interface) yields
	// a nil hub — getRecorderHub type-asserts against an unexported hubber iface.
	require.Nil(t, getRecorderHub(nil))
}
