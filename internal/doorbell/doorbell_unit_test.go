package doorbell

import (
	"crypto/md5" //nolint:gosec // SIP digest mandates MD5
	"encoding/hex"
	"os"
	"strings"
	"testing"
	"time"

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
