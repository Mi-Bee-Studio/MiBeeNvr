package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTimelapseConfig_Defaults(t *testing.T) {
	t.Parallel()
	cfg := &Config{Cameras: []CameraConfig{{
		ID: "cam1", Protocol: "rtsp", Encoding: "h264", URL: "rtsp://192.168.1.10/stream",
		Timelapse: &CameraTimelapseConfig{
			Enabled: true,
		},
	}}}
	cfg.ApplyDefaults()

	require.Equal(t, "30s", cfg.Cameras[0].Timelapse.Interval)
	require.Equal(t, 30, cfg.Cameras[0].Timelapse.OutputFPS)
	require.Equal(t, "h264", cfg.Cameras[0].Timelapse.VideoCodec)
	require.False(t, cfg.Cameras[0].Timelapse.DeleteOriginal)
}

func TestTimelapseConfig_DefaultsWithExplicitValues(t *testing.T) {
	t.Parallel()
	cfg := &Config{Cameras: []CameraConfig{{
		ID: "cam1", Protocol: "rtsp", Encoding: "h264", URL: "rtsp://192.168.1.10/stream",
		Timelapse: &CameraTimelapseConfig{
			Enabled:        true,
			Interval:       "10s",
			OutputFPS:      15,
			VideoCodec:     "h265",
			DeleteOriginal: true,
		},
	}}}
	cfg.ApplyDefaults()

	require.Equal(t, "10s", cfg.Cameras[0].Timelapse.Interval)
	require.Equal(t, 15, cfg.Cameras[0].Timelapse.OutputFPS)
	require.Equal(t, "h265", cfg.Cameras[0].Timelapse.VideoCodec)
	require.True(t, cfg.Cameras[0].Timelapse.DeleteOriginal)
}

func TestTimelapseConfig_NilIsValid(t *testing.T) {
	t.Parallel()
	// Per-camera timelapse is nil — should pass validation
	cfg := &Config{Cameras: []CameraConfig{{
		ID: "cam1", Protocol: "rtsp", Encoding: "h264", URL: "rtsp://192.168.1.10/stream",
		// Timelapse intentionally nil
	}}}
	cfg.ApplyDefaults()
	err := Validate(cfg)
	require.NoError(t, err)
}

func TestTimelapseConfig_InvalidInterval(t *testing.T) {
	t.Parallel()
	cfg := &Config{Cameras: []CameraConfig{{
		ID: "cam1", Protocol: "rtsp", Encoding: "h264", URL: "rtsp://192.168.1.10/stream",
		Timelapse: &CameraTimelapseConfig{
			Enabled:  true,
			Interval: "500ms", // < 1s
		},
	}}}
	cfg.ApplyDefaults()
	err := Validate(cfg)
	require.Error(t, err)
	require.Contains(t, err.Error(), "timelapse.interval")
}

func TestTimelapseConfig_InvalidOutputFPS_TooLow(t *testing.T) {
	t.Parallel()
	cfg := &Config{Cameras: []CameraConfig{{
		ID: "cam1", Protocol: "rtsp", Encoding: "h264", URL: "rtsp://192.168.1.10/stream",
		Timelapse: &CameraTimelapseConfig{
			Enabled:   true,
			OutputFPS: -1, // deprecated, will be reset to 0
		},
	}}}
	cfg.ApplyDefaults()
	err := Validate(cfg)
	require.NoError(t, err)
	require.Equal(t, 0, cfg.Cameras[0].Timelapse.OutputFPS, "out-of-range OutputFPS should be reset to 0")
}

func TestTimelapseConfig_InvalidOutputFPS_TooHigh(t *testing.T) {
	t.Parallel()
	cfg := &Config{Cameras: []CameraConfig{{
		ID: "cam1", Protocol: "rtsp", Encoding: "h264", URL: "rtsp://192.168.1.10/stream",
		Timelapse: &CameraTimelapseConfig{
			Enabled:   true,
			OutputFPS: 120, // deprecated, will be reset to 0
		},
	}}}
	cfg.ApplyDefaults()
	err := Validate(cfg)
	require.NoError(t, err)
	require.Equal(t, 0, cfg.Cameras[0].Timelapse.OutputFPS, "out-of-range OutputFPS should be reset to 0")
}

func TestTimelapseConfig_InvalidVideoCodec(t *testing.T) {
	t.Parallel()
	cfg := &Config{Cameras: []CameraConfig{{
		ID: "cam1", Protocol: "rtsp", Encoding: "h264", URL: "rtsp://192.168.1.10/stream",
		Timelapse: &CameraTimelapseConfig{
			Enabled:    true,
			VideoCodec: "vp9", // deprecated, will be reset to empty
		},
	}}}
	cfg.ApplyDefaults()
	err := Validate(cfg)
	require.NoError(t, err)
	require.Equal(t, "", cfg.Cameras[0].Timelapse.VideoCodec, "non-empty VideoCodec should be reset to empty")
}

func TestTimelapseConfig_ValidCodecs(t *testing.T) {
	t.Parallel()
	for _, codec := range []string{"h264", "h265"} {
		cfg := &Config{Cameras: []CameraConfig{{
			ID: "cam1", Protocol: "rtsp", Encoding: "h264", URL: "rtsp://192.168.1.10/stream",
			Timelapse: &CameraTimelapseConfig{
				Enabled:    true,
				VideoCodec: codec,
			},
		}}}
		cfg.ApplyDefaults()
		err := Validate(cfg)
		require.NoError(t, err, "video_codec=%s should be valid", codec)
	}
}

func TestTimelapseConfig_ValidOutputFPSRange(t *testing.T) {
	t.Parallel()
	for _, fps := range []int{1, 15, 30, 60} {
		cfg := &Config{Cameras: []CameraConfig{{
			ID: "cam1", Protocol: "rtsp", Encoding: "h264", URL: "rtsp://192.168.1.10/stream",
			Timelapse: &CameraTimelapseConfig{
				Enabled:   true,
				OutputFPS: fps,
			},
		}}}
		cfg.ApplyDefaults()
		err := Validate(cfg)
		require.NoError(t, err, "output_fps=%d should be valid", fps)
	}
}

func TestTimelapseConfig_DisableDoesNotValidateTimelapse(t *testing.T) {
	t.Parallel()
	// When timelapse.Enabled is false, validation should still run
	// (unset values get defaults, which are valid)
	cfg := &Config{Cameras: []CameraConfig{{
		ID: "cam1", Protocol: "rtsp", Encoding: "h264", URL: "rtsp://192.168.1.10/stream",
		Timelapse: &CameraTimelapseConfig{
			Enabled: false,
		},
	}}}
	cfg.ApplyDefaults()
	err := Validate(cfg)
	require.NoError(t, err)
}

func TestTimelapseMergeConfig_Defaults(t *testing.T) {
	t.Parallel()
	cfg := &Config{Cameras: []CameraConfig{{
		ID: "cam1", Protocol: "rtsp", Encoding: "h264", URL: "rtsp://192.168.1.10/stream",
		Timelapse: &CameraTimelapseConfig{
			Enabled: true,
		},
	}}}
	cfg.ApplyDefaults()

	require.Equal(t, "auto", cfg.Cameras[0].Timelapse.MergeMode)
	require.NotNil(t, cfg.Cameras[0].Timelapse.DailyMerge)
	require.True(t, *cfg.Cameras[0].Timelapse.DailyMerge)
	require.Equal(t, 30, cfg.Cameras[0].Timelapse.MergeOutputFPS)
	require.Nil(t, cfg.Cameras[0].Timelapse.MergeEnabled, "merge_enabled should be nil (auto-detect)")
}

func TestTimelapseMergeConfig_DefaultsWithExplicitValues(t *testing.T) {
	t.Parallel()
	v := false
	cfg := &Config{Cameras: []CameraConfig{{
		ID: "cam1", Protocol: "rtsp", Encoding: "h264", URL: "rtsp://192.168.1.10/stream",
		Timelapse: &CameraTimelapseConfig{
			Enabled:        true,
			MergeMode:      "mp4",
			DailyMerge:     &v,
			MergeOutputFPS: 15,
			MergeEnabled:   &v,
		},
	}}}
	cfg.ApplyDefaults()

	require.Equal(t, "mp4", cfg.Cameras[0].Timelapse.MergeMode)
	require.False(t, *cfg.Cameras[0].Timelapse.DailyMerge)
	require.Equal(t, 15, cfg.Cameras[0].Timelapse.MergeOutputFPS)
	require.False(t, *cfg.Cameras[0].Timelapse.MergeEnabled)
}

func TestTimelapseMergeConfig_Validation(t *testing.T) {
	t.Parallel()

	t.Run("invalid merge_mode", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{Cameras: []CameraConfig{{
			ID: "cam1", Protocol: "rtsp", Encoding: "h264", URL: "rtsp://192.168.1.10/stream",
			Timelapse: &CameraTimelapseConfig{
				Enabled:        true,
				MergeMode:      "invalid",
				MergeOutputFPS: 30,
			},
		}}}
		cfg.ApplyDefaults()
		err := Validate(cfg)
		require.Error(t, err)
		require.Contains(t, err.Error(), "merge_mode")
	})

	t.Run("valid merge_mode values", func(t *testing.T) {
		t.Parallel()
		for _, mode := range []string{"auto", "mp4", "jpeg"} {
			cfg := &Config{Cameras: []CameraConfig{{
				ID: "cam1", Protocol: "rtsp", Encoding: "h264", URL: "rtsp://192.168.1.10/stream",
				Timelapse: &CameraTimelapseConfig{
					Enabled:        true,
					MergeMode:      mode,
					MergeOutputFPS: 30,
				},
			}}}
			cfg.ApplyDefaults()
			err := Validate(cfg)
			require.NoError(t, err, "merge_mode=%s should be valid", mode)
		}
	})

	t.Run("merge_output_fps zero is invalid", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{Cameras: []CameraConfig{{
			ID: "cam1", Protocol: "rtsp", Encoding: "h264", URL: "rtsp://192.168.1.10/stream",
			Timelapse: &CameraTimelapseConfig{
				Enabled:        true,
				MergeMode:      "auto",
				MergeOutputFPS: 0,
			},
		}}}
		cfg.ApplyDefaults()
		// ApplyDefaults sets 0 to 30, so override back to 0 for validation check
		cfg.Cameras[0].Timelapse.MergeOutputFPS = 0
		err := Validate(cfg)
		require.Error(t, err)
		require.Contains(t, err.Error(), "merge_output_fps")
	})

	t.Run("merge_output_fps negative is invalid", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{Cameras: []CameraConfig{{
			ID: "cam1", Protocol: "rtsp", Encoding: "h264", URL: "rtsp://192.168.1.10/stream",
			Timelapse: &CameraTimelapseConfig{
				Enabled:        true,
				MergeMode:      "auto",
				MergeOutputFPS: -5,
			},
		}}}
		cfg.ApplyDefaults()
		err := Validate(cfg)
		require.Error(t, err)
		require.Contains(t, err.Error(), "merge_output_fps")
	})

	t.Run("merge_output_fps too high is invalid", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{Cameras: []CameraConfig{{
			ID: "cam1", Protocol: "rtsp", Encoding: "h264", URL: "rtsp://192.168.1.10/stream",
			Timelapse: &CameraTimelapseConfig{
				Enabled:        true,
				MergeMode:      "auto",
				MergeOutputFPS: 120,
			},
		}}}
		cfg.ApplyDefaults()
		err := Validate(cfg)
		require.Error(t, err)
		require.Contains(t, err.Error(), "merge_output_fps")
	})

	t.Run("merge_output_fps range is valid", func(t *testing.T) {
		t.Parallel()
		for _, fps := range []int{1, 15, 30, 60} {
			cfg := &Config{Cameras: []CameraConfig{{
				ID: "cam1", Protocol: "rtsp", Encoding: "h264", URL: "rtsp://192.168.1.10/stream",
				Timelapse: &CameraTimelapseConfig{
					Enabled:        true,
					MergeMode:      "auto",
					MergeOutputFPS: fps,
				},
			}}}
			cfg.ApplyDefaults()
			err := Validate(cfg)
			require.NoError(t, err, "merge_output_fps=%d should be valid", fps)
		}
	})
}
