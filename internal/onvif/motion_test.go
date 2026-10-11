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
				"State":         "true",
				"Score":         "87",
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

	// Many cameras (TP-Link, some Dahua/Uniview firmwares) never emit
	// VideoSource/MotionAlarm — their motion detection only surfaces as the
	// ONVIF standard rule-engine topic, with the boolean under IsMotion.
	t.Run("CellMotionDetector motion", func(t *testing.T) {
		ma, ok := ParseMotionAlarm(ONVIFEvent{
			Topic: "tns1:RuleEngine/CellMotionDetector/Motion",
			Data: map[string]any{
				"IsMotion":                             "true",
				"source.VideoSourceConfigurationToken": "vsconf",
				"source.Rule":                          "MyMotionDetectorRule",
			},
		})
		require.True(t, ok)
		require.True(t, ma.Active)
		require.Zero(t, ma.Score)
	})

	t.Run("CellMotionDetector clear", func(t *testing.T) {
		ma, ok := ParseMotionAlarm(ONVIFEvent{
			Topic: "tns1:RuleEngine/CellMotionDetector/Motion",
			Data:  map[string]any{"IsMotion": "false"},
		})
		require.True(t, ok)
		require.False(t, ma.Active)
	})

	t.Run("CellMotionDetector missing IsMotion rejected", func(t *testing.T) {
		_, ok := ParseMotionAlarm(ONVIFEvent{
			Topic: "tns1:RuleEngine/CellMotionDetector/Motion",
			Data:  map[string]any{"State": "true"},
		})
		require.False(t, ok)
	})

	t.Run("other rule-engine topics rejected", func(t *testing.T) {
		_, ok := ParseMotionAlarm(ONVIFEvent{
			Topic: "tns1:RuleEngine/TamperDetector/Tamper",
			Data:  map[string]any{"IsTamper": "true"},
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

func TestIsMotionTopic(t *testing.T) {
	t.Parallel()

	for topic, want := range map[string]bool{
		"tns1:VideoSource/MotionAlarm":              true,
		"tns1:RuleEngine/CellMotionDetector/Motion": true,
		"TNS1:RULEENGINE/CELLMOTIONDETECTOR/MOTION": true,
		"tns1:RuleEngine/TamperDetector/Tamper":     false,
		"tns1:Device/Humidity":                      false,
		"tns1:Monitoring/ProcessorUsage":            false,
	} {
		require.Equal(t, want, IsMotionTopic(topic), topic)
	}
}
