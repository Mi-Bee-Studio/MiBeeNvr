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
		{"the observed artifact (docker-bridge prflx, 360ms)", StatsSnapshot{
			ICERemoteCandidateType:    "prflx",
			ICERemoteCandidateAddress: "192.168.193.63",
			ICERTTSeconds:             0.363,
		}, true},
		{"artifact with stats lag (no RTT yet)", StatsSnapshot{
			ICERemoteCandidateType:    "prflx",
			ICERemoteCandidateAddress: "100.64.0.9",
		}, true},
		{"genuine same-LAN prflx (3ms) stays", StatsSnapshot{
			ICERemoteCandidateType:    "prflx",
			ICERemoteCandidateAddress: "192.168.63.52",
			ICERTTSeconds:             0.003,
		}, false},
		{"public prflx stays", StatsSnapshot{
			ICERemoteCandidateType:    "prflx",
			ICERemoteCandidateAddress: "121.8.0.156",
			ICERTTSeconds:             0.4,
		}, false},
		{"signaled private host candidate stays", StatsSnapshot{
			ICERemoteCandidateType:    "host",
			ICERemoteCandidateAddress: "192.168.63.52",
			ICERTTSeconds:             0.363,
		}, false},
		{"relay-nominated pair stays", StatsSnapshot{
			ICERemoteCandidateType:    "relay",
			ICERemoteCandidateAddress: "8.134.136.138",
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
