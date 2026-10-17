package p2p

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/p2psdk/relay"
)

// relay 拨号熔断器（#23/#24）。背景（平台 #94 事故）：relay 不可用的
// 8.5 小时里，设备侧每次信令重拨都重试一次 relay 建连——对已经死亡的
// relay 这只是给平台添乱。熔断器把「连续失败」翻译成一段静默冷却：
// 冷却期内 SDK 跳过 relay 拨号（信令与直连仲裁完全不受影响），冷却
// 结束放行恰好一次半开探测（single-flight），探测结果决定回 closed
// 还是续一轮冷却。
const (
	// relayBreakerThreshold 是连续建连失败进入熔断的阈值。
	relayBreakerThreshold = 3
	// relayCooldown 是熔断打开后的冷却时长（#23 建议值 5min）。
	relayCooldown = 5 * time.Minute
)

// relay 熔断状态（状态面字符串；指标 gauge 用 [relayCircuitNumeric]）。
const (
	relayCircuitClosed   = "closed"
	relayCircuitOpen     = "open"
	relayCircuitHalfOpen = "half_open"
)

// relayCircuitNumeric 把状态映射为指标 gauge 值（0/1/2）。
func relayCircuitNumeric(state string) float64 {
	switch state {
	case relayCircuitOpen:
		return 1
	case relayCircuitHalfOpen:
		return 2
	default:
		return 0
	}
}

// classifyRelayFailure 把一次 relay 建连失败归类（指标 reason 标签与
// 日志语义共用，#24 消费点）。0x03（平台 #95 起）与关闭码 4005 是容量
// 语义：退避即可，绝非凭据故障；0x01 在新平台 = 凭据/盒子失败（旧平台
// 可能复用为过载，如实记 auth）；4006-4008 = 各类限速。
func classifyRelayFailure(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, relay.ErrOverloaded) {
		return "overloaded"
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "4005"):
		return "overloaded" // close 4005 relay_overloaded
	case strings.Contains(msg, "4006"), strings.Contains(msg, "4007"), strings.Contains(msg, "4008"):
		return "rate_limited" // per-IP limit / auth rate limit family
	case strings.Contains(msg, "status=0x01"):
		return "auth"
	default:
		return "network"
	}
}

// relayBreaker 是设备侧 relay 拨号熔断器。所有方法并发安全（SDK 的
// Dial 与重建循环可能并发询问/回报）。
type relayBreaker struct {
	mu         sync.Mutex
	now        func() time.Time
	state      string
	fails      int // 连续失败（成功清零）
	openUntil  time.Time
	lastReason string

	dials    uint64
	failures uint64
	byReason map[string]uint64
}

func newRelayBreaker() *relayBreaker {
	return &relayBreaker{state: relayCircuitClosed, byReason: map[string]uint64{}, now: time.Now}
}

// allowDial 是 SignalingConfig.RelayDialGate 的实现：closed 恒放行；
// open 未到期拒绝；到期转 half_open 放行恰好一次探测，探测在途期间
// 其余询问一律拒绝（single-flight）。
func (b *relayBreaker) allowDial() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	switch b.state {
	case relayCircuitOpen:
		if b.now().Before(b.openUntil) {
			return false
		}
		b.state = relayCircuitHalfOpen
		return true
	case relayCircuitHalfOpen:
		return false
	default:
		return true
	}
}

// record 消费一次建连结果（OnRelayConnect 的输入）。成功回 closed 并
// 清连击；失败计数归类，连击达阈值（或半开探测失败）即 open 一轮新
// 冷却。返回是否刚转入 open（供调用方记一次迁移日志）。
func (b *relayBreaker) record(err error) (opened bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.dials++
	if err == nil {
		b.state = relayCircuitClosed
		b.fails = 0
		return false
	}
	b.failures++
	b.fails++
	b.lastReason = classifyRelayFailure(err)
	b.byReason[b.lastReason]++
	if b.fails >= relayBreakerThreshold || b.state == relayCircuitHalfOpen {
		opened = b.state != relayCircuitOpen
		b.state = relayCircuitOpen
		b.openUntil = b.now().Add(relayCooldown)
	}
	return opened
}

// cooldownActive 报告当前是否处于熔断冷却窗内——钉 relay 的仲裁决策
// 在此期间让位直连（#23 P1：relay 死不能拖死整线）。
func (b *relayBreaker) cooldownActive() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.state == relayCircuitOpen && b.now().Before(b.openUntil)
}

// RelayCircuit 是 /api/p2p/status 的 relay 熔断器视图。
type RelayCircuit struct {
	State            string            `json:"state"` // closed|open|half_open
	ConsecutiveFails int               `json:"consecutive_failures,omitempty"`
	LastReason       string            `json:"last_reason,omitempty"`
	OpenUntil        time.Time         `json:"open_until,omitempty"`
	DialsTotal       uint64            `json:"dials_total"`
	FailuresTotal    uint64            `json:"failures_total"`
	FailuresByReason map[string]uint64 `json:"failures_by_reason,omitempty"`
}

// snapshot 导出状态面视图（map 拷贝，调用方可自由持有）。
func (b *relayBreaker) snapshot() RelayCircuit {
	b.mu.Lock()
	defer b.mu.Unlock()
	snap := RelayCircuit{
		State:            b.state,
		ConsecutiveFails: b.fails,
		LastReason:       b.lastReason,
		OpenUntil:        b.openUntil,
		DialsTotal:       b.dials,
		FailuresTotal:    b.failures,
	}
	if len(b.byReason) > 0 {
		snap.FailuresByReason = make(map[string]uint64, len(b.byReason))
		for k, v := range b.byReason {
			snap.FailuresByReason[k] = v
		}
	}
	return snap
}

// onRelayConnect 消费 SDK 的 relay 建连结果（#23/#24 消费点）：熔断
// 状态机 + 分类计数 + 事件/指标三面落。过载（overloaded/rate_limited）
// 与凭据失败（auth）在指标与日志里分开呈现——过载绝不触发任何「弃
// 梯」类语义，只参与冷却。
func (m *Manager) onRelayConnect(err error) {
	opened := m.relay.record(err)
	m.recorder.IncP2PRelayDial()
	if err == nil {
		m.recorder.SetP2PRelayCircuitState(relayCircuitNumeric(relayCircuitClosed))
		m.addEvent("relay_up", "", "circuit=closed")
		return
	}
	reason := classifyRelayFailure(err)
	snap := m.relay.snapshot()
	m.recorder.IncP2PRelayDialFailure(reason)
	m.recorder.SetP2PRelayCircuitState(relayCircuitNumeric(snap.State))
	detail := fmt.Sprintf("reason=%s dials=%d fails=%d circuit=%s",
		reason, snap.DialsTotal, snap.FailuresTotal, snap.State)
	if snap.State == relayCircuitOpen {
		detail += " open_until=" + snap.OpenUntil.Format(time.RFC3339)
	}
	m.addEvent("relay_failed", "", detail)
	if opened {
		m.logger.Warn("relay circuit open — cooling down; sessions fall back to direct arbitration",
			"consecutive_failures", snap.ConsecutiveFails,
			"cooldown", relayCooldown.String(),
			"last_reason", reason)
	}
}
