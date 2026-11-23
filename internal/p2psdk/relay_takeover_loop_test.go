package peer

// The relay takeover loop. Production shape — a rebuilt client
// registers with the SAME identity key, the server evicts the previous
// registration on a same-key re-auth, and that eviction must NOT feed another
// rebuild. Two independent guards are pinned here:
//
//  1. relayFatalHook — a fatal from a superseded client is stale. This is the
//     only thing that closes the loop: the server's takeover close always
//     lands on the PREVIOUS connection, so without the ownership check every
//     completed rebuild immediately triggers the next one.
//  2. relayRebuilding — at most one ladder in flight, so concurrent fatals
//     cannot stack rebuilds that evict each other's work.
//  3. relay.IsReplaced — the server's dedicated takeover close (4012) is
//     recognized as "a newer registration of ours already owns the key", so
//     even a client that somehow misses the ownership check does not rebuild.
//
// Both tests observe the RELAY DIAL GATE (called once per ladder entry, before
// any sleep), which makes "did a rebuild start?" directly countable without
// touching the network. This matters: asserting on internal flags alone passes
// vacuously when the ladder bails out early for unrelated reasons.

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/p2psdk/relay"
)

const loopTestRelayPubKey = "abababababababababababababababababababababababababababababababab"

func loopTestSession() *Session {
	return &Session{
		cfg:     SignalingConfig{Logger: gateTestLogger()},
		logger:  gateTestLogger().With("component", "p2p_session"),
		closeCh: make(chan struct{}),
		peers:   make(map[string]*p2pSession),
		welcome: &Welcome{
			RelayURL:    "wss://127.0.0.1:1/relay",
			RelayPubKey: loopTestRelayPubKey,
		},
	}
}

// countGate installs a relay dial gate that records each ladder entry and
// refuses the dial (so no sockets are opened).
func countGate(s *Session) *atomic.Int32 {
	var n atomic.Int32
	s.cfg.RelayDialGate = func() bool {
		n.Add(1)
		return false
	}
	return &n
}

// TestStaleRelayFatalDoesNotRebuild pins guard 1: the eviction close that a
// same-key takeover produces lands on the SUPERSEDED connection, and it must
// be swallowed. Without the ownership check this is a closed loop:
//
//	rebuild installs rc2 → server evicts rc1 → rc1 fatal → rebuild → ...
//
// (production 2026-11-23: 5s/round, 3000+ rounds, remote access down).
func TestStaleRelayFatalDoesNotRebuild(t *testing.T) {
	s := loopTestSession()
	gate := countGate(s)

	old := relay.NewClient()
	s.relayClient = old
	hook := s.relayFatalHook(old) // captured while old was current

	// A rebuild or a cross-node repoint supersedes it.
	s.relayClient = relay.NewClient()

	// The takeover closes the PREVIOUS connection — that is `old`.
	hook(errors.New("relay read: websocket: close 1011: relay connection replaced by re-auth"))

	// Give any (wrongly) spawned ladder time to hit the gate.
	if waitFor(t, 500*time.Millisecond, func() bool { return gate.Load() > 0 }) {
		t.Fatalf("stale fatal started %d rebuild ladder(s) — takeover loop not closed", gate.Load())
	}
}

// TestCurrentRelayFatalStillRebuilds is the counterpart that keeps the fix
// honest: a fatal from the client that IS current must still rebuild.
func TestCurrentRelayFatalStillRebuilds(t *testing.T) {
	s := loopTestSession()
	gate := countGate(s)

	cur := relay.NewClient()
	s.relayClient = cur
	// A plain transport error (NOT a takeover close): the ladder must run.
	// Note the gate is consulted at ladder entry, before the first backoff
	// sleep, so this returns quickly.
	s.relayFatalHook(cur)(errors.New("relay read: i/o timeout"))

	if !waitFor(t, 2*time.Second, func() bool { return gate.Load() >= 1 }) {
		t.Fatal("current client's fatal did not start a rebuild")
	}
}

// TestReplacedErrorNeverRebuilds pins guard 3: the server's dedicated
// takeover close (4012) means a NEWER registration of ours already owns the
// key. Even for the CURRENT client this must not rebuild — the transport is
// healthy, only this older socket was displaced.
func TestReplacedErrorNeverRebuilds(t *testing.T) {
	s := loopTestSession()
	gate := countGate(s)

	cur := relay.NewClient()
	s.relayClient = cur

	s.handleRelayFatal(&relay.ReplacedError{})

	if waitFor(t, 500*time.Millisecond, func() bool { return gate.Load() > 0 }) {
		t.Fatalf("takeover close started %d rebuild ladder(s), want 0", gate.Load())
	}
}

// TestRebuildSingleFlight pins guard 2: while one ladder holds the flight
// slot, a second call returns instead of stacking another ladder. Concurrent
// ladders each dial the same identity key, and the server evicts the prior
// registration — the fastest ladder kills what a slower one just built.
func TestRebuildSingleFlight(t *testing.T) {
	s := loopTestSession()

	var gateCalls atomic.Int32
	entered := make(chan struct{}, 4)
	release := make(chan struct{})
	s.cfg.RelayDialGate = func() bool {
		gateCalls.Add(1)
		select {
		case entered <- struct{}{}:
		default:
		}
		<-release // hold the ladder open so a second caller has a window
		return false
	}

	aDone := make(chan struct{})
	go func() { s.rebuildRelayClient(errors.New("first")); close(aDone) }()

	// Wait until ladder A is inside the gate (i.e. holds the flight slot).
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("first rebuild never entered the ladder")
	}

	bDone := make(chan struct{})
	go func() { s.rebuildRelayClient(errors.New("second")); close(bDone) }()

	// B must coalesce: it should return while A is still blocked.
	select {
	case <-bDone:
	case <-time.After(2 * time.Second):
		t.Fatal("second rebuild blocked instead of coalescing")
	}

	close(release)
	select {
	case <-aDone:
	case <-time.After(2 * time.Second):
		t.Fatal("first rebuild did not finish after release")
	}

	if got := gateCalls.Load(); got != 1 {
		t.Fatalf("gate entered %d times, want exactly 1 (concurrent ladders not coalesced)", got)
	}
	if s.relayRebuilding.Load() {
		t.Fatal("single-flight flag not released after the ladder exited")
	}
}
