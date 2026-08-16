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
	// AudioEnabled gates the PS audio path: demuxed G.711/AAC frames are
	// muxed into MP4 segments and broadcast on the hub only when set
	// (mirrors the per-camera audio_enabled flag of the RTSP recorders).
	AudioEnabled bool
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

	// Audio state (PS audio demux, #340). The audio track is added lazily on
	// the first frame — GB28181 streams may start video-only and interleave
	// audio PES later.
	audioTrackID   int
	audioCodec     string // "g711a" | "g711u" | "aac"
	audioSampleRat int    // Hz (G.711: 8000, AAC: from ASC)
	audioChannels  int    // AAC only (G.711 is always 1)
	aacConfig      []byte // AudioSpecificConfig
	audioWarned    bool   // one-time warning for unusable AAC frames
	lastAudioPts   int64  // monotonic guard
	audioBytes     int64  // diagnostics
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

// maxClockJumpTicks bounds the plausible PTS advance between two consecutive
// AUs (5s @ 90kHz). A larger forward gap means the RTP clock switched to a
// new domain (upstream session recycle / source switch) — not slow motion.
const maxClockJumpTicks = int64(5 * 90000)

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
	// RTP clock-domain jump guard: an upstream session recycle (device, or a
	// cascaded lower platform re-INVITE) restarts the 90kHz clock at a new
	// arbitrary base. Mid-segment that arrives as a huge FORWARD gap;
	// trusting it inflates sample PTS — and merged MP4 durations — to days
	// (observed 41h). Close the segment at the discontinuity; the next IDR
	// re-anchors ptsBase in the new clock domain.
	if r.muxer != nil && ptsTicks-r.lastPtsTicks > maxClockJumpTicks {
		gb28181Logger.Warn("gb28181: RTP clock jumped — closing segment at discontinuity",
			"camera_id", r.cfg.CameraID,
			"gap", ticksToDuration(ptsTicks-r.lastPtsTicks))
		r.closeCurrentSegmentLocked()
		curMux = nil
	}
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

// WriteAudio ingests one demuxed audio frame from the PS stream.
// codec: "g711a" | "g711u" | "aac". config is the AAC AudioSpecificConfig
// (nil for G.711); samples is the frame's sample count for duration math.
// Frames are broadcast on the hub (live WS audio) and muxed into the open
// MP4 segment. A no-op when the camera has audio_enabled=false.
func (r *GB28181Recorder) WriteAudio(codec string, data, config []byte, ptsTicks int64, samples int) {
	if !r.cfg.AudioEnabled || len(data) == 0 {
		return
	}

	r.mu.Lock()
	if r.status == model.StatusStopped {
		r.mu.Unlock()
		return
	}
	if r.audioCodec == "" {
		r.audioCodec = codec
		r.audioSampleRat = 8000
		r.audioChannels = 1
		if codec == "aac" {
			r.aacConfig = append([]byte(nil), config...)
			if rate := ascSampleRate(config); rate > 0 {
				r.audioSampleRat = rate
			}
			if ch := ascChannels(config); ch > 0 {
				r.audioChannels = ch
			}
		}
	} else if r.audioCodec != codec {
		// Mid-stream codec switches are not remuxable — keep the first.
		r.mu.Unlock()
		return
	}
	if codec == "aac" && r.aacConfig == nil {
		// Raw AAC without a derivable ASC cannot be muxed or decoded.
		if !r.audioWarned {
			r.audioWarned = true
			gb28181Logger.Warn("gb28181: AAC frames carry no ADTS — cannot derive config; audio dropped",
				"camera_id", r.cfg.CameraID)
		}
		r.mu.Unlock()
		return
	}
	hub, curMux, audioTrack := r.Hub, r.muxer, r.audioTrackID
	if ptsTicks <= r.lastAudioPts {
		ptsTicks = r.lastAudioPts + 1
	}
	r.lastAudioPts = ptsTicks
	r.audioBytes += int64(len(data))
	r.mu.Unlock()

	if hub != nil {
		switch codec {
		case "g711a", "g711u":
			hub.BroadcastAudio(ptsTicks, model.AudioG711, data)
		case "aac":
			hub.BroadcastAudio(ptsTicks, model.AudioAAC, data)
		}
	}

	if curMux == nil {
		return // no open video segment — audio before first IDR is dropped
	}

	r.mu.Lock()
	if r.audioTrackID == 0 {
		muxCodec := "g711"
		var trackConfig []byte
		if codec == "aac" {
			muxCodec = "aac"
			trackConfig = r.aacConfig
		} else {
			// Muxer G.711 config: [0]=μ-law flag (0=A-law, 1=μ-law), [1:5]=rate BE.
			muLaw := byte(0)
			if codec == "g711u" {
				muLaw = 1
			}
			trackConfig = []byte{muLaw, 0, 0, 0x1F, 0x40} // 8000 Hz
		}
		tid, err := curMux.AddAudioTrack(muxCodec, trackConfig)
		if err != nil {
			r.mu.Unlock()
			gb28181Logger.Error("failed to add audio track", "camera_id", r.cfg.CameraID, "codec", codec, "error", err)
			return
		}
		r.audioTrackID = tid
		audioTrack = tid
	}
	ptsBase := r.ptsBase
	r.mu.Unlock()

	rel := ptsTicks - ptsBase
	if rel < 0 {
		rel = 0
	}
	rate := int64(8000)
	if codec == "aac" && r.audioSampleRat > 0 {
		rate = int64(r.audioSampleRat)
	}
	if samples <= 0 {
		samples = 1
	}
	dur := time.Duration(samples) * time.Second / time.Duration(rate)
	if dur < time.Millisecond {
		dur = time.Millisecond
	}
	if err := curMux.WriteAudioSample(audioTrack, data, ticksToDuration(rel), dur); err != nil &&
		err.Error() != "muxer is closed" {
		gb28181Logger.Error("failed to write audio sample", "camera_id", r.cfg.CameraID, "error", err)
	}
}

// AudioCodec implements the audioInfoProvider interface consumed by the WS
// streaming layer (handlers_ws.go). Returns "" until the first audio frame.
func (r *GB28181Recorder) AudioCodec() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	switch r.audioCodec {
	case "g711a", "g711u":
		return "g711"
	case "aac":
		return "aac"
	default:
		return ""
	}
}

// AudioSampleRate implements audioInfoProvider.
func (r *GB28181Recorder) AudioSampleRate() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.audioSampleRat == 0 {
		return 8000
	}
	return r.audioSampleRat
}

// AudioChannels implements audioInfoProvider.
func (r *GB28181Recorder) AudioChannels() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.audioChannels == 0 {
		return 1
	}
	return r.audioChannels
}

// AudioConfig implements audioInfoProvider. G.711: 1-byte μ-law flag + 4-byte
// rate (muxer convention); AAC: the AudioSpecificConfig.
func (r *GB28181Recorder) AudioConfig() []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	switch r.audioCodec {
	case "g711u":
		return []byte{1, 0, 0, 0x1F, 0x40}
	case "g711a":
		return []byte{0, 0, 0, 0x1F, 0x40}
	case "aac":
		return r.aacConfig
	default:
		return nil
	}
}

// ascSampleRate reads the sampling frequency index from a 2-byte ASC.
func ascSampleRate(asc []byte) int {
	if len(asc) < 2 {
		return 0
	}
	// ASC: 5 bits AOT | 4 bits freqIdx | 4 bits chans (spans both bytes)
	idx := (asc[0] >> 1) & 0x0F
	switch idx {
	case 0:
		return 96000
	case 1:
		return 88200
	case 2:
		return 64000
	case 3:
		return 48000
	case 4:
		return 44100
	case 5:
		return 32000
	case 6:
		return 24000
	case 7:
		return 22050
	case 8:
		return 16000
	case 9:
		return 12000
	case 10:
		return 11025
	case 11:
		return 8000
	case 12:
		return 7350
	default:
		return 0
	}
}

// ascChannels reads the channel configuration from a 2-byte ASC.
func ascChannels(asc []byte) int {
	if len(asc) < 2 {
		return 0
	}
	return int(asc[1] >> 3 & 0x0F)
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
	r.audioTrackID = 0 // the next segment's muxer needs a fresh audio track
}
