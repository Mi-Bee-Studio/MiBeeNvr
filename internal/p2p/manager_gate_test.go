package p2p

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
)

// fakeGate 是可脚本化的授权门控测试替身。
type fakeGate struct {
	allowed bool
	state   string
	feature map[string]bool // nil = 全能（未限定 features）
	maxCli  int             // 授权并发配额（0 = 不限）
}

func (f *fakeGate) Allowed() bool { return f.allowed }
func (f *fakeGate) State() string { return f.state }

func (f *fakeGate) FeatureAllowed(feature string) bool {
	if f.feature == nil {
		return true
	}
	return f.feature[feature]
}

func (f *fakeGate) MaxClients() int { return f.maxCli }

// newGateTestManager 构造一个指向不可达信令/token 端点的 Manager——mint
// 必然失败，"token mint failed" 日志即「门控放行且确实走到了铸 token」
// 的可观测证据。
func newGateTestManager(t *testing.T, gate LicenseGate, buf *bytes.Buffer) *Manager {
	t.Helper()
	cfg := config.P2PConfig{
		ServerURL:            "wss://gate-test.invalid/signal",
		Username:             "u",
		Password:             "p",
		RedialMinIntervalSec: 30,
	}
	logger := slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return New(cfg, "127.0.0.1:9090", t.TempDir()+"/token.json", logger, gate)
}

func TestGateBlockedNeverMints(t *testing.T) {
	var buf bytes.Buffer
	m := newGateTestManager(t, &fakeGate{allowed: false, state: "expired"}, &buf)

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	m.Run(ctx)

	logs := buf.String()
	if strings.Contains(logs, "token mint failed") {
		t.Fatal("blocked gate must not reach token minting")
	}
	if !strings.Contains(logs, "license gate blocking") {
		t.Fatalf("expected gate-blocked log, got: %s", logs)
	}
}

func TestGateNilOrAllowedProceedsToMint(t *testing.T) {
	for _, tc := range []struct {
		name string
		gate LicenseGate
	}{
		{name: "nil gate (授权缺席恒放行)", gate: nil},
		{name: "allowed gate", gate: &fakeGate{allowed: true, state: "valid"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			m := newGateTestManager(t, tc.gate, &buf)

			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			m.Run(ctx)

			// 端点不可达 → mint 失败日志即「已放行进入拨号准备」的证据。
			if !strings.Contains(buf.String(), "token mint failed") {
				t.Fatalf("expected token mint attempt log, got: %s", buf.String())
			}
		})
	}
}

func TestGateAllowedLogOnStateChange(t *testing.T) {
	var buf bytes.Buffer
	gate := &fakeGate{allowed: false, state: "expired"}
	m := newGateTestManager(t, gate, &buf)

	if m.gateAllowed() {
		t.Fatal("blocked gate must not be allowed")
	}
	// 热生效：模拟 API 安装新文档后门控翻转为放行。
	gate.allowed, gate.state = true, "valid"
	if !m.gateAllowed() {
		t.Fatal("allowed gate must be allowed")
	}

	logs := buf.String()
	if !strings.Contains(logs, "license gate blocking") {
		t.Fatalf("expected blocking log first, got: %s", logs)
	}
	if !strings.Contains(logs, "license gate allowing") {
		t.Fatalf("expected allowing log after transition, got: %s", logs)
	}
	// 稳态复查不刷屏（同状态只记一次）。
	m.gateAllowed()
	if got := strings.Count(buf.String(), "license gate allowing"); got != 1 {
		t.Fatalf("steady-state re-check logged again (%d times), want 1", got)
	}
}

func TestNilGateAlwaysAllowed(t *testing.T) {
	var buf bytes.Buffer
	m := newGateTestManager(t, nil, &buf)
	if !m.gateAllowed() {
		t.Fatal("nil gate must always allow")
	}
	if buf.Len() != 0 {
		t.Fatalf("nil gate must not log, got: %s", buf.String())
	}
}
