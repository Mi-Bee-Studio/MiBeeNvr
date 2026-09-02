package vision

import (
	"sync"
	"time"
)

// maxHistorySamples caps the in-memory heartbeat history ring: 24h at the
// 30s heartbeat interval. Lightweight by design — no DB persistence; history
// resets on NVR restart.
const maxHistorySamples = 2880

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
	// Drops 是心跳 v2(#671)携带的批量丢弃报告(压缩范围)。nil = 本次心跳
	// 没有丢弃要回报(旧版本消费者永远为 nil)。
	Drops *VisionDrops `json:"drops,omitempty"`
	// Metrics 是心跳 v2(#671)携带的运行指标(队列/worker/吞吐/资源)。
	Metrics *VisionMetrics `json:"metrics,omitempty"`
}

// VisionDropRange 是一个压缩后的丢弃范围:同一相机同一原因、时间上相邻的
// 连续丢弃合并为一条(消费者侧负责压缩),count 是该范围内的丢弃段数。
// ids 为有限条精确录像 ID(消费者上限截断);只带范围不带 ids 时 NVR 用
// 相机 + 时间窗批量标记。
type VisionDropRange struct {
	CameraID string   `json:"camera_id"`
	Reason   string   `json:"reason"` // queue_full / ttl_expired / ...
	Count    int      `json:"count"`
	From     string   `json:"from"` // RFC3339
	To       string   `json:"to"`   // RFC3339
	IDs      []string `json:"ids,omitempty"`
}

// VisionDrops 是一次心跳携带的完整丢弃报告。seq 单调递增,NVR 在响应里
// 回 ack_drops=seq,消费者据此清空已确认的暂存。
type VisionDrops struct {
	Seq    int64             `json:"seq"`
	Ranges []VisionDropRange `json:"ranges"`
}

// VisionMetrics 是消费者上报的运行指标快照(#671)。全部字段可选——
// 旧版本消费者的心跳没有这个块。
type VisionMetrics struct {
	QueueCapacity     int     `json:"queue_capacity"`
	DecodeWorkers     int     `json:"decode_workers"`
	WorkersBusy       int     `json:"workers_busy"`
	ReceivedTotal     int64   `json:"received_total"`
	DroppedTotal      int64   `json:"dropped_total"`
	DroppedQueueFull  int64   `json:"dropped_queue_full"`
	DroppedTTL        int64   `json:"dropped_ttl"`
	EventsEmitted     int64   `json:"events_emitted"`
	SegMsP50          int64   `json:"seg_ms_p50"`
	SegMsP90          int64   `json:"seg_ms_p90"`
	DecodedQueueDepth int     `json:"decoded_queue_depth"`
	MemAvailableMB    int64   `json:"mem_available_mb"`
	Load1             float64 `json:"load1"`
}

// VisionSample 是历史环里的一次心跳采样(供仪表盘趋势图)。
type VisionSample struct {
	TS             time.Time `json:"ts"`
	QueueDepth     int       `json:"queue_depth"`
	ProcessedCount int64     `json:"processed_count"`
	DroppedTotal   int64     `json:"dropped_total"`
	DecodeWorkers  int       `json:"decode_workers"`
	WorkersBusy    int       `json:"workers_busy"`
	EventsEmitted  int64     `json:"events_emitted"`
}

// HealthTracker 追踪 Vision 的心跳健康状态。
//
// NVR 的推送协调器在推送段之前检查 IsHealthy()——只在 Vision 健康时推送,
// 避免向不可用的 Vision 发请求。Vision 恢复后,心跳恢复,推送自动续上。
type HealthTracker struct {
	mu          sync.RWMutex
	lastSeen    time.Time
	status      HeartbeatStatus
	timeout     time.Duration
	onRecovery  func()
	samples     []VisionSample // 心跳历史环(≤maxHistorySamples)
	markedDrops int64          // 累计按丢弃报告标记为 skipped 的录像数
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
	now := time.Now()
	if !h.lastSeen.IsZero() && now.Before(h.lastSeen) {
		now = h.lastSeen // 保证历史环时间戳单调不减
	}
	h.lastSeen = now
	h.status = status

	sample := VisionSample{
		TS:             now,
		QueueDepth:     status.QueueDepth,
		ProcessedCount: int64(status.ProcessedCount),
	}
	if status.Metrics != nil {
		sample.DroppedTotal = status.Metrics.DroppedTotal
		sample.DecodeWorkers = status.Metrics.DecodeWorkers
		sample.WorkersBusy = status.Metrics.WorkersBusy
		sample.EventsEmitted = status.Metrics.EventsEmitted
	}
	if len(h.samples) >= maxHistorySamples {
		h.samples = h.samples[1:]
	}
	h.samples = append(h.samples, sample)
	cb := h.onRecovery
	h.mu.Unlock()
	if !wasHealthy && cb != nil {
		// Fire off the request goroutine — the heartbeat handler must not
		// block on compensation work.
		go cb()
	}
}

// MetricsHistory 返回 since 之后(含)的心跳采样,按到达顺序。
func (h *HealthTracker) MetricsHistory(since time.Time) []VisionSample {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make([]VisionSample, 0, len(h.samples))
	for _, s := range h.samples {
		if !s.TS.Before(since) {
			out = append(out, s)
		}
	}
	return out
}

// NoteMarkedDrops 累加按丢弃报告标记的录像数(心跳处理路径调用)。
func (h *HealthTracker) NoteMarkedDrops(n int64) {
	if n <= 0 {
		return
	}
	h.mu.Lock()
	h.markedDrops += n
	h.mu.Unlock()
}

// MarkedDropTotal 返回累计标记数。
func (h *HealthTracker) MarkedDropTotal() int64 {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.markedDrops
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
