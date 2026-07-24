package middleware

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Session token (a.k.a. signed-token) authentication.
//
// This replaces the legacy "base64(user:pass) in sessionStorage" scheme, which
// stored a reversible encoding of the plaintext password in the browser and was
// scoped to a single tab. The new scheme issues a stateless HMAC-SHA256 signed
// token that the browser keeps in localStorage (so it survives new tabs and
// browser restarts) and that NEVER contains the password — only a username plus
// expiry. The server validates it purely by recomputing the HMAC; there is no
// session store, no revocation list, no DB table.
//
// Token format:  mbs_<base64url(payload)>.<base64url(signature)>
//   - "mbs_" (MiBee Session) prefix separates it from "mbv_" API keys.
//   - payload = { sub (username), iat, exp, jti (random nonce) } — JSON, NOT
//     encrypted (confidentiality comes from the password never being in it).
//   - signing key = HMAC-SHA256(pepper, passwordBcryptHash). The pepper is a
//     random 32-byte value generated once per process start (see init below) and
//     kept only in memory, so:
//       • process restart  → pepper changes → all tokens invalidated (clean slate)
//       • password changed → bcrypt hash changes → that user's tokens invalidated
//       • config file leak alone CANNOT forge tokens (still needs the in-memory pepper)
//
// Sliding renewal: when a presented token has less than RenewThreshold lifetime
// left, the auth middleware signs a fresh token and returns it via the
// X-Renewed-Token response header; the frontend swaps it in silently. There is
// deliberately NO revocation/refresh endpoint — revocation is impossible without
// server-side state, which the project's stateless philosophy forbids. The risk
// window for a leaked token is therefore bounded only by TokenTTL.

// SessionTokenPrefix identifies browser session tokens (as opposed to "mbv_"
// API keys used by MiBeeVision).
const SessionTokenPrefix = "mbs_"

// TokenTTL is how long a signed session token remains valid.
const TokenTTL = 2 * time.Hour

// RenewThreshold is the remaining-lifetime below which the middleware issues a
// fresh token in the X-Renewed-Token response header (sliding renewal).
const RenewThreshold = 15 * time.Minute

// RenewedTokenHeader is the response header carrying a freshly-signed token on
// sliding renewal.
const RenewedTokenHeader = "X-Renewed-Token"

// pepper is a per-process random key mixed into the HMAC signing key. It is
// generated once at startup and never persisted, so restarting the binary
// invalidates every outstanding token. This is intentional: the only "state"
// the token scheme requires lives in memory and is wiped on restart.
var pepper []byte

func init() {
	pepper = make([]byte, 32)
	if _, err := rand.Read(pepper); err != nil {
		// rand.Read on Linux/Windows/macOS only errors when the system CSPRNG is
		// unavailable, which means the host is already broken for any crypto use.
		// Failing fast at startup is the safe choice — never continue with a zero
		// key, which would let anyone forge tokens.
		panic(fmt.Sprintf("middleware: failed to generate token pepper: %v", err))
	}
}

// tokenClaims is the JSON payload embedded in the token (base64url-encoded, not
// encrypted). Only non-secret fields are stored.
type tokenClaims struct {
	Sub string `json:"sub"` // username (subject)
	IAT int64  `json:"iat"` // issued-at, unix seconds
	EXP int64  `json:"exp"` // expiry, unix seconds
	JTI string `json:"jti"` // random nonce — guarantees every signed token differs
}

// ErrInvalidToken is returned by Verify for any malformed, tampered, or
// expired token. Callers should treat all variants identically (reject).
var ErrInvalidToken = errors.New("invalid session token")

// SignSessionToken mints a new session token for the given username.
// passwordBcryptHash is the user's current bcrypt hash (the very same value the
// auth middleware already reads on every request via AuthProvider.GetHash); it
// is folded into the signing key so changing the password invalidates old tokens
// without any revocation list.
//
// The token expires at TokenTTL from now. The returned expiry is the absolute
// time for the caller to hand back to clients (e.g. login response body).
func SignSessionToken(username, passwordBcryptHash string, now time.Time) (token string, expiresAt time.Time) {
	expiresAt = now.Add(TokenTTL)
	jti := randNonce(16)
	claims := tokenClaims{
		Sub: username,
		IAT: now.Unix(),
		EXP: expiresAt.Unix(),
		JTI: jti,
	}
	payloadJSON, _ := json.Marshal(claims) // json.Marshal on this struct cannot fail
	payloadB64 := base64.RawURLEncoding.EncodeToString(payloadJSON)

	sig := signingMAC(passwordBcryptHash, []byte(payloadB64))
	sigB64 := base64.RawURLEncoding.EncodeToString(sig)

	return SessionTokenPrefix + payloadB64 + "." + sigB64, expiresAt
}

// VerifySessionToken validates a token's signature and expiry and returns the
// parsed claims. Any failure (bad format, tampered payload, wrong key, expired)
// returns ErrInvalidToken. now lets tests inject time.
func VerifySessionToken(token, passwordBcryptHash string, now time.Time) (*tokenClaims, error) {
	if !strings.HasPrefix(token, SessionTokenPrefix) {
		return nil, ErrInvalidToken
	}
	body := token[len(SessionTokenPrefix):]
	dot := strings.IndexByte(body, '.')
	if dot < 0 {
		return nil, ErrInvalidToken
	}
	payloadB64 := body[:dot]
	sigB64 := body[dot+1:]

	wantSig := signingMAC(passwordBcryptHash, []byte(payloadB64))
	gotSig, err := base64.RawURLEncoding.DecodeString(sigB64)
	if err != nil {
		return nil, ErrInvalidToken
	}
	// Constant-time compare of the full MAC; length mismatch still returns 0 and
	// is rejected. This guards against timing oracles on the signature.
	if !hmac.Equal(wantSig, gotSig) {
		return nil, ErrInvalidToken
	}

	payloadJSON, err := base64.RawURLEncoding.DecodeString(payloadB64)
	if err != nil {
		return nil, ErrInvalidToken
	}
	var claims tokenClaims
	if err := json.Unmarshal(payloadJSON, &claims); err != nil {
		return nil, ErrInvalidToken
	}
	if now.Unix() >= claims.EXP {
		return nil, ErrInvalidToken
	}
	return &claims, nil
}

// NeedsRenewal reports whether a token with the given expiry should be renewed
// at the current time (i.e. its remaining life is under RenewThreshold).
func NeedsRenewal(expiresAt, now time.Time) bool {
	return expiresAt.Sub(now) <= RenewThreshold
}

// signingMAC derives the per-user signing key (HMAC(pepper, bcryptHash)) and
// returns HMAC-SHA256(key, msg). The double-HMAC means an attacker who learns
// one token's signature learns nothing reusable about the pepper or the bcrypt
// hash, and cannot forge a signature for a different payload.
func signingMAC(passwordBcryptHash string, msg []byte) []byte {
	key := hmacSHA256(pepper, []byte(passwordBcryptHash))
	return hmacSHA256(key, msg)
}

func hmacSHA256(key, msg []byte) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write(msg)
	return mac.Sum(nil)
}

// randNonce returns n random bytes hex-encoded (URL-safe, compact). It is used
// only to guarantee that two tokens signed at the same second for the same user
// still differ byte-for-byte; it is not a secret.
func randNonce(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		// Should not happen on a healthy host; return a deterministic fallback so
		// the token is still valid (just not unique). Uniqueness is a nice-to-have,
		// signature validity is the hard requirement.
		return "00000000000000000000000000000000"
	}
	return hex.EncodeToString(b)
}

// IsSessionToken reports whether s looks like a session token (mbs_ prefix).
// Used by the auth middleware to route ?token= query params to the right path
// without trying to base64-decode them.
func IsSessionToken(s string) bool {
	return strings.HasPrefix(s, SessionTokenPrefix)
}

// bearerSessionToken extracts a session token (mbs_...) from a request, in
// either of the two forms the frontend uses:
//   - Authorization: Bearer mbs_...   (normal API calls)
//   - ?token=mbs_...                  (WebSocket upgrades + sendBeacon, which
//     cannot set headers)
//
// Returns "" when no session token is present (the request then falls through
// to API-Key / BasicAuth / legacy base64 ?token= handling).
func bearerSessionToken(r *http.Request) string {
	if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer "+SessionTokenPrefix) {
		return strings.TrimPrefix(auth, "Bearer ")
	}
	if tok := r.URL.Query().Get("token"); IsSessionToken(tok) {
		return tok
	}
	return ""
}


