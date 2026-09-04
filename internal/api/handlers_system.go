package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/middleware"

	"github.com/go-chi/chi/v5"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
)

func (h *Handler) handleHealth(w http.ResponseWriter, r *http.Request) {
	resp := HealthResponse{Checks: make(map[string]HealthCheck)}
	hasWarning, hasError := false, false

	// Database check
	if h.db != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		err := h.db.DB().PingContext(ctx)
		if err != nil {
			resp.Checks["database"] = HealthCheck{Status: "error", Message: err.Error()}
			hasError = true
		} else {
			resp.Checks["database"] = HealthCheck{Status: "ok"}
		}
	} else {
		resp.Checks["database"] = HealthCheck{Status: "error", Message: "database not configured"}
		hasError = true
	}

	// Storage check — combines disk usage with I/O health state.
	if h.store != nil {
		total, used, err := h.store.GetDiskUsage()
		if err != nil {
			resp.Checks["storage"] = HealthCheck{Status: "error", Message: err.Error()}
			hasError = true
		} else {
			pct := 0
			if total > 0 {
				pct = int(float64(used) / float64(total) * 100)
			}
			msg := fmt.Sprintf("%d%% used (%d / %d bytes)", pct, used, total)

			// Check I/O health state — per-camera health aggregated.
			storageFailed := h.store.StorageFailedLegacy()
			var healthMsg string
			if storageFailed {
				healthMsg = " I/O errors detected — some cameras may fail to record"
				hasError = true
			}

			if storageFailed {
				resp.Checks["storage"] = HealthCheck{Status: "error", Message: msg + healthMsg}
			} else if pct > 95 {
				resp.Checks["storage"] = HealthCheck{Status: "error", Message: msg}
				hasError = true
			} else if pct > 90 {
				resp.Checks["storage"] = HealthCheck{Status: "warning", Message: msg}
				hasWarning = true
			} else {
				resp.Checks["storage"] = HealthCheck{Status: "ok", Message: msg}
			}
		}
	} else {
		resp.Checks["storage"] = HealthCheck{Status: "error", Message: "storage not configured"}
		hasError = true
	}
	// Goroutine check
	numGoroutines := runtime.NumGoroutine()
	if numGoroutines > 1000 {
		resp.Checks["goroutines"] = HealthCheck{Status: "error", Message: fmt.Sprintf("%d goroutines (threshold: 1000)", numGoroutines)}
		hasError = true
	} else {
		resp.Checks["goroutines"] = HealthCheck{Status: "ok", Message: fmt.Sprintf("%d goroutines", numGoroutines)}
	}

	// Camera health aggregation (influences overall status)
	if h.healthMgr != nil {
		camHealth := h.aggregateCameraHealth(r)
		resp.Cameras = camHealth
		if camHealth != nil {
			if camHealth.Error > 0 {
				hasWarning = true // any camera in error = degraded
			}
			if camHealth.Reconnecting > 0 {
				hasWarning = true // any reconnecting = degraded
			}
			if camHealth.Total > 0 && camHealth.Offline > camHealth.Total/2 {
				hasError = true // majority offline = error
			}
		}
	}

	// Overall status
	switch {
	case hasError:
		resp.Status = "unhealthy"
	case hasWarning:
		resp.Status = "degraded"
	default:
		resp.Status = "ok"
	}

	// Uptime
	resp.Uptime = formatUptime(time.Since(appStartTime))

	// SetupRequired — true when no password is configured
	resp.SetupRequired = h.config != nil && h.config.Auth.PasswordHash == "" && h.config.Auth.Password == ""

	// LocalAccess — true only when the request is genuinely from a browser on
	// the NVR host machine: a loopback connection with a loopback Host header,
	// no proxy/gateway headers, AND the operator enabled auth.local_bypass.
	// Reuses middleware.IsBypassEligible so this cannot drift from the auth
	// middleware's own gate. Kept strict for reverse-proxy and Docker published-
	// port deployments, where every request arrives from 127.0.0.1 and would
	// otherwise be misreported as local (and the frontend would skip the login
	// page for remote users).
	localBypass := h.config != nil && h.config.Auth.LocalBypass != nil && *h.config.Auth.LocalBypass
	resp.LocalAccess = localBypass && middleware.IsBypassEligible(r)

	// Stable device identity for LAN clients (#330)
	if h.config != nil {
		resp.DeviceID = h.config.Server.DeviceID
		resp.DeviceName = h.config.Server.DeviceName
	}
	writeJSON(w, http.StatusOK, resp)
}

// aggregateCameraHealth builds a CameraHealthSummary from the health manager and camera DB.

// aggregateCameraHealth builds a CameraHealthSummary from the health manager and camera DB.
func (h *Handler) aggregateCameraHealth(r *http.Request) *CameraHealthSummary {
	allHealth := h.healthMgr.GetAllHealth()
	if allHealth == nil {
		allHealth = map[string]*model.CameraHealth{}
	}

	// Active-camera lookup from DB. ListCameras excludes archived rows, so it
	// doubles as the filter that keeps retired cameras out of the aggregate:
	// an archived camera (e.g. the auto-enrolled device-self pseudo-channel
	// camera retired once its catalog arrived, #416) keeps its last health
	// entry forever and would pin the system degraded eternally (#420). When
	// the lookup is unavailable (no DB / query error) we count everything
	// rather than reporting an empty system.
	var active map[string]string // nil = unknown, count all
	if h.db != nil {
		if cameras, err := h.db.ListCameras(r.Context()); err == nil {
			active = make(map[string]string, len(cameras))
			for _, c := range cameras {
				active[c.ID] = c.Name
			}
		}
	}

	summary := &CameraHealthSummary{}
	for id, ch := range allHealth {
		if active != nil {
			if _, ok := active[id]; !ok {
				continue
			}
		}
		detail := CameraHealthDetail{
			ID:     id,
			Name:   active[id],
			Score:  ch.Score,
			Status: ch.LatestStatus,
		}
		summary.Details = append(summary.Details, detail)

		switch ch.LatestStatus {
		case "healthy", "recording":
			summary.Recording++
		case "reconnecting":
			summary.Reconnecting++
		case "error", "unhealthy":
			summary.Error++
		default:
			summary.Offline++
		}
	}
	summary.Total = len(summary.Details)

	return summary
}

// handleHealthCameras returns full camera health map with scores.
// Public endpoint — no auth required.

// handleHealthCameras returns full camera health map with scores.
// Public endpoint — no auth required.
func (h *Handler) handleHealthCameras(w http.ResponseWriter, r *http.Request) {
	if h.healthMgr == nil {
		writeJSON(w, http.StatusOK, map[string]*model.CameraHealth{})
		return
	}
	health := h.healthMgr.GetAllHealth()
	if health == nil {
		health = map[string]*model.CameraHealth{}
	}
	writeJSON(w, http.StatusOK, health)
}

func (h *Handler) handleReadyz(w http.ResponseWriter, r *http.Request) {
	checks := make(map[string]HealthCheck)

	// Database must be ok
	allOK := true
	if h.db == nil {
		checks["database"] = HealthCheck{Status: "error", Message: "database not configured"}
		allOK = false
	} else {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := h.db.DB().PingContext(ctx); err != nil {
			checks["database"] = HealthCheck{Status: "error", Message: err.Error()}
			allOK = false
		} else {
			checks["database"] = HealthCheck{Status: "ok"}
		}
	}

	// Storage must be < 95%
	if h.store == nil {
		checks["storage"] = HealthCheck{Status: "error", Message: "storage not configured"}
		allOK = false
	} else {
		total, used, err := h.store.GetDiskUsage()
		if err != nil {
			checks["storage"] = HealthCheck{Status: "error", Message: err.Error()}
			allOK = false
		} else {
			pct := 0
			if total > 0 {
				pct = int(float64(used) / float64(total) * 100)
			}
			if pct >= 95 {
				checks["storage"] = HealthCheck{Status: "error", Message: fmt.Sprintf("%d%% used (threshold: 95%%)", pct)}
				allOK = false
			} else {
				checks["storage"] = HealthCheck{Status: "ok"}
			}
		}
	}

	// Goroutines must be < 5000
	numGoroutines := runtime.NumGoroutine()
	if numGoroutines >= 5000 {
		checks["goroutines"] = HealthCheck{Status: "error", Message: fmt.Sprintf("%d goroutines (threshold: 5000)", numGoroutines)}
		allOK = false
	} else {
		checks["goroutines"] = HealthCheck{Status: "ok"}
	}

	if allOK {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	} else {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "not ready", "checks": checks})
	}
}

func (h *Handler) handleStats(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	total, used, err := h.store.GetDiskUsage()
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "failed to get disk usage")
		return
	}

	count, err := h.db.CountRecordings(ctx)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "failed to count recordings")
		return
	}

	// Camera count from the in-memory config (O(1)) — avoids a redundant
	// ListCameras DB round-trip on every 30s Dashboard poll.
	var cameraCount int
	if h.camMgr != nil {
		cameraCount = h.camMgr.CameraCount()
	} else {
		cameras, err := h.db.ListCameras(ctx)
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

func (h *Handler) handleStatsTrends(w http.ResponseWriter, r *http.Request) {
	days := 7
	if v := r.URL.Query().Get("days"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 && n <= 30 {
			days = n
		}
	}

	// Load display timezone from config
	loc := time.UTC
	if h.config != nil && h.config.Timezone != "" && h.config.Timezone != "UTC" {
		if l, err := time.LoadLocation(h.config.Timezone); err == nil {
			loc = l
		}
	}

	trends, err := h.db.GetRecordingTrends(r.Context(), days, loc)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "failed to get recording trends")
		return
	}
	writeJSON(w, http.StatusOK, trends)
}

// handleStatsCameras serves GET /api/stats/cameras — the per-camera storage
// footprint (bytes + segment count) rendered on the Dashboard's storage-usage
// card. Backed by a short-lived DB cache (see GetCameraStorageStats).
func (h *Handler) handleStatsCameras(w http.ResponseWriter, r *http.Request) {
	stats, err := h.db.GetCameraStorageStats(r.Context())
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

func (h *Handler) handleProtocols(w http.ResponseWriter, r *http.Request) {
	protocols := []protocolInfo{
		{
			ID:           "rtsp",
			Label:        "RTSP",
			Encodings:    []string{"h264", "h265", "mjpeg"},
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
			// fields in the add/edit form (issue #73).
			Capabilities: map[string]bool{"hls": true, "ptz": false, "snapshot": false, "discovery": true, "auth": false},
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
			Encodings: []string{"h264"},
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

func (h *Handler) handleGetFeatures(w http.ResponseWriter, r *http.Request) {
	flags, err := h.db.GetFeatureFlags(r.Context())
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

func (h *Handler) handleUpdateFeatures(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Protocols map[string]bool `json:"protocols"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	ctx := r.Context()
	for proto, enabled := range body.Protocols {
		if err := h.db.SetFeatureFlag(ctx, "protocol."+proto, enabled); err != nil {
			logger.Warn("failed to set feature flag", "protocol", proto, "error", err)
		}
		if h.camMgr != nil {
			h.camMgr.SetProtocolEnabled(proto, enabled)
		}
	}
	// Return updated state
	h.handleGetFeatures(w, r)
}

// formatUptime converts a duration to a human-readable string like "2h 15m 30s".

// registerSystemRoutes registers system/stats/settings/backup/protocol/feature routes.
func (h *Handler) registerSystemRoutes(r chi.Router) {
	r.Get("/api/stats", h.handleStats)
	r.Get("/api/stats/system", h.handleSystemStats)
	r.Get("/api/stats/trends", h.handleStatsTrends)
	r.Get("/api/stats/cameras", h.handleStatsCameras)
	r.Get("/api/settings", h.handleGetSettings)
	r.Put("/api/settings", h.handleUpdateSettings)
	r.Get("/api/storage/candidates", h.handleStorageCandidates)
	r.Post("/api/storage/candidates", h.handleAddStorageCandidate)
	r.Delete("/api/storage/candidates", h.handleRemoveStorageCandidate)
	r.Post("/api/storage/migrate", h.handleStartStorageMigrate)
	r.Get("/api/storage/migrate/status", h.handleStorageMigrateStatus)
	r.Get("/api/cameras/{id}/storage-root", h.handleGetCameraStorageRoot)
	r.Put("/api/cameras/{id}/storage-root", h.handleSetCameraStorageRoot)
	r.Post("/api/settings/api-keys", h.handleGenerateAPIKey)
	r.Delete("/api/settings/api-keys/{name}", h.handleRevokeAPIKey)
	r.Get("/api/settings/merge", h.handleGetMergeSettings)
	r.Put("/api/settings/merge", h.handleUpdateMergeSettings)
	r.Get("/api/settings/streaming", h.handleGetStreamingSettings)
	r.Put("/api/settings/streaming", h.handleUpdateStreamingSettings)
	r.Get("/api/settings/auto-discover", h.handleGetAutoDiscoverSettings)
	r.Put("/api/settings/auto-discover", h.handleUpdateAutoDiscoverSettings)
	r.Get("/api/settings/transcoding", h.handleGetTranscodingSettings)
	r.Put("/api/settings/transcoding", h.handleUpdateTranscodingSettings)
	r.Get("/api/settings/rtsp-output", h.handleGetRtspOutputSettings)
	r.Put("/api/settings/rtsp-output", h.handleUpdateRtspOutputSettings)
	r.Post("/api/backup", h.handleBackup)
	r.Get("/api/backups", h.handleListBackups)
	r.Get("/api/protocols", h.handleProtocols)
	r.Get("/api/features", h.handleGetFeatures)
	r.Put("/api/features", h.handleUpdateFeatures)
	r.Get("/api/version", h.handleVersion)
	r.Get("/api/update/check", h.handleUpdateCheck)
	r.Post("/api/update/check", h.handleUpdateCheck)
	// Upgrade execution (#648) — BasicAuth-protected like the rest of /api.
	r.Post("/api/update/apply", h.handleUpdateApply)
	r.Get("/api/update/apply/status", h.handleUpdateApplyStatus)
	r.Get("/api/update/history", h.handleUpdateHistory)
}
