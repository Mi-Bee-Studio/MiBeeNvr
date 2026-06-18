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
