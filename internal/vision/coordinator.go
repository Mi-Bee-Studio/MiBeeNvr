// Package vision 实现 NVR → MiBeeVision 的主动推送集成。
//
// NVR 在段刚落盘时,直接把视频文件字节流 POST 给 Vision(不是通知 file_path 让 Vision 下载)。
// 这样视频数据在 NVR 合并进程删除原文件之前就已经到了 Vision 手里——彻底消除 404。
//
// 多实例(vision.instances):NVR 可同时接入多个 Vision 消费端,每实例独立的
// 地址/心跳健康/暂停窗/离线补偿;相机按 vision_instances 路由(空 = 全部启用
// 实例),推送对路由内全部实例扇出,单实例失败不影响其它。实例身份由心跳/
// 事件/ai_status 回传携带的 API Key 名归因(api_key_name 关联,缺省落 default)。
//
// 推送条件:目标实例心跳健康(HealthTracker.IsHealthy)。
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

// instance 是一个 Vision 消费端实例的运行时状态。配置变化时按 name 对账
// 保留(心跳历史/暂停窗跨配置编辑存活)。
type instance struct {
	// name 创建后不变;url/apiKeyName 由 reconcile 在 instMu 内写、路由快照
	// (routedInstances) 同锁内拷贝读取——推路径只见不可变值拷贝。
	name         string
	url          string
	apiKeyName   string
	health       *HealthTracker
	compensating atomic.Bool // 该实例的补偿重推 single-flight
}

// routeTarget 是推送路径使用的实例快照(值类型,字段在 instMu 内复制——
// 与 reconcile 的字段写不竞态)。
type routeTarget struct {
	name   string
	url    string
	health *HealthTracker
}

// Coordinator 订阅 segment.completed 事件,把视频文件推送给路由命中的各个
// Vision 实例。
type Coordinator struct {
	eventBus      *event.EventBus
	eventCh       chan event.Event
	cfg           func() config.VisionConfig
	storageRoot   func() string // 返回 NVR 录像根目录,用于解析段的绝对路径
	db            Repusher      // nil → 补偿重推禁用(测试/降级部署)
	client        *http.Client
	wg            sync.WaitGroup
	cancelFn      context.CancelFunc
	mu            sync.Mutex
	runCtx        context.Context                // 供恢复回调 goroutine 使用(Start 时设置)
	cameraTargets func(cameraID string) []string // nil → 相机未配置路由(全部实例)
	// instMu 保护 insts/order;reconcileInstances 按配置对账。
	instMu sync.Mutex
	insts  map[string]*instance
	order  []string // 配置顺序的实例名
	// paused 是各实例的推送暂停窗口起点(mu 保护;缺键=未暂停)。
	paused map[string]time.Time
	// subLayer 是子码流分析层 (#514)。nil(未接 provider)时整个层停用。
	subLayer *SubLayerManager
}

// NewCoordinator 创建推送协调器。db 可为 nil(禁用离线补偿);provider 可为
// nil(禁用子码流分析层)。相机路由解析器由 builders 通过 SetCameraTargets
// 注入(nil = 全部实例)。
func NewCoordinator(cfg func() config.VisionConfig, storageRoot func() string, eventBus *event.EventBus, db Repusher, provider SubLayerProvider) *Coordinator {
	c := &Coordinator{
		eventBus:    eventBus,
		cfg:         cfg,
		storageRoot: storageRoot,
		db:          db,
		client: &http.Client{
			Timeout: 120 * time.Second, // 大文件传输可能需要较长时间
		},
		insts:  map[string]*instance{},
		paused: map[string]time.Time{},
	}
	if provider != nil {
		c.subLayer = NewSubLayerManager(provider, cfg, storageRoot, SubLayerDeps{
			Push:    c.pushSubSegment,
			Healthy: c.anyInstanceHealthy,
		})
	}
	return c
}

// SetCameraTargets 注入相机 → vision 实例名的解析器(camera.vision_targets)。
// nil 或返回 nil 表示该相机走默认路由(全部启用实例)。
func (c *Coordinator) SetCameraTargets(fn func(cameraID string) []string) {
	c.cameraTargets = fn
}

// reconcileInstances 按当前配置对账实例集:新增的建 tracker,消失的丢弃,
// 同名保留(健康状态/心跳历史/暂停窗跨配置编辑存活)。
func (c *Coordinator) reconcileInstances() {
	vcfg := c.cfg()
	want := vcfg.EffectiveInstances()
	c.instMu.Lock()
	defer c.instMu.Unlock()
	if c.insts == nil {
		c.insts = map[string]*instance{}
	}
	seen := make(map[string]bool, len(want))
	order := make([]string, 0, len(want))
	for _, ins := range want {
		seen[ins.Name] = true
		order = append(order, ins.Name)
		if old, ok := c.insts[ins.Name]; ok {
			old.url = ins.URL
			old.apiKeyName = ins.APIKeyName
			continue
		}
		h := NewHealthTracker(vcfg.HeartbeatTimeoutSecs)
		// 心跳恢复(不健康→健康)触发该实例的离线补偿重推 (#329)。
		name := ins.Name
		h.SetOnRecovery(func() { c.compensateOffline(name) })
		c.insts[ins.Name] = &instance{
			name:       ins.Name,
			url:        ins.URL,
			apiKeyName: ins.APIKeyName,
			health:     h,
		}
	}
	for name := range c.insts {
		if !seen[name] {
			delete(c.insts, name)
		}
	}
	c.order = order
}

// defaultInstance 返回兼容视角的"默认实例":优先名为 default 的(legacy 单
// 实例合成名),否则配置顺序第一个。无实例时返回 nil。
func (c *Coordinator) defaultInstance() *instance {
	c.reconcileInstances()
	c.instMu.Lock()
	defer c.instMu.Unlock()
	if d, ok := c.insts["default"]; ok {
		return d
	}
	for _, name := range c.order {
		if c.insts[name] != nil {
			return c.insts[name]
		}
	}
	return nil
}

// Health 返回默认实例的 HealthTracker,供 API handler/旧测试记录心跳。
// 单实例部署即唯一实例;多实例部署心跳应走 RecordHeartbeat 按 key 归因。
// 无实例时返回一个临时 tracker(永不健康,行为同旧版未配置)。
func (c *Coordinator) Health() *HealthTracker {
	if ins := c.defaultInstance(); ins != nil {
		return ins.health
	}
	return NewHealthTracker(c.cfg().HeartbeatTimeoutSecs)
}

// RecordHeartbeat 按 API Key 名把心跳归因到实例(找不到 key 关联或匿名 →
// default 实例,兼容旧版消费者)。返回实例名;"" 表示当前没有任何实例
// (集成未启用)。
func (c *Coordinator) RecordHeartbeat(apiKeyName string, st HeartbeatStatus) string {
	ins := c.instanceByAPIKey(apiKeyName)
	if ins == nil {
		return ""
	}
	ins.health.RecordHeartbeat(st)
	return ins.name
}

// instanceByAPIKey 按 api_key_name 查实例;key 为空或未命中时落 default。
func (c *Coordinator) instanceByAPIKey(key string) *instance {
	c.reconcileInstances()
	c.instMu.Lock()
	defer c.instMu.Unlock()
	if key != "" {
		for _, name := range c.order {
			if ins := c.insts[name]; ins != nil && ins.apiKeyName == key {
				return ins
			}
		}
		slog.Debug("vision heartbeat key matches no instance, attributing to default",
			"api_key_name", key)
	}
	if d, ok := c.insts["default"]; ok {
		return d
	}
	for _, name := range c.order {
		if c.insts[name] != nil {
			return c.insts[name]
		}
	}
	return nil
}

// routedInstances 解析一台相机当前的推送目标实例(路由配置 + 实例启用态;
// 健康与否在推送时逐实例判断)。
func (c *Coordinator) routedInstances(cameraID string) []routeTarget {
	vcfg := c.cfg()
	var targets []string
	if c.cameraTargets != nil {
		targets = c.cameraTargets(cameraID)
	}
	routed := vcfg.RouteFor(targets)
	c.instMu.Lock()
	defer c.instMu.Unlock()
	out := make([]routeTarget, 0, len(routed))
	for _, ins := range routed {
		if live := c.insts[ins.Name]; live != nil {
			out = append(out, routeTarget{name: live.name, url: live.url, health: live.health})
		}
	}
	return out
}

// anyInstanceHealthy 报告是否至少一个启用实例健康(子层扫描门控用)。
func (c *Coordinator) anyInstanceHealthy() bool {
	for _, ins := range c.routedInstances("") {
		if ins.health.IsHealthy() {
			return true
		}
	}
	return false
}

// InstanceStatus 一个实例的对外状态快照(供 /api/vision/status)。
type InstanceStatus struct {
	Name        string         `json:"name"`
	URL         string         `json:"url"`
	Healthy     bool           `json:"healthy"`
	LastSeen    *time.Time     `json:"last_seen,omitempty"`
	Device      string         `json:"device,omitempty"`
	QueueDepth  int            `json:"queue_depth,omitempty"`
	Processed   int            `json:"processed,omitempty"`
	SkipCameras []string       `json:"skip_cameras,omitempty"`
	Metrics     *VisionMetrics `json:"metrics,omitempty"`
	MarkedDrops int64          `json:"drops_marked_total,omitempty"`
}

// InstancesStatus 返回全部生效实例的状态(配置顺序)。
func (c *Coordinator) InstancesStatus() []InstanceStatus {
	c.reconcileInstances()
	c.instMu.Lock()
	defer c.instMu.Unlock()
	out := make([]InstanceStatus, 0, len(c.order))
	for _, name := range c.order {
		ins := c.insts[name]
		if ins == nil {
			continue
		}
		st := InstanceStatus{Name: ins.name, URL: ins.url}
		healthy, lastSeen, hs := ins.health.Snapshot()
		st.Healthy = healthy
		if !lastSeen.IsZero() {
			t := lastSeen
			st.LastSeen = &t
		}
		st.Device = hs.Device
		st.QueueDepth = hs.QueueDepth
		st.Processed = hs.ProcessedCount
		st.SkipCameras = hs.SkipCameras
		st.Metrics = hs.Metrics
		st.MarkedDrops = ins.health.MarkedDropTotal()
		out = append(out, st)
	}
	return out
}

// InstanceByName 按名取实例的 HealthTracker(供 /api/vision/metrics?instance=)。
// 未知名或未指定时回落 default 实例。
func (c *Coordinator) InstanceByName(name string) (*HealthTracker, string) {
	c.reconcileInstances()
	c.instMu.Lock()
	defer c.instMu.Unlock()
	if name != "" {
		if ins := c.insts[name]; ins != nil {
			return ins.health, ins.name
		}
	}
	if d, ok := c.insts["default"]; ok {
		return d.health, d.name
	}
	for _, n := range c.order {
		if c.insts[n] != nil {
			return c.insts[n].health, c.insts[n].name
		}
	}
	return nil, ""
}

// SubLayer exposes the sub-layer manager (observability/tests). May be nil.
func (c *Coordinator) SubLayer() *SubLayerManager {
	return c.subLayer
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
		"instances", len(c.InstancesStatus()),
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

// handleSegment 把段视频文件推送给相机路由命中的各个 Vision 实例。
func (c *Coordinator) handleSegment(ctx context.Context, seg event.SegmentCompleted) {
	vcfg := c.cfg()
	if !vcfg.Enabled {
		return
	}
	routed := c.routedInstances(seg.CameraID)
	if len(routed) == 0 {
		return
	}
	// Feed the sub-layer's recording-id join regardless of push outcome — a
	// later sub segment must still map onto the covering main recording.
	// (Only main-layer segments define the join anchor; tierrec layer=1
	// segments reference their own rows.)
	if c.subLayer != nil && seg.Layer == model.LayerMain {
		c.subLayer.NoteMainSegment(seg.CameraID, seg.RecordingID)
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
		c.fanout(ctx, routed, absPath, seg.FileSize, map[string]string{
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

	c.fanout(ctx, routed, absPath, seg.FileSize, map[string]string{
		"X-Recording-Id": seg.RecordingID,
		"X-Camera-Id":    seg.CameraID,
		"X-Format":       seg.Format,
		"X-Encoding":     seg.Encoding,
		"X-Started-At":   seg.StartedAt,
		"X-Ended-At":     seg.EndedAt,
		"X-File-Size":    strconv.FormatInt(seg.FileSize, 10),
	})
}

// fanout 逐实例推送(健康检查+消费者跳单+上传各自独立,失败互不影响)。
func (c *Coordinator) fanout(ctx context.Context, routed []routeTarget, absPath string, fileSize int64, hdr map[string]string) {
	for _, rt := range routed {
		c.pushToInstance(ctx, rt, absPath, fileSize, hdr)
	}
}

// pushToInstance 单实例推送:无地址(default 合成、URL 未配)→ 静默跳过;
// 不健康 → 记暂停窗(补偿重推的窗口锚点)并跳过;该实例心跳声明的跳单
// (#515)→ 跳过;否则上传。
func (c *Coordinator) pushToInstance(ctx context.Context, rt routeTarget, absPath string, fileSize int64, hdr map[string]string) bool {
	if rt.url == "" {
		return false
	}
	if !rt.health.IsHealthy() {
		c.markPausedFor(rt.name)
		slog.Debug("vision instance not healthy, skip push",
			"instance", rt.name,
			"recording_id", hdr["X-Recording-Id"])
		return false
	}
	if rt.health.SkipCamera(hdr["X-Camera-Id"]) {
		slog.Debug("vision push skipped by consumer-reported skip list",
			"instance", rt.name,
			"camera_id", hdr["X-Camera-Id"],
			"recording_id", hdr["X-Recording-Id"])
		return false
	}
	return c.uploadSegment(ctx, rt.url, absPath, fileSize, hdr)
}

// uploadSegment POSTs a segment file's bytes to a Vision instance. Headers
// carry the metadata (Vision's minimal HTTP parser can't do chunked encoding —
// Content-Length is set explicitly). Returns true on a 2xx response.
//
// Content-Length MUST come from the file on disk, never from the event's
// FileSize: producers count only media bytes while the muxer appends the moov
// box at close (tierrec's 60s sub segments carry a constant ~10 KB moov), and
// a short Content-Length against a longer body aborts the transfer client-
// side ("ContentLength=X with Body length Y" — field data 2026-09-02: 530
// consecutive tiered-push losses before this was found).
func (c *Coordinator) uploadSegment(ctx context.Context, baseURL string, absPath string, fileSize int64, hdr map[string]string) bool {
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

	url := strings.TrimRight(baseURL, "/") + "/vision/segment/upload"
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
	slog.Debug("pushed video to vision",
		"camera_id", hdr["X-Camera-Id"],
		"size_mb", realSize/1024/1024)
	return true
}

// pushSubSegment pushes one sub-layer segment (#514) to every routed instance.
// Same transport/headers as the main layer, plus X-Layer: sub; the recording
// id already carries the joined MAIN recording so consumer-side dedup and
// ai_status semantics are unchanged. Returns true (caller deletes) only when
// EVERY currently-healthy routed instance accepted it — an offline instance's
// copy waits on disk for its recovery (TTL-bounded); zero healthy instances
// keeps the file too.
func (c *Coordinator) pushSubSegment(ctx context.Context, seg SubSegment) bool {
	vcfg := c.cfg()
	if !vcfg.Enabled {
		return false
	}
	routed := c.routedInstances(seg.CameraID)
	if len(routed) == 0 {
		return false
	}
	healthy, accepted := 0, 0
	for _, rt := range routed {
		if !rt.health.IsHealthy() {
			continue
		}
		healthy++
		if c.pushToInstance(ctx, rt, seg.Path, seg.FileSize, map[string]string{
			"X-Recording-Id": seg.RecordingID,
			"X-Camera-Id":    seg.CameraID,
			"X-Format":       seg.Codec,
			"X-Encoding":     seg.Codec,
			"X-Started-At":   seg.StartedAt.UTC().Format(time.RFC3339Nano),
			"X-Ended-At":     seg.EndedAt.UTC().Format(time.RFC3339Nano),
			"X-File-Size":    strconv.FormatInt(seg.FileSize, 10),
			"X-Layer":        "sub",
		}) {
			accepted++
		}
	}
	if healthy > 0 && accepted == healthy {
		slog.Info("pushed sub-layer video to vision instances",
			"camera_id", seg.CameraID,
			"recording_id", seg.RecordingID,
			"instances", accepted,
			"size_mb", seg.FileSize/1024/1024)
		return true
	}
	return false
}

// markPausedFor records the start of an instance's push-pause window (first
// segment skipped while that instance is unhealthy). The timestamp bounds the
// offline-compensation query when the instance recovers (#329).
func (c *Coordinator) markPausedFor(name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.paused[name]; !ok {
		c.paused[name] = time.Now()
		slog.Info("vision push paused — offline window starts (segments will be compensated on recovery)",
			"instance", name,
			"since", c.paused[name])
	}
}

// rearmPausedFor restores the pause window with the given start (used when the
// instance drops again mid-compensation, so the next recovery retries the
// remainder instead of only the new gap).
func (c *Coordinator) rearmPausedFor(name string, since time.Time) {
	c.mu.Lock()
	c.paused[name] = since
	c.mu.Unlock()
}

// takePausedSinceFor returns and clears the instance's pause-window start.
func (c *Coordinator) takePausedSinceFor(name string) time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	since, ok := c.paused[name]
	delete(c.paused, name)
	if !ok {
		return time.Time{}
	}
	return since
}

// takePausedSince 兼容访问器:default 实例的暂停窗(旧测试用)。
func (c *Coordinator) takePausedSince() time.Time {
	if ins := c.defaultInstance(); ins != nil {
		return c.takePausedSinceFor(ins.name)
	}
	return time.Time{}
}

func (c *Coordinator) markPaused() {
	if ins := c.defaultInstance(); ins != nil {
		c.markPausedFor(ins.name)
	}
}

func (c *Coordinator) rearmPaused(since time.Time) {
	if ins := c.defaultInstance(); ins != nil {
		c.rearmPausedFor(ins.name, since)
	}
}

// compensateOffline re-pushes segments that completed while ONE instance was
// offline. Triggered by that instance's health tracker recovery callback.
// The re-push routes through handleSegment, so every currently-routed healthy
// instance receives it again — overlap with never-offline instances is
// harmless (consumers dedup by X-Recording-Id) and keeps the single-code-path
// property; the bandwidth cost is bounded by repushWindow.
func (c *Coordinator) compensateOffline(name string) {
	c.mu.Lock()
	ctx := c.runCtx
	c.mu.Unlock()
	if ctx == nil || ctx.Err() != nil {
		return
	}
	c.instMu.Lock()
	ins := c.insts[name]
	c.instMu.Unlock()
	if ins == nil {
		return
	}
	since := c.takePausedSinceFor(name)
	if since.IsZero() || c.db == nil {
		return
	}
	// Only one compensation run per instance at a time (heartbeats can flap).
	if !ins.compensating.CompareAndSwap(false, true) {
		c.rearmPausedFor(name, since)
		return
	}
	defer ins.compensating.Store(false)

	if time.Since(since) > repushWindow {
		since = time.Now().Add(-repushWindow)
	}

	recs, err := c.db.ListRecordingsForVisionRepush(ctx, since, time.Now(), repushMax)
	if err != nil {
		c.rearmPausedFor(name, since)
		slog.Warn("vision compensation lookup failed", "error", err, "instance", name)
		return
	}
	if len(recs) == 0 {
		slog.Debug("vision recovered — nothing to compensate", "instance", name)
		return
	}
	slog.Info("vision recovered — re-pushing segments missed while offline",
		"instance", name,
		"count", len(recs), "since", since)

	pushed := 0
	for _, rec := range recs {
		if ctx.Err() != nil {
			return
		}
		if !ins.health.IsHealthy() {
			// Dropped again mid-compensation: restore the window so the next
			// recovery retries the remainder.
			c.rearmPausedFor(name, since)
			slog.Warn("vision unhealthy during compensation — stopping early",
				"instance", name,
				"pushed", pushed, "total", len(recs))
			return
		}
		c.handleSegment(ctx, recordingToSegment(rec))
		pushed++
		// Pace the burst so a long offline gap doesn't storm Vision.
		time.Sleep(repushPacing)
	}
	slog.Info("vision offline compensation finished",
		"instance", name,
		"pushed", pushed, "window", time.Since(since).Round(time.Second))
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
