package onvif

import (
	"strconv"
	"strings"
)

// MotionAlarm is a parsed tns1:VideoSource/MotionAlarm notification.
//
// mibee_cam family contract v1.5 §13: Source=CSI (WiFi-CSI motion sensing —
// works with the MJPEG push in parallel, independent of light), Data carries
// State ("true" = motion entered / "false" = cleared) and an optional Score
// in 0–100. Standard ONVIF cameras emit the same topic with
// Source=VideoSourceToken and Data State only. Cameras that only report
// tns1:RuleEngine/CellMotionDetector/Motion (Data IsMotion) are parsed into
// the same struct, with Score 0.
type MotionAlarm struct {
	Active bool
	Score  float64 // 0–100; 0 when the device omits it
	Source string  // e.g. "CSI", "VideoSourceToken"
}

// motionStateKey returns the Data item that carries the boolean motion state
// for a motion topic, or "" for anything else. Two topics are recognized:
//
//   - tns1:VideoSource/MotionAlarm — State (mibee_cam, Hikvision, ...)
//   - tns1:RuleEngine/CellMotionDetector/Motion — IsMotion, the ONVIF
//     standard cell-motion rule. Many cameras (e.g. TP-Link) emit only this
//     one, so without it camera:onvif never fires for them.
func motionStateKey(topic string) string {
	t := strings.ToLower(topic)
	switch {
	case strings.Contains(t, "motionalarm"):
		return "State"
	case strings.Contains(t, "cellmotiondetector/motion"):
		return "IsMotion"
	}
	return ""
}

// IsMotionTopic reports whether topic is one of the motion topics
// ParseMotionAlarm understands.
func IsMotionTopic(topic string) bool { return motionStateKey(topic) != "" }

// ParseMotionAlarm extracts a MotionAlarm from a parsed ONVIF event. Returns
// false for non-motion topics or a missing/unparseable state item — the
// camera-side queue (12 deep, drop-oldest) only makes the true/clear pairing
// best-effort, so callers must tolerate clears that never arrive.
func ParseMotionAlarm(evt ONVIFEvent) (MotionAlarm, bool) {
	key := motionStateKey(evt.Topic)
	if key == "" {
		return MotionAlarm{}, false
	}
	rawState, ok := evt.Data[key].(string)
	if !ok {
		return MotionAlarm{}, false
	}
	active, err := strconv.ParseBool(strings.TrimSpace(rawState))
	if err != nil {
		return MotionAlarm{}, false
	}
	ma := MotionAlarm{Active: active}
	if s, ok := evt.Data["Score"].(string); ok {
		if f, err := strconv.ParseFloat(strings.TrimSpace(s), 64); err == nil {
			ma.Score = f
		}
	}
	if s, ok := evt.Data["source.Source"].(string); ok {
		ma.Source = s
	}
	return ma, true
}
