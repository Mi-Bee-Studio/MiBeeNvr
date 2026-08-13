// Package vision 实现 NVR → MiBeeVision 的主动推送集成。
//
// 核心思路:NVR 不再被动等 Vision 通过 SSE 拉取 + HTTP 下载(78% 因段文件已被
// 合并/删除而 404),而是在段刚落盘、合并进程还没删它时,主动推给 Vision。
//
// 推送条件:Vision 心跳健康(HealthTracker.IsHealthy)。
// 如果 Vision 不可用,推送暂停;Vision 恢复后心跳恢复,推送自动续上。
// 未启用时(enabled=false)或从未收到心跳时,退化为现有 SSE 模式(向后兼容)。
package vision

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/event"
)

// segmentNotifyPayload 是推送给 Vision 的段通知(Sidecar 模式)。
// Vision 收到后根据 file_path 直读共享文件系统上的文件(零拷贝)。
type segmentNotifyPayload struct {
	RecordingID string `json:"recording_id"`
	CameraID    string `json:"camera_id"`
	FilePath    string `json:"file_path"` // 相对 storage root
	Format      string `json:"format"`
	Encoding    string `json:"encoding"`
	StartedAt   string `json:"started_at"`
	EndedAt     string `json:"ended_at"`
	FileSize    int64  `json:"file_size"`
}

// Coordinator 订阅 segment.completed 事件,在 Vision 健康时推送段通知。
//
// 生命周期:Start(ctx) 启动事件循环,Stop() 取消订阅并等待 goroutine 退出。
// 线程安全:HealthTracker 读写锁保护;eventBus 的 Subscribe/Unsubscribe 线程安全。
type Coordinator struct {
	health   *HealthTracker
	eventBus *event.EventBus
	eventCh  chan event.Event
	cfg      func() config.VisionConfig
	client   *http.Client
	wg       sync.WaitGroup
	cancelFn context.CancelFunc
	mu       sync.Mutex // 保护 cancelFn
}

// NewCoordinator 创建推送协调器。
// cfg 是闭包形式的配置访问器,确保 NVR 热加载配置时能读到最新值。
func NewCoordinator(cfg func() config.VisionConfig, eventBus *event.EventBus) *Coordinator {
	vcfg := cfg()
	return &Coordinator{
		health:   NewHealthTracker(vcfg.HeartbeatTimeoutSecs),
		eventBus: eventBus,
		cfg:      cfg,
		client: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// Health 返回 HealthTracker,供 API handler 记录心跳。
func (c *Coordinator) Health() *HealthTracker {
	return c.health
}

// Start 订阅 segment.completed 并启动事件处理循环。
func (c *Coordinator) Start(ctx context.Context) error {
	if c == nil {
		return nil
	}
	c.eventCh = make(chan event.Event, 64)
	if err := c.eventBus.Subscribe(event.TopicSegmentCompleted, c.eventCh, 0); err != nil {
		return fmt.Errorf("vision coordinator subscribe segment.completed: %w", err)
	}

	c.mu.Lock()
	ctx, c.cancelFn = context.WithCancel(ctx)
	c.mu.Unlock()

	c.wg.Add(1)
	go c.eventLoop(ctx)
	slog.Info("vision coordinator started",
		"push_mode", c.cfg().PushMode,
		"vision_url", c.cfg().URL)
	return nil
}

// Stop 取消订阅并等待事件循环退出。
func (c *Coordinator) Stop() {
	if c == nil {
		return
	}
	c.mu.Lock()
	if c.cancelFn != nil {
		c.cancelFn()
	}
	c.mu.Unlock()
	if c.eventBus != nil && c.eventCh != nil {
		c.eventBus.Unsubscribe(event.TopicSegmentCompleted, c.eventCh)
	}
	c.wg.Wait()
	slog.Info("vision coordinator stopped")
}

func (c *Coordinator) eventLoop(ctx context.Context) {
	defer c.wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case evt := <-c.eventCh:
			seg, ok := evt.Data.(event.SegmentCompleted)
			if !ok {
				continue
			}
			c.handleSegment(ctx, seg)
		}
	}
}

// handleSegment 处理一个段完成事件:检查配置 + 健康状态,然后推送通知。
func (c *Coordinator) handleSegment(ctx context.Context, seg event.SegmentCompleted) {
	vcfg := c.cfg()
	if !vcfg.Enabled || vcfg.URL == "" {
		return
	}
	if !c.health.IsHealthy() {
		slog.Debug("vision not healthy, skip push",
			"recording_id", seg.RecordingID)
		return
	}

	// 跳过 timelapse 格式(Vision 不处理)。
	if seg.Format == "timelapse" {
		return
	}

	payload := segmentNotifyPayload{
		RecordingID: seg.RecordingID,
		CameraID:    seg.CameraID,
		FilePath:    seg.FilePath,
		Format:      seg.Format,
		Encoding:    seg.Encoding,
		StartedAt:   seg.StartedAt,
		EndedAt:     seg.EndedAt,
		FileSize:    seg.FileSize,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		slog.Error("marshal segment notify", "error", err)
		return
	}

	url := strings.TrimRight(vcfg.URL, "/") + "/vision/segment/notify"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		slog.Error("create push request", "error", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		slog.Warn("push segment to vision failed",
			"error", err,
			"recording_id", seg.RecordingID)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		slog.Warn("vision returned non-success status",
			"status", resp.StatusCode,
			"recording_id", seg.RecordingID)
	} else {
		slog.Debug("pushed segment to vision",
			"recording_id", seg.RecordingID,
			"camera_id", seg.CameraID)
	}
}
