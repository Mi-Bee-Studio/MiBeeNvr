package api

import (
	"encoding/json"
	"net/http"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/vision"
	"github.com/go-chi/chi/v5"
)

// registerVisionPublicRoutes 注册无需认证的 Vision 端点(与 SSE 一样在 public 组)。
// 心跳是 Vision 的"我在"信号,不应要求 BasicAuth(Vision 只有 API Key)。
func (h *Handler) registerVisionPublicRoutes(r chi.Router) {
	r.Post("/api/vision/heartbeat", h.handleVisionHeartbeat)
}

// registerVisionRoutes 注册需要认证的 Vision 端点(在 protected 组,Web UI 用)。
func (h *Handler) registerVisionRoutes(r chi.Router) {
	r.Get("/api/vision/status", h.handleVisionStatus)
}

// handleVisionHeartbeat 接收 MiBeeVision 的心跳报告。
// Vision 每 30 秒 POST 一次,NVR 据此判断 Vision 是否健康。
// 只有健康的 Vision 才会收到段推送。
func (h *Handler) handleVisionHeartbeat(w http.ResponseWriter, r *http.Request) {
	if h.visionCoordinator == nil {
		WriteError(w, http.StatusServiceUnavailable, "vision integration not enabled")
		return
	}

	// 心跳端点接受 API Key 认证或 BasicAuth(方便不同部署模式)。
	// 不强制 API Key,因为心跳是"我在"信号而非数据写入。
	var body struct {
		Status         string `json:"status"`
		Device         string `json:"device"`
		QueueDepth     int    `json:"queue_depth"`
		ProcessedCount int    `json:"processed_count"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	h.visionCoordinator.Health().RecordHeartbeat(vision.HeartbeatStatus{
		Status:         body.Status,
		Device:         body.Device,
		QueueDepth:     body.QueueDepth,
		ProcessedCount: body.ProcessedCount,
	})

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"ok":           true,
		"push_enabled": h.visionCoordinator.Health().IsHealthy(),
	})
}

// handleVisionStatus 返回 Vision 集成的当前状态(供 NVR Web UI 展示)。
// 这个端点在 authMW 保护组内,只有登录用户可访问。
func (h *Handler) handleVisionStatus(w http.ResponseWriter, r *http.Request) {
	if h.visionCoordinator == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"enabled": false,
		})
		return
	}

	healthy, lastSeen, status := h.visionCoordinator.Health().Snapshot()
	resp := map[string]interface{}{
		"enabled":     true,
		"healthy":     healthy,
		"device":      status.Device,
		"queue_depth": status.QueueDepth,
		"processed":   status.ProcessedCount,
	}
	// Omit the zero time — no heartbeat has ever been received. Rendering it
	// would surface "0001-01-01" in the UI (#328).
	if !lastSeen.IsZero() {
		resp["last_seen"] = lastSeen
	}
	writeJSON(w, http.StatusOK, resp)
}
