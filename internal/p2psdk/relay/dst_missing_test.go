package relay

// #160 client-side tests for FrameDstMissing: the hint frame the server
// sends when a send_packet was dropped for lack of a local data-leg
// registration.

import (
	"context"
	"encoding/hex"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestClientDstMissingCallback: the frame delivers the 8-byte destination
// prefix to the registered handler.
func TestClientDstMissingCallback(t *testing.T) {
	mock := newMockRelayServer(t)
	pubKeyHex := hex.EncodeToString(mock.pubKey[:])

	client := NewClient()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, client.Connect(ctx, mock.url, pubKeyHex))
	defer func() { _ = client.Close() }()

	want := [DstMissingPrefixLen]byte{0xD1, 0xE2, 0xA3, 0xD4, 0xE5, 0xB6, 0xC7, 0xD8}
	got := make(chan [DstMissingPrefixLen]byte, 1)
	client.OnDstMissing(func(prefix [DstMissingPrefixLen]byte) { got <- prefix })

	require.True(t, mock.pushTo(client.PubKey(), encodeFrame(FrameDstMissing, want[:])))

	select {
	case prefix := <-got:
		assert.Equal(t, want, prefix)
	case <-time.After(3 * time.Second):
		t.Fatal("dst_missing frame must fire the callback")
	}
}

// TestClientDstMissingShortPayloadIgnored: a truncated payload must not fire
// the callback (defensive parse, matching the PeerGone branch).
func TestClientDstMissingShortPayloadIgnored(t *testing.T) {
	mock := newMockRelayServer(t)
	pubKeyHex := hex.EncodeToString(mock.pubKey[:])

	client := NewClient()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, client.Connect(ctx, mock.url, pubKeyHex))
	defer func() { _ = client.Close() }()

	fired := make(chan struct{}, 1)
	client.OnDstMissing(func([DstMissingPrefixLen]byte) { fired <- struct{}{} })

	require.True(t, mock.pushTo(client.PubKey(), encodeFrame(FrameDstMissing, []byte{0x01, 0x02})))

	select {
	case <-fired:
		t.Fatal("short payload must not fire the callback")
	case <-time.After(400 * time.Millisecond):
	}
}

// TestClientDstMissingNoCallbackNoPanic: an unregistered handler (the
// pre-#160 caller) must simply ignore the frame.
func TestClientDstMissingNoCallbackNoPanic(t *testing.T) {
	mock := newMockRelayServer(t)
	pubKeyHex := hex.EncodeToString(mock.pubKey[:])

	client := NewClient()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, client.Connect(ctx, mock.url, pubKeyHex))
	defer func() { _ = client.Close() }()

	require.True(t, mock.pushTo(client.PubKey(), encodeFrame(FrameDstMissing,
		[]byte{1, 2, 3, 4, 5, 6, 7, 8})))

	// The read loop must survive an unhandled frame: a subsequent ping still
	// gets its pong (proving the loop is alive).
	assert.Eventually(t, func() bool {
		return mock.pushTo(client.PubKey(), encodeFrame(FramePing, nil))
	}, time.Second, 50*time.Millisecond)
	time.Sleep(100 * time.Millisecond) // let the read loop consume both frames
}
