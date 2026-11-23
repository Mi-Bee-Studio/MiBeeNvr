package peer

import (
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/p2psdk/relay"
)

// newPlaybackFixture builds a Session+p2pSession pair for the #136 policy
// tests (registry-backed owner, like the sentinel fixture).
func newPlaybackFixture(t *testing.T, purpose SessionPurpose, tunnelOnly bool) (*Session, *p2pSession) {
	t.Helper()
	s := &Session{
		logger:  slog.Default(),
		peers:   make(map[string]*p2pSession),
		welcome: &Welcome{},
		closeCh: make(chan struct{}),
		done:    make(chan struct{}),
	}
	s.cfg.PlaybackTunnelOnly = tunnelOnly
	p := &p2pSession{sessionID: "playback-1", owner: s, purpose: purpose}
	s.peers[p.sessionID] = p
	return s, p
}

// TestPathClass pins the relay/turn/direct classification (#136): relay
// transport adoption dominates, then TURN-typed nominated candidates, then
// direct.
func TestPathClass(t *testing.T) {
	_, p := newPlaybackFixture(t, PurposeDefault, false)

	t.Run("relay transport adopted", func(t *testing.T) {
		p.mu.Lock()
		p.relayActive = true
		p.mu.Unlock()
		assert.Equal(t, "relay", p.pathClass(StatsSnapshot{}))
	})

	t.Run("TURN-typed nominated pair", func(t *testing.T) {
		p.mu.Lock()
		p.relayActive = false
		p.mu.Unlock()
		assert.Equal(t, "turn", p.pathClass(StatsSnapshot{
			ICELocalCandidateType:  "relay",
			ICERemoteCandidateType: "host",
		}))
		assert.Equal(t, "turn", p.pathClass(StatsSnapshot{
			ICELocalCandidateType:  "srflx",
			ICERemoteCandidateType: "relay",
		}))
	})

	t.Run("direct host/srflx pairs", func(t *testing.T) {
		assert.Equal(t, "direct", p.pathClass(StatsSnapshot{
			ICELocalCandidateType:  "host",
			ICERemoteCandidateType: "srflx",
		}))
		assert.Equal(t, "direct", p.pathClass(StatsSnapshot{}))
	})
}

// TestCheckPlaybackPolicy pins the enforcement core: under
// PlaybackTunnelOnly, a playback-purpose session on a TURN/relay path
// degrades exactly once — OnPlaybackDegraded fires with a stable reason and
// the session tears down with reason playback_tunnel_only. Direct paths and
// default-purpose sessions pass untouched.
func TestCheckPlaybackPolicy(t *testing.T) {
	relaySnap := StatsSnapshot{
		ICELocalCandidateType:  "relay",
		ICERemoteCandidateType: "host",
	}

	t.Run("TURN pair violates the policy → degrade + teardown", func(t *testing.T) {
		s, p := newPlaybackFixture(t, PurposePlayback, true)

		degraded := make(chan string, 2)
		s.OnPlaybackDegraded = func(sessionID, reason string) { degraded <- reason }
		closed := make(chan string, 2)
		s.OnSessionClose = func(sessionID, reason string) { closed <- reason }

		p.checkPlaybackPolicy(relaySnap)

		select {
		case r := <-degraded:
			assert.Equal(t, PlaybackDegradedTurn, r)
		case <-time.After(2 * time.Second):
			t.Fatal("TURN pair must trigger the degradation notice")
		}
		select {
		case r := <-closed:
			assert.Equal(t, playbackTeardownReason, r)
		case <-time.After(2 * time.Second):
			t.Fatal("policy violation must tear the session down")
		}
		_, still := s.peers[p.sessionID]
		assert.False(t, still, "teardown removes the registry entry")

		// Second tick on the same violation is a no-op (one-shot).
		p.checkPlaybackPolicy(relaySnap)
		select {
		case r := <-degraded:
			t.Fatalf("degradation must be one-shot, got %s", r)
		default:
		}
	})

	t.Run("WSS relay transport violates the policy", func(t *testing.T) {
		s, p := newPlaybackFixture(t, PurposePlayback, true)
		p.mu.Lock()
		p.relayActive = true
		p.mu.Unlock()
		degraded := make(chan string, 2)
		s.OnPlaybackDegraded = func(_, reason string) { degraded <- reason }
		p.checkPlaybackPolicy(StatsSnapshot{})
		select {
		case r := <-degraded:
			assert.Equal(t, PlaybackDegradedRelay, r)
		case <-time.After(2 * time.Second):
			t.Fatal("relay transport must trigger the degradation notice")
		}
	})

	t.Run("direct pair passes", func(t *testing.T) {
		s, p := newPlaybackFixture(t, PurposePlayback, true)
		degraded := make(chan string, 2)
		s.OnPlaybackDegraded = func(_, reason string) { degraded <- reason }
		p.checkPlaybackPolicy(StatsSnapshot{ICELocalCandidateType: "host"})
		select {
		case r := <-degraded:
			t.Fatalf("direct pair must pass, got %s", r)
		default:
		}
	})

	t.Run("policy switch off = legacy behavior", func(t *testing.T) {
		s, p := newPlaybackFixture(t, PurposePlayback, false)
		degraded := make(chan string, 2)
		s.OnPlaybackDegraded = func(_, reason string) { degraded <- reason }
		p.checkPlaybackPolicy(relaySnap)
		select {
		case r := <-degraded:
			t.Fatalf("policy off must keep legacy behavior, got %s", r)
		default:
		}
	})

	t.Run("default purpose is exempt", func(t *testing.T) {
		s, p := newPlaybackFixture(t, PurposeDefault, true)
		degraded := make(chan string, 2)
		s.OnPlaybackDegraded = func(_, reason string) { degraded <- reason }
		p.checkPlaybackPolicy(relaySnap)
		select {
		case r := <-degraded:
			t.Fatalf("live-preview purpose keeps the full chain, got %s", r)
		default:
		}
	})
}

// TestPlaybackPolicyBlocksRelayEviction pins the pathevict gate (#136): a
// playback session sustaining a pathological direct pair degrades with a
// notice instead of silently adopting the relay transport (which would
// violate the tunnel-only policy).
func TestPlaybackPolicyBlocksRelayEviction(t *testing.T) {
	s, p := newPlaybackFixture(t, PurposePlayback, true)
	// The eviction path bails out early when no relay fallback exists (the
	// gate sits at the adoption point — semantics: only forbid the
	// transport we are about to adopt), so the fixture arms both.
	p.relayClient = relay.NewClient()
	p.watchSignal = newSignalingClient(SignalingConfig{})
	degraded := make(chan string, 2)
	s.OnPlaybackDegraded = func(_, reason string) { degraded <- reason }
	closed := make(chan string, 2)
	s.OnSessionClose = func(_, reason string) { closed <- reason }

	bad := StatsSnapshot{
		ICERemoteCandidateType:    "host",
		ICERemoteCandidateAddress: "192.168.194.7",
		ICERTTSeconds:             0.5,
	}
	for range pathologicalStreakMin {
		p.checkPathEviction(bad)
	}

	select {
	case r := <-degraded:
		assert.Equal(t, PlaybackDegradedRelay, r)
	case <-time.After(2 * time.Second):
		t.Fatal("eviction trigger must degrade playback sessions explicitly")
	}
	select {
	case r := <-closed:
		assert.Equal(t, playbackTeardownReason, r)
	case <-time.After(2 * time.Second):
		t.Fatal("degradation must end in teardown")
	}
}
