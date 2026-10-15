package doorbell

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/event"
)

// udpSIPClient is a raw-UDP test door station: it speaks just enough SIP to
// exercise the server (REGISTER / INVITE / BYE) and reads responses off the
// same socket, like real intercom firmware does.
type udpSIPClient struct {
	conn   *net.UDPConn
	server string
	local  string
	user   string
	secret string
	callID string
	tag    string
	cseq   int
}

func dialSIPClient(t *testing.T, serverAddr, user, secret string) *udpSIPClient {
	t.Helper()
	conn, err := net.DialUDP("udp4", nil, mustUDPAddr(serverAddr))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	c := &udpSIPClient{
		conn:   conn,
		server: serverAddr,
		local:  conn.LocalAddr().String(),
		user:   user,
		secret: secret,
	}
	c.newDialog()
	return c
}

func (c *udpSIPClient) newDialog() {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	c.callID = hex.EncodeToString(b)
	b = make([]byte, 4)
	_, _ = rand.Read(b)
	c.tag = hex.EncodeToString(b)
	c.cseq = 1
}

func (c *udpSIPClient) send(raw string) error {
	_, err := c.conn.Write([]byte(strings.ReplaceAll(raw, "\n", "\r\n")))
	return err
}

// recv reads SIP messages until one matches pred (retransmits and stray
// datagrams are skipped), or the deadline passes.
func (c *udpSIPClient) recv(pred func(string) bool, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return "", fmt.Errorf("timed out waiting for response")
		}
		_ = c.conn.SetReadDeadline(time.Now().Add(remaining))
		buf := make([]byte, 4096)
		n, err := c.conn.Read(buf)
		if err != nil {
			return "", err
		}
		msg := string(buf[:n])
		if pred == nil || pred(msg) {
			return msg, nil
		}
	}
}

func (c *udpSIPClient) template(t *testing.T, name string, vars map[string]string) string {
	t.Helper()
	raw, err := os.ReadFile("testdata/" + name)
	require.NoError(t, err)
	out := string(raw)
	for k, v := range vars {
		out = strings.ReplaceAll(out, "%"+k+"%", v)
	}
	return out
}

func (c *udpSIPClient) register(t *testing.T, expires int) string {
	c.cseq++
	return c.template(t, "register.txt", map[string]string{
		"SERVER_ADDR": c.server,
		"LOCAL_ADDR":  c.local,
		"USER":        c.user,
		"BRANCH":      "z9hG4bK" + c.callID + strconv.Itoa(c.cseq),
		"TAG":         c.tag,
		"CALLID":      c.callID,
		"CSEQ":        strconv.Itoa(c.cseq),
		"EXPIRES":     strconv.Itoa(expires),
	})
}

func (c *udpSIPClient) registerAuth(t *testing.T, realm, nonce, uri string) string {
	resp := digestResponse(c.user, realm, c.secret, "REGISTER", uri, nonce)
	base := c.register(t, 300)
	auth := fmt.Sprintf(
		"Authorization: Digest realm=\"%s\",algorithm=MD5,nonce=\"%s\",username=\"%s\",uri=\"%s\",response=\"%s\"",
		realm, nonce, c.user, uri, resp)
	return strings.Replace(base, "Content-Length: 0", auth+"\nContent-Length: 0", 1)
}

func (c *udpSIPClient) invite(t *testing.T, sdp string) string {
	c.newDialog()
	body := strings.ReplaceAll(sdp, "\n", "\r\n")
	msg := c.template(t, "invite.txt", map[string]string{
		"SERVER_ADDR": c.server,
		"LOCAL_ADDR":  c.local,
		"USER":        c.user,
		"BRANCH":      "z9hG4bKinv" + c.callID,
		"TAG":         c.tag,
		"CALLID":      c.callID,
		"BODY_LEN":    strconv.Itoa(len(body)),
		"SDP":         body,
	})
	return strings.ReplaceAll(msg, "\r\n\r\n%SDP%\r\n", "\r\n\r\n"+body+"\r\n")
}

func (c *udpSIPClient) bye(t *testing.T) string {
	return c.template(t, "bye.txt", map[string]string{
		"SERVER_ADDR": c.server,
		"LOCAL_ADDR":  c.local,
		"USER":        c.user,
		"BRANCH":      "z9hG4bKbye" + c.callID,
		"TAG":         c.tag,
		"CALLID":      c.callID,
	})
}

// startTestServer boots a registrar-mode server on an ephemeral port with
// one door station configured.
func startTestServer(t *testing.T, extraUsers ...config.DoorbellUserConfig) (*Server, *event.EventBus, string) {
	t.Helper()
	port := freePort(t)
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	users := []config.DoorbellUserConfig{
		{Username: "door1", Secret: "s3cret", Name: "Front door"},
	}
	users = append(users, extraUsers...)
	cfg := ConfigFrom(config.DoorbellConfig{
		Listen:    addr,
		Realm:     "mibee-nvr",
		Transport: "udp",
		Users:     users,
	})
	cfg.RingTimeout = 60 * time.Second
	bus := event.NewEventBus(64)
	srv := NewServer(cfg, bus, nil)
	require.NoError(t, srv.Start(context.Background()))
	t.Cleanup(srv.Stop)
	return srv, bus, addr
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.ListenPacket("udp4", "127.0.0.1:0")
	require.NoError(t, err)
	defer l.Close()
	return l.LocalAddr().(*net.UDPAddr).Port
}

func mustUDPAddr(s string) *net.UDPAddr {
	a, err := net.ResolveUDPAddr("udp4", s)
	if err != nil {
		panic(err)
	}
	return a
}

func subscribeDoorbell(t *testing.T, bus *event.EventBus) chan event.Event {
	t.Helper()
	ch := make(chan event.Event, 16)
	require.NoError(t, bus.SubscribeByPrefix("doorbell.", ch, 16))
	return ch
}

func waitForEvent(t *testing.T, ch chan event.Event, topic string) event.DoorbellEvent {
	t.Helper()
	for {
		select {
		case ev := <-ch:
			if ev.Topic == topic {
				de, ok := ev.Data.(event.DoorbellEvent)
				require.True(t, ok)
				return de
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out waiting for %s", topic)
		}
	}
}

// TestFlow_RegisterChallengeAndAuth: REGISTER gets a 401 challenge; with the
// digest computed from the challenge the REGISTER succeeds.
func TestFlow_RegisterChallengeAndAuth(t *testing.T) {
	_, _, addr := startTestServer(t)
	c := dialSIPClient(t, addr, "door1", "s3cret")

	require.NoError(t, c.send(c.register(t, 300)))
	challenge, err := c.recv(func(m string) bool { return strings.HasPrefix(m, "SIP/2.0 401") }, 5*time.Second)
	require.NoError(t, err)

	realm := extractParam(challenge, "realm")
	nonce := extractParam(challenge, "nonce")
	require.Equal(t, "mibee-nvr", realm)
	require.NotEmpty(t, nonce)

	uri := "sip:" + addr
	require.NoError(t, c.send(c.registerAuth(t, realm, nonce, uri)))
	ok, err := c.recv(func(m string) bool { return strings.HasPrefix(m, "SIP/2.0 200") }, 5*time.Second)
	require.NoError(t, err)
	require.Contains(t, ok, "SIP/2.0 200")
}

// TestFlow_RegisterWrongSecretRejected.
func TestFlow_RegisterWrongSecretRejected(t *testing.T) {
	_, _, addr := startTestServer(t)
	c := dialSIPClient(t, addr, "door1", "wrong")

	require.NoError(t, c.send(c.register(t, 300)))
	challenge, err := c.recv(func(m string) bool { return strings.HasPrefix(m, "SIP/2.0 401") }, 5*time.Second)
	require.NoError(t, err)
	nonce := extractParam(challenge, "nonce")

	uri := "sip:" + addr
	require.NoError(t, c.send(c.registerAuth(t, "mibee-nvr", nonce, uri)))
	resp, err := c.recv(func(m string) bool {
		return strings.HasPrefix(m, "SIP/2.0 401") || strings.HasPrefix(m, "SIP/2.0 403")
	}, 5*time.Second)
	require.NoError(t, err)
	require.Contains(t, resp, "401", "bad credentials must be re-challenged, got: %s", firstLine(resp))
}

// TestFlow_UnknownUserInvitedRejected: anonymous/unconfigured From never
// gets past the door — hard requirement from #971.
func TestFlow_UnknownUserInvitedRejected(t *testing.T) {
	_, _, addr := startTestServer(t)
	c := dialSIPClient(t, addr, "intruder", "whatever")

	require.NoError(t, c.send(c.register(t, 300)))
	resp, err := c.recv(nil, 5*time.Second)
	require.NoError(t, err)
	require.Contains(t, firstLine(resp), "403")
}

func extractParam(msg, name string) string {
	for _, line := range strings.Split(msg, "\r\n") {
		if i := strings.Index(line, name+`="`); i >= 0 {
			rest := line[i+len(name)+2:]
			if j := strings.IndexByte(rest, '"'); j >= 0 {
				return rest[:j]
			}
		}
	}
	return ""
}

func firstLine(msg string) string {
	if i := strings.Index(msg, "\r\n"); i >= 0 {
		return msg[:i]
	}
	return msg
}

// TestFlow_RingAnswerTalkBye is the #971 acceptance path on the signaling
// layer: registered doorbell INVITEs → ring event → API answer → 200 OK with
// SDP → two-way RTP through the talk bridge → BYE ends the call.
func TestFlow_RingAnswerTalkBye(t *testing.T) {
	srv, bus, addr := startTestServer(t)
	events := subscribeDoorbell(t, bus)

	// Register the door station first.
	c := dialSIPClient(t, addr, "door1", "s3cret")
	require.NoError(t, c.send(c.register(t, 300)))
	challenge, err := c.recv(func(m string) bool { return strings.HasPrefix(m, "SIP/2.0 401") }, 5*time.Second)
	require.NoError(t, err)
	nonce := extractParam(challenge, "nonce")
	require.NoError(t, c.send(c.registerAuth(t, "mibee-nvr", nonce, "sip:"+addr)))
	_, err = c.recv(func(m string) bool { return strings.HasPrefix(m, "SIP/2.0 200") }, 5*time.Second)
	require.NoError(t, err)

	// The doorbell's own RTP socket (advertised in the SDP offer).
	rtpSock, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	require.NoError(t, err)
	t.Cleanup(func() { _ = rtpSock.Close() })
	rtpAddr := rtpSock.LocalAddr().(*net.UDPAddr)

	rawSDP, err := os.ReadFile("testdata/invite_offer.sdp")
	require.NoError(t, err)
	sdp := strings.ReplaceAll(string(rawSDP), "%DOORBELL_IP%", "127.0.0.1")
	sdp = strings.ReplaceAll(sdp, "%DOORBELL_RTP_PORT%", strconv.Itoa(rtpAddr.Port))

	// INVITE (ring).
	require.NoError(t, c.send(c.invite(t, sdp)))
	ringing, err := c.recv(func(m string) bool { return strings.HasPrefix(m, "SIP/2.0 180") }, 5*time.Second)
	require.NoError(t, err)
	require.Contains(t, ringing, "180 Ringing")

	ring := waitForEvent(t, events, "doorbell.ring")
	require.Equal(t, "door1", ring.User)
	require.Equal(t, "Front door", ring.Name)

	// Answer via the API path.
	require.NoError(t, srv.Answer(ring.CallID))
	okMsg, err := c.recv(func(m string) bool { return strings.HasPrefix(m, "SIP/2.0 200") }, 5*time.Second)
	require.NoError(t, err)
	require.Contains(t, okMsg, "m=audio")
	nvrRTPPort := sdpAudioPort(t, okMsg)
	require.Greater(t, nvrRTPPort, 0)

	waitForEvent(t, events, "doorbell.answered")

	// Attach the talk bridge (what the WS handler does).
	bridge, err := srv.TalkBridge(ring.CallID)
	require.NoError(t, err)
	defer bridge.Close()

	// Doorbell → NVR: send a standard RTP/PCMA packet at the answered port.
	alaw := []byte{0xd5, 0x2a, 0x7b}
	pkt := make([]byte, 12+len(alaw))
	pkt[0] = 0x80
	pkt[1] = 8
	binary.BigEndian.PutUint16(pkt[2:], 1)
	binary.BigEndian.PutUint32(pkt[4:], 160)
	binary.BigEndian.PutUint32(pkt[8:], 0xdeadbeef)
	copy(pkt[12:], alaw)
	nvrMedia := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: nvrRTPPort}
	_, err = rtpSock.WriteToUDP(pkt, nvrMedia)
	require.NoError(t, err)

	select {
	case got := <-bridge.Out:
		require.Equal(t, alaw, got)
	case <-time.After(5 * time.Second):
		t.Fatal("talk bridge did not receive the doorbell's RTP payload")
	}

	// NVR → doorbell: bridge In → RTP arrives at the doorbell's socket
	// (symmetric latch redirects to the source of the first packet).
	bridge.In <- []byte{0x55, 0x11}
	buf := make([]byte, 2048)
	_ = rtpSock.SetReadDeadline(time.Now().Add(5 * time.Second))
	n, _, err := rtpSock.ReadFromUDP(buf)
	require.NoError(t, err)
	require.Greater(t, n, 12)
	require.Equal(t, uint8(8), buf[1]&0x7f)
	require.Equal(t, byte(0x55), buf[12])
	require.Equal(t, byte(0x11), buf[13])

	// BYE ends the call.
	require.NoError(t, c.send(c.bye(t)))
	_, err = c.recv(func(m string) bool { return strings.HasPrefix(m, "SIP/2.0 200") }, 5*time.Second)
	require.NoError(t, err)
	ended := waitForEvent(t, events, "doorbell.ended")
	require.Equal(t, "bye", ended.Reason)
}

// TestFlow_UnregisteredInviteRejected: mode A requires a live registration —
// a From-spoofed INVITE from an unregistered socket gets 403.
func TestFlow_UnregisteredInviteRejected(t *testing.T) {
	_, bus, addr := startTestServer(t)
	events := subscribeDoorbell(t, bus)
	c := dialSIPClient(t, addr, "door1", "s3cret") // configured user, never registered

	require.NoError(t, c.send(c.invite(t, "v=0\r\nc=IN IP4 127.0.0.1\r\nm=audio 4000 RTP/AVP 8\r\n")))
	resp, err := c.recv(nil, 5*time.Second)
	require.NoError(t, err)
	require.Contains(t, firstLine(resp), "403")

	select {
	case <-events:
		t.Fatal("no ring event may fire for an unregistered INVITE")
	case <-time.After(300 * time.Millisecond):
	}
}

// sdpAudioPort extracts the m=audio port from a 200 OK body.
func sdpAudioPort(t *testing.T, msg string) int {
	t.Helper()
	body := msg
	if i := strings.Index(msg, "\r\n\r\n"); i >= 0 {
		body = msg[i+4:]
	}
	for _, line := range strings.Split(body, "\r\n") {
		if strings.HasPrefix(line, "m=audio ") {
			fields := strings.Fields(line)
			port, err := strconv.Atoi(fields[1])
			require.NoError(t, err)
			return port
		}
	}
	return 0
}
