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

func TestWSOriginAllowed(t *testing.T) {
	t.Helper()
	gwID := &GatewayIdentity{Username: "admin", UserID: "1000", Admin: true}

	cases := []struct {
		name   string
		origin string
		host   string
		gwCtx  bool
		want   bool
	}{
		{"no origin (non-browser client)", "", "192.168.1.10:9090", false, true},
		{"same-host origin", "http://192.168.1.10:9090", "192.168.1.10:9090", false, true},
		{"cross-site origin rejected", "http://evil.example", "192.168.1.10:9090", false, false},
		{"port mismatch rejected", "http://192.168.1.10:5666", "192.168.1.10:9090", false, false},
		{
			// fnOS gateway forwarding: Origin is the desktop origin (:5666) but the
			// Host header is the gateway's forwarding host — must pass on the
			// verified gateway identity (unix-socket listener only).
			"gateway identity allows host mismatch",
			"http://192.168.1.10:5666", "internal-fwd-host", true, true,
		},
		{
			// The identity gate trusts the request's transport (fnOS verified it),
			// so the Origin content itself is irrelevant once the identity exists.
			"gateway identity trusts transport regardless of origin value",
			"http://evil.example", "internal-fwd-host", true, true,
		},
		{
			// Raw X-Trim-* headers without the middleware-attached identity (a
			// forged header on the TCP listener) must NOT relax the gate.
			"forged identity stays rejected without context",
			"http://evil.example", "192.168.1.10:9090", false, false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/cameras/x/stream/ws", nil)
			req.Host = tc.host
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}
			if tc.gwCtx {
				req = req.WithContext(WithGatewayIdentity(req.Context(), gwID))
			}
			if got := WSOriginAllowed(req); got != tc.want {
				t.Fatalf("WSOriginAllowed() = %v, want %v", got, tc.want)
			}
		})
	}
}
