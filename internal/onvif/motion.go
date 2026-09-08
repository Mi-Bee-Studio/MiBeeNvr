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
// Source=VideoSourceToken and Data State only.
type MotionAlarm struct {
	Active bool
	Score  float64 // 0–100; 0 when the device omits it
	Source string  // e.g. "CSI", "VideoSourceToken"
}

// ParseMotionAlarm extracts a MotionAlarm from a parsed ONVIF event. Returns
// false for non-MotionAlarm topics or a missing/unparseable State item — the
// camera-side queue (12 deep, drop-oldest) only makes the true/clear pairing
// best-effort, so callers must tolerate clears that never arrive.
func ParseMotionAlarm(evt ONVIFEvent) (MotionAlarm, bool) {
	if !strings.Contains(strings.ToLower(evt.Topic), "motionalarm") {
		return MotionAlarm{}, false
	}
	rawState, ok := evt.Data["State"].(string)
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
