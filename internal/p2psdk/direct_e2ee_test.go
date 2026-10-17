package peer

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/p2psdk/relay"
)

// TestConnectRequestCallerRelayKeyFieldCompat (mibeescout#16): the wire
// field is additive — JSON from an old caller (field absent) decodes empty,
// so an old caller never arms a new device.
func TestConnectRequestCallerRelayKeyFieldCompat(t *testing.T) {
	var req ConnectRequest
	require.NoError(t, json.Unmarshal([]byte(`{
		"target_peer_id": "device-1",
		"session_id": "s1",
		"service_name": "echo"
	}`), &req))
	assert.Empty(t, req.CallerRelayPubKey, "absent field must decode empty (old caller)")

	out, err := json.Marshal(ConnectRequest{TargetPeerID: "d", SessionID: "s", CallerRelayPubKey: "ab"})
	require.NoError(t, err)
	assert.Contains(t, string(out), `"caller_relay_pub_key":"ab"`)
}

// TestConnectArmsDeviceE2EEBeforeSDP (mibeescout#16): the caller's relay
// identity key rides the connect_request, so the DEVICE-side offerer's
// session is armed at creation — before any SDP could carry the key. This
// is the direct-path plaintext fix: e2eeKey() must be derivable with zero
// SDP round-trips.
func TestConnectArmsDeviceE2EEBeforeSDP(t *testing.T) {
	mock := newMockServer(t)

	device, err := Dial(context.Background(), SignalingConfig{
		ServerURL: mock.url,
		JWT:       "test-device-jwt",
		ProductID: "test-product",
		PeerID:    "device-1",
		Role:      "device",
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = device.Close() })
	device.relayClient = relay.NewClient()

	clientRC := relay.NewClient()
	callerPub := clientRC.PubKey()

	client, err := Dial(context.Background(), SignalingConfig{
		ServerURL: mock.url,
		JWT:       "test-client-jwt",
		ProductID: "test-product",
		PeerID:    "client-1",
		Role:      "client",
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })
	client.relayClient = clientRC

	require.NoError(t, client.Connect(context.Background(), "device-1"))

	// The device session must exist with the caller's key armed — poll
	// briefly for the forwarded connect_request.
	deadline := time.Now().Add(5 * time.Second)
	for {
		device.mu.Lock()
		var p2p *p2pSession
		for _, v := range device.peers {
			p2p = v
		}
		device.mu.Unlock()
		if p2p != nil {
			p2p.mu.Lock()
			armedKey := p2p.remoteRelayKey
			p2p.mu.Unlock()
			require.Equal(t, callerPub, armedKey,
				"caller relay key must be armed from connect_request (before SDP)")
			require.NotNil(t, p2p.e2eeKey(),
				"device-side E2EE must be derivable with zero SDP round-trips")
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("device session never appeared after connect_request")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
