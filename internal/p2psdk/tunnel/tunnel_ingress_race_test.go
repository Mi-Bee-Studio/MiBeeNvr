package tunnel

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 入站注册竞态（2026-10-09 三方巡检 R19/R41/R44 三例同族根因）：
// New() 返回即意味着传输路由已活（relay 路由随会话通道构造即注册），
// 对端在 session_open 后立刻发 tunnel_open——若 OnMessage 注册要等
// Serve()（应用层 go tun.Serve() 存在调度窗口），先到的帧会被丢：
// tunnel_open 无应用层重传 → 流打不开 → 首请求挂死到超时（生产形态：
// 链路好但 login/recordings 15s 超时）。
//
// 契约：New() 必须同步注册缓冲式入站 handler；Serve() 只消费。

type raceTransport struct {
	mu      sync.Mutex
	handler func([]byte)
	closed  chan struct{}
}

func newRaceTransport() *raceTransport {
	return &raceTransport{closed: make(chan struct{})}
}

func (r *raceTransport) Send([]byte) error { return nil }
func (r *raceTransport) Close() error      { close(r.closed); return nil }
func (r *raceTransport) OnMessage(h func([]byte)) {
	r.mu.Lock()
	r.handler = h
	r.mu.Unlock()
}

// deliver 模拟一帧到达（注册前到达即丢——正是被钉死的老行为）。
func (r *raceTransport) deliver(data []byte) {
	r.mu.Lock()
	h := r.handler
	r.mu.Unlock()
	if h != nil {
		h(data)
	}
}

func TestTunnelRegistersIngressAtConstruction(t *testing.T) {
	tr := newRaceTransport()
	_ = New(tr)
	// 不调 Serve——注册必须在构造期就位。
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		tr.mu.Lock()
		h := tr.handler
		tr.mu.Unlock()
		if h != nil {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("New() 后、Serve() 前入站 handler 未注册——先到的帧会被丢（R19/R41/R44 竞态）")
}

func TestTunnelPreServeFramesSurvive(t *testing.T) {
	// 服务端：本地 TCP echo 探针。
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()
	got := make(chan []byte, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		buf := make([]byte, 64)
		n, _ := bufio.NewReader(c).Read(buf)
		got <- buf[:n]
	}()

	tr := newRaceTransport()
	tun := New(tr)
	tun.RegisterService("probe", ln.Addr().String())

	// Serve 之前对端已发来 open + data（真实时序：session_open 一到，
	// 客户端暖机请求即刻上路）。帧格式与 sendMsg 同构。
	mustJSON := func(v any) string {
		b, err := json.Marshal(v)
		require.NoError(t, err)
		return string(b)
	}
	openFrame, _ := json.Marshal(TunnelMessage{
		Type:    TunnelMessageTypeOpen,
		Payload: mustJSON(TunnelOpen{StreamID: 7, TargetService: "probe"}),
	})
	dataFrame, _ := json.Marshal(TunnelMessage{
		Type: TunnelMessageTypeData,
		Payload: mustJSON(TunnelData{
			StreamID: 7, Seq: 0, Total: 1,
			Payload: base64.StdEncoding.EncodeToString([]byte("ping-before-serve")),
		}),
	})
	tr.deliver(openFrame)
	tr.deliver(dataFrame)

	go tun.Serve()

	select {
	case data := <-got:
		assert.Equal(t, "ping-before-serve", string(data))
	case <-time.After(10 * time.Second):
		t.Fatal("Serve 前到达的帧未被处理——预注册缓冲语义失效")
	}
}
