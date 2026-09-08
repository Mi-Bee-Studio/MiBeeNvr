package onvif

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseMotionAlarm(t *testing.T) {
	t.Parallel()

	t.Run("mibee_cam CSI payload", func(t *testing.T) {
		ma, ok := ParseMotionAlarm(ONVIFEvent{
			Topic: "tns1:VideoSource/MotionAlarm",
			Data: map[string]any{
				"State":        "true",
				"Score":        "87",
				"source.Source": "CSI",
			},
		})
		require.True(t, ok)
		require.True(t, ma.Active)
		require.Equal(t, 87.0, ma.Score)
		require.Equal(t, "CSI", ma.Source)
	})

	t.Run("clear event", func(t *testing.T) {
		ma, ok := ParseMotionAlarm(ONVIFEvent{
			Topic: "tns1:VideoSource/MotionAlarm",
			Data:  map[string]any{"State": "false"},
		})
		require.True(t, ok)
		require.False(t, ma.Active)
	})

	t.Run("standard camera without score", func(t *testing.T) {
		ma, ok := ParseMotionAlarm(ONVIFEvent{
			Topic: "tns1:VideoSource/MotionAlarm",
			Data:  map[string]any{"State": "true", "source.Source": "VideoSourceToken"},
		})
		require.True(t, ok)
		require.True(t, ma.Active)
		require.Zero(t, ma.Score)
	})

	t.Run("non-motion topic rejected", func(t *testing.T) {
		_, ok := ParseMotionAlarm(ONVIFEvent{
			Topic: "tns1:Device/Humidity",
			Data:  map[string]any{"State": "true"},
		})
		require.False(t, ok)
	})

	t.Run("missing state rejected", func(t *testing.T) {
		_, ok := ParseMotionAlarm(ONVIFEvent{
			Topic: "tns1:VideoSource/MotionAlarm",
			Data:  map[string]any{"Score": "50"},
		})
		require.False(t, ok)
	})

	t.Run("garbage state rejected", func(t *testing.T) {
		_, ok := ParseMotionAlarm(ONVIFEvent{
			Topic: "tns1:VideoSource/MotionAlarm",
			Data:  map[string]any{"State": "maybe"},
		})
		require.False(t, ok)
	})

	t.Run("garbage score tolerated", func(t *testing.T) {
		ma, ok := ParseMotionAlarm(ONVIFEvent{
			Topic: "tns1:VideoSource/MotionAlarm",
			Data:  map[string]any{"State": "true", "Score": "high"},
		})
		require.True(t, ok)
		require.True(t, ma.Active)
		require.Zero(t, ma.Score)
	})
}
