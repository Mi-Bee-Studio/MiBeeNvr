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

// handlePasswordChange handles POST /api/auth/password — change the admin
// password. Auth model: the endpoint sits behind the standard auth
// middleware, so the request's own BasicAuth credentials ARE the current
// password — proving knowledge of it is what authorizes the change (the
// same guarantee login gives; a wrong old password dies at 401 before the
// handler ever runs).
//
// Side effect worth knowing: the bcrypt hash is part of the session-token
// signing key (middleware/token.go), so a successful change invalidates
// EVERY outstanding session token — callers must re-login with the new
// password (the desktop tray dialog does exactly that on next use).
func (h *Handler) handlePasswordChange(w http.ResponseWriter, r *http.Request) {
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

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
