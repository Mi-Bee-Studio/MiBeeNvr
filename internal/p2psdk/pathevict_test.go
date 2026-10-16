package peer

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestPathologicalDirectPair pins the #82 eviction rule: prflx + unroutable
// remote address + terrible RTT = evict; everything saner stays direct.
func TestPathologicalDirectPair(t *testing.T) {
	cases := []struct {
		name string
		snap StatsSnapshot
		want bool
	}{
		{"docker-bridge prflx shape with terrible RTT (360ms)", StatsSnapshot{
			ICERemoteCandidateType:    "prflx",
			ICERemoteCandidateAddress: "192.168.77.63",
			ICERTTSeconds:             0.363,
		}, true},
		{"artifact with stats lag (no RTT yet)", StatsSnapshot{
			ICERemoteCandidateType:    "prflx",
			ICERemoteCandidateAddress: "100.64.0.9",
		}, true},
		{"genuine same-LAN prflx (3ms) stays", StatsSnapshot{
			ICERemoteCandidateType:    "prflx",
			ICERemoteCandidateAddress: "192.168.77.52",
			ICERTTSeconds:             0.003,
		}, false},
		{"public prflx stays", StatsSnapshot{
			ICERemoteCandidateType:    "prflx",
			ICERemoteCandidateAddress: "198.51.100.156",
			ICERTTSeconds:             0.4,
		}, false},
		{"signaled private host candidate stays", StatsSnapshot{
			ICERemoteCandidateType:    "host",
			ICERemoteCandidateAddress: "192.168.77.52",
			ICERTTSeconds:             0.363,
		}, false},
		{"relay-nominated pair stays", StatsSnapshot{
			ICERemoteCandidateType:    "relay",
			ICERemoteCandidateAddress: "203.0.113.20",
		}, false},
		{"garbage address stays", StatsSnapshot{
			ICERemoteCandidateType:    "prflx",
			ICERemoteCandidateAddress: "not-an-ip",
			ICERTTSeconds:             0.4,
		}, false},
		{"empty snapshot stays", StatsSnapshot{}, false},
	}
	for _, c := range cases {
		assert.Equal(t, c.want, pathologicalDirectPair(c.snap), "case %q", c.name)
	}
}
