package peer

import (
	"errors"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"
)

// autoAdvertise turns SignalingConfig.AdvertiseAddr = "auto" / "auto:<port>"
// into a concrete "<ip>:<port>" before each PeerConnection build, with a
// TTL-bounded cache so session churn does not spam SSDP. Resolution ladder
// (the linktest-validated zero-config direct path, #43-A/B/D):
//
//  1. UPnP — discover the gateway's IGD, AddPortMapping for the fixed UDP
//     port (on OpenWrt miniupnpd this also opens the WAN firewall pinhole),
//     advertise the external address. Best case: consumer routers with UPnP
//     on (factory default for most) get DIRECT connections with zero config.
//  2. NAT-PMP — same-port UDP mapping for gateways without an IGD.
//  3. STUN — ask the platform's STUN server (from the Welcome message) for
//     the WAN address and assume a manual port forward exists for the port.
//     Unverifiable from here; ICE falls back to TURN when wrong.
//
// Cache expiry re-runs the ladder — a router whose UPnP gets enabled later
// upgrades from STUN mode on the next refresh (adaptive retry), and dynamic
// WAN IP rotation self-heals the same way. Resolution failure returns "" so
// the session proceeds with stock ICE (relay fallback) instead of dying.
const (
	autoAdvertiseTTL    = 5 * time.Minute
	autoAdvertisePort   = 40000
	upnpMappingLeaseSec = 3600
)

type autoEndpointState struct {
	mu       sync.Mutex
	addr     string
	mode     string // "upnp" | "stun" | "" (unresolved/negative)
	port     int
	resolved bool // true once ANY result (incl. negative) is cached
	gw       *UPnPGateway
	expires  time.Time
}

// Hooks for deterministic tests; production uses the real network paths.
type autoEndpointHooks struct {
	discoverUPnP func(timeout time.Duration) (*UPnPGateway, error)
	externalIP   func(gw *UPnPGateway) (string, error)
	addMapping   func(gw *UPnPGateway, ext, inner, lease int) error
	stunQuery    func(addr string, timeout time.Duration) (string, error)
	pmpMapping   func(port int) (string, error)
}

var (
	autoEndpoint         = &autoEndpointState{}
	autoEndpointHooksPtr = &autoEndpointHooks{
		discoverUPnP: DiscoverGatewayUPnP,
		externalIP:   func(gw *UPnPGateway) (string, error) { return gw.ExternalIP() },
		addMapping:   func(gw *UPnPGateway, ext, inner, lease int) error { return gw.AddUDPMapping(ext, inner, lease) },
		stunQuery:    stunQuery,
		pmpMapping:   tryPMPMapping,
	}
)

// resolveAdvertise maps an AdvertiseAddr config value to the concrete
// address for this session: "" → disabled; "auto"/"auto:N" → cached ladder
// resolution (stunServers = STUN URLs from the Welcome message, may be
// empty); "<ip>:<port>" → validated passthrough. Errors yield "" (session
// continues without advertisement) — a broken endpoint hint must never
// break connectivity.
func resolveAdvertise(spec string, stunServers []string) string {
	if spec == "" {
		return ""
	}
	// Everything that is not "auto"/"auto:<port>" passes through UNVALIDATED:
	// malformed static values (including "auto:xyz" typos) fail loudly in
	// applyAdvertiseAddr — a config typo must be visible, not silently
	// degraded to relay.
	if spec != "auto" && !strings.HasPrefix(spec, "auto:") {
		return spec
	}
	port := autoAdvertisePort
	if spec != "auto" {
		n, err := strconv.Atoi(strings.TrimPrefix(spec, "auto:"))
		if err != nil || n < 1 || n > 65535 {
			return spec // typo → loud downstream
		}
		port = n
	}
	addr, _ := autoEndpoint.resolve(port, stunServers, autoEndpointHooksPtr)
	return addr
}

func (s *autoEndpointState) resolve(port int, stunServers []string, h *autoEndpointHooks) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.resolved && time.Now().Before(s.expires) && s.port == port {
		return s.addr, nil // cached hit, including negative results
	}

	// Ladder 1: UPnP (mapping = detection + firewall pinhole in one shot).
	gw, err := h.discoverUPnP(3 * time.Second)
	if err == nil {
		if ip, iperr := h.externalIP(gw); iperr == nil {
			if merr := h.addMapping(gw, port, port, upnpMappingLeaseSec); merr == nil {
				s.addr, s.mode, s.port, s.gw, s.resolved = net.JoinHostPort(ip, strconv.Itoa(port)), "upnp", port, gw, true
				s.expires = time.Now().Add(autoAdvertiseTTL)
				return s.addr, nil
			}
		}
	}

	// Ladder 2: NAT-PMP — gateways without an IGD (Apple lineage, some ISP
	// firmware). Same-port mapping only; see natpmp.go.
	if h.pmpMapping != nil {
		if pmpAddr, perr := h.pmpMapping(port); perr == nil && pmpAddr != "" {
			s.addr, s.mode, s.port, s.gw, s.resolved = pmpAddr, "pmp", port, nil, true
			s.expires = time.Now().Add(autoAdvertiseTTL)
			return s.addr, nil
		}
	}

	// Ladder 3: STUN + assumed manual forward.
	for _, u := range stunServers {
		addr := stunURLToAddr(u)
		if addr == "" {
			continue
		}
		if mapped, serr := h.stunQuery(addr, 3*time.Second); serr == nil {
			if host, _, herr := net.SplitHostPort(mapped); herr == nil && net.ParseIP(host) != nil {
				s.addr, s.mode, s.port, s.gw, s.resolved = net.JoinHostPort(host, strconv.Itoa(port)), "stun", port, nil, true
				s.expires = time.Now().Add(autoAdvertiseTTL)
				return s.addr, nil
			}
		}
	}
	s.addr, s.mode, s.gw, s.resolved, s.port = "", "", nil, true, port // negative cache too
	s.expires = time.Now().Add(autoAdvertiseTTL)
	return "", errors.New("auto advertise: upnp unavailable and stun unresolved")
}

// stunURLToAddr converts a Welcome ICE server URL ("stun:host:port" or
// "stun:host") to "host:port" (default STUN port 3478). Non-STUN URLs
// (turn:…) are skipped.
func stunURLToAddr(u string) string {
	if !strings.HasPrefix(u, "stun:") {
		return ""
	}
	host := strings.TrimPrefix(u, "stun:")
	if host == "" {
		return ""
	}
	if _, _, err := net.SplitHostPort(host); err != nil {
		return net.JoinHostPort(host, "3478")
	}
	return host
}
