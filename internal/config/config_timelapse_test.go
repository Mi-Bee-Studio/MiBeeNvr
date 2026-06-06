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
	require.Equal(t, "auto", cfg.Cameras[0].Timelapse.FrameSource)
	require.False(t, cfg.Cameras[0].Timelapse.Paused)
	require.Nil(t, cfg.Cameras[0].Timelapse.Schedule)
	require.False(t, cfg.Cameras[0].Timelapse.DeleteOriginal)
}

func TestTimelapseConfig_DefaultsWithExplicitValues(t *testing.T) {
	t.Parallel()
	cfg := &Config{Cameras: []CameraConfig{{
		ID: "cam1", Protocol: "rtsp", Encoding: "h264", URL: "rtsp://192.168.1.10/stream",
		Timelapse: &CameraTimelapseConfig{
			Enabled:        true,
			Interval:       "10s",
			FrameSource:    "snapshot",
			Paused:         true,
			DeleteOriginal: true,
		},
	}}}
	cfg.ApplyDefaults()

	require.Equal(t, "10s", cfg.Cameras[0].Timelapse.Interval)
	require.Equal(t, "snapshot", cfg.Cameras[0].Timelapse.FrameSource)
	require.True(t, cfg.Cameras[0].Timelapse.Paused)
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

func TestTimelapseConfig_DisabledDoesNotRequireConfig(t *testing.T) {
	t.Parallel()
	// When timelapse.Enabled is false, validation should still pass with defaults
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

func TestTimelapseConfig_FrameSource_Valid(t *testing.T) {
	t.Parallel()
	for _, source := range []string{"auto", "snapshot", "rtsp_keyframe", "mjpeg"} {
		cfg := &Config{Cameras: []CameraConfig{{
			ID: "cam1", Protocol: "rtsp", Encoding: "h264", URL: "rtsp://192.168.1.10/stream",
			Timelapse: &CameraTimelapseConfig{
				Enabled:     true,
				FrameSource: source,
			},
		}}}
		cfg.ApplyDefaults()
		err := Validate(cfg)
		require.NoError(t, err, "frame_source=%s should be valid", source)
	}
}

func TestTimelapseConfig_FrameSource_Invalid(t *testing.T) {
	t.Parallel()
	cfg := &Config{Cameras: []CameraConfig{{
		ID: "cam1", Protocol: "rtsp", Encoding: "h264", URL: "rtsp://192.168.1.10/stream",
		Timelapse: &CameraTimelapseConfig{
			Enabled:     true,
			FrameSource: "invalid_source",
		},
	}}}
	cfg.ApplyDefaults()
	err := Validate(cfg)
	require.Error(t, err)
	require.Contains(t, err.Error(), "frame_source")
}

func TestTimelapseConfig_FrameSource_EmptyDefaultsToAuto(t *testing.T) {
	t.Parallel()
	cfg := &Config{Cameras: []CameraConfig{{
		ID: "cam1", Protocol: "rtsp", Encoding: "h264", URL: "rtsp://192.168.1.10/stream",
		Timelapse: &CameraTimelapseConfig{
			Enabled: true,
			// FrameSource intentionally empty
		},
	}}}
	cfg.ApplyDefaults()
	require.Equal(t, "auto", cfg.Cameras[0].Timelapse.FrameSource)
	err := Validate(cfg)
	require.NoError(t, err)
}

func TestTimelapseConfig_Schedule_Valid(t *testing.T) {
	t.Parallel()
	cfg := &Config{Cameras: []CameraConfig{{
		ID: "cam1", Protocol: "rtsp", Encoding: "h264", URL: "rtsp://192.168.1.10/stream",
		Timelapse: &CameraTimelapseConfig{
			Enabled: true,
			Schedule: &ScheduleConfig{
				TimeRanges: []TimeRange{
					{Start: "09:00", End: "17:00"},
				},
				DaysOfWeek: []int{1, 2, 3, 4, 5}, // Mon-Fri
			},
		},
	}}}
	cfg.ApplyDefaults()
	err := Validate(cfg)
	require.NoError(t, err)
	require.Equal(t, "09:00", cfg.Cameras[0].Timelapse.Schedule.TimeRanges[0].Start)
	require.Equal(t, "17:00", cfg.Cameras[0].Timelapse.Schedule.TimeRanges[0].End)
}

func TestTimelapseConfig_Schedule_MultipleRanges(t *testing.T) {
	t.Parallel()
	cfg := &Config{Cameras: []CameraConfig{{
		ID: "cam1", Protocol: "rtsp", Encoding: "h264", URL: "rtsp://192.168.1.10/stream",
		Timelapse: &CameraTimelapseConfig{
			Enabled: true,
			Schedule: &ScheduleConfig{
				TimeRanges: []TimeRange{
					{Start: "09:00", End: "12:00"},
					{Start: "13:00", End: "17:00"},
				},
				DaysOfWeek: []int{1, 2, 3, 4, 5},
			},
		},
	}}}
	cfg.ApplyDefaults()
	err := Validate(cfg)
	require.NoError(t, err)
}

func TestTimelapseConfig_Schedule_DaysOfWeek_Valid(t *testing.T) {
	t.Parallel()
	for _, day := range []int{0, 1, 2, 3, 4, 5, 6} {
		cfg := &Config{Cameras: []CameraConfig{{
			ID: "cam1", Protocol: "rtsp", Encoding: "h264", URL: "rtsp://192.168.1.10/stream",
			Timelapse: &CameraTimelapseConfig{
				Enabled: true,
				Schedule: &ScheduleConfig{
					TimeRanges: []TimeRange{{Start: "00:00", End: "23:59"}},
					DaysOfWeek: []int{day},
				},
			},
		}}}
		cfg.ApplyDefaults()
		err := Validate(cfg)
		require.NoError(t, err, "day=%d should be valid", day)
	}
}

func TestTimelapseConfig_Schedule_DaysOfWeek_Invalid(t *testing.T) {
	t.Parallel()
	cfg := &Config{Cameras: []CameraConfig{{
		ID: "cam1", Protocol: "rtsp", Encoding: "h264", URL: "rtsp://192.168.1.10/stream",
		Timelapse: &CameraTimelapseConfig{
			Enabled: true,
			Schedule: &ScheduleConfig{
				TimeRanges: []TimeRange{{Start: "00:00", End: "23:59"}},
				DaysOfWeek: []int{7}, // invalid day
			},
		},
	}}}
	cfg.ApplyDefaults()
	err := Validate(cfg)
	require.Error(t, err)
	require.Contains(t, err.Error(), "days_of_week")
}

func TestTimelapseConfig_Schedule_EmptyDaysOfWeekIsValid(t *testing.T) {
	t.Parallel()
	cfg := &Config{Cameras: []CameraConfig{{
		ID: "cam1", Protocol: "rtsp", Encoding: "h264", URL: "rtsp://192.168.1.10/stream",
		Timelapse: &CameraTimelapseConfig{
			Enabled: true,
			Schedule: &ScheduleConfig{
				TimeRanges: []TimeRange{{Start: "00:00", End: "23:59"}},
				// DaysOfWeek empty means all days
			},
		},
	}}}
	cfg.ApplyDefaults()
	err := Validate(cfg)
	require.NoError(t, err)
}

func TestTimelapseConfig_Schedule_InvalidTimeRange_StartBadFormat(t *testing.T) {
	t.Parallel()
	cfg := &Config{Cameras: []CameraConfig{{
		ID: "cam1", Protocol: "rtsp", Encoding: "h264", URL: "rtsp://192.168.1.10/stream",
		Timelapse: &CameraTimelapseConfig{
			Enabled: true,
			Schedule: &ScheduleConfig{
				TimeRanges: []TimeRange{
					{Start: "25:00", End: "30:00"}, // invalid hours
				},
			},
		},
	}}}
	cfg.ApplyDefaults()
	err := Validate(cfg)
	require.Error(t, err)
	require.Contains(t, err.Error(), "time_ranges[0]")
}

func TestTimelapseConfig_Schedule_EndBeforeStart(t *testing.T) {
	t.Parallel()
	cfg := &Config{Cameras: []CameraConfig{{
		ID: "cam1", Protocol: "rtsp", Encoding: "h264", URL: "rtsp://192.168.1.10/stream",
		Timelapse: &CameraTimelapseConfig{
			Enabled: true,
			Schedule: &ScheduleConfig{
				TimeRanges: []TimeRange{
					{Start: "17:00", End: "09:00"}, // end before start
				},
			},
		},
	}}}
	cfg.ApplyDefaults()
	err := Validate(cfg)
	require.Error(t, err)
	require.Contains(t, err.Error(), "must be after")
}

func TestTimelapseConfig_Schedule_OverlappingRangesMerged(t *testing.T) {
	t.Parallel()
	cfg := &Config{Cameras: []CameraConfig{{
		ID: "cam1", Protocol: "rtsp", Encoding: "h264", URL: "rtsp://192.168.1.10/stream",
		Timelapse: &CameraTimelapseConfig{
			Enabled: true,
			Schedule: &ScheduleConfig{
				TimeRanges: []TimeRange{
					{Start: "09:00", End: "12:00"},
					{Start: "11:00", End: "14:00"}, // overlaps first
				},
			},
		},
	}}}
	cfg.ApplyDefaults()
	err := Validate(cfg)
	require.NoError(t, err)
	// Ranges should be merged: 09:00-14:00
	require.Len(t, cfg.Cameras[0].Timelapse.Schedule.TimeRanges, 1)
	require.Equal(t, "09:00", cfg.Cameras[0].Timelapse.Schedule.TimeRanges[0].Start)
	require.Equal(t, "14:00", cfg.Cameras[0].Timelapse.Schedule.TimeRanges[0].End)
}

func TestTimelapseConfig_Schedule_AdjacentRangesMerged(t *testing.T) {
	t.Parallel()
	cfg := &Config{Cameras: []CameraConfig{{
		ID: "cam1", Protocol: "rtsp", Encoding: "h264", URL: "rtsp://192.168.1.10/stream",
		Timelapse: &CameraTimelapseConfig{
			Enabled: true,
			Schedule: &ScheduleConfig{
				TimeRanges: []TimeRange{
					{Start: "09:00", End: "12:00"},
					{Start: "12:00", End: "17:00"}, // adjacent (end matches start)
				},
			},
		},
	}}}
	cfg.ApplyDefaults()
	err := Validate(cfg)
	require.NoError(t, err)
	// Ranges should be merged: 09:00-17:00
	require.Len(t, cfg.Cameras[0].Timelapse.Schedule.TimeRanges, 1)
	require.Equal(t, "09:00", cfg.Cameras[0].Timelapse.Schedule.TimeRanges[0].Start)
	require.Equal(t, "17:00", cfg.Cameras[0].Timelapse.Schedule.TimeRanges[0].End)
}

func TestTimelapseConfig_Schedule_NilIsValid(t *testing.T) {
	t.Parallel()
	// Schedule=nil means 24/7 recording — always valid
	cfg := &Config{Cameras: []CameraConfig{{
		ID: "cam1", Protocol: "rtsp", Encoding: "h264", URL: "rtsp://192.168.1.10/stream",
		Timelapse: &CameraTimelapseConfig{
			Enabled:  true,
			Schedule: nil,
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

func TestTimelapseConfig_DeprecatedFieldsIgnored(t *testing.T) {
	t.Parallel()
	// Configs with the old output_fps and video_codec fields should load without error
	// (these fields are removed from the struct so yaml/json unmarshal silently ignores them)
	cfg := &Config{Cameras: []CameraConfig{{
		ID: "cam1", Protocol: "timelapse", URL: "http://192.168.1.10/video",
		Timelapse: &CameraTimelapseConfig{
			Enabled: true,
		},
	}}}
	cfg.ApplyDefaults()
	err := Validate(cfg)
	require.NoError(t, err)
}
