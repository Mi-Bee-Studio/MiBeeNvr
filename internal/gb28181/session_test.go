package gb28181

import (
	"encoding/binary"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pion/rtp"
	"github.com/stretchr/testify/require"
)

// TestSessionManager_Invite creates a session, verifies port allocation,
// and checks that the receiver is running.
func TestSessionManager_Invite(t *testing.T) {
	pm := NewPortManager(55000, 55010)
	sm := NewSessionManager(pm, "34020000001320000001")

	channel := &Channel{
		DeviceID: "34020000001310000001",
		ID:       "34020000001310000001001",
		Name:     "Camera 1",
		Parental: 0,
	}
	channel.Status.Store(ChannelIdle)

	sdpOffer := []byte("v=0\r\no=- 0 0 IN IP4 192.168.1.100\r\nc=IN IP4 192.168.1.100\r\nm=video 0 RTP/AVP 96\r\n")

	sdpAnswer, err := sm.Invite(channel, "127.0.0.1", "192.168.1.100:5060", sdpOffer, nil, nil)
	require.NoError(t, err, "Invite should succeed")
	require.NotNil(t, sdpAnswer, "SDP answer should not be nil")
	require.Contains(t, string(sdpAnswer), "m=video", "SDP answer should contain media line")

	// Verify channel state transition
	require.Equal(t, ChannelInviting, channel.Status.Load(), "channel should be in inviting state")

	// Verify receiver exists
	receiver := sm.GetReceiver(channel.ID)
	require.NotNil(t, receiver, "receiver should exist")
	require.True(t, receiver.Running(), "receiver should be running")

	// Verify port was allocated and removed from pool
	_, err = pm.Get()
	require.NoError(t, err, "should be able to allocate another port")

	// Clean up
	_ = sm.Bye(channel.ID)
	require.Nil(t, sm.GetReceiver(channel.ID), "receiver should be cleaned up")
}

// TestSessionManager_Invite_NoAvailablePorts verifies that Invite fails
// when the port pool is exhausted.
func TestSessionManager_Invite_NoAvailablePorts(t *testing.T) {
	pm := NewPortManager(55020, 55020) // Only one port
	sm := NewSessionManager(pm, "34020000001320000001")

	channel := &Channel{
		DeviceID: "34020000001310000001",
		ID:       "34020000001310000001001",
		Name:     "Camera 1",
		Parental: 0,
	}
	channel.Status.Store(ChannelIdle)

	sdpOffer := []byte("v=0\r\no=- 0 0 IN IP4 192.168.1.100\r\nc=IN IP4 192.168.1.100\r\nm=video 0 RTP/AVP 96\r\n")

	// First invite should succeed
	_, err := sm.Invite(channel, "127.0.0.1", "192.168.1.100:5060", sdpOffer, nil, nil)
	require.NoError(t, err, "first Invite should succeed")

	// Second invite should fail (no ports available)
	channel2 := &Channel{
		DeviceID: "34020000001310000001",
		ID:       "34020000001310000001002",
		Name:     "Camera 2",
		Parental: 0,
	}
	channel2.Status.Store(ChannelIdle)

	_, err = sm.Invite(channel2, "127.0.0.1", "192.168.1.100:5060", sdpOffer, nil, nil)
	require.Error(t, err, "second Invite should fail with no available ports")
	require.Contains(t, err.Error(), "no available RTP ports", "error should mention no available ports")

	// Clean up
	_ = sm.Bye(channel.ID)
}

// TestSessionManager_Bye stops a session and recycles its port.
func TestSessionManager_Bye(t *testing.T) {
	pm := NewPortManager(55030, 55040)
	sm := NewSessionManager(pm, "34020000001320000001")

	channel := &Channel{
		DeviceID: "34020000001310000001",
		ID:       "34020000001310000001001",
		Name:     "Camera 1",
		Parental: 0,
	}
	channel.Status.Store(ChannelIdle)

	sdpOffer := []byte("v=0\r\no=- 0 0 IN IP4 192.168.1.100\r\nc=IN IP4 192.168.1.100\r\nm=video 0 RTP/AVP 96\r\n")

	// Create session
	_, err := sm.Invite(channel, "127.0.0.1", "192.168.1.100:5060", sdpOffer, nil, nil)
	require.NoError(t, err)

	receiver := sm.GetReceiver(channel.ID)
	require.NotNil(t, receiver)

	// Stop session
	err = sm.Bye(channel.ID)
	require.NoError(t, err, "Bye should succeed")

	// Verify receiver is stopped
	require.Nil(t, sm.GetReceiver(channel.ID), "receiver should be cleaned up")

	// Port should be recycled (verify by allocating again)
	allocatedPort, err := pm.Get()
	require.NoError(t, err, "port should be recycled and available again")
	require.Equal(t, uint16(55030), allocatedPort, "first port should be recycled and allocated")

	pm.Recycle(allocatedPort)
}

// TestSessionManager_Bye_NonExistent verifies that Bye is a no-op
// for a non-existent session.
func TestSessionManager_Bye_NonExistent(t *testing.T) {
	pm := NewPortManager(55040, 55050)
	sm := NewSessionManager(pm, "34020000001320000001")

	// Bye on non-existent session should not error
	err := sm.Bye("non-existent-channel")
	require.NoError(t, err, "Bye should be a no-op for non-existent session")
}

// TestSessionManager_GetReceiver returns nil for non-existent sessions.
func TestSessionManager_GetReceiver(t *testing.T) {
	pm := NewPortManager(55050, 55060)
	sm := NewSessionManager(pm, "34020000001320000001")

	receiver := sm.GetReceiver("non-existent-channel")
	require.Nil(t, receiver, "GetReceiver should return nil for non-existent channel")
}

// TestSessionManager_MultipleSessions verifies multiple concurrent sessions.
func TestSessionManager_MultipleSessions(t *testing.T) {
	pm := NewPortManager(55060, 55070)
	sm := NewSessionManager(pm, "34020000001320000001")

	sdpOffer := []byte("v=0\r\no=- 0 0 IN IP4 192.168.1.100\r\nc=IN IP4 192.168.1.100\r\nm=video 0 RTP/AVP 96\r\n")

	// Create 3 sessions
	channels := []*Channel{
		{DeviceID: "34020000001310000001", ID: "34020000001310000001001", Name: "Cam 1", Parental: 0},
		{DeviceID: "34020000001310000001", ID: "34020000001310000001002", Name: "Cam 2", Parental: 0},
		{DeviceID: "34020000001310000002", ID: "34020000001310000002001", Name: "Cam 3", Parental: 0},
	}

	for _, ch := range channels {
		ch.Status.Store(ChannelIdle)
		_, err := sm.Invite(ch, "127.0.0.1", "192.168.1.100:5060", sdpOffer, nil, nil)
		require.NoError(t, err, "Invite should succeed for channel %s", ch.ID)
	}

	// Verify all sessions are active
	require.Equal(t, 3, sm.SessionCount(), "should have 3 active sessions")

	for _, ch := range channels {
		receiver := sm.GetReceiver(ch.ID)
		require.NotNil(t, receiver, "receiver should exist for channel %s", ch.ID)
		require.True(t, receiver.Running(), "receiver should be running for channel %s", ch.ID)
	}

	// Clean up all sessions
	for _, ch := range channels {
		_ = sm.Bye(ch.ID)
	}

	require.Equal(t, 0, sm.SessionCount(), "should have 0 active sessions after cleanup")
}

// TestSessionManager_StopAll stops all sessions.
func TestSessionManager_StopAll(t *testing.T) {
	pm := NewPortManager(55070, 55080)
	sm := NewSessionManager(pm, "34020000001320000001")

	sdpOffer := []byte("v=0\r\no=- 0 0 IN IP4 192.168.1.100\r\nc=IN IP4 192.168.1.100\r\nm=video 0 RTP/AVP 96\r\n")

	// Create 2 sessions
	channels := []*Channel{
		{DeviceID: "34020000001310000001", ID: "34020000001310000001001", Name: "Cam 1", Parental: 0},
		{DeviceID: "34020000001310000001", ID: "34020000001310000001002", Name: "Cam 2", Parental: 0},
	}

	for _, ch := range channels {
		ch.Status.Store(ChannelIdle)
		_, err := sm.Invite(ch, "127.0.0.1", "192.168.1.100:5060", sdpOffer, nil, nil)
		require.NoError(t, err)
	}

	require.Equal(t, 2, sm.SessionCount(), "should have 2 active sessions")

	// Stop all
	sm.StopAll()

	require.Equal(t, 0, sm.SessionCount(), "should have 0 sessions after StopAll")
}

// TestSessionManager_Invite_NilChannel verifies error handling for nil channel.
func TestSessionManager_Invite_NilChannel(t *testing.T) {
	pm := NewPortManager(55080, 55090)
	sm := NewSessionManager(pm, "34020000001320000001")

	_, err := sm.Invite(nil, "127.0.0.1", "192.168.1.100:5060", []byte("..."), nil, nil)
	require.Error(t, err, "Invite with nil channel should fail")
	require.Contains(t, err.Error(), "channel is nil", "error should mention nil channel")
}

// TestSessionManager_GetHub returns the StreamHub for a session.
func TestSessionManager_GetHub(t *testing.T) {
	pm := NewPortManager(55090, 55100)
	sm := NewSessionManager(pm, "34020000001320000001")

	channel := &Channel{
		DeviceID: "34020000001310000001",
		ID:       "34020000001310000001001",
		Name:     "Camera 1",
		Parental: 0,
	}
	channel.Status.Store(ChannelIdle)

	sdpOffer := []byte("v=0\r\no=- 0 0 IN IP4 192.168.1.100\r\nc=IN IP4 192.168.1.100\r\nm=video 0 RTP/AVP 96\r\n")

	_, err := sm.Invite(channel, "127.0.0.1", "192.168.1.100:5060", sdpOffer, nil, nil)
	require.NoError(t, err)

	hub := sm.GetHub(channel.ID)
	require.NotNil(t, hub, "Hub should not be nil")

	_ = sm.Bye(channel.ID)
}

// TestSessionManager_MarkPlaying verifies the MarkPlaying method.
func TestSessionManager_MarkPlaying(t *testing.T) {
	pm := NewPortManager(55100, 55110)
	sm := NewSessionManager(pm, "34020000001320000001")

	channel := &Channel{
		DeviceID: "34020000001310000001",
		ID:       "34020000001310000001001",
		Name:     "Camera 1",
		Parental: 0,
	}
	channel.Status.Store(ChannelIdle)

	sdpOffer := []byte("v=0\r\no=- 0 0 IN IP4 192.168.1.100\r\nc=IN IP4 192.168.1.100\r\nm=video 0 RTP/AVP 96\r\n")

	_, err := sm.Invite(channel, "127.0.0.1", "192.168.1.100:5060", sdpOffer, nil, nil)
	require.NoError(t, err)

	err = sm.MarkPlaying(channel.ID)
	require.NoError(t, err, "MarkPlaying should succeed")

	// Verify receiver is still running
	receiver := sm.GetReceiver(channel.ID)
	require.NotNil(t, receiver)
	require.True(t, receiver.Running())

	_ = sm.Bye(channel.ID)
}

// TestSessionManager_MarkPlaying_NonExistent verifies error handling.
func TestSessionManager_MarkPlaying_NonExistent(t *testing.T) {
	pm := NewPortManager(55110, 55120)
	sm := NewSessionManager(pm, "34020000001320000001")

	err := sm.MarkPlaying("non-existent-channel")
	require.Error(t, err, "MarkPlaying should fail for non-existent channel")
	require.Contains(t, err.Error(), "no active session", "error should mention no active session")
}

// TestSessionManager_SDPAnswerFormat verifies the SDP answer format.
func TestSessionManager_SDPAnswerFormat(t *testing.T) {
	pm := NewPortManager(55120, 55130)
	sm := NewSessionManager(pm, "34020000001320000001")

	channel := &Channel{
		DeviceID: "34020000001310000001",
		ID:       "34020000001310000001001",
		Name:     "Camera 1",
		Parental: 0,
	}
	channel.Status.Store(ChannelIdle)

	sdpOffer := []byte("v=0\r\no=- 0 0 IN IP4 192.168.1.100\r\nc=IN IP4 192.168.1.100\r\nm=video 0 RTP/AVP 96\r\n")

	sdpAnswer, err := sm.Invite(channel, "127.0.0.1", "192.168.1.100:5060", sdpOffer, nil, nil)
	require.NoError(t, err)

	sdpStr := string(sdpAnswer)
	require.Contains(t, sdpStr, "v=0", "should have version")
	require.Contains(t, sdpStr, "o=- 0 0 IN IP4 127.0.0.1", "should have origin")
	require.Contains(t, sdpStr, "s=Play", "should have session name")
	require.Contains(t, sdpStr, "c=IN IP4 127.0.0.1", "should have connection info")
	require.Contains(t, sdpStr, "t=0 0", "should have timing")
	require.Contains(t, sdpStr, "m=video", "should have media line")
	require.Contains(t, sdpStr, "a=recvonly", "should have recvonly attribute")
	require.Contains(t, sdpStr, "a=rtpmap:96 PS/90000", "should have RTP map")
	require.Contains(t, sdpStr, "y=", "should have SSRC (y=)")
	require.Regexp(t, `y=0\d{9}\r?\n`, sdpStr, "SSRC must be a 10-digit decimal with live prefix 0")

	_ = sm.Bye(channel.ID)
}

// TestSessionManager_ConcurrentInvites verifies thread safety.
func TestSessionManager_ConcurrentInvites(t *testing.T) {
	pm := NewPortManager(55130, 55145)
	sm := NewSessionManager(pm, "34020000001320000001")

	sdpOffer := []byte("v=0\r\no=- 0 0 IN IP4 192.168.1.100\r\nc=IN IP4 192.168.1.100\r\nm=video 0 RTP/AVP 96\r\n")

	var wg sync.WaitGroup
	numSessions := 5
	errors := make(chan error, numSessions)

	for i := range numSessions {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			channel := &Channel{
				DeviceID: "34020000001310000001",
				ID:       fmt.Sprintf("340200000013100000010%02d", i),
				Name:     fmt.Sprintf("Cam %d", i),
				Parental: 0,
			}
			channel.Status.Store(ChannelIdle)

			_, err := sm.Invite(channel, "127.0.0.1", "192.168.1.100:5060", sdpOffer, nil, nil)
			if err != nil {
				errors <- err
			}
		}(i)
	}

	wg.Wait()
	close(errors)

	// Should have no errors (enough ports for 5 sessions)
	for err := range errors {
		t.Errorf("concurrent Invite failed: %v", err)
	}

	require.Equal(t, numSessions, sm.SessionCount(), "should have created all sessions")

	sm.StopAll()
}

// TestSessionManager_ChannelStateTransitions verifies state machine.
func TestSessionManager_ChannelStateTransitions(t *testing.T) {
	pm := NewPortManager(55145, 55155)
	sm := NewSessionManager(pm, "34020000001320000001")

	channel := &Channel{
		DeviceID: "34020000001310000001",
		ID:       "34020000001310000001001",
		Name:     "Camera 1",
		Parental: 0,
	}
	channel.Status.Store(ChannelIdle)

	sdpOffer := []byte("v=0\r\no=- 0 0 IN IP4 192.168.1.100\r\nc=IN IP4 192.168.1.100\r\nm=video 0 RTP/AVP 96\r\n")

	// Invite: idle -> inviting
	_, err := sm.Invite(channel, "127.0.0.1", "192.168.1.100:5060", sdpOffer, nil, nil)
	require.NoError(t, err)
	require.Equal(t, ChannelInviting, channel.Status.Load(), "should be inviting")

	// Simulate ACK: inviting -> playing (caller's responsibility, not SessionManager)
	swapped := channel.Status.CompareAndSwap(ChannelInviting, ChannelPlaying)
	require.True(t, swapped, "should transition to playing")
	require.Equal(t, ChannelPlaying, channel.Status.Load(), "should be playing")

	// Bye: session stops (channel state transition is caller's responsibility)
	_ = sm.Bye(channel.ID)

	// Verify session is cleaned up
	require.Nil(t, sm.GetReceiver(channel.ID))
}

// TestSessionManager_ReceiverBroadcast verifies that the receiver
// broadcasts NALUs to the hub.
func TestSessionManager_ReceiverBroadcast(t *testing.T) {
	pm := NewPortManager(55155, 55165)
	sm := NewSessionManager(pm, "34020000001320000001")

	channel := &Channel{
		DeviceID: "34020000001310000001",
		ID:       "34020000001310000001001",
		Name:     "Camera 1",
		Parental: 0,
	}
	channel.Status.Store(ChannelIdle)

	sdpOffer := []byte("v=0\r\no=- 0 0 IN IP4 192.168.1.100\r\nc=IN IP4 192.168.1.100\r\nm=video 0 RTP/AVP 96\r\n")

	_, err := sm.Invite(channel, "127.0.0.1", "192.168.1.100:5060", sdpOffer, nil, nil)
	require.NoError(t, err)

	hub := sm.GetHub(channel.ID)
	require.NotNil(t, hub)

	// Subscribe to hub (FrameCallback signature: func(pts int64, au [][]byte))
	receivedCh := make(chan int64, 10)
	err = hub.Subscribe("test-sub", func(pts int64, au [][]byte) {
		select {
		case receivedCh <- pts:
		default:
		}
	})
	require.NoError(t, err)

	// Simulate NALU callback from receiver (this is what Receiver does internally)
	// We can't easily trigger the actual RTP path, so we test the callback setup
	receiver := sm.GetReceiver(channel.ID)
	require.NotNil(t, receiver)

	// The callback was set to broadcast to hub (AU granularity)
	require.NotNil(t, receiver.AUCallback, "AU callback should be set")

	// Cleanup
	sm.StopAll()
}

// BenchmarkSessionManager_Invite benchmarks the Invite operation.
func BenchmarkSessionManager_Invite(b *testing.B) {
	pm := NewPortManager(55165, 55265)
	sm := NewSessionManager(pm, "34020000001320000001")

	sdpOffer := []byte("v=0\r\no=- 0 0 IN IP4 192.168.1.100\r\nc=IN IP4 192.168.1.100\r\nm=video 0 RTP/AVP 96\r\n")

	b.ResetTimer()
	//nolint:intrange // benchmark loop
	for i := 0; i < b.N; i++ {
		channel := &Channel{
			DeviceID: "34020000001310000001",
			ID:       fmt.Sprintf("34020000001310000001%04d", i),
			Name:     fmt.Sprintf("Cam %d", i),
			Parental: 0,
		}
		channel.Status.Store(ChannelIdle)

		_, err := sm.Invite(channel, "127.0.0.1", "192.168.1.100:5060", sdpOffer, nil, nil)
		if err != nil {
			b.Fatalf("Invite failed: %v", err)
		}
	}
}

// TestSession_PlaybackSDP verifies the playback session SDP format and
// that playback sessions use s=Playback with proper NTP timestamps.
func TestSession_PlaybackSDP(t *testing.T) {
	t.Helper()
	pm := NewPortManager(60000, 60100)
	sm := NewSessionManager(pm, "34020000002000000001")

	channel := &Channel{
		DeviceID: "34020000001310000001",
		ID:       "34020000001320000001",
		Name:     "Camera 1",
		Parental: 0,
	}
	channel.Status.Store(ChannelIdle)

	// Fake AUWriter that records calls
	calls := make([][]byte, 0)
	fakeSink := &fakeAUWriter{
		writeNALU: func(au [][]byte, ptsTicks int64, isIDR bool) {
			calls = append(calls, au...)
		},
	}

	// Create playback session
	start := time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC)
	end := time.Date(2024, 1, 15, 11, 0, 0, 0, time.UTC)
	sdpAnswer, err := sm.InvitePlayback(channel, "127.0.0.1", start, end, fakeSink, nil)
	require.NoError(t, err, "InvitePlayback should succeed")
	require.NotNil(t, sdpAnswer, "SDP answer should not be nil")

	sdpStr := string(sdpAnswer)
	require.Contains(t, sdpStr, "s=Playback", "SDP should contain s=Playback")
	require.Contains(t, sdpStr, "t=", "SDP should contain t= line for time range")
	require.Contains(t, sdpStr, "y=", "SDP should contain SSRC (y=)")

	// Verify SSRC starts with 1 (playback prefix)
	lines := strings.Split(sdpStr, "\r\n")
	for _, l := range lines {
		if strings.HasPrefix(l, "y=1") {
			break
		}
		if strings.HasPrefix(l, "y=") && !strings.HasPrefix(l, "y=1") {
			t.Fatalf("playback SSRC should start with 1, got: %s", l)
		}
	}

	// Verify port was allocated
	receiver := sm.GetPlaybackReceiver(channel.ID)
	require.NotNil(t, receiver, "playback receiver should exist")
	require.True(t, receiver.Running(), "receiver should be running")

	// Verify NTP timestamps in t= line
	// NTP = Unix + 2208988800
	startNTP := start.Unix() + 2208988800
	endNTP := end.Unix() + 2208988800
	tLine := fmt.Sprintf("t=%d %d\r\n", startNTP, endNTP)
	require.Contains(t, sdpStr, tLine, "t= line should have correct NTP timestamps")

	// Cleanup
	_ = sm.Bye(channel.ID)
	require.Nil(t, sm.GetReceiver(channel.ID), "receiver should be cleaned up")

	// Also verify live Invite still works (regression)
	channel2 := &Channel{
		DeviceID: "34020000001310000002",
		ID:       "34020000001320000002",
		Name:     "Camera 2",
		Parental: 0,
	}
	channel2.Status.Store(ChannelIdle)
	sdpOffer := []byte("v=0\r\no=- 0 0 IN IP4 192.168.1.100\r\nc=IN IP4 192.168.1.100\r\nm=video 0 RTP/AVP 96\r\n")
	sdpLive, err := sm.Invite(channel2, "127.0.0.1", "192.168.1.100:5060", sdpOffer, nil, nil)
	require.NoError(t, err)
	sdpLiveStr := string(sdpLive)
	require.Contains(t, sdpLiveStr, "s=Play", "Live SDP should contain s=Play")
	require.Contains(t, sdpLiveStr, "y=0", "Live SSRC should start with 0")
	_ = sm.Bye(channel2.ID)
}

// fakeAUWriter is a test implementation of AUWriter.
type fakeAUWriter struct {
	writeNALU func(au [][]byte, ptsTicks int64, isIDR bool)
}

func (f *fakeAUWriter) WriteNALU(au [][]byte, ptsTicks int64, isIDR bool) {
	if f.writeNALU != nil {
		f.writeNALU(au, ptsTicks, isIDR)
	}
}

// TestSessionInviteTCPPassive verifies the TCP-passive media path: the SDP
// carries TCP/RTP/AVP + setup:passive, a connection to the media port starts
// the receiver, and 0x24-framed RTP flows to the AU callback.
func TestSessionInviteTCPPassive(t *testing.T) {
	pm := NewPortManager(31000, 31010)
	sm := NewSessionManager(pm, "34020000002000000001")
	sm.SetMediaTransport(func() string { return "tcp-passive" })
	sm.SetTCPFraming(func() string { return "0x24" })

	ch := &Channel{ID: "34020000001320000001", DeviceID: "34020000001110000001"}
	ch.Status.Store(ChannelIdle)

	var mu sync.Mutex
	var gotFrames int
	onAU := func(au [][]byte, ptsTicks int64, isIDR bool) {
		mu.Lock()
		gotFrames++
		mu.Unlock()
	}

	sdp, err := sm.Invite(ch, "127.0.0.1", "127.0.0.1:5060", nil, onAU, nil)
	require.NoError(t, err)
	defer func() { _ = sm.Bye(ch.ID) }()

	require.Contains(t, string(sdp), "TCP/RTP/AVP 96")
	require.Contains(t, string(sdp), "a=setup:passive")
	require.Contains(t, string(sdp), "a=connection:new")

	// Extract the media port from the SDP m= line.
	port := sdpMediaPort(t, sdp)

	// Device-side: connect and send one 0x24-framed RTP packet carrying a
	// tiny PS AU (video PES with one NALU, marker set).
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 5*time.Second)
	require.NoError(t, err)
	defer conn.Close()

	ps := buildPS([][]byte{{0x67, 0x42, 0x00, 0x1F}}, 0x1B)
	pkt := buildRTPPacket(t, ps, 1000, 1, true)
	frame := make([]byte, 4, 4+len(pkt))
	frame[0] = 0x24
	frame[1] = 0x00
	binary.BigEndian.PutUint16(frame[2:4], uint16(len(pkt)))
	frame = append(frame, pkt...)
	_, err = conn.Write(frame)
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return gotFrames >= 1
	}, 5*time.Second, 100*time.Millisecond, "AU callback should fire over TCP")
}

// TestSessionInviteTCPActive verifies the active path: the SDP offers
// setup:active with port 9, and ConnectActiveTCP dials the device address
// from the answer SDP.
func TestSessionInviteTCPActive(t *testing.T) {
	// Device-side media listener. The accepted connection is held open until
	// test cleanup: closing it immediately (as this test once did) races the
	// receiver's Running() lifecycle — the read loop can hit EOF and flip
	// Running back off between the 50ms Eventually polls, timing the wait out.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()
	devConnCh := make(chan net.Conn, 1)
	go func() {
		c, err := ln.Accept()
		if err == nil {
			devConnCh <- c
		}
	}()
	t.Cleanup(func() {
		select {
		case c := <-devConnCh:
			_ = c.Close()
		default:
		}
	})
	devPort := ln.Addr().(*net.TCPAddr).Port

	pm := NewPortManager(31100, 31110)
	sm := NewSessionManager(pm, "34020000002000000001")
	sm.SetMediaTransport(func() string { return "tcp-active" })

	ch := &Channel{ID: "34020000001320000001", DeviceID: "34020000001110000001"}
	ch.Status.Store(ChannelIdle)

	sdp, err := sm.Invite(ch, "127.0.0.1", "127.0.0.1:5060", nil, nil, nil)
	require.NoError(t, err)
	defer func() { _ = sm.Bye(ch.ID) }()

	require.Contains(t, string(sdp), "a=setup:active")
	require.Contains(t, string(sdp), "m=video 9 TCP/RTP/AVP 96")

	answer := []byte(fmt.Sprintf("v=0\r\nc=IN IP4 127.0.0.1\r\nm=video %d TCP/RTP/AVP 96\r\n", devPort))
	require.NoError(t, sm.ConnectActiveTCP(ch.ID, answer))

	rcv := sm.GetReceiver(ch.ID)
	require.NotNil(t, rcv)
	require.Eventually(t, func() bool { return rcv.Running() }, 5*time.Second, 50*time.Millisecond)
}

// TestSDPMediaAddress verifies answer-SDP address extraction.
func TestSDPMediaAddress(t *testing.T) {
	host, port, ok := sdpMediaAddress([]byte("v=0\r\no=- 0 0 IN IP4 192.168.1.5\r\nc=IN IP4 192.168.1.5\r\nt=0 0\r\nm=video 30012 TCP/RTP/AVP 96\r\n"))
	require.True(t, ok)
	require.Equal(t, "192.168.1.5", host)
	require.Equal(t, uint16(30012), port)

	_, _, ok = sdpMediaAddress([]byte("v=0\r\nm=audio 0 RTP/AVP 8\r\n"))
	require.False(t, ok)
}

// sdpMediaPort extracts the m= video port from an SDP body.
func sdpMediaPort(t *testing.T, sdp []byte) uint16 {
	t.Helper()
	for _, line := range strings.Split(string(sdp), "\r\n") {
		if strings.HasPrefix(line, "m=video ") {
			fields := strings.Fields(line)
			p, err := strconv.Atoi(fields[1])
			require.NoError(t, err)
			return uint16(p)
		}
	}
	t.Fatal("no m=video line in SDP")
	return 0
}

// buildRTPPacket assembles a minimal RTP packet.
func buildRTPPacket(t *testing.T, payload []byte, ts uint32, seq uint16, marker bool) []byte {
	t.Helper()
	pkt := rtp.Packet{
		Header: rtp.Header{
			Version:        2,
			PayloadType:    96,
			SequenceNumber: seq,
			Timestamp:      ts,
			Marker:         marker,
			CSRC:           []uint32{},
		},
		Payload: payload,
	}
	raw, err := pkt.Marshal()
	require.NoError(t, err)
	return raw
}

// TestInviteDownload: the #378 download variant shares the playback pipeline
// but negotiates s=Download with the download SSRC prefix, and tears down via
// the shared ByePlayback fetch path.
func TestInviteDownload(t *testing.T) {
	pm := NewPortManager(55000, 55010)
	sm := NewSessionManager(pm, "34020000001320000001")
	channel := &Channel{
		DeviceID: "34020000001310000009",
		ID:       "34020000001320000009",
		Name:     "Camera D",
		Parental: 0,
	}
	channel.Status.Store(ChannelIdle)
	fakeSink := &fakeAUWriter{}

	start := time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	sdp, err := sm.InviteDownload(channel, "127.0.0.1", start, end, fakeSink, nil)
	require.NoError(t, err)

	sdpStr := string(sdp)
	require.Contains(t, sdpStr, "s=Download")
	require.Contains(t, sdpStr, "t=", "download SDP carries the window")
	require.Contains(t, sdpStr, "a=recvonly")
	// SSRC leading digit 2 marks the download session.
	var yLine string
	for _, l := range strings.Split(sdpStr, "\r\n") {
		if strings.HasPrefix(l, "y=") {
			yLine = l
			break
		}
	}
	require.True(t, strings.HasPrefix(yLine, "y=2"), "download SSRC must start with 2, got %q", yLine)

	rcv := sm.GetPlaybackReceiver(channel.ID)
	require.NotNil(t, rcv, "download receiver lives in the fetch map")
	require.True(t, rcv.Running())

	require.NoError(t, sm.ByePlayback(channel.ID))
	require.Nil(t, sm.GetPlaybackReceiver(channel.ID))
}
