package api

// handlers_p2p_association.go — 「App 发起账号授权关联」本地状态面（产品化
// M1，docs/05 方案）。App 在 LAN 内（持正常管理凭据）发起 request；设备
// 主人在 NVR Web 上 confirm；之后 App 经 PUT /api/settings/p2p 下发运行
// 配置（复用既有端点，热生效）。平台侧 user-device 绑定上报等
// 平台联调后接入——本文件只管本地状态机。
//
// 状态机：pending(至多一) --confirm--> confirmed(至多一) --cancel--> 空。
// 新 request 覆盖旧 pending（App 侧重试/换账号幂等）；新 confirm 覆盖旧
// confirmed（家庭单主模型）。

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/p2p"
)

// handleGetP2PAssociation 回报关联状态（pending/confirmed，均可能为 null）。
func (h *Handler) handleGetP2PAssociation(w http.ResponseWriter, _ *http.Request) {
	if h.config == nil {
		WriteError(w, http.StatusInternalServerError, "config not available")
		return
	}
	st := h.config.P2P.Association
	if st == nil {
		st = &config.P2PAssociationState{}
	}
	writeJSON(w, http.StatusOK, st)
}

// handleRequestP2PAssociation App 发起关联申请。同一时刻只保留最新一条
// pending（覆盖旧申请——重试与换账号都幂等）；confirmed 不受影响。
func (h *Handler) handleRequestP2PAssociation(w http.ResponseWriter, r *http.Request) {
	if h.config == nil {
		WriteError(w, http.StatusInternalServerError, "config not available")
		return
	}
	var body struct {
		PlatformUserID   string `json:"platform_user_id"`
		PlatformUsername string `json:"platform_username"`
		DisplayName      string `json:"display_name"`
		RequestID        string `json:"request_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	body.PlatformUserID = strings.TrimSpace(body.PlatformUserID)
	if body.PlatformUserID == "" {
		WriteError(w, http.StatusBadRequest, "platform_user_id is required")
		return
	}
	if body.RequestID == "" {
		body.RequestID = time.Now().UTC().Format("20060102T150405.000000000")
	}

	st := h.config.P2P.Association
	if st == nil {
		st = &config.P2PAssociationState{}
		h.config.P2P.Association = st
	}
	st.Pending = &config.P2PAssociationEntry{
		PlatformUserID:   body.PlatformUserID,
		PlatformUsername: strings.TrimSpace(body.PlatformUsername),
		DisplayName:      strings.TrimSpace(body.DisplayName),
		RequestID:        body.RequestID,
		At:               time.Now().UTC(),
	}
	if err := h.saveAssociation(); err != nil {
		WriteError(w, http.StatusInternalServerError, "failed to save config")
		return
	}
	logger.Info("p2p association requested",
		"platform_user", body.PlatformUserID, "request_id", body.RequestID)
	writeJSON(w, http.StatusAccepted, st)
}

// handleConfirmP2PAssociation 设备主人确认当前申请。body.request_id 可选——
// 提供且与 pending 不一致时拒绝（防确认到被覆盖后的新申请）。
func (h *Handler) handleConfirmP2PAssociation(w http.ResponseWriter, r *http.Request) {
	if h.config == nil {
		WriteError(w, http.StatusInternalServerError, "config not available")
		return
	}
	var body struct {
		RequestID string `json:"request_id"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body) // body 可空；解码失败按未提供处理

	st := h.config.P2P.Association
	if st == nil || st.Pending == nil {
		WriteError(w, http.StatusConflict, "no pending association request")
		return
	}
	if body.RequestID != "" && body.RequestID != st.Pending.RequestID {
		WriteError(w, http.StatusConflict, "request_id mismatch")
		return
	}
	now := time.Now().UTC()
	entry := st.Pending
	entry.ConfirmedAt = &now
	st.Confirmed = entry
	st.Pending = nil
	if err := h.saveAssociation(); err != nil {
		WriteError(w, http.StatusInternalServerError, "failed to save config")
		return
	}
	logger.Info("p2p association confirmed",
		"platform_user", entry.PlatformUserID, "confirmed_at", now)
	writeJSON(w, http.StatusOK, st)
}

// handleCancelP2PAssociation 取消关联/拒绝申请：清空 confirmed 与 pending。
// （拒绝申请 = 在有 pending 无 confirmed 时调用同一端点；语义都是「清」。）
// 解绑联动（2026-10-04 实测补齐）：取消**已确认**的关联时停用 P2P
// （enabled=false + 热应用断链）——只清记录不断链的话，被解绑的 App
// 仍能 P2P 访问。凭据保留（App 不存平台密码、无法重新下发；重绑时
// App 经 PUT settings/p2p 带 enabled=true 重新拉起，零手工）。
func (h *Handler) handleCancelP2PAssociation(w http.ResponseWriter, _ *http.Request) {
	if h.config == nil {
		WriteError(w, http.StatusInternalServerError, "config not available")
		return
	}
	hadConfirmed := h.config.P2P.Association != nil && h.config.P2P.Association.Confirmed != nil
	h.config.P2P.Association = nil
	p2pDisabled := false
	if hadConfirmed {
		h.config.P2P.Enabled = false
		p2pDisabled = true
	}
	if err := h.saveAssociation(); err != nil {
		WriteError(w, http.StatusInternalServerError, "failed to save config")
		return
	}
	if p2pDisabled && h.p2pAgent != nil {
		h.p2pAgent.Apply(h.config.P2P)
	}
	if hadConfirmed {
		logger.Info("p2p association cancelled (p2p disabled, credentials kept)")
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":       "cleared",
		"p2p_disabled": p2pDisabled,
	})
}

// saveAssociation 落盘当前配置（yaml p2p.association 节）。纯状态变更，
// 不触碰链路运行时——无需热应用。
func (h *Handler) saveAssociation() error {
	if err := config.Save(h.configPath, h.config); err != nil {
		logger.Warn("failed to save config", "error", err)
		return err
	}
	return nil
}

// ── Phase 3：enrollment 绑定流（user-tenant-model §5） ──────────────────────
//
// App（持 NVR 管理凭据 = 绑定双要素之一，D13）经 LAN 下发平台签发的一次
// 性凭证；NVR 本地生成 Ed25519 身份密钥、注册公钥、绑定归属并经 re-enroll
// 挑战换得设备令牌。私钥永不出 NVR；重绑=全新身份。

// DeviceKeyPathFor 返回 p2p.device_key 的默认落点（配置文件同目录旁）。
func DeviceKeyPathFor(configPath string) string {
	return p2p.StatePathFor(configPath) + ".key"
}

// handleEnrollP2PAssociation App 下发 enrollment 凭证并完成设备绑定。
//
// POST /api/p2p/association/enroll {ticket_id, secret, server_url, product_id, display_name?}
func (h *Handler) handleEnrollP2PAssociation(w http.ResponseWriter, r *http.Request) {
	if h.config == nil {
		WriteError(w, http.StatusInternalServerError, "config not available")
		return
	}
	var body struct {
		TicketID    string `json:"ticket_id"`
		Secret      string `json:"secret"`
		ServerURL   string `json:"server_url"`   // 平台信令地址（wss://…/signal）
		ProductID   string `json:"product_id"`   // 票券所属实例域（App 从票券响应透传）
		DisplayName string `json:"display_name"` // 绑定方展示名（可选）
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	body.TicketID = strings.TrimSpace(body.TicketID)
	body.Secret = strings.TrimSpace(body.Secret)
	body.ServerURL = strings.TrimSpace(body.ServerURL)
	body.ProductID = strings.TrimSpace(body.ProductID)
	if body.TicketID == "" || body.Secret == "" || body.ServerURL == "" || body.ProductID == "" {
		WriteError(w, http.StatusBadRequest, "ticket_id, secret, server_url and product_id are required")
		return
	}

	keyPath := h.config.P2P.DeviceKeyPath
	if keyPath == "" {
		keyPath = DeviceKeyPathFor(h.configPath)
	}

	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	res, err := p2p.Enroll(ctx, p2p.EnrollOptions{
		BaseURL:  p2p.EnrollHTTPBase(body.ServerURL),
		TicketID: body.TicketID, Secret: body.Secret, ProductID: body.ProductID,
		KeyPath: keyPath,
	})
	if err != nil {
		switch {
		case errors.Is(err, p2p.ErrBadTicket):
			WriteError(w, http.StatusUnauthorized, "invalid or expired ticket")
		case errors.Is(err, p2p.ErrDeviceBound):
			WriteError(w, http.StatusConflict, "device already bound to another account; unbind first")
		case errors.Is(err, p2p.ErrDeviceRetired):
			WriteError(w, http.StatusConflict, "device identity retired; rebind required")
		default:
			logger.Warn("p2p enrollment failed", "error", err)
			WriteError(w, http.StatusBadGateway, "enrollment failed: "+err.Error())
		}
		return
	}

	// 先停旧链路（防旧 Manager 并发 Mint 覆写 token state），再落 token
	// state 与配置，最后以新身份热拉起。
	stopCfg := h.config.P2P
	stopCfg.Enabled = false
	if h.p2pAgent != nil {
		h.p2pAgent.Apply(stopCfg)
	}
	if err := os.WriteFile(p2p.StatePathFor(h.configPath),
		mustJSON(map[string]string{"refresh_token": res.Refresh}), 0o600); err != nil {
		logger.Warn("p2p enroll: persist token state", "error", err)
	}

	c := &h.config.P2P
	c.Enabled = true
	c.ServerURL = body.ServerURL
	c.ProductID = res.ProductID
	c.PeerID = res.DeviceID
	c.Username = res.Username
	c.Password = ""
	c.DeviceKeyPath = keyPath
	name := body.DisplayName
	if name == "" {
		name = res.AccountID
	}
	c.Association = &config.P2PAssociationState{Confirmed: &config.P2PAssociationEntry{
		PlatformUserID:   res.AccountID,
		PlatformUsername: res.AccountID,
		DisplayName:      name,
		RequestID:        res.DeviceID,
		At:               time.Now().UTC(),
	}}
	if err := h.config.ValidateP2P(); err != nil {
		WriteError(w, http.StatusInternalServerError, "post-enroll config invalid: "+err.Error())
		return
	}
	if err := h.saveAssociation(); err != nil {
		WriteError(w, http.StatusInternalServerError, "failed to save config")
		return
	}
	if h.p2pAgent != nil {
		h.p2pAgent.Apply(h.config.P2P)
	}
	logger.Info("p2p enrolled (device identity bound)",
		"device_id", res.DeviceID, "product_id", res.ProductID, "account", res.AccountID)

	resp := map[string]any{
		"enrolled":   true,
		"device_id":  res.DeviceID,
		"product_id": res.ProductID,
		"account_id": res.AccountID,
		"username":   res.Username,
	}
	if h.p2pAgent != nil {
		resp["status"] = h.p2pAgent.Status()
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleUnbindP2PAssociation 本地解绑：清设备身份与令牌链并停用 P2P。
// 平台侧解绑由 App 凭用户 token 调 /api/enrollment/unbind 完成（平台为
// 准）；本地清理不影响平台状态（§5：解绑不可逆，重绑=全新身份）。
//
// POST /api/p2p/association/unbind
func (h *Handler) handleUnbindP2PAssociation(w http.ResponseWriter, _ *http.Request) {
	if h.config == nil {
		WriteError(w, http.StatusInternalServerError, "config not available")
		return
	}
	wasDeviceIdentity := strings.HasPrefix(h.config.P2P.Username, "device:")
	h.config.P2P.Enabled = false
	if wasDeviceIdentity {
		h.config.P2P.Username, h.config.P2P.Password = "", ""
	}
	h.config.P2P.Association = nil
	_ = os.Remove(p2p.StatePathFor(h.configPath)) // refresh 链随之作废
	if err := h.saveAssociation(); err != nil {
		WriteError(w, http.StatusInternalServerError, "failed to save config")
		return
	}
	if h.p2pAgent != nil {
		h.p2pAgent.Apply(h.config.P2P)
	}
	logger.Info("p2p unbound locally", "device_identity", wasDeviceIdentity)
	writeJSON(w, http.StatusOK, map[string]any{"status": "unbound", "p2p_disabled": true})
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}
