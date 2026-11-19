package api

// API-key management endpoints and the info projection used by the settings
// view. Split out of handlers_settings.go.

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/middleware"
)

// buildAPIKeyInfo returns a safe summary of configured API keys (never the key
// itself). Revoked keys are included (grayed in the UI) so owners can see the
// full per-device token list; last_used comes from the live store when wired.
func buildAPIKeyInfo(keys []config.APIKeyConfig, lastUsed map[string]time.Time) []map[string]any {
	result := make([]map[string]any, 0, len(keys))
	for _, k := range keys {
		info := map[string]any{
			"name":    k.Name,
			"prefix":  k.Key[:min(8, len(k.Key))] + "…", // e.g. "mbv_ab12…"
			"revoked": k.Revoked,
		}
		if t, ok := lastUsed[k.Name]; ok && !t.IsZero() {
			info["last_used"] = t.UTC().Format(time.RFC3339)
		}
		result = append(result, info)
	}
	return result
}

// syncAPIKeyStore rebuilds the live key set from the in-memory config. Called
// after generate/revoke mutations so the API-key middleware picks up the
// change on the next request without a restart (#335).

// syncAPIKeyStore rebuilds the live key set from the in-memory config. Called
// after generate/revoke mutations so the API-key middleware picks up the
// change on the next request without a restart (#335).
func (h *Handler) syncAPIKeyStore() {
	if h.apiKeyStore == nil || h.config == nil {
		return
	}
	valid := make(map[string]string)
	for _, k := range h.config.APIKeys {
		if !k.Revoked && k.Key != "" {
			valid[k.Key] = k.Name
		}
	}
	h.apiKeyStore.SetKeys(valid)
}

// apiKeyLastUsed returns the store's last-used map, or nil when no store is
// wired (tests, sub-handlers).

// apiKeyLastUsed returns the store's last-used map, or nil when no store is
// wired (tests, sub-handlers).
func (h *Handler) apiKeyLastUsed() map[string]time.Time {
	if h.apiKeyStore == nil {
		return nil
	}
	return h.apiKeyStore.LastUsed()
}

// handleStorageCandidates reports the recording-root choices available to the
// settings UI (#395): the current storage.root_dir plus any extra locations the
// host platform granted (fnOS user-authorized dirs, mounted under /media/* by
// the lifecycle script and passed via NVR_STORAGE_CANDIDATES).
func (h *Handler) handleStorageCandidates(w http.ResponseWriter, r *http.Request) {
	if h.config == nil {
		WriteError(w, http.StatusInternalServerError, "config not available")
		return
	}
	type candidate struct {
		Path  string `json:"path"`
		Label string `json:"label"`
	}
	resp := struct {
		Current     string      `json:"current"`
		Candidates  []candidate `json:"candidates"`
		RestartHint string      `json:"restart_hint"`
		EnvManaged  bool        `json:"env_managed"`
	}{
		Current:     h.config.Storage.RootDir,
		Candidates:  []candidate{{Path: h.config.Storage.RootDir, Label: "current"}},
		RestartHint: "切换立即生效：新录像将写入所选位置（无需重启）",
		EnvManaged:  os.Getenv("NVR_STORAGE_CANDIDATES") != "",
	}
	seen := map[string]bool{h.config.Storage.RootDir: true}
	for _, p := range h.config.Storage.Candidates {
		if p == "" || seen[p] {
			continue
		}
		// Live existence filter: a mount revoked or unplugged since boot must
		// not keep offering a dead choice here (the startup-time prune in
		// config.ApplyDefaults only covers the persisted list).
		if info, err := os.Stat(p); err != nil || !info.IsDir() {
			continue
		}
		seen[p] = true
		label := strings.TrimPrefix(p, "/media/")
		if label == p {
			label = filepath.Base(p)
		}
		resp.Candidates = append(resp.Candidates, candidate{Path: p, Label: label})
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleGenerateAPIKey creates a new API key for MiBeeVision integration.
// POST /api/settings/api-keys  body: {"name": "mibeevision-prod"}
// Returns the full key ONCE (never exposed again).

// handleGenerateAPIKey creates a new API key for MiBeeVision integration.
// POST /api/settings/api-keys  body: {"name": "mibeevision-prod"}
// Returns the full key ONCE (never exposed again).
func (h *Handler) handleGenerateAPIKey(w http.ResponseWriter, r *http.Request) {
	if h.config == nil {
		WriteError(w, http.StatusInternalServerError, "config not available")
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	name := strings.TrimSpace(body.Name)
	if name == "" {
		name = "mibeevision"
	}

	// Reject duplicate ACTIVE names (#705): two live entries sharing a name make
	// revoke-by-name ambiguous and the settings list confusing. Names that exist
	// only in revoked history stay reusable (re-pairing flow).
	for _, existing := range h.config.APIKeys {
		if existing.Name == name && !existing.Revoked {
			WriteError(w, http.StatusConflict, "an active API key with this name already exists")
			return
		}
	}

	key := middleware.GenerateAPIKey()
	h.config.APIKeys = append(h.config.APIKeys, config.APIKeyConfig{
		Key:  key,
		Name: name,
	})

	if err := config.Save(h.configPath, h.config); err != nil {
		WriteError(w, http.StatusInternalServerError, "failed to save config")
		return
	}
	h.syncAPIKeyStore()

	logger.Info("API key generated", "name", name)
	writeJSON(w, http.StatusCreated, map[string]any{
		"name":   name,
		"key":    key,
		"prefix": key[:min(8, len(key))] + "…",
	})
}

// handleRevokeAPIKey marks an API key as revoked by name.
// DELETE /api/settings/api-keys/{name}

// handleRevokeAPIKey marks an API key as revoked by name.
// DELETE /api/settings/api-keys/{name}
func (h *Handler) handleRevokeAPIKey(w http.ResponseWriter, r *http.Request) {
	if h.config == nil {
		WriteError(w, http.StatusInternalServerError, "config not available")
		return
	}
	name := chi.URLParam(r, "name")
	if name == "" {
		WriteError(w, http.StatusBadRequest, "name is required")
		return
	}

	// Mark ALL entries with the matching name (#705): restored/legacy configs can
	// carry duplicates, and stopping at the first match left the newer key
	// authorized while reporting success.
	count := 0
	for i := range h.config.APIKeys {
		if h.config.APIKeys[i].Name == name {
			h.config.APIKeys[i].Revoked = true
			count++
		}
	}
	if count == 0 {
		WriteError(w, http.StatusNotFound, "API key not found")
		return
	}

	if err := config.Save(h.configPath, h.config); err != nil {
		WriteError(w, http.StatusInternalServerError, "failed to save config")
		return
	}
	h.syncAPIKeyStore()

	logger.Info("API key revoked", "name", name, "count", count)
	writeJSON(w, http.StatusOK, map[string]any{"status": "revoked", "count": count})
}
