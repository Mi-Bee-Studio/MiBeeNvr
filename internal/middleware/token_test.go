package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestSessionTokenRoundTrip signs a token and verifies it decodes back to the
// same subject. This is the baseline happy path for the whole scheme.
func TestSessionTokenRoundTrip(t *testing.T) {
	hash := "$2a$10$placeholderhashfortestvalue123456789012345678901234567890"
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)

	token, expiresAt := SignSessionToken("admin", hash, now)
	require.True(t, strings.HasPrefix(token, "mbs_"), "token must carry the mbs_ prefix")
	require.Equal(t, now.Add(TokenTTL).Unix(), expiresAt.Unix())

	claims, err := VerifySessionToken(token, hash, now)
	require.NoError(t, err)
	require.Equal(t, "admin", claims.Sub)
	require.Equal(t, now.Unix(), claims.IAT)
	require.Equal(t, expiresAt.Unix(), claims.EXP)
	require.NotEmpty(t, claims.JTI, "jti nonce must be present")
}

// TestSessionTokenExpired is the reason the scheme is safe to keep in localStorage
// across browser restarts: an expired token is rejected regardless of signature.
func TestSessionTokenExpired(t *testing.T) {
	hash := "$2a$10$placeholderhashfortestvalue123456789012345678901234567890"
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	token, _ := SignSessionToken("admin", hash, now)

	later := now.Add(TokenTTL + time.Second)
	_, err := VerifySessionToken(token, hash, later)
	require.ErrorIs(t, err, ErrInvalidToken)
}

// TestSessionTokenPasswordChangeInvalidates ensures that when the user changes
// their password (→ new bcrypt hash), every token signed under the old hash
// stops validating. This is the property that replaces a server-side revocation
// list: there is no need to track "logged-out" tokens because changing the
// password key rolls them all.
func TestSessionTokenPasswordChangeInvalidates(t *testing.T) {
	oldHash := "$2a$10$oldhashplaceholderfortestvalue12345678901234567890123456"
	newHash := "$2a$10$newhashplaceholderfortestvalue12345678901234567890123456"
	now := time.Now()

	token, _ := SignSessionToken("admin", oldHash, now)
	_, err := VerifySessionToken(token, newHash, now)
	require.ErrorIs(t, err, ErrInvalidToken, "token signed with old hash must not validate under new hash")

	// Sanity: it still validates under the original hash.
	_, err = VerifySessionToken(token, oldHash, now)
	require.NoError(t, err)
}

// TestSessionTokenTamperRejected covers the core forgery defense: mutating the
// payload (or signature) breaks the HMAC check.
func TestSessionTokenTamperRejected(t *testing.T) {
	hash := "$2a$10$placeholderhashfortestvalue123456789012345678901234567890"
	now := time.Now()
	token, _ := SignSessionToken("admin", hash, now)

	// Flip a character in the payload region. We must avoid touching the prefix.
	tampered := token[:len(SessionTokenPrefix)+5] + "X" + token[len(SessionTokenPrefix)+6:]
	_, err := VerifySessionToken(tampered, hash, now)
	require.ErrorIs(t, err, ErrInvalidToken)
}

// TestSessionTokenUniqueJTI guards the "every renewal differs" property: two
// tokens signed in the same second for the same user must still differ, because
// the frontend relies on X-Renewed-Token carrying a genuinely new token.
func TestSessionTokenUniqueJTI(t *testing.T) {
	hash := "$2a$10$placeholderhashfortestvalue123456789012345678901234567890"
	now := time.Now()
	t1, _ := SignSessionToken("admin", hash, now)
	t2, _ := SignSessionToken("admin", hash, now)
	require.NotEqual(t, t1, t2, "two tokens in the same second must still differ (random jti)")
}

// TestSessionTokenBadPrefix rejects anything that doesn't carry the mbs_ prefix
// (notably mbv_ API keys and legacy base64(user:pass) strings).
func TestSessionTokenBadPrefix(t *testing.T) {
	hash := "$2a$10$placeholderhashfortestvalue123456789012345678901234567890"
	_, err := VerifySessionToken("mbv_xyz", hash, time.Now())
	require.ErrorIs(t, err, ErrInvalidToken)

	// No dot separator.
	_, err = VerifySessionToken("mbs_onlypayloadnosig", hash, time.Now())
	require.ErrorIs(t, err, ErrInvalidToken)
}

// TestBearerSessionTokenExtraction confirms the two surfaces a browser uses
// (Authorization header for normal calls, ?token= for WS/sendBeacon) are both
// recognized, and that non-session tokens (legacy base64) are left alone.
func TestBearerSessionTokenExtraction(t *testing.T) {
	hash := "$2a$10$placeholderhashfortestvalue123456789012345678901234567890"
	tok, _ := SignSessionToken("admin", hash, time.Now())

	// 1. Bearer header.
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Authorization", "Bearer "+tok)
	require.Equal(t, tok, bearerSessionToken(r))

	// 2. ?token= query (WebSocket / sendBeacon path).
	r2 := httptest.NewRequest(http.MethodGet, "/?token="+tok, nil)
	require.Equal(t, tok, bearerSessionToken(r2))

	// 3. Legacy base64(user:pass) ?token= must NOT be picked up as a session token.
	r3 := httptest.NewRequest(http.MethodGet, "/?token=YWRtaW46cGFzcw==", nil)
	require.Equal(t, "", bearerSessionToken(r3))

	// 4. mbv_ API key in Authorization must not be picked up.
	r4 := httptest.NewRequest(http.MethodGet, "/", nil)
	r4.Header.Set("Authorization", "Bearer mbv_abcdef")
	require.Equal(t, "", bearerSessionToken(r4))
}

// TestNeedsRenewal covers the sliding-renewal threshold: tokens within the last
// RenewThreshold of their life get refreshed, others don't.
func TestNeedsRenewal(t *testing.T) {
	now := time.Now()
	require.True(t, NeedsRenewal(now.Add(RenewThreshold-time.Second), now), "under threshold → renew")
	require.False(t, NeedsRenewal(now.Add(RenewThreshold+time.Minute), now), "over threshold → keep")
}

// TestAuthMiddlewareBearerSession is the integration test for the end-to-end
// path the browser actually uses after login: send Bearer mbs_..., get 200,
// and crucially get NO bcrypt computation (the token path must short-circuit).
func TestAuthMiddlewareBearerSession(t *testing.T) {
	hash, _ := HashPassword("secret")
	mw, _ := NewAuthMiddleware(staticProvider("admin", hash), "", AuthRateLimitConfig{})

	called := false
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	token, _ := SignSessionToken("admin", hash, time.Now())
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	require.True(t, called, "handler must be invoked for a valid session token")
	require.Equal(t, http.StatusOK, w.Code)
}

// TestAuthMiddlewareBearerSessionWrongPasswordChange guarantees that after a
// password change, old browser tokens stop authenticating — the user is forced
// to log in again. This is the closest thing to "logout-everywhere" the
// stateless scheme provides.
func TestAuthMiddlewareBearerSessionPasswordChanged(t *testing.T) {
	oldHash, _ := HashPassword("oldpw")
	mw, _ := NewAuthMiddleware(staticProvider("admin", oldHash), "", AuthRateLimitConfig{})

	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// Token signed under the OLD hash, but the middleware now serves a NEW hash.
	oldToken, _ := SignSessionToken("admin", oldHash, time.Now())
	newHash, _ := HashPassword("newpw")
	// Swap the provider's hash to simulate a password change in config.
	mwChanged, _ := NewAuthMiddleware(staticProvider("admin", newHash), "", AuthRateLimitConfig{})
	handler = mwChanged(handler)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+oldToken)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	require.Equal(t, http.StatusUnauthorized, w.Code, "stale token must be rejected after password change")
}

// TestAuthMiddlewareBearerSessionQueryToken covers the WebSocket / sendBeacon
// surface, which cannot set headers and instead passes ?token=mbs_...
func TestAuthMiddlewareBearerSessionQueryToken(t *testing.T) {
	hash, _ := HashPassword("secret")
	mw, _ := NewAuthMiddleware(staticProvider("admin", hash), "", AuthRateLimitConfig{})
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	token, _ := SignSessionToken("admin", hash, time.Now())
	req := httptest.NewRequest(http.MethodGet, "/?token="+token, nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code, "?token=mbs_ must authenticate (WS path)")
}

// TestAuthMiddlewareLegacyBasicAuthStillWorks guards backward compatibility:
// external scripts and the login call itself still use BasicAuth, which must
// keep working alongside the new Bearer path.
func TestAuthMiddlewareLegacyBasicAuthStillWorks(t *testing.T) {
	hash, _ := HashPassword("secret")
	mw, _ := NewAuthMiddleware(staticProvider("admin", hash), "", AuthRateLimitConfig{})
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Basic "+basic("admin", "secret"))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
}

// TestAuthMiddlewareLegacyQueryBase64TokenStillWorks guards the migration window:
// old browsers still holding base64(user:pass) in their ?token= continue to work
// until the next login, which swaps them for a signed token.
func TestAuthMiddlewareLegacyQueryBase64TokenStillWorks(t *testing.T) {
	hash, _ := HashPassword("secret")
	mw, _ := NewAuthMiddleware(staticProvider("admin", hash), "", AuthRateLimitConfig{})
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/?token="+basic("admin", "secret"), nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code, "legacy base64 ?token= must still work during migration")
}

// TestAuthMiddlewareRenewsExpiringToken checks that a token within the renewal
// window causes the middleware to attach a fresh token in X-Renewed-Token, and
// that the renewed token itself validates.
func TestAuthMiddlewareRenewsExpiringToken(t *testing.T) {
	hash, _ := HashPassword("secret")
	mw, _ := NewAuthMiddleware(staticProvider("admin", hash), "", AuthRateLimitConfig{})
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// Sign a token that is about to expire (5s of life left, well under RenewThreshold).
	issuedAt := time.Now().Add(-(TokenTTL - 5 * time.Second))
	token, _ := SignSessionToken("admin", hash, issuedAt)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	renewed := w.Header().Get(RenewedTokenHeader)
	require.NotEmpty(t, renewed, "expiring token must trigger a renewal header")

	// The renewed token must validate under the same hash.
	_, err := VerifySessionToken(renewed, hash, time.Now())
	require.NoError(t, err, "renewed token must be valid")
}
