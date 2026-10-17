package p2p

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/metrics"
	peer "github.com/Mi-Bee-Studio/MiBeeNvr/internal/p2psdk"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/p2psdk/relay"
)

// fakeClock 是可推进的时钟（熔断器冷却窗测试用）。
type fakeClock struct{ t time.Time }

func (c *fakeClock) Now() time.Time          { return c.t }
func (c *fakeClock) Advance(d time.Duration) { c.t = c.t.Add(d) }

func newBreakerTestManager(t *testing.T) (*Manager, *fakeClock) {
	t.Helper()
	m := New(config.P2PConfig{}, "127.0.0.1:9090",
		filepath.Join(t.TempDir(), "token.json"),
		slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), nil)
	fc := &fakeClock{t: time.Now()}
	m.relay.now = fc.Now
	return m, fc
}

// TestRelayBreakerOpensAndCools 钉死熔断主链路（#23 验收 1）：
// 连续 N 次失败 → open（拒绝拨号）→ 冷却到期 → half_open 放行恰好
// 一次探测（single-flight）→ 探测失败重新 open / 成功回 closed。
func TestRelayBreakerOpensAndCools(t *testing.T) {
	m, fc := newBreakerTestManager(t)
	overloaded := fmt.Errorf("relay auth: %w", relay.ErrOverloaded)

	if !m.relay.allowDial() {
		t.Fatal("fresh breaker must allow dialing")
	}
	for range relayBreakerThreshold {
		m.onRelayConnect(overloaded)
	}
	if m.relay.allowDial() {
		t.Fatal("breaker must be open after consecutive failures")
	}
	if got := m.relay.snapshot().State; got != relayCircuitOpen {
		t.Fatalf("state = %q, want open", got)
	}

	// 冷却期内持续拒绝。
	fc.Advance(relayCooldown - time.Second)
	if m.relay.allowDial() {
		t.Fatal("breaker must stay closed-for-dial inside the cooldown window")
	}

	// 到期 → half_open：恰好放行一次。
	fc.Advance(2 * time.Second)
	if !m.relay.allowDial() {
		t.Fatal("cooldown elapsed: probe must be allowed")
	}
	if m.relay.allowDial() {
		t.Fatal("half-open must be single-flight: second ask rejected while probing")
	}

	// 探测失败 → 重新 open（新一轮冷却）。
	m.onRelayConnect(overloaded)
	if m.relay.allowDial() {
		t.Fatal("failed half-open probe must re-open the circuit")
	}
	snap := m.relay.snapshot()
	if snap.State != relayCircuitOpen || !snap.OpenUntil.After(fc.Now()) {
		t.Fatalf("re-opened circuit snapshot = %+v", snap)
	}

	// 第二轮冷却到期 → 探测成功 → closed，拨号恢复。
	fc.Advance(relayCooldown + time.Second)
	if !m.relay.allowDial() {
		t.Fatal("second cooldown elapsed: probe must be allowed")
	}
	m.onRelayConnect(nil)
	if !m.relay.allowDial() {
		t.Fatal("successful probe must close the circuit")
	}
	if got := m.relay.snapshot().State; got != relayCircuitClosed {
		t.Fatalf("state = %q, want closed", got)
	}
}

// TestRelayBreakerClassifiesReasons 钉死失败分类（#24 消费点）：
// 0x03 过载 / 0x01 凭据 / 4005 容量 / 4008 限速 / 其它网络——指标与
// 日志按 reason 分开呈现，过载绝不并入凭据失败。
func TestRelayBreakerClassifiesReasons(t *testing.T) {
	m, _ := newBreakerTestManager(t)
	m.onRelayConnect(fmt.Errorf("relay auth: %w", relay.ErrOverloaded))
	m.onRelayConnect(errors.New("relay auth: relay auth rejected (status=0x01)"))
	m.onRelayConnect(errors.New("websocket: close 4005 (relay_overloaded)"))
	m.onRelayConnect(errors.New("websocket: close 4008 (auth rate limited)"))
	m.onRelayConnect(errors.New("dial tcp 192.0.2.1:443: connect: network is unreachable"))

	snap := m.relay.snapshot()
	want := map[string]uint64{
		"overloaded":   2, // 0x03 + close 4005
		"auth":         1,
		"rate_limited": 1,
		"network":      1,
	}
	for reason, n := range want {
		if snap.FailuresByReason[reason] != n {
			t.Fatalf("failures_by_reason[%s] = %d, want %d (full map: %v)",
				reason, snap.FailuresByReason[reason], n, snap.FailuresByReason)
		}
	}
	if snap.DialsTotal != 5 || snap.FailuresTotal != 5 {
		t.Fatalf("dials=%d failures=%d, want 5/5", snap.DialsTotal, snap.FailuresTotal)
	}
}

// TestTransportDecisionRelayCooldownFallback 钉死 auto/强制钉选在熔断
// 冷却期的回落（#23 P1）：钉 relay 的决策让位直连仲裁，reason 如实
// 标注 relay_cooldown——「relay 死时整线不可用」不存在。
func TestTransportDecisionRelayCooldownFallback(t *testing.T) {
	overloaded := fmt.Errorf("relay auth: %w", relay.ErrOverloaded)

	always := New(config.P2PConfig{ForceRelay: config.ForceRelayAlways},
		"127.0.0.1:9090", filepath.Join(t.TempDir(), "token.json"),
		slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), nil)
	for range relayBreakerThreshold {
		always.onRelayConnect(overloaded)
	}
	if pinned, reason := always.transportDecision(); pinned || reason != "relay_cooldown" {
		t.Fatalf("force_relay + cooldown: pinned=%v reason=%q, want false/relay_cooldown", pinned, reason)
	}

	auto := New(config.P2PConfig{ForceRelay: config.ForceRelayAuto},
		"127.0.0.1:9090", filepath.Join(t.TempDir(), "token.json"),
		slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), nil)
	auto.natObs.Store(&peer.NatObservation{Class: peer.NATSymmetric})
	for range relayBreakerThreshold {
		auto.onRelayConnect(overloaded)
	}
	if pinned, reason := auto.transportDecision(); pinned || reason != "relay_cooldown" {
		t.Fatalf("auto+symmetric + cooldown: pinned=%v reason=%q, want false/relay_cooldown", pinned, reason)
	}

	// 冷却结束（half_open 探测在途）→ 钉选语义恢复（探测成败由下一次
	// record 决定，决策面如实反映当前钉选）。
	auto.relay.now = func() time.Time { return time.Now().Add(relayCooldown + time.Minute) }
	if pinned, reason := auto.transportDecision(); !pinned || reason != "nat_symmetric" {
		t.Fatalf("after cooldown: pinned=%v reason=%q, want true/nat_symmetric", pinned, reason)
	}
}

// TestStatusExposesRelayCircuit 状态面可见性（#23 验收 2/#24 消费点）：
// /api/p2p/status 的 relay 块给出熔断状态/连续失败/分类计数。
func TestStatusExposesRelayCircuit(t *testing.T) {
	m, _ := newBreakerTestManager(t)
	overloaded := fmt.Errorf("relay auth: %w", relay.ErrOverloaded)
	for range relayBreakerThreshold {
		m.onRelayConnect(overloaded)
	}

	snap := m.Status()
	if snap.Relay == nil {
		t.Fatal("status snapshot must carry the relay circuit block")
	}
	if snap.Relay.State != relayCircuitOpen {
		t.Fatalf("relay.state = %q, want open after threshold failures", snap.Relay.State)
	}
	if snap.Relay.LastReason != "overloaded" {
		t.Fatalf("relay.last_reason = %q, want overloaded", snap.Relay.LastReason)
	}
	if snap.Relay.FailuresByReason["overloaded"] != relayBreakerThreshold {
		t.Fatalf("relay.failures_by_reason[overloaded] = %d, want %d",
			snap.Relay.FailuresByReason["overloaded"], relayBreakerThreshold)
	}
	// 钉选回落同步可见（auto+对称+熔断 → direct/relay_cooldown）。
	if snap.TransportDecision != "direct" || snap.DecisionReason != "default" {
		t.Fatalf("default-mode decision = %s/%s (cooldown only overrides pinned decisions)",
			snap.TransportDecision, snap.DecisionReason)
	}
}

// TestRelayCircuitMetrics 钉死指标面（#23 验收 3）：拨号总数/按因失败
// 数/熔断状态 gauge。
func TestRelayCircuitMetrics(t *testing.T) {
	m := New(config.P2PConfig{}, "127.0.0.1:9090",
		filepath.Join(t.TempDir(), "token.json"),
		slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), nil)
	rec := metrics.NewMetrics()
	m.SetMetrics(rec)

	m.onRelayConnect(fmt.Errorf("relay auth: %w", relay.ErrOverloaded))
	if got := testutil.ToFloat64(rec.P2PRelayDials); got != 1 {
		t.Fatalf("relay dials counter = %v, want 1", got)
	}
	if got := testutil.ToFloat64(rec.P2PRelayDialFailures.WithLabelValues("overloaded")); got != 1 {
		t.Fatalf("relay failures{overloaded} = %v, want 1", got)
	}
	if got := testutil.ToFloat64(rec.P2PRelayCircuitState); got != 0 {
		t.Fatalf("circuit gauge (closed) = %v, want 0", got)
	}

	for range relayBreakerThreshold - 1 {
		m.onRelayConnect(errors.New("dial tcp: refused"))
	}
	if got := testutil.ToFloat64(rec.P2PRelayDialFailures.WithLabelValues("network")); got != 2 {
		t.Fatalf("relay failures{network} = %v, want 2", got)
	}
	if got := testutil.ToFloat64(rec.P2PRelayCircuitState); got != 1 {
		t.Fatalf("circuit gauge (open) = %v, want 1", got)
	}
}
