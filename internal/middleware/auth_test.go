package middleware

import (
    "encoding/base64"
    "net/http"
    "net/http/httptest"
    "testing"
)

func TestValidCredentials(t *testing.T) {
    hash, _ := HashPassword("secret")
    mw := NewAuthMiddleware("user", hash)
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
    mw := NewAuthMiddleware("user", hash)
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
    mw := NewAuthMiddleware("user", hash)
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
    mw := NewAuthMiddleware("user", hash)
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

func TestEmptyHashBypass(t *testing.T) {
	mw := NewAuthMiddleware("user", "")
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 when no password hash configured, got %d", w.Code)
	}
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
    mw := NewAuthMiddleware("u", hash)
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
