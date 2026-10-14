package peer

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSDK_KeepaliveSuppressedWhenBusy（#35 ①）：半周期内有真实流量的会话
// 跳过定时 ping——忙窗内 ping 计数远低于空闲基线；停止流量后 ping 恢复。
// Pong 帧对 mock 是「已处理不计数」的合成真实流量（不算 ping）。
func TestSDK_KeepaliveSuppressedWhenBusy(t *testing.T) {
	orig := currentKeepaliveInterval()
	setKeepaliveInterval(80 * time.Millisecond)
	t.Cleanup(func() { setKeepaliveInterval(orig) })

	mock := newMockServer(t)
	sess, err := Dial(context.Background(), SignalingConfig{
		ServerURL: mock.url,
		JWT:       "test-jwt",
		ProductID: "test-product",
		PeerID:    "ka-busy",
		Role:      "device",
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = sess.Close() })

	// 忙窗 500ms（~6 个 tick 机会）：每 ~30ms 写一帧（半周期 40ms 内持续刷新）。
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		pong, err := Marshal(MessageTypePong, Ping{Timestamp: time.Now().UnixMilli()})
		require.NoError(t, err)
		require.NoError(t, sess.signal.sendBytes(pong))
		time.Sleep(30 * time.Millisecond)
	}
	assert.Less(t, mock.pingCount("ka-busy"), 3,
		"忙会话的 keepalive ping 应被抑制（空闲基线 ~6，抑制后至多边界漏 1-2）")

	// 转空闲后 ping 恢复（抑制不得变成永久不发）。
	idle := time.Now().Add(3 * time.Second)
	for time.Now().Before(idle) {
		if mock.pingCount("ka-busy") >= 3 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("空闲后 ping 应恢复发送，got %d", mock.pingCount("ka-busy"))
}

// TestReconnectCooldownForStreak（#35 ②）：连击 2 次起冷却 60s 起步翻倍、
// 300s 封顶（含移位溢出防御）。
func TestReconnectCooldownForStreak(t *testing.T) {
	assert.Zero(t, reconnectCooldownFor(0), "单次闪断不罚")
	assert.Zero(t, reconnectCooldownFor(1), "首次短命不罚")
	assert.Equal(t, 60*time.Second, reconnectCooldownFor(2))
	assert.Equal(t, 120*time.Second, reconnectCooldownFor(3))
	assert.Equal(t, 240*time.Second, reconnectCooldownFor(4))
	assert.Equal(t, 300*time.Second, reconnectCooldownFor(5), "封顶")
	assert.Equal(t, 300*time.Second, reconnectCooldownFor(63), "移位溢出防御回落封顶")
}

// TestShortLivedStreakLifecycle（#35 ②）：短命连击累积冷却；长存活清零；
// 中等时长（30s-2min）既不累积也不清零。
func TestShortLivedStreakLifecycle(t *testing.T) {
	sc := &signalingClient{}
	sc.recordSessionLifetime(10 * time.Second)
	sc.recordSessionLifetime(20 * time.Second)
	assert.Equal(t, 60*time.Second, sc.currentReconnectCooldown(),
		"连续 2 次短命应叠加 60s 冷却")
	sc.recordSessionLifetime(3 * time.Minute)
	assert.Zero(t, sc.currentReconnectCooldown(),
		"长存活会话应清零连击")

	sc2 := &signalingClient{}
	sc2.recordSessionLifetime(time.Minute)
	assert.Zero(t, sc2.currentReconnectCooldown(),
		"中等时长不应触发冷却")
}
