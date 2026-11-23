package peer

import (
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// #132: TURN credentials minted at welcome expire; long-lived sessions
// schedule a mid-life refresh and apply fresh material with
// SetConfiguration so relay paths survive past the credential TTL.

func TestTurnExpiryFromUsername(t *testing.T) {
	// turn-rest shape: "<expiry>:<product>:<account>:<device>:<rand>".
	exp := turnExpiryFromUsername("1799000000:mibeenvr:acct:dev-1:abcd")
	require.Equal(t, exp, time.Unix(1799000000, 0))

	// Static credential (no expiry prefix) → zero.
	require.True(t, turnExpiryFromUsername("static-user").IsZero())
	require.True(t, turnExpiryFromUsername("").IsZero())
	require.True(t, turnExpiryFromUsername("not-a-number:x").IsZero())
}

func TestIceRefreshHalfLife(t *testing.T) {
	now := time.Now()
	require.Zero(t, iceRefreshHalfLife(now.Add(-time.Minute), now), "expired → nothing to schedule")
	require.Zero(t, iceRefreshHalfLife(now, now))

	got := iceRefreshHalfLife(now.Add(24*time.Hour), now)
	require.InDelta(t, 12*time.Hour, got, float64(time.Minute))

	// Floor: pathological tiny remaining life still yields the 30s minimum.
	got = iceRefreshHalfLife(now.Add(40*time.Second), now)
	require.Equal(t, 30*time.Second, got)
}

// The refresh path end-to-end against a real PC: fresh material lands via
// SetConfiguration, the welcome snapshot updates, the offerer re-gathers,
// and the renewal timer re-arms from the new expiry.
func TestSessionHandleIceRefresh(t *testing.T) {
	s := &Session{
		logger:  slog.Default(),
		peers:   make(map[string]*p2pSession),
		closeCh: make(chan struct{}),
		done:    make(chan struct{}),
		cfg:     SignalingConfig{},
		welcome: &Welcome{},
	}

	pc, err := newPeerConnection(SignalingConfig{}, nil, slog.Default())
	require.NoError(t, err)
	defer func() { _ = pc.Close() }()

	p := &p2pSession{
		sessionID: "sess-132",
		isOfferer: true,
		// relayActive skips the offerer-side restart side effect (the async
		// re-gather would race the test teardown); SetConfiguration — the
		// behavior under test — runs regardless.
		relayActive: true,
		logger:      s.logger,
		owner:       s,
		pc:          pc,
	}
	p.touch()
	s.peers["sess-132"] = p

	fresh := []ICEServer{
		{
			Username:   "1799000000:mibeenvr:acct:dev:ff",
			Credential: "hmac",
			URLs:       []string{"turn:turn.example:3478?transport=udp"},
			Tag:        "self",
		},
		{URLs: []string{"stun:stun.example:3478"}, Tag: "self"},
	}
	msgBytes, mErr := json.Marshal(IceRefresh{ICEServers: fresh})
	require.NoError(t, mErr)
	msg := Message{Version: ProtocolVersion, Type: MessageTypeIceRefresh, Payload: json.RawMessage(msgBytes)}
	s.handleIceRefresh(&msg)

	// Welcome snapshot updated (telemetry attribution reads it).
	require.Equal(t, fresh, s.welcome.ICEServers)

	// Renewal re-armed from the fresh expiry (~half of the remaining life).
	s.mu.Lock()
	armed := s.iceRefreshTimer != nil
	s.mu.Unlock()
	require.True(t, armed, "refresh timer must re-arm")

	// The PC configuration now carries the converted server list (both
	// entries survive — empty ICEProviderTags = unfiltered).
	got := pc.GetConfiguration()
	require.Len(t, got.ICEServers, 2)
	require.Equal(t, "1799000000:mibeenvr:acct:dev:ff", got.ICEServers[0].Username)

	// Empty server list is a no-op (provider disabled server-side).
	s.welcome.ICEServers = nil
	empty := Message{Version: ProtocolVersion, Type: MessageTypeIceRefresh, Payload: json.RawMessage("{}")}
	s.handleIceRefresh(&empty)
	require.Nil(t, s.welcome.ICEServers)
}
