package autodiscover

import (
	"context"
	"path/filepath"
	"sync"
	"testing"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/camera"
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
	if err := db.UpsertCamera(ctx, id, "Existing", "onvif", "", "", "", "", endpoint, "", "", ""); err != nil {
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

	// Same endpoint → duplicate (matchKind="endpoint").
	id, kind := adder.findExistingCamera(context.Background(), endpoint, "", "")
	if id != "cam-existing" || kind != "endpoint" {
		t.Errorf("findExistingCamera(known endpoint) = (%q, %q), want (cam-existing, endpoint)", id, kind)
	}
	// Different endpoint, empty serial → not a duplicate.
	id, kind = adder.findExistingCamera(context.Background(), "http://192.168.1.99:80/onvif/device_service", "", "")
	if id != "" || kind != "" {
		t.Errorf("findExistingCamera(unknown endpoint) = (%q, %q), want (\"\", \"\")", id, kind)
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
	id, kind := adder.findExistingCamera(context.Background(), "http://192.168.1.99:80/onvif/device_service", "ABC123", "")
	if id != "cam-roamed" || kind != "serial" {
		t.Errorf("findExistingCamera(serial match) = (%q, %q), want (cam-roamed, serial)", id, kind)
	}
	// Different serial → not a duplicate.
	id, kind = adder.findExistingCamera(context.Background(), "http://192.168.1.99:80/onvif/device_service", "DIFFERENT", "")
	if id != "" || kind != "" {
		t.Errorf("findExistingCamera(unknown serial) = (%q, %q), want (\"\", \"\")", id, kind)
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
		"rtsp://192.168.1.50/stream", "", "", "", "", "", ""); err != nil {
		t.Fatalf("UpsertCamera: %v", err)
	}
	cfg := &config.AutoDiscoverConfig{}
	adder := NewAdder(cfg, nil, db, nil)
	if id, _ := adder.findExistingCamera(ctx, "http://192.168.1.50:80/onvif/device_service", "", ""); id != "" {
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
		"http://192.168.1.50:81/stream", "", "", endpoint, "", "", ""); err != nil {
		t.Fatalf("UpsertCamera: %v", err)
	}
	cfg := &config.AutoDiscoverConfig{}
	adder := NewAdder(cfg, nil, db, nil)
	if id, _ := adder.findExistingCamera(ctx, endpoint, "", ""); id != "cam-http" {
		t.Error("a manually-added http camera with a matching onvif_endpoint must trigger dedup (same physical device)")
	}
}

func TestExistsInDB_NilDB(t *testing.T) {
	t.Helper()
	// A nil DB (defensive) must report "does not exist" rather than panic.
	adder := NewAdder(&config.AutoDiscoverConfig{}, nil, nil, nil)
	if id, _ := adder.findExistingCamera(context.Background(), "http://anything", "anyserial", ""); id != "" {
		t.Error("nil DB should report findExistingCamera=(\"\",\"\")")
	}
}

// TestExistsInDB_StableIDPriority verifies that stable_id dedup takes precedence
// over endpoint dedup. A camera whose IP changed (new endpoint) but has the same
// ONVIF serial (stored as stable_id) must be recognized as existing with
// matchKind="stable_id" (triggering an endpoint update, not just a skip).
func TestExistsInDB_StableIDPriority(t *testing.T) {
	t.Helper()
	db := newTestDB(t)
	ctx := context.Background()
	cfg := &config.AutoDiscoverConfig{}
	adder := NewAdder(cfg, nil, db, nil)

	// Seed a camera with stable_id="ABC" at one endpoint.
	endpoint := "http://192.168.1.50:80/onvif/device_service"
	if err := db.UpsertCamera(ctx, "cam-existing", "Existing", "onvif", "", "", "", "", endpoint, "", "", "ABC"); err != nil {
		t.Fatalf("UpsertCamera: %v", err)
	}

	// Same stable_id, DIFFERENT endpoint → matchKind="stable_id" (IP change →
	// caller should UPDATE the endpoint, not just skip).
	id, kind := adder.findExistingCamera(ctx, "http://192.168.1.99:80/onvif/device_service", "", "ABC")
	if id != "cam-existing" || kind != "stable_id" {
		t.Errorf("findExistingCamera(stable_id match, IP change) = (%q, %q), want (cam-existing, stable_id)", id, kind)
	}

	// Different stable_id → not a duplicate.
	id, kind = adder.findExistingCamera(ctx, "http://192.168.1.99:80/onvif/device_service", "", "XYZ")
	if id != "" || kind != "" {
		t.Errorf("findExistingCamera(unknown stable_id) = (%q, %q), want (\"\", \"\")", id, kind)
	}

	// Empty stableID + known endpoint → "endpoint" fallback (same address, skip).
	id, kind = adder.findExistingCamera(ctx, endpoint, "", "")
	if id != "cam-existing" || kind != "endpoint" {
		t.Errorf("findExistingCamera(endpoint fallback) = (%q, %q), want (cam-existing, endpoint)", id, kind)
	}

	// Empty stableID + unknown endpoint → not a duplicate.
	id, kind = adder.findExistingCamera(ctx, "http://unknown/onvif/device_service", "", "")
	if id != "" || kind != "" {
		t.Errorf("findExistingCamera(unknown endpoint) = (%q, %q), want (\"\", \"\")", id, kind)
	}
}

func TestRecentlySeen_DedupWindow(t *testing.T) {
	t.Helper()
	adder := NewAdder(&config.AutoDiscoverConfig{}, nil, nil, nil)
	ep := "http://192.168.1.50/onvif/device_service"

	// Fresh endpoint: not seen.
	if adder.recentlySeen(ep, "") {
		t.Error("fresh endpoint should not be recentlySeen")
	}
	// After markSeenBothKeys, it is within the window.
	adder.markSeenBothKeys(ep, "")
	if !adder.recentlySeen(ep, "") {
		t.Error("endpoint marked seen should be recentlySeen")
	}
	// A different endpoint is still fresh.
	if adder.recentlySeen("http://other", "") {
		t.Error("unrelated endpoint should not be recentlySeen")
	}
}

// TestRecentlySeen_SerialKeyedAcrossIPChange confirms the dedup is keyed by
// device serial (not endpoint), so a device that re-announces at a NEW IP is
// still suppressed within dedupWindow. This is the issue #121 fix: endpoint-only
// keying let the same device retrigger every cycle after an IP change.
func TestRecentlySeen_SerialKeyedAcrossIPChange(t *testing.T) {
	t.Helper()
	adder := NewAdder(&config.AutoDiscoverConfig{}, nil, nil, nil)
	const serial = "ABC123"
	oldEP := "http://192.168.1.50:80/onvif/device_service"
	newEP := "http://192.168.1.99:80/onvif/device_service"

	// Device marked seen at old IP (with its serial).
	adder.markSeenBothKeys(oldEP, serial)
	// Same device re-announces at NEW IP, same serial → must be suppressed.
	if !adder.recentlySeen(newEP, serial) {
		t.Error("a device marked seen by serial must be recentlySeen even at a new endpoint (IP roaming)")
	}
	// A DIFFERENT serial at the new IP is NOT suppressed.
	if adder.recentlySeen(newEP, "DIFFERENT") {
		t.Error("a different serial should not be recentlySeen")
	}
	// Empty serial (enrichment failed) falls back to endpoint keying (legacy).
	adder.markSeenBothKeys("http://10.0.0.1/onvif/device_service", "")
	if !adder.recentlySeen("http://10.0.0.1/onvif/device_service", "") {
		t.Error("endpoint fallback keying (empty serial) must still work")
	}
}

// TestRecentlySeen_PreEnrichmentEndpointLookupHits is the regression test for
// the second #121-fix bug: the passive listener + active scanner re-discover a
// device every cycle. The FIRST lookup (step 2) happens BEFORE enrichment, with
// an empty serial, so it queries the endpoint key. markSeenBothKeys must have
// marked the endpoint key (not just the serial key) on the previous cycle, or
// the device retriggered every time — disrupting recording/preview with endless
// recorder restarts. Verified in production: 212/251 were restarted ~10x/hour.
func TestRecentlySeen_PreEnrichmentEndpointLookupHits(t *testing.T) {
	t.Helper()
	adder := NewAdder(&config.AutoDiscoverConfig{}, nil, nil, nil)
	const ep = "http://192.168.1.50:80/onvif/device_service"
	const serial = "ABC123"

	// Simulate a completed discovery cycle: device processed with its serial.
	adder.markSeenBothKeys(ep, serial)
	// Next cycle re-announces the SAME device, but recentlySeen is called BEFORE
	// enrichment (serial unknown at this point) → endpoint-keyed lookup.
	if !adder.recentlySeen(ep, "") {
		t.Error("endpoint-keyed lookup (pre-enrichment) must hit after markSeenBothKeys — chatty devices would retrigger every cycle otherwise")
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

// fakeEnroller is a test double for CameraEnroller. It records calls so tests
// can assert that UpdateCamera + RestartRecorder were invoked (the IP-roaming
// fix path). AddCamera is included to satisfy the interface but unused in the
// roaming tests.
type fakeEnroller struct {
	mu sync.Mutex
	// cameraID → new endpoint passed to UpdateCamera
	updatedEndpoints map[string]string
	// cameraID → RestartRecorder called
	restartedIDs map[string]bool
	// optional: force UpdateCamera to fail
	updateErr error
	// optional: force RestartRecorder to fail
	restartErr error
}

func newFakeEnroller() *fakeEnroller {
	return &fakeEnroller{
		updatedEndpoints: make(map[string]string),
		restartedIDs:     make(map[string]bool),
	}
}

func (f *fakeEnroller) AddCamera(_ context.Context, _ config.CameraConfig) (string, error) {
	return "cam-fake", nil
}

func (f *fakeEnroller) UpdateCamera(_ context.Context, cameraID string, updates camera.CameraUpdate) (*config.CameraConfig, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.updateErr != nil {
		return nil, f.updateErr
	}
	if updates.ONVIFEndpoint != nil {
		f.updatedEndpoints[cameraID] = *updates.ONVIFEndpoint
	}
	name := "Fake Cam"
	return &config.CameraConfig{ID: cameraID, Name: name}, nil
}

func (f *fakeEnroller) RestartRecorder(_ context.Context, cameraID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.restartedIDs[cameraID] = true
	return f.restartErr
}

// TestEndpointChanged is the regression test for the recorder-storm bug: a
// device that re-announces at its CURRENT address must NOT trigger a roaming
// update (which restarts the recorder). Only a genuine endpoint change should.
// In production, the missing check restarted 212/251's recorder ~10x/hour,
// disrupting recording and live preview.
func TestEndpointChanged(t *testing.T) {
	t.Helper()
	db := newTestDB(t)
	ctx := context.Background()
	const camID = "cam-existing"
	const ep = "http://192.168.63.212:80/onvif/device_service"
	require := func(cond bool, msg string) {
		t.Helper()
		if !cond {
			t.Error(msg)
		}
	}

	// Seed a camera at ep.
	if err := db.UpsertCamera(ctx, camID, "Cam", "onvif", "", "", "", "", ep, "", "", "ABC"); err != nil {
		t.Fatalf("UpsertCamera: %v", err)
	}
	adder := NewAdder(&config.AutoDiscoverConfig{}, nil, db, nil)

	// Same endpoint (with trailing slash variant) → NOT changed.
	require(!adder.endpointChanged(ctx, camID, ep), "identical endpoint must report unchanged")
	require(!adder.endpointChanged(ctx, camID, ep+"/"), "trailing-slash variant must report unchanged (normalized)")

	// Genuinely different endpoint → changed.
	require(adder.endpointChanged(ctx, camID, "http://192.168.63.99:80/onvif/device_service"), "different IP must report changed")

	// Unknown camera ID → fail-open (true), so the update is attempted.
	require(adder.endpointChanged(ctx, camID+"-nope", ep), "unknown camera must fail-open (true)")
}

// TestEndpointChanged_PortAndSchemeNormalization is the regression test for the
// port/scheme normalization gap (issue #133): a device added via manual probe
// (which forces :80) must NOT appear "changed" when auto-discover later sees it
// via WS-Discovery Hello with a device-controlled XAddr format (often without
// :80). Without normalization, this triggered a spurious recorder restart every
// dedup window — same bug class as the trailing-slash issue (PR #123).
func TestEndpointChanged_PortAndSchemeNormalization(t *testing.T) {
	t.Helper()
	db := newTestDB(t)
	ctx := context.Background()

	cases := []struct {
		name       string
		stored     string
		discovered string
		changed    bool
	}{
		// Port normalization: :80 is default for http → same endpoint.
		{"explicit-80 vs no-port", "http://1.2.3.4:80/onvif/device_service", "http://1.2.3.4/onvif/device_service", false},
		{"no-port vs explicit-80", "http://1.2.3.4/onvif/device_service", "http://1.2.3.4:80/onvif/device_service", false},
		// Non-default port must still report changed.
		{"port-80 vs port-81", "http://1.2.3.4:80/onvif/device_service", "http://1.2.3.4:81/onvif/device_service", true},
		// HTTPS default port :443.
		{"https-443 vs no-port", "https://1.2.3.4:443/onvif/device_service", "https://1.2.3.4/onvif/device_service", false},
		// Scheme case-insensitive.
		{"HTTP vs http", "HTTP://1.2.3.4/onvif/device_service", "http://1.2.3.4/onvif/device_service", false},
		// Host case-insensitive.
		{"uppercase-host", "http://CAMERA.LOCAL/onvif/device_service", "http://camera.local/onvif/device_service", false},
		// Trailing slash still handled.
		{"trailing-slash", "http://1.2.3.4:80/onvif/device_service", "http://1.2.3.4/onvif/device_service/", false},
		// Different IP still changed.
		{"different-ip", "http://1.2.3.4/onvif/device_service", "http://1.2.3.5/onvif/device_service", true},
		// http vs https genuinely different (different listener).
		{"http-vs-https", "http://1.2.3.4/onvif/device_service", "https://1.2.3.4/onvif/device_service", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			camID := "cam-norm-" + tc.name
			if err := db.UpsertCamera(ctx, camID, "Cam", "onvif", "", "", "", "", tc.stored, "", "", "SERIAL-"+tc.name); err != nil {
				t.Fatalf("UpsertCamera: %v", err)
			}
			adder := NewAdder(&config.AutoDiscoverConfig{}, nil, db, nil)
			got := adder.endpointChanged(ctx, camID, tc.discovered)
			if got != tc.changed {
				t.Errorf("endpointChanged(stored=%q, discovered=%q) = %v, want %v",
					tc.stored, tc.discovered, got, tc.changed)
			}
		})
	}
}

// TestNormalizeEndpoint verifies the URL canonicalization helper directly.
func TestNormalizeEndpoint(t *testing.T) {
	t.Helper()
	cases := []struct {
		input string
		want  string
	}{
		{"", ""},
		{"http://1.2.3.4/onvif/device_service", "http://1.2.3.4/onvif/device_service"},
		{"http://1.2.3.4:80/onvif/device_service", "http://1.2.3.4/onvif/device_service"},
		{"https://1.2.3.4:443/onvif/device_service", "https://1.2.3.4/onvif/device_service"},
		{"http://1.2.3.4:8080/onvif/device_service", "http://1.2.3.4:8080/onvif/device_service"},
		{"HTTP://1.2.3.4/Path", "http://1.2.3.4/Path"},
		{"http://CAMERA.LOCAL/onvif/device_service", "http://camera.local/onvif/device_service"},
		{"http://1.2.3.4/onvif/device_service/", "http://1.2.3.4/onvif/device_service"},
		{"  http://1.2.3.4/onvif/device_service  ", "http://1.2.3.4/onvif/device_service"},
	}
	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			got := normalizeEndpoint(tc.input)
			if got != tc.want {
				t.Errorf("normalizeEndpoint(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

// TestCanonicalEndpoint_Normalizes verifies that canonicalEndpoint normalizes
// both the explicit endpoint and the XAddrs fallback path.
func TestCanonicalEndpoint_Normalizes(t *testing.T) {
	t.Helper()
	// Explicit endpoint with :80 → normalized to no-port.
	got := canonicalEndpoint("http://1.2.3.4:80/onvif/device_service", nil)
	want := "http://1.2.3.4/onvif/device_service"
	if got != want {
		t.Errorf("canonicalEndpoint(endpoint=:80) = %q, want %q", got, want)
	}
	// Fallback to XAddrs — also normalized.
	got = canonicalEndpoint("", []string{"http://1.2.3.4:80/onvif/device_service"})
	if got != want {
		t.Errorf("canonicalEndpoint(xaddrs=:80) = %q, want %q", got, want)
	}
}

// TestUpdateEndpointForRoaming verifies that when auto-discover recognizes a
// known device at a NEW endpoint, it updates the existing camera's endpoint and
// restarts its recorder (issue #121 core fix).
func TestUpdateEndpointForRoaming(t *testing.T) {
	t.Helper()
	enroller := newFakeEnroller()
	adder := NewAdder(&config.AutoDiscoverConfig{}, enroller, nil, nil)
	const cameraID = "cam-existing"
	const newEP = "http://192.168.1.99:80/onvif/device_service"
	dev := onvif.DiscoveredDevice{Serial: "ABC123", Endpoint: newEP}

	adder.updateEndpointForRoaming(context.Background(), cameraID, newEP, dev)

	enroller.mu.Lock()
	gotEP, updated := enroller.updatedEndpoints[cameraID]
	restarted := enroller.restartedIDs[cameraID]
	enroller.mu.Unlock()

	if !updated {
		t.Error("expected UpdateCamera to be called with the new endpoint")
	} else if gotEP != newEP {
		t.Errorf("UpdateCamera endpoint = %q, want %q", gotEP, newEP)
	}
	if !restarted {
		t.Error("expected RestartRecorder to be called so the new endpoint takes effect")
	}
}

// TestUpdateEndpointForRoaming_ArchivedCameraSkipped verifies that an archived
// camera (UpdateCamera returns a not-found error) is silently skipped — the
// device exists in the dedup tables but not in the live config, so updating it
// would fail. The error is swallowed (best-effort) and RestartRecorder is NOT
// called.
func TestUpdateEndpointForRoaming_ArchivedCameraSkipped(t *testing.T) {
	t.Helper()
	enroller := newFakeEnroller()
	enroller.updateErr = &cameraNotFoundError{cameraID: "cam-archived"}
	adder := NewAdder(&config.AutoDiscoverConfig{}, enroller, nil, nil)
	const cameraID = "cam-archived"
	const newEP = "http://192.168.1.99:80/onvif/device_service"

	// Must not panic and must not call RestartRecorder.
	adder.updateEndpointForRoaming(context.Background(), cameraID, newEP, onvif.DiscoveredDevice{})

	enroller.mu.Lock()
	restarted := enroller.restartedIDs[cameraID]
	enroller.mu.Unlock()
	if restarted {
		t.Error("RestartRecorder must NOT be called when UpdateCamera fails (archived camera)")
	}
}

// TestUpdateEndpointForRoaming_RestartFailureStillLogged verifies that a
// RestartRecorder failure does not panic and leaves the endpoint update in
// place (the endpoint is still updated even if the recorder can't restart —
// better to have the new address persisted than to roll it back).
func TestUpdateEndpointForRoaming_RestartFailureStillLogged(t *testing.T) {
	t.Helper()
	enroller := newFakeEnroller()
	enroller.restartErr = context.DeadlineExceeded
	adder := NewAdder(&config.AutoDiscoverConfig{}, enroller, nil, nil)
	const cameraID = "cam-existing"
	const newEP = "http://192.168.1.99:80/onvif/device_service"

	adder.updateEndpointForRoaming(context.Background(), cameraID, newEP, onvif.DiscoveredDevice{Serial: "X"})

	enroller.mu.Lock()
	_, updated := enroller.updatedEndpoints[cameraID]
	enroller.mu.Unlock()
	if !updated {
		t.Error("endpoint update must persist even when RestartRecorder fails")
	}
}

// cameraNotFoundError is a minimal stand-in for camera.CameraNotFoundError used
// by the archived-camera test (avoids importing the model package just for the
// error type — the updateEndpointForRoaming path checks err != nil, not the type).
type cameraNotFoundError struct{ cameraID string }

func (e *cameraNotFoundError) Error() string { return "camera " + e.cameraID + " not found" }
