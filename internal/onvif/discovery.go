package onvif

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	discovery "github.com/mickeyzzc/onvif-go/v2/discovery"
)

const defaultDiscoveryTimeout = 5 * time.Second

var discoverFunc atomic.Pointer[func(ctx context.Context, timeout time.Duration, enrich bool) *DiscoveryResult]

func init() {
	defaultDiscover := discover
	discoverFunc.Store(&defaultDiscover)
}

// SetDiscoverFuncForTest replaces the discovery implementation for the
// duration of a test. Pass nil to restore the default (real WS-Discovery). It
// exists so callers in other packages (e.g. internal/api handler tests) can
// make the /api/onvif/discover endpoint deterministic without performing real
// network multicast. The returned restore func MUST be deferred by the caller.
//
// Usage:
//
//	restore := onvif.SetDiscoverFuncForTest(func(ctx context.Context, timeout time.Duration, enrich bool) *onvif.DiscoveryResult {
//	    return &onvif.DiscoveryResult{Devices: []onvif.DiscoveredDevice{}}
//	})
//	defer restore()
func SetDiscoverFuncForTest(fn func(ctx context.Context, timeout time.Duration, enrich bool) *DiscoveryResult) (restore func()) {
	prev := discoverFunc.Load()
	if fn == nil {
		defaultDiscover := discover
		fn = defaultDiscover
	}
	discoverFunc.Store(&fn)
	return func() { discoverFunc.Store(prev) }
}

// callDiscover invokes the current discovery implementation.
func callDiscover(ctx context.Context, timeout time.Duration, enrich bool) *DiscoveryResult {
	if fn := discoverFunc.Load(); fn != nil {
		return (*fn)(ctx, timeout, enrich)
	}
	return discover(ctx, timeout, enrich)
}

// Discover performs WS-Discovery to find ONVIF devices on the local network
// via UDP multicast, then enriches each device with GetDeviceInformation.
// The enrichment runs in parallel and does not require authentication.
// Returns a DiscoveryResult with categorized errors.
// The result always contains a non-nil Devices slice (empty when no devices found).
//
// This is the variant for callers that consume the enriched fields directly
// (e.g. the manual-scan API handler, which returns Manufacturer/Firmware to the
// UI). Background callers that re-enrich themselves (autodiscover.Scanner →
// Adder.HandleDiscovered) should use DiscoverNoEnrich to avoid a redundant
// GetDeviceInformation round-trip per device (issue #161).
func Discover(ctx context.Context, timeout time.Duration) *DiscoveryResult {
	return callDiscover(ctx, timeout, true)
}

// DiscoverNoEnrich is the same as Discover but WITHOUT the internal
// GetDeviceInformation enrichment pass. Use it when the caller will enrich the
// devices itself (and wants its own dedup/gating to apply BEFORE enrichment) —
// notably autodiscover.Scanner, whose Adder.HandleDiscovered re-runs
// EnrichDevice and would otherwise pay for GetDeviceInformation twice per
// device on the first scan (once inside Discover, once inside HandleDiscovered).
// The returned devices have Endpoint/XAddrs/Scopes populated but
// Manufacturer/Model/Firmware/Serial empty (filled in later by the caller).
func DiscoverNoEnrich(ctx context.Context, timeout time.Duration) *DiscoveryResult {
	return callDiscover(ctx, timeout, false)
}

// discover is the shared core of Discover / DiscoverNoEnrich: library
// WS-Discovery, non-ONVIF responder filtering, and (when enrich) the library's
// parallel unauthenticated GetDeviceInformation pass on the filtered set.
func discover(ctx context.Context, timeout time.Duration, enrich bool) *DiscoveryResult {
	if timeout <= 0 {
		timeout = defaultDiscoveryTimeout
	}

	logger.Info("starting ONVIF device discovery", "timeout", timeout, "enrich", enrich)

	devices, err := discovery.Discover(ctx, timeout)
	if err != nil {
		logger.Warn("WS-Discovery returned error", "error", err)
		return &DiscoveryResult{
			Devices: []DiscoveredDevice{},
			Error:   categorizeDiscoveryError(ctx, err),
		}
	}

	// Filter non-ONVIF responders BEFORE enrichment so its per-device budget
	// is spent only on cameras (same filter MapDiscoveredDevices applies).
	filtered := make([]*discovery.Device, 0, len(devices))
	for _, d := range devices {
		if isONVIFDevice(d) {
			filtered = append(filtered, d)
		}
	}

	result := MapDiscoveredDevices(filtered)
	if len(result) == 0 {
		logger.Info("ONVIF discovery completed, no devices found")
		return &DiscoveryResult{
			Devices: []DiscoveredDevice{},
			Error: &DiscoveryError{
				Category: "NO_DEVICES",
				Message:  "no ONVIF devices found on the network",
			},
		}
	}

	if enrich && len(filtered) > 0 {
		// Fresh background context with its own timeout so enrichment is not
		// affected by the (already expired) discovery context.
		enrichCtx, enrichCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer enrichCancel()
		discovery.EnrichDevices(enrichCtx, filtered)
		result = MapDiscoveredDevices(filtered)
	}

	logger.Info("ONVIF discovery completed", "device_count", len(result), "enriched", enrich)
	return &DiscoveryResult{Devices: result}
}

// ProbeDevice probes an ONVIF device at a specific host:port via the library's
// two-strategy probe (WS-Discovery Probe over HTTP, then unauthenticated
// GetDeviceInformation as fallback for cameras that reject Probe).
// A refused/unreachable endpoint is an ERROR (the API handler surfaces it as
// "probe failed"); a reachable endpoint that is not ONVIF returns (nil, nil).
func ProbeDevice(ctx context.Context, host string, port int, timeout time.Duration) (device *DiscoveredDevice, err error) {
	if timeout <= 0 {
		timeout = defaultDiscoveryTimeout
	}

	logger.Info("probing ONVIF device", "host", host, "port", port, "timeout", timeout)

	// Reachability pre-check, preserving the historical error contract: a
	// dead endpoint must not silently look like "no ONVIF here".
	dialCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var d net.Dialer
	conn, derr := d.DialContext(dialCtx, "tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if derr != nil {
		return nil, fmt.Errorf("probe request failed: %w", derr)
	}
	_ = conn.Close()

	dev := discovery.ProbeEndpoint(ctx, host, port, timeout)
	if dev == nil {
		return nil, nil
	}

	mapped := MapDiscoveredDevice(dev)
	// The GetDeviceInformation fallback carries no WS-Discovery EndpointRef;
	// the serial is the stable identifier in that case (matches what the
	// add-camera flow persists as stable_id).
	if mapped.UUID == "" && mapped.Serial != "" {
		mapped.UUID = mapped.Serial
	}
	return &mapped, nil
}

// categorizeDiscoveryError maps a raw discovery failure to the UI-facing
// taxonomy (NETWORK / TIMEOUT / NO_DEVICES / PARSE_ERROR).
func categorizeDiscoveryError(ctx context.Context, err error) *DiscoveryError {
	if err == nil {
		return nil
	}

	msg := err.Error()

	ctxErr := ctx.Err()
	if ctxErr == context.DeadlineExceeded {
		return &DiscoveryError{Category: "TIMEOUT", Message: "discovery timed out: " + msg}
	}
	if ctxErr == context.Canceled {
		return &DiscoveryError{Category: "TIMEOUT", Message: "discovery was cancelled"}
	}
	if strings.Contains(msg, "deadline exceeded") || strings.Contains(msg, "timeout") {
		return &DiscoveryError{Category: "TIMEOUT", Message: "discovery timed out: " + msg}
	}

	if strings.Contains(msg, "network is unreachable") ||
		strings.Contains(msg, "no route to host") ||
		strings.Contains(msg, "connection refused") ||
		strings.Contains(msg, "dial ") ||
		strings.Contains(msg, "resolve") ||
		strings.Contains(msg, "DNS") {
		return &DiscoveryError{Category: "NETWORK", Message: "network error: " + msg}
	}

	// Default to PARSE_ERROR for unexpected errors
	return &DiscoveryError{Category: "PARSE_ERROR", Message: msg}
}
