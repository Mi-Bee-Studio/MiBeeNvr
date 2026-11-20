package systemapi

import (
	"encoding/json"
	"net/http"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
)

func (s *server) handleGetStreamingSettings(w http.ResponseWriter, r *http.Request) {
	if s.deps.Config == nil {
		WriteError(w, http.StatusInternalServerError, "config not available")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"webrtc": map[string]any{
			"enabled":      s.deps.Config.Streaming.WebRTC.Enabled != nil && *s.deps.Config.Streaming.WebRTC.Enabled,
			"max_viewers":  s.deps.Config.Streaming.WebRTC.MaxViewers,
			"idle_timeout": s.deps.Config.Streaming.WebRTC.IdleTimeout,
		},
		"flv": map[string]any{
			"enabled":        s.deps.Config.Streaming.FLV.Enabled != nil && *s.deps.Config.Streaming.FLV.Enabled,
			"max_viewers":    s.deps.Config.Streaming.FLV.MaxViewers,
			"idle_timeout":   s.deps.Config.Streaming.FLV.IdleTimeout,
			"gop_cache_size": s.deps.Config.Streaming.FLV.GOPCacheSize,
		},
		"hls": map[string]any{
			"low_latency": s.deps.Config.HLS.LowLatencyEnabled(),
			// Concurrent HLS preview-stream budget — the global LRU eviction
			// threshold shared by all clients (#1072). Takes effect at
			// restart (the Manager reads it at construction).
			"max_streams": s.deps.Config.HLS.MaxStreams,
		},
		// RTMP/SRT ingest settings. The streaming panel persisted nothing for
		// them before: the PUT body had no rtmp/srt fields, so the UI switches
		// silently no-op'd while still toasting success (found configuring the
		// fnOS test box, 2026-08-19).
		"rtmp": map[string]any{
			"enabled":     s.deps.Config.RTMP.Enabled != nil && *s.deps.Config.RTMP.Enabled,
			"port":        s.deps.Config.RTMP.Port,
			"stream_keys": s.deps.Config.RTMP.StreamKeys, // camera_id → stream_key (legacy map; per-camera stream_key takes precedence)
		},
		"srt": map[string]any{
			"enabled": s.deps.Config.SRT.Enabled != nil && *s.deps.Config.SRT.Enabled,
			"port":    s.deps.Config.SRT.Port,
			"streams": s.deps.Config.SRT.Streams,
		},
	})
}

func (s *server) handleUpdateStreamingSettings(w http.ResponseWriter, r *http.Request) {
	if s.deps.Config == nil {
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
			MaxStreams *int  `json:"max_streams"`
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
			if s.deps.Config.Streaming.WebRTC.Enabled == nil {
				s.deps.Config.Streaming.WebRTC.Enabled = new(bool)
			}
			*s.deps.Config.Streaming.WebRTC.Enabled = *body.WebRTC.Enabled
		}
		if body.WebRTC.MaxViewers != nil {
			s.deps.Config.Streaming.WebRTC.MaxViewers = *body.WebRTC.MaxViewers
		}
		if body.WebRTC.IdleTimeout != nil {
			s.deps.Config.Streaming.WebRTC.IdleTimeout = *body.WebRTC.IdleTimeout
		}
	}

	if body.FLV != nil {
		if body.FLV.Enabled != nil {
			if s.deps.Config.Streaming.FLV.Enabled == nil {
				s.deps.Config.Streaming.FLV.Enabled = new(bool)
			}
			*s.deps.Config.Streaming.FLV.Enabled = *body.FLV.Enabled
		}
		if body.FLV.MaxViewers != nil {
			s.deps.Config.Streaming.FLV.MaxViewers = *body.FLV.MaxViewers
		}
		if body.FLV.IdleTimeout != nil {
			s.deps.Config.Streaming.FLV.IdleTimeout = *body.FLV.IdleTimeout
		}
		if body.FLV.GOPCacheSize != nil {
			s.deps.Config.Streaming.FLV.GOPCacheSize = *body.FLV.GOPCacheSize
		}
	}

	if body.HLS != nil {
		if body.HLS.LowLatency != nil {
			if s.deps.Config.HLS.LowLatency == nil {
				s.deps.Config.HLS.LowLatency = new(bool)
			}
			*s.deps.Config.HLS.LowLatency = *body.HLS.LowLatency
		}
		if body.HLS.MaxStreams != nil {
			if *body.HLS.MaxStreams < 1 || *body.HLS.MaxStreams > 20 {
				WriteError(w, http.StatusBadRequest, "hls.max_streams must be between 1 and 20")
				return
			}
			s.deps.Config.HLS.MaxStreams = *body.HLS.MaxStreams
		}
	}

	if body.RTMP != nil {
		if body.RTMP.Enabled != nil {
			if s.deps.Config.RTMP.Enabled == nil {
				s.deps.Config.RTMP.Enabled = new(bool)
			}
			*s.deps.Config.RTMP.Enabled = *body.RTMP.Enabled
		}
		if body.RTMP.Port != nil {
			s.deps.Config.RTMP.Port = *body.RTMP.Port
		}
		if body.RTMP.StreamKeys != nil {
			s.deps.Config.RTMP.StreamKeys = body.RTMP.StreamKeys
		}
	}

	if body.SRT != nil {
		if body.SRT.Enabled != nil {
			if s.deps.Config.SRT.Enabled == nil {
				s.deps.Config.SRT.Enabled = new(bool)
			}
			*s.deps.Config.SRT.Enabled = *body.SRT.Enabled
		}
		if body.SRT.Port != nil {
			s.deps.Config.SRT.Port = *body.SRT.Port
		}
		if body.SRT.Streams != nil {
			s.deps.Config.SRT.Streams = body.SRT.Streams
		}
	}

	// Persist config to disk
	if err := config.Save(s.deps.ConfigPath, s.deps.Config); err != nil {
		logger.Warn("failed to save config", "error", err)
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

func (s *server) handleGetTranscodingSettings(w http.ResponseWriter, r *http.Request) {
	if s.deps.Config == nil {
		WriteError(w, http.StatusInternalServerError, "config not available")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"enabled":     s.deps.Config.Transcoding.Enabled,
		"max_workers": s.deps.Config.Transcoding.MaxWorkers,
	})
}

func (s *server) handleUpdateTranscodingSettings(w http.ResponseWriter, r *http.Request) {
	if s.deps.Config == nil {
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
		s.deps.Config.Transcoding.MaxWorkers = *body.MaxWorkers
	}

	if body.Enabled != nil {
		s.deps.Config.Transcoding.Enabled = *body.Enabled
	}

	// Persist config to disk
	if err := config.Save(s.deps.ConfigPath, s.deps.Config); err != nil {
		logger.Warn("failed to save config", "error", err)
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

// handleGetAutoDiscoverSettings returns the current auto_discover config. The
// default_password is NEVER returned over the API (avoid plaintext leakage);
// instead a boolean has_default_password indicates whether one is configured.
func (s *server) handleGetAutoDiscoverSettings(w http.ResponseWriter, r *http.Request) {
	if s.deps.Config == nil {
		WriteError(w, http.StatusInternalServerError, "config not available")
		return
	}
	ad := s.deps.Config.AutoDiscover
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
func (s *server) handleUpdateAutoDiscoverSettings(w http.ResponseWriter, r *http.Request) {
	if s.deps.Config == nil {
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

	ad := &s.deps.Config.AutoDiscover
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

	if err := config.Save(s.deps.ConfigPath, s.deps.Config); err != nil {
		logger.Warn("failed to save config", "error", err)
		WriteError(w, http.StatusInternalServerError, "failed to save config")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}
