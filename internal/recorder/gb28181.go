package recorder

import (
	"context"
	"log/slog"
	"os"
	"runtime"
	"strconv"
	"sync"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/event"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/metrics"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/muxer"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
)

var gb28181Logger = slog.Default().With("component", "gb28181-recorder")

// GB28181Config configures the passive GB28181 recorder. It mirrors
// IngestConfig: Store/DB/Metrics/EventBus wire the recorder into the normal
// NVR recording pipeline (segments on disk + recordings rows + events), and
// RecordEnabled=false keeps the camera live-only (hub fan-out, no segments).
type GB28181Config struct {
	CameraID      string
	Encoding      string
	SegmentDur    time.Duration
	Store         *storage.Manager
	DB            *storage.DB
	Metrics       *metrics.Metrics
	EventBus      *event.EventBus
	RecordEnabled bool
}

// GB28181Recorder is a passive recorder for GB/T 28181 channels: media does
// not arrive from a client connection we dial — the SIP server INVITEs the
// channel and bridges RTP receiver output into WriteNALU. Start puts the
// recorder into Reconnecting ("waiting for INVITE"); the SIP server flips it
// to Recording via OnInvite once the INVITE succeeds.
type GB28181Recorder struct {
	cfg GB28181Config
	// Hub is set by camera.initStreamHub (same pattern as H264Recorder.Hub).
	Hub           *model.StreamHub
	mu            sync.Mutex
	status        model.RecorderStatus
	sps, pps, vps []byte
	codecType     string
	muxer         *muxer.MP4Muxer
	trackID       int
	curTemp       string
	curFinal      string
	segStart      time.Time
	lastFrameTime time.Time
	frameCount    int
	ptsBase       int64 // first AU's RTP timestamp (90kHz), PTS origin
	lastPtsTicks  int64 // last written AU's RTP timestamp (monotonic guard)
}

var (
	_ model.Recorder    = (*GB28181Recorder)(nil)
	_ model.HLSProvider = (*GB28181Recorder)(nil)
)

// NewGB28181Recorder creates a recorder for a GB28181 channel camera.
func NewGB28181Recorder(cfg GB28181Config, hub *model.StreamHub) *GB28181Recorder {
	if cfg.SegmentDur < time.Millisecond {
		cfg.SegmentDur = 10 * time.Minute
	}
	return &GB28181Recorder{cfg: cfg, Hub: hub, status: model.StatusStopped}
}

// Start marks the recorder as waiting for its INVITE (Reconnecting). The SIP
// server calls OnInvite after the INVITE handshake completes.
func (r *GB28181Recorder) Start(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.status == model.StatusRecording || r.status == model.StatusReconnecting {
		return nil
	}
	r.status = model.StatusReconnecting
	return nil
}

func (r *GB28181Recorder) Stop() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.status == model.StatusStopped {
		return nil
	}
	r.closeCurrentSegmentLocked()
	r.status = model.StatusStopped
	return nil
}

func (r *GB28181Recorder) Status() model.RecorderStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.status
}

func (r *GB28181Recorder) GetHub() *model.StreamHub {
	return r.Hub
}

func (r *GB28181Recorder) CodecParams() (codec model.Format, sps, pps, vps []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.codecType == "h265" {
		return model.FormatH265, r.sps, r.pps, r.vps
	}
	return model.FormatH264, r.sps, r.pps, nil
}

// OnInvite transitions to Recording — called by the SIP server after the
// INVITE 200 OK + ACK handshake succeeded.
func (r *GB28181Recorder) OnInvite() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.status == model.StatusStopped {
		return
	}
	r.status = model.StatusRecording
}

// OnBye transitions back to Reconnecting — the session ended (device BYE,
// device offline); the periodic re-REGISTER auto-INVITE will resume media.
func (r *GB28181Recorder) OnBye() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.status == model.StatusStopped {
		return
	}
	r.closeCurrentSegmentLocked()
	r.status = model.StatusReconnecting
}

// WriteNALU ingests one complete access unit from the RTP receiver bridge.
// ptsTicks is the RTP timestamp (90kHz) of the AU.
func (r *GB28181Recorder) WriteNALU(au [][]byte, ptsTicks int64, isIDR bool) {
	defer func() {
		if panicErr := recover(); panicErr != nil {
			buf := make([]byte, 4096)
			buf = buf[:runtime.Stack(buf, false)]
			gb28181Logger.Error("PANIC recovered in WriteNALU", "camera_id", r.cfg.CameraID, "panic", panicErr, "stack", string(buf))
		}
	}()

	r.mu.Lock()
	// A stopped recorder must stay stopped: stale RTP from a torn-down
	// session must not resurrect segments for a removed/archived camera.
	if r.status == model.StatusStopped {
		r.mu.Unlock()
		return
	}
	if r.codecType == "" && len(au) > 0 {
		r.codecType = detectCodec(au, r.cfg.Encoding)
	}
	if r.codecType == "h264" {
		newSPS, newPPS, changed := updateParamSetsH264(au, r.sps, r.pps)
		if changed {
			r.closeCurrentSegmentLocked()
		}
		if newSPS != nil {
			r.sps = newSPS
		}
		if newPPS != nil {
			r.pps = newPPS
		}
	} else if r.codecType == "h265" {
		newVPS, newSPS, newPPS, changed := updateParamSetsH265(au, r.vps, r.sps, r.pps)
		if changed {
			r.closeCurrentSegmentLocked()
		}
		if newVPS != nil {
			r.vps = newVPS
		}
		if newSPS != nil {
			r.sps = newSPS
		}
		if newPPS != nil {
			r.pps = newPPS
		}
	}
	hub := r.Hub
	localSPS, localPPS, localVPS, localCodecType, curMux := r.sps, r.pps, r.vps, r.codecType, r.muxer
	recordEnabled := r.cfg.RecordEnabled && r.cfg.Store != nil
	if curMux == nil && (!isIDR || !recordEnabled) {
		r.mu.Unlock()
		// Live-only or waiting for an IDR: fan out to the hub, no segment.
		if hub != nil {
			broadcastAU := prepareBroadcastAU(au, isIDR, localCodecType, localSPS, localPPS, localVPS)
			hub.Broadcast(ptsTicks, broadcastAU, isIDR)
		}
		return
	}
	if curMux == nil {
		if localCodecType == "h264" && (localSPS == nil || localPPS == nil) {
			r.mu.Unlock()
			return
		}
		if localCodecType == "h265" && (localVPS == nil || localSPS == nil || localPPS == nil) {
			r.mu.Unlock()
			return
		}
	}
	trackID := r.trackID
	r.mu.Unlock()

	// Hub fan-out (live view) happens for every AU, recording or not.
	if hub != nil {
		broadcastAU := prepareBroadcastAU(au, isIDR, localCodecType, localSPS, localPPS, localVPS)
		hub.Broadcast(ptsTicks, broadcastAU, isIDR)
	}

	if !recordEnabled {
		return
	}

	vclNALU := findVCLNALU(au, localCodecType)
	if vclNALU == nil {
		return
	}

	r.mu.Lock()
	if curMux == nil {
		tempPath, finalPath, err := r.cfg.Store.CreateSegment(r.cfg.CameraID, localCodecType)
		if err != nil {
			r.mu.Unlock()
			gb28181Logger.Error("failed to create segment", "camera_id", r.cfg.CameraID, "error", err)
			return
		}
		newMux := muxer.NewMP4Muxer(tempPath)
		var newTrackID int
		if localCodecType == "h265" {
			newTrackID, err = newMux.AddH265Track(localVPS, localSPS, localPPS)
		} else {
			newTrackID, err = newMux.AddH264Track(localSPS, localPPS)
		}
		if err != nil {
			r.mu.Unlock()
			gb28181Logger.Error("failed to add video track", "camera_id", r.cfg.CameraID, "codec", localCodecType, "error", err)
			os.Remove(tempPath)
			return
		}
		now := time.Now()
		r.muxer = newMux
		r.trackID = newTrackID
		r.segStart = now
		r.curTemp = tempPath
		r.curFinal = finalPath
		r.lastFrameTime = now
		r.frameCount = 0
		r.ptsBase = ptsTicks
		r.lastPtsTicks = ptsTicks
		curMux = newMux
		trackID = newTrackID
	}

	// PTS from the RTP 90kHz clock, anchored at the first AU of the segment.
	// Monotonic guard: RTP timestamp wrap or jitter must never move backwards.
	if ptsTicks <= r.ptsBase {
		ptsTicks = r.lastPtsTicks + 1
	}
	lastPts := r.lastPtsTicks
	if lastPts < r.ptsBase {
		lastPts = r.ptsBase
	}
	r.lastPtsTicks = ptsTicks
	r.mu.Unlock()

	pts := ticksToDuration(ptsTicks - r.ptsBase)
	dur := ticksToDuration(ptsTicks - lastPts)
	if dur < time.Millisecond {
		dur = time.Millisecond
	}

	if err := curMux.WriteSample(trackID, vclNALU, pts, dur); err != nil {
		gb28181Logger.Error("failed to write sample", "camera_id", r.cfg.CameraID, "error", err)
		return
	}

	r.mu.Lock()
	r.lastFrameTime = time.Now()
	r.frameCount++
	if time.Since(r.segStart) >= r.cfg.SegmentDur {
		r.closeCurrentSegmentLocked()
	}
	r.mu.Unlock()
}

// ticksToDuration converts 90kHz RTP clock ticks to a duration.
func ticksToDuration(ticks int64) time.Duration {
	return time.Duration(ticks) * time.Second / 90000
}

func (r *GB28181Recorder) closeCurrentSegmentLocked() {
	if r.muxer == nil {
		return
	}
	if err := r.muxer.Close(); err != nil {
		gb28181Logger.Error("failed to close muxer", "camera_id", r.cfg.CameraID, "error", err)
		if r.curTemp != "" {
			os.Remove(r.curTemp)
		}
		r.muxer = nil
		r.curTemp = ""
		r.curFinal = ""
		r.frameCount = 0
		return
	}

	// Atomic rename: temp → final.
	if r.curTemp != "" && r.curFinal != "" {
		if err := r.cfg.Store.CloseSegment(r.curTemp, r.curFinal); err != nil {
			gb28181Logger.Error("failed to close segment", "camera_id", r.cfg.CameraID, "error", err)
		}
	}

	// Insert the recording row so the segment is visible to playback, merge,
	// timelapse, and retention cleanup.
	var fileSize int64
	var recordingID string
	format := model.FormatH264
	if r.codecType == "h265" {
		format = model.FormatH265
	}
	if r.cfg.DB != nil && r.curFinal != "" {
		now := time.Now()
		duration := now.Sub(r.segStart).Seconds()
		rec := &model.Recording{
			ID:         strconv.FormatInt(now.UnixNano(), 10),
			CameraID:   r.cfg.CameraID,
			FilePath:   r.curFinal,
			Format:     format,
			StartedAt:  r.segStart,
			EndedAt:    now,
			Duration:   duration,
			FrameCount: r.frameCount,
		}
		recordingID = rec.ID
		if info, err := os.Stat(r.curFinal); err == nil {
			fileSize = info.Size()
			rec.FileSize = fileSize
		}
		if err := r.cfg.DB.InsertRecordingWithRetry(context.Background(), rec, 3, 500*time.Millisecond); err != nil {
			gb28181Logger.Error("failed to insert recording", "camera_id", r.cfg.CameraID, "error", err)
		}
	}

	// Publish SegmentCompleted so merge/timelapse pick the segment up.
	if r.cfg.EventBus != nil && recordingID != "" {
		r.cfg.EventBus.Publish(context.Background(), event.TopicSegmentCompleted, event.SegmentCompleted{
			CameraID:    r.cfg.CameraID,
			FilePath:    r.curFinal,
			Format:      string(format),
			Encoding:    string(format),
			StartedAt:   r.segStart.Format(time.RFC3339Nano),
			EndedAt:     time.Now().Format(time.RFC3339Nano),
			FileSize:    fileSize,
			RecordingID: recordingID,
		})
	}

	// Update metrics for the completed segment.
	if r.frameCount > 0 && r.curFinal != "" && r.cfg.Metrics != nil {
		r.cfg.Metrics.SegmentsCreated.WithLabelValues(r.cfg.CameraID, r.codecType).Inc()
		if fileSize > 0 {
			r.cfg.Metrics.RecordingBytesTotal.WithLabelValues(r.cfg.CameraID, r.codecType).Add(float64(fileSize))
		}
	}

	r.muxer = nil
	r.curTemp = ""
	r.curFinal = ""
	r.frameCount = 0
}
