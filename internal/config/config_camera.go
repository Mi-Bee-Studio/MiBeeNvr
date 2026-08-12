package config

// Package-level camera configuration types. See config.go for the
// top-level Config aggregate and Validate/ApplyDefaults entry points.

type CameraConfig struct {
	ID                   string                   `yaml:"id"`
	Name                 string                   `yaml:"name"`
	Protocol             string                   `yaml:"protocol"` // rtsp_h264, rtsp_mjpeg, http_jpeg
	Encoding             string                   `yaml:"encoding"` // h264, h265, mjpeg, jpeg (independent of protocol)
	URL                  string                   `yaml:"url"`
	Username             string                   `yaml:"username"`
	Password             string                   `yaml:"password"`
	ONVIFEndpoint        string                   `yaml:"onvif_endpoint"`
	ProfileToken         string                   `yaml:"profile_token"`
	StreamEncoding       string                   `yaml:"stream_encoding"` // H264 or H265, for ONVIF cameras. Empty = auto-detect.
	SubStreamURL         string                   `yaml:"sub_stream_url"`
	SnapshotURL          string                   `yaml:"snapshot_url"`
	SampleInterval       int                      `yaml:"sample_interval"`
	HLSMaxFPS            int                      `yaml:"hls_max_fps"`
	Merge                *MergeConfig             `yaml:"merge"`
	Transcoding          *CameraTranscodingConfig `yaml:"transcoding,omitempty"`
	Timelapse            *CameraTimelapseConfig   `yaml:"timelapse,omitempty" json:"timelapse,omitempty"`
	AudioEnabled         bool                     `yaml:"audio_enabled"`
	HealthOverrides      HealthOverrides          `yaml:"health_overrides,omitempty"`
	FrameWatchdogTimeout string                   `yaml:"frame_watchdog_timeout,omitempty"` // default "30s" (per-camera frame watchdog)
	HTTPJPEGAVI          bool                     `yaml:"http_jpeg_avi"`                    // write AVI single-file instead of MJPEG directory

	// StableID is a hardware-level stable identifier (ONVIF serial number) used to
	// re-acquire the SAME camera after its IP changes (e.g. after an AP reboot when
	// cameras roam across subnets with per-subnet DHCP). Empty = IP self-healing
	// disabled. ONVIF cameras auto-populate this on first successful connection.
	// See internal/rediscovery/ for the re-discovery engine.
	StableID string `yaml:"stable_id,omitempty" json:"stable_id,omitempty"`
	// SubnetHints are candidate CIDRs (e.g. "192.168.63.0/24") where the camera may
	// appear after roaming. The re-discovery scanner probes these in addition to the
	// last-known host and the NVR's own interface subnets. Empty = scan last-known +
	// local subnets only.
	SubnetHints []string `yaml:"subnet_hints,omitempty" json:"subnet_hints,omitempty"`

	// Xiaomi-specific camera fields (only used when protocol is "xiaomi")
	DID     string `yaml:"did,omitempty"`     // Xiaomi Device ID
	Vendor  string `yaml:"vendor,omitempty"`  // Transport vendor: "cs2" (default)
	Channel string `yaml:"channel,omitempty"` // Xiaomi dual-lens channel ("" or "0" = main, "1" = secondary)
	Quality string `yaml:"quality,omitempty"` // Xiaomi stream quality: "" or "auto" (HD→SD fallback), "hd", "sd"

	// Push/ingest camera fields (only used when protocol is "srt" or "rtmp").
	// For these cameras the publisher connects TO the NVR; the URL field is
	// not used. RTMP uses StreamKey (the last path segment of rtmp://host/live/{key}).
	// SRT uses SRTPassphrase (AES encryption) and SRTStreamID (the streamid query).
	StreamKey     string `yaml:"stream_key,omitempty" json:"stream_key,omitempty"`
	SRTPassphrase string `yaml:"srt_passphrase,omitempty" json:"srt_passphrase,omitempty"`
	SRTStreamID   string `yaml:"srt_stream_id,omitempty" json:"srt_stream_id,omitempty"`

	// GB28181-specific camera fields (only used when protocol is "gb28181").
	// The camera is SIP-pull: the NVR INVITEs the channel by device/channel GB code
	// and ingests its RTP/PS stream. URL is not used.
	GB28181 GB28181ChannelConfig `yaml:"gb28181,omitempty" json:"gb28181,omitempty"`

	// Dark frame filtering: skip recording segments that are too dark to be useful
	// (night without IR capability). Only applies to MJPEG/AVI cameras.
	// When enabled, each segment is brightness-checked at close time; dark segments
	// are marked merge_status='dark' and excluded from merge + cleaned up early.
	DarkFrameFilterEnabled bool `yaml:"dark_frame_filter_enabled,omitempty" json:"dark_frame_filter_enabled,omitempty"`
	DarkFrameThreshold     int  `yaml:"dark_frame_threshold,omitempty" json:"dark_frame_threshold,omitempty"` // 0-255, default 15

	// RecordingEnabled gates whether this camera writes segments to disk.
	// nil or true (default) = record normally. false = "live-only" mode: the
	// recorder stays connected and feeds the StreamHub (live preview, relay,
	// health all work) but writes NO segments — useful when the NVR is used
	// purely as a live/relay gateway and SD-card writes must be avoided.
	// Issue #36.
	RecordingEnabled *bool `yaml:"recording_enabled,omitempty" json:"recording_enabled,omitempty"`

	// Recording schedule: restrict recording to specific time ranges (e.g. daytime only).
	// When nil or disabled, records 24/7. Uses the same TimeRange/ScheduleConfig
	// pattern as timelapse scheduling.
	RecordingSchedule *ScheduleConfig `yaml:"recording_schedule,omitempty" json:"recording_schedule,omitempty"`

	// ActivationState gates recorder startup. "" or "active" (default) = the camera
	// connects and records normally. "pending_activation" = the camera is persisted
	// (DB + config) and visible in the UI, but its recorder is NOT started and no
	// connection attempts are made — used by auto-discover for ONVIF devices that
	// require credentials the NVR does not yet have. The user supplies credentials
	// via the "activate" action, which flips this to "active" and starts the recorder.
	// See internal/autodiscover/ for the discovery engine.
	ActivationState string `yaml:"activation_state,omitempty" json:"activation_state,omitempty"`

	// Push-out targets (relay): forward this camera's live stream to remote
	// destinations (another NVR's RTMP/SRT ingest, a live platform, a backup).
	// Applies to ANY camera protocol — the engine subscribes to the camera's
	// StreamHub, so no re-pull happens. Each entry is one independent target.
	PushTargets []PushTargetConfig `yaml:"push_targets,omitempty" json:"push_targets,omitempty"`
	// Per-camera push-in retention override. nil = follow global retention,
	// 0 = live-only (no recording), N = keep N days. Only meaningful for srt/rtmp.
	PushRetentionDays *int `yaml:"push_retention_days,omitempty" json:"push_retention_days,omitempty"`
}

// PushTargetConfig defines one push-out (relay) destination for a camera.
type PushTargetConfig struct {
	ID                  string                `yaml:"id" json:"id"` // stable id within the camera (kebab/uuid)
	Name                string                `yaml:"name,omitempty" json:"name,omitempty"`
	Protocol            string                `yaml:"protocol" json:"protocol"` // "rtmp" or "rtsp"
	URL                 string                `yaml:"url" json:"url"`           // rtmp://host[:port]/app/key | rtsp://host[:port]/path
	Enabled             bool                  `yaml:"enabled" json:"enabled"`
	Platform            string                `yaml:"platform,omitempty" json:"platform,omitempty"`                 // preset name (bilibili/douyin/youtube/kuaishou/generic/empty)
	TranscodePolicy     string                `yaml:"transcode_policy,omitempty" json:"transcode_policy,omitempty"` // auto/force_sw/off
	VideoPresetOverride *VideoPresetOverrides `yaml:"video_preset_override,omitempty" json:"video_preset_override,omitempty"`
	SourceURL           string                `yaml:"source_url,omitempty" json:"source_url,omitempty"` // optional: if set, relay uses FFmpeg to pull from this URL instead of hub
	UseFFmpeg           bool                  `yaml:"use_ffmpeg,omitempty" json:"use_ffmpeg,omitempty"` // if true, use FFmpeg subprocess for relay (compatibility mode)
}

// VideoPresetOverrides allows overriding individual encoding parameters
// for a push target. Only non-zero/non-empty fields are applied.
type VideoPresetOverrides struct {
	Resolution       string `yaml:"resolution,omitempty" json:"resolution,omitempty"`
	Framerate        int    `yaml:"framerate,omitempty" json:"framerate,omitempty"`
	VideoBitrateKbps int    `yaml:"video_bitrate_kbps,omitempty" json:"video_bitrate_kbps,omitempty"`
	GopSeconds       int    `yaml:"gop_seconds,omitempty" json:"gop_seconds,omitempty"`
	Profile          string `yaml:"profile,omitempty" json:"profile,omitempty"`
	Bframes          int    `yaml:"bframes,omitempty" json:"bframes,omitempty"`
}

// HealthOverrides allows per-camera health monitoring threshold overrides.
// When set, non-zero values take precedence over global health config.
type HealthOverrides struct {
	MaxIDRInterval         string  `yaml:"max_idr_interval,omitempty"`
	BitrateChangeThreshold float64 `yaml:"bitrate_change_threshold,omitempty"`
	MinFPS                 int     `yaml:"min_fps,omitempty"`
	OfflineThreshold       string  `yaml:"offline_threshold,omitempty"`
	FreezeTimeout          string  `yaml:"freeze_timeout,omitempty"`
}

// XiaomiConfig holds Xiaomi cloud authentication settings.
type XiaomiConfig struct {
	UserID string `yaml:"user_id"` // Xiaomi account user ID (from auth response)
	Token  string `yaml:"token"`   // Xiaomi passToken for API access
	Region string `yaml:"region"`  // Region code (e.g. "cn", "sg", "de")
}
