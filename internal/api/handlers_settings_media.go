package api

import (
	"encoding/json"
	"net/http"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
)

func (h *Handler) handleGetStreamingSettings(w http.ResponseWriter, r *http.Request) {
	if h.config == nil {
		WriteError(w, http.StatusInternalServerError, "config not available")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"webrtc": map[string]any{
			"enabled":      h.config.Streaming.WebRTC.Enabled != nil && *h.config.Streaming.WebRTC.Enabled,
			"max_viewers":  h.config.Streaming.WebRTC.MaxViewers,
			"idle_timeout": h.config.Streaming.WebRTC.IdleTimeout,
		},
		"flv": map[string]any{
			"enabled":        h.config.Streaming.FLV.Enabled != nil && *h.config.Streaming.FLV.Enabled,
			"max_viewers":    h.config.Streaming.FLV.MaxViewers,
			"idle_timeout":   h.config.Streaming.FLV.IdleTimeout,
			"gop_cache_size": h.config.Streaming.FLV.GOPCacheSize,
		},
		"hls": map[string]any{
			"low_latency": h.config.HLS.LowLatency,
		},
		// RTMP/SRT ingest settings. The streaming panel persisted nothing for
		// them before: the PUT body had no rtmp/srt fields, so the UI switches
		// silently no-op'd while still toasting success (found configuring the
		// fnOS test box, 2026-08-19).
		"rtmp": map[string]any{
			"enabled":     h.config.RTMP.Enabled != nil && *h.config.RTMP.Enabled,
			"port":        h.config.RTMP.Port,
			"stream_keys": h.config.RTMP.StreamKeys, // camera_id → stream_key (legacy map; per-camera stream_key takes precedence)
		},
		"srt": map[string]any{
			"enabled": h.config.SRT.Enabled != nil && *h.config.SRT.Enabled,
			"port":    h.config.SRT.Port,
			"streams": h.config.SRT.Streams,
		},
	})
}

func (h *Handler) handleUpdateStreamingSettings(w http.ResponseWriter, r *http.Request) {
	if h.config == nil {
		WriteError(w, http.StatusInternalServerError, "config not available")
		return
	}

	var body struct {
		WebRTC *struct {
			Enabled     *bool   `json:"enabled"`
			MaxViewers  *int    `json:"max_viewers"`
			IdleTimeout *string `json:"idle_timeout"`
		} `json:"webrtc"`
		FLV *struct {
			Enabled      *bool   `json:"enabled"`
			MaxViewers   *int    `json:"max_viewers"`
			IdleTimeout  *string `json:"idle_timeout"`
			GOPCacheSize *int    `json:"gop_cache_size"`
		} `json:"flv"`
		HLS *struct {
			LowLatency *bool `json:"low_latency"`
		} `json:"hls"`
		RTMP *struct {
			Enabled    *bool             `json:"enabled"`
			Port       *int              `json:"port"`
			StreamKeys map[string]string `json:"stream_keys"` // camera_id → stream_key
		} `json:"rtmp"`
		SRT *struct {
			Enabled *bool              `json:"enabled"`
			Port    *int               `json:"port"`
			Streams []config.SRTStream `json:"streams"`
		} `json:"srt"`
	}

	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if body.WebRTC != nil {
		if body.WebRTC.Enabled != nil {
			if h.config.Streaming.WebRTC.Enabled == nil {
				h.config.Streaming.WebRTC.Enabled = new(bool)
			}
			*h.config.Streaming.WebRTC.Enabled = *body.WebRTC.Enabled
		}
		if body.WebRTC.MaxViewers != nil {
			h.config.Streaming.WebRTC.MaxViewers = *body.WebRTC.MaxViewers
		}
		if body.WebRTC.IdleTimeout != nil {
			h.config.Streaming.WebRTC.IdleTimeout = *body.WebRTC.IdleTimeout
		}
	}

	if body.FLV != nil {
		if body.FLV.Enabled != nil {
			if h.config.Streaming.FLV.Enabled == nil {
				h.config.Streaming.FLV.Enabled = new(bool)
			}
			*h.config.Streaming.FLV.Enabled = *body.FLV.Enabled
		}
		if body.FLV.MaxViewers != nil {
			h.config.Streaming.FLV.MaxViewers = *body.FLV.MaxViewers
		}
		if body.FLV.IdleTimeout != nil {
			h.config.Streaming.FLV.IdleTimeout = *body.FLV.IdleTimeout
		}
		if body.FLV.GOPCacheSize != nil {
			h.config.Streaming.FLV.GOPCacheSize = *body.FLV.GOPCacheSize
		}
	}

	if body.HLS != nil && body.HLS.LowLatency != nil {
		h.config.HLS.LowLatency = *body.HLS.LowLatency
	}

	if body.RTMP != nil {
		if body.RTMP.Enabled != nil {
			if h.config.RTMP.Enabled == nil {
				h.config.RTMP.Enabled = new(bool)
			}
			*h.config.RTMP.Enabled = *body.RTMP.Enabled
		}
		if body.RTMP.Port != nil {
			h.config.RTMP.Port = *body.RTMP.Port
		}
		if body.RTMP.StreamKeys != nil {
			h.config.RTMP.StreamKeys = body.RTMP.StreamKeys
		}
	}

	if body.SRT != nil {
		if body.SRT.Enabled != nil {
			if h.config.SRT.Enabled == nil {
				h.config.SRT.Enabled = new(bool)
			}
			*h.config.SRT.Enabled = *body.SRT.Enabled
		}
		if body.SRT.Port != nil {
			h.config.SRT.Port = *body.SRT.Port
		}
		if body.SRT.Streams != nil {
			h.config.SRT.Streams = body.SRT.Streams
		}
	}

	// Persist config to disk
	if err := config.Save(h.configPath, h.config); err != nil {
		logger.Warn("failed to save config", "error", err)
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

func (h *Handler) handleGetTranscodingSettings(w http.ResponseWriter, r *http.Request) {
	if h.config == nil {
		WriteError(w, http.StatusInternalServerError, "config not available")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"enabled":     h.config.Transcoding.Enabled,
		"max_workers": h.config.Transcoding.MaxWorkers,
	})
}

func (h *Handler) handleUpdateTranscodingSettings(w http.ResponseWriter, r *http.Request) {
	if h.config == nil {
		WriteError(w, http.StatusInternalServerError, "config not available")
		return
	}

	var body struct {
		Enabled    *bool `json:"enabled"`
		MaxWorkers *int  `json:"max_workers"`
	}

	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if body.MaxWorkers != nil {
		if *body.MaxWorkers < 1 || *body.MaxWorkers > 4 {
			WriteError(w, http.StatusBadRequest, "max_workers must be between 1 and 4")
			return
		}
		h.config.Transcoding.MaxWorkers = *body.MaxWorkers
	}

	if body.Enabled != nil {
		h.config.Transcoding.Enabled = *body.Enabled
	}

	// Persist config to disk
	if err := config.Save(h.configPath, h.config); err != nil {
		logger.Warn("failed to save config", "error", err)
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

// handleGetAutoDiscoverSettings returns the current auto_discover config. The
// default_password is NEVER returned over the API (avoid plaintext leakage);
// instead a boolean has_default_password indicates whether one is configured.
func (h *Handler) handleGetAutoDiscoverSettings(w http.ResponseWriter, r *http.Request) {
	if h.config == nil {
		WriteError(w, http.StatusInternalServerError, "config not available")
		return
	}
	ad := h.config.AutoDiscover
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled":              ad.AutoDiscoverEnabled(),
		"scan_interval":        ad.ScanIntervalSeconds,
		"listen_for_hello":     ad.ListenForHelloEnabled(),
		"network_interface":    ad.NetworkInterface,
		"default_username":     ad.DefaultUsername,
		"has_default_password": ad.DefaultPassword != "",
		"ignore_scopes":        ad.IgnoreScopes,
	})
}

// handleUpdateAutoDiscoverSettings updates the auto_discover config and persists
// it to disk. All fields are optional (nil = unchanged); scan_interval is
// floored to 30 to respect RPi-3B resource constraints.

// handleUpdateAutoDiscoverSettings updates the auto_discover config and persists
// it to disk. All fields are optional (nil = unchanged); scan_interval is
// floored to 30 to respect RPi-3B resource constraints.
func (h *Handler) handleUpdateAutoDiscoverSettings(w http.ResponseWriter, r *http.Request) {
	if h.config == nil {
		WriteError(w, http.StatusInternalServerError, "config not available")
		return
	}
	var body struct {
		Enabled          *bool    `json:"enabled"`
		ScanInterval     *int     `json:"scan_interval"`
		ListenForHello   *bool    `json:"listen_for_hello"`
		NetworkInterface *string  `json:"network_interface"`
		DefaultUsername  *string  `json:"default_username"`
		DefaultPassword  *string  `json:"default_password"`
		IgnoreScopes     []string `json:"ignore_scopes"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	ad := &h.config.AutoDiscover
	if body.Enabled != nil {
		ad.Enabled = body.Enabled
	}
	if body.ScanInterval != nil {
		v := *body.ScanInterval
		if v < 30 {
			v = 30 // floor: RPi-3B resource constraint
		}
		ad.ScanIntervalSeconds = v
	}
	if body.ListenForHello != nil {
		ad.ListenForHello = body.ListenForHello
	}
	if body.NetworkInterface != nil {
		ad.NetworkInterface = *body.NetworkInterface
	}
	if body.DefaultUsername != nil {
		ad.DefaultUsername = *body.DefaultUsername
	}
	// Only overwrite the password when the client explicitly sends a non-empty
	// one. The GET handler never returns it, so the frontend sends it only when
	// the user types a new value; an empty/nil is treated as "leave unchanged"
	// so a save that doesn't touch the password field doesn't wipe it.
	if body.DefaultPassword != nil && *body.DefaultPassword != "" {
		ad.DefaultPassword = *body.DefaultPassword
	}
	if body.IgnoreScopes != nil {
		ad.IgnoreScopes = body.IgnoreScopes
	}

	if err := config.Save(h.configPath, h.config); err != nil {
		logger.Warn("failed to save config", "error", err)
		WriteError(w, http.StatusInternalServerError, "failed to save config")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

// registerSystemRoutes registers system/stats/settings/backup/protocol/feature routes.
