// Package relayapi serves the push-out relay preset and capability endpoints.
//
// It is the first extracted domain of internal/api (see
// the same shape every later domain package follows
// follows — a local Deps interface plus a Register function, so the parent
// api.Handler only wires dependencies and never owns the handlers.
package relayapi

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/httpx"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/relay"
)

// Deps is what this domain needs from the API assembly. Keeping it as an
// interface (rather than a *api.Handler) is what makes the extraction possible
// without an import cycle.
type Deps struct {
	// Relay is nil when push-out relay is not configured; every handler below
	// treats nil as "no presets, ffmpeg unavailable" rather than an error.
	Relay RelayManager
}

// RelayManager is the subset of relay.Manager this domain uses.
type RelayManager interface {
	ListAllPresets() []relay.Preset
	GetPreset(name string) (relay.Preset, bool)
	FFmpegAvailable() bool
}

type capabilitiesResponse struct {
	FFmpegRelaySupported bool `json:"ffmpeg_relay_supported"`
	FFmpegAvailable      bool `json:"ffmpeg_available"`
	MaxTargetsPerCamera  int  `json:"max_targets_per_camera"`
}

// Register mounts the relay routes on r.
func Register(r chi.Router, deps Deps) {
	r.Get("/api/relay-presets", listPresets(deps))
	r.Get("/api/relay-presets/{name}", getPreset(deps))
	r.Get("/api/relay/capabilities", capabilities(deps))
}

func listPresets(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if deps.Relay == nil {
			httpx.WriteJSON(w, http.StatusOK, []relay.Preset{})
			return
		}
		presets := deps.Relay.ListAllPresets()
		if presets == nil {
			presets = []relay.Preset{}
		}
		httpx.WriteJSON(w, http.StatusOK, presets)
	}
}

func getPreset(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if deps.Relay == nil {
			httpx.WriteError(w, http.StatusNotFound, "relay manager not available")
			return
		}
		name := chi.URLParam(r, "name")
		if name == "" {
			httpx.WriteError(w, http.StatusBadRequest, "preset name required")
			return
		}
		preset, ok := deps.Relay.GetPreset(name)
		if !ok {
			httpx.WriteError(w, http.StatusNotFound, "preset not found")
			return
		}
		httpx.WriteJSON(w, http.StatusOK, preset)
	}
}

func capabilities(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		resp := capabilitiesResponse{
			FFmpegRelaySupported: true,
			MaxTargetsPerCamera:  10,
		}
		if deps.Relay != nil {
			resp.FFmpegAvailable = deps.Relay.FFmpegAvailable()
		}
		httpx.WriteJSON(w, http.StatusOK, resp)
	}
}
