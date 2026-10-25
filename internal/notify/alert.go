// Package notify forwards NVR alerts to external webhook receivers (alerting
// hubs, chat robots, monitoring platforms). It is deliberately decoupled from
// any specific receiver: the wire format is a small self-describing JSON
// envelope ("mibee-alert/1") built from industry-standard alert concepts
// (type / severity / title / source_ref / resolved), so any HTTP endpoint that
// accepts a JSON POST can consume it.
//
// Delivery is lossy by design: a bounded per-sink queue drops the oldest alert
// when full, transient failures get a few bounded retries, and a dead receiver
// never blocks recording or any other critical path. Notification is a
// convenience surface — the NVR's strong-consistency domain is recording.
package notify

import (
	"time"

	"github.com/google/uuid"
)

// SchemaVersion identifies the alert envelope. Receivers should treat unknown
// future schema versions leniently: unknown fields are ignorable and the
// vocabulary only grows (a published type never changes meaning).
const SchemaVersion = "mibee-alert/1"

// Severity levels, ordered. Receivers commonly use the same four-value ladder.
const (
	SeverityInfo     = "info"
	SeverityWarning  = "warning"
	SeverityError    = "error"
	SeverityCritical = "critical"
)

// Alert type vocabulary. The set is open (receivers must route unknown types
// to a default rule rather than reject), but published types never change
// name, meaning, or pairing semantics.
const (
	// TypeCameraConnection covers connection_lost / connection_restored.
	// lost↔restored pair on the same DedupKey with Resolved flipping.
	TypeCameraConnection = "camera.connection"
	// TypeCameraFreeze covers freeze_detected / freeze_recovered, paired
	// the same way as camera.connection.
	TypeCameraFreeze = "camera.freeze"
	// TypeCameraStream covers one-shot stream anomalies (no pairing).
	TypeCameraStream = "camera.stream"
	// TypeCameraQuality covers quality state transitions (HD↔SD etc.).
	TypeCameraQuality = "camera.quality"
	// TypeCameraMotion covers camera-side motion triggers (ONVIF
	// MotionAlarm). High-frequency by nature: receivers are expected to
	// aggregate.
	TypeCameraMotion = "camera.motion"
	// TypeAIDetection covers AI backend events written back into the NVR
	// (POST /api/ai/events). Carries a snapshot URL when available.
	TypeAIDetection = "ai.detection"
	// TypeDoorbellRing covers the doorbell lifecycle: the initial ring is
	// the alert, answered/rejected/ended resolve it on the same DedupKey.
	TypeDoorbellRing = "doorbell.ring"
	// TypeStorageHealth covers storage health transitions. Recovery to
	// healthy resolves the previous failure on the same DedupKey.
	TypeStorageHealth = "storage.health"
	// TypeGB28181Alarm covers GB/T 28181 device alarm notifications.
	TypeGB28181Alarm = "gb28181.alarm"
	// TypeCameraAdded covers camera registration (manual add + discovery).
	TypeCameraAdded = "camera.added"
	// TypeNotifyTest is emitted by the "send test alert" API so a receiver
	// setup can be verified end to end. Bypasses sink filters.
	TypeNotifyTest = "notify.test"
)

// knownTypes is the closed list the config validator checks against — an
// unknown entry in a sink's events filter is almost always a typo, better
// caught at config load than silently never matching.
var knownTypes = map[string]bool{
	TypeCameraConnection: true,
	TypeCameraFreeze:     true,
	TypeCameraStream:     true,
	TypeCameraQuality:    true,
	TypeCameraMotion:     true,
	TypeAIDetection:      true,
	TypeDoorbellRing:     true,
	TypeStorageHealth:    true,
	TypeGB28181Alarm:     true,
	TypeCameraAdded:      true,
	TypeNotifyTest:       true,
}

// KnownTypes returns the alert type vocabulary (for config validation and UI).
func KnownTypes() []string {
	return []string{
		TypeCameraConnection, TypeCameraFreeze, TypeCameraStream,
		TypeCameraQuality, TypeCameraMotion, TypeAIDetection,
		TypeDoorbellRing, TypeStorageHealth, TypeGB28181Alarm,
		TypeCameraAdded, TypeNotifyTest,
	}
}

// IsValidType reports whether t is in the current vocabulary.
func IsValidType(t string) bool { return knownTypes[t] }

// SeverityRank maps a severity to its position in the ladder; unknown
// severities rank lowest. Used by sink-level severity_min filtering.
func SeverityRank(s string) int {
	switch s {
	case SeverityCritical:
		return 3
	case SeverityError:
		return 2
	case SeverityWarning:
		return 1
	default:
		return 0
	}
}

// IsValidSeverity reports whether s is one of the four levels.
func IsValidSeverity(s string) bool {
	switch s {
	case SeverityInfo, SeverityWarning, SeverityError, SeverityCritical:
		return true
	default:
		return false
	}
}

// Alert is the outbound envelope (schema "mibee-alert/1").
//
// Field semantics receivers can rely on:
//   - source_ref is the NVR camera ID for camera-scoped alerts ("system" /
//     "storage" for system-scoped ones) and is the recommended
//     deduplication/routing dimension.
//   - dedup_key is only set for stateful pairs (offline↔online, storage
//     failure↔recovery, doorbell ring↔handled): the resolve leg carries the
//     same key with resolved=true.
//   - snapshot_url, when present, is a URL the receiver (or the user, via the
//     receiver's app) can fetch while the alert is fresh. It embeds a
//     dedicated, revocable API key.
//   - tags carry optional enrichment: camera_name for display, zone for
//     common-cause grouping. Unknown tags must be passed through.
type Alert struct {
	Schema      string            `json:"schema"`
	EventID     string            `json:"event_id"`
	OccurredAt  string            `json:"occurred_at"` // RFC3339Nano, event time (not send time)
	Type        string            `json:"type"`
	Severity    string            `json:"severity"`
	Title       string            `json:"title"`
	Body        string            `json:"body,omitempty"`
	SourceRef   string            `json:"source_ref"`
	DedupKey    string            `json:"dedup_key,omitempty"`
	Resolved    bool              `json:"resolved,omitempty"`
	SnapshotURL string            `json:"snapshot_url,omitempty"`
	Tags        map[string]string `json:"tags,omitempty"`
}

// SystemSourceRef is used for alerts that are not camera-scoped.
const SystemSourceRef = "system"

func newAlert(now time.Time) Alert {
	return Alert{
		Schema:     SchemaVersion,
		EventID:    newEventID(),
		OccurredAt: now.UTC().Format(time.RFC3339Nano),
		Severity:   SeverityInfo,
		SourceRef:  SystemSourceRef,
	}
}

// newEventID mints a per-emission identifier. It is an audit/delivery marker,
// not a semantic identity — stateful pairing travels in dedup_key.
func newEventID() string { return uuid.NewString() }
