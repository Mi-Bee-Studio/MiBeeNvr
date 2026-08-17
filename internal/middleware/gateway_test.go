package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGatewayAuthMiddlewareExtractsIdentity(t *testing.T) {
	t.Helper()
	var got *GatewayIdentity
	h := GatewayAuthMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = GatewayIdentityFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	req.Header.Set("X-Trim-Userid", "1000")
	req.Header.Set("X-Trim-Username", "admin")
	req.Header.Set("X-Trim-Isadmin", "true")
	h.ServeHTTP(httptest.NewRecorder(), req)

	if got == nil {
		t.Fatal("expected identity in context")
	}
	if got.Username != "admin" || got.UserID != "1000" || !got.Admin {
		t.Fatalf("unexpected identity: %+v", got)
	}
}

func TestGatewayAuthMiddlewareNoHeaders(t *testing.T) {
	t.Helper()
	var got *GatewayIdentity
	h := GatewayAuthMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = GatewayIdentityFromContext(r.Context())
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	if got != nil {
		t.Fatalf("expected nil identity without headers, got %+v", got)
	}
}

// The auth middleware must let a gateway-verified ADMIN through without
// BasicAuth, but must NOT let a gateway non-admin (or no identity) through.
func TestAuthMiddlewareGatewayBypass(t *testing.T) {
	t.Helper()
	hash, err := HashPassword("test-password-1")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	authMW, _ := NewAuthMiddleware(AuthProvider{
		GetUsername: func() string { return "admin" },
		GetHash:     func() string { return hash },
	}, "", AuthRateLimitConfig{})

	cases := []struct {
		name    string
		ctxID   *GatewayIdentity
		want    int
		withHdr bool
	}{
		{"gateway admin bypasses", &GatewayIdentity{Username: "nas-admin", Admin: true}, http.StatusOK, false},
		{"gateway non-admin still needs auth", &GatewayIdentity{Username: "nas-user", Admin: false}, http.StatusUnauthorized, false},
		// Forged X-Trim-* headers WITHOUT the gateway context must be ignored
		// (this is the TCP-listener scenario).
		{"forged headers without context rejected", nil, http.StatusUnauthorized, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Helper()
			h := authMW(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			}))
			req := httptest.NewRequest(http.MethodGet, "/api/cameras", nil)
			if tc.withHdr {
				req.Header.Set("X-Trim-Isadmin", "true")
				req.Header.Set("X-Trim-Username", "admin")
			}
			if tc.ctxID != nil {
				req = req.WithContext(context.WithValue(req.Context(), gatewayContextKey{}, tc.ctxID))
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d", rec.Code, tc.want)
			}
		})
	}
}

func TestStripBasePath(t *testing.T) {
	t.Helper()
	var seen []string
	h := StripBasePath("/app/mibee-nvr")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.URL.Path)
	}))

	for in, want := range map[string]string{
		"/app/mibee-nvr":            "/",
		"/app/mibee-nvr/":           "/",
		"/app/mibee-nvr/api/health": "/api/health",
		"/app/mibee-nvr/index.html": "/index.html",
		"/api/health":               "/api/health",
		"/":                         "/",
		"/app/mibee-nvr-other":      "/app/mibee-nvr-other", // sibling prefix untouched
	} {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, in, nil))
		if seen[len(seen)-1] != want {
			t.Fatalf("path %q stripped to %q, want %q", in, seen[len(seen)-1], want)
		}
	}
}
