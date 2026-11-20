package systemapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
)

func (s *server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	if s.deps.Config == nil {
		WriteError(w, http.StatusInternalServerError, "config not available")
		return
	}

	// Compute timezone display string
	tzDisplay := s.deps.Config.Timezone
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
			"retention_days":         s.deps.Config.Cleanup.RetentionDays,
			"check_interval":         s.deps.Config.Cleanup.CheckInterval,
			"disk_threshold_percent": s.deps.Config.Cleanup.DiskThresholdPercent,
			// Motion-aware disk cleanup (#435): when the disk threshold is
			// hit, evict boring (low motion_score) segments first. nil = on.
			"motion_aware_disk_cleanup": s.deps.Config.Cleanup.MotionAwareDiskCleanup == nil || *s.deps.Config.Cleanup.MotionAwareDiskCleanup,
		},
		// Global recording gate default: cameras without an explicit
		// recording_enabled inherit this (new + auto-enrolled channels).
		"recording": map[string]any{
			"default_enabled": s.deps.Config.RecordingGate(nil),
		},
		"webdav": map[string]any{
			"enabled":     s.deps.Config.WebDAV.Enabled != nil && *s.deps.Config.WebDAV.Enabled,
			"path_prefix": s.deps.Config.WebDAV.PathPrefix,
			"read_write":  s.deps.Config.WebDAV.ReadWrite,
		},
		"storage": map[string]any{
			"root_dir": s.deps.Config.Storage.RootDir,
			// Remote object-storage offload (issue #874 batch 2). The secret
			// is never returned — only whether one is configured (mirrors
			// gb28181.password_configured). access_key_id round-trips because
			// it may carry a ${VAR} reference the UI must not destroy.
			"remote": map[string]any{
				"enabled":           s.deps.Config.Storage.Remote.Enabled,
				"endpoint_url":      s.deps.Config.Storage.Remote.EndpointURL,
				"region":            s.deps.Config.Storage.Remote.Region,
				"bucket":            s.deps.Config.Storage.Remote.Bucket,
				"prefix":            s.deps.Config.Storage.Remote.Prefix,
				"path_style":        s.deps.Config.Storage.Remote.PathStyle == nil || *s.deps.Config.Storage.Remote.PathStyle,
				"access_key_id":     s.deps.Config.Storage.Remote.AccessKeyID,
				"secret_configured": s.deps.Config.Storage.Remote.SecretAccessKey != "",
				"upload": map[string]any{
					"max_concurrency": s.deps.Config.Storage.Remote.Upload.MaxConcurrency,
					"scan_interval_s": s.deps.Config.Storage.Remote.Upload.ScanIntervalS,
					"min_age_s":       s.deps.Config.Storage.Remote.Upload.MinAgeS,
					"backlog_limit":   s.deps.Config.Storage.Remote.Upload.BacklogLimit,
				},
				"evict": map[string]any{
					"after_days": s.deps.Config.Storage.Remote.Evict.AfterDays,
				},
				"playback": map[string]any{
					"presigned":    s.deps.Config.Storage.Remote.Playback.Presigned,
					"endpoint_url": s.deps.Config.Storage.Remote.Playback.EndpointURL,
					"ttl_s":        s.deps.Config.Storage.Remote.Playback.TTLS,
				},
				"camera_overrides": s.deps.Config.Storage.Remote.CameraOverrides,
			},
		},
		"auth": map[string]any{
			"username":        s.deps.Config.Auth.Username,
			"auth_configured": s.deps.Config.Auth.PasswordHash != "" || s.deps.Config.Auth.Password != "",
		},
		"mibeevision": map[string]any{
			"api_keys": buildAPIKeyInfo(s.deps.Config.APIKeys, s.apiKeyLastUsed()),
		},
		"vision": map[string]any{
			"enabled":                s.deps.Config.Vision.Enabled,
			"url":                    s.deps.Config.Vision.URL,
			"push_mode":              s.deps.Config.Vision.PushMode,
			"heartbeat_timeout_secs": s.deps.Config.Vision.HeartbeatTimeoutSecs,
			"instances":              s.deps.Config.Vision.Instances,
		},
		"update": map[string]any{
			// Bare-metal auto-apply toggle (#648 UI switch, #647 execution).
			"auto_apply": s.deps.Config.Update.IsAutoApply(),
		},
		"timezone":         s.deps.Config.Timezone,
		"timezone_display": tzDisplay,
		"server": map[string]any{
			"listen": s.deps.Config.Server.Listen,
		},
		"gb28181": map[string]any{
			"enabled":    s.deps.Config.GB28181.Enabled,
			"sip_listen": s.deps.Config.GB28181.SIPListen,
			"server_id":  s.deps.Config.GB28181.ServerID,
			"realm":      s.deps.Config.GB28181.Realm,
			// Never return the SIP password (mirrors the masked auth/API-key
			// fields above); the frontend only needs whether one is set.
			"password_configured": s.deps.Config.GB28181.Password != "",
			"port_range":          s.deps.Config.GB28181.PortRange,
			"allowed_device_ids":  s.deps.Config.GB28181.AllowedDeviceIDs,
			"heartbeat_interval":  s.deps.Config.GB28181.HeartbeatInterval,
			"catalog_interval":    s.deps.Config.GB28181.CatalogInterval,
			"tcp_mode":            s.deps.Config.GB28181.TCPMode,
			"tcp_framing":         s.deps.Config.GB28181.TCPFraming,
			"media_transport":     s.deps.Config.GB28181.MediaTransport,
			"sip_transport":       s.deps.Config.GB28181.SIPTransport,
			// Subscription toggles (#341): resolve the *bool defaults so the
			// UI sees the effective values.
			"subscribe_catalog":         s.deps.Config.GB28181.CatalogSubscriptionOn(),
			"subscribe_alarm":           s.deps.Config.GB28181.AlarmSubscriptionOn(),
			"subscribe_mobile_position": s.deps.Config.GB28181.SubscribeMobilePosition,
			"subscribe_expires":         s.deps.Config.GB28181.SubscribeExpires,
		},
		"doorbell": s.doorbellSettingsView(),
		"dlna": map[string]any{
			"enabled":       s.deps.Config.DLNA.Enabled,
			"port":          s.deps.Config.DLNA.Port,
			"interfaces":    s.deps.Config.DLNA.Interfaces,
			"friendly_name": s.deps.Config.DLNA.FriendlyName,
		},
		"notifications": s.notificationsSettingsView(),
	})
}

// notificationsSettingsView renders the notifications section for GET
// /api/settings. Tokens and the snapshot API key are never returned — only
// whether they are configured (gb28181.password_configured convention); the
// UI round-trips blank secrets to keep existing values on save.
func (s *server) notificationsSettingsView() map[string]any {
	sinks := make([]map[string]any, 0, len(s.deps.Config.Notifications.Sinks))
	for _, s := range s.deps.Config.Notifications.Sinks {
		sinks = append(sinks, map[string]any{
			"name":             s.Name,
			"url":              s.URL,
			"token_configured": s.Token != "",
			"events":           s.Events,
			"severity_min":     s.SeverityMin,
			"timeout_s":        s.TimeoutS,
			"burst_per_min":    s.BurstPerMin,
		})
	}
	return map[string]any{
		"enabled":            s.deps.Config.Notifications.Enabled,
		"base_url":           s.deps.Config.Notifications.BaseURL,
		"api_key_configured": s.deps.Config.Notifications.APIKey != "",
		"sinks":              sinks,
	}
}

// doorbellSettingsView renders the doorbell section for GET /api/settings
// (#971). Secrets are never returned — per-user and outbound secrets
// surface as secret_configured booleans (gb28181.password_configured
// convention); outbound is null when mode B is off.
func (s *server) doorbellSettingsView() map[string]any {
	users := make([]map[string]any, 0, len(s.deps.Config.Doorbell.Users))
	for _, u := range s.deps.Config.Doorbell.Users {
		users = append(users, map[string]any{
			"username":          u.Username,
			"name":              u.Name,
			"camera_id":         u.CameraID,
			"secret_configured": u.Secret != "",
		})
	}
	var outbound any
	if ob := s.deps.Config.Doorbell.Outbound; ob != nil {
		outbound = map[string]any{
			"server":            ob.Server,
			"username":          ob.Username,
			"expires":           ob.Expires,
			"secret_configured": ob.Secret != "",
		}
	}
	return map[string]any{
		"enabled":      s.deps.Config.Doorbell.Enabled,
		"listen":       s.deps.Config.Doorbell.Listen,
		"realm":        s.deps.Config.Doorbell.Realm,
		"transport":    s.deps.Config.Doorbell.Transport,
		"tls_cert":     s.deps.Config.Doorbell.TLSCert,
		"tls_key":      s.deps.Config.Doorbell.TLSKey,
		"lan_only":     s.deps.Config.Doorbell.LANOnlyEnabled(),
		"ring_timeout": s.deps.Config.Doorbell.RingTimeout,
		"users":        users,
		"outbound":     outbound,
	}
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

func (s *server) handleUpdateSettings(w http.ResponseWriter, r *http.Request) {
	if s.deps.Config == nil {
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
		Recording *struct {
			// Global recording gate default for cameras without an explicit
			// recording_enabled. nil = unchanged. Applied hot: running cameras
			// that inherit the default are recycled so the rebuilt recorder
			// reads the new gate (same semantics as the per-camera toggle).
			DefaultEnabled *bool `json:"default_enabled"`
		} `json:"recording"`
		Storage *struct {
			// Recording root directory (#395). Takes effect on the NEXT start —
			// the DB and all subsystems open paths under the old root at boot,
			// so a live switch would corrupt state. The response carries
			// restart_required=true when this changes.
			RootDir *string `json:"root_dir"`
			// Remote object-storage offload (issue #874). nil = unchanged.
			// Blank credential strings keep the current values (the GET never
			// returns the secret, so the UI round-trips blanks). Takes effect
			// on the next start — the uploader + proxy are built at boot.
			Remote *remoteSettingsUpdate `json:"remote"`
		} `json:"storage"`
		WebDAV *struct {
			Enabled    *bool   `json:"enabled"`
			PathPrefix *string `json:"path_prefix"`
			ReadWrite  *bool   `json:"read_write"`
		} `json:"webdav"`
		Vision *struct {
			// 多实例消费端配置(整表替换)。实例名唯一/URL http(s) 在此校验;
			// 删除仍被相机路由引用的实例会被 400 拒绝(先解路由再删)。
			Enabled   *bool                    `json:"enabled"`
			URL       *string                  `json:"url"`
			Instances *[]config.VisionInstance `json:"instances"`
		} `json:"vision"`
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
		Doorbell *struct {
			Enabled     *bool                   `json:"enabled"`
			Listen      *string                 `json:"listen"`
			Realm       *string                 `json:"realm"`
			Transport   *string                 `json:"transport"`
			TLSCert     *string                 `json:"tls_cert"`
			TLSKey      *string                 `json:"tls_key"`
			LANOnly     *bool                   `json:"lan_only"`
			RingTimeout *string                 `json:"ring_timeout"`
			Users       *[]doorbellUserUpdate   `json:"users"`
			Outbound    *doorbellOutboundUpdate `json:"outbound"`
		} `json:"doorbell"`
		DLNA *struct {
			Enabled      *bool     `json:"enabled"`
			Port         *int      `json:"port"`
			Interfaces   *[]string `json:"interfaces"`
			FriendlyName *string   `json:"friendly_name"`
		} `json:"dlna"`
		// Outbound alert forwarding — whole-table sink replacement. A blank
		// token (or blank api_key) keeps the existing secret for the sink
		// with the same name (the GET never returns secrets, so the UI
		// round-trips blanks). Applies on the next start.
		Notifications *struct {
			Enabled *bool   `json:"enabled"`
			BaseURL *string `json:"base_url"`
			APIKey  *string `json:"api_key"`
			Sinks   *[]struct {
				Name        string   `json:"name"`
				URL         string   `json:"url"`
				Token       string   `json:"token"`
				Events      []string `json:"events"`
				SeverityMin string   `json:"severity_min"`
				TimeoutS    int      `json:"timeout_s"`
				BurstPerMin int      `json:"burst_per_min"`
			} `json:"sinks"`
		} `json:"notifications"`
	}

	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// Update auto-apply toggle (#648): takes effect on the next check cycle.
	if body.Update != nil && body.Update.AutoApply != nil {
		s.deps.Config.Update.AutoApply = body.Update.AutoApply
	}

	// Global recording gate default: hot-apply by recycling running cameras
	// that inherit it. Run detached — a fleet-wide recycle takes a while and
	// the settings save must not block on it (config is already persisted by
	// the time the goroutine reads it).
	if body.Recording != nil && body.Recording.DefaultEnabled != nil {
		oldDefault := s.deps.Config.RecordingGate(nil)
		s.deps.Config.Recording.DefaultEnabled = body.Recording.DefaultEnabled
		if oldDefault != *body.Recording.DefaultEnabled && s.deps.Cam != nil {
			enabled := *body.Recording.DefaultEnabled
			go s.deps.Cam.ApplyRecordingDefault(enabled)
		}
	}

	// Update cleanup settings. Merge into a copy and validate with the SAME
	// rules the startup load enforces (CleanupConfig.Validate — single source
	// of truth), committing only when the whole section passes. #867: the old
	// per-field ad-hoc windows (disk_threshold_percent 1–100, retention_days
	// unbounded) accepted values the load then rejected at next start →
	// crash loop; field-by-field early returns also half-committed a save
	// when a later field failed.
	if body.Cleanup != nil {
		merged := s.deps.Config.Cleanup
		if body.Cleanup.RetentionDays != nil {
			merged.RetentionDays = *body.Cleanup.RetentionDays
		}
		if body.Cleanup.DiskThresholdPercent != nil {
			merged.DiskThresholdPercent = *body.Cleanup.DiskThresholdPercent
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
				merged.CheckInterval = trimmed
			}
		}
		if body.Cleanup.MotionAwareDiskCleanup != nil {
			merged.MotionAwareDiskCleanup = body.Cleanup.MotionAwareDiskCleanup
		}
		if err := merged.Validate(); err != nil {
			WriteError(w, http.StatusBadRequest, err.Error())
			return
		}
		s.deps.Config.Cleanup = merged
	}

	// Update storage root (#395) — next-start semantics, see body.Storage.
	storageChanged := false
	remoteOffloadChanged := false
	if body.Storage != nil && body.Storage.RootDir != nil {
		dir := strings.TrimSpace(*body.Storage.RootDir)
		if dir == "" || !filepath.IsAbs(dir) {
			WriteError(w, http.StatusBadRequest, "storage.root_dir must be an absolute path")
			return
		}
		dir = strings.TrimRight(dir, "/")
		if dir == "" {
			dir = "/"
		}
		if dir != s.deps.Config.Storage.RootDir {
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
			s.deps.Config.Storage.RootDir = dir
			// Hot switch: NEW segments immediately go to the new root (the DB
			// is decoupled and stays on the data volume; in-flight segments
			// finish where they started). No restart needed.
			if s.deps.Store != nil {
				if err := s.deps.Store.SetRootDir(dir); err != nil {
					WriteError(w, http.StatusBadRequest, fmt.Sprintf("cannot switch recording root: %v", err))
					return
				}
			}
			storageChanged = true
			logger.Info("storage root_dir switched (hot)", "new_root", dir)
		}
	}

	// Update remote object-storage offload settings (issue #874). Merge into
	// a copy and validate with the SAME rules a startup load enforces
	// (config.ValidateRemoteStorage), committing only when the whole section
	// passes (#867 discipline). Next-start semantics: the uploader service
	// and playback proxy are constructed at boot.
	if body.Storage != nil && body.Storage.Remote != nil {
		merged := s.deps.Config.Storage.Remote
		ru := body.Storage.Remote
		if ru.Enabled != nil {
			merged.Enabled = *ru.Enabled
		}
		if ru.EndpointURL != nil {
			merged.EndpointURL = strings.TrimSpace(*ru.EndpointURL)
		}
		if ru.Region != nil {
			merged.Region = strings.TrimSpace(*ru.Region)
		}
		if ru.Bucket != nil {
			merged.Bucket = strings.TrimSpace(*ru.Bucket)
		}
		if ru.Prefix != nil {
			merged.Prefix = strings.TrimSpace(*ru.Prefix)
		}
		if ru.PathStyle != nil {
			merged.PathStyle = ru.PathStyle
		}
		if ru.AccessKeyID != nil && strings.TrimSpace(*ru.AccessKeyID) != "" {
			merged.AccessKeyID = strings.TrimSpace(*ru.AccessKeyID)
		}
		if ru.SecretAccessKey != nil && strings.TrimSpace(*ru.SecretAccessKey) != "" {
			merged.SecretAccessKey = strings.TrimSpace(*ru.SecretAccessKey)
		}
		if ru.Upload != nil {
			if ru.Upload.MaxConcurrency != nil {
				merged.Upload.MaxConcurrency = *ru.Upload.MaxConcurrency
			}
			if ru.Upload.ScanIntervalS != nil {
				merged.Upload.ScanIntervalS = *ru.Upload.ScanIntervalS
			}
			if ru.Upload.MinAgeS != nil {
				merged.Upload.MinAgeS = *ru.Upload.MinAgeS
			}
			if ru.Upload.BacklogLimit != nil {
				merged.Upload.BacklogLimit = *ru.Upload.BacklogLimit
			}
		}
		if ru.Evict != nil && ru.Evict.AfterDays != nil {
			merged.Evict.AfterDays = *ru.Evict.AfterDays
		}
		if ru.Playback != nil {
			if ru.Playback.Presigned != nil {
				merged.Playback.Presigned = *ru.Playback.Presigned
			}
			if ru.Playback.EndpointURL != nil {
				merged.Playback.EndpointURL = strings.TrimSpace(*ru.Playback.EndpointURL)
			}
			if ru.Playback.TTLS != nil {
				merged.Playback.TTLS = *ru.Playback.TTLS
			}
		}
		if ru.CameraOverrides != nil {
			merged.CameraOverrides = *ru.CameraOverrides
		}
		if err := config.ValidateRemoteStorage(merged); err != nil {
			WriteError(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := config.ValidateRemoteCameraOverrides(s.deps.Config.Cameras, merged.CameraOverrides); err != nil {
			WriteError(w, http.StatusBadRequest, err.Error())
			return
		}
		s.deps.Config.Storage.Remote = merged
		remoteOffloadChanged = true
	}

	if body.WebDAV != nil {
		if body.WebDAV.Enabled != nil {
			if s.deps.Config.WebDAV.Enabled == nil {
				s.deps.Config.WebDAV.Enabled = new(bool)
			}
			*s.deps.Config.WebDAV.Enabled = *body.WebDAV.Enabled
		}
		if body.WebDAV.PathPrefix != nil {
			s.deps.Config.WebDAV.PathPrefix = *body.WebDAV.PathPrefix
		}
		if body.WebDAV.ReadWrite != nil {
			s.deps.Config.WebDAV.ReadWrite = *body.WebDAV.ReadWrite
		}
	}

	// Update vision consumer instances (multi-instance routing). The
	// coordinator reads cfg live (same pointer), so changes apply without a
	// restart; instance health trackers survive edits (reconciled by name).
	if body.Vision != nil {
		if body.Vision.Instances != nil {
			seen := make(map[string]bool, len(*body.Vision.Instances))
			for i, ins := range *body.Vision.Instances {
				if strings.TrimSpace(ins.Name) == "" {
					WriteError(w, http.StatusBadRequest, fmt.Sprintf("vision.instances[%d].name is required", i))
					return
				}
				if seen[ins.Name] {
					WriteError(w, http.StatusBadRequest, fmt.Sprintf("vision.instances[%d] duplicate name %q", i, ins.Name))
					return
				}
				seen[ins.Name] = true
				u, perr := url.Parse(ins.URL)
				if perr != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
					WriteError(w, http.StatusBadRequest, fmt.Sprintf("vision.instances[%d].url must be an http(s) URL, got %q", i, ins.URL))
					return
				}
			}
			// Removing an instance still referenced by a camera's routing
			// would silently strand that camera — reject with the offenders.
			known := seen
			known["default"] = true
			var stranded []string
			for _, cam := range s.deps.Config.Cameras {
				for _, name := range cam.VisionTargets {
					if !known[name] {
						stranded = append(stranded, cam.ID+"→"+name)
						break
					}
				}
			}
			if len(stranded) > 0 {
				WriteError(w, http.StatusBadRequest,
					"cameras still route to removed vision instances: "+strings.Join(stranded, ", "))
				return
			}
			s.deps.Config.Vision.Instances = *body.Vision.Instances
		}
		if body.Vision.URL != nil {
			s.deps.Config.Vision.URL = strings.TrimSpace(*body.Vision.URL)
		}
		if body.Vision.Enabled != nil {
			s.deps.Config.Vision.Enabled = *body.Vision.Enabled
		}
		logger.Info("vision settings updated",
			"instances", len(s.deps.Config.Vision.EffectiveInstances()),
			"enabled", s.deps.Config.Vision.Enabled)
	}

	if body.Timezone != nil {
		tz := strings.TrimSpace(*body.Timezone)
		if tz != "" && tz != "UTC" && tz != "Local" {
			if _, err := time.LoadLocation(tz); err != nil {
				WriteError(w, http.StatusBadRequest, fmt.Sprintf("invalid timezone: %q", tz))
				return
			}
		}
		s.deps.Config.Timezone = tz
	}

	if body.Server != nil && body.Server.Listen != nil {
		raw := strings.TrimSpace(*body.Server.Listen)
		raw = strings.TrimPrefix(raw, ":")
		port, err := strconv.Atoi(raw)
		if err != nil || port < 1 || port > 65535 {
			WriteError(w, http.StatusBadRequest, "listen must be a valid port (1-65535)")
			return
		}
		s.deps.Config.Server.Listen = fmt.Sprintf(":%d", port)
	}

	if body.GB28181 != nil {
		if body.GB28181.Enabled != nil {
			s.deps.Config.GB28181.Enabled = *body.GB28181.Enabled
		}
		if body.GB28181.SIPListen != nil {
			s.deps.Config.GB28181.SIPListen = *body.GB28181.SIPListen
		}
		if body.GB28181.ServerID != nil {
			s.deps.Config.GB28181.ServerID = *body.GB28181.ServerID
		}
		if body.GB28181.Realm != nil {
			s.deps.Config.GB28181.Realm = *body.GB28181.Realm
		}
		if body.GB28181.Password != nil {
			// Blank password = keep current (the GET no longer returns the
			// password, so the UI round-trips an empty field when unchanged).
			if trimmed := strings.TrimSpace(*body.GB28181.Password); trimmed != "" {
				s.deps.Config.GB28181.Password = trimmed
			}
		}
		if body.GB28181.PortRange != nil {
			s.deps.Config.GB28181.PortRange = *body.GB28181.PortRange
		}
		if body.GB28181.AllowedDeviceIDs != nil {
			s.deps.Config.GB28181.AllowedDeviceIDs = *body.GB28181.AllowedDeviceIDs
		}
		if body.GB28181.HeartbeatInterval != nil {
			s.deps.Config.GB28181.HeartbeatInterval = *body.GB28181.HeartbeatInterval
		}
		if body.GB28181.CatalogInterval != nil {
			s.deps.Config.GB28181.CatalogInterval = *body.GB28181.CatalogInterval
		}
		if body.GB28181.TCPMode != nil {
			s.deps.Config.GB28181.TCPMode = *body.GB28181.TCPMode
		}
		if body.GB28181.TCPFraming != nil {
			s.deps.Config.GB28181.TCPFraming = *body.GB28181.TCPFraming
		}
		if body.GB28181.MediaTransport != nil {
			s.deps.Config.GB28181.MediaTransport = *body.GB28181.MediaTransport
			// Keep the legacy alias coherent for older config readers.
			s.deps.Config.GB28181.TCPMode = *body.GB28181.MediaTransport != "udp"
		}
		if body.GB28181.SIPTransport != nil {
			s.deps.Config.GB28181.SIPTransport = *body.GB28181.SIPTransport
		}
		if body.GB28181.SubscribeCatalog != nil {
			s.deps.Config.GB28181.SubscribeCatalog = body.GB28181.SubscribeCatalog
		}
		if body.GB28181.SubscribeAlarm != nil {
			s.deps.Config.GB28181.SubscribeAlarm = body.GB28181.SubscribeAlarm
		}
		if body.GB28181.SubscribeMobilePosition != nil {
			s.deps.Config.GB28181.SubscribeMobilePosition = *body.GB28181.SubscribeMobilePosition
		}
		if body.GB28181.SubscribeExpires != nil {
			if trimmed := strings.TrimSpace(*body.GB28181.SubscribeExpires); trimmed != "" {
				if _, err := time.ParseDuration(trimmed); err != nil {
					WriteError(w, http.StatusBadRequest, "subscribe_expires must be a valid duration (e.g., \"3600s\", \"2h\")")
					return
				}
				s.deps.Config.GB28181.SubscribeExpires = trimmed
			}
		}
		// Validate the updated config (catches invalid server_id, sip_listen, etc.)
		if err := config.Validate(s.deps.Config); err != nil {
			WriteError(w, http.StatusBadRequest, err.Error())
			return
		}
	}

	// Doorbell ingress (#971). The SIP listener and the popup talk bridge
	// snapshot their config at boot — section changes apply on the next
	// start, so the response carries restart_required. Merge into a copy and
	// validate with the SAME rules the startup load enforces
	// (config.Validate), rolling the section back on failure (#867
	// discipline).
	doorbellChanged := false
	if body.Doorbell != nil {
		merged := s.deps.Config.Doorbell
		db := body.Doorbell
		if db.Enabled != nil {
			merged.Enabled = *db.Enabled
		}
		if db.Listen != nil {
			merged.Listen = strings.TrimSpace(*db.Listen)
		}
		if db.Realm != nil {
			merged.Realm = strings.TrimSpace(*db.Realm)
		}
		if db.Transport != nil {
			merged.Transport = strings.TrimSpace(*db.Transport)
		}
		if db.TLSCert != nil {
			merged.TLSCert = strings.TrimSpace(*db.TLSCert)
		}
		if db.TLSKey != nil {
			merged.TLSKey = strings.TrimSpace(*db.TLSKey)
		}
		if db.LANOnly != nil {
			merged.LANOnly = db.LANOnly
		}
		if db.RingTimeout != nil {
			merged.RingTimeout = strings.TrimSpace(*db.RingTimeout)
		}
		if db.Users != nil {
			// Whole-table replace. A blank secret keeps the existing secret
			// for the same username (the GET never returns secrets, so the
			// UI round-trips blanks — same semantics as the gb28181 password).
			oldSecrets := make(map[string]string, len(s.deps.Config.Doorbell.Users))
			for _, u := range s.deps.Config.Doorbell.Users {
				oldSecrets[u.Username] = u.Secret
			}
			users := make([]config.DoorbellUserConfig, 0, len(*db.Users))
			for _, u := range *db.Users {
				secret := u.Secret
				if strings.TrimSpace(secret) == "" {
					secret = oldSecrets[u.Username]
				}
				users = append(users, config.DoorbellUserConfig{
					Username: u.Username,
					Name:     u.Name,
					CameraID: u.CameraID,
					Secret:   secret,
				})
			}
			merged.Users = users
		}
		if db.Outbound != nil {
			if db.Outbound.Enabled != nil && !*db.Outbound.Enabled {
				merged.Outbound = nil
			} else {
				target := config.DoorbellOutboundConfig{}
				if merged.Outbound != nil {
					target = *merged.Outbound
				}
				if db.Outbound.Server != nil {
					target.Server = strings.TrimSpace(*db.Outbound.Server)
				}
				if db.Outbound.Username != nil {
					target.Username = strings.TrimSpace(*db.Outbound.Username)
				}
				if db.Outbound.Secret != nil {
					if s := strings.TrimSpace(*db.Outbound.Secret); s != "" {
						target.Secret = s
					}
				}
				if db.Outbound.Expires != nil {
					target.Expires = strings.TrimSpace(*db.Outbound.Expires)
				}
				merged.Outbound = &target
			}
		}
		prev := s.deps.Config.Doorbell
		s.deps.Config.Doorbell = merged
		if err := config.Validate(s.deps.Config); err != nil {
			s.deps.Config.Doorbell = prev
			WriteError(w, http.StatusBadRequest, err.Error())
			return
		}
		doorbellChanged = true
	}

	// DLNA media server (#970) — same next-start semantics as doorbell.
	dlnaChanged := false
	if body.DLNA != nil {
		merged := s.deps.Config.DLNA
		if body.DLNA.Enabled != nil {
			merged.Enabled = *body.DLNA.Enabled
		}
		if body.DLNA.Port != nil {
			merged.Port = *body.DLNA.Port
		}
		if body.DLNA.Interfaces != nil {
			merged.Interfaces = *body.DLNA.Interfaces
		}
		if body.DLNA.FriendlyName != nil {
			merged.FriendlyName = strings.TrimSpace(*body.DLNA.FriendlyName)
		}
		prev := s.deps.Config.DLNA
		s.deps.Config.DLNA = merged
		if err := config.Validate(s.deps.Config); err != nil {
			s.deps.Config.DLNA = prev
			WriteError(w, http.StatusBadRequest, err.Error())
			return
		}
		dlnaChanged = true
	}

	// Outbound alert forwarding (notifications) — same next-start semantics
	// as doorbell/DLNA: the dispatcher and its sinks are constructed at boot.
	notificationsChanged := false
	if body.Notifications != nil {
		merged := s.deps.Config.Notifications
		if body.Notifications.Enabled != nil {
			merged.Enabled = *body.Notifications.Enabled
		}
		if body.Notifications.BaseURL != nil {
			merged.BaseURL = strings.TrimSpace(*body.Notifications.BaseURL)
		}
		if body.Notifications.APIKey != nil {
			if key := strings.TrimSpace(*body.Notifications.APIKey); key != "" {
				merged.APIKey = key
			}
		}
		if body.Notifications.Sinks != nil {
			// Keep existing secrets for same-named sinks; blank token on a
			// NEW sink is a config error caught by Validate below.
			prevTokens := make(map[string]string, len(s.deps.Config.Notifications.Sinks))
			for _, s := range s.deps.Config.Notifications.Sinks {
				prevTokens[s.Name] = s.Token
			}
			sinks := make([]config.NotificationSinkConfig, 0, len(*body.Notifications.Sinks))
			for _, s := range *body.Notifications.Sinks {
				token := strings.TrimSpace(s.Token)
				if token == "" {
					token = prevTokens[s.Name]
				}
				sinks = append(sinks, config.NotificationSinkConfig{
					Name:        strings.TrimSpace(s.Name),
					URL:         strings.TrimSpace(s.URL),
					Token:       token,
					Events:      s.Events,
					SeverityMin: s.SeverityMin,
					TimeoutS:    s.TimeoutS,
					BurstPerMin: s.BurstPerMin,
				})
			}
			merged.Sinks = sinks
		}
		prev := s.deps.Config.Notifications
		s.deps.Config.Notifications = merged
		if err := config.Validate(s.deps.Config); err != nil {
			s.deps.Config.Notifications = prev
			WriteError(w, http.StatusBadRequest, err.Error())
			return
		}
		notificationsChanged = true
	}

	// Persist config to disk
	if err := config.Save(s.deps.ConfigPath, s.deps.Config); err != nil {
		logger.Warn("failed to save config", "error", err)
	}

	if remoteOffloadChanged || doorbellChanged || dlnaChanged || notificationsChanged {
		// Remote offload, doorbell and DLNA settings apply on the next start
		// (their services are constructed at boot).
		writeJSON(w, http.StatusOK, map[string]any{"status": "updated", "restart_required": true})
		return
	}
	if storageChanged {
		writeJSON(w, http.StatusOK, map[string]any{"status": "updated", "restart_required": false})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

// doorbellUserUpdate is one row of the doorbell.users PUT body. A blank
// secret keeps the existing secret for the same username (the GET never
// returns secrets, so the UI round-trips blanks).
type doorbellUserUpdate struct {
	Username string `json:"username"`
	Name     string `json:"name"`
	CameraID string `json:"camera_id"`
	Secret   string `json:"secret"`
}

// doorbellOutboundUpdate is the doorbell.outbound PUT body. enabled=false
// removes the outbound registration (mode A only); any other field present
// materializes/merges the struct. A blank secret keeps the current one.
type doorbellOutboundUpdate struct {
	Enabled  *bool   `json:"enabled"`
	Server   *string `json:"server"`
	Username *string `json:"username"`
	Secret   *string `json:"secret"`
	Expires  *string `json:"expires"`
}

// remoteSettingsUpdate is the PUT /api/settings storage.remote body. Pointer
// fields = "change me"; blank credential strings keep current values.
type remoteSettingsUpdate struct {
	Enabled         *bool   `json:"enabled"`
	EndpointURL     *string `json:"endpoint_url"`
	Region          *string `json:"region"`
	Bucket          *string `json:"bucket"`
	Prefix          *string `json:"prefix"`
	PathStyle       *bool   `json:"path_style"`
	AccessKeyID     *string `json:"access_key_id"`
	SecretAccessKey *string `json:"secret_access_key"`
	Upload          *struct {
		MaxConcurrency *int `json:"max_concurrency"`
		ScanIntervalS  *int `json:"scan_interval_s"`
		MinAgeS        *int `json:"min_age_s"`
		BacklogLimit   *int `json:"backlog_limit"`
	} `json:"upload"`
	Evict *struct {
		AfterDays *int `json:"after_days"`
	} `json:"evict"`
	Playback *struct {
		Presigned   *bool   `json:"presigned"`
		EndpointURL *string `json:"endpoint_url"`
		TTLS        *int    `json:"ttl_s"`
	} `json:"playback"`
	CameraOverrides *map[string]config.RemoteCameraOverride `json:"camera_overrides"`
}
