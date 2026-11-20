package middleware

import (
	"net/http"
	"strings"
)

// SetStreamCookieOnPlaylist sets the mbs_session cookie when the request is
// for an HLS playlist (path ends in .m3u8) and was authenticated with a
// session token. A freshly-renewed token (X-Renewed-Token) takes precedence
// so the cookie outlives the original token's remaining lifetime.
// API-key callers (#706) get the same channel with the key itself as the
// cookie value: relative segment URLs do not inherit the playlist's
// ?api_key= query, and players like iOS AVPlayer cannot attach headers to
// every segment request. The value is only echoed when the auth middleware
// actually validated the key (IsAPIKeyAuthenticated), and the cookie stays
// scoped to this camera's subtree + GET/HEAD media fetches — no more power
// than the ?api_key= playlist URL already granted.
func SetStreamCookieOnPlaylist(w http.ResponseWriter, r *http.Request, cameraID string) {
	if !strings.HasSuffix(r.URL.Path, ".m3u8") {
		return
	}
	tok := SessionTokenFromRequest(r)
	if tok == "" {
		if !IsAPIKeyAuthenticated(r.Context()) {
			return // BasicAuth/unauthenticated callers: nothing to hand out
		}
		key := APIKeyFromRequest(r)
		if key == "" {
			return
		}
		http.SetCookie(w, &http.Cookie{
			Name:     StreamCookieName,
			Value:    key,
			Path:     "/api/cameras/" + cameraID + "/",
			MaxAge:   int(StreamCookieAPIKeyTTL.Seconds()),
			HttpOnly: true,
			SameSite: http.SameSiteLaxMode,
			Secure:   r.TLS != nil,
		})
		return
	}
	if renewed := w.Header().Get(RenewedTokenHeader); renewed != "" {
		tok = renewed
	}
	http.SetCookie(w, &http.Cookie{
		Name:     StreamCookieName,
		Value:    tok,
		Path:     "/api/cameras/" + cameraID + "/",
		MaxAge:   int(TokenTTL.Seconds()),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   r.TLS != nil,
	})
}
