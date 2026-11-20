package api

// Tests for handlers_mjpeg.go — MJPEG stream URL resolution + latest-frame
// snapshot endpoint (#232). The success paths require a live recorder; here we
// cover the nil-manager / no-camera error paths. (The pure URL-extractor unit
// tests moved with the handler into streamsapi.)

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMjpeg_StreamURL_NotAvailable(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	t.Cleanup(func() { db.Close() })
	h := TestHandler(db, store) // camMgr is nil → resolveMjpegURL returns ""

	rr := doRequest(t, h.Routes(), "GET", "/api/cameras/cam-1/stream.mjpeg", nil, "", "")
	require.Equal(t, http.StatusNotFound, rr.Code)
}

func TestMjpeg_LatestFrame_NotAvailable(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	t.Cleanup(func() { db.Close() })
	h := TestHandler(db, store) // camMgr is nil → no frame

	rr := doRequest(t, h.Routes(), "GET", "/api/cameras/cam-1/latest-frame", nil, "", "")
	require.Equal(t, http.StatusNotFound, rr.Code)
}
