package api

import (
	"encoding/json"
	"net/http"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
)

// RTSP output server settings (#686): the Settings card edits
// server.rtsp.{enabled,port,username,password} through these endpoints so
// users never hand-edit the yaml (a mis-edit there breaks the whole config
// parse — #651 question 1). The RTSP server reads its Config copy at
// construction, so every change here takes effect after a restart; the PUT
// response carries restart_required=true for the UI hint.

func (h *Handler) handleGetRtspOutputSettings(w http.ResponseWriter, r *http.Request) {
	if h.config == nil {
		WriteError(w, http.StatusInternalServerError, "config not available")
		return
	}
	rtspCfg := h.config.Server.RTSP
	writeJSON(w, http.StatusOK, map[string]any{
		// *bool default-true: nil resolves to enabled.
		"enabled": rtspCfg.Enabled == nil || *rtspCfg.Enabled,
		"port":    rtspCfg.Port,
		// Never return the password (mirrors the masked gb28181/auth fields);
		// the frontend only needs whether one is set.
		"username":            rtspCfg.Username,
		"password_configured": rtspCfg.Password != "",
	})
}

func (h *Handler) handleUpdateRtspOutputSettings(w http.ResponseWriter, r *http.Request) {
	if h.config == nil {
		WriteError(w, http.StatusInternalServerError, "config not available")
		return
	}

	var body struct {
		Enabled  *bool   `json:"enabled"`
		Port     *int    `json:"port"`
		Username *string `json:"username"`
		Password *string `json:"password"`
		// ClearCredentials wipes username+password in one shot (open access).
		// The password field alone can't express "clear" — blank = keep current,
		// mirroring gb28181/auto-discover semantics.
		ClearCredentials *bool `json:"clear_credentials"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	rtspCfg := &h.config.Server.RTSP
	if body.Enabled != nil {
		rtspCfg.Enabled = body.Enabled
	}
	if body.Port != nil {
		if *body.Port < 1 || *body.Port > 65535 {
			WriteError(w, http.StatusBadRequest, "port must be between 1 and 65535")
			return
		}
		rtspCfg.Port = *body.Port
	}
	if body.Username != nil {
		rtspCfg.Username = *body.Username
	}
	// Blank password = keep current (the GET never returns it, so the UI
	// round-trips an empty field when unchanged).
	if body.Password != nil && *body.Password != "" {
		rtspCfg.Password = *body.Password
	}
	if body.ClearCredentials != nil && *body.ClearCredentials {
		rtspCfg.Username = ""
		rtspCfg.Password = ""
	}

	if err := config.Save(h.configPath, h.config); err != nil {
		logger.Warn("failed to save config", "error", err)
		WriteError(w, http.StatusInternalServerError, "failed to save config")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "updated", "restart_required": true})
}
