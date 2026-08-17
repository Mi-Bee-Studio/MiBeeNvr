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
	"sync/atomic"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/event"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
)

// Offline-compensation bounds (#329). A long offline gap is capped so the
// recovery burst can't storm Vision or the disk; segments beyond the window
// are skipped (they would mostly be merged away anyway).
const (
	repushWindow = 2 * time.Hour
	repushMax    = 500
	repushPacing = 200 * time.Millisecond
)

// Repusher abstracts the recordings lookup needed for offline compensation.
// Production wiring passes *storage.DB; tests use fakes. Kept as an interface
// so the coordinator stays testable without SQLite.
type Repusher interface {
	ListRecordingsForVisionRepush(ctx context.Context, since, until time.Time, limit int) ([]model.Recording, error)
}

// Coordinator 订阅 segment.completed 事件,在 Vision 健康时把视频文件推送给 Vision。
type Coordinator struct {
	health       *HealthTracker
	eventBus     *event.EventBus
	eventCh      chan event.Event
	cfg          func() config.VisionConfig
	storageRoot  func() string // 返回 NVR 录像根目录,用于解析段的绝对路径
	db           Repusher      // nil → 补偿重推禁用(测试/降级部署)
	client       *http.Client
	wg           sync.WaitGroup
	cancelFn     context.CancelFunc
	mu           sync.Mutex
	runCtx       context.Context // 供恢复回调 goroutine 使用(Start 时设置)
	pausedSince  time.Time       // 推送暂停窗口起点(mu 保护;零值=未暂停)
	compensating atomic.Bool     // 补偿重推 single-flight
}

// NewCoordinator 创建推送协调器。db 可为 nil(禁用离线补偿)。
func NewCoordinator(cfg func() config.VisionConfig, storageRoot func() string, eventBus *event.EventBus, db Repusher) *Coordinator {
	vcfg := cfg()
	c := &Coordinator{
		health:      NewHealthTracker(vcfg.HeartbeatTimeoutSecs),
		eventBus:    eventBus,
		cfg:         cfg,
		storageRoot: storageRoot,
		db:          db,
		client: &http.Client{
			Timeout: 120 * time.Second, // 大文件传输可能需要较长时间
		},
	}
	// 心跳恢复(不健康→健康)触发离线补偿重推 (#329)。
	c.health.SetOnRecovery(c.compensateOffline)
	return c
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
	c.runCtx = ctx
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
		c.markPaused()
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

// markPaused records the start of a push-pause window (first segment skipped
// while Vision is unhealthy). The timestamp bounds the offline-compensation
// query when Vision recovers (#329).
func (c *Coordinator) markPaused() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.pausedSince.IsZero() {
		c.pausedSince = time.Now()
		slog.Info("vision push paused — offline window starts (segments will be compensated on recovery)",
			"since", c.pausedSince)
	}
}

// rearmPaused restores the pause window with the given start (used when
// Vision drops again mid-compensation, so the next recovery retries the
// remainder instead of only the new gap).
func (c *Coordinator) rearmPaused(since time.Time) {
	c.mu.Lock()
	c.pausedSince = since
	c.mu.Unlock()
}

// takePausedSince returns and clears the pause-window start.
func (c *Coordinator) takePausedSince() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	since := c.pausedSince
	c.pausedSince = time.Time{}
	return since
}

// compensateOffline re-pushes segments that completed while Vision was
// offline. Triggered by the health tracker's recovery callback — a heartbeat
// after an unhealthy gap. New segments keep flowing through the event loop
// concurrently; Vision dedups by X-Recording-Id, so overlap is harmless.
func (c *Coordinator) compensateOffline() {
	c.mu.Lock()
	ctx := c.runCtx
	c.mu.Unlock()
	if ctx == nil || ctx.Err() != nil {
		return
	}
	since := c.takePausedSince()
	if since.IsZero() || c.db == nil {
		return
	}
	// Only one compensation run at a time (heartbeats can flap).
	if !c.compensating.CompareAndSwap(false, true) {
		c.rearmPaused(since)
		return
	}
	defer c.compensating.Store(false)

	if time.Since(since) > repushWindow {
		since = time.Now().Add(-repushWindow)
	}

	recs, err := c.db.ListRecordingsForVisionRepush(ctx, since, time.Now(), repushMax)
	if err != nil {
		c.rearmPaused(since)
		slog.Warn("vision compensation lookup failed", "error", err)
		return
	}
	if len(recs) == 0 {
		slog.Debug("vision recovered — nothing to compensate")
		return
	}
	slog.Info("vision recovered — re-pushing segments missed while offline",
		"count", len(recs), "since", since)

	pushed := 0
	for _, rec := range recs {
		if ctx.Err() != nil {
			return
		}
		if !c.health.IsHealthy() {
			// Dropped again mid-compensation: restore the window so the next
			// recovery retries the remainder.
			c.rearmPaused(since)
			slog.Warn("vision unhealthy during compensation — stopping early",
				"pushed", pushed, "total", len(recs))
			return
		}
		c.handleSegment(ctx, recordingToSegment(rec))
		pushed++
		// Pace the burst so a long offline gap doesn't storm Vision.
		time.Sleep(repushPacing)
	}
	slog.Info("vision offline compensation finished", "pushed", pushed, "window", time.Since(since).Round(time.Second))
}

// recordingToSegment synthesizes the push payload from a DB recording row.
// encoding is not persisted on the recordings table — Vision sniffs the codec
// from the MP4 payload when the header is empty.
func recordingToSegment(r model.Recording) event.SegmentCompleted {
	return event.SegmentCompleted{
		CameraID:    r.CameraID,
		FilePath:    r.FilePath,
		Format:      string(r.Format),
		StartedAt:   r.StartedAt.UTC().Format(time.RFC3339Nano),
		EndedAt:     r.EndedAt.UTC().Format(time.RFC3339Nano),
		FileSize:    r.FileSize,
		RecordingID: r.ID,
	}
}
