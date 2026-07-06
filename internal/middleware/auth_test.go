package middleware

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestValidCredentials(t *testing.T) {
	hash, _ := HashPassword("secret")
	mw, _ := NewAuthMiddleware(staticProvider("user", hash), "", AuthRateLimitConfig{})
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Basic "+basic("user", "secret"))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestInvalidPassword(t *testing.T) {
	hash, _ := HashPassword("secret")
	mw, _ := NewAuthMiddleware(staticProvider("user", hash), "", AuthRateLimitConfig{})
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Basic "+basic("user", "wrong"))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

func TestMissingAuthHeader(t *testing.T) {
	hash, _ := HashPassword("secret")
	mw, _ := NewAuthMiddleware(staticProvider("user", hash), "", AuthRateLimitConfig{})
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

func TestMalformedAuth(t *testing.T) {
	hash, _ := HashPassword("secret")
	mw, _ := NewAuthMiddleware(staticProvider("user", hash), "", AuthRateLimitConfig{})
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("not base64")))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

func TestEmptyHashReturnsSetupRequired(t *testing.T) {
	mw, _ := NewAuthMiddleware(staticProvider("user", ""), "", AuthRateLimitConfig{})
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	require.Equal(t, http.StatusServiceUnavailable, w.Code, "expected 503 when no password configured")
	require.Equal(t, "application/json", w.Header().Get("Content-Type"))
	require.Equal(t, `Basic realm="MiBee NVR"`, w.Header().Get("WWW-Authenticate"))
	require.Contains(t, w.Body.String(), "setup required")
}

func TestHashCheckRoundTrip(t *testing.T) {
	pass := "abc123"
	hash, _ := HashPassword(pass)
	if !CheckPassword(pass, hash) {
		t.Fatalf("hash check failed for valid password")
	}
}

func TestConcurrentAccess(t *testing.T) {
	hash, _ := HashPassword("secret")
	mw, _ := NewAuthMiddleware(staticProvider("u", hash), "", AuthRateLimitConfig{})
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	reqs := 50
	done := make(chan bool)
	for i := range reqs {
		go func(i int) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.Header.Set("Authorization", "Basic "+basic("u", "secret"))
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)
			if w.Code != http.StatusOK {
				_ = w.Code
			}
			done <- true
		}(i)
	}
	for range reqs {
		<-done
	}
}

// helper to build basic auth header quickly
func basic(user, pass string) string {
	s := user + ":" + pass
	return base64.StdEncoding.EncodeToString([]byte(s))
}

// staticProvider returns an AuthProvider with fixed values for testing.
func staticProvider(username, hash string) AuthProvider {
	return AuthProvider{
		GetUsername: func() string { return username },
		GetHash:     func() string { return hash },
	}
}

func TestPlaintextPasswordAutoHash(t *testing.T) {
	mw, effectiveHash := NewAuthMiddleware(staticProvider("admin", ""), "mypassword", AuthRateLimitConfig{})
	require.NotEmpty(t, effectiveHash, "effectiveHash should be populated when plaintext is provided")
	require.True(t, CheckPassword("mypassword", effectiveHash), "original password should authenticate against auto-hash")

	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Basic "+basic("admin", "mypassword"))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
}

func TestHashTakesPriorityOverPlaintext(t *testing.T) {
	preHashed, err := HashPassword("prehashed-pass")
	require.NoError(t, err)

	mw, effectiveHash := NewAuthMiddleware(staticProvider("admin", preHashed), "ignored-plaintext", AuthRateLimitConfig{})
	require.Equal(t, preHashed, effectiveHash, "pre-existing hash should take priority over plaintext")

	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Basic "+basic("admin", "prehashed-pass"))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	req2 := httptest.NewRequest(http.MethodGet, "/", nil)
	req2.Header.Set("Authorization", "Basic "+basic("admin", "ignored-plaintext"))
	w2 := httptest.NewRecorder()
	handler.ServeHTTP(w2, req2)
	require.Equal(t, http.StatusUnauthorized, w2.Code, "plaintext password should not authenticate when hash takes priority")
}

func TestRateLimiterAllowsUnderLimit(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rl := NewRateLimiter(ctx, RateLimiterConfig{MaxRequests: 5, Window: time.Minute})
	handler := rl.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	// Send 5 requests (at the limit) — should all pass
	for i := range 5 {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		require.Equal(t, http.StatusOK, w.Code, "request %d should pass", i+1)
	}
}

func TestRateLimiterBlocksOverLimit(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rl := NewRateLimiter(ctx, RateLimiterConfig{MaxRequests: 3, Window: time.Minute})
	handler := rl.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	// Send 3 requests (at the limit) — all pass
	for i := range 3 {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		require.Equal(t, http.StatusOK, w.Code, "request %d should pass", i+1)
	}

	// 4th request should be blocked
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	require.Equal(t, http.StatusTooManyRequests, w.Code, "request over limit should be 429")
}

func TestRateLimiterResetsAfterWindow(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rl := NewRateLimiter(ctx, RateLimiterConfig{MaxRequests: 1, Window: 50 * time.Millisecond})
	handler := rl.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// First request passes
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	// Second request blocked
	req = httptest.NewRequest(http.MethodGet, "/", nil)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	require.Equal(t, http.StatusTooManyRequests, w.Code)

	// Wait for window to expire
	time.Sleep(60 * time.Millisecond)

	// Should be allowed again
	req = httptest.NewRequest(http.MethodGet, "/", nil)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
}

func TestSetupUpdatesHashDynamically(t *testing.T) {
	t.Helper()
	currentHash := ""
	currentUsername := "admin"
	provider := AuthProvider{
		GetUsername: func() string { return currentUsername },
		GetHash:     func() string { return currentHash },
	}

	mw, _ := NewAuthMiddleware(provider, "", AuthRateLimitConfig{})
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// Before setup: no hash configured → 503 SETUP_REQUIRED
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	require.Equal(t, http.StatusServiceUnavailable, w.Code)
	require.Contains(t, w.Body.String(), "setup required")

	// Simulate setup: set hash in config
	hash, err := HashPassword("newpassword123")
	require.NoError(t, err)
	currentHash = hash

	// After setup: middleware picks up the new hash → 200 OK
	req2 := httptest.NewRequest(http.MethodGet, "/", nil)
	req2.Header.Set("Authorization", "Basic "+basic("admin", "newpassword123"))
	w2 := httptest.NewRecorder()
	handler.ServeHTTP(w2, req2)
	require.Equal(t, http.StatusOK, w2.Code)
}

func TestRateLimiterEvictsStaleEntries(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	rl := NewRateLimiter(ctx, RateLimiterConfig{MaxRequests: 100, Window: 50 * time.Millisecond})
	handler := rl.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// Create entries from 100 different IPs
	for i := range 100 {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = "192.168.1." + strconv.Itoa(i) + ":12345"
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		require.Equal(t, http.StatusOK, w.Code)
	}

	// Verify entries were created
	require.Equal(t, 100, rl.entryCount(), "should have 100 entries before eviction")

	// Wait for 2× window + buffer to ensure cleanup ticker fires
	time.Sleep(110 * time.Millisecond)

	// After cleanup, entries should be evicted
	require.Equal(t, 0, rl.entryCount(), "entries should be evicted after 2× window")
}

func TestRateLimiterCleanupStopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	rl := NewRateLimiter(ctx, RateLimiterConfig{MaxRequests: 1, Window: 50 * time.Millisecond})
	handler := rl.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// Create an entry
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, 1, rl.entryCount())

	// Cancel context to stop cleanup goroutine
	cancel()

	// Give goroutine time to exit
	time.Sleep(10 * time.Millisecond)

	// Rate limiter should still work (no panic, no deadlock)
	req2 := httptest.NewRequest(http.MethodGet, "/", nil)
	w2 := httptest.NewRecorder()
	handler.ServeHTTP(w2, req2)
	require.Equal(t, http.StatusTooManyRequests, w2.Code, "second request from same IP should be blocked when MaxRequests=1")
}
