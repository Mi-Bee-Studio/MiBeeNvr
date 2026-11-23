package p2p

// 熔断器的 flap 盲区（mibee-nvr#61 衍生项）。
//
// #61 的 relay 热循环形状是「建连成功 → 5s 后被踢 → 再建连成功」。旧
// record() 只看建连结果，每轮成功都清零 fails，于是连续失败计数永远到
// 不了阈值——事故期间 failures_total 只记了 18，而真实被打断 3000+ 轮。
//
// 现在「成功」必须活过 relayFlapWindow 才算健康；两次成功相隔不足该
// 窗口即按 flap 计一次失败。

import (
	"testing"
	"time"
)

// TestBreakerFlapOpensCircuit 复刻事故波形：每 5s 一轮「建连成功」。三轮
// 后必须熔断，而不是无限循环。
func TestBreakerFlapOpensCircuit(t *testing.T) {
	m, fc := newBreakerTestManager(t)

	// 第一轮成功：尚无前一次成功可比，按健康处理（不能一上来就判 flap）。
	if opened := m.relay.record(nil); opened {
		t.Fatal("first-ever success must not open the circuit")
	}

	// 之后每 5s 一轮成功 —— 全是短命连接。
	openedAt := 0
	for round := 2; round <= 5; round++ {
		fc.Advance(5 * time.Second)
		if m.relay.record(nil) && openedAt == 0 {
			openedAt = round
		}
	}
	if openedAt == 0 {
		t.Fatalf("flapping connections never opened the circuit (fails=%d, want ≥%d)",
			m.relay.snapshot().ConsecutiveFails, relayBreakerThreshold)
	}
	// 阈值 3：第 2/3/4 轮各计一次 flap → 第 4 轮达阈值。
	if openedAt != relayBreakerThreshold+1 {
		t.Fatalf("circuit opened at round %d, want %d", openedAt, relayBreakerThreshold+1)
	}
	if !m.relay.cooldownActive() {
		t.Fatal("circuit should be cooling down after flap detection")
	}
}

// TestBreakerStableConnectionIsHealthy 防过度识别：真正站住的连接（活过
// flap 窗口）必须回 closed 并清零连击，不能被误判成 flap。
func TestBreakerStableConnectionIsHealthy(t *testing.T) {
	m, fc := newBreakerTestManager(t)

	m.relay.record(nil) // 首次成功
	fc.Advance(2 * time.Minute)
	m.relay.record(nil) // 活了 2 分钟 —— 健康

	snap := m.relay.snapshot()
	if snap.ConsecutiveFails != 0 {
		t.Fatalf("stable reconnects counted as failures: fails=%d", snap.ConsecutiveFails)
	}
	if snap.State != relayCircuitClosed {
		t.Fatalf("state = %s, want closed", snap.State)
	}
}

// TestBreakerFlapThenStableRecovers 混合场景：连抖几轮后真的稳住，连击
// 必须归零（否则设备一旦抖过就再也回不到健康态）。
func TestBreakerFlapThenStableRecovers(t *testing.T) {
	m, fc := newBreakerTestManager(t)

	m.relay.record(nil)
	fc.Advance(5 * time.Second)
	m.relay.record(nil) // flap #1

	if m.relay.snapshot().ConsecutiveFails == 0 {
		t.Fatal("flap was not counted as a failure")
	}

	// 这次真稳住了：活过 flap 窗口再成功。
	fc.Advance(2 * time.Minute)
	m.relay.record(nil)
	if got := m.relay.snapshot().ConsecutiveFails; got != 0 {
		t.Fatalf("stable connection did not clear the streak: fails=%d", got)
	}
}

// TestBreakerFlapReasonLabelled flap 事件在按因统计里可见（运维排障时
// 要能区分「连不上」与「连上就断」）。
func TestBreakerFlapReasonLabelled(t *testing.T) {
	m, fc := newBreakerTestManager(t)

	m.relay.record(nil)
	fc.Advance(5 * time.Second)
	m.relay.record(nil)

	if got := m.relay.snapshot().FailuresByReason[relayFlapReason]; got != 1 {
		t.Fatalf("flap reason counter = %d, want 1", got)
	}
}
