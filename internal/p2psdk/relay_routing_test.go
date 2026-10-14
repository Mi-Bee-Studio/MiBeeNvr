package peer

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/p2psdk/relay"
)

// 多会话共享一条 relay 连接的路由正确性（2026-10-09 三方巡检发现的
// 潜在串扰）：出站必须按会话显式目的地、入站必须按源 key 分发、会话
// 关闭必须真通知应用层（OnClose 不再是吞注册的 no-op）。

func routedClient(t *testing.T, mock *miniRelayServer) *relay.Client {
	t.Helper()
	c := relay.NewClient()
	require.NoError(t, c.Connect(context.Background(), mock.url, mock.pubKeyHex()))
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func waitData(t *testing.T, ch chan []byte) []byte {
	t.Helper()
	select {
	case d := <-ch:
		return d
	case <-time.After(10 * time.Second):
		t.Fatal("no relay packet within 10s")
		return nil
	}
}

// 显式目的地：同一客户端可以同时向两个不同对端发包，互不串扰。
func TestRelayClientSendToExplicitDestinations(t *testing.T) {
	mock := newMiniRelayServer(t)
	a := routedClient(t, mock)
	b := routedClient(t, mock)
	c := routedClient(t, mock)

	gotB := make(chan []byte, 4)
	gotC := make(chan []byte, 4)
	b.OnMessage(func(d []byte) { gotB <- d })
	c.OnMessage(func(d []byte) { gotC <- d })

	require.NoError(t, a.SendTo(b.PubKey(), []byte("for-b")))
	require.NoError(t, a.SendTo(c.PubKey(), []byte("for-c")))
	assert.Equal(t, "for-b", string(waitData(t, gotB)))
	assert.Equal(t, "for-c", string(waitData(t, gotC)))
	select {
	case d := <-gotB:
		t.Fatalf("b 不应收到第二包: %s", d)
	case d := <-gotC:
		t.Fatalf("c 不应收到第二包: %s", d)
	default:
	}
}

// 入站按源分发：两个会话各自的 handler 只收自己源的数据。
func TestRelayClientPerSourceDispatch(t *testing.T) {
	mock := newMiniRelayServer(t)
	d := routedClient(t, mock)
	b := routedClient(t, mock)
	c := routedClient(t, mock)

	fromB := make(chan []byte, 4)
	fromC := make(chan []byte, 4)
	d.OnPacket(b.PubKey(), func(data []byte) { fromB <- data })
	d.OnPacket(c.PubKey(), func(data []byte) { fromC <- data })

	require.NoError(t, b.SendTo(d.PubKey(), []byte("src-b")))
	require.NoError(t, c.SendTo(d.PubKey(), []byte("src-c")))
	assert.Equal(t, "src-b", string(waitData(t, fromB)))
	assert.Equal(t, "src-c", string(waitData(t, fromC)))
}

// 会话通道全生命周期：Send 走显式目的地；OnClose 注册后随会话关闭触发
// （且共享 relay 连接存活、路由摘除）。
func TestRelaySessionChannelLifecycle(t *testing.T) {
	mock := newMiniRelayServer(t)
	shared := routedClient(t, mock)
	peerC := routedClient(t, mock)

	peerGot := make(chan []byte, 4)
	peerC.OnMessage(func(d []byte) { peerGot <- d })

	ch := &relaySessionChannel{rc: shared, peer: peerC.PubKey()}
	p2p := &p2pSession{sessionID: "s1", relayClient: shared, relayActive: true}
	p2p.mu.Lock()
	p2p.relayChan = ch
	p2p.mu.Unlock()

	closed := make(chan struct{}, 2)
	dc := ch.dataChannel()
	dc.OnClose(func() { closed <- struct{}{} })
	dc.OnMessage(func([]byte) {}) // 占位：注册路由即可

	require.NoError(t, dc.Send([]byte("hello")))
	assert.Equal(t, "hello", string(waitData(t, peerGot)))

	require.NoError(t, p2p.close())
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("会话关闭必须触发通道 OnClose（应用层注销依赖它）")
	}

	// 共享连接仍活：路由摘除后，全局 handler 应能收到对端后续包。
	global := make(chan []byte, 1)
	shared.OnMessage(func(d []byte) { global <- d })
	require.NoError(t, peerC.SendTo(shared.PubKey(), []byte("still-shared")))
	assert.Equal(t, "still-shared", string(waitData(t, global)))
}

// 串扰回归（本根修的核心用例）：两个会话共享一条连接，各自发各自的，
// 对端互不串包。
func TestRelaySharedConnectionNoCrossTalk(t *testing.T) {
	mock := newMiniRelayServer(t)
	shared := routedClient(t, mock)
	viewer1 := routedClient(t, mock)
	viewer2 := routedClient(t, mock)

	got1 := make(chan []byte, 4)
	got2 := make(chan []byte, 4)
	viewer1.OnMessage(func(d []byte) { got1 <- d })
	viewer2.OnMessage(func(d []byte) { got2 <- d })

	s1 := &relaySessionChannel{rc: shared, peer: viewer1.PubKey()}
	s2 := &relaySessionChannel{rc: shared, peer: viewer2.PubKey()}
	dc1, dc2 := s1.dataChannel(), s2.dataChannel()
	dc1.OnMessage(func([]byte) {})
	dc2.OnMessage(func([]byte) {})

	require.NoError(t, dc2.Send([]byte("to-viewer2")))
	require.NoError(t, dc1.Send([]byte("to-viewer1")))
	assert.Equal(t, "to-viewer2", string(waitData(t, got2)))
	assert.Equal(t, "to-viewer1", string(waitData(t, got1)))
}
