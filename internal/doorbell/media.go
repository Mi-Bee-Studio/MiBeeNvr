package doorbell

import (
	"encoding/binary"
	"net"
	"sync"
	"time"
)

// mediaSession is one call's RTP endpoint: G.711 packets in both
// directions between the doorbell and the talk bridge (the WebSocket
// client on the other side). The socket is unconnected and the write
// destination starts as the SDP-advertised address, but the first valid
// inbound packet latches it for good (symmetric RTP, the intercom-firmware
// reality: some firmware sends from a different port than it advertised).
type mediaSession struct {
	conn *net.UDPConn
	pt   uint8

	mu      sync.Mutex
	dst     *net.UDPAddr
	latched bool
	subs    map[chan []byte]struct{}
	closed  bool

	seq  uint16
	ts   uint32
	ssrc uint32
}

const rtpHeaderLen = 12

func startMedia(pt uint8) (*mediaSession, error) {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
	if err != nil {
		return nil, err
	}
	m := &mediaSession{
		conn: conn,
		pt:   pt,
		subs: map[chan []byte]struct{}{},
		ssrc: uint32(time.Now().UnixNano()),
	}
	go m.readLoop()
	return m, nil
}

// localEndpoint reports the RTP address to advertise in the SDP answer for
// a doorbell reachable at dstIP: the interface IP routing there.
func (m *mediaSession) localEndpoint(dstIP string) (string, int, error) {
	d := &net.Dialer{Timeout: 2 * time.Second}
	c, err := d.Dial("udp", net.JoinHostPort(dstIP, "9"))
	if err != nil {
		return "", 0, err
	}
	defer c.Close()
	host, _, err := net.SplitHostPort(c.LocalAddr().String())
	if err != nil {
		return "", 0, err
	}
	return host, m.conn.LocalAddr().(*net.UDPAddr).Port, nil
}

func (m *mediaSession) setDestination(addr *net.UDPAddr) {
	m.mu.Lock()
	m.dst = addr
	m.mu.Unlock()
}

// subscribe attaches one talk bridge. Returns the inbound channel, a
// writer for browser→doorbell G.711 payloads, and a detach func.
func (m *mediaSession) subscribe(buf int) (<-chan []byte, func([]byte), func()) {
	out := make(chan []byte, buf)
	m.mu.Lock()
	m.subs[out] = struct{}{}
	m.mu.Unlock()
	detach := func() {
		m.mu.Lock()
		if _, ok := m.subs[out]; ok {
			delete(m.subs, out)
			close(out)
		}
		m.mu.Unlock()
	}
	write := func(payload []byte) {
		m.mu.Lock()
		dst := m.dst
		m.mu.Unlock()
		if dst == nil || len(payload) == 0 {
			return
		}
		_, _ = m.conn.WriteToUDP(m.rtpPacket(payload), dst)
	}
	return out, write, detach
}

// rtpPacket frames a G.711 payload in RTP (no padding, no CSRC, no
// extensions; marker set on the first packet of the stream).
func (m *mediaSession) rtpPacket(payload []byte) []byte {
	m.seq++
	if m.ts == 0 {
		m.ts = 160
	} else {
		m.ts += uint32(len(payload))
	}
	pkt := make([]byte, rtpHeaderLen+len(payload))
	pkt[0] = 0x80
	pkt[1] = m.pt
	binary.BigEndian.PutUint16(pkt[2:], m.seq)
	binary.BigEndian.PutUint32(pkt[4:], m.ts)
	binary.BigEndian.PutUint32(pkt[8:], m.ssrc)
	copy(pkt[rtpHeaderLen:], payload)
	return pkt
}

func (m *mediaSession) readLoop() {
	buf := make([]byte, 2048)
	for {
		n, src, err := m.conn.ReadFromUDP(buf)
		if err != nil {
			m.close()
			return
		}
		if n <= rtpHeaderLen || buf[0] != 0x80 {
			continue // not RTP
		}
		// Symmetric-RTP latch: the first valid inbound packet fixes the
		// write destination for the rest of the call.
		m.mu.Lock()
		if !m.latched {
			m.dst = src
			m.latched = true
		}
		subs := make([]chan []byte, 0, len(m.subs))
		for ch := range m.subs {
			subs = append(subs, ch)
		}
		m.mu.Unlock()
		payload := make([]byte, n-rtpHeaderLen)
		copy(payload, buf[rtpHeaderLen:n])
		for _, ch := range subs {
			select {
			case ch <- payload:
			default: // slow bridge — drop this packet, talk stays live
			}
		}
	}
}

func (m *mediaSession) close() {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.closed = true
	subs := m.subs
	m.subs = map[chan []byte]struct{}{}
	m.mu.Unlock()
	for ch := range subs {
		close(ch)
	}
	m.conn.Close()
}
