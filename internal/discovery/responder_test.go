package discovery

import (
	"encoding/json"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// startTestResponder starts a responder on an ephemeral port and returns it
// plus the bound address.
func startTestResponder(t *testing.T) (*UDPResponder, *net.UDPAddr) {
	t.Helper()
	r := NewUDPResponder("test-device-id", "test-host", 9090, false, 0)
	require.NoError(t, r.Start(t.Context()))
	addr := r.Addr()
	require.NotNil(t, addr, "responder did not bind")
	t.Cleanup(func() { _ = r.Stop() })
	return r, addr
}

// probeOnce sends a single datagram and waits for a reply.
func probeOnce(t *testing.T, addr *net.UDPAddr, payload string, wait time.Duration) ([]byte, bool) {
	t.Helper()
	c, err := net.DialUDP("udp4", nil, addr)
	require.NoError(t, err)
	defer c.Close()
	_, err = c.Write([]byte(payload))
	require.NoError(t, err)
	require.NoError(t, c.SetReadDeadline(time.Now().Add(wait)))
	buf := make([]byte, 512)
	n, err := c.Read(buf)
	if err != nil {
		return nil, false
	}
	return buf[:n], true
}

func TestUDPResponder_ReplyPayload(t *testing.T) {
	t.Parallel()
	_, addr := startTestResponder(t)

	reply, ok := probeOnce(t, addr, ProbeV1, 2*time.Second)
	require.True(t, ok, "expected a reply to the v1 probe")

	var got struct {
		V    int    `json:"v"`
		ID   string `json:"id"`
		Name string `json:"name"`
		API  int    `json:"api"`
		TLS  bool   `json:"tls"`
	}
	require.NoError(t, json.Unmarshal(reply, &got))
	require.Equal(t, 1, got.V)
	require.Equal(t, "test-device-id", got.ID)
	require.Equal(t, "test-host", got.Name)
	require.Equal(t, 9090, got.API)
	require.False(t, got.TLS)
}

func TestUDPResponder_IgnoresForeignPayload(t *testing.T) {
	t.Parallel()
	_, addr := startTestResponder(t)

	// Wrong payload and near-miss variants must not trigger a reply.
	for _, p := range []string{
		"",
		"MIBEE-NVR-DISCv1",        // missing '?'
		"mibee-nvr-discv1?",       // wrong case
		"MIBEE-NVR-DISCv1? extra", // trailing bytes
		"\x00\x01\x02",
	} {
		_, ok := probeOnce(t, addr, p, 300*time.Millisecond)
		require.False(t, ok, "unexpected reply to payload %q", p)
	}
}

func TestUDPResponder_RateLimitPerIP(t *testing.T) {
	t.Parallel()
	_, addr := startTestResponder(t)

	reply, ok := probeOnce(t, addr, ProbeV1, 2*time.Second)
	require.True(t, ok, "first probe must be answered")
	require.NotEmpty(t, reply)

	// An immediate second probe from the same source is throttled.
	_, ok = probeOnce(t, addr, ProbeV1, 300*time.Millisecond)
	require.False(t, ok, "second probe within the throttle window must not be answered")
}

func TestUDPResponder_StopIsIdempotent(t *testing.T) {
	t.Parallel()
	r, _ := startTestResponder(t)
	require.NoError(t, r.Stop())
	require.NoError(t, r.Stop())
	require.Nil(t, r.Addr(), "Addr must be nil after Stop")
}

func TestParseAPIPort(t *testing.T) {
	t.Parallel()
	cases := map[string]int{
		"":              9090,
		":9090":         9090,
		"0.0.0.0:9090":  9090,
		"192.0.2.1:80":  80,
		"9091":          9091,
		"host.example:": 9090, // empty port
		"not-a-port":    9090,
		":-1":           9090,
	}
	for in, want := range cases {
		require.Equal(t, want, ParseAPIPort(in), "input %q", in)
	}
}
