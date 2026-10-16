package relay

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCtrlLinkEstablishedAndUsed (#88②): when the server advertises
// FrameCapDualConn, the client opens a second ctrl connection with the
// same key pair, and SendPriorityTo rides it (the frame shows up on the
// mock's ctrl lane, not the data lane).
func TestCtrlLinkEstablishedAndUsed(t *testing.T) {
	mock := newMockRelayServer(t)
	mock.caps = FrameCapPriority | FrameCapDualConn

	client := NewClient()
	t.Cleanup(func() { _ = client.Close() })
	require.NoError(t, client.Connect(t.Context(), mock.url, pubKeyHex(&mock.pubKey)))
	require.True(t, client.SupportsDualConn())

	// The ctrl dial is async — wait for it.
	require.Eventually(t, client.CtrlLinkActive, 5*time.Second, 20*time.Millisecond,
		"ctrl link must come up after Connect")

	client.SetRemotePeer(mock.pubKey) // destination irrelevant on the mock
	require.NoError(t, client.SendPriorityTo(mock.pubKey, []byte("via-ctrl")))

	select {
	case data := <-mock.ctrlFrames:
		ft, payload, err := readFrame(data)
		require.NoError(t, err)
		assert.Equal(t, FrameSendPacket|FramePriorityBit, ft)
		assert.Equal(t, "via-ctrl", string(payload[KeyLen:]))
	case <-time.After(3 * time.Second):
		t.Fatal("priority frame never reached the mock ctrl lane")
	}
}

// TestCtrlLinkNotDialedWithoutCapability (#88②): a legacy server (no
// capability byte) never triggers a second connection.
func TestCtrlLinkNotDialedWithoutCapability(t *testing.T) {
	mock := newMockRelayServer(t) // caps = 0 → legacy 1-byte server_auth

	client := NewClient()
	t.Cleanup(func() { _ = client.Close() })
	require.NoError(t, client.Connect(t.Context(), mock.url, pubKeyHex(&mock.pubKey)))
	assert.False(t, client.SupportsDualConn())

	time.Sleep(300 * time.Millisecond) // the dial would be immediate if wrong
	assert.False(t, client.CtrlLinkActive(), "no ctrl dial without the capability bit")

	mock.ctrlMu.Lock()
	n := len(mock.ctrlConns)
	mock.ctrlMu.Unlock()
	assert.Zero(t, n, "mock must not have seen a ctrl connection")
}

// pubKeyHex renders a key as hex for Connect.
func pubKeyHex(k *[32]byte) string {
	const hexdigits = "0123456789abcdef"
	out := make([]byte, 0, KeyLen*2)
	for _, b := range k {
		out = append(out, hexdigits[b>>4], hexdigits[b&0x0f])
	}
	return string(out)
}
