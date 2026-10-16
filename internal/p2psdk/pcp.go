package peer

// pcp.go — PCP (RFC 6887) MAP opcode, UDP only, as the third rung of the
// auto-advertise mapping ladder (#43-D): UPnP → NAT-PMP → PCP → STUN.
//
// Wire format (RFC 6887 Figures 2/3/9/10, offsets from message start):
//
//	request  = version(1B=2) | opcode(1B, MAP=1) | reserved(2B)
//	           | lifetime(4B) | client ip(16B, v4-mapped)
//	           | nonce(12B) | protocol(1B, UDP=17) | reserved(3B)
//	           | internal port(2B) | suggested external port(2B)
//	           | suggested external ip(16B, zero = server assigns)
//	           → 60 bytes
//	response = version(1B) | 0x80|opcode(1B) | reserved(1B)
//	           | result code(1B) ← the offset that blocked earlier work
//	           | lifetime(4B) | epoch(4B) | reserved(12B)
//	           | nonce(12B) | protocol(1B) | reserved(3B)
//	           | internal port(2B) | assigned external port(2B)
//	           | assigned external ip(16B)
//	           → 60 bytes
//
// Same-port constraint as PMP: the NAT1To1 host mode rewrites the address
// only, so an assigned external port ≠ internal port is unusable → treated
// as failure and the ladder falls through.

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"strconv"
	"time"
)

const (
	pcpPort          = 5351
	pcpVersion       = 2
	pcpOpMap         = 1
	pcpProtoUDP      = 17
	pcpMsgLen        = 60
	pcpResultSuccess = 0
	pcpLifetimeSec   = 3600
	pcpTimeout       = 750 * time.Millisecond
	pcpRequestTries  = 2
)

// pcpMapUDP requests an external-port==internal-port UDP MAP and returns
// the assigned external port.
func pcpMapUDP(gwAddr string, port int, timeout time.Duration) (int, error) {
	conn, err := net.DialTimeout("udp", gwAddr, timeout)
	if err != nil {
		return 0, fmt.Errorf("pcp dial: %w", err)
	}
	defer func() { _ = conn.Close() }() //nolint:errcheck // teardown

	var nonce [12]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return 0, fmt.Errorf("pcp nonce: %w", err)
	}

	req := make([]byte, pcpMsgLen)
	req[0] = pcpVersion
	req[1] = pcpOpMap
	binary.BigEndian.PutUint32(req[4:8], pcpLifetimeSec)
	// Client IP at [8:24]: the server derives it from the datagram source;
	// an all-zero address is what a client without a known address sends.
	copy(req[24:36], nonce[:])
	req[36] = pcpProtoUDP
	binary.BigEndian.PutUint16(req[40:42], uint16(port)) //nolint:gosec // G115
	binary.BigEndian.PutUint16(req[42:44], uint16(port)) //nolint:gosec // same-port requested
	// Suggested external IP at [44:60] stays zero — server assigns.

	buf := make([]byte, 64)
	lastErr := errors.New("pcp: no response")
	for range pcpRequestTries {
		if _, err := conn.Write(req); err != nil {
			return 0, err
		}
		_ = conn.SetReadDeadline(time.Now().Add(timeout))
		n, err := conn.Read(buf)
		if err != nil {
			lastErr = err
			continue
		}
		resp := buf[:n]
		if len(resp) < pcpMsgLen {
			lastErr = fmt.Errorf("pcp response too short: %d", len(resp))
			continue
		}
		if resp[0] != pcpVersion || resp[1] != 0x80|pcpOpMap {
			lastErr = fmt.Errorf("pcp response header mismatch: v%d op 0x%02x", resp[0], resp[1])
			continue
		}
		if code := resp[3]; code != pcpResultSuccess {
			lastErr = fmt.Errorf("pcp result code %d", code)
			continue
		}
		// Nonce must echo our request (response binding).
		if string(resp[24:36]) != string(nonce[:]) {
			lastErr = errors.New("pcp nonce mismatch")
			continue
		}
		return int(binary.BigEndian.Uint16(resp[42:44])), nil //nolint:gosec // G115
	}
	return 0, lastErr
}

// tryPCPMapping walks the gateway candidates and returns the advertised
// "ip:port" on the first same-port mapping success. The external IP is the
// PMP ADDR answer (both protocols live on the same gateway; a PCP-only
// gateway still answers PMP ADDR in practice — PCP mandates interworking).
func tryPCPMapping(port int) (string, error) {
	for _, gw := range discoverGatewayIPs(natPMPMaxGateways) {
		gwAddr := net.JoinHostPort(gw.String(), strconv.Itoa(pcpPort))
		extPort, err := pcpMapUDP(gwAddr, port, pcpTimeout)
		if err != nil || extPort != port {
			continue // wrong-port assignment is unusable (see file comment)
		}
		ip, err := pmpExternalIP(gwAddr, pcpTimeout)
		if err != nil || ip == nil {
			continue
		}
		return net.JoinHostPort(ip.String(), strconv.Itoa(port)), nil
	}
	return "", errors.New("pcp: no gateway accepted the mapping")
}
