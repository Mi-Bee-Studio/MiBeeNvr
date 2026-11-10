package onboarding

// adopt.go — the guided zero-touch enrollment flow (#1120): move a camera
// from the factory-default subnet onto the NVR's own subnet, then enroll it.
//
// Flow (each step logged, failures carry the step name):
//  1. bind the IP alias on the factory-default subnet (AliasManager.Up)
//  2. ONVIF SetNetworkInterfaces on the camera — DHCP mode, or a static
//     address on the NVR's subnet (field-verified #1120: immediate apply,
//     RebootNeeded=false on current Hikvision firmware; the flag is honored
//     if a device answers true — the wait budget covers the reboot window)
//  3. find the camera at its new address: static mode polls the known
//     address; DHCP mode sweeps the NVR's own subnet matching the camera's
//     serial (unauthenticated ONVIF GetDeviceInformation)
//  4. enroll via the camera manager (AddCamera carries the serial as
//     stable_id so IP self-healing binds immediately)
//  5. drop the alias (deferred — runs even on failure)

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
)

// AdoptRequest is one guided-adopt operation. Credentials travel with the
// request (the operator types them into the UI); they are never persisted
// into the NVR config by this package — AddCamera stores them in the camera
// entry, which is the normal credential surface.
type AdoptRequest struct {
	// IP is the camera's current address on the factory-default subnet.
	IP string `json:"ip"`
	// Username/Password are the device credentials (post-activation).
	Username string `json:"username"`
	Password string `json:"password"`
	// Mode overrides the config target: "dhcp" or "static". Empty = resolve
	// from config (target_dhcp / target_static_ip).
	Mode string `json:"mode,omitempty"`
	// StaticIP overrides the config static target in static mode.
	StaticIP  string `json:"static_ip,omitempty"`
	PrefixLen int    `json:"prefix_len,omitempty"`
}

// AdoptResult reports what the flow did.
type AdoptResult struct {
	Serial       string `json:"serial,omitempty"`
	NewIP        string `json:"new_ip"`
	CameraID     string `json:"camera_id"`
	RebootNeeded bool   `json:"reboot_needed"`
}

// adopt budget bounds: the DHCP rediscovery sweep dominates.
const (
	adoptReachTries      = 20
	adoptReachInterval   = 3 * time.Second // static-mode poll cadence (60s budget)
	adoptSweepBudget     = 90 * time.Second
	adoptSweepCadence    = 5 * time.Second
	adoptRebootExtraWait = 45 * time.Second
)

// seams — fields on Manager (NewManager sets production defaults, tests
// swap them). Keep the flow logic free of library wiring.

// probeSerial resolves a device serial at a bare ip (port 80 ISAPI face
// first — unactivated cameras serve it — then the unauth ONVIF probe).
func probeSerial(ctx context.Context, ip string) string {
	cctx, cancel := context.WithTimeout(ctx, scanHostTimeout)
	defer cancel()
	if c := classifyISAPI(cctx, net.JoinHostPort(ip, "80")); c != nil && c.Serial != "" {
		return c.Serial
	}
	cctx2, cancel2 := context.WithTimeout(ctx, scanHostTimeout)
	defer cancel2()
	if c := classifyHost(cctx2, net.JoinHostPort(ip, "80"), scanHostTimeout); c != nil {
		return c.Serial
	}
	return ""
}

// primarySubnetTargets expands the NVR's own IPv4 subnet(s) into probe
// targets (ip:80 pairs) for the DHCP rediscovery sweep.
func primarySubnetTargets() []string {
	targets := make([]string, 0, 256)
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	seen := make(map[string]bool)
	for _, ifi := range ifaces {
		if ifi.Flags&net.FlagUp == 0 || ifi.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := ifi.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip4 := ipnet.IP.To4()
			if ip4 == nil || ip4.IsLoopback() || ip4.IsLinkLocalUnicast() {
				continue
			}
			base := ip4.Mask(ipnet.Mask)
			_, bits := ipnet.Mask.Size()
			if bits != 24 {
				// Only /24-class networks sweep exhaustively; wider nets stay
				// bounded to their /24 slice around the NVR's own address.
				ipnet.Mask = net.CIDRMask(24, 32)
				base = ip4.Mask(ipnet.Mask)
			}
			for i := 1; i < 255; i++ {
				t := fmt.Sprintf("%d.%d.%d.%d:80", base[0], base[1], base[2], i)
				if !seen[t] {
					seen[t] = true
					targets = append(targets, t)
				}
			}
		}
	}
	return targets
}

// Adopt runs the whole flow synchronously; the Manager wraps it in a job
// for the API surface.
func (m *Manager) Adopt(ctx context.Context, req AdoptRequest) (*AdoptResult, error) {
	cfg := m.cfg()
	if !cfg.CameraOnboarding.Enabled {
		return nil, errors.New("camera onboarding is disabled")
	}
	req.IP = strings.TrimSpace(req.IP)
	if net.ParseIP(req.IP) == nil {
		return nil, fmt.Errorf("adopt: invalid camera address %q", req.IP)
	}

	dhcp := cfg.CameraOnboarding.ResolveOnboardingTargetDHCP()
	if req.Mode == "dhcp" {
		dhcp = true
	} else if req.Mode == "static" {
		dhcp = false
	}
	target := strings.TrimSpace(req.StaticIP)
	if target == "" {
		target = strings.TrimSpace(cfg.CameraOnboarding.TargetStaticIP)
	}
	prefix := req.PrefixLen
	if prefix == 0 {
		prefix = 24
	}
	if !dhcp && net.ParseIP(target) == nil {
		return nil, errors.New("adopt: static mode needs a target address (static_ip or camera_onboarding.target_static_ip)")
	}

	aliasIP := cfg.CameraOnboarding.ResolveOnboardingAliasIP()
	_, defaultSubnet, err := net.ParseCIDR(strings.TrimSpace(cfg.CameraOnboarding.DefaultSubnet))
	if err != nil {
		return nil, fmt.Errorf("adopt: camera_onboarding.default_subnet: %w", err)
	}
	_ = defaultSubnet

	// 1. Alias up — the camera can only answer while we share its subnet.
	if err := m.alias.Up(aliasIP, prefix); err != nil {
		return nil, fmt.Errorf("adopt: %w", err)
	}
	defer func() {
		if derr := m.alias.Down(aliasIP); derr != nil {
			logger.Warn("adopt: alias removal failed (leftover secondary address)", "ip", aliasIP, "error", derr)
		}
	}()

	// 2. ONVIF network change.
	reboot, err := m.setNet(ctx, req.IP, req.Username, req.Password, dhcp, target, prefix)
	if err != nil {
		return nil, fmt.Errorf("adopt: ONVIF SetNetworkInterfaces: %w", err)
	}
	if reboot {
		logger.Info("adopt: device reported rebootNeeded — waiting out the reboot window", "ip", req.IP)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(adoptRebootExtraWait):
		}
	}

	// 3. Find the camera at its new address.
	serial, newIP, err := m.findAdoptedCamera(ctx, req, dhcp, target, prefix)
	if err != nil {
		return nil, err
	}

	// 4. Enroll.
	cameraID, err := m.enroll(ctx, req, serial, newIP)
	if err != nil {
		return nil, fmt.Errorf("adopt: enroll: %w", err)
	}

	return &AdoptResult{Serial: serial, NewIP: newIP, CameraID: cameraID, RebootNeeded: reboot}, nil
}

// findAdoptedCamera locates the camera after the network change: static
// mode polls the known address for the serial it had before the move;
// DHCP mode sweeps the NVR's own subnet matching the serial captured at
// the old address.
func (m *Manager) findAdoptedCamera(ctx context.Context, req AdoptRequest, dhcp bool, staticTarget string, prefix int) (serial, newIP string, err error) {
	// Serial captured at the factory address (either face may serve it).
	serial = m.serialAt(ctx, req.IP)
	if serial == "" {
		serial = m.authedSerial(ctx, req.IP, req.Username, req.Password)
	}

	if !dhcp {
		if serial == "" {
			serial = "" // unknown serial is fine — the static address IS the identity
		}
		for range adoptReachTries {
			if ctx.Err() != nil {
				return "", "", ctx.Err()
			}
			// Only an actual answer counts as "moved" — an empty probe at
			// the target must keep polling (a silent address is not the
			// camera; treating it as success would enroll a ghost).
			if s := m.serialAt(ctx, staticTarget); s != "" {
				if serial == "" || s == serial {
					if serial == "" {
						serial = s
					}
					return serial, staticTarget, nil
				}
			}
			// Activated cameras wall every unauthenticated probe off (401
			// across ISAPI and ONVIF) — an authenticated GetDeviceInformation
			// answering is the reachability signal for them.
			if m.reach(ctx, staticTarget, req.Username, req.Password) {
				if serial == "" {
					serial = m.authedSerial(ctx, staticTarget, req.Username, req.Password)
				}
				return serial, staticTarget, nil
			}
			select {
			case <-ctx.Done():
				return "", "", ctx.Err()
			case <-time.After(adoptReachInterval):
			}
		}
		return "", "", fmt.Errorf("adopt: camera did not become reachable at %s within the budget", staticTarget)
	}

	// DHCP mode: sweep the NVR's own subnet(s), match by serial (when known)
	// or by absence at the old address + ONVIF face (single-candidate case).
	deadline := time.Now().Add(adoptSweepBudget)
	for attempt := 0; time.Now().Before(deadline); attempt++ {
		if ctx.Err() != nil {
			return "", "", ctx.Err()
		}
		targets := m.sweepTargets()
		cands := ScanSubnet(ctx, targets, scanHostTimeout, scanParallel)
		matched := make([]Candidate, 0, 1)
		for _, c := range cands {
			if c.IP == req.IP {
				continue // still answering at the old address = move didn't land
			}
			if serial == "" || c.Serial == serial || (c.Serial == "" && c.State != "") {
				matched = append(matched, c)
			}
		}
		if len(matched) == 1 {
			return serial, matched[0].IP, nil
		}
		if len(matched) > 1 {
			// Ambiguous: prefer a serial-exact match if we have one.
			if serial != "" {
				for _, c := range matched {
					if c.Serial == serial {
						return serial, c.IP, nil
					}
				}
			}
			return "", "", fmt.Errorf("adopt: %d candidate devices on the target subnet — cannot pick safely; use static mode", len(matched))
		}
		select {
		case <-ctx.Done():
			return "", "", ctx.Err()
		case <-time.After(adoptSweepCadence):
		}
	}
	return "", "", errors.New("adopt: camera not found on the target subnet within the sweep budget (DHCP lease pending? increase the budget or use static mode)")
}

// enroll calls the camera manager's AddCamera with the adopted identity.
func (m *Manager) enroll(ctx context.Context, req AdoptRequest, serial, newIP string) (string, error) {
	if m.enroller == nil {
		return "", errors.New("camera manager unavailable")
	}
	endpoint := "http://" + newIP + "/onvif/device_service"
	cam := config.CameraConfig{
		Name:          "Camera " + newIP,
		Protocol:      "onvif",
		URL:           endpoint,
		Username:      req.Username,
		Password:      req.Password,
		StableID:      serial,
		ONVIFEndpoint: endpoint,
	}
	return m.enroller.AddCamera(ctx, cam)
}
