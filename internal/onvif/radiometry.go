package onvif

import (
	"strconv"
	"strings"
)

// RadiometryAlarm is the fire/over-temperature state carried by the ONVIF
// Thermal Service's RadiometryAlarm event — the thermal counterpart of
// VideoSource/MotionAlarm. The thermal rules themselves (spot/region
// temperature thresholds) run on the camera; the NVR only consumes the
// verdict and turns it into the same recording trigger surface motion uses.
type RadiometryAlarm struct {
	Active      bool
	Temperature float64 // °C when the device reports a reading; 0 otherwise
	Source      string  // Source SimpleItem value (e.g. a thermal source token)
}

// radiometryTopicSuffix is the normalized topic tail a RadiometryAlarm
// carries. Matched on the last two segments so both the spec topic
// (tns1:Thermal/RadiometryAlarm) and RuleEngine-qualified vendor variants
// compare equal, mirroring how the motion topics normalize.
const radiometryTopicSuffix = "thermal/radiometryalarm"

// normalizeEventTopic lower-cases a topic path and drops the namespace prefix
// of every segment ("tns1:", "tnsalias:", vendor prefixes). Same rules as the
// onvif-go events package's normalizeTopic, kept here because that helper is
// package-private in the library.
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

// IsRadiometryTopic reports whether topic is a RadiometryAlarm topic
// ParseRadiometryAlarm understands (case-insensitive, namespace-prefix
// agnostic).
func IsRadiometryTopic(topic string) bool {
	n := normalizeEventTopic(topic)
	return strings.HasSuffix(n, radiometryTopicSuffix)
}

// ParseRadiometryAlarm extracts the radiometry state from a parsed ONVIF
// event. The topic is matched by its thermal/radiometryalarm tail; the state
// is read from the Data slot only — "Active" (spec shape) with "State"
// accepted as vendor variance, both parsed as booleans. An optional
// "Temperature" Data item (°C) is surfaced when present and numeric. Source
// is the "source.Source" key, else the first source.* key.
//
// Any other topic, or a missing or unparseable state item, returns
// (RadiometryAlarm{}, false) — same contract as ParseMotionAlarm.
func ParseRadiometryAlarm(evt ONVIFEvent) (RadiometryAlarm, bool) {
	if !IsRadiometryTopic(evt.Topic) {
		return RadiometryAlarm{}, false
	}

	raw, ok := radiometryState(evt.Data)
	if !ok {
		return RadiometryAlarm{}, false
	}
	active, err := strconv.ParseBool(strings.TrimSpace(raw))
	if err != nil {
		return RadiometryAlarm{}, false
	}

	alarm := RadiometryAlarm{Active: active, Source: radiometrySource(evt.Data)}
	if s, ok := evt.Data["Temperature"].(string); ok {
		if temp, err := strconv.ParseFloat(strings.TrimSpace(s), 64); err == nil {
			alarm.Temperature = temp
		}
	}
	return alarm, true
}

// radiometryState returns the raw boolean-carrying Data value. ONVIFEvent.Data
// is flattened: Data-slot items keep their names, Source-slot items arrive
// "source."-prefixed (see notificationMessageFromEvent).
func radiometryState(data map[string]any) (string, bool) {
	for _, key := range []string{"Active", "State"} {
		if s, ok := data[key].(string); ok {
			return s, true
		}
	}
	return "", false
}

func radiometrySource(data map[string]any) string {
	if s, ok := data["source.Source"].(string); ok {
		return s
	}
	for k, v := range data {
		if s, ok := v.(string); ok && strings.HasPrefix(k, "source.") {
			return s
		}
	}
	return ""
}
