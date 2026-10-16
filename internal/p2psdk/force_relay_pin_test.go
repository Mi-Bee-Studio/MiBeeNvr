package peer

import (
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/p2psdk/relay"
)

// TestWatchTransportForceRelayPinsImmediately：ForceRelay=true 且 relay
// 客户端可用 → watchTransport 不进入 ICE 仲裁，立即采用 relay 传输
// （#18 验收第 1 条的代码级证据：pinned 分支不等待任何直连事件）。
func TestWatchTransportForceRelayPinsImmediately(t *testing.T) {
	p2p := &p2pSession{
		sessionID:   "pin-1",
		relayClient: &relay.Client{},
		owner:       &Session{logger: slog.Default()},
		logger:      slog.New(slog.DiscardHandler),
	}

	done := make(chan struct{})
	go func() {
		p2p.watchTransport(&signalingClient{}, SignalingConfig{ForceRelay: true})
		close(done)
	}()

	deadline := time.After(3 * time.Second)
	for {
		p2p.mu.Lock()
		active := p2p.relayActive
		p2p.mu.Unlock()
		if active {
			break
		}
		select {
		case <-deadline:
			t.Fatal("ForceRelay pin must adopt the relay transport without waiting for ICE arbitration")
		case <-done:
			t.Fatal("watchTransport returned before adopting relay")
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}

	// watchTransport returns right after the adoption (nil owner callbacks
	// exercised too — the point is no panic and the adoption holding).
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("watchTransport did not return after adoption")
	}
	p2p.mu.Lock()
	defer p2p.mu.Unlock()
	assert.True(t, p2p.relayActive, "relay transport must stay adopted")
}
