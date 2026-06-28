// SPDX-License-Identifier: MIT
//
// Xiaomi camera recorder implementing model.Recorder and model.HLSProvider interfaces.
// Connects via MISS protocol, probes codec (H264/H265), records to MP4 segments.

package xiaomi

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"log/slog"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/event"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/metrics"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/muxer"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/recorder"
)

var xiaomiLogger = slog.Default().With("component", "xiaomi-recorder")

// SegmentStore abstracts storage operations needed by the recorder.
type SegmentStore interface {
	CreateSegment(cameraID string, format string) (tempPath string, finalPath string, err error)
	CloseSegment(tempPath, finalPath string) error
}

// RecordingDB abstracts database operations needed by the recorder.
type RecordingDB interface {
	InsertRecording(ctx context.Context, r *model.Recording) error
	InsertRecordingWithRetry(ctx context.Context, r *model.Recording, maxRetries int, backoff time.Duration) error
}

// ErrorReporter abstracts camera error reporting to avoid circular imports.
// CameraManager satisfies this interface.
type ErrorReporter interface {
	SetErrorDetail(cameraID string, detail *model.CameraErrorDetail)
}

const (
	defaultSegmentDur = 10 * time.Minute
)

// XiaomiCloudConfig holds Xiaomi cloud API credentials for URL resolution.
type XiaomiCloudConfig struct {
	UserID string
	Token  string
	Region string
}

// XiaomiRecorderConfig holds configuration for the Xiaomi recorder.
type XiaomiRecorderConfig struct {
	CameraID     string
	DID          string            // Device ID extracted from xiaomi:// URL (e.g. "655448418")
	Model        string            // Camera model (e.g. ModelC200, ModelC300)
	CloudCfg     XiaomiCloudConfig // Cloud API credentials for MISS URL resolution
	SegmentDur   time.Duration
	DB           RecordingDB
	ErrReporter  ErrorReporter // Optional: reports detailed errors (e.g. TUTK incompatibility)
	AudioEnabled bool          // Capture and broadcast audio via StreamHub when true
	IdleTimeout  time.Duration
	Channel      string // Xiaomi dual-lens channel ("" or "0" = main, "1" = secondary)
	EventBus     *event.EventBus
}

// XiaomiRecorder records H.264/H.265 video from a Xiaomi camera via MISS protocol.
type XiaomiRecorder struct {
	cfg     XiaomiRecorderConfig
	store   SegmentStore
	metrics *metrics.Metrics

	mu     sync.Mutex
	status model.RecorderStatus
	cancel context.CancelFunc
	done   chan struct{}

	muxer         *muxer.MP4Muxer
	trackID       int
	audioTrackID  int
	curFinalPath  string
	curTempPath   string
	segStart      time.Time
	frameCount    int
	lastFrameTime time.Time

	// Codec state (probed from first packets)
	codec   model.Format // "h264" or "h265"
	sps     []byte
	pps     []byte
	vps     []byte // H265 only
	codecOK bool   // true once codec type is determined

	// Audio state (probed from first audio packet)
	audioCodecID uint32 // MISS codec ID for audio (0 = not detected yet)

	Hub         *model.StreamHub // Frame fan-out to multiple consumers (HLS, WebRTC, etc.)
	streamStart time.Time        // For PTS rebase (used by forwardHLS)
}

// GetHub returns the StreamHub for frame fan-out.
func (r *XiaomiRecorder) GetHub() *model.StreamHub { return r.Hub }

// SPS returns the current H.264/H.265 SPS NAL unit (without start bytes).
func (r *XiaomiRecorder) SPS() []byte { return r.sps }

// PPS returns the current H.264/H.265 PPS NAL unit (without start bytes).
func (r *XiaomiRecorder) PPS() []byte { return r.pps }

// VPS returns the current H.265 VPS NAL unit (without start bytes), or nil for H.264.
func (r *XiaomiRecorder) VPS() []byte { return r.vps }

// AudioCodec returns the audio codec name ("g711" or "" for no audio).
func (r *XiaomiRecorder) AudioCodec() string {
	c, ok := missCodecToAudio(r.audioCodecID)
	if !ok {
		return ""
	}
	return string(c)
}

// AudioConfig returns the audio codec config. G.711 returns nil (no ASC).
func (r *XiaomiRecorder) AudioConfig() []byte { return nil }

// AudioSampleRate returns the audio sample rate. G.711 defaults to 8 kHz.
func (r *XiaomiRecorder) AudioSampleRate() int {
	if _, ok := missCodecToAudio(r.audioCodecID); ok {
		return 8000
	}
	return 0
}

// AudioChannels returns the number of audio channels. G.711 is mono.
func (r *XiaomiRecorder) AudioChannels() int {
	if _, ok := missCodecToAudio(r.audioCodecID); ok {
		return 1
	}
	return 0
}

var _ model.Recorder = (*XiaomiRecorder)(nil)
var _ model.HLSProvider = (*XiaomiRecorder)(nil)

// NewXiaomiRecorder creates a new Xiaomi MISS protocol recorder.
func NewXiaomiRecorder(cfg XiaomiRecorderConfig, store SegmentStore, opts ...*metrics.Metrics) *XiaomiRecorder {
	var m *metrics.Metrics
	if len(opts) > 0 {
		m = opts[0]
	}
	if cfg.SegmentDur == 0 {
		cfg.SegmentDur = defaultSegmentDur
	}
	if cfg.IdleTimeout == 0 {
		cfg.IdleTimeout = defaultIdleTimeout
	}
	return &XiaomiRecorder{
		cfg:     cfg,
		store:   store,
		metrics: m,
		status:  model.StatusStopped,
	}
}

// SetErrorReporter sets the error reporter for vendor error reporting.
func (r *XiaomiRecorder) SetErrorReporter(reporter ErrorReporter) {
	r.cfg.ErrReporter = reporter
}

// Start begins recording from the Xiaomi camera in a background goroutine.
func (r *XiaomiRecorder) Start(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.status == model.StatusRecording || r.status == model.StatusReconnecting {
		return fmt.Errorf("recorder for %q already running", r.cfg.CameraID)
	}
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	r.done = make(chan struct{})
	r.status = model.StatusRecording
	r.incActive()
	r.streamStart = time.Now() // Set PTS base for HLS — only once per Start() lifecycle
	go r.run(ctx)
	return nil
}

// Stop terminates the recording goroutine and waits for it to finish.
func (r *XiaomiRecorder) Stop() error {
	r.mu.Lock()
	if r.cancel != nil {
		r.cancel()
	}
	r.mu.Unlock()
	if r.done != nil {
		<-r.done
	}
	r.decActive()
	return nil
}

// Status returns the current recorder status.
func (r *XiaomiRecorder) Status() model.RecorderStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.status
}

func (r *XiaomiRecorder) setStatus(s model.RecorderStatus) {
	r.mu.Lock()
	r.status = s
	r.mu.Unlock()
}

// CodecParams returns the current codec parameters detected from the stream.
// Implements model.HLSProvider.
func (r *XiaomiRecorder) CodecParams() (codec model.Format, sps, pps, vps []byte) {
	return r.codec, r.sps, r.pps, r.vps
}

// incActive increments the active recordings gauge if metrics is available.
func (r *XiaomiRecorder) incActive() {
	if r.metrics != nil {
		r.metrics.ActiveRecordings.Inc()
	}
}

// decActive decrements the active recordings gauge if metrics is available.
func (r *XiaomiRecorder) decActive() {
	if r.metrics != nil {
		r.metrics.ActiveRecordings.Dec()
	}
}

// recordSegmentCreated increments the segments created counter if metrics is available.
func (r *XiaomiRecorder) recordSegmentCreated() {
	if r.metrics != nil {
		r.metrics.SegmentsCreated.WithLabelValues(r.cfg.CameraID, string(r.codec)).Inc()
	}
}

// recordBytes adds to the recording bytes counter if metrics is available.
func (r *XiaomiRecorder) recordBytes(b int64) {
	if r.metrics != nil {
		r.metrics.RecordingBytesTotal.WithLabelValues(r.cfg.CameraID, string(r.codec)).Add(float64(b))
	}
}

// recordError increments the camera errors counter if metrics is available.
func (r *XiaomiRecorder) recordError(errorType string) {
	if r.metrics != nil {
		r.metrics.CameraErrors.WithLabelValues(r.cfg.CameraID, errorType).Inc()
	}
}

// classifyDisconnectReason maps an error to a disconnect reason label.
func classifyDisconnectReason(err error) string {
	if err == nil {
		return "network"
	}
	msg := err.Error()
	if strings.Contains(msg, "no data") {
		return "idle_timeout"
	}
	if strings.Contains(msg, "EOF") || strings.Contains(msg, "connection closed") {
		return "eof"
	}
	if strings.Contains(msg, "cloud") || strings.Contains(msg, "resolve") {
		return "cloud_resolve"
	}
	return "network"
}

// recordXiaomiDisconnect increments the Xiaomi disconnect counter if metrics is available.
func (r *XiaomiRecorder) recordXiaomiDisconnect(reason string) {
	if r.metrics != nil && r.metrics.XiaomiDisconnects != nil {
		r.metrics.XiaomiDisconnects.WithLabelValues(r.cfg.CameraID, reason).Inc()
	}
}

// recordXiaomiReconnect increments the Xiaomi reconnect counter if metrics is available.
func (r *XiaomiRecorder) recordXiaomiReconnect() {
	if r.metrics != nil && r.metrics.XiaomiReconnects != nil {
		r.metrics.XiaomiReconnects.WithLabelValues(r.cfg.CameraID).Inc()
	}
}

// reportVendorError checks if the error indicates an unsupported TUTK vendor
// and, if so, reports a detailed CameraErrorDetail via the ErrorReporter.
// This fires on every reconnect attempt so the frontend always has current state.
func (r *XiaomiRecorder) reportVendorError(err error) {
	if r.cfg.ErrReporter == nil || err == nil {
		return
	}
	errMsg := err.Error()
	if !strings.Contains(errMsg, "unsupported vendor") {
		return
	}
	// Extract vendor name from error: "miss: unsupported vendor \"tutk\""
	vendor := extractQuotedValue(errMsg)
	msg := fmt.Sprintf("Camera uses unsupported transport vendor %q (TUTK). This camera model is not compatible.", vendor)
	r.cfg.ErrReporter.SetErrorDetail(r.cfg.CameraID, &model.CameraErrorDetail{
		Type:       "tutk_incompatible",
		Message:    msg,
		DetectedAt: time.Now(),
	})
}

// extractQuotedValue extracts the content between the first pair of double quotes in s.
// Returns empty string if no quotes are found.
func extractQuotedValue(s string) string {
	start := strings.Index(s, `"`)
	if start < 0 {
		return ""
	}
	end := strings.Index(s[start+1:], `"`)
	if end < 0 {
		return ""
	}
	return s[start+1 : start+1+end]
}

// run is the main reconnect loop.
func (r *XiaomiRecorder) run(ctx context.Context) {
	defer func() {
		if panicErr := recover(); panicErr != nil {
			buf := make([]byte, 4096)
			buf = buf[:runtime.Stack(buf, false)]
			xiaomiLogger.Error("PANIC recovered in run", "camera_id", r.cfg.CameraID, "panic", panicErr, "stack", string(buf))
		}
	}()
	defer close(r.done)
	defer r.setStatus(model.StatusStopped)

	var retryCount int
	for {
		// Resolve xiaomi://deviceID to miss://... URL via cloud API.
		missURL, err := ResolveMISSURL(r.cfg.CloudCfg, r.cfg.DID, r.cfg.Model)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			retryCount++
			backoff := recorder.TieredBackoffWithJitter(retryCount)
			r.reportVendorError(err)
			xiaomiLogger.Error("failed to resolve MISS URL, retrying", "camera_id", r.cfg.CameraID, "error", err, "backoff", backoff, "attempt", retryCount)
			r.recordError("cloud_resolve")
			r.recordXiaomiDisconnect("cloud_resolve")
			r.setStatus(model.StatusReconnecting)
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			continue
		}

		err, connected := r.connectAndRecord(ctx, missURL)
		if ctx.Err() != nil {
			return
		}
		if connected {
			retryCount = 0
			r.recordXiaomiReconnect()
		}
		retryCount++
		backoff := recorder.TieredBackoffWithJitter(retryCount)
		xiaomiLogger.Error("connection error, reconnecting", "camera_id", r.cfg.CameraID, "error", err, "backoff", backoff, "attempt", retryCount)
		r.recordError("connection")
		r.recordXiaomiDisconnect(classifyDisconnectReason(err))
		r.setStatus(model.StatusReconnecting)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
	}
}

// connectAndRecord connects to the Xiaomi camera, starts media, and records packets.
func (r *XiaomiRecorder) connectAndRecord(ctx context.Context, missURL string) (error, bool) {
	client, err := NewMISSClient(missURL, r.cfg.IdleTimeout)
	if err != nil {
		return fmt.Errorf("miss connect: %w", err), false
	}
	defer client.Conn.Close()

	// Start HD video stream (use configured channel for dual-lens cameras).
	if err := client.StartMedia(r.cfg.Channel, "hd", r.cfg.AudioEnabled); err != nil {
		return fmt.Errorf("miss start media: %w", err), false
	}
	defer func() {
		_ = client.StopMedia()
	}()

	r.setStatus(model.StatusRecording)

	// Reset codec probe state for each new connection.
	r.codecOK = false
	r.sps = nil
	r.pps = nil
	r.vps = nil
	r.audioCodecID = 0

	var lastTimestamp uint64

	for {
		select {
		case <-ctx.Done():
			r.closeCurrentSegment()
			return ctx.Err(), true
		default:
		}

		pkt, err := client.ReadPacket()
		if err != nil {
			r.closeCurrentSegment()
			return fmt.Errorf("miss read: %w", err), false
		}

		// Handle audio packets when AudioEnabled.
		if pkt.CodecID >= 1024 {
			if r.cfg.AudioEnabled {
				r.forwardAudio(pkt.CodecID, pkt.Payload)
			}
			continue
		}

		// Skip other non-video packets.
		if pkt.CodecID != missCodecH264 && pkt.CodecID != missCodecH265 {
			continue
		}

		// Probe codec type from first video packet.
		if !r.codecOK {
			switch pkt.CodecID {
			case missCodecH264:
				r.codec = model.FormatH264
			case missCodecH265:
				r.codec = model.FormatH265
			}
			r.codecOK = true
			xiaomiLogger.Info("codec detected", "camera_id", r.cfg.CameraID, "codec", r.codec)
		}

		// Parse Annex B NALUs from payload and process each one.
		nalus := splitAnnexBNALUs(pkt.Payload)
		for _, nalu := range nalus {
			r.processNALU(nalu, pkt.Timestamp, &lastTimestamp)
		}
	}
}

// processNALU handles a single NALU extracted from Annex B payload.
func (r *XiaomiRecorder) processNALU(nalu []byte, timestamp uint64, lastTimestamp *uint64) {
	if len(nalu) == 0 {
		return
	}

	switch r.codec {
	case model.FormatH264:
		r.processH264NALU(nalu, timestamp, lastTimestamp)
	case model.FormatH265:
		r.processH265NALU(nalu, timestamp, lastTimestamp)
	}
}

// processH264NALU handles an H.264 NAL unit.
func (r *XiaomiRecorder) processH264NALU(nalu []byte, timestamp uint64, lastTimestamp *uint64) {
	naluType := nalu[0] & 0x1F
	switch naluType {
	case 7: // SPS
		if r.sps != nil && !bytes.Equal(r.sps, nalu) {
			xiaomiLogger.Info("SPS change detected, rotating segment", "camera_id", r.cfg.CameraID)
			r.closeCurrentSegment()
		}
		r.sps = append([]byte(nil), nalu...)
		return
	case 8: // PPS
		if r.pps != nil && !bytes.Equal(r.pps, nalu) {
			xiaomiLogger.Info("PPS change detected, rotating segment", "camera_id", r.cfg.CameraID)
			r.closeCurrentSegment()
		}
		r.pps = append([]byte(nil), nalu...)
		return
	}

	// Only write video frames (IDR=5, non-IDR=1).
	if naluType != 5 && naluType != 1 {
		return
	}
	if r.sps == nil || r.pps == nil {
		return
	}

	// Wait for IDR frame before starting a new segment.
	if r.muxer == nil && naluType != 5 {
		return
	}

	if r.muxer == nil {
		tempPath, finalPath, err := r.store.CreateSegment(r.cfg.CameraID, string(model.FormatH264))
		if err != nil {
			xiaomiLogger.Error("failed to create segment", "camera_id", r.cfg.CameraID, "error", err)
			return
		}
		r.muxer = muxer.NewMP4Muxer(tempPath)
		trackID, err := r.muxer.AddH264Track(r.sps, r.pps)
		if err != nil {
			xiaomiLogger.Error("failed to add H264 track", "camera_id", r.cfg.CameraID, "error", err)
			r.muxer = nil
			os.Remove(tempPath)
			return
		}
		r.trackID = trackID

		// Add audio track if audio codec detected (G.711 or Opus).
		if r.cfg.AudioEnabled && r.audioCodecID > 0 {
			codec, cfg, ok := buildAudioMuxerConfig(r.audioCodecID)
			if ok {
				aID, err := r.muxer.AddAudioTrack(codec, cfg)
				if err != nil {
					xiaomiLogger.Debug("audio track not added to muxer", "camera_id", r.cfg.CameraID, "error", err)
				} else {
					r.audioTrackID = aID
				}
			}
		}
		r.curTempPath = tempPath
		r.curFinalPath = finalPath
		r.segStart = time.Now()
		r.lastFrameTime = r.segStart
		r.frameCount = 0
	}

	now := time.Now()
	pts := now.Sub(r.segStart)
	duration := now.Sub(r.lastFrameTime)
	if duration < time.Millisecond {
		duration = time.Millisecond
	}
	r.lastFrameTime = now

	if err := r.muxer.WriteSample(r.trackID, nalu, pts, duration); err != nil {
		xiaomiLogger.Error("failed to write sample", "camera_id", r.cfg.CameraID, "error", err)
		return
	}
	r.frameCount++

	// Forward to HLS (non-blocking)
	r.forwardHLS(nalu)

	if time.Since(r.segStart) >= r.cfg.SegmentDur {
		r.closeCurrentSegment()
	}
}

// processH265NALU handles an H.265/HEVC NAL unit.
func (r *XiaomiRecorder) processH265NALU(nalu []byte, timestamp uint64, lastTimestamp *uint64) {
	// HEVC NALU type: 2-byte header, type is in bits 1-6 of first byte.
	naluType := (nalu[0] >> 1) & 0x3F
	switch naluType {
	case 32: // VPS
		if r.vps != nil && !bytes.Equal(r.vps, nalu) {
			xiaomiLogger.Info("VPS change detected, rotating segment", "camera_id", r.cfg.CameraID)
			r.closeCurrentSegment()
		}
		r.vps = append([]byte(nil), nalu...)
		return
	case 33: // SPS
		if r.sps != nil && !bytes.Equal(r.sps, nalu) {
			xiaomiLogger.Info("SPS change detected, rotating segment", "camera_id", r.cfg.CameraID)
			r.closeCurrentSegment()
		}
		r.sps = append([]byte(nil), nalu...)
		return
	case 34: // PPS
		if r.pps != nil && !bytes.Equal(r.pps, nalu) {
			xiaomiLogger.Info("PPS change detected, rotating segment", "camera_id", r.cfg.CameraID)
			r.closeCurrentSegment()
		}
		r.pps = append([]byte(nil), nalu...)
		return
	}

	// Only write VCL NALUs (types 0-31). Non-VCL types are 32+.
	if naluType >= 32 {
		return
	}
	if r.vps == nil || r.sps == nil || r.pps == nil {
		return
	}

	// Wait for IDR frame (types 19=IDR_W_RADL, 20=IDR_N_LP).
	if r.muxer == nil && naluType != 19 && naluType != 20 {
		return
	}

	if r.muxer == nil {
		tempPath, finalPath, err := r.store.CreateSegment(r.cfg.CameraID, string(model.FormatH265))
		if err != nil {
			xiaomiLogger.Error("failed to create segment", "camera_id", r.cfg.CameraID, "error", err)
			return
		}
		r.muxer = muxer.NewMP4Muxer(tempPath)
		trackID, err := r.muxer.AddH265Track(r.vps, r.sps, r.pps)
		if err != nil {
			xiaomiLogger.Error("failed to add H265 track", "camera_id", r.cfg.CameraID, "error", err)
			r.muxer = nil
			os.Remove(tempPath)
			return
		}
		r.trackID = trackID

		// Add audio track if audio codec detected (same as H264 path).
		if r.cfg.AudioEnabled && r.audioCodecID > 0 {
			codec, cfg, ok := buildAudioMuxerConfig(r.audioCodecID)
			if ok {
				aID, err := r.muxer.AddAudioTrack(codec, cfg)
				if err != nil {
					xiaomiLogger.Debug("audio track not added to muxer", "camera_id", r.cfg.CameraID, "error", err)
				} else {
					r.audioTrackID = aID
				}
			}
		}
		r.curTempPath = tempPath
		r.curFinalPath = finalPath
		r.segStart = time.Now()
		r.lastFrameTime = r.segStart
		r.frameCount = 0
	}

	now := time.Now()
	pts := now.Sub(r.segStart)
	duration := now.Sub(r.lastFrameTime)
	if duration < time.Millisecond {
		duration = time.Millisecond
	}
	r.lastFrameTime = now

	if err := r.muxer.WriteSample(r.trackID, nalu, pts, duration); err != nil {
		xiaomiLogger.Error("failed to write sample", "camera_id", r.cfg.CameraID, "error", err)
		return
	}
	r.frameCount++

	// Forward to HLS (non-blocking)
	r.forwardHLS(nalu)

	if time.Since(r.segStart) >= r.cfg.SegmentDur {
		r.closeCurrentSegment()
	}
}

// forwardHLS sends a NALU to all stream consumers via StreamHub (non-blocking).
// For IDR frames, the codec parameter sets (SPS/PPS or VPS/SPS/PPS) are prepended
// so the HLS DTS extractor receives a complete access unit.
func (r *XiaomiRecorder) forwardHLS(nalu []byte) {
	if r.Hub == nil || r.Hub.ConsumerCount() == 0 {
		return
	}
	// Convert wall-clock duration to 90kHz ticks (RTP timestamp units).
	// HLS manager uses ClockRate=90000, so PTS must be in 90kHz ticks,
	// not nanoseconds. This matches built-in H264/H265 recorders which
	// pass RTP timestamps directly.
	pts := time.Since(r.streamStart).Nanoseconds() * 90000 / int64(time.Second)

	switch r.codec {
	case model.FormatH264:
		naluType := nalu[0] & 0x1F
		if naluType == 5 && r.sps != nil && r.pps != nil {
			r.Hub.Broadcast(pts, [][]byte{r.sps, r.pps, nalu}, true)
		} else {
			r.Hub.Broadcast(pts, [][]byte{nalu}, false)
		}
	case model.FormatH265:
		naluType := (nalu[0] >> 1) & 0x3F
		if (naluType == 19 || naluType == 20) && r.vps != nil && r.sps != nil && r.pps != nil {
			r.Hub.Broadcast(pts, [][]byte{r.vps, r.sps, r.pps, nalu}, true)
		} else {
			r.Hub.Broadcast(pts, [][]byte{nalu}, false)
		}
	default:
		r.Hub.Broadcast(pts, [][]byte{nalu}, false)
	}
}

// missCodecToAudio maps a MISS audio codec ID to a model.AudioCodec.
// Returns (codec, true) for known codecs, ("", false) for unknown/unsupported.
// PCMA, PCMU, and PCM all map to G.711 for StreamHub broadcast purposes.
func missCodecToAudio(codecID uint32) (model.AudioCodec, bool) {
	switch codecID {
	case missCodecPCMA, missCodecPCMU, missCodecPCM:
		return model.AudioG711, true
	case missCodecOPUS:
		return model.AudioOpus, true
	default:
		return "", false
	}
}

// buildAudioMuxerConfig returns the codec name and config bytes for the MP4 muxer
// based on the MISS audio codec ID.
func buildAudioMuxerConfig(codecID uint32) (codec string, config []byte, ok bool) {
	switch codecID {
	case missCodecPCMA:
		sr := 8000
		return "g711", []byte{0, byte(sr >> 24), byte(sr >> 16), byte(sr >> 8), byte(sr)}, true
	case missCodecPCMU:
		sr := 8000
		return "g711", []byte{1, byte(sr >> 24), byte(sr >> 16), byte(sr >> 8), byte(sr)}, true
	case missCodecOPUS:
		// Opus config: 1 byte channel count + 2 bytes PreSkip (BE) + 4 bytes InputSampleRate (BE)
		// Xiaomi cameras: mono, 16kHz input rate, no pre-skip info from MISS protocol
		opusRate := 16000
		return "opus", []byte{1, 0, 0, byte(opusRate >> 24), byte(opusRate >> 16), byte(opusRate >> 8), byte(opusRate)}, true
	default:
		return "", nil, false
	}
}


// forwardAudio broadcasts audio data via StreamHub (non-blocking)
// and writes to the MP4 muxer when an audio track is available.
// Skips silently when AudioEnabled is false.
// Unknown codec IDs are skipped with a warning log.
func (r *XiaomiRecorder) forwardAudio(codecID uint32, payload []byte) {
	if !r.cfg.AudioEnabled {
		return
	}
	audioCodec, ok := missCodecToAudio(codecID)
	if !ok {
		// Log unsupported codecs once, not per-frame (e.g. Opus codec_id=1032)
		if r.audioCodecID == 0 {
			xiaomiLogger.Info("audio codec not yet supported for recording, skipping", "camera_id", r.cfg.CameraID, "codec_id", codecID)
			r.audioCodecID = codecID // mark as seen to suppress repeated logs
		}
		return
	}

	// Remember the audio codec ID for muxer track creation.
	if r.audioCodecID == 0 {
		r.audioCodecID = codecID
		xiaomiLogger.Info("audio codec detected", "camera_id", r.cfg.CameraID, "codec_id", codecID, "codec", audioCodec)
	}

	// Broadcast to live consumers via StreamHub.
	if r.Hub != nil && r.Hub.AudioConsumerCount() > 0 {
		pts := time.Since(r.streamStart).Nanoseconds() * 90000 / int64(time.Second)
		r.Hub.BroadcastAudio(pts, audioCodec, payload)
	}

	// Write audio to MP4 muxer if audio track is available.
	r.mu.Lock()
	m := r.muxer
	aid := r.audioTrackID
	start := r.segStart
	r.mu.Unlock()
	if m != nil && aid > 0 {
		pts := time.Since(start)
		// Audio frame duration: 20ms (G.711 and Opus both use 20ms frames).
		dur := 20 * time.Millisecond
		if err := m.WriteAudioSample(aid, payload, pts, dur); err != nil {
			xiaomiLogger.Error("failed to write audio sample", "camera_id", r.cfg.CameraID, "error", err)
		}
	}
}

// closeCurrentSegment finalizes the current MP4 segment.
func (r *XiaomiRecorder) closeCurrentSegment() {
	if r.muxer == nil {
		return
	}
	if err := r.muxer.Close(); err != nil {
		xiaomiLogger.Error("failed to close muxer", "camera_id", r.cfg.CameraID, "error", err)
		if r.curTempPath != "" {
			os.Remove(r.curTempPath)
		}
		r.muxer = nil
		r.curTempPath = ""
		r.curFinalPath = ""
		r.frameCount = 0
		return
	}

	// Atomic rename: temp → final
	if r.curTempPath != "" && r.curFinalPath != "" {
		if err := r.store.CloseSegment(r.curTempPath, r.curFinalPath); err != nil {
			xiaomiLogger.Error("failed to close segment", "camera_id", r.cfg.CameraID, "error", err)
		}
	}

	// Insert recording entry into database.
	var fileSize int64
	var recordingID string
	if r.cfg.DB != nil && r.curFinalPath != "" {
		now := time.Now()
		duration := now.Sub(r.segStart).Seconds()
		rec := &model.Recording{
			ID:         fmt.Sprintf("%d", now.UnixNano()),
			CameraID:   r.cfg.CameraID,
			FilePath:   r.curFinalPath,
			Format:     r.codec,
			StartedAt:  r.segStart,
			EndedAt:    now,
			Duration:   duration,
			FrameCount: r.frameCount,
		}
		recordingID = rec.ID
		if info, err := os.Stat(r.curFinalPath); err == nil {
			fileSize = info.Size()
			rec.FileSize = fileSize
		}
		if err := r.cfg.DB.InsertRecordingWithRetry(context.Background(), rec, 3, 500*time.Millisecond); err != nil {
			xiaomiLogger.Error("failed to insert recording", "camera_id", r.cfg.CameraID, "error", err)
		}
	}

	// Publish SegmentCompleted event.
	if r.cfg.EventBus != nil && recordingID != "" {
		r.cfg.EventBus.Publish(context.Background(), event.TopicSegmentCompleted, event.SegmentCompleted{
			CameraID:    r.cfg.CameraID,
			FilePath:    r.curFinalPath,
			Format:      string(r.codec),
			Encoding:    string(r.codec),
			StartedAt:   r.segStart.Format(time.RFC3339Nano),
			EndedAt:     time.Now().Format(time.RFC3339Nano),
			FileSize:    fileSize,
			RecordingID: recordingID,
		})
	}

	// Update metrics.
	if r.frameCount > 0 && r.curFinalPath != "" {
		r.recordSegmentCreated()
		if fileSize > 0 {
			r.recordBytes(fileSize)
		}
	}

	r.muxer = nil
	r.trackID = 0
	r.audioTrackID = 0
	r.curTempPath = ""
	r.curFinalPath = ""
	r.frameCount = 0
}

// splitAnnexBNALUs splits Annex B formatted data into individual NALUs.
// It finds 00 00 00 01 or 00 00 01 start codes and returns the data between them.
func splitAnnexBNALUs(data []byte) [][]byte {
	var nalus [][]byte
	start := -1 // -1 means we haven't found the first start code yet

	for i := 0; i <= len(data)-3; i++ {
		if data[i] == 0 && data[i+1] == 0 {
			scLen := 0
			if data[i+2] == 1 {
				scLen = 3
			} else if i <= len(data)-4 && data[i+2] == 0 && data[i+3] == 1 {
				scLen = 4
			}
			if scLen > 0 {
				// If we had a previous start, extract the NALU up to this start code.
				if start >= 0 {
					// Trim trailing zeros before the start code.
					end := i
					for end > start && data[end-1] == 0 {
						end--
					}
					if end > start {
						nalus = append(nalus, bytes.Clone(data[start:end]))
					}
				}
				start = i + scLen
				i += scLen - 1
			}
		}
	}

	// Append the last NALU.
	if start >= 0 && start < len(data) {
		nalus = append(nalus, bytes.Clone(data[start:]))
	}

	return nalus
}

// annexBToAVCC converts Annex B formatted H264/H265 NALUs to AVCC format.
// It finds start codes, extracts NALUs, and prepends 4-byte big-endian length to each.
func annexBToAVCC(data []byte) []byte {
	nalus := splitAnnexBNALUs(data)
	if len(nalus) == 0 {
		return nil
	}

	// Calculate total size: sum of (4-byte length + nalu) for each NALU.
	totalSize := 0
	for _, nalu := range nalus {
		totalSize += 4 + len(nalu)
	}

	// Build AVCC buffer.
	result := make([]byte, 0, totalSize)
	lenBuf := make([]byte, 4)
	for _, nalu := range nalus {
		binary.BigEndian.PutUint32(lenBuf, uint32(len(nalu)))
		result = append(result, lenBuf...)
		result = append(result, nalu...)
	}
	return result
}
