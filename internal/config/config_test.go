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
	        MQTT:    MQTTConfig{Enabled: true, Broker: "tcp://mqtt.local:1883", Topic: "nvr/trigger", ClientID: "mibee", Username: "mqttuser", Password: "mqttpass"},
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
	require.Equal(t, "mqttuser", loaded.MQTT.Username)
	require.Equal(t, "mqttpass", loaded.MQTT.Password)
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

func TestHLSSegmentCountDefault(t *testing.T) {
	cfg := &Config{}
	cfg.applyDefaults()
	require.Equal(t, 7, cfg.HLS.SegmentCount)
	require.Equal(t, 100, cfg.HLS.WriteBufferSize)
}

func TestHLSSegmentCountValidation_Valid(t *testing.T) {
	for _, sc := range []int{3, 5, 7, 10} {
		cfg := &Config{HLS: HLSConfig{SegmentCount: sc}}
		cfg.applyDefaults()
		err := Validate(cfg)
		require.NoError(t, err, "segment_count=%d should be valid", sc)
	}
}

func TestHLSSegmentCountValidation_TooLow(t *testing.T) {
	cfg := &Config{HLS: HLSConfig{SegmentCount: 2}}
	cfg.applyDefaults()
	err := Validate(cfg)
	require.Error(t, err)
	require.Contains(t, err.Error(), "hls.segment_count")
}

func TestHLSSegmentCountValidation_TooHigh(t *testing.T) {
	cfg := &Config{HLS: HLSConfig{SegmentCount: 11}}
	cfg.applyDefaults()
	err := Validate(cfg)
	require.Error(t, err)
	require.Contains(t, err.Error(), "hls.segment_count")
}

func TestXiaomiConfigDefaults(t *testing.T) {
	cfg := &Config{}
	cfg.applyDefaults()
	require.Equal(t, "cn", cfg.Xiaomi.Region)
}

func TestXiaomiConfigValidationRequiresToken(t *testing.T) {
	cfg := &Config{Cameras: []CameraConfig{{ID: "c1", Protocol: "xiaomi", Encoding: "h264", URL: "xiaomi://device"}}}
	cfg.applyDefaults()
	err := Validate(cfg)
	require.Error(t, err)
	require.Contains(t, err.Error(), "xiaomi.token")
}

func TestXiaomiConfigValidationWithToken(t *testing.T) {
	cfg := &Config{
		Cameras: []CameraConfig{{ID: "c1", Protocol: "xiaomi", Encoding: "h264", URL: "xiaomi://device"}},
		Xiaomi:  XiaomiConfig{Token: "test-token", Region: "cn"},
	}
	cfg.applyDefaults()
	err := Validate(cfg)
	require.NoError(t, err)
}

func TestCameraConfigXiaomiFields(t *testing.T) {
	cfg := &Config{
		Cameras: []CameraConfig{{
			ID:       "c1",
			Protocol: "xiaomi",
			Encoding: "h264",
			URL:      "xiaomi://device",
			DID:      "12345678",
			Vendor:   "cs2",
		}},
		Xiaomi: XiaomiConfig{Token: "test-token", Region: "cn"},
	}
	cfg.applyDefaults()
	err := Validate(cfg)
	require.NoError(t, err)
	require.Equal(t, "12345678", cfg.Cameras[0].DID)
	require.Equal(t, "cs2", cfg.Cameras[0].Vendor)
}

func writeTempYAML(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "mibee-nvr.yaml")
	err := os.WriteFile(path, []byte(content), 0o644)
	require.NoError(t, err)
	return path
}

func TestDuplicateCameraID(t *testing.T) {
	cfg := &Config{
		Cameras: []CameraConfig{
			{ID: "cam1", Protocol: "rtsp", URL: "rtsp://192.168.1.10/stream"},
			{ID: "cam1", Protocol: "rtsp", URL: "rtsp://192.168.1.11/stream"},
		},
	}
	cfg.applyDefaults()
	err := Validate(cfg)
	require.Error(t, err)
	require.Contains(t, err.Error(), "duplicate id")
}

func TestUniqueCameraIDPasses(t *testing.T) {
	cfg := &Config{
		Cameras: []CameraConfig{
			{ID: "cam1", Protocol: "rtsp", URL: "rtsp://192.168.1.10/stream"},
			{ID: "cam2", Protocol: "rtsp", URL: "rtsp://192.168.1.11/stream"},
		},
	}
	cfg.applyDefaults()
	err := Validate(cfg)
	require.NoError(t, err)
}

func TestCameraURLInvalidFormat(t *testing.T) {
	tests := []struct {
		name string
		url  string
	}{
		{"missing scheme", "192.168.1.10:554/stream"},
		{"missing host", "rtsp://"},
		{"garbage", ":///"},
		{"no path", "rtsp://"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{Cameras: []CameraConfig{{ID: "c1", Protocol: "rtsp", URL: tt.url}}}
			cfg.applyDefaults()
			err := Validate(cfg)
			require.Error(t, err)
			require.Contains(t, err.Error(), "url has invalid format")
		})
	}
}

func TestCameraURLValidFormat(t *testing.T) {
	tests := []struct {
		name     string
		url      string
		protocol string
	}{
		{"rtsp", "rtsp://192.168.1.10:554/stream", "rtsp"},
		{"http", "http://192.168.1.101/capture", "http"},
		{"https", "https://camera.example.com/stream", "rtsp"},
		{"xiaomi", "xiaomi://device123", "xiaomi"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.protocol == "xiaomi" {
				cfg := &Config{Cameras: []CameraConfig{{ID: "c1", Protocol: "xiaomi", Encoding: "h264", URL: tt.url}}, Xiaomi: XiaomiConfig{Token: "test", Region: "cn"}}
				cfg.applyDefaults()
				require.NoError(t, Validate(cfg))
			} else {
				cfg := &Config{Cameras: []CameraConfig{{ID: "c1", Protocol: tt.protocol, URL: tt.url}}}
				cfg.applyDefaults()
				require.NoError(t, Validate(cfg))
			}
		})
}
}

func TestONVIFEndpointInvalidFormat(t *testing.T) {
	cfg := &Config{Cameras: []CameraConfig{{ID: "c1", Protocol: "onvif", ONVIFEndpoint: "no-scheme"}}}
	cfg.applyDefaults()
	err := Validate(cfg)
	require.Error(t, err)
	require.Contains(t, err.Error(), "onvif_endpoint has invalid format")
}

func TestFTPPortZeroRejected(t *testing.T) {
	cfg := &Config{}
	cfg.applyDefaults()
	cfg.FTP.Port = 0 // override default to test validation
	err := Validate(cfg)
	require.Error(t, err)
	require.Contains(t, err.Error(), "ftp port out of range")
}

func TestSegmentDurationExceeds30s(t *testing.T) {
	cfg := &Config{Storage: StorageConfig{SegmentDuration: "60s"}}
	cfg.applyDefaults()
	err := Validate(cfg)
	require.Error(t, err)
	require.Contains(t, err.Error(), "must be <= 30s")
}

func TestHLSSegmentDurationDefault30sPasses(t *testing.T) {
	cfg := &Config{}
	cfg.applyDefaults()
	require.Equal(t, "30s", cfg.Storage.SegmentDuration)
	err := Validate(cfg)
	require.NoError(t, err)
}

func TestHLSMaxStreamsDefault(t *testing.T) {
	cfg := &Config{}
	cfg.applyDefaults()
	require.Equal(t, 4, cfg.HLS.MaxStreams)
}

func TestHLSMaxStreamsValidation_Valid(t *testing.T) {
	for _, ms := range []int{1, 4, 10, 20} {
		cfg := &Config{HLS: HLSConfig{MaxStreams: ms, SegmentCount: 7}}
		cfg.applyDefaults()
		err := Validate(cfg)
		require.NoError(t, err, "max_streams=%d should be valid", ms)
	}
}

func TestHLSMaxStreamsValidation_TooLow(t *testing.T) {
	cfg := &Config{HLS: HLSConfig{MaxStreams: 0, SegmentCount: 7}}
	cfg.applyDefaults()
	cfg.HLS.MaxStreams = 0 // override default to test validation
	err := Validate(cfg)
	require.Error(t, err)
	require.Contains(t, err.Error(), "hls.max_streams")
}
func TestHLSMaxStreamsValidation_TooHigh(t *testing.T) {
	cfg := &Config{HLS: HLSConfig{MaxStreams: 21, SegmentCount: 7}}
	cfg.applyDefaults()
	err := Validate(cfg)
	require.Error(t, err)
	require.Contains(t, err.Error(), "hls.max_streams")
}

func TestValidateNilConfig(t *testing.T) {
	err := Validate(nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "config is nil")
}

func TestValidateRetentionDaysTooLow(t *testing.T) {
	cfg := &Config{Cleanup: CleanupConfig{RetentionDays: 0}}
	cfg.applyDefaults()
	err := Validate(cfg)
	// Default applies 30, so this should pass
	require.NoError(t, err)
}

func TestValidateRetentionDaysTooHigh(t *testing.T) {
	cfg := &Config{Cleanup: CleanupConfig{RetentionDays: 4000}}
	cfg.applyDefaults()
	err := Validate(cfg)
	require.Error(t, err)
	require.Contains(t, err.Error(), "retention_days")
}

func TestValidateDiskThresholdTooLow(t *testing.T) {
	cfg := &Config{Cleanup: CleanupConfig{DiskThresholdPercent: 40}}
	cfg.applyDefaults()
	err := Validate(cfg)
	require.Error(t, err)
	require.Contains(t, err.Error(), "disk_threshold_percent")
}

func TestValidateDiskThresholdTooHigh(t *testing.T) {
	cfg := &Config{Cleanup: CleanupConfig{DiskThresholdPercent: 100}}
	cfg.applyDefaults()
	err := Validate(cfg)
	require.Error(t, err)
	require.Contains(t, err.Error(), "disk_threshold_percent")
}

func TestValidateLogLevelInvalid(t *testing.T) {
	cfg := &Config{Observability: ObservabilityConfig{LogLevel: "verbose"}}
	cfg.applyDefaults()
	err := Validate(cfg)
	require.Error(t, err)
	require.Contains(t, err.Error(), "log_level")
}

func TestValidateLogFormatInvalid(t *testing.T) {
	cfg := &Config{Observability: ObservabilityConfig{LogFormat: "xml"}}
	cfg.applyDefaults()
	err := Validate(cfg)
	require.Error(t, err)
	require.Contains(t, err.Error(), "log_format")
}

func TestValidateLogLevelValid(t *testing.T) {
	for _, level := range []string{"debug", "info", "warn", "error"} {
		cfg := &Config{Observability: ObservabilityConfig{LogLevel: level, LogFormat: "json"}}
		cfg.applyDefaults()
		require.NoError(t, Validate(cfg), "log_level=%s should be valid", level)
	}
}

func TestValidateLogFormatValid(t *testing.T) {
	for _, format := range []string{"json", "text"} {
		cfg := &Config{Observability: ObservabilityConfig{LogFormat: format}}
		cfg.applyDefaults()
		require.NoError(t, Validate(cfg), "log_format=%s should be valid", format)
	}
}

func TestValidateMergeEnabledInvalidInterval(t *testing.T) {
	cfg := &Config{Merge: MergeConfig{Enabled: true, CheckInterval: "not-a-duration", WindowSize: "1h", BatchLimit: 10, MinSegmentAge: "5m", MinSegmentsToMerge: 3}}
	cfg.applyDefaults()
	err := Validate(cfg)
	require.Error(t, err)
	require.Contains(t, err.Error(), "merge check_interval")
}

func TestValidateMergeEnabledInvalidWindowSize(t *testing.T) {
	cfg := &Config{Merge: MergeConfig{Enabled: true, CheckInterval: "1h", WindowSize: "bad", BatchLimit: 10, MinSegmentAge: "5m", MinSegmentsToMerge: 3}}
	cfg.applyDefaults()
	err := Validate(cfg)
	require.Error(t, err)
	require.Contains(t, err.Error(), "merge window_size")
}

func TestValidateMergeEnabledZeroBatchLimit(t *testing.T) {
	cfg := &Config{Merge: MergeConfig{Enabled: true, CheckInterval: "1h", WindowSize: "1h", BatchLimit: 0, MinSegmentAge: "5m", MinSegmentsToMerge: 3}}
	cfg.applyDefaults()
	cfg.Merge.BatchLimit = 0 // override default
	err := Validate(cfg)
	require.Error(t, err)
	require.Contains(t, err.Error(), "batch_limit")
}

func TestValidateMergeEnabledInvalidMinSegmentAge(t *testing.T) {
	cfg := &Config{Merge: MergeConfig{Enabled: true, CheckInterval: "1h", WindowSize: "1h", BatchLimit: 10, MinSegmentAge: "bad", MinSegmentsToMerge: 3}}
	cfg.applyDefaults()
	err := Validate(cfg)
	require.Error(t, err)
	require.Contains(t, err.Error(), "min_segment_age")
}

func TestValidateMergeEnabledTooFewSegments(t *testing.T) {
	cfg := &Config{Merge: MergeConfig{Enabled: true, CheckInterval: "1h", WindowSize: "1h", BatchLimit: 10, MinSegmentAge: "5m", MinSegmentsToMerge: 1}}
	cfg.applyDefaults()
	err := Validate(cfg)
	require.Error(t, err)
	require.Contains(t, err.Error(), "min_segments_to_merge")
}

func TestValidateMergeDisabledSkipsValidation(t *testing.T) {
	cfg := &Config{Merge: MergeConfig{Enabled: false, CheckInterval: "bad"}}
	cfg.applyDefaults()
	err := Validate(cfg)
	require.NoError(t, err) // merge disabled, so invalid fields ignored
}

func TestValidateSegmentDurationInvalid(t *testing.T) {
	cfg := &Config{Storage: StorageConfig{SegmentDuration: "not-a-duration"}}
	cfg.applyDefaults()
	cfg.Storage.SegmentDuration = "not-a-duration" // override default
	err := Validate(cfg)
	require.Error(t, err)
	require.Contains(t, err.Error(), "segment_duration")
}

func TestValidateFTPPortNegative(t *testing.T) {
	cfg := &Config{FTP: FTPConfig{Port: -1}}
	cfg.applyDefaults()
	err := Validate(cfg)
	require.Error(t, err)
}

func TestCameraWhitespaceID(t *testing.T) {
	cfg := &Config{Cameras: []CameraConfig{{ID: "   ", Protocol: "rtsp", URL: "rtsp://192.168.1.10/stream"}}}
	cfg.applyDefaults()
	err := Validate(cfg)
	require.Error(t, err)
}

func TestCameraMissingURLXiaomiExempt(t *testing.T) {
	cfg := &Config{Cameras: []CameraConfig{{ID: "c1", Protocol: "xiaomi", Encoding: "h264", URL: "xiaomi://device"}}, Xiaomi: XiaomiConfig{Token: "test", Region: "cn"}}
	cfg.applyDefaults()
	err := Validate(cfg)
	require.NoError(t, err)
}

func TestSaveNilConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.yaml")
	err := Save(path, nil)
	require.Error(t, err)
}

func TestSaveEmptyPath(t *testing.T) {
	cfg := &Config{}
	cfg.applyDefaults()
	err := Save("", cfg)
	require.Error(t, err)
}

func TestLoadEmptyPath(t *testing.T) {
	_, err := Load("")
	require.Error(t, err)
}

func TestApplyDefaultsMergeFields(t *testing.T) {
	cfg := &Config{}
	cfg.applyDefaults()
	require.Equal(t, 200, cfg.Merge.BatchLimit)
	require.Equal(t, "1h", cfg.Merge.CheckInterval)
	require.Equal(t, "1h", cfg.Merge.WindowSize)
	require.Equal(t, "10m", cfg.Merge.MinSegmentAge)
	require.Equal(t, 3, cfg.Merge.MinSegmentsToMerge)
}

func TestApplyDefaultsObservability(t *testing.T) {
	cfg := &Config{}
	cfg.applyDefaults()
	require.Equal(t, "info", cfg.Observability.LogLevel)
	require.Equal(t, "text", cfg.Observability.LogFormat)
	require.Equal(t, false, cfg.Observability.EnablePprof)
}

func TestApplyDefaultsVersion(t *testing.T) {
	cfg := &Config{}
	cfg.applyDefaults()
	require.Equal(t, "1.0", cfg.Version)
}

func TestApplyDefaultsHLS(t *testing.T) {
	cfg := &Config{}
	cfg.applyDefaults()
	require.Equal(t, 100, cfg.HLS.WriteBufferSize)
	require.Equal(t, 10, cfg.HLS.SegmentMaxSizeMB)
	require.Equal(t, 7, cfg.HLS.SegmentCount)
	require.Equal(t, 4, cfg.HLS.MaxStreams)
	require.False(t, cfg.HLS.LowLatency)
	require.Equal(t, "200ms", cfg.HLS.PartMinDuration)
}

func TestHLSPartMinDurationValidation_Invalid(t *testing.T) {
	cfg := &Config{HLS: HLSConfig{SegmentCount: 7, PartMinDuration: "invalid"}}
	cfg.applyDefaults()
	err := Validate(cfg)
	require.Error(t, err)
	require.Contains(t, err.Error(), "hls.part_min_duration")
}

func TestHLSPartMinDurationValidation_TooLow(t *testing.T) {
	cfg := &Config{HLS: HLSConfig{SegmentCount: 7, PartMinDuration: "50ms"}}
	cfg.applyDefaults()
	err := Validate(cfg)
	require.Error(t, err)
	require.Contains(t, err.Error(), "hls.part_min_duration")
}

func TestHLSPartMinDurationValidation_TooHigh(t *testing.T) {
	cfg := &Config{HLS: HLSConfig{SegmentCount: 7, PartMinDuration: "2s"}}
	cfg.applyDefaults()
	err := Validate(cfg)
	require.Error(t, err)
	require.Contains(t, err.Error(), "hls.part_min_duration")
}

func TestHLSLowLatency_SegmentCountTooLow(t *testing.T) {
	cfg := &Config{HLS: HLSConfig{SegmentCount: 5, LowLatency: true, PartMinDuration: "200ms"}}
	cfg.applyDefaults()
	err := Validate(cfg)
	require.Error(t, err)
	require.Contains(t, err.Error(), "hls.segment_count must be >= 7 when low_latency is enabled")
}

func TestHLSLowLatency_SegmentCount7(t *testing.T) {
	cfg := &Config{HLS: HLSConfig{SegmentCount: 7, LowLatency: true, PartMinDuration: "200ms"}}
	cfg.applyDefaults()
	err := Validate(cfg)
	require.NoError(t, err)
}

func TestApplyDefaultsFTP(t *testing.T) {
	cfg := &Config{}
	cfg.applyDefaults()
	require.Equal(t, 2121, cfg.FTP.Port)
	require.Equal(t, "2122-2140", cfg.FTP.PassivePortRange)
	require.NotNil(t, cfg.FTP.Enabled)
	require.True(t, *cfg.FTP.Enabled)
}

func TestCameraProtocolNormalization_RtspH264(t *testing.T) {
	cfg := &Config{Cameras: []CameraConfig{{ID: "c1", Protocol: "rtsp_h264", URL: "rtsp://192.168.1.10/stream"}}}
	cfg.applyDefaults()
	require.Equal(t, "rtsp", cfg.Cameras[0].Protocol)
	require.Equal(t, "h264", cfg.Cameras[0].Encoding)
}

func TestCameraProtocolNormalization_RtspMjpeg(t *testing.T) {
	cfg := &Config{Cameras: []CameraConfig{{ID: "c1", Protocol: "rtsp_mjpeg", URL: "rtsp://192.168.1.10/stream"}}}
	cfg.applyDefaults()
	require.Equal(t, "rtsp", cfg.Cameras[0].Protocol)
	require.Equal(t, "mjpeg", cfg.Cameras[0].Encoding)
}

func TestCameraProtocolNormalization_HttpJpeg(t *testing.T) {
	cfg := &Config{Cameras: []CameraConfig{{ID: "c1", Protocol: "http_jpeg", URL: "http://192.168.1.10/capture"}}}
	cfg.applyDefaults()
	require.Equal(t, "http", cfg.Cameras[0].Protocol)
	require.Equal(t, "jpeg", cfg.Cameras[0].Encoding)
}

func TestCameraEncodingDefault_Rtsp(t *testing.T) {
	cfg := &Config{Cameras: []CameraConfig{{ID: "c1", Protocol: "rtsp", URL: "rtsp://192.168.1.10/stream"}}}
	cfg.applyDefaults()
	require.Equal(t, "h264", cfg.Cameras[0].Encoding) // default for rtsp
}

func TestCameraEncodingDefault_Http(t *testing.T) {
	cfg := &Config{Cameras: []CameraConfig{{ID: "c1", Protocol: "http", URL: "http://192.168.1.10/capture"}}}
	cfg.applyDefaults()
	require.Equal(t, "jpeg", cfg.Cameras[0].Encoding) // default for http
}

func TestValidateONVIFEndpointAutoPopulated(t *testing.T) {
	cfg := &Config{Cameras: []CameraConfig{{ID: "c1", Protocol: "onvif", URL: "http://192.168.1.100/onvif/device_service"}}}
	cfg.applyDefaults()
	err := Validate(cfg)
	require.NoError(t, err)
}

func TestStreamingDefaults(t *testing.T) {
	cfg := &Config{}
	cfg.applyDefaults()
	require.Equal(t, "hls", cfg.Streaming.DefaultProtocol)
	require.NotNil(t, cfg.Streaming.WebRTC.Enabled)
	require.True(t, *cfg.Streaming.WebRTC.Enabled)
	require.Equal(t, 2, cfg.Streaming.WebRTC.MaxViewers)
	require.Equal(t, "60s", cfg.Streaming.WebRTC.IdleTimeout)
	require.NotNil(t, cfg.Streaming.FLV.Enabled)
	require.True(t, *cfg.Streaming.FLV.Enabled)
	require.Equal(t, 10, cfg.Streaming.FLV.MaxViewers)
	require.Equal(t, "60s", cfg.Streaming.FLV.IdleTimeout)
	require.Equal(t, 1, cfg.Streaming.FLV.GOPCacheSize)
}

func TestStreamingDefaultProtocolInvalid(t *testing.T) {
	cfg := &Config{Streaming: StreamingConfig{DefaultProtocol: "rtmp"}}
	cfg.applyDefaults()
	err := Validate(cfg)
	require.Error(t, err)
	require.Contains(t, err.Error(), "streaming.default_protocol")
}

func TestWebRTCMaxViewersTooLow(t *testing.T) {
	cfg := &Config{Streaming: StreamingConfig{WebRTC: WebRTCConfig{MaxViewers: 0}}}
	cfg.applyDefaults()
	cfg.Streaming.WebRTC.MaxViewers = 0 // override default
	err := Validate(cfg)
	require.Error(t, err)
	require.Contains(t, err.Error(), "streaming.webrtc.max_viewers")
}

func TestWebRTCMaxViewersTooHigh(t *testing.T) {
	cfg := &Config{Streaming: StreamingConfig{WebRTC: WebRTCConfig{MaxViewers: 11}}}
	cfg.applyDefaults()
	err := Validate(cfg)
	require.Error(t, err)
	require.Contains(t, err.Error(), "streaming.webrtc.max_viewers")
}

func TestFLVMaxViewersTooLow(t *testing.T) {
	cfg := &Config{Streaming: StreamingConfig{FLV: FLVConfig{MaxViewers: 0}}}
	cfg.applyDefaults()
	cfg.Streaming.FLV.MaxViewers = 0 // override default
	err := Validate(cfg)
	require.Error(t, err)
	require.Contains(t, err.Error(), "streaming.flv.max_viewers")
}

func TestFLVMaxViewersTooHigh(t *testing.T) {
	cfg := &Config{Streaming: StreamingConfig{FLV: FLVConfig{MaxViewers: 51}}}
	cfg.applyDefaults()
	err := Validate(cfg)
	require.Error(t, err)
	require.Contains(t, err.Error(), "streaming.flv.max_viewers")
}

func TestFLVGOPCacheSizeNegative(t *testing.T) {
	cfg := &Config{Streaming: StreamingConfig{FLV: FLVConfig{GOPCacheSize: -1}}}
	cfg.applyDefaults()
	cfg.Streaming.FLV.GOPCacheSize = -1 // override default
	err := Validate(cfg)
	require.Error(t, err)
	require.Contains(t, err.Error(), "streaming.flv.gop_cache_size")
}

