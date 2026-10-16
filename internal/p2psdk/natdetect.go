package peer

import (
	"context"
	"crypto/rand"
	"net"
	"sync"
	"time"
)

// NATClass is the behavior classification of the local network's NAT
// (#70): the input to path decisions — a symmetric NAT cannot pair with
// another symmetric NAT via classic ICE hole punching, so both-symmetric
// peerings should prefer the relay instead of burning the arbitration
// window on a doomed direct attempt.
type NATClass string

const (
	// NATUnknown means probing failed (fewer than two STUN servers
	// answered) — no decision input; callers keep their default policy.
	NATUnknown NATClass = "unknown"
	// NATPublic means the socket's mapped address is the machine's own —
	// no NAT in the path.
	NATPublic NATClass = "public"
	// NATCone means one mapping for all destinations (full-/restricted-
	// cone — the distinction does not change OUR path decision: restricted
	// cones still pair with each other via ICE).
	NATCone NATClass = "cone"
	// NATSymmetric means per-destination mappings — direct ICE pairing
	// with another symmetric NAT is effectively impossible; relay is the
	// path.
	NATSymmetric NATClass = "symmetric"
)

// PreferRelay is the path-decision function over a detected NAT class
// (#70): symmetric NATs cannot rely on UDP hole punching, so the relay
// path should be chosen (or attempted first) instead of a doomed direct
// arbitration. Everything else keeps the direct-first default.
func PreferRelay(class NATClass) bool {
	return class == NATSymmetric
}

// NATProbeResult carries one NAT detection outcome.
type NATProbeResult struct {
	Class          NATClass
	MappedByServer map[string]string // server addr -> mapped ip:port
	LocalAddr      string
	// DualStack reports a global-unicast IPv6 address on any interface.
	// The classification probes UDP/IPv4 only, so a symmetric verdict says
	// nothing about IPv6 direct connectivity — ICE can still pair via v6
	// host candidates on a dual-stack machine. Relay-pinning decisions fed
	// by the class must exempt such machines. The check is address-based
	// (no route/firewall probing): a stale or firewalled v6 address only
	// degrades to the unpinned default behavior, never worse.
	DualStack bool
}

// DetectNATClass classifies the local NAT by querying TWO OR MORE STUN
// servers from ONE socket and comparing the observed mappings (#70, the
// linktest natprobe method): differing mapped ports per destination =
// symmetric (endpoint-dependent mapping); identical = cone-ish; mapped ==
// local = public. This is deliberately the simple multi-server compare —
// RFC 5780 RESPONSE-ORIGIN/OTHER-ADDRESS discovery needs server support
// most deployments don't have, while any standard STUN server answers the
// plain Binding request used here.
//
// servers should list ≥2 distinct STUN endpoints ("host:port"). Results
// with fewer than two responsive servers are NATUnknown (no compare).
func DetectNATClass(_ context.Context, servers []string, timeout time.Duration) NATProbeResult {
	res := NATProbeResult{Class: NATUnknown, MappedByServer: map[string]string{}, DualStack: globalIPv6Probe()}
	if len(servers) < 2 || timeout <= 0 {
		return res
	}

	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
	if err != nil {
		return res
	}
	defer conn.Close() //nolint:errcheck // probe teardown
	if local, ok := conn.LocalAddr().(*net.UDPAddr); ok {
		res.LocalAddr = local.String()
	}

	deadline := time.Now().Add(timeout)

	// Fan out the Binding requests (writes on a UDPConn are concurrency
	// safe), then single-reader collect: responses are matched by
	// transaction id, which is exactly how several servers share one
	// socket without cross-talk.
	txToServer := fanOutStunRequests(conn, servers, deadline)
	if len(txToServer) < 2 {
		return res
	}

	collectStunResponses(conn, txToServer, deadline, &res)

	switch {
	case len(res.MappedByServer) < 2:
		return res // cannot compare
	case sameAddrAsLocal(res):
		res.Class = NATPublic
	case consistentMapping(res.MappedByServer):
		res.Class = NATCone
	default:
		res.Class = NATSymmetric
	}
	return res
}

// consistentMapping reports whether every server observed the same mapped
// port — the address-(in)dependence the classification hinges on.
func consistentMapping(byServer map[string]string) bool {
	port := ""
	for _, mapped := range byServer {
		_, p, err := net.SplitHostPort(mapped)
		if err != nil {
			return false
		}
		if port == "" {
			port = p
			continue
		}
		if port != p {
			return false
		}
	}
	return true
}

// sameAddrAsLocal reports whether every mapped address is the machine's
// own interface address with the same port — the no-NAT case.
func sameAddrAsLocal(res NATProbeResult) bool {
	_, localPort, err := net.SplitHostPort(res.LocalAddr)
	if err != nil {
		return false
	}
	localIPs := localInterfaceIPs()
	for _, mapped := range res.MappedByServer {
		host, port, err := net.SplitHostPort(mapped)
		if err != nil {
			return false
		}
		ip := net.ParseIP(host)
		if port != localPort || ip == nil || !localIPs[ip.String()] {
			return false
		}
	}
	return true
}

func localInterfaceIPs() map[string]bool {
	out := map[string]bool{}
	ifaces, err := net.Interfaces()
	if err != nil {
		return out
	}
	for _, ifc := range ifaces {
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			if ipnet, ok := a.(*net.IPNet); ok {
				out[ipnet.IP.String()] = true
			}
		}
	}
	return out
}

// globalIPv6Probe reports whether any interface carries a globally routable
// IPv6 address. Swappable in tests — host interface state is not
// controllable from a unit test (same pattern as the auto-endpoint hooks).
var globalIPv6Probe = func() bool {
	ifaces, err := net.Interfaces()
	if err != nil {
		return false
	}
	for _, ifc := range ifaces {
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip := ipnet.IP
			sixteen := ip.To16()
			if ip.To4() != nil || sixteen == nil || !ip.IsGlobalUnicast() || ip.IsLinkLocalUnicast() {
				continue
			}
			// ULA (fc00::/7) counts as global-unicast to Go but is not
			// globally routable — not a v6 direct-connectivity path.
			if sixteen[0]&0xfe == 0xfc {
				continue
			}
			return true
		}
	}
	return false
}

// fanOutStunRequests sends one Binding request per server over the shared
// socket (UDPConn writes are concurrency safe) and returns the
// transaction-id -> server map the collector matches responses against.
func fanOutStunRequests(conn *net.UDPConn, servers []string, deadline time.Time) map[string]string {
	txToServer := map[string]string{}
	for _, srv := range servers {
		raddr, err := net.ResolveUDPAddr("udp4", srv)
		if err != nil {
			continue
		}
		var pkt [20]byte
		pkt[0], pkt[1] = 0x00, 0x01 // Binding Request
		pkt[4], pkt[5], pkt[6], pkt[7] = 0x21, 0x12, 0xA4, 0x42
		if _, err := rand.Read(pkt[8:]); err != nil {
			continue
		}
		txToServer[string(pkt[8:20])] = srv
		go func(pkt [20]byte) {
			_ = conn.SetWriteDeadline(deadline)
			_, _ = conn.WriteToUDP(pkt[:], raddr)
		}(pkt)
	}
	return txToServer
}

// collectStunResponses reads the shared probe socket until the deadline or
// until every fanned-out transaction has answered, filling MappedByServer
// (first response per server wins).
func collectStunResponses(conn *net.UDPConn, txToServer map[string]string, deadline time.Time, res *NATProbeResult) {
	var mu sync.Mutex
	buf := make([]byte, 1500)
	for {
		_ = conn.SetReadDeadline(deadline)
		n, _, err := conn.ReadFromUDP(buf)
		if err != nil {
			return // deadline (or error): whatever answered is the result
		}
		if n < 20 || buf[0] != 0x01 || buf[1] != 0x01 {
			continue
		}
		srv, ok := txToServer[string(buf[8:20])]
		if !ok {
			continue
		}
		if mapped, err := parseStunXORMapped(buf[:n]); err == nil {
			mu.Lock()
			if _, seen := res.MappedByServer[srv]; !seen {
				res.MappedByServer[srv] = mapped
			}
			mu.Unlock()
		}
		if len(res.MappedByServer) >= len(txToServer) {
			return
		}
	}
}
