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
	// subLayer 是子码流分析层 (#514)。nil(未接 provider)时整个层停用。
	subLayer *SubLayerManager
}

// NewCoordinator 创建推送协调器。db 可为 nil(禁用离线补偿);provider 可为
// nil(禁用子码流分析层)。
func NewCoordinator(cfg func() config.VisionConfig, storageRoot func() string, eventBus *event.EventBus, db Repusher, provider SubLayerProvider) *Coordinator {
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
	if provider != nil {
		c.subLayer = NewSubLayerManager(provider, cfg, storageRoot, SubLayerDeps{
			Push:    c.pushSubSegment,
			Healthy: c.health.IsHealthy,
		})
	}
	// 心跳恢复(不健康→健康)触发离线补偿重推 (#329)。
	c.health.SetOnRecovery(c.compensateOffline)
	return c
}

// SubLayer exposes the sub-layer manager (observability/tests). May be nil.
func (c *Coordinator) SubLayer() *SubLayerManager {
	return c.subLayer
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
	c.subLayer.Start(ctx)
	slog.Info("vision coordinator started",
		"push_mode", c.cfg().PushMode,
		"vision_url", c.cfg().URL,
		"sub_layer_cameras", len(c.cfg().SubLayerCameras))
	return nil
}

// Stop 取消订阅并等待事件循环退出。
func (c *Coordinator) Stop() {
	if c == nil {
		return
	}
	c.subLayer.Stop()
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
	// Feed the sub-layer's recording-id join regardless of push outcome — a
	// later sub segment must still map onto the covering main recording.
	// (Only main-layer segments define the join anchor; tierrec layer=1
	// segments reference their own rows.)
	if c.subLayer != nil && seg.Layer == model.LayerMain {
		c.subLayer.NoteMainSegment(seg.CameraID, seg.RecordingID)
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
	// 配置显式排除的相机(MJPEG/JPEG 等外部消费者无法解码的编码):推送纯浪费
	// 带宽与 CPU,直接跳过(离线补偿重推走同一路径,一并跳过)。
	if vcfg.ShouldSkipCamera(seg.CameraID) {
		slog.Debug("vision push skipped by skip_cameras config",
			"camera_id", seg.CameraID,
			"recording_id", seg.RecordingID)
		return
	}
	// 消费者心跳声明的跳单(#515):与静态配置取并集生效。
	if c.health.SkipCamera(seg.CameraID) {
		slog.Debug("vision push skipped by consumer-reported skip list",
			"camera_id", seg.CameraID,
			"recording_id", seg.RecordingID)
		return
	}
	// 分层录制相机 (#637): 分析输入改由 tierrec 子层段(layer=1,60s 低清连续)
	// 提供,主流段(稀疏 TL/全速)不再推送——让位语义与 #514 子流分析层相同。
	tiered := vcfg.TieredCameraSet()[seg.CameraID]
	if seg.Layer == model.LayerSub {
		if !tiered {
			slog.Debug("vision push skipped — sub-layer segment of a non-tiered camera",
				"camera_id", seg.CameraID,
				"recording_id", seg.RecordingID)
			return
		}
		// tierrec 子层段直接推送(它是正式录像行,路径由 storageRoot 解析)。
		absPath := seg.FilePath
		if !filepath.IsAbs(absPath) {
			absPath = filepath.Join(c.storageRoot(), absPath)
		}
		c.uploadSegment(ctx, absPath, seg.FileSize, map[string]string{
			"X-Recording-Id": seg.RecordingID,
			"X-Camera-Id":    seg.CameraID,
			"X-Format":       seg.Format,
			"X-Encoding":     seg.Encoding,
			"X-Started-At":   seg.StartedAt,
			"X-Ended-At":     seg.EndedAt,
			"X-File-Size":    strconv.FormatInt(seg.FileSize, 10),
			"X-Layer":        "sub",
		})
		return
	}
	if tiered {
		slog.Debug("vision push skipped — camera served by tierrec sub layer",
			"camera_id", seg.CameraID,
			"recording_id", seg.RecordingID)
		return
	}
	// 子流分析层相机 (#514):分析输入改由子流段提供(独立目录、磁盘即队
	// 列),主流段不再推送——低分辨率段的解码成本才能让单消费者覆盖全部
	// 高分辨率相机。
	if vcfg.SubLayerCameraSet()[seg.CameraID] {
		slog.Debug("vision push skipped — camera served by sub layer",
			"camera_id", seg.CameraID,
			"recording_id", seg.RecordingID)
		return
	}

	// 解析段文件的绝对路径。NVR 的 file_path 已经是绝对路径(/mnt/data/nvr/...)。
	absPath := seg.FilePath
	if !filepath.IsAbs(absPath) {
		absPath = filepath.Join(c.storageRoot(), absPath)
	}

	c.uploadSegment(ctx, absPath, seg.FileSize, map[string]string{
		"X-Recording-Id": seg.RecordingID,
		"X-Camera-Id":    seg.CameraID,
		"X-Format":       seg.Format,
		"X-Encoding":     seg.Encoding,
		"X-Started-At":   seg.StartedAt,
		"X-Ended-At":     seg.EndedAt,
		"X-File-Size":    strconv.FormatInt(seg.FileSize, 10),
	})
}

// uploadSegment POSTs a segment file's bytes to Vision. Headers carry the
// metadata (Vision's minimal HTTP parser can't do chunked encoding — Content-
// Length is set explicitly). Returns true on a 2xx response.
//
// Content-Length MUST come from the file on disk, never from the event's
// FileSize: producers count only media bytes while the muxer appends the moov
// box at close (tierrec's 60s sub segments carry a constant ~10 KB moov), and
// a short Content-Length against a longer body aborts the transfer client-
// side ("ContentLength=X with Body length Y" — field data 2026-09-02: 530
// consecutive tiered-push losses before this was found).
func (c *Coordinator) uploadSegment(ctx context.Context, absPath string, fileSize int64, hdr map[string]string) bool {
	file, err := os.Open(absPath)
	if err != nil {
		slog.Warn("open segment file for push failed",
			"error", err,
			"path", absPath)
		return false
	}
	defer file.Close()

	st, err := file.Stat()
	if err != nil {
		slog.Warn("stat segment file for push failed",
			"error", err,
			"path", absPath)
		return false
	}
	realSize := st.Size()
	hdr["X-File-Size"] = strconv.FormatInt(realSize, 10)

	url := strings.TrimRight(c.cfg().URL, "/") + "/vision/segment/upload"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, file)
	if err != nil {
		slog.Error("create upload request", "error", err)
		return false
	}
	// 显式设置 Content-Length,避免 Go 使用 chunked encoding(Vision 的极简 HTTP 解析器不支持 chunked)。
	req.ContentLength = realSize
	req.Header.Set("Content-Type", "application/octet-stream")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		slog.Warn("push video to vision failed",
			"error", err,
			"path", filepath.Base(absPath))
		return false
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		slog.Warn("vision returned non-success status",
			"status", resp.StatusCode,
			"path", filepath.Base(absPath))
		return false
	}
	slog.Info("pushed video to vision",
		"camera_id", hdr["X-Camera-Id"],
		"size_mb", realSize/1024/1024)
	return true
}

// pushSubSegment pushes one sub-layer segment (#514). Same transport/headers
// as the main layer, plus X-Layer: sub; the recording id already carries the
// joined MAIN recording so consumer-side dedup and ai_status semantics are
// unchanged. Returns true when the consumer accepted it (caller deletes).
func (c *Coordinator) pushSubSegment(ctx context.Context, seg SubSegment) bool {
	vcfg := c.cfg()
	if !vcfg.Enabled || vcfg.URL == "" || !c.health.IsHealthy() {
		return false
	}
	ok := c.uploadSegment(ctx, seg.Path, seg.FileSize, map[string]string{
		"X-Recording-Id": seg.RecordingID,
		"X-Camera-Id":    seg.CameraID,
		"X-Format":       seg.Codec,
		"X-Encoding":     seg.Codec,
		"X-Started-At":   seg.StartedAt.UTC().Format(time.RFC3339Nano),
		"X-Ended-At":     seg.EndedAt.UTC().Format(time.RFC3339Nano),
		"X-File-Size":    strconv.FormatInt(seg.FileSize, 10),
		"X-Layer":        "sub",
	})
	if ok {
		slog.Info("pushed sub-layer video to vision",
			"camera_id", seg.CameraID,
			"recording_id", seg.RecordingID,
			"size_mb", seg.FileSize/1024/1024)
	}
	return ok
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
