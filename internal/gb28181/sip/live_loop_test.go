package sip

// Live pull loopback tests (#566): the platform INVITEs the fake device for
// the live stream (InviteChannel), RTP/PS media flows into the bound
// recorder's AU callback, and ByeChannel tears the session down with an
// in-dialog BYE. Also covers OPTIONS handling and diagnostics helpers.

import (
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/gb28181"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/gb28181/manscdp"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/gb28181/psmux"
	"github.com/ghettovoice/gosip/log"
	"github.com/ghettovoice/gosip/sip"
	"github.com/stretchr/testify/require"
)

func TestInviteChannelLiveFlowAndBye(t *testing.T) {
	cfg := testConfig(t)
	srv, dm := startTestServer(t, cfg)
	client := newSIPClient(t, cfg.SIPListen)

	var auCount atomic.Int64
	var mu sync.Mutex
	var gotIDR bool
	enrol := &fakeEnroller{naluWriter: func(au [][]byte, ptsTicks int64, isIDR bool) {
		auCount.Add(1)
		if isIDR {
			mu.Lock()
			gotIDR = true
			mu.Unlock()
		}
	}}
	// The catalog handler AUTO-INVITEs bound channels asynchronously (the
	// production recovery flow) — answer its INVITE (and retransmissions)
	// until the dialog is confirmed, rather than racing an explicit
	// InviteChannel (which the existing receiver would make a no-op anyway).
	require.NoError(t, enrol.EnsureGB28181Camera(testDeviceID, fakeChannelID, "Front Door", "127.0.0.1"))
	srv.SetCameraEnroller(enrol)

	dm.Register(&gb28181.Device{ID: testDeviceID, NetAddr: client.conn.LocalAddr().String()})
	catalog, err := manscdp.Encode(manscdp.Catalog{
		CmdType: manscdp.CmdCatalog, SN: 1, DeviceID: testDeviceID, SumNum: 1,
		Item: []manscdp.Item{{DeviceID: fakeChannelID, Name: "Front Door", Parental: 0}},
	})
	require.NoError(t, err)
	client.sendMessage(string(catalog))

	var mediaPort int
	require.Eventually(t, func() bool {
		req := client.nextRequest(150 * time.Millisecond)
		if req != nil && req.Method() == sip.INVITE {
			require.Contains(t, string(req.Body()), "a=recvonly", "live INVITE SDP is recvonly")
			if mediaPort == 0 {
				mediaPort = sdpMediaPort(t, string(req.Body()))
			}
			client.respondRaw(req, 200, "OK", string(req.Body()), "application/sdp")
		}
		srv.mu.Lock()
		dlg := srv.dialogs[fakeChannelID]
		srv.mu.Unlock()
		return dlg != nil && mediaPort != 0
	}, 10*time.Second, 10*time.Millisecond, "auto-INVITE must complete its handshake")
	require.NotZero(t, mediaPort)

	// Stream a couple of IDR AUs as RTP/PS to the platform's receive port.
	media, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	require.NoError(t, err)
	defer media.Close()
	dst := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: mediaPort}
	mux := psmux.New()
	mux.SetVideoCodec("h264")
	rtp := psmux.NewRTPPacketizer(media, dst, 1234567890, 1)
	idrAU := [][]byte{{0x67, 0x64, 0x00, 0x1f}, {0x65, 0x01, 0x02, 0x03}}

	require.Eventually(t, func() bool {
		ps := mux.WriteAU(appendAU(idrAU), 90000, true)
		if err := rtp.Send(ps, 90000); err != nil {
			return false
		}
		mu.Lock()
		defer mu.Unlock()
		return gotIDR && auCount.Load() >= 1
	}, 5*time.Second, 50*time.Millisecond, "live RTP must reach the bound recorder AU callback")

	// OPTIONS from the device is answered.
	optReq := buildRequest(t, sip.OPTIONS, testDeviceID, testServerID, cfg.SIPListen, client.localPort(), "")
	optRes := client.roundTrip(optReq)
	require.Equal(t, 200, int(optRes.StatusCode()), "OPTIONS must be answered 200")

	// Platform-initiated BYE tears the session down (ByeChannelByID resolves
	// the owning device → ByeChannel → sendByeForChannel).
	byed := make(chan error, 1)
	go func() { byed <- srv.ByeChannelByID(fakeChannelID) }()
	require.NoError(t, client.answerRetransmits(sip.BYE, func(bye sip.Request) {
		client.respondRaw(bye, 200, "OK", "", "")
	}, byed, 10*time.Second))

	// With the session gone the blanket BYE is a no-op.
	srv.ByeAllSessions()

	// Stop the server before the raw-UDP client socket teardown (LIFO) to
	// avoid racing gosip transport errors against transaction termination.
	_ = srv.Stop()
}

func TestOnDeviceOfflineTearsDown(t *testing.T) {
	cfg := testConfig(t)
	srv, dm := startTestServer(t, cfg)
	client := newSIPClient(t, cfg.SIPListen)

	dm.Register(&gb28181.Device{ID: testDeviceID, NetAddr: client.conn.LocalAddr().String()})
	dev, ok := dm.Device(testDeviceID)
	require.True(t, ok)
	dev.Status.Store(gb28181.DeviceOnline)

	// Offline with no sessions is a clean no-op path; with the device marked
	// offline the watcher tears down anything bound to it.
	srv.OnDeviceOffline(testDeviceID)
}

func TestChannelStatusString(t *testing.T) {
	require.Equal(t, "inviting", channelStatusString(gb28181.ChannelInviting))
	require.Equal(t, "playing", channelStatusString(gb28181.ChannelPlaying))
	require.Equal(t, "idle", channelStatusString(0))
}

func TestSetGBTimezoneServer(t *testing.T) {
	srv := &Server{}
	require.Equal(t, time.Local, srv.gbTZ())
	srv.SetGBTimezone(time.UTC)
	require.Equal(t, time.UTC, srv.gbTZ())
}

// The slogAdapter must keep satisfying gosip's log.Logger surface — a method
// set change there breaks the adapter at runtime, not compile time.
func TestSlogAdapterSurface(t *testing.T) {
	var a log.Logger = slogAdapter{logger: slog.Default()}
	a = a.WithPrefix("x").WithFields(map[string]interface{}{"k": 1})
	require.Equal(t, "", a.Prefix())
	require.Nil(t, a.Fields())
	a.SetLevel(0)
	// Fatal/Panic variants must not actually exit/panic — they log at error.
	a.Fatal("fatal-msg")
	a.Fatalf("fatalf-%d", 1)
	a.Panic("panic-msg")
	a.Panicf("panicf-%d", 1)
	a.Trace("trace-msg")
	a.Tracef("tracef-%d", 1)
}

func TestPlaybackStatusForUnknownChannel(t *testing.T) {
	srv := &Server{}
	st, ok := srv.PlaybackStatusFor("no-such-channel")
	require.False(t, ok, "unknown channel has no playback status")
	require.False(t, st.Active)
}
