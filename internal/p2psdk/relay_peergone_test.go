package peer

// #106-B session-level FramePeerGone handling: sessions bound to the
// departed relay key are torn down; sessions on other keys (and other
// transports) are untouched.

import (
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/p2psdk/relay"
)

func TestHandleRelayPeerGoneTearsDownBoundSessions(t *testing.T) {
	s := &Session{
		logger:  slog.Default(),
		peers:   make(map[string]*p2pSession),
		welcome: &Welcome{}, // empty RelayURL → rebuild is a no-op
		closeCh: make(chan struct{}),
		done:    make(chan struct{}),
	}
	var boundKey, otherKey [32]byte
	boundKey[0] = 0xAA
	otherKey[0] = 0xBB
	bound := &p2pSession{
		sessionID: "bound-1", owner: s, relayActive: true, remoteRelayKey: boundKey,
	}
	otherPeer := &p2pSession{
		sessionID: "other-1", owner: s, relayActive: true, remoteRelayKey: otherKey,
	}
	s.peers["bound-1"] = bound
	s.peers["other-1"] = otherPeer

	closed := make(chan string, 2)
	s.OnSessionClose = func(sessionID, _ string) { closed <- sessionID }

	s.handleRelayPeerGone(boundKey, relay.PeerGoneIdleReaped)

	select {
	case id := <-closed:
		assert.Equal(t, "bound-1", id)
	case <-time.After(2 * time.Second):
		t.Fatal("bound session close callback not fired")
	}
	s.mu.RLock()
	_, boundGone := s.peers["bound-1"]
	_, otherAlive := s.peers["other-1"]
	s.mu.RUnlock()
	assert.False(t, boundGone, "session bound to the departed key must be torn down")
	assert.True(t, otherAlive, "session on another relay key must survive")
}

// TestHandleRelayPeerGoneUnknownKeyIsNoop: a departure with no matching
// session neither panics nor disturbs the registry.
func TestHandleRelayPeerGoneUnknownKeyIsNoop(t *testing.T) {
	s := &Session{
		logger:  slog.Default(),
		peers:   make(map[string]*p2pSession),
		welcome: &Welcome{},
		closeCh: make(chan struct{}),
		done:    make(chan struct{}),
	}
	var unknown [32]byte
	unknown[0] = 0xCC
	s.handleRelayPeerGone(unknown, relay.PeerGoneConnLost)

	require.Empty(t, s.peers)
}
