package recorder

import (
	"context"
	"log/slog"
	"os"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/event"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/metrics"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model/nalutil"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/muxer"
)

var ingestLogger = slog.Default().With("component", "ingest-recorder")

// IngestConfig holds configuration for the IngestRecorder.
//
// Unlike the pull recorders (H264/H265/ONVIF), the IngestRecorder does not dial
// out to a source. Frames arrive via WriteNALU(), called from the SRT listener
// and RTMP server callbacks. It therefore has no URL/credentials — those are
// irrelevant because the publisher connects to us.
type IngestConfig struct {
	CameraID   string
	Encoding   string // "h264" or "h265" (RTMP H.265 via enhanced-RTMP hvc1; SRT H.265 demux is a follow-up)
	SegmentDur time.Duration

	Store SegmentStore // satisfies *storage.Manager
	DB    RecordingDB
	// Metrics, EventBus optional (nil-safe)
	Metrics  *metrics.Metrics
	EventBus *event.EventBus
	// RecordEnabled gates whether pushed frames are written to disk as segments.
	// nil or true (default) = record normally. false = "live-only" mode: the
	// recorder keeps accepting publishers and the StreamHub fan-out keeps feeding
	// live preview (HLS/WebRTC/FLV/WS) and relay, but NO segments are written —
	// useful when the NVR is used purely as a live/relay gateway and disk writes
	// must be avoided. Mirrors RecordEnabled on the pull recorders (base.go).
	RecordEnabled *bool
}

// IngestRecorder records H.264/H.265 video pushed into the NVR via SRT/RTMP ingest.
//
// Lifecycle: Start() enters the Idle (waiting) state — no network activity.
// When a publisher connects, the ingest server calls WriteConnected(). Each
// incoming access unit is delivered via WriteNALU(), which fans the frames out
// to the StreamHub (live HLS/WebRTC/FLV/WS) AND writes rolling MP4 segments to
// disk (recordings). When the publisher disconnects, OnDisconnect() closes the
// in-flight segment and returns to Idle, ready for the next publisher.
//
// It implements model.Recorder so it slots into the existing CameraManager /
// HLS / WebRTC / FLV / WS pipeline unchanged.
type IngestRecorder struct {
	cfg IngestConfig

	mu     sync.Mutex
	status model.RecorderStatus

	// Hub is set by camera.initStreamHub (same pattern as H264Recorder.Hub).
	Hub *model.StreamHub

	// auAsm regroups delivered NALUs into picture-complete AUs before fan-out
	// (push publishers disagree on delivery granularity; see AUAssembler).
	auAsm *nalutil.AUAssembler

	// connected reflects whether a publisher is currently pushing frames.
	// Drives the Idle ↔ Recording status transitions.
	connected atomic.Bool

	cancel context.CancelFunc
	done   chan struct{}

	// Rolling MP4 segment state (guarded by mu). Mirrors H264Recorder's
	// writeFrames/closeCurrentSegment logic but driven synchronously by
	// WriteNALU callbacks instead of a frameCh goroutine.
	muxer      *muxer.MP4Muxer
	trackID    int
	sps, pps   []byte
	vps        []byte // H.265 only (nil for H.264 sources)
	curTemp    string
	curFinal   string
	segStart   time.Time
	lastFrame  time.Time
	frameCount int

	// Audio (WHIP push-in, #369). The negotiated format is set via SetAudioFormat
	// when the publisher's audio track is accepted; frames then arrive via
	// WriteAudio. audioTrackID is bound when a segment muxer is created (Opus
	// track added alongside H.264).
	audioCodec    string // "opus" or "" (no audio)
	audioSampleHz int
	audioChans    int
	audioTrackID  int
}

var _ model.Recorder = (*IngestRecorder)(nil)

// isH265 reports whether this recorder was configured for H.265 push ingest.
func (r *IngestRecorder) isH265() bool { return r.cfg.Encoding == "h265" }

// format is the recording format label derived from the configured encoding.
func (r *IngestRecorder) format() model.Format {
	if r.isH265() {
		return model.FormatH265
	}
	return model.FormatH264
}

// NewIngestRecorder constructs an IngestRecorder. The Hub is injected later by
// camera.initStreamHub (consistent with every other recorder).
func NewIngestRecorder(cfg IngestConfig) *IngestRecorder {
	if cfg.SegmentDur <= 0 {
		cfg.SegmentDur = DefaultSegmentDur
	}
	return &IngestRecorder{
		cfg:   cfg,
		auAsm: nalutil.NewAUAssembler(cfg.Encoding == "h265"),
	}
}

// GetHub returns the StreamHub for frame fan-out (satisfies the hubber interface
// used by getRecorderHub across the API layer).
func (r *IngestRecorder) GetHub() *model.StreamHub { return r.Hub }

// SetHub wires the StreamHub for frame fan-out (model.HubHost).
func (r *IngestRecorder) SetHub(hub *model.StreamHub) { r.Hub = hub }

// HubSource labels the hub for the flow-path observability view.
func (r *IngestRecorder) HubSource() string { return "ingest" }

// VPS returns the most recently captured H.265 VPS NAL unit (without start
// code). Always nil for H.264 sources. Thread-safe.
func (r *IngestRecorder) VPS() []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.vps
}

// SPS returns the most recently captured H.264 SPS NAL unit (without start code).
// nil until the publisher has sent a keyframe with param sets. Thread-safe.
func (r *IngestRecorder) SPS() []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.sps
}

// PPS returns the most recently captured H.264 PPS NAL unit (without start code).
// nil until the publisher has sent a keyframe with param sets. Thread-safe.
func (r *IngestRecorder) PPS() []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.pps
}

// CodecParams implements model.HLSProvider so the HLS handler can initialize a
// stream from the parameter sets captured during ingest (push cameras). Returns
// the configured format with the current SPS/PPS (+VPS for H.265). Returns nil
// params before the publisher's first keyframe arrives.
func (r *IngestRecorder) CodecParams() (codec model.Format, sps, pps, vps []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.isH265() {
		return model.FormatH265, r.sps, r.pps, r.vps
	}
	return model.FormatH264, r.sps, r.pps, nil
}

// AudioCodec returns the negotiated audio codec ("opus") or "" when the push
// publisher has no audio track. Set via SetAudioFormat (#369).
func (r *IngestRecorder) AudioCodec() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.audioCodec
}

// AudioConfig returns audio config bytes. Always nil for ingest — Opus needs
// no out-of-band config (the browser decoder self-configures from the stream).
func (r *IngestRecorder) AudioConfig() []byte { return nil }

// AudioSampleRate returns the negotiated audio sample rate (48000 for Opus).
func (r *IngestRecorder) AudioSampleRate() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.audioSampleHz
}

// AudioChannels returns the negotiated audio channel count.
func (r *IngestRecorder) AudioChannels() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.audioChans
}

// SetAudioFormat records the audio format negotiated by a push publisher
// (WHIP OnTrack). Call before frames arrive; later calls are ignored so a
// renegotiation mid-stream can't swap the muxer's track format. Only "opus"
// is supported — G.711 push-in has no producer today.
func (r *IngestRecorder) SetAudioFormat(codec string, sampleRate, channels int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.audioCodec != "" || codec != "opus" {
		return
	}
	if sampleRate <= 0 {
		sampleRate = 48000
	}
	if channels <= 0 {
		channels = 2
	}
	r.audioCodec = codec
	r.audioSampleHz = sampleRate
	r.audioChans = channels
	ingestLogger.Info("ingest audio configured",
		"camera_id", r.cfg.CameraID, "codec", codec, "sample_rate", sampleRate, "channels", channels)
}

// WriteAudio ingests one raw audio frame from a push publisher (#369).
// ptsTicks is on the audio RTP clock (48 kHz for Opus) — used for the hub
// fan-out so live consumers pace themselves; the MP4 sample uses wall-clock
// PTS like the video path. dur is the frame duration (caller-derived from
// RTP timestamps; 20ms fallback when unset).
func (r *IngestRecorder) WriteAudio(codec string, ptsTicks int64, data []byte, dur time.Duration) {
	defer func() {
		if panicErr := recover(); panicErr != nil {
			ingestLogger.Error("PANIC recovered in WriteAudio",
				"camera_id", r.cfg.CameraID, "panic", panicErr)
		}
	}()
	if codec != "opus" || len(data) == 0 {
		return
	}
	if dur < time.Millisecond {
		dur = 20 * time.Millisecond
	}

	r.mu.Lock()
	hub := r.Hub
	m := r.muxer
	aid := r.audioTrackID
	start := r.segStart
	r.mu.Unlock()

	// Live fan-out (outside lock, hub is non-blocking by design).
	if hub != nil {
		hub.BroadcastAudio(ptsTicks, model.AudioOpus, data)
	}

	// Live-only mode: skip all segment I/O (mirrors the WriteNALU gate).
	if r.cfg.RecordEnabled != nil && !*r.cfg.RecordEnabled {
		return
	}

	if m != nil && aid > 0 {
		pts := time.Since(start)
		if err := m.WriteAudioSample(aid, data, pts, dur); err != nil {
			if err.Error() != "muxer is closed" {
				ingestLogger.Error("failed to write audio sample",
					"camera_id", r.cfg.CameraID, "error", err)
			}
		}
	}
}

// Start initializes the recorder into the Idle state (awaiting a publisher).
// It does NOT dial any source — unlike the pull recorders, there is nothing to
// connect to here. Returns immediately.
func (r *IngestRecorder) Start(_ context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.status == model.StatusRecording {
		return nil
	}
	_, r.cancel = context.WithCancel(context.Background())
	r.done = make(chan struct{})
	// Idle = "waiting for publisher". We model this as StatusReconnecting so
	// the existing health/status UI shows the camera as not-yet-streaming
	// without introducing a new status constant. The transition to
	// StatusRecording happens on the first WriteNALU.
	r.status = model.StatusReconnecting
	close(r.done) // no long-running goroutine; closed to keep Stop() idempotent
	ingestLogger.Info("ingest recorder ready, awaiting publisher",
		"camera_id", r.cfg.CameraID, "encoding", r.cfg.Encoding)
	return nil
}

// Stop closes any in-flight segment and marks the recorder stopped.
func (r *IngestRecorder) Stop() error {
	// Emit the pending picture before closing the segment (the assembler holds
	// it back until the next picture starts, which will never come).
	r.auAsm.Flush(r.writeAssembledAU)
	r.mu.Lock()
	if r.cancel != nil {
		r.cancel()
		r.cancel = nil
	}
	// Close the active segment (if any) before reporting stopped.
	r.closeCurrentSegmentLocked()
	r.status = model.StatusStopped
	r.mu.Unlock()
	ingestLogger.Info("ingest recorder stopped", "camera_id", r.cfg.CameraID)
	return nil
}

// Status returns the current recorder status.
func (r *IngestRecorder) Status() model.RecorderStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.status
}

// WriteConnected signals that a publisher has connected and is about to stream.
// Called by the SRT listener / RTMP server on publisher connect.
func (r *IngestRecorder) WriteConnected() {
	r.connected.Store(true)
}

// WriteNALU ingests H.264 NAL units (WITHOUT start codes) delivered by an
// ingest server (SRT tsdemux / RTMP reader / WHIP track handler).
//
// Delivery granularity varies by publisher: FFmpeg sends picture-complete AUs
// per message, while restreamer-style publishers emit one NAL unit per message
// — splitting multi-slice pictures (libx264 sliced-threads) into fragments no
// decoder can decode. An AUAssembler regroups everything into picture-complete
// AUs before fan-out, so the isIDR hint from the transport is ignored and IDR
// is recomputed on the assembled picture.
//
// ptsTicks is a 90 kHz clock value (matching the RTP/StreamHub convention used
// by the pull recorders).
//
// It performs three jobs:
//  1. Broadcasts the assembled AU to the StreamHub for live consumers
//     (HLS/WebRTC/FLV/WS).
//  2. Captures SPS/PPS and rolls the MP4 segment when they change.
//  3. Writes VCL NALUs (types 1, 5) to the rolling MP4 segment for recordings.
func (r *IngestRecorder) WriteNALU(au [][]byte, ptsTicks int64, _ bool) {
	defer func() {
		if panicErr := recover(); panicErr != nil {
			buf := make([]byte, 4096)
			buf = buf[:runtime.Stack(buf, false)]
			ingestLogger.Error("PANIC recovered in WriteNALU",
				"camera_id", r.cfg.CameraID, "panic", panicErr, "stack", string(buf))
		}
	}()

	// Assembly runs first so a completing picture is written under the PREVIOUS
	// codec parameters; the incoming AU's parameter sets are cached afterwards
	// and rotate the recording segment before the next picture opens one.
	r.auAsm.Add(au, ptsTicks, r.writeAssembledAU)
	r.cacheParamSets(au)
}

// cacheParamSets extracts parameter sets from an incoming delivery (any
// granularity) and rotates the recording segment when they change. Runs on
// every WriteNALU call so out-of-band parameter-set deliveries (the RTMP
// sequence-header feed, which carries no VCL NALU and is never emitted by the
// assembler) are still captured in time for HLS/FLV/WebRTC initialization.
// H.265 sources additionally cache the VPS (required by the hvc1/hvcC track
// and by consumers that expect the full VPS/SPS/PPS triple in-band).
func (r *IngestRecorder) cacheParamSets(au [][]byte) {
	if r.isH265() {
		vps, sps, pps := nalutil.ExtractParamSetsH265(au)
		r.mu.Lock()
		defer r.mu.Unlock()
		if vps != nil {
			if r.vps != nil && !nalutil.EqualParamSets(r.vps, vps) {
				ingestLogger.Info("VPS change detected, rotating segment", "camera_id", r.cfg.CameraID)
				r.closeCurrentSegmentLocked()
			}
			r.vps = append([]byte(nil), vps...)
		}
		if sps != nil {
			if r.sps != nil && !nalutil.EqualParamSets(r.sps, sps) {
				ingestLogger.Info("SPS change detected, rotating segment", "camera_id", r.cfg.CameraID)
				r.closeCurrentSegmentLocked()
			}
			r.sps = append([]byte(nil), sps...)
		}
		if pps != nil {
			if r.pps != nil && !nalutil.EqualParamSets(r.pps, pps) {
				ingestLogger.Info("PPS change detected, rotating segment", "camera_id", r.cfg.CameraID)
				r.closeCurrentSegmentLocked()
			}
			r.pps = append([]byte(nil), pps...)
		}
		return
	}
	sps, pps := nalutil.ExtractParamSetsH264(au)
	r.mu.Lock()
	defer r.mu.Unlock()
	if sps != nil {
		if r.sps != nil && !nalutil.EqualParamSets(r.sps, sps) {
			ingestLogger.Info("SPS change detected, rotating segment", "camera_id", r.cfg.CameraID)
			r.closeCurrentSegmentLocked()
		}
		r.sps = append([]byte(nil), sps...)
	}
	if pps != nil {
		if r.pps != nil && !nalutil.EqualParamSets(r.pps, pps) {
			ingestLogger.Info("PPS change detected, rotating segment", "camera_id", r.cfg.CameraID)
			r.closeCurrentSegmentLocked()
		}
		r.pps = append([]byte(nil), pps...)
	}
}

// writeAssembledAU fans one picture-complete AU out to the StreamHub and the
// rolling segment. Called synchronously by the AUAssembler.
func (r *IngestRecorder) writeAssembledAU(au [][]byte, ptsTicks int64) {
	defer func() {
		if panicErr := recover(); panicErr != nil {
			buf := make([]byte, 4096)
			buf = buf[:runtime.Stack(buf, false)]
			ingestLogger.Error("PANIC recovered in writeAssembledAU",
				"camera_id", r.cfg.CameraID, "panic", panicErr, "stack", string(buf))
		}
	}()

	isIDR := nalutil.IsIDR(au, r.isH265())

	// ---- Lock scope 1: status transition, shared-state snapshot ----
	r.mu.Lock()
	if r.status != model.StatusRecording {
		r.status = model.StatusRecording
		r.incActive()
		ingestLogger.Info("ingest publisher streaming", "camera_id", r.cfg.CameraID)
	}

	// Copy shared state to locals while holding lock.
	hub := r.Hub
	localSPS := r.sps
	localPPS := r.pps
	localVPS := r.vps
	r.mu.Unlock()

	// ---- Hub broadcast (outside lock, non-blocking by design) ----
	// Downstream consumers need param sets in-band on keyframes — notably
	// gohlslib's DTS extractor scans the AU for an SPS NAL and fails with
	// "SPS not received yet" otherwise. Match the RTSP path by prepending the
	// cached param sets to IDR AUs that don't already carry them inline
	// (restreamer publishers inline them; FFmpeg delivers them out-of-band
	// only, via the sequence-header feed cached in cacheParamSets).
	if hub != nil {
		broadcastAU := au
		if isIDR && localSPS != nil && localPPS != nil {
			if r.isH265() {
				// H.265 consumers need the full VPS/SPS/PPS triple in-band.
				inVPS, inSPS, inPPS := nalutil.ExtractParamSetsH265(au)
				if inVPS == nil || inSPS == nil || inPPS == nil {
					broadcastAU = make([][]byte, 0, len(au)+3)
					broadcastAU = append(broadcastAU, localVPS, localSPS, localPPS)
					broadcastAU = append(broadcastAU, au...)
				}
			} else {
				inSPS, inPPS := nalutil.ExtractParamSetsH264(au)
				if inSPS == nil || inPPS == nil {
					broadcastAU = make([][]byte, 0, len(au)+2)
					broadcastAU = append(broadcastAU, localSPS, localPPS)
					broadcastAU = append(broadcastAU, au...)
				}
			}
		}
		hub.Broadcast(ptsTicks, broadcastAU, isIDR)
	}

	// Live-only mode: the StreamHub fan-out above already delivered this frame to
	// live preview (HLS/WebRTC/FLV/WS) and relay, and the cached SPS/PPS keep
	// getCodecParams() populated. Skip all segment I/O from here on so nothing is
	// written to disk. Mirrors the gate in base.go writeFrames.
	// nil => recording enabled (default); pointer to false => live-only.
	if r.cfg.RecordEnabled != nil && !*r.cfg.RecordEnabled {
		return
	}

	// ---- Find VCL NALU to write to disk ----
	// H.264: type 1 (non-IDR) or 5 (IDR). H.265: any VCL type (0-31, IDR/WPP
	// slices are 16-21 — the AU carries one VCL NALU per picture here).
	var vclNALU []byte
	for _, nalu := range au {
		if len(nalu) == 0 {
			continue
		}
		if r.isH265() {
			if (nalu[0]>>1)&0x3F < 32 {
				vclNALU = nalu
				break
			}
		} else {
			naluType := nalu[0] & 0x1F
			if naluType == 5 || naluType == 1 {
				vclNALU = nalu
				break
			}
		}
	}
	if vclNALU == nil {
		return
	}

	// ---- Storage health check (lock only for muxer cleanup) ----
	if isStorageFailed(r.cfg.Store, r.cfg.CameraID) {
		r.mu.Lock()
		if r.muxer != nil {
			r.muxer.Close()
			if r.curTemp != "" {
				os.Remove(r.curTemp)
			}
			r.muxer = nil
			r.curTemp = ""
			r.curFinal = ""
			r.frameCount = 0
		}
		r.mu.Unlock()
		return
	}

	if localSPS == nil || localPPS == nil || (r.isH265() && localVPS == nil) {
		return
	}

	// ---- Ensure segment muxer is open ----
	r.mu.Lock()
	curMux := r.muxer
	if curMux == nil && !isIDR {
		r.mu.Unlock()
		return
	}
	r.mu.Unlock()

	if curMux == nil {
		format := r.format()
		tempPath, finalPath, err := r.cfg.Store.CreateSegment(r.cfg.CameraID, string(format))
		if err != nil {
			ingestLogger.Error("failed to create segment", "camera_id", r.cfg.CameraID, "error", err)
			return
		}
		newMux := muxer.NewMP4Muxer(tempPath)
		var newTrackID int
		if r.isH265() {
			newTrackID, err = newMux.AddH265Track(localVPS, localSPS, localPPS)
			if err != nil {
				ingestLogger.Error("failed to add H265 track", "camera_id", r.cfg.CameraID, "error", err)
				os.Remove(tempPath)
				return
			}
		} else {
			newTrackID, err = newMux.AddH264Track(localSPS, localPPS)
			if err != nil {
				ingestLogger.Error("failed to add H264 track", "camera_id", r.cfg.CameraID, "error", err)
				os.Remove(tempPath)
				return
			}
		}
		// Opus audio track (#369): config = 1 byte channels + 2 bytes PreSkip +
		// 4 bytes InputSampleRate (big-endian) — see muxer.AddAudioTrack.
		r.mu.Lock()
		var newAudioTrackID int
		if r.audioCodec == "opus" {
			opusCfg := []byte{
				byte(min(r.audioChans, 2)), 0, 0,
				byte(r.audioSampleHz >> 24), byte(r.audioSampleHz >> 16), byte(r.audioSampleHz >> 8), byte(r.audioSampleHz),
			}
			aid, aerr := newMux.AddAudioTrack("opus", opusCfg)
			if aerr != nil {
				ingestLogger.Warn("failed to add opus track — recording video-only",
					"camera_id", r.cfg.CameraID, "error", aerr)
			} else {
				newAudioTrackID = aid
			}
		}
		r.mu.Unlock()
		now := time.Now()
		r.mu.Lock()
		r.muxer = newMux
		r.trackID = newTrackID
		if newAudioTrackID > 0 {
			r.audioTrackID = newAudioTrackID
		}
		r.segStart = now
		r.curTemp = tempPath
		r.curFinal = finalPath
		r.lastFrame = now
		r.frameCount = 0
		curMux = newMux
		r.mu.Unlock()
	}

	// ---- Read write parameters under lock ----
	r.mu.Lock()
	segStart := r.segStart
	lastFrame := r.lastFrame
	trackID := r.trackID
	r.mu.Unlock()

	// ---- Write sample (muxer has its own mutex) ----
	now := time.Now()
	pts := now.Sub(segStart)
	dur := now.Sub(lastFrame)
	if dur < time.Millisecond {
		dur = time.Millisecond
	}

	r.mu.Lock()
	r.lastFrame = now
	r.mu.Unlock()

	if err := curMux.WriteSample(trackID, vclNALU, pts, dur); err != nil {
		ingestLogger.Error("failed to write sample", "camera_id", r.cfg.CameraID, "error", err)
		return
	}

	r.mu.Lock()
	r.frameCount++
	// Duration-based segment rollover.
	if time.Since(r.segStart) >= r.cfg.SegmentDur {
		r.closeCurrentSegmentLocked()
	}
	r.mu.Unlock()
}

// OnDisconnect is called by the ingest server when the publisher disconnects.
// It flushes the in-flight segment and returns the recorder to Idle, ready to
// accept the next publisher without being restarted.
func (r *IngestRecorder) OnDisconnect() {
	r.connected.Store(false)
	// Emit the pending picture before flushing the segment (see Stop).
	r.auAsm.Flush(r.writeAssembledAU)
	r.mu.Lock()
	wasRecording := r.status == model.StatusRecording
	r.closeCurrentSegmentLocked()
	if wasRecording {
		r.status = model.StatusReconnecting // Idle / awaiting next publisher
		r.decActive()
		ingestLogger.Info("ingest publisher disconnected, awaiting reconnect",
			"camera_id", r.cfg.CameraID)
	}
	r.mu.Unlock()
}

// closeCurrentSegmentLocked flushes the active MP4 segment to disk, inserts the
// Recording DB row, publishes the SegmentCompleted event, and updates metrics.
// Caller must hold r.mu. No-op if no segment is open. Mirrors
// H264Recorder.closeCurrentSegment.
func (r *IngestRecorder) closeCurrentSegmentLocked() {
	if r.muxer == nil {
		return
	}
	if err := r.muxer.Close(); err != nil {
		ingestLogger.Error("failed to close muxer", "camera_id", r.cfg.CameraID, "error", err)
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
			ingestLogger.Error("failed to close segment", "camera_id", r.cfg.CameraID, "error", err)
		}
	}

	// Insert recording entry into the database.
	var fileSize int64
	var recordingID string
	format := r.format()
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
			ingestLogger.Error("failed to insert recording", "camera_id", r.cfg.CameraID, "error", err)
		}
	}

	// Publish SegmentCompleted event.
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
	if r.frameCount > 0 && r.curFinal != "" {
		r.recordSegmentCreated()
		if fileSize > 0 {
			r.recordBytes(fileSize)
		}
	}

	r.muxer = nil
	r.curTemp = ""
	r.curFinal = ""
	r.frameCount = 0
	r.audioTrackID = 0
}

// --- metrics helpers (nil-safe, mirror H264Recorder) ---

func (r *IngestRecorder) incActive() {
	if r.cfg.Metrics != nil {
		r.cfg.Metrics.ActiveRecordings.Inc()
		r.cfg.Metrics.ActiveCameras.Inc()
	}
}

func (r *IngestRecorder) decActive() {
	if r.cfg.Metrics != nil {
		r.cfg.Metrics.ActiveRecordings.Dec()
		r.cfg.Metrics.ActiveCameras.Dec()
	}
}

func (r *IngestRecorder) recordSegmentCreated() {
	if r.cfg.Metrics != nil {
		r.cfg.Metrics.SegmentsCreated.WithLabelValues(r.cfg.CameraID, string(r.format())).Inc()
	}
}

func (r *IngestRecorder) recordBytes(bytes int64) {
	if r.cfg.Metrics != nil {
		r.cfg.Metrics.RecordingBytesTotal.WithLabelValues(r.cfg.CameraID, string(r.format())).Add(float64(bytes))
	}
}
