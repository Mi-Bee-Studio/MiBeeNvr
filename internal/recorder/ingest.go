package recorder

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"runtime"
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
	Encoding   string // "h264" (H.265 over SRT is a follow-up; RTMP is H.264 only)
	SegmentDur time.Duration

	Store SegmentStore // satisfies *storage.Manager
	DB    RecordingDB
	// Metrics, EventBus optional (nil-safe)
	Metrics  *metrics.Metrics
	EventBus *event.EventBus
}

// IngestRecorder records H.264 video pushed into the NVR via SRT/RTMP ingest.
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
	curTemp    string
	curFinal   string
	segStart   time.Time
	lastFrame  time.Time
	frameCount int
}

var _ model.Recorder = (*IngestRecorder)(nil)

// NewIngestRecorder constructs an IngestRecorder. The Hub is injected later by
// camera.initStreamHub (consistent with every other recorder).
func NewIngestRecorder(cfg IngestConfig) *IngestRecorder {
	if cfg.SegmentDur <= 0 {
		cfg.SegmentDur = DefaultSegmentDur
	}
	return &IngestRecorder{cfg: cfg}
}

// GetHub returns the StreamHub for frame fan-out (satisfies the hubber interface
// used by getRecorderHub across the API layer).
func (r *IngestRecorder) GetHub() *model.StreamHub { return r.Hub }

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
// stream from the SPS/PPS captured during ingest (push cameras). Returns H.264
// with the current SPS/PPS (vps is nil for H.264). Returns nil params before the
// publisher's first keyframe arrives.
func (r *IngestRecorder) CodecParams() (codec model.Format, sps, pps, vps []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return model.FormatH264, r.sps, r.pps, nil
}

// AudioCodec returns the audio codec name. IngestRecorder does not currently
// support audio; returns empty string.
func (r *IngestRecorder) AudioCodec() string { return "" }

// AudioConfig returns audio config bytes. Always nil for ingest (no audio).
func (r *IngestRecorder) AudioConfig() []byte { return nil }

// AudioSampleRate returns the audio sample rate. Always 0 for ingest (no audio).
func (r *IngestRecorder) AudioSampleRate() int { return 0 }

// AudioChannels returns the number of audio channels. Always 0 for ingest (no audio).
func (r *IngestRecorder) AudioChannels() int { return 0 }

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

// WriteNALU ingests one H.264 access unit (slice of NAL units, WITHOUT start
// codes) delivered by an ingest server (SRT tsdemux / RTMP reader).
//
// ptsTicks is a 90 kHz clock value (matching the RTP/StreamHub convention used
// by the pull recorders). isIDR indicates the AU contains a keyframe.
//
// It performs three jobs:
//  1. Broadcasts the AU to the StreamHub for live consumers (HLS/WebRTC/FLV/WS).
//  2. Captures SPS/PPS and rolls the MP4 segment when they change.
//  3. Writes VCL NALUs (types 1, 5) to the rolling MP4 segment for recordings.
func (r *IngestRecorder) WriteNALU(au [][]byte, ptsTicks int64, isIDR bool) {
	defer func() {
		if panicErr := recover(); panicErr != nil {
			buf := make([]byte, 4096)
			buf = buf[:runtime.Stack(buf, false)]
			ingestLogger.Error("PANIC recovered in WriteNALU",
				"camera_id", r.cfg.CameraID, "panic", panicErr, "stack", string(buf))
		}
	}()

	r.mu.Lock()
	defer r.mu.Unlock()

	// Transition Idle → Recording on first frame.
	if r.status != model.StatusRecording {
		r.status = model.StatusRecording
		r.incActive()
		ingestLogger.Info("ingest publisher streaming", "camera_id", r.cfg.CameraID)
	}

	// Capture SPS/PPS; roll the segment if they changed (avcC must be
	// self-consistent within a segment — same rule as H264Recorder).
	sps, pps := nalutil.ExtractParamSetsH264(au)
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

	// 1. Live fan-out to the StreamHub (non-blocking; hub drops on full buffer).
	// RTMP/SRT publishers send SPS/PPS out-of-band (RTMP sequence header), so a
	// keyframe AU typically does NOT contain the param sets that downstream
	// consumers need — notably gohlslib's DTS extractor scans the AU for an SPS
	// NAL and fails with "SPS not received yet" otherwise. Match the RTSP path
	// (which inlines SPS/PPS in every IDR AU) by ALWAYS prepending the cached
	// param sets to IDR frames (idempotent if the AU already has them).
	if r.Hub != nil {
		broadcastAU := au
		if isIDR && r.sps != nil && r.pps != nil {
			broadcastAU = make([][]byte, 0, len(au)+2)
			broadcastAU = append(broadcastAU, r.sps, r.pps)
			broadcastAU = append(broadcastAU, au...)
		}
		r.Hub.Broadcast(ptsTicks, broadcastAU, isIDR)
	}

	// 3. Find the VCL NALU (type 1 non-IDR or type 5 IDR) to write to disk.
	var vclNALU []byte
	for _, nalu := range au {
		if len(nalu) == 0 {
			continue
		}
		naluType := nalu[0] & 0x1F
		if naluType == 5 || naluType == 1 {
			vclNALU = nalu
			break
		}
	}
	if vclNALU == nil {
		return
	}

	// Skip recording when storage is unhealthy (keep the live stream alive).
	if isStorageFailed(r.cfg.Store) {
		if r.muxer != nil {
			r.muxer.Close()
			os.Remove(r.curTemp)
			r.muxer = nil
			r.curTemp = ""
			r.curFinal = ""
			r.frameCount = 0
		}
		return
	}

	if r.sps == nil || r.pps == nil {
		return
	}
	// Wait for an IDR before opening a new segment (prevents P-frame-first
	// segments that render as black until the next keyframe).
	if r.muxer == nil && !isIDR {
		return
	}

	// Open a new segment on the first (IDR) frame.
	if r.muxer == nil {
		tempPath, finalPath, err := r.cfg.Store.CreateSegment(r.cfg.CameraID, string(model.FormatH264))
		if err != nil {
			ingestLogger.Error("failed to create segment", "camera_id", r.cfg.CameraID, "error", err)
			return
		}
		m := muxer.NewMP4Muxer(tempPath)
		trackID, err := m.AddH264Track(r.sps, r.pps)
		if err != nil {
			ingestLogger.Error("failed to add H264 track", "camera_id", r.cfg.CameraID, "error", err)
			os.Remove(tempPath)
			return
		}
		r.muxer = m
		r.trackID = trackID
		r.segStart = time.Now()
		r.curTemp = tempPath
		r.curFinal = finalPath
		r.lastFrame = r.segStart
		r.frameCount = 0
	}

	now := time.Now()
	pts := now.Sub(r.segStart)
	dur := now.Sub(r.lastFrame)
	if dur < time.Millisecond {
		dur = time.Millisecond
	}
	r.lastFrame = now
	if err := r.muxer.WriteSample(r.trackID, vclNALU, pts, dur); err != nil {
		ingestLogger.Error("failed to write sample", "camera_id", r.cfg.CameraID, "error", err)
		return
	}
	r.frameCount++

	// Duration-based segment rollover.
	if time.Since(r.segStart) >= r.cfg.SegmentDur {
		r.closeCurrentSegmentLocked()
	}
}

// OnDisconnect is called by the ingest server when the publisher disconnects.
// It flushes the in-flight segment and returns the recorder to Idle, ready to
// accept the next publisher without being restarted.
func (r *IngestRecorder) OnDisconnect() {
	r.connected.Store(false)
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
	if r.cfg.DB != nil && r.curFinal != "" {
		now := time.Now()
		duration := now.Sub(r.segStart).Seconds()
		rec := &model.Recording{
			ID:         fmt.Sprintf("%d", now.UnixNano()),
			CameraID:   r.cfg.CameraID,
			FilePath:   r.curFinal,
			Format:     model.FormatH264,
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
			Format:      string(model.FormatH264),
			Encoding:    string(model.FormatH264),
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
		r.cfg.Metrics.SegmentsCreated.WithLabelValues(r.cfg.CameraID, "h264").Inc()
	}
}

func (r *IngestRecorder) recordBytes(bytes int64) {
	if r.cfg.Metrics != nil {
		r.cfg.Metrics.RecordingBytesTotal.WithLabelValues(r.cfg.CameraID, "h264").Add(float64(bytes))
	}
}
