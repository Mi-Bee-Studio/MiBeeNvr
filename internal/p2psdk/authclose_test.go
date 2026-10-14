package peer

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAuthClassCloseClassification(t *testing.T) {
	// Every auth-class reason is classified; terminal vs mintable splits.
	for _, r := range []string{
		"authentication_failed", "subject_revoked", "token_invalid",
		"invalid_token", "unauthorized", "scope_resolution_failed",
	} {
		assert.True(t, IsAuthClassClose(r), "terminal reason %q", r)
		assert.False(t, IsMintableAuthClose(r), "terminal reason %q must not be mintable", r)
	}
	for _, r := range []string{"token_expired", "token_replayed"} {
		assert.True(t, IsAuthClassClose(r), "mintable reason %q", r)
		assert.True(t, IsMintableAuthClose(r), "mintable reason %q", r)
	}
	// Non-auth closes stay class-less — the ladder keeps its normal
	// behavior (idle_timeout, license_expired, network errors).
	for _, r := range []string{"idle_timeout", "license_expired", "license_required", ""} {
		assert.False(t, IsAuthClassClose(r), "non-auth reason %q", r)
	}

	// classifyCloseError triages real gorilla close errors.
	cls := classifyCloseError(&websocket.CloseError{Code: 1008, Text: "token_replayed"})
	assert.Equal(t, authCloseMintable, cls)
	cls = classifyCloseError(&websocket.CloseError{Code: 1008, Text: "subject_revoked"})
	assert.Equal(t, authCloseTerminal, cls)
	cls = classifyCloseError(errors.New("dial tcp: connection refused"))
	assert.Equal(t, authCloseNone, cls)
}

// scriptedAuthServer answers every hello with a Welcome (tracking the
// conns so tests can deliver verdicts server-side), and can be scripted to
// REJECT the Nth connection's hello with a policy close (#66).
type scriptedAuthServer struct {
	url string

	mu           sync.Mutex
	conns        map[int]*websocket.Conn
	nextConn     int
	rejectHelloN int // 1-based: this connection's hello is rejected (0 = never)
	rejectReason string
	hellos       int
	lastJWT      string
}

//nolint:funlen // scripted server: one handler, full handshake branches
func newScriptedAuthServer(t *testing.T) *scriptedAuthServer {
	t.Helper()
	s := &scriptedAuthServer{conns: map[int]*websocket.Conn{}, rejectHelloN: 0}
	up := websocket.Upgrader{}
	mux := http.NewServeMux()
	mux.HandleFunc("/signal", func(w http.ResponseWriter, r *http.Request) {
		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		s.mu.Lock()
		s.nextConn++
		connN := s.nextConn
		s.conns[connN] = conn
		s.mu.Unlock()
		defer func() {
			s.mu.Lock()
			delete(s.conns, connN)
			s.mu.Unlock()
		}()

		_, raw, err := conn.ReadMessage()
		if err != nil {
			return
		}
		msg, err := ParseAndValidate(raw)
		if err != nil || msg.Type != MessageTypeHello {
			return
		}
		var hello Hello
		if err := UnmarshalPayload(msg, &hello); err != nil {
			return
		}
		s.mu.Lock()
		s.hellos++
		s.lastJWT = hello.JWT
		reject := connN == s.rejectHelloN
		s.mu.Unlock()

		if reject {
			_ = conn.WriteControl(websocket.CloseMessage,
				websocket.FormatCloseMessage(websocket.ClosePolicyViolation, s.rejectReason),
				time.Now().Add(time.Second))
			return
		}
		welcome := Welcome{
			SessionID: "sess-" + hello.PeerID, PeerID: hello.PeerID,
			ServerTime: time.Now().Unix(),
		}
		data, _ := Marshal(MessageTypeWelcome, welcome)
		_ = conn.WriteMessage(websocket.TextMessage, data)
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	s.url = "ws" + srv.URL[len("http"):] + "/signal"
	return s
}

func (s *scriptedAuthServer) hellosSeen() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hellos
}

// deliverVerdict closes connection N's server side with a policy close.
func (s *scriptedAuthServer) deliverVerdict(n int, reason string) {
	s.mu.Lock()
	conn := s.conns[n]
	s.mu.Unlock()
	if conn == nil {
		return
	}
	_ = conn.WriteControl(websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.ClosePolicyViolation, reason),
		time.Now().Add(time.Second))
}

// TestSDK_TerminalAuthCloseAbandonsLadder pins #66: a terminal verdict
// (token_invalid) delivered on a live connection stops the reconnect
// ladder — the client must NOT keep presenting the same dead credential
// every backoff step.
func TestSDK_TerminalAuthCloseAbandonsLadder(t *testing.T) {
	srv := newScriptedAuthServer(t)

	sess, err := Dial(context.Background(), SignalingConfig{
		ServerURL: srv.url,
		JWT:       "static-dead-jwt",
		ProductID: "p", PeerID: "dev-1", Role: "device",
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = sess.Close() })

	// Deliver the verdict on connection #1.
	srv.deliverVerdict(1, "token_invalid")

	// The ladder must abandon: no re-dial storm, no live connection.
	time.Sleep(3 * time.Second)
	require.True(t, sess.signal.authTerminal.Load(),
		"terminal verdict must set the abandon flag")
	hellos := srv.hellosSeen()
	assert.LessOrEqual(t, hellos, 2,
		"no re-dial storm after a terminal verdict (initial + at most one in-flight)")

	sess.signal.connMu.Lock()
	conn := sess.signal.conn
	sess.signal.connMu.Unlock()
	assert.Nil(t, conn, "no live re-established connection expected")
}

// TestSDK_MintableAuthCloseRemints pins #66: token_expired with a TokenFunc
// re-mints and recovers on the next ladder attempt.
func TestSDK_MintableAuthCloseRemints(t *testing.T) {
	srv := newScriptedAuthServer(t)
	srv.mu.Lock()
	srv.rejectHelloN = 2 // second connection's hello is rejected once
	srv.rejectReason = "token_expired"
	srv.mu.Unlock()

	var mu sync.Mutex
	mints := 0
	cfg := SignalingConfig{
		ServerURL: srv.url,
		JWT:       "static",
		ProductID: "p", PeerID: "dev-2", Role: "device",
		TokenFunc: func() (string, error) {
			mu.Lock()
			defer mu.Unlock()
			mints++
			return "minted-jwt", nil
		},
	}

	sess, err := Dial(context.Background(), cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sess.Close() })

	// Drop the link network-style: the ladder reconnects, and connection
	// #2's hello hits the scripted token_expired rejection; the ladder must
	// re-mint and land connection #3.
	sess.signal.connMu.Lock()
	oldConn := sess.signal.conn
	sess.signal.connMu.Unlock()
	require.NotNil(t, oldConn)
	_ = oldConn.Close()

	require.Eventually(t, func() bool {
		sess.signal.connMu.Lock()
		defer sess.signal.connMu.Unlock()
		return sess.signal.conn != nil && sess.signal.conn != oldConn
	}, 15*time.Second, 100*time.Millisecond, "should re-mint and reconnect after token_expired")

	mu.Lock()
	m := mints
	mu.Unlock()
	require.GreaterOrEqual(t, m, 1, "TokenFunc must have been used for the re-mint")
	assert.False(t, sess.signal.authTerminal.Load(), "mintable verdict must not abandon with TokenFunc")
}
