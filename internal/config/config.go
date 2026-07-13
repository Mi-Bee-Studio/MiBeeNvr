package config

import (
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/ai"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
)

// Pre-compiled regex patterns for validation (avoids SA6000: regexp.MatchString in loop)
var (
	rePlatformName = regexp.MustCompile(`^[a-zA-Z0-9_]+$`)
	reResolution   = regexp.MustCompile(`^\d+x\d+$`)
	reBitrate      = regexp.MustCompile(`^(0|\d+(\.\d+)?[kMG])$`)
)

type Config struct {
	Server        ServerConfig        `yaml:"server"`
	Storage       StorageConfig       `yaml:"storage"`
	Cameras       []CameraConfig      `yaml:"cameras"`
	Cleanup       CleanupConfig       `yaml:"cleanup"`
	Merge         MergeConfig         `yaml:"merge"`
	Auth          AuthConfig          `yaml:"auth"`
	FTP           FTPConfig           `yaml:"ftp"`
	MQTT          MQTTConfig          `yaml:"mqtt"`
	WebDAV        WebDAVConfig        `yaml:"webdav"`
	HLS           HLSConfig           `yaml:"hls"`
	Streaming     StreamingConfig     `yaml:"streaming"`
	Observability ObservabilityConfig `yaml:"observability"`
	Xiaomi        XiaomiConfig        `yaml:"xiaomi"`
	RTMP          RTMPConfig          `yaml:"rtmp"`
	SRT           SRTConfig           `yaml:"srt"`
	Health        HealthConfig        `yaml:"health"`
	RemoteLog     RemoteLogConfig     `yaml:"remote_log"`
	Transcoding   TranscodingConfig   `yaml:"transcoding"`
	WebSocket     WebSocketConfig     `yaml:"websocket"`
	AI            AIConfig            `yaml:"ai"`
	MetricsAuth   MetricsAuthConfig   `yaml:"metrics_auth"`
	APIKeys       []APIKeyConfig      `yaml:"api_keys,omitempty" json:"api_keys,omitempty"`
	Version       string              `yaml:"version"`
	Timezone      string              `yaml:"timezone"` // display timezone, e.g. "Asia/Shanghai", "America/New_York"; default "UTC"

	// Extensions holds arbitrary key-value pairs for external modules to
	// declare custom configuration sections. MiBeeNvr core does NOT read
	// or validate these — they are a passthrough for out-of-module consumers
	// (e.g. service extensions registered via pkg/app.App.Register).
	// Unknown keys are silently ignored if no consumer reads them.
	Extensions map[string]any `yaml:"extensions,omitempty"`
}

type ServerConfig struct {
	Listen string `yaml:"listen"` // default ":9090"
	// TLSListen enables a second HTTPS listener alongside the plain-HTTP one.
	// Required for browser WebRTC (WHEP) which needs a Secure Context, and for
	// secure WebUI access when not behind a TLS-terminating reverse proxy.
	// Empty = no HTTPS listener (plain HTTP only). e.g. ":9443".
	TLSListen string `yaml:"tls_listen"`
	// CertFile / KeyFile are the TLS certificate and private key paths. Required
	// when TLSListen is set. For production use a real CA-signed cert (e.g. via
	// Caddy/Let's Encrypt or an internal CA); for LAN testing a self-signed cert
	// works (browsers will warn). See deploy/AGENTS.md.
	CertFile string `yaml:"cert_file"`
	KeyFile  string `yaml:"key_file"`
}

type StorageConfig struct {
	RootDir         string `yaml:"root_dir"`         // default "/mnt/data/nvr"
	SegmentDuration string `yaml:"segment_duration"` // default "30s"
}

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

	// Dark frame filtering: skip recording segments that are too dark to be useful
	// (night without IR capability). Only applies to MJPEG/AVI cameras.
	// When enabled, each segment is brightness-checked at close time; dark segments
	// are marked merge_status='dark' and excluded from merge + cleaned up early.
	DarkFrameFilterEnabled bool `yaml:"dark_frame_filter_enabled,omitempty" json:"dark_frame_filter_enabled,omitempty"`
	DarkFrameThreshold     int  `yaml:"dark_frame_threshold,omitempty" json:"dark_frame_threshold,omitempty"` // 0-255, default 15

	// Recording schedule: restrict recording to specific time ranges (e.g. daytime only).
	// When nil or disabled, records 24/7. Uses the same TimeRange/ScheduleConfig
	// pattern as timelapse scheduling.
	RecordingSchedule *ScheduleConfig `yaml:"recording_schedule,omitempty" json:"recording_schedule,omitempty"`

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

type CleanupConfig struct {
	RetentionDays        int    `yaml:"retention_days"`         // default 30
	CheckInterval        string `yaml:"check_interval"`         // default "1h"
	DiskThresholdPercent int    `yaml:"disk_threshold_percent"` // default 95
}

type MergeConfig struct {
	Enabled            bool   `yaml:"enabled"`
	CheckInterval      string `yaml:"check_interval"`
	WindowSize         string `yaml:"window_size"`
	BatchLimit         int    `yaml:"batch_limit"`
	MinSegmentAge      string `yaml:"min_segment_age"`
	MinSegmentsToMerge int    `yaml:"min_segments_to_merge"`

	// Rolling merge (quasi-real-time): event-driven merge on SegmentCompleted.
	// When enabled, each newly-closed segment is merged into a per-camera window
	// bucket within seconds (vs the periodic MergeManager's ~1h latency).
	// Independent of Enabled/CheckInterval — can run alongside the periodic merge.
	RollingEnabled  bool   `yaml:"rolling_enabled" json:"rolling_enabled"`
	RollingDebounce string `yaml:"rolling_debounce" json:"rolling_debounce"` // e.g. "500ms", "2s"
	RollingWindow   string `yaml:"rolling_window" json:"rolling_window"`     // e.g. "1h", "30m"

	// RollingMinDuration is the target minimum duration for merged recordings.
	// Merged files shorter than this are marked merge_quality='short' and can be
	// further consolidated via POST /api/merge/consolidate. Default "5m".
	RollingMinDuration string `yaml:"rolling_min_duration" json:"rolling_min_duration"`
}

type TranscodingConfig struct {
	Enabled          bool   `yaml:"enabled" json:"enabled"`                               // default false
	FFmpegPath       string `yaml:"ffmpeg_path,omitempty" json:"ffmpeg_path"`             // auto-detected or user-specified
	MaxWorkers       int    `yaml:"max_workers,omitempty" json:"max_workers"`             // default 1, max 4
	DownloadURL      string `yaml:"download_url,omitempty" json:"download_url"`           // auto-populated per platform
	JobTimeout       string `yaml:"job_timeout,omitempty" json:"job_timeout"`             // per-job timeout, default "30m", max 4h
	HistoryRetention string `yaml:"history_retention,omitempty" json:"history_retention"` // e.g. "168h" (7d), "720h" (30d), ""=never
}

type CameraTranscodingConfig struct {
	Enabled     bool   `yaml:"enabled" json:"enabled"`                     // default false
	TargetCodec string `yaml:"target_codec,omitempty" json:"target_codec"` // h264, h265
	Preset      string `yaml:"preset,omitempty" json:"preset"`             // ultrafast, faster, medium
	Bitrate     string `yaml:"bitrate,omitempty" json:"bitrate"`           // e.g. "2M"
	CRF         int    `yaml:"crf,omitempty" json:"crf"`                   // 0=default(23/28), 1-51 quality
}

// TimeRange defines a start and end time for timelapse scheduling.
type TimeRange struct {
	Start string `yaml:"start,omitempty" json:"start,omitempty"` // HH:MM format (24h)
	End   string `yaml:"end,omitempty" json:"end,omitempty"`     // HH:MM format (24h)
}

// ScheduleConfig defines when timelapse recording should be active.
type ScheduleConfig struct {
	// TimeRanges specifies the time windows for recording (e.g., 09:00-17:00).
	// Multiple ranges are supported and overlapping ranges are auto-merged.
	TimeRanges []TimeRange `yaml:"time_ranges,omitempty" json:"time_ranges,omitempty"`
	// DaysOfWeek restricts recording to specific days (0=Sunday, 1=Monday, ..., 6=Saturday).
	// Empty or nil means all days.
	DaysOfWeek []int `yaml:"days_of_week,omitempty" json:"days_of_week,omitempty"`
}

type CameraTimelapseConfig struct {
	Enabled        bool            `yaml:"enabled" json:"enabled"`                                     // default false
	Interval       string          `yaml:"interval,omitempty" json:"interval,omitempty"`               // snapshot interval, default "30s", min 1s
	FrameSource    string          `yaml:"frame_source,omitempty" json:"frame_source,omitempty"`       // auto, snapshot, rtsp_keyframe, mjpeg — default auto
	SnapshotURL    string          `yaml:"snapshot_url,omitempty" json:"snapshot_url,omitempty"`       // URL for snapshot source (required when frame_source=snapshot)
	Schedule       *ScheduleConfig `yaml:"schedule,omitempty" json:"schedule,omitempty"`               // nil = 24/7 recording
	Paused         bool            `yaml:"paused" json:"paused"`                                       // pause timelapse recording, default false
	DeleteOriginal bool            `yaml:"delete_original,omitempty" json:"delete_original,omitempty"` // remove original segments after timelapse, default false
	MergeEnabled   *bool           `yaml:"merge_enabled,omitempty" json:"merge_enabled,omitempty"`     // auto-detect (nil=auto)
	MergeMode      string          `yaml:"merge_mode,omitempty" json:"merge_mode,omitempty"`           // auto, mp4, jpeg — default auto
	DailyMerge     *bool           `yaml:"daily_merge,omitempty" json:"daily_merge,omitempty"`         // default true
	MergeDuration  string          `yaml:"merge_duration,omitempty" json:"merge_duration,omitempty"`
	MergeOutputFPS int             `yaml:"merge_output_fps,omitempty" json:"merge_output_fps,omitempty"` // default 30, range 1-60
}

type AuthConfig struct {
	Username     string          `yaml:"username"`
	PasswordHash string          `yaml:"password_hash"`
	Password     string          `yaml:"password"`
	RateLimit    RateLimitConfig `yaml:"rate_limit"`
}

// RateLimitConfig controls auth failure rate limiting.
// When Enabled is false (default), no rate limiting is applied.
type RateLimitConfig struct {
	Enabled       *bool `yaml:"enabled"`        // default false
	MaxFailures   int   `yaml:"max_failures"`   // default 20
	WindowMinutes int   `yaml:"window_minutes"` // default 1
}

type FTPConfig struct {
	Enabled          *bool  `yaml:"enabled"`            // default true
	Port             int    `yaml:"port"`               // default 2121
	PassivePortRange string `yaml:"passive_port_range"` // default "2122-2140"
}

type MQTTConfig struct {
	Enabled  bool   `yaml:"enabled"` // default false
	Broker   string `yaml:"broker"`
	Topic    string `yaml:"topic"`
	ClientID string `yaml:"client_id"`
	Username string `yaml:"username"`
	Password string `yaml:"password"`
}

type WebDAVConfig struct {
	Enabled    *bool  `yaml:"enabled"`     // default true
	PathPrefix string `yaml:"path_prefix"` // default "/dav"
	ReadWrite  bool   `yaml:"read_write"`  // default false
}

// ObservabilityConfig defines observability settings
type ObservabilityConfig struct {
	LogLevel    string `yaml:"log_level"`    // default "info"
	LogFormat   string `yaml:"log_format"`   // default "text"
	EnablePprof bool   `yaml:"enable_pprof"` // default false
}

type HLSConfig struct {
	WriteBufferSize  int    `yaml:"write_buffer_size"`   // async frame buffer per stream (default 100)
	SegmentMaxSizeMB int    `yaml:"segment_max_size_mb"` // HLS segment max size in MB (default 10)
	SegmentCount     int    `yaml:"segment_count"`       // HLS segment count per stream (default 7, range [3,10])
	MaxStreams       int    `yaml:"max_streams"`         // default 4 (RPi constraint)
	LowLatency       bool   `yaml:"low_latency"`         // enable Low-Latency HLS (gohlslib MuxerVariantLowLatency)
	PartMinDuration  string `yaml:"part_min_duration"`   // LL-HLS partial segment duration (default "200ms", range [100ms-1s])
}

// StreamingConfig configures streaming protocol options (WebRTC, FLV, etc.)
type StreamingConfig struct {
	DefaultProtocol string       `yaml:"default_protocol"` // webrtc | flv | hls | ll-hls (default "hls")
	WebRTC          WebRTCConfig `yaml:"webrtc"`
	FLV             FLVConfig    `yaml:"flv"`
}

// WebRTCConfig configures WebRTC WHEP streaming
type WebRTCConfig struct {
	Enabled     *bool  `yaml:"enabled"`      // default true
	MaxViewers  int    `yaml:"max_viewers"`  // default 2, range [1,10]
	IdleTimeout string `yaml:"idle_timeout"` // default "60s"
}

// FLVConfig configures HTTP-FLV streaming
type FLVConfig struct {
	Enabled      *bool  `yaml:"enabled"`        // default true
	MaxViewers   int    `yaml:"max_viewers"`    // default 10, range [1,50]
	IdleTimeout  string `yaml:"idle_timeout"`   // default "60s"
	GOPCacheSize int    `yaml:"gop_cache_size"` // default 1
}

// XiaomiConfig holds Xiaomi cloud authentication settings.
type XiaomiConfig struct {
	UserID string `yaml:"user_id"` // Xiaomi account user ID (from auth response)
	Token  string `yaml:"token"`   // Xiaomi passToken for API access
	Region string `yaml:"region"`  // Region code (e.g. "cn", "sg", "de")
}

// SRTConfig configures the SRT listener for receiving MPEG-TS streams.
type SRTConfig struct {
	Enabled *bool       `yaml:"enabled"` // default false
	Port    int         `yaml:"port"`    // default 9000
	Streams []SRTStream `yaml:"streams"`
}

// SRTStream configures a single SRT stream mapping.
type SRTStream struct {
	CameraID   string `yaml:"camera_id"`
	Mode       string `yaml:"mode"`       // "listener" (receive pushes) or "caller" (pull from remote)
	Address    string `yaml:"address"`    // For caller mode: remote SRT address (e.g. "192.168.1.100:9000")
	Passphrase string `yaml:"passphrase"` // AES encryption passphrase (optional)
	StreamID   string `yaml:"stream_id"`  // SRT stream ID for caller mode (optional)
}

// RTMPConfig configures the RTMP ingest server.
type RTMPConfig struct {
	Enabled    *bool             `yaml:"enabled"`     // default false
	Port       int               `yaml:"port"`        // default 1935
	StreamKeys map[string]string `yaml:"stream_keys"` // camera_id → stream_key
}

// HealthConfig configures the camera health monitoring system.
type HealthConfig struct {
	Enabled         bool                        `yaml:"enabled"`
	EventsRetention string                      `yaml:"events_retention"`
	Alerts          HealthAlertsConfig          `yaml:"alerts"`
	Layer1          HealthLayer1Config          `yaml:"layer1"`
	Layer2          HealthLayer2Config          `yaml:"layer2"`
	Layer2_5        HealthLayer2_5Config        `yaml:"layer2_5"`
	AutoRemediation HealthAutoRemediationConfig `yaml:"auto_remediation"`
	// Rediscovery triggers IP re-discovery (ONVIF unicast scan) when a camera is
	// blacklisted by auto-remediation — i.e. the IP has permanently changed.
	Rediscovery RediscoveryConfig `yaml:"rediscovery"`
}

// RediscoveryConfig controls the IP self-healing engine (internal/rediscovery/).
// When a camera's IP changes (e.g. after an AP reboot across per-subnet DHCP),
// the engine re-discovers the camera by its ONVIF serial number via unicast
// probing (cross-subnet; does NOT rely on multicast WS-Discovery).
type RediscoveryConfig struct {
	// Enabled is a *bool so the feature defaults to ON when unset, but can be
	// explicitly turned off with `rediscovery: { enabled: false }`. Use
	// RediscoveryEnabled() to read the effective value.
	Enabled     *bool `yaml:"enabled"`
	MaxParallel int   `yaml:"max_parallel"` // concurrent unicast probes (default 16, RPi-3B friendly)
	// ProbeTimeout is the per-IP probe timeout (default "2s").
	ProbeTimeout string `yaml:"probe_timeout"`
	// MaxDuration bounds a single full scan (default "30s") so a wide subnet_hints
	// list cannot pin the heal loop forever.
	MaxDuration string `yaml:"max_duration"`
}

// RediscoveryEnabled reports the effective enabled state (defaults to true when
// the pointer is nil, i.e. when the user did not explicitly set it).
func (r RediscoveryConfig) RediscoveryEnabled() bool {
	if r.Enabled == nil {
		return true
	}
	return *r.Enabled
}

type HealthAlertsConfig struct {
	Cooldown string `yaml:"cooldown"`
	MQTT     bool   `yaml:"mqtt"`
}

type HealthLayer1Config struct {
	OfflineThreshold string `yaml:"offline_threshold"`
}

type HealthLayer2Config struct {
	BitrateChangeThreshold float64 `yaml:"bitrate_change_threshold"`
	MinFPS                 int     `yaml:"min_fps"`
	MaxIDRInterval         string  `yaml:"max_idr_interval"`
}

type HealthLayer2_5Config struct {
	FreezeTimeout string `yaml:"freeze_timeout"`
}

type HealthAutoRemediationConfig struct {
	Enabled            bool `yaml:"enabled"`
	MaxRestartsPerHour int  `yaml:"max_restarts_per_hour"`
	CooldownMinutes    int  `yaml:"cooldown_minutes"`
	BlacklistHours     int  `yaml:"blacklist_hours"`
	GlobalMaxPerMin    int  `yaml:"global_max_per_min"`
	// ReconnectingTimeoutMinutes is how long a recorder may stay in the
	// "reconnecting" state before auto-remediation treats it as a dead-end and
	// triggers a hard restart (which can then escalate to blacklist + IP
	// rediscovery). A recorder's own reconnect loop never escalates to
	// StatusError, so without this gate a camera whose IP changed would loop
	// forever and rediscovery would never fire. 0 = use default (10 min).
	ReconnectingTimeoutMinutes int `yaml:"reconnecting_timeout_minutes"`
}

// RemoteLogConfig defines remote log shipping settings (e.g. VictoriaLogs).
type RemoteLogConfig struct {
	Enabled  bool   `yaml:"enabled"`  // default false
	Endpoint string `yaml:"endpoint"` // VictoriaLogs URL, e.g. "http://localhost:9428/insert/jsonline"
	Format   string `yaml:"format"`   // "jsonline" (default) or "loki"
}

// MetricsAuthConfig defines optional independent authentication for the /metrics endpoint.
// When username and password (or password_hash) are non-empty, /metrics requires BasicAuth.
// When empty, /metrics stays public (backward compatible).
type MetricsAuthConfig struct {
	Username     string `yaml:"username"`
	Password     string `yaml:"password"`
	PasswordHash string `yaml:"password_hash"`
}

// APIKeyConfig represents a single API key for MiBeeVision integration.
type APIKeyConfig struct {
	Key     string `yaml:"key" json:"key"`
	Name    string `yaml:"name" json:"name"`
	Revoked bool   `yaml:"revoked,omitempty" json:"revoked,omitempty"`
}
type WebSocketConfig struct {
	MaxViewers   int           `yaml:"max_viewers" json:"maxViewers"`
	WriteBufSize int           `yaml:"write_buf_size" json:"writeBufSize"`
	IdleTimeout  time.Duration `yaml:"idle_timeout" json:"idleTimeout"`
}

type AIConfig struct {
	Enabled             bool                `yaml:"enabled" json:"enabled"`
	EnabledCameras      []string            `yaml:"enabled_cameras" json:"enabledCameras"`
	ModelURL            string              `yaml:"model_url" json:"modelUrl"`
	Zones               map[string][]ai.ROI `yaml:"zones" json:"zones"`
	FrameSkipRate       int                 `yaml:"frame_skip_rate" json:"frameSkipRate"`
	ConfidenceThreshold float64             `yaml:"confidence_threshold" json:"confidenceThreshold"`
}

// IsConfigured returns true if both username and a password (or hash) are set.
func (c MetricsAuthConfig) IsConfigured() bool {
	return strings.TrimSpace(c.Username) != "" &&
		(strings.TrimSpace(c.Password) != "" || strings.TrimSpace(c.PasswordHash) != "")
}

// Load reads a YAML config file and returns a Config with defaults applied.
func Load(path string) (*Config, error) {
	if path == "" {
		return nil, fmt.Errorf("path must be provided")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := yaml.Unmarshal(b, &cfg); err != nil {
		return nil, err
	}
	// apply defaults
	cfg.ApplyDefaults()

	// Decrypt sensitive fields if encryption key is available
	if key := GetEncryptionKey(); key != nil {
		decryptConfig(&cfg, key)
	}

	return &cfg, nil
}

// Save writes the Config to path as YAML using atomic write (temp file + rename).
// If an encryption key is available, sensitive fields are encrypted before writing
// and restored to plaintext in memory after the write completes.
func Save(path string, cfg *Config) error {
	if path == "" {
		return fmt.Errorf("path must be provided")
	}
	if cfg == nil {
		return fmt.Errorf("config is nil")
	}

	// Snapshot and encrypt sensitive fields if key is available
	key := GetEncryptionKey()
	if key != nil {
		snap := snapshotSensitive(cfg)
		encryptConfig(cfg, key)
		defer snap.restore(cfg)
	}

	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	// Temp file in same directory to ensure same filesystem for rename.
	tmp, err := os.CreateTemp(filepath.Dir(path), ".mibee-nvr.yaml.tmp")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("close temp file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("rename temp file: %w", err)
	}
	return nil
}

// Validate ensures required fields and basic constraints.
func Validate(cfg *Config) error {
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
			c.Protocol != string(model.ProtoSRT) && c.Protocol != string(model.ProtoRTMP) {
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
		// Accept both old combined format and new separate format
		proto := c.Protocol
		enc := c.Encoding
		if strings.Contains(proto, "_") {
			// Old combined format like "rtsp_h264" — parse and validate
			parsedProto, parsedEnc, err := model.ParseLegacyProtocol(proto)
			if err != nil {
				return fmt.Errorf("camera[%d].protocol invalid: %s", i, proto)
			}
			proto = parsedProto
			enc = parsedEnc
		}
		if err := model.ValidateProtocolEncoding(proto, enc); err != nil {
			return fmt.Errorf("camera[%d].%w", i, err)
		}

		// Validate IP self-healing fields (stable_id + subnet_hints).
		if strings.TrimSpace(c.StableID) != "" {
			// Loose sanity: limit length to avoid accidental misuse (e.g. pasting a URL).
			if len(c.StableID) > 128 {
				return fmt.Errorf("camera[%d].stable_id is too long (max 128 chars): got %d", i, len(c.StableID))
			}
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
	// Validate segment_duration — clamp to 30s with warning (RPi constraint)
	if dur, err := time.ParseDuration(cfg.Storage.SegmentDuration); err != nil {
		return fmt.Errorf("storage.segment_duration invalid: %w", err)
	} else if dur > 30*time.Second {
		slog.Warn("storage.segment_duration exceeds 30s on RPi 3B, clamping to 30s", "got", cfg.Storage.SegmentDuration)
		cfg.Storage.SegmentDuration = "30s"
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
	if cfg.Streaming.DefaultProtocol != "webrtc" && cfg.Streaming.DefaultProtocol != "flv" && cfg.Streaming.DefaultProtocol != "hls" && cfg.Streaming.DefaultProtocol != "ll-hls" {
		return fmt.Errorf("streaming.default_protocol invalid: %s (must be webrtc/flv/hls/ll-hls)", cfg.Streaming.DefaultProtocol)
	}
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

func (cfg *Config) ApplyDefaults() {
	// Timezone
	if cfg.Timezone == "" {
		cfg.Timezone = "Local"
	}
	// Server
	if strings.TrimSpace(cfg.Server.Listen) == "" {
		cfg.Server.Listen = ":9090"
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
		cfg.Cleanup.DiskThresholdPercent = 95
	}
	// Auth - rate limit defaults
	if cfg.Auth.RateLimit.MaxFailures == 0 {
		cfg.Auth.RateLimit.MaxFailures = 20
	}
	if cfg.Auth.RateLimit.WindowMinutes == 0 {
		cfg.Auth.RateLimit.WindowMinutes = 1
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
	if strings.TrimSpace(cfg.Streaming.DefaultProtocol) == "" {
		cfg.Streaming.DefaultProtocol = "hls"
	}
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

	// Remote log defaults
	if cfg.RemoteLog.Format == "" {
		cfg.RemoteLog.Format = "jsonline"
	}
	// Camera protocol/encoding normalization (backward compat with old combined protocol strings)
	for i := range cfg.Cameras {
		cam := &cfg.Cameras[i]
		// If encoding is empty but protocol looks like old combined format (e.g. "rtsp_h264")
		if cam.Encoding == "" && strings.Contains(cam.Protocol, "_") {
			proto, enc, err := model.ParseLegacyProtocol(cam.Protocol)
			if err == nil {
				cam.Protocol = proto
				cam.Encoding = enc
			}
		}
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
				cam.Timelapse.MergeDuration = "natural-day"
			}
			// MergeEnabled defaults to nil (auto-detect)
		}
	}
}

// ResolveMergeConfig returns the effective MergeConfig for a camera.
// If perCamera is nil, the global config is returned unchanged.
// If perCamera is non-nil, only non-zero fields override the global config.
func ResolveMergeConfig(global MergeConfig, perCamera *MergeConfig) MergeConfig {
	if perCamera == nil {
		return global
	}
	result := global
	if perCamera.Enabled {
		result.Enabled = true
	}
	if perCamera.CheckInterval != "" {
		result.CheckInterval = perCamera.CheckInterval
	}
	if perCamera.WindowSize != "" {
		result.WindowSize = perCamera.WindowSize
	}
	if perCamera.BatchLimit > 0 {
		result.BatchLimit = perCamera.BatchLimit
	}
	if perCamera.MinSegmentAge != "" {
		result.MinSegmentAge = perCamera.MinSegmentAge
	}
	if perCamera.MinSegmentsToMerge > 0 {
		result.MinSegmentsToMerge = perCamera.MinSegmentsToMerge
	}
	// Rolling merge fields: per-camera overrides only when explicitly set.
	if perCamera.RollingEnabled {
		result.RollingEnabled = true
	}
	if perCamera.RollingDebounce != "" {
		result.RollingDebounce = perCamera.RollingDebounce
	}
	if perCamera.RollingWindow != "" {
		result.RollingWindow = perCamera.RollingWindow
	}
	if perCamera.RollingMinDuration != "" {
		result.RollingMinDuration = perCamera.RollingMinDuration
	}
	return result
}

// ResolveTranscodingConfig returns the effective transcoding config for a camera.
// If per-camera config is nil, the global enabled state is used.
// If per-camera config is set, its fields override the global enabled state.
func (c *Config) ResolveTranscodingConfig(cameraID string) *CameraTranscodingConfig {
	result := &CameraTranscodingConfig{
		Enabled: c.Transcoding.Enabled,
	}
	for i := range c.Cameras {
		cam := &c.Cameras[i]
		if cam.ID == cameraID && cam.Transcoding != nil {
			result.Enabled = cam.Transcoding.Enabled
			if cam.Transcoding.TargetCodec != "" {
				result.TargetCodec = cam.Transcoding.TargetCodec
			}
			if cam.Transcoding.Preset != "" {
				result.Preset = cam.Transcoding.Preset
			}
			if cam.Transcoding.Bitrate != "" {
				result.Bitrate = cam.Transcoding.Bitrate
			}
		}
	}
	return result
}

// ResolveHealthOverrides returns the effective health thresholds for a camera.
// Per-camera overrides take precedence over global health config when set.
// Duration strings are left as-is (empty string means "use global").
func ResolveHealthOverrides(global HealthConfig, overrides HealthOverrides) ResolvedHealthOverrides {
	result := ResolvedHealthOverrides{
		MaxIDRInterval:         global.Layer2.MaxIDRInterval,
		BitrateChangeThreshold: global.Layer2.BitrateChangeThreshold,
		MinFPS:                 global.Layer2.MinFPS,
		OfflineThreshold:       global.Layer1.OfflineThreshold,
		FreezeTimeout:          global.Layer2_5.FreezeTimeout,
	}
	if overrides.MaxIDRInterval != "" {
		result.MaxIDRInterval = overrides.MaxIDRInterval
	}
	if overrides.BitrateChangeThreshold > 0 {
		result.BitrateChangeThreshold = overrides.BitrateChangeThreshold
	}
	if overrides.MinFPS > 0 {
		result.MinFPS = overrides.MinFPS
	}
	if overrides.OfflineThreshold != "" {
		result.OfflineThreshold = overrides.OfflineThreshold
	}
	if overrides.FreezeTimeout != "" {
		result.FreezeTimeout = overrides.FreezeTimeout
	}
	return result
}

// ResolvedHealthOverrides holds fully-resolved health threshold values
// (duration strings ready for time.ParseDuration).
type ResolvedHealthOverrides struct {
	MaxIDRInterval         string
	BitrateChangeThreshold float64
	MinFPS                 int
	OfflineThreshold       string
	FreezeTimeout          string
}

// EncryptConfigFile loads a config file, encrypts all sensitive fields, and saves it back.
// Returns the list of field paths that were encrypted.
// Returns an error if no encryption key is available or if the config cannot be loaded/saved.
func EncryptConfigFile(path string) ([]string, error) {
	key := GetEncryptionKey()
	if key == nil {
		return nil, fmt.Errorf("NVR_ENCRYPTION_KEY environment variable not set (must be 32-byte base64-encoded key)")
	}
	cfg, err := Load(path)
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}

	// Find plaintext fields before encryption
	plaintextFields := SensitiveFieldPaths(cfg)
	if len(plaintextFields) == 0 {
		return nil, nil // nothing to encrypt
	}

	slog.Info("encrypting config fields", "path", path, "fields", plaintextFields)

	// Save will encrypt via the snapshot mechanism
	if err := Save(path, cfg); err != nil {
		return nil, fmt.Errorf("save encrypted config: %w", err)
	}

	return plaintextFields, nil
}

// parseHHMM parses a time string in HH:MM format and returns hours and minutes.
func parseHHMM(s string) (hours, minutes int, err error) {
	if len(s) != 5 || s[2] != ':' {
		return 0, 0, fmt.Errorf("invalid time format %q, expected HH:MM", s)
	}
	hours = int(s[0]-'0')*10 + int(s[1]-'0')
	minutes = int(s[3]-'0')*10 + int(s[4]-'0')
	if hours < 0 || hours > 23 || minutes < 0 || minutes > 59 {
		return 0, 0, fmt.Errorf("invalid time %q, hours must be 00-23, minutes 00-59", s)
	}
	return hours, minutes, nil
}

// mergeTimeRanges merges overlapping or adjacent time ranges and returns a non-overlapping sorted list.
func mergeTimeRanges(ranges []TimeRange) []TimeRange {
	if len(ranges) <= 1 {
		return ranges
	}
	// Convert to minutes since midnight for sorting
	type tr struct {
		start, end int
		original   TimeRange
	}
	parsed := make([]tr, len(ranges))
	for i, r := range ranges {
		sh, sm, _ := parseHHMM(r.Start)
		eh, em, _ := parseHHMM(r.End)
		parsed[i] = tr{start: sh*60 + sm, end: eh*60 + em, original: r}
	}
	// Sort by start time
	for i := 0; i < len(parsed); i++ {
		for j := i + 1; j < len(parsed); j++ {
			if parsed[j].start < parsed[i].start {
				parsed[i], parsed[j] = parsed[j], parsed[i]
			}
		}
	}
	// Merge overlapping/adjacent ranges
	merged := []tr{parsed[0]}
	for i := 1; i < len(parsed); i++ {
		last := &merged[len(merged)-1]
		if parsed[i].start <= last.end {
			// Overlapping or adjacent — extend end if needed
			if parsed[i].end > last.end {
				last.end = parsed[i].end
			}
		} else {
			merged = append(merged, parsed[i])
		}
	}
	// Convert back to TimeRanges
	result := make([]TimeRange, len(merged))
	for i, m := range merged {
		result[i] = TimeRange{
			Start: fmt.Sprintf("%02d:%02d", m.start/60, m.start%60),
			End:   fmt.Sprintf("%02d:%02d", m.end/60, m.end%60),
		}
	}
	return result
}

// ParseMergeDuration parses a MergeDuration value and returns the corresponding time.Duration.
// Valid values: "8h", "12h", "24h", "natural-day", "7d", "30d"
// Empty string defaults to "natural-day" (24 hours).
func ParseMergeDuration(s string) (time.Duration, error) {
	switch s {
	case "", "natural-day":
		return 24 * time.Hour, nil
	case "8h":
		return 8 * time.Hour, nil
	case "12h":
		return 12 * time.Hour, nil
	case "24h":
		return 24 * time.Hour, nil
	case "7d":
		return 7 * 24 * time.Hour, nil
	case "30d":
		return 30 * 24 * time.Hour, nil
	default:
		return 0, fmt.Errorf("invalid merge duration %q: must be one of \"8h\", \"12h\", \"24h\", \"natural-day\", \"7d\", \"30d\"", s)
	}
}
