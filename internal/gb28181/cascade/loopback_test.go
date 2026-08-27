package cascade

// Protocol-level loopback tests: the Service runs its real SIP server on a
// free localhost UDP port and a raw-UDP "upper platform" socket exchanges
// SIP messages with it. Covers the request handlers (INVITE/BYE/SUBSCRIBE/
// MESSAGE/INFO/OPTIONS) and the service lifecycle without any real device.
// Harness ported from internal/gb28181/sip/server_test.go. See #566.

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/gb28181/manscdp"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
	"github.com/ghettovoice/gosip/log"
	"github.com/ghettovoice/gosip/sip"
	"github.com/ghettovoice/gosip/sip/parser"
	"github.com/stretchr/testify/require"
)

const (
	lbUpperDevice = "34020000002000000002" // fake upper platform's device ID
	lbChannelOne  = "34020000001320000001" // first allocated channel
	lbLocalHost   = "127.0.0.1"
)

// freeUDPPort returns a free UDP port on loopback.
func freeUDPPort(t *testing.T) int {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	require.NoError(t, err)
	defer conn.Close()
	return conn.LocalAddr().(*net.UDPAddr).Port
}

var lbSeq atomic.Int64

// upperSocket is the fake upper platform: one UDP socket that sends requests
// to the cascade's SIP server and receives responses plus server-initiated
// requests (NOTIFY / record-info MESSAGEs).
type upperSocket struct {
	t    *testing.T
	conn *net.UDPConn
	sip  *net.UDPAddr // cascade SIP listen address
}

func newUpperSocket(t *testing.T, sipAddr string) *upperSocket {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	require.NoError(t, err)
	addr, err := net.ResolveUDPAddr("udp", sipAddr)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	return &upperSocket{t: t, conn: conn, sip: addr}
}

func (u *upperSocket) send(req sip.Request) {
	u.t.Helper()
	_, err := u.conn.WriteToUDP([]byte(req.String()), u.sip)
	require.NoError(u.t, err)
}

// readMessage reads one datagram and parses it as a SIP message.
func (u *upperSocket) readMessage() sip.Message {
	u.t.Helper()
	buf := make([]byte, 65535)
	require.NoError(u.t, u.conn.SetReadDeadline(time.Now().Add(3*time.Second)))
	n, _, err := u.conn.ReadFromUDP(buf)
	require.NoError(u.t, err, "expected a SIP message on the upper socket")
	msg, err := parser.ParseMessage(buf[:n], log.NewDefaultLogrusLogger())
	require.NoError(u.t, err)
	return msg
}

// roundTrip sends a request and returns the first final (>=200) response,
// matching by Call-ID and skipping server-initiated requests.
func (u *upperSocket) roundTrip(req sip.Request) sip.Response {
	u.t.Helper()
	callID := ""
	if id, ok := req.CallID(); ok {
		callID = id.String()
	}
	u.send(req)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		msg := u.readMessage()
		res, ok := msg.(sip.Response)
		if !ok {
			continue
		}
		if rc, ok := res.CallID(); ok && rc.String() == callID && res.StatusCode() >= 200 {
			return res
		}
	}
	u.t.Fatalf("roundTrip: no final response for Call-ID %s within 5s", callID)
	return nil
}

// awaitServerRequest polls until a server-initiated request of the given
// method arrives whose body contains bodyPart.
func (u *upperSocket) awaitServerRequest(method sip.RequestMethod, bodyPart string) sip.Request {
	u.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		msg := u.readMessage()
		req, ok := msg.(sip.Request)
		if !ok || req.Method() != method {
			continue
		}
		if bodyPart == "" || strings.Contains(string(req.Body()), bodyPart) {
			return req
		}
	}
	u.t.Fatalf("awaitServerRequest: no %s with body %q within 5s", method, bodyPart)
	return nil
}

// request builds a SIP request from the upper platform to the cascade. The
// Via sent-by carries the upper socket's real port so responses route back.
func (u *upperSocket) request(method sip.RequestMethod, toUser, body, contentType string, extra ...sip.Header) sip.Request {
	return u.requestDialog(method, toUser, body, contentType, nil, extra...)
}

// requestDialog additionally reuses an existing dialog Call-ID with a fresh
// branch and bumped CSeq — a real in-dialog re-INVITE. (Retransmitting the
// identical request is absorbed by the transaction layer as a retransmission
// and gets no new response.)
func (u *upperSocket) requestDialog(method sip.RequestMethod, toUser, body, contentType string, dialog *sip.CallID, extra ...sip.Header) sip.Request {
	u.t.Helper()
	port := sip.Port(u.conn.LocalAddr().(*net.UDPAddr).Port)
	rb := sip.NewRequestBuilder()
	rb.SetMethod(method)
	rb.SetFrom(&sip.Address{Uri: &sip.SipUri{FUser: sip.String{Str: lbUpperDevice}, FHost: lbLocalHost}})
	rb.SetTo(&sip.Address{Uri: &sip.SipUri{FUser: sip.String{Str: toUser}, FHost: lbLocalHost}})
	rb.SetRecipient(&sip.SipUri{FUser: sip.String{Str: toUser}, FHost: lbLocalHost})
	rb.SetHost(lbLocalHost)
	rb.AddVia(&sip.ViaHop{
		Host: lbLocalHost,
		Port: &port,
		Params: sip.NewParams().Add("branch", sip.String{Str: sip.GenerateBranch()}).
			Add("rport", sip.String{}),
	})
	cid := sip.CallID(fmt.Sprintf("lb-%d-%d", time.Now().UnixNano(), lbSeq.Add(1)))
	seq := uint(1)
	if dialog != nil {
		cid = *dialog
		seq = 2
	}
	rb.SetCallID(&cid)
	rb.SetSeqNo(seq)
	mf := sip.MaxForwards(70)
	rb.SetMaxForwards(&mf)
	if body != "" {
		rb.SetBody(body)
		ct := sip.ContentType(contentType)
		rb.SetContentType(&ct)
	}
	for _, h := range extra {
		rb.AddHeader(h)
	}
	req, err := rb.Build()
	require.NoError(u.t, err)
	return req
}

// playSDP builds a live-forward INVITE body pointing media at a throwaway
// port (never the SIP socket — stray RTP must not garble SIP reads).
func playSDP(t *testing.T, name string, withT bool) string {
	t.Helper()
	tline := "t=0 0\r\n"
	if withT {
		now := time.Now().UTC()
		tline = "t=" + strconv.FormatInt(now.Add(-10*time.Minute).Unix(), 10) + " " +
			strconv.FormatInt(now.Add(-5*time.Minute).Unix(), 10) + "\r\n"
	}
	return "v=0\r\no=" + lbUpperDevice + " 0 0 IN IP4 " + lbLocalHost + "\r\ns=" + name + "\r\n" +
		"c=IN IP4 " + lbLocalHost + "\r\n" + tline +
		"m=video " + strconv.Itoa(freeUDPPort(t)) + " RTP/AVP 96\r\ny=12345678\r\n"
}

// startLoopbackService boots the cascade against a fake upper socket and
// returns the service and socket. Both are cleaned up automatically.
func startLoopbackService(t *testing.T, src CameraSource, db *storage.DB) (*Service, *upperSocket) {
	t.Helper()
	cfg := testCfg()
	cfg.SIPListen = net.JoinHostPort(lbLocalHost, strconv.Itoa(freeUDPPort(t)))

	up := newUpperSocket(t, cfg.SIPListen)
	cfg.ServerAddr = up.conn.LocalAddr().String() // register/NOTIFY traffic target

	svc := New(cfg, src, db)
	require.NoError(t, svc.Start(context.Background()))
	t.Cleanup(func() { _ = svc.Stop() })
	return svc, up
}

// sessionIDs/playbackIDs snapshot the dialog maps under the service mutex.
// NEVER assert between Lock and Unlock: a failed require exits the goroutine
// while still holding svc.mu, which deadlocks registerLoop (setOnline) and
// makes the cleanup Stop() hang forever (#571 anti-flake: assert on copies).
func sessionIDs(svc *Service) []string {
	svc.mu.Lock()
	defer svc.mu.Unlock()
	ids := make([]string, 0, len(svc.sessions))
	for id := range svc.sessions {
		ids = append(ids, id)
	}
	return ids
}

func playbackIDs(svc *Service) []string {
	svc.mu.Lock()
	defer svc.mu.Unlock()
	ids := make([]string, 0, len(svc.playbacks))
	for id := range svc.playbacks {
		ids = append(ids, id)
	}
	return ids
}

func subCount(svc *Service) int {
	svc.mu.Lock()
	defer svc.mu.Unlock()
	return len(svc.subs)
}

func TestServiceNameAndNoUpperStart(t *testing.T) {
	db := newCascadeTestDB(t)
	svc := New(testCfg(), fakeSource{}, db)
	require.Equal(t, "gb28181-cascade", svc.Name())

	// No upper platform configured → Start must refuse.
	cfg := testCfg()
	cfg.ServerAddr = ""
	cfg.Upstreams = nil
	bare := New(cfg, fakeSource{}, db)
	require.Error(t, bare.Start(context.Background()), "Start without uppers must fail")
}

func TestLoopbackInviteLiveForwardAndBye(t *testing.T) {
	hub := model.NewStreamHub()
	db := newCascadeTestDB(t)
	svc, up := startLoopbackService(t, hubSource{fakeSource{cams: []CameraInfo{{ID: "cam-1", Name: "Front"}}}, hub}, db)
	_, err := svc.catalogItems()
	require.NoError(t, err)

	// Unknown channel → 404.
	res := up.roundTrip(up.request(sip.INVITE, "34020099991320000099", playSDP(t, "Play", false), "application/sdp"))
	require.Equal(t, 404, int(res.StatusCode()), "INVITE for unknown channel must 404")

	// Bad SDP → 400.
	res = up.roundTrip(up.request(sip.INVITE, lbChannelOne, "not-an-sdp", "application/sdp"))
	require.Equal(t, 400, int(res.StatusCode()), "INVITE with unparseable SDP must 400")

	// Valid INVITE → 200 with sendonly Play SDP.
	invite := up.request(sip.INVITE, lbChannelOne, playSDP(t, "Play", false), "application/sdp")
	res = up.roundTrip(invite)
	require.Equal(t, 200, int(res.StatusCode()))
	require.Contains(t, string(res.Body()), "s=Play")
	require.Contains(t, string(res.Body()), "a=sendonly")

	first := sessionIDs(svc)
	require.Len(t, first, 1, "live forward session must be registered")

	// Re-INVITE inside the dialog (same Call-ID, new branch + CSeq) is
	// idempotent — the same SDP is answered, no second session.
	dialogID, ok := invite.CallID()
	require.True(t, ok)
	res = up.roundTrip(up.requestDialog(sip.INVITE, lbChannelOne, playSDP(t, "Play", false), "application/sdp", dialogID))
	require.Equal(t, 200, int(res.StatusCode()))
	require.Contains(t, string(res.Body()), "s=Play")
	require.Len(t, sessionIDs(svc), 1, "re-INVITE must not create a second session")

	// Supersede: a new-dialog INVITE for the same channel tears the old one.
	invite2 := up.request(sip.INVITE, lbChannelOne, playSDP(t, "Play", false), "application/sdp")
	res = up.roundTrip(invite2)
	require.Equal(t, 200, int(res.StatusCode()))
	live := sessionIDs(svc)
	require.Len(t, live, 1, "one channel, one live forward")
	require.NotEqual(t, first[0], live[0], "the superseding dialog must own the session")

	// BYE (in-dialog: the INVITE's Call-ID) tears the forward down.
	byeID, ok := invite2.CallID()
	require.True(t, ok)
	res = up.roundTrip(up.requestDialog(sip.BYE, lbChannelOne, "", "", byeID))
	require.Equal(t, 200, int(res.StatusCode()))
	require.Empty(t, sessionIDs(svc), "BYE must remove the forward session")
}

func TestLoopbackInviteHiddenCameraRefused(t *testing.T) {
	hub := model.NewStreamHub()
	db := newCascadeTestDB(t)
	svc, up := startLoopbackService(t, hubSource{fakeSource{cams: []CameraInfo{{ID: "cam-1", Name: "Front", CascadeHidden: true}}}, hub}, db)
	_, err := svc.catalogItems()
	require.NoError(t, err) // allocation persists even for hidden cameras

	res := up.roundTrip(up.request(sip.INVITE, lbChannelOne, playSDP(t, "Play", false), "application/sdp"))
	require.Equal(t, 404, int(res.StatusCode()), "INVITE for a cascade-hidden camera must 404")
}

func TestLoopbackInviteNoHub(t *testing.T) {
	db := newCascadeTestDB(t)
	svc, up := startLoopbackService(t, fakeSource{cams: []CameraInfo{{ID: "cam-1", Name: "Front"}}}, db)
	_, err := svc.catalogItems()
	require.NoError(t, err)

	res := up.roundTrip(up.request(sip.INVITE, lbChannelOne, playSDP(t, "Play", false), "application/sdp"))
	require.Equal(t, 500, int(res.StatusCode()), "INVITE for a camera with no hub must 500")
}

func TestLoopbackSubscribeCatalogNotify(t *testing.T) {
	db := newCascadeTestDB(t)
	svc, up := startLoopbackService(t, hubSource{fakeSource{cams: []CameraInfo{{ID: "cam-1", Name: "Front"}}}, model.NewStreamHub()}, db)

	sub := up.request(sip.SUBSCRIBE, testCfg().LocalDeviceID, "", "")
	sub.AppendHeader(&sip.GenericHeader{HeaderName: "Event", Contents: "Catalog"})
	exp := sip.Expires(300)
	sub.AppendHeader(&exp)
	res := up.roundTrip(sub)
	require.Equal(t, 200, int(res.StatusCode()))

	// A fresh catalog subscription immediately gets the current catalog.
	notify := up.awaitServerRequest(sip.NOTIFY, "<CmdType>Catalog</CmdType>")
	require.Contains(t, string(notify.Body()), lbChannelOne, "NOTIFY must carry the allocated channel")

	require.Equal(t, 1, subCount(svc), "catalog subscription must be registered")

	// Non-catalog events are declined with Expires 0 and register nothing.
	alarmSub := up.request(sip.SUBSCRIBE, testCfg().LocalDeviceID, "", "")
	alarmSub.AppendHeader(&sip.GenericHeader{HeaderName: "Event", Contents: "Alarm"})
	res = up.roundTrip(alarmSub)
	require.Equal(t, 200, int(res.StatusCode()))
	require.Equal(t, 1, subCount(svc), "non-catalog SUBSCRIBE must not register a dialog")
}

func TestLoopbackRecordInfoQuery(t *testing.T) {
	hub := model.NewStreamHub()
	db := newCascadeTestDB(t)
	svc, up := startLoopbackService(t, hubSource{fakeSource{cams: []CameraInfo{{ID: "cam-1", Name: "Front"}}}, hub}, db)
	_, err := svc.catalogItems()
	require.NoError(t, err)

	// GB naive timestamps are wall-clock in the service's effective zone (the
	// host's local zone here) — build both the fixture times and the query
	// window in that zone so they overlap.
	now := time.Now()
	for i := range 2 {
		require.NoError(t, db.InsertRecording(context.Background(), &model.Recording{
			ID:        fmt.Sprintf("rec-%d", i),
			CameraID:  "cam-1",
			FilePath:  "/tmp/nonexistent.mp4",
			Format:    model.FormatH264,
			StartedAt: now.Add(-time.Duration(i+1) * time.Minute),
			EndedAt:   now.Add(-time.Duration(i) * time.Minute),
			Duration:  60,
		}))
	}

	body, err := manscdp.Encode(manscdp.RecordInfoQuery{
		CmdType:   manscdp.CmdRecordInfo,
		SN:        7,
		DeviceID:  lbChannelOne,
		StartTime: now.Add(-time.Hour).Format("2006-01-02T15:04:05"),
		EndTime:   now.Format("2006-01-02T15:04:05"),
	})
	require.NoError(t, err)

	res := up.roundTrip(up.request(sip.MESSAGE, lbChannelOne, string(body), "Application/MANSCDP+xml"))
	require.Equal(t, 200, int(res.StatusCode()))

	answer := up.awaitServerRequest(sip.MESSAGE, "<CmdType>RecordInfo</CmdType>")
	require.Contains(t, string(answer.Body()), "<SN>7</SN>", "answer must echo the query SN")
	require.Contains(t, string(answer.Body()), lbChannelOne)
	require.Contains(t, string(answer.Body()), "<SumNum>2</SumNum>", "both recordings must be reported")
}

func TestLoopbackPlaybackInviteAndControl(t *testing.T) {
	hub := model.NewStreamHub()
	db := newCascadeTestDB(t)
	svc, up := startLoopbackService(t, hubSource{fakeSource{cams: []CameraInfo{{ID: "cam-1", Name: "Front"}}}, hub}, db)
	_, err := svc.catalogItems()
	require.NoError(t, err)

	now := time.Now().UTC()
	require.NoError(t, db.InsertRecording(context.Background(), &model.Recording{
		ID:        "pb-1",
		CameraID:  "cam-1",
		FilePath:  "/tmp/nonexistent.mp4",
		Format:    model.FormatH264,
		StartedAt: now.Add(-10 * time.Minute),
		EndedAt:   now.Add(-5 * time.Minute),
		Duration:  300,
	}))

	pbInvite := up.request(sip.INVITE, lbChannelOne, playSDP(t, "Playback", true), "application/sdp")
	res := up.roundTrip(pbInvite)
	require.Equal(t, 200, int(res.StatusCode()))
	require.Contains(t, string(res.Body()), "s=Playback")
	require.Len(t, playbackIDs(svc), 1, "playback dialog must be registered")

	// In-dialog INFO (playback INVITE's Call-ID) with a MANSRTSP pause control.
	pbID, ok := pbInvite.CallID()
	require.True(t, ok)
	res = up.roundTrip(up.requestDialog(sip.INFO, lbChannelOne, "PAUSE\r\n", "application/MANSRTSP+rtsp", pbID))
	require.Equal(t, 200, int(res.StatusCode()))

	res = up.roundTrip(up.requestDialog(sip.BYE, lbChannelOne, "", "", pbID))
	require.Equal(t, 200, int(res.StatusCode()))
	require.Empty(t, playbackIDs(svc), "BYE must tear the playback dialog down")
}

func TestLoopbackOptions(t *testing.T) {
	db := newCascadeTestDB(t)
	_, up := startLoopbackService(t, fakeSource{}, db)
	res := up.roundTrip(up.request(sip.OPTIONS, testCfg().LocalDeviceID, "", ""))
	require.Equal(t, 200, int(res.StatusCode()))
}

func TestSetGBTimezoneEffective(t *testing.T) {
	svc := New(testCfg(), fakeSource{}, nil)
	require.Equal(t, time.Local, svc.gbTZ(), "default zone is the host's")
	svc.SetGBTimezone(time.UTC)
	require.Equal(t, time.UTC, svc.gbTZ())
}
