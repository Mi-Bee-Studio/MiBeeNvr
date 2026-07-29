// Package autodiscover implements background ONVIF device discovery and
// automatic camera enrollment — the "plug-and-play" experience where a camera
// appears in the NVR the moment it joins the network, with no manual scan.
//
// Two complementary discovery modes run concurrently:
//   - Passive: a resident WS-Discovery Hello listener (internal/onvif.HelloListener)
//     reacts the instant a device announces itself on power-on (zero latency).
//   - Active: a periodic multicast Probe sweep (Scanner) catches devices that
//     did not send a Hello — e.g. after a silent IP change, or devices that
//     only respond to Probe.
//
// Both modes funnel discovered devices through Adder.HandleDiscovered, which
// dedupes (by ONVIF endpoint and serial), enriches via GetDeviceInformation,
// classifies by auth requirement, and enrolls the camera. Authenticated devices
// whose credentials are unknown are persisted in "pending_activation" state
// (visible but recorder not started) for the user to activate later.
//
// The whole subsystem defaults to OFF (config.AutoDiscoverConfig.Enabled); it
// must be explicitly enabled by the user.
package autodiscover

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/camera"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/event"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/onvif"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
)

var logger = slog.Default().With("component", "autodiscover")

// dedupWindow suppresses repeated discovery of the same device within this
// interval. WS-Discovery Hello is sent periodically (some firmware every ~30s)
// and a device that fails to enroll should not be retried every cycle. The
// window is long enough to cover a few Hello cycles but short enough that a
// transient failure (e.g. enrichment timeout) is retried within minutes.
const dedupWindow = 5 * time.Minute

// CameraEnroller is the subset of *camera.CameraManager that Adder needs.
// Defined as an interface so autodiscover's own tests can inject a fake without
// standing up a full CameraManager (which requires a real DB, store, recorder
// factory, etc.). *camera.CameraManager satisfies this interface — production
// wiring in service.New passes the concrete type unchanged.
type CameraEnroller interface {
	// AddCamera enrolls a brand-new camera. Returns the assigned camera ID.
	AddCamera(ctx context.Context, cam config.CameraConfig) (string, error)
	// UpdateCamera applies a partial update (e.g. a new ONVIFEndpoint after IP
	// roaming). Returns the updated config.
	UpdateCamera(ctx context.Context, cameraID string, updates camera.CameraUpdate) (*config.CameraConfig, error)
	// RestartRecorder stops and recreates the recorder for a camera, serialized
	// per-camera via withCameraLifecycle (safe against concurrent health restarts).
	RestartRecorder(ctx context.Context, cameraID string) error
}

// dedupKey is the in-memory dedup identity. Prefer the device's hardware serial
// (stable across IP changes); fall back to the endpoint string when the serial
// is unavailable (e.g. enrichment failed for an auth-required device). Keying on
// serial means a roaming device that re-announces at a new IP is still
// suppressed within dedupWindow — endpoint-only keying would let it retrigger
// every cycle (issue #121).
type dedupKey struct {
	identity string
}

// makeDedupKey returns the dedup key for a device. serial takes precedence;
// empty serial falls back to a normalized endpoint (so http://x:80 and
// http://x produce the same key).
func makeDedupKey(endpoint, serial string) dedupKey {
	if s := strings.TrimSpace(serial); s != "" {
		return dedupKey{identity: "serial:" + s}
	}
	return dedupKey{identity: "endpoint:" + normalizeEndpoint(endpoint)}
}

// Adder is the shared enrollment pipeline invoked by both the passive listener
// and the active scanner. It is the single place that decides whether a
// discovered device becomes a camera, and in what state.
//
// Adder is safe for concurrent use: a mutex guards the in-memory dedup map, and
// persistence dedup uses the DB as the source of truth. HandleDiscovered never
// blocks the caller for long — enrichment and credential probing run on a
// dedicated goroutine per device.
type Adder struct {
	camMgr CameraEnroller
	db     *storage.DB
	cfg    *config.AutoDiscoverConfig
	bus    *event.EventBus

	// in-memory dedup: device identity → last attempted time. Guards against a
	// chatty device (repeated Hello) retriggering enrollment. Persisted cameras
	// are the authoritative dedup; this map only short-circuits the
	// enrichment/probe work for devices already known to be unenrollable (e.g.
	// pending activation). Keyed by serial when available, endpoint otherwise
	// (see makeDedupKey).
	dedup   map[dedupKey]time.Time
	dedupMu sync.Mutex
}

// NewAdder constructs an Adder. cfg, db, and camMgr must be non-nil; bus may be
// nil (events are silently skipped). camMgr is typed as CameraEnroller so tests
// can inject a fake; pass a *camera.CameraManager in production.
func NewAdder(cfg *config.AutoDiscoverConfig, camMgr CameraEnroller, db *storage.DB, bus *event.EventBus) *Adder {
	return &Adder{
		camMgr: camMgr,
		db:     db,
		cfg:    cfg,
		bus:    bus,
		dedup:  make(map[dedupKey]time.Time),
	}
}

// HandleDiscovered processes one discovered device end-to-end: dedup → enrich →
// classify → enroll. It runs synchronously and may take a few seconds (ONVIF
// enrichment + optional credential probe), so callers (listener, scanner) must
// invoke it in a goroutine to avoid blocking their loops.
//
// All errors are logged and swallowed — a single device failure never affects
// discovery of others.
func (a *Adder) HandleDiscovered(ctx context.Context, dev onvif.DiscoveredDevice) {
	endpoint := canonicalEndpoint(dev.Endpoint, dev.XAddrs)
	if endpoint == "" {
		return
	}

	// 1. Scope deny-list (e.g. exclude a legacy hardware line).
	if matchesIgnoreScope(dev.Scopes, a.cfg.IgnoreScopes) {
		logger.Debug("skipping device: matches ignore_scopes", "endpoint", endpoint, "scopes", dev.Scopes)
		return
	}

	// 2. In-memory dedup window — short-circuits chatty devices before any
	// network work. A device already enrolled (DB dedup, below) is also caught
	// here for the duration of the window, but we re-check the DB after the
	// window expires to pick up deletions.
	//
	// NOTE: dev.Serial is empty before enrichment (step 3), so this first check
	// is endpoint-keyed. After enrichment, step 4 re-marks the device under its
	// serial key so subsequent announcements at a NEW IP are still suppressed
	// within the window (issue #121).
	if a.recentlySeen(endpoint, dev.Serial) {
		return
	}

	// 2b. Reserve the endpoint in the dedup map BEFORE the (slow) enrich step.
	// The passive listener (WS-Discovery Hello) and the active scanner (Probe)
	// run concurrently and report the same device from two goroutines; the
	// endpoint strings differ only by a default port (e.g. http://x:80 from
	// probe vs http://x from Hello), which makeDedupKey normalizes to the same
	// key. Without reserving here, the second path's recentlySeen check (above)
	// runs while the first is still inside enrich — a classic check-then-act
	// race — so BOTH paths call EnrichDevice (issue #161). Reserving under the
	// endpoint key now makes the second path's recentlySeen hit on its next
	// iteration. The serial key is still marked later (step 4 / enroll) once
	// enrich fills it in. The reserve expires naturally after dedupWindow, so a
	// genuinely transient enrich failure is retried within minutes.
	a.reserveSeen(endpoint)

	// 3. Enrich with GetDeviceInformation (unauthenticated). Fills Serial —
	// needed for both the stable_id (IP self-healing) and DB dedup.
	enrichCtx, enrichCancel := context.WithTimeout(ctx, 5*time.Second)
	a.enrich(enrichCtx, &dev, endpoint)
	enrichCancel()

	// 4. Persisted dedup: skip if a camera with the same stable_id, endpoint, or
	// serial already exists. When the match is by stable_id or serial AND the
	// endpoint actually changed, UPDATE the existing camera's endpoint — this is
	// the IP-roaming fix (issue #121). If the endpoint is UNCHANGED (the device
	// simply re-announced at its current address), skip the update to avoid
	// pointlessly restarting the recorder every discovery cycle.
	existingID, matchKind := a.findExistingCamera(ctx, endpoint, dev.Serial, dev.Serial)
	if existingID != "" {
		if (matchKind == "stable_id" || matchKind == "serial") && a.camMgr != nil && a.endpointChanged(ctx, existingID, endpoint) {
			a.updateEndpointForRoaming(ctx, existingID, endpoint, dev)
		}
		// Mark BOTH keys seen (endpoint + serial) so the next announcement is
		// suppressed regardless of whether its serial is known yet (before
		// enrichment the serial is empty, so only the endpoint key would match;
		// after enrichment only the serial key would match — marking both closes
		// the gap that let chatty devices retrigger every cycle).
		a.markSeenBothKeys(endpoint, dev.Serial)
		return
	}

	// 5. Classify: determine whether the device is usable as-is (no auth, or
	// auth works with default creds) or needs the user to supply credentials.
	state := a.classify(ctx, endpoint, dev)

	// 6. Enroll.
	a.enroll(ctx, dev, endpoint, state)
}

// canonicalEndpoint returns the device service URL to use as the dedup key and
// the camera's ONVIFEndpoint. Prefers the explicit Endpoint field; falls back to
// the first XAddrs entry. Empty if neither is present (device not usable).
//
// The result is normalized via normalizeEndpoint so that semantically-identical
// endpoints (e.g. http://1.2.3.4:80/... vs http://1.2.3.4/...) produce the same
// string — critical for dedup and endpointChanged comparisons. Without this,
// a device added via manual probe (which forces :80) would appear "changed" when
// auto-discover later sees it via WS-Discovery Hello (device-controlled XAddr
// format, often without :80), triggering a spurious recorder restart every
// dedup window. Same bug class as the trailing-slash issue fixed in PR #123.
func canonicalEndpoint(endpoint string, xaddrs []string) string {
	if endpoint != "" {
		return normalizeEndpoint(endpoint)
	}
	if len(xaddrs) > 0 {
		return normalizeEndpoint(xaddrs[0])
	}
	return ""
}

// normalizeEndpoint canonicalizes an ONVIF device-service URL. It is a thin
// wrapper around storage.NormalizeOnvifEndpoint so that the canonical form is
// identical between the autodiscover discovery path and the storage write/dedup
// paths — preventing :80-vs-no-port mismatches from defeating dedup (#175).
// Kept as an unexported alias for the existing call sites in this package.
func normalizeEndpoint(raw string) string {
	return storage.NormalizeOnvifEndpoint(raw)
}

// matchesIgnoreScope reports whether any of the device's scopes contains any
// configured ignore substring. Substring match (not exact) so "hardware/Aqara"
// matches "onvif://www.onvif.org/hardware/AqaraG2". Case-insensitive.
func matchesIgnoreScope(deviceScopes, ignore []string) bool {
	if len(ignore) == 0 {
		return false
	}
	for _, ds := range deviceScopes {
		low := strings.ToLower(ds)
		for _, ig := range ignore {
			if strings.Contains(low, strings.ToLower(ig)) {
				return true
			}
		}
	}
	return false
}

// enrich fills Manufacturer/Model/Firmware/Serial via GetDeviceInformation.
// Failures are non-fatal: the device is enrolled with whatever the Hello/Probe
// advertised. Logged at debug level since enrichment commonly fails for
// authenticated devices (they reject unauthenticated GetDeviceInformation).
func (a *Adder) enrich(ctx context.Context, dev *onvif.DiscoveredDevice, endpoint string) {
	dev.Endpoint = endpoint
	onvif.EnrichDevice(ctx, dev)
}

// findExistingCamera reports whether a camera already persists with the given
// stable_id, endpoint, or serial, and returns the matching camera ID + match
// kind so the caller can decide whether to UPDATE the endpoint (IP-roaming
// case) or simply skip (same-address case).
//
// matchKind values:
//   - ""          : no existing camera (proceed to enroll)
//   - "stable_id" : same physical device (by ONVIF serial / stable_id), possibly at a NEW endpoint - caller should UPDATE the endpoint
//   - "serial"    : same physical device (by serial_number column), possibly at a NEW endpoint - caller should UPDATE the endpoint
//   - "endpoint"  : same device at the SAME endpoint already enrolled - caller should skip (nothing to update)
//
// stable_id is checked first (strongest identity signal), then endpoint+serial.
// A nil DB disables persisted dedup (returns "", "").
//
// The query covers ALL camera rows — including ARCHIVED ones — via
// CameraIDByStableID and CameraIDByOnvifEndpoint. This matters because
// ListCameras only returns archived=0 rows; without the archived-inclusive
// lookup, archiving a camera would make it invisible to dedup and
// auto-discover would immediately re-enroll the same physical device the
// user just archived (verified in production: archiving .224, then
// auto-discover re-added it within 60s).
//
// Endpoint dedup is protocol-agnostic: a camera manually added as protocol=http
// (direct MJPEG) still carries the device's onvif_endpoint (backfilled at add
// time), so an ONVIF device discovered later must NOT be re-enrolled under a
// second protocol=onvif row. Serial is ONVIF-specific (serial_number column).
func (a *Adder) findExistingCamera(ctx context.Context, endpoint, serial, stableID string) (cameraID, matchKind string) {
	if a.db == nil {
		return "", ""
	}

	// Priority 1: stable_id match. A device with the same ONVIF serial that
	// was previously enrolled (and whose IP may have changed) is the same
	// physical camera — the caller will UPDATE its endpoint.
	if stableID != "" {
		id, err := a.db.CameraIDByStableID(ctx, stableID)
		if err != nil {
			logger.Warn("dedup: CameraIDByStableID failed, falling back to endpoint dedup", "error", err)
		} else if id != "" {
			return id, "stable_id"
		}
	}

	// Priority 2: endpoint or serial match (existing dedup logic).
	// Covers non-ONVIF cameras without a stable_id, and the serial_number
	// fallback for devices whose enrichment succeeded.
	id, kind, err := a.db.CameraIDByOnvifEndpoint(ctx, endpoint, serial)
	if err != nil {
		logger.Warn("dedup: CameraIDByOnvifEndpoint failed, skipping dedup this cycle", "error", err)
		return "", ""
	}
	return id, kind
}

// endpointChanged reports whether the discovered endpoint differs from the
// existing camera's current onvif_endpoint. This guards the roaming-update path
// against the common case of a device simply re-announcing at its CURRENT
// address (WS-Discovery Hello is sent periodically, and the active scanner
// probes every cycle). Without this check, every discovery cycle would restart
// the recorder — disrupting recording and live preview (regression introduced
// by the initial #121 fix, where a stable_id match unconditionally triggered
// updateEndpointForRoaming).
//
// Comparison normalizes the URLs via normalizeEndpoint (lowercases scheme/host,
// elides default ports :80/:443, strips trailing slashes) so that
// semantically-identical endpoints from different discovery paths (manual probe
// vs WS-Discovery Hello) compare equal — preventing spurious recorder restarts.
// On DB error or missing row, returns true (fail-open: attempt the update, which
// is idempotent and will no-op if the endpoint is in fact unchanged).
func (a *Adder) endpointChanged(ctx context.Context, cameraID, discoveredEndpoint string) bool {
	if a.db == nil {
		return true // can't check, fail-open
	}
	existing, err := a.db.GetCameraOnvifEndpoint(ctx, cameraID)
	if err != nil {
		logger.Warn("roaming check: failed to read existing endpoint, failing open", "camera_id", cameraID, "error", err)
		return true
	}
	return normalizeEndpoint(existing) != normalizeEndpoint(discoveredEndpoint)
}

// updateEndpointForRoaming updates an existing camera's ONVIF endpoint (and URL)
// to the newly-discovered address, then restarts its recorder so the new
// address takes effect immediately. This is the IP-roaming fix (issue #121):
// when auto-discover recognizes a known device at a NEW address, it updates the
// existing row instead of creating a duplicate.
//
// Best-effort: failures are logged and swallowed — a failed update does not
// block discovery of other devices, and the device will simply be retried on
// the next announcement cycle (subject to the dedup window).
//
// Concurrency: UpdateCamera only mutates the endpoint + evicts the cached ONVIF
// client (it does NOT trigger a recorder restart on its own, avoiding a race
// with concurrent health-loop restarts); RestartRecorder is serialized
// per-camera via withCameraLifecycle. This is safer than calling
// RediscoverAndReconnect (which would re-scan the subnet AND restart the
// recorder outside the lifecycle guard).
func (a *Adder) updateEndpointForRoaming(ctx context.Context, cameraID, newEndpoint string, dev onvif.DiscoveredDevice) {
	ep := newEndpoint
	updates := camera.CameraUpdate{
		ONVIFEndpoint: &ep,
		URL:           &ep,
	}
	cam, err := a.camMgr.UpdateCamera(ctx, cameraID, updates)
	if err != nil {
		// A CameraNotFoundError here means the camera is ARCHIVED (it exists in
		// the DB dedup tables but not in the live cfg.Cameras). That is expected
		// — an archived device that reappears should be left alone, not updated.
		logger.Debug("roaming update: camera not in live config (likely archived), skipping",
			"camera_id", cameraID, "endpoint", newEndpoint, "error", err)
		return
	}
	// Restart the recorder so it reconnects to the new endpoint. The old
	// recorder (if any) was bound to the old address and would keep failing.
	// For pending_activation cameras (no recorder running), this is a no-op.
	if err := a.camMgr.RestartRecorder(ctx, cameraID); err != nil {
		logger.Warn("roaming update: recorder restart failed (endpoint was still updated)",
			"camera_id", cameraID, "endpoint", newEndpoint, "error", err)
	}

	logger.Info("updated camera endpoint after IP roaming",
		"camera_id", cameraID, "name", cam.Name,
		"new_endpoint", newEndpoint, "serial", dev.Serial)

	// Notify subscribers (frontend toast, etc.) using camera.added with a
	// distinct source so the UI can message "IP updated" rather than "new camera".
	a.publish(ctx, event.TopicCameraAdded, map[string]any{
		"camera_id": cameraID,
		"name":      cam.Name,
		"endpoint":  newEndpoint,
		"source":    "auto-rediscover",
	})
}

// classify decides the activation state for a new device:
//   - "active": the device responds to unauthenticated ONVIF calls (e.g. ESP32
//     MiBeeCam), OR default credentials are configured and successfully probe
//     the device. The recorder will start immediately.
//   - "pending_activation": the device requires auth and no valid credentials
//     are available. The camera is persisted (visible in UI) but the recorder
//     is not started, avoiding a health-loop storm of auth failures.
func (a *Adder) classify(ctx context.Context, endpoint string, dev onvif.DiscoveredDevice) string {
	// No default creds configured: a successful unauthenticated enrichment
	// (Manufacturer/Serial present) implies the device is open; otherwise treat
	// it as needing activation. This is the common ESP32 path.
	if a.cfg.DefaultUsername == "" && a.cfg.DefaultPassword == "" {
		if dev.Manufacturer != "" || dev.Serial != "" {
			return "active"
		}
		return "pending_activation"
	}
	// Default creds configured: probe them. A successful authenticated probe
	// activates the device; failure falls back to pending.
	if a.probeCredentials(ctx, endpoint) {
		return "active"
	}
	return "pending_activation"
}

// probeCredentials tests whether the configured default credentials can reach
// the device's media service. Returns true on success, false on any failure
// (including timeout). Bounded to 3s to keep the enrollment pipeline moving.
func (a *Adder) probeCredentials(ctx context.Context, endpoint string) bool {
	probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	client := onvif.NewClient(endpoint, a.cfg.DefaultUsername, a.cfg.DefaultPassword)
	// GetProfiles is the cheapest authenticated call that proves the creds work
	// for media access. GetDeviceInformation would also work but some devices
	// allow it unauthenticated, giving a false positive.
	_, err := client.GetProfiles(probeCtx)
	return err == nil
}

// enroll persists the device as a camera in the given activation state and
// emits the camera.added event. On the ONVIFEndpoint/encoding front it mirrors
// the manual add path (web/src/lib/components/DiscoveryPanel.svelte):
// protocol=onvif, encoding left empty for runtime detection.
func (a *Adder) enroll(ctx context.Context, dev onvif.DiscoveredDevice, endpoint, state string) {
	a.markSeenBothKeys(endpoint, dev.Serial)

	name := dev.Name
	if name == "" {
		name = dev.Manufacturer
	}
	if name == "" {
		name = dev.Hardware
	}
	if name == "" {
		name = "ONVIF Camera"
	}

	cam := config.CameraConfig{
		Name:            name,
		Protocol:        "onvif",
		Encoding:        "", // runtime-detected by the ONVIF recorder
		ONVIFEndpoint:   endpoint,
		Username:        a.defaultCredsForState(state),
		Password:        a.defaultPasswordForState(state),
		StableID:        dev.Serial,
		ActivationState: state,
	}

	id, err := a.camMgr.AddCamera(ctx, cam)
	if err != nil {
		logger.Warn("failed to auto-add discovered camera", "endpoint", endpoint, "error", err)
		return
	}

	// Persist the activation_state column (AddCamera→UpsertCamera does not write
	// it; the column is managed via UpdateCameraActivationState, mirroring the
	// UpsertCameraIngest split pattern).
	if a.db != nil && state != "" && state != "active" {
		if err := a.db.UpdateCameraActivationState(ctx, id, state); err != nil {
			logger.Warn("failed to persist activation_state", "camera_id", id, "error", err)
		}
	}
	// Persist Brand/Model/Serial metadata (UpsertCamera does not write these;
	// they go through UpdateCameraMetadata like the manual add path).
	if a.db != nil && (dev.Manufacturer != "" || dev.Model != "" || dev.Serial != "") {
		if err := a.db.UpdateCameraMetadata(ctx, id, "", "", dev.Manufacturer, dev.Model, dev.Serial, 0); err != nil {
			logger.Warn("failed to persist camera metadata", "camera_id", id, "error", err)
		}
	}

	a.publish(ctx, event.TopicCameraAdded, map[string]any{
		"camera_id":        id,
		"name":             name,
		"endpoint":         endpoint,
		"activation_state": state,
		"source":           "auto",
	})
	logger.Info("auto-added camera", "camera_id", id, "name", name, "endpoint", endpoint, "state", state)
}

// defaultCredsForState returns the username to persist with the camera. For
// active devices probed with default creds, those creds are stored so the
// recorder can connect. For pending devices, no creds are stored (the user
// supplies them at activation time).
func (a *Adder) defaultCredsForState(state string) string {
	if state == "active" {
		return a.cfg.DefaultUsername
	}
	return ""
}

func (a *Adder) defaultPasswordForState(state string) string {
	if state == "active" {
		return a.cfg.DefaultPassword
	}
	return ""
}

// publish emits an event on the bus if one is configured. Non-blocking.
func (a *Adder) publish(ctx context.Context, topic string, data any) {
	if a.bus == nil {
		return
	}
	a.bus.Publish(ctx, topic, data)
}

// recentlySeen reports whether the device (keyed by serial, or endpoint when
// serial is unavailable) was attempted within dedupWindow. Thread-safe; GCs
// expired entries opportunistically.
//
// Note: the lookup key depends on whether the serial is known at the call site.
// Before enrichment the serial is empty, so this queries the endpoint key;
// after enrichment it queries the serial key. markSeenBothKeys therefore marks
// BOTH keys so the suppression holds across the enrich boundary (otherwise a
// chatty device re-announcing every cycle would retrigger after the serial
// becomes known — the second #121-fix regression).
func (a *Adder) recentlySeen(endpoint, serial string) bool {
	key := makeDedupKey(endpoint, serial)
	a.dedupMu.Lock()
	defer a.dedupMu.Unlock()
	now := time.Now()
	// GC expired entries to keep the map bounded (one device per identity, but
	// churn from IP changes could grow it).
	for k, t := range a.dedup {
		if now.Sub(t) > dedupWindow {
			delete(a.dedup, k)
		}
	}
	if t, ok := a.dedup[key]; ok {
		return now.Sub(t) < dedupWindow
	}
	return false
}

// markSeenBothKeys records the device as attempted now under BOTH its endpoint
// key and (when the serial is known) its serial key. This is necessary because
// recentlySeen's lookup key depends on whether the serial is populated at query
// time: the next announcement starts with an empty serial (endpoint key) and
// only gains it after enrichment (serial key). Marking just one would let the
// other lookup miss, retriggering enrichment/enrollment every cycle.
func (a *Adder) markSeenBothKeys(endpoint, serial string) {
	now := time.Now()
	a.dedupMu.Lock()
	a.dedup[makeDedupKey(endpoint, "")] = now // endpoint key (pre-enrichment lookups)
	if s := strings.TrimSpace(serial); s != "" {
		a.dedup[makeDedupKey(endpoint, s)] = now // serial key (post-enrichment lookups)
	}
	a.dedupMu.Unlock()
}

// reserveSeen records ONLY the endpoint key as seen now. It is the minimal
// pre-enrichment reservation that closes the probe/Hello race window of
// issue #161: between recentlySeen and markSeenBothKeys lies the (hundreds of
// ms) enrich step, during which a concurrently-reported discovery of the SAME
// device (different endpoint string, same normalized key) would slip past the
// recentlySeen gate. Unlike markSeenBothKeys, this intentionally does NOT mark
// the serial key — the serial is unknown before enrichment, and the full
// both-keys marking still happens later (in enroll, or in the existing-camera
// branch via markSeenBothKeys). Reserve is idempotent with markSeenBothKeys,
// which simply overwrites the endpoint key with a fresh timestamp.
func (a *Adder) reserveSeen(endpoint string) {
	now := time.Now()
	a.dedupMu.Lock()
	a.dedup[makeDedupKey(endpoint, "")] = now
	a.dedupMu.Unlock()
}
