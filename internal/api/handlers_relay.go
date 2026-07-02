package api

import (
	"net/http"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/relay"
	"github.com/go-chi/chi/v5"
)

// handleListRelayPresets handles GET /api/relay-presets.
// It returns all registered platform presets sorted by name.
func (h *Handler) handleListRelayPresets(w http.ResponseWriter, r *http.Request) {
	if h.relayMgr == nil {
		writeJSON(w, http.StatusOK, []relay.Preset{})
		return
	}
	presets := h.relayMgr.ListAllPresets()
	if presets == nil {
		presets = []relay.Preset{}
	}
	writeJSON(w, http.StatusOK, presets)
}

// handleGetRelayPreset handles GET /api/relay-presets/{name}.
// It returns a single platform preset by name.
func (h *Handler) handleGetRelayPreset(w http.ResponseWriter, r *http.Request) {
	if h.relayMgr == nil {
		writeError(w, http.StatusNotFound, "relay manager not available")
		return
	}
	name := chi.URLParam(r, "name")
	if name == "" {
		writeError(w, http.StatusBadRequest, "preset name required")
		return
	}
	preset, ok := h.relayMgr.GetPreset(name)
	if !ok {
		writeError(w, http.StatusNotFound, "preset not found")
		return
	}
	writeJSON(w, http.StatusOK, preset)
}

type relayCapabilitiesResponse struct {
	FFmpegRelaySupported bool `json:"ffmpeg_relay_supported"`
	FFmpegAvailable      bool `json:"ffmpeg_available"`
	MaxTargetsPerCamera  int  `json:"max_targets_per_camera"`
}

func (h *Handler) handleRelayCapabilities(w http.ResponseWriter, r *http.Request) {
	resp := relayCapabilitiesResponse{
		FFmpegRelaySupported: true,
		FFmpegAvailable:      false,
		MaxTargetsPerCamera:  10,
	}
	if h.relayMgr != nil {
		resp.FFmpegAvailable = h.relayMgr.FFmpegAvailable()
	}
	writeJSON(w, http.StatusOK, resp)
}
