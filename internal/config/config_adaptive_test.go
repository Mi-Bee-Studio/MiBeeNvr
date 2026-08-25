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
		{"spike too high", &AdaptiveRecordingConfig{SpikeFactor: 25}, "spike_factor"},
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

func TestValidateAudioTrigger(t *testing.T) {
	base := func(at *CameraAudioTriggerConfig) *Config {
		cfg := adaptiveTestConfig("adaptive", "h265", nil)
		cfg.Cameras[0].AudioTrigger = at
		return cfg
	}
	if err := Validate(base(&CameraAudioTriggerConfig{Enabled: true, MinDBFS: -45, PreCaptureS: 3})); err != nil {
		t.Fatalf("valid audio_trigger rejected: %v", err)
	}
	if err := Validate(base(nil)); err != nil {
		t.Fatalf("nil audio_trigger rejected: %v", err)
	}
	cases := []struct {
		name string
		at   *CameraAudioTriggerConfig
		want string
	}{
		{"requires adaptive mode", &CameraAudioTriggerConfig{Enabled: true}, "adaptive"}, // set via continuous below
		{"dbfs too low", &CameraAudioTriggerConfig{Enabled: true, MinDBFS: -100}, "min_dbfs"},
		{"dbfs positive", &CameraAudioTriggerConfig{Enabled: true, MinDBFS: 3}, "min_dbfs"},
		{"precap negative", &CameraAudioTriggerConfig{Enabled: true, PreCaptureS: -1}, "pre_capture_s"},
		{"precap too large", &CameraAudioTriggerConfig{Enabled: true, PreCaptureS: 31}, "pre_capture_s"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var err error
			if tc.name == "requires adaptive mode" {
				cfg := adaptiveTestConfig("continuous", "h265", nil)
				cfg.Cameras[0].AudioTrigger = tc.at
				err = Validate(cfg)
			} else {
				err = Validate(base(tc.at))
			}
			require.ErrorContains(t, err, tc.want)
		})
	}
}


func TestValidateTimelapseFrameMs(t *testing.T) {
	ok := func(ms int) *Config {
		c := adaptiveTestConfig("adaptive", "h264", nil)
		c.Cameras[0].Adaptive = &AdaptiveRecordingConfig{TimelapseFrameMs: ms}
		return c
	}
	for _, ms := range []int{0, 100, 300, 500} {
		if err := Validate(ok(ms)); err != nil {
			t.Fatalf("timelapse_frame_ms=%d must validate: %v", ms, err)
		}
	}
	for _, ms := range []int{50, 200, 1000} {
		if err := Validate(ok(ms)); err == nil {
			t.Fatalf("timelapse_frame_ms=%d must be rejected", ms)
		}
	}
	// ambient_archive requires ambient_audio
	c := ok(100)
	c.Cameras[0].Adaptive.AmbientArchive = true
	if err := Validate(c); err == nil {
		t.Fatal("ambient_archive without ambient_audio must be rejected")
	}
	c.Cameras[0].Adaptive.AmbientAudio = true
	if err := Validate(c); err != nil {
		t.Fatalf("ambient_archive with ambient_audio must validate: %v", err)
	}
}
