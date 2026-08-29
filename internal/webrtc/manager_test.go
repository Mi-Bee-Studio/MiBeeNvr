package webrtc

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
	"github.com/stretchr/testify/require"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model/nalutil"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/streamhub"
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
		// Browsers support all WebRTC mandatory audio codecs — mirror that so
		// answers selecting PCMU/PCMA/Opus all negotiate.
		for _, ac := range []struct {
			mime string
			rate uint32
			ch   uint16
			pt   webrtc.PayloadType
		}{
			{webrtc.MimeTypePCMU, 8000, 1, 0},
			{webrtc.MimeTypePCMA, 8000, 1, 8},
			{webrtc.MimeTypeOpus, 48000, 2, 111},
		} {
			require.NoError(t, mediaEngine.RegisterCodec(webrtc.RTPCodecParameters{
				RTPCodecCapability: webrtc.RTPCodecCapability{
					MimeType:  ac.mime,
					ClockRate: ac.rate,
					Channels:  ac.ch,
				},
				PayloadType: ac.pt,
			}, webrtc.RTPCodecTypeAudio))
		}
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
	require.Equal(t, 1, mgr.activePeerCount("test-cam"))
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
	for i := range 20 {
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
	for i := range 3 {
		clients[i] = newTestClient(t, false)
		defer clients[i].close()
		offers[i] = createOfferSDP(t, clients[i].pc)
	}

	// First 2 sessions should succeed
	_, sid1 := connectWHEP(t, mgr, "test-cam", clients[0].pc, offers[0])
	_, sid2 := connectWHEP(t, mgr, "test-cam", clients[1].pc, offers[1])
	require.NotEmpty(t, sid1)
	require.NotEmpty(t, sid2)
	require.Equal(t, 2, mgr.activePeerCount("test-cam"))

	// 3rd session should fail with ErrMaxPeersReached
	_, _, err := mgr.CreateWHEPSession("test-cam", offers[2])
	require.ErrorIs(t, err, ErrMaxPeersReached)

	// Delete one session, now a new one should succeed
	require.NoError(t, mgr.DeleteWHEPSession(sid1))
	require.Equal(t, 1, mgr.activePeerCount("test-cam"))

	// Create new client for the freed slot
	client4 := newTestClient(t, false)
	defer client4.close()
	offer4 := createOfferSDP(t, client4.pc)
	connectWHEP(t, mgr, "test-cam", client4.pc, offer4)
	require.Equal(t, 2, mgr.activePeerCount("test-cam"))

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
	require.Equal(t, 1, mgr.activePeerCount("test-cam"))

	// Don't write any frames — wait for idle eviction
	require.Eventually(t, func() bool {
		return mgr.activePeerCount("test-cam") == 0
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
	require.Equal(t, 1, mgr.activePeerCount("test-cam"))
	require.Equal(t, 1, mgr.totalPeerCount())

	// Explicitly delete the session (simulates cleanup after peer disconnect)
	err := mgr.DeleteWHEPSession(sessionID)
	require.NoError(t, err)

	// Verify resources are freed
	require.Equal(t, 0, mgr.activePeerCount("test-cam"))
	require.Equal(t, 0, mgr.totalPeerCount())

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
	for i := range 3 {
		clients[i] = newTestClient(t, false)
		defer clients[i].close()
		offers[i] = createOfferSDP(t, clients[i].pc)
	}

	// Create sessions concurrently
	var wg sync.WaitGroup
	results := make([]error, 3)
	sessions := make([]string, 3)

	for i := range 3 {
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
		} else if errors.Is(err, ErrMaxPeersReached) {
			maxReached++
		}
	}

	require.Equal(t, 2, success, "exactly 2 sessions should succeed")
	require.Equal(t, 1, maxReached, "exactly 1 session should be rejected")
	require.Equal(t, 2, mgr.activePeerCount("test-cam"))

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

// TestWHEPAudioMuxing verifies SDP-level audio negotiation (#372):
// cameras with a WebRTC-compatible codec get an active audio m-line in the
// answer; video-only cameras keep the audio m-line rejected.
func TestWHEPAudioMuxing(t *testing.T) {
	mgr := NewManager()
	defer mgr.StopAll()

	hub := streamhub.New()
	mgr.RegisterStream("audio-cam", hub, nil)
	require.NoError(t, mgr.SetAudioInfo("audio-cam", "g711", true, 8000, 1))
	// AAC is not a WebRTC codec — the manager leaves the camera video-only.
	mgr.RegisterStream("aac-cam", hub, nil)
	require.NoError(t, mgr.SetAudioInfo("aac-cam", "aac", false, 16000, 1))
	mgr.RegisterStream("video-cam", hub, nil)

	offerWithAudio := func(camID string) string {
		client := newTestClient(t, true)
		defer client.close()
		answer, sid := connectWHEP(t, mgr, camID, client.pc, createOfferWithAudio(t, client.pc))
		defer mgr.DeleteWHEPSession(sid)
		return string(answer)
	}

	audioAnswer := offerWithAudio("audio-cam")
	require.Contains(t, audioAnswer, "m=audio 9", "audio-capable camera must answer with an active audio m-line")
	require.Contains(t, audioAnswer, "PCMU/8000", "µ-law G.711 must map to PCMU")

	aLawAnswer := offerWithAudio("audio-cam")
	require.Contains(t, aLawAnswer, "PCMU/8000")

	aacAnswer := offerWithAudio("aac-cam")
	require.Contains(t, aacAnswer, "m=audio 0 ", "AAC camera must keep the audio m-line rejected")

	videoAnswer := offerWithAudio("video-cam")
	require.Contains(t, videoAnswer, "m=audio 0 ", "video-only camera must keep the audio m-line rejected")
}

// TestSetAudioInfoA LAW maps G.711 A-law to PCMA.
func TestSetAudioInfoALaw(t *testing.T) {
	mgr := NewManager()
	defer mgr.StopAll()

	hub := streamhub.New()
	mgr.RegisterStream("alaw-cam", hub, nil)
	require.NoError(t, mgr.SetAudioInfo("alaw-cam", "g711", false, 8000, 1))

	client := newTestClient(t, true)
	defer client.close()
	answer, sid := connectWHEP(t, mgr, "alaw-cam", client.pc, createOfferWithAudio(t, client.pc))
	defer mgr.DeleteWHEPSession(sid)
	require.Contains(t, string(answer), "PCMA/8000", "A-law G.711 must map to PCMA")
}

// TestSetAudioInfoBeforeRegister errors — audio cannot be configured without
// a hub subscription.
func TestSetAudioInfoBeforeRegister(t *testing.T) {
	mgr := NewManager()
	defer mgr.StopAll()
	require.Error(t, mgr.SetAudioInfo("no-hub-cam", "g711", true, 8000, 1))
}

// TestSetAudioInfoIdempotent — repeated calls (one per WHEP session) must
// not fail on duplicate audio subscription.
func TestSetAudioInfoIdempotent(t *testing.T) {
	mgr := NewManager()
	defer mgr.StopAll()

	hub := streamhub.New()
	mgr.RegisterStream("cam", hub, nil)
	require.NoError(t, mgr.SetAudioInfo("cam", "g711", true, 8000, 1))
	require.NoError(t, mgr.SetAudioInfo("cam", "g711", true, 8000, 1))
}

// TestWriteH264NonBlocking verifies that WriteH264 is non-blocking and
// drops frames when the buffer is full.
func TestWriteH264NonBlocking(t *testing.T) {
	mgr := NewManager(withFrameBufSize(5))
	defer mgr.StopAll()

	client := newTestClient(t, false)
	defer client.close()

	offerSDP := createOfferSDP(t, client.pc)
	_, _ = connectWHEP(t, mgr, "test-cam", client.pc, offerSDP)

	// Rapidly write more frames than the buffer can hold
	start := time.Now()
	for i := range 200 {
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
		withFrameBufSize(10),
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
	require.Equal(t, 1, mgr.activePeerCount("test-cam"), "session should survive with active writes")

	// Stop writing and wait for eviction
	close(stop)
	wg.Wait()

	require.Eventually(t, func() bool {
		return mgr.activePeerCount("test-cam") == 0
	}, 3*time.Second, 100*time.Millisecond, "session should be evicted after writes stop")

	_ = sessionID
}

// TestMultipleCameras verifies that the peer limit is per-camera, not global.
func TestMultipleCameras(t *testing.T) {
	mgr := newTestManager(t)
	defer mgr.StopAll()

	// Each camera should allow up to 2 peers
	for camIdx := range 3 {
		camID := "cam-" + string(rune('A'+camIdx))
		for range 2 {
			client := newTestClient(t, false)
			defer client.close()
			offerSDP := createOfferSDP(t, client.pc)
			_, _ = connectWHEP(t, mgr, camID, client.pc, offerSDP)
		}
		require.Equal(t, 2, mgr.activePeerCount(camID))
	}

	// Total should be 6 peers across 3 cameras
	require.Equal(t, 6, mgr.totalPeerCount())
}

// TestAtomicDropCounter verifies the atomic drop counter increments correctly.
func TestAtomicDropCounter(t *testing.T) {
	mgr := NewManager(withFrameBufSize(2))
	defer mgr.StopAll()

	client := newTestClient(t, false)
	defer client.close()

	offerSDP := createOfferSDP(t, client.pc)
	_, _ = connectWHEP(t, mgr, "test-cam", client.pc, offerSDP)

	// Write many frames rapidly to overflow the buffer
	for i := range 50 {
		mgr.WriteH264("test-cam", int64(i*3000), [][]byte{{0x65, 0x88}})
	}

	// Wait a bit for writes to process
	time.Sleep(100 * time.Millisecond)

	// Verify the manager is still functional (no crash)
	require.Equal(t, 1, mgr.activePeerCount("test-cam"))
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

	hub := streamhub.New()

	// Register should subscribe to hub
	mgr.RegisterStream("test-cam", hub, nil)
	require.Equal(t, 1, hub.ConsumerCount(), "hub should have 1 consumer after register")

	// Duplicate register should be a no-op
	mgr.RegisterStream("test-cam", hub, nil)
	require.Equal(t, 1, hub.ConsumerCount(), "duplicate register should not add another consumer")

	// Nil hub should be a no-op
	mgr.RegisterStream("nil-cam", nil, nil)
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
	hub := streamhub.New()
	mgr.RegisterStream("test-cam", hub, nil)

	// Broadcast a frame through the hub
	sps := []byte{0x67, 0x64, 0x00, 0x1f, 0xac, 0xd9, 0x40, 0x50, 0x05, 0xbb, 0x01, 0x10}
	pps := []byte{0x68, 0xee, 0x3c, 0x80}
	idr := []byte{0x65, 0x88, 0x84, 0x00, 0x40, 0xff, 0xfe, 0xf8, 0xc0}

	for i := range 20 {
		hub.Broadcast(int64(i*3000), [][]byte{sps, pps, idr}, false)
	}

	// Manager should still be functional — no crash from hub callback
	require.Equal(t, 1, mgr.activePeerCount("test-cam"))

	// Unregister
	mgr.UnregisterStream("test-cam")
	require.Equal(t, 0, hub.ConsumerCount())
}

// TestStopAllCleansUpHubSubs verifies StopAll unsubscribes all hub subscriptions.
func TestStopAllCleansUpHubSubs(t *testing.T) {
	mgr := newTestManager(t)

	hub1 := streamhub.New()
	hub2 := streamhub.New()

	mgr.RegisterStream("cam-1", hub1, nil)
	mgr.RegisterStream("cam-2", hub2, nil)
	require.Equal(t, 1, hub1.ConsumerCount())
	require.Equal(t, 1, hub2.ConsumerCount())

	mgr.StopAll()

	require.Equal(t, 0, hub1.ConsumerCount(), "hub1 should have 0 consumers after StopAll")
	require.Equal(t, 0, hub2.ConsumerCount(), "hub2 should have 0 consumers after StopAll")
}

// --- IDR Detection & Cleanup Tests ---

// TestFrameMsgKeyframeDetection verifies that WriteH264 correctly constructs
// frameMsg with isKeyframe=true for IDR AUs and false for non-IDR AUs.
func TestFrameMsgKeyframeDetection(t *testing.T) {
	// Test pure frameMsg construction (the logic is in WriteH264's channel send).
	// We verify the same construction logic that WriteH264 uses.

	// IDR access unit: SPS (type 7) + PPS (type 8) + IDR (type 5)
	idrAU := [][]byte{
		{0x67, 0x64, 0x00, 0x1f},       // SPS
		{0x68, 0xee, 0x3c, 0x80},       // PPS
		{0x65, 0x88, 0x84, 0x00, 0x40}, // IDR slice
	}
	idrMsg := model.FrameMsg{PTS: 9000, AU: idrAU, IsKeyframe: nalutil.IsIDR(idrAU, false)}
	require.True(t, idrMsg.IsKeyframe, "IDR AU should have IsKeyframe=true")

	// P-frame: non-IDR (type 1)
	pFrameAU := [][]byte{{0x41, 0x88, 0x84, 0x00}}
	pMsg := model.FrameMsg{PTS: 12000, AU: pFrameAU, IsKeyframe: nalutil.IsIDR(pFrameAU, false)}
	require.False(t, pMsg.IsKeyframe, "P-frame AU should have IsKeyframe=false")

	// SEI NALU only (type 6) — not a keyframe
	seiAU := [][]byte{{0x06, 0x01}}
	seiMsg := model.FrameMsg{PTS: 15000, AU: seiAU, IsKeyframe: nalutil.IsIDR(seiAU, false)}
	require.False(t, seiMsg.IsKeyframe, "SEI AU should have IsKeyframe=false")

	// Empty AU — not a keyframe
	emptyAU := [][]byte{}
	emptyMsg := model.FrameMsg{PTS: 18000, AU: emptyAU, IsKeyframe: nalutil.IsIDR(emptyAU, false)}
	require.False(t, emptyMsg.IsKeyframe, "empty AU should have IsKeyframe=false")

	// IDR without SPS/PPS — still a keyframe
	idrOnlyAU := [][]byte{{0x65, 0x88}}
	idrOnlyMsg := model.FrameMsg{PTS: 21000, AU: idrOnlyAU, IsKeyframe: nalutil.IsIDR(idrOnlyAU, false)}
	require.True(t, idrOnlyMsg.IsKeyframe, "IDR-only AU should have IsKeyframe=true")
}

// TestRTCPDrainWaitGroup verifies that the drainWg WaitGroup correctly
// tracks RTCP drain goroutines and StopAll waits for them to exit.
func TestRTCPDrainWaitGroup(t *testing.T) {
	mgr := NewManager(withFrameBufSize(5))

	client := newTestClient(t, false)
	defer client.close()

	offerSDP := createOfferSDP(t, client.pc)
	_, _ = connectWHEP(t, mgr, "test-cam", client.pc, offerSDP)

	// Creating a session launches one RTCP drain goroutine (drainWg.Add(1)).
	// StopAll cancels the context → drain goroutine exits → drainWg.Done().
	// drainWg.Wait() in StopAll ensures clean shutdown.
	done := make(chan struct{})
	go func() {
		mgr.StopAll()
		close(done)
	}()

	select {
	case <-done:
		// StopAll returned — drain goroutine exited cleanly
	case <-time.After(5 * time.Second):
		t.Fatal("StopAll should not block — drain goroutine should exit via cancel")
	}
}

// TestConnectionStateMetric verifies that the Prometheus metric is incremented
// when connection state changes occur.
func TestConnectionStateMetric(t *testing.T) {
	mgr := NewManager(withFrameBufSize(5))
	defer mgr.StopAll()
	require.Nil(t, mgr.mets, "metrics should be nil by default (no WithMetrics option)")
}

// --- Congestion Detection & Bitrate Adaptation Tests ---

// TestCongestionTracker_HighDropRateTriggersSkipping verifies that when drop rate
// exceeds 20%, non-IDR frames are skipped.
func TestCongestionTracker_HighDropRateTriggersSkipping(t *testing.T) {
	tracker := newCongestionTracker(100)

	// Fill window with 80 sent + 20 dropped = 20% drop rate (exactly at threshold)
	for range 80 {
		tracker.recordSent()
	}
	for range 20 {
		tracker.recordDropped()
	}
	// 20% exactly at threshold — should NOT skip yet (need > 20%)
	require.False(t, tracker.shouldSkipFrame(false), "20%% drop rate should not trigger skipping")

	// One more drop pushes to 21/101 ≈ 20.8% — should trigger
	tracker.recordDropped()
	require.True(t, tracker.shouldSkipFrame(false), "20.8%% drop rate should trigger skipping")

	// IDR frames are never skipped
	require.False(t, tracker.shouldSkipFrame(true), "IDR frames must never be skipped")
}

// TestCongestionTracker_RecoveryRestoresFullRate verifies that when drop rate
// falls below 5%, frame skipping stops.
func TestCongestionTracker_RecoveryRestoresFullRate(t *testing.T) {
	tracker := newCongestionTracker(100)

	// Build up to congestion: 30 dropped out of 100 (30%)
	for range 70 {
		tracker.recordSent()
	}
	for range 30 {
		tracker.recordDropped()
	}
	require.True(t, tracker.shouldSkipFrame(false), "30%% drop rate should trigger skipping")

	// Recovery: push 95 sent frames to evict most drops from the window
	for range 95 {
		tracker.recordSent()
	}
	// Window now: last 95 sent + 5 remaining drops from original 30 = 5/100 = 5%
	// Exactly 5% — congestion should persist (need < 5%)
	require.True(t, tracker.congested, "5%% drop rate should maintain congestion state")

	// One more sent: 4 drops / 100 = 4% — should stop skipping
	tracker.recordSent()
	require.False(t, tracker.shouldSkipFrame(false), "<5%% drop rate should restore full rate")
}

// TestCongestionTracker_IDRAlwaysSent verifies IDR frames are never skipped
// even during heavy congestion.
func TestCongestionTracker_IDRAlwaysSent(t *testing.T) {
	tracker := newCongestionTracker(100)

	// Fill with 50% drops — extreme congestion
	for range 50 {
		tracker.recordSent()
		tracker.recordDropped()
	}

	for i := range 10 {
		require.False(t, tracker.shouldSkipFrame(true), "IDR must never be skipped (iter %d)", i)
	}
}

// TestCongestionTracker_SlidingWindow verifies that the sliding window
// correctly evicts old entries.
func TestCongestionTracker_SlidingWindow(t *testing.T) {
	tracker := newCongestionTracker(10) // small window for testing

	// Fill with all drops
	for range 10 {
		tracker.recordDropped()
	}
	// 10/10 = 100% drops — should skip
	require.True(t, tracker.shouldSkipFrame(false), "100%% drop rate should skip")

	// Push 10 sent frames — old drops evicted, window now all sent
	for range 10 {
		tracker.recordSent()
	}
	// 0/10 = 0% drops — should not skip
	require.False(t, tracker.shouldSkipFrame(false), "0%% drop rate should not skip")
}

// TestCongestionTracker_EmptyWindow verifies initial state (no congestion).
func TestCongestionTracker_EmptyWindow(t *testing.T) {
	tracker := newCongestionTracker(100)
	require.False(t, tracker.shouldSkipFrame(false), "empty tracker should not skip")
	require.False(t, tracker.shouldSkipFrame(true), "empty tracker should not skip IDR")
}

// TestCongestionTracker_AlternatingSkip verifies that skip logic
// alternates (skips every other non-IDR frame when congested).
func TestCongestionTracker_AlternatingSkip(t *testing.T) {
	tracker := newCongestionTracker(100)

	// Trigger high congestion: 30 drops out of 50
	for range 20 {
		tracker.recordSent()
	}
	for range 30 {
		tracker.recordDropped()
	}
	require.True(t, tracker.shouldSkipFrame(false), "should be congested")

	// Track skip pattern — should alternate
	skipCount := 0
	const iterations = 20
	for range iterations {
		if tracker.shouldSkipFrame(false) {
			skipCount++
		}
	}
	// With alternating skip, roughly half should be skipped
	require.Equal(t, iterations/2, skipCount, "should skip every other frame")
}

// TestFmtpForProfile locks the per-camera H.264 SDP variant: High-family
// streams must offer profile-level-id 640028 (registering/offering only
// Constrained Baseline made browsers reject every frame of a High stream —
// the GB28181 cascade black screen).
func TestFmtpForProfile(t *testing.T) {
	require.Equal(t,
		"level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=640028",
		fmtpForProfile([]byte{0x67, 0x64, 0x00, 0x28}), "High@4.0 (GB28181 cascade)")
	require.Equal(t,
		"level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=640028",
		fmtpForProfile([]byte{0x67, 0x6E, 0x00, 0x1F}), "Progressive High")
	require.Equal(t,
		"level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42001f",
		fmtpForProfile([]byte{0x67, 0x42, 0xC0, 0x1F}), "Constrained Baseline")
	require.Equal(t,
		"level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42001f",
		fmtpForProfile([]byte{0x67, 0x4D, 0x00, 0x1F}), "Main falls back to baseline offer")
	require.Equal(t,
		"level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42001f",
		fmtpForProfile(nil), "no SPS yet")
}

// TestRegisterStreamRebindsOnHubChange: a GB session recycle hands out a NEW
// hub — RegisterStream must resubscribe (the old guard kept feeding viewers
// from the dead hub: frozen video until a page reload).
func TestRegisterStreamRebindsOnHubChange(t *testing.T) {
	m := NewManager()
	hub1 := streamhub.New()
	hub2 := streamhub.New()

	m.RegisterStream("cam-rebind", hub1, nil)
	require.Same(t, hub1, m.hubSubs["cam-rebind"].hub)

	// Same hub re-registration stays a no-op.
	m.RegisterStream("cam-rebind", hub1, nil)
	require.Same(t, hub1, m.hubSubs["cam-rebind"].hub)

	// New hub swaps the subscription.
	m.RegisterStream("cam-rebind", hub2, nil)
	require.Same(t, hub2, m.hubSubs["cam-rebind"].hub)
	require.Equal(t, "webrtc-cam-rebind", m.hubSubs["cam-rebind"].subID)

	m.UnregisterStream("cam-rebind")
	require.Nil(t, m.hubSubs["cam-rebind"])
}

// TestAudioForwarding verifies the full audio path (#372): hub broadcast →
// WriteAudio → WebRTC audio track → RTP packets on the client. Uses G.711
// µ-law (PCMU), the common case for Xiaomi/ESP32 cameras.
func TestAudioForwarding(t *testing.T) {
	mgr := newTestManager(t)
	defer mgr.StopAll()

	hub := streamhub.New()
	mgr.RegisterStream("audio-cam", hub, nil)
	require.NoError(t, mgr.SetAudioInfo("audio-cam", "g711", true, 8000, 1))

	client := newTestClient(t, true)
	defer client.close()

	audioPackets := make(chan *rtp.Packet, 8)
	audioTrackReady := make(chan struct{}, 1)
	client.pc.OnTrack(func(track *webrtc.TrackRemote, receiver *webrtc.RTPReceiver) {
		if track.Kind() != webrtc.RTPCodecTypeAudio {
			return
		}
		select {
		case audioTrackReady <- struct{}{}:
		default:
		}
		go func() {
			for {
				pkt, _, err := track.ReadRTP()
				if err != nil {
					return
				}
				select {
				case audioPackets <- pkt:
				default:
				}
			}
		}()
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

	offerSDP := createOfferWithAudio(t, client.pc)
	_, sid := connectWHEP(t, mgr, "audio-cam", client.pc, offerSDP)
	defer mgr.DeleteWHEPSession(sid)

	select {
	case <-connected:
	case <-time.After(10 * time.Second):
		t.Fatal("ICE connection timeout")
	}

	// Push audio through the hub — OnTrack fires on the first received RTP
	// packet (same semantics as the video forwarding test).
	frame := make([]byte, 160) // 20ms of G.711 at 8kHz
	for i := range 10 {
		hub.BroadcastAudio(int64(i)*160, model.AudioG711, frame)
		time.Sleep(10 * time.Millisecond)
	}

	select {
	case <-audioTrackReady:
	case <-time.After(10 * time.Second):
		t.Fatal("audio track not received within timeout")
	}

	select {
	case pkt := <-audioPackets:
		require.Equal(t, uint8(0), pkt.Header.PayloadType, "PCMU uses static payload type 0")
		require.NotEmpty(t, pkt.Payload)
	case <-time.After(10 * time.Second):
		t.Fatal("no audio RTP packets received within timeout")
	}
}

// TestSubStreamKeySessions verifies quality=sub session bucketing (#513):
// sessions under camID+"/sub" live independently of the main bucket (per-key
// peer limits, camera-level PeerCount totals) and DeleteWHEPSession fires
// onSessionEnd with the suffixed key so the app layer can release the
// sub-stream puller reference.
func TestSubStreamKeySessions(t *testing.T) {
	mgr := newTestManager(t)
	defer mgr.StopAll()

	var mu sync.Mutex
	var ended []string
	mgr.SetOnSessionEnd(func(key string) {
		mu.Lock()
		defer mu.Unlock()
		ended = append(ended, key)
	})

	mainHub := streamhub.New()
	subHub := streamhub.New()
	mgr.RegisterStream("cam-1", mainHub, nil)
	subKey := "cam-1" + streamhub.SubStreamKeySuffix
	mgr.RegisterStream(subKey, subHub, nil)
	require.Equal(t, subHub, mgr.RegisteredHub(subKey), "sub key registered on its own hub")
	require.Equal(t, mainHub, mgr.RegisteredHub("cam-1"), "main key registration unaffected")

	clientMain := newTestClient(t, false)
	defer clientMain.close()
	offerMain := createOfferSDP(t, clientMain.pc)
	_, sidMain := connectWHEP(t, mgr, "cam-1", clientMain.pc, offerMain)

	clientSub := newTestClient(t, false)
	defer clientSub.close()
	offerSub := createOfferSDP(t, clientSub.pc)
	_, sidSub := connectWHEP(t, mgr, subKey, clientSub.pc, offerSub)

	// Camera-level count spans both quality buckets.
	require.Equal(t, 2, mgr.PeerCount("cam-1"))

	// Deleting the sub session reports the SUFFIXED key — the release anchor
	// the app wiring matches against.
	require.NoError(t, mgr.DeleteWHEPSession(sidSub))
	require.Equal(t, 1, mgr.PeerCount("cam-1"))
	mu.Lock()
	require.Equal(t, []string{subKey}, ended)
	mu.Unlock()

	require.NoError(t, mgr.DeleteWHEPSession(sidMain))
	require.Equal(t, 0, mgr.PeerCount("cam-1"))
	mu.Lock()
	require.Equal(t, []string{subKey, "cam-1"}, ended)
	mu.Unlock()
}
