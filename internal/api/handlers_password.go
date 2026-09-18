package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/middleware"
)

// passwordChangeRequest is the JSON body for POST /api/auth/password.
type passwordChangeRequest struct {
	NewPassword string `json:"new_password"`
}

// handlePasswordChange handles POST /api/auth/password — set a new admin
// password WITHOUT knowing the old one. Authorization is locality: the
// request must be a loopback connection with a loopback Host and no proxy
// headers (middleware.IsBypassEligible — the same judgement the local login
// bypass uses). The desktop tray / macOS menu-bar helper call this from the
// NVR's own machine, where the operator could equally edit the config file
// by hand; remote callers get 403.
//
// The password exists for NON-local (LAN) logins — local sessions bypass
// auth entirely when auth.local_bypass is on (the desktop-install default).
//
// Side effect worth knowing: the bcrypt hash is part of the session-token
// signing key (middleware/token.go), so a successful change invalidates
// EVERY outstanding session token — remote sessions must re-login.
func (h *Handler) handlePasswordChange(w http.ResponseWriter, r *http.Request) {
	if !middleware.IsBypassEligible(r) {
		WriteError(w, http.StatusForbidden,
			"password change is only available from a local session on the NVR machine")
		return
	}

	if strings.TrimSpace(h.config.Auth.PasswordHash) == "" {
		WriteError(w, http.StatusConflict, "setup not completed — configure a password first")
		return
	}

	var req passwordChangeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// Same rule as setup / CLI: min 8 chars.
	if len(req.NewPassword) < 8 {
		WriteError(w, http.StatusBadRequest, "password must be at least 8 characters")
		return
	}

	hash, err := middleware.HashPassword(req.NewPassword)
	if err != nil {
		logger.Error("failed to hash password", "error", err, "path", r.URL.Path)
		WriteError(w, http.StatusInternalServerError, "failed to hash password")
		return
	}

	// Patch the loaded config in place (the #388 discipline: never rebuild
	// from defaults) and persist atomically through the same path as setup.
	h.config.Auth.PasswordHash = hash
	h.config.Auth.Password = "" // never leave plaintext behind

	if err := config.Save(h.configPath, h.config); err != nil {
		logger.Error("failed to save config", "error", err, "path", r.URL.Path)
		WriteError(w, http.StatusInternalServerError, "failed to save config")
		return
	}

	logger.Info("admin password changed from a local session", "remote", r.RemoteAddr)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
