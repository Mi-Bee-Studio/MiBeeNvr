package peer

import (
	"context"
	"encoding/hex"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/nacl/box"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/p2psdk/relay"
)

// miniRelayServer answers the client_auth handshake and keeps connections
// registered — the minimum for session-level relay lifecycle tests. Frame
// routing (send_packet → recv_packet) forwards to the destination by key.
type miniRelayServer struct {
	server   *httptest.Server
	url      string
	upgrader websocket.Upgrader
	pubKey   [32]byte
	privKey  [32]byte

	mu      sync.Mutex
	clients map[[32]byte]*websocket.Conn
	// wmu serializes ALL conn writes: gorilla panics on concurrent writers,
	// and with multiple peers sending to one recipient (per-source routing
	// tests) handler goroutines race (the real server has per-client locks).
	wmu sync.Mutex
}

func (s *miniRelayServer) writeTo(conn *websocket.Conn, data []byte) {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	_ = conn.WriteMessage(websocket.BinaryMessage, data)
}

func newMiniRelayServer(t *testing.T) *miniRelayServer {
	t.Helper()
	s := &miniRelayServer{
		clients: make(map[[32]byte]*websocket.Conn),
		upgrader: websocket.Upgrader{
			CheckOrigin: func(_ *http.Request) bool { return true },
		},
	}
	s.pubKey, s.privKey = relay.GenerateKeyPair()
	s.server = httptest.NewServer(http.HandlerFunc(s.handleWS))
	s.url = "ws" + s.server.URL[4:]
	t.Cleanup(s.server.Close)
	return s
}

func (s *miniRelayServer) pubKeyHex() string { return hex.EncodeToString(s.pubKey[:]) }

func (s *miniRelayServer) handleWS(w http.ResponseWriter, r *http.Request) {
	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer func() { _ = conn.Close() }()

	_, data, err := conn.ReadMessage()
	if err != nil {
		return
	}
	const (
		frameHeaderLen  = 5
		keyLen          = 32
		nonceLen        = 24
		frameClientAuth = 0x01
		frameServerAuth = 0x02
		frameSendPacket = 0x03
		frameRecvPacket = 0x04
		framePing       = 0x06
		framePong       = 0x07
	)
	if len(data) < frameHeaderLen || data[0] != frameClientAuth {
		return
	}
	plen := int(data[1])<<24 | int(data[2])<<16 | int(data[3])<<8 | int(data[4])
	payload := data[frameHeaderLen:]
	if len(payload) < plen || plen < keyLen+nonceLen+1 {
		return
	}
	payload = payload[:plen]

	var pubKey [32]byte
	var nonce [24]byte
	copy(pubKey[:], payload[:keyLen])
	copy(nonce[:], payload[keyLen:keyLen+nonceLen])
	sealed := payload[keyLen+nonceLen:]
	decoded, ok := box.Open(nil, sealed, &nonce, &pubKey, &s.privKey)
	if !ok || len(decoded) != keyLen {
		return
	}
	var claimed [32]byte
	copy(claimed[:], decoded)
	if claimed != pubKey {
		return
	}
	enc := func(ft byte, p []byte) []byte {
		b := make([]byte, 5+len(p))
		b[0] = ft
		b[1] = byte(len(p) >> 24)
		b[2] = byte(len(p) >> 16)
		b[3] = byte(len(p) >> 8)
		b[4] = byte(len(p))
		copy(b[5:], p)
		return b
	}
	_ = conn.WriteMessage(websocket.BinaryMessage, enc(frameServerAuth, []byte{0x00}))

	s.mu.Lock()
	s.clients[pubKey] = conn
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.clients, pubKey)
		s.mu.Unlock()
	}()

	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		if len(data) < 5 {
			continue
		}
		switch data[0] {
		case framePing:
			s.writeTo(conn, enc(framePong, nil))
		case frameSendPacket:
			if len(data) < 5+keyLen {
				continue
			}
			var dst [32]byte
			copy(dst[:], data[5:5+keyLen])
			s.mu.Lock()
			dstConn := s.clients[dst]
			s.mu.Unlock()
			if dstConn == nil {
				continue
			}
			recv := make([]byte, 0, keyLen+len(data)-5-keyLen)
			recv = append(recv, pubKey[:]...)
			recv = append(recv, data[5+keyLen:]...)
			s.writeTo(dstConn, enc(frameRecvPacket, recv))
		}
	}
}

// TestP2PSessionCloseKeepsSharedRelay: p2pSession.close() must NOT close
// the session-level shared relay client — pre-fix, one session's teardown
// black-holed relay for every other session and for all future adoptions
// (production 2026-10-08).
func TestP2PSessionCloseKeepsSharedRelay(t *testing.T) {
	mock := newMiniRelayServer(t)

	a := relay.NewClient()
	require.NoError(t, a.Connect(context.Background(), mock.url, mock.pubKeyHex()))
	defer a.Close()
	b := relay.NewClient()
	require.NoError(t, b.Connect(context.Background(), mock.url, mock.pubKeyHex()))
	defer b.Close()

	got := make(chan []byte, 1)
	b.OnMessage(func(data []byte) { got <- data })
	a.SetRemotePeer(b.PubKey())

	p2p := &p2pSession{sessionID: "s1", relayClient: a, relayActive: true}
	require.NoError(t, p2p.close())

	// The shared relay transport must still route A→B after the session
	// teardown.
	require.NoError(t, a.Send([]byte("still-alive")))
	select {
	case data := <-got:
		assert.Equal(t, "still-alive", string(data))
	case <-time.After(3 * time.Second):
		t.Fatal("shared relay client was killed by p2pSession.close()")
	}
}

// TestHandleRelayFatalTearsDownRelaySessions: a fatal shared relay client
// must (1) tear down only the sessions that adopted relay, (2) leave
// direct-transport sessions alone, (3) fire the session-close callback.
func TestHandleRelayFatalTearsDownRelaySessions(t *testing.T) {
	s := &Session{
		logger:  slog.Default(),
		peers:   make(map[string]*p2pSession),
		welcome: &Welcome{}, // empty RelayURL → rebuild is a no-op
		closeCh: make(chan struct{}),
		done:    make(chan struct{}),
	}
	relaySess := &p2pSession{sessionID: "relay-1", owner: s, relayActive: true}
	directSess := &p2pSession{sessionID: "direct-1", owner: s}
	s.peers["relay-1"] = relaySess
	s.peers["direct-1"] = directSess

	closed := make(chan string, 2)
	s.OnSessionClose = func(sessionID, _ string) { closed <- sessionID }

	s.handleRelayFatal(assert.AnError)

	select {
	case id := <-closed:
		assert.Equal(t, "relay-1", id)
	case <-time.After(2 * time.Second):
		t.Fatal("relay session close callback not fired")
	}
	s.mu.RLock()
	_, relayGone := s.peers["relay-1"]
	_, directAlive := s.peers["direct-1"]
	s.mu.RUnlock()
	assert.False(t, relayGone, "relay-riding session must be torn down")
	assert.True(t, directAlive, "direct session must survive")
}
