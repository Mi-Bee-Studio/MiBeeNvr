package discovery

import (
	"context"
	"net"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/net/dns/dnsmessage"
)

func TestMDNSRegistrarConstructor(t *testing.T) {
	t.Parallel()
	r := NewMDNSRegistrar("dev-id-1", "客厅录像机", 9090, true)

	require.Equal(t, "mdns", r.Name())
	require.Equal(t, "客厅录像机", r.instance, "instance is the plain device name")
	require.Equal(t, 9090, r.port)

	// TXT must match the discovery contract field-for-field.
	want := map[string]string{
		"ver":  "1",
		"id":   "dev-id-1",
		"name": url.QueryEscape("客厅录像机"),
		"tls":  "1",
		"api":  "9090",
	}
	require.Len(t, r.txt, len(want))
	for _, kv := range r.txt {
		var k, v string
		for i := range len(kv) {
			if kv[i] == '=' {
				k, v = kv[:i], kv[i+1:]
				break
			}
		}
		require.Contains(t, want, k, "unexpected TXT key %q", k)
		require.Equal(t, want[k], v, "TXT %s mismatch", k)
	}
}

func TestMDNSRegistrarPlainTextName(t *testing.T) {
	t.Parallel()
	r := NewMDNSRegistrar("id-2", "testhost", 9091, false)
	for _, kv := range r.txt {
		if len(kv) > 5 && kv[:5] == "name=" {
			require.Equal(t, "name=testhost", kv)
		}
		if len(kv) > 4 && kv[:4] == "tls=" {
			require.Equal(t, "tls=0", kv)
		}
	}
}

func TestMDNSRegistrarStopWithoutStart(t *testing.T) {
	t.Parallel()
	r := NewMDNSRegistrar("id-3", "host", 9090, false)
	require.NoError(t, r.Stop(), "Stop before Start must be a no-op")
	require.NoError(t, r.Stop(), "Stop must be idempotent")
}

// startTestRegistrar binds a registrar to a loopback ephemeral port so the
// tests exercise the real read/answer path without touching multicast or
// port 5353 (legacy one-shot queries take the same answer-building code).
func startTestRegistrar(t *testing.T, deviceID, deviceName string, apiPort int) (*MDNSRegistrar, int) {
	t.Helper()
	r := NewMDNSRegistrar(deviceID, deviceName, apiPort, false)
	require.NoError(t, r.startOn(context.Background(), "127.0.0.1:0"))
	t.Cleanup(func() { _ = r.Stop() })
	return r, r.conn.LocalAddr().(*net.UDPAddr).Port
}

// query sends one mDNS question and returns the response (nil on timeout).
func query(t *testing.T, port int, q dnsmessage.Question, known []dnsmessage.Resource) *dnsmessage.Message {
	t.Helper()
	msg := dnsmessage.Message{
		Header:    dnsmessage.Header{ID: 0x1234},
		Answers:   known,
		Questions: []dnsmessage.Question{{Type: q.Type, Class: dnsmessage.ClassINET, Name: q.Name}},
	}
	buf, err := msg.Pack()
	require.NoError(t, err)

	c, err := net.DialTimeout("udp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), time.Second)
	require.NoError(t, err)
	defer c.Close()
	_, err = c.Write(buf)
	require.NoError(t, err)
	require.NoError(t, c.SetReadDeadline(time.Now().Add(700*time.Millisecond)))

	resp := make([]byte, 1500)
	n, err := c.Read(resp)
	if err != nil {
		return nil // timeout: caller asserts on absence
	}
	var out dnsmessage.Message
	require.NoError(t, out.Unpack(resp[:n]))
	return &out
}

func mustName(t *testing.T, s string) dnsmessage.Name {
	t.Helper()
	n, err := dnsmessage.NewName(s)
	require.NoError(t, err)
	return n
}

// A PTR browse query (what the app's NSD browse and avahi send) must yield
// the full record set: PTR for the browse, SRV+TXT for the resolve, exactly
// ONE A record for the connect (multi-address sets are unsafe — clients
// pick first/last/arbitrarily), with the five contract TXT keys. Queries
// from an ephemeral port are legacy one-shot queries: the response must
// echo the transaction ID (RFC 6762 §6.7).
func TestRegistrarAnswersPTRBrowse(t *testing.T) {
	t.Parallel()
	r, port := startTestRegistrar(t, "id-mdns-1", "living-nvr", 9090)

	resp := query(t, port,
		dnsmessage.Question{Type: dnsmessage.TypePTR, Name: mustName(t, "_mibee-nvr._tcp.local.")}, nil)
	require.NotNil(t, resp, "no answer to PTR browse query")
	require.True(t, resp.Response)
	require.True(t, resp.Authoritative)
	require.Equal(t, uint16(0x1234), resp.ID, "legacy response must echo the transaction ID")

	var sawPTR, sawSRV, sawTXT, sawA bool
	var txt []string
	var srvPort uint16
	for _, rr := range resp.Answers {
		name := nameString(rr.Header.Name)
		switch body := rr.Body.(type) {
		case *dnsmessage.PTRResource:
			if name == "_mibee-nvr._tcp.local." && nameString(body.PTR) == "living-nvr._mibee-nvr._tcp.local." {
				sawPTR = true
			}
		case *dnsmessage.SRVResource:
			if name == "living-nvr._mibee-nvr._tcp.local." {
				sawSRV = true
				srvPort = body.Port
			}
		case *dnsmessage.TXTResource:
			if name == "living-nvr._mibee-nvr._tcp.local." {
				sawTXT = true
				txt = body.TXT
			}
		case *dnsmessage.AResource:
			if name == r.hostFQDN {
				sawA = true
			}
		}
	}
	require.True(t, sawPTR, "missing PTR in %+v", resp.Answers)
	require.True(t, sawSRV, "missing SRV in %+v", resp.Answers)
	require.True(t, sawTXT, "missing TXT in %+v", resp.Answers)
	require.True(t, sawA, "missing A for SRV target %q in %+v", r.hostFQDN, resp.Answers)
	require.Equal(t, uint16(9090), srvPort)

	wantTXT := TXTRecords("id-mdns-1", "living-nvr", 9090, false)
	require.Equal(t, wantTXT, txt)

	// Exactly ONE A record even on multi-homed hosts (default-route
	// address; environments without one are skipped by the selector test
	// below — this assertion assumes the common routable case).
	aCount := 0
	for _, rr := range resp.Answers {
		if rr.Header.Type == dnsmessage.TypeA {
			aCount++
		}
	}
	require.Equal(t, 1, aCount, "multi-address A sets are unsafe; want exactly one")
}

// SRV and TXT queries target the instance record (the resolve phase).
func TestRegistrarAnswersInstanceQueries(t *testing.T) {
	t.Parallel()
	_, port := startTestRegistrar(t, "id-2", "inst", 9090)
	inst := "inst._mibee-nvr._tcp.local."

	txtResp := query(t, port, dnsmessage.Question{Type: dnsmessage.TypeTXT, Name: mustName(t, inst)}, nil)
	require.NotNil(t, txtResp, "no answer to TXT query")
	sawTXT := false
	for _, rr := range txtResp.Answers {
		if rr.Header.Type == dnsmessage.TypeTXT && nameString(rr.Header.Name) == inst {
			sawTXT = true
		}
	}
	require.True(t, sawTXT, "no TXT record in %+v", txtResp.Answers)

	srvResp := query(t, port, dnsmessage.Question{Type: dnsmessage.TypeSRV, Name: mustName(t, inst)}, nil)
	require.NotNil(t, srvResp, "no answer to SRV query")
}

// Known-answer suppression (RFC 6762 §7.1): a query that already carries our
// records gets no answer — answering would spark a storm between instances.
func TestRegistrarKnownAnswerSilence(t *testing.T) {
	t.Parallel()
	_, port := startTestRegistrar(t, "id-3", "inst", 9090)
	known := []dnsmessage.Resource{{
		Header: dnsmessage.ResourceHeader{
			Name:  mustName(t, "_mibee-nvr._tcp.local."),
			Type:  dnsmessage.TypePTR,
			Class: dnsmessage.ClassINET,
		},
		Body: &dnsmessage.PTRResource{PTR: mustName(t, "inst._mibee-nvr._tcp.local.")},
	}}
	require.Nil(t, query(t, port,
		dnsmessage.Question{Type: dnsmessage.TypePTR, Name: mustName(t, "_mibee-nvr._tcp.local.")}, known),
		"answered a known-answer query")
}

// Unrelated questions must not be answered (an NVR is not a printer or any
// other service on the wire).
func TestRegistrarIgnoresUnrelatedQueries(t *testing.T) {
	t.Parallel()
	_, port := startTestRegistrar(t, "id-4", "inst", 9090)
	require.Nil(t, query(t, port,
		dnsmessage.Question{Type: dnsmessage.TypePTR, Name: mustName(t, "_mibee-bell._tcp.local.")}, nil),
		"answered a foreign service query")
	require.Nil(t, query(t, port,
		dnsmessage.Question{Type: dnsmessage.TypeA, Name: mustName(t, "other-host.local.")}, nil),
		"answered a foreign host query")
}

// The A-record set collapses to the single default-route address; without a
// default route the fallback keeps the NVR discoverable (every non-loopback
// IPv4 — multi-address beats invisible).
func TestAnnounceIPsSelection(t *testing.T) {
	t.Parallel()
	routed := announceIPsFrom(defaultRouteProbe)
	if len(routed) == 0 {
		t.Skipf("no default route in this environment")
	}
	require.Len(t, routed, 1, "multi-address A sets are unsafe")
	require.False(t, routed[0].IsLoopback())

	fallback := announceIPsFrom("127.0.0.1:1") // loopback local addr → rejected
	require.NotEmpty(t, fallback, "fallback produced no addresses")
	for _, ip := range fallback {
		require.False(t, ip.IsLoopback(), "fallback contains loopback %v", ip)
	}
}

// A dotted device name must not leak into the DNS label: the wire packer
// splits at dots, which would turn the RFC 6763 single instance label into
// two. The display name still travels exact via TXT name=.
func TestDottedInstanceNameSanitized(t *testing.T) {
	t.Parallel()
	r := NewMDNSRegistrar("id", "living.room.5F", 9090, false)
	require.Equal(t, "living-room-5f._mibee-nvr._tcp.local.", r.instanceFQDN)
	require.Equal(t, TXTRecords("id", "living.room.5F", 9090, false), r.txt)
}

// Hostnames that are not DNS-safe (spaces, dots — NAS convention) fold to a
// single valid label instead of breaking the SRV target.
func TestHostLabelFromFolded(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"My NAS":   "my-nas",
		"a..b":     "a-b",
		"...":      "mibee-nvr",
		"":         "mibee-nvr",
		"ok-host":  "ok-host",
		"Box (客厅)": "box",
	}
	for in, want := range cases {
		require.Equal(t, want, hostLabelFrom(in), "hostLabelFrom(%q)", in)
	}
}

// Two registrars must coexist on 5353 — a resident avahi/systemd-resolved
// already holds that port, so the socket options must tolerate siblings
// (SO_REUSEADDR, no REUSEPORT). Environments without a usable 5353 skip;
// rig verification covers the real coexistence (issue #1130).
func TestRegistrarTwoServersCoexist(t *testing.T) {
	a := NewMDNSRegistrar("id-a", "nvr-a", 9090, false)
	if err := a.Start(context.Background()); err != nil {
		t.Skipf("cannot bind 5353 in this environment (%v); coexistence covered on the rig", err)
	}
	defer func() { _ = a.Stop() }()
	b := NewMDNSRegistrar("id-b", "nvr-b", 9091, false)
	if err := b.Start(context.Background()); err != nil {
		t.Fatalf("second registrar Start (5353 coexistence broken): %v", err)
	}
	defer func() { _ = b.Stop() }()
}
