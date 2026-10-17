package peer

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func gateTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// TestRelayGateSkipsConnect 钉死 RelayDialGate 语义（#23）：闸门关闭时
// 完全不拨号——返回 (nil, nil)，且 OnRelayConnect 不得触发（一次没有
// 发生的尝试不可能失败）。
func TestRelayGateSkipsConnect(t *testing.T) {
	called := false
	cfg := SignalingConfig{
		Logger:         gateTestLogger(),
		RelayDialGate:  func() bool { return false },
		OnRelayConnect: func(err error) { called = true },
	}
	rc, err := connectSessionRelay(context.Background(),
		"wss://127.0.0.1:1/relay", strings.Repeat("ab", 32), cfg, gateTestLogger())
	if !errors.Is(err, ErrRelayGated) {
		t.Fatalf("gated dial must return ErrRelayGated, got %v", err)
	}
	if rc != nil {
		t.Fatal("gated dial must return a nil client")
	}
	if called {
		t.Fatal("OnRelayConnect fired for a gate-skipped dial")
	}
}

// TestRelayConnectReportsFailure 真实失败拨号：错误原样回报
// OnRelayConnect，客户端为 nil。
func TestRelayConnectReportsFailure(t *testing.T) {
	var got error
	cfg := SignalingConfig{
		Logger:         gateTestLogger(),
		OnRelayConnect: func(err error) { got = err },
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	rc, err := connectSessionRelay(ctx,
		"wss://127.0.0.1:1/relay", strings.Repeat("ab", 32), cfg, gateTestLogger())
	if err == nil {
		t.Fatal("expected a dial error against 127.0.0.1:1")
	}
	if rc != nil {
		t.Fatal("failed dial must return a nil client")
	}
	if got == nil {
		t.Fatal("OnRelayConnect was not called with the failure")
	}
	if got.Error() != err.Error() {
		t.Fatalf("OnRelayConnect error = %q, want the attempt error %q", got, err)
	}
}

// TestRelayRebuildHonorsGate 重建循环逐次过闸：闸门关闭时立即退出
// （不拨号、不睡眠），relayClient 保持 nil。
func TestRelayRebuildHonorsGate(t *testing.T) {
	s := &Session{
		cfg: SignalingConfig{
			Logger:        gateTestLogger(),
			RelayDialGate: func() bool { return false },
		},
		logger: gateTestLogger().With("component", "p2p_session"),
		welcome: &Welcome{
			RelayURL:    "wss://127.0.0.1:1/relay",
			RelayPubKey: strings.Repeat("ab", 32),
		},
		closeCh: make(chan struct{}),
	}
	done := make(chan struct{})
	go func() { s.rebuildRelayClient(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("gated rebuild did not return promptly")
	}
	if s.relayClient != nil {
		t.Fatal("gated rebuild must not install a relay client")
	}
}
