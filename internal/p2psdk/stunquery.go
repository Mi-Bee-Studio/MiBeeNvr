package peer

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"strconv"
	"time"
)

// Minimal UDP STUN Binding query, stdlib only (ported from the linktest
// natprobe): resolves the public ip:port a STUN server observes for a fresh
// socket. Used by AdvertiseAddr "auto" mode as the fallback when the gateway
// has no usable UPnP IGD.
const stunMagicCookie = 0x2112A442

// stunQuery sends one Binding Request to addr and returns the XOR-mapped
// address. The socket is created fresh per call: the mapping it observes is
// the mapping a NEW ICE session's socket would get, which is exactly what we
// want to advertise.
func stunQuery(addr string, timeout time.Duration) (string, error) {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
	if err != nil {
		return "", err
	}
	defer conn.Close() //nolint:errcheck // probe teardown
	return stunQueryOnConn(conn, addr, timeout)
}

// stunQueryOnConn runs one Binding Request round-trip over an existing
// socket. netcheck shares one socket across servers so the per-destination
// mapped addresses are comparable (EIM/EDM classification depends on it).
func stunQueryOnConn(conn *net.UDPConn, addr string, timeout time.Duration) (string, error) {
	raddr, err := net.ResolveUDPAddr("udp4", addr)
	if err != nil {
		return "", err
	}

	var pkt [20]byte
	binary.BigEndian.PutUint16(pkt[0:], 0x0001) // Binding Request
	binary.BigEndian.PutUint32(pkt[4:], stunMagicCookie)
	if _, randErr := rand.Read(pkt[8:]); randErr != nil {
		return "", randErr
	}
	txid := string(pkt[8:20])

	_ = conn.SetWriteDeadline(time.Now().Add(timeout))
	if _, writeErr := conn.WriteToUDP(pkt[:], raddr); writeErr != nil {
		return "", writeErr
	}
	buf := make([]byte, 1500)
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		_ = conn.SetReadDeadline(deadline)
		n, _, readErr := conn.ReadFromUDP(buf)
		if readErr != nil {
			return "", readErr
		}
		if n < 20 || binary.BigEndian.Uint16(buf[0:]) != 0x0101 {
			continue
		}
		if string(buf[8:20]) != txid {
			continue // stray response for someone else's transaction
		}
		return parseStunXORMapped(buf[:n])
	}
	return "", fmt.Errorf("stun %s: response timeout", addr)
}

// parseStunXORMapped extracts XOR-MAPPED-ADDRESS (IPv4) or legacy
// MAPPED-ADDRESS from a Binding Response.
func parseStunXORMapped(msg []byte) (string, error) {
	for off := 20; off+4 <= len(msg); {
		attrType := binary.BigEndian.Uint16(msg[off:])
		attrLen := int(binary.BigEndian.Uint16(msg[off+2:]))
		v := off + 4
		if v+attrLen > len(msg) {
			return "", fmt.Errorf("stun: malformed attribute at %d", off)
		}
		switch attrType {
		case 0x0020: // XOR-MAPPED-ADDRESS
			if attrLen < 8 || msg[v+1] != 0x01 {
				break // IPv6 mapped addresses are out of scope
			}
			port := binary.BigEndian.Uint16(msg[v+2:]) ^ 0x2112
			var ip [4]byte
			binary.BigEndian.PutUint32(ip[:], binary.BigEndian.Uint32(msg[v+4:])^stunMagicCookie)
			return net.JoinHostPort(net.IP(ip[:]).String(), strconv.Itoa(int(port))), nil
		case 0x0001: // MAPPED-ADDRESS (legacy, non-XOR)
			if attrLen < 8 || msg[v+1] != 0x01 {
				break
			}
			port := binary.BigEndian.Uint16(msg[v+2:])
			return net.JoinHostPort(net.IP(msg[v+4:v+8]).String(), strconv.Itoa(int(port))), nil
		}
		off = v + (attrLen+3)&^3 // STUN attributes are 4-byte aligned
	}
	return "", errors.New("stun: no mapped address in response")
}
