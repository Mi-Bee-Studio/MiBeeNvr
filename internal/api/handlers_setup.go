package api

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/middleware"
)

// setupRequest is the JSON body for POST /api/setup.
type setupRequest struct {
	Username    string `json:"username"`
	Password    string `json:"password"`
	Language    string `json:"language,omitempty"`
	StoragePath string `json:"storage_path,omitempty"`
}

// handleSetup handles POST /api/setup — first-time initialization.
// Only succeeds when no password_hash is configured (SETUP_REQUIRED state).
func (h *Handler) handleSetup(w http.ResponseWriter, r *http.Request) {
	// Security: reject if auth is already configured
	if strings.TrimSpace(h.config.Auth.PasswordHash) != "" {
		WriteError(w, http.StatusConflict, "setup already completed")
		return
	}

	var req setupRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// Validate username
	if strings.TrimSpace(req.Username) == "" {
		WriteError(w, http.StatusBadRequest, "username is required")
		return
	}

	// Validate password (same rule as CLI: min 8 chars)
	if len(req.Password) < 8 {
		WriteError(w, http.StatusBadRequest, "password must be at least 8 characters")
		return
	}

	// Hash password with bcrypt
	hash, err := middleware.HashPassword(req.Password)
	if err != nil {
		logger.Error("failed to hash password", "error", err, "path", r.URL.Path)
		WriteError(w, http.StatusInternalServerError, "failed to hash password")
		return
	}

	// Storage path: only a wizard-provided path overrides the loaded config.
	// When absent, keep the existing root_dir; fall back to Docker env detection
	// only if the loaded config has none (it normally went through ApplyDefaults,
	// which already sets /var/lib/mibee-nvr).
	dataDir := strings.TrimSpace(req.StoragePath)
	if dataDir == "" && strings.TrimSpace(h.config.Storage.RootDir) == "" {
		switch {
		case os.Getenv("NVR_DATA_DIR") != "":
			dataDir = os.Getenv("NVR_DATA_DIR")
		default:
			if info, err := os.Stat("/data"); err == nil && info.IsDir() {
				dataDir = "/data"
			} else {
				dataDir = "/var/lib/mibee-nvr"
			}
		}
	}

	// Patch the ALREADY-LOADED config in place — never rebuild from defaults
	// (#388): a hand-preconfigured YAML (listen, vision, api_keys, cameras, …)
	// completed through the setup wizard must keep every non-auth field. The
	// loaded config already carries ApplyDefaults values, so a fresh install
	// still materializes the same defaults the old rebuild wrote.
	h.config.Auth.Username = req.Username
	h.config.Auth.PasswordHash = hash
	if dataDir != "" {
		h.config.Storage.RootDir = dataDir
	}
	if strings.TrimSpace(h.config.Version) == "" {
		h.config.Version = "1.0"
	}

	// Atomic save
	if err := config.Save(h.configPath, h.config); err != nil {
		logger.Error("failed to save config", "error", err, "path", r.URL.Path)
		WriteError(w, http.StatusInternalServerError, "failed to save config")
		return
	}

	// Issue a stateless signed session token (same scheme as /api/auth/login) so
	// the just-initialized browser session never carries a reversible password.
	token, expiresAt := middleware.SignSessionToken(req.Username, hash, time.Now())

	writeJSON(w, http.StatusOK, map[string]string{
		"status":     "ok",
		"token":      token,
		"expires_at": expiresAt.UTC().Format(time.RFC3339),
	})
}
