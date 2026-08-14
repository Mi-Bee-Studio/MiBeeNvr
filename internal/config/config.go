package config

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
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
	GB28181       GB28181ServerConfig `yaml:"gb28181"`
	Health        HealthConfig        `yaml:"health"`
	AutoDiscover  AutoDiscoverConfig  `yaml:"auto_discover"`
	RemoteLog     RemoteLogConfig     `yaml:"remote_log"`
	Transcoding   TranscodingConfig   `yaml:"transcoding"`
	WebSocket     WebSocketConfig     `yaml:"websocket"`
	AI            AIConfig            `yaml:"ai"`
	Vision        VisionConfig        `yaml:"vision"`
	MetricsAuth   MetricsAuthConfig   `yaml:"metrics_auth"`
	Security      SecurityConfig      `yaml:"security"`
	Update        UpdateConfig        `yaml:"update"`
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
	return validateConfigDetails(cfg)
}

func (cfg *Config) ApplyDefaults() {
	applyConfigDefaults(cfg)
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
