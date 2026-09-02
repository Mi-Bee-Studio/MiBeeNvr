// Package tierrec — dual-stream tiered recording (#637).
//
// recording_tier: tiered adds a CONTINUOUS sub-stream recording channel: the
// camera's low-bitrate sub-stream is recorded 24/7 as layer-1 rows (the
// never-miss baseline), while the main stream keeps whatever write density
// its recording_mode dictates — for "almost nobody" cameras the intended
// pairing is adaptive + video_exit: false (+ pixgate), making the main
// stream event-only at full quality with the sub stream covering every gap.
//
// The segment writer is the field-proven vision-sublayer recorder shape
// (hub subscribe → GOP-gated MP4 with codec-param rotation, source re-
// acquire with backoff), writing into the regular recordings pipeline
// (same hour-bucket tree, `sub_` filename prefix, layer=1 DB rows). Sub rows
// are excluded from the default list/timeline/merge/Vision-push filters
// (see storage.recordingsFilterWhere + the merge/push queries) and follow
// normal retention.

package tierrec

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/event"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model/nalutil"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/muxer"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/streamhub"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/substream"
)

const (
	segmentSecs    = 60 * time.Second
	statePoll      = 5 * time.Second
	reacquireWait  = 5 * time.Second
	minSampleDur   = 20 * time.Millisecond
	maxSampleDur   = time.Second
	subFilePrefix  = "sub_"
	tmpSuffix      = ".tmp"
	acquireTimeout = 15 * time.Second
)

// Provider acquires/releases the camera's on-demand sub-stream. Satisfied
// by *camera.CameraManager.
type Provider interface {
	AcquireSubStream(ctx context.Context, cameraID string) (*substream.Source, error)
	ReleaseSubStream(cameraID string)
}

// Store persists recording rows.
type Store interface {
	InsertRecording(ctx context.Context, r *model.Recording) error
}

// Config configures the Manager.
type Config struct {
	Provider Provider
	Store    Store
	Bus      *event.EventBus
	// StorageRoot is the recordings root; segment paths are stored relative
	// to it (matching the main recorders' convention).
	StorageRoot string
	// SegmentDur overrides the rotation window (tests).
	SegmentDur time.Duration
	Log        *slog.Logger
}

// Manager runs one sub-stream recorder per tiered camera.
type Manager struct {
	cfg  Config
	log  *slog.Logger
	mu   sync.Mutex
	cams map[string]struct{}
	runs map[string]*camRun
	ctx  context.Context
}

// NewManager builds the manager (a pkg/app Service). Cameras are staged via
// SetCameras and reconciled on Start.
func NewManager(cfg Config) *Manager {
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	if cfg.SegmentDur <= 0 {
		cfg.SegmentDur = segmentSecs
	}
	return &Manager{cfg: cfg, log: cfg.Log.With("component", "tierrec"), cams: map[string]struct{}{}, runs: map[string]*camRun{}}
}

// Name implements pkg/app.Service.
func (m *Manager) Name() string { return "tiered-recording" }

// SetCameras stages the tiered camera set (camera IDs).
func (m *Manager) SetCameras(ids []string) {
	m.mu.Lock()
	m.cams = make(map[string]struct{}, len(ids))
	for _, id := range ids {
		m.cams[id] = struct{}{}
	}
	m.mu.Unlock()
	if m.ctx != nil && m.ctx.Err() == nil {
		m.reconcile()
	}
}

// Start launches the recorders.
func (m *Manager) Start(ctx context.Context) error {
	m.mu.Lock()
	m.ctx = ctx
	cams := make([]string, 0, len(m.cams))
	for id := range m.cams {
		cams = append(cams, id)
	}
	m.mu.Unlock()
	for _, id := range cams {
		m.startCamera(ctx, id)
	}
	return nil
}

// Stop cancels every recorder and joins them.
func (m *Manager) Stop() error {
	m.mu.Lock()
	runs := m.runs
	m.runs = map[string]*camRun{}
	m.mu.Unlock()
	for _, r := range runs {
		r.cancel()
		<-r.done
	}
	return nil
}

func (m *Manager) reconcile() {
	m.mu.Lock()
	ctx := m.ctx
	for id := range m.cams {
		if _, ok := m.runs[id]; !ok {
			m.startCamera(ctx, id)
		}
	}
	var stop []*camRun
	for id, r := range m.runs {
		if _, ok := m.cams[id]; !ok {
			delete(m.runs, id)
			stop = append(stop, r)
		}
	}
	m.mu.Unlock()
	for _, r := range stop {
		r.cancel()
		<-r.done
	}
}

type camRun struct {
	cancel context.CancelFunc
	done   chan struct{}
}

func (m *Manager) startCamera(ctx context.Context, cameraID string) {
	m.mu.Lock()
	if _, ok := m.runs[cameraID]; ok {
		m.mu.Unlock()
		return
	}
	runCtx, cancel := context.WithCancel(ctx)
	r := &camRun{cancel: cancel, done: make(chan struct{})}
	m.runs[cameraID] = r
	m.mu.Unlock()
	go m.runCamera(runCtx, cameraID, r)
}

func (m *Manager) runCamera(ctx context.Context, cameraID string, r *camRun) {
	defer close(r.done)
	log := m.log.With("camera_id", cameraID)
	holding := false
	for ctx.Err() == nil {
		acqCtx, cancel := context.WithTimeout(ctx, acquireTimeout)
		src, err := m.cfg.Provider.AcquireSubStream(acqCtx, cameraID)
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Warn("tierrec: sub-stream acquire failed; retrying", "error", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(reacquireWait):
			}
			continue
		}
		holding = true
		rec := newSubRecorder(m, cameraID, src)
		rec.record(ctx)
		rec.close()
		m.cfg.Provider.ReleaseSubStream(cameraID)
		holding = false
		if ctx.Err() != nil {
			return
		}
		log.Warn("tierrec: sub-stream source ended; re-acquiring")
		select {
		case <-ctx.Done():
			return
		case <-time.After(reacquireWait):
		}
	}
	_ = holding
}

// subSource is the surface subRecorder needs from an acquired sub-stream —
// *substream.Source satisfies it; tests substitute a fake around a real
// streamhub.StreamHub.
type subSource interface {
	Hub() *streamhub.StreamHub
	CodecParams() (codec model.Format, sps, pps, vps []byte)
	State() string
}

// subRecorder writes layer-1 segments from one acquired source. Its shape
// mirrors the vision-sublayer recorder (internal/vision/sublayer.go), minus
// the push/sweep machinery — this is the recordings-pipeline sibling.
type subRecorder struct {
	mgr      *Manager
	cameraID string
	src      subSource

	subID string

	mu           sync.Mutex
	stopped      bool
	mux          *muxer.MP4Muxer
	trackID      int
	tmpPath      string
	finalPath    string
	segCodec     model.Format
	segSPS       []byte
	segStartTick int64
	lastTick     int64
	startedAt    time.Time
	frames       int
	bytes        int64
}

func newSubRecorder(m *Manager, cameraID string, src subSource) *subRecorder {
	return &subRecorder{mgr: m, cameraID: cameraID, src: src}
}

// record subscribes to the hub and returns when the source fails or the
// context ends (the caller then releases + re-acquires).
func (r *subRecorder) record(ctx context.Context) {
	subID := "tierrec-" + r.cameraID
	if err := r.src.Hub().Subscribe(subID, func(pts int64, au [][]byte) {
		r.onFrame(pts, au)
	}); err != nil {
		r.mgr.log.Warn("tierrec: hub subscribe failed", "camera_id", r.cameraID, "error", err)
		return
	}
	r.mu.Lock()
	r.subID = subID
	r.mu.Unlock()

	ticker := time.NewTicker(statePoll)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if r.src.State() == substream.StateFailed {
				return
			}
		}
	}
}

// close tears the recorder down (unsubscribe outside mu — same deadlock
// note as the sublayer recorder).
func (r *subRecorder) close() {
	r.mu.Lock()
	r.stopped = true
	r.closeSegmentLocked()
	unsubID := r.subID
	r.subID = ""
	src := r.src
	r.mu.Unlock()
	if unsubID != "" && src != nil {
		src.Hub().Unsubscribe(unsubID)
	}
}

func (r *subRecorder) onFrame(pts int64, au [][]byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stopped || r.src == nil {
		return
	}
	codec, sps, pps, vps := r.src.CodecParams()
	if codec != model.FormatH264 && codec != model.FormatH265 {
		return
	}
	isH265 := codec == model.FormatH265
	isIDR := nalutil.IsIDR(au, isH265)

	if r.mux != nil && isIDR && !bytes.Equal(sps, r.segSPS) {
		r.closeSegmentLocked()
	}
	if r.mux == nil {
		if !isIDR || sps == nil || pps == nil || (isH265 && vps == nil) {
			if r.frames == 0 && r.mux == nil && isIDR {
				r.mgr.log.Debug("tierrec: waiting for codec params",
					"camera_id", r.cameraID, "codec", codec,
					"sps", sps != nil, "pps", pps != nil, "vps", vps != nil)
			}
			return
		}
		if err := r.openSegmentLocked(codec, sps, pps, vps); err != nil {
			r.mgr.log.Warn("tierrec: segment open failed", "camera_id", r.cameraID, "error", err)
			return
		}
		r.segStartTick = pts
		r.lastTick = pts
		r.mgr.log.Info("tierrec: segment started",
			"camera_id", r.cameraID, "path", filepath.Base(r.finalPath), "codec", codec)
	}

	dur := ticksToDuration(pts - r.lastTick)
	if dur < minSampleDur {
		dur = minSampleDur
	} else if dur > maxSampleDur {
		dur = maxSampleDur
	}
	rel := ticksToDuration(pts - r.segStartTick)
	r.lastTick = pts

	for _, nalu := range au {
		if !isVCL(nalu, codec) {
			continue
		}
		if err := r.mux.WriteSample(r.trackID, nalu, rel, dur); err != nil {
			r.mgr.log.Warn("tierrec: write failed", "camera_id", r.cameraID, "error", err)
			r.closeSegmentLocked()
			return
		}
		r.frames++
		r.bytes += int64(len(nalu))
	}

	if ticksToDuration(pts-r.segStartTick) >= r.mgr.cfg.SegmentDur {
		r.closeSegmentLocked()
	}
}

func (r *subRecorder) openSegmentLocked(codec model.Format, sps, pps, vps []byte) error {
	now := time.Now()
	hourDir := filepath.Join(r.mgr.cfg.StorageRoot, r.cameraID, now.Format("200601"), now.Format("02"), now.Format("15"))
	if err := os.MkdirAll(hourDir, 0o755); err != nil {
		return err
	}
	ts := now.Format("20060102_150405")
	id := now.UnixNano()
	tmp := filepath.Join(hourDir, fmt.Sprintf("%d%s", id, tmpSuffix))
	final := filepath.Join(hourDir, fmt.Sprintf("%s%s_%s_%d.mp4", subFilePrefix, r.cameraID, ts, id))

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
		return err
	}
	r.mux = m
	r.trackID = trackID
	r.tmpPath = tmp
	r.finalPath = final
	r.segCodec = codec
	r.segSPS = append([]byte(nil), sps...)
	r.startedAt = now
	r.frames = 0
	r.bytes = 0
	return nil
}

// closeSegmentLocked finalizes the current segment: rename over the tmp file
// and insert the layer-1 recording row + SegmentCompleted event.
func (r *subRecorder) closeSegmentLocked() {
	if r.mux == nil {
		return
	}
	_ = r.mux.Close()
	r.mux = nil
	if r.frames == 0 {
		_ = os.Remove(r.tmpPath)
		return
	}
	if err := os.Rename(r.tmpPath, r.finalPath); err != nil {
		r.mgr.log.Warn("tierrec: segment rename failed", "camera_id", r.cameraID, "error", err)
		return
	}
	ended := time.Now()
	rel, _ := filepath.Rel(r.mgr.cfg.StorageRoot, r.finalPath)
	if rel == "" || filepath.IsAbs(rel) {
		rel = r.finalPath
	}
	rec := &model.Recording{
		ID:          fmt.Sprintf("%d", r.startedAt.UnixNano()),
		CameraID:    r.cameraID,
		FilePath:    rel,
		Format:      r.segCodec,
		StartedAt:   r.startedAt,
		EndedAt:     ended,
		Duration:    ended.Sub(r.startedAt).Seconds(),
		FileSize:    r.bytes,
		FrameCount:  r.frames,
		MergeStatus: model.MergeStatusPending,
		Layer:       model.LayerSub,
	}
	if err := r.mgr.cfg.Store.InsertRecording(context.Background(), rec); err != nil {
		r.mgr.log.Warn("tierrec: insert recording failed", "camera_id", r.cameraID, "error", err)
	}
	if r.mgr.cfg.Bus != nil {
		r.mgr.cfg.Bus.Publish(context.Background(), event.TopicSegmentCompleted, event.SegmentCompleted{
			CameraID:    r.cameraID,
			FilePath:    rel,
			Format:      string(r.segCodec),
			Encoding:    string(r.segCodec),
			StartedAt:   rec.StartedAt.UTC().Format(time.RFC3339Nano),
			EndedAt:     rec.EndedAt.UTC().Format(time.RFC3339Nano),
			FileSize:    rec.FileSize,
			RecordingID: rec.ID,
			Layer:       model.LayerSub,
		})
	}
	r.mgr.log.Info("tierrec: segment finalized",
		"camera_id", r.cameraID, "frames", r.frames, "bytes", r.bytes,
		"duration_s", ended.Sub(r.startedAt).Round(time.Millisecond).Seconds())
}

func ticksToDuration(t int64) time.Duration { return time.Duration(t) * time.Second / 90000 }

func isVCL(nalu []byte, codec model.Format) bool {
	if len(nalu) == 0 {
		return false
	}
	if codec == model.FormatH265 {
		return (nalu[0]>>1)&0x3f <= 31
	}
	return nalu[0]&0x1f <= 5 && nalu[0]&0x1f != 0
}
