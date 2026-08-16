package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/middleware"

	"github.com/go-chi/chi/v5"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
)

// --- Public endpoints ---

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

	// Stable device identity for LAN clients (#330)
	if h.config != nil {
		resp.DeviceID = h.config.Server.DeviceID
		resp.DeviceName = h.config.Server.DeviceName
	}
	writeJSON(w, http.StatusOK, resp)
}

// aggregateCameraHealth builds a CameraHealthSummary from the health manager and camera DB.
func (h *Handler) aggregateCameraHealth(r *http.Request) *CameraHealthSummary {
	allHealth := h.healthMgr.GetAllHealth()
	if allHealth == nil {
		allHealth = map[string]*model.CameraHealth{}
	}

	// Build camera name lookup from DB
	nameLookup := map[string]string{}
	if h.db != nil {
		cameras, err := h.db.ListCameras(r.Context())
		if err == nil {
			for _, c := range cameras {
				nameLookup[c.ID] = c.Name
			}
		}
	}

	summary := &CameraHealthSummary{Total: len(allHealth)}
	for id, ch := range allHealth {
		detail := CameraHealthDetail{
			ID:     id,
			Name:   nameLookup[id],
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

	return summary
}

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

func (h *Handler) handleLogin(w http.ResponseWriter, r *http.Request) {
	// Validate credentials by running through the auth middleware.
	// If auth is disabled, any request succeeds; otherwise BasicAuth is checked.
	// Use httptest.ResponseRecorder to capture middleware output without writing to client w.
	done := make(chan int, 1)

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		done <- http.StatusOK
	})
	rec := httptest.NewRecorder()
	h.authMW(inner).ServeHTTP(rec, r)

	select {
	case status := <-done:
		if status == http.StatusOK {
			// Credentials validated by the middleware (BasicAuth path). Mint a
			// stateless signed session token so the browser can stop carrying the
			// reversible base64(user:pass). The username comes from the request's
			// BasicAuth header (just validated); the bcrypt hash from config drives
			// the HMAC key, so a later password change invalidates this token.
			// Tests use a nil-config handler, so guard against that here.
			if h.config == nil {
				writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
				return
			}
			username := h.config.Auth.Username
			if u, _, ok := r.BasicAuth(); ok && u != "" {
				username = u
			}
			hash := h.config.Auth.PasswordHash
			token, expiresAt := middleware.SignSessionToken(username, hash, time.Now())
			writeJSON(w, http.StatusOK, map[string]string{
				"status":     "ok",
				"token":      token,
				"expires_at": expiresAt.UTC().Format(time.RFC3339),
			})
		}
	default:
		// Forward the middleware's captured response (503 SETUP_REQUIRED, 401, etc.)
		// without double-writing to the client.
		for k, vv := range rec.Header() {
			for _, v := range vv {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(rec.Code)
		w.Write(rec.Body.Bytes())
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

// --- Settings endpoints ---

func (h *Handler) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	if h.config == nil {
		WriteError(w, http.StatusInternalServerError, "config not available")
		return
	}

	// Compute timezone display string
	tzDisplay := h.config.Timezone
	if tzDisplay != "" && tzDisplay != "UTC" && tzDisplay != "Local" {
		if loc, err := time.LoadLocation(tzDisplay); err == nil {
			_, offset := time.Now().In(loc).Zone()
			tzDisplay = fmt.Sprintf("%s (UTC%s)", tzDisplay, formatOffset(offset))
		}
	} else if tzDisplay == "UTC" {
		tzDisplay = "UTC"
	} else if tzDisplay == "Local" {
		if loc, err := time.LoadLocation("Local"); err == nil {
			name, offset := time.Now().In(loc).Zone()
			tzDisplay = fmt.Sprintf("%s (UTC%s)", name, formatOffset(offset))
		} else {
			tzDisplay = "Local"
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"cleanup": map[string]any{
			"retention_days":         h.config.Cleanup.RetentionDays,
			"check_interval":         h.config.Cleanup.CheckInterval,
			"disk_threshold_percent": h.config.Cleanup.DiskThresholdPercent,
		},
		"webdav": map[string]any{
			"enabled":     h.config.WebDAV.Enabled != nil && *h.config.WebDAV.Enabled,
			"path_prefix": h.config.WebDAV.PathPrefix,
			"read_write":  h.config.WebDAV.ReadWrite,
		},
		"auth": map[string]any{
			"username":        h.config.Auth.Username,
			"auth_configured": h.config.Auth.PasswordHash != "" || h.config.Auth.Password != "",
		},
		"mibeevision": map[string]any{
			"api_keys": buildAPIKeyInfo(h.config.APIKeys, h.apiKeyLastUsed()),
		},
		"timezone":         h.config.Timezone,
		"timezone_display": tzDisplay,
		"server": map[string]any{
			"listen": h.config.Server.Listen,
		},
		"gb28181": map[string]any{
			"enabled":    h.config.GB28181.Enabled,
			"sip_listen": h.config.GB28181.SIPListen,
			"server_id":  h.config.GB28181.ServerID,
			"realm":      h.config.GB28181.Realm,
			// Never return the SIP password (mirrors the masked auth/API-key
			// fields above); the frontend only needs whether one is set.
			"password_configured": h.config.GB28181.Password != "",
			"port_range":          h.config.GB28181.PortRange,
			"allowed_device_ids":  h.config.GB28181.AllowedDeviceIDs,
			"heartbeat_interval":  h.config.GB28181.HeartbeatInterval,
			"catalog_interval":    h.config.GB28181.CatalogInterval,
			"tcp_mode":            h.config.GB28181.TCPMode,
			"tcp_framing":         h.config.GB28181.TCPFraming,
			"media_transport":     h.config.GB28181.MediaTransport,
			"sip_transport":       h.config.GB28181.SIPTransport,
			// Subscription toggles (#341): resolve the *bool defaults so the
			// UI sees the effective values.
			"subscribe_catalog":         h.config.GB28181.CatalogSubscriptionOn(),
			"subscribe_alarm":           h.config.GB28181.AlarmSubscriptionOn(),
			"subscribe_mobile_position": h.config.GB28181.SubscribeMobilePosition,
			"subscribe_expires":         h.config.GB28181.SubscribeExpires,
		},
	})
}

func formatOffset(seconds int) string {
	sign := "+"
	if seconds < 0 {
		sign = "-"
		seconds = -seconds
	}
	hours := seconds / 3600
	mins := (seconds % 3600) / 60
	if mins == 0 {
		return fmt.Sprintf("%s%d", sign, hours)
	}
	return fmt.Sprintf("%s%d:%02d", sign, hours, mins)
}

// buildAPIKeyInfo returns a safe summary of configured API keys (never the key
// itself). Revoked keys are included (grayed in the UI) so owners can see the
// full per-device token list; last_used comes from the live store when wired.
func buildAPIKeyInfo(keys []config.APIKeyConfig, lastUsed map[string]time.Time) []map[string]any {
	result := make([]map[string]any, 0, len(keys))
	for _, k := range keys {
		info := map[string]any{
			"name":    k.Name,
			"prefix":  k.Key[:min(8, len(k.Key))] + "…", // e.g. "mbv_ab12…"
			"revoked": k.Revoked,
		}
		if t, ok := lastUsed[k.Name]; ok && !t.IsZero() {
			info["last_used"] = t.UTC().Format(time.RFC3339)
		}
		result = append(result, info)
	}
	return result
}

// syncAPIKeyStore rebuilds the live key set from the in-memory config. Called
// after generate/revoke mutations so the API-key middleware picks up the
// change on the next request without a restart (#335).
func (h *Handler) syncAPIKeyStore() {
	if h.apiKeyStore == nil || h.config == nil {
		return
	}
	valid := make(map[string]string)
	for _, k := range h.config.APIKeys {
		if !k.Revoked && k.Key != "" {
			valid[k.Key] = k.Name
		}
	}
	h.apiKeyStore.SetKeys(valid)
}

// apiKeyLastUsed returns the store's last-used map, or nil when no store is
// wired (tests, sub-handlers).
func (h *Handler) apiKeyLastUsed() map[string]time.Time {
	if h.apiKeyStore == nil {
		return nil
	}
	return h.apiKeyStore.LastUsed()
}

func (h *Handler) handleUpdateSettings(w http.ResponseWriter, r *http.Request) {
	if h.config == nil {
		WriteError(w, http.StatusInternalServerError, "config not available")
		return
	}

	var body struct {
		Cleanup *struct {
			RetentionDays        *int    `json:"retention_days"`
			DiskThresholdPercent *int    `json:"disk_threshold_percent"`
			CheckInterval        *string `json:"check_interval"`
		} `json:"cleanup"`
		WebDAV *struct {
			Enabled    *bool   `json:"enabled"`
			PathPrefix *string `json:"path_prefix"`
			ReadWrite  *bool   `json:"read_write"`
		} `json:"webdav"`
		Timezone *string `json:"timezone"`
		Server   *struct {
			Listen *string `json:"listen"`
		} `json:"server"`
		GB28181 *struct {
			Enabled           *bool     `json:"enabled"`
			SIPListen         *string   `json:"sip_listen"`
			ServerID          *string   `json:"server_id"`
			Realm             *string   `json:"realm"`
			Password          *string   `json:"password"`
			PortRange         *string   `json:"port_range"`
			AllowedDeviceIDs  *[]string `json:"allowed_device_ids"`
			HeartbeatInterval *string   `json:"heartbeat_interval"`
			CatalogInterval   *string   `json:"catalog_interval"`
			TCPMode           *bool     `json:"tcp_mode"`
			TCPFraming        *string   `json:"tcp_framing"`
			MediaTransport    *string   `json:"media_transport"`
			SIPTransport      *string   `json:"sip_transport"`

			SubscribeCatalog        *bool   `json:"subscribe_catalog"`
			SubscribeAlarm          *bool   `json:"subscribe_alarm"`
			SubscribeMobilePosition *bool   `json:"subscribe_mobile_position"`
			SubscribeExpires        *string `json:"subscribe_expires"`
		} `json:"gb28181"`
	}

	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// Update cleanup settings
	if body.Cleanup != nil {
		if body.Cleanup.RetentionDays != nil {
			if *body.Cleanup.RetentionDays < 1 {
				WriteError(w, http.StatusBadRequest, "retention_days must be >= 1")
				return
			}
			h.config.Cleanup.RetentionDays = *body.Cleanup.RetentionDays
		}
		if body.Cleanup.DiskThresholdPercent != nil {
			if *body.Cleanup.DiskThresholdPercent < 1 || *body.Cleanup.DiskThresholdPercent > 100 {
				WriteError(w, http.StatusBadRequest, "disk_threshold_percent must be between 1 and 100")
				return
			}
			h.config.Cleanup.DiskThresholdPercent = *body.Cleanup.DiskThresholdPercent
		}
		if body.Cleanup.CheckInterval != nil {
			// An empty/whitespace string means "keep current value" (partial PUT
			// semantics). Previously any non-nil value — including "" sent by the
			// cleanup settings UI — hit time.ParseDuration("") → 400 and aborted
			// the whole cleanup save (#294). Treat blank as "no change".
			if trimmed := strings.TrimSpace(*body.Cleanup.CheckInterval); trimmed != "" {
				if _, err := time.ParseDuration(trimmed); err != nil {
					WriteError(w, http.StatusBadRequest, "check_interval must be a valid duration (e.g., \"30m\", \"1h\")")
					return
				}
				h.config.Cleanup.CheckInterval = trimmed
			}
		}
	}

	// Update webdav settings
	if body.WebDAV != nil {
		if body.WebDAV.Enabled != nil {
			if h.config.WebDAV.Enabled == nil {
				h.config.WebDAV.Enabled = new(bool)
			}
			*h.config.WebDAV.Enabled = *body.WebDAV.Enabled
		}
		if body.WebDAV.PathPrefix != nil {
			h.config.WebDAV.PathPrefix = *body.WebDAV.PathPrefix
		}
		if body.WebDAV.ReadWrite != nil {
			h.config.WebDAV.ReadWrite = *body.WebDAV.ReadWrite
		}
	}

	// Update timezone
	if body.Timezone != nil {
		tz := strings.TrimSpace(*body.Timezone)
		if tz != "" && tz != "UTC" && tz != "Local" {
			if _, err := time.LoadLocation(tz); err != nil {
				WriteError(w, http.StatusBadRequest, fmt.Sprintf("invalid timezone: %q", tz))
				return
			}
		}
		h.config.Timezone = tz
	}

	// Update server listen port
	if body.Server != nil && body.Server.Listen != nil {
		raw := strings.TrimSpace(*body.Server.Listen)
		raw = strings.TrimPrefix(raw, ":")
		port, err := strconv.Atoi(raw)
		if err != nil || port < 1 || port > 65535 {
			WriteError(w, http.StatusBadRequest, "listen must be a valid port (1-65535)")
			return
		}
		h.config.Server.Listen = fmt.Sprintf(":%d", port)
	}

	// Update GB28181 settings
	if body.GB28181 != nil {
		if body.GB28181.Enabled != nil {
			h.config.GB28181.Enabled = *body.GB28181.Enabled
		}
		if body.GB28181.SIPListen != nil {
			h.config.GB28181.SIPListen = *body.GB28181.SIPListen
		}
		if body.GB28181.ServerID != nil {
			h.config.GB28181.ServerID = *body.GB28181.ServerID
		}
		if body.GB28181.Realm != nil {
			h.config.GB28181.Realm = *body.GB28181.Realm
		}
		if body.GB28181.Password != nil {
			// Blank password = keep current (the GET no longer returns the
			// password, so the UI round-trips an empty field when unchanged).
			if trimmed := strings.TrimSpace(*body.GB28181.Password); trimmed != "" {
				h.config.GB28181.Password = trimmed
			}
		}
		if body.GB28181.PortRange != nil {
			h.config.GB28181.PortRange = *body.GB28181.PortRange
		}
		if body.GB28181.AllowedDeviceIDs != nil {
			h.config.GB28181.AllowedDeviceIDs = *body.GB28181.AllowedDeviceIDs
		}
		if body.GB28181.HeartbeatInterval != nil {
			h.config.GB28181.HeartbeatInterval = *body.GB28181.HeartbeatInterval
		}
		if body.GB28181.CatalogInterval != nil {
			h.config.GB28181.CatalogInterval = *body.GB28181.CatalogInterval
		}
		if body.GB28181.TCPMode != nil {
			h.config.GB28181.TCPMode = *body.GB28181.TCPMode
		}
		if body.GB28181.TCPFraming != nil {
			h.config.GB28181.TCPFraming = *body.GB28181.TCPFraming
		}
		if body.GB28181.MediaTransport != nil {
			h.config.GB28181.MediaTransport = *body.GB28181.MediaTransport
			// Keep the legacy alias coherent for older config readers.
			h.config.GB28181.TCPMode = *body.GB28181.MediaTransport != "udp"
		}
		if body.GB28181.SIPTransport != nil {
			h.config.GB28181.SIPTransport = *body.GB28181.SIPTransport
		}
		if body.GB28181.SubscribeCatalog != nil {
			h.config.GB28181.SubscribeCatalog = body.GB28181.SubscribeCatalog
		}
		if body.GB28181.SubscribeAlarm != nil {
			h.config.GB28181.SubscribeAlarm = body.GB28181.SubscribeAlarm
		}
		if body.GB28181.SubscribeMobilePosition != nil {
			h.config.GB28181.SubscribeMobilePosition = *body.GB28181.SubscribeMobilePosition
		}
		if body.GB28181.SubscribeExpires != nil {
			if trimmed := strings.TrimSpace(*body.GB28181.SubscribeExpires); trimmed != "" {
				if _, err := time.ParseDuration(trimmed); err != nil {
					WriteError(w, http.StatusBadRequest, "subscribe_expires must be a valid duration (e.g., \"3600s\", \"2h\")")
					return
				}
				h.config.GB28181.SubscribeExpires = trimmed
			}
		}
		// Validate the updated config (catches invalid server_id, sip_listen, etc.)
		if err := config.Validate(h.config); err != nil {
			WriteError(w, http.StatusBadRequest, err.Error())
			return
		}
	}

	// Persist config to disk
	if err := config.Save(h.configPath, h.config); err != nil {
		logger.Warn("failed to save config", "error", err)
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

// handleGenerateAPIKey creates a new API key for MiBeeVision integration.
// POST /api/settings/api-keys  body: {"name": "mibeevision-prod"}
// Returns the full key ONCE (never exposed again).
func (h *Handler) handleGenerateAPIKey(w http.ResponseWriter, r *http.Request) {
	if h.config == nil {
		WriteError(w, http.StatusInternalServerError, "config not available")
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	name := strings.TrimSpace(body.Name)
	if name == "" {
		name = "mibeevision"
	}

	key := middleware.GenerateAPIKey()
	h.config.APIKeys = append(h.config.APIKeys, config.APIKeyConfig{
		Key:  key,
		Name: name,
	})

	if err := config.Save(h.configPath, h.config); err != nil {
		WriteError(w, http.StatusInternalServerError, "failed to save config")
		return
	}
	h.syncAPIKeyStore()

	logger.Info("API key generated", "name", name)
	writeJSON(w, http.StatusCreated, map[string]any{
		"name":   name,
		"key":    key,
		"prefix": key[:min(8, len(key))] + "…",
	})
}

// handleRevokeAPIKey marks an API key as revoked by name.
// DELETE /api/settings/api-keys/{name}
func (h *Handler) handleRevokeAPIKey(w http.ResponseWriter, r *http.Request) {
	if h.config == nil {
		WriteError(w, http.StatusInternalServerError, "config not available")
		return
	}
	name := chi.URLParam(r, "name")
	if name == "" {
		WriteError(w, http.StatusBadRequest, "name is required")
		return
	}

	found := false
	for i := range h.config.APIKeys {
		if h.config.APIKeys[i].Name == name {
			h.config.APIKeys[i].Revoked = true
			found = true
			break
		}
	}
	if !found {
		WriteError(w, http.StatusNotFound, "API key not found")
		return
	}

	if err := config.Save(h.configPath, h.config); err != nil {
		WriteError(w, http.StatusInternalServerError, "failed to save config")
		return
	}
	h.syncAPIKeyStore()

	logger.Info("API key revoked", "name", name)
	writeJSON(w, http.StatusOK, map[string]string{"status": "revoked"})
}

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

func (h *Handler) handleBackup(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		WriteError(w, http.StatusInternalServerError, "database not available")
		return
	}
	backupDir := filepath.Join(filepath.Dir(h.configPath), "backups")
	if err := os.MkdirAll(backupDir, 0o755); err != nil {
		WriteError(w, http.StatusInternalServerError, "failed to create backup directory")
		return
	}
	filename := fmt.Sprintf("nvr-backup-%s.db", time.Now().Format("20060102-150405"))
	destPath := filepath.Join(backupDir, filename)
	if err := h.db.Backup(r.Context(), destPath); err != nil {
		WriteError(w, http.StatusInternalServerError, "failed to create backup")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "created", "file": filename})
}

func (h *Handler) handleListBackups(w http.ResponseWriter, r *http.Request) {
	backupDir := filepath.Join(filepath.Dir(h.configPath), "backups")
	entries, err := os.ReadDir(backupDir)
	if err != nil {
		writeJSON(w, http.StatusOK, []string{})
		return
	}
	var backups []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".db") {
			backups = append(backups, e.Name())
		}
	}
	if backups == nil {
		backups = []string{}
	}
	writeJSON(w, http.StatusOK, backups)
}

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
			Capabilities: map[string]bool{"hls": false, "ptz": false, "snapshot": false, "discovery": false, "auth": false},
		},
		{
			ID:           "rtmp",
			Label:        "RTMP (push)",
			Encodings:    []string{"h264"},
			BuiltIn:      true,
			Capabilities: map[string]bool{"hls": false, "ptz": false, "snapshot": false, "discovery": false, "auth": false},
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
func formatUptime(d time.Duration) string {
	rounded := d.Round(time.Second)
	h := rounded / time.Hour
	rounded -= h * time.Hour
	m := rounded / time.Minute
	rounded -= m * time.Minute
	s := rounded / time.Second
	if h > 0 {
		return fmt.Sprintf("%dh %dm %ds", h, m, s)
	}
	if m > 0 {
		return fmt.Sprintf("%dm %ds", m, s)
	}
	return fmt.Sprintf("%ds", s)
}

// --- System stats helpers (Linux /proc) ---

func readCPURaw() (total, idle uint64, err error) {
	data, err := os.ReadFile("/proc/stat")
	if err != nil {
		return 0, 0, err
	}
	lines := strings.Split(string(data), "\n")
	if len(lines) == 0 {
		return 0, 0, fmt.Errorf("empty /proc/stat")
	}
	fields := strings.Fields(lines[0])
	if len(fields) < 5 {
		return 0, 0, fmt.Errorf("unexpected /proc/stat format")
	}
	for i := 1; i < len(fields); i++ {
		v, _ := strconv.ParseUint(fields[i], 10, 64)
		total += v
	}
	idle, _ = strconv.ParseUint(fields[4], 10, 64)
	return
}

func readMemoryInfo() (total, available uint64, err error) {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, 0, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		v, _ := strconv.ParseUint(fields[1], 10, 64)
		switch fields[0] {
		case "MemTotal:":
			total = v * 1024
		case "MemAvailable:":
			available = v * 1024
		}
	}
	return
}

func readNetworkInfo() (bytesSent, bytesRecv uint64, err error) {
	data, err := os.ReadFile("/proc/net/dev")
	if err != nil {
		return 0, 0, err
	}
	// Try eth0 or wlan0 first
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "eth0:") && !strings.HasPrefix(trimmed, "wlan0:") {
			continue
		}
		parts := strings.SplitN(trimmed, ":", 2)
		if len(parts) < 2 {
			continue
		}
		fields := strings.Fields(parts[1])
		if len(fields) < 10 {
			continue
		}
		bytesRecv, _ = strconv.ParseUint(fields[0], 10, 64)
		bytesSent, _ = strconv.ParseUint(fields[8], 10, 64)
		return
	}
	// Fallback: sum all interfaces
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.Contains(trimmed, ":") {
			continue
		}
		parts := strings.SplitN(trimmed, ":", 2)
		if len(parts) < 2 {
			continue
		}
		fields := strings.Fields(parts[1])
		if len(fields) < 10 {
			continue
		}
		r, _ := strconv.ParseUint(fields[0], 10, 64)
		s, _ := strconv.ParseUint(fields[8], 10, 64)
		bytesRecv += r
		bytesSent += s
	}
	return
}

func readProcessRSS() uint64 {
	data, err := os.ReadFile("/proc/self/statm")
	if err != nil {
		return 0
	}
	fields := strings.Fields(string(data))
	if len(fields) < 2 {
		return 0
	}
	rssPages, _ := strconv.ParseUint(fields[1], 10, 64)
	return rssPages * uint64(os.Getpagesize())
}

func (h *Handler) handleSystemStats(w http.ResponseWriter, r *http.Request) {
	cpuTotal, cpuIdle, _ := readCPURaw()
	memTotal, memAvailable, _ := readMemoryInfo()
	netSent, netRecv, _ := readNetworkInfo()
	processRSS := readProcessRSS()

	writeJSON(w, http.StatusOK, SystemStats{
		CPU:       CPUStats{Total: cpuTotal, Idle: cpuIdle},
		Memory:    MemoryStats{Total: memTotal, Available: memAvailable, ProcessRSS: processRSS},
		Network:   NetworkStats{BytesSent: netSent, BytesRecv: netRecv},
		Uptime:    formatUptime(time.Since(appStartTime)),
		Timestamp: time.Now().Unix(),
	})
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
func (h *Handler) registerSystemRoutes(r chi.Router) {
	r.Get("/api/stats", h.handleStats)
	r.Get("/api/stats/system", h.handleSystemStats)
	r.Get("/api/stats/trends", h.handleStatsTrends)
	r.Get("/api/settings", h.handleGetSettings)
	r.Put("/api/settings", h.handleUpdateSettings)
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
	r.Post("/api/backup", h.handleBackup)
	r.Get("/api/backups", h.handleListBackups)
	r.Get("/api/protocols", h.handleProtocols)
	r.Get("/api/features", h.handleGetFeatures)
	r.Put("/api/features", h.handleUpdateFeatures)
	r.Get("/api/version", h.handleVersion)
	r.Get("/api/update/check", h.handleUpdateCheck)
	r.Post("/api/update/check", h.handleUpdateCheck)
}
