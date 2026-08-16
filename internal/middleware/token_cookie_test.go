package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestStreamCookieEligible(t *testing.T) {
	t.Parallel()
	cases := []struct {
		method string
		path   string
		want   bool
	}{
		{http.MethodGet, "/api/cameras/cam-1/stream/index.m3u8", true},
		{http.MethodGet, "/api/cameras/cam-1/stream/seg-12.ts", true},
		{http.MethodGet, "/api/cameras/cam-1/stream/init.mp4", true},
		{http.MethodHead, "/api/cameras/cam-1/stream/seg-3.m4s", true},
		{http.MethodGet, "/api/cameras/cam-1/stream.flv", true},
		// State-changing methods must NEVER authenticate via cookie.
		{http.MethodPut, "/api/cameras/cam-1/stream/seg-12.ts", false},
		{http.MethodDelete, "/api/cameras/cam-1/stream/seg-12.ts", false},
		{http.MethodPost, "/api/cameras/cam-1/stream/index.m3u8", false},
		// Non-media API paths (even GET) never authenticate via cookie.
		{http.MethodGet, "/api/cameras", false},
		{http.MethodGet, "/api/cameras/cam-1", false},
		{http.MethodGet, "/api/recordings", false},
		{http.MethodPut, "/api/cameras/cam-1", false},
	}
	for _, tc := range cases {
		r := httptest.NewRequest(tc.method, tc.path, nil)
		require.Equal(t, tc.want, streamCookieEligible(r), "%s %s", tc.method, tc.path)
	}
}

// TestAuthMiddlewareStreamCookie covers the HLS-on-iOS flow (#331): a media
// fetch (GET .ts) authenticated ONLY by the stream cookie passes, while the
// same cookie on a state-changing or non-media request is ignored (falls
// through to BasicAuth and fails without credentials).
func TestAuthMiddlewareStreamCookie(t *testing.T) {
	t.Parallel()
	hash, _ := HashPassword("secret")
	mw, _ := NewAuthMiddleware(staticProvider("admin", hash), "", AuthRateLimitConfig{})
	token, _ := SignSessionToken("admin", hash, time.Now())

	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	do := func(method, path string, cookie bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, nil)
		if cookie {
			r.AddCookie(&http.Cookie{Name: StreamCookieName, Value: token})
		}
		rr := httptest.NewRecorder()
		mw(ok).ServeHTTP(rr, r)
		return rr
	}

	require.Equal(t, http.StatusOK, do(http.MethodGet, "/api/cameras/cam-1/stream/seg-1.ts", true).Code,
		"media GET with stream cookie must authenticate")
	require.Equal(t, http.StatusUnauthorized, do(http.MethodGet, "/api/cameras/cam-1/stream/seg-1.ts", false).Code,
		"no credentials → 401")
	require.Equal(t, http.StatusUnauthorized, do(http.MethodPut, "/api/cameras/cam-1", true).Code,
		"cookie must not authenticate a state-changing API call (CSRF containment)")
	require.Equal(t, http.StatusUnauthorized, do(http.MethodGet, "/api/recordings", true).Code,
		"cookie must not authenticate non-media API calls")
}

func TestBearerSessionTokenPrecedence(t *testing.T) {
	t.Parallel()
	bearer := SessionTokenPrefix + "bearer-value"
	cookie := SessionTokenPrefix + "cookie-value"

	r := httptest.NewRequest(http.MethodGet, "/api/cameras/cam-1/stream/seg-1.ts", nil)
	r.Header.Set("Authorization", "Bearer "+bearer)
	r.AddCookie(&http.Cookie{Name: StreamCookieName, Value: cookie})
	require.Equal(t, bearer, bearerSessionToken(r), "Authorization header wins over cookie")
}

func TestSessionTokenFromRequestCookieForm(t *testing.T) {
	t.Parallel()
	r := httptest.NewRequest(http.MethodGet, "/x/seg.ts", nil)
	r.AddCookie(&http.Cookie{Name: StreamCookieName, Value: SessionTokenPrefix + "abc"})
	require.Equal(t, SessionTokenPrefix+"abc", SessionTokenFromRequest(r))
}
