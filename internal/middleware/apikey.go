package middleware

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"strings"
)

// APIKeyPrefix identifies MiBeeVision API keys.
const APIKeyPrefix = "mbv_"

// apiKeyContextKey is the context key for the authenticated API key name.
type apiKeyContextKey struct{}

// APIKeyAuthMiddleware validates Bearer tokens with the "mbv_" prefix against
// a set of configured API keys. It runs alongside BasicAuth — if the request
// has a Bearer token, API Key auth is attempted first; otherwise BasicAuth
// handles it. This allows MiBeeVision to use API Keys while regular users
// continue using BasicAuth.
//
// validKeys is a map of key → name (for logging/audit). The middleware does
// constant-time comparison to prevent timing attacks.
func APIKeyAuthMiddleware(validKeys map[string]string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Bearer "+APIKeyPrefix) {
			next.ServeHTTP(w, r)
			return
		}

		token := strings.TrimPrefix(auth, "Bearer ")
		// Constant-time key lookup
		var keyName string
		var matched bool
		for k, name := range validKeys {
			if subtle.ConstantTimeCompare([]byte(token), []byte(k)) == 1 {
				keyName = name
				matched = true
				break
			}
		}

		if !matched {
			// Also check ?api_key= for SSE/WebSocket compatibility
			if qk := r.URL.Query().Get("api_key"); strings.HasPrefix(qk, APIKeyPrefix) {
				for k, name := range validKeys {
					if subtle.ConstantTimeCompare([]byte(qk), []byte(k)) == 1 {
						keyName = name
						matched = true
						break
					}
				}
			}
		}

		if !matched {
			recordAuthAttempt("failure")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"error":"invalid API key"}`))
			return
		}

		recordAuthAttempt("success")
		ctx := context.WithValue(r.Context(), apiKeyContextKey{}, keyName)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// APIKeyNameFromContext returns the authenticated API key name, or empty string.
func APIKeyNameFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(apiKeyContextKey{}).(string); ok {
		return v
	}
	return ""
}

// WithAPIKeyName returns a derived context carrying the API-key name, mirroring
// what APIKeyAuthMiddleware sets. Exported so tests (and only tests) can exercise
// handlers that gate on IsAPIKeyAuthenticated without standing up the full key
// map + middleware chain. Production code should authenticate via the middleware.
func WithAPIKeyName(ctx context.Context, name string) context.Context {
	return context.WithValue(ctx, apiKeyContextKey{}, name)
}

// IsAPIKeyAuthenticated reports whether the request was authenticated via API Key.
func IsAPIKeyAuthenticated(ctx context.Context) bool {
	return APIKeyNameFromContext(ctx) != ""
}

// GenerateAPIKey creates a new random API key with the mbv_ prefix.
// Returns a 40-char hex string prefixed with "mbv_" (44 chars total).
func GenerateAPIKey() string {
	b := make([]byte, 20)
	_, _ = rand.Read(b)
	return APIKeyPrefix + hex.EncodeToString(b)
}
