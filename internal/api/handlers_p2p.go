package api

// handlers_p2p.go — P2P 外网接入的产品面：状态诊断 + 设置读写（热生效）。
// 设置 PUT 持久化 yaml（p2p.password 走 ENC 加密落盘）后经 P2PController
// 即时重建链路，无需重启进程、无需 ssh 改文件。

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/p2p"
)

// P2PController 是 pkg/app p2pAgent 暴露给 API 层的控制面（状态快照 +
// 热应用配置）。nil（单测/能力缺席）时状态端点只报 unavailable。
type P2PController interface {
	Status() p2p.StatusSnapshot
	Apply(cfg config.P2PConfig)
}

// SetP2PAgent wires the hot-reconfigurable P2P agent.
func (h *Handler) SetP2PAgent(c P2PController) { h.p2pAgent = c }

// handleGetP2PStatus 报告 P2P 链路诊断快照（在线/活跃客户端/并发上限与
// 其来源/授权门控/最近一次平台策略拒绝）。
func (h *Handler) handleGetP2PStatus(w http.ResponseWriter, _ *http.Request) {
	if h.p2pAgent == nil {
		writeJSON(w, http.StatusOK, map[string]any{"available": false, "enabled": false})
		return
	}
	writeJSON(w, http.StatusOK, h.p2pAgent.Status())
}

// handleGetP2PSettings 回显 p2p 配置（密码只报是否已配，永不回传）。
func (h *Handler) handleGetP2PSettings(w http.ResponseWriter, _ *http.Request) {
	if h.config == nil {
		WriteError(w, http.StatusInternalServerError, "config not available")
		return
	}
	c := h.config.P2P
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled":                     c.Enabled,
		"server_url":                  c.ServerURL,
		"product_id":                  c.ProductID,
		"peer_id":                     c.PeerID,
		"service_name":                c.ServiceName,
		"username":                    c.Username,
		"password_configured":         c.Password != "",
		"client_id":                   c.ClientID,
		"nodes_url":                   c.NodesURL,
		"transport_timeout_s":         c.TransportTimeoutSec,
		"force_relay":                 c.ForceRelay.Normalized(),
		"max_clients":                 c.MaxClients,
		"device_key_configured":       c.DeviceKeyPath != "",
		"advertise":                   c.Advertise,
		"ice_keepalive_interval_ms":   c.ICEKeepaliveIntervalMS,
		"ice_disconnected_timeout_ms": c.ICEDisconnectedTimeoutMS,
		"ice_failed_timeout_ms":       c.ICEFailedTimeoutMS,
		"ice_receive_mtu":             c.ICEReceiveMTU,
		"tunnel_max_payload_bytes":    c.TunnelMaxPayloadBytes,
		"ice_udp_only":                true, // pion v4 仅 UDP 候选（TCP 上游未支持）
	})
}

// handleUpdateP2PSettings 更新 p2p 配置。body 字段均可选（nil = 不变）；
// 密码空串 = 保持现状（GET 不回传，UI 空框往返）；clear_credentials 一键
// 清空账号密码。校验通过 → 存 yaml（ENC）→ 热应用（重建链路）。
func (h *Handler) handleUpdateP2PSettings(w http.ResponseWriter, r *http.Request) {
	if h.config == nil {
		WriteError(w, http.StatusInternalServerError, "config not available")
		return
	}

	var body struct {
		Enabled                  *bool   `json:"enabled"`
		ServerURL                *string `json:"server_url"`
		ProductID                *string `json:"product_id"`
		PeerID                   *string `json:"peer_id"`
		ServiceName              *string `json:"service_name"`
		Username                 *string `json:"username"`
		Password                 *string `json:"password"`
		ClearCredentials         *bool   `json:"clear_credentials"`
		NodesURL                 *string `json:"nodes_url"`
		TransportTimeoutSec      *int    `json:"transport_timeout_s"`
		ForceRelay               *string `json:"force_relay"`
		MaxClients               *int    `json:"max_clients"`
		Advertise                *string `json:"advertise"`
		ICEKeepaliveIntervalMS   *int    `json:"ice_keepalive_interval_ms"`
		ICEDisconnectedTimeoutMS *int    `json:"ice_disconnected_timeout_ms"`
		ICEFailedTimeoutMS       *int    `json:"ice_failed_timeout_ms"`
		ICEReceiveMTU            *int    `json:"ice_receive_mtu"`
		TunnelMaxPayloadBytes    *int    `json:"tunnel_max_payload_bytes"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// 副本上应用+校验，通过才提交内存与磁盘——被拒请求不得留下脏内存
	// （预存缺陷：此前直接改 h.config.P2P，校验 400 后内存已被污染，
	// 下一次成功 PUT 会把污染值一并落盘/热应用）。
	c := &h.config.P2P
	next := *c
	c = &next
	if body.Enabled != nil {
		c.Enabled = *body.Enabled
	}
	if body.ServerURL != nil {
		c.ServerURL = strings.TrimSpace(*body.ServerURL)
	}
	if body.ProductID != nil {
		c.ProductID = strings.TrimSpace(*body.ProductID)
	}
	if body.PeerID != nil {
		c.PeerID = strings.TrimSpace(*body.PeerID)
	}
	if body.ServiceName != nil {
		c.ServiceName = strings.TrimSpace(*body.ServiceName)
	}
	if body.Username != nil {
		c.Username = strings.TrimSpace(*body.Username)
	}
	if body.Password != nil && *body.Password != "" {
		c.Password = *body.Password
	}
	if body.ClearCredentials != nil && *body.ClearCredentials {
		c.Username, c.Password = "", ""
	}
	if body.NodesURL != nil {
		c.NodesURL = strings.TrimSpace(*body.NodesURL)
	}
	if body.TransportTimeoutSec != nil {
		c.TransportTimeoutSec = *body.TransportTimeoutSec
	}
	if body.ForceRelay != nil {
		switch v := strings.ToLower(strings.TrimSpace(*body.ForceRelay)); v {
		case "", "false":
			c.ForceRelay = config.ForceRelayDefault
		case "true":
			c.ForceRelay = config.ForceRelayAlways
		case "auto":
			c.ForceRelay = config.ForceRelayAuto
		default:
			WriteError(w, http.StatusBadRequest, "force_relay must be true/false/auto")
			return
		}
	}
	if body.MaxClients != nil {
		c.MaxClients = *body.MaxClients
	}
	if body.Advertise != nil {
		c.Advertise = strings.TrimSpace(*body.Advertise)
	}
	if body.ICEKeepaliveIntervalMS != nil {
		c.ICEKeepaliveIntervalMS = *body.ICEKeepaliveIntervalMS
	}
	if body.ICEDisconnectedTimeoutMS != nil {
		c.ICEDisconnectedTimeoutMS = *body.ICEDisconnectedTimeoutMS
	}
	if body.ICEFailedTimeoutMS != nil {
		c.ICEFailedTimeoutMS = *body.ICEFailedTimeoutMS
	}
	if body.ICEReceiveMTU != nil {
		c.ICEReceiveMTU = *body.ICEReceiveMTU
	}
	if body.TunnelMaxPayloadBytes != nil {
		c.TunnelMaxPayloadBytes = *body.TunnelMaxPayloadBytes
	}
	// peer_id 留空 = 安装级默认（Server.DeviceID，与 App 侧发现锚点同源）。
	if c.PeerID == "" {
		c.PeerID = h.config.Server.DeviceID
	}

	// 落盘前预检（server_url 形态/凭据完备性/max_clients 范围/device_key
	// 文件存在性/advertise 形态）；坏配置不落盘不应用。
	if err := config.ValidateP2PConfig(c); err != nil {
		WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	h.config.P2P = next
	if err := config.Save(h.configPath, h.config); err != nil {
		logger.Warn("failed to save config", "error", err)
		WriteError(w, http.StatusInternalServerError, "failed to save config")
		return
	}

	if h.p2pAgent != nil {
		h.p2pAgent.Apply(h.config.P2P)
	}
	logger.Info("p2p settings updated", "enabled", c.Enabled)

	if h.p2pAgent != nil {
		writeJSON(w, http.StatusOK, h.p2pAgent.Status())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "updated"})
}
