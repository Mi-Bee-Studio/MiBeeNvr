package sip

// Fake-device playback-fetch tests (#566): a raw-UDP "GB device" registers
// (DeviceManager direct + catalog MESSAGE), the platform INVITEs it for a
// recording fetch, the device answers 200 OK and streams RTP/PS media, and
// the fetched AUs land in the camera-bound sink. Covers the playback client
// path end-to-end: QueryChannelRecords, StartPlayback/StartDownload,
// sendPlaybackInvite, sendInDialogInfo, StopPlayback, watchPlayback.

import (
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/gb28181"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/gb28181/manscdp"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/gb28181/psmux"
	"github.com/ghettovoice/gosip/sip"
	"github.com/stretchr/testify/require"
)

const (
	fakeChannelID = "34020000001320000011"
)

// fakeSink records the AUs the fetch pipeline writes.
type fakeSink struct {
	mu    sync.Mutex
	aus   [][][]byte
	idrs  int
	stops int
}

func newFakeSink() *fakeSink { return &fakeSink{} }

func (f *fakeSink) WriteNALU(au [][]byte, ptsTicks int64, isIDR bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.aus = append(f.aus, au)
	if isIDR {
		f.idrs++
	}
}

func (f *fakeSink) Stop() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stops++
	return nil
}

func (f *fakeSink) stats() (aus, idrs, stops int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.aus), f.idrs, f.stops
}

// respondRaw answers a server-initiated request with a raw SIP response,
// optionally carrying a body (header-copy pattern from respond200).
func (c *sipClient) respondRaw(req sip.Request, code int, reason, body, contentType string) {
	c.t.Helper()
	var b strings.Builder
	fmt.Fprintf(&b, "SIP/2.0 %d %s\r\n", code, reason)
	for _, h := range []string{"Via", "From", "To", "Call-ID", "CSeq", "Max-Forwards"} {
		for _, v := range req.GetHeaders(h) {
			b.WriteString(h + ": " + v.Value() + "\r\n")
		}
	}
	if contentType != "" {
		b.WriteString("Content-Type: " + contentType + "\r\n")
	}
	b.WriteString(fmt.Sprintf("Content-Length: %d\r\n\r\n", len(body)))
	b.WriteString(body)
	if _, err := c.conn.WriteToUDP([]byte(b.String()), c.addr); err != nil {
		c.t.Fatalf("respondRaw: write: %v", err)
	}
}

// answerRetransmits answers every retransmission of a server-initiated
// request until the paired API call finishes. Rationale: gosip's client
// transaction can race the FIRST 2xx response (response processed before
// the transaction finishes installing → dropped); INVITE/BYE retransmit on
// UDP, and answering the retransmission recovers deterministically — no
// sleeps, no flakiness (#571).
func (c *sipClient) answerRetransmits(method sip.RequestMethod, answer func(sip.Request), done <-chan error, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		req := c.nextRequest(150 * time.Millisecond)
		if req != nil && req.Method() == method {
			answer(req)
		}
		select {
		case err := <-done:
			return err
		default:
		}
	}
	select {
	case err := <-done:
		return err
	default:
		return fmt.Errorf("answerRetransmits: %s not completed within %s", method, timeout)
	}
}

// sendMessage sends a server-directed MESSAGE (device → platform).
func (c *sipClient) sendMessage(body string) {
	c.t.Helper()
	req := buildRequest(c.t, sip.MESSAGE, testDeviceID, testServerID, c.addr.String(), c.localPort(), body)
	if _, err := c.conn.WriteToUDP([]byte(req.String()), c.addr); err != nil {
		c.t.Fatalf("sendMessage: write: %v", err)
	}
}

// awaitRequestOfType polls until a server-initiated request of the given
// method arrives; unsolicited messages are skipped.
func (c *sipClient) awaitRequest(method sip.RequestMethod, timeout time.Duration) sip.Request {
	c.t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		req := c.nextRequest(time.Until(deadline))
		if req == nil {
			continue
		}
		if req.Method() == method {
			return req
		}
	}
	c.t.Fatalf("awaitRequest: no %s within %s", method, timeout)
	return nil
}

// sdpMediaPort extracts the m=video port from an SDP body.
func sdpMediaPort(t *testing.T, sdp string) int {
	t.Helper()
	for _, line := range strings.Split(sdp, "\r\n") {
		if strings.HasPrefix(line, "m=video ") {
			port, err := strconv.Atoi(strings.Fields(line)[1])
			require.NoError(t, err)
			return port
		}
	}
	t.Fatalf("no m=video line in SDP: %q", sdp)
	return 0
}

func TestPlaybackFetchEndToEnd(t *testing.T) {
	fetchFlow(t, false)
}

func TestDownloadFetchEndToEnd(t *testing.T) {
	fetchFlow(t, true)
}

func fetchFlow(t *testing.T, download bool) {
	cfg := testConfig(t)
	srv, dm := startTestServer(t, cfg)
	client := newSIPClient(t, cfg.SIPListen)

	sink := newFakeSink()
	enrol := &fakeEnroller{pbSink: sink}
	require.NoError(t, enrol.EnsureGB28181Camera(testDeviceID, fakeChannelID, "Front Door", "127.0.0.1"))
	srv.SetCameraEnroller(enrol)

	dm.Register(&gb28181.Device{ID: testDeviceID, NetAddr: client.conn.LocalAddr().String()})
	catalog, err := manscdp.Encode(manscdp.Catalog{
		CmdType: manscdp.CmdCatalog, SN: 1, DeviceID: testDeviceID, SumNum: 1,
		Item: []manscdp.Item{{DeviceID: fakeChannelID, Name: "Front Door", Parental: 0}},
	})
	require.NoError(t, err)
	client.sendMessage(string(catalog))
	require.Eventually(t, func() bool { return len(dm.Channels(testDeviceID)) == 1 },
		3*time.Second, 50*time.Millisecond, "catalog must register the channel")

	// Kick off the fetch; the INVITE lands on the device socket.
	start := time.Now().Add(-2 * time.Hour)
	end := start.Add(time.Hour)
	fetchDone := make(chan error, 1)
	go func() {
		if download {
			fetchDone <- srv.StartDownload(testDeviceID, fakeChannelID, start, end)
		} else {
			fetchDone <- srv.StartPlayback(testDeviceID, fakeChannelID, start, end)
		}
	}()

	sdpName := "Playback"
	if download {
		sdpName = "Download"
	}
	var mediaPort int
	var dialogID sip.CallID
	err = client.answerRetransmits(sip.INVITE, func(invite sip.Request) {
		// Both the catalog auto-INVITE (live) and the fetch INVITE arrive on
		// this socket — answer every one; only the fetch INVITE names the
		// session we stream media to.
		if strings.Contains(string(invite.Body()), "s="+sdpName) {
			if mediaPort == 0 {
				mediaPort = sdpMediaPort(t, string(invite.Body()))
				if id, ok := invite.CallID(); ok {
					dialogID = *id
				}
			}
		}
		client.respondRaw(invite, 200, "OK", string(invite.Body()), "application/sdp")
	}, fetchDone, 15*time.Second)
	require.NoError(t, err)
	require.NotZero(t, mediaPort, "fetch INVITE (s=%s) must have been answered", sdpName)
	require.NotEmpty(t, dialogID.Value())

	// Stream one IDR AU as RTP/PS to the platform's receive port.
	media, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	require.NoError(t, err)
	defer media.Close()
	dst := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: mediaPort}
	mux := psmux.New()
	mux.SetVideoCodec("h264")
	rtp := psmux.NewRTPPacketizer(media, dst, 1234567890, 1)
	idrAU := [][]byte{{0x67, 0x64, 0x00, 0x1f}, {0x68, 0xeb, 0xe3, 0xcb}, {0x65, 0x01, 0x02, 0x03}}
	ps := mux.WriteAU(appendAU(idrAU), 90000, true)
	require.NoError(t, rtp.Send(ps, 90000))

	require.Eventually(t, func() bool {
		aus, idrs, _ := sink.stats()
		return aus >= 1 && idrs >= 1
	}, 5*time.Second, 50*time.Millisecond, "fetched IDR AU must reach the camera sink")

	// INFO control: pause routes in-dialog to the device.
	paused := make(chan error, 1)
	go func() { paused <- srv.PlaybackControl(fakeChannelID, "pause", 0, 0) }()
	err = client.answerRetransmits(sip.INFO, func(info sip.Request) {
		require.Contains(t, string(info.Body()), "PAUSE", "pause INFO must reach the device")
		client.respondRaw(info, 200, "OK", "", "")
	}, paused, 10*time.Second)
	require.NoError(t, err)

	// Device-side MediaStatus finished INFO ends the fetch (playback only —
	// for downloads the platform keeps ownership of the stop). The INFO must
	// ride the fetch dialog: same Call-ID as the INVITE (handleInfo matches it).
	if !download {
		st, ok := srv.PlaybackStatusFor(fakeChannelID)
		require.True(t, ok, "active fetch must expose status")
		require.True(t, st.Active)

		require.NotEmpty(t, dialogID.Value())
		var b strings.Builder
		b.WriteString("INFO sip:" + testServerID + "@127.0.0.1 SIP/2.0\r\n")
		fmt.Fprintf(&b, "From: <sip:%s@127.0.0.1>\r\n", fakeChannelID)
		fmt.Fprintf(&b, "To: <sip:%s@127.0.0.1>\r\n", testServerID)
		fmt.Fprintf(&b, "Call-ID: %s\r\n", dialogID.Value())
		b.WriteString("CSeq: 2 INFO\r\n")
		b.WriteString("Via: SIP/2.0/UDP 127.0.0.1:" + strconv.Itoa(client.localPort()) +
			";branch=" + sip.GenerateBranch() + ";rport\r\n")
		b.WriteString("Content-Type: application/MANSRTSP+rtsp\r\n")
		b.WriteString("Content-Length: 21\r\n\r\nMediaStatus: finished\r\n")
		if _, err := client.conn.WriteToUDP([]byte(b.String()), client.addr); err != nil {
			t.Fatalf("send INFO: %v", err)
		}
		require.Eventually(t, func() bool {
			st, ok := srv.PlaybackStatusFor(fakeChannelID)
			return !ok || !st.Active
		}, 5*time.Second, 50*time.Millisecond, "MediaStatus finished must end the fetch")
	}

	// Stop (owner for downloads; idempotent after a finished playback). The
	// teardown's BYE may land on the device socket — answer it (and its
	// retransmissions) when it does; a finished playback may send no BYE at
	// all, so the done channel is buffered-nil-safe: drain it separately.
	stopped := make(chan error, 1)
	go func() { stopped <- srv.StopPlayback(fakeChannelID) }()
	byeSeen := false
	_ = client.answerRetransmits(sip.BYE, func(bye sip.Request) {
		byeSeen = true
		client.respondRaw(bye, 200, "OK", "", "")
	}, stopped, 5*time.Second)
	select {
	case err := <-stopped:
		require.NoError(t, err)
	default:
	}
	_ = byeSeen

	require.Eventually(t, func() bool {
		_, _, stops := sink.stats()
		return stops >= 1
	}, 5*time.Second, 50*time.Millisecond, "sink must be stopped with the fetch")

	// Stop the server before the raw-UDP client socket teardown (LIFO) to
	// avoid racing gosip transport errors against transaction termination.
	_ = srv.Stop()
}

func appendAU(au [][]byte) []byte {
	var out []byte
	for _, nalu := range au {
		out = append(out, 0, 0, 0, 1)
		out = append(out, nalu...)
	}
	return out
}
