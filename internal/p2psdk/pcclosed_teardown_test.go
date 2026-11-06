package peer

// 2026-10-20 root-cause fix tests: the remote graceful-close cascade turns
// PeerConnection → Closed into an immediate session teardown for a
// still-registered session, and is a no-op when the registry entry is
// already gone (our own teardown path — its pc.Close() re-fires the
// callback).

import (
	"log/slog"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"
	"github.com/stretchr/testify/assert"
)

func newSentinelFixtureSession(t *testing.T) (*Session, *p2pSession) {
	t.Helper()
	s := &Session{
		logger:  slog.Default(),
		peers:   make(map[string]*p2pSession),
		welcome: &Welcome{},
		closeCh: make(chan struct{}),
		done:    make(chan struct{}),
	}
	p := &p2pSession{sessionID: "sentinel-1", owner: s}
	return s, p
}

func TestWatchPCClosedTearsDownRegisteredSession(t *testing.T) {
	s, p := newSentinelFixtureSession(t)
	s.peers[p.sessionID] = p

	closed := make(chan string, 1)
	s.OnSessionClose = func(sessionID, reason string) { closed <- sessionID + "/" + reason }

	p.watchPCClosed(webrtc.PeerConnectionStateClosed)

	select {
	case got := <-closed:
		assert.Equal(t, "sentinel-1/peerconnection_closed", got)
	case <-time.After(2 * time.Second):
		t.Fatal("remote-close cascade must tear the registered session down immediately")
	}
	s.mu.RLock()
	_, still := s.peers[p.sessionID]
	s.mu.RUnlock()
	assert.False(t, still, "session must leave the registry")
}

func TestWatchPCClosedSkipsUnregisteredSession(t *testing.T) {
	s, p := newSentinelFixtureSession(t) // NOT registered

	closed := make(chan string, 1)
	s.OnSessionClose = func(sessionID, reason string) { closed <- sessionID + "/" + reason }

	p.watchPCClosed(webrtc.PeerConnectionStateClosed)

	select {
	case got := <-closed:
		t.Fatalf("own-teardown re-fire must be a no-op, got %s", got)
	case <-time.After(300 * time.Millisecond):
		// Expected: registry identity check filtered it out.
	}
}

func TestWatchPCClosedIgnoresNonClosedStates(t *testing.T) {
	s, p := newSentinelFixtureSession(t)
	closed := make(chan string, 1)
	s.OnSessionClose = func(sessionID, reason string) { closed <- sessionID + "/" + reason }

	p.watchPCClosed(webrtc.PeerConnectionStateConnected)

	select {
	case got := <-closed:
		t.Fatalf("non-Closed states must not trigger teardown, got %s", got)
	case <-time.After(200 * time.Millisecond):
		// Expected: only Closed transitions act.
	}
}
