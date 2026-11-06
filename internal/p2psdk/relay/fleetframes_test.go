package relay

// #106-B client-side fleet frame tests: FrameRestarting (typed fatal with
// the retry hint), close 4009 mapping, FramePeerGone callback, RetryHint.

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pushTo writes one raw frame to a registered client's connection (server
// push for restarting / peer_gone).
func (s *mockRelayServer) pushTo(key [32]byte, frame []byte) bool {
	s.mu.Lock()
	c := s.clients[key]
	s.mu.Unlock()
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.conn.WriteMessage(websocket.BinaryMessage, frame) == nil
}

// closeWith pushes a WS close frame with the given status.
func (s *mockRelayServer) closeWith(key [32]byte, code int, reason string) bool {
	s.mu.Lock()
	c := s.clients[key]
	s.mu.Unlock()
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.conn.WriteControl(websocket.CloseMessage,
		websocket.FormatCloseMessage(code, reason),
		time.Now().Add(time.Second)) == nil
}

// TestClientRestartingFrameFiresHintedFatal: FrameRestarting fails the
// transport with *RestartingError carrying the server's retry hint.
func TestClientRestartingFrameFiresHintedFatal(t *testing.T) {
	mock := newMockRelayServer(t)
	pubKeyHex := hex.EncodeToString(mock.pubKey[:])

	client := NewClient()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, client.Connect(ctx, mock.url, pubKeyHex))
	defer func() { _ = client.Close() }()

	fatal := make(chan error, 1)
	client.OnFatal(func(err error) { fatal <- err })

	payload := make([]byte, 2)
	payload[0] = 0x05 // 1500ms = 0x05DC
	payload[1] = 0xDC
	require.True(t, mock.pushTo(client.PubKey(), encodeFrame(FrameRestarting, payload)))

	select {
	case err := <-fatal:
		var re *RestartingError
		require.ErrorAs(t, err, &re, "fatal must be the typed restarting error, got %v", err)
		assert.Equal(t, 1500*time.Millisecond, re.RetryAfter)
		hint, ok := RetryHint(err)
		require.True(t, ok)
		assert.Equal(t, 1500*time.Millisecond, hint)
	case <-time.After(3 * time.Second):
		t.Fatal("restarting frame must fire the fatal callback")
	}
}

// TestClientClose4009FiresRestarting: a 4009 close without a preceding
// frame still maps to the typed error with the conservative default hint.
func TestClientClose4009FiresRestarting(t *testing.T) {
	mock := newMockRelayServer(t)
	pubKeyHex := hex.EncodeToString(mock.pubKey[:])

	client := NewClient()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, client.Connect(ctx, mock.url, pubKeyHex))
	defer func() { _ = client.Close() }()

	fatal := make(chan error, 1)
	client.OnFatal(func(err error) { fatal <- err })

	require.True(t, mock.closeWith(client.PubKey(), closeStatusRestarting, "relay_restarting"))

	select {
	case err := <-fatal:
		var re *RestartingError
		require.ErrorAs(t, err, &re, "4009 close must map to the restarting error, got %v", err)
		assert.Equal(t, defaultRestartingRetryAfter, re.RetryAfter)
	case <-time.After(3 * time.Second):
		t.Fatal("4009 close must fire the fatal callback")
	}
}

// TestClientPeerGoneCallback: FramePeerGone delivers the peer key and the
// reason byte to the registered handler.
func TestClientPeerGoneCallback(t *testing.T) {
	mock := newMockRelayServer(t)
	pubKeyHex := hex.EncodeToString(mock.pubKey[:])

	client := NewClient()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, client.Connect(ctx, mock.url, pubKeyHex))
	defer func() { _ = client.Close() }()

	var peerKey [32]byte
	for i := range peerKey {
		peerKey[i] = byte(i + 1)
	}
	gone := make(chan byte, 1)
	client.OnPeerGone(func(peer [32]byte, reason byte) {
		assert.Equal(t, peerKey, peer)
		gone <- reason
	})

	payload := make([]byte, KeyLen+1)
	copy(payload[:KeyLen], peerKey[:])
	payload[KeyLen] = PeerGoneIdleReaped
	require.True(t, mock.pushTo(client.PubKey(), encodeFrame(FramePeerGone, payload)))

	select {
	case reason := <-gone:
		assert.Equal(t, PeerGoneIdleReaped, reason)
	case <-time.After(3 * time.Second):
		t.Fatal("peer_gone frame must fire the callback")
	}
}

// TestRetryHintNegative: ordinary errors carry no hint; wrapped
// restarting errors still do.
func TestRetryHintNegative(t *testing.T) {
	_, ok := RetryHint(errors.New("ordinary failure"))
	assert.False(t, ok)
	wrapped := fmt.Errorf("read: %w", &RestartingError{RetryAfter: time.Second})
	hint, ok := RetryHint(wrapped)
	require.True(t, ok, "wrapped restarting errors must still carry the hint")
	assert.Equal(t, time.Second, hint)
}
