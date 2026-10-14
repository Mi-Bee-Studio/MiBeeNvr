package peer

// natpmp.go — NAT-PMP (RFC 6886) UDP 端口映射：auto advertise 的备用映射
// 协议（#43-D），覆盖没有 UPnP IGD 的网关。仅接受**同端口**映射——
// NAT1To1 host 模式只改写地址不改端口，网关若分配了不同的外部端口，
// ICE 候选将指向错误端口，视同失败。
//
// PCP（RFC 6887）本轮未实现：线格式解析存在歧义且离线无法权威核对，
// 不把未经验证的协议代码发进生产；待能核对 RFC 后补。

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	natPMPPort         = 5351
	natPMPVersion      = 0
	natPMPOpAddr       = 0 // external address request
	natPMPOpMap        = 1 // UDP mapping request
	natPMPMaxGateways  = 3
	natPMPTimeout      = 750 * time.Millisecond
	natPMPRequestTries = 2
	natPMPLifetimeSec  = 3600
)

// natPMPGateway renders the PMP endpoint for a gateway IP.
func natPMPGateway(gw net.IP) string {
	return net.JoinHostPort(gw.String(), strconv.Itoa(natPMPPort))
}

// pmpExchange sends a request and reads one response, retrying per the
// RFC 6886 retransmission spirit (one quick retry; the caller's ladder TTL
// handles longer-term retry).
func pmpExchange(conn *net.UDPConn, req []byte, timeout time.Duration) ([]byte, error) {
	buf := make([]byte, 64)
	lastErr := errors.New("pmp: no response")
	for range natPMPRequestTries {
		if _, err := conn.Write(req); err != nil {
			return nil, err
		}
		_ = conn.SetReadDeadline(time.Now().Add(timeout))
		n, err := conn.Read(buf)
		if err == nil {
			return buf[:n], nil
		}
		lastErr = err
	}
	return nil, lastErr
}

// pmpExternalIP asks the gateway for its WAN address (RFC 6886 §3.3).
func pmpExternalIP(gwAddr string, timeout time.Duration) (net.IP, error) {
	conn, err := net.DialTimeout("udp", gwAddr, timeout)
	if err != nil {
		return nil, fmt.Errorf("pmp dial: %w", err)
	}
	defer func() { _ = conn.Close() }()

	resp, err := pmpExchange(conn.(*net.UDPConn), []byte{natPMPVersion, natPMPOpAddr}, timeout)
	if err != nil {
		return nil, fmt.Errorf("pmp addr request: %w", err)
	}
	if len(resp) < 12 {
		return nil, fmt.Errorf("pmp addr response too short: %d", len(resp))
	}
	if resp[0] != natPMPVersion || resp[1] != natPMPOpAddr|0x80 {
		return nil, fmt.Errorf("pmp addr response opcode mismatch: v%d op 0x%02x", resp[0], resp[1])
	}
	if code := binary.BigEndian.Uint16(resp[2:4]); code != 0 {
		return nil, fmt.Errorf("pmp addr result code %d", code)
	}
	ip := net.IP(append([]byte(nil), resp[8:12]...))
	if ip.IsUnspecified() {
		return nil, errors.New("pmp gateway has no WAN address")
	}
	return ip, nil
}

// pmpMapUDP requests an external-port==internal-port UDP mapping and returns
// the assigned external port (RFC 6886 §3.4).
func pmpMapUDP(gwAddr string, port int, timeout time.Duration) (int, error) {
	conn, err := net.DialTimeout("udp", gwAddr, timeout)
	if err != nil {
		return 0, fmt.Errorf("pmp dial: %w", err)
	}
	defer func() { _ = conn.Close() }()

	req := make([]byte, 12)
	req[0], req[1] = natPMPVersion, natPMPOpMap
	binary.BigEndian.PutUint16(req[4:6], uint16(port)) //nolint:gosec // G115: port bounded 1-65535
	binary.BigEndian.PutUint16(req[6:8], uint16(port)) //nolint:gosec // G115: same-port mapping requested
	binary.BigEndian.PutUint32(req[8:12], natPMPLifetimeSec)

	resp, err := pmpExchange(conn.(*net.UDPConn), req, timeout)
	if err != nil {
		return 0, fmt.Errorf("pmp map request: %w", err)
	}
	if len(resp) < 16 {
		return 0, fmt.Errorf("pmp map response too short: %d", len(resp))
	}
	if resp[0] != natPMPVersion || resp[1] != natPMPOpMap|0x80 {
		return 0, fmt.Errorf("pmp map response opcode mismatch: v%d op 0x%02x", resp[0], resp[1])
	}
	if code := binary.BigEndian.Uint16(resp[2:4]); code != 0 {
		return 0, fmt.Errorf("pmp map result code %d", code)
	}
	return int(binary.BigEndian.Uint16(resp[12:14])), nil //nolint:gosec // G115: port is 16-bit on the wire
}

// tryPMPMapping walks the gateway candidates and returns the advertised
// "ip:port" on the first same-port mapping success.
func tryPMPMapping(port int) (string, error) {
	for _, gw := range discoverGatewayIPs(natPMPMaxGateways) {
		gwAddr := natPMPGateway(gw)
		extPort, err := pmpMapUDP(gwAddr, port, natPMPTimeout)
		if err != nil || extPort != port {
			continue // wrong-port assignment is unusable (see file comment)
		}
		ip, err := pmpExternalIP(gwAddr, natPMPTimeout)
		if err != nil {
			continue
		}
		return net.JoinHostPort(ip.String(), strconv.Itoa(port)), nil
	}
	return "", errors.New("pmp: no gateway accepted the mapping")
}

// discoverGatewayIPs returns default-gateway candidates. Linux reads
// /proc/net/route (authoritative); elsewhere — and as a fallback — the .1
// host of each interface's IPv4 network is tried (consumer-router
// convention; a stray probe at a non-gateway host is silently dropped).
func discoverGatewayIPs(limit int) []net.IP {
	var cands []net.IP
	seen := map[string]bool{}
	add := func(ip net.IP) {
		if ip == nil || ip.IsLoopback() || ip.IsUnspecified() {
			return
		}
		k := ip.String()
		if !seen[k] {
			seen[k] = true
			cands = append(cands, ip)
		}
	}

	for _, ip := range parseProcNetRoutes() {
		add(ip)
	}

	ifaces, err := net.Interfaces()
	if err == nil {
		for _, ifc := range ifaces {
			addrs, aerr := ifc.Addrs()
			if aerr != nil {
				continue
			}
			for _, a := range addrs {
				ipn, ok := a.(*net.IPNet)
				if !ok {
					continue
				}
				ip4 := ipn.IP.To4()
				if ip4 == nil || ip4.IsLoopback() || len(ipn.Mask) != 4 {
					continue
				}
				gw := ip4.Mask(ipn.Mask)
				if len(gw) == 4 {
					gw[3] |= 1
					add(net.IP(append([]byte(nil), gw...)))
				}
			}
		}
	}

	if len(cands) > limit {
		cands = cands[:limit]
	}
	return cands
}

// parseProcNetRoutes extracts default-route gateway IPs from /proc/net/route
// (hex little-endian). Returns nil wherever the file does not exist.
func parseProcNetRoutes() []net.IP {
	data, err := os.ReadFile("/proc/net/route")
	if err != nil {
		return nil
	}
	return parseProcNetRoutesBytes(string(data))
}

// parseProcNetRoutesBytes parses /proc/net/route content.
func parseProcNetRoutesBytes(data string) []net.IP {
	var out []net.IP
	for _, line := range strings.Split(data, "\n")[1:] {
		f := strings.Fields(line)
		if len(f) < 3 || f[1] != "00000000" {
			continue
		}
		hexIP := f[2]
		if len(hexIP) != 8 {
			continue
		}
		ip := make(net.IP, 4)
		ok := true
		for i := range 4 {
			v, perr := strconv.ParseUint(hexIP[6-2*i:8-2*i], 16, 8)
			if perr != nil {
				ok = false
				break
			}
			ip[i] = byte(v)
		}
		if ok {
			out = append(out, ip)
		}
	}
	return out
}
