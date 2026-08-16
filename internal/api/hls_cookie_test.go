package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/middleware"
	"github.com/stretchr/testify/require"
)

// setStreamCookieOnPlaylist is unit-tested directly: the full handleHLSStream
// path requires a live recorder + HLS muxer (see the M5 real-device
// validation for the end-to-end flow).
func TestSetStreamCookieOnPlaylist(t *testing.T) {
	t.Parallel()
	token, _ := middleware.SignSessionToken("admin", "$2a$10$somehash", time.Now())

	mkReq := func(path string, withBearer bool) *http.Request {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		if withBearer {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		return r
	}

	t.Run("playlist with session token sets cookie", func(t *testing.T) {
		rr := httptest.NewRecorder()
		setStreamCookieOnPlaylist(rr, mkReq("/api/cameras/cam-1/stream/index.m3u8", true), "cam-1")

		cookies := rr.Result().Cookies()
		require.Len(t, cookies, 1)
		c := cookies[0]
		require.Equal(t, middleware.StreamCookieName, c.Name)
		require.Equal(t, token, c.Value)
		require.Equal(t, "/api/cameras/cam-1/", c.Path, "cookie scoped to the camera's subtree")
		require.Equal(t, int(middleware.TokenTTL.Seconds()), c.MaxAge)
		require.True(t, c.HttpOnly)
		require.Equal(t, http.SameSiteLaxMode, c.SameSite)
		require.False(t, c.Secure, "plain-HTTP LAN must not set Secure (cookie would be dropped)")
	})

	t.Run("segment fetch does not set cookie", func(t *testing.T) {
		rr := httptest.NewRecorder()
		setStreamCookieOnPlaylist(rr, mkReq("/api/cameras/cam-1/stream/seg-4.ts", true), "cam-1")
		require.Empty(t, rr.Result().Cookies())
	})

	t.Run("playlist without session token (BasicAuth) sets nothing", func(t *testing.T) {
		rr := httptest.NewRecorder()
		setStreamCookieOnPlaylist(rr, mkReq("/api/cameras/cam-1/stream/index.m3u8", false), "cam-1")
		require.Empty(t, rr.Result().Cookies())
	})

	t.Run("renewed token takes precedence", func(t *testing.T) {
		fresh, _ := middleware.SignSessionToken("admin", "$2a$10$somehash", time.Now())
		rr := httptest.NewRecorder()
		rr.Header().Set(middleware.RenewedTokenHeader, fresh)
		setStreamCookieOnPlaylist(rr, mkReq("/api/cameras/cam-1/stream/index.m3u8", true), "cam-1")
		cookies := rr.Result().Cookies()
		require.Len(t, cookies, 1)
		require.Equal(t, fresh, cookies[0].Value, "cookie must carry the freshly-renewed token")
	})
}
