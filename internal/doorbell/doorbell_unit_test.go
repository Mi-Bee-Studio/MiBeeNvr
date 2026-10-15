package doorbell

import (
	"crypto/md5" //nolint:gosec // SIP digest mandates MD5
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	gosiplog "github.com/ghettovoice/gosip/log"
	"github.com/ghettovoice/gosip/sip"
	"github.com/ghettovoice/gosip/sip/parser"
	"github.com/stretchr/testify/require"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
)

func TestParseAudioOffer_Testdata(t *testing.T) {
	raw, err := os.ReadFile("testdata/invite_offer.sdp")
	require.NoError(t, err)
	sdp := strings.ReplaceAll(string(raw), "%DOORBELL_IP%", "192.0.2.50")
	sdp = strings.ReplaceAll(sdp, "%DOORBELL_RTP_PORT%", "49170")

	offer, ok := parseAudioOffer(sdp)
	require.True(t, ok)
	require.Equal(t, "192.0.2.50", offer.Address)
	require.Equal(t, 49170, offer.Port)
	require.True(t, offer.PCMA)
	require.True(t, offer.PCMU)

	pt, name, ok := offer.payloadType()
	require.True(t, ok)
	require.Equal(t, uint8(8), pt)
	require.Equal(t, "PCMA", name)
}

func TestParseAudioOffer_Rejects(t *testing.T) {
	// No audio m-line.
	_, ok := parseAudioOffer("v=0\r\nc=IN IP4 192.0.2.1\r\nm=video 6000 RTP/AVP 96\r\n")
	require.False(t, ok)
	// Audio but no G.711 payload offered.
	_, ok = parseAudioOffer("v=0\r\nc=IN IP4 192.0.2.1\r\nm=audio 6000 RTP/AVP 97\r\na=rtpmap:97 opus/48000\r\n")
	require.False(t, ok)
}

func TestParseAudioOffer_PCMUOnly(t *testing.T) {
	offer, ok := parseAudioOffer("v=0\r\nc=IN IP4 192.0.2.7\r\nm=audio 5000 RTP/AVP 0\r\n")
	require.True(t, ok)
	pt, name, ok := offer.payloadType()
	require.True(t, ok)
	require.Equal(t, uint8(0), pt)
	require.Equal(t, "PCMU", name)
}

func TestBuildAudioAnswer(t *testing.T) {
	ans := buildAudioAnswer("192.0.2.9", 40000, 8, "PCMA")
	require.Contains(t, ans, "c=IN IP4 192.0.2.9")
	require.Contains(t, ans, "m=audio 40000 RTP/AVP 8")
	require.Contains(t, ans, "a=rtpmap:8 PCMA/8000")
	require.Contains(t, ans, "a=sendrecv")
}

func TestNonces_SingleUse(t *testing.T) {
	n := newNonces()
	id := n.issue("door1")
	require.True(t, n.consume(id, "door1"))
	require.False(t, n.consume(id, "door1"), "nonce must be single-use")
	require.False(t, n.consume("bogus", "door1"))
}

func TestRegistry_ExpiryAndSourceMatch(t *testing.T) {
	r := newRegistry()
	r.put("door1", "192.0.2.50:52000", time.Minute)
	require.True(t, r.registered("door1", "192.0.2.50"))
	require.False(t, r.registered("door1", "203.0.113.9"), "source IP must match the registration")
	require.False(t, r.registered("door2", ""))
	require.Equal(t, 1, r.count())

	r.put("door1", "192.0.2.50:52000", -time.Second)
	require.False(t, r.registered("door1", ""), "expired binding must not count")

	r.put("door1", "192.0.2.50:52000", time.Minute)
	r.remove("door1")
	require.False(t, r.registered("door1", "192.0.2.50"))
}

func TestConfigFrom_Defaults(t *testing.T) {
	c := config.DoorbellConfig{
		Listen:    "127.0.0.1:0",
		Realm:     "mibee-nvr",
		Transport: "udp",
		Users: []config.DoorbellUserConfig{
			{Username: "door1", Secret: "s3cret", Name: "Front door"},
		},
		Outbound: &config.DoorbellOutboundConfig{
			Server:   "127.0.0.1:5060",
			Username: "nvr",
			Secret:   "pbx-pass",
			Expires:  "300s",
		},
	}
	cfg := ConfigFrom(c)
	require.True(t, cfg.LANOnly)
	require.Equal(t, 60*time.Second, cfg.RingTimeout)
	require.Equal(t, 300*time.Second, cfg.Outbound.Expires)
	u, ok := cfg.user("door1")
	require.True(t, ok)
	require.Equal(t, "Front door", u.Name)
}

// md5hexLocal mirrors the client-side digest math for the flow tests.
func md5hexLocal(parts ...string) string {
	h := md5.New() //nolint:gosec // SIP digest mandates MD5
	_, _ = h.Write([]byte(strings.Join(parts, ":")))
	return hex.EncodeToString(h.Sum(nil))
}

func digestResponse(username, realm, secret, method, uri, nonce string) string {
	ha1 := md5hexLocal(username, realm, secret)
	ha2 := md5hexLocal(method, uri)
	return md5hexLocal(ha1, nonce, ha2)
}

// TestMedia_SymmetricLatch: the media session's write destination starts
// as the SDP guess but the first valid inbound RTP packet re-targets it —
// OS-independent (plain UDP, no SIP stack).
func TestMedia_SymmetricLatch(t *testing.T) {
	m, err := startMedia(8)
	require.NoError(t, err)
	t.Cleanup(m.close)

	advertised, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	require.NoError(t, err)
	t.Cleanup(func() { _ = advertised.Close() })
	actual, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	require.NoError(t, err)
	t.Cleanup(func() { _ = actual.Close() })

	m.setDestination(advertised.LocalAddr().(*net.UDPAddr))
	out, write, detach := m.subscribe(16)
	defer detach()

	// One valid RTP packet from the unadvertised socket latches the route.
	payload := []byte{0xab, 0xcd}
	pkt := append([]byte{0x80, 8, 0, 1, 0, 0, 0, 160, 0, 0, 0, 1}, payload...)
	mediaPort := m.conn.LocalAddr().(*net.UDPAddr).Port
	_, err = actual.WriteToUDP(pkt, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: mediaPort})
	require.NoError(t, err)
	select {
	case got := <-out:
		require.Equal(t, payload, got)
	case <-time.After(5 * time.Second):
		t.Fatal("media session did not fan out the inbound payload")
	}

	// The write path now targets the latched source, not the SDP guess.
	write([]byte{0x01, 0x02})
	buf := make([]byte, 64)
	_ = actual.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, _, err = actual.ReadFromUDP(buf)
	require.NoError(t, err)
	require.Equal(t, byte(0x01), buf[12])
	require.Equal(t, byte(0x02), buf[13])
	_ = advertised.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	if n, _, err := advertised.ReadFromUDP(buf); err == nil {
		t.Fatalf("writes must follow the latched source, not the SDP guess (got %d bytes)", n)
	}
}

// TestOutboundHost: PBX host extraction for the mode-B INVITE source check.
func TestOutboundHost(t *testing.T) {
	s := &Server{cfg: Config{}}
	require.Equal(t, "", s.outboundHost())
	s.cfg.Outbound = &Outbound{Server: "192.168.1.5:5060"}
	require.Equal(t, "192.168.1.5", s.outboundHost())
	s.cfg.Outbound = &Outbound{Server: "sip:pbx.example.com"}
	require.Equal(t, "pbx.example.com", s.outboundHost())
}

// TestWidenWildcardListen: ":5070" must become "0.0.0.0:5070" — gosip fills
// an empty host with 127.0.0.1, which would loopback-bind the registrar.
func TestWidenWildcardListen(t *testing.T) {
	require.Equal(t, "0.0.0.0:5070", widenWildcardListen(":5070"))
	require.Equal(t, "0.0.0.0:5060", widenWildcardListen(" :5060"))
	require.Equal(t, "192.168.1.5:5070", widenWildcardListen("192.168.1.5:5070"))
	require.Equal(t, "[::]:5070", widenWildcardListen("[::]:5070"))
	require.Equal(t, "5070", widenWildcardListen("5070"))
}

// TestHeaderIntValue_TypedExpires: gosip's parser maps Expires to a typed
// *sip.Expires header — the value (including a meaningful 0) must survive
// extraction through a real parse, not just hand-built GenericHeaders.
func TestHeaderIntValue_TypedExpires(t *testing.T) {
	raw := "REGISTER sip:192.168.1.9:5070 SIP/2.0\r\n" +
		"Via: SIP/2.0/UDP 192.168.1.2:5555;branch=z9hG4bKtest\r\n" +
		"From: <sip:front-door@192.168.1.9>;tag=x\r\n" +
		"To: <sip:front-door@192.168.1.9>\r\n" +
		"Call-ID: typed-expires-test\r\n" +
		"CSeq: 1 REGISTER\r\n" +
		"Expires: %s\r\n" +
		"Content-Length: 0\r\n\r\n"
	for _, tc := range []struct {
		val    string
		want   int
		wantOK bool
	}{{"0", 0, true}, {"300", 300, true}, {"3601", 3601, true}, {"x", 0, false}} {
		msg, err := parser.ParseMessage([]byte(fmt.Sprintf(raw, tc.val)), gosiplog.NewDefaultLogrusLogger())
		require.NoError(t, err, tc.val)
		req, ok := msg.(sip.Request)
		require.True(t, ok)
		got, ok2 := headerIntValue(req, "Expires")
		require.Equal(t, tc.wantOK, ok2, tc.val)
		require.Equal(t, tc.want, got, tc.val)
	}

	// Absent header → not ok.
	msg, err := parser.ParseMessage([]byte(fmt.Sprintf(raw, "0")), gosiplog.NewDefaultLogrusLogger())
	require.NoError(t, err)
	req := msg.(sip.Request)
	req.RemoveHeader("Expires")
	_, ok := headerIntValue(req, "Expires")
	require.False(t, ok)
}
