package relay

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/curve25519"
	"golang.org/x/crypto/nacl/box"
)

// mockRelayServer is an in-process relay server for testing.
type mockRelayServer struct {
	server   *httptest.Server
	url      string
	upgrader websocket.Upgrader
	pubKey   [32]byte
	privKey  [32]byte
	mu       sync.Mutex
	clients  map[[32]byte]*mockClient

	// silent: when set, the read loop answers nothing (no pong) and never
	// pushes — simulates a black-holed / half-open path for liveness tests.
	silent atomic.Bool
}

type mockClient struct {
	pubKey [32]byte
	conn   *websocket.Conn
	mu     sync.Mutex
}

func newMockRelayServer(t *testing.T) *mockRelayServer {
	s := &mockRelayServer{
		clients: make(map[[32]byte]*mockClient),
		upgrader: websocket.Upgrader{
			CheckOrigin: func(_ *http.Request) bool { return true },
		},
	}
	s.pubKey, s.privKey = GenerateKeyPair()
	s.server = httptest.NewServer(http.HandlerFunc(s.handleWS))
	s.url = "ws" + s.server.URL[4:]
	if t != nil {
		t.Cleanup(s.server.Close)
	}
	return s
}

//nolint:funlen // test mock function
func (s *mockRelayServer) handleWS(w http.ResponseWriter, r *http.Request) {
	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer func() { _ = conn.Close() }()

	// Read auth frame.
	_, data, err := conn.ReadMessage()
	if err != nil {
		return
	}

	ft, payload, err := readFrame(data)
	if err != nil || ft != FrameClientAuth {
		return
	}

	// Parse client auth.
	if len(payload) < KeyLen+NonceLen {
		return
	}
	var msg struct {
		pubKey [32]byte
		nonce  [24]byte
		box    []byte
	}
	copy(msg.pubKey[:], payload[:KeyLen])
	copy(msg.nonce[:], payload[KeyLen:KeyLen+NonceLen])
	msg.box = payload[KeyLen+NonceLen:]

	// Verify.
	decoded, ok := box.Open(nil, msg.box, &msg.nonce, &msg.pubKey, &s.privKey)
	if !ok || len(decoded) != KeyLen {
		_ = conn.WriteMessage(websocket.BinaryMessage, encodeFrame(FrameServerAuth, []byte{0x01}))
		return
	}
	var claimedKey [32]byte
	copy(claimedKey[:], decoded)
	if claimedKey != msg.pubKey {
		_ = conn.WriteMessage(websocket.BinaryMessage, encodeFrame(FrameServerAuth, []byte{0x01}))
		return
	}

	// Register BEFORE the auth reply: the client's Connect returns the
	// moment it reads the reply, and peers may send (or tests may close
	// conns) immediately — an unregistered key made those races silently
	// drop frames (flaky TestClientSendAndReceive/TestClientServerCloseFiresFatal).
	client := &mockClient{pubKey: msg.pubKey, conn: conn}
	s.mu.Lock()
	s.clients[msg.pubKey] = client
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		delete(s.clients, msg.pubKey)
		s.mu.Unlock()
	}()

	// Success (under client.mu: a concurrent forward to this freshly
	// registered conn would race gorilla's single-writer constraint).
	client.mu.Lock()
	_ = conn.WriteMessage(websocket.BinaryMessage, encodeFrame(FrameServerAuth, []byte{0x00}))
	client.mu.Unlock()

	// Read loop.
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		ft, payload, err := readFrame(data)
		if err != nil {
			continue
		}
		if ft == FramePing {
			if !s.silent.Load() {
				client.mu.Lock()
				_ = conn.WriteMessage(websocket.BinaryMessage, encodeFrame(FramePong, nil))
				client.mu.Unlock()
			}
			continue
		}
		if ft != FrameSendPacket || len(payload) < KeyLen {
			continue
		}

		var dstKey [32]byte
		copy(dstKey[:], payload[:KeyLen])

		s.mu.Lock()
		dst, ok := s.clients[dstKey]
		s.mu.Unlock()
		if !ok {
			continue
		}

		// Forward as RecvPacket: [0:32] source key, [32:] data.
		recvPayload := make([]byte, KeyLen+len(payload)-KeyLen)
		copy(recvPayload[:KeyLen], msg.pubKey[:])
		copy(recvPayload[KeyLen:], payload[KeyLen:])
		frame := encodeFrame(FrameRecvPacket, recvPayload)
		dst.mu.Lock()
		_ = dst.conn.WriteMessage(websocket.BinaryMessage, frame)
		dst.mu.Unlock()
	}
}

func TestGenerateKeyPair(t *testing.T) {
	pub, priv := GenerateKeyPair()
	// Public key should not be zero.
	assert.NotEqual(t, [32]byte{}, pub)
	// Private key should not be zero.
	assert.NotEqual(t, [32]byte{}, priv)
	// Keys should be different.
	assert.NotEqual(t, pub, priv)
}

func TestKeyPairDeterminism(t *testing.T) {
	// Two calls should produce different keys.
	pub1, priv1 := GenerateKeyPair()
	pub2, priv2 := GenerateKeyPair()
	assert.NotEqual(t, pub1, pub2)
	assert.NotEqual(t, priv1, priv2)
}

func TestFrameEncodeDecode(t *testing.T) {
	tests := []struct {
		name    string
		ft      byte
		payload []byte
	}{
		{"client_auth", FrameClientAuth, []byte("hello")},
		{"server_auth", FrameServerAuth, []byte{0x00}},
		{"send_packet", FrameSendPacket, make([]byte, KeyLen+10)},
		{"recv_packet", FrameRecvPacket, make([]byte, KeyLen+5)},
		{"keepalive", FrameKeepAlive, []byte{}},
		{"ping", FramePing, []byte{}},
		{"pong", FramePong, []byte{}},
		{"empty_payload", FrameSendPacket, []byte{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			frame := encodeFrame(tt.ft, tt.payload)
			ft, decoded, err := readFrame(frame)
			require.NoError(t, err)
			assert.Equal(t, tt.ft, ft)
			assert.Equal(t, tt.payload, decoded)
		})
	}
}

func TestFrameTruncated(t *testing.T) {
	_, _, err := readFrame([]byte{0x01, 0x00, 0x00, 0x00})
	require.Error(t, err)
}

func TestFrameTooLarge(t *testing.T) {
	payload := make([]byte, MaxPacketSize+1)
	frame := encodeFrame(FrameSendPacket, payload)
	_, _, err := readFrame(frame)
	require.Error(t, err)
}

func TestClientConnectAndAuth(t *testing.T) {
	mock := newMockRelayServer(t)
	pubKeyHex := hex.EncodeToString(mock.pubKey[:])

	client := NewClient()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := client.Connect(ctx, mock.url, pubKeyHex)
	require.NoError(t, err)
	defer func() { _ = client.Close() }()

	// Verify the client has a non-zero pub key.
	assert.NotEqual(t, [32]byte{}, client.PubKey())
}

func TestClientRejectWrongKey(t *testing.T) {
	mock := newMockRelayServer(t)
	// Use a wrong public key.
	wrongKey := hex.EncodeToString([]byte{
		0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08,
		0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x10,
		0x11, 0x12, 0x13, 0x14, 0x15, 0x16, 0x17, 0x18,
		0x19, 0x1a, 0x1b, 0x1c, 0x1d, 0x1e, 0x1f, 0x20,
	})

	client := NewClient()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := client.Connect(ctx, mock.url, wrongKey)
	require.Error(t, err)
}

func TestClientSendAndReceive(t *testing.T) {
	mock := newMockRelayServer(t)
	pubKeyHex := hex.EncodeToString(mock.pubKey[:])

	// Create two clients.
	clientA := NewClient()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := clientA.Connect(ctx, mock.url, pubKeyHex)
	require.NoError(t, err)
	defer func() { _ = clientA.Close() }()

	clientB := NewClient()
	err = clientB.Connect(ctx, mock.url, pubKeyHex)
	require.NoError(t, err)
	defer func() { _ = clientB.Close() }()

	// Exchange public keys.
	clientA.SetRemotePeer(clientB.PubKey())
	clientB.SetRemotePeer(clientA.PubKey())

	// Set up B to receive.
	received := make(chan []byte, 1)
	clientB.OnMessage(func(data []byte) {
		received <- data
	})

	// A sends to B.
	err = clientA.Send([]byte("hello through relay"))
	require.NoError(t, err)

	select {
	case msg := <-received:
		assert.Equal(t, "hello through relay", string(msg))
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for message")
	}

	// B sends to A.
	receivedA := make(chan []byte, 1)
	clientA.OnMessage(func(data []byte) {
		receivedA <- data
	})
	err = clientB.Send([]byte("reply from B"))
	require.NoError(t, err)

	select {
	case msg := <-receivedA:
		assert.Equal(t, "reply from B", string(msg))
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for reply")
	}
}

func TestClientSendWithoutRemoteKey(t *testing.T) {
	mock := newMockRelayServer(t)
	pubKeyHex := hex.EncodeToString(mock.pubKey[:])

	client := NewClient()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := client.Connect(ctx, mock.url, pubKeyHex)
	require.NoError(t, err)
	defer func() { _ = client.Close() }()

	err = client.Send([]byte("no key set"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "remote peer key not set")
}

func TestClientDoubleClose(t *testing.T) {
	mock := newMockRelayServer(t)
	pubKeyHex := hex.EncodeToString(mock.pubKey[:])

	client := NewClient()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := client.Connect(ctx, mock.url, pubKeyHex)
	require.NoError(t, err)

	// Close twice should not panic.
	assert.NoError(t, client.Close())
	assert.NoError(t, client.Close())
}

func TestClientPubKey(t *testing.T) {
	pub, priv := GenerateKeyPair()
	_ = priv // priv not needed for this test

	client := &Client{pubKey: pub}
	got := client.PubKey()
	assert.Equal(t, pub, got)
}

// fastLivenessClient builds a client with test-speed liveness intervals.
func fastLivenessClient(ping, pong, write time.Duration) *Client {
	c := NewClient()
	c.pingInterval = ping
	c.pongTimeout = pong
	c.writeTimeout = write
	return c
}

// TestClientKeepaliveDetectsSilentServer: a black-holed path (server
// receives pings but answers nothing, never sends) must surface as a fatal
// within the pong window — pre-liveness this sat silently forever while the
// tunnel wrote into the void (production 2026-10-08).
func TestClientKeepaliveDetectsSilentServer(t *testing.T) {
	mock := newMockRelayServer(t)
	mock.silent.Store(true)

	client := fastLivenessClient(40*time.Millisecond, 150*time.Millisecond, time.Second)
	fatal := make(chan error, 1)
	client.OnFatal(func(err error) { fatal <- err })
	require.NoError(t, client.Connect(t.Context(), mock.url, hex.EncodeToString(mock.pubKey[:])))

	select {
	case err := <-fatal:
		require.Error(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("silent server not detected within pong window")
	}
}

// TestClientPingPongKeepsAlive: with the server answering pongs, the
// connection must stay healthy across several ping cycles.
func TestClientPingPongKeepsAlive(t *testing.T) {
	mock := newMockRelayServer(t)

	client := fastLivenessClient(40*time.Millisecond, 500*time.Millisecond, time.Second)
	fatal := make(chan error, 1)
	client.OnFatal(func(err error) { fatal <- err })
	require.NoError(t, client.Connect(t.Context(), mock.url, hex.EncodeToString(mock.pubKey[:])))
	defer func() { _ = client.Close() }()

	select {
	case err := <-fatal:
		t.Fatalf("healthy connection declared fatal: %v", err)
	case <-time.After(400 * time.Millisecond):
		// survived ~10 ping cycles — good.
	}
}

// TestClientServerCloseFiresFatal: an abrupt server-side close must reach
// the fatal callback (read error path, not closeCh).
func TestClientServerCloseFiresFatal(t *testing.T) {
	mock := newMockRelayServer(t)

	client := fastLivenessClient(40*time.Millisecond, 500*time.Millisecond, time.Second)
	fatal := make(chan error, 1)
	client.OnFatal(func(err error) { fatal <- err })
	require.NoError(t, client.Connect(t.Context(), mock.url, hex.EncodeToString(mock.pubKey[:])))

	// Close the accepted server-side connection directly (httptest
	// Server.Close waits on stuck handlers — closing the specific conn is
	// the deterministic equivalent).
	mock.mu.Lock()
	for _, c := range mock.clients {
		_ = c.conn.Close()
	}
	mock.mu.Unlock()

	select {
	case err := <-fatal:
		require.Error(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("server close not detected")
	}
}

// TestClientDeliberateCloseNoFatal: Close() is deliberate teardown — it
// must not fire the fatal callback.
func TestClientDeliberateCloseNoFatal(t *testing.T) {
	mock := newMockRelayServer(t)

	client := fastLivenessClient(40*time.Millisecond, 500*time.Millisecond, time.Second)
	fatal := make(chan error, 1)
	client.OnFatal(func(err error) { fatal <- err })
	require.NoError(t, client.Connect(t.Context(), mock.url, hex.EncodeToString(mock.pubKey[:])))
	require.NoError(t, client.Close())

	select {
	case err := <-fatal:
		t.Fatalf("deliberate Close fired fatal: %v", err)
	case <-time.After(300 * time.Millisecond):
		// clean — no false alarm.
	}
}

// TestAuthenticate_VerifiesServerAuthProof pins #64: when server_auth
// carries the identity proof, a matching server key verifies and a foreign
// (MITM-substituted) key fails closed.
func TestAuthenticate_VerifiesServerAuthProof(t *testing.T) {
	clientPub, clientPriv := GenerateKeyPair()
	_, serverPriv := GenerateKeyPair()
	var serverPub [32]byte
	curve25519.ScalarBaseMult(&serverPub, &serverPriv)

	var clientNonce [NonceLen]byte
	for i := range clientNonce {
		clientNonce[i] = byte(i)
	}

	buildProof := func(sealerPriv [32]byte) []byte {
		var proofNonce [NonceLen]byte
		for i := range proofNonce {
			proofNonce[i] = byte(0xC0 + i)
		}
		bind := sha256.Sum256(append(append([]byte{}, clientNonce[:]...), clientPub[:]...))
		sealed := box.Seal(nil, bind[:], &proofNonce, &clientPub, &sealerPriv)
		out := append([]byte{}, proofNonce[:]...)
		return append(out, sealed...)
	}

	c := NewClient()
	c.pubKey, c.privKey = clientPub, clientPriv
	c.serverPubKey = serverPub

	// Genuine server key: proof verifies.
	if err := c.verifyServerAuthProof(buildProof(serverPriv), clientNonce); err != nil {
		t.Fatalf("genuine proof must verify: %v", err)
	}

	// Foreign key (MITM holding a different relay key): fail closed.
	var strangerPriv [32]byte
	strangerPriv[0] = 0x99
	err := c.verifyServerAuthProof(buildProof(strangerPriv), clientNonce)
	if err == nil {
		t.Fatal("foreign-key proof must be rejected (fail closed)")
	}

	// Tampered bind (nonce substitution replay): fail closed.
	tampered := buildProof(serverPriv)
	tampered[len(tampered)-1] ^= 0xFF
	if err := c.verifyServerAuthProof(tampered, clientNonce); err == nil {
		t.Fatal("tampered proof must be rejected")
	}
}
