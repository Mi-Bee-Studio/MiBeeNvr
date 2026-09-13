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

// TestRewriteLegacyPlaylistName covers the #772 compatibility aliases: docs
// through v0.12.0 advertised stream.m3u8 (never a registered muxer name);
// playlist.m3u8 is the conventional guess. Segment/init names pass through.
func TestRewriteLegacyPlaylistName(t *testing.T) {
	t.Parallel()
	cases := []struct{ in, want string }{
		{"/api/cameras/cam-1/stream/stream.m3u8", "/api/cameras/cam-1/stream/index.m3u8"},
		{"/api/cameras/cam-1/stream/playlist.m3u8", "/api/cameras/cam-1/stream/index.m3u8"},
		{"/api/cameras/cam-1/stream/sub/stream.m3u8", "/api/cameras/cam-1/stream/sub/index.m3u8"},
		{"/api/cameras/cam-1/stream/STREAM.M3U8", "/api/cameras/cam-1/stream/index.m3u8"},
		// Registered names and server-generated segment files: untouched.
		{"/api/cameras/cam-1/stream/index.m3u8", "/api/cameras/cam-1/stream/index.m3u8"},
		{"/api/cameras/cam-1/stream/abc123_video1_seg0.mp4", "/api/cameras/cam-1/stream/abc123_video1_seg0.mp4"},
		{"/api/cameras/cam-1/stream/video1_stream.m3u8", "/api/cameras/cam-1/stream/video1_stream.m3u8"},
		{"/api/cameras/cam-1/stream/init.mp4", "/api/cameras/cam-1/stream/init.mp4"},
	}
	for _, tc := range cases {
		require.Equal(t, tc.want, rewriteLegacyPlaylistName(tc.in), "input %q", tc.in)
	}
}
