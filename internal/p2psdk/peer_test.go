package peer

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

//nolint:funlen // test function
func TestSDK_TwoPeersDirectConnect(t *testing.T) {
	mock := newMockServer(t)

	deviceDCOpen := make(chan struct{}, 1)
	clientDCOpen := make(chan struct{}, 1)
	deviceMsg := make(chan []byte, 10)
	clientMsg := make(chan []byte, 10)

	// Connect device
	device, err := Dial(context.Background(), SignalingConfig{
		ServerURL: mock.url,
		JWT:       "test-device-jwt",
		ProductID: "test-product",
		PeerID:    "device-1",
		Role:      "device",
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = device.Close() })

	device.OnDataChannel = func(dc DataChannel) {
		t.Log("device: DataChannel opened (dtls)")
		dc.OnMessage(func(data []byte) {
			deviceMsg <- data
		})
		deviceDCOpen <- struct{}{}
	}
	device.OnSession = func() { t.Log("device: session established") }

	// Connect client
	client, err := Dial(context.Background(), SignalingConfig{
		ServerURL: mock.url,
		JWT:       "test-client-jwt",
		ProductID: "test-product",
		PeerID:    "client-1",
		Role:      "client",
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })

	client.OnDataChannel = func(dc DataChannel) {
		t.Log("client: DataChannel opened (dtls)")
		dc.OnMessage(func(data []byte) {
			clientMsg <- data
		})
		clientDCOpen <- struct{}{}
	}
	client.OnSession = func() { t.Log("client: session established") }

	// Initiate connection
	err = client.Connect(context.Background(), "device-1")
	require.NoError(t, err)

	// Wait for device DataChannel
	select {
	case <-deviceDCOpen:
	case <-time.After(30 * time.Second):
		t.Fatal("timeout waiting for device DataChannel")
	}

	// Wait for client DataChannel
	select {
	case <-clientDCOpen:
	case <-time.After(30 * time.Second):
		t.Fatal("timeout waiting for client DataChannel")
	}

	// Client sends to device
	err = sendOnFirstDC(client, []byte("hello from client"))
	require.NoError(t, err)
	select {
	case msg := <-deviceMsg:
		assert.Equal(t, "hello from client", string(msg))
	case <-time.After(10 * time.Second):
		t.Fatal("timeout: device did not receive message")
	}

	// Device sends to client
	err = sendOnFirstDC(device, []byte("hello from device"))
	require.NoError(t, err)
	select {
	case msg := <-clientMsg:
		assert.Equal(t, "hello from device", string(msg))
	case <-time.After(10 * time.Second):
		t.Fatal("timeout: client did not receive message")
	}

	t.Log("P2P connection successful: DataChannel opened (dtls) and messages exchanged")
}

func sendOnFirstDC(s *Session, data []byte) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, p2p := range s.peers {
		if p2p.dc != nil {
			return p2p.dc.Send(data)
		}
	}
	return errors.New("no data channel found")
}

func TestSDK_DialValidation(t *testing.T) {
	tests := []struct {
		name    string
		cfg     SignalingConfig
		wantErr string
	}{
		{"no url", SignalingConfig{JWT: "j", ProductID: "p", PeerID: "p", Role: "d"}, "URL is required"},
		{"no jwt", SignalingConfig{ServerURL: "ws://x", ProductID: "p", PeerID: "p", Role: "d"}, "JWT is required"},
		{"no product", SignalingConfig{ServerURL: "ws://x", JWT: "j", PeerID: "p", Role: "d"}, "product ID is required"},
		{"no peer", SignalingConfig{ServerURL: "ws://x", JWT: "j", ProductID: "p", Role: "d"}, "peer ID is required"},
		{"no role", SignalingConfig{ServerURL: "ws://x", JWT: "j", ProductID: "p", PeerID: "p"}, "role is required"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Dial(context.Background(), tt.cfg)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func BenchmarkSDKDial(b *testing.B) {
	mock := newMockServer(nil)
	for b.Loop() {
		s, err := Dial(context.Background(), SignalingConfig{
			ServerURL: mock.url,
			JWT:       "test-jwt",
			ProductID: "test-product",
			PeerID:    fmt.Sprintf("bench-%d", b.N),
			Role:      "device",
		})
		if err == nil {
			_ = s.Close()
		}
	}
}
