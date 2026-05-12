package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestSecurityHeaders(t *testing.T) {
	handler := SecurityHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	tests := []struct {
		header string
		want   string
	}{
		{"X-Content-Type-Options", "nosniff"},
		{"X-Frame-Options", "DENY"},
		{"X-XSS-Protection", "1; mode=block"},
		{"Referrer-Policy", "strict-origin-when-cross-origin"},
		{"Content-Security-Policy", "default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; img-src blob: 'self'; media-src blob: 'self'"},
	}
	for _, tt := range tests {
		got := w.Header().Get(tt.header)
		if got != tt.want {
			t.Errorf("header %s = %q, want %q", tt.header, got, tt.want)
		}
	}
}

func TestRateLimitBlocksAfterMaxFailures(t *testing.T) {
	ResetAuthFailures()
	hash, _ := HashPassword("secret")
	mw, _ := NewAuthMiddleware("user", hash, "")
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// Send authMaxFailures failed requests
	for i := 0; i < authMaxFailures; i++ {
		req := httptest.NewRequest("GET", "/", nil)
		req.Header.Set("Authorization", "Basic "+basic("user", "wrong"))
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("request %d: expected 401, got %d", i+1, w.Code)
		}
		// Small delay to ensure proper timing
		time.Sleep(10 * time.Millisecond)
	}

	// Next failed request should be 429 (rate limiter checks BEFORE incrementing)
	// After 20 failures, the 21st request should be blocked
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Basic "+basic("user", "wrong"))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429 after 20 failures, got %d", w.Code)
	}
}
func TestRateLimitDoesNotBlockValidAuthAfterFailures(t *testing.T) {
	ResetAuthFailures()
	hash, _ := HashPassword("secret")
	mw, _ := NewAuthMiddleware("user", hash, "")
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// Send some failed requests but don't hit the limit
	for i := 0; i < authMaxFailures-5; i++ {
		req := httptest.NewRequest("GET", "/", nil)
		req.Header.Set("Authorization", "Basic "+basic("user", "wrong"))
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
	}

	// Valid auth should still work
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Basic "+basic("user", "secret"))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("valid auth should succeed, got %d", w.Code)
	}
}

func TestRateLimitResetsOnSuccess(t *testing.T) {
	ResetAuthFailures()
	hash, _ := HashPassword("secret")
	mw, _ := NewAuthMiddleware("user", hash, "")
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// Send 15 failures (below limit)
	for i := 0; i < 15; i++ {
		req := httptest.NewRequest("GET", "/", nil)
		req.Header.Set("Authorization", "Basic "+basic("user", "wrong"))
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		// Small delay to ensure proper timing
		time.Sleep(10 * time.Millisecond)
	}

	// Successful auth resets the counter
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Basic "+basic("user", "secret"))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("valid auth failed: %d", w.Code)
	}

	// Should be able to try authMaxFailures more times
	for i := 0; i < authMaxFailures; i++ {
		req := httptest.NewRequest("GET", "/", nil)
		req.Header.Set("Authorization", "Basic "+basic("user", "wrong"))
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("after reset, request %d: expected 401, got %d", i+1, w.Code)
		}
	}

	// Now should be 429
	req = httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Basic "+basic("user", "wrong"))
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429 after reset + %d failures, got %d", authMaxFailures, w.Code)
	}
}

func TestExtractIP(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"192.168.1.1:12345", "192.168.1.1"},
		{"[::1]:8080", "[::1]"},
		{"10.0.0.1", "10.0.0.1"}, // no port
	}
	for _, tt := range tests {
		got := extractIP(tt.input)
		if got != tt.want {
			t.Errorf("extractIP(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
