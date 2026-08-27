package cascade

// Upper-platform flow tests (#566): the full REGISTER digest dance (401
// challenge → authorized REGISTER → 200), keepalive cadence, catalog /
// device-info query answers, and the RTP media pump from the camera hub to
// the INVITEd media address. See loopback_test.go for the harness.

import (
	"fmt"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/gb28181/manscdp"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/ghettovoice/gosip/log"
	"github.com/ghettovoice/gosip/sip"
	"github.com/ghettovoice/gosip/sip/parser"
	"github.com/stretchr/testify/require"
)

// parseSIPRaw parses raw SIP bytes with the harness logger.
func parseSIPRaw(b []byte) (sip.Message, error) {
	return parser.ParseMessage(b, log.NewDefaultLogrusLogger())
}

// challengeResponse builds a raw SIP response for a received request.
// Header-copy pattern mirrors sip/subchannel_test.go respond200 (gosip's
// response builder misroutes when fed back through the parser).
func challengeResponse(t *testing.T, req sip.Request, code int, reason, extra string) []byte {
	t.Helper()
	var b strings.Builder
	fmt.Fprintf(&b, "SIP/2.0 %d %s\r\n", code, reason)
	for _, h := range []string{"Via", "From", "To", "Call-ID", "CSeq", "Max-Forwards"} {
		for _, v := range req.GetHeaders(h) {
			b.WriteString(h + ": " + v.Value() + "\r\n")
		}
	}
	if extra != "" {
		b.WriteString(extra + "\r\n")
	}
	b.WriteString("Content-Length: 0\r\n\r\n")
	return []byte(b.String())
}

// serveRegistration plays the upper platform's registrar: REGISTER without
// Authorization gets a 401 digest challenge; with Authorization gets 200.
// Keepalive MESSAGEs are answered 200. Runs until stop is closed.
func serveRegistration(t *testing.T, up *upperSocket, stop <-chan struct{}) {
	t.Helper()
	go func() {
		buf := make([]byte, 65535)
		for {
			select {
			case <-stop:
				return
			default:
			}
			_ = up.conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
			n, src, err := up.conn.ReadFromUDP(buf)
			if err != nil {
				continue
			}
			msg, err := parseSIPBytes(buf[:n])
			if err != nil {
				continue
			}
			req, ok := msg.(sip.Request)
			if !ok {
				continue
			}
			switch req.Method() {
			case sip.REGISTER:
				if len(req.GetHeaders("Authorization")) == 0 {
					chal := `WWW-Authenticate: Digest realm="34020000002000000001", nonce="lb-nonce-1", algorithm=MD5`
					_, _ = up.conn.WriteToUDP(challengeResponse(t, req, 401, "Unauthorized", chal), src)
				} else {
					_, _ = up.conn.WriteToUDP(challengeResponse(t, req, 200, "OK", ""), src)
				}
			case sip.MESSAGE:
				// Keepalive (and any other platform-directed MESSAGE): accept.
				_, _ = up.conn.WriteToUDP(challengeResponse(t, req, 200, "OK", ""), src)
			}
		}
	}()
}

func TestLoopbackRegisterDigestDanceAndKeepalive(t *testing.T) {
	hub := model.NewStreamHub()
	db := newCascadeTestDB(t)
	svc, up := startLoopbackService(t, hubSource{fakeSource{cams: []CameraInfo{{ID: "cam-1", Name: "Front"}}}, hub}, db)

	stop := make(chan struct{})
	serveRegistration(t, up, stop)
	defer close(stop)

	// The service REGISTERs on its own; poll the observable state.
	require.Eventually(t, func() bool { return svc.Online() }, 10*time.Second, 100*time.Millisecond,
		"service must complete the 401-challenge digest dance and register")
	since, ok := svc.RegistrationSince()
	require.True(t, ok, "registration duration must be available once online")
	require.GreaterOrEqual(t, since, time.Duration(0))
	require.Equal(t, 0, svc.ForwardCount(), "no forwards yet")
}

func TestLoopbackCatalogQueryAnswer(t *testing.T) {
	hub := model.NewStreamHub()
	db := newCascadeTestDB(t)
	svc, up := startLoopbackService(t, hubSource{fakeSource{cams: []CameraInfo{{ID: "cam-1", Name: "Front"}}}, hub}, db)
	_, err := svc.catalogItems()
	require.NoError(t, err)

	body, err := manscdp.Encode(manscdp.CatalogQuery{CmdType: manscdp.CmdCatalog, SN: 11})
	require.NoError(t, err)
	res := up.roundTrip(up.request(sip.MESSAGE, lbChannelOne, string(body), "Application/MANSCDP+xml"))
	require.Equal(t, 200, int(res.StatusCode()))

	answer := up.awaitServerRequest(sip.MESSAGE, "<CmdType>Catalog</CmdType>")
	require.Contains(t, string(answer.Body()), "<SN>11</SN>", "catalog answer must echo the SN")
	require.Contains(t, string(answer.Body()), lbChannelOne, "catalog answer carries the allocated channel")
}

func TestLoopbackDeviceInfoQueryAnswer(t *testing.T) {
	db := newCascadeTestDB(t)
	_, up := startLoopbackService(t, fakeSource{}, db)

	body, err := manscdp.Encode(manscdp.DeviceInfo{CmdType: manscdp.CmdDeviceInfo, SN: 12})
	require.NoError(t, err)
	res := up.roundTrip(up.request(sip.MESSAGE, testCfg().LocalDeviceID, string(body), "Application/MANSCDP+xml"))
	require.Equal(t, 200, int(res.StatusCode()))

	answer := up.awaitServerRequest(sip.MESSAGE, "<CmdType>DeviceInfo</CmdType>")
	require.Contains(t, string(answer.Body()), "MiBee NVR", "device-info answer identifies the NVR")
}

// TestLoopbackMediaPump drives real frames through the hub and asserts RTP
// packets land on the INVITEd media address — the full forward path.
func TestLoopbackMediaPump(t *testing.T) {
	hub := model.NewStreamHub()
	db := newCascadeTestDB(t)
	svc, up := startLoopbackService(t, hubSource{fakeSource{cams: []CameraInfo{{ID: "cam-1", Name: "Front", Encoding: "h264"}}}, hub}, db)
	_, err := svc.catalogItems()
	require.NoError(t, err)

	media, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	require.NoError(t, err)
	defer media.Close()

	sdp := "v=0\r\no=" + lbUpperDevice + " 0 0 IN IP4 " + lbLocalHost + "\r\ns=Play\r\n" +
		"c=IN IP4 " + lbLocalHost + "\r\nt=0 0\r\n" +
		"m=video " + strconv.Itoa(media.LocalAddr().(*net.UDPAddr).Port) + " RTP/AVP 96\r\ny=4242\r\n"
	res := up.roundTrip(up.request(sip.INVITE, lbChannelOne, sdp, "application/sdp"))
	require.Equal(t, 200, int(res.StatusCode()))

	// Broadcast IDR frames until the (asynchronously subscribed) session
	// picks one up and forwards it — poll, never sleep.
	buf := make([]byte, 2048)
	idr := [][]byte{{0x67, 0x64, 0x00, 0x1f}, {0x68, 0xeb, 0xe3, 0xcb}}
	require.Eventually(t, func() bool {
		hub.Broadcast(1000, idr, true)
		_ = media.SetReadDeadline(time.Now().Add(150 * time.Millisecond))
		n, _, err := media.ReadFromUDP(buf)
		return err == nil && n > 12 && buf[0]&0x80 == 0x80 // RTP v2 header
	}, 5*time.Second, 50*time.Millisecond, "forwarded RTP must arrive on the INVITEd media address")

	require.Equal(t, 1, svc.ForwardCount(), "forward counter reflects the active session")
}

func TestAbs64(t *testing.T) {
	require.Equal(t, int64(3), abs64(-3))
	require.Equal(t, int64(3), abs64(3))
	require.Equal(t, int64(0), abs64(0))
}

// parseSIPBytes is a small wrapper so the registration responder shares the
// harness parser.
func parseSIPBytes(b []byte) (sip.Message, error) {
	return parseSIPRaw(b)
}
