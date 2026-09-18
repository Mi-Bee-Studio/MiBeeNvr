package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/middleware"
	"github.com/stretchr/testify/require"
)

func setupTestHandlerForPassword(t *testing.T) (*Handler, string) {
	t.Helper()
	h, cfgPath := setupTestHandlerForSetup(t)
	hash, err := middleware.HashPassword("oldpassword123")
	require.NoError(t, err)
	h.config.Auth.Username = "admin"
	h.config.Auth.PasswordHash = hash
	return h, cfgPath
}

// localReq builds a request that satisfies middleware.IsBypassEligible
// (loopback RemoteAddr + loopback Host, no proxy headers) — what the desktop
// tray / menu-bar helper sends.
func localReq(method, target string, body []byte) *http.Request {
	req := httptest.NewRequest(method, target, bytes.NewReader(body))
	req.RemoteAddr = "127.0.0.1:52000"
	req.Host = "127.0.0.1:9090"
	return req
}

func TestHandlePasswordChange_Success(t *testing.T) {
	t.Parallel()
	h, cfgPath := setupTestHandlerForPassword(t)

	body, _ := json.Marshal(map[string]string{"new_password": "newpassword456"})
	req := localReq(http.MethodPost, "/api/auth/password", body)
	rec := httptest.NewRecorder()
	h.handlePasswordChange(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var resp map[string]string
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	require.Equal(t, "ok", resp["status"])

	// Persisted file carries the NEW hash (atomic save through config.Save).
	saved, err := config.Load(cfgPath)
	require.NoError(t, err)
	require.Equal(t, "admin", saved.Auth.Username)
	require.True(t, middleware.CheckPassword("newpassword456", saved.Auth.PasswordHash))
	require.False(t, middleware.CheckPassword("oldpassword123", saved.Auth.PasswordHash))

	// In-memory config updated too (running server validates immediately).
	require.True(t, middleware.CheckPassword("newpassword456", h.config.Auth.PasswordHash))
}

func TestHandlePasswordChange_RemoteRefused(t *testing.T) {
	t.Parallel()
	h, _ := setupTestHandlerForPassword(t)

	body, _ := json.Marshal(map[string]string{"new_password": "newpassword456"})

	// Non-loopback source.
	req := httptest.NewRequest(http.MethodPost, "/api/auth/password", bytes.NewReader(body))
	req.RemoteAddr = "192.168.63.30:52000"
	req.Host = "192.168.63.30:9090"
	rec := httptest.NewRecorder()
	h.handlePasswordChange(rec, req)
	require.Equal(t, http.StatusForbidden, rec.Code)

	// Loopback source but non-loopback Host (DNS rebinding shape).
	req2 := httptest.NewRequest(http.MethodPost, "/api/auth/password", bytes.NewReader(body))
	req2.RemoteAddr = "127.0.0.1:52000"
	req2.Host = "attacker.example:9090"
	rec2 := httptest.NewRecorder()
	h.handlePasswordChange(rec2, req2)
	require.Equal(t, http.StatusForbidden, rec2.Code)

	// Proxy headers present (reverse-proxied shape).
	req3 := httptest.NewRequest(http.MethodPost, "/api/auth/password", bytes.NewReader(body))
	req3.RemoteAddr = "127.0.0.1:52000"
	req3.Host = "127.0.0.1:9090"
	req3.Header.Set("X-Forwarded-For", "192.168.1.5")
	rec3 := httptest.NewRecorder()
	h.handlePasswordChange(rec3, req3)
	require.Equal(t, http.StatusForbidden, rec3.Code)
}

func TestHandlePasswordChange_ShortPassword(t *testing.T) {
	t.Parallel()
	h, _ := setupTestHandlerForPassword(t)

	body, _ := json.Marshal(map[string]string{"new_password": "short"})
	rec := httptest.NewRecorder()
	h.handlePasswordChange(rec, localReq(http.MethodPost, "/api/auth/password", body))
	require.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestHandlePasswordChange_SetupNotCompleted(t *testing.T) {
	t.Parallel()
	h, _ := setupTestHandlerForSetup(t) // no auth configured

	body, _ := json.Marshal(map[string]string{"new_password": "newpassword456"})
	rec := httptest.NewRecorder()
	h.handlePasswordChange(rec, localReq(http.MethodPost, "/api/auth/password", body))
	require.Equal(t, http.StatusConflict, rec.Code)
}

func TestHandlePasswordChange_BadBody(t *testing.T) {
	t.Parallel()
	h, _ := setupTestHandlerForPassword(t)

	rec := httptest.NewRecorder()
	h.handlePasswordChange(rec, localReq(http.MethodPost, "/api/auth/password", []byte("not json")))
	require.Equal(t, http.StatusBadRequest, rec.Code)
}
