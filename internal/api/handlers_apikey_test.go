package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/middleware"
	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"
)

func setupAPIKeyTestHandler(t *testing.T) (*Handler, *middleware.APIKeyStore) {
	t.Helper()
	db, store := setupTestDB(t)
	cfgPath := filepath.Join(t.TempDir(), "test-config.yaml")
	err := os.WriteFile(cfgPath, []byte("version: \"1.0\"\n"), 0o644)
	require.NoError(t, err)
	cfg := &config.Config{Version: "1.0"}
	h := NewHandler(db, store, noopAuthMW(), cfg, nil, nil, cfgPath, nil, nil, nil, nil, nil)
	keyStore := middleware.NewAPIKeyStore()
	h.SetAPIKeyStore(keyStore)
	return h, keyStore
}

func postGenerateKey(t *testing.T, h *Handler, name string) string {
	t.Helper()
	b, _ := json.Marshal(map[string]string{"name": name})
	req := httptest.NewRequest(http.MethodPost, "/api/settings/api-keys", bytes.NewReader(b))
	rec := httptest.NewRecorder()
	h.handleGenerateAPIKey(rec, req)
	require.Equal(t, http.StatusCreated, rec.Code)
	var resp struct {
		Key string `json:"key"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	return resp.Key
}

func deleteRevokeKey(t *testing.T, h *Handler, name string) {
	t.Helper()
	r := chi.NewRouter()
	r.Delete("/api/settings/api-keys/{name}", h.handleRevokeAPIKey)
	req := httptest.NewRequest(http.MethodDelete, "/api/settings/api-keys/"+name, nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
}

// Minted keys must authenticate immediately (no restart) and revoked keys
// must stop authenticating on the next request (#335).
func TestAPIKeyHotReloadViaHandlers(t *testing.T) {
	t.Parallel()
	h, keyStore := setupAPIKeyTestHandler(t)

	key := postGenerateKey(t, h, "dads-phone")
	_, ok := keyStore.Lookup(key)
	require.True(t, ok, "minted key must be valid immediately")

	deleteRevokeKey(t, h, "dads-phone")
	_, ok = keyStore.Lookup(key)
	require.False(t, ok, "revoked key must be invalid on next request")
}

func TestBuildAPIKeyInfoIncludesRevokedAndLastUsed(t *testing.T) {
	t.Parallel()
	h, keyStore := setupAPIKeyTestHandler(t)

	key := postGenerateKey(t, h, "tablet")
	_, ok := keyStore.Lookup(key)
	require.True(t, ok)
	require.NotZero(t, keyStore.LastUsed()["tablet"], "lookup should record last-used")

	deleteRevokeKey(t, h, "tablet")
	// The middleware store drops revoked keys, but the list API keeps showing
	// them (with the revoked flag + last-used) for audit.
	info := buildAPIKeyInfo(h.config.APIKeys, h.apiKeyLastUsed())
	require.Len(t, info, 1)
	require.Equal(t, "tablet", info[0]["name"])
	require.Equal(t, true, info[0]["revoked"])
	require.NotEmpty(t, info[0]["last_used"], "revoked key should still report last-used")
}

func TestBuildAPIKeyInfoNoStore(t *testing.T) {
	t.Parallel()
	keys := []config.APIKeyConfig{
		{Key: "mbv_abcdef1234567890", Name: "vision", Revoked: false},
	}
	info := buildAPIKeyInfo(keys, nil)
	require.Len(t, info, 1)
	require.Equal(t, false, info[0]["revoked"])
	_, hasLastUsed := info[0]["last_used"]
	require.False(t, hasLastUsed, "no store wired → no last_used field")
}
