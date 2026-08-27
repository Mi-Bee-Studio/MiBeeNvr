package onvif

import (
	"strings"

	"github.com/mickeyzzc/onvif-go/v2/discovery"
)

// MapDiscoveredDevice converts an onvif-go discovery.Device to the project's DiscoveredDevice.
func MapDiscoveredDevice(d *discovery.Device) DiscoveredDevice {
	name := d.GetName()
	endpoint := d.GetDeviceEndpoint()

	// Extract hardware/model from scopes if available
	var hardware string
	for _, scope := range d.Scopes {
		if strings.Contains(scope, "hardware") {
			parts := strings.Split(scope, "/")
			if len(parts) > 0 {
				hardware = parts[len(parts)-1]
			}
		}
	}

	return DiscoveredDevice{
		UUID:     d.EndpointRef,
		Name:     name,
		XAddrs:   d.XAddrs,
		Scopes:   d.Scopes,
		Hardware: hardware,
		Endpoint: endpoint,
	}
}

// isONVIFDevice reports whether a WS-Discovery ProbeMatch answerer is actually
// an ONVIF network video device, as opposed to a generic WS-Discovery responder
// (Synology NAS DSM on :5357/:5000, Windows machines, printers, scanners) that
// answers ANY Probe regardless of its d:Types filter.
//
// Per ONVIF Core Spec, a real camera's ProbeMatch carries either:
//   - Types containing "NetworkVideoTransmitter" (the ONVIF device type the
//     NVR's Probe explicitly asks for — see wsDiscoveryProbe), or
//   - a Scope beginning with "onvif://www.onvif.org/" (ONVIF scope namespace).
//
// Synology et al. advertise neither (their Types is "wsdiscovery:Device" or
// empty, and their scopes are device-vendor-specific), so filtering on these
// two signals drops them at the discovery boundary instead of letting them
// flow into HandleDiscovered → enrich → enroll as forever-pending_activation
// shells that can never connect (issue #266).
//
// The check is permissive on purpose: matching either signal keeps the device.
// This avoids false-negative drops of marginal ONVIF implementations that
// populate Scopes but leave Types empty (or vice versa).
func isONVIFDevice(d *discovery.Device) bool {
	return isONVIFSignal(strings.Join(d.Types, " "), d.Scopes)
}

// isONVIFSignal reports whether a WS-Discovery Types line (space-separated
// QNames) or Scopes list carries an ONVIF device signal: a Types entry whose
// local part contains NetworkVideoTransmitter, or any scope in the ONVIF
// namespace. Shared by the active Discover path (ProbeMatch filtering, #266)
// and the passive Hello listener (#554) so both ingress routes apply the same
// permissive-but-real gate — matching either signal keeps the device.
func isONVIFSignal(typesLine string, scopes []string) bool {
	for _, t := range strings.Fields(typesLine) {
		// Type entries are QNames like "dp0:NetworkVideoTransmitter"; match on
		// the local part so the prefix (dp0 / tns / etc.) doesn't matter.
		if strings.Contains(t, "NetworkVideoTransmitter") {
			return true
		}
	}
	for _, s := range scopes {
		if strings.HasPrefix(s, "onvif://www.onvif.org/") {
			return true
		}
	}
	return false
}

// MapDiscoveredDevices converts a slice of onvif-go discovery.Device to project
// DiscoveredDevice, dropping any that are not ONVIF devices (isONVIFDevice).
// Non-ONVIF WS-Discovery responders (Synology NAS, printers, Windows) are
// filtered here so they never reach HandleDiscovered/enroll (issue #266).
func MapDiscoveredDevices(devices []*discovery.Device) []DiscoveredDevice {
	result := make([]DiscoveredDevice, 0, len(devices))
	for _, d := range devices {
		if !isONVIFDevice(d) {
			logger.Debug("dropping non-ONVIF WS-Discovery responder",
				"endpoint", d.GetDeviceEndpoint(),
				"types", d.Types, "scope_count", len(d.Scopes))
			continue
		}
		result = append(result, MapDiscoveredDevice(d))
	}
	return result
}
