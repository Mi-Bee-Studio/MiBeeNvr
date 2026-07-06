package api

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"os"
	"strings"

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

	// Build minimal valid config (mirrors cmdInit pattern)
	dataDir := strings.TrimSpace(req.StoragePath)
	if dataDir == "" {
		// Prefer existing config value, then Docker env detection
		if h.config.Storage.RootDir != "" {
			dataDir = h.config.Storage.RootDir
		} else if envDir := os.Getenv("NVR_DATA_DIR"); envDir != "" {
			dataDir = envDir
		} else if info, err := os.Stat("/data"); err == nil && info.IsDir() {
			dataDir = "/data"
		} else {
			dataDir = "/var/lib/mibee-nvr"
		}
	}

	cfg := config.Config{
		Server:  config.ServerConfig{Listen: ":9090"},
		Storage: config.StorageConfig{RootDir: dataDir, SegmentDuration: "30s"},
		Auth:    config.AuthConfig{Username: req.Username, PasswordHash: hash},
		Cameras: []config.CameraConfig{},
		Cleanup: config.CleanupConfig{RetentionDays: 30, CheckInterval: "1h", DiskThresholdPercent: 95},
		FTP:     config.FTPConfig{Port: 2121, PassivePortRange: "2122-2140"},
		WebDAV:  config.WebDAVConfig{PathPrefix: "/dav"},
		Observability: config.ObservabilityConfig{
			LogLevel:  "info",
			LogFormat: "text",
		},
		Version: "1.0",
	}

	// Atomic save
	if err := config.Save(h.configPath, &cfg); err != nil {
		logger.Error("failed to save config", "error", err, "path", r.URL.Path)
		WriteError(w, http.StatusInternalServerError, "failed to save config")
		return
	}

	// Update in-memory config so middleware picks up the new password hash
	h.config.Auth.Username = req.Username
	h.config.Auth.PasswordHash = hash
	h.config.Storage.RootDir = dataDir

	// Generate basic auth token for auto-login
	token := base64.StdEncoding.EncodeToString([]byte(req.Username + ":" + req.Password))

	writeJSON(w, http.StatusOK, map[string]string{
		"status": "ok",
		"token":  token,
	})
}
