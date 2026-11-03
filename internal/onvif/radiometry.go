package onvif

import (
	"github.com/mickeyzzc/onvif-go/v2/events"
)

// RadiometryAlarm is the library-parsed fire/over-temperature state of the
// ONVIF Thermal Service's RadiometryAlarm event — the thermal counterpart of
// MotionAlarm. The thermal rules themselves (spot/region temperature
// thresholds) run on the camera; the NVR only consumes the verdict and turns
// it into the same recording trigger surface motion uses. Semantics are
// owned by onvif-go's events package (v2.7.2): tns1:Thermal/RadiometryAlarm
// plus RuleEngine-qualified vendor variants, Data "Active" (spec) with
// "State" accepted as vendor variance, an optional numeric Temperature (°C).
type RadiometryAlarm = events.RadiometryAlarm

// ParseRadiometryAlarm extracts a RadiometryAlarm from a parsed ONVIF event.
// Returns false for non-radiometry topics or a missing/unparseable state
// item — same contract as ParseMotionAlarm (the caller WARNs on the
// radiometry-shaped-but-unparseable case).
func ParseRadiometryAlarm(evt ONVIFEvent) (RadiometryAlarm, bool) {
	return events.ParseRadiometryAlarm(notificationMessageFromEvent(evt))
}

// IsRadiometryTopic reports whether topic is a RadiometryAlarm topic
// ParseRadiometryAlarm understands (case-insensitive).
func IsRadiometryTopic(topic string) bool { return events.IsRadiometryTopic(topic) }
