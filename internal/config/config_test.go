package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoadValidConfig(t *testing.T) {
    path := filepath.Join("..", "..", "config.example.yaml")
    cfg, err := Load(path)
    // it's okay if example has minimal; just ensure no error
    require.NoError(t, err)
    require.NotNil(t, cfg)
}

func TestValidateMissingCameraID(t *testing.T) {
    cfg := &Config{Cameras: []CameraConfig{{ID: "", URL: "rtsp://x"}}}
    cfg.applyDefaults()
    err := Validate(cfg)
    require.Error(t, err)
}

func TestValidateInvalidProtocol(t *testing.T) {
    cfg := &Config{Cameras: []CameraConfig{{ID: "c1", URL: "rtsp://a", Protocol: "invalid"}}}
    cfg.applyDefaults()
    err := Validate(cfg)
    require.Error(t, err)
}

func TestPortRangeValidation(t *testing.T) {
    cfg := &Config{FTP: FTPConfig{Port: 70000}}
    cfg.applyDefaults()
    err := Validate(cfg)
    require.Error(t, err)
}

func TestDefaultsApplied(t *testing.T) {
    cfg := &Config{}
    cfg.applyDefaults()
    require.Equal(t, ":9090", cfg.Server.Listen)
    require.Equal(t, "/var/lib/mibee-nvr", cfg.Storage.RootDir)
    require.Equal(t, "30s", cfg.Storage.SegmentDuration)
    require.Equal(t, 30, cfg.Cleanup.RetentionDays)
    require.Equal(t, "1h", cfg.Cleanup.CheckInterval)
    require.Equal(t, 95, cfg.Cleanup.DiskThresholdPercent)
    require.Equal(t, 2121, cfg.FTP.Port)
    require.Equal(t, true, *cfg.FTP.Enabled)
    require.Equal(t, true, *cfg.WebDAV.Enabled)
    require.Equal(t, "/dav", cfg.WebDAV.PathPrefix)
}

func TestLoadNonExistentFile(t *testing.T) {
    _, err := Load("no_such_file.yaml")
    require.Error(t, err)
}

func TestFTPExplicitlyDisabled(t *testing.T) {
    cfg := &Config{FTP: FTPConfig{Enabled: new(bool)}}
    *cfg.FTP.Enabled = false // explicitly set to false
    cfg.applyDefaults()
    require.NotNil(t, cfg.FTP.Enabled)
    require.Equal(t, false, *cfg.FTP.Enabled) // should remain false
}

func TestWebDAVExplicitlyDisabled(t *testing.T) {
    cfg := &Config{WebDAV: WebDAVConfig{Enabled: new(bool)}}
    *cfg.WebDAV.Enabled = false // explicitly set to false
    cfg.applyDefaults()
    require.NotNil(t, cfg.WebDAV.Enabled)
    require.Equal(t, false, *cfg.WebDAV.Enabled) // should remain false
}

func TestFTPNotConfigured(t *testing.T) {
    cfg := &Config{}
    cfg.applyDefaults()
    require.NotNil(t, cfg.FTP.Enabled)
    require.Equal(t, true, *cfg.FTP.Enabled) // should default to true
}

func TestWebDAVNotConfigured(t *testing.T) {
    cfg := &Config{}
    cfg.applyDefaults()
    require.NotNil(t, cfg.WebDAV.Enabled)
    require.Equal(t, true, *cfg.WebDAV.Enabled) // should default to true
}

func TestSave(t *testing.T) {
    dir := t.TempDir()
    path := filepath.Join(dir, "mibee-nvr.yaml")

    ftpEnabled := true
    webdavEnabled := false
    original := &Config{
        Server:  ServerConfig{Listen: ":8080"},
        Storage: StorageConfig{RootDir: "/data/rec", SegmentDuration: "5m"},
        Cameras: []CameraConfig{{
            ID: "cam1", Name: "Front", Protocol: "rtsp", Encoding: "h264",
            URL: "rtsp://192.168.1.10/stream", Username: "admin", Password: "secret", Enabled: true,
        }},
        Cleanup: CleanupConfig{RetentionDays: 7, CheckInterval: "30m", DiskThresholdPercent: 80},
        Auth:    AuthConfig{Username: "admin", PasswordHash: "$2a$10$xxx"},
        FTP:     FTPConfig{Enabled: &ftpEnabled, Port: 2121, PassivePortRange: "3000-3010"},
        MQTT:    MQTTConfig{Enabled: true, Broker: "tcp://mqtt.local:1883", Topic: "nvr/trigger", ClientID: "mibee"},
        WebDAV:  WebDAVConfig{Enabled: &webdavEnabled, PathPrefix: "/files"},
    }
    original.applyDefaults()

    err := Save(path, original)
    require.NoError(t, err)

    loaded, err := Load(path)
    require.NoError(t, err)
    require.Equal(t, ":8080", loaded.Server.Listen)
    require.Equal(t, "/data/rec", loaded.Storage.RootDir)
    require.Equal(t, "5m", loaded.Storage.SegmentDuration)
    require.Len(t, loaded.Cameras, 1)
    require.Equal(t, "cam1", loaded.Cameras[0].ID)
    require.Equal(t, "Front", loaded.Cameras[0].Name)
    require.Equal(t, "rtsp", loaded.Cameras[0].Protocol)
    require.Equal(t, "rtsp://192.168.1.10/stream", loaded.Cameras[0].URL)
    require.Equal(t, "admin", loaded.Cameras[0].Username)
    require.Equal(t, "secret", loaded.Cameras[0].Password)
    require.True(t, loaded.Cameras[0].Enabled)
    require.Equal(t, 7, loaded.Cleanup.RetentionDays)
    require.Equal(t, "30m", loaded.Cleanup.CheckInterval)
    require.Equal(t, 80, loaded.Cleanup.DiskThresholdPercent)
    require.Equal(t, "admin", loaded.Auth.Username)
    require.Equal(t, "$2a$10$xxx", loaded.Auth.PasswordHash)
    require.Equal(t, 2121, loaded.FTP.Port)
    require.Equal(t, "3000-3010", loaded.FTP.PassivePortRange)
    require.True(t, *loaded.FTP.Enabled)
    require.True(t, loaded.MQTT.Enabled)
    require.Equal(t, "tcp://mqtt.local:1883", loaded.MQTT.Broker)
    require.Equal(t, "nvr/trigger", loaded.MQTT.Topic)
    require.Equal(t, "mibee", loaded.MQTT.ClientID)
    require.NotNil(t, loaded.WebDAV.Enabled)
    require.False(t, *loaded.WebDAV.Enabled)
    require.Equal(t, "/files", loaded.WebDAV.PathPrefix)
}

func TestSaveAtomic(t *testing.T) {
    dir := t.TempDir()
    path := filepath.Join(dir, "subdir", "mibee-nvr.yaml")
    require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))

    cfg := &Config{Server: ServerConfig{Listen: ":9090"}}
    cfg.applyDefaults()

    err := Save(path, cfg)
    require.NoError(t, err)

    // Make directory read-only so a second Save should fail
    require.NoError(t, os.Chmod(filepath.Dir(path), 0o555))
    defer os.Chmod(filepath.Dir(path), 0o755) // restore for cleanup

    // Read the original content before failed write attempt
    original, err := os.ReadFile(path)
    require.NoError(t, err)

    err = Save(path, &Config{Server: ServerConfig{Listen: ":0000"}})
    require.Error(t, err)

    // Verify original file is untouched
    after, err := os.ReadFile(path)
    require.NoError(t, err)
    require.Equal(t, string(original), string(after))
}

func TestSaveOverwrite(t *testing.T) {
    dir := t.TempDir()
    path := filepath.Join(dir, "mibee-nvr.yaml")

    first := &Config{Server: ServerConfig{Listen: ":7070"}, Storage: StorageConfig{RootDir: "/old"}}
    first.applyDefaults()
    require.NoError(t, Save(path, first))

    second := &Config{Server: ServerConfig{Listen: ":3333"}, Storage: StorageConfig{RootDir: "/new"}}
    second.applyDefaults()
    require.NoError(t, Save(path, second))

    loaded, err := Load(path)
    require.NoError(t, err)
    require.Equal(t, ":3333", loaded.Server.Listen)
    require.Equal(t, "/new", loaded.Storage.RootDir)
}
func TestValidateOnvifProtocol(t *testing.T) {
	cfg := &Config{Cameras: []CameraConfig{{ID: "c1", ONVIFEndpoint: "http://192.168.1.100/onvif/device_service", Protocol: "onvif"}}}
	cfg.applyDefaults()
	err := Validate(cfg)
	require.NoError(t, err)
}

func TestResolveMergeConfig_NilReturnsGlobal(t *testing.T) {
	global := MergeConfig{
		Enabled:            true,
		CheckInterval:      "1h",
		WindowSize:         "1h",
		BatchLimit:         200,
		MinSegmentAge:      "10m",
		MinSegmentsToMerge: 3,
	}
	result := ResolveMergeConfig(global, nil)
	require.Equal(t, global, result)
}

func TestResolveMergeConfig_OverridesNonZeroFields(t *testing.T) {
	global := MergeConfig{
		Enabled:            true,
		CheckInterval:      "1h",
		WindowSize:         "1h",
		BatchLimit:         200,
		MinSegmentAge:      "10m",
		MinSegmentsToMerge: 3,
	}
	perCamera := &MergeConfig{
		CheckInterval:      "30m",
		BatchLimit:         50,
	}
	result := ResolveMergeConfig(global, perCamera)
	// Enabled stays true (global)
	require.True(t, result.Enabled)
	// Overridden fields
	require.Equal(t, "30m", result.CheckInterval)
	require.Equal(t, 50, result.BatchLimit)
	// Non-overridden fields stay global
	require.Equal(t, "1h", result.WindowSize)
	require.Equal(t, "10m", result.MinSegmentAge)
	require.Equal(t, 3, result.MinSegmentsToMerge)
}

func TestResolveMergeConfig_AllFieldsOverridden(t *testing.T) {
	global := MergeConfig{
		Enabled:            true,
		CheckInterval:      "1h",
		WindowSize:         "1h",
		BatchLimit:         200,
		MinSegmentAge:      "10m",
		MinSegmentsToMerge: 3,
	}
	perCamera := &MergeConfig{
		Enabled:            false,
		CheckInterval:      "5m",
		WindowSize:         "30m",
		BatchLimit:         10,
		MinSegmentAge:      "2m",
		MinSegmentsToMerge: 2,
	}
	result := ResolveMergeConfig(global, perCamera)
	require.True(t, result.Enabled) // perCamera.Enabled=false is not >0/!="", so global stays
	require.Equal(t, "5m", result.CheckInterval)
	require.Equal(t, "30m", result.WindowSize)
	require.Equal(t, 10, result.BatchLimit)
	require.Equal(t, "2m", result.MinSegmentAge)
	require.Equal(t, 2, result.MinSegmentsToMerge)
}
