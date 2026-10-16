package peer

import (
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// TestAdvertiseableIP pins the #82 cross-site endpoint guard: anything a
// remote peer can never route to must not be advertised.
func TestAdvertiseableIP(t *testing.T) {
	cases := []struct {
		ip   string
		want bool
	}{
		{"8.8.8.8", true},
		{"198.51.100.156", true},
		{"1.1.1.1", true},
		{"", false},               // unparsable → nil IP
		{"127.0.0.1", false},      // loopback
		{"169.254.1.1", false},    // link-local
		{"224.0.0.1", false},      // multicast
		{"0.0.0.0", false},        // unspecified
		{"10.1.2.3", false},       // RFC1918
		{"172.16.0.1", false},     // RFC1918
		{"192.168.77.60", false}, // RFC1918 (docker-bridge poison form from #82)
		{"100.64.0.1", false},     // RFC 6598 CGNAT
		{"100.127.255.255", false},
		{"100.128.0.1", true},     // just outside CGNAT
		{"fd00::1", false},        // ULA
		{"fe80::1", false},        // IPv6 link-local
		{"2606:4700::1111", true}, // global IPv6
	}
	for _, c := range cases {
		var ip net.IP
		if c.ip != "" {
			ip = net.ParseIP(c.ip)
			assert.NotNil(t, ip, "fixture %q must parse", c.ip)
		}
		assert.Equal(t, c.want, advertiseableIP(ip), "ip=%q", c.ip)
	}
}

// TestAutoendpointSkipsPrivateUPnPResult: an IGD reporting a private WAN
// (router itself behind NAT) must fail the UPnP rung — the ladder continues
// instead of advertising an unreachable address (#82).
func TestAutoendpointSkipsPrivateUPnPResult(t *testing.T) {
	s := &autoEndpointState{}
	h := &autoEndpointHooks{
		discoverUPnP: func(time.Duration) (*UPnPGateway, error) { return &UPnPGateway{}, nil },
		externalIP:   func(*UPnPGateway) (string, error) { return "192.168.5.1", nil },
		addMapping:   func(*UPnPGateway, int, int, int) error { return nil },
		stunQuery: func(string, time.Duration) (string, error) {
			return "198.51.100.156:40000", nil
		},
	}
	res := s.resolve(40000, []string{"stun://x:3478"}, h)
	assert.Equal(t, "stun", res.mode,
		"UPnP rung must be skipped (private external), ladder falls through to STUN")
	assert.NotEmpty(t, res.addr)
	assert.Error(t, res.upnpErr, "the private-UPnP failure must surface in the rung error")
}
