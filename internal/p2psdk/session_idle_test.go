package peer

import (
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// #20 relay 槽泄漏的根修（设备侧被动空闲回收）：死亡观看者会话没有
// session_close 也不会再有任何流量，此前会永久滞留（2026-10-08 生产
// 实测泄漏 8 槽）。策略：会话级活跃度跟踪（用户面 DataChannel 的
// OnMessage/Send 双向 touch）+ Session 周期清扫，超时未活跃的会话按
// session_idle 拆除（复用 teardownWithReason：通知对端 + 回调齐全）。

func newIdleTestSession(t *testing.T, timeout time.Duration) *Session {
	t.Helper()
	return &Session{
		logger:  slog.Default(),
		peers:   make(map[string]*p2pSession),
		closeCh: make(chan struct{}),
		done:    make(chan struct{}),
		cfg:     SignalingConfig{SessionIdleTimeout: timeout},
	}
}

func TestSessionIdleSweepTearsDownStaleSessions(t *testing.T) {
	s := newIdleTestSession(t, 50*time.Millisecond)

	var mu sync.Mutex
	var closed []string
	s.OnSessionClose = func(id, reason string) {
		mu.Lock()
		closed = append(closed, id+":"+reason)
		mu.Unlock()
	}

	stale := &p2pSession{sessionID: "stale", owner: s, logger: s.logger}
	stale.lastActivity.Store(time.Now().Add(-time.Hour).UnixNano())
	alive := &p2pSession{sessionID: "alive", owner: s, logger: s.logger}
	alive.lastActivity.Store(time.Now().UnixNano())
	s.peers["stale"] = stale
	s.peers["alive"] = alive

	s.sweepIdleSessions()

	mu.Lock()
	defer mu.Unlock()
	assert.Contains(t, closed, "stale:session_idle",
		"超时未活跃的会话应被拆除并回调 OnSessionClose")
	assert.NotContains(t, closed, "alive:session_idle",
		"活跃会话不得误杀")
	assert.NotContains(t, s.peers, "stale", "被拆会话应移出名册")
	assert.Contains(t, s.peers, "alive", "活跃会话应保留")
}

func TestSessionIdleSweepDisabled(t *testing.T) {
	s := newIdleTestSession(t, -1) // 负值 = 显式停用

	var closed int32
	var mu sync.Mutex
	s.OnSessionClose = func(_, _ string) {
		mu.Lock()
		closed++
		mu.Unlock()
	}

	stale := &p2pSession{sessionID: "stale", owner: s, logger: s.logger}
	stale.lastActivity.Store(time.Now().Add(-time.Hour).UnixNano())
	s.peers["stale"] = stale

	s.sweepIdleSessions()

	mu.Lock()
	defer mu.Unlock()
	assert.EqualValues(t, 0, closed, "停用清扫时不得拆除任何会话")
	assert.Contains(t, s.peers, "stale")
}

func TestSessionIdleTimeoutDefault(t *testing.T) {
	s := newIdleTestSession(t, 0)
	assert.Equal(t, DefaultSessionIdleTimeout, s.cfg.sessionIdleTimeout(),
		"零值应取默认空闲超时")
	neg := newIdleTestSession(t, -time.Second)
	assert.Less(t, neg.cfg.sessionIdleTimeout(), time.Duration(0),
		"负值配置应原样透传（清扫侧按停用处理）")
}

func TestActivityDataChannelTouchesOnBothDirections(t *testing.T) {
	p := &p2pSession{sessionID: "x", logger: slog.Default()}
	p.lastActivity.Store(0)

	var delivered []byte
	inner := DataChannel{
		OnMessage: func(h func([]byte)) { h([]byte("in")) },
		Send:      func(b []byte) error { return nil },
	}
	wrapped := p.activityDataChannel(inner)
	wrapped.OnMessage(func(b []byte) { delivered = append(delivered, b...) })

	assert.True(t, p.lastActivity.Load() > 0, "入站消息应 touch 活跃度")
	assert.Equal(t, "in", string(delivered), "包装不得吞消息")

	before := p.lastActivity.Load()
	time.Sleep(2 * time.Millisecond)
	_ = wrapped.Send([]byte("out"))
	assert.True(t, p.lastActivity.Load() > before, "出站发送应 touch 活跃度")
}
