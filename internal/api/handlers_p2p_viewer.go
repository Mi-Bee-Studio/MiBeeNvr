package api

// handlers_p2p_viewer.go — App（viewer）侧互联质量上报 + 仪表盘读取。
//
// 背景（2026-10-07 用户需求）：NVR 仪表盘要能看到远程 App 的公网地址与
// 互联质量（延时/带宽/流量）。质量数据由 App 自采（隧道内 HTTP 探测 =
// 真实往返；本地桥字节计数 = 真实吞吐），经既有认证通道周期上报——
// NVR 端不依赖 SDK 统计（relay 会话没有 WebRTC DataChannel 统计可用）。
//
// 可见性口径（用户裁定）：仪表盘卡片只在「P2P 已启用 且 已关联（账号
// 绑定确认）」时显示；取消关联/未绑定即隐藏——由 GET 响应里的
// enabled/associated 两字段驱动，SPA 侧据此渲染。

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/p2p"
)

// p2pViewerSample 一次上报换算出的质量采样点（历史曲线用）。
type p2pViewerSample struct {
	At       time.Time `json:"at"`
	Mode     string    `json:"mode"` // tunnel|relay
	RttMs    int       `json:"rtt_ms"`
	KbpsUp   int       `json:"kbps_up"`
	KbpsDown int       `json:"kbps_down"`
}

const viewerHistoryMaxBytes = 5 << 20 // rotate: <name>.jsonl → <name>-1.jsonl

// appendViewerJSONL 追加一行 JSONL 到 <root>/diag/viewer/<name>（超限轮转
// 为 <name>-1.jsonl）。失败静默（观测数据，不挡上报/请求）。
func (h *Handler) appendViewerJSONL(name string, line []byte) {
	dir := filepath.Join(h.store.RootDir(), "diag", "viewer")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	path := filepath.Join(dir, name)
	if fi, err := os.Stat(path); err == nil && fi.Size() > viewerHistoryMaxBytes {
		_ = os.Rename(path, filepath.Join(dir, name[:len(name)-len(".jsonl")]+"-1.jsonl"))
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(line, 10))
}

// p2pViewerReport App 上报体（字段口径见 App 侧 postP2pViewerReport）。
// rtt_* 为应用层（HTTP 过隧道）窗口统计；net_rtt_ms 为直连隧道的
// ICE 传输层真实往返（「网络差」vs「隧道被视频流量占满」的分离证据）。
type p2pViewerReport struct {
	Mode       string `json:"mode"` // tunnel|relay
	ViewerID   string `json:"viewer_id,omitempty"` // App 装机稳定标识（fix4 起上报）
	SelfAddr   string `json:"self_addr"`
	RttMs      int    `json:"rtt_ms"`
	RttMinMs   *int   `json:"rtt_min_ms,omitempty"`
	RttMaxMs   *int   `json:"rtt_max_ms,omitempty"`
	NetRttMs   *int   `json:"net_rtt_ms,omitempty"`
	KbpsUp     int    `json:"kbps_up"`
	KbpsDown   int    `json:"kbps_down"`
	BytesTotal int64  `json:"bytes_total"`
	AppVersion string `json:"app_version"`
}

const p2pViewerSampleCap = 240 // 30s 间隔 ≈ 2h 环形历史

// p2pViewerMax 同时记录的观众（App）上限——超出逐出最久无更新者；被逐出
// 观众的连接记录仍在事件环/落盘日志里。
const p2pViewerMax = 8

// p2pViewerGCAfter 无上报多久后把观众从注册表清掉（质量曲线已无意义；
// 3 分钟即 stale，30 分钟后整条移除，事件历史不受影响）。
const p2pViewerGCAfter = 30 * time.Minute

// p2pViewerEntry 单个观众（App）的上报态。身份键 = viewer_id（App 每
// 装机稳定的随机标识，2026-10-13 #7：NAT 换端口/重连不再裂卡）；
// 旧 App 无 viewer_id 回落 self_addr 键（公网 ip:port，重连即新键，
// 旧条目走 GC）。
func (r p2pViewerReport) viewerKey() string {
	if r.ViewerID != "" {
		return r.ViewerID
	}
	return r.SelfAddr
}
type p2pViewerEntry struct {
	latest    p2pViewerReport
	updatedAt time.Time
	samples   []p2pViewerSample
}

type p2pViewerState struct {
	mu      sync.Mutex
	viewers map[string]*p2pViewerEntry
}

// p2pLinkEvent 仪表盘「连接记录」条目：Manager 链路事件的透传形态
// （tunnel_up/tunnel_down/closed/connect_request/rejected/online/offline/
// platform_rejected），由 p2p.Manager.OnEvent 实时推入。
type p2pLinkEvent struct {
	At        time.Time `json:"at"`
	Type      string    `json:"type"`
	SessionID string    `json:"session_id,omitempty"`
	Detail    string    `json:"detail,omitempty"`
}

// p2pLinkEventCap 内存事件环上限（≈ 最近 200 条连接记录；落盘无上限，
// 轮转见 appendViewerJSONL）。
const p2pLinkEventCap = 200

// RecordP2pLinkEvent 收一条链路事件（pkg/app 经 Manager.OnEvent 接线）：
// 落内存环 + 追加 events.jsonl（连接处理记录，重启后仍可查）。
func (h *Handler) RecordP2pLinkEvent(ev p2p.Event) {
	e := p2pLinkEvent{At: ev.At, Type: ev.Type, SessionID: ev.SessionID, Detail: ev.Detail}
	h.p2pEventsMu.Lock()
	h.p2pEvents = append(h.p2pEvents, e)
	if len(h.p2pEvents) > p2pLinkEventCap {
		h.p2pEvents = h.p2pEvents[len(h.p2pEvents)-p2pLinkEventCap:]
	}
	h.p2pEventsMu.Unlock()
	if raw, err := json.Marshal(e); err == nil {
		h.appendViewerJSONL("events.jsonl", raw)
	}
}

// p2pLinkEvents 返回事件环副本（旧→新）。
func (h *Handler) p2pLinkEvents() []p2pLinkEvent {
	h.p2pEventsMu.Lock()
	defer h.p2pEventsMu.Unlock()
	out := make([]p2pLinkEvent, len(h.p2pEvents))
	copy(out, h.p2pEvents)
	return out
}

// RecordP2pViewerReport 记录一次 App 上报（进程级内存态——仪表盘观测
// 数据，重启清零可接受；连接记录在事件环/落盘，不受影响）。多观众各记
// 各的；容量超限逐出最久无更新者，30 分钟无上报整条 GC。
func (h *Handler) RecordP2pViewerReport(r p2pViewerReport) {
	now := time.Now()
	h.p2pViewer.mu.Lock()
	defer h.p2pViewer.mu.Unlock()
	if h.p2pViewer.viewers == nil {
		h.p2pViewer.viewers = make(map[string]*p2pViewerEntry)
	}
	key := r.viewerKey()
	if e, ok := h.p2pViewer.viewers[key]; ok {
		e.latest = r
		e.updatedAt = now
		e.samples = append(e.samples, p2pViewerSample{
			At: now, Mode: r.Mode, RttMs: r.RttMs, KbpsUp: r.KbpsUp, KbpsDown: r.KbpsDown,
		})
		if len(e.samples) > p2pViewerSampleCap {
			e.samples = e.samples[len(e.samples)-p2pViewerSampleCap:]
		}
	} else {
		if len(h.p2pViewer.viewers) >= p2pViewerMax {
			oldestKey, oldestAt := "", now
			for k, v := range h.p2pViewer.viewers {
				if v.updatedAt.Before(oldestAt) {
					oldestKey, oldestAt = k, v.updatedAt
				}
			}
			delete(h.p2pViewer.viewers, oldestKey)
		}
		h.p2pViewer.viewers[key] = &p2pViewerEntry{
			latest:    r,
			updatedAt: now,
			samples: []p2pViewerSample{{
				At: now, Mode: r.Mode, RttMs: r.RttMs, KbpsUp: r.KbpsUp, KbpsDown: r.KbpsDown,
			}},
		}
	}
	// 顺带 GC：清掉 30 分钟无上报的僵尸条目。
	for k, v := range h.p2pViewer.viewers {
		if now.Sub(v.updatedAt) > p2pViewerGCAfter {
			delete(h.p2pViewer.viewers, k)
		}
	}
}

// handleP2pViewerReport POST /api/p2p/viewer-report：App 周期上报。
// 走既有鉴权（App 的会话/设备 token），非法 mode 拒收。
func (h *Handler) handleP2pViewerReport(w http.ResponseWriter, r *http.Request) {
	var body p2pViewerReport
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if body.Mode != "tunnel" && body.Mode != "relay" {
		WriteError(w, http.StatusBadRequest, "mode must be tunnel|relay")
		return
	}
	if body.RttMs < 0 || body.RttMs > 60000 || body.KbpsUp < 0 || body.KbpsDown < 0 ||
		body.KbpsUp > 1000000 || body.KbpsDown > 1000000 {
		WriteError(w, http.StatusBadRequest, "values out of range")
		return
	}
	h.RecordP2pViewerReport(body)
	if raw, err := json.Marshal(struct {
		At time.Time `json:"at"`
		p2pViewerReport
	}{time.Now(), body}); err == nil {
		h.appendViewerJSONL("history.jsonl", raw)
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

// handleGetP2pViewer GET /api/p2p/viewer：仪表盘卡片数据源。
// enabled/associated 驱动卡片可见性；viewers 为空数组 = 尚无上报（已关联
// 但 App 未连过/刚重启）。viewer 字段 = 最新上报的观众（兼容旧前端）；
// events = 连接记录（Manager 链路事件环，旧→新）。
func (h *Handler) handleGetP2pViewer(w http.ResponseWriter, _ *http.Request) {
	associated := false
	displayName := ""
	if h.config != nil && h.config.P2P.Association != nil &&
		h.config.P2P.Association.Confirmed != nil {
		associated = true
		displayName = h.config.P2P.Association.Confirmed.DisplayName
	}
	enabled := h.config != nil && h.config.P2P.Enabled
	online := false
	var active int
	if h.p2pAgent != nil {
		st := h.p2pAgent.Status()
		online = st.Online
		active = st.ActiveClients
	}

	resp := map[string]any{
		"enabled":     enabled,
		"associated":  associated,
		"online":      online,
		"clients":     active,
		"viewer":      nil,
		"viewers":     []any{},
		"events":      []any{},
		"associ_name": displayName,
	}

	type viewerOut struct {
		ViewerID   string            `json:"viewer_id,omitempty"`
		SelfAddr   string            `json:"self_addr"`
		Mode       string            `json:"mode"`
		RttMs      int               `json:"rtt_ms"`
		NetRttMs   *int              `json:"net_rtt_ms,omitempty"`
		KbpsUp     int               `json:"kbps_up"`
		KbpsDown   int               `json:"kbps_down"`
		BytesTotal int64             `json:"bytes_total"`
		AppVersion string            `json:"app_version"`
		UpdatedAt  string            `json:"updated_at"`
		Stale      bool              `json:"stale"`
		Samples    []p2pViewerSample `json:"samples"`
	}

	h.p2pViewer.mu.Lock()
	if len(h.p2pViewer.viewers) > 0 {
		// 按最近上报时间新→旧排列；3 分钟无上报标 stale。
		entries := make([]*p2pViewerEntry, 0, len(h.p2pViewer.viewers))
		for _, e := range h.p2pViewer.viewers {
			entries = append(entries, e)
		}
		sort.Slice(entries, func(i, j int) bool {
			return entries[i].updatedAt.After(entries[j].updatedAt)
		})
		outs := make([]viewerOut, 0, len(entries))
		for _, e := range entries {
			stale := time.Since(e.updatedAt) > 3*time.Minute
			outs = append(outs, viewerOut{
				ViewerID:   e.latest.ViewerID,
				SelfAddr:   e.latest.SelfAddr,
				Mode:       e.latest.Mode,
				RttMs:      e.latest.RttMs,
				NetRttMs:   e.latest.NetRttMs,
				KbpsUp:     e.latest.KbpsUp,
				KbpsDown:   e.latest.KbpsDown,
				BytesTotal: e.latest.BytesTotal,
				AppVersion: e.latest.AppVersion,
				UpdatedAt:  e.updatedAt.UTC().Format(time.RFC3339),
				Stale:      stale,
				Samples:    append([]p2pViewerSample(nil), e.samples...),
			})
		}
		resp["viewers"] = outs
		if len(outs) > 0 {
			// 兼容字段：取最新观众拍平成旧结构（samples 同源）。
			resp["viewer"] = map[string]any{
				"self_addr":   outs[0].SelfAddr,
				"mode":        outs[0].Mode,
				"rtt_ms":      outs[0].RttMs,
				"kbps_up":     outs[0].KbpsUp,
				"kbps_down":   outs[0].KbpsDown,
				"bytes_total": outs[0].BytesTotal,
				"app_version": outs[0].AppVersion,
				"updated_at":  outs[0].UpdatedAt,
				"stale":       outs[0].Stale,
				"samples":     outs[0].Samples,
			}
		}
	}
	h.p2pViewer.mu.Unlock()

	if evs := h.p2pLinkEvents(); len(evs) > 0 {
		resp["events"] = evs
	}
	writeJSON(w, http.StatusOK, resp)
}
