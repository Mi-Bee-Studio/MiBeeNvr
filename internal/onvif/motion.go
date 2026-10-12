package onvif

import (
	"strings"

	"github.com/mickeyzzc/onvif-go/v2/events"
	"github.com/mickeyzzc/onvif-go/v2/types"
)

// MotionAlarm is the library-parsed motion state of a standard ONVIF motion
// topic. Two topics are recognized (semantics owned by onvif-go's events
// package, #950/#951):
//
//   - tns1:VideoSource/MotionAlarm — Data State ("true"/"false") and an
//     optional Score 0–100. The mibee_cam family contract v1.5 §13 emits
//     Source=CSI there (WiFi-CSI motion sensing); standard cameras emit
//     Source=VideoSourceToken.
//   - tns1:RuleEngine/CellMotionDetector/Motion — Data IsMotion. Many
//     cameras (e.g. TP-Link) emit only this one, so without it
//     motion_source: camera:onvif never fires for them (#950).
type MotionAlarm = events.MotionAlarm

// ParseMotionAlarm extracts a MotionAlarm from a parsed ONVIF event. Returns
// false for non-motion topics or a missing/unparseable state item — the
// camera-side queue (12 deep, drop-oldest) only makes the true/clear pairing
// best-effort, so callers must tolerate clears that never arrive.
func ParseMotionAlarm(evt ONVIFEvent) (MotionAlarm, bool) {
	return events.ParseMotionAlarm(notificationMessageFromEvent(evt))
}

// IsMotionTopic reports whether topic is one of the standard motion topics
// ParseMotionAlarm understands (case-insensitive).
func IsMotionTopic(topic string) bool { return events.IsMotionTopic(topic) }

// notificationMessageFromEvent rebuilds the library's NotificationMessage
// from the NVR's flattened ONVIFEvent: plain Data items restore to the Data
// slot and "source."-prefixed keys restore to the Source slot, so the library
// parser sees the same Source/Data separation the wire carried.
func notificationMessageFromEvent(evt ONVIFEvent) events.NotificationMessage {
	msg := events.NotificationMessage{Topic: evt.Topic}
	for k, v := range evt.Data {
		s, ok := v.(string)
		if !ok {
			continue
		}
		if name, isSource := strings.CutPrefix(k, "source."); isSource {
			msg.Message.Source = append(msg.Message.Source, types.SimpleItem{Name: name, Value: s})
			continue
		}
		msg.Message.Data = append(msg.Message.Data, types.SimpleItem{Name: k, Value: s})
	}
	return msg
}
