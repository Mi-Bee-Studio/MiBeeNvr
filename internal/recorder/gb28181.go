package recorder

import (
	"context"
	"log/slog"
	"runtime"
	"sync"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/muxer"
)

var gb28181Logger = slog.Default().With("component", "gb28181-recorder")

type GB28181Recorder struct {
	cameraID string
	encoding string
	// Hub is set by camera.initStreamHub (same pattern as H264Recorder.Hub).
	Hub           *model.StreamHub
	mu            sync.Mutex
	status        model.RecorderStatus
	sps, pps, vps []byte
	codecType     string
	muxer         *muxer.MP4Muxer
	trackID       int
	curTempPath   string
	curFinalPath  string
	segStart      time.Time
	lastFrameTime time.Time
	frameCount    int
}

var (
	_ model.Recorder    = (*GB28181Recorder)(nil)
	_ model.HLSProvider = (*GB28181Recorder)(nil)
)

func NewGB28181Recorder(cameraID, encoding string, hub *model.StreamHub) *GB28181Recorder {
	return &GB28181Recorder{cameraID: cameraID, encoding: encoding, Hub: hub, status: model.StatusStopped}
}

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

func (r *GB28181Recorder) OnInvite() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.status = model.StatusRecording
}

func (r *GB28181Recorder) OnBye() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closeCurrentSegmentLocked()
	r.status = model.StatusReconnecting
}

func (r *GB28181Recorder) WriteNALU(au [][]byte, ptsTicks int64, isIDR bool) {
	defer func() {
		if panicErr := recover(); panicErr != nil {
			buf := make([]byte, 4096)
			buf = buf[:runtime.Stack(buf, false)]
			gb28181Logger.Error("PANIC recovered in WriteNALU", "camera_id", r.cameraID, "panic", panicErr, "stack", string(buf))
		}
	}()

	r.mu.Lock()
	if r.status != model.StatusRecording {
		r.status = model.StatusRecording
	}
	if r.codecType == "" && len(au) > 0 {
		r.codecType = detectCodec(au, r.encoding)
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
	if curMux == nil && !isIDR {
		r.mu.Unlock()
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
		newMux, newTrackID, tempPath, finalPath, err := createMuxer(r.cameraID, localCodecType, localSPS, localPPS, localVPS)
		if err != nil {
			gb28181Logger.Error("failed to add video track", "camera_id", r.cameraID, "codec", localCodecType, "error", err)
			r.mu.Unlock()
			return
		}
		now := time.Now()
		r.muxer = newMux
		r.trackID = newTrackID
		r.segStart = now
		r.curTempPath = tempPath
		r.curFinalPath = finalPath
		r.lastFrameTime = now
		r.frameCount = 0
		curMux = newMux
	}
	segStart := r.segStart
	lastFrame := r.lastFrameTime
	trackID := r.trackID
	r.mu.Unlock()

	if hub != nil {
		broadcastAU := prepareBroadcastAU(au, isIDR, localCodecType, localSPS, localPPS, localVPS)
		hub.Broadcast(ptsTicks, broadcastAU, isIDR)
	}

	vclNALU := findVCLNALU(au, localCodecType)
	if vclNALU == nil {
		return
	}

	now := time.Now()
	pts := now.Sub(segStart)
	dur := now.Sub(lastFrame)
	if dur < time.Millisecond {
		dur = time.Millisecond
	}
	if err := curMux.WriteSample(trackID, vclNALU, pts, dur); err != nil {
		gb28181Logger.Error("failed to write sample", "camera_id", r.cameraID, "error", err)
		return
	}

	r.mu.Lock()
	r.lastFrameTime = now
	r.frameCount++
	if time.Since(r.segStart) >= 10*time.Minute {
		r.closeCurrentSegmentLocked()
	}
	r.mu.Unlock()
}

func (r *GB28181Recorder) closeCurrentSegmentLocked() {
	closeMuxer(r.muxer, r.curTempPath, r.curFinalPath, r.cameraID)
	r.muxer = nil
	r.curTempPath = ""
	r.curFinalPath = ""
	r.frameCount = 0
}
