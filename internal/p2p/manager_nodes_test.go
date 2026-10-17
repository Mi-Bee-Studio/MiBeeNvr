package p2p

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	peer "github.com/Mi-Bee-Studio/MiBeeNvr/internal/p2psdk"
)

// TestManagerDialPassesNodesURL 钉住 nodes_url 透传（#21 NVR 侧接线）：
// 配置原样进 SignalingConfig.NodesURL；缺省空 = 不启用（SDK 单节点
// 行为逐字节不变）。
func TestManagerDialPassesNodesURL(t *testing.T) {
	var gotCfg *peer.SignalingConfig
	saved := peerDial
	peerDial = func(_ context.Context, sc peer.SignalingConfig) (*peer.Session, error) {
		cp := sc
		gotCfg = &cp
		return nil, errors.New("stop")
	}
	t.Cleanup(func() { peerDial = saved })

	m := New(config.P2PConfig{
		ServerURL: "wss://nodes-test.invalid/signal",
		Username:  "device:mibeenvr:nvr-x",
		NodesURL:  "https://nodes-test.invalid/api/nodes",
	}, "127.0.0.1:9090", filepath.Join(t.TempDir(), "token.json"),
		slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), nil)

	if err := m.runSession(context.Background(), "jwt"); err == nil {
		t.Fatal("dial failure must propagate")
	}
	if gotCfg == nil {
		t.Fatal("peerDial must have been called")
	}
	if gotCfg.NodesURL != "https://nodes-test.invalid/api/nodes" {
		t.Fatalf("NodesURL = %q, want the configured nodes endpoint", gotCfg.NodesURL)
	}

	// 缺省：不配置 = 空串（SDK 侧零行为变化）。
	m2 := New(config.P2PConfig{
		ServerURL: "wss://nodes-test.invalid/signal",
		Username:  "device:mibeenvr:nvr-x",
	}, "127.0.0.1:9090", filepath.Join(t.TempDir(), "token.json"),
		slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), nil)
	gotCfg = nil
	if err := m2.runSession(context.Background(), "jwt"); err == nil {
		t.Fatal("dial failure must propagate")
	}
	if gotCfg.NodesURL != "" {
		t.Fatalf("unset NodesURL must stay empty, got %q", gotCfg.NodesURL)
	}
}

// TestStatusNodeSelectionNilWhenUnconfigured 状态面（#21 观测）：
// 未配置 nodes_url 时 /api/p2p/status 不携带 node_selection 块。
func TestStatusNodeSelectionNilWhenUnconfigured(t *testing.T) {
	m := New(config.P2PConfig{}, "127.0.0.1:9090",
		filepath.Join(t.TempDir(), "token.json"),
		slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), nil)
	if snap := m.Status(); snap.NodeSelection != nil {
		t.Fatalf("node_selection must be nil without nodes_url, got %+v", snap.NodeSelection)
	}
}
