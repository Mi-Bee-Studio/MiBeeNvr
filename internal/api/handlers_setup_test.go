package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/middleware"
	"github.com/stretchr/testify/require"
)

func setupTestHandlerForSetup(t *testing.T) (*Handler, string) {
	t.Helper()
	db, store := setupTestDB(t)
	cfgPath := filepath.Join(t.TempDir(), "test-config.yaml")
	err := os.WriteFile(cfgPath, []byte("version: \"1.0\"\n"), 0o644)
	require.NoError(t, err)
	cfg := &config.Config{Version: "1.0"}
	h := NewHandler(db, store, noopAuthMW(), cfg, nil, nil, cfgPath, nil, nil, nil, nil, nil)
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

// TestHandleSetup_PreservesPreconfiguredFields locks in the #388 fix: setup
// must patch auth (plus an explicit wizard storage path) on the already-loaded
// config — never rewrite the file from defaults. A hand-preconfigured YAML
// (listen / vision / api_keys / cameras) completed through the setup wizard
// previously had every non-auth field silently reset.
func TestHandleSetup_PreservesPreconfiguredFields(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	cfgPath := filepath.Join(t.TempDir(), "test-config.yaml")

	cfg := &config.Config{
		Version: "1.0",
		Server:  config.ServerConfig{Listen: "127.0.0.1:9777"},
		Storage: config.StorageConfig{RootDir: "/mnt/preconfigured"},
		Vision:  config.VisionConfig{Enabled: true, URL: "http://192.168.63.110:9091"},
		APIKeys: []config.APIKeyConfig{{Key: "mbv_abcdef0123456789", Name: "vision"}},
		Cameras: []config.CameraConfig{{ID: "test-cam", Name: "Test", Protocol: "rtsp", URL: "rtsp://example/stream", Encoding: "h264"}},
	}
	h := NewHandler(db, store, noopAuthMW(), cfg, nil, nil, cfgPath, nil, nil, nil, nil, nil)

	body := setupRequest{Username: "admin", Password: "testpassword123"}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/setup", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	h.handleSetup(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)

	saved, err := config.Load(cfgPath)
	require.NoError(t, err)
	require.Equal(t, "127.0.0.1:9777", saved.Server.Listen)
	require.Equal(t, "/mnt/preconfigured", saved.Storage.RootDir)
	require.True(t, saved.Vision.Enabled)
	require.Equal(t, "http://192.168.63.110:9091", saved.Vision.URL)
	require.Len(t, saved.APIKeys, 1)
	require.Equal(t, "mbv_abcdef0123456789", saved.APIKeys[0].Key)
	require.Len(t, saved.Cameras, 1)
	require.Equal(t, "test-cam", saved.Cameras[0].ID)
	require.Equal(t, "admin", saved.Auth.Username)
	require.NotEmpty(t, saved.Auth.PasswordHash)
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

	// Setup now returns a stateless HMAC-signed session token (mbs_...) instead
	// of the legacy base64(user:pass). Verify it carries the mbs_ prefix and
	// validates against the bcrypt hash just stored in config.
	tok := resp["token"]
	require.NotEmpty(t, tok)
	require.True(t, middleware.IsSessionToken(tok), "token must carry the mbs_ prefix")

	// Verify the hashed password actually validates via bcrypt
	require.True(t, middleware.CheckPassword(password, h.config.Auth.PasswordHash))

	// And the signed token must verify under that same hash.
	claims, err := middleware.VerifySessionToken(tok, h.config.Auth.PasswordHash, time.Now())
	require.NoError(t, err)
	require.Equal(t, username, claims.Sub)
}
