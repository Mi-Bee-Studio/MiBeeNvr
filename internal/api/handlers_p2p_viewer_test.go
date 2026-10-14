package api

// handlers_p2p_viewer_test.go — App 互联质量上报/读取端点（仪表盘 P2P
// 卡片数据源，2026-10-07 用户需求）。

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/stretchr/testify/require"
)

func postViewerReport(t *testing.T, r http.Handler, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, "/api/p2p/viewer-report", bytes.NewReader(raw))
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	return rr
}

func TestP2pViewerReportRoundTrip(t *testing.T) {
	h, cfg, _ := setupP2PTestHandler(t)
	r := h.Routes()

	// 未关联未启用：GET 报 enabled=false/associated=false，viewer 空。
	rr := doRequest(t, r, "GET", "/api/p2p/viewer", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	var st map[string]any
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &st))
	require.Equal(t, false, st["enabled"])
	require.Equal(t, false, st["associated"])
	require.Nil(t, st["viewer"])

	// 合法上报 → 200。
	rr = postViewerReport(t, r, map[string]any{
		"mode": "relay", "self_addr": "192.0.2.10:40001",
		"rtt_ms": 87, "rtt_min_ms": 60, "rtt_max_ms": 140, "net_rtt_ms": 33,
		"kbps_up": 24, "kbps_down": 380,
		"bytes_total": 25123456, "app_version": "0.1.3-debug+6107",
	})
	require.Equal(t, http.StatusOK, rr.Code)

	// 非法 mode → 400；越界 rtt → 400。
	rr = postViewerReport(t, r, map[string]any{"mode": "direct"})
	require.Equal(t, http.StatusBadRequest, rr.Code)
	rr = postViewerReport(t, r, map[string]any{"mode": "tunnel", "rtt_ms": 999999})
	require.Equal(t, http.StatusBadRequest, rr.Code)

	// 读回：viewer 就位，含历史采样。
	rr = doRequest(t, r, "GET", "/api/p2p/viewer", nil, "", "")
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &st))
	viewer := st["viewer"].(map[string]any)
	require.Equal(t, "relay", viewer["mode"])
	require.Equal(t, "192.0.2.10:40001", viewer["self_addr"])
	require.Equal(t, float64(87), viewer["rtt_ms"])
	samples := viewer["samples"].([]any)
	require.Len(t, samples, 1)
	require.Equal(t, false, viewer["stale"])

	// 历史落盘：一行 JSONL（含窗口统计与网络层 RTT 分离字段）。
	histPath := filepath.Join(h.store.RootDir(), "diag", "viewer", "history.jsonl")
	rawLine, err := os.ReadFile(histPath)
	require.NoError(t, err)
	require.Contains(t, string(rawLine), `"mode":"relay"`)
	require.Contains(t, string(rawLine), `"app_version":"0.1.3-debug+6107"`)

	// 关联确认后：associated=true + 展示名回传（仪表盘卡片可见性口径）。
	cfg.P2P.Association = &config.P2PAssociationState{
		Confirmed: &config.P2PAssociationEntry{DisplayName: "小米手机 · 阿明"},
	}
	rr = doRequest(t, r, "GET", "/api/p2p/viewer", nil, "", "")
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &st))
	require.Equal(t, true, st["associated"])
	require.Equal(t, "小米手机 · 阿明", st["associ_name"])
}

func TestP2pViewerReportGarbageBody(t *testing.T) {
	h, _, _ := setupP2PTestHandler(t)
	r := h.Routes()
	req := httptest.NewRequest(http.MethodPost, "/api/p2p/viewer-report", bytes.NewReader([]byte("not json")))
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	require.Equal(t, http.StatusBadRequest, rr.Code)
}
