package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/middleware"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/vision"
	"github.com/go-chi/chi/v5"
)

// registerVisionPublicRoutes 注册无需认证的 Vision 端点(与 SSE 一样在 public 组)。
// 心跳是 Vision 的"我在"信号,不应要求 BasicAuth(Vision 只有 API Key)。
// 若请求恰好携带了 API Key(心跳 v2 客户端都会带),则用于多实例归因。
func (h *Handler) registerVisionPublicRoutes(r chi.Router) {
	r.Post("/api/vision/heartbeat", h.handleVisionHeartbeat)
}

// registerVisionRoutes 注册需要认证的 Vision 端点(在 protected 组,Web UI 用)。
func (h *Handler) registerVisionRoutes(r chi.Router) {
	r.Get("/api/vision/status", h.handleVisionStatus)
	r.Get("/api/vision/metrics", h.handleVisionMetrics)
}

// handleVisionHeartbeat 接收 MiBeeVision 实例的心跳报告。
// Vision 每 30 秒 POST 一次,NVR 据此判断实例是否健康。
// 只有健康的实例才会收到段推送。
//
// 多实例归因:请求携带的 Bearer API Key 名匹配 vision.instances[].api_key_name
// 时心跳记到该实例;匿名/未匹配落到 default 实例(兼容旧版单实例部署)。
//
// 心跳 v2 (#671) 增加两个可选块(旧版本消费者不带,完全向后兼容):
//   - drops:批量丢弃报告(压缩范围)。收到即把受影响录像标记
//     ai_status='skipped',并在响应回 ack_drops=seq 让消费者清空暂存。
//   - metrics:运行指标快照,进内存历史环供仪表盘查询。
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
		// SkipCameras(#515): 消费者声明不处理的相机,NVR 停推(含离线补偿)。
		// 缺省/为空 = 全推(向后兼容)。
		SkipCameras []string `json:"skip_cameras"`
		// 心跳 v2 (#671) 可选块。
		Drops   *vision.VisionDrops   `json:"drops"`
		Metrics *vision.VisionMetrics `json:"metrics"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	keyName := ""
	if middleware.IsAPIKeyAuthenticated(r.Context()) {
		keyName = middleware.APIKeyNameFromContext(r.Context())
	}
	insName := h.visionCoordinator.RecordHeartbeat(keyName, vision.HeartbeatStatus{
		Status:         body.Status,
		Device:         body.Device,
		QueueDepth:     body.QueueDepth,
		ProcessedCount: body.ProcessedCount,
		SkipCameras:    body.SkipCameras,
		Drops:          body.Drops,
		Metrics:        body.Metrics,
	})

	resp := map[string]interface{}{
		"ok":           true,
		"push_enabled": false,
	}
	if tracker, _ := h.visionCoordinator.InstanceByName(insName); tracker != nil {
		resp["push_enabled"] = tracker.IsHealthy()
	}
	if body.Drops != nil {
		if h.db != nil {
			if marked := vision.ApplyDrops(r.Context(), h.db, body.Drops); marked > 0 {
				if tracker, _ := h.visionCoordinator.InstanceByName(insName); tracker != nil {
					tracker.NoteMarkedDrops(marked)
				}
			}
		}
		// ack = "已收到并消费这份报告"。即使个别范围数据坏掉被跳过,
		// 也回 ack——不让消费者对一份注定无法生效的报告无限重试。
		resp["ack_drops"] = body.Drops.Seq
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleVisionStatus 返回 Vision 集成的当前状态(供 NVR Web UI 展示)。
// 顶层字段保持单实例时代的形状(= default 实例),多实例部署附加
// instances[] 数组逐实例展开。
func (h *Handler) handleVisionStatus(w http.ResponseWriter, r *http.Request) {
	if h.visionCoordinator == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"enabled": false,
		})
		return
	}

	healthy, lastSeen, status := h.visionCoordinator.Health().Snapshot()
	resp := map[string]interface{}{
		"enabled":      true,
		"healthy":      healthy,
		"device":       status.Device,
		"queue_depth":  status.QueueDepth,
		"processed":    status.ProcessedCount,
		"skip_cameras": status.SkipCameras, // #515: 消费者声明的跳单(空=nil=全推)
		"instances":    h.visionCoordinator.InstancesStatus(),
	}
	if status.Metrics != nil {
		resp["metrics"] = status.Metrics
	}
	if n := h.visionCoordinator.Health().MarkedDropTotal(); n > 0 {
		resp["drops_marked_total"] = n
	}
	// Omit the zero time — no heartbeat has ever been received. Rendering it
	// would surface "0001-01-01" in the UI (#328).
	if !lastSeen.IsZero() {
		resp["last_seen"] = lastSeen
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleVisionMetrics 返回心跳历史采样环(内存,约 24h @ 30s),供仪表盘
// 趋势图。?hours=1..168(默认 24);?instance=NAME 指定实例(默认 default)。
func (h *Handler) handleVisionMetrics(w http.ResponseWriter, r *http.Request) {
	if h.visionCoordinator == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"enabled":      false,
			"points":       []vision.VisionSample{},
			"marked_total": 0,
		})
		return
	}

	hours := 24
	if v := r.URL.Query().Get("hours"); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil {
			hours = parsed
		}
	}
	if hours < 1 {
		hours = 1
	}
	if hours > 168 {
		hours = 168
	}
	tracker, name := h.visionCoordinator.InstanceByName(r.URL.Query().Get("instance"))
	if tracker == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"enabled":      true,
			"instance":     "",
			"points":       []vision.VisionSample{},
			"marked_total": 0,
		})
		return
	}
	since := time.Now().Add(-time.Duration(hours) * time.Hour)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"enabled":      true,
		"instance":     name,
		"points":       tracker.MetricsHistory(since),
		"marked_total": tracker.MarkedDropTotal(),
	})
}
