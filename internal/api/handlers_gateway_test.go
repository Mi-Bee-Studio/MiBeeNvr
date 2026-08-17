package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/middleware"
)

// gatewaySessionResponse mirrors the JSON shape of /api/auth/gateway-session.
type gatewaySessionResponse struct {
	Status      string `json:"status"`
	Token       string `json:"token"`
	ExpiresAt   string `json:"expires_at"`
	GatewayUser string `json:"gateway_user"`
}

func TestGatewaySessionMintsTokenForGatewayAdmin(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)
	h.config = &config.Config{}
	h.config.Auth.Username = "admin"
	hash, err := middleware.HashPassword("unit-test-password")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	h.config.Auth.PasswordHash = hash

	req := httptest.NewRequest(http.MethodGet, "/api/auth/gateway-session", nil)
	req = req.WithContext(middleware.WithGatewayIdentity(req.Context(), &middleware.GatewayIdentity{
		Username: "nas-admin", UserID: "1000", Admin: true,
	}))
	rec := httptest.NewRecorder()
	h.handleGatewaySession(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp gatewaySessionResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Token == "" || resp.GatewayUser != "nas-admin" {
		t.Fatalf("unexpected response: %+v", resp)
	}
	// The minted token must validate as a session token under the same hash.
	if _, err := middleware.VerifySessionToken(resp.Token, hash, time.Now()); err != nil {
		t.Fatalf("minted token does not verify: %v", err)
	}
}

func TestGatewaySessionRejectsWithoutGatewayIdentity(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)
	h.config = &config.Config{}
	h.config.Auth.PasswordHash = "x"

	// Direct TCP listener scenario: no gateway identity in context → 401 even
	// with forged X-Trim-* headers on the wire.
	req := httptest.NewRequest(http.MethodGet, "/api/auth/gateway-session", nil)
	req.Header.Set("X-Trim-Isadmin", "true")
	req.Header.Set("X-Trim-Username", "attacker")
	rec := httptest.NewRecorder()
	h.handleGatewaySession(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestGatewaySessionRejectsNonAdminIdentity(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)
	h.config = &config.Config{}

	req := httptest.NewRequest(http.MethodGet, "/api/auth/gateway-session", nil)
	req = req.WithContext(middleware.WithGatewayIdentity(req.Context(), &middleware.GatewayIdentity{
		Username: "nas-user", Admin: false,
	}))
	rec := httptest.NewRecorder()
	h.handleGatewaySession(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestGatewaySessionRouteRegistered(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	// Through the full router (no gateway context) the endpoint must exist and
	// answer 401 — proving registration on the anonymous route group.
	req := httptest.NewRequest(http.MethodGet, "/api/auth/gateway-session", nil)
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}
