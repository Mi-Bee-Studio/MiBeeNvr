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
		"enabled":               c.Enabled,
		"server_url":            c.ServerURL,
		"product_id":            c.ProductID,
		"peer_id":               c.PeerID,
		"service_name":          c.ServiceName,
		"username":              c.Username,
		"password_configured":   c.Password != "",
		"client_id":             c.ClientID,
		"transport_timeout_s":   c.TransportTimeoutSec,
		"max_clients":           c.MaxClients,
		"device_key_configured": c.DeviceKeyPath != "",
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
		Enabled             *bool   `json:"enabled"`
		ServerURL           *string `json:"server_url"`
		ProductID           *string `json:"product_id"`
		PeerID              *string `json:"peer_id"`
		ServiceName         *string `json:"service_name"`
		Username            *string `json:"username"`
		Password            *string `json:"password"`
		ClearCredentials    *bool   `json:"clear_credentials"`
		TransportTimeoutSec *int    `json:"transport_timeout_s"`
		MaxClients          *int    `json:"max_clients"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	c := &h.config.P2P
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
	if body.TransportTimeoutSec != nil {
		c.TransportTimeoutSec = *body.TransportTimeoutSec
	}
	if body.MaxClients != nil {
		c.MaxClients = *body.MaxClients
	}
	// peer_id 留空 = 安装级默认（Server.DeviceID，与 App 侧发现锚点同源）。
	if c.PeerID == "" {
		c.PeerID = h.config.Server.DeviceID
	}

	// 落盘前预检（server_url 形态/凭据完备性/max_clients 范围/device_key
	// 文件存在性）；坏配置不落盘不应用。
	if err := h.config.ValidateP2P(); err != nil {
		WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
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
