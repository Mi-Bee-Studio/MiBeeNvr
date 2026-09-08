package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The stream cookie (mbs_session) may also carry an API key for media
// fetches (#706): the eligibility gate (GET/HEAD + media extension) is shared
// with the session-token cookie path, and only mbv_-prefixed cookie values are
// routed into API-key validation — an mbs_ value falls through untouched.
func TestAPIKeyStreamCookie(t *testing.T) {
	t.Parallel()
	store := NewAPIKeyStore()
	validKey := APIKeyPrefix + strings.Repeat("k", 40)
	store.SetKeys(map[string]string{validKey: "dad-phone"})

	serve := func(method, target string, cookie *http.Cookie) (int, bool) {
		var authed bool
		next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			authed = IsAPIKeyAuthenticated(r.Context())
		})
		handler := APIKeyAuthMiddleware(store, next)
		req := httptest.NewRequest(method, target, nil)
		if cookie != nil {
			req.AddCookie(cookie)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec.Code, authed
	}

	valid := &http.Cookie{Name: StreamCookieName, Value: validKey}

	code, authed := serve(http.MethodGet, "/api/cameras/cam-1/stream/seg-4.ts", valid)
	require.Equal(t, http.StatusOK, code)
	require.True(t, authed, "segment fetch via api-key cookie must authenticate")

	code, authed = serve(http.MethodGet, "/api/cameras/cam-1/stream/index.m3u8", valid)
	require.Equal(t, http.StatusOK, code)
	require.True(t, authed, "playlist fetch via api-key cookie must authenticate")

	// Non-media path: the cookie must not authenticate (falls through to the
	// next middleware, e.g. session/BasicAuth, with no api-key context).
	_, authed = serve(http.MethodGet, "/api/recordings", valid)
	require.False(t, authed, "cookie must not authenticate non-media API paths")

	// POST to a media path is also outside the gate.
	_, authed = serve(http.MethodPost, "/api/cameras/cam-1/stream/seg-4.ts", valid)
	require.False(t, authed, "POST must not authenticate via cookie")

	// Unknown/revoked key in the cookie → explicit 401 (the key IS mbv_-shaped,
	// so it claims our auth path and fails it, rather than falling through).
	code, _ = serve(http.MethodGet, "/api/cameras/cam-1/stream/seg-4.ts",
		&http.Cookie{Name: StreamCookieName, Value: APIKeyPrefix + strings.Repeat("x", 40)})
	require.Equal(t, http.StatusUnauthorized, code)

	// mbs_ session tokens in the cookie are NOT API-key middleware's business.
	sess := &http.Cookie{Name: StreamCookieName, Value: "mbs_abc"}
	code, authed = serve(http.MethodGet, "/api/cameras/cam-1/stream/seg-4.ts", sess)
	require.Equal(t, http.StatusOK, code, "mbs_ cookie falls through, next handler still runs")
	require.False(t, authed, "mbs_ cookie must fall through to the session middleware")
}
