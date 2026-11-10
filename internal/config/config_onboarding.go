package config

import (
	"fmt"
	"net"
	"strings"
)

// CameraOnboardingConfig configures zero-touch camera onboarding (#1120):
// discovering cameras that sit on a factory-default IP subnet the NVR is not
// part of (e.g. 192.168.1.0/24 while the NVR lives on 192.168.63.0/24), and
// moving them onto the NVR's own subnet for enrollment — without touching a
// laptop and re-addressing by hand.
//
// The whole feature is OFF by default: the scan binds a temporary secondary
// address (IP alias) on the NVR's primary interface, which needs
// CAP_NET_ADMIN, and the adopt flow rewrites a camera's network settings.
// Both are operator actions that must be explicitly opted into.
type CameraOnboardingConfig struct {
	// Enabled turns the feature on. default false. Requires the service to
	// run with CAP_NET_ADMIN (see deploy/mibee-nvr.service) on Linux.
	Enabled bool `yaml:"enabled"`

	// DefaultSubnet is the factory-default subnet cameras are expected on,
	// in CIDR form (e.g. "192.168.1.0/24"). The scan sweeps this range with
	// unauthenticated device probes; required when enabled.
	DefaultSubnet string `yaml:"default_subnet"`

	// AliasIP is the temporary secondary address the NVR puts on its own
	// interface while scanning/adopting. Must live inside DefaultSubnet.
	// Empty = the last address of the subnet (x.x.x.254 for a /24).
	AliasIP string `yaml:"alias_ip"`

	// TargetDHCP selects how the camera is re-addressed: true = switch the
	// camera to DHCP (it picks up an address on the NVR's subnet from the
	// network's DHCP server); false = assign TargetStaticIP. When unset the
	// DHCP path is used when TargetStaticIP is empty, and vice versa.
	TargetDHCP *bool `yaml:"target_dhcp"`

	// TargetStaticIP is the address assigned in static mode. Must be inside
	// the NVR's own (primary) subnet. Required for static mode.
	TargetStaticIP string `yaml:"target_static_ip"`

	// ScanIntervalS runs the subnet sweep periodically (seconds). 0 (the
	// default) = manual only, via POST /api/onboarding/scan. Positive values
	// ≥60 enable a background sweep at that cadence.
	ScanIntervalS int `yaml:"scan_interval_s"`
}

// ResolveOnboardingTargetDHCP resolves the DHCP-vs-static switch (pointer
// tri-state: nil falls back to whichever explicit target was provided).
func (c *CameraOnboardingConfig) ResolveOnboardingTargetDHCP() bool {
	if c.TargetDHCP != nil {
		return *c.TargetDHCP
	}
	return c.TargetStaticIP == ""
}

// ValidateOnboarding checks the camera_onboarding section when enabled.
func ValidateOnboarding(cfg *Config) error {
	o := &cfg.CameraOnboarding
	if !o.Enabled {
		return nil
	}

	_, _, err := net.ParseCIDR(strings.TrimSpace(o.DefaultSubnet))
	if err != nil {
		return fmt.Errorf("camera_onboarding.default_subnet must be CIDR (e.g. 192.168.1.0/24), got %q", o.DefaultSubnet)
	}

	aliasIP := o.ResolveOnboardingAliasIP()
	if aliasIP == nil {
		return fmt.Errorf("camera_onboarding.alias_ip %q is not a valid IPv4 address", o.AliasIP)
	}
	subnetIP, subnet, _ := net.ParseCIDR(strings.TrimSpace(o.DefaultSubnet))
	_ = subnetIP
	if !subnet.Contains(aliasIP) {
		return fmt.Errorf("camera_onboarding.alias_ip %s must be inside default_subnet %s", aliasIP, o.DefaultSubnet)
	}

	if !o.ResolveOnboardingTargetDHCP() {
		ip := net.ParseIP(strings.TrimSpace(o.TargetStaticIP))
		if ip == nil || ip.To4() == nil {
			return fmt.Errorf("camera_onboarding.target_static_ip must be a valid IPv4 address in static mode, got %q", o.TargetStaticIP)
		}
	}

	if o.ScanIntervalS < 0 {
		return fmt.Errorf("camera_onboarding.scan_interval_s must be >= 0, got %d", o.ScanIntervalS)
	}
	if o.ScanIntervalS != 0 && o.ScanIntervalS < 60 {
		return fmt.Errorf("camera_onboarding.scan_interval_s below 60 would hammer the LAN; use 0 for manual-only scans")
	}
	return nil
}

// ResolveOnboardingAliasIP returns the alias address to bind (explicit, or
// the subnet's last usable host address), or nil when unparseable.
func (c *CameraOnboardingConfig) ResolveOnboardingAliasIP() net.IP {
	if a := strings.TrimSpace(c.AliasIP); a != "" {
		ip := net.ParseIP(a)
		if ip == nil || ip.To4() == nil {
			return nil
		}
		return ip.To4()
	}

	_, subnet, err := net.ParseCIDR(strings.TrimSpace(c.DefaultSubnet))
	if err != nil {
		return nil
	}
	// Last host address of an IPv4 subnet: base | ^mask (minus broadcast).
	base := subnet.IP.To4()
	mask := subnet.Mask
	last := make(net.IP, 4)
	for i := range 4 {
		last[i] = base[i] | ^mask[i]
	}
	last[3]-- // .255 → .254 (drop broadcast)
	return last
}
