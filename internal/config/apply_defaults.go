package config

import (
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/ai"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
)

// applyConfigDefaults contains the full per-subsystem default-setting logic,
// extracted from ApplyDefaults() for readability. Each section applies zero-value
// checks in declaration order. All mutations go through the *Config pointer.
func applyConfigDefaults(cfg *Config) {
	// Timezone
	if cfg.Timezone == "" {
		cfg.Timezone = "Local"
	}
	// Server
	if strings.TrimSpace(cfg.Server.Listen) == "" {
		cfg.Server.Listen = ":9090"
	}

	// NVR_LISTEN_PORT overrides server.listen (env wins over the config file —
	// 12-factor). Docker host-network deployments need this because ONVIF WS-Discovery
	// multicast forces network_mode: host, which makes Docker port mapping impossible;
	// users change the port via a single compose env var instead of editing the YAML.
	// Accepts both "9091" and ":9091". See issue #269, docs/zh/deployment-faq.md Q2.
	if env := strings.TrimSpace(os.Getenv("NVR_LISTEN_PORT")); env != "" {
		if !strings.HasPrefix(env, ":") {
			env = ":" + env
		}
		cfg.Server.Listen = env
	}
	// NVR_FRAME_ANCESTORS overrides security.frame_ancestors (env wins over the
	// config file). Lets NAS platforms that embed the UI in a cross-origin iframe
	// (fnOS desktop: the desktop page is served from a different origin than the
	// NVR's :9090) whitelist the embedder without editing the YAML, e.g.
	//   NVR_FRAME_ANCESTORS="http://192.168.1.10 http://192.168.1.11"
	// Empty/unset keeps the default 'self' (no cross-origin framing).
	if env := strings.TrimSpace(os.Getenv("NVR_FRAME_ANCESTORS")); env != "" {
		cfg.Security.FrameAncestors = env
	}
	// NVR_UNIX_SOCKET overrides server.unix_socket (env wins over the config
	// file). Set by the fnOS app lifecycle script: the fnOS unified gateway
	// forwards authenticated requests to a Unix socket inside the app's target
	// directory (see docs/core-concepts gateway-registration).
	if env := strings.TrimSpace(os.Getenv("NVR_UNIX_SOCKET")); env != "" {
		cfg.Server.UnixSocket = env
	}
	// NVR_BASE_PATH overrides server.base_path (env wins over the config
	// file). "/app/mibee-nvr" on fnOS — the unified-gateway prefix the SPA is
	// served under. Normalized to a leading slash and no trailing slash.
	if env := strings.TrimSpace(os.Getenv("NVR_BASE_PATH")); env != "" {
		cfg.Server.BasePath = NormalizeBasePath(env)
	}
	if cfg.Server.BasePath != "" {
		cfg.Server.BasePath = NormalizeBasePath(cfg.Server.BasePath)
	}
	// NVR_STORAGE_CANDIDATES (colon-separated container paths, #395): extra
	// storage locations the host platform granted the app (fnOS user-authorized
	// directories, mounted by the lifecycle script under /media/*). Informational
	// only — surfaced via /api/storage/candidates for the settings UI.
	if env := strings.TrimSpace(os.Getenv("NVR_STORAGE_CANDIDATES")); env != "" {
		var candidates []string
		for _, p := range strings.Split(env, ":") {
			if p = strings.TrimSpace(p); p != "" {
				candidates = append(candidates, p)
			}
		}
		if len(candidates) > 0 {
			cfg.Storage.Candidates = candidates
		}
	}
	// Device identity (#330): the name defaults to the system hostname and is
	// intentionally NOT persisted (an explicit server.device_name overrides it;
	// a hostname change is reflected on the next restart). The ID itself is
	// generated and persisted by Load via ensureDeviceIdentity because it must
	// stay stable across restarts.
	if cfg.Server.DeviceName == "" {
		if hn, err := os.Hostname(); err == nil && hn != "" {
			cfg.Server.DeviceName = hn
		}
	}
	// LAN discovery (#333/#334): mDNS registration + UDP broadcast responder
	// on by default — they carry no more information than the already-public
	// GET /api/health.
	if cfg.Server.Discovery.UDP.Enabled == nil {
		enabled := true
		cfg.Server.Discovery.UDP.Enabled = &enabled
	}
	if cfg.Server.Discovery.UDP.Port == 0 {
		cfg.Server.Discovery.UDP.Port = DefaultUDPPort
	}
	if cfg.Server.Discovery.MDNS.Enabled == nil {
		enabled := true
		cfg.Server.Discovery.MDNS.Enabled = &enabled
	}
	// Storage
	if strings.TrimSpace(cfg.Storage.RootDir) == "" {
		cfg.Storage.RootDir = "/var/lib/mibee-nvr"
	}
	if strings.TrimSpace(cfg.Storage.SegmentDuration) == "" {
		cfg.Storage.SegmentDuration = "30s"
	}
	// Cleanup
	if cfg.Cleanup.RetentionDays == 0 {
		cfg.Cleanup.RetentionDays = 30
	}
	if strings.TrimSpace(cfg.Cleanup.CheckInterval) == "" {
		cfg.Cleanup.CheckInterval = "1h"
	}
	if cfg.Cleanup.DiskThresholdPercent == 0 {
		// 85% avoids the HDD performance cliff that starts around 90%+ full,
		// where random writes during SQLite checkpoints + segment merges start
		// contending with sequential recording writes for head seeks.
		cfg.Cleanup.DiskThresholdPercent = 85
	}
	// Auth - rate limit defaults
	if cfg.Auth.RateLimit.MaxFailures == 0 {
		cfg.Auth.RateLimit.MaxFailures = 20
	}
	if cfg.Auth.RateLimit.WindowMinutes == 0 {
		cfg.Auth.RateLimit.WindowMinutes = 1
	}
	// Auth - local bypass defaults to OFF. Enabling it lets browsers on the NVR
	// host machine (loopback, no proxy headers) skip the login page, but it MUST
	// stay off behind a reverse proxy or Docker published port — there every
	// proxied request arrives from 127.0.0.1 and would bypass auth entirely.
	if cfg.Auth.LocalBypass == nil {
		v := false
		cfg.Auth.LocalBypass = &v
	}
	// FTP
	if cfg.FTP.Enabled == nil {
		// set default to true only if not configured by user
		cfg.FTP.Enabled = new(bool)
		*cfg.FTP.Enabled = true
	}
	if cfg.FTP.Port == 0 {
		cfg.FTP.Port = 2121
	}
	if strings.TrimSpace(cfg.FTP.PassivePortRange) == "" {
		cfg.FTP.PassivePortRange = "2122-2140"
	}
	// MQTT
	// default false already
	// WebDAV
	if cfg.WebDAV.Enabled == nil {
		// set default to true only if not configured by user
		cfg.WebDAV.Enabled = new(bool)
		*cfg.WebDAV.Enabled = true
	}
	if strings.TrimSpace(cfg.WebDAV.PathPrefix) == "" {
		cfg.WebDAV.PathPrefix = "/dav"
	}
	// Xiaomi
	if cfg.Xiaomi.Region == "" {
		cfg.Xiaomi.Region = "cn"
	}
	// Observability
	if strings.TrimSpace(cfg.Observability.LogLevel) == "" {
		cfg.Observability.LogLevel = "info"
	}
	if strings.TrimSpace(cfg.Observability.LogFormat) == "" {
		cfg.Observability.LogFormat = "text"
	}
	// EnablePprof defaults to false (zero value)
	// Version
	// HLS defaults
	if cfg.HLS.WriteBufferSize <= 0 {
		cfg.HLS.WriteBufferSize = 100
	}
	if cfg.HLS.SegmentMaxSizeMB <= 0 {
		cfg.HLS.SegmentMaxSizeMB = 10
	}
	if cfg.HLS.SegmentCount <= 0 {
		cfg.HLS.SegmentCount = 7
	}
	if cfg.HLS.MaxStreams <= 0 {
		cfg.HLS.MaxStreams = 4
	}
	// LL-HLS: low_latency defaults to false (zero value)
	if strings.TrimSpace(cfg.HLS.PartMinDuration) == "" {
		cfg.HLS.PartMinDuration = "200ms"
	}

	// Streaming defaults
	if cfg.Streaming.WebRTC.Enabled == nil {
		cfg.Streaming.WebRTC.Enabled = new(bool)
		*cfg.Streaming.WebRTC.Enabled = true
	}
	if cfg.Streaming.WebRTC.MaxViewers <= 0 {
		cfg.Streaming.WebRTC.MaxViewers = 2
	}
	if strings.TrimSpace(cfg.Streaming.WebRTC.IdleTimeout) == "" {
		cfg.Streaming.WebRTC.IdleTimeout = "60s"
	}
	if cfg.Streaming.FLV.Enabled == nil {
		cfg.Streaming.FLV.Enabled = new(bool)
		*cfg.Streaming.FLV.Enabled = true
	}
	if cfg.Streaming.FLV.MaxViewers <= 0 {
		cfg.Streaming.FLV.MaxViewers = 10
	}
	if strings.TrimSpace(cfg.Streaming.FLV.IdleTimeout) == "" {
		cfg.Streaming.FLV.IdleTimeout = "60s"
	}
	if cfg.Streaming.FLV.GOPCacheSize <= 0 {
		cfg.Streaming.FLV.GOPCacheSize = 1
	}
	if strings.TrimSpace(cfg.Version) == "" {
		cfg.Version = "1.0"
	}
	// Merge defaults
	if cfg.Merge.BatchLimit <= 0 {
		cfg.Merge.BatchLimit = 200
	}
	if cfg.Merge.CheckInterval == "" {
		cfg.Merge.CheckInterval = "1h"
	}
	if cfg.Merge.WindowSize == "" {
		cfg.Merge.WindowSize = "1h"
	}
	if cfg.Merge.MinSegmentAge == "" {
		cfg.Merge.MinSegmentAge = "10m"
	}
	if cfg.Merge.MinSegmentsToMerge <= 0 {
		cfg.Merge.MinSegmentsToMerge = 3
	}
	// Rolling merge defaults to ON (nil → true via RollingEnabledValue()).
	// Only set the pointer here if the user left it unset; an explicit
	// rolling_enabled: false must be honored.
	if cfg.Merge.RollingEnabled == nil {
		t := true
		cfg.Merge.RollingEnabled = &t
	}
	// Backfill throttling — protects RPi 3B from an IO storm on the first boot
	// after upgrading to a default-on rolling merge. MaxSegments caps the total
	// rows loaded/merged at startup; MaxAge bounds it to recent segments so
	// months of historical fragments are digested gradually by the periodic
	// MergeManager instead of all at once.
	if cfg.Merge.RollingBackfillMaxSegments == 0 {
		cfg.Merge.RollingBackfillMaxSegments = 500
	}
	if cfg.Merge.RollingBackfillMaxAge == "" {
		cfg.Merge.RollingBackfillMaxAge = "72h"
	}
	if cfg.Merge.RollingBackfillInterval == "" {
		cfg.Merge.RollingBackfillInterval = "10m"
	}
	if cfg.Merge.RollingBackfillBatch == 0 {
		cfg.Merge.RollingBackfillBatch = 500
	}
	// RollingBackfillConcurrency: auto-select by available RAM if user didn't
	// set it explicitly. RPi 3B (1GB RAM, USB-bound IO, Cortex-A53) gets 1 to
	// avoid seek thrashing against the recorder; hosts with >2GB get 3.
	if cfg.Merge.RollingBackfillConcurrency == 0 {
		if memAvailableMB() > 2048 {
			cfg.Merge.RollingBackfillConcurrency = 3
		} else {
			cfg.Merge.RollingBackfillConcurrency = 1
		}
	}
	// Transcoding defaults
	if cfg.Transcoding.MaxWorkers == 0 {
		cfg.Transcoding.MaxWorkers = 1
	}
	if cfg.Transcoding.JobTimeout == "" {
		cfg.Transcoding.JobTimeout = "30m"
	}
	if cfg.Transcoding.HistoryRetention == "" {
		cfg.Transcoding.HistoryRetention = "168h" // 7 days
	}

	// Vision push integration defaults
	if cfg.Vision.HeartbeatTimeoutSecs <= 0 {
		cfg.Vision.HeartbeatTimeoutSecs = 60
	}
	if cfg.Vision.PushMode == "" {
		cfg.Vision.PushMode = "notify"
	}
	// RTMP defaults
	if cfg.RTMP.Enabled == nil {
		cfg.RTMP.Enabled = new(bool)
		*cfg.RTMP.Enabled = false
	}
	if cfg.RTMP.Port <= 0 {
		cfg.RTMP.Port = 1935
	}
	if cfg.RTMP.StreamKeys == nil {
		cfg.RTMP.StreamKeys = make(map[string]string)
	}
	// WebSocket defaults
	if cfg.WebSocket.MaxViewers <= 0 {
		cfg.WebSocket.MaxViewers = 10
	}
	if cfg.WebSocket.WriteBufSize <= 0 {
		cfg.WebSocket.WriteBufSize = 100
	}
	if cfg.WebSocket.IdleTimeout <= 0 {
		cfg.WebSocket.IdleTimeout = 60 * time.Second
	}

	// SRT defaults
	if cfg.SRT.Enabled == nil {
		cfg.SRT.Enabled = new(bool)
		*cfg.SRT.Enabled = false
	}
	if cfg.SRT.Port <= 0 {
		cfg.SRT.Port = 9000
	}

	// GB28181 cascade client defaults (#364)
	if strings.TrimSpace(cfg.GB28181Cascade.SIPListen) == "" {
		cfg.GB28181Cascade.SIPListen = ":5061"
	}
	if strings.TrimSpace(cfg.GB28181Cascade.HeartbeatInterval) == "" {
		cfg.GB28181Cascade.HeartbeatInterval = "60s"
	}
	if cfg.GB28181Cascade.RegisterExpires == 0 {
		cfg.GB28181Cascade.RegisterExpires = 3600
	}

	// GB28181 defaults (server block; per-camera fields have no defaults)
	if strings.TrimSpace(cfg.GB28181.SIPListen) == "" {
		cfg.GB28181.SIPListen = ":5060"
	}
	if strings.TrimSpace(cfg.GB28181.HeartbeatInterval) == "" {
		cfg.GB28181.HeartbeatInterval = "60s"
	}
	if strings.TrimSpace(cfg.GB28181.CatalogInterval) == "" {
		cfg.GB28181.CatalogInterval = "30m"
	}
	if strings.TrimSpace(cfg.GB28181.PortRange) == "" {
		cfg.GB28181.PortRange = "30000-30050"
	}
	if strings.TrimSpace(cfg.GB28181.TCPFraming) == "" {
		cfg.GB28181.TCPFraming = "auto"
	}
	if strings.TrimSpace(cfg.GB28181.MediaTransport) == "" {
		if cfg.GB28181.TCPMode {
			cfg.GB28181.MediaTransport = "tcp-passive" // legacy tcp_mode alias
		} else {
			cfg.GB28181.MediaTransport = "udp"
		}
	}
	if strings.TrimSpace(cfg.GB28181.SIPTransport) == "" {
		cfg.GB28181.SIPTransport = "udp"
	}
	if strings.TrimSpace(cfg.GB28181.SubscribeExpires) == "" {
		cfg.GB28181.SubscribeExpires = "3600s"
	}

	// Health defaults
	if cfg.Health.EventsRetention == "" {
		cfg.Health.EventsRetention = "720h" // 30 days
	}
	if cfg.Health.Alerts.Cooldown == "" {
		cfg.Health.Alerts.Cooldown = "5m"
	}
	if cfg.Health.Layer1.OfflineThreshold == "" {
		cfg.Health.Layer1.OfflineThreshold = "30s"
	}
	if cfg.Health.Layer2.BitrateChangeThreshold == 0 {
		cfg.Health.Layer2.BitrateChangeThreshold = 0.5
	}
	if cfg.Health.Layer2.MinFPS == 0 {
		cfg.Health.Layer2.MinFPS = 5
	}
	if cfg.Health.Layer2.MaxIDRInterval == "" {
		cfg.Health.Layer2.MaxIDRInterval = "60s"
	}
	if cfg.Health.Layer2_5.FreezeTimeout == "" {
		cfg.Health.Layer2_5.FreezeTimeout = "10s"
	}

	// Auto-remediation defaults
	if cfg.Health.AutoRemediation.MaxRestartsPerHour == 0 {
		cfg.Health.AutoRemediation.MaxRestartsPerHour = 3
	}
	if cfg.Health.AutoRemediation.CooldownMinutes == 0 {
		cfg.Health.AutoRemediation.CooldownMinutes = 5
	}
	if cfg.Health.AutoRemediation.BlacklistHours == 0 {
		cfg.Health.AutoRemediation.BlacklistHours = 1
	}
	if cfg.Health.AutoRemediation.GlobalMaxPerMin == 0 {
		cfg.Health.AutoRemediation.GlobalMaxPerMin = 10
	}
	if cfg.Health.AutoRemediation.ReconnectingTimeoutMinutes == 0 {
		cfg.Health.AutoRemediation.ReconnectingTimeoutMinutes = 10
	}
	if cfg.Health.AutoRemediation.RediscoveryRescanMinutes == 0 {
		cfg.Health.AutoRemediation.RediscoveryRescanMinutes = 5
	}
	if cfg.Health.AutoRemediation.RediscoveryRescanMaxMinutes == 0 {
		cfg.Health.AutoRemediation.RediscoveryRescanMaxMinutes = 60
	}
	if cfg.Health.AutoRemediation.RediscoveryRescanBackoff < 1.0 {
		cfg.Health.AutoRemediation.RediscoveryRescanBackoff = 2.0
	}

	// IP re-discovery (self-healing) defaults. Enabled by default since it only
	// activates for ONVIF cameras that have a stable_id AND are blacklisted, so it
	// is a no-op for everything else. Honours RPi-3B constraints.
	if cfg.Health.Rediscovery.MaxParallel == 0 {
		cfg.Health.Rediscovery.MaxParallel = 16
	}
	if cfg.Health.Rediscovery.ProbeTimeout == "" {
		cfg.Health.Rediscovery.ProbeTimeout = "2s"
	}
	if cfg.Health.Rediscovery.MaxDuration == "" {
		cfg.Health.Rediscovery.MaxDuration = "30s"
	}

	// Auto-discover defaults. The feature itself defaults to OFF (see
	// AutoDiscoverEnabled); these only apply when the user turns it on.
	// ScanInterval floor of 60s keeps multicast Probe load RPi-3B-friendly.
	if cfg.AutoDiscover.ScanIntervalSeconds == 0 {
		cfg.AutoDiscover.ScanIntervalSeconds = 60
	}
	if cfg.AutoDiscover.ScanIntervalSeconds < 30 {
		cfg.AutoDiscover.ScanIntervalSeconds = 30
	}

	// AI defaults
	if cfg.AI.ConfidenceThreshold <= 0 {
		cfg.AI.ConfidenceThreshold = 0.5
	}
	if cfg.AI.FrameSkipRate <= 0 {
		cfg.AI.FrameSkipRate = 10
	}
	if cfg.AI.Zones == nil {
		cfg.AI.Zones = make(map[string][]ai.ROI)
	}
	if cfg.AI.ModelURL == "" {
		cfg.AI.ModelURL = "/models/yolo11n.onnx"
	}
	// EMA smoothing defaults (#183): mirror the ObjectDetector constants so a
	// config that omits these fields behaves identically to the pre-config era.
	if cfg.AI.EmaAlpha <= 0 {
		cfg.AI.EmaAlpha = 0.3
	}
	if cfg.AI.MaxAge <= 0 {
		cfg.AI.MaxAge = 15
	}
	// EnabledClasses default: empty = all 80 COCO classes (no filtering, #184).

	// Remote log defaults
	if cfg.RemoteLog.Format == "" {
		cfg.RemoteLog.Format = "jsonline"
	}
	// Camera protocol/encoding normalization
	for i := range cfg.Cameras {
		cam := &cfg.Cameras[i]
		// If encoding is still empty for known transport-only protocols, set sensible defaults
		if cam.Encoding == "" {
			switch cam.Protocol {
			case "rtsp":
				cam.Encoding = "h264"
			case "http":
				cam.Encoding = "jpeg"
			case "onvif":
				cam.Encoding = "" // ONVIF auto-detects
			case string(model.ProtoSRT), string(model.ProtoRTMP):
				// Push cameras: encoding is derived from the published stream.
				// Default to h264 (the only codec RTMP supports; SRT's current
				// MPEG-TS demux is also H.264-only).
				cam.Encoding = "h264"
			}
		}
		// Reject audio_enabled only for true HTTP JPEG cameras (HTTP multipart MJPEG
		// has no audio source). Gate on protocol, NOT encoding: an ONVIF camera whose
		// profile reports Encoding="jpeg" may still serve MJPEG over RTSP with G.711
		// audio (e.g. ESP32 MiBeeCam RTSP-AVI firmware) and record into AVI, so it must
		// keep audio_enabled. RTSP MJPEG and ONVIF MJPEG-over-RTSP are audio-capable.
		if cam.AudioEnabled && cam.Protocol == string(model.ProtoHTTP) {
			slog.Warn("audio_enabled not supported for HTTP JPEG cameras (no audio source), disabling", "camera_id", cam.ID)
			cam.AudioEnabled = false
		}

		// Timelapse defaults
		if cam.Timelapse != nil {
			if cam.Timelapse.Interval == "" {
				cam.Timelapse.Interval = "30s"
			}
			if cam.Timelapse.FrameSource == "" {
				cam.Timelapse.FrameSource = "auto"
			}
			// SnapshotURL defaults to empty string (must be set explicitly for snapshot source)
			// Schedule defaults to nil (24/7 recording)
			// Paused defaults to false (zero value)
			// DeleteOriginal defaults to false (zero value)
			// Merge defaults
			if cam.Timelapse.MergeMode == "" {
				cam.Timelapse.MergeMode = "auto"
			}
			if cam.Timelapse.DailyMerge == nil {
				v := true
				cam.Timelapse.DailyMerge = &v
			}
			if cam.Timelapse.MergeOutputFPS == 0 {
				cam.Timelapse.MergeOutputFPS = 30
			}
			if cam.Timelapse.MergeDuration == "" {
				// Default to natural-day (midnight-aligned 24h window) to match the
				// DB schema default (cameras.merge_duration DEFAULT 'natural-day')
				// and the handleGetCameraTimelapse / handleTimelapseMerge fallbacks.
				cam.Timelapse.MergeDuration = "natural-day"
			}
			// MergeEnabled defaults to nil (auto-detect)
		}
	}

	// Update (in-app version check — sensing layer only, never executes upgrade)
	if cfg.Update.Channel == "" {
		cfg.Update.Channel = "stable"
	}
	if cfg.Update.CheckInterval == "" {
		cfg.Update.CheckInterval = "1h"
	}
	if cfg.Update.Repo == "" {
		cfg.Update.Repo = "Mi-Bee-Studio/MiBeeNvr"
	}
}
