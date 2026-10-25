package notify

import (
	"strings"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/event"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
)

func testMapper() *mapper {
	return &mapper{
		nameOf: func(cameraID string) string {
			if cameraID == "cam-1" {
				return "Studio"
			}
			return ""
		},
		aiLookup: func(eventID int64) (AIEventInfo, bool) {
			if eventID == 42 {
				return AIEventInfo{EventType: "person", Severity: "warning", ClassName: "person", ZoneName: "yard"}, true
			}
			return AIEventInfo{}, false
		},
		snapshots: Snapshots{BaseURL: "http://nvr.local:9090", APIKey: "mbv_test"},
	}
}

func TestMapHealthEvent_Pairing(t *testing.T) {
	m := testMapper()
	ts := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)

	cases := []struct {
		eventType model.HealthEventType
		wantType  string
		wantSev   string
		resolved  bool
		wantDedup string
	}{
		{model.HealthEventConnectionLost, TypeCameraConnection, SeverityError, false, "mibee-nvr/connection/cam-1"},
		{model.HealthEventConnectionRestored, TypeCameraConnection, SeverityInfo, true, "mibee-nvr/connection/cam-1"},
		{model.HealthEventFreezeDetected, TypeCameraFreeze, SeverityWarning, false, "mibee-nvr/freeze/cam-1"},
		{model.HealthEventFreezeRecovered, TypeCameraFreeze, SeverityInfo, true, "mibee-nvr/freeze/cam-1"},
		{model.HealthEventStreamAnomaly, TypeCameraStream, SeverityWarning, false, ""},
		{model.HealthEventQualityChanged, TypeCameraQuality, SeverityInfo, false, ""},
	}
	for _, tc := range cases {
		evt := model.HealthEvent{EventType: string(tc.eventType), Message: "msg", Status: "warning", CreatedAt: ts}
		a, ok := m.mapHealthEvent("cam-1", evt)
		if !ok {
			t.Fatalf("%s: not mapped", tc.eventType)
		}
		if a.Type != tc.wantType || a.Severity != tc.wantSev || a.Resolved != tc.resolved {
			t.Errorf("%s: got type=%s sev=%s resolved=%v, want %s/%s/%v", tc.eventType, a.Type, a.Severity, a.Resolved, tc.wantType, tc.wantSev, tc.resolved)
		}
		if a.DedupKey != tc.wantDedup {
			t.Errorf("%s: dedup=%q, want %q", tc.eventType, a.DedupKey, tc.wantDedup)
		}
		if a.SourceRef != "cam-1" {
			t.Errorf("%s: source_ref=%q, want cam-1", tc.eventType, a.SourceRef)
		}
		if a.Tags["camera_name"] != "Studio" {
			t.Errorf("%s: camera_name tag missing", tc.eventType)
		}
	}
}

func TestMapHealthEvent_UnknownTypeSkipped(t *testing.T) {
	m := testMapper()
	if _, ok := m.mapHealthEvent("cam-1", model.HealthEvent{EventType: "mystery"}); ok {
		t.Fatal("unknown health event type should not map")
	}
}

func TestMapAIEvent_EnrichmentAndSnapshot(t *testing.T) {
	m := testMapper()
	a, ok := m.mapAIEvent(map[string]any{
		"event_id":   int64(42),
		"camera_id":  "cam-1",
		"event_type": "raw-type",
		"severity":   "info",
	})
	if !ok {
		t.Fatal("ai event not mapped")
	}
	if a.Type != TypeAIDetection {
		t.Errorf("type=%s, want ai.detection", a.Type)
	}
	// Enrichment overrides the raw payload fields.
	if !strings.Contains(a.Title, "person") {
		t.Errorf("title should carry the enriched class, got %q", a.Title)
	}
	if a.Severity != SeverityWarning {
		t.Errorf("severity should come from the stored row, got %s", a.Severity)
	}
	if a.SnapshotURL != "http://nvr.local:9090/api/ai/events/42/snapshot?api_key=mbv_test" {
		t.Errorf("snapshot url=%q", a.SnapshotURL)
	}
	if !strings.Contains(a.Body, "zone=yard") {
		t.Errorf("body should carry zone, got %q", a.Body)
	}
}

func TestMapAIEvent_MissingFieldsSkipped(t *testing.T) {
	m := testMapper()
	if _, ok := m.mapAIEvent(map[string]any{"camera_id": "cam-1"}); ok {
		t.Fatal("ai event without event_id should not map")
	}
	if _, ok := m.mapAIEvent("not-a-map"); ok {
		t.Fatal("non-map payload should not map")
	}
}

func TestMapAIEvent_NoSnapshotsConfigured(t *testing.T) {
	m := &mapper{}
	a, ok := m.mapAIEvent(map[string]any{"event_id": int64(7), "camera_id": "cam-x", "event_type": "person"})
	if !ok {
		t.Fatal("ai event should map without snapshot config")
	}
	if a.SnapshotURL != "" {
		t.Errorf("snapshot url should be empty, got %q", a.SnapshotURL)
	}
}

func TestMapStorageHealth(t *testing.T) {
	m := testMapper()
	a, ok := m.mapStorageHealth(event.StorageHealthChanged{
		CameraID: "cam-1", PreviousState: "healthy", CurrentState: "failed",
	})
	if !ok {
		t.Fatal("storage event not mapped")
	}
	if a.Type != TypeStorageHealth || a.Severity != SeverityError || a.Resolved {
		t.Errorf("failed state: got %s/%s/%v", a.Type, a.Severity, a.Resolved)
	}
	if a.DedupKey != "mibee-nvr/storage/cam-1" {
		t.Errorf("dedup=%q", a.DedupKey)
	}
	if !strings.Contains(a.Body, "healthy → failed") {
		t.Errorf("body should carry the transition, got %q", a.Body)
	}

	b, _ := m.mapStorageHealth(event.StorageHealthChanged{CurrentState: "healthy"})
	if !b.Resolved || b.Severity != SeverityInfo {
		t.Errorf("healthy state should resolve: got %s/%v", b.Severity, b.Resolved)
	}
	if b.DedupKey != "mibee-nvr/storage" {
		t.Errorf("global dedup=%q, want mibee-nvr/storage", b.DedupKey)
	}
	if b.SourceRef != SystemSourceRef {
		t.Errorf("global source_ref=%q, want %s", b.SourceRef, SystemSourceRef)
	}
}

func TestMapCameraAddedAndQuality(t *testing.T) {
	m := testMapper()
	a, ok := m.mapCameraAdded(map[string]any{"camera_id": "cam-new", "name": "Porch", "source": "auto"})
	if !ok || a.Type != TypeCameraAdded || a.Title != "Camera added: Porch" {
		t.Fatalf("camera.added mapping wrong: %+v ok=%v", a, ok)
	}
	if a.Tags["camera_name"] != "Porch" || a.Tags["source"] != "auto" {
		t.Errorf("tags=%v", a.Tags)
	}

	q, ok := m.mapCameraQuality(map[string]any{"camera_id": "cam-1", "from": "HD", "to": "SD", "reason": "flaky wifi"})
	if !ok || q.Type != TypeCameraQuality || !strings.Contains(q.Title, "HD → SD") {
		t.Fatalf("camera.quality mapping wrong: %+v ok=%v", q, ok)
	}
	if q.Body != "flaky wifi" {
		t.Errorf("body=%q", q.Body)
	}
}

func TestMapGB28181Alarm_Priority(t *testing.T) {
	m := testMapper()
	base := event.GB28181AlarmEvent{DeviceID: "34020000002000000001", ChannelID: "gb-chan", AlarmType: "2"}
	for prio, want := range map[string]string{"1": SeverityCritical, "2": SeverityWarning, "3": SeverityInfo, "": SeverityWarning} {
		e := base
		e.AlarmPriority = prio
		a, ok := m.mapGB28181Alarm(e)
		if !ok {
			t.Fatalf("priority %q: not mapped", prio)
		}
		if a.Severity != want {
			t.Errorf("priority %q: severity=%s, want %s", prio, a.Severity, want)
		}
	}
	a, _ := m.mapGB28181Alarm(base)
	if a.SourceRef != "gb-chan" {
		t.Errorf("source_ref should prefer channel, got %q", a.SourceRef)
	}
}

func TestMapDoorbell_LifecyclePairs(t *testing.T) {
	m := testMapper()
	ring := event.DoorbellEvent{CallID: "call-9", CameraID: "cam-1", Name: "Front door", Timestamp: time.Now()}
	a, _ := m.mapDoorbell(event.TopicDoorbellRing, ring)
	if a.Type != TypeDoorbellRing || a.Severity != SeverityWarning || a.Resolved {
		t.Errorf("ring: %+v", a)
	}
	if a.DedupKey != "mibee-nvr/doorbell/call-9" {
		t.Errorf("ring dedup=%q", a.DedupKey)
	}
	if a.SnapshotURL != "http://nvr.local:9090/api/doorbell/calls/call-9/snapshot?api_key=mbv_test" {
		t.Errorf("ring snapshot=%q", a.SnapshotURL)
	}
	if a.Title != "Doorbell: Front door" {
		t.Errorf("ring title=%q", a.Title)
	}

	for _, topic := range []string{event.TopicDoorbellAnswered, event.TopicDoorbellRejected, event.TopicDoorbellEnded} {
		r, _ := m.mapDoorbell(topic, event.DoorbellEvent{CallID: "call-9", CameraID: "cam-1"})
		if !r.Resolved || r.DedupKey != a.DedupKey {
			t.Errorf("%s: resolved=%v dedup=%q", topic, r.Resolved, r.DedupKey)
		}
	}
}

func TestMapMotion(t *testing.T) {
	m := testMapper()
	a, ok := m.mapMotion(map[string]any{"camera_id": "cam-2", "camera_name": "Yard"})
	if !ok || a.Type != TypeCameraMotion || a.Severity != SeverityWarning {
		t.Fatalf("motion mapping wrong: %+v ok=%v", a, ok)
	}
	// camera_name from the payload wins (no DB round trip).
	if a.Tags["camera_name"] != "Yard" {
		t.Errorf("tags=%v", a.Tags)
	}
	if a.SnapshotURL != "http://nvr.local:9090/api/cameras/cam-2/snapshot?api_key=mbv_test" {
		t.Errorf("snapshot=%q", a.SnapshotURL)
	}
}

func TestAlertEnvelopeBasics(t *testing.T) {
	a := newAlert(time.Date(2026, 10, 25, 4, 5, 6, 7, time.FixedZone("CST", 8*3600)))
	if a.Schema != SchemaVersion {
		t.Errorf("schema=%q", a.Schema)
	}
	if a.EventID == "" {
		t.Error("event_id should be set")
	}
	if a.OccurredAt != "2026-10-24T20:05:06.000000007Z" {
		t.Errorf("occurred_at should be RFC3339Nano UTC, got %q", a.OccurredAt)
	}
	if SeverityRank(SeverityCritical) <= SeverityRank(SeverityInfo) {
		t.Error("severity ladder broken")
	}
	if IsValidSeverity("fatal") {
		t.Error("fatal is not a valid severity")
	}
	for _, ty := range KnownTypes() {
		if !IsValidType(ty) {
			t.Errorf("vocabulary self-check failed for %q", ty)
		}
	}
}
