package peer

// 熔断闸门语义（#23/#24）在上游 SDK 结构下的形态。
//
// 上游把所有建连路径收敛成 `Session.dialRelay` / `rebuildRelayClient`，
// 不再有独立的 connectRelayClient/ErrRelayGated。闸门仍是
// SignalingConfig.RelayDialGate：关闭时该次尝试**完全不发生**——不建
// socket、不跑认证，也不得触发 OnRelayConnect（一次没有发生的尝试不可能
// 失败）。

import (
	"io"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func gateTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// newGateSession 造一个只够跑 dialRelay 的 Session（无真实信令）。
func newGateSession(cfg SignalingConfig) *Session {
	if cfg.Logger == nil {
		cfg.Logger = gateTestLogger()
	}
	return &Session{
		cfg:     cfg,
		logger:  gateTestLogger().With("component", "p2p_session"),
		closeCh: make(chan struct{}),
		peers:   make(map[string]*p2pSession),
	}
}

func gateTestWelcome() *Welcome {
	return &Welcome{
		RelayURL:    "wss://127.0.0.1:1/relay",
		RelayPubKey: strings.Repeat("ab", 32),
	}
}

// TestRelayGateSkipsConnect 钉死闸门语义：关闭时完全不拨号，且不得回报
// 建连结果。
func TestRelayGateSkipsConnect(t *testing.T) {
	var reported atomic.Int32
	s := newGateSession(SignalingConfig{
		RelayDialGate:  func() bool { return false },
		OnRelayConnect: func(error) { reported.Add(1) },
	})

	s.dialRelay(t.Context(), gateTestWelcome(), s.logger)

	if got := reported.Load(); got != 0 {
		t.Fatalf("OnRelayConnect fired %d time(s) for a gate-skipped dial, want 0", got)
	}
	if s.relayClient != nil {
		t.Fatal("gated dial must not install a relay client")
	}
}

// TestRelayGateAllowsConnect 闸门放行时必须真的尝试（这里对死端点，因此
// 必然失败并回报一次错误）——证明闸门不是无条件短路。
func TestRelayGateAllowsConnect(t *testing.T) {
	var gotErr atomic.Bool
	var reported atomic.Int32
	s := newGateSession(SignalingConfig{
		RelayDialGate: func() bool { return true },
		OnRelayConnect: func(err error) {
			reported.Add(1)
			if err != nil {
				gotErr.Store(true)
			}
		},
	})

	s.dialRelay(t.Context(), gateTestWelcome(), s.logger)

	if got := reported.Load(); got != 1 {
		t.Fatalf("OnRelayConnect fired %d time(s) for an allowed dial, want 1", got)
	}
	if !gotErr.Load() {
		t.Fatal("dial against a dead endpoint must report a failure")
	}
	if s.relayClient != nil {
		t.Fatal("failed dial must not install a relay client")
	}
}

// TestRelayRebuildHonorsGate 重建梯子逐次过闸：闸门关闭时立即退出（不拨
// 号、不睡眠），relayClient 保持 nil。
func TestRelayRebuildHonorsGate(t *testing.T) {
	s := newGateSession(SignalingConfig{RelayDialGate: func() bool { return false }})
	s.welcome = gateTestWelcome()

	done := make(chan struct{})
	go func() { s.rebuildRelayClient(nil); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("gated rebuild did not return promptly")
	}
	if s.relayClient != nil {
		t.Fatal("gated rebuild must not install a relay client")
	}
}
