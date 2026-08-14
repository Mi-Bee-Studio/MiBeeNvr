// Package vision 实现 NVR → MiBeeVision 的主动推送集成。
//
// NVR 在段刚落盘时,直接把视频文件字节流 POST 给 Vision(不是通知 file_path 让 Vision 下载)。
// 这样视频数据在 NVR 合并进程删除原文件之前就已经到了 Vision 手里——彻底消除 404。
//
// 推送条件:Vision 心跳健康(HealthTracker.IsHealthy)。
// 未启用时(enabled=false)退化为现有 SSE 模式(向后兼容)。
package vision

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/event"
)

// Coordinator 订阅 segment.completed 事件,在 Vision 健康时把视频文件推送给 Vision。
type Coordinator struct {
	health      *HealthTracker
	eventBus    *event.EventBus
	eventCh     chan event.Event
	cfg         func() config.VisionConfig
	storageRoot func() string // 返回 NVR 录像根目录,用于解析段的绝对路径
	client      *http.Client
	wg          sync.WaitGroup
	cancelFn    context.CancelFunc
	mu          sync.Mutex
}

// NewCoordinator 创建推送协调器。
func NewCoordinator(cfg func() config.VisionConfig, storageRoot func() string, eventBus *event.EventBus) *Coordinator {
	vcfg := cfg()
	return &Coordinator{
		health:      NewHealthTracker(vcfg.HeartbeatTimeoutSecs),
		eventBus:    eventBus,
		cfg:         cfg,
		storageRoot: storageRoot,
		client: &http.Client{
			Timeout: 120 * time.Second, // 大文件传输可能需要较长时间
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

// handleSegment 把段视频文件直接推送给 Vision。
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
	if seg.Format == "timelapse" {
		return
	}

	// 解析段文件的绝对路径。NVR 的 file_path 已经是绝对路径(/mnt/data/nvr/...)。
	absPath := seg.FilePath
	if !filepath.IsAbs(absPath) {
		absPath = filepath.Join(c.storageRoot(), absPath)
	}

	file, err := os.Open(absPath)
	if err != nil {
		slog.Warn("open segment file for push failed",
			"error", err,
			"path", absPath,
			"recording_id", seg.RecordingID)
		return
	}
	defer file.Close()

	// POST 视频字节流到 Vision。元数据通过 HTTP headers 传递。
	url := strings.TrimRight(vcfg.URL, "/") + "/vision/segment/upload"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, file)
	if err != nil {
		slog.Error("create upload request", "error", err)
		return
	}
	// 显式设置 Content-Length,避免 Go 使用 chunked encoding(Vision 的极简 HTTP 解析器不支持 chunked)。
	req.ContentLength = seg.FileSize
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("X-Recording-Id", seg.RecordingID)
	req.Header.Set("X-Camera-Id", seg.CameraID)
	req.Header.Set("X-Format", seg.Format)
	req.Header.Set("X-Encoding", seg.Encoding)
	req.Header.Set("X-Started-At", seg.StartedAt)
	req.Header.Set("X-Ended-At", seg.EndedAt)
	req.Header.Set("X-File-Size", strconv.FormatInt(seg.FileSize, 10))

	resp, err := c.client.Do(req)
	if err != nil {
		slog.Warn("push video to vision failed",
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
		slog.Info("pushed video to vision",
			"recording_id", seg.RecordingID,
			"camera_id", seg.CameraID,
			"size_mb", seg.FileSize/1024/1024)
	}
}
