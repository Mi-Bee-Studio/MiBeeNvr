package onboarding

import (
	"net"
)

// AliasManager adds and removes a temporary secondary IPv4 address (an "IP
// alias") on the NVR's primary interface. Zero-touch onboarding (#1120)
// needs it to speak to cameras sitting on a factory-default subnet the NVR
// is not otherwise part of: the camera's protocol stack strictly follows
// routing (a cross-subnet reply would go to the gateway and die), so the
// NVR must hold an address INSIDE the camera's subnet for the round trip
// to work. Field-verified 2026-11-07 (issue #1120 rehearsals): alias up →
// ISAPI/ONVIF/RTSP all reachable; alias down → unreachable again.
type AliasManager interface {
	// Up binds ip/prefixLen as a secondary address on the default-route
	// interface. Idempotent: adding an address that already exists is OK.
	Up(ip net.IP, prefixLen int) error
	// Down removes the address. Removing an address that is not present is
	// OK. Safe to call when Up never succeeded.
	Down(ip net.IP) error
	// UpHeld reports whether the address is currently bound (diagnostics).
	UpHeld(ip net.IP) bool
}

// NewAliasManager returns the platform alias implementation. Linux uses a
// small netlink client (needs CAP_NET_ADMIN); other platforms return a
// stub whose operations fail with a clear error (the feature is
// Linux-targeted; Windows/macOS installs are dev hosts).
func NewAliasManager() AliasManager {
	return newPlatformAliasManager()
}
