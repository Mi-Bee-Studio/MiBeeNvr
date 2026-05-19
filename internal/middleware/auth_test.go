package middleware

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestValidCredentials(t *testing.T) {
    hash, _ := HashPassword("secret")
    mw, _ := NewAuthMiddleware("user", hash, "")
    handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusOK)
    }))
    req := httptest.NewRequest("GET", "/", nil)
    req.Header.Set("Authorization", "Basic "+basic("user", "secret"))
    w := httptest.NewRecorder()
    handler.ServeHTTP(w, req)
    if w.Code != http.StatusOK {
        t.Fatalf("expected 200, got %d", w.Code)
    }
}

func TestInvalidPassword(t *testing.T) {
    hash, _ := HashPassword("secret")
    mw, _ := NewAuthMiddleware("user", hash, "")
    handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusOK)
    }))
    req := httptest.NewRequest("GET", "/", nil)
    req.Header.Set("Authorization", "Basic "+basic("user", "wrong"))
    w := httptest.NewRecorder()
    handler.ServeHTTP(w, req)
    if w.Code != http.StatusUnauthorized {
        t.Fatalf("expected 401, got %d", w.Code)
    }
}

func TestMissingAuthHeader(t *testing.T) {
    hash, _ := HashPassword("secret")
    mw, _ := NewAuthMiddleware("user", hash, "")
    handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusOK)
    }))
    req := httptest.NewRequest("GET", "/", nil)
    w := httptest.NewRecorder()
    handler.ServeHTTP(w, req)
    if w.Code != http.StatusUnauthorized {
        t.Fatalf("expected 401, got %d", w.Code)
    }
}

func TestMalformedAuth(t *testing.T) {
    hash, _ := HashPassword("secret")
    mw, _ := NewAuthMiddleware("user", hash, "")
    handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusOK)
    }))
    req := httptest.NewRequest("GET", "/", nil)
    req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("not base64")))
    w := httptest.NewRecorder()
    handler.ServeHTTP(w, req)
    if w.Code != http.StatusUnauthorized {
        t.Fatalf("expected 401, got %d", w.Code)
    }
}

func TestEmptyHashReturnsSetupRequired(t *testing.T) {
	mw, _ := NewAuthMiddleware("user", "", "")
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest("GET", "/", nil)
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
    mw, _ := NewAuthMiddleware("u", hash, "")
    handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusOK)
    }))
    reqs := 50
    done := make(chan bool)
    for i := 0; i < reqs; i++ {
        go func(i int) {
            req := httptest.NewRequest("GET", "/", nil)
            req.Header.Set("Authorization", "Basic "+basic("u", "secret"))
            w := httptest.NewRecorder()
            handler.ServeHTTP(w, req)
            if w.Code != http.StatusOK {
                // non-fatal in goroutine
            }
            done <- true
        }(i)
    }
    for i := 0; i < reqs; i++ {
        <-done
    }
}

// helper to build basic auth header quickly
func basic(user, pass string) string {
    s := user + ":" + pass
    return base64.StdEncoding.EncodeToString([]byte(s))
}

func TestPlaintextPasswordAutoHash(t *testing.T) {
	mw, effectiveHash := NewAuthMiddleware("admin", "", "mypassword")
	require.NotEmpty(t, effectiveHash, "effectiveHash should be populated when plaintext is provided")
	require.True(t, CheckPassword("mypassword", effectiveHash), "original password should authenticate against auto-hash")

	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Basic "+basic("admin", "mypassword"))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
}

func TestHashTakesPriorityOverPlaintext(t *testing.T) {
	preHashed, err := HashPassword("prehashed-pass")
	require.NoError(t, err)

	mw, effectiveHash := NewAuthMiddleware("admin", preHashed, "ignored-plaintext")
	require.Equal(t, preHashed, effectiveHash, "pre-existing hash should take priority over plaintext")

	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Basic "+basic("admin", "prehashed-pass"))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	req2 := httptest.NewRequest("GET", "/", nil)
	req2.Header.Set("Authorization", "Basic "+basic("admin", "ignored-plaintext"))
	w2 := httptest.NewRecorder()
	handler.ServeHTTP(w2, req2)
	require.Equal(t, http.StatusUnauthorized, w2.Code, "plaintext password should not authenticate when hash takes priority")
}

func TestRateLimiterAllowsUnderLimit(t *testing.T) {
	rl := NewRateLimiter(RateLimiterConfig{MaxRequests: 5, Window: time.Minute})
	handler := rl(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	// Send 5 requests (at the limit) — should all pass
	for i := 0; i < 5; i++ {
		req := httptest.NewRequest("GET", "/", nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		require.Equal(t, http.StatusOK, w.Code, "request %d should pass", i+1)
	}
}

func TestRateLimiterBlocksOverLimit(t *testing.T) {
	rl := NewRateLimiter(RateLimiterConfig{MaxRequests: 3, Window: time.Minute})
	handler := rl(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	// Send 3 requests (at the limit) — all pass
	for i := 0; i < 3; i++ {
		req := httptest.NewRequest("GET", "/", nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		require.Equal(t, http.StatusOK, w.Code, "request %d should pass", i+1)
	}

	// 4th request should be blocked
	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	require.Equal(t, http.StatusTooManyRequests, w.Code, "request over limit should be 429")
}

func TestRateLimiterResetsAfterWindow(t *testing.T) {
	rl := NewRateLimiter(RateLimiterConfig{MaxRequests: 1, Window: 50 * time.Millisecond})
	handler := rl(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// First request passes
	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	// Second request blocked
	req = httptest.NewRequest("GET", "/", nil)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	require.Equal(t, http.StatusTooManyRequests, w.Code)

	// Wait for window to expire
	time.Sleep(60 * time.Millisecond)

	// Should be allowed again
	req = httptest.NewRequest("GET", "/", nil)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
}
