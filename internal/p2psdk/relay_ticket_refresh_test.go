package peer

// Relay session-ticket refresh tests (#143, v3 prerequisite).
//
// The gap these pin: the welcome ticket admits exactly one relay connection
// (two jti uses) and expires 15 minutes after the welcome, so a relay
// rebuild in a long-lived session must obtain a FRESH ticket over the live
// signal link — otherwise, with the relay gate on, every rebuild is closed
// with 4011 and the session's relay fallback stays dead.

import (
	"context"
	"encoding/base64"
	"log/slog"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/p2psdk/relay"
)

// ticketMock extends the signaling mock with the relay_ticket handshake:
// a request for wantPub gets grant (or an empty grant when grant is "").
type ticketMock struct {
	*mockSignalServer
	requests chan RelayTicketRequest
}

func newTicketMock(t *testing.T, wantPub, grant string) *ticketMock {
	t.Helper()
	m := &ticketMock{mockSignalServer: newMockServer(t), requests: make(chan RelayTicketRequest, 4)}
	m.relayTicketHandler = func(_ *websocket.Conn, req RelayTicketRequest) string {
		select {
		case m.requests <- req:
		default:
		}
		if req.RelayPubKey != wantPub {
			return "" // not this connection's offer: never granted
		}
		return grant
	}
	return m
}

// ticketOf reads the installed ticket off a relay client.
func ticketOf(t *testing.T, rc *relay.Client) []byte {
	t.Helper()
	return rc.TicketForTest()
}

// ticketFixture mints a real ticket-shaped base64 string of the given size.
func ticketFixture(t *testing.T) (b64 string, raw []byte) {
	t.Helper()
	raw = make([]byte, 61)
	for i := range raw {
		raw[i] = byte(i + 1)
	}
	return base64.StdEncoding.EncodeToString(raw), raw
}

func discardLogger() *slog.Logger { return slog.New(slog.DiscardHandler) }

// mustRelayTicketMsg builds a parsed relay_ticket frame.
func mustRelayTicketMsg(t *testing.T, rt RelayTicket) *Message {
	t.Helper()
	data, err := Marshal(MessageTypeRelayTicket, rt)
	require.NoError(t, err)
	msg, err := ParseAndValidate(data)
	require.NoError(t, err)
	return msg
}

func TestRequestRelayTicket_GrantIsDecodedAndInstalled(t *testing.T) {
	const relayPub = "deadbeef"
	grant, wantRaw := ticketFixture(t)
	m := newTicketMock(t, relayPub, grant)

	sess, err := Dial(context.Background(), SignalingConfig{
		ServerURL: m.url, JWT: "test-jwt", ProductID: "test-product",
		PeerID: "device-ticket", Role: "device",
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = sess.Close() })

	welcome := &Welcome{RelayURL: "ws://relay.example/relay", RelayPubKey: relayPub, RelayTicket: grant}
	rc := sess.newIdentityClient()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sess.armFreshRelayTicket(ctx, rc, welcome)

	select {
	case req := <-m.requests:
		require.Equal(t, relayPub, req.RelayPubKey, "the request must name the relay being dialed")
		require.Equal(t, welcome.RelayURL, req.RelayURL)
	case <-time.After(3 * time.Second):
		t.Fatal("no relay_ticket_request reached the server")
	}
	require.Equal(t, wantRaw, ticketOf(t, rc), "the granted ticket must be installed before the dial")
}

// TestArmFreshRelayTicket_SkipsWhenWelcomeCarriedNoTicket: gate-off
// deployments and pre-#143 servers must produce the exact ticket-less dial
// they always had — no request, no ticket, no behaviour change.
func TestArmFreshRelayTicket_SkipsWhenWelcomeCarriedNoTicket(t *testing.T) {
	grant, _ := ticketFixture(t)
	m := newTicketMock(t, "deadbeef", grant)
	sess, err := Dial(context.Background(), SignalingConfig{
		ServerURL: m.url, JWT: "test-jwt", ProductID: "test-product",
		PeerID: "device-noticket", Role: "device",
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = sess.Close() })

	rc := sess.newIdentityClient()
	welcome := &Welcome{RelayURL: "ws://relay.example/relay", RelayPubKey: "deadbeef"}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	sess.armFreshRelayTicket(ctx, rc, welcome)

	select {
	case req := <-m.requests:
		t.Fatalf("a ticket-less welcome must not trigger a request (got %+v)", req)
	case <-time.After(300 * time.Millisecond):
	}
	require.Empty(t, ticketOf(t, rc), "no ticket may be installed")
}

// TestRequestRelayTicket_RefusedGrantMeansTicketlessDial: a refused or
// empty grant is not an error — the dial proceeds ticket-less (the legacy
// path while the gate is off) instead of blocking the rebuild ladder.
func TestRequestRelayTicket_RefusedGrantMeansTicketlessDial(t *testing.T) {
	grant, _ := ticketFixture(t)
	m := newTicketMock(t, "deadbeef", grant)
	sess, err := Dial(context.Background(), SignalingConfig{
		ServerURL: m.url, JWT: "test-jwt", ProductID: "test-product",
		PeerID: "device-refused", Role: "device",
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = sess.Close() })

	rc := sess.newIdentityClient()
	welcome := &Welcome{
		RelayURL: "ws://relay.example/relay", RelayPubKey: "somebody-elses-relay",
		RelayTicket: grant,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sess.armFreshRelayTicket(ctx, rc, welcome)
	require.Empty(t, ticketOf(t, rc),
		"a grant for a relay the server did not offer must never be installed")
}

// TestHandleRelayTicket_IgnoresUnmatchedAnswers: a late answer for a
// timed-out request, or one bound to another relay, must not reach the next
// waiter — it could not admit that dial.
func TestHandleRelayTicket_IgnoresUnmatchedAnswers(t *testing.T) {
	sess := &Session{closeCh: make(chan struct{}), logger: discardLogger()}
	wait := &relayTicketWait{relayPubKey: "wanted", answer: make(chan string, 1)}
	sess.relayTicketWait = wait

	sess.handleRelayTicket(mustRelayTicketMsg(t, RelayTicket{RelayPubKey: "other", RelayTicket: "Zm9v"}))
	select {
	case got := <-wait.answer:
		t.Fatalf("an answer bound to another relay must be dropped (got %q)", got)
	default:
	}

	sess.handleRelayTicket(mustRelayTicketMsg(t, RelayTicket{RelayPubKey: "wanted", RelayTicket: "Zm9v"}))
	select {
	case got := <-wait.answer:
		require.Equal(t, "Zm9v", got)
	default:
		t.Fatal("the matching answer must be delivered")
	}

	// Nobody waiting: an answer is dropped, not queued for the next ladder.
	sess.relayTicketWait = nil
	require.NotPanics(t, func() {
		sess.handleRelayTicket(mustRelayTicketMsg(t, RelayTicket{RelayPubKey: "wanted", RelayTicket: "YmFy"}))
	})
}

// TestRequestRelayTicket_TimeoutIsNotFatal: a server that never answers
// (pre-#143) must not stall the rebuild — the request times out and the dial
// proceeds ticket-less.
func TestRequestRelayTicket_TimeoutIsNotFatal(t *testing.T) {
	m := newMockServer(t) // relayTicketHandler nil: the request is swallowed
	sess, err := Dial(context.Background(), SignalingConfig{
		ServerURL: m.url, JWT: "test-jwt", ProductID: "test-product",
		PeerID: "device-silent", Role: "device",
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = sess.Close() })

	rc := sess.newIdentityClient()
	welcome := &Welcome{
		RelayURL: "ws://relay.example/relay", RelayPubKey: "deadbeef", RelayTicket: "Zm9v",
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	start := time.Now()
	sess.armFreshRelayTicket(ctx, rc, welcome)
	require.Empty(t, ticketOf(t, rc))
	require.Less(t, time.Since(start), 10*time.Second, "the wait must be bounded")
}
