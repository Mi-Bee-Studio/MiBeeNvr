package rediscovery

import (
	"net"
	"net/url"
	"sort"
	"strings"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
)

// buildCandidates returns a de-duplicated, priority-ordered list of hosts to
// probe. Order: 1) last-known host (fast path if the IP didn't actually change),
// 2) the NVR's own interface subnets (/24), 3) user-declared SubnetHints.
//
// Only IPv4 hosts are emitted (ONVIF unicast probing is IPv4-oriented; the
// public probe API validates IPs via net.ParseIP). Network and broadcast
// addresses are skipped.
func buildCandidates(cam config.CameraConfig) []string {
	seen := make(map[string]struct{})
	var out []string

	add := func(ip string) {
		ip = strings.TrimSpace(ip)
		if ip == "" {
			return
		}
		parsed := net.ParseIP(ip)
		if parsed == nil || parsed.To4() == nil {
			return // skip non-IPv4
		}
		if _, ok := seen[ip]; ok {
			return
		}
		seen[ip] = struct{}{}
		out = append(out, ip)
	}

	// 1. Last-known host — the common case is "IP didn't change, camera was just
	// briefly offline", so try it first for a fast recovery.
	if host := hostFromEndpoint(cam.ONVIFEndpoint); host != "" {
		add(host)
	}
	if host := hostFromEndpoint(cam.URL); host != "" {
		add(host)
	}

	// 2. The NVR's own interface subnets (/24). Catches same-L2 roams and the
	// common home setup where everything shares a /24. Ordered by numeric
	// distance from the last-known host so a DHCP re-lease nearby is found
	// early instead of the scan exhausting its budget in the low range (#58).
	prev := hostFromEndpoint(cam.ONVIFEndpoint)
	if prev == "" {
		prev = hostFromEndpoint(cam.URL)
	}
	for _, ipnet := range localSubnets() {
		for _, ip := range sortByDistanceFrom(ipsInIPNet(ipnet), prev) {
			add(ip)
		}
	}

	// 3. User-declared hints (other AP segments / VLANs). These are essential
	// when the camera roams to a routed subnet the NVR is not directly on.
	for _, hint := range cam.SubnetHints {
		hint = strings.TrimSpace(hint)
		if hint == "" {
			continue
		}
		_, ipnet, err := net.ParseCIDR(hint)
		if err != nil {
			continue
		}
		for _, ip := range sortByDistanceFrom(ipsInIPNet(ipnet), prev) {
			add(ip)
		}
	}

	return out
}

// hostFromEndpoint extracts the bare IPv4 host from a URL or host:port string.
func hostFromEndpoint(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	// Try URL parse first.
	if u, err := url.Parse(raw); err == nil && u.Hostname() != "" {
		h := u.Hostname()
		if net.ParseIP(h) != nil {
			return h
		}
	}
	// Fall back to host:port.
	if h, _, err := net.SplitHostPort(raw); err == nil && net.ParseIP(h) != nil {
		return h
	}
	// Bare IP.
	if net.ParseIP(raw) != nil {
		return raw
	}
	return ""
}

// localSubnets returns the /24 IPv4 network of each non-loopback, non-link-local
// interface address on this host. Used to seed same-subnet scanning.
func localSubnets() []*net.IPNet {
	var nets []*net.IPNet
	ifaces, err := net.Interfaces()
	if err != nil {
		return nets
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			ipNet, ok := addrIPNet(addr)
			if !ok {
				continue
			}
			ip4 := ipNet.IP.To4()
			if ip4 == nil {
				continue
			}
			// Collapse to /24 — the overwhelmingly common home/office case, and
			// keeps the scan bounded (256 hosts) on resource-constrained devices.
			mask := net.CIDRMask(24, 32)
			nets = append(nets, &net.IPNet{IP: ip4.Mask(mask), Mask: mask})
		}
	}
	return nets
}

func addrIPNet(addr net.Addr) (*net.IPNet, bool) {
	if v, ok := addr.(*net.IPNet); ok {
		return v, true
	}
	return nil, false
}

// ipsInIPNet enumerates usable hosts for /24-or-smaller IPv4 networks. Returns
// nil for larger networks to avoid runaway scans on RPi-3B.
func ipsInIPNet(ipnet *net.IPNet) []string {
	ip4 := ipnet.IP.To4()
	if ip4 == nil {
		return nil
	}
	ones, bits := ipnet.Mask.Size()
	if bits != 32 {
		return nil
	}
	if ones < 24 {
		// /23 or wider would be 512+ hosts — too many for the constrained target.
		// Users should split these into /24 hints. The /16 case (ones<=8) is the
		// most abusive, so guard hard.
		return nil
	}

	start := ip4.Mask(ipnet.Mask)
	end := broadcastIP(ip4, ipnet.Mask)

	var out []string
	for cur := incIP(start); !cur.Equal(end) && len(out) < 254; cur = incIP(cur) {
		out = append(out, net.IP(cur).String())
	}
	return out
}

func broadcastIP(ip net.IP, mask net.IPMask) net.IP {
	b := make(net.IP, len(ip))
	for i := range ip {
		b[i] = ip[i] | ^mask[i]
	}
	return net.IP(b)
}

// sortByDistanceFrom reorders hosts so the ones numerically closest to the
// last-known address come first (#58). A DHCP server almost always hands a
// returning device an address in the same block it held before, so probing
// the numeric neighbours before sweeping .1 upward finds the camera within
// the 30s scan budget instead of running out of time in the low range.
//
// prev may be empty (no last-known host) — the input order is then kept.
// Hosts with unparsable IPv4 stay at the end in their original relative order.
func sortByDistanceFrom(hosts []string, prev string) []string {
	prevIP := net.ParseIP(strings.TrimSpace(prev)).To4()
	if prevIP == nil {
		return hosts
	}
	prevNum := ipToUint32(prevIP)
	out := make([]string, len(hosts))
	copy(out, hosts)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := net.ParseIP(out[i]).To4(), net.ParseIP(out[j]).To4()
		switch {
		case a == nil && b == nil:
			return false
		case a == nil:
			return false
		case b == nil:
			return true
		}
		return absDiff(ipToUint32(a), prevNum) < absDiff(ipToUint32(b), prevNum)
	})
	return out
}

func ipToUint32(ip net.IP) uint32 {
	v := ip.To4()
	if v == nil {
		return 0
	}
	return uint32(v[0])<<24 | uint32(v[1])<<16 | uint32(v[2])<<8 | uint32(v[3])
}

func absDiff(a, b uint32) uint32 {
	if a > b {
		return a - b
	}
	return b - a
}

func incIP(ip net.IP) net.IP {
	next := make(net.IP, len(ip))
	copy(next, ip)
	for i := len(next) - 1; i >= 0; i-- {
		next[i]++
		if next[i] != 0 {
			break
		}
	}
	return next
}
