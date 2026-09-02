package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
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
	cfg.ApplyDefaults()
	err := Validate(cfg)
	require.Error(t, err)
}

func TestValidateInvalidProtocol(t *testing.T) {
	cfg := &Config{Cameras: []CameraConfig{{ID: "c1", URL: "rtsp://a", Protocol: "invalid"}}}
	cfg.ApplyDefaults()
	err := Validate(cfg)
	require.Error(t, err)
}

func TestPortRangeValidation(t *testing.T) {
	cfg := &Config{FTP: FTPConfig{Port: 70000}}
	cfg.ApplyDefaults()
	err := Validate(cfg)
	require.Error(t, err)
}

func TestDefaultsApplied(t *testing.T) {
	cfg := &Config{}
	cfg.ApplyDefaults()
	require.Equal(t, ":9090", cfg.Server.Listen)
	require.Equal(t, "/var/lib/mibee-nvr", cfg.Storage.RootDir)
	require.Equal(t, "30s", cfg.Storage.SegmentDuration)
	require.Equal(t, 30, cfg.Cleanup.RetentionDays)
	require.Equal(t, "1h", cfg.Cleanup.CheckInterval)
	require.Equal(t, 85, cfg.Cleanup.DiskThresholdPercent)
	require.Equal(t, 2121, cfg.FTP.Port)
	require.Equal(t, true, *cfg.FTP.Enabled)
	require.Equal(t, true, *cfg.WebDAV.Enabled)
	require.Equal(t, "/dav", cfg.WebDAV.PathPrefix)
	require.Equal(t, "Local", cfg.Timezone)
}

func TestFrameWatchdogTimeoutDefaultEmpty(t *testing.T) {
	cfg := &Config{Cameras: []CameraConfig{{ID: "cam1", URL: "rtsp://localhost/stream"}}}
	cfg.ApplyDefaults()
	require.Equal(t, "", cfg.Cameras[0].FrameWatchdogTimeout)
}

func TestFrameWatchdogTimeoutCustomValue(t *testing.T) {
	cfg := &Config{Cameras: []CameraConfig{{
		ID:                   "cam1",
		URL:                  "rtsp://localhost/stream",
		FrameWatchdogTimeout: "15s",
	}}}
	cfg.ApplyDefaults()
	require.Equal(t, "15s", cfg.Cameras[0].FrameWatchdogTimeout)
}

func TestLoadNonExistentFile(t *testing.T) {
	_, err := Load("no_such_file.yaml")
	require.Error(t, err)
}

func TestFTPExplicitlyDisabled(t *testing.T) {
	cfg := &Config{FTP: FTPConfig{Enabled: new(bool)}}
	*cfg.FTP.Enabled = false // explicitly set to false
	cfg.ApplyDefaults()
	require.NotNil(t, cfg.FTP.Enabled)
	require.Equal(t, false, *cfg.FTP.Enabled) // should remain false
}

func TestWebDAVExplicitlyDisabled(t *testing.T) {
	cfg := &Config{WebDAV: WebDAVConfig{Enabled: new(bool)}}
	*cfg.WebDAV.Enabled = false // explicitly set to false
	cfg.ApplyDefaults()
	require.NotNil(t, cfg.WebDAV.Enabled)
	require.Equal(t, false, *cfg.WebDAV.Enabled) // should remain false
}

func TestFTPNotConfigured(t *testing.T) {
	cfg := &Config{}
	cfg.ApplyDefaults()
	require.NotNil(t, cfg.FTP.Enabled)
	require.Equal(t, true, *cfg.FTP.Enabled) // should default to true
}

func TestWebDAVNotConfigured(t *testing.T) {
	cfg := &Config{}
	cfg.ApplyDefaults()
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
			URL: "rtsp://192.168.1.10/stream", Username: "admin", Password: "secret",
		}},
		Cleanup: CleanupConfig{RetentionDays: 7, CheckInterval: "30m", DiskThresholdPercent: 80},
		Auth:    AuthConfig{Username: "admin", PasswordHash: "$2a$10$xxx"},
		FTP:     FTPConfig{Enabled: &ftpEnabled, Port: 2121, PassivePortRange: "3000-3010"},
		MQTT:    MQTTConfig{Enabled: true, Broker: "tcp://mqtt.local:1883", Topic: "nvr/trigger", ClientID: "mibee", Username: "mqttuser", Password: "mqttpass"},
		WebDAV:  WebDAVConfig{Enabled: &webdavEnabled, PathPrefix: "/files"},
	}
	original.ApplyDefaults()

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
	cfg.ApplyDefaults()

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
	first.ApplyDefaults()
	require.NoError(t, Save(path, first))

	second := &Config{Server: ServerConfig{Listen: ":3333"}, Storage: StorageConfig{RootDir: "/new"}}
	second.ApplyDefaults()
	require.NoError(t, Save(path, second))

	loaded, err := Load(path)
	require.NoError(t, err)
	require.Equal(t, ":3333", loaded.Server.Listen)
	require.Equal(t, "/new", loaded.Storage.RootDir)
}

func TestValidateOnvifProtocol(t *testing.T) {
	cfg := &Config{Cameras: []CameraConfig{{ID: "c1", ONVIFEndpoint: "http://192.168.1.100/onvif/device_service", Protocol: "onvif"}}}
	cfg.ApplyDefaults()
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
		CheckInterval: "30m",
		BatchLimit:    50,
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

// TestResolveMergeConfig_RollingPerCameraDisable verifies the C1 blocker fix:
// when the global rolling merge is ON (default), a per-camera explicit
// rolling_enabled: false MUST override it to false. With the old bare-bool
// implementation this was impossible — `if perCamera.RollingEnabled` was a
// one-way true switch with no way to express "off". The *bool change makes
// explicit false win.
func TestResolveMergeConfig_RollingPerCameraDisable(t *testing.T) {
	tr := true
	global := MergeConfig{RollingEnabled: &tr} // global default-on

	t.Run("per_camera_explicit_false_disables", func(t *testing.T) {
		fl := false
		perCamera := &MergeConfig{RollingEnabled: &fl}
		result := ResolveMergeConfig(global, perCamera)
		require.NotNil(t, result.RollingEnabled)
		require.False(t, result.RollingEnabledValue(), "per-camera explicit false must disable rolling merge")
	})

	t.Run("per_camera_explicit_true_keeps_enabled", func(t *testing.T) {
		perCameraTrue := &MergeConfig{RollingEnabled: &tr}
		result := ResolveMergeConfig(global, perCameraTrue)
		require.True(t, result.RollingEnabledValue())
	})

	t.Run("per_camera_nil_keeps_global", func(t *testing.T) {
		// Per-camera unset (nil pointer) inherits global.
		perCameraNil := &MergeConfig{} // RollingEnabled is nil here
		result := ResolveMergeConfig(global, perCameraNil)
		require.True(t, result.RollingEnabledValue(), "unset per-camera inherits global true")
	})

	t.Run("per_camera_backfill_overrides", func(t *testing.T) {
		globalBF := MergeConfig{RollingEnabled: &tr, RollingBackfillMaxSegments: 500, RollingBackfillMaxAge: "72h"}
		perCamera := &MergeConfig{RollingBackfillMaxSegments: 100, RollingBackfillMaxAge: "24h"}
		result := ResolveMergeConfig(globalBF, perCamera)
		require.Equal(t, 100, result.RollingBackfillMaxSegments)
		require.Equal(t, "24h", result.RollingBackfillMaxAge)
	})
}

// TestValidate_RollingEnabledByDefault confirms Validate exercises the rolling
// duration fields when rolling is on by default (regression guard for the guard
// at config.go that previously only ran when Enabled=true).
func TestValidate_RollingEnabledByDefault(t *testing.T) {
	t.Run("valid_rolling_durations_pass", func(t *testing.T) {
		cfg := &Config{}
		cfg.ApplyDefaults()
		require.NoError(t, Validate(cfg))
	})

	t.Run("invalid_rolling_debounce_rejected", func(t *testing.T) {
		cfg := &Config{}
		cfg.ApplyDefaults()
		cfg.Merge.RollingDebounce = "not-a-duration"
		err := Validate(cfg)
		require.Error(t, err)
		require.Contains(t, err.Error(), "rolling_debounce")
	})

	t.Run("invalid_rolling_backfill_age_rejected", func(t *testing.T) {
		cfg := &Config{}
		cfg.ApplyDefaults()
		cfg.Merge.RollingBackfillMaxAge = "xyz"
		err := Validate(cfg)
		require.Error(t, err)
		require.Contains(t, err.Error(), "rolling_backfill_max_age")
	})

	t.Run("rolling_disabled_skips_validation", func(t *testing.T) {
		// When rolling is explicitly off, invalid durations are tolerated
		// (the values are never used).
		fl := false
		cfg := &Config{Merge: MergeConfig{RollingEnabled: &fl, RollingDebounce: "garbage"}}
		cfg.ApplyDefaults()
		require.NoError(t, Validate(cfg))
	})
}

func TestHLSSegmentCountDefault(t *testing.T) {
	cfg := &Config{}
	cfg.ApplyDefaults()
	require.Equal(t, 7, cfg.HLS.SegmentCount)
	require.Equal(t, 100, cfg.HLS.WriteBufferSize)
}

func TestHLSSegmentCountValidation_Valid(t *testing.T) {
	for _, sc := range []int{3, 5, 7, 10} {
		cfg := &Config{HLS: HLSConfig{SegmentCount: sc}}
		cfg.ApplyDefaults()
		err := Validate(cfg)
		require.NoError(t, err, "segment_count=%d should be valid", sc)
	}
}

func TestHLSSegmentCountValidation_TooLow(t *testing.T) {
	cfg := &Config{HLS: HLSConfig{SegmentCount: 2}}
	cfg.ApplyDefaults()
	err := Validate(cfg)
	require.Error(t, err)
	require.Contains(t, err.Error(), "hls.segment_count")
}

func TestHLSSegmentCountValidation_TooHigh(t *testing.T) {
	cfg := &Config{HLS: HLSConfig{SegmentCount: 11}}
	cfg.ApplyDefaults()
	err := Validate(cfg)
	require.Error(t, err)
	require.Contains(t, err.Error(), "hls.segment_count")
}

func TestXiaomiConfigDefaults(t *testing.T) {
	cfg := &Config{}
	cfg.ApplyDefaults()
	require.Equal(t, "cn", cfg.Xiaomi.Region)
}

func TestXiaomiConfigValidationDisablesCameraWithoutToken(t *testing.T) {
	cfg := &Config{Cameras: []CameraConfig{{ID: "c1", Protocol: "xiaomi", Encoding: "h264", URL: "xiaomi://device"}}}
	cfg.ApplyDefaults()
	err := Validate(cfg)
	require.NoError(t, err)
}

func TestXiaomiConfigValidationWithToken(t *testing.T) {
	cfg := &Config{
		Cameras: []CameraConfig{{ID: "c1", Protocol: "xiaomi", Encoding: "h264", URL: "xiaomi://device"}},
		Xiaomi:  XiaomiConfig{Token: "test-token", Region: "cn"},
	}
	cfg.ApplyDefaults()
	err := Validate(cfg)
	require.NoError(t, err)
}

// TestXiaomiEmptyEncodingBackfilled (#402): configs written since v0.10.0
// store encoding:"" for xiaomi cameras (the UI deliberately omits it for
// auto-detect protocols; the add handler lacked a xiaomi default). The strict
// protocol/encoding validation rejected that fatally at startup — Validate
// must instead backfill the h264 hint so the next start succeeds, while still
// rejecting a genuinely wrong encoding value.
func TestXiaomiEmptyEncodingBackfilled(t *testing.T) {
	cfg := &Config{Cameras: []CameraConfig{{ID: "c1", Protocol: "xiaomi", Encoding: "", URL: "xiaomi://device"}}}
	cfg.ApplyDefaults()
	err := Validate(cfg)
	require.NoError(t, err)
	require.Equal(t, "h264", cfg.Cameras[0].Encoding, "empty xiaomi encoding must be backfilled to the h264 hint")
}

func TestXiaomiInvalidEncodingStillRejected(t *testing.T) {
	cfg := &Config{Cameras: []CameraConfig{{ID: "c1", Protocol: "xiaomi", Encoding: "jpeg", URL: "xiaomi://device"}}}
	cfg.ApplyDefaults()
	err := Validate(cfg)
	require.Error(t, err, "backfill must only heal the empty value, not loosen validation for garbage")
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
	cfg.ApplyDefaults()
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
	cfg.ApplyDefaults()
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
	cfg.ApplyDefaults()
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
			cfg.ApplyDefaults()
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
				cfg.ApplyDefaults()
				require.NoError(t, Validate(cfg))
			} else {
				cfg := &Config{Cameras: []CameraConfig{{ID: "c1", Protocol: tt.protocol, URL: tt.url}}}
				cfg.ApplyDefaults()
				require.NoError(t, Validate(cfg))
			}
		})
	}
}

func TestONVIFEndpointInvalidFormat(t *testing.T) {
	cfg := &Config{Cameras: []CameraConfig{{ID: "c1", Protocol: "onvif", ONVIFEndpoint: "no-scheme"}}}
	cfg.ApplyDefaults()
	err := Validate(cfg)
	require.Error(t, err)
	require.Contains(t, err.Error(), "onvif_endpoint has invalid format")
}

func TestFTPPortZeroRejected(t *testing.T) {
	cfg := &Config{}
	cfg.ApplyDefaults()
	cfg.FTP.Port = 0 // override default to test validation
	err := Validate(cfg)
	require.Error(t, err)
	require.Contains(t, err.Error(), "ftp port out of range")
}

func TestSegmentDurationClampedToPlatformCap(t *testing.T) {
	// The cap is platform-aware: 30s on low-memory devices (RPi 3B ≤2GB),
	// 2m on higher-memory devices. A value above the platform cap is clamped.
	segDurCap := maxSegmentDurationForMem()
	// Pick a value strictly above the cap.
	over := segDurCap + 30*time.Second
	cfg := &Config{Storage: StorageConfig{SegmentDuration: over.String()}}
	cfg.ApplyDefaults()
	err := Validate(cfg)
	require.NoError(t, err)
	got, err := time.ParseDuration(cfg.Storage.SegmentDuration)
	require.NoError(t, err)
	require.LessOrEqual(t, got, segDurCap, "segment_duration should be clamped to the platform cap")
}

func TestSegmentDurationHighMemAllows1m(t *testing.T) {
	// On a high-memory host (CI runners typically have >2GB), 1m is allowed
	// without clamping. On a low-memory host this test is skipped — the 1m
	// value would be clamped to 30s there, which is correct behavior.
	if memAvailableMB() <= 2048 {
		t.Skip("host has ≤2GB RAM; 1m segment_duration correctly clamped to 30s here")
	}
	cfg := &Config{Storage: StorageConfig{SegmentDuration: "1m"}}
	cfg.ApplyDefaults()
	err := Validate(cfg)
	require.NoError(t, err)
	require.Equal(t, "1m", cfg.Storage.SegmentDuration, "1m should pass on high-memory host")
}

func TestHLSSegmentDurationDefault30sPasses(t *testing.T) {
	cfg := &Config{}
	cfg.ApplyDefaults()
	require.Equal(t, "30s", cfg.Storage.SegmentDuration)
	err := Validate(cfg)
	require.NoError(t, err)
}

func TestHLSMaxStreamsDefault(t *testing.T) {
	cfg := &Config{}
	cfg.ApplyDefaults()
	require.Equal(t, 4, cfg.HLS.MaxStreams)
}

func TestHLSMaxStreamsValidation_Valid(t *testing.T) {
	for _, ms := range []int{1, 4, 10, 20} {
		cfg := &Config{HLS: HLSConfig{MaxStreams: ms, SegmentCount: 7}}
		cfg.ApplyDefaults()
		err := Validate(cfg)
		require.NoError(t, err, "max_streams=%d should be valid", ms)
	}
}

func TestHLSMaxStreamsValidation_TooLow(t *testing.T) {
	cfg := &Config{HLS: HLSConfig{MaxStreams: 0, SegmentCount: 7}}
	cfg.ApplyDefaults()
	cfg.HLS.MaxStreams = 0 // override default to test validation
	err := Validate(cfg)
	require.Error(t, err)
	require.Contains(t, err.Error(), "hls.max_streams")
}

func TestHLSMaxStreamsValidation_TooHigh(t *testing.T) {
	cfg := &Config{HLS: HLSConfig{MaxStreams: 21, SegmentCount: 7}}
	cfg.ApplyDefaults()
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
	cfg.ApplyDefaults()
	err := Validate(cfg)
	// Default applies 30, so this should pass
	require.NoError(t, err)
}

func TestValidateRetentionDaysTooHigh(t *testing.T) {
	cfg := &Config{Cleanup: CleanupConfig{RetentionDays: 4000}}
	cfg.ApplyDefaults()
	err := Validate(cfg)
	require.Error(t, err)
	require.Contains(t, err.Error(), "retention_days")
}

func TestValidateDiskThresholdTooLow(t *testing.T) {
	cfg := &Config{Cleanup: CleanupConfig{DiskThresholdPercent: 40}}
	cfg.ApplyDefaults()
	err := Validate(cfg)
	require.Error(t, err)
	require.Contains(t, err.Error(), "disk_threshold_percent")
}

func TestValidateDiskThresholdTooHigh(t *testing.T) {
	cfg := &Config{Cleanup: CleanupConfig{DiskThresholdPercent: 100}}
	cfg.ApplyDefaults()
	err := Validate(cfg)
	require.Error(t, err)
	require.Contains(t, err.Error(), "disk_threshold_percent")
}

func TestValidateLogLevelInvalid(t *testing.T) {
	cfg := &Config{Observability: ObservabilityConfig{LogLevel: "verbose"}}
	cfg.ApplyDefaults()
	err := Validate(cfg)
	require.Error(t, err)
	require.Contains(t, err.Error(), "log_level")
}

func TestValidateLogFormatInvalid(t *testing.T) {
	cfg := &Config{Observability: ObservabilityConfig{LogFormat: "xml"}}
	cfg.ApplyDefaults()
	err := Validate(cfg)
	require.Error(t, err)
	require.Contains(t, err.Error(), "log_format")
}

func TestValidateLogLevelValid(t *testing.T) {
	for _, level := range []string{"debug", "info", "warn", "error"} {
		cfg := &Config{Observability: ObservabilityConfig{LogLevel: level, LogFormat: "json"}}
		cfg.ApplyDefaults()
		require.NoError(t, Validate(cfg), "log_level=%s should be valid", level)
	}
}

func TestValidateLogFormatValid(t *testing.T) {
	for _, format := range []string{"json", "text"} {
		cfg := &Config{Observability: ObservabilityConfig{LogFormat: format}}
		cfg.ApplyDefaults()
		require.NoError(t, Validate(cfg), "log_format=%s should be valid", format)
	}
}

func TestValidateMergeEnabledInvalidInterval(t *testing.T) {
	cfg := &Config{Merge: MergeConfig{Enabled: true, CheckInterval: "not-a-duration", WindowSize: "1h", BatchLimit: 10, MinSegmentAge: "5m", MinSegmentsToMerge: 3}}
	cfg.ApplyDefaults()
	err := Validate(cfg)
	require.Error(t, err)
	require.Contains(t, err.Error(), "merge check_interval")
}

func TestValidateMergeEnabledInvalidWindowSize(t *testing.T) {
	cfg := &Config{Merge: MergeConfig{Enabled: true, CheckInterval: "1h", WindowSize: "bad", BatchLimit: 10, MinSegmentAge: "5m", MinSegmentsToMerge: 3}}
	cfg.ApplyDefaults()
	err := Validate(cfg)
	require.Error(t, err)
	require.Contains(t, err.Error(), "merge window_size")
}

func TestValidateMergeEnabledZeroBatchLimit(t *testing.T) {
	cfg := &Config{Merge: MergeConfig{Enabled: true, CheckInterval: "1h", WindowSize: "1h", BatchLimit: 0, MinSegmentAge: "5m", MinSegmentsToMerge: 3}}
	cfg.ApplyDefaults()
	cfg.Merge.BatchLimit = 0 // override default
	err := Validate(cfg)
	require.Error(t, err)
	require.Contains(t, err.Error(), "batch_limit")
}

func TestValidateMergeEnabledInvalidMinSegmentAge(t *testing.T) {
	cfg := &Config{Merge: MergeConfig{Enabled: true, CheckInterval: "1h", WindowSize: "1h", BatchLimit: 10, MinSegmentAge: "bad", MinSegmentsToMerge: 3}}
	cfg.ApplyDefaults()
	err := Validate(cfg)
	require.Error(t, err)
	require.Contains(t, err.Error(), "min_segment_age")
}

func TestValidateMergeEnabledTooFewSegments(t *testing.T) {
	cfg := &Config{Merge: MergeConfig{Enabled: true, CheckInterval: "1h", WindowSize: "1h", BatchLimit: 10, MinSegmentAge: "5m", MinSegmentsToMerge: 1}}
	cfg.ApplyDefaults()
	err := Validate(cfg)
	require.Error(t, err)
	require.Contains(t, err.Error(), "min_segments_to_merge")
}

func TestValidateMergeDisabledSkipsValidation(t *testing.T) {
	cfg := &Config{Merge: MergeConfig{Enabled: false, CheckInterval: "bad"}}
	cfg.ApplyDefaults()
	err := Validate(cfg)
	require.NoError(t, err) // merge disabled, so invalid fields ignored
}

func TestValidateSegmentDurationInvalid(t *testing.T) {
	cfg := &Config{Storage: StorageConfig{SegmentDuration: "not-a-duration"}}
	cfg.ApplyDefaults()
	cfg.Storage.SegmentDuration = "not-a-duration" // override default
	err := Validate(cfg)
	require.Error(t, err)
	require.Contains(t, err.Error(), "segment_duration")
}

func TestValidateFTPPortNegative(t *testing.T) {
	cfg := &Config{FTP: FTPConfig{Port: -1}}
	cfg.ApplyDefaults()
	err := Validate(cfg)
	require.Error(t, err)
}

func TestCameraWhitespaceID(t *testing.T) {
	cfg := &Config{Cameras: []CameraConfig{{ID: "   ", Protocol: "rtsp", URL: "rtsp://192.168.1.10/stream"}}}
	cfg.ApplyDefaults()
	err := Validate(cfg)
	require.Error(t, err)
}

func TestCameraMissingURLXiaomiExempt(t *testing.T) {
	cfg := &Config{Cameras: []CameraConfig{{ID: "c1", Protocol: "xiaomi", Encoding: "h264", URL: "xiaomi://device"}}, Xiaomi: XiaomiConfig{Token: "test", Region: "cn"}}
	cfg.ApplyDefaults()
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
	cfg.ApplyDefaults()
	err := Save("", cfg)
	require.Error(t, err)
}

func TestLoadEmptyPath(t *testing.T) {
	_, err := Load("")
	require.Error(t, err)
}

func TestApplyDefaultsMergeFields(t *testing.T) {
	cfg := &Config{}
	cfg.ApplyDefaults()
	require.Equal(t, 200, cfg.Merge.BatchLimit)
	require.Equal(t, "1h", cfg.Merge.CheckInterval)
	require.Equal(t, "1h", cfg.Merge.WindowSize)
	require.Equal(t, "10m", cfg.Merge.MinSegmentAge)
	require.Equal(t, 3, cfg.Merge.MinSegmentsToMerge)

	// Rolling merge defaults to ON. ApplyDefaults sets the pointer to true
	// when the user left it unset, so an explicit *bool (not bare bool) is set.
	require.NotNil(t, cfg.Merge.RollingEnabled, "RollingEnabled pointer should be set by ApplyDefaults")
	require.True(t, cfg.Merge.RollingEnabledValue(), "rolling merge should default to enabled")
	// Backfill throttling defaults.
	require.Equal(t, 500, cfg.Merge.RollingBackfillMaxSegments)
	require.Equal(t, "72h", cfg.Merge.RollingBackfillMaxAge)
}

func TestApplyDefaultsRollingEnabledExplicitFalse(t *testing.T) {
	// When the user explicitly sets rolling_enabled: false, ApplyDefaults must
	// NOT override it to true. This is the per-camera-disable guarantee.
	f := false
	cfg := &Config{Merge: MergeConfig{RollingEnabled: &f}}
	cfg.ApplyDefaults()
	require.False(t, cfg.Merge.RollingEnabledValue(), "explicit false must be preserved")
}

func TestRollingEnabledValue(t *testing.T) {
	// nil → true (default-on).
	var m MergeConfig
	require.True(t, m.RollingEnabledValue())

	// explicit true → true.
	tr := true
	m.RollingEnabled = &tr
	require.True(t, m.RollingEnabledValue())

	// explicit false → false.
	fl := false
	m.RollingEnabled = &fl
	require.False(t, m.RollingEnabledValue())
}

func TestApplyDefaultsObservability(t *testing.T) {
	cfg := &Config{}
	cfg.ApplyDefaults()
	require.Equal(t, "info", cfg.Observability.LogLevel)
	require.Equal(t, "text", cfg.Observability.LogFormat)
	require.Equal(t, false, cfg.Observability.EnablePprof)
}

func TestApplyDefaultsVersion(t *testing.T) {
	cfg := &Config{}
	cfg.ApplyDefaults()
	require.Equal(t, "1.0", cfg.Version)
}

func TestApplyDefaultsHLS(t *testing.T) {
	cfg := &Config{}
	cfg.ApplyDefaults()
	require.Equal(t, 100, cfg.HLS.WriteBufferSize)
	require.Equal(t, 10, cfg.HLS.SegmentMaxSizeMB)
	require.Equal(t, 7, cfg.HLS.SegmentCount)
	require.Equal(t, 4, cfg.HLS.MaxStreams)
	require.False(t, cfg.HLS.LowLatency)
	require.Equal(t, "200ms", cfg.HLS.PartMinDuration)
}

func TestHLSPartMinDurationValidation_Invalid(t *testing.T) {
	cfg := &Config{HLS: HLSConfig{SegmentCount: 7, PartMinDuration: "invalid"}}
	cfg.ApplyDefaults()
	err := Validate(cfg)
	require.Error(t, err)
	require.Contains(t, err.Error(), "hls.part_min_duration")
}

func TestHLSPartMinDurationValidation_TooLow(t *testing.T) {
	cfg := &Config{HLS: HLSConfig{SegmentCount: 7, PartMinDuration: "50ms"}}
	cfg.ApplyDefaults()
	err := Validate(cfg)
	require.Error(t, err)
	require.Contains(t, err.Error(), "hls.part_min_duration")
}

func TestHLSPartMinDurationValidation_TooHigh(t *testing.T) {
	cfg := &Config{HLS: HLSConfig{SegmentCount: 7, PartMinDuration: "2s"}}
	cfg.ApplyDefaults()
	err := Validate(cfg)
	require.Error(t, err)
	require.Contains(t, err.Error(), "hls.part_min_duration")
}

func TestHLSLowLatency_SegmentCountTooLow(t *testing.T) {
	cfg := &Config{HLS: HLSConfig{SegmentCount: 5, LowLatency: true, PartMinDuration: "200ms"}}
	cfg.ApplyDefaults()
	err := Validate(cfg)
	require.Error(t, err)
	require.Contains(t, err.Error(), "hls.segment_count must be >= 7 when low_latency is enabled")
}

func TestHLSLowLatency_SegmentCount7(t *testing.T) {
	cfg := &Config{HLS: HLSConfig{SegmentCount: 7, LowLatency: true, PartMinDuration: "200ms"}}
	cfg.ApplyDefaults()
	err := Validate(cfg)
	require.NoError(t, err)
}

func TestApplyDefaultsFTP(t *testing.T) {
	cfg := &Config{}
	cfg.ApplyDefaults()
	require.Equal(t, 2121, cfg.FTP.Port)
	require.Equal(t, "2122-2140", cfg.FTP.PassivePortRange)
	require.NotNil(t, cfg.FTP.Enabled)
	require.True(t, *cfg.FTP.Enabled)
}

// 0.10.0+: combined protocol strings are rejected by Validate.
func TestCameraProtocolCombined_Rejected(t *testing.T) {
	for _, proto := range []string{"rtsp", "h264", "rtsp", "mjpeg", "http", "jpeg", "rtsp_h265"} {
		cfg := &Config{Cameras: []CameraConfig{{ID: "c1", Protocol: proto, URL: "rtsp://192.168.1.10/stream"}}}
		err := Validate(cfg)
		require.Error(t, err, "combined protocol %q should be rejected", proto)
	}
}

func TestCameraEncodingDefault_Rtsp(t *testing.T) {
	cfg := &Config{Cameras: []CameraConfig{{ID: "c1", Protocol: "rtsp", URL: "rtsp://192.168.1.10/stream"}}}
	cfg.ApplyDefaults()
	require.Equal(t, "h264", cfg.Cameras[0].Encoding) // default for rtsp
}

func TestCameraEncodingDefault_Http(t *testing.T) {
	cfg := &Config{Cameras: []CameraConfig{{ID: "c1", Protocol: "http", URL: "http://192.168.1.10/capture"}}}
	cfg.ApplyDefaults()
	require.Equal(t, "jpeg", cfg.Cameras[0].Encoding) // default for http
}

func TestValidateONVIFEndpointAutoPopulated(t *testing.T) {
	cfg := &Config{Cameras: []CameraConfig{{ID: "c1", Protocol: "onvif", URL: "http://192.168.1.100/onvif/device_service"}}}
	cfg.ApplyDefaults()
	err := Validate(cfg)
	require.NoError(t, err)
}

func TestStreamingDefaults(t *testing.T) {
	cfg := &Config{}
	cfg.ApplyDefaults()
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

// TestStreamingDefaultProtocolBackwardCompat ensures a YAML config carrying
// the removed streaming.default_protocol key still loads cleanly (unknown
// fields are ignored, not strict-decoded). The field was removed because the
// frontend Player Orchestrator now auto-selects the protocol per camera.
func TestStreamingDefaultProtocolBackwardCompat(t *testing.T) {
	const yamlStr = `
storage: {root_dir: /tmp/nvr}
auth: {username: u, password_hash: "$2a$10$x"}
streaming:
  default_protocol: webrtc
  webrtc: {max_viewers: 3}
`
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(yamlStr), 0o600))
	cfg, err := Load(path)
	require.NoError(t, err)
	require.Equal(t, 3, cfg.Streaming.WebRTC.MaxViewers)
}

func TestWebRTCMaxViewersTooLow(t *testing.T) {
	cfg := &Config{Streaming: StreamingConfig{WebRTC: WebRTCConfig{MaxViewers: 0}}}
	cfg.ApplyDefaults()
	cfg.Streaming.WebRTC.MaxViewers = 0 // override default
	err := Validate(cfg)
	require.Error(t, err)
	require.Contains(t, err.Error(), "streaming.webrtc.max_viewers")
}

func TestWebRTCMaxViewersTooHigh(t *testing.T) {
	cfg := &Config{Streaming: StreamingConfig{WebRTC: WebRTCConfig{MaxViewers: 11}}}
	cfg.ApplyDefaults()
	err := Validate(cfg)
	require.Error(t, err)
	require.Contains(t, err.Error(), "streaming.webrtc.max_viewers")
}

func TestFLVMaxViewersTooLow(t *testing.T) {
	cfg := &Config{Streaming: StreamingConfig{FLV: FLVConfig{MaxViewers: 0}}}
	cfg.ApplyDefaults()
	cfg.Streaming.FLV.MaxViewers = 0 // override default
	err := Validate(cfg)
	require.Error(t, err)
	require.Contains(t, err.Error(), "streaming.flv.max_viewers")
}

func TestFLVMaxViewersTooHigh(t *testing.T) {
	cfg := &Config{Streaming: StreamingConfig{FLV: FLVConfig{MaxViewers: 51}}}
	cfg.ApplyDefaults()
	err := Validate(cfg)
	require.Error(t, err)
	require.Contains(t, err.Error(), "streaming.flv.max_viewers")
}

func TestFLVGOPCacheSizeNegative(t *testing.T) {
	cfg := &Config{Streaming: StreamingConfig{FLV: FLVConfig{GOPCacheSize: -1}}}
	cfg.ApplyDefaults()
	cfg.Streaming.FLV.GOPCacheSize = -1 // override default
	err := Validate(cfg)
	require.Error(t, err)
	require.Contains(t, err.Error(), "streaming.flv.gop_cache_size")
}

func TestHealthDefaults(t *testing.T) {
	cfg := &Config{}
	cfg.ApplyDefaults()
	require.Equal(t, "720h", cfg.Health.EventsRetention, "default events_retention should be 720h (30 days)")
	require.Equal(t, "5m", cfg.Health.Alerts.Cooldown, "default cooldown should be 5m")
	require.False(t, cfg.Health.Alerts.MQTT, "default mqtt alerts should be false")
	require.Equal(t, "30s", cfg.Health.Layer1.OfflineThreshold, "default offline_threshold should be 30s")
	require.Equal(t, 0.5, cfg.Health.Layer2.BitrateChangeThreshold, "default bitrate_change_threshold should be 0.5")
	require.Equal(t, 5, cfg.Health.Layer2.MinFPS, "default min_fps should be 5")
	require.Equal(t, "60s", cfg.Health.Layer2.MaxIDRInterval, "default max_idr_interval should be 60s")
	require.Equal(t, "10s", cfg.Health.Layer2_5.FreezeTimeout, "default freeze_timeout should be 10s")
	require.Equal(t, "10s", cfg.Health.Layer2_5.FreezeTimeout, "default freeze_timeout should be 10s")
	require.False(t, cfg.Health.AutoRemediation.Enabled, "default auto_remediation should be disabled")
	require.Equal(t, 3, cfg.Health.AutoRemediation.MaxRestartsPerHour, "default max_restarts_per_hour should be 3")
	require.Equal(t, 5, cfg.Health.AutoRemediation.CooldownMinutes, "default cooldown_minutes should be 5")
	require.Equal(t, 1, cfg.Health.AutoRemediation.BlacklistHours, "default blacklist_hours should be 1")
	require.Equal(t, 10, cfg.Health.AutoRemediation.GlobalMaxPerMin, "default global_max_per_min should be 10")
}

func TestHealthValidConfig(t *testing.T) {
	cfg := &Config{
		Health: HealthConfig{
			Enabled:         true,
			EventsRetention: "360h",
			Alerts: HealthAlertsConfig{
				Cooldown: "10m",
				MQTT:     true,
			},
			Layer1: HealthLayer1Config{
				OfflineThreshold: "60s",
			},
			Layer2: HealthLayer2Config{
				BitrateChangeThreshold: 0.3,
				MinFPS:                 10,
				MaxIDRInterval:         "15s",
			},
			Layer2_5: HealthLayer2_5Config{
				FreezeTimeout: "20s",
			},
			AutoRemediation: HealthAutoRemediationConfig{
				Enabled:            true,
				MaxRestartsPerHour: 5,
				CooldownMinutes:    10,
				BlacklistHours:     2,
				GlobalMaxPerMin:    20,
			},
		},
	}
	cfg.ApplyDefaults()
	err := Validate(cfg)
	require.NoError(t, err)
}

func TestHealthValidationInvalidEventsRetention(t *testing.T) {
	cfg := &Config{Health: HealthConfig{Enabled: true, EventsRetention: "not-a-duration"}}
	cfg.ApplyDefaults()
	err := Validate(cfg)
	require.Error(t, err)
	require.Contains(t, err.Error(), "health.events_retention")
}

func TestHealthValidationInvalidCooldown(t *testing.T) {
	cfg := &Config{Health: HealthConfig{Enabled: true, Alerts: HealthAlertsConfig{Cooldown: "bad"}}}
	cfg.ApplyDefaults()
	err := Validate(cfg)
	require.Error(t, err)
	require.Contains(t, err.Error(), "health.alerts.cooldown")
}

func TestHealthValidationInvalidOfflineThreshold(t *testing.T) {
	cfg := &Config{Health: HealthConfig{Enabled: true, Layer1: HealthLayer1Config{OfflineThreshold: "bad"}}}
	cfg.ApplyDefaults()
	err := Validate(cfg)
	require.Error(t, err)
	require.Contains(t, err.Error(), "health.layer1.offline_threshold")
}

func TestHealthValidationInvalidBitrateChangeThreshold(t *testing.T) {
	cfg := &Config{Health: HealthConfig{Enabled: true, Layer2: HealthLayer2Config{BitrateChangeThreshold: 1.5}}}
	cfg.ApplyDefaults()
	err := Validate(cfg)
	require.Error(t, err)
	require.Contains(t, err.Error(), "bitrate_change_threshold")
}

func TestHealthValidationInvalidMinFPS(t *testing.T) {
	cfg := &Config{Health: HealthConfig{Enabled: true, Layer2: HealthLayer2Config{MinFPS: 0}}}
	cfg.ApplyDefaults()
	cfg.Health.Layer2.MinFPS = 0 // override default
	err := Validate(cfg)
	require.Error(t, err)
	require.Contains(t, err.Error(), "min_fps")
}

func TestHealthValidationInvalidMaxIDRInterval(t *testing.T) {
	cfg := &Config{Health: HealthConfig{Enabled: true, Layer2: HealthLayer2Config{MaxIDRInterval: "bad"}}}
	cfg.ApplyDefaults()
	err := Validate(cfg)
	require.Error(t, err)
	require.Contains(t, err.Error(), "health.layer2.max_idr_interval")
}

func TestHealthValidationInvalidFreezeTimeout(t *testing.T) {
	cfg := &Config{Health: HealthConfig{Enabled: true, Layer2_5: HealthLayer2_5Config{FreezeTimeout: "bad"}}}
	cfg.ApplyDefaults()
	err := Validate(cfg)
	require.Error(t, err)
	require.Contains(t, err.Error(), "health.layer2_5.freeze_timeout")
}

func TestHealthValidationDisabledSkips(t *testing.T) {
	cfg := &Config{Health: HealthConfig{Enabled: false, EventsRetention: "bad", Layer1: HealthLayer1Config{OfflineThreshold: "bad"}}}
	cfg.ApplyDefaults()
	err := Validate(cfg)
	require.NoError(t, err, "validation should be skipped when health is disabled")
}

func TestAutoRemediationDefaults(t *testing.T) {
	// When no auto_remediation section in YAML, defaults should apply
	cfg := &Config{}
	cfg.ApplyDefaults()
	require.False(t, cfg.Health.AutoRemediation.Enabled, "auto_remediation should be disabled by default")
	require.Equal(t, 3, cfg.Health.AutoRemediation.MaxRestartsPerHour)
	require.Equal(t, 5, cfg.Health.AutoRemediation.CooldownMinutes)
	require.Equal(t, 1, cfg.Health.AutoRemediation.BlacklistHours)
	require.Equal(t, 10, cfg.Health.AutoRemediation.GlobalMaxPerMin)
}

func TestAutoRemediationConfig(t *testing.T) {
	// When auto_remediation section has explicit values, they should be preserved
	cfg := &Config{
		Health: HealthConfig{
			AutoRemediation: HealthAutoRemediationConfig{
				Enabled:            true,
				MaxRestartsPerHour: 5,
				CooldownMinutes:    10,
				BlacklistHours:     2,
				GlobalMaxPerMin:    20,
			},
		},
	}
	cfg.ApplyDefaults()
	require.True(t, cfg.Health.AutoRemediation.Enabled)
	require.Equal(t, 5, cfg.Health.AutoRemediation.MaxRestartsPerHour)
	require.Equal(t, 10, cfg.Health.AutoRemediation.CooldownMinutes)
	require.Equal(t, 2, cfg.Health.AutoRemediation.BlacklistHours)
	require.Equal(t, 20, cfg.Health.AutoRemediation.GlobalMaxPerMin)
}

func TestAutoRemediationValidation(t *testing.T) {
	t.Run("max_restarts_per_hour = 0 with enabled", func(t *testing.T) {
		cfg := &Config{
			Health: HealthConfig{
				Enabled: true,
				AutoRemediation: HealthAutoRemediationConfig{
					Enabled:            true,
					MaxRestartsPerHour: 0,
					CooldownMinutes:    5,
					BlacklistHours:     1,
					GlobalMaxPerMin:    10,
				},
			},
		}
		cfg.ApplyDefaults()
		cfg.Health.AutoRemediation.MaxRestartsPerHour = 0 // override default
		err := Validate(cfg)
		require.Error(t, err)
		require.Contains(t, err.Error(), "max_restarts_per_hour")
	})

	t.Run("cooldown_minutes = 0 with enabled", func(t *testing.T) {
		cfg := &Config{
			Health: HealthConfig{
				Enabled: true,
				AutoRemediation: HealthAutoRemediationConfig{
					Enabled:            true,
					MaxRestartsPerHour: 3,
					CooldownMinutes:    0,
					BlacklistHours:     1,
					GlobalMaxPerMin:    10,
				},
			},
		}
		cfg.ApplyDefaults()
		cfg.Health.AutoRemediation.CooldownMinutes = 0 // override default
		err := Validate(cfg)
		require.Error(t, err)
		require.Contains(t, err.Error(), "cooldown_minutes")
	})

	t.Run("disabled with zero values passes", func(t *testing.T) {
		cfg := &Config{
			Health: HealthConfig{
				Enabled: true,
				AutoRemediation: HealthAutoRemediationConfig{
					Enabled: false,
				},
			},
		}
		cfg.ApplyDefaults()
		err := Validate(cfg)
		require.NoError(t, err, "validation should pass when auto_remediation is disabled")
	})
}

func TestAudioEnabledDefaultFalse(t *testing.T) {
	cfg := &Config{Cameras: []CameraConfig{{
		ID: "c1", Protocol: "rtsp", Encoding: "h264", URL: "rtsp://192.168.1.10/stream",
	}}}
	cfg.ApplyDefaults()
	require.False(t, cfg.Cameras[0].AudioEnabled, "audio_enabled should default to false")
}

func TestAudioEnabledAllowedForMJPEG(t *testing.T) {
	cfg := &Config{Cameras: []CameraConfig{{
		ID: "c1", Protocol: "rtsp", Encoding: "mjpeg", URL: "rtsp://192.168.1.10/stream",
		AudioEnabled: true,
	}}}
	cfg.ApplyDefaults()
	require.True(t, cfg.Cameras[0].AudioEnabled, "MJPEG cameras now support audio via AVI container")
}

func TestAudioEnabledRejectedForHTTPJPEG(t *testing.T) {
	cfg := &Config{Cameras: []CameraConfig{{
		ID: "c1", Protocol: "http", Encoding: "jpeg", URL: "http://192.168.1.10/capture",
		AudioEnabled: true,
	}}}
	cfg.ApplyDefaults()
	require.False(t, cfg.Cameras[0].AudioEnabled, "HTTP-JPEG cameras have no audio source")
}

func TestAudioEnabledAllowedForRTSPH264(t *testing.T) {
	cfg := &Config{Cameras: []CameraConfig{{
		ID: "c1", Protocol: "rtsp", Encoding: "h264", URL: "rtsp://192.168.1.10/stream",
		AudioEnabled: true,
	}}}
	cfg.ApplyDefaults()
	require.True(t, cfg.Cameras[0].AudioEnabled, "RTSP H.264 cameras should support audio")
}

func TestAudioEnabledAllowedForONVIF(t *testing.T) {
	cfg := &Config{
		Cameras: []CameraConfig{{
			ID: "c1", Protocol: "onvif", Encoding: "h264", URL: "http://192.168.1.100/onvif/device_service",
			AudioEnabled: true,
		}},
	}
	cfg.ApplyDefaults()
	require.True(t, cfg.Cameras[0].AudioEnabled, "ONVIF H.264 cameras should support audio")
}

func TestAudioEnabledAllowedForONVIFJPEG(t *testing.T) {
	// Regression: an ONVIF camera whose profile reports Encoding="jpeg" (e.g. the
	// ESP32 MiBeeCam-S3 with RTSP-AVI firmware) must keep audio_enabled. The device
	// serves MJPEG+G.711 over RTSP and records into AVI; gating audio on the
	// stored encoding would wrongly disable it.
	cfg := &Config{
		Cameras: []CameraConfig{{
			ID: "c1", Protocol: "onvif", Encoding: "jpeg", URL: "http://192.168.1.100/onvif/device_service",
			AudioEnabled: true,
		}},
	}
	cfg.ApplyDefaults()
	require.True(t, cfg.Cameras[0].AudioEnabled, "ONVIF JPEG (MJPEG-over-RTSP) cameras are audio-capable via AVI")
}

func TestAudioEnabledAllowedForXiaomi(t *testing.T) {
	cfg := &Config{
		Cameras: []CameraConfig{{
			ID: "c1", Protocol: "xiaomi", Encoding: "h264", URL: "xiaomi://device",
			AudioEnabled: true,
		}},
		Xiaomi: XiaomiConfig{Token: "test", Region: "cn"},
	}
	cfg.ApplyDefaults()
	require.True(t, cfg.Cameras[0].AudioEnabled, "Xiaomi cameras should support audio")
}

func TestMetricsAuthIsConfigured(t *testing.T) {
	t.Helper()
	require.False(t, MetricsAuthConfig{}.IsConfigured(), "empty config should not be configured")
	require.False(t, MetricsAuthConfig{Username: "user"}.IsConfigured(), "username only should not be configured")
	require.False(t, MetricsAuthConfig{Password: "pass"}.IsConfigured(), "password only should not be configured")
	require.True(t, MetricsAuthConfig{Username: "metrics", Password: "secret"}.IsConfigured(), "username+password should be configured")
	require.True(t, MetricsAuthConfig{Username: "metrics", PasswordHash: "$2a$10$xxxx"}.IsConfigured(), "username+hash should be configured")
}

func TestMetricsAuthInConfigYAML(t *testing.T) {
	t.Helper()
	yaml := `
server:
  listen: ":9090"
auth:
  username: admin
  password: admin12345
metrics_auth:
  username: metrics
  password: metpass
`
	tmp := t.TempDir()
	path := filepath.Join(tmp, "test.yaml")
	require.NoError(t, os.WriteFile(path, []byte(yaml), 0o644))
	cfg, err := Load(path)
	require.NoError(t, err)
	require.Equal(t, "metrics", cfg.MetricsAuth.Username)
	require.Equal(t, "metpass", cfg.MetricsAuth.Password)
	require.True(t, cfg.MetricsAuth.IsConfigured())
}

func TestWebSocketConfig(t *testing.T) {
	yaml := `
websocket:
  max_viewers: 5
  write_buf_size: 200
  idle_timeout: 30s
`
	path := writeTempYAML(t, yaml)
	cfg, err := Load(path)
	require.NoError(t, err)
	require.Equal(t, 5, cfg.WebSocket.MaxViewers)
	require.Equal(t, 200, cfg.WebSocket.WriteBufSize)
	require.Equal(t, 30*time.Second, cfg.WebSocket.IdleTimeout)
}

func TestWebSocketConfigDefaults(t *testing.T) {
	cfg := &Config{}
	cfg.ApplyDefaults()
	require.Equal(t, 10, cfg.WebSocket.MaxViewers)
	require.Equal(t, 100, cfg.WebSocket.WriteBufSize)
	require.Equal(t, 60*time.Second, cfg.WebSocket.IdleTimeout)
}

func TestWebSocketConfigValidation(t *testing.T) {
	t.Run("max_viewers = 0", func(t *testing.T) {
		cfg := &Config{WebSocket: WebSocketConfig{MaxViewers: 0}}
		cfg.ApplyDefaults()
		cfg.WebSocket.MaxViewers = 0 // override default
		err := Validate(cfg)
		require.Error(t, err)
		require.Contains(t, err.Error(), "websocket.max_viewers")
	})
	t.Run("write_buf_size = 0", func(t *testing.T) {
		cfg := &Config{WebSocket: WebSocketConfig{WriteBufSize: 0}}
		cfg.ApplyDefaults()
		cfg.WebSocket.WriteBufSize = 0 // override default
		err := Validate(cfg)
		require.Error(t, err)
		require.Contains(t, err.Error(), "websocket.write_buf_size")
	})
	t.Run("idle_timeout = 0", func(t *testing.T) {
		cfg := &Config{WebSocket: WebSocketConfig{IdleTimeout: 0}}
		cfg.ApplyDefaults()
		cfg.WebSocket.IdleTimeout = 0 // override default
		err := Validate(cfg)
		require.Error(t, err)
		require.Contains(t, err.Error(), "websocket.idle_timeout")
	})
	t.Run("valid config passes", func(t *testing.T) {
		cfg := &Config{WebSocket: WebSocketConfig{MaxViewers: 5, WriteBufSize: 200, IdleTimeout: 30 * time.Second}}
		cfg.ApplyDefaults()
		err := Validate(cfg)
		require.NoError(t, err)
	})
}

func TestAIConfig(t *testing.T) {
	yaml := `
ai:
  frame_skip_rate: 3
  confidence_threshold: 0.5
`
	path := writeTempYAML(t, yaml)
	cfg, err := Load(path)
	require.NoError(t, err)
	require.Equal(t, 3, cfg.AI.FrameSkipRate)
	require.Equal(t, 0.5, cfg.AI.ConfidenceThreshold)
}

func TestAIConfigDefaults(t *testing.T) {
	cfg := &Config{}
	cfg.ApplyDefaults()
	require.Equal(t, 0.5, cfg.AI.ConfidenceThreshold)
	require.Equal(t, 10, cfg.AI.FrameSkipRate)
	require.NotNil(t, cfg.AI.Zones)
	require.Empty(t, cfg.AI.EnabledCameras)
}

func TestParseMergeDuration_Valid(t *testing.T) {
	tests := []struct {
		input    string
		expected time.Duration
	}{
		{"", time.Hour}, // empty defaults to 1h
		{"1h", time.Hour},
		{"30m", 30 * time.Minute},
		{"15m", 15 * time.Minute},
		{"10m", 10 * time.Minute},
		{"5m", 5 * time.Minute},
		// Named windows — natively supported (Timelapse v3 lifted the 1h cap).
		{"natural-day", 24 * time.Hour},
		{"24h", 24 * time.Hour}, // alias of natural-day
		{"8h", 8 * time.Hour},
		{"12h", 12 * time.Hour},
		{"7d", 7 * 24 * time.Hour},
		{"30d", 30 * 24 * time.Hour},
		// Arbitrary Go durations are also accepted (positive, ≤ 30d).
		{"2h", 2 * time.Hour},
		{"6h", 6 * time.Hour},
		{"45m", 45 * time.Minute},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			dur, err := ParseMergeDuration(tt.input)
			require.NoError(t, err)
			require.Equal(t, tt.expected, dur)
		})
	}
}

// TestParseMergeDuration_NamedWindows ensures the six canonical named windows
// resolve to their full durations (not capped to 1h). These align in the
// configured app timezone via timelapse.parseMergeRange / computeNextRun.
func TestParseMergeDuration_NamedWindows(t *testing.T) {
	cases := map[string]time.Duration{
		"natural-day": 24 * time.Hour,
		"24h":         24 * time.Hour,
		"8h":          8 * time.Hour,
		"12h":         12 * time.Hour,
		"7d":          7 * 24 * time.Hour,
		"30d":         30 * 24 * time.Hour,
	}
	for input, expected := range cases {
		t.Run(input, func(t *testing.T) {
			dur, err := ParseMergeDuration(input)
			require.NoError(t, err)
			require.Equal(t, expected, dur, "%s must resolve to full duration, not 1h", input)
		})
	}
}

func TestParseMergeDuration_Invalid(t *testing.T) {
	// Garbage input
	_, err := ParseMergeDuration("invalid")
	require.Error(t, err)
	// Values >30d are rejected (30d is the largest named window / alignment cap).
	_, err = ParseMergeDuration("744h") // 31d > 30d cap
	require.Error(t, err)
	require.Contains(t, err.Error(), "≤ 30d")
	// Zero / negative are rejected
	_, err = ParseMergeDuration("0")
	require.Error(t, err)
	_, err = ParseMergeDuration("-1h")
	require.Error(t, err)
}

func TestValidateTimelapseMergeDuration_Valid(t *testing.T) {
	// All six named windows + arbitrary Go durations ≤30d are valid.
	for _, md := range []string{"1h", "8h", "12h", "24h", "natural-day", "7d", "30d", "30m", "6h"} {
		cfg := &Config{
			Cameras: []CameraConfig{{
				ID: "c1", Protocol: "rtsp", URL: "rtsp://192.168.1.10/stream",
				Timelapse: &CameraTimelapseConfig{
					MergeDuration: md,
				},
			}},
		}
		cfg.ApplyDefaults()
		// Re-assert the value — ApplyDefaults must not overwrite an explicit MergeDuration.
		require.Equal(t, md, cfg.Cameras[0].Timelapse.MergeDuration)
		err := Validate(cfg)
		require.NoError(t, err, "merge_duration=%s should be valid", md)
	}
}

// TestApplyDefaults_TimelapseMergeDuration_Default verifies that a camera with
// timelapse enabled but no explicit merge_duration gets the natural-day
// default (24h, midnight-aligned) — matching the DB schema default and the
// API handler fallbacks.
func TestApplyDefaults_TimelapseMergeDuration_Default(t *testing.T) {
	cfg := &Config{
		Cameras: []CameraConfig{{
			ID: "c1", Protocol: "rtsp", URL: "rtsp://192.168.1.10/stream",
			Timelapse: &CameraTimelapseConfig{
				Enabled: true,
				// MergeDuration intentionally unset
			},
		}},
	}
	cfg.ApplyDefaults()
	require.Equal(t, "natural-day", cfg.Cameras[0].Timelapse.MergeDuration)
}

func TestValidateTimelapseMergeDuration_Invalid(t *testing.T) {
	cfg := &Config{
		Cameras: []CameraConfig{{
			ID: "c1", Protocol: "rtsp", URL: "rtsp://192.168.1.10/stream",
			Timelapse: &CameraTimelapseConfig{
				MergeDuration: "invalid",
			},
		}},
	}
	cfg.ApplyDefaults()
	// Override the default that ApplyDefaults would set
	cfg.Cameras[0].Timelapse.MergeDuration = "invalid"
	err := Validate(cfg)
	require.Error(t, err)
	require.Contains(t, err.Error(), "merge_duration")
}

func TestValidate_Timezone_Valid(t *testing.T) {
	tests := []struct {
		name     string
		timezone string
	}{
		{name: "Local timezone", timezone: "Local"},
		{name: "UTC timezone", timezone: "UTC"},
		{name: "Asia/Shanghai", timezone: "Asia/Shanghai"},
		{name: "America/New_York", timezone: "America/New_York"},
		{name: "empty timezone", timezone: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Helper()
			cfg := &Config{Timezone: tt.timezone}
			cfg.ApplyDefaults()
			err := Validate(cfg)
			require.NoError(t, err)
		})
	}
}

func TestValidate_Timezone_Invalid(t *testing.T) {
	cfg := &Config{Timezone: "Invalid/TZ"}
	cfg.ApplyDefaults()
	err := Validate(cfg)
	require.Error(t, err)
	require.Contains(t, err.Error(), "timezone")
}

func TestPushTarget_ValidFullConfig(t *testing.T) {
	cfg := &Config{
		Cameras: []CameraConfig{{
			ID: "cam1", URL: "rtsp://example/stream", Protocol: "rtsp", Encoding: "h264",
			PushTargets: []PushTargetConfig{{
				ID: "t1", Name: "YouTube", Protocol: "rtmp", URL: "rtmp://a.live/youtube/key", Enabled: true,
				Platform: "youtube", TranscodePolicy: "auto",
				VideoPresetOverride: &VideoPresetOverrides{
					Resolution: "1920x1080", Framerate: 30, VideoBitrateKbps: 4500,
					GopSeconds: 2, Profile: "high", Bframes: 1,
				},
			}},
		}},
	}
	cfg.ApplyDefaults()
	err := Validate(cfg)
	require.NoError(t, err)
}

func TestPushTarget_EmptyPlatform(t *testing.T) {
	cfg := &Config{
		Cameras: []CameraConfig{{
			ID: "cam1", URL: "rtsp://example/stream", Protocol: "rtsp", Encoding: "h264",
			PushTargets: []PushTargetConfig{{
				ID: "t1", Name: "Generic", Protocol: "rtmp", URL: "rtmp://h/live/k", Enabled: true,
				Platform: "", TranscodePolicy: "",
			}},
		}},
	}
	cfg.ApplyDefaults()
	err := Validate(cfg)
	require.NoError(t, err)
}

func TestPushTarget_InvalidPlatform(t *testing.T) {
	cfg := &Config{
		Cameras: []CameraConfig{{
			ID: "cam1", URL: "rtsp://example/stream", Protocol: "rtsp", Encoding: "h264",
			PushTargets: []PushTargetConfig{{
				ID: "t1", Protocol: "rtmp", URL: "rtmp://h/live/k", Enabled: true,
				Platform: "bad platform!",
			}},
		}},
	}
	cfg.ApplyDefaults()
	err := Validate(cfg)
	require.ErrorContains(t, err, "platform")
}

func TestPushTarget_InvalidTranscodePolicy(t *testing.T) {
	cfg := &Config{
		Cameras: []CameraConfig{{
			ID: "cam1", URL: "rtsp://example/stream", Protocol: "rtsp", Encoding: "h264",
			PushTargets: []PushTargetConfig{{
				ID: "t1", Protocol: "rtmp", URL: "rtmp://h/live/k", Enabled: true,
				TranscodePolicy: "invalid",
			}},
		}},
	}
	cfg.ApplyDefaults()
	err := Validate(cfg)
	require.ErrorContains(t, err, "transcode_policy")
}

func TestPushTarget_InvalidResolution(t *testing.T) {
	cfg := &Config{
		Cameras: []CameraConfig{{
			ID: "cam1", URL: "rtsp://example/stream", Protocol: "rtsp", Encoding: "h264",
			PushTargets: []PushTargetConfig{{
				ID: "t1", Protocol: "rtmp", URL: "rtmp://h/live/k", Enabled: true,
				VideoPresetOverride: &VideoPresetOverrides{Resolution: "bad"},
			}},
		}},
	}
	cfg.ApplyDefaults()
	err := Validate(cfg)
	require.ErrorContains(t, err, "resolution")
}

func TestPushTarget_InvalidBitrateLow(t *testing.T) {
	cfg := &Config{
		Cameras: []CameraConfig{{
			ID: "cam1", URL: "rtsp://example/stream", Protocol: "rtsp", Encoding: "h264",
			PushTargets: []PushTargetConfig{{
				ID: "t1", Protocol: "rtmp", URL: "rtmp://h/live/k", Enabled: true,
				VideoPresetOverride: &VideoPresetOverrides{VideoBitrateKbps: 50},
			}},
		}},
	}
	cfg.ApplyDefaults()
	err := Validate(cfg)
	require.ErrorContains(t, err, "bitrate")
}

func TestPushTarget_InvalidBitrateHigh(t *testing.T) {
	cfg := &Config{
		Cameras: []CameraConfig{{
			ID: "cam1", URL: "rtsp://example/stream", Protocol: "rtsp", Encoding: "h264",
			PushTargets: []PushTargetConfig{{
				ID: "t1", Protocol: "rtmp", URL: "rtmp://h/live/k", Enabled: true,
				VideoPresetOverride: &VideoPresetOverrides{VideoBitrateKbps: 99999},
			}},
		}},
	}
	cfg.ApplyDefaults()
	err := Validate(cfg)
	require.ErrorContains(t, err, "bitrate")
}

func TestPushTarget_InvalidGop(t *testing.T) {
	cfg := &Config{
		Cameras: []CameraConfig{{
			ID: "cam1", URL: "rtsp://example/stream", Protocol: "rtsp", Encoding: "h264",
			PushTargets: []PushTargetConfig{{
				ID: "t1", Protocol: "rtmp", URL: "rtmp://h/live/k", Enabled: true,
				VideoPresetOverride: &VideoPresetOverrides{GopSeconds: 999},
			}},
		}},
	}
	cfg.ApplyDefaults()
	err := Validate(cfg)
	require.ErrorContains(t, err, "gop_seconds")
}

func TestPushTarget_InvalidProfile(t *testing.T) {
	cfg := &Config{
		Cameras: []CameraConfig{{
			ID: "cam1", URL: "rtsp://example/stream", Protocol: "rtsp", Encoding: "h264",
			PushTargets: []PushTargetConfig{{
				ID: "t1", Protocol: "rtmp", URL: "rtmp://h/live/k", Enabled: true,
				VideoPresetOverride: &VideoPresetOverrides{Profile: "badprofile"},
			}},
		}},
	}
	cfg.ApplyDefaults()
	err := Validate(cfg)
	require.ErrorContains(t, err, "profile")
}

func TestPushTarget_InvalidBframes(t *testing.T) {
	cfg := &Config{
		Cameras: []CameraConfig{{
			ID: "cam1", URL: "rtsp://example/stream", Protocol: "rtsp", Encoding: "h264",
			PushTargets: []PushTargetConfig{{
				ID: "t1", Protocol: "rtmp", URL: "rtmp://h/live/k", Enabled: true,
				VideoPresetOverride: &VideoPresetOverrides{Bframes: 999},
			}},
		}},
	}
	cfg.ApplyDefaults()
	err := Validate(cfg)
	require.ErrorContains(t, err, "bframes")
}

func TestPushTarget_NilVideoPresetOverride(t *testing.T) {
	cfg := &Config{
		Cameras: []CameraConfig{{
			ID: "cam1", URL: "rtsp://example/stream", Protocol: "rtsp", Encoding: "h264",
			PushTargets: []PushTargetConfig{{
				ID: "t1", Protocol: "rtmp", URL: "rtmp://h/live/k", Enabled: true,
				Platform: "bilibili", TranscodePolicy: "force_sw",
				// VideoPresetOverride is nil — should pass
			}},
		}},
	}
	cfg.ApplyDefaults()
	err := Validate(cfg)
	require.NoError(t, err)
}

func TestConfigExtensions(t *testing.T) {
	dir := t.TempDir()

	// Test with extensions
	yamlContent := `
server:
  listen: ":9090"
extensions:
  example_key: example_value
  count: 42
  nested:
    sub_key: sub_value
`
	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(yamlContent), 0o644))

	cfg, err := Load(path)
	require.NoError(t, err)
	require.NotNil(t, cfg.Extensions)
	assert.Equal(t, "example_value", cfg.Extensions["example_key"])
	assert.Equal(t, 42, cfg.Extensions["count"])

	// Test without extensions
	yamlContent2 := `
server:
  listen: ":9090"
`
	path2 := filepath.Join(dir, "config2.yaml")
	require.NoError(t, os.WriteFile(path2, []byte(yamlContent2), 0o644))

	cfg2, err := Load(path2)
	require.NoError(t, err)
	assert.Nil(t, cfg2.Extensions)
}

// TestValidate_CameraRingBufCap verifies the per-camera ring_buf_cap bounds
// (issue #521): 0 (default) and the sane positive range pass, negative or
// absurd values are rejected.
func TestValidate_CameraRingBufCap(t *testing.T) {
	t.Helper()
	base := func(capacity int) *Config {
		cfg := &Config{}
		cfg.ApplyDefaults()
		cfg.Cameras = []CameraConfig{{
			ID: "cam-a", Name: "A", Protocol: "rtsp", Encoding: "h264",
			URL: "rtsp://127.0.0.1:8554/a", RingBufCap: capacity,
		}}
		return cfg
	}
	require.NoError(t, Validate(base(0)))
	require.NoError(t, Validate(base(300)))
	require.NoError(t, Validate(base(10000)))
	err := Validate(base(-1))
	require.ErrorContains(t, err, "ring_buf_cap")
	err = Validate(base(10001))
	require.ErrorContains(t, err, "ring_buf_cap")
}

func TestMQTTStatusEventsConfig(t *testing.T) {
	t.Helper()

	// Round-trip: status_events survives Save/Load.
	dir := t.TempDir()
	path := filepath.Join(dir, "mibee-nvr.yaml")
	original := &Config{
		MQTT: MQTTConfig{
			Enabled:      true,
			Broker:       "tcp://mqtt.local:1883",
			Topic:        "mibee",
			ClientID:     "mibee-nvr",
			StatusEvents: true,
		},
	}
	original.ApplyDefaults()
	require.NoError(t, Save(path, original))

	loaded, err := Load(path)
	require.NoError(t, err)
	require.True(t, loaded.MQTT.StatusEvents, "status_events=true should round-trip")

	// Default: opt-in, off unless explicitly enabled.
	cfg := &Config{}
	cfg.ApplyDefaults()
	require.False(t, cfg.MQTT.StatusEvents, "default mqtt.status_events should be false")
}
