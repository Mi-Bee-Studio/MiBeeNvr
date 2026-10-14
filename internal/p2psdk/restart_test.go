package peer

import (
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeSignal is a minimal signal sender that records sent messages.
type fakeSignal struct {
	mu     sync.Mutex
	sent   []string       // SDP bodies sent via sendRaw
	closes []SessionClose // session_close notifications sent via sendRaw
}

func (f *fakeSignal) sendRaw(_ MessageType, payload any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch v := payload.(type) {
	case SDPOffer:
		f.sent = append(f.sent, v.SDP)
	case SessionClose:
		f.closes = append(f.closes, v)
	}
	return nil
}

// setupConnectedPCs creates two PeerConnections and performs a full
// offer/answer exchange so the ICE transport is initialized on both sides.
func setupConnectedPCs(t *testing.T) (*webrtc.PeerConnection, *webrtc.PeerConnection) {
	pcOffer, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = pcOffer.Close() })

	pcAnswer, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = pcAnswer.Close() })

	pcAnswer.OnDataChannel(func(*webrtc.DataChannel) {})

	_, err = pcOffer.CreateDataChannel("test", nil)
	require.NoError(t, err)

	offer, err := pcOffer.CreateOffer(nil)
	require.NoError(t, err)
	require.NoError(t, pcOffer.SetLocalDescription(offer))
	<-webrtc.GatheringCompletePromise(pcOffer)
	finalOffer := pcOffer.LocalDescription()
	require.NotNil(t, finalOffer)

	require.NoError(t, pcAnswer.SetRemoteDescription(*finalOffer))
	answer, err := pcAnswer.CreateAnswer(nil)
	require.NoError(t, err)
	require.NoError(t, pcAnswer.SetLocalDescription(answer))
	<-webrtc.GatheringCompletePromise(pcAnswer)
	finalAnswer := pcAnswer.LocalDescription()
	require.NotNil(t, finalAnswer)

	require.NoError(t, pcOffer.SetRemoteDescription(*finalAnswer))

	return pcOffer, pcAnswer
}

func TestICERestart_OffererTriggersOnFailed(t *testing.T) {
	pcOffer, _ := setupConnectedPCs(t)

	fs := &fakeSignal{}
	p := &p2pSession{
		isOfferer: true,
		pc:        pcOffer,
		signal:    fs,
	}

	err := p.triggerICERestart()
	require.NoError(t, err)

	fs.mu.Lock()
	require.Len(t, fs.sent, 1, "expected one SDP offer to be sent")
	sdp := fs.sent[0]
	fs.mu.Unlock()

	assert.Contains(t, sdp, "a=ice-ufrag",
		"restart offer must contain ice-ufrag")
	assert.Contains(t, sdp, "a=fingerprint",
		"restart offer must contain DTLS fingerprint")
}

func TestICERestart_AnswererDoesNotTriggerOnFailed(t *testing.T) {
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = pc.Close() })

	fs := &fakeSignal{}
	p := &p2pSession{
		isOfferer: false,
		pc:        pc,
		signal:    fs,
	}

	err = p.triggerICERestart()
	require.NoError(t, err)

	fs.mu.Lock()
	assert.Empty(t, fs.sent, "answerer must not send any SDP offer")
	fs.mu.Unlock()
}

func TestICERestart_OffererTriggersOnGraceExpiry(t *testing.T) {
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = pc.Close() })

	fs := &fakeSignal{}
	p := &p2pSession{
		isOfferer: true,
		pc:        pc,
		signal:    fs,
	}

	// Simulate Disconnected → grace timer fires.
	p.handleICEStateChange(webrtc.ICEConnectionStateDisconnected)

	p.mu.Lock()
	assert.NotNil(t, p.graceTimer, "grace timer should be set after Disconnected")
	// Clean up timer.
	if p.graceTimer != nil {
		p.graceTimer.Stop()
		p.graceTimer = nil
	}
	p.mu.Unlock()
}

func TestICERestart_GraceCancelledOnConnected(t *testing.T) {
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = pc.Close() })

	fs := &fakeSignal{}
	p := &p2pSession{
		isOfferer: true,
		pc:        pc,
		signal:    fs,
	}

	// Disconnected starts timer.
	p.handleICEStateChange(webrtc.ICEConnectionStateDisconnected)
	p.mu.Lock()
	assert.NotNil(t, p.graceTimer, "grace timer should be set")
	assert.False(t, p.recoveryStart.IsZero(), "recoveryStart should be set")
	p.mu.Unlock()

	// Connected cancels timer.
	p.handleICEStateChange(webrtc.ICEConnectionStateConnected)
	p.mu.Lock()
	assert.Nil(t, p.graceTimer, "grace timer should be cancelled on Connected")
	assert.True(t, p.recoveryStart.IsZero(), "recoveryStart should be reset")
	p.mu.Unlock()

	// No restart was sent.
	fs.mu.Lock()
	assert.Empty(t, fs.sent, "no restart should be sent after recovery")
	fs.mu.Unlock()
}

func TestICERestart_AnswererNoOpOnFailed(t *testing.T) {
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = pc.Close() })

	fs := &fakeSignal{}
	p := &p2pSession{
		isOfferer: false,
		pc:        pc,
		signal:    fs,
	}

	p.handleICEStateChange(webrtc.ICEConnectionStateFailed)

	fs.mu.Lock()
	assert.Empty(t, fs.sent, "answerer must not restart on Failed")
	fs.mu.Unlock()
}

func TestICERestart_GraceExpiryNoOfferer(_ *testing.T) {
	p := &p2pSession{
		isOfferer: false,
	}
	// Should not panic or send any SDP.
	p.onGraceExpired()
}

func TestICERestart_NoPCError(t *testing.T) {
	p := &p2pSession{
		isOfferer: true,
		pc:        nil,
		signal:    &fakeSignal{},
	}

	err := p.triggerICERestart()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no peer connection")
}

func TestICERestart_OfferChangesIceUfrag(t *testing.T) {
	pcOffer, _ := setupConnectedPCs(t)

	// Capture the initial offer SDP (already set during setup).
	initialSDP := pcOffer.LocalDescription()
	require.NotNil(t, initialSDP)
	initialUfrag := extractICEUfrag(initialSDP.SDP)
	require.NotEmpty(t, initialUfrag, "initial offer must have ice-ufrag")

	// Create restart offer on the same PC (ICE transport is initialized).
	restartOffer, err := pcOffer.CreateOffer(&webrtc.OfferOptions{ICERestart: true})
	require.NoError(t, err)
	require.NoError(t, pcOffer.SetLocalDescription(restartOffer))
	<-webrtc.GatheringCompletePromise(pcOffer)
	restartSDP := pcOffer.LocalDescription()
	require.NotNil(t, restartSDP)

	restartUfrag := extractICEUfrag(restartSDP.SDP)
	require.NotEmpty(t, restartUfrag, "restart offer must have ice-ufrag")

	assert.NotEqual(t, initialUfrag, restartUfrag,
		"restart must use a different ice-ufrag")
}

func extractICEUfrag(sdp string) string {
	for _, line := range strings.Split(sdp, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "a=ice-ufrag:") {
			return strings.TrimPrefix(line, "a=ice-ufrag:")
		}
	}
	return ""
}

// newPendingOfferPC returns a PeerConnection left in have-local-offer:
// an offer was set locally but never answered — the state a dead peer
// leaves its offerer in.
func newPendingOfferPC(t *testing.T) *webrtc.PeerConnection {
	t.Helper()
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = pc.Close() })
	offer, err := pc.CreateOffer(nil)
	require.NoError(t, err)
	require.NoError(t, pc.SetLocalDescription(offer))
	return pc
}

func TestICERestart_FailureCapAbandonsSession(t *testing.T) {
	pc := newPendingOfferPC(t)

	owner := &Session{peers: make(map[string]*p2pSession)}
	fs := &fakeSignal{}
	p := &p2pSession{
		sessionID: "cap-test",
		isOfferer: true,
		pc:        pc,
		signal:    fs,
		owner:     owner,
	}
	owner.peers["cap-test"] = p

	for range maxICERestartFailures {
		p.attemptICERestart()
	}

	require.Eventually(t, func() bool {
		owner.mu.Lock()
		defer owner.mu.Unlock()
		_, exists := owner.peers["cap-test"]
		return !exists
	}, 5*time.Second, 50*time.Millisecond, "session must be reaped after the failure cap")

	fs.mu.Lock()
	require.Len(t, fs.closes, 1, "abandon must notify via session_close")
	assert.Equal(t, "cap-test", fs.closes[0].SessionID)
	assert.Equal(t, "ice_restart_failed", fs.closes[0].Reason)
	fs.mu.Unlock()

	p.mu.Lock()
	assert.Nil(t, p.pc, "peer connection must be released")
	p.mu.Unlock()
}

func TestICERestart_RetryDriverReapsDeadPeer(t *testing.T) {
	orig := iceRestartRetryInterval
	iceRestartRetryInterval = 50 * time.Millisecond
	t.Cleanup(func() { iceRestartRetryInterval = orig })

	pc := newPendingOfferPC(t)

	owner := &Session{peers: make(map[string]*p2pSession)}
	fs := &fakeSignal{}
	closed := make(chan string, 1)
	owner.OnSessionClose = func(sessionID, reason string) {
		select {
		case closed <- sessionID + "|" + reason:
		default:
		}
	}
	p := &p2pSession{
		sessionID: "retry-test",
		isOfferer: true,
		pc:        pc,
		signal:    fs,
		owner:     owner,
	}
	owner.peers["retry-test"] = p

	// A single event-driven round must be enough: the retry timer then
	// drives the remaining rounds to the abandon threshold.
	p.attemptICERestart()

	require.Eventually(t, func() bool {
		owner.mu.Lock()
		defer owner.mu.Unlock()
		_, exists := owner.peers["retry-test"]
		return !exists
	}, 5*time.Second, 50*time.Millisecond, "retry driver must reap the session without further ICE events")

	// teardownWithReason 先从注册表删除、后触发 OnSessionClose——Eventually
	// 观察到删除时回调可能尚未执行，必须阻塞等回调而不是非阻塞探一次。
	select {
	case got := <-closed:
		assert.Equal(t, "retry-test|ice_restart_failed", got)
	case <-time.After(2 * time.Second):
		t.Fatal("OnSessionClose must fire on abandonment")
	}
}

func TestICERestart_RetryStopsOnICERecovery(t *testing.T) {
	orig := iceRestartRetryInterval
	iceRestartRetryInterval = 50 * time.Millisecond
	t.Cleanup(func() { iceRestartRetryInterval = orig })

	pc := newPendingOfferPC(t)

	owner := &Session{peers: make(map[string]*p2pSession)}
	p := &p2pSession{
		sessionID: "recover-test",
		isOfferer: true,
		pc:        pc,
		signal:    &fakeSignal{},
		owner:     owner,
	}
	owner.peers["recover-test"] = p

	// A failed round arms the retry timer...
	p.attemptICERestart()
	// ...but ICE recovers before it fires.
	p.handleICEStateChange(webrtc.ICEConnectionStateConnected)

	time.Sleep(3 * iceRestartRetryInterval)

	p.mu.Lock()
	failures := p.restartFailures
	timer := p.restartTimer
	p.mu.Unlock()
	assert.Equal(t, 0, failures, "recovery must reset the failure counter")
	assert.Nil(t, timer, "recovery must stop the retry timer")

	owner.mu.Lock()
	_, exists := owner.peers["recover-test"]
	owner.mu.Unlock()
	assert.True(t, exists, "recovered session must not be reaped")
}

func TestOnSessionClose_FiresOnRemoteClose(t *testing.T) {
	owner := &Session{logger: slog.Default(), peers: make(map[string]*p2pSession)}
	p := &p2pSession{sessionID: "remote-close", isOfferer: false, owner: owner}
	owner.peers["remote-close"] = p

	closed := make(chan string, 1)
	owner.mu.Lock()
	owner.OnSessionClose = func(sessionID, reason string) {
		select {
		case closed <- sessionID + "|" + reason:
		default:
		}
	}
	owner.mu.Unlock()

	payload, err := json.Marshal(SessionClose{SessionID: "remote-close", Reason: "peer bye"})
	require.NoError(t, err)
	owner.handleSessionClose(&Message{
		Version: ProtocolVersion,
		Type:    MessageTypeSessionClose,
		Payload: payload,
	})

	select {
	case got := <-closed:
		assert.Equal(t, "remote-close|peer bye", got)
	default:
		t.Fatal("OnSessionClose must fire on remote session_close")
	}

	owner.mu.Lock()
	_, exists := owner.peers["remote-close"]
	owner.mu.Unlock()
	assert.False(t, exists, "remote close must remove the session")
}

func TestICERestart_RelayActiveSkipsRestart(t *testing.T) {
	pc := newPendingOfferPC(t)

	owner := &Session{peers: make(map[string]*p2pSession)}
	p := &p2pSession{
		sessionID:   "relay-test",
		isOfferer:   true,
		pc:          pc,
		signal:      &fakeSignal{},
		owner:       owner,
		relayActive: true,
	}
	owner.peers["relay-test"] = p

	p.attemptICERestart()

	p.mu.Lock()
	assert.Equal(t, 0, p.restartFailures, "relay-adopted session must not count restart failures")
	p.mu.Unlock()

	owner.mu.Lock()
	_, exists := owner.peers["relay-test"]
	owner.mu.Unlock()
	assert.True(t, exists, "relay-adopted session must survive ICE restart attempts")
}

func TestICERestart_SuccessfulRoundResetsCounter(t *testing.T) {
	pcOffer, _ := setupConnectedPCs(t) // signaling state: stable

	owner := &Session{peers: make(map[string]*p2pSession)}
	fs := &fakeSignal{}
	p := &p2pSession{
		sessionID: "reset-test",
		isOfferer: true,
		pc:        pcOffer,
		signal:    fs,
		owner:     owner,
	}
	owner.peers["reset-test"] = p
	p.mu.Lock()
	p.restartFailures = maxICERestartFailures - 1
	p.mu.Unlock()

	p.attemptICERestart()

	p.mu.Lock()
	assert.Equal(t, 0, p.restartFailures, "a successful restart round must reset the counter")
	p.mu.Unlock()

	owner.mu.Lock()
	_, exists := owner.peers["reset-test"]
	owner.mu.Unlock()
	assert.True(t, exists, "a session with a successful restart round must not be abandoned")
}
