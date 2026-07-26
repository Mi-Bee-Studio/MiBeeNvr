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
// empty serial falls back to endpoint (legacy behavior).
func makeDedupKey(endpoint, serial string) dedupKey {
	if s := strings.TrimSpace(serial); s != "" {
		return dedupKey{identity: "serial:" + s}
	}
	return dedupKey{identity: "endpoint:" + endpoint}
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

	// 3. Enrich with GetDeviceInformation (unauthenticated). Fills Serial —
	// needed for both the stable_id (IP self-healing) and DB dedup.
	enrichCtx, enrichCancel := context.WithTimeout(ctx, 5*time.Second)
	a.enrich(enrichCtx, &dev, endpoint)
	enrichCancel()

	// 4. Persisted dedup: skip if a camera with the same stable_id, endpoint, or
	// serial already exists. When the match is by stable_id or serial (the SAME
	// physical device at a NEW address), UPDATE the existing camera's endpoint
	// instead of silently skipping — this is the IP-roaming fix (issue #121).
	// An "endpoint" match means the same device at the same address is already
	// enrolled, so there is nothing to update.
	existingID, matchKind := a.findExistingCamera(ctx, endpoint, dev.Serial, dev.Serial)
	if existingID != "" {
		if (matchKind == "stable_id" || matchKind == "serial") && a.camMgr != nil {
			a.updateEndpointForRoaming(ctx, existingID, endpoint, dev)
		}
		a.markSeen(endpoint, dev.Serial) // refresh window under the (now-known) serial key
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
func canonicalEndpoint(endpoint string, xaddrs []string) string {
	if endpoint != "" {
		return endpoint
	}
	if len(xaddrs) > 0 {
		return xaddrs[0]
	}
	return ""
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
	a.markSeen(endpoint, dev.Serial)

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

// markSeen records that the device (keyed by serial, or endpoint when serial is
// unavailable) was attempted now.
func (a *Adder) markSeen(endpoint, serial string) {
	key := makeDedupKey(endpoint, serial)
	a.dedupMu.Lock()
	a.dedup[key] = time.Now()
	a.dedupMu.Unlock()
}
