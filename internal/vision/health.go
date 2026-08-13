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
}

// HealthTracker 追踪 Vision 的心跳健康状态。
//
// NVR 的推送协调器在推送段之前检查 IsHealthy()——只在 Vision 健康时推送,
// 避免向不可用的 Vision 发请求。Vision 恢复后,心跳恢复,推送自动续上。
type HealthTracker struct {
	mu       sync.RWMutex
	lastSeen time.Time
	status   HeartbeatStatus
	timeout  time.Duration
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

// RecordHeartbeat 记录一次 Vision 心跳。
func (h *HealthTracker) RecordHeartbeat(status HeartbeatStatus) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.lastSeen = time.Now()
	h.status = status
}

// IsHealthy 返回 Vision 是否健康(最近 timeout 内收到过心跳)。
func (h *HealthTracker) IsHealthy() bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.lastSeen.IsZero() {
		return false
	}
	return time.Since(h.lastSeen) < h.timeout
}

// Snapshot 返回当前健康状态、最后心跳时间和状态详情。
func (h *HealthTracker) Snapshot() (healthy bool, lastSeen time.Time, status HeartbeatStatus) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	healthy = !h.lastSeen.IsZero() && time.Since(h.lastSeen) < h.timeout
	return healthy, h.lastSeen, h.status
}
