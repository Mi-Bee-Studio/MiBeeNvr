package onvif

import (
	"strconv"
	"strings"

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

// ruleEngineTemperatureAlarm is the ONVIF Radiometry rule-engine topic
// (normalized: namespace prefixes stripped, lower-cased) that shipping
// thermographic cameras actually emit for their temperature rules: a
// Hikvision DS-2TB2617 (firmware V5.5.38) advertises
// tns1:RuleEngine/Radiometry/TemperatureAlarm in GetEventProperties, with
// the state in Data "AlarmActive" (Source: VideoSourceToken,
// VideoAnalyticsConfigurationToken, RuleName) — and no Thermal service in
// GetServices. onvif-go v2.7.2 only maps the Thermal-service topic, so the
// rule-engine shape is bridged here until the library adopts it.
const ruleEngineTemperatureAlarm = "ruleengine/radiometry/temperaturealarm"

// ParseRadiometryAlarm extracts a RadiometryAlarm from a parsed ONVIF event.
// Returns false for non-radiometry topics or a missing/unparseable state
// item — same contract as ParseMotionAlarm (the caller WARNs on the
// radiometry-shaped-but-unparseable case).
func ParseRadiometryAlarm(evt ONVIFEvent) (RadiometryAlarm, bool) {
	if normalizeEventTopic(evt.Topic) == ruleEngineTemperatureAlarm {
		return parseTemperatureAlarm(evt)
	}
	return events.ParseRadiometryAlarm(notificationMessageFromEvent(evt))
}

// IsRadiometryTopic reports whether topic is a RadiometryAlarm topic
// ParseRadiometryAlarm understands (case-insensitive).
func IsRadiometryTopic(topic string) bool {
	return events.IsRadiometryTopic(topic) || normalizeEventTopic(topic) == ruleEngineTemperatureAlarm
}

// parseTemperatureAlarm reads the rule-engine TemperatureAlarm shape: state
// from Data "AlarmActive" ("Active"/"State" accepted as variance, the same
// leniency the library applies), Source preferring the video source token
// (the channel the rule runs on) over the rule name, optional Temperature.
func parseTemperatureAlarm(evt ONVIFEvent) (RadiometryAlarm, bool) {
	var raw string
	for _, k := range []string{"AlarmActive", "Active", "State"} {
		if s, ok := evt.Data[k].(string); ok && strings.TrimSpace(s) != "" {
			raw = s
			break
		}
	}
	if raw == "" {
		return RadiometryAlarm{}, false
	}
	active, err := strconv.ParseBool(strings.TrimSpace(raw))
	if err != nil {
		return RadiometryAlarm{}, false
	}
	ra := RadiometryAlarm{Active: active}
	for _, k := range []string{"source.VideoSourceToken", "source.VideoSourceConfigurationToken", "source.Source", "source.RuleName"} {
		if s, ok := evt.Data[k].(string); ok && s != "" {
			ra.Source = s
			break
		}
	}
	if s, ok := evt.Data["Temperature"].(string); ok {
		if v, err := strconv.ParseFloat(strings.TrimSpace(s), 64); err == nil {
			ra.Temperature = v
		}
	}
	return ra, true
}

// normalizeEventTopic strips per-segment namespace prefixes and lower-cases
// ("tns1:RuleEngine/Radiometry/TemperatureAlarm" →
// "ruleengine/radiometry/temperaturealarm") — the same normalization the
// library's topic matchers use.
func normalizeEventTopic(topic string) string {
	segments := strings.Split(strings.TrimSpace(topic), "/")
	for i, seg := range segments {
		if j := strings.LastIndexByte(seg, ':'); j >= 0 {
			seg = seg[j+1:]
		}
		segments[i] = strings.ToLower(seg)
	}
	return strings.Join(segments, "/")
}
