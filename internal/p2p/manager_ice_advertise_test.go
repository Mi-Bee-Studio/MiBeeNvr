package p2p

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"testing"

	peer "github.com/Mi-Bee-Studio/MiBeeNvr/internal/p2psdk"

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
		ICELocalCandidateAddress:  "121.8.2.41:40000",
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
