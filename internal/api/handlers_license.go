package api

// handlers_license.go — 商业化收费授权（internal/license）：状态查询与
// 授权文档安装。安装热生效：共享管理器（P2P 门控同源）立即换新配置，
// P2P 侧在下一个门控检查周期感知（未授权停拨 → 授权后自动重拨）。

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/license"
)

// SetLicenseManager wires the shared license manager (built once in pkg/app;
// the P2P device agent gates on the same instance). nil = 授权能力缺席，
// 端点只报 none。Setter 形态与 vodMgr 等自包含组件一致，避免再撑
// NewHandler 的位置参数。
func (h *Handler) SetLicenseManager(m *license.Manager) { h.licenseMgr = m }

// handleGetLicense 报告授权评估快照（状态/文档要素/剩余时长/放行与否）。
func (h *Handler) handleGetLicense(w http.ResponseWriter, r *http.Request) {
	if h.licenseMgr == nil {
		writeJSON(w, http.StatusOK, license.Status{State: license.StateNone, Allowed: true})
		return
	}
	writeJSON(w, http.StatusOK, h.licenseMgr.Status())
}

// handleInstallLicense 安装/更新/清除授权文档。body:
//
//	{"document": "payload.sig", "public_key": "b64...", "required": true}
//
// document 空串 = 清除文档；public_key 缺省沿用已配置公钥（换钥即轮换）；
// required 缺省沿用现值。只做可解码性校验（签名/过期状态如实经 GET
// 呈现——安装成功 ≠ 授权有效，过期文档也允许装上等待生效窗口）。
func (h *Handler) handleInstallLicense(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Document  string `json:"document"`
		PublicKey string `json:"public_key,omitempty"`
		Required  *bool  `json:"required,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	newDoc := strings.TrimSpace(body.Document)
	newPub := strings.TrimSpace(body.PublicKey)
	if newPub == "" {
		newPub = strings.TrimSpace(h.config.License.PublicKey)
	}
	newRequired := h.config.License.Required
	if body.Required != nil {
		newRequired = *body.Required
	}
	if newDoc != "" && newPub == "" {
		WriteError(w, http.StatusBadRequest, "public_key required when installing a document")
		return
	}

	// 草稿管理器预检可解码性（不落任何状态）。
	draft := license.NewManager(h.config.Server.DeviceID)
	if err := draft.Configure(newDoc, newPub, newRequired); err != nil {
		WriteError(w, http.StatusBadRequest, err.Error())
		return
	}

	h.config.License.Document = newDoc
	h.config.License.PublicKey = newPub
	h.config.License.Required = newRequired
	if err := config.Save(h.configPath, h.config); err != nil {
		WriteError(w, http.StatusInternalServerError, "failed to save config: "+err.Error())
		return
	}
	if h.licenseMgr != nil {
		if err := h.licenseMgr.Configure(newDoc, newPub, newRequired); err != nil {
			// 草稿已过、落盘已过，共享实例更新失败只降级日志（下次
			// 重启从 yaml 恢复同状态）。
			logger.Warn("license hot-apply to live manager failed", "error", err)
		}
	}
	logger.Info("license installed", "required", newRequired, "has_document", newDoc != "")

	if h.licenseMgr != nil {
		writeJSON(w, http.StatusOK, h.licenseMgr.Status())
		return
	}
	writeJSON(w, http.StatusOK, draft.Status())
}
