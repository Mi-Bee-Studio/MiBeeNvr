package config

import (
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
)

// Pre-compiled regex patterns for validation (avoids SA6000: regexp.MatchString in loop).
// Used by camera push-target and transcoding validation below.
var (
	rePlatformName = regexp.MustCompile(`^[a-zA-Z0-9_]+$`)
	reResolution   = regexp.MustCompile(`^\d+x\d+$`)
	reBitrate      = regexp.MustCompile(`^(0|\d+(\.\d+)?[kMG])$`)
	// reStableID matches the character class allowed for a camera's stable_id
	// (the hardware-level identity used by IP self-healing / rediscovery):
	// alphanumerics plus ':', '_', '-'. Length 3–64. This deliberately rejects
	// values that cannot be a hardware serial: IPs (contain '.'), URLs (contain
	// '/'), all-zero / all-same-char strings (firmware glitch returns — see
	// upstream seeed-esp32s3-cam issue #2). See #216.
	reStableID = regexp.MustCompile(`^[A-Za-z0-9:_-]{3,64}$`)
)

// IsValidStableID reports whether s is a plausible hardware-identity value
// suitable for persisting as a camera's StableID. It accepts real-world serial
// formats observed in the field: 12-hex MAC (lowercase `744dbd988218`), uppercase
// efuse MAC (`88492D665CCF`), colon-separated MAC (`74:4d:bd:98:82:18`), and
// vendor serials with `-`/`_` separators (`SN-AAA`, `XIAOMI-CAM-001`). It rejects
// the dirty values that have caused permanent rediscovery failure in production
// (see #216): IP addresses, URLs, all-zero, all-same-char, too short/long.
//
// Used to gate both write paths (ONVIF reverse lookup, API add/update, config
// validation) and the "already set, skip" guards so a once-persisted dirty value
// gets overwritten on the next successful ONVIF lookup instead of being frozen.
func IsValidStableID(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" || !reStableID.MatchString(s) {
		return false
	}
	// Reject all-zero / all-same-character runs. Compare on the alphanumerics
	// only (skip ':'/'_'/'-' separators) so a colon-separated all-zero MAC like
	// "00:00:00:00:00:00" is also caught — its alphanumerics are all '0'.
	var first byte
	for i := range len(s) {
		c := s[i]
		if c == ':' || c == '_' || c == '-' {
			continue
		}
		if first == 0 {
			first = c
		} else if c != first {
			return true
		}
	}
	return false
}

// validateConfigDetails contains the full per-subsystem validation logic,
// extracted from Validate() for readability. Order matters: each section may
// depend on mutations performed by earlier sections (e.g. camera validation
// auto-populates ONVIFEndpoint from URL; storage validation clamps
// SegmentDuration based on available RAM). All mutations go through the *Config
// pointer so they persist.
func validateConfigDetails(cfg *Config) error {
	if cfg == nil {
		return fmt.Errorf("config is nil")
	}
	// Timezone validation
	if cfg.Timezone != "" && cfg.Timezone != "UTC" && cfg.Timezone != "Local" {
		if _, err := time.LoadLocation(cfg.Timezone); err != nil {
			return fmt.Errorf("timezone: invalid IANA name %q: %w", cfg.Timezone, err)
		}
	}
	// HTTPS/TLS listener validation
	if strings.TrimSpace(cfg.Server.TLSListen) != "" {
		if strings.TrimSpace(cfg.Server.CertFile) == "" || strings.TrimSpace(cfg.Server.KeyFile) == "" {
			return fmt.Errorf("server.tls_listen %q is set but server.cert_file / server.key_file are missing", cfg.Server.TLSListen)
		}
	}
	// LAN discovery responder port validation (0 = not set, defaults applied)
	if p := cfg.Server.Discovery.UDP.Port; p < 0 || p > 65535 {
		return fmt.Errorf("server.discovery.udp.port must be between 1 and 65535, got %d", p)
	}
	// cameras must have id and url
	seen := make(map[string]int)
	for i, c := range cfg.Cameras {
		if strings.TrimSpace(c.ID) == "" {
			return fmt.Errorf("camera[%d].id is required", i)
		}
		if j, ok := seen[c.ID]; ok {
			return fmt.Errorf("camera[%d] and camera[%d] have duplicate id %q", j, i, c.ID)
		}
		seen[c.ID] = i
		if strings.TrimSpace(c.URL) == "" && c.Protocol != "onvif" && c.Protocol != "xiaomi" &&
			c.Protocol != string(model.ProtoSRT) && c.Protocol != string(model.ProtoRTMP) &&
			c.Protocol != string(model.ProtoWHIP) &&
			c.Protocol != string(model.ProtoGB28181) {
			return fmt.Errorf("camera[%d].url is required", i)
		}
		// Validate URL format if set
		if c.URL != "" {
			parsed, err := url.Parse(c.URL)
			if err != nil || parsed.Scheme == "" || parsed.Host == "" {
				return fmt.Errorf("camera[%d].url has invalid format: %s", i, c.URL)
			}
		}
		if (c.Protocol == "onvif" || c.Protocol == string(model.ProtoONVIF)) && strings.TrimSpace(c.ONVIFEndpoint) == "" && strings.TrimSpace(c.URL) == "" {
			return fmt.Errorf("camera[%d].url or onvif_endpoint is required for ONVIF cameras", i)
		}
		// Auto-populate: if url is set but onvif_endpoint is empty, copy url to onvif_endpoint
		if (c.Protocol == "onvif" || c.Protocol == string(model.ProtoONVIF)) && strings.TrimSpace(c.ONVIFEndpoint) == "" && strings.TrimSpace(c.URL) != "" {
			c.ONVIFEndpoint = c.URL
		}
		// Validate ONVIF endpoint URL format if set
		if c.ONVIFEndpoint != "" {
			parsed, err := url.Parse(c.ONVIFEndpoint)
			if err != nil || parsed.Scheme == "" || parsed.Host == "" {
				return fmt.Errorf("camera[%d].onvif_endpoint has invalid format: %s", i, c.ONVIFEndpoint)
			}
		}
		// Self-heal legacy xiaomi configs with an empty encoding (#402): since
		// v0.10.0 the UI deliberately omits encoding for xiaomi cameras (the
		// recorder probes H264/H265 from the MISS stream at runtime), but the
		// add handler only started defaulting it — configs written in between
		// hold encoding:"" which the strict protocol/encoding validation below
		// rejects fatally at startup. Backfill the same h264 hint the add path
		// now writes; the runtime codec probe corrects the label for H.265
		// devices. Mirrors the onvif_endpoint auto-populate above.
		if c.Protocol == string(model.ProtoXiaomi) && c.Encoding == "" {
			// Write through the loop index — c is a copy (the onvif_endpoint
			// auto-populate above mutates only its copy and never persists).
			cfg.Cameras[i].Encoding = string(model.FormatH264)
			c.Encoding = string(model.FormatH264)
		}
		// 0.10.0+: combined protocol strings (e.g. "rtsp_h264") are no longer
		// accepted. protocol and encoding must be specified separately.
		proto := c.Protocol
		enc := c.Encoding
		if strings.Contains(proto, "_") {
			return fmt.Errorf("camera[%d].protocol %q: combined format is no longer supported in 0.10.0+; split into separate protocol (%q) and encoding fields", i, proto, strings.SplitN(proto, "_", 2)[0])
		}
		if err := model.ValidateProtocolEncoding(proto, enc); err != nil {
			return fmt.Errorf("camera[%d].%w", i, err)
		}

		// Validate GB28181-specific camera fields.
		if c.Protocol == string(model.ProtoGB28181) {
			if strings.TrimSpace(c.GB28181.DeviceID) == "" {
				return fmt.Errorf("camera[%d].gb28181.device_id is required for gb28181 cameras", i)
			}
			if strings.TrimSpace(c.GB28181.ChannelID) == "" {
				return fmt.Errorf("camera[%d].gb28181.channel_id is required for gb28181 cameras", i)
			}
		}

		// Validate IP self-healing fields (stable_id + subnet_hints).
		// A non-empty stable_id that fails IsValidStableID (IP, URL, all-zero
		// MAC — frozen in YAML by a prior firmware glitch, see #216) is logged
		// as a WARNING, NOT a hard error. Hard-erroring would brick startup on
		// pre-existing dirty data (the very thing #216 exists to fix). Instead
		// the value is tolerated at load time and the async self-heal path
		// (backfillStableIDs / ensureStableID) overwrites it on the next
		// successful ONVIF lookup. New dirty values are still rejected at the
		// API write boundary (handleAddCamera / handleUpdateCamera).
		if strings.TrimSpace(c.StableID) != "" && !IsValidStableID(c.StableID) {
			slog.Warn("camera stable_id is not a valid hardware identity; will be overwritten by the next ONVIF lookup",
				"camera_idx", i, "camera_id", c.ID, "stable_id", c.StableID)
		}
		for j, hint := range c.SubnetHints {
			hint = strings.TrimSpace(hint)
			if hint == "" {
				return fmt.Errorf("camera[%d].subnet_hints[%d] is empty", i, j)
			}
			if _, _, err := net.ParseCIDR(hint); err != nil {
				return fmt.Errorf("camera[%d].subnet_hints[%d] invalid CIDR %q: %w", i, j, hint, err)
			}
		}

		// Validate push-out targets (relay).
		seenTargetIDs := make(map[string]bool, len(c.PushTargets))
		for j, pt := range c.PushTargets {
			if strings.TrimSpace(pt.ID) == "" {
				return fmt.Errorf("camera[%d].push_targets[%d].id is required", i, j)
			}
			if seenTargetIDs[pt.ID] {
				return fmt.Errorf("camera[%d].push_targets[%d] duplicate id %q", i, j, pt.ID)
			}
			seenTargetIDs[pt.ID] = true
			if pt.Protocol != "rtmp" && pt.Protocol != "rtsp" {
				return fmt.Errorf("camera[%d].push_targets[%d].protocol must be rtmp or rtsp", i, j)
			}
			pu, perr := url.Parse(pt.URL)
			if perr != nil || pu.Host == "" {
				return fmt.Errorf("camera[%d].push_targets[%d].url has invalid format: %s", i, j, pt.URL)
			}
			wantScheme := "rtmp"
			if pt.Protocol == "rtsp" {
				wantScheme = "rtsp"
			}
			if pu.Scheme != wantScheme && pu.Scheme != wantScheme+"s" {
				return fmt.Errorf("camera[%d].push_targets[%d].url scheme must be %s://, got %s", i, j, wantScheme, pu.Scheme)
			}
			// Validate platform preset name.
			if pt.Platform != "" {
				if !rePlatformName.MatchString(pt.Platform) {
					return fmt.Errorf("camera[%d].push_targets[%d].platform must be alphanumeric (underscores allowed)", i, j)
				}
			}
			// Validate transcode policy.
			switch pt.TranscodePolicy {
			case "", "auto", "force_sw", "off":
				// valid
			default:
				return fmt.Errorf("camera[%d].push_targets[%d].transcode_policy must be one of \"auto\", \"force_sw\", \"off\" (got %q)", i, j, pt.TranscodePolicy)
			}
			// Validate video preset overrides.
			if pt.VideoPresetOverride != nil {
				v := pt.VideoPresetOverride
				if v.Resolution != "" {
					if !reResolution.MatchString(v.Resolution) {
						return fmt.Errorf("camera[%d].push_targets[%d].video_preset_override.resolution must be in format WxH (e.g. 1920x1080)", i, j)
					}
				}
				if v.Framerate > 0 && (v.Framerate < 1 || v.Framerate > 120) {
					return fmt.Errorf("camera[%d].push_targets[%d].video_preset_override.framerate must be between 1 and 120", i, j)
				}
				if v.VideoBitrateKbps > 0 && (v.VideoBitrateKbps < 100 || v.VideoBitrateKbps > 50000) {
					return fmt.Errorf("camera[%d].push_targets[%d].video_preset_override.video_bitrate_kbps must be between 100 and 50000", i, j)
				}
				if v.GopSeconds > 0 && (v.GopSeconds < 1 || v.GopSeconds > 10) {
					return fmt.Errorf("camera[%d].push_targets[%d].video_preset_override.gop_seconds must be between 1 and 10", i, j)
				}
				if v.Profile != "" && v.Profile != "baseline" && v.Profile != "main" && v.Profile != "high" {
					return fmt.Errorf("camera[%d].push_targets[%d].video_preset_override.profile must be one of \"baseline\", \"main\", \"high\" (got %q)", i, j, v.Profile)
				}
				if v.Bframes < 0 || v.Bframes > 2 {
					return fmt.Errorf("camera[%d].push_targets[%d].video_preset_override.bframes must be between 0 and 2", i, j)
				}
			}
			// Warn when use_ffmpeg is enabled — requires FFmpeg binary at runtime.
			if pt.UseFFmpeg && pt.SourceURL == "" {
				slog.Warn("push_target use_ffmpeg enabled without source_url — will auto-resolve from camera URL", "camera_idx", i, "target_idx", j)
			}
		}

		// Validate per-camera health overrides
		if c.HealthOverrides.MaxIDRInterval != "" {
			if _, err := time.ParseDuration(c.HealthOverrides.MaxIDRInterval); err != nil {
				return fmt.Errorf("camera[%d].health_overrides.max_idr_interval invalid duration: %w", i, err)
			}
		}
		if c.HealthOverrides.OfflineThreshold != "" {
			if _, err := time.ParseDuration(c.HealthOverrides.OfflineThreshold); err != nil {
				return fmt.Errorf("camera[%d].health_overrides.offline_threshold invalid duration: %w", i, err)
			}
		}
		if c.HealthOverrides.FreezeTimeout != "" {
			if _, err := time.ParseDuration(c.HealthOverrides.FreezeTimeout); err != nil {
				return fmt.Errorf("camera[%d].health_overrides.freeze_timeout invalid duration: %w", i, err)
			}
		}
		if c.HealthOverrides.BitrateChangeThreshold < 0 || c.HealthOverrides.BitrateChangeThreshold > 1 {
			return fmt.Errorf("camera[%d].health_overrides.bitrate_change_threshold must be between 0 and 1", i)
		}
		if c.HealthOverrides.MinFPS < 0 {
			return fmt.Errorf("camera[%d].health_overrides.min_fps must be >= 0", i)
		}
	}
	// Validate Xiaomi configuration: non-fatal — disable cameras instead of crashing
	// port ranges
	if cfg.FTP.Port < 1 || cfg.FTP.Port > 65535 {
		return fmt.Errorf("ftp port out of range: %d", cfg.FTP.Port)
	}
	// Validate segment_duration — cap based on available RAM.
	// The MP4 muxer holds all samples in RAM until the segment closes (moov atom
	// written last), so a longer segment = more RAM per stream. On a 1GB device
	// (RPi 3B) a 2-minute 1080p H265 segment is ~60MB/stream — 4 streams risk OOM.
	// On a 4GB+ device (Banana Pi M5, x86) 1-2 minute segments are safe and
	// halve the fragment count rolling merge must digest (the main driver of
	// timeline clutter and merge backlog).
	if dur, err := time.ParseDuration(cfg.Storage.SegmentDuration); err != nil {
		return fmt.Errorf("storage.segment_duration invalid: %w", err)
	} else {
		maxSegDur := maxSegmentDurationForMem()
		if dur > maxSegDur {
			capped := maxSegDur.String()
			slog.Warn("storage.segment_duration exceeds platform RAM limit, clamping",
				"got", cfg.Storage.SegmentDuration, "capped_to", capped,
				"mem_available_mb", memAvailableMB(), "reason", "MP4 muxer holds samples in RAM until segment close")
			cfg.Storage.SegmentDuration = capped
		}
	}
	// Validate retention_days
	if cfg.Cleanup.RetentionDays < 1 || cfg.Cleanup.RetentionDays > 3650 {
		return fmt.Errorf("cleanup.retention_days must be between 1 and 3650, got %d", cfg.Cleanup.RetentionDays)
	}
	// Validate disk_threshold_percent
	if cfg.Cleanup.DiskThresholdPercent < 50 || cfg.Cleanup.DiskThresholdPercent > 99 {
		return fmt.Errorf("cleanup.disk_threshold_percent must be between 50 and 99, got %d", cfg.Cleanup.DiskThresholdPercent)
	}
	// Validate observability.log_level
	if cfg.Observability.LogLevel != "debug" && cfg.Observability.LogLevel != "info" && cfg.Observability.LogLevel != "warn" && cfg.Observability.LogLevel != "error" {
		return fmt.Errorf("observability.log_level invalid: %s (must be debug/info/warn/error)", cfg.Observability.LogLevel)
	}
	// Validate observability.log_format
	if cfg.Observability.LogFormat != "json" && cfg.Observability.LogFormat != "text" {
		return fmt.Errorf("observability.log_format invalid: %s (must be json/text)", cfg.Observability.LogFormat)
	}

	// Validate remote_log
	if cfg.RemoteLog.Enabled {
		if strings.TrimSpace(cfg.RemoteLog.Endpoint) == "" {
			return fmt.Errorf("remote_log.endpoint is required when remote_log.enabled=true")
		}
		if cfg.RemoteLog.Format != "jsonline" && cfg.RemoteLog.Format != "loki" {
			return fmt.Errorf("remote_log.format must be \"jsonline\" or \"loki\", got %q", cfg.RemoteLog.Format)
		}
	}
	if cfg.Merge.Enabled {
		if _, err := time.ParseDuration(cfg.Merge.CheckInterval); err != nil {
			return fmt.Errorf("invalid merge check_interval: %w", err)
		}
		if _, err := time.ParseDuration(cfg.Merge.WindowSize); err != nil {
			return fmt.Errorf("invalid merge window_size: %w", err)
		}
		if cfg.Merge.BatchLimit <= 0 {
			return fmt.Errorf("merge batch_limit must be positive")
		}
		if _, err := time.ParseDuration(cfg.Merge.MinSegmentAge); err != nil {
			return fmt.Errorf("invalid merge min_segment_age: %w", err)
		}
		if cfg.Merge.MinSegmentsToMerge < 2 {
			return fmt.Errorf("merge min_segments_to_merge must be at least 2")
		}
	}
	// Validate rolling merge config when enabled (it defaults ON, so this runs
	// for typical deployments). Mirrors the best-effort parse logic in
	// rolling.resolveRollingConfig but fails fast at config load instead of
	// silently falling back.
	if cfg.Merge.RollingEnabledValue() {
		if cfg.Merge.RollingDebounce != "" {
			if d, err := time.ParseDuration(cfg.Merge.RollingDebounce); err != nil || d <= 0 {
				return fmt.Errorf("invalid merge.rolling_debounce %q: must be a positive duration", cfg.Merge.RollingDebounce)
			}
		}
		if cfg.Merge.RollingWindow != "" {
			if d, err := time.ParseDuration(cfg.Merge.RollingWindow); err != nil || d <= 0 {
				return fmt.Errorf("invalid merge.rolling_window %q: must be a positive duration", cfg.Merge.RollingWindow)
			} else if d > time.Hour {
				return fmt.Errorf("invalid merge.rolling_window %q: must be ≤ 1h (windows >1h span a UTC day boundary and were removed for timezone safety)", cfg.Merge.RollingWindow)
			}
		}
		if cfg.Merge.RollingMinDuration != "" {
			if d, err := time.ParseDuration(cfg.Merge.RollingMinDuration); err != nil || d <= 0 {
				return fmt.Errorf("invalid merge.rolling_min_duration %q: must be a positive duration", cfg.Merge.RollingMinDuration)
			}
		}
		if cfg.Merge.RollingBackfillMaxAge != "" {
			if d, err := time.ParseDuration(cfg.Merge.RollingBackfillMaxAge); err != nil || d <= 0 {
				return fmt.Errorf("invalid merge.rolling_backfill_max_age %q: must be a positive duration", cfg.Merge.RollingBackfillMaxAge)
			}
		}
		if cfg.Merge.RollingBackfillMaxSegments < 0 {
			return fmt.Errorf("merge.rolling_backfill_max_segments must be >= 0, got %d", cfg.Merge.RollingBackfillMaxSegments)
		}
		if cfg.Merge.RollingBackfillInterval != "" && cfg.Merge.RollingBackfillInterval != "0" {
			if d, err := time.ParseDuration(cfg.Merge.RollingBackfillInterval); err != nil || d <= 0 {
				return fmt.Errorf("invalid merge.rolling_backfill_interval %q: must be a positive duration or \"0\" to disable", cfg.Merge.RollingBackfillInterval)
			}
		}
		if cfg.Merge.RollingBackfillBatch < 0 {
			return fmt.Errorf("merge.rolling_backfill_batch must be >= 0, got %d", cfg.Merge.RollingBackfillBatch)
		}
		if cfg.Merge.RollingBackfillConcurrency < 0 || cfg.Merge.RollingBackfillConcurrency > 16 {
			return fmt.Errorf("merge.rolling_backfill_concurrency must be between 0 (auto) and 16, got %d", cfg.Merge.RollingBackfillConcurrency)
		}
	}
	// Validate transcoding configuration
	if cfg.Transcoding.MaxWorkers < 1 || cfg.Transcoding.MaxWorkers > 4 {
		return fmt.Errorf("transcoding.max_workers must be between 1 and 4, got %d", cfg.Transcoding.MaxWorkers)
	}
	if cfg.Transcoding.JobTimeout != "" {
		jobTimeout, err := time.ParseDuration(cfg.Transcoding.JobTimeout)
		if err != nil {
			return fmt.Errorf("transcoding.job_timeout invalid duration: %w", err)
		}
		if jobTimeout < time.Second {
			return fmt.Errorf("transcoding.job_timeout must be at least 1s, got %s", cfg.Transcoding.JobTimeout)
		}
		if jobTimeout > 4*time.Hour {
			return fmt.Errorf("transcoding.job_timeout must be <= 4h, got %s", cfg.Transcoding.JobTimeout)
		}
	}
	if cfg.Transcoding.HistoryRetention != "" {
		hr, err := time.ParseDuration(cfg.Transcoding.HistoryRetention)
		if err != nil {
			return fmt.Errorf("transcoding.history_retention invalid duration: %w", err)
		}
		if hr < 24*time.Hour {
			return fmt.Errorf("transcoding.history_retention must be at least 24h, got %s", cfg.Transcoding.HistoryRetention)
		}
	}
	for _, cam := range cfg.Cameras {
		if cam.Transcoding == nil {
			continue
		}
		if cam.Transcoding.TargetCodec != "" && cam.Transcoding.TargetCodec != "h264" && cam.Transcoding.TargetCodec != "h265" {
			return fmt.Errorf("cameras.%s.transcoding.target_codec must be h264 or h265, got %q", cam.ID, cam.Transcoding.TargetCodec)
		}
		validPresets := map[string]bool{"": true, "ultrafast": true, "faster": true, "medium": true}
		if !validPresets[cam.Transcoding.Preset] {
			return fmt.Errorf("cameras.%s.transcoding.preset must be ultrafast, faster, or medium, got %q", cam.ID, cam.Transcoding.Preset)
		}

		if cam.Transcoding.Bitrate != "" {
			if !reBitrate.MatchString(cam.Transcoding.Bitrate) {
				return fmt.Errorf("cameras.%s.transcoding.bitrate must be in format like 500k, 2M, 1.5G (got %q)", cam.ID, cam.Transcoding.Bitrate)
			}
		}
		if cam.Transcoding.CRF < 0 || cam.Transcoding.CRF > 51 {
			return fmt.Errorf("cameras.%s.transcoding.crf must be between 0 and 51 (got %d)", cam.ID, cam.Transcoding.CRF)
		}
	}

	// Validate per-camera timelapse configuration
	for _, cam := range cfg.Cameras {
		if cam.Timelapse == nil {
			continue
		}
		if cam.Timelapse.Interval != "" {
			dur, err := time.ParseDuration(cam.Timelapse.Interval)
			if err != nil {
				return fmt.Errorf("cameras.%s.timelapse.interval invalid duration: %w", cam.ID, err)
			}
			if dur < time.Second {
				return fmt.Errorf("cameras.%s.timelapse.interval must be at least 1s, got %s", cam.ID, cam.Timelapse.Interval)
			}
		}
		// Validate frame_source
		if cam.Timelapse.FrameSource != "" {
			switch cam.Timelapse.FrameSource {
			case "auto", "snapshot", "rtsp_keyframe", "mjpeg", "latest_frame":
				// valid
			default:
				return fmt.Errorf("cameras.%s.timelapse.frame_source must be one of \"auto\", \"snapshot\", \"rtsp_keyframe\", \"mjpeg\", \"latest_frame\" (got %q)", cam.ID, cam.Timelapse.FrameSource)
			}
		}
		// Validate schedule
		if sched := cam.Timelapse.Schedule; sched != nil {
			// Validate days of week (0-6)
			for i, d := range sched.DaysOfWeek {
				if d < 0 || d > 6 {
					return fmt.Errorf("cameras.%s.timelapse.schedule.days_of_week[%d] must be between 0 (Sunday) and 6 (Saturday), got %d", cam.ID, i, d)
				}
			}
			// Validate and normalize time ranges
			var parsed []TimeRange
			for i, tr := range sched.TimeRanges {
				startH, startM, err := parseHHMM(tr.Start)
				if err != nil {
					return fmt.Errorf("cameras.%s.timelapse.schedule.time_ranges[%d].start invalid time: %w", cam.ID, i, err)
				}
				endH, endM, err := parseHHMM(tr.End)
				if err != nil {
					return fmt.Errorf("cameras.%s.timelapse.schedule.time_ranges[%d].end invalid time: %w", cam.ID, i, err)
				}
				// Convert to minutes since midnight for comparison
				startMins := startH*60 + startM
				endMins := endH*60 + endM
				if endMins <= startMins {
					return fmt.Errorf("cameras.%s.timelapse.schedule.time_ranges[%d].end (%s) must be after start (%s)", cam.ID, i, tr.End, tr.Start)
				}
				parsed = append(parsed, TimeRange{Start: tr.Start, End: tr.End})
			}
			// Auto-merge overlapping time ranges
			sched.TimeRanges = mergeTimeRanges(parsed)
		}
		// Validate merge config fields
		if cam.Timelapse.MergeMode != "" {
			if cam.Timelapse.MergeMode != "auto" && cam.Timelapse.MergeMode != "mp4" && cam.Timelapse.MergeMode != "jpeg" {
				return fmt.Errorf("cameras.%s.timelapse.merge_mode must be one of \"auto\", \"mp4\", \"jpeg\" (got %q)", cam.ID, cam.Timelapse.MergeMode)
			}
		}
		if cam.Timelapse.MergeOutputFPS < 1 || cam.Timelapse.MergeOutputFPS > 60 {
			return fmt.Errorf("cameras.%s.timelapse.merge_output_fps must be between 1 and 60, got %d", cam.ID, cam.Timelapse.MergeOutputFPS)
		}
		if _, err := ParseMergeDuration(cam.Timelapse.MergeDuration); err != nil {
			return fmt.Errorf("cameras.%s.timelapse.merge_duration invalid: %w", cam.ID, err)
		}
	}

	// Validate hls.segment_count
	if cfg.HLS.SegmentCount < 3 || cfg.HLS.SegmentCount > 10 {
		return fmt.Errorf("hls.segment_count must be between 3 and 10, got %d", cfg.HLS.SegmentCount)
	}
	// Validate hls.max_streams
	if cfg.HLS.MaxStreams < 1 || cfg.HLS.MaxStreams > 20 {
		return fmt.Errorf("hls.max_streams must be between 1 and 20, got %d", cfg.HLS.MaxStreams)
	}
	// Validate LL-HLS configuration
	if cfg.HLS.LowLatency {
		if cfg.HLS.SegmentCount < 7 {
			return fmt.Errorf("hls.segment_count must be >= 7 when low_latency is enabled, got %d", cfg.HLS.SegmentCount)
		}
	}
	// Validate hls.part_min_duration
	if partDur, err := time.ParseDuration(cfg.HLS.PartMinDuration); err != nil {
		return fmt.Errorf("hls.part_min_duration invalid: %w", err)
	} else if partDur < 100*time.Millisecond || partDur > 1*time.Second {
		return fmt.Errorf("hls.part_min_duration must be between 100ms and 1s, got %s", cfg.HLS.PartMinDuration)
	}

	// Validate streaming configuration
	if cfg.Streaming.WebRTC.MaxViewers < 1 || cfg.Streaming.WebRTC.MaxViewers > 10 {
		return fmt.Errorf("streaming.webrtc.max_viewers must be between 1 and 10, got %d", cfg.Streaming.WebRTC.MaxViewers)
	}
	if cfg.Streaming.FLV.MaxViewers < 1 || cfg.Streaming.FLV.MaxViewers > 50 {
		return fmt.Errorf("streaming.flv.max_viewers must be between 1 and 50, got %d", cfg.Streaming.FLV.MaxViewers)
	}
	if cfg.Streaming.FLV.GOPCacheSize < 0 {
		return fmt.Errorf("streaming.flv.gop_cache_size must be >= 0, got %d", cfg.Streaming.FLV.GOPCacheSize)
	}
	if _, err := time.ParseDuration(cfg.Streaming.WebRTC.IdleTimeout); err != nil {
		return fmt.Errorf("streaming.webrtc.idle_timeout invalid: %w", err)
	}
	// Validate WebRTC ICE servers (for cross-network access). Empty = LAN-only.
	for i, s := range cfg.Streaming.WebRTC.ICEServers {
		if len(s.URLs) == 0 {
			return fmt.Errorf("streaming.webrtc.ice_servers[%d].urls is required", i)
		}
		for _, u := range s.URLs {
			if !strings.HasPrefix(u, "stun:") && !strings.HasPrefix(u, "turn:") && !strings.HasPrefix(u, "turns:") {
				return fmt.Errorf("streaming.webrtc.ice_servers[%d].urls: %q must start with stun:/turn:/turns:", i, u)
			}
		}
	}
	if _, err := time.ParseDuration(cfg.Streaming.FLV.IdleTimeout); err != nil {
		return fmt.Errorf("streaming.flv.idle_timeout invalid: %w", err)
	}
	// Validate WebSocket configuration
	if cfg.WebSocket.MaxViewers <= 0 {
		return fmt.Errorf("websocket.max_viewers must be > 0, got %d", cfg.WebSocket.MaxViewers)
	}
	if cfg.WebSocket.WriteBufSize <= 0 {
		return fmt.Errorf("websocket.write_buf_size must be > 0, got %d", cfg.WebSocket.WriteBufSize)
	}
	if cfg.WebSocket.IdleTimeout <= 0 {
		return fmt.Errorf("websocket.idle_timeout must be > 0, got %s", cfg.WebSocket.IdleTimeout)
	}

	// Validate SRT configuration
	if cfg.SRT.Port < 1 || cfg.SRT.Port > 65535 {
		return fmt.Errorf("srt.port must be between 1 and 65535, got %d", cfg.SRT.Port)
	}
	for i, s := range cfg.SRT.Streams {
		if strings.TrimSpace(s.CameraID) == "" {
			return fmt.Errorf("srt.streams[%d].camera_id is required", i)
		}
		if s.Mode != "listener" && s.Mode != "caller" {
			return fmt.Errorf("srt.streams[%d].mode must be 'listener' or 'caller', got %q", i, s.Mode)
		}
		if s.Mode == "caller" && strings.TrimSpace(s.Address) == "" {
			return fmt.Errorf("srt.streams[%d].address is required for caller mode", i)
		}
	}

	// Validate GB28181 server configuration
	if cfg.GB28181.Enabled {
		if strings.TrimSpace(cfg.GB28181.ServerID) == "" {
			return fmt.Errorf("gb28181.server_id is required when gb28181.enabled=true")
		}
		if strings.TrimSpace(cfg.GB28181.SIPListen) == "" {
			return fmt.Errorf("gb28181.sip_listen is required when gb28181.enabled=true")
		}
		if strings.TrimSpace(cfg.GB28181.Password) == "" {
			return fmt.Errorf("gb28181.password is required when gb28181.enabled=true")
		}
		if err := validatePortRange(cfg.GB28181.PortRange); err != nil {
			return fmt.Errorf("gb28181.port_range invalid: %w", err)
		}
		switch cfg.GB28181.TCPFraming {
		case "rfc4571", "0x24", "auto":
			// valid
		default:
			return fmt.Errorf("gb28181.tcp_framing must be one of \"rfc4571\", \"0x24\", \"auto\" (got %q)", cfg.GB28181.TCPFraming)
		}
		switch cfg.GB28181.MediaTransport {
		case "", "udp", "tcp-passive", "tcp-active":
			// valid ("" resolves via defaults)
		default:
			return fmt.Errorf("gb28181.media_transport must be one of \"udp\", \"tcp-passive\", \"tcp-active\" (got %q)", cfg.GB28181.MediaTransport)
		}
		switch cfg.GB28181.SIPTransport {
		case "", "udp", "tcp":
			// valid ("" resolves via defaults)
		default:
			return fmt.Errorf("gb28181.sip_transport must be \"udp\" or \"tcp\" (got %q)", cfg.GB28181.SIPTransport)
		}
		if _, err := time.ParseDuration(cfg.GB28181.HeartbeatInterval); err != nil {
			return fmt.Errorf("gb28181.heartbeat_interval invalid duration: %w", err)
		}
		if _, err := time.ParseDuration(cfg.GB28181.CatalogInterval); err != nil {
			return fmt.Errorf("gb28181.catalog_interval invalid duration: %w", err)
		}
		for i, id := range cfg.GB28181.AllowedDeviceIDs {
			if strings.TrimSpace(id) == "" {
				return fmt.Errorf("gb28181.allowed_device_ids[%d] is empty", i)
			}
		}
	}

	// Validate health configuration
	if cfg.Health.Enabled {
		if _, err := time.ParseDuration(cfg.Health.EventsRetention); err != nil {
			return fmt.Errorf("health.events_retention invalid duration: %w", err)
		}
		if _, err := time.ParseDuration(cfg.Health.Alerts.Cooldown); err != nil {
			return fmt.Errorf("health.alerts.cooldown invalid duration: %w", err)
		}
		if _, err := time.ParseDuration(cfg.Health.Layer1.OfflineThreshold); err != nil {
			return fmt.Errorf("health.layer1.offline_threshold invalid duration: %w", err)
		}
		if cfg.Health.Layer2.BitrateChangeThreshold <= 0 || cfg.Health.Layer2.BitrateChangeThreshold > 1 {
			return fmt.Errorf("health.layer2.bitrate_change_threshold must be between 0 and 1")
		}
		if cfg.Health.Layer2.MinFPS < 1 {
			return fmt.Errorf("health.layer2.min_fps must be >= 1")
		}
		if _, err := time.ParseDuration(cfg.Health.Layer2.MaxIDRInterval); err != nil {
			return fmt.Errorf("health.layer2.max_idr_interval invalid duration: %w", err)
		}
		if _, err := time.ParseDuration(cfg.Health.Layer2_5.FreezeTimeout); err != nil {
			return fmt.Errorf("health.layer2_5.freeze_timeout invalid duration: %w", err)
		}
		if cfg.Health.AutoRemediation.Enabled {
			if cfg.Health.AutoRemediation.MaxRestartsPerHour <= 0 {
				return fmt.Errorf("health.auto_remediation.max_restarts_per_hour must be > 0")
			}
			if cfg.Health.AutoRemediation.CooldownMinutes < 1 {
				return fmt.Errorf("health.auto_remediation.cooldown_minutes must be >= 1")
			}
		}
	}

	// AI validation
	if cfg.AI.Enabled {
		if cfg.AI.ConfidenceThreshold < 0 || cfg.AI.ConfidenceThreshold > 1 {
			return fmt.Errorf("ai.confidence_threshold must be between 0 and 1, got %.2f", cfg.AI.ConfidenceThreshold)
		}
		if cfg.AI.FrameSkipRate <= 0 {
			return fmt.Errorf("ai.frame_skip_rate must be > 0, got %d", cfg.AI.FrameSkipRate)
		}
		// Validate enabled_cameras
		for i, camID := range cfg.AI.EnabledCameras {
			if strings.TrimSpace(camID) == "" {
				return fmt.Errorf("ai.enabled_cameras[%d] must be non-empty", i)
			}
		}
		// Validate zones
		for cameraID, zones := range cfg.AI.Zones {
			if strings.TrimSpace(cameraID) == "" {
				return fmt.Errorf("ai.zones: camera ID must not be empty")
			}
			for j, zone := range zones {
				if strings.TrimSpace(zone.Name) == "" {
					return fmt.Errorf("ai.zones[%q][%d].name must not be empty", cameraID, j)
				}
				if len(zone.Points) < 3 {
					return fmt.Errorf("ai.zones[%q][%d] (%q) must have at least 3 points, got %d", cameraID, j, zone.Name, len(zone.Points))
				}
				for k, p := range zone.Points {
					if p[0] < 0 || p[0] > 1 || p[1] < 0 || p[1] > 1 {
						return fmt.Errorf("ai.zones[%q][%d] (%q) point %d coordinates (%.2f, %.2f) outside [0,1] range", cameraID, j, zone.Name, k, p[0], p[1])
					}
				}
			}
		}
	}
	return nil
}

// validatePortRange checks a "start-end" port range string (e.g. "30000-30050").
// Both endpoints must be valid TCP/UDP port numbers and start must be <= end.
func validatePortRange(r string) error {
	parts := strings.SplitN(r, "-", 2)
	if len(parts) != 2 {
		return fmt.Errorf("must be in format start-end (e.g. \"30000-30050\")")
	}
	start, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil {
		return fmt.Errorf("invalid start port %q: %w", parts[0], err)
	}
	end, err := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err != nil {
		return fmt.Errorf("invalid end port %q: %w", parts[1], err)
	}
	if start < 1 || start > 65535 || end < 1 || end > 65535 {
		return fmt.Errorf("ports must be between 1 and 65535, got %d-%d", start, end)
	}
	if start > end {
		return fmt.Errorf("start port %d must be <= end port %d", start, end)
	}
	return nil
}
