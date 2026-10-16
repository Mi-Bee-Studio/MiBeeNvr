package peer

import (
	"errors"
	"fmt"
	"log/slog"
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
	mode     string // "upnp" | "pmp" | "stun" | "" (unresolved/negative)
	port     int
	resolved bool // true once ANY result (incl. negative) is cached
	gw       *UPnPGateway
	expires  time.Time

	// Per-rung failure detail of the most recent ladder run (guarded by mu).
	// The ladder is otherwise silent — these make "why did auto fail" answerable
	// from the device journal instead of a black box.
	upnpErr error
	pmpErr  error
	stunErr error
}

// advertiseResolution is the diagnosable outcome of one resolveAdvertise call:
// what address was chosen, which rung won, whether the ladder actually ran
// (fresh) vs served from the TTL cache, and why each rung failed.
type advertiseResolution struct {
	addr    string // concrete advertise address ("" = none)
	mode    string // "upnp" | "pmp" | "stun" | "" (negative)
	fresh   bool   // ladder ran this call (false = cache hit or non-auto spec)
	ladder  bool   // spec was "auto"/"auto:<port>" (vs static/empty passthrough)
	upnpErr error
	pmpErr  error
	stunErr error
}

// Hooks for deterministic tests; production uses the real network paths.
type autoEndpointHooks struct {
	discoverUPnP func(timeout time.Duration) (*UPnPGateway, error)
	externalIP   func(gw *UPnPGateway) (string, error)
	addMapping   func(gw *UPnPGateway, ext, inner, lease int) error
	stunQuery    func(addr string, timeout time.Duration) (string, error)
	pmpMapping   func(port int) (string, error)
	pcpMapping   func(port int) (string, error)
}

var (
	autoEndpoint         = &autoEndpointState{}
	autoEndpointHooksPtr = &autoEndpointHooks{
		discoverUPnP: DiscoverGatewayUPnP,
		externalIP:   func(gw *UPnPGateway) (string, error) { return gw.ExternalIP() },
		addMapping:   func(gw *UPnPGateway, ext, inner, lease int) error { return gw.AddUDPMapping(ext, inner, lease) },
		stunQuery:    stunQuery,
		pmpMapping:   tryPMPMapping,
		pcpMapping:   tryPCPMapping,
	}
)

// resolveAdvertise maps an AdvertiseAddr config value to the concrete
// address for this session: "" → disabled; "auto"/"auto:N" → cached ladder
// resolution (stunServers = STUN URLs from the Welcome message, may be
// empty); "<ip>:<port>" → validated passthrough. Errors yield "" (session
// continues without advertisement) — a broken endpoint hint must never
// break connectivity.
func resolveAdvertise(spec string, stunServers []string) string {
	return resolveAdvertiseDetailed(spec, stunServers).addr
}

// resolveAdvertiseDetailed is resolveAdvertise with a diagnosable outcome
// (see advertiseResolution) so callers can log ladder execution and results.
func resolveAdvertiseDetailed(spec string, stunServers []string) advertiseResolution {
	if spec == "" {
		return advertiseResolution{}
	}
	// Everything that is not "auto"/"auto:<port>" passes through UNVALIDATED:
	// malformed static values (including "auto:xyz" typos) fail loudly in
	// applyAdvertiseAddr — a config typo must be visible, not silently
	// degraded to relay.
	if spec != "auto" && !strings.HasPrefix(spec, "auto:") {
		return advertiseResolution{addr: spec}
	}
	port := autoAdvertisePort
	if spec != "auto" {
		n, err := strconv.Atoi(strings.TrimPrefix(spec, "auto:"))
		if err != nil || n < 1 || n > 65535 {
			return advertiseResolution{addr: spec} // typo → loud downstream
		}
		port = n
	}
	return autoEndpoint.resolve(port, stunServers, autoEndpointHooksPtr)
}

func (s *autoEndpointState) resolve(port int, stunServers []string, h *autoEndpointHooks) advertiseResolution {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.resolved && time.Now().Before(s.expires) && s.port == port {
		return advertiseResolution{addr: s.addr, mode: s.mode, ladder: true} // cached hit, including negative results
	}
	upnpRes, upnpOK, upnpErr := s.tryUPnP(port, h)
	if upnpOK {
		return upnpRes
	}
	pmpRes, pmpOK, pmpErr := s.tryPMP(port, upnpErr, h)
	if pmpOK {
		return pmpRes
	}
	pcpRes, pcpOK, pcpErr := s.tryPCP(port, upnpErr, pmpErr, h)
	if pcpOK {
		return pcpRes
	}
	stunRes, stunOK, stunErr := s.trySTUN(port, stunServers, upnpErr, coalesceErr(pmpErr, pcpErr), h)
	if stunOK {
		return stunRes
	}
	s.addr, s.mode, s.gw, s.resolved, s.port = "", "", nil, true, port // negative cache too
	s.expires = time.Now().Add(autoAdvertiseTTL)
	s.upnpErr, s.pmpErr, s.stunErr = upnpErr, pmpErr, stunErr
	return advertiseResolution{
		addr: "", mode: "", fresh: true, ladder: true,
		upnpErr: upnpErr, pmpErr: pmpErr, stunErr: stunErr,
	}
}

// tryUPnP runs ladder 1 (mapping = detection + firewall pinhole in one
// shot). Caller must hold s.mu; on success the cache state is updated.
func (s *autoEndpointState) tryUPnP(port int, h *autoEndpointHooks) (advertiseResolution, bool, error) {
	gw, err := h.discoverUPnP(3 * time.Second)
	if err != nil {
		return advertiseResolution{}, false, err
	}
	ip, err := h.externalIP(gw)
	if err != nil {
		return advertiseResolution{}, false, err
	}
	if pip := net.ParseIP(ip); !advertiseableIP(pip) {
		// #82: a private/CGNAT "external" address means the IGD itself
		// sits behind another NAT — cross-site peers can never reach it.
		return advertiseResolution{}, false, fmt.Errorf("upnp external %s is private/CGNAT (NAT behind NAT)", ip)
	}
	if err := h.addMapping(gw, port, port, upnpMappingLeaseSec); err != nil {
		return advertiseResolution{}, false, err
	}
	s.addr, s.mode, s.port, s.gw, s.resolved = net.JoinHostPort(ip, strconv.Itoa(port)), "upnp", port, gw, true
	s.expires = time.Now().Add(autoAdvertiseTTL)
	s.upnpErr, s.pmpErr, s.stunErr = nil, nil, nil
	return advertiseResolution{addr: s.addr, mode: "upnp", fresh: true, ladder: true}, true, nil
}

// tryPMP runs ladder 2 — gateways without an IGD (Apple lineage, some ISP
// firmware). Same-port mapping only; see natpmp.go. Caller must hold s.mu.
func (s *autoEndpointState) tryPMP(port int, upnpErr error, h *autoEndpointHooks) (advertiseResolution, bool, error) {
	if h.pmpMapping == nil {
		return advertiseResolution{}, false, nil
	}
	pmpAddr, perr := h.pmpMapping(port)
	if perr != nil {
		return advertiseResolution{}, false, perr
	}
	if pmpAddr == "" {
		return advertiseResolution{}, false, errors.New("empty mapping address")
	}
	if host, _, herr := net.SplitHostPort(pmpAddr); herr != nil {
		return advertiseResolution{}, false, herr
	} else if !advertiseableIP(net.ParseIP(host)) { //nolint:revive // early-return style reads clearer here
		return advertiseResolution{}, false, fmt.Errorf("pmp address %s is private/CGNAT", pmpAddr)
	}
	s.addr, s.mode, s.port, s.gw, s.resolved = pmpAddr, "pmp", port, nil, true
	s.expires = time.Now().Add(autoAdvertiseTTL)
	s.upnpErr, s.pmpErr, s.stunErr = upnpErr, nil, nil
	return advertiseResolution{addr: s.addr, mode: "pmp", fresh: true, ladder: true, upnpErr: upnpErr}, true, nil
}

// tryPCP runs ladder 2b (#43-D completion) — PCP-only gateways that
// neither UPnP nor NAT-PMP served. Same-port mapping only; see pcp.go.
// Caller must hold s.mu.
func (s *autoEndpointState) tryPCP(port int, upnpErr, pmpErr error, h *autoEndpointHooks) (advertiseResolution, bool, error) {
	if h.pcpMapping == nil {
		return advertiseResolution{}, false, nil
	}
	pcpAddr, perr := h.pcpMapping(port)
	if perr != nil {
		return advertiseResolution{}, false, perr
	}
	if pcpAddr == "" {
		return advertiseResolution{}, false, errors.New("empty mapping address")
	}
	if host, _, herr := net.SplitHostPort(pcpAddr); herr != nil {
		return advertiseResolution{}, false, herr
	} else if !advertiseableIP(net.ParseIP(host)) { //nolint:revive // early-return style reads clearer here
		return advertiseResolution{}, false, fmt.Errorf("pcp address %s is private/CGNAT", pcpAddr)
	}
	s.addr, s.mode, s.port, s.gw, s.resolved = pcpAddr, "pcp", port, nil, true
	s.expires = time.Now().Add(autoAdvertiseTTL)
	s.upnpErr, s.pmpErr, s.stunErr = upnpErr, pmpErr, nil
	return advertiseResolution{
		addr: s.addr, mode: "pcp", fresh: true, ladder: true,
		upnpErr: upnpErr, pmpErr: pmpErr,
	}, true, nil
}

// coalesceErr returns the first non-nil error (diagnostics chaining).
func coalesceErr(errs ...error) error {
	for _, e := range errs {
		if e != nil {
			return e
		}
	}
	return nil
}

// trySTUN runs ladder 3 — STUN reflection + assumed manual port forward.
// Caller must hold s.mu.
func (s *autoEndpointState) trySTUN(
	port int, stunServers []string, upnpErr, pmpErr error, h *autoEndpointHooks,
) (advertiseResolution, bool, error) {
	var stunErr error
	for _, u := range stunServers {
		addr := stunURLToAddr(u)
		if addr == "" {
			continue
		}
		mapped, serr := h.stunQuery(addr, 3*time.Second)
		if serr != nil {
			if stunErr == nil {
				stunErr = serr
			}
			continue
		}
		if host, _, herr := net.SplitHostPort(mapped); herr == nil && advertiseableIP(net.ParseIP(host)) {
			s.addr, s.mode, s.port, s.gw, s.resolved = net.JoinHostPort(host, strconv.Itoa(port)), "stun", port, nil, true
			s.expires = time.Now().Add(autoAdvertiseTTL)
			s.upnpErr, s.pmpErr, s.stunErr = upnpErr, pmpErr, nil
			return advertiseResolution{
				addr: s.addr, mode: "stun", fresh: true, ladder: true,
				upnpErr: upnpErr, pmpErr: pmpErr,
			}, true, nil
		}
		if stunErr == nil {
			stunErr = fmt.Errorf("stun mapped %q: not a usable address", mapped)
		}
	}
	if len(stunServers) == 0 {
		stunErr = errors.New("no stun servers advertised")
	}
	return advertiseResolution{}, false, stunErr
}

// logAdvertiseResolution surfaces ladder execution in the peer journal: a
// fresh success (Info) or total failure (Warn, with per-rung causes) per
// ladder run — cache hits stay Debug so steady state does not spam.
func logAdvertiseResolution(logger *slog.Logger, res advertiseResolution) {
	if logger == nil || !res.ladder || !res.fresh {
		return
	}
	if res.mode != "" {
		logger.Info("auto advertise resolved", "mode", res.mode, "addr", res.addr)
		return
	}
	logger.Warn("auto advertise ladder failed; proceeding with stock ICE (relay fallback)",
		"upnp", res.upnpErr, "pmp", res.pmpErr, "stun", res.stunErr)
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

// advertiseableIP reports whether an IP is usable as a cross-site
// advertised endpoint (#82): loopback, link-local, multicast, unspecified,
// RFC1918/ULA private and RFC 6598 CGNAT (100.64.0.0/10) addresses can
// never be reached from a remote site — advertising them only poisons the
// peers' candidate pools (a docker-bridge host address produced a
// pathological prflx nomination in the mesh fleet).
func advertiseableIP(ip net.IP) bool {
	if ip == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() ||
		ip.IsMulticast() || ip.IsUnspecified() || ip.IsPrivate() {
		return false
	}
	if ip4 := ip.To4(); ip4 != nil && ip4[0] == 100 && ip4[1] >= 64 && ip4[1] <= 127 {
		return false
	}
	return true
}
