package api

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/middleware"
	"github.com/stretchr/testify/require"
)

func setupTestHandlerForSetup(t *testing.T) (*Handler, string) {
	t.Helper()
	db, store := setupTestDB(t)
	cfgPath := filepath.Join(t.TempDir(), "test-config.yaml")
	err := os.WriteFile(cfgPath, []byte("version: \"1.0\"\n"), 0644)
	require.NoError(t, err)
	cfg := &config.Config{Version: "1.0"}
	h := NewHandler(db, store, noopAuthMW(), cfg, nil, nil, cfgPath, nil, nil)
	return h, cfgPath
}

func TestHandleSetup_Success(t *testing.T) {
	t.Parallel()
	h, cfgPath := setupTestHandlerForSetup(t)

	body := setupRequest{Username: "admin", Password: "testpassword123"}
	b, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPost, "/api/setup", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	h.handleSetup(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)

	var resp map[string]string
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	require.Equal(t, "ok", resp["status"])
	require.NotEmpty(t, resp["token"])

	// Verify config file was written with password_hash
	saved, err := config.Load(cfgPath)
	require.NoError(t, err)
	require.Equal(t, "admin", saved.Auth.Username)
	require.NotEmpty(t, saved.Auth.PasswordHash)

	// Verify in-memory config updated
	require.Equal(t, "admin", h.config.Auth.Username)
	require.NotEmpty(t, h.config.Auth.PasswordHash)
}

func TestHandleSetup_AlreadyConfigured(t *testing.T) {
	t.Parallel()
	h, _ := setupTestHandlerForSetup(t)

	h.config.Auth.PasswordHash = "$2a$10$somehash"

	body := setupRequest{Username: "admin", Password: "testpassword123"}
	b, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPost, "/api/setup", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	h.handleSetup(rec, req)

	require.Equal(t, http.StatusConflict, rec.Code)
}

func TestHandleSetup_ShortPassword(t *testing.T) {
	t.Parallel()
	h, _ := setupTestHandlerForSetup(t)

	body := setupRequest{Username: "admin", Password: "short"}
	b, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPost, "/api/setup", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	h.handleSetup(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestHandleSetup_EmptyUsername(t *testing.T) {
	t.Parallel()
	h, _ := setupTestHandlerForSetup(t)

	body := setupRequest{Username: "", Password: "testpassword123"}
	b, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPost, "/api/setup", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	h.handleSetup(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestHandleSetup_InvalidJSON(t *testing.T) {
	t.Parallel()
	h, _ := setupTestHandlerForSetup(t)

	req := httptest.NewRequest(http.MethodPost, "/api/setup", bytes.NewReader([]byte("not json")))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	h.handleSetup(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestHandleSetup_CustomStoragePath(t *testing.T) {
	t.Parallel()
	h, cfgPath := setupTestHandlerForSetup(t)

	body := setupRequest{Username: "admin", Password: "testpassword123", StoragePath: "/tmp/custom-nvr"}
	b, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPost, "/api/setup", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	h.handleSetup(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)

	saved, err := config.Load(cfgPath)
	require.NoError(t, err)
	require.Equal(t, "/tmp/custom-nvr", saved.Storage.RootDir)
}

func TestHandleSetup_TokenIsValid(t *testing.T) {
	t.Parallel()
	h, _ := setupTestHandlerForSetup(t)

	username := "testuser"
	password := "securepassword123"
	body := setupRequest{Username: username, Password: password}
	b, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPost, "/api/setup", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	h.handleSetup(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)

	var resp map[string]string
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))

	// Verify token matches expected BasicAuth encoding
	decoded, err := base64.StdEncoding.DecodeString(resp["token"])
	require.NoError(t, err)
	require.Equal(t, username+":"+password, string(decoded))

	// Verify the hashed password actually validates via bcrypt
	require.True(t, middleware.CheckPassword(password, h.config.Auth.PasswordHash))
}
