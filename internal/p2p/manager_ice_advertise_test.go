package p2p

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	peer "github.com/Mi-Bee-Studio/MiBeeNvr/internal/p2psdk"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/p2psdk/tunnel"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/metrics"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// TestManagerDialPassesAdvertiseAndOnStats 钉住 SignalingConfig 透传
// （#10/#12 的 NVR 侧接线）：advertise 原样进 AdvertiseAddr，OnStats 非
// nil（SDK 侧注入 SessionID 后回调）。
func TestManagerDialPassesAdvertiseAndOnStats(t *testing.T) {
	var gotCfg *peer.SignalingConfig
	saved := peerDial
	peerDial = func(_ context.Context, sc peer.SignalingConfig) (*peer.Session, error) {
		cp := sc
		gotCfg = &cp
		return nil, errors.New("stop")
	}
	t.Cleanup(func() { peerDial = saved })

	cfg := config.P2PConfig{
		ServerURL: "wss://adv-test.invalid/signal",
		Username:  "device:mibeenvr:nvr-x",
		Advertise: "auto:40000",
		// #13 计时器/MTU 透传断言（单项配置 → 其余项 0 由 SDK 回填库默认）。
		ICEKeepaliveIntervalMS: 8000,
		ICEReceiveMTU:          1280,
	}
	m := New(cfg, "127.0.0.1:9090", filepath.Join(t.TempDir(), "token.json"),
		slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), nil)

	// runSession 在 Dial 失败后返回错误——捕获发生在覆写闭包内。
	if err := m.runSession(context.Background(), "jwt"); err == nil {
		t.Fatal("dial failure must propagate")
	}
	if gotCfg == nil {
		t.Fatal("peerDial must have been called")
	}
	if gotCfg.AdvertiseAddr != "auto:40000" {
		t.Fatalf("AdvertiseAddr = %q, want auto:40000", gotCfg.AdvertiseAddr)
	}
	if gotCfg.ICETimeouts.KeepaliveInterval != 8_000*1e6 {
		t.Fatalf("KeepaliveInterval = %v, want 8s", gotCfg.ICETimeouts.KeepaliveInterval)
	}
	if gotCfg.ICETimeouts.DisconnectedTimeout != 0 || gotCfg.ICETimeouts.FailedTimeout != 0 {
		t.Fatalf("unset timers must stay 0 (SDK substitutes defaults), got %+v", gotCfg.ICETimeouts)
	}
	if gotCfg.ICEReceiveMTU != 1280 {
		t.Fatalf("ICEReceiveMTU = %d, want 1280", gotCfg.ICEReceiveMTU)
	}
}

// TestManagerOnStatsExposesICE 钉住 #12 状态面：注入的 StatsSnapshot 透传
// 到 StatusSnapshot.ICESessions（键=SessionID），无 nominated pair 的快照
// 不落表，会话关闭修剪条目。
func TestManagerOnStatsExposesICE(t *testing.T) {
	m := New(config.P2PConfig{}, "127.0.0.1:9090",
		filepath.Join(t.TempDir(), "token.json"),
		slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), nil)

	m.onICEStats(peer.StatsSnapshot{
		SessionID:                 "sess-1",
		ICELocalCandidateType:     "srflx",
		ICERemoteCandidateType:    "host",
		ICELocalCandidateAddress:  "203.0.113.41:40000",
		ICERemoteCandidateAddress: "223.5.5.5",
		ICEIPVersion:              "4",
		ICERTTSeconds:             0.0235,
		ICEAvailableBitrateBits:   2_500_000,
		DataChannelBytesSent:      111,
		DataChannelBytesReceived:  222,
	})
	// 无可选对（建链早期）与无 SessionID 的快照必须被忽略。
	m.onICEStats(peer.StatsSnapshot{SessionID: "sess-2"})
	m.onICEStats(peer.StatsSnapshot{ICELocalCandidateType: "host"})

	snap := m.Status()
	got, ok := snap.ICESessions["sess-1"]
	if !ok {
		t.Fatalf("sess-1 missing from ice sessions: %+v", snap.ICESessions)
	}
	if got.LocalType != "srflx" || got.RemoteType != "host" || got.IPVersion != "4" {
		t.Fatalf("candidate types not passed through: %+v", got)
	}
	if got.RTTMs < 23.4 || got.RTTMs > 23.6 {
		t.Fatalf("rtt_ms = %v, want ~23.5", got.RTTMs)
	}
	if got.AvailableKbps != 2500 {
		t.Fatalf("available_kbps = %v, want 2500", got.AvailableKbps)
	}
	if len(snap.ICESessions) != 1 {
		t.Fatalf("empty-nominated snapshots must not create entries, got %+v", snap.ICESessions)
	}

	m.dropICESession("sess-1")
	if s := m.Status(); len(s.ICESessions) != 0 {
		t.Fatalf("session close must prune the entry, got %+v", s.ICESessions)
	}
	// 幂等：重复修剪无副作用。
	m.dropICESession("sess-1")
}

// TestManagerOnStatsMetrics 钉住 /metrics 面：会话按 local_type 计数、
// RTT 进直方图、条目删除后对应 series 归零/移除。
func TestManagerOnStatsMetrics(t *testing.T) {
	m := New(config.P2PConfig{}, "127.0.0.1:9090",
		filepath.Join(t.TempDir(), "token.json"),
		slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), nil)
	rec := metrics.NewMetrics()
	m.SetMetrics(rec)

	m.onICEStats(peer.StatsSnapshot{SessionID: "a", ICELocalCandidateType: "host", ICERTTSeconds: 0.02})
	m.onICEStats(peer.StatsSnapshot{SessionID: "b", ICELocalCandidateType: "host", ICERTTSeconds: 0.03})
	m.onICEStats(peer.StatsSnapshot{SessionID: "c", ICELocalCandidateType: "relay", ICERelayProtocol: "udp", ICERTTSeconds: 0.12})

	if got := testutil.ToFloat64(rec.P2PICESessions.WithLabelValues("host")); got != 2 {
		t.Fatalf("host sessions gauge = %v, want 2", got)
	}
	if got := testutil.ToFloat64(rec.P2PICESessions.WithLabelValues("relay")); got != 1 {
		t.Fatalf("relay sessions gauge = %v, want 1", got)
	}
	if got := testutil.CollectAndCount(rec.P2PICERTTMillis); got != 2 {
		t.Fatalf("rtt histogram series = %v, want 2 (host+relay)", got)
	}

	m.dropICESession("a")
	m.dropICESession("b")
	if got := testutil.ToFloat64(rec.P2PICESessions.WithLabelValues("host")); got != 0 {
		t.Fatalf("host sessions gauge after prune = %v, want series removed/0", got)
	}
}

// TestManagerTunnelDroppedFramesSum 钉住 #13 丢帧观测的 manager 汇总：
// 黑盒驱动隧道入站路径（客户端流 + 不读的消费者 → 投递队列溢出）→
// live 求和 → dropClient 结转 → Status/metrics 双面可见。
func TestManagerTunnelDroppedFramesSum(t *testing.T) {
	rec := metrics.NewMetrics()
	m := New(config.P2PConfig{}, "127.0.0.1:9090", filepath.Join(t.TempDir(), "token.json"),
		slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), nil)
	m.SetMetrics(rec)

	var inbound func([]byte)
	tr := &captureTransport{onMessage: func(h func([]byte)) { inbound = h }}
	live := tunnel.New(tr)
	go live.Serve()
	t.Cleanup(func() { _ = live.Close() })

	// 客户端流（Dial 注册 streams 表；captureTransport 吞掉 open 帧，
	// 本端不读任何数据 = 消费端停转）。
	conn, err := live.Dial("nvr-api")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	m.clientsMu.Lock()
	m.clients[live] = struct{}{}
	m.clientsMu.Unlock()

	frame := func(typ, payload string) []byte {
		b, _ := json.Marshal(map[string]string{"type": typ, "payload": payload})
		return b
	}
	const total = 100 // dataCh 容量 64：36 帧注定溢出
	for i := range total {
		d, _ := json.Marshal(tunnel.TunnelData{
			StreamID: 1, Seq: 0, Total: 1,
			Payload: base64.StdEncoding.EncodeToString([]byte("x")),
		})
		inbound(frame("data", string(d)))
		if i%16 == 15 {
			time.Sleep(10 * time.Millisecond) // 让 Serve 跟上，避免 msgQ 吞帧
		}
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if m.tunnelDroppedTotal() >= total-64 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got := m.tunnelDroppedTotal(); got < total-64 {
		t.Fatalf("TunnelDroppedFrames = %d, want >= %d (queue 64 + %d pushes)", got, total-64, total)
	}
	snap := m.Status()
	if snap.TunnelDroppedFrames == 0 {
		t.Fatal("Status must expose the drop count")
	}
	if !snap.ICEUDPOnly {
		t.Fatal("ICEUDPOnly must be reported true (pion UDP-only)")
	}

	// 结转：会话结束后计数保持单调。
	before := snap.TunnelDroppedFrames
	m.dropClient(live)
	if got := m.Status().TunnelDroppedFrames; got < before {
		t.Fatalf("after dropClient, TunnelDroppedFrames = %d, want >= %d (carried over)", got, before)
	}
	if got := testutil.ToFloat64(rec.P2PTunnelDroppedFrames); got == 0 {
		t.Fatal("metrics nvr_p2p_tunnel_dropped_frames must be published")
	}
}

// captureTransport 记录 OnMessage 注册的入站回调（测试驱动入站帧）。
type captureTransport struct{ onMessage func(func([]byte)) }

func (c *captureTransport) Send([]byte) error        { return nil }
func (c *captureTransport) OnMessage(h func([]byte)) { c.onMessage(h) }
func (c *captureTransport) Close() error             { return nil }

// TestManagerNATObservationStatus 钉住 #14 观测消费链：SDK 观测 →
// StatusSnapshot.nat（含 prefer_relay 判定）+ metrics 单标签发布。
func TestManagerNATObservationStatus(t *testing.T) {
	rec := metrics.NewMetrics()
	m := New(config.P2PConfig{}, "127.0.0.1:9090", filepath.Join(t.TempDir(), "token.json"),
		slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), nil)
	m.SetMetrics(rec)

	if m.Status().NAT != nil {
		t.Fatal("before any observation, NAT must be nil")
	}

	m.onNATObservation(peer.NatObservation{
		Class:         peer.NATSymmetric,
		StunReachable: true,
		Responsive:    2,
		LocalAddr:     "192.168.1.10:52100",
	})
	snap := m.Status()
	if snap.NAT == nil || snap.NAT.Class != "symmetric" {
		t.Fatalf("NAT = %+v, want symmetric", snap.NAT)
	}
	if !snap.NAT.PreferRelay {
		t.Fatal("symmetric NAT must set prefer_relay")
	}
	if got := testutil.ToFloat64(rec.P2PNATClass.WithLabelValues("symmetric")); got != 1 {
		t.Fatalf("nvr_p2p_nat_class{symmetric} = %v, want 1", got)
	}
	if n := testutil.CollectAndCount(rec.P2PNATClass); n != 1 {
		t.Fatalf("exactly one class label must be live, got %d", n)
	}
}
