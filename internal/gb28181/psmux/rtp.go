package psmux

import (
	"fmt"
	"net"
)

// RTPMTU is the max RTP payload size per packet — 1400 fits any Ethernet path.
const RTPMTU = 1400

// RTPPacketizer fragments a PS byte stream across RTP/UDP packets: PT 96,
// 90kHz timestamps, marker on the last packet of each PS burst (access unit).
type RTPPacketizer struct {
	conn       *net.UDPConn
	dst        *net.UDPAddr
	ssrc       uint32
	seq        uint16
	payloadTyp byte
	sent       uint64
}

func NewRTPPacketizer(conn *net.UDPConn, dst *net.UDPAddr, ssrc uint32, initialSeq uint16) *RTPPacketizer {
	return &RTPPacketizer{conn: conn, dst: dst, ssrc: ssrc, seq: initialSeq, payloadTyp: 96}
}

// Send fragments and sends one PS burst; ts is the 90kHz AU timestamp.
func (p *RTPPacketizer) Send(ps []byte, tsTicks int64) error {
	total := len(ps)
	for off := 0; off < total; off += RTPMTU {
		end := off + RTPMTU
		if end > total {
			end = total
		}
		marker := end == total
		pkt := make([]byte, 12, 12+end-off)
		pkt[0] = 0x80
		pkt[1] = p.payloadTyp
		if marker {
			pkt[1] |= 0x80
		}
		pkt[2] = byte(p.seq >> 8)
		pkt[3] = byte(p.seq)
		pkt[4] = byte(tsTicks >> 24)
		pkt[5] = byte(tsTicks >> 16)
		pkt[6] = byte(tsTicks >> 8)
		pkt[7] = byte(tsTicks)
		pkt[8] = byte(p.ssrc >> 24)
		pkt[9] = byte(p.ssrc >> 16)
		pkt[10] = byte(p.ssrc >> 8)
		pkt[11] = byte(p.ssrc)
		pkt = append(pkt, ps[off:end]...)
		p.seq++
		if _, err := p.conn.WriteToUDP(pkt, p.dst); err != nil {
			return fmt.Errorf("psmux: rtp send: %w", err)
		}
		p.sent++
	}
	return nil
}

// Sent returns the number of RTP packets transmitted (diagnostics).
func (p *RTPPacketizer) Sent() uint64 { return p.sent }
