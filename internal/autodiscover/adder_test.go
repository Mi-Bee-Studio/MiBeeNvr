package autodiscover

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/onvif"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
)

// newTestDB constructs an initialized in-temp-dir SQLite DB for testing, mirroring
// internal/camera's newTestManager but without the camera manager (the adder's
// pure-logic and dedup paths don't need a full recorder stack).
func newTestDB(t *testing.T) *storage.DB {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := storage.New(dbPath)
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Init(context.Background()); err != nil {
		t.Fatalf("db.Init: %v", err)
	}
	return db
}

// seedOnvifCamera inserts an ONVIF camera row directly, simulating one that was
// already added (manual or prior auto-add). Used to test dedup.
func seedOnvifCamera(t *testing.T, db *storage.DB, id, endpoint, serial string) {
	t.Helper()
	ctx := context.Background()
	if err := db.UpsertCamera(ctx, id, "Existing", "onvif", "", "", "", "", endpoint, "", ""); err != nil {
		t.Fatalf("UpsertCamera: %v", err)
	}
	if serial != "" {
		if err := db.UpdateCameraMetadata(ctx, id, "", "", "", "", serial, 0); err != nil {
			t.Fatalf("UpdateCameraMetadata: %v", err)
		}
	}
}

func TestCanonicalEndpoint(t *testing.T) {
	t.Helper()
	cases := []struct {
		endpoint string
		xaddrs   []string
		want     string
	}{
		{"http://1.2.3.4/onvif/device_service", nil, "http://1.2.3.4/onvif/device_service"},
		{"", []string{"http://1.2.3.5/onvif/device_service"}, "http://1.2.3.5/onvif/device_service"},
		{"", nil, ""},
		// Endpoint takes precedence over XAddrs.
		{"http://first/onvif/device_service", []string{"http://second/..."}, "http://first/onvif/device_service"},
	}
	for _, tc := range cases {
		if got := canonicalEndpoint(tc.endpoint, tc.xaddrs); got != tc.want {
			t.Errorf("canonicalEndpoint(%q, %v) = %q, want %q", tc.endpoint, tc.xaddrs, got, tc.want)
		}
	}
}

func TestMatchesIgnoreScope(t *testing.T) {
	t.Helper()
	deviceScopes := []string{
		"onvif://www.onvif.org/name/MiBeeCam",
		"onvif://www.onvif.org/hardware/MiBeeCam",
	}
	// Substring + case-insensitive.
	if !matchesIgnoreScope(deviceScopes, []string{"hardware/mibeecam"}) {
		t.Error("expected match on hardware/mibeecam (case-insensitive substring)")
	}
	if matchesIgnoreScope(deviceScopes, []string{"hardware/Aqara"}) {
		t.Error("did not expect match on unrelated scope")
	}
	if matchesIgnoreScope(deviceScopes, nil) {
		t.Error("nil ignore list should never match")
	}
	if matchesIgnoreScope(deviceScopes, []string{}) {
		t.Error("empty ignore list should never match")
	}
}

func TestExistsInDB_EndpointDedup(t *testing.T) {
	t.Helper()
	db := newTestDB(t)
	cfg := &config.AutoDiscoverConfig{}
	adder := NewAdder(cfg, nil, db, nil)

	endpoint := "http://192.168.1.50:80/onvif/device_service"
	seedOnvifCamera(t, db, "cam-existing", endpoint, "")

	// Same endpoint → duplicate.
	if !adder.existsInDB(context.Background(), endpoint, "") {
		t.Error("expected existsInDB=true for a known endpoint")
	}
	// Different endpoint, empty serial → not a duplicate.
	if adder.existsInDB(context.Background(), "http://192.168.1.99:80/onvif/device_service", "") {
		t.Error("expected existsInDB=false for an unknown endpoint")
	}
}

func TestExistsInDB_SerialDedup(t *testing.T) {
	t.Helper()
	db := newTestDB(t)
	cfg := &config.AutoDiscoverConfig{}
	adder := NewAdder(cfg, nil, db, nil)

	// A camera added at one IP, with a known serial.
	seedOnvifCamera(t, db, "cam-roamed", "http://192.168.1.50:80/onvif/device_service", "ABC123")

	// The same physical camera reappears at a NEW IP (different endpoint string)
	// but the same serial → must still be deduplicated, otherwise the NVR would
	// create a duplicate entry every time a camera's DHCP lease changes.
	if !adder.existsInDB(context.Background(), "http://192.168.1.99:80/onvif/device_service", "ABC123") {
		t.Error("expected existsInDB=true by serial match (roaming camera)")
	}
	// Different serial → not a duplicate.
	if adder.existsInDB(context.Background(), "http://192.168.1.99:80/onvif/device_service", "DIFFERENT") {
		t.Error("expected existsInDB=false for unknown serial")
	}
}

func TestExistsInDB_NonOnvifCameraWithoutOnvifEndpoint(t *testing.T) {
	t.Helper()
	db := newTestDB(t)
	ctx := context.Background()
	// An RTSP camera whose URL merely collides with an ONVIF device_service path
	// (but whose onvif_endpoint column is EMPTY) must NOT trigger dedup — there
	// is no evidence it is the same physical device. Dedup keys on the
	// onvif_endpoint column, not the url.
	if err := db.UpsertCamera(ctx, "cam-rtsp", "RTSP Cam", "rtsp", "h264",
		"rtsp://192.168.1.50/stream", "", "", "", "", ""); err != nil {
		t.Fatalf("UpsertCamera: %v", err)
	}
	cfg := &config.AutoDiscoverConfig{}
	adder := NewAdder(cfg, nil, db, nil)
	if adder.existsInDB(ctx, "http://192.168.1.50:80/onvif/device_service", "") {
		t.Error("RTSP camera with no onvif_endpoint must not trigger ONVIF dedup")
	}
}

// TestExistsInDB_NonOnvifCameraWithMatchingOnvifEndpoint covers the exact
// production regression: an ESP32 camera was manually added as protocol=http
// (direct MJPEG) but carries the device's onvif_endpoint (backfilled at add
// time). Auto-discover then saw the same device via ONVIF and — because dedup
// used to skip non-onvif cameras — enrolled a DUPLICATE protocol=onvif row,
// one of which was broken. Dedup must match onvif_endpoint across ALL protocols.
func TestExistsInDB_NonOnvifCameraWithMatchingOnvifEndpoint(t *testing.T) {
	t.Helper()
	db := newTestDB(t)
	ctx := context.Background()
	endpoint := "http://192.168.1.50:80/onvif/device_service"
	// Manually-added http camera (direct MJPEG) that ALSO has the onvif_endpoint
	// populated — exactly like the production ESP32 .224 case.
	if err := db.UpsertCamera(ctx, "cam-http", "MiBeeCam", "http", "jpeg",
		"http://192.168.1.50:81/stream", "", "", endpoint, "", ""); err != nil {
		t.Fatalf("UpsertCamera: %v", err)
	}
	cfg := &config.AutoDiscoverConfig{}
	adder := NewAdder(cfg, nil, db, nil)
	if !adder.existsInDB(ctx, endpoint, "") {
		t.Error("a manually-added http camera with a matching onvif_endpoint must trigger dedup (same physical device)")
	}
}

func TestExistsInDB_NilDB(t *testing.T) {
	t.Helper()
	// A nil DB (defensive) must report "does not exist" rather than panic.
	adder := NewAdder(&config.AutoDiscoverConfig{}, nil, nil, nil)
	if adder.existsInDB(context.Background(), "http://anything", "anyserial") {
		t.Error("nil DB should report existsInDB=false")
	}
}

func TestRecentlySeen_DedupWindow(t *testing.T) {
	t.Helper()
	adder := NewAdder(&config.AutoDiscoverConfig{}, nil, nil, nil)
	ep := "http://192.168.1.50/onvif/device_service"

	// Fresh endpoint: not seen.
	if adder.recentlySeen(ep) {
		t.Error("fresh endpoint should not be recentlySeen")
	}
	// After markSeen, it is within the window.
	adder.markSeen(ep)
	if !adder.recentlySeen(ep) {
		t.Error("endpoint marked seen should be recentlySeen")
	}
	// A different endpoint is still fresh.
	if adder.recentlySeen("http://other") {
		t.Error("unrelated endpoint should not be recentlySeen")
	}
}

func TestClassify_NoCredsUnauthDevice(t *testing.T) {
	t.Helper()
	// An unauthenticated device (ESP32 MiBeeCam): enrichment filled in
	// Manufacturer/Serial, no default creds configured → classify as "active".
	// This is the happy path for the open-firmware companion cameras.
	cfg := &config.AutoDiscoverConfig{} // no default creds
	adder := NewAdder(cfg, nil, nil, nil)
	dev := onvif.DiscoveredDevice{
		Manufacturer: "MiBeeCam",
		Serial:       "c82e1845d868",
	}
	got := adder.classify(context.Background(), "http://192.168.1.50/onvif/device_service", dev)
	if got != "active" {
		t.Errorf("classify unauthenticated enrichable device = %q, want active", got)
	}
}

func TestClassify_NoCredsNoEnrichment(t *testing.T) {
	t.Helper()
	// No default creds AND enrichment yielded nothing (device rejected
	// unauthenticated GetDeviceInformation) → must be pending_activation, since
	// we cannot know whether the device is open or merely needs auth.
	cfg := &config.AutoDiscoverConfig{}
	adder := NewAdder(cfg, nil, nil, nil)
	dev := onvif.DiscoveredDevice{} // empty: no Manufacturer/Serial
	got := adder.classify(context.Background(), "http://192.168.1.50/onvif/device_service", dev)
	if got != "pending_activation" {
		t.Errorf("classify unknown-auth device = %q, want pending_activation", got)
	}
}

func TestDefaultCredsForState(t *testing.T) {
	t.Helper()
	cfg := &config.AutoDiscoverConfig{
		DefaultUsername: "admin",
		DefaultPassword: "pass",
	}
	adder := NewAdder(cfg, nil, nil, nil)
	// Active: persist default creds so the recorder can connect.
	if got := adder.defaultCredsForState("active"); got != "admin" {
		t.Errorf("active state creds = %q, want admin", got)
	}
	// Pending: no creds stored (user supplies them at activation).
	if got := adder.defaultCredsForState("pending_activation"); got != "" {
		t.Errorf("pending state creds = %q, want empty", got)
	}
}
