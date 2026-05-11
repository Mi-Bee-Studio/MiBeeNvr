package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
)

type Config struct {
	Server      ServerConfig      `yaml:"server"`
	Storage     StorageConfig     `yaml:"storage"`
	Cameras     []CameraConfig    `yaml:"cameras"`
	Cleanup     CleanupConfig     `yaml:"cleanup"`
	Merge       MergeConfig        `yaml:"merge"`
	Auth        AuthConfig        `yaml:"auth"`
	FTP         FTPConfig         `yaml:"ftp"`
	MQTT        MQTTConfig        `yaml:"mqtt"`
	WebDAV      WebDAVConfig      `yaml:"webdav"`
	HLS         HLSConfig         `yaml:"hls"`
	Observability ObservabilityConfig `yaml:"observability"`
	Version     string            `yaml:"version"`
}

type ServerConfig struct {
	Listen string `yaml:"listen"` // default ":9090"
}

type StorageConfig struct {
	RootDir         string `yaml:"root_dir"`        // default "/mnt/data/nvr"
	SegmentDuration string `yaml:"segment_duration"` // default "30s"
}

type CameraConfig struct {
	ID       string `yaml:"id"`
	Name     string `yaml:"name"`
	Protocol string `yaml:"protocol"` // rtsp_h264, rtsp_mjpeg, http_jpeg
	Encoding       string `yaml:"encoding"` // h264, h265, mjpeg, jpeg (independent of protocol)
	URL      string `yaml:"url"`
	Username string `yaml:"username"`
	Password string `yaml:"password"`
	ONVIFEndpoint  string `yaml:"onvif_endpoint"`
	ProfileToken   string `yaml:"profile_token"`
	StreamEncoding string `yaml:"stream_encoding"` // H264 or H265, for ONVIF cameras. Empty = auto-detect.
	Enabled  bool   `yaml:"enabled"`
	SubStreamURL   string `yaml:"sub_stream_url"`
	SnapshotURL    string `yaml:"snapshot_url"`
	SampleInterval int    `yaml:"sample_interval"`
	HLSMaxFPS      int    `yaml:"hls_max_fps"`
	Merge         *MergeConfig `yaml:"merge"`
}

type CleanupConfig struct {
	RetentionDays       int    `yaml:"retention_days"`        // default 30
	CheckInterval       string `yaml:"check_interval"`         // default "1h"
	DiskThresholdPercent int   `yaml:"disk_threshold_percent"` // default 95
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
	Enabled          *bool  `yaml:"enabled"`           // default true
	Port             int    `yaml:"port"`              // default 2121
	PassivePortRange string `yaml:"passive_port_range"` // default "2122-2140"
}

type MQTTConfig struct {
	Enabled  bool   `yaml:"enabled"`   // default false
	Broker   string `yaml:"broker"`
	Topic    string `yaml:"topic"`
	ClientID string `yaml:"client_id"`
}

type WebDAVConfig struct {
	Enabled    *bool  `yaml:"enabled"`     // default true
	PathPrefix string `yaml:"path_prefix"` // default "/dav"
	ReadWrite  bool   `yaml:"read_write"`  // default false
}


// ObservabilityConfig defines observability settings
type ObservabilityConfig struct {
	LogLevel     string `yaml:"log_level"`     // default "info"
	LogFormat    string `yaml:"log_format"`    // default "text"
	EnablePprof  bool   `yaml:"enable_pprof"`  // default false
}

type HLSConfig struct {
	WriteBufferSize  int `yaml:"write_buffer_size"`   // async frame buffer per stream (default 40)
	SegmentMaxSizeMB int `yaml:"segment_max_size_mb"` // HLS segment max size in MB (default 10)
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
	return &cfg, nil
}

// Save writes the Config to path as YAML using atomic write (temp file + rename).
func Save(path string, cfg *Config) error {
	if path == "" {
		return fmt.Errorf("path must be provided")
	}
	if cfg == nil {
		return fmt.Errorf("config is nil")
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
	for i, c := range cfg.Cameras {
		if strings.TrimSpace(c.ID) == "" {
			return fmt.Errorf("camera[%d].id is required", i)
		}
		if strings.TrimSpace(c.URL) == "" && c.Protocol != "onvif" {
			return fmt.Errorf("camera[%d].url is required", i)
		}
		if (c.Protocol == "onvif" || c.Protocol == string(model.ProtoONVIF)) && strings.TrimSpace(c.ONVIFEndpoint) == "" && strings.TrimSpace(c.URL) == "" {
			return fmt.Errorf("camera[%d].url or onvif_endpoint is required for ONVIF cameras", i)
		}
		// Auto-populate: if url is set but onvif_endpoint is empty, copy url to onvif_endpoint
		if (c.Protocol == "onvif" || c.Protocol == string(model.ProtoONVIF)) && strings.TrimSpace(c.ONVIFEndpoint) == "" && strings.TrimSpace(c.URL) != "" {
			c.ONVIFEndpoint = c.URL
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
	// port ranges
	if cfg.FTP.Port < 0 || cfg.FTP.Port > 65535 {
		return fmt.Errorf("ftp port out of range: %d", cfg.FTP.Port)
	}
	// Validate segment_duration
	if dur, err := time.ParseDuration(cfg.Storage.SegmentDuration); err != nil {
		return fmt.Errorf("storage.segment_duration invalid: %w", err)
	} else if dur > 5*time.Minute {
		return fmt.Errorf("storage.segment_duration must be <= 5m on RPi 3B, got %s", cfg.Storage.SegmentDuration)
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
	return nil
}

func (cfg *Config) applyDefaults() {
	// Server
	if strings.TrimSpace(cfg.Server.Listen) == "" {
		cfg.Server.Listen = ":9090"
	}
	// Storage
	if strings.TrimSpace(cfg.Storage.RootDir) == "" {
		cfg.Storage.RootDir = "/mnt/data/nvr"
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
		cfg.HLS.WriteBufferSize = 40
	}
	if cfg.HLS.SegmentMaxSizeMB <= 0 {
		cfg.HLS.SegmentMaxSizeMB = 10
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