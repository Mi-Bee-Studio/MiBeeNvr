package api

// Tests for handlers_vod.go — VOD HLS playback endpoints (#321, batch #578).
// Error/validation paths only: the happy path needs a real parseable MP4
// fixture (vod.Manager parses sample tables from disk), which is out of
// scope for the hermetic batch per the #578 non-goals.

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const vodDay = "2026-08-20T10:00:00Z"

func vodRange(extra string) string {
	return "?start=" + vodDay + "&end=2026-08-20T12:00:00Z" + extra
}

func TestVODPlaylist_Validation(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	t.Cleanup(func() { db.Close() })
	h := TestHandler(db, store)
	routes := h.Routes()

	cases := []struct {
		name string
		qs   string
		code int
	}{
		{"missing start", "?end=2026-08-20T12:00:00Z", http.StatusBadRequest},
		{"missing end", "?start=" + vodDay, http.StatusBadRequest},
		{"bad start", "?start=nope&end=2026-08-20T12:00:00Z", http.StatusBadRequest},
		{"bad end", "?start=" + vodDay + "&end=nope", http.StatusBadRequest},
		{"end before start", "?start=" + vodDay + "&end=2026-08-20T09:00:00Z", http.StatusBadRequest},
		{"range too wide", "?start=" + vodDay + "&end=2026-08-25T10:00:00Z", http.StatusBadRequest},
		{"no recordings in range", vodRange(""), http.StatusNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rr := doRequest(t, routes, http.MethodGet, "/api/cameras/cam-1/playback/playlist.m3u8"+tc.qs, nil, "", "")
			require.Equal(t, tc.code, rr.Code)
		})
	}
}

func TestVODSegment_Guards(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	t.Cleanup(func() { db.Close() })
	h := TestHandler(db, store)
	routes := h.Routes()

	seedRecording(t, db, makeRecording("vod-1", "cam-1", "h264", time.Now(), false))
	// A recording of a different camera: URL camera mismatch → 404.
	seedRecording(t, db, makeRecording("vod-other", "cam-2", "h264", time.Now(), false))
	// Non-video format → 404 "not video-playable".
	seedRecording(t, db, makeRecording("vod-mjpeg", "cam-1", "mjpeg", time.Now(), false))

	cases := []struct {
		name string
		path string
		code int
	}{
		{"unknown recording", "/api/cameras/cam-1/playback/nope/init.mp4", http.StatusNotFound},
		{"camera mismatch", "/api/cameras/cam-1/playback/vod-other/init.mp4", http.StatusNotFound},
		{"non-video format", "/api/cameras/cam-1/playback/vod-mjpeg/init.mp4", http.StatusNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rr := doRequest(t, routes, http.MethodGet, tc.path, nil, "", "")
			require.Equal(t, tc.code, rr.Code)
		})
	}
}

func TestVODSegment_GarbageFileIs500(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	t.Cleanup(func() { db.Close() })
	h := TestHandler(db, store)

	// A video recording whose file exists but is not an MP4: the fragment
	// planner must fail with 500, not serve garbage.
	p := filepath.Join(t.TempDir(), "seg.mp4")
	require.NoError(t, os.WriteFile(p, []byte("definitely not an mp4"), 0o644))
	rec := makeRecording("vod-garbage", "cam-1", "h264", time.Now(), false)
	rec.FilePath = p
	seedRecording(t, db, rec)

	rr := doRequest(t, h.Routes(), http.MethodGet, "/api/cameras/cam-1/playback/vod-garbage/init.mp4", nil, "", "")
	require.Equal(t, http.StatusInternalServerError, rr.Code)

	rr = doRequest(t, h.Routes(), http.MethodGet, "/api/cameras/cam-1/playback/vod-garbage/f0-1.m4s", nil, "", "")
	require.Equal(t, http.StatusInternalServerError, rr.Code)
}
