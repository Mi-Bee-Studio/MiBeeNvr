package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/middleware"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"

	"github.com/go-chi/chi/v5"
)

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
			// Motion-aware disk cleanup (#435): when the disk threshold is
			// hit, evict boring (low motion_score) segments first. nil = on.
			"motion_aware_disk_cleanup": h.config.Cleanup.MotionAwareDiskCleanup == nil || *h.config.Cleanup.MotionAwareDiskCleanup,
		},
		"webdav": map[string]any{
			"enabled":     h.config.WebDAV.Enabled != nil && *h.config.WebDAV.Enabled,
			"path_prefix": h.config.WebDAV.PathPrefix,
			"read_write":  h.config.WebDAV.ReadWrite,
		},
		"storage": map[string]any{
			"root_dir": h.config.Storage.RootDir,
		},
		"auth": map[string]any{
			"username":        h.config.Auth.Username,
			"auth_configured": h.config.Auth.PasswordHash != "" || h.config.Auth.Password != "",
		},
		"mibeevision": map[string]any{
			"api_keys": buildAPIKeyInfo(h.config.APIKeys, h.apiKeyLastUsed()),
		},
		"update": map[string]any{
			// Bare-metal auto-apply toggle (#648 UI switch, #647 execution).
			"auto_apply": h.config.Update.IsAutoApply(),
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
			// Motion-aware disk cleanup toggle (#435): evict boring segments
			// first when the disk threshold is hit. nil = unchanged.
			MotionAwareDiskCleanup *bool `json:"motion_aware_disk_cleanup"`
		} `json:"cleanup"`
		Storage *struct {
			// Recording root directory (#395). Takes effect on the NEXT start —
			// the DB and all subsystems open paths under the old root at boot,
			// so a live switch would corrupt state. The response carries
			// restart_required=true when this changes.
			RootDir *string `json:"root_dir"`
		} `json:"storage"`
		WebDAV *struct {
			Enabled    *bool   `json:"enabled"`
			PathPrefix *string `json:"path_prefix"`
			ReadWrite  *bool   `json:"read_write"`
		} `json:"webdav"`
		Timezone *string `json:"timezone"`
		Server   *struct {
			Listen *string `json:"listen"`
		} `json:"server"`
		Update *struct {
			// AutoApply toggles bare-metal auto-upgrade (#647/#648). nil = unchanged.
			AutoApply *bool `json:"auto_apply"`
		} `json:"update"`
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

	// Update auto-apply toggle (#648): takes effect on the next check cycle.
	if body.Update != nil && body.Update.AutoApply != nil {
		h.config.Update.AutoApply = body.Update.AutoApply
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
		if body.Cleanup.MotionAwareDiskCleanup != nil {
			h.config.Cleanup.MotionAwareDiskCleanup = body.Cleanup.MotionAwareDiskCleanup
		}
	}

	// Update storage root (#395) — next-start semantics, see body.Storage.
	storageChanged := false
	if body.Storage != nil && body.Storage.RootDir != nil {
		dir := strings.TrimSpace(*body.Storage.RootDir)
		if dir == "" || !strings.HasPrefix(dir, "/") {
			WriteError(w, http.StatusBadRequest, "storage.root_dir must be an absolute path")
			return
		}
		dir = strings.TrimRight(dir, "/")
		if dir == "" {
			dir = "/"
		}
		if dir != h.config.Storage.RootDir {
			// Preflight the target with a real SQLite round-trip (create dir,
			// create+drop a table under the same WAL/mmap pragmas the NVR DB
			// uses). MkdirAll alone accepted paths that later crash-looped the
			// app at db init — some platforms (fnOS external storage) take
			// plain file creation but reject the syscalls SQLite needs.
			if err := storage.ProbeRoot(dir); err != nil {
				WriteError(w, http.StatusBadRequest,
					fmt.Sprintf("storage root not usable: %v (the recording root must host the NVR database)", err))
				return
			}
			h.config.Storage.RootDir = dir
			// Hot switch: NEW segments immediately go to the new root (the DB
			// is decoupled and stays on the data volume; in-flight segments
			// finish where they started). No restart needed.
			if h.store != nil {
				if err := h.store.SetRootDir(dir); err != nil {
					WriteError(w, http.StatusBadRequest, fmt.Sprintf("cannot switch recording root: %v", err))
					return
				}
			}
			storageChanged = true
			logger.Info("storage root_dir switched (hot)", "new_root", dir)
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

	if storageChanged {
		writeJSON(w, http.StatusOK, map[string]any{"status": "updated", "restart_required": false})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

// handleStorageCandidates reports the recording-root choices available to the
// settings UI (#395): the current storage.root_dir plus any extra locations the
// host platform granted (fnOS user-authorized dirs, mounted under /media/* by
// the lifecycle script and passed via NVR_STORAGE_CANDIDATES).

// handleStorageCandidates reports the recording-root choices available to the
// settings UI (#395): the current storage.root_dir plus any extra locations the
// host platform granted (fnOS user-authorized dirs, mounted under /media/* by
// the lifecycle script and passed via NVR_STORAGE_CANDIDATES).
func (h *Handler) handleStorageCandidates(w http.ResponseWriter, r *http.Request) {
	if h.config == nil {
		WriteError(w, http.StatusInternalServerError, "config not available")
		return
	}
	type candidate struct {
		Path  string `json:"path"`
		Label string `json:"label"`
	}
	resp := struct {
		Current     string      `json:"current"`
		Candidates  []candidate `json:"candidates"`
		RestartHint string      `json:"restart_hint"`
		EnvManaged  bool        `json:"env_managed"`
	}{
		Current:     h.config.Storage.RootDir,
		Candidates:  []candidate{{Path: h.config.Storage.RootDir, Label: "current"}},
		RestartHint: "切换立即生效：新录像将写入所选位置（无需重启）",
		EnvManaged:  os.Getenv("NVR_STORAGE_CANDIDATES") != "",
	}
	seen := map[string]bool{h.config.Storage.RootDir: true}
	for _, p := range h.config.Storage.Candidates {
		if p == "" || seen[p] {
			continue
		}
		// Live existence filter: a mount revoked or unplugged since boot must
		// not keep offering a dead choice here (the startup-time prune in
		// config.ApplyDefaults only covers the persisted list).
		if info, err := os.Stat(p); err != nil || !info.IsDir() {
			continue
		}
		seen[p] = true
		label := strings.TrimPrefix(p, "/media/")
		if label == p {
			label = filepath.Base(p)
		}
		resp.Candidates = append(resp.Candidates, candidate{Path: p, Label: label})
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleGenerateAPIKey creates a new API key for MiBeeVision integration.
// POST /api/settings/api-keys  body: {"name": "mibeevision-prod"}
// Returns the full key ONCE (never exposed again).

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
