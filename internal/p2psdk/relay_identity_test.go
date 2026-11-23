package peer

// relay_identity_test.go — #162: relay 身份与连接解耦 + 重建去环。
//
// 事故形态（下游 MiBeeNvr 生产实录 2026-11-23）：设备侧把 relay 身份键
// 从「每次重建新铸」改为「会话级稳定」后，与服务端的同键接管语义撞成
// 热循环 —— 重建成功 → 服务端踢掉上一代连接（close 1011 replaced by
// re-auth）→ 上一代连接的 fatal 回调照样触发重建 → 5s/轮，3000+ 轮。
//
// 两条缺一不漏的成因，本文件各钉一组契约：
//  1. 回调不校验归属：任何一代 client 的 fatal 都能驱动重建；
//  2. rebuild 无单飞：并发梯子用同一身份互踢。
//
// 同时钉住身份解耦本身：公告键必须跨重建/重指派不漂移，且在从未拨号
// 的情况下也存在（门禁拦截/在途/失败时设备仍要能公告并武装 E2EE）。

import (
	"bytes"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/p2psdk/relay"
)

func newIdentityTestSession(t *testing.T) (*Session, *bytes.Buffer) {
	t.Helper()
	buf := &bytes.Buffer{}
	s := &Session{
		logger:  slog.New(slog.NewTextHandler(buf, nil)),
		peers:   make(map[string]*p2pSession),
		welcome: &Welcome{}, // empty RelayURL → the async rebuild is a no-op
		closeCh: make(chan struct{}),
		done:    make(chan struct{}),
	}
	pub, priv := relay.GenerateKeyPair()
	s.relayIdentity = &identityKeypair{pub: pub, priv: priv}
	return s, buf
}

// TestRelayIdentity_ExistsWithoutAnyDial: the identity is minted at session
// creation, so announcements never depend on a successful relay dial. This
// is the the relay-identity gap root cause: a ticket-gated or in-flight dial used to
// leave the device with no key to announce.
func TestRelayIdentity_ExistsWithoutAnyDial(t *testing.T) {
	s, _ := newIdentityTestSession(t)

	pub, ok := s.relayIdentityPub()
	require.True(t, ok, "identity must exist before any relay connection")
	assert.NotEqual(t, [32]byte{}, pub)

	assert.Nil(t, s.relayClient, "no relay client was ever dialed")
}

// TestRelayIdentity_StableAcrossRebuildReplacement: a replacement client
// built from the session identity presents the SAME public key. This is what
// stops the announced key from drifting when the transport is replaced.
func TestRelayIdentity_StableAcrossRebuildReplacement(t *testing.T) {
	s, _ := newIdentityTestSession(t)

	first := s.newIdentityClient()
	second := s.newIdentityClient()

	assert.Equal(t, first.PubKey(), second.PubKey(),
		"rebuilds must present one identity, not mint a new key each time")
	assert.Equal(t, s.relayIdentity.pub, first.PubKey(),
		"the client key must equal the key announced to peers")

	// The identity round-trips through IdentityKeys, which is how a caller
	// (or a downstream mirror) rebuilds without drifting.
	pub, priv := first.IdentityKeys()
	assert.Equal(t, s.relayIdentity.pub, pub)
	assert.Equal(t, s.relayIdentity.priv, priv)
}

// TestRelayFatalHook_IgnoresSupersededClient is guard #1: the server's
// same-key takeover closes the OLD connection by design, so the old
// generation's fatal callback must not schedule another rebuild. Without
// this the loop is unconditional (measured at 5s/cycle, 3000+ rounds).
func TestRelayFatalHook_IgnoresSupersededClient(t *testing.T) {
	s, buf := newIdentityTestSession(t)

	oldClient := s.newIdentityClient()
	newClient := s.newIdentityClient()

	// The new generation is the registered one; the old is superseded.
	s.relayClient = newClient

	// Invoke the OLD client's fatal hook — exactly what the server's
	// takeover triggers via close 1011 on the displaced connection.
	s.relayFatalHook(oldClient)(errReplacedByReAuth{})

	time.Sleep(30 * time.Millisecond)
	assert.Contains(t, buf.String(), "stale relay client fatal ignored",
		"a superseded client must be recognized as stale")
	assert.NotContains(t, buf.String(), "relay connection fatal — rebuilding",
		"a superseded client must never trigger a rebuild")
}

// TestRelayFatalHook_ActsOnCurrentClient: the guard must not swallow real
// failures of the live client.
func TestRelayFatalHook_ActsOnCurrentClient(t *testing.T) {
	s, buf := newIdentityTestSession(t)

	current := s.newIdentityClient()
	s.relayClient = current

	s.relayFatalHook(current)(errReplacedByReAuth{})

	time.Sleep(30 * time.Millisecond)
	assert.Contains(t, buf.String(), "relay connection fatal — rebuilding",
		"the live client's death must still drive recovery")
}

// TestBeginRelayRebuild_SingleFlight is guard #2: concurrent rebuild
// triggers must claim the slot exactly once. Parallel ladders dial with the
// same identity and displace each other, which is self-sustaining.
//
// The guard itself is asserted here rather than through rebuildRelayClient,
// because the real ladder sleeps through 5s+10s+15s retry delays against an
// unreachable endpoint — a 30s test for a boolean.
func TestBeginRelayRebuild_SingleFlight(t *testing.T) {
	s, _ := newIdentityTestSession(t)

	assert.True(t, s.beginRelayRebuild(), "the first trigger owns the slot")
	assert.False(t, s.beginRelayRebuild(), "a concurrent trigger must be dropped")

	s.relayRebuilding.Store(false) // the ladder's defer releases it
	assert.True(t, s.beginRelayRebuild(), "the slot is reusable once released")
}

// TestBeginRelayRebuild_ConcurrentTriggersClaimOnce hammers the guard from
// many goroutines: exactly one winner, the rest dropped.
func TestBeginRelayRebuild_ConcurrentTriggersClaimOnce(t *testing.T) {
	s, _ := newIdentityTestSession(t)

	const goroutines = 32
	var wins atomic.Int32
	start := make(chan struct{})
	done := make(chan struct{}, goroutines)
	for range goroutines {
		go func() {
			defer func() { done <- struct{}{} }()
			<-start
			if s.beginRelayRebuild() {
				wins.Add(1)
			}
		}()
	}
	close(start)
	for range goroutines {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("guard callers never returned")
		}
	}
	assert.Equal(t, int32(1), wins.Load(), "exactly one rebuild may be in flight")
}

// TestHandleRelayFatal_TakeoverDoesNotRebuild is the end-to-end form of
// guard #1: a same-key takeover (close 4012 → relay.ReplacedError) must
// tear down the sessions riding the displaced socket but must NOT rebuild.
// Rebuilding is precisely what turned the takeover into a 5s loop.
func TestHandleRelayFatal_TakeoverDoesNotRebuild(t *testing.T) {
	s, buf := newIdentityTestSession(t)

	current := s.newIdentityClient()
	s.relayClient = current

	s.handleRelayFatal(&relay.ReplacedError{})

	time.Sleep(30 * time.Millisecond)
	assert.Contains(t, buf.String(), "superseded by a newer registration",
		"a takeover must be logged as supersession, not as a fault")
	assert.NotContains(t, buf.String(), "relay connection fatal — rebuilding",
		"a takeover must never schedule a rebuild")
}

// TestRelayCloseCodeClassification pins the wire status the server now
// sends (#162): 4012 → ReplacedError. A regression here would silently
// restore the loop for every downstream client.
func TestRelayCloseCodeClassification(t *testing.T) {
	assert.True(t, relay.IsReplaced(&relay.ReplacedError{}))

	// The dedicated status must not be confused with the restart hint.
	_, isRestart := relay.RetryHint(&relay.ReplacedError{})
	assert.False(t, isRestart, "a takeover is not a restart hint")

	// And a plain error is neither.
	assert.False(t, relay.IsReplaced(errReplacedByReAuth{}),
		"the pre-#162 1011 message must not be misread as a takeover")
}

// TestRelayPeerGoneHook_IgnoresSupersededClient: the peer-gone callback has
// the same ownership problem — a superseded generation must not drive
// recovery for its successor.
func TestRelayPeerGoneHook_IgnoresSupersededClient(t *testing.T) {
	s, buf := newIdentityTestSession(t)

	oldClient := s.newIdentityClient()
	newClient := s.newIdentityClient()
	s.relayClient = newClient

	// Own-key self-notification from the superseded generation would
	// normally be logged as a rebuild trigger.
	s.relayPeerGoneHook(oldClient)(s.relayIdentity.pub, relay.PeerGoneConnLost)

	time.Sleep(30 * time.Millisecond)
	assert.NotContains(t, buf.String(), "own sibling leg departed",
		"a superseded client must not trigger self-notify recovery")
}

// errReplacedByReAuth mirrors the relay server's takeover close (1011
// "relay connection replaced by re-auth").
type errReplacedByReAuth struct{}

func (errReplacedByReAuth) Error() string {
	return "relay read: websocket: close 1011: relay connection replaced by re-auth"
}
