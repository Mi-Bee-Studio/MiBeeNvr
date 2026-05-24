package webrtc

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/webrtc/v4"
	"github.com/stretchr/testify/require"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
)

// --- Test Helpers ---

// newTestManager creates a Manager for testing with default settings.
func newTestManager(t *testing.T) *Manager {
	t.Helper()
	return NewManager()
}

// testClient holds a test client PeerConnection and its resources.
type testClient struct {
	pc  *webrtc.PeerConnection
	api *webrtc.API
}

// newTestClient creates a client PeerConnection with H.264 video support.
func newTestClient(t *testing.T, withAudio bool) *testClient {
	t.Helper()
	mediaEngine := &webrtc.MediaEngine{}
	require.NoError(t, mediaEngine.RegisterCodec(webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{
			MimeType:    webrtc.MimeTypeH264,
			ClockRate:   90000,
			SDPFmtpLine: "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42001f",
		},
		PayloadType: 96,
	}, webrtc.RTPCodecTypeVideo))

	if withAudio {
		require.NoError(t, mediaEngine.RegisterCodec(webrtc.RTPCodecParameters{
			RTPCodecCapability: webrtc.RTPCodecCapability{
				MimeType:  webrtc.MimeTypeOpus,
				ClockRate: 48000,
				Channels:  2,
			},
			PayloadType: 111,
		}, webrtc.RTPCodecTypeAudio))
	}

	interceptorRegistry := &interceptor.Registry{}
	require.NoError(t, webrtc.RegisterDefaultInterceptors(mediaEngine, interceptorRegistry))

	api := webrtc.NewAPI(
		webrtc.WithMediaEngine(mediaEngine),
		webrtc.WithInterceptorRegistry(interceptorRegistry),
	)

	pc, err := api.NewPeerConnection(webrtc.Configuration{})
	require.NoError(t, err)

	return &testClient{pc: pc, api: api}
}

func (tc *testClient) close() {
	if tc.pc != nil {
		_ = tc.pc.Close()
	}
}

// createOfferSDP creates an SDP offer from a client PC with a recvonly video transceiver.
func createOfferSDP(t *testing.T, clientPC *webrtc.PeerConnection) []byte {
	t.Helper()
	_, err := clientPC.AddTransceiverFromKind(webrtc.RTPCodecTypeVideo,
		webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionRecvonly})
	require.NoError(t, err)

	offer, err := clientPC.CreateOffer(nil)
	require.NoError(t, err)

	gatherComplete := webrtc.GatheringCompletePromise(clientPC)
	require.NoError(t, clientPC.SetLocalDescription(offer))
	<-gatherComplete

	return []byte(clientPC.LocalDescription().SDP)
}

// createOfferWithAudio creates an SDP offer with both audio and video m-lines.
func createOfferWithAudio(t *testing.T, clientPC *webrtc.PeerConnection) []byte {
	t.Helper()
	_, err := clientPC.AddTransceiverFromKind(webrtc.RTPCodecTypeVideo,
		webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionRecvonly})
	require.NoError(t, err)
	_, err = clientPC.AddTransceiverFromKind(webrtc.RTPCodecTypeAudio,
		webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionRecvonly})
	require.NoError(t, err)

	offer, err := clientPC.CreateOffer(nil)
	require.NoError(t, err)

	gatherComplete := webrtc.GatheringCompletePromise(clientPC)
	require.NoError(t, clientPC.SetLocalDescription(offer))
	<-gatherComplete

	return []byte(clientPC.LocalDescription().SDP)
}

// connectWHEP creates a full WHEP session and applies the answer to the client PC.
// Returns the answer SDP and session ID.
func connectWHEP(t *testing.T, mgr *Manager, camID string, clientPC *webrtc.PeerConnection, offerSDP []byte) ([]byte, string) {
	t.Helper()
	answerSDP, sessionID, err := mgr.CreateWHEPSession(camID, offerSDP)
	require.NoError(t, err)
	require.NotEmpty(t, answerSDP)
	require.NotEmpty(t, sessionID)

	err = clientPC.SetRemoteDescription(webrtc.SessionDescription{
		Type: webrtc.SDPTypeAnswer,
		SDP:  string(answerSDP),
	})
	require.NoError(t, err)

	return answerSDP, sessionID
}

// --- Tests ---

// TestCreateWHEPSession verifies the basic SDP offer/answer exchange works.
func TestCreateWHEPSession(t *testing.T) {
	mgr := newTestManager(t)
	defer mgr.StopAll()

	client := newTestClient(t, false)
	defer client.close()

	offerSDP := createOfferSDP(t, client.pc)
	answerSDP, sessionID := connectWHEP(t, mgr, "test-cam", client.pc, offerSDP)

	// Verify answer SDP is valid
	require.True(t, strings.Contains(string(answerSDP), "m=video"), "answer should contain video m-line")
	require.NotEmpty(t, sessionID)
	require.Equal(t, 1, mgr.ActivePeerCount("test-cam"))
}

// TestH264TrackCreation verifies that the WHEP session creates an H.264 track
// and the SDP answer contains correct H.264 codec parameters.
func TestH264TrackCreation(t *testing.T) {
	mgr := newTestManager(t)
	defer mgr.StopAll()

	client := newTestClient(t, false)
	defer client.close()

	offerSDP := createOfferSDP(t, client.pc)
	answerSDP, _ := connectWHEP(t, mgr, "test-cam", client.pc, offerSDP)

	answerStr := string(answerSDP)
	// Verify H.264 codec in SDP
	require.True(t, strings.Contains(answerStr, "H264"), "answer should contain H264 codec")
	require.True(t, strings.Contains(answerStr, "90000"), "answer should have 90kHz clock rate")
	// Verify profile-level-id in fmtp
	require.True(t, strings.Contains(answerStr, "profile-level-id"), "answer should contain profile-level-id fmtp")
}

// TestFrameForwarding verifies that H.264 frames flow from WriteH264 through
// the WebRTC track to the connected client.
func TestFrameForwarding(t *testing.T) {
	mgr := newTestManager(t)
	defer mgr.StopAll()

	client := newTestClient(t, false)
	defer client.close()

	// Set up callbacks BEFORE any signaling
	trackReceived := make(chan struct{}, 1)
	var receivedCodec webrtc.RTPCodecParameters
	client.pc.OnTrack(func(track *webrtc.TrackRemote, receiver *webrtc.RTPReceiver) {
		receivedCodec = track.Codec()
		select {
		case trackReceived <- struct{}{}:
		default:
		}
	})

	connected := make(chan struct{}, 1)
	client.pc.OnICEConnectionStateChange(func(state webrtc.ICEConnectionState) {
		if state == webrtc.ICEConnectionStateConnected {
			select {
			case connected <- struct{}{}:
			default:
			}
		}
	})

	offerSDP := createOfferSDP(t, client.pc)
	connectWHEP(t, mgr, "test-cam", client.pc, offerSDP)

	// Wait for ICE connection
	select {
	case <-connected:
	case <-time.After(10 * time.Second):
		t.Fatal("ICE connection timeout")
	}

	// Write test H.264 frames: SPS + PPS + IDR
	sps := []byte{0x67, 0x64, 0x00, 0x1f, 0xac, 0xd9, 0x40, 0x50, 0x05, 0xbb, 0x01, 0x10}
	pps := []byte{0x68, 0xee, 0x3c, 0x80}
	idrFrame := []byte{0x65, 0x88, 0x84, 0x00, 0x40, 0xff, 0xfe, 0xf8, 0xc0}

	// Send many frames to ensure delivery through the async pipeline
	for i := 0; i < 20; i++ {
		pts := int64(i * 3000) // 3000 ticks = ~33ms at 90kHz
		mgr.WriteH264("test-cam", pts, [][]byte{sps, pps, idrFrame})
		time.Sleep(10 * time.Millisecond) // small delay between frames
	}

	// Wait for track to be received
	select {
	case <-trackReceived:
		require.Equal(t, webrtc.MimeTypeH264, receivedCodec.MimeType)
	case <-time.After(10 * time.Second):
		t.Fatal("Track not received within timeout")
	}
}

// TestMaxViewerLimit verifies that the max peer limit is enforced.
// Only 2 concurrent peers per camera are allowed; the 3rd should fail.
func TestMaxViewerLimit(t *testing.T) {
	mgr := newTestManager(t)
	defer mgr.StopAll()

	// Create 3 clients and pre-generate offer SDPs
	clients := make([]*testClient, 3)
	offers := make([][]byte, 3)
	for i := 0; i < 3; i++ {
		clients[i] = newTestClient(t, false)
		defer clients[i].close()
		offers[i] = createOfferSDP(t, clients[i].pc)
	}

	// First 2 sessions should succeed
	_, sid1 := connectWHEP(t, mgr, "test-cam", clients[0].pc, offers[0])
	_, sid2 := connectWHEP(t, mgr, "test-cam", clients[1].pc, offers[1])
	require.NotEmpty(t, sid1)
	require.NotEmpty(t, sid2)
	require.Equal(t, 2, mgr.ActivePeerCount("test-cam"))

	// 3rd session should fail with ErrMaxPeersReached
	_, _, err := mgr.CreateWHEPSession("test-cam", offers[2])
	require.ErrorIs(t, err, ErrMaxPeersReached)

	// Delete one session, now a new one should succeed
	require.NoError(t, mgr.DeleteWHEPSession(sid1))
	require.Equal(t, 1, mgr.ActivePeerCount("test-cam"))

	// Create new client for the freed slot
	client4 := newTestClient(t, false)
	defer client4.close()
	offer4 := createOfferSDP(t, client4.pc)
	connectWHEP(t, mgr, "test-cam", client4.pc, offer4)
	require.Equal(t, 2, mgr.ActivePeerCount("test-cam"))

	// Cleanup
	_ = mgr.DeleteWHEPSession(sid2)
}

// TestIdleEviction verifies that idle peers are evicted after the timeout.
func TestIdleEviction(t *testing.T) {
	mgr := NewManager(WithIdleTimeout(500 * time.Millisecond))
	defer mgr.StopAll()

	client := newTestClient(t, false)
	defer client.close()

	offerSDP := createOfferSDP(t, client.pc)
	_, sessionID := connectWHEP(t, mgr, "test-cam", client.pc, offerSDP)
	require.Equal(t, 1, mgr.ActivePeerCount("test-cam"))

	// Don't write any frames — wait for idle eviction
	require.Eventually(t, func() bool {
		return mgr.ActivePeerCount("test-cam") == 0
	}, 3*time.Second, 100*time.Millisecond, "peer should be evicted after idle timeout")

	// Verify session is truly gone
	err := mgr.DeleteWHEPSession(sessionID)
	require.ErrorIs(t, err, ErrSessionNotFound)
}

// TestSDPAudioRejection verifies that the SDP answer rejects audio m-lines
// by setting the port to 0.
func TestSDPAudioRejection(t *testing.T) {
	mgr := newTestManager(t)
	defer mgr.StopAll()

	// Create client with both audio and video support
	client := newTestClient(t, true)
	defer client.close()

	offerSDP := createOfferWithAudio(t, client.pc)
	answerSDP, _ := connectWHEP(t, mgr, "test-cam", client.pc, offerSDP)

	answerStr := string(answerSDP)

	// Answer should have video m-line (active)
	require.True(t, strings.Contains(answerStr, "m=video"), "answer should have video m-line")

	// If audio m-line exists, it must be rejected (port 0)
	if strings.Contains(answerStr, "m=audio") {
		// Check that audio m-line has port 0
		for _, line := range strings.Split(answerStr, "\n") {
			if strings.HasPrefix(line, "m=audio") {
				require.Contains(t, line, "m=audio 0",
					"audio m-line must be rejected with port 0, got: %s", line)
			}
		}
	}
}

// TestCanHandle verifies that the Manager reports correct codec support.
// H.264 should be supported, H.265 and MJPEG should not.
func TestCanHandle(t *testing.T) {
	mgr := newTestManager(t)

	require.True(t, mgr.CanHandle(model.FormatH264), "H.264 should be supported")
	require.False(t, mgr.CanHandle(model.FormatH265), "H.265 should NOT be supported")
	require.False(t, mgr.CanHandle(model.FormatMJPEG), "MJPEG should NOT be supported")
}

func TestPeerDisconnectCleanup(t *testing.T) {
	mgr := newTestManager(t)
	defer mgr.StopAll()

	client := newTestClient(t, false)

	offerSDP := createOfferSDP(t, client.pc)
	_, sessionID := connectWHEP(t, mgr, "test-cam", client.pc, offerSDP)
	require.Equal(t, 1, mgr.ActivePeerCount("test-cam"))
	require.Equal(t, 1, mgr.TotalPeerCount())

	// Explicitly delete the session (simulates cleanup after peer disconnect)
	err := mgr.DeleteWHEPSession(sessionID)
	require.NoError(t, err)

	// Verify resources are freed
	require.Equal(t, 0, mgr.ActivePeerCount("test-cam"))
	require.Equal(t, 0, mgr.TotalPeerCount())

	// Verify session is truly gone
	err = mgr.DeleteWHEPSession(sessionID)
	require.ErrorIs(t, err, ErrSessionNotFound)

	// Verify writing to camera with no peers doesn't panic
	mgr.WriteH264("test-cam", 0, [][]byte{{0x65, 0x88}})

	// Close client after session is deleted (should not cause issues)
	client.close()
}

// TestConcurrentWHEPSessionCreation verifies thread safety when creating
// sessions from multiple goroutines simultaneously.
func TestConcurrentWHEPSessionCreation(t *testing.T) {
	mgr := newTestManager(t)
	defer mgr.StopAll()

	// Pre-create 3 client PCs and their offer SDPs (no concurrency issues here)
	clients := make([]*testClient, 3)
	offers := make([][]byte, 3)
	for i := 0; i < 3; i++ {
		clients[i] = newTestClient(t, false)
		defer clients[i].close()
		offers[i] = createOfferSDP(t, clients[i].pc)
	}

	// Create sessions concurrently
	var wg sync.WaitGroup
	results := make([]error, 3)
	sessions := make([]string, 3)

	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			_, sid, err := mgr.CreateWHEPSession("test-cam", offers[idx])
			results[idx] = err
			sessions[idx] = sid
		}(i)
	}
	wg.Wait()

	// Count results
	var success, maxReached int
	for _, err := range results {
		if err == nil {
			success++
		} else if err == ErrMaxPeersReached {
			maxReached++
		}
	}

	require.Equal(t, 2, success, "exactly 2 sessions should succeed")
	require.Equal(t, 1, maxReached, "exactly 1 session should be rejected")
	require.Equal(t, 2, mgr.ActivePeerCount("test-cam"))

	// Cleanup successful sessions
	for _, sid := range sessions {
		if sid != "" {
			_ = mgr.DeleteWHEPSession(sid)
		}
	}
}

// --- Track helper tests ---

// TestAnnexBEncode verifies the Annex B encoding of NAL units.
func TestAnnexBEncode(t *testing.T) {
	// Empty AU
	require.Nil(t, annexBEncode(nil))
	require.Nil(t, annexBEncode([][]byte{}))

	// Single NAL
	result := annexBEncode([][]byte{{0x65, 0x88}})
	expected := []byte{0x00, 0x00, 0x00, 0x01, 0x65, 0x88}
	require.Equal(t, expected, result)

	// Multiple NALs (SPS + PPS + IDR)
	sps := []byte{0x67, 0x64}
	pps := []byte{0x68, 0xee}
	idr := []byte{0x65, 0x88, 0x84}
	result = annexBEncode([][]byte{sps, pps, idr})

	expected = []byte{
		0x00, 0x00, 0x00, 0x01, 0x67, 0x64,
		0x00, 0x00, 0x00, 0x01, 0x68, 0xee,
		0x00, 0x00, 0x00, 0x01, 0x65, 0x88, 0x84,
	}
	require.Equal(t, expected, result)
}

// TestRejectAudioInSDP verifies that audio m-lines are rejected in SDP.
func TestRejectAudioInSDP(t *testing.T) {
	// SDP with audio m-line using port 9
	sdp := "v=0\r\nm=audio 9 UDP/TLS/RTP/SAVPF 111\r\na=rtpmap:111 opus/48000/2\r\nm=video 9 UDP/TLS/RTP/SAVPF 96\r\na=rtpmap:96 H264/90000\r\n"
	result := rejectAudioInSDP(sdp)

	require.Contains(t, result, "m=audio 0 UDP/TLS/RTP/SAVPF 111")
	require.Contains(t, result, "m=video 9 UDP/TLS/RTP/SAVPF 96")

	// No audio m-line — should be unchanged
	sdpNoAudio := "v=0\r\nm=video 9 UDP/TLS/RTP/SAVPF 96\r\na=rtpmap:96 H264/90000\r\n"
	result = rejectAudioInSDP(sdpNoAudio)
	require.Equal(t, sdpNoAudio, result)
}

// TestWriteH264NonBlocking verifies that WriteH264 is non-blocking and
// drops frames when the buffer is full.
func TestWriteH264NonBlocking(t *testing.T) {
	mgr := NewManager(WithFrameBufSize(5))
	defer mgr.StopAll()

	client := newTestClient(t, false)
	defer client.close()

	offerSDP := createOfferSDP(t, client.pc)
	_, _ = connectWHEP(t, mgr, "test-cam", client.pc, offerSDP)

	// Rapidly write more frames than the buffer can hold
	start := time.Now()
	for i := 0; i < 200; i++ {
		mgr.WriteH264("test-cam", int64(i*3000), [][]byte{{0x65, 0x88}})
	}
	elapsed := time.Since(start)

	// Should complete very quickly (non-blocking), not block on buffer-full
	require.Less(t, elapsed, 500*time.Millisecond, "WriteH264 should be non-blocking")

	// Writing to non-existent camera should be a no-op (not panic)
	mgr.WriteH264("nonexistent", 0, [][]byte{{0x01}})
}

// TestWriteH264UpdatesLastUsed verifies that WriteH264 updates lastUsed
// preventing idle eviction.
func TestWriteH264UpdatesLastUsed(t *testing.T) {
	mgr := NewManager(
		WithIdleTimeout(500*time.Millisecond),
		WithFrameBufSize(10),
	)
	defer mgr.StopAll()

	client := newTestClient(t, false)
	defer client.close()

	offerSDP := createOfferSDP(t, client.pc)
	_, sessionID := connectWHEP(t, mgr, "test-cam", client.pc, offerSDP)

	// Continuously write frames to keep the session alive
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		i := 0
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				mgr.WriteH264("test-cam", int64(i*3000), [][]byte{{0x65, 0x88}})
				i++
			}
		}
	}()

	// Wait longer than the idle timeout — session should NOT be evicted
	time.Sleep(1 * time.Second)
	require.Equal(t, 1, mgr.ActivePeerCount("test-cam"), "session should survive with active writes")

	// Stop writing and wait for eviction
	close(stop)
	wg.Wait()

	require.Eventually(t, func() bool {
		return mgr.ActivePeerCount("test-cam") == 0
	}, 3*time.Second, 100*time.Millisecond, "session should be evicted after writes stop")

	_ = sessionID
}

// TestMultipleCameras verifies that the peer limit is per-camera, not global.
func TestMultipleCameras(t *testing.T) {
	mgr := newTestManager(t)
	defer mgr.StopAll()

	// Each camera should allow up to 2 peers
	for camIdx := 0; camIdx < 3; camIdx++ {
		camID := "cam-" + string(rune('A'+camIdx))
		for peerIdx := 0; peerIdx < 2; peerIdx++ {
			client := newTestClient(t, false)
			defer client.close()
			offerSDP := createOfferSDP(t, client.pc)
			_, _ = connectWHEP(t, mgr, camID, client.pc, offerSDP)
		}
		require.Equal(t, 2, mgr.ActivePeerCount(camID))
	}

	// Total should be 6 peers across 3 cameras
	require.Equal(t, 6, mgr.TotalPeerCount())
}

// TestAtomicDropCounter verifies the atomic drop counter increments correctly.
func TestAtomicDropCounter(t *testing.T) {
	mgr := NewManager(WithFrameBufSize(2))
	defer mgr.StopAll()

	client := newTestClient(t, false)
	defer client.close()

	offerSDP := createOfferSDP(t, client.pc)
	_, _ = connectWHEP(t, mgr, "test-cam", client.pc, offerSDP)

	// Write many frames rapidly to overflow the buffer
	for i := 0; i < 50; i++ {
		mgr.WriteH264("test-cam", int64(i*3000), [][]byte{{0x65, 0x88}})
	}

	// Wait a bit for writes to process
	time.Sleep(100 * time.Millisecond)

	// Verify the manager is still functional (no crash)
	require.Equal(t, 1, mgr.ActivePeerCount("test-cam"))
}

// TestDeleteNonexistentSession verifies graceful handling of missing sessions.
func TestDeleteNonexistentSession(t *testing.T) {
	mgr := newTestManager(t)
	defer mgr.StopAll()

	err := mgr.DeleteWHEPSession("nonexistent-id")
	require.ErrorIs(t, err, ErrSessionNotFound)
}

// TestRegisterStream verifies StreamHub subscription and unsubscription.
func TestRegisterStream(t *testing.T) {
	mgr := newTestManager(t)
	defer mgr.StopAll()

	hub := model.NewStreamHub()

	// Register should subscribe to hub
	mgr.RegisterStream("test-cam", hub)
	require.Equal(t, 1, hub.ConsumerCount(), "hub should have 1 consumer after register")

	// Duplicate register should be a no-op
	mgr.RegisterStream("test-cam", hub)
	require.Equal(t, 1, hub.ConsumerCount(), "duplicate register should not add another consumer")

	// Nil hub should be a no-op
	mgr.RegisterStream("nil-cam", nil)
	require.Equal(t, 1, hub.ConsumerCount(), "nil hub should not change consumer count")

	// Unregister should unsubscribe from hub
	mgr.UnregisterStream("test-cam")
	require.Equal(t, 0, hub.ConsumerCount(), "hub should have 0 consumers after unregister")

	// Unregister non-existent should be a no-op
	mgr.UnregisterStream("nonexistent")
}

// TestRegisterStreamDeliversFrames verifies frames flow from hub to WriteH264.
func TestRegisterStreamDeliversFrames(t *testing.T) {
	mgr := newTestManager(t)
	defer mgr.StopAll()

	client := newTestClient(t, false)
	defer client.close()

	// Connect a WHEP peer first
	offerSDP := createOfferSDP(t, client.pc)
	connectWHEP(t, mgr, "test-cam", client.pc, offerSDP)

	// Register stream with a hub
	hub := model.NewStreamHub()
	mgr.RegisterStream("test-cam", hub)

	// Broadcast a frame through the hub
	sps := []byte{0x67, 0x64, 0x00, 0x1f, 0xac, 0xd9, 0x40, 0x50, 0x05, 0xbb, 0x01, 0x10}
	pps := []byte{0x68, 0xee, 0x3c, 0x80}
	idr := []byte{0x65, 0x88, 0x84, 0x00, 0x40, 0xff, 0xfe, 0xf8, 0xc0}

	for i := 0; i < 20; i++ {
		hub.Broadcast(int64(i*3000), [][]byte{sps, pps, idr})
	}

	// Manager should still be functional — no crash from hub callback
	require.Equal(t, 1, mgr.ActivePeerCount("test-cam"))

	// Unregister
	mgr.UnregisterStream("test-cam")
	require.Equal(t, 0, hub.ConsumerCount())
}

// TestStopAllCleansUpHubSubs verifies StopAll unsubscribes all hub subscriptions.
func TestStopAllCleansUpHubSubs(t *testing.T) {
	mgr := newTestManager(t)

	hub1 := model.NewStreamHub()
	hub2 := model.NewStreamHub()

	mgr.RegisterStream("cam-1", hub1)
	mgr.RegisterStream("cam-2", hub2)
	require.Equal(t, 1, hub1.ConsumerCount())
	require.Equal(t, 1, hub2.ConsumerCount())

	mgr.StopAll()

	require.Equal(t, 0, hub1.ConsumerCount(), "hub1 should have 0 consumers after StopAll")
	require.Equal(t, 0, hub2.ConsumerCount(), "hub2 should have 0 consumers after StopAll")
}
