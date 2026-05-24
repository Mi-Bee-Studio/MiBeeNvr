package config

import (
	"fmt"
	"log/slog"
	"os"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
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
	Streaming StreamingConfig `yaml:"streaming"`
	Observability ObservabilityConfig `yaml:"observability"`
	Xiaomi        XiaomiConfig        `yaml:"xiaomi"`
	RTMP          RTMPConfig         `yaml:"rtmp"`
	SRT           SRTConfig          `yaml:"srt"`
	Health        HealthConfig       `yaml:"health"`
	Version       string              `yaml:"version"`
}

type ServerConfig struct {
	Listen string `yaml:"listen"` // default ":9090"
}

type StorageConfig struct {
	RootDir         string `yaml:"root_dir"`         // default "/mnt/data/nvr"
	SegmentDuration string `yaml:"segment_duration"` // default "30s"
}

type CameraConfig struct {
	ID             string       `yaml:"id"`
	Name           string       `yaml:"name"`
	Protocol       string       `yaml:"protocol"` // rtsp_h264, rtsp_mjpeg, http_jpeg
	Encoding       string       `yaml:"encoding"` // h264, h265, mjpeg, jpeg (independent of protocol)
	URL            string       `yaml:"url"`
	Username       string       `yaml:"username"`
	Password       string       `yaml:"password"`
	ONVIFEndpoint  string       `yaml:"onvif_endpoint"`
	ProfileToken   string       `yaml:"profile_token"`
	StreamEncoding string       `yaml:"stream_encoding"` // H264 or H265, for ONVIF cameras. Empty = auto-detect.
	Enabled        bool         `yaml:"enabled"`
	SubStreamURL   string       `yaml:"sub_stream_url"`
	SnapshotURL    string       `yaml:"snapshot_url"`
	SampleInterval int          `yaml:"sample_interval"`
	HLSMaxFPS      int          `yaml:"hls_max_fps"`
	Merge          *MergeConfig `yaml:"merge"`

	// Xiaomi-specific camera fields (only used when protocol is "xiaomi")
	DID    string `yaml:"did,omitempty"`    // Xiaomi Device ID
	Vendor string `yaml:"vendor,omitempty"` // Transport vendor: "cs2" (default)
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
}

type AuthConfig struct {
	Username     string `yaml:"username"`
	PasswordHash string `yaml:"password_hash"`
	Password     string `yaml:"password"`
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
	DefaultProtocol string      `yaml:"default_protocol"` // webrtc | flv | hls | ll-hls (default "hls")
	WebRTC         WebRTCConfig `yaml:"webrtc"`
	FLV            FLVConfig    `yaml:"flv"`
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
	Enabled *bool          `yaml:"enabled"` // default false
	Port    int            `yaml:"port"`    // default 9000
	Streams []SRTStream    `yaml:"streams"`
}

// SRTStream configures a single SRT stream mapping.
type SRTStream struct {
	CameraID   string `yaml:"camera_id"`
	Mode       string `yaml:"mode"`        // "listener" (receive pushes) or "caller" (pull from remote)
	Address    string `yaml:"address"`     // For caller mode: remote SRT address (e.g. "192.168.1.100:9000")
	Passphrase string `yaml:"passphrase"`  // AES encryption passphrase (optional)
	StreamID   string `yaml:"stream_id"`   // SRT stream ID for caller mode (optional)
}

// RTMPConfig configures the RTMP ingest server.
type RTMPConfig struct {
	Enabled    *bool             `yaml:"enabled"`    // default false
	Port       int               `yaml:"port"`       // default 1935
	StreamKeys map[string]string `yaml:"stream_keys"` // camera_id → stream_key
}

// HealthConfig configures the camera health monitoring system.
type HealthConfig struct {
	Enabled         bool                `yaml:"enabled"`
	EventsRetention string              `yaml:"events_retention"`
	Alerts          HealthAlertsConfig  `yaml:"alerts"`
	Layer1          HealthLayer1Config  `yaml:"layer1"`
	Layer2          HealthLayer2Config  `yaml:"layer2"`
	Layer2_5        HealthLayer2_5Config `yaml:"layer2_5"`
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
	cfg.applyDefaults()

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
		if strings.TrimSpace(c.URL) == "" && c.Protocol != "onvif" && c.Protocol != "xiaomi" {
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
	}
	// Validate Xiaomi configuration
	for _, cam := range cfg.Cameras {
		if cam.Protocol == "xiaomi" && strings.TrimSpace(cfg.Xiaomi.Token) == "" {
			return fmt.Errorf("xiaomi camera %q requires xiaomi.token in config", cam.ID)
		}
	}
	// port ranges
	if cfg.FTP.Port < 1 || cfg.FTP.Port > 65535 {
		return fmt.Errorf("ftp port out of range: %d", cfg.FTP.Port)
	}
	// Validate segment_duration
	if dur, err := time.ParseDuration(cfg.Storage.SegmentDuration); err != nil {
		return fmt.Errorf("storage.segment_duration invalid: %w", err)
	} else if dur > 30*time.Second {
		return fmt.Errorf("storage.segment_duration must be <= 30s on RPi 3B, got %s", cfg.Storage.SegmentDuration)
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
	}
	return nil
}

func (cfg *Config) applyDefaults() {
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
	// Cameras: nothing heavy, but ensure at least enable false
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
	// Auth - no defaults
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
		cfg.Health.Layer2.MaxIDRInterval = "30s"
	}
	if cfg.Health.Layer2_5.FreezeTimeout == "" {
		cfg.Health.Layer2_5.FreezeTimeout = "10s"
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
			}
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
	return result
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
