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

func TestHandlePasswordChange_Success(t *testing.T) {
	t.Parallel()
	h, cfgPath := setupTestHandlerForPassword(t)

	body, _ := json.Marshal(map[string]string{"new_password": "newpassword456"})
	req := httptest.NewRequest(http.MethodPost, "/api/auth/password", bytes.NewReader(body))
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

func TestHandlePasswordChange_ShortPassword(t *testing.T) {
	t.Parallel()
	h, _ := setupTestHandlerForPassword(t)

	body, _ := json.Marshal(map[string]string{"new_password": "short"})
	req := httptest.NewRequest(http.MethodPost, "/api/auth/password", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	h.handlePasswordChange(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestHandlePasswordChange_SetupNotCompleted(t *testing.T) {
	t.Parallel()
	h, _ := setupTestHandlerForSetup(t) // no auth configured

	body, _ := json.Marshal(map[string]string{"new_password": "newpassword456"})
	req := httptest.NewRequest(http.MethodPost, "/api/auth/password", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	h.handlePasswordChange(rec, req)

	require.Equal(t, http.StatusConflict, rec.Code)
}

func TestHandlePasswordChange_BadBody(t *testing.T) {
	t.Parallel()
	h, _ := setupTestHandlerForPassword(t)

	req := httptest.NewRequest(http.MethodPost, "/api/auth/password", bytes.NewReader([]byte("not json")))
	rec := httptest.NewRecorder()
	h.handlePasswordChange(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
}
