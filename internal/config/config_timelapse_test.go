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
