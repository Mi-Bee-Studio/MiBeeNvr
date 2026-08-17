package middleware

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"strings"
	"sync"
	"time"
)

// APIKeyPrefix identifies MiBeeVision API keys.
const APIKeyPrefix = "mbv_"

// apiKeyContextKey is the context key for the authenticated API key name.
type apiKeyContextKey struct{}

// lastUsedThrottle bounds how often a key's last-used timestamp is written.
// Under bursty load (e.g. an AI consumer polling) this keeps the store lock
// uncontended while keeping the reported timestamp accurate to ~1 minute.
const lastUsedThrottle = time.Minute

// APIKeyStore is the live set of valid API keys plus per-key last-used
// timestamps. It replaces the static map snapshot previously captured at
// router-build time, so minting and revoking keys take effect on the next
// request without a service restart (#335).
type APIKeyStore struct {
	mu       sync.RWMutex
	keys     map[string]string    // token → key name
	lastUsed map[string]time.Time // key name → last successful auth
}

// NewAPIKeyStore returns an empty store.
func NewAPIKeyStore() *APIKeyStore {
	return &APIKeyStore{
		keys:     make(map[string]string),
		lastUsed: make(map[string]time.Time),
	}
}

// SetKeys atomically replaces the valid key set (token → name).
func (s *APIKeyStore) SetKeys(keys map[string]string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.keys = keys
}

// Lookup resolves a token to its key name. Usage timestamps are recorded with
// a per-key throttle; comparisons stay constant-time like the previous
// static-map implementation.
func (s *APIKeyStore) Lookup(token string) (string, bool) {
	s.mu.RLock()
	var name string
	var matched bool
	for k, n := range s.keys {
		if subtle.ConstantTimeCompare([]byte(token), []byte(k)) == 1 {
			name = n
			matched = true
			break
		}
	}
	var last time.Time
	if matched {
		last = s.lastUsed[name]
	}
	s.mu.RUnlock()
	if !matched {
		return "", false
	}
	if time.Since(last) > lastUsedThrottle {
		s.mu.Lock()
		s.lastUsed[name] = time.Now()
		s.mu.Unlock()
	}
	return name, true
}

// LastUsed returns a copy of the key-name → last-used timestamps.
func (s *APIKeyStore) LastUsed() map[string]time.Time {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]time.Time, len(s.lastUsed))
	for k, v := range s.lastUsed {
		out[k] = v
	}
	return out
}

// APIKeyAuthMiddleware validates Bearer tokens with the "mbv_" prefix against
// the live API key store. It runs alongside BasicAuth — if the request has a
// Bearer token, API Key auth is attempted first; otherwise BasicAuth handles
// it. This allows MiBeeVision (and per-device app tokens) to use API Keys
// while regular users continue using BasicAuth.
func APIKeyAuthMiddleware(store *APIKeyStore, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if store == nil {
			next.ServeHTTP(w, r)
			return
		}
		auth := r.Header.Get("Authorization")
		var token string
		if strings.HasPrefix(auth, "Bearer "+APIKeyPrefix) {
			token = strings.TrimPrefix(auth, "Bearer ")
		} else if qk := r.URL.Query().Get("api_key"); strings.HasPrefix(qk, APIKeyPrefix) {
			// ?api_key= for SSE/WebSocket clients that cannot set headers.
			// Checked on its own — previously it only ran as a fallback after
			// a failed Bearer match, so query-param-only requests never
			// authenticated.
			token = qk
		} else {
			next.ServeHTTP(w, r)
			return
		}

		keyName, matched := store.Lookup(token)
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
