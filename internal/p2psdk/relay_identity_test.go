package peer

// mibeescout#16 device-side half: the relay IDENTITY keypair must exist —
// and be usable for key announcement + tunnel E2EE derivation —
// independent of any relay CONNECTION. Before this fix, offer/answer and
// connect_request emitted a relay public key only when the relay dial had
// SUCCEEDED (relayClient != nil); a gated/failed/pending dial left the
// device with no key at all, so direct-path sessions ran plaintext and the
// App's forced-relay re-dial policy (remoteRelayKey) went unarmed.
import (
	"strings"
	"testing"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/p2psdk/relay"
)

// TestIdentityKeysRoundtrip: identity injection must be lossless — the
// re-keyed client announces exactly the session-stable key.
func TestIdentityKeysRoundtrip(t *testing.T) {
	orig := relay.NewClient()
	pub, priv := orig.IdentityKeys()
	if pub == ([32]byte{}) || priv == ([32]byte{}) {
		t.Fatal("generated identity must be non-zero")
	}
	reborn := relay.NewClientWithIdentity(pub, priv)
	if reborn.PubKey() != orig.PubKey() {
		t.Fatal("re-keyed client must announce the same identity")
	}
}

// TestRelayIntentGatesAnnouncement: no relay config in welcome → nothing
// announced (an unroutable key would actively mislead the peer's
// forced-relay path); config present → intent, regardless of dial state.
func TestRelayIntentGatesAnnouncement(t *testing.T) {
	cases := []struct {
		name    string
		welcome *Welcome
		want    bool
	}{
		{"no welcome", nil, false},
		{"empty relay", &Welcome{}, false},
		{"url only", &Welcome{RelayURL: "wss://x/relay"}, false},
		{"key only", &Welcome{RelayPubKey: strings.Repeat("ab", 32)}, false},
		{"full config", &Welcome{
			RelayURL:    "wss://x/relay",
			RelayPubKey: strings.Repeat("ab", 32),
		}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &Session{welcome: tc.welcome}
			if got := s.relayIntent(); got != tc.want {
				t.Fatalf("relayIntent = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestAnnouncedRelayKey: the offer/answer emit gate — identity + intent →
// announce; nil identity or no intent → silent (legacy wire shape).
func TestAnnouncedRelayKey(t *testing.T) {
	id := relay.NewClient()
	full := &Session{welcome: &Welcome{
		RelayURL:    "wss://x/relay",
		RelayPubKey: strings.Repeat("ab", 32),
	}}

	t.Run("intent + identity → announce", func(t *testing.T) {
		p := &p2pSession{owner: full, relayIdentity: id}
		pk, ok := p.announcedRelayKey()
		if !ok || pk != id.PubKey() {
			t.Fatal("identity key must be announced under intent")
		}
	})
	t.Run("no intent → not announced (legacy shape)", func(t *testing.T) {
		p := &p2pSession{owner: &Session{}, relayIdentity: id}
		if _, ok := p.announcedRelayKey(); ok {
			t.Fatal("without relay intent nothing may be announced")
		}
	})
	t.Run("intent but nil identity → not announced", func(t *testing.T) {
		p := &p2pSession{owner: full}
		if _, ok := p.announcedRelayKey(); ok {
			t.Fatal("nil identity must not announce a zero key")
		}
	})
}

// THE #16 acceptance: with the relay dial GATED (no connection at all),
// the session still announces its identity and derives the tunnel E2EE key
// from the caller's announced key — direct-path sessions arm E2EE while
// the relay leg never came up.
func TestGatedRelayDialKeepsE2EEArmed(t *testing.T) {
	// Caller side: mints its identity and announces it (its own dial may be
	// gated too — symmetric).
	callerIdentity := relay.NewClient()

	// Device side: relay intent present, dial gated → relayClient stays nil
	// while the identity survives.
	s := &Session{
		welcome: &Welcome{
			RelayURL:    "wss://x/relay",
			RelayPubKey: strings.Repeat("ab", 32),
		},
	}
	s.relayIdentity = relay.NewClient()

	p := &p2pSession{
		owner:          s,
		relayIdentity:  s.relayIdentity,
		relayClient:    s.relayClient, // nil — gated
		remoteRelayKey: callerIdentity.PubKey(),
	}

	if _, ok := p.announcedRelayKey(); !ok {
		t.Fatal("gated dial must still announce the identity key")
	}
	key := p.e2eeKey()
	if key == nil {
		t.Fatal("E2EE must derive from identity + caller key WITHOUT any relay connection")
	}

	// Cross-check: the caller derives the same key with its own identity
	// and the device's announced key (both sides, symmetric math).
	callerKey, err := callerIdentity.DeriveE2EEKey(s.relayIdentity.PubKey())
	if err != nil {
		t.Fatalf("caller-side derivation failed: %v", err)
	}
	if callerKey != *(*[32]byte)(key) {
		t.Fatal("both sides must derive the same tunnel key")
	}
}

// TestRebuildRekeysWithSessionIdentity: rebuild/re-point clients must
// carry the session's stable identity — the announced key must not drift
// across dials or the peer's armed E2EE key and routing key go stale.
func TestRebuildRekeysWithSessionIdentity(t *testing.T) {
	s := &Session{}
	s.relayIdentity = relay.NewClient()
	pub, priv := s.relayIdentity.IdentityKeys()
	reborn := relay.NewClientWithIdentity(pub, priv)
	if reborn.PubKey() != s.relayIdentity.PubKey() {
		t.Fatal("rebuild client must keep the session identity")
	}
}
