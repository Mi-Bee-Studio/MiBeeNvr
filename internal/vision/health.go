package vision

import (
	"sync"
	"time"
)

// HeartbeatStatus 是 Vision 心跳报告中携带的状态信息。
type HeartbeatStatus struct {
	Status         string `json:"status"`          // "healthy"
	Device         string `json:"device"`          // "cuda" / "cpu"
	QueueDepth     int    `json:"queue_depth"`     // 待处理段数
	ProcessedCount int    `json:"processed_count"` // 累计已处理段数
	// SkipCameras 是消费者声明不处理的相机 ID(#515):编码不支持(如 MJPEG/JPEG)
	// 或处理容量取舍。NVR 对这些相机停推(含离线补偿),省下纯浪费的上传带宽。
	// 字段缺省/为空 = 不跳过任何相机(向后兼容旧版本消费者)。
	SkipCameras []string `json:"skip_cameras,omitempty"`
}

// HealthTracker 追踪 Vision 的心跳健康状态。
//
// NVR 的推送协调器在推送段之前检查 IsHealthy()——只在 Vision 健康时推送,
// 避免向不可用的 Vision 发请求。Vision 恢复后,心跳恢复,推送自动续上。
type HealthTracker struct {
	mu         sync.RWMutex
	lastSeen   time.Time
	status     HeartbeatStatus
	timeout    time.Duration
	onRecovery func()
}

// NewHealthTracker 创建健康追踪器。timeoutSecs ≤ 0 时默认 60 秒。
func NewHealthTracker(timeoutSecs int) *HealthTracker {
	if timeoutSecs <= 0 {
		timeoutSecs = 60
	}
	return &HealthTracker{
		timeout: time.Duration(timeoutSecs) * time.Second,
	}
}

// SetOnRecovery registers a callback fired (on its own goroutine) when a
// heartbeat arrives after an unhealthy gap — the hook that triggers offline
// compensation in the push coordinator (#329). Optional; nil disables.
func (h *HealthTracker) SetOnRecovery(f func()) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.onRecovery = f
}

// RecordHeartbeat 记录一次 Vision 心跳。
func (h *HealthTracker) RecordHeartbeat(status HeartbeatStatus) {
	h.mu.Lock()
	wasHealthy := !h.lastSeen.IsZero() && time.Since(h.lastSeen) < h.timeout &&
		h.status.Status != "degraded"
	h.lastSeen = time.Now()
	h.status = status
	cb := h.onRecovery
	h.mu.Unlock()
	if !wasHealthy && cb != nil {
		// Fire off the request goroutine — the heartbeat handler must not
		// block on compensation work.
		go cb()
	}
}

// IsHealthy 返回 Vision 是否健康(最近 timeout 内收到过非 degraded 心跳)。
//
// "degraded" = 消费者活着但 worker 卡死(2026-09 Jetson 事故:worker 卡死数日
// 心跳照常,NVR 持续推送喂泄漏)。视为不健康停推;恢复(心跳转回 healthy)走
// 现有 onRecovery 离线补偿路径补齐停推窗口。
func (h *HealthTracker) IsHealthy() bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.lastSeen.IsZero() {
		return false
	}
	if h.status.Status == "degraded" {
		return false
	}
	return time.Since(h.lastSeen) < h.timeout
}

// SkipCamera 报告相机是否在消费者最近一次心跳声明的跳单里(#515)。
// 以最后一次心跳为准:字段缺省(旧消费者)或清空即恢复全推。
func (h *HealthTracker) SkipCamera(cameraID string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, c := range h.status.SkipCameras {
		if c == cameraID {
			return true
		}
	}
	return false
}

// Snapshot 返回当前健康状态、最后心跳时间和状态详情。
func (h *HealthTracker) Snapshot() (healthy bool, lastSeen time.Time, status HeartbeatStatus) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	healthy = !h.lastSeen.IsZero() && time.Since(h.lastSeen) < h.timeout &&
		h.status.Status != "degraded"
	return healthy, h.lastSeen, h.status
}
