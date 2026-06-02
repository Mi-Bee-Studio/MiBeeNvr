package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTranscodingConfig_Defaults(t *testing.T) {
	t.Parallel()
	cfg := &Config{}
	cfg.ApplyDefaults()

	require.False(t, cfg.Transcoding.Enabled, "transcoding.enabled should default to false")
	require.Equal(t, 1, cfg.Transcoding.MaxWorkers, "transcoding.max_workers should default to 1")
	require.False(t, cfg.Transcoding.ReplaceOriginal, "transcoding.replace_original should default to false")
	require.Empty(t, cfg.Transcoding.FFmpegPath, "transcoding.ffmpeg_path should default to empty (auto-detect)")
	require.Empty(t, cfg.Transcoding.DownloadURL, "transcoding.download_url should default to empty")
}

func TestTranscodingConfig_InvalidMaxWorkers(t *testing.T) {
	t.Parallel()
	cfg := &Config{}
	cfg.ApplyDefaults()
	cfg.Transcoding.MaxWorkers = 5 // > 4
	err := Validate(cfg)
	require.Error(t, err)
	require.Contains(t, err.Error(), "transcoding.max_workers")
}

func TestTranscodingConfig_InvalidMaxWorkersZero(t *testing.T) {
	t.Parallel()
	cfg := &Config{}
	cfg.ApplyDefaults()
	cfg.Transcoding.MaxWorkers = 0 // < 1
	err := Validate(cfg)
	require.Error(t, err)
	require.Contains(t, err.Error(), "transcoding.max_workers")
}

func TestTranscodingConfig_InvalidMaxWorkersNegative(t *testing.T) {
	t.Parallel()
	cfg := &Config{}
	cfg.ApplyDefaults()
	cfg.Transcoding.MaxWorkers = -1 // < 0
	err := Validate(cfg)
	require.Error(t, err)
	require.Contains(t, err.Error(), "transcoding.max_workers")
}

func TestTranscodingConfig_InvalidTargetCodec(t *testing.T) {
	t.Parallel()
	cfg := &Config{
		Cameras: []CameraConfig{{
			ID: "cam1", Protocol: "rtsp", Encoding: "h264", URL: "rtsp://192.168.1.10/stream",
			Transcoding: &CameraTranscodingConfig{TargetCodec: "invalid"},
		}},
	}
	cfg.ApplyDefaults()
	err := Validate(cfg)
	require.Error(t, err)
	require.Contains(t, err.Error(), "target_codec")
}

func TestTranscodingConfig_InvalidPreset(t *testing.T) {
	t.Parallel()
	cfg := &Config{
		Cameras: []CameraConfig{{
			ID: "cam1", Protocol: "rtsp", Encoding: "h264", URL: "rtsp://192.168.1.10/stream",
			Transcoding: &CameraTranscodingConfig{Preset: "veryfast"},
		}},
	}
	cfg.ApplyDefaults()
	err := Validate(cfg)
	require.Error(t, err)
	require.Contains(t, err.Error(), "preset")
}

func TestTranscodingConfig_ValidConfig(t *testing.T) {
	t.Parallel()
	cfg := &Config{
		Transcoding: TranscodingConfig{
			Enabled:         true,
			MaxWorkers:      2,
			ReplaceOriginal: true,
		},
		Cameras: []CameraConfig{{
			ID: "cam1", Protocol: "rtsp", Encoding: "h264", URL: "rtsp://192.168.1.10/stream",
			Transcoding: &CameraTranscodingConfig{
				Enabled:     true,
				TargetCodec: "h265",
				Preset:      "faster",
				Bitrate:     "4M",
			},
		}},
	}
	cfg.ApplyDefaults()
	err := Validate(cfg)
	require.NoError(t, err)
}

func TestTranscodingConfig_ValidEmptyPerCamera(t *testing.T) {
	t.Parallel()
	// Per-camera transcoding with only TargetCodec set (partial override) should pass
	cfg := &Config{
		Transcoding: TranscodingConfig{
			Enabled:    true,
			MaxWorkers: 2,
		},
		Cameras: []CameraConfig{{
			ID: "cam1", Protocol: "rtsp", Encoding: "h264", URL: "rtsp://192.168.1.10/stream",
			Transcoding: &CameraTranscodingConfig{
				TargetCodec: "h265", // partial override, no Preset/Bitrate
			},
		}},
	}
	cfg.ApplyDefaults()
	err := Validate(cfg)
	require.NoError(t, err)
}

func TestResolveTranscodingConfig_GlobalOnly(t *testing.T) {
	t.Parallel()
	cfg := &Config{
		Transcoding: TranscodingConfig{Enabled: true},
		Cameras: []CameraConfig{{
			ID: "cam1", Protocol: "rtsp", Encoding: "h264", URL: "rtsp://192.168.1.10/stream",
			// No Transcoding override
		}},
	}
	cfg.ApplyDefaults()
	result := cfg.ResolveTranscodingConfig("cam1")
	require.NotNil(t, result)
	require.True(t, result.Enabled) // inherits global
	require.Empty(t, result.TargetCodec)
	require.Empty(t, result.Preset)
	require.Empty(t, result.Bitrate)
}

func TestResolveTranscodingConfig_PerCameraOverride(t *testing.T) {
	t.Parallel()
	cfg := &Config{
		Transcoding: TranscodingConfig{Enabled: false}, // global disabled
		Cameras: []CameraConfig{{
			ID: "cam1", Protocol: "rtsp", Encoding: "h264", URL: "rtsp://192.168.1.10/stream",
			Transcoding: &CameraTranscodingConfig{
				Enabled:     true, // per-camera enabled
				TargetCodec: "h265",
				Preset:      "ultrafast",
				Bitrate:     "2M",
			},
		}},
	}
	cfg.ApplyDefaults()
	result := cfg.ResolveTranscodingConfig("cam1")
	require.NotNil(t, result)
	require.True(t, result.Enabled) // per-camera overrides global
	require.Equal(t, "h265", result.TargetCodec)
	require.Equal(t, "ultrafast", result.Preset)
	require.Equal(t, "2M", result.Bitrate)
}

func TestResolveTranscodingConfig_PerCameraDisabled(t *testing.T) {
	t.Parallel()
	cfg := &Config{
		Transcoding: TranscodingConfig{Enabled: true}, // global enabled
		Cameras: []CameraConfig{{
			ID: "cam1", Protocol: "rtsp", Encoding: "h264", URL: "rtsp://192.168.1.10/stream",
			Transcoding: &CameraTranscodingConfig{
				Enabled: false, // per-camera disabled
			},
		}},
	}
	cfg.ApplyDefaults()
	result := cfg.ResolveTranscodingConfig("cam1")
	require.NotNil(t, result)
	require.False(t, result.Enabled) // per-camera overrides to disabled
}

func TestResolveTranscodingConfig_NonExistentCamera(t *testing.T) {
	t.Parallel()
	cfg := &Config{
		Transcoding: TranscodingConfig{Enabled: true, MaxWorkers: 2},
	}
	cfg.ApplyDefaults()
	result := cfg.ResolveTranscodingConfig("nonexistent")
	require.NotNil(t, result)
	require.True(t, result.Enabled)
	require.Empty(t, result.TargetCodec)
	require.Empty(t, result.Preset)
	require.Empty(t, result.Bitrate)
}

func TestTranscodingEnabledFalse_ValidatesClean(t *testing.T) {
	t.Parallel()
	cfg := &Config{
		Transcoding: TranscodingConfig{
			Enabled: false, // explicitly disabled
		},
		Cameras: []CameraConfig{{
			ID: "cam1", Protocol: "rtsp", Encoding: "h264", URL: "rtsp://192.168.1.10/stream",
		}},
	}
	cfg.ApplyDefaults()
	err := Validate(cfg)
	require.NoError(t, err)
}

func TestTranscodingConfig_ValidMaxWorkersRange(t *testing.T) {
	t.Parallel()
	for _, w := range []int{1, 2, 3, 4} {
		cfg := &Config{Transcoding: TranscodingConfig{MaxWorkers: w}}
		cfg.ApplyDefaults()
		err := Validate(cfg)
		require.NoError(t, err, "max_workers=%d should be valid", w)
	}
}

func TestTranscodingConfig_ValidCodecs(t *testing.T) {
	t.Parallel()
	for _, codec := range []string{"h264", "h265"} {
		cfg := &Config{
			Cameras: []CameraConfig{{
				ID: "cam1", Protocol: "rtsp", Encoding: "h264", URL: "rtsp://192.168.1.10/stream",
				Transcoding: &CameraTranscodingConfig{TargetCodec: codec},
			}},
		}
		cfg.ApplyDefaults()
		err := Validate(cfg)
		require.NoError(t, err, "target_codec=%s should be valid", codec)
	}
}

func TestTranscodingConfig_ValidPresets(t *testing.T) {
	t.Parallel()
	for _, preset := range []string{"ultrafast", "faster", "medium"} {
		cfg := &Config{
			Cameras: []CameraConfig{{
				ID: "cam1", Protocol: "rtsp", Encoding: "h264", URL: "rtsp://192.168.1.10/stream",
				Transcoding: &CameraTranscodingConfig{Preset: preset},
			}},
		}
		cfg.ApplyDefaults()
		err := Validate(cfg)
		require.NoError(t, err, "preset=%s should be valid", preset)
	}
}
