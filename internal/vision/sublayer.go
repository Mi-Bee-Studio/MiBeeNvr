package vision

// sublayer.go — 子码流分析层 (#514)。
//
// 为 vision.sub_layer_cameras 列表内的相机维护一个独立的低分辨率录像层:
// 按需拉取子码流(持久引用,复用 #528 的 substream.Manager),按段写成 mp4,
// 存放于 <storage.root>/sublayer/<camera_id>/。该层:
//   - 不进录像库(无 recordings 行)、不参与合并/常规清理;
//   - 生命周期自管: 推送成功即删,磁盘保留时长兜底(消费者离线防堆积);
//   - 推送走磁盘扫描而非 segment.completed 事件——子流段不与合并/删除窗口
//     竞争,磁盘本身就是队列。X-Recording-Id 关联同时段的主流录像 ID,
//     消费端去重与 ai_status 语义与主流层完全一致。
//
// 主流让位: Coordinator.handleSegment 对 sub_layer 相机跳过主流段推送,
// 该相机的分析输入完全来自本层(低分辨率段解码成本为主流的 1/4~1/16)。

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model/nalutil"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/muxer"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/substream"
)

const (
	subLayerDir = "sublayer"
	subLayerTmp = ".tmp"

	defaultSubLayerSegmentSecs   = 60
	defaultSubLayerRetentionSecs = 7200
	defaultSubLayerPushInterval  = 20
	subLayerReconcileInterval    = 15 * time.Second
	subLayerStatePoll            = 5 * time.Second
	subLayerReacquireBackoff     = 5 * time.Second
	subLayerPushPacing           = 100 * time.Millisecond
	// RTP video clock — the sub-stream hub's pts timeline is 90 kHz ticks
	// (rebased cross-session by the puller).
	subLayerTickHz = 90000
	// Clamp a single sample's duration: the puller's cross-session rebase adds
	// a 1s gap, and a stalled-then-resumed feed must not produce one giant
	// frame that dwarfs the segment timeline.
	subLayerMaxSampleDur = 500 * time.Millisecond
	subLayerMinSampleDur = time.Millisecond
)

var subLayerLogger = slog.Default().With("component", "vision-sublayer")

// SubLayerProvider abstracts the camera manager's on-demand sub-stream source
// (AcquireSubStream/ReleaseSubStream) so the vision package doesn't import the
// camera package.
type SubLayerProvider interface {
	AcquireSubStream(ctx context.Context, cameraID string) (*substream.Source, error)
	ReleaseSubStream(cameraID string)
}

// SubSegment describes one finalized sub-layer segment, for the push headers
// and the on-disk metadata registry.
type SubSegment struct {
	CameraID    string
	Path        string
	Codec       string // "h264" | "h265"
	StartedAt   time.Time
	EndedAt     time.Time
	FileSize    int64
	RecordingID string // joined main recording id; "sub-<nano>" when none
}

// SubLayerDeps are the coordinator-provided behaviors the layer needs.
type SubLayerDeps struct {
	// Push attempts one segment upload; true = consumer accepted (file may be
	// deleted).
	Push func(ctx context.Context, seg SubSegment) bool
	// Healthy gates pushing (segments stay on disk, TTL-bounded, and are
	// pushed on a later sweep once healthy — the disk is the queue).
	Healthy func() bool
}

// SubLayerManager owns the per-camera sub-layer recorders plus the reconcile /
// push / retention loops.
type SubLayerManager struct {
	provider    SubLayerProvider
	cfg         func() config.VisionConfig
	storageRoot func() string
	deps        SubLayerDeps

	mu        sync.Mutex
	recorders map[string]*subLayerRecorder
	// meta carries finalized-but-unpushed segment info (path → payload). Lost
	// on restart — the sweep then degrades to filename-derived times and the
	// consumer sniffs the codec from the MP4 payload anyway.
	meta map[string]SubSegment
	// mainIDs: cameraID → latest main recording id (event-fed join).
	mainIDs map[string]string

	cancelFn context.CancelFunc
	wg       sync.WaitGroup
	started  bool
}

// NewSubLayerManager creates the manager. A nil provider or nil deps disables
// the layer entirely (Start is a no-op).
func NewSubLayerManager(provider SubLayerProvider, cfg func() config.VisionConfig, storageRoot func() string, deps SubLayerDeps) *SubLayerManager {
	return &SubLayerManager{
		provider:    provider,
		cfg:         cfg,
		storageRoot: storageRoot,
		deps:        deps,
		recorders:   make(map[string]*subLayerRecorder),
		meta:        make(map[string]SubSegment),
		mainIDs:     make(map[string]string),
	}
}

// Start launches the reconcile/push/retention loop. Cheap no-op when the layer
// is disabled or the camera list is empty (the tick re-evaluates config each
// pass, so cameras can be added at runtime).
func (m *SubLayerManager) Start(ctx context.Context) {
	if m == nil || m.provider == nil || m.deps.Push == nil || m.deps.Healthy == nil {
		return
	}
	m.mu.Lock()
	if m.started {
		m.mu.Unlock()
		return
	}
	m.started = true
	ctx, m.cancelFn = context.WithCancel(ctx)
	m.mu.Unlock()

	m.wg.Add(1)
	go m.loop(ctx)
	subLayerLogger.Info("vision sub-layer manager started")
}

// Stop tears down every recorder and the loops.
func (m *SubLayerManager) Stop() {
	if m == nil {
		return
	}
	m.mu.Lock()
	if m.cancelFn != nil {
		m.cancelFn()
	}
	recorders := make([]*subLayerRecorder, 0, len(m.recorders))
	for _, r := range m.recorders {
		recorders = append(recorders, r)
	}
	m.mu.Unlock()
	for _, r := range recorders {
		r.stop()
	}
	m.wg.Wait()
	m.mu.Lock()
	m.started = false
	m.mu.Unlock()
	subLayerLogger.Info("vision sub-layer manager stopped")
}

// NoteMainSegment records the latest main recording id for a camera — the
// event-fed join that keeps X-Recording-Id pointing at real recordings rows.
func (m *SubLayerManager) NoteMainSegment(cameraID, recordingID string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.mainIDs[cameraID] = recordingID
	m.mu.Unlock()
}

// loop runs reconcile (recorder set vs config), retention cleanup, and the
// push sweep. One goroutine, three cadences — they share no state beyond the
// maps (mu-guarded) so a single loop keeps the ordering simple: reconcile →
// push → expire.
func (m *SubLayerManager) loop(ctx context.Context) {
	defer m.wg.Done()
	reconcile := time.NewTicker(subLayerReconcileInterval)
	defer reconcile.Stop()
	pushEvery := m.pushInterval()
	push := time.NewTicker(pushEvery)
	defer push.Stop()

	m.reconcile(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-reconcile.C:
			m.reconcile(ctx)
			// Config may have changed the push cadence.
			if next := m.pushInterval(); next != pushEvery {
				pushEvery = next
				push.Reset(pushEvery)
			}
		case <-push.C:
			m.sweepPush(ctx)
			m.sweepExpire()
		}
	}
}

func (m *SubLayerManager) pushInterval() time.Duration {
	secs := m.cfg().SubLayerPushIntervalSecs
	if secs <= 0 {
		secs = defaultSubLayerPushInterval
	}
	return time.Duration(secs) * time.Second
}

// reconcile aligns the active recorder set with cfg().SubLayerCameraSet().
// An empty set (or vision disabled) stops everything — the layer exists only
// for the consumer.
func (m *SubLayerManager) reconcile(ctx context.Context) {
	desired := map[string]bool{}
	if m.cfg().Enabled {
		desired = m.cfg().SubLayerCameraSet()
	}

	m.mu.Lock()
	var toStop []*subLayerRecorder
	for id, r := range m.recorders {
		if !desired[id] {
			toStop = append(toStop, r)
			delete(m.recorders, id)
		}
	}
	m.mu.Unlock()
	for _, r := range toStop {
		subLayerLogger.Info("sub-layer recorder stopping (removed from config)", "camera_id", r.cameraID)
		r.stop()
	}

	for id := range desired {
		m.mu.Lock()
		_, active := m.recorders[id]
		m.mu.Unlock()
		if active {
			continue
		}
		acqCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		src, err := m.provider.AcquireSubStream(acqCtx, id)
		cancel()
		if err != nil {
			subLayerLogger.Warn("sub-layer acquire failed, will retry", "camera_id", id, "error", err)
			continue
		}
		r := newSubLayerRecorder(m, id, src)
		m.mu.Lock()
		m.recorders[id] = r
		m.mu.Unlock()
		r.start(ctx)
		subLayerLogger.Info("sub-layer recorder started",
			"camera_id", id, "segment_secs", m.segmentSecs())
	}
}

func (m *SubLayerManager) segmentSecs() time.Duration {
	secs := m.cfg().SubLayerSegmentSecs
	if secs <= 0 {
		secs = defaultSubLayerSegmentSecs
	}
	return time.Duration(secs) * time.Second
}

func (m *SubLayerManager) retention() time.Duration {
	secs := m.cfg().SubLayerRetentionSecs
	if secs <= 0 {
		secs = defaultSubLayerRetentionSecs
	}
	return time.Duration(secs) * time.Second
}

func (m *SubLayerManager) cameraDir(cameraID string) string {
	return filepath.Join(m.storageRoot(), subLayerDir, cameraID)
}

// registerMeta records a finalized segment's push payload.
func (m *SubLayerManager) registerMeta(seg SubSegment) {
	m.mu.Lock()
	m.meta[seg.Path] = seg
	m.mu.Unlock()
}

// joinRecordingID picks the payload recording id: the latest main recording
// for the camera when one exists (ai_status/ai_processed_at keep working),
// else a synthetic id so the consumer can still dedup/report.
func (m *SubLayerManager) joinRecordingID(cameraID string, startedAt time.Time) string {
	m.mu.Lock()
	id := m.mainIDs[cameraID]
	m.mu.Unlock()
	if id != "" {
		return id
	}
	return "sub-" + strconv.FormatInt(startedAt.UnixNano(), 10)
}

// sweepPush walks the sub-layer tree and pushes finalized segments (oldest
// first) while the consumer is healthy. The DIRS are the queue — not the
// active recorder set — so files left by a restart (or a camera since removed
// from the list) still drain. Files are deleted only after an accepted push.
func (m *SubLayerManager) sweepPush(ctx context.Context) {
	if ctx.Err() != nil || !m.deps.Healthy() || !m.cfg().Enabled {
		return
	}
	root := filepath.Join(m.storageRoot(), subLayerDir)
	dirs, err := os.ReadDir(root)
	if err != nil {
		if !os.IsNotExist(err) {
			subLayerLogger.Warn("sub-layer scan failed", "error", err)
		}
		return
	}
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		cameraID := d.Name()
		if m.cfg().ShouldSkipCamera(cameraID) {
			continue
		}
		files, err := listSubLayerFiles(filepath.Join(root, cameraID))
		if err != nil {
			continue
		}
		for _, path := range files {
			if ctx.Err() != nil {
				return
			}
			if !m.deps.Push(ctx, m.segmentFor(path)) {
				// Unhealthy mid-sweep or upload failed — stop this camera's
				// pass; next sweep retries (oldest-first keeps ordering).
				return
			}
			_ = os.Remove(path)
			m.mu.Lock()
			delete(m.meta, path)
			m.mu.Unlock()
			subLayerLogger.Debug("sub-layer segment pushed and removed", "path", path)
			time.Sleep(subLayerPushPacing)
		}
	}
}

// sweepExpire drops segments older than the retention bound regardless of push
// state — a permanently-offline consumer must not grow the layer unbounded.
func (m *SubLayerManager) sweepExpire() {
	cutoff := time.Now().Add(-m.retention())
	root := filepath.Join(m.storageRoot(), subLayerDir)
	entries, err := os.ReadDir(root)
	if err != nil {
		if !os.IsNotExist(err) {
			subLayerLogger.Warn("sub-layer retention scan failed", "error", err)
		}
		return
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		files, err := listSubLayerFiles(filepath.Join(root, e.Name()))
		if err != nil {
			continue
		}
		for _, path := range files {
			info, err := os.Stat(path)
			if err != nil {
				continue
			}
			if info.ModTime().Before(cutoff) {
				_ = os.Remove(path)
				m.mu.Lock()
				delete(m.meta, path)
				m.mu.Unlock()
				subLayerLogger.Info("sub-layer segment expired", "path", path, "age", time.Since(info.ModTime()).Round(time.Second))
			}
		}
	}
}

// segmentFor assembles the push payload for a file, preferring the recorder's
// registry entry and degrading to filename-derived values.
func (m *SubLayerManager) segmentFor(path string) SubSegment {
	m.mu.Lock()
	seg, ok := m.meta[path]
	m.mu.Unlock()
	if ok {
		return seg
	}
	cameraID := filepath.Base(filepath.Dir(path))
	startedAt, codec := subLayerFilenameInfo(filepath.Base(path))
	seg = SubSegment{
		CameraID:    cameraID,
		Path:        path,
		Codec:       codec,
		StartedAt:   startedAt,
		EndedAt:     startedAt,
		RecordingID: m.joinRecordingID(cameraID, startedAt),
	}
	if info, err := os.Stat(path); err == nil {
		seg.FileSize = info.Size()
	}
	return seg
}

// listSubLayerFiles returns finalized segment paths in a camera dir, oldest
// first (names embed the start unix-nano). Tmp files are skipped — the
// recorder still owns them.
func listSubLayerFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".mp4") || strings.HasSuffix(e.Name(), subLayerTmp) {
			continue
		}
		out = append(out, filepath.Join(dir, e.Name()))
	}
	sort.Strings(out)
	return out, nil
}

// subLayerFilenameInfo parses "<unixnano>-<codec>.mp4" back into its parts.
func subLayerFilenameInfo(name string) (time.Time, string) {
	base := strings.TrimSuffix(name, ".mp4")
	codec := ""
	if i := strings.LastIndex(base, "-"); i >= 0 {
		if c := base[i+1:]; c == "h264" || c == "h265" {
			codec = c
			base = base[:i]
		}
	}
	nano, err := strconv.ParseInt(base, 10, 64)
	if err != nil {
		return time.Time{}, codec
	}
	return time.Unix(0, nano), codec
}

// ─── per-camera recorder ────────────────────────────────────────────────────

// subLayerRecorder pulls one camera's sub-stream (the acquisition is held for
// the recorder's whole lifetime — the substream manager's idle recycle never
// fires under a held reference) and rotates mp4 segments out of its hub feed.
type subLayerRecorder struct {
	mgr      *SubLayerManager
	cameraID string

	cancel context.CancelFunc
	done   chan struct{}

	// The hub consumer callback runs on the hub's drain goroutine while the
	// run loop may close the segment on source failure — smu guards the
	// segment state.
	smu          sync.Mutex
	stopped      bool
	src          *substream.Source
	subID        string
	mux          *muxer.MP4Muxer
	trackID      int
	tmpPath      string
	segCodec     model.Format
	segSPS       []byte
	segStartTick int64
	lastTick     int64
	startedAt    time.Time
	frames       int
}

func newSubLayerRecorder(m *SubLayerManager, cameraID string, src *substream.Source) *subLayerRecorder {
	return &subLayerRecorder{mgr: m, cameraID: cameraID, src: src}
}

func (r *subLayerRecorder) start(ctx context.Context) {
	ctx, r.cancel = context.WithCancel(ctx)
	r.done = make(chan struct{})
	go r.run(ctx)
}

func (r *subLayerRecorder) stop() {
	if r.cancel != nil {
		r.cancel()
	}
	if r.done != nil {
		<-r.done
	}
}

// run keeps a healthy source under the recorder: on permanent pull failure
// (StateFailed → the source self-recycled) it releases and re-acquires with
// backoff. Transient stalls are the substream manager's own reconnect's job.
func (r *subLayerRecorder) run(ctx context.Context) {
	defer close(r.done)
	holding := true
	for {
		if ctx.Err() != nil {
			break
		}
		if err := r.record(ctx); err != nil {
			subLayerLogger.Warn("sub-layer recording ended", "camera_id", r.cameraID, "reason", err.Error())
		}
		r.smu.Lock()
		r.stopped = ctx.Err() != nil
		r.closeSegmentLocked()
		r.unsubscribeLocked()
		if holding {
			r.mgr.provider.ReleaseSubStream(r.cameraID)
			holding = false
		}
		r.smu.Unlock()
		if ctx.Err() != nil {
			break
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(subLayerReacquireBackoff):
		}
		acqCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		src, err := r.mgr.provider.AcquireSubStream(acqCtx, r.cameraID)
		cancel()
		if err != nil {
			subLayerLogger.Warn("sub-layer re-acquire failed, retrying", "camera_id", r.cameraID, "error", err)
			continue
		}
		r.smu.Lock()
		r.src = src
		r.stopped = false
		r.smu.Unlock()
		holding = true
	}
}

// record subscribes to the source's hub and writes segments until the source
// fails or the recorder is stopped.
func (r *subLayerRecorder) record(ctx context.Context) error {
	r.smu.Lock()
	src := r.src
	r.smu.Unlock()
	if src == nil {
		return fmt.Errorf("no source")
	}

	subID := "vision-sublayer-" + r.cameraID
	if err := src.Hub().Subscribe(subID, func(pts int64, au [][]byte) {
		r.onFrame(pts, au)
	}); err != nil {
		return fmt.Errorf("hub subscribe: %w", err)
	}
	r.smu.Lock()
	r.subID = subID
	r.smu.Unlock()

	ticker := time.NewTicker(subLayerStatePoll)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if src.State() == substream.StateFailed {
				return fmt.Errorf("sub-stream source failed (no video track)")
			}
		}
	}
}

func (r *subLayerRecorder) unsubscribeLocked() {
	if r.src != nil && r.subID != "" {
		r.src.Hub().Unsubscribe(r.subID)
		r.subID = ""
	}
}

// onFrame writes one access unit into the current segment, opening/rotating
// as needed. Runs on the hub's per-consumer drain goroutine.
func (r *subLayerRecorder) onFrame(pts int64, au [][]byte) {
	r.smu.Lock()
	defer r.smu.Unlock()
	if r.stopped || r.src == nil {
		return
	}

	codec, sps, pps, vps := r.src.CodecParams()
	if codec != model.FormatH264 && codec != model.FormatH265 {
		return // params not ready yet
	}
	isH265 := codec == model.FormatH265
	isIDR := nalutil.IsIDR(au, isH265)

	if r.mux != nil && isIDR && !bytes.Equal(sps, r.segSPS) {
		// Parameter-set change (camera reconfigured / fresh pull session with
		// new params): the track description is stale — close and reopen with
		// the current sets on this IDR.
		r.closeSegmentLocked()
	}
	if r.mux == nil {
		if !isIDR {
			return // segments start on a keyframe
		}
		if sps == nil || pps == nil || (isH265 && vps == nil) {
			return
		}
		if err := r.openSegmentLocked(codec, sps, pps, vps, pts); err != nil {
			subLayerLogger.Warn("sub-layer segment open failed", "camera_id", r.cameraID, "error", err)
			return
		}
	}

	// Sample timing on the 90 kHz tick timeline: pts relative to the segment's
	// first AU, duration from the AU delta (clamped — the cross-session rebase
	// inserts a 1s gap that must not become one giant sample).
	dur := ticksToDuration(pts - r.lastTick)
	if dur < subLayerMinSampleDur {
		dur = subLayerMinSampleDur
	} else if dur > subLayerMaxSampleDur {
		dur = subLayerMaxSampleDur
	}
	rel := ticksToDuration(pts - r.segStartTick)
	r.lastTick = pts

	for _, nalu := range au {
		if !isVCLNALU(nalu, codec) {
			continue // parameter sets live in the track, SEI is noise here
		}
		if err := r.mux.WriteSample(r.trackID, nalu, rel, dur); err != nil {
			subLayerLogger.Warn("sub-layer write failed", "camera_id", r.cameraID, "error", err)
			r.closeSegmentLocked()
			return
		}
		r.frames++
	}

	if ticksToDuration(pts-r.segStartTick) >= r.mgr.segmentSecs() {
		r.closeSegmentLocked()
	}
}

func (r *subLayerRecorder) openSegmentLocked(codec model.Format, sps, pps, vps []byte, startTick int64) error {
	dir := r.mgr.cameraDir(r.cameraID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	name := fmt.Sprintf("%d-%s.mp4%s", time.Now().UnixNano(), codec, subLayerTmp)
	tmp := filepath.Join(dir, name)
	m := muxer.NewMP4Muxer(tmp)
	var (
		trackID int
		err     error
	)
	switch codec {
	case model.FormatH264:
		trackID, err = m.AddH264Track(sps, pps)
	case model.FormatH265:
		trackID, err = m.AddH265Track(vps, sps, pps)
	default:
		err = fmt.Errorf("unsupported codec %q", codec)
	}
	if err != nil {
		_ = m.Close()
		_ = os.Remove(tmp)
		return err
	}
	r.mux = m
	r.trackID = trackID
	r.tmpPath = tmp
	r.segCodec = codec
	r.segSPS = append([]byte(nil), sps...)
	r.segStartTick = startTick
	r.lastTick = startTick
	r.startedAt = time.Now()
	r.frames = 0
	return nil
}

// closeSegmentLocked finalizes the current segment: close the muxer, publish
// the file, and register its push payload. Callers hold smu.
func (r *subLayerRecorder) closeSegmentLocked() {
	if r.mux == nil {
		return
	}
	m := r.mux
	r.mux = nil
	frames := r.frames
	startedAt := r.startedAt
	codec := r.segCodec
	tmp := r.tmpPath
	r.tmpPath = ""
	if err := m.Close(); err != nil {
		subLayerLogger.Warn("sub-layer muxer close failed", "camera_id", r.cameraID, "error", err)
		_ = os.Remove(tmp)
		return
	}
	if frames == 0 {
		_ = os.Remove(tmp)
		return
	}
	final := strings.TrimSuffix(tmp, subLayerTmp)
	if err := os.Rename(tmp, final); err != nil {
		subLayerLogger.Warn("sub-layer segment publish failed", "camera_id", r.cameraID, "error", err)
		return
	}
	seg := SubSegment{
		CameraID:    r.cameraID,
		Path:        final,
		Codec:       string(codec),
		StartedAt:   startedAt,
		EndedAt:     time.Now(),
		RecordingID: r.mgr.joinRecordingID(r.cameraID, startedAt),
	}
	if info, err := os.Stat(final); err == nil {
		seg.FileSize = info.Size()
	}
	r.mgr.registerMeta(seg)
	subLayerLogger.Info("sub-layer segment finalized",
		"camera_id", r.cameraID, "path", filepath.Base(final),
		"frames", frames, "size", seg.FileSize,
		"duration", seg.EndedAt.Sub(startedAt).Round(time.Second))
}

// ─── helpers ────────────────────────────────────────────────────────────────

func ticksToDuration(ticks int64) time.Duration {
	return time.Duration(ticks) * time.Second / subLayerTickHz
}

// isVCLNALU reports whether a raw NALU (no start code) is a video slice.
func isVCLNALU(nalu []byte, codec model.Format) bool {
	if len(nalu) == 0 {
		return false
	}
	if codec == model.FormatH265 {
		return (nalu[0]>>1)&0x3F <= 31 // IRSI..RSL_NST (0..31); params are 32+
	}
	return nalu[0]&0x1F > 0 && nalu[0]&0x1F <= 5
}
