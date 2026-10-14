package api

// handlers_p2p_viewer_multi_test.go — 多观众注册表 + 连接记录环（2026-10-09
// 用户需求：多个 App 同时连入各自独立记录/显示；连接处理记录在卡片展示）。

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/p2p"
	"github.com/stretchr/testify/require"
)

func TestP2pViewerMultiViewerRoundTrip(t *testing.T) {
	h, _, _ := setupP2PTestHandler(t)
	r := h.Routes()

	// 两个观众先后上报（不同 self_addr = 不同 App）。
	rr := postViewerReport(t, r, map[string]any{
		"mode": "relay", "self_addr": "198.51.100.21:40001",
		"rtt_ms": 87, "kbps_up": 24, "kbps_down": 380, "bytes_total": 1000,
		"app_version": "0.1.3",
	})
	require.Equal(t, http.StatusOK, rr.Code)
	rr = postViewerReport(t, r, map[string]any{
		"mode": "tunnel", "self_addr": "116.22.1.93:13365",
		"rtt_ms": 12, "kbps_up": 300, "kbps_down": 1200, "bytes_total": 2000,
		"app_version": "0.1.4",
	})
	require.Equal(t, http.StatusOK, rr.Code)

	rr = doRequest(t, r, "GET", "/api/p2p/viewer", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	var st struct {
		Viewer *struct {
			SelfAddr string `json:"self_addr"`
			Samples  []any  `json:"samples"`
		} `json:"viewer"`
		Viewers []struct {
			SelfAddr   string `json:"self_addr"`
			Mode       string `json:"mode"`
			RttMs      int    `json:"rtt_ms"`
			AppVersion string `json:"app_version"`
			Stale      bool   `json:"stale"`
			Samples    []any  `json:"samples"`
		} `json:"viewers"`
		Events []p2pLinkEvent `json:"events"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &st))

	// 两观众并存，最新上报者在前。
	require.Len(t, st.Viewers, 2)
	require.Equal(t, "116.22.1.93:13365", st.Viewers[0].SelfAddr)
	require.Equal(t, "tunnel", st.Viewers[0].Mode)
	require.Equal(t, "198.51.100.21:40001", st.Viewers[1].SelfAddr)
	require.Equal(t, "relay", st.Viewers[1].Mode)
	require.False(t, st.Viewers[0].Stale)
	require.False(t, st.Viewers[1].Stale)

	// 兼容字段 viewer = 最新观众，samples 与 viewers[0] 同源。
	require.NotNil(t, st.Viewer)
	require.Equal(t, "116.22.1.93:13365", st.Viewer.SelfAddr)
	require.Len(t, st.Viewer.Samples, 1)
}

// viewer_id 优先定键（mibee-nvr #7）：App 每装机稳定一个 viewer_id，
// 重连/NAT 换端口不再裂成多张卡；旧 App（无 viewer_id）回落 self_addr。
func TestP2pViewerViewerIdKeysAcrossAddrChange(t *testing.T) {
	h, _, _ := setupP2PTestHandler(t)
	r := h.Routes()

	// 同一 App：viewer_id 相同、self_addr 随重连变化。
	for i, addr := range []string{"198.51.100.21:40001", "198.51.100.99:53200"} {
		rr := postViewerReport(t, r, map[string]any{
			"mode": "relay", "self_addr": addr, "viewer_id": "app-9f2c71",
			"rtt_ms": 60 + i, "kbps_up": 24, "kbps_down": 380,
			"bytes_total": 1000, "app_version": "0.1.4",
		})
		require.Equal(t, http.StatusOK, rr.Code)
	}
	// 换地址重连仍是同一条记录，不是两张卡。
	require.Len(t, h.p2pViewer.viewers, 1)
	entry, ok := h.p2pViewer.viewers["app-9f2c71"]
	require.True(t, ok, "键应为 viewer_id")
	require.Equal(t, "198.51.100.99:53200", entry.latest.SelfAddr)

	// 旧 App 无 viewer_id → 回落 self_addr 键（各自一卡，互不合并）。
	rr := postViewerReport(t, r, map[string]any{
		"mode": "tunnel", "self_addr": "116.22.1.93:13365",
		"rtt_ms": 12, "kbps_up": 300, "kbps_down": 1200,
		"bytes_total": 2000, "app_version": "0.1.3",
	})
	require.Equal(t, http.StatusOK, rr.Code)
	require.Len(t, h.p2pViewer.viewers, 2)
}

func TestP2pViewerSameAddrUpdatesOneEntry(t *testing.T) {
	h, _, _ := setupP2PTestHandler(t)
	r := h.Routes()

	for i := 0; i < 3; i++ {
		rr := postViewerReport(t, r, map[string]any{
			"mode": "tunnel", "self_addr": "116.22.1.93:13365", "rtt_ms": 10 + i,
		})
		require.Equal(t, http.StatusOK, rr.Code)
	}
	require.Len(t, h.p2pViewer.viewers, 1)
	require.Len(t, h.p2pViewer.viewers["116.22.1.93:13365"].samples, 3)
}

func TestP2pViewerCapacityEviction(t *testing.T) {
	h, _, _ := setupP2PTestHandler(t)
	r := h.Routes()

	for i := 0; i < p2pViewerMax+2; i++ {
		rr := postViewerReport(t, r, map[string]any{
			"mode": "relay", "self_addr": string(rune('a'+i)) + ".example:1", "rtt_ms": i,
		})
		require.Equal(t, http.StatusOK, rr.Code)
	}
	h.p2pViewer.mu.Lock()
	n := len(h.p2pViewer.viewers)
	_, firstStillThere := h.p2pViewer.viewers["a.example:1"]
	h.p2pViewer.mu.Unlock()
	require.Equal(t, p2pViewerMax, n)
	// 最早上报的观众被逐出。
	require.False(t, firstStillThere)
}

func TestP2pViewerGCAfterIdle(t *testing.T) {
	h, _, _ := setupP2PTestHandler(t)

	// 预置一个 31 分钟前上报的观众，再让新观众上报触发 GC。
	h.p2pViewer.mu.Lock()
	h.p2pViewer.viewers = map[string]*p2pViewerEntry{
		"zombie.example:9": {
			updatedAt: time.Now().Add(-31 * time.Minute),
		},
	}
	h.p2pViewer.mu.Unlock()

	rr := postViewerReport(t, h.Routes(), map[string]any{
		"mode": "tunnel", "self_addr": "1.2.3.4:5", "rtt_ms": 5,
	})
	require.Equal(t, http.StatusOK, rr.Code)

	h.p2pViewer.mu.Lock()
	_, zombieLeft := h.p2pViewer.viewers["zombie.example:9"]
	n := len(h.p2pViewer.viewers)
	h.p2pViewer.mu.Unlock()
	require.False(t, zombieLeft)
	require.Equal(t, 1, n)
}

func TestP2pLinkEventRingAndFileLog(t *testing.T) {
	h, _, _ := setupP2PTestHandler(t)
	r := h.Routes()

	h.RecordP2pLinkEvent(p2p.Event{At: time.Now().Add(-2 * time.Second), Type: "online", Detail: "signaling connected"})
	h.RecordP2pLinkEvent(p2p.Event{At: time.Now().Add(-1 * time.Second), Type: "tunnel_up", Detail: "clients=1"})
	h.RecordP2pLinkEvent(p2p.Event{At: time.Now(), Type: "tunnel_down", Detail: "active=0"})

	// GET 返回事件（旧→新）。
	rr := doRequest(t, r, "GET", "/api/p2p/viewer", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	var st struct {
		Events []p2pLinkEvent `json:"events"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &st))
	require.Len(t, st.Events, 3)
	require.Equal(t, "online", st.Events[0].Type)
	require.Equal(t, "tunnel_up", st.Events[1].Type)
	require.Equal(t, "tunnel_down", st.Events[2].Type)

	// 落盘：events.jsonl 三行，字段完整。
	raw, err := os.ReadFile(filepath.Join(h.store.RootDir(), "diag", "viewer", "events.jsonl"))
	require.NoError(t, err)
	lines := splitLines(string(raw))
	require.Len(t, lines, 3)
	require.Contains(t, lines[1], `"type":"tunnel_up"`)
	require.Contains(t, lines[2], `"detail":"active=0"`)
}

// splitLines 拆行并剔除尾空行。
func splitLines(s string) []string {
	var out []string
	for _, l := range strings.Split(string(s), "\n") {
		l = strings.TrimRight(l, "\r")
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}
