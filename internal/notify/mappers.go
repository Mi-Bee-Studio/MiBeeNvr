package notify

import (
	"fmt"
	"strconv"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/event"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
)

// CameraNamer resolves a camera ID to its display name ("" when unknown).
// Alert titles embed the name for human readability; resolution failures are
// non-fatal — the camera_id is always present in source_ref.
type CameraNamer func(cameraID string) string

// AIEventInfo is the slice of a stored AI event the mapper needs for
// enrichment (label text). Kept local so the package stays decoupled from
// storage.
type AIEventInfo struct {
	EventType string
	Severity  string
	ClassName string
	ZoneName  string
	CameraID  string
}

// AILookup fetches enrichment info for an AI event by its numeric ID.
type AILookup func(eventID int64) (AIEventInfo, bool)

// Snapshots builds externally fetchable snapshot URLs. Empty BaseURL or
// APIKey disables snapshots entirely (alerts still deliver without them).
type Snapshots struct {
	BaseURL string // e.g. "http://192.168.1.10:9090", no trailing slash
	APIKey  string // dedicated mbv_ key; embedded as ?api_key=
}

func (s Snapshots) enabled() bool { return s.BaseURL != "" && s.APIKey != "" }

// AI returns the stored-snapshot URL of an AI event (the frame the backend
// analyzed), or "" when snapshots are not configured.
func (s Snapshots) AI(eventID int64) string {
	if !s.enabled() {
		return ""
	}
	return fmt.Sprintf("%s/api/ai/events/%d/snapshot?api_key=%s", s.BaseURL, eventID, s.APIKey)
}

// Doorbell returns the snapshot URL captured for a doorbell call.
func (s Snapshots) Doorbell(callID string) string {
	if !s.enabled() || callID == "" {
		return ""
	}
	return fmt.Sprintf("%s/api/doorbell/calls/%s/snapshot?api_key=%s", s.BaseURL, callID, s.APIKey)
}

// Camera returns a live-snapshot URL for a camera (a fresh frame captured at
// fetch time — the closest available approximation of event time for
// motion-class alerts).
func (s Snapshots) Camera(cameraID string) string {
	if !s.enabled() || cameraID == "" {
		return ""
	}
	return fmt.Sprintf("%s/api/cameras/%s/snapshot?api_key=%s", s.BaseURL, cameraID, s.APIKey)
}

// mapper turns event-bus payloads and health events into Alerts.
type mapper struct {
	nameOf    CameraNamer
	aiLookup  AILookup
	snapshots Snapshots
}

func (m *mapper) cameraName(cameraID string) string {
	if m.nameOf == nil {
		return ""
	}
	return m.nameOf(cameraID)
}

// mapHealthEvent converts a health pipeline event (already cooldown-filtered
// and escalated by the pipeline) into an alert.
func (m *mapper) mapHealthEvent(cameraID string, evt model.HealthEvent) (Alert, bool) {
	a := newAlert(evt.CreatedAt)
	a.SourceRef = cameraRef(cameraID)
	if name := m.cameraName(cameraID); name != "" {
		a.Tags = map[string]string{"camera_name": name}
	}
	a.Body = evt.Message

	switch model.HealthEventType(evt.EventType) {
	case model.HealthEventConnectionLost:
		a.Type = TypeCameraConnection
		a.Severity = SeverityError
		a.Title = titleWithCamera("Camera offline", cameraID, m)
		a.DedupKey = "mibee-nvr/connection/" + cameraID
	case model.HealthEventConnectionRestored:
		a.Type = TypeCameraConnection
		a.Severity = SeverityInfo
		a.Title = titleWithCamera("Camera back online", cameraID, m)
		a.DedupKey = "mibee-nvr/connection/" + cameraID
		a.Resolved = true
	case model.HealthEventFreezeDetected:
		a.Type = TypeCameraFreeze
		a.Severity = SeverityWarning
		a.Title = titleWithCamera("Video frozen", cameraID, m)
		a.DedupKey = "mibee-nvr/freeze/" + cameraID
	case model.HealthEventFreezeRecovered:
		a.Type = TypeCameraFreeze
		a.Severity = SeverityInfo
		a.Title = titleWithCamera("Video recovered", cameraID, m)
		a.DedupKey = "mibee-nvr/freeze/" + cameraID
		a.Resolved = true
	case model.HealthEventStreamAnomaly:
		a.Type = TypeCameraStream
		a.Severity = SeverityWarning
		a.Title = titleWithCamera("Stream anomaly", cameraID, m)
	case model.HealthEventQualityChanged:
		a.Type = TypeCameraQuality
		a.Severity = SeverityInfo
		a.Title = titleWithCamera("Quality changed", cameraID, m)
	default:
		return Alert{}, false
	}
	return a, true
}

// mapBusEvent converts a whitelisted event-bus event into an alert. Unknown
// topics return false (the service only subscribes to its whitelist, this is
// a defensive guard).
func (m *mapper) mapBusEvent(evt event.Event) (Alert, bool) {
	switch evt.Topic {
	case event.TopicAIEventCreated:
		return m.mapAIEvent(evt.Data)
	case event.TopicStorageHealthChanged:
		return m.mapStorageHealth(evt.Data)
	case event.TopicCameraAdded:
		return m.mapCameraAdded(evt.Data)
	case event.TopicCameraQuality:
		return m.mapCameraQuality(evt.Data)
	case event.TopicGB28181Alarm:
		return m.mapGB28181Alarm(evt.Data)
	case event.TopicDoorbellRing, event.TopicDoorbellAnswered, event.TopicDoorbellRejected, event.TopicDoorbellEnded:
		return m.mapDoorbell(evt.Topic, evt.Data)
	case TopicONVIFMotionAlarm:
		return m.mapMotion(evt.Data)
	default:
		return Alert{}, false
	}
}

// TopicONVIFMotionAlarm is the bus topic the camera manager republishes ONVIF
// PullPoint MotionAlarm events under (onvif.<leaf> of the device topic).
const TopicONVIFMotionAlarm = "onvif.motionalarm"

func (m *mapper) mapAIEvent(data any) (Alert, bool) {
	payload, ok := data.(map[string]any)
	if !ok {
		return Alert{}, false
	}
	id, _ := toInt64(payload["event_id"])
	cameraID, _ := payload["camera_id"].(string)
	eventType, _ := payload["event_type"].(string)
	severity, _ := payload["severity"].(string)
	if id == 0 || cameraID == "" {
		return Alert{}, false
	}

	a := newAlert(timeNow())
	a.Type = TypeAIDetection
	a.SourceRef = cameraRef(cameraID)
	a.SnapshotURL = m.snapshots.AI(id)

	// Enrich from the stored row when available (class/zone read better than
	// the raw event_type); fall back to the published payload fields.
	aiType, class, zone := eventType, "", ""
	if m.aiLookup != nil {
		if info, ok := m.aiLookup(id); ok {
			if info.EventType != "" {
				aiType = info.EventType
			}
			if info.Severity != "" {
				severity = info.Severity
			}
			if info.ClassName != "" {
				class = info.ClassName
			}
			if info.ZoneName != "" {
				zone = info.ZoneName
			}
		}
	}
	a.Severity = normalizeSeverity(severity, SeverityWarning)
	subject := aiType
	if class != "" {
		subject = class
	}
	a.Title = titleWithCamera("AI event: "+subject, cameraID, m)
	a.Body = describeAIBody(aiType, class, zone)
	if name := m.cameraName(cameraID); name != "" {
		a.Tags = map[string]string{"camera_name": name}
	}
	return a, true
}

func describeAIBody(eventType, class, zone string) string {
	parts := make([]string, 0, 3)
	if eventType != "" {
		parts = append(parts, "type="+eventType)
	}
	if class != "" {
		parts = append(parts, "class="+class)
	}
	if zone != "" {
		parts = append(parts, "zone="+zone)
	}
	if len(parts) == 0 {
		return ""
	}
	return "AI backend event (" + joinComma(parts) + ")"
}

func joinComma(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += ", "
		}
		out += p
	}
	return out
}

func (m *mapper) mapStorageHealth(data any) (Alert, bool) {
	evt, ok := data.(event.StorageHealthChanged)
	if !ok {
		return Alert{}, false
	}
	a := newAlert(timeNow())
	a.Type = TypeStorageHealth
	a.SourceRef = SystemSourceRef
	if evt.CameraID != "" {
		a.SourceRef = cameraRef(evt.CameraID)
	}
	a.Body = evt.Message
	switch evt.CurrentState {
	case "healthy":
		a.Severity = SeverityInfo
		a.Title = "Storage healthy"
		a.Resolved = true
	case "degraded":
		a.Severity = SeverityWarning
		a.Title = "Storage degraded"
	case "failed":
		a.Severity = SeverityError
		a.Title = "Storage failed"
	default:
		a.Severity = SeverityWarning
		a.Title = "Storage health: " + evt.CurrentState
	}
	if evt.PreviousState != "" && evt.PreviousState != evt.CurrentState {
		a.Body = joinNonEmpty(evt.Message, fmt.Sprintf("state %s → %s", evt.PreviousState, evt.CurrentState), " | ")
	}
	a.DedupKey = "mibee-nvr/storage"
	if evt.CameraID != "" {
		a.DedupKey += "/" + evt.CameraID
	}
	return a, true
}

func (m *mapper) mapCameraAdded(data any) (Alert, bool) {
	payload, ok := data.(map[string]any)
	if !ok {
		return Alert{}, false
	}
	cameraID, _ := payload["camera_id"].(string)
	name, _ := payload["name"].(string)
	if cameraID == "" {
		return Alert{}, false
	}
	a := newAlert(timeNow())
	a.Type = TypeCameraAdded
	a.Severity = SeverityInfo
	a.SourceRef = cameraRef(cameraID)
	if name != "" {
		a.Title = "Camera added: " + name
		a.Tags = map[string]string{"camera_name": name}
	} else {
		a.Title = titleWithCamera("Camera added", cameraID, m)
	}
	if source, _ := payload["source"].(string); source != "" {
		a.setTag("source", source)
	}
	return a, true
}

func (m *mapper) mapCameraQuality(data any) (Alert, bool) {
	payload, ok := data.(map[string]any)
	if !ok {
		return Alert{}, false
	}
	cameraID, _ := payload["camera_id"].(string)
	if cameraID == "" {
		return Alert{}, false
	}
	from, _ := payload["from"].(string)
	to, _ := payload["to"].(string)
	reason, _ := payload["reason"].(string)

	a := newAlert(timeNow())
	a.Type = TypeCameraQuality
	a.Severity = SeverityInfo
	a.SourceRef = cameraRef(cameraID)
	a.Title = titleWithCamera("Camera quality "+from+" → "+to, cameraID, m)
	if reason != "" {
		a.Body = reason
	}
	if name := m.cameraName(cameraID); name != "" {
		a.Tags = map[string]string{"camera_name": name}
	}
	return a, true
}

func (m *mapper) mapGB28181Alarm(data any) (Alert, bool) {
	evt, ok := data.(event.GB28181AlarmEvent)
	if !ok {
		return Alert{}, false
	}
	a := newAlert(evt.ReceivedAt)
	a.Type = TypeGB28181Alarm
	a.SourceRef = cameraRef(firstNonEmpty(evt.CameraID, evt.ChannelID, evt.DeviceID))
	a.Title = "Device alarm: " + firstNonEmpty(evt.AlarmDescription, evt.AlarmType, "gb28181")
	a.Body = describeGBAlarmBody(evt)
	// GB/T 28181 alarm priority: 1 high, 2 medium, 3 low.
	switch evt.AlarmPriority {
	case "1":
		a.Severity = SeverityCritical
	case "3":
		a.Severity = SeverityInfo
	default:
		a.Severity = SeverityWarning
	}
	if name := m.cameraName(a.SourceRef); name != "" {
		a.Tags = map[string]string{"camera_name": name}
	}
	return a, true
}

func describeGBAlarmBody(evt event.GB28181AlarmEvent) string {
	parts := make([]string, 0, 4)
	if evt.DeviceID != "" {
		parts = append(parts, "device="+evt.DeviceID)
	}
	if evt.ChannelID != "" {
		parts = append(parts, "channel="+evt.ChannelID)
	}
	if evt.AlarmMethod != "" {
		parts = append(parts, "method="+evt.AlarmMethod)
	}
	if evt.AlarmType != "" {
		parts = append(parts, "alarm_type="+evt.AlarmType)
	}
	return joinComma(parts)
}

func (m *mapper) mapDoorbell(topic string, data any) (Alert, bool) {
	evt, ok := data.(event.DoorbellEvent)
	if !ok {
		return Alert{}, false
	}
	a := newAlert(evt.Timestamp)
	a.Type = TypeDoorbellRing
	a.DedupKey = "mibee-nvr/doorbell/" + evt.CallID
	a.SourceRef = cameraRef(firstNonEmpty(evt.CameraID, evt.CallID))
	a.SnapshotURL = m.snapshots.Doorbell(evt.CallID)

	switch topic {
	case event.TopicDoorbellRing:
		a.Severity = SeverityWarning
		a.Title = "Doorbell ringing"
		if evt.Name != "" {
			a.Title = "Doorbell: " + evt.Name
		}
	case event.TopicDoorbellAnswered:
		a.Severity = SeverityInfo
		a.Resolved = true
		a.Title = "Doorbell answered by " + evt.User
	case event.TopicDoorbellRejected:
		a.Severity = SeverityInfo
		a.Resolved = true
		a.Title = "Doorbell rejected by " + evt.User
	default: // ended
		a.Severity = SeverityInfo
		a.Resolved = true
		a.Title = "Doorbell call ended"
		if evt.Reason != "" {
			a.Title += " (" + evt.Reason + ")"
		}
	}
	if name := m.cameraName(evt.CameraID); name != "" {
		a.Tags = map[string]string{"camera_name": name}
	}
	return a, true
}

func (m *mapper) mapMotion(data any) (Alert, bool) {
	payload, ok := data.(map[string]any)
	if !ok {
		return Alert{}, false
	}
	cameraID, _ := payload["camera_id"].(string)
	if cameraID == "" {
		return Alert{}, false
	}
	a := newAlert(timeNow())
	a.Type = TypeCameraMotion
	a.Severity = SeverityWarning
	a.SourceRef = cameraRef(cameraID)
	a.Title = titleWithCamera("Motion detected", cameraID, m)
	a.SnapshotURL = m.snapshots.Camera(cameraID)
	// The republished payload carries camera_name when the manager knows it —
	// prefer that over a DB round trip.
	if name, _ := payload["camera_name"].(string); name != "" {
		a.Tags = map[string]string{"camera_name": name}
	} else if name := m.cameraName(cameraID); name != "" {
		a.Tags = map[string]string{"camera_name": name}
	}
	return a, true
}

// --- small helpers -------------------------------------------------------

func cameraRef(cameraID string) string {
	if cameraID == "" {
		return SystemSourceRef
	}
	return cameraID
}

func titleWithCamera(prefix, cameraID string, m *mapper) string {
	if name := m.cameraName(cameraID); name != "" {
		return prefix + ": " + name
	}
	if cameraID != "" {
		return prefix + ": " + cameraID
	}
	return prefix
}

func (a *Alert) setTag(key, value string) {
	if a.Tags == nil {
		a.Tags = map[string]string{}
	}
	a.Tags[key] = value
}

func normalizeSeverity(s, fallback string) string {
	if IsValidSeverity(s) {
		return s
	}
	return fallback
}

func toInt64(v any) (int64, bool) {
	switch n := v.(type) {
	case int64:
		return n, true
	case int:
		return int64(n), true
	case float64:
		return int64(n), true
	case string:
		i, err := strconv.ParseInt(n, 10, 64)
		return i, err == nil
	default:
		return 0, false
	}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func joinNonEmpty(a, b, sep string) string {
	switch {
	case a == "":
		return b
	case b == "":
		return a
	default:
		return a + sep + b
	}
}
