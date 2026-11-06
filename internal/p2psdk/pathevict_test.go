package peer

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPathologicalDirectPair pins the #82 eviction rule. Two shapes are
// pathological: prflx+private+terrible RTT (the original cross-wiring
// artifact) and host+private+sustained-terrible RTT (the docker-bridge
// candidate a container signals but nobody outside its bridge can reach —
// added after the prflx-only rule was observed leaking exactly this).
// Everything saner stays direct.
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
		{"host-typed docker-bridge artifact (468ms) evicts", StatsSnapshot{
			ICERemoteCandidateType:    "host",
			ICERemoteCandidateAddress: "192.168.194.7",
			ICERTTSeconds:             0.468,
		}, true},
		{"host-typed dead pair (no RTT ever) evicts", StatsSnapshot{
			ICERemoteCandidateType:    "host",
			ICERemoteCandidateAddress: "172.18.0.3",
		}, true},
		{"routed cross-subnet LAN host pair (11ms) stays", StatsSnapshot{
			ICERemoteCandidateType:    "host",
			ICERemoteCandidateAddress: "192.168.62.41",
			ICERTTSeconds:             0.011,
		}, false},
		{"CGNAT-space VPN overlay host pair (220ms) stays", StatsSnapshot{
			ICERemoteCandidateType:    "host",
			ICERemoteCandidateAddress: "100.91.12.4",
			ICERTTSeconds:             0.22,
		}, false},
		{"public host pair stays", StatsSnapshot{
			ICERemoteCandidateType:    "host",
			ICERemoteCandidateAddress: "8.134.136.138",
			ICERTTSeconds:             0.4,
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

// TestCheckPathEvictionStreakHysteresis: eviction requires
// pathologicalStreakMin CONSECUTIVE pathological samples — a lone jitter
// spike (or a burst broken by one healthy sample) must never demote a
// healthy direct session to the relay. watchSignal stays nil so the
// adoption path is not exercised here (its wiring is unchanged); the
// streak accounting under mu is what this pins.
func TestCheckPathEvictionStreakHysteresis(t *testing.T) {
	p := &p2pSession{}
	bad := StatsSnapshot{
		ICERemoteCandidateType:    "host",
		ICERemoteCandidateAddress: "192.168.194.7",
		ICERTTSeconds:             0.5,
	}
	good := StatsSnapshot{
		ICERemoteCandidateType:    "host",
		ICERemoteCandidateAddress: "192.168.194.7",
		ICERTTSeconds:             0.005,
	}

	p.checkPathEviction(bad)
	p.checkPathEviction(bad)
	require.Equal(t, 2, p.evictStreak, "two bad samples alone must not arm eviction")

	p.checkPathEviction(good)
	require.Equal(t, 0, p.evictStreak, "one healthy sample resets the streak")

	for range pathologicalStreakMin {
		p.checkPathEviction(bad)
	}
	require.Equal(t, pathologicalStreakMin, p.evictStreak, "sustained bad samples accumulate to the threshold")
}
