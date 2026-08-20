package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func adaptiveTestConfig(mode, encoding string, a *AdaptiveRecordingConfig) *Config {
	cfg := &Config{
		Cameras: []CameraConfig{{
			ID:            "c1",
			Protocol:      "rtsp",
			Encoding:      encoding,
			URL:           "rtsp://a",
			RecordingMode: mode,
			Adaptive:      a,
		}},
	}
	cfg.ApplyDefaults()
	return cfg
}

func TestValidateRecordingMode_ContinuousOK(t *testing.T) {
	require.NoError(t, Validate(adaptiveTestConfig("continuous", "h264", nil)))
	require.NoError(t, Validate(adaptiveTestConfig("", "h264", nil)))
}

func TestValidateRecordingMode_AdaptiveOK(t *testing.T) {
	cfg := adaptiveTestConfig("adaptive", "h264", &AdaptiveRecordingConfig{
		CalmThreshold:     "90s",
		TimelapseInterval: "1m",
		SpikeFactor:       2.5,
		GOPBufferBytes:    32 << 20,
	})
	require.NoError(t, Validate(cfg))
}

func TestValidateRecordingMode_UnknownModeRejected(t *testing.T) {
	require.Error(t, Validate(adaptiveTestConfig("burst", "h264", nil)))
}

func TestValidateRecordingMode_AdaptiveRequiresDifferentialCodec(t *testing.T) {
	err := Validate(adaptiveTestConfig("adaptive", "mjpeg", nil))
	require.ErrorContains(t, err, "h264/h265")
}

func TestValidateRecordingMode_AdaptiveRanges(t *testing.T) {
	cases := []struct {
		name string
		a    *AdaptiveRecordingConfig
		want string
	}{
		{"calm too short", &AdaptiveRecordingConfig{CalmThreshold: "5s"}, "calm_threshold"},
		{"calm too long", &AdaptiveRecordingConfig{CalmThreshold: "31m"}, "calm_threshold"},
		{"interval too short", &AdaptiveRecordingConfig{TimelapseInterval: "1s"}, "timelapse_interval"},
		{"interval too long", &AdaptiveRecordingConfig{TimelapseInterval: "11m"}, "timelapse_interval"},
		{"spike too low", &AdaptiveRecordingConfig{SpikeFactor: 1.0}, "spike_factor"},
		{"spike too high", &AdaptiveRecordingConfig{SpikeFactor: 11}, "spike_factor"},
		{"buffer too small", &AdaptiveRecordingConfig{GOPBufferBytes: 1 << 10}, "gop_buffer_bytes"},
		{"buffer too large", &AdaptiveRecordingConfig{GOPBufferBytes: 128 << 20}, "gop_buffer_bytes"},
		{"bad duration", &AdaptiveRecordingConfig{CalmThreshold: "abc"}, "calm_threshold"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := Validate(adaptiveTestConfig("adaptive", "h265", tc.a))
			require.ErrorContains(t, err, tc.want)
		})
	}
}
