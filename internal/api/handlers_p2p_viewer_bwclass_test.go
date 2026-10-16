package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestP2pViewerBandwidthClassRoundTrip：App 弱带宽分级上报 → 状态面
// （viewers + 兼容 viewer + samples 历史点）携带；垃圾值拒收；旧 App
// 不带字段（空）兼容。
func TestP2pViewerBandwidthClassRoundTrip(t *testing.T) {
	h, _, _ := setupP2PTestHandler(t)
	r := h.Routes()

	rr := postViewerReport(t, r, map[string]any{
		"mode": "relay", "self_addr": "198.51.100.31:40001",
		"rtt_ms": 210, "kbps_up": 30, "kbps_down": 420, "bytes_total": 1000,
		"bandwidth_class": "starved", "app_version": "0.1.9",
	})
	require.Equal(t, http.StatusOK, rr.Code)

	// 旧 App 无字段 → 接受（空串不落 JSON 键）。
	rr = postViewerReport(t, r, map[string]any{
		"mode": "relay", "self_addr": "198.51.100.32:40002",
		"rtt_ms": 40, "kbps_up": 30, "kbps_down": 2400, "bytes_total": 1000,
		"app_version": "0.1.8",
	})
	require.Equal(t, http.StatusOK, rr.Code)

	// 垃圾分级拒收（防上游笔误污染曲线）。
	rr = postViewerReport(t, r, map[string]any{
		"mode": "relay", "self_addr": "198.51.100.33:40003",
		"rtt_ms": 40, "kbps_up": 30, "kbps_down": 2400,
		"bandwidth_class": "hungry",
	})
	require.Equal(t, http.StatusBadRequest, rr.Code)

	rr = doRequest(t, r, "GET", "/api/p2p/viewer", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	var st struct {
		Viewer *struct {
			BandwidthClass string `json:"bandwidth_class"`
		} `json:"viewer"`
		Viewers []struct {
			SelfAddr       string `json:"self_addr"`
			BandwidthClass string `json:"bandwidth_class"`
			Samples        []struct {
				BandwidthClass string `json:"bandwidth_class"`
			} `json:"samples"`
		} `json:"viewers"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &st))
	require.Len(t, st.Viewers, 2)

	// 最新上报者（无字段的旧 App）在前：空串；带分级者在后。
	require.Empty(t, st.Viewers[0].BandwidthClass)
	require.Equal(t, "starved", st.Viewers[1].BandwidthClass)
	require.Len(t, st.Viewers[1].Samples, 1)
	require.Equal(t, "starved", st.Viewers[1].Samples[0].BandwidthClass)

	// 兼容 viewer = 最新上报者（本例为空串——旧语义不破）。
	require.NotNil(t, st.Viewer)
}
