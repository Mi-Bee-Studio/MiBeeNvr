package sntp

import (
	"encoding/binary"
	"time"
)

// NTP packet layout (RFC 5905 §7.3): 48-byte fixed header. All timestamps are
// 64-bit: 32-bit seconds since 1900-01-01 + 32-bit fraction.
const (
	ntpEpochOffset = 2208988800 // seconds between 1900-01-01 and 1970-01-01

	liNoWarning = 0
	vn4         = 4
	modeServer  = 4
)

// toNTPTime converts a wall time to the 64-bit NTP fixed-point representation.
func toNTPTime(t time.Time) uint64 {
	if t.IsZero() {
		return 0
	}
	secs := uint64(t.Unix()) + ntpEpochOffset
	frac := uint64(t.Nanosecond()) << 32 / 1e9
	return secs<<32 | frac
}

// buildResponse renders the 48-byte server reply for a client packet.
// The originate timestamp is echoed from the client's transmit field so the
// client can validate the round trip (RFC 4330 §8).
func buildResponse(req []byte) []byte {
	now := time.Now()
	resp := make([]byte, 48)

	// LI(2) | VN(2, echo client's version) | Mode(4=server)
	resp[0] = byte(liNoWarning<<6 | req[0]&0x38 | modeServer)
	resp[1] = 1                                 // stratum 1 (local clock source)
	resp[2] = 6                                 // poll interval (log2 seconds, 64s — informational)
	resp[3] = 0xFA                              // precision ≈ -6 (coarse, ~15ms claim is fine for cameras)
	copy(resp[4:8], []byte{'L', 'O', 'C', 'L'}) // reference id: local clock

	binary.BigEndian.PutUint32(resp[8:], 0)  // root delay
	binary.BigEndian.PutUint32(resp[12:], 0) // root dispersion
	// reference timestamp: now (no separate sync epoch to point at)
	binary.BigEndian.PutUint64(resp[16:], toNTPTime(now))
	// originate: echo the client's transmit timestamp
	binary.BigEndian.PutUint64(resp[24:], binary.BigEndian.Uint64(req[40:48]))
	// receive + transmit: now (sub-millisecond processing)
	binary.BigEndian.PutUint64(resp[32:], toNTPTime(now))
	binary.BigEndian.PutUint64(resp[40:], toNTPTime(now))
	return resp
}
