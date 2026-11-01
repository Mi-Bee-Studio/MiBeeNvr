package onvif

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIsRadiometryTopic(t *testing.T) {
	t.Parallel()
	require.True(t, IsRadiometryTopic("tns1:Thermal/RadiometryAlarm"))
	// RuleEngine-qualified vendor variants compare equal by topic tail.
	require.True(t, IsRadiometryTopic("tns1:RuleEngine/Thermal/RadiometryAlarm"))
	require.True(t, IsRadiometryTopic("Thermal/RadiometryAlarm"))
	require.True(t, IsRadiometryTopic("tns1:thermal/radiometryalarm"))

	require.False(t, IsRadiometryTopic("tns1:VideoSource/MotionAlarm"))
	require.False(t, IsRadiometryTopic("tns1:Thermal/Temperature"))
	require.False(t, IsRadiometryTopic(""))
}

func TestParseRadiometryAlarm(t *testing.T) {
	t.Parallel()

	// Spec shape: Active boolean + optional Temperature reading.
	ra, ok := ParseRadiometryAlarm(ONVIFEvent{
		Topic: "tns1:Thermal/RadiometryAlarm",
		Data:  map[string]any{"Active": "true", "Temperature": "87.5", "source.Source": "ThermalSource_1"},
	})
	require.True(t, ok)
	require.True(t, ra.Active)
	require.InDelta(t, 87.5, ra.Temperature, 0.001)
	require.Equal(t, "ThermalSource_1", ra.Source)

	// Vendor variance: State instead of Active; RuleEngine-qualified topic.
	ra, ok = ParseRadiometryAlarm(ONVIFEvent{
		Topic: "tns1:RuleEngine/Thermal/RadiometryAlarm",
		Data:  map[string]any{"State": "false"},
	})
	require.True(t, ok)
	require.False(t, ra.Active)
	require.InDelta(t, 0, ra.Temperature, 0.001)

	// First source.* key when source.Source is absent.
	ra, ok = ParseRadiometryAlarm(ONVIFEvent{
		Topic: "Thermal/RadiometryAlarm",
		Data:  map[string]any{"Active": "1", "source.VideoSourceToken": "vs-2"},
	})
	require.True(t, ok)
	require.True(t, ra.Active)
	require.Equal(t, "vs-2", ra.Source)
}

func TestParseRadiometryAlarmRejects(t *testing.T) {
	t.Parallel()

	// Non-radiometry topic: not our contract.
	_, ok := ParseRadiometryAlarm(ONVIFEvent{
		Topic: "tns1:VideoSource/MotionAlarm",
		Data:  map[string]any{"Active": "true"},
	})
	require.False(t, ok)

	// Radiometry topic with a missing state item: unparseable, same contract
	// as ParseMotionAlarm (caller WARNs — a vendor encoding mismatch must be
	// distinguishable from "device emits nothing").
	_, ok = ParseRadiometryAlarm(ONVIFEvent{
		Topic: "tns1:Thermal/RadiometryAlarm",
		Data:  map[string]any{"Temperature": "42"},
	})
	require.False(t, ok)

	// Non-boolean state.
	_, ok = ParseRadiometryAlarm(ONVIFEvent{
		Topic: "tns1:Thermal/RadiometryAlarm",
		Data:  map[string]any{"Active": "hot"},
	})
	require.False(t, ok)
}
