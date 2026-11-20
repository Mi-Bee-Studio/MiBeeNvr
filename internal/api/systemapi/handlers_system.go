package systemapi

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
)

func (s *server) handleStats(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	total, used, err := s.deps.Store.GetDiskUsage()
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "failed to get disk usage")
		return
	}

	count, err := s.deps.DB.CountRecordings(ctx)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "failed to count recordings")
		return
	}

	// Camera count from the in-memory config (O(1)) — avoids a redundant
	// ListCameras DB round-trip on every 30s Dashboard poll.
	var cameraCount int
	if s.deps.Cam != nil {
		cameraCount = s.deps.Cam.CameraCount()
	} else {
		cameras, err := s.deps.DB.ListCameras(ctx)
		if err != nil {
			WriteError(w, http.StatusInternalServerError, "failed to count cameras")
			return
		}
		cameraCount = len(cameras)
	}

	stats := model.StorageStats{
		TotalBytes:     total,
		UsedBytes:      used,
		RecordingCount: count,
		CameraCount:    cameraCount,
	}
	writeJSON(w, http.StatusOK, stats)
}

func (s *server) handleStatsTrends(w http.ResponseWriter, r *http.Request) {
	days := 7
	if v := r.URL.Query().Get("days"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 && n <= 30 {
			days = n
		}
	}

	loc := time.UTC
	if s.deps.Config != nil && s.deps.Config.Timezone != "" && s.deps.Config.Timezone != "UTC" {
		if l, err := time.LoadLocation(s.deps.Config.Timezone); err == nil {
			loc = l
		}
	}

	trends, err := s.deps.DB.GetRecordingTrends(r.Context(), days, loc)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "failed to get recording trends")
		return
	}
	writeJSON(w, http.StatusOK, trends)
}

// handleStatsCameras serves GET /api/stats/cameras — the per-camera storage
// footprint (bytes + segment count) rendered on the Dashboard's storage-usage
// card. Backed by a short-lived DB cache (see GetCameraStorageStats).
func (s *server) handleStatsCameras(w http.ResponseWriter, r *http.Request) {
	stats, err := s.deps.DB.GetCameraStorageStats(r.Context())
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "failed to get camera storage stats")
		return
	}
	writeJSON(w, http.StatusOK, stats)
}

// --- Settings endpoints ---

// protocolInfo describes a protocol for the /api/protocols endpoint.
type protocolInfo struct {
	ID           string          `json:"id"`
	Label        string          `json:"label"`
	Encodings    []string        `json:"encodings"`
	BuiltIn      bool            `json:"built_in"`
	Capabilities map[string]bool `json:"capabilities"`
}

func (s *server) handleProtocols(w http.ResponseWriter, r *http.Request) {
	protocols := []protocolInfo{
		{
			ID:    "rtsp",
			Label: "RTSP",
			// "audio" = audio-only source device (independent mic): the
			// recorder consumes the stream's audio track (AAC/G.711) and
			// writes standalone audio segments.
			Encodings:    []string{"h264", "h265", "mjpeg", "audio"},
			BuiltIn:      true,
			Capabilities: map[string]bool{"hls": true, "ptz": false, "snapshot": false, "discovery": false, "auth": true},
		},
		{
			ID:           "http",
			Label:        "HTTP JPEG",
			Encodings:    []string{"jpeg"},
			BuiltIn:      true,
			Capabilities: map[string]bool{"hls": false, "ptz": false, "snapshot": true, "discovery": false, "auth": true},
		},
		{
			ID:           "onvif",
			Label:        "ONVIF",
			Encodings:    []string{"h264", "h265", "mjpeg"},
			BuiltIn:      true,
			Capabilities: map[string]bool{"hls": true, "ptz": true, "snapshot": false, "discovery": true, "auth": true},
		},
		{
			ID:        "xiaomi",
			Label:     "Xiaomi",
			Encodings: []string{"h264", "h265"},
			BuiltIn:   true,
			// Xiaomi cameras authenticate via Xiaomi cloud account token, NOT
			// per-camera username/password. auth=false hides the credential
			// fields in the add/edit form (issue #73). ptz=true: PT models'
			// motor control is driven through the unified /ptz endpoints
			// (vector → motor direction/speed) plus the legacy
			// /xiaomi/ptz/* endpoints.
			Capabilities: map[string]bool{"hls": true, "ptz": true, "snapshot": false, "discovery": true, "auth": false},
		},
		{
			ID:           "srt",
			Label:        "SRT (push)",
			Encodings:    []string{"h264", "h265"},
			BuiltIn:      true,
			Capabilities: map[string]bool{"hls": true, "ptz": false, "snapshot": false, "discovery": false, "auth": false},
		},
		{
			ID:        "whip",
			Label:     "WHIP (WebRTC push)",
			Encodings: []string{"h264", "audio"},
			BuiltIn:   true,
			// WHIP publishers send the stream key inside the endpoint URL — no
			// per-camera credentials (same model as RTMP push keys).
			Capabilities: map[string]bool{"hls": true, "ptz": false, "snapshot": false, "discovery": false, "auth": false},
		},
		{
			ID:           "rtmp",
			Label:        "RTMP (push)",
			Encodings:    []string{"h264"},
			BuiltIn:      true,
			Capabilities: map[string]bool{"hls": true, "ptz": false, "snapshot": false, "discovery": false, "auth": false},
		},
		{
			ID:        "gb28181",
			Label:     "GB28181",
			Encodings: []string{"h264", "h265"},
			BuiltIn:   true,
			// GB/T 28181 devices register via SIP (server-side digest auth in
			// Settings — no per-camera credentials); PTZ rides the standard
			// camera PTZ endpoints which dispatch to DeviceControl.
			Capabilities: map[string]bool{"hls": true, "ptz": true, "snapshot": false, "discovery": false, "auth": false},
		},
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"protocols": protocols,
	})
}

// --- Feature toggle endpoints ---

func (s *server) handleGetFeatures(w http.ResponseWriter, r *http.Request) {
	flags, err := s.deps.DB.GetFeatureFlags(r.Context())
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "failed to get feature flags")
		return
	}
	protocols := make(map[string]bool)
	for k, v := range flags {
		if strings.HasPrefix(k, "protocol.") {
			proto := strings.TrimPrefix(k, "protocol.")
			protocols[proto] = v
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"protocols": protocols})
}

func (s *server) handleUpdateFeatures(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Protocols map[string]bool `json:"protocols"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	ctx := r.Context()
	for proto, enabled := range body.Protocols {
		if err := s.deps.DB.SetFeatureFlag(ctx, "protocol."+proto, enabled); err != nil {
			logger.Warn("failed to set feature flag", "protocol", proto, "error", err)
		}
		if s.deps.Cam != nil {
			s.deps.Cam.SetProtocolEnabled(proto, enabled)
		}
	}
	s.handleGetFeatures(w, r)
}
