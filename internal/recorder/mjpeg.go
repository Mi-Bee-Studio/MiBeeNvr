package recorder

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bluenviron/gortsplib/v5"
	"github.com/bluenviron/gortsplib/v5/pkg/base"
	"github.com/bluenviron/gortsplib/v5/pkg/description"
	"github.com/bluenviron/gortsplib/v5/pkg/format"
	"github.com/bluenviron/gortsplib/v5/pkg/format/rtplpcm"
	"github.com/bluenviron/gortsplib/v5/pkg/format/rtpmjpeg"
	"github.com/pion/rtp"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/avi"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/event"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/metrics"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
)

var mjpegLogger = slog.Default().With("component", "mjpeg-recorder")

// MJPEGConfig holds configuration for the MJPEG recorder.
type MJPEGConfig struct {
	CameraID               string
	RTSPURL                string
	SegmentDur             time.Duration
	SampleInterval         int // if >1, only save every Nth frame
	DB                     RecordingDB
	EventBus               *event.EventBus
	AudioEnabled           bool
	DarkFrameFilterEnabled bool // skip dark/night segments
	DarkFrameThreshold     int  // luminance threshold 0-255 (default 15)
	// RecordEnabled gates segment writes (nil => record; ptr-to-false => live-only).
	// See BaseConfig.RecordEnabled for details.
	RecordEnabled *bool
}

// MJPEGRecorder records Motion-JPEG video from an RTSP source.
// When audio is present (AudioEnabled + G.711 in SDP), it creates AVI files
// with MJPEG video + G.711 audio. Without audio, it stores JPEG frames to a
// directory (backward compatible).
type MJPEGRecorder struct {
	cfg     MJPEGConfig
	store   SegmentStore
	metrics *metrics.Metrics

	mu     sync.Mutex
	status model.RecorderStatus
	cancel context.CancelFunc
	done   chan struct{}

	curTempPath  string
	curFinalPath string
	segStart     time.Time
	frameCount   int
	frameSeq     int64 // monotonic counter for frame sampling

	frameCh         chan []byte
	dropped         atomic.Int64
	latestFrame     atomic.Pointer[[]byte] // cached latest JPEG frame for zero-copy polling
	Hub             *model.StreamHub       // Frame fan-out (nil for MJPEG — no HLS support, reserved for future consumers)
	lastHealthLogAt time.Time              // throttled log for storage health failures

	// Audio/AVI fields
	hasAudio       bool
	g711MULaw      bool
	g711SampleRate int
	aviMuxer       *avi.Muxer
	aviFile        *os.File
}

// segmentFormat returns the recording format for the current segment.
func (r *MJPEGRecorder) segmentFormat() model.Format {
	if r.hasAudio {
		return model.FormatAVI
	}
	return model.FormatMJPEG
}

// jpegDimensions extracts JPEG image dimensions from raw JPEG data.
func jpegDimensions(data []byte) (width, height int, ok bool) {
	if len(data) < 4 || data[0] != 0xFF || data[1] != 0xD8 {
		return 0, 0, false
	}
	idx := 2
	for idx < len(data)-1 {
		if data[idx] != 0xFF {
			return 0, 0, false
		}
		marker := data[idx+1]
		if marker == 0xC0 || marker == 0xC1 || marker == 0xC2 {
			// SOF0/1/2: length(2), precision(1), height(2), width(2)
			if idx+9 >= len(data) {
				return 0, 0, false
			}
			height = int(data[idx+5])<<8 | int(data[idx+6])
			width = int(data[idx+7])<<8 | int(data[idx+8])
			return width, height, true
		}
		if marker == 0xD9 || marker == 0xDA {
			// EOI or SOS - no SOF found
			return 0, 0, false
		}
		if marker == 0xFF || (marker >= 0xD0 && marker <= 0xD7) || marker == 0x01 {
			idx += 2
		} else {
			if idx+3 >= len(data) {
				return 0, 0, false
			}
			segLen := int(data[idx+2])<<8 | int(data[idx+3])
			if segLen < 2 {
				return 0, 0, false
			}
			idx += 2 + segLen
		}
	}
	return 0, 0, false
}

// GetHub returns the StreamHub for frame fan-out.
func (r *MJPEGRecorder) GetHub() *model.StreamHub { return r.Hub }

// SetHub wires the StreamHub for frame fan-out (model.HubHost).
func (r *MJPEGRecorder) SetHub(hub *model.StreamHub) { r.Hub = hub }

// HubSource labels the hub for the flow-path observability view.
func (r *MJPEGRecorder) HubSource() string { return "mjpeg" }

// LatestFrame returns the most recently decoded JPEG frame WITHOUT copying.
// The returned slice is shared and must be treated as read-only by callers.
// Returns nil if no frame has been decoded yet. Safe for concurrent use.
// Used by dual-mode timelapse frame polling (LatestFrame()) and the MJPEG
// snapshot endpoint.
func (r *MJPEGRecorder) LatestFrame() []byte {
	p := r.latestFrame.Load()
	if p == nil {
		return nil
	}
	return *p
}

// incActive increments the active recordings gauge if metrics is available.
func (r *MJPEGRecorder) incActive() {
	if r.metrics != nil {
		r.metrics.ActiveRecordings.Inc()
	}
}

// decActive decrements the active recordings gauge if metrics is available.
func (r *MJPEGRecorder) decActive() {
	if r.metrics != nil {
		r.metrics.ActiveRecordings.Dec()
	}
}

// recordSegmentCreated increments the segments created counter if metrics is available.
func (r *MJPEGRecorder) recordSegmentCreated() {
	if r.metrics != nil {
		r.metrics.SegmentsCreated.WithLabelValues(r.cfg.CameraID, string(r.segmentFormat())).Inc()
	}
}

// recordError increments the camera errors counter if metrics is available.
func (r *MJPEGRecorder) recordError(errorType string) {
	if r.metrics != nil {
		r.metrics.CameraErrors.WithLabelValues(r.cfg.CameraID, errorType).Inc()
	}
}

var _ model.Recorder = (*MJPEGRecorder)(nil)

func NewMJPEGRecorder(cfg MJPEGConfig, store SegmentStore, opts ...*metrics.Metrics) *MJPEGRecorder {
	var m *metrics.Metrics
	if len(opts) > 0 {
		m = opts[0]
	}
	// When audio is enabled, MJPEG goes through the AVI muxer (same as
	// HTTPJPEGRecorder with AVI=true), which buffers all frames in RAM until
	// segment close. Apply the same RAM-dependent cap to prevent OOM on
	// low-memory hosts. See aviSegmentDurCap for rationale.
	if cfg.AudioEnabled {
		if durCap := aviSegmentDurCap(); cfg.SegmentDur > durCap {
			mjpegLogger.Warn("AVI (audio) mode: SegmentDur capped by available RAM",
				"camera_id", cfg.CameraID, "configured", cfg.SegmentDur, "capped_to", durCap,
				"mem_available_mb", memAvailableMB())
			cfg.SegmentDur = durCap
		}
	}
	if cfg.SegmentDur == 0 {
		cfg.SegmentDur = DefaultSegmentDur
	}
	if cfg.SampleInterval <= 0 {
		cfg.SampleInterval = 1
	}
	return &MJPEGRecorder{
		cfg:     cfg,
		store:   store,
		metrics: m,
		status:  model.StatusStopped,
	}
}

func (r *MJPEGRecorder) Start(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.status == model.StatusRecording || r.status == model.StatusReconnecting {
		return fmt.Errorf("recorder for %q already running", r.cfg.CameraID)
	}
	ctx, cancel := context.WithCancel(ctx)
	r.cancel = cancel
	r.done = make(chan struct{})
	r.status = model.StatusRecording
	r.incActive()
	go r.run(ctx)
	return nil
}

func (r *MJPEGRecorder) Stop() error {
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

func (r *MJPEGRecorder) Status() model.RecorderStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.status
}

func (r *MJPEGRecorder) setStatus(s model.RecorderStatus) {
	r.mu.Lock()
	r.status = s
	r.mu.Unlock()
}

func (r *MJPEGRecorder) run(ctx context.Context) {
	defer close(r.done)
	defer r.setStatus(model.StatusStopped)
	var retryCount int
	for {
		err, connected := r.connectAndRecord(ctx)
		if ctx.Err() != nil {
			return
		}
		if connected {
			retryCount = 0
			if r.metrics != nil {
				r.metrics.CameraReconnectBackoffSeconds.WithLabelValues(r.cfg.CameraID).Set(0)
			}
		}
		retryCount++
		backoff := TieredBackoffWithJitter(retryCount)
		storageFailed := isStorageFailed(r.store, r.cfg.CameraID)
		if storageFailed {
			backoff = StorageBackoffWithJitter()
		}
		if r.metrics != nil {
			r.metrics.CameraReconnectBackoffSeconds.WithLabelValues(r.cfg.CameraID).Set(backoff.Seconds())
		}
		mjpegLogger.Error("connection error, reconnecting", "camera_id", r.cfg.CameraID, "error", err, "backoff", backoff, "attempt", retryCount, "storage_failed", storageFailed)
		r.recordError("connection")
		r.setStatus(model.StatusReconnecting)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
	}
}

func (r *MJPEGRecorder) connectAndRecord(ctx context.Context) (error, bool) {
	// Reset audio/AVI state for new connection.
	r.hasAudio = false
	r.mu.Lock()
	r.aviMuxer = nil
	r.aviFile = nil
	r.mu.Unlock()

	u, err := base.ParseURL(r.cfg.RTSPURL)
	if err != nil {
		return fmt.Errorf("invalid RTSP URL: %w", err), false
	}
	tcp := gortsplib.ProtocolTCP
	client := &gortsplib.Client{
		Scheme:       u.Scheme,
		Host:         u.Host,
		Protocol:     &tcp,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}
	if err := client.Start(); err != nil {
		return fmt.Errorf("client start: %w", err), false
	}
	defer client.Close()

	desc, _, err := client.Describe(u)
	if err != nil {
		return fmt.Errorf("DESCRIBE: %w", err), false
	}

	var forma *format.MJPEG
	medi := desc.FindFormat(&forma)
	if medi == nil {
		return fmt.Errorf("MJPEG media not found in stream"), false
	}

	rtpDec, err := forma.CreateDecoder()
	if err != nil {
		return fmt.Errorf("create RTP decoder: %w", err), false
	}

	if _, err := client.Setup(desc.BaseURL, medi, 0, 0); err != nil {
		return fmt.Errorf("SETUP: %w", err), false
	}

	// Audio setup: find G.711 format if AudioEnabled.
	var g711Dec *rtplpcm.Decoder
	var g711Forma *format.G711
	var audioMedi *description.Media

	if r.cfg.AudioEnabled {
		audioMedi = desc.FindFormat(&g711Forma)
		if audioMedi != nil {
			dec := &rtplpcm.Decoder{BitDepth: 8, ChannelCount: 1}
			if err := dec.Init(); err != nil {
				mjpegLogger.Warn("G.711 decoder init failed", "camera_id", r.cfg.CameraID, "error", err)
			} else {
				g711Dec = dec
				if _, err := client.Setup(desc.BaseURL, audioMedi, 0, 1); err != nil {
					mjpegLogger.Warn("G.711 audio SETUP failed, continuing video-only", "camera_id", r.cfg.CameraID, "error", err)
					g711Dec = nil
				} else {
					r.hasAudio = true
					r.g711MULaw = g711Forma.MULaw
					r.g711SampleRate = g711Forma.SampleRate
					mjpegLogger.Info("G.711 audio track detected", "camera_id", r.cfg.CameraID, "mulaw", g711Forma.MULaw, "rate", g711Forma.SampleRate)
				}
			}
		} else {
			mjpegLogger.Debug("no G.711 audio format found in stream", "camera_id", r.cfg.CameraID)
		}
	}

	r.frameCh = make(chan []byte, DefaultRingBufCap)
	r.dropped.Store(0)
	r.frameSeq = 0
	writerDone := make(chan struct{})
	go r.writeFrames(writerDone)

	client.OnPacketRTP(medi, forma, func(pkt *rtp.Packet) {
		jpeg, err := rtpDec.Decode(pkt)
		if err != nil {
			// "need more packets" is the normal multi-packet-frame accumulation
			// signal (returned for every non-final fragment). ESP32 RTSP-AVI firmware
			// sends large JPEGs fragmented across many RTP packets, so this fires
			// dozens of times per frame — down-rate it to avoid flooding the log.
			if !errors.Is(err, rtpmjpeg.ErrMorePacketsNeeded) {
				mjpegLogger.Error("RTP decode error", "camera_id", r.cfg.CameraID, "error", err)
			}
			return
		}
		// Cache latest frame for timelapse frame polling (LatestFrame). The decoder
		// returns a freshly allocated slice, so storing the pointer is safe.
		dp := jpeg
		r.latestFrame.Store(&dp)
		// Broadcast to StreamHub for wsstream live preview (MJPEG cameras).
		// Each JPEG frame is wrapped as a single-element [][]byte to match
		// the FrameCallback signature. wsstream treats every MJPEG frame as
		// a keyframe (independently decodable).
		if r.Hub != nil {
			r.Hub.Broadcast(int64(pkt.Timestamp), [][]byte{jpeg}, true)
		}
		select {
		case r.frameCh <- jpeg:
		default:
			d := r.dropped.Add(1)
			if r.metrics != nil {
				r.metrics.RecorderRingBufferDropsTotal.WithLabelValues(r.cfg.CameraID).Inc()
			}
			if d%100 == 1 {
				mjpegLogger.Warn("ring buffer full, dropped frames", "camera_id", r.cfg.CameraID, "dropped", d)
			}
		}
	})

	// Audio RTP callback: decode G.711 and dual-write to hub + AVI muxer.
	if g711Dec != nil && audioMedi != nil && g711Forma != nil {
		client.OnPacketRTP(audioMedi, g711Forma, func(pkt *rtp.Packet) {
			data, err := g711Dec.Decode(pkt)
			if err != nil {
				mjpegLogger.Error("G.711 RTP decode error", "camera_id", r.cfg.CameraID, "error", err)
				return
			}
			// Broadcast for live preview.
			if r.Hub != nil {
				r.Hub.BroadcastAudio(int64(pkt.Timestamp), model.AudioG711, data)
			}
			// Write to AVI muxer (if active segment).
			r.mu.Lock()
			m := r.aviMuxer
			if m != nil {
				if err := m.WriteAudio(data, 0); err != nil {
					mjpegLogger.Error("failed to write audio to AVI muxer", "camera_id", r.cfg.CameraID, "error", err)
				}
			}
			r.mu.Unlock()
		})
	}

	r.setStatus(model.StatusRecording)
	if _, err := client.Play(nil); err != nil {
		close(r.frameCh)
		<-writerDone
		return fmt.Errorf("PLAY: %w", err), false
	}

	errCh := make(chan error, 1)
	go func() { errCh <- client.Wait() }()

	select {
	case err := <-errCh:
		close(r.frameCh)
		<-writerDone
		r.closeCurrentSegment()
		return err, true
	case <-ctx.Done():
		client.Close()
		close(r.frameCh)
		<-writerDone
		r.closeCurrentSegment()
		return ctx.Err(), true
	}
}

func (r *MJPEGRecorder) writeFrames(done chan struct{}) {
	defer func() {
		if panicErr := recover(); panicErr != nil {
			buf := make([]byte, 4096)
			buf = buf[:runtime.Stack(buf, false)]
			mjpegLogger.Error("PANIC recovered in writeFrames", "camera_id", r.cfg.CameraID, "panic", panicErr, "stack", string(buf))
		}
	}()

	defer close(done)

	for data := range r.frameCh {
		// Live-only mode: drain the channel (so the RTP callback's non-blocking
		// send never blocks) but perform no segment I/O. The StreamHub fan-out
		// already happened in the RTP callback, so live preview keeps working.
		if r.cfg.RecordEnabled != nil && !*r.cfg.RecordEnabled {
			continue
		}
		if len(data) == 0 {
			continue
		}

		// Frame sampling: only save every Nth frame
		seq := atomic.AddInt64(&r.frameSeq, 1)
		if int(seq)%r.cfg.SampleInterval != 0 {
			continue
		}

		// Check storage health — if failed, skip recording but keep stream alive.
		if isStorageFailed(r.store, r.cfg.CameraID) {
			if r.curTempPath != "" {
				r.closeCurrentSegment()
			}
			if logNow, ok := shouldLogHealth(r.lastHealthLogAt); ok {
				r.lastHealthLogAt = logNow
				mjpegLogger.Warn("storage health failed, skipping recording (stream kept alive)",
					"camera_id", r.cfg.CameraID)
			}
			continue
		}

		if r.curTempPath == "" {
			if r.hasAudio {
				// Create AVI file segment.
				tempPath, finalPath, err := r.store.CreateSegment(r.cfg.CameraID, string(model.FormatAVI))
				if err != nil {
					mjpegLogger.Error("failed to create AVI segment", "camera_id", r.cfg.CameraID, "error", err)
					continue
				}
				w, h, ok := jpegDimensions(data)
				if !ok {
					w, h = 640, 480 // fallback dimensions
				}
				f, err := os.OpenFile(tempPath, os.O_RDWR, 0o644)
				if err != nil {
					mjpegLogger.Error("failed to open AVI file", "camera_id", r.cfg.CameraID, "error", err)
					// Clean up the temp path on failure.
					os.Remove(tempPath)
					continue
				}
				r.aviFile = f
				// aviMuxer is read by the RTP audio callback under r.mu —
				// publish it under the same lock (race found by
				// TestMJPEGRecorderAudioDrop under -race on CI).
				r.mu.Lock()
				r.aviMuxer = avi.NewMuxer(f, w, h, r.g711SampleRate, r.g711MULaw)
				r.mu.Unlock()
				r.curTempPath = tempPath
				r.curFinalPath = finalPath
				r.segStart = time.Now()
				r.frameCount = 0
			} else {
				tempPath, finalPath, err := r.store.CreateSegment(r.cfg.CameraID, string(model.FormatMJPEG))
				if err != nil {
					mjpegLogger.Error("failed to create segment", "camera_id", r.cfg.CameraID, "error", err)
					continue
				}
				r.curTempPath = tempPath
				r.curFinalPath = finalPath
				r.segStart = time.Now()
				r.frameCount = 0
			}
		}

		if r.hasAudio {
			// Write video frame to AVI muxer (nil-check under the lock —
			// segment rotation clears it concurrently from our own
			// closeCurrentSegment, and the audio callback shares the muxer).
			r.mu.Lock()
			m := r.aviMuxer
			if m != nil {
				if err := m.WriteVideo(data, 0); err != nil {
					mjpegLogger.Error("failed to write video to AVI muxer", "camera_id", r.cfg.CameraID, "error", err)
				}
			}
			r.mu.Unlock()
		} else {
			if _, err := r.store.WriteFrame(r.curTempPath, data); err != nil {
				mjpegLogger.Error("failed to write frame", "camera_id", r.cfg.CameraID, "error", err)
				continue
			}
		}
		r.frameCount++

		if time.Since(r.segStart) >= r.cfg.SegmentDur {
			r.closeCurrentSegment()
		}
	}
}

func (r *MJPEGRecorder) closeCurrentSegment() {
	if r.curTempPath == "" {
		return
	}

	// For AVI mode: close muxer and file before renaming.
	if r.hasAudio {
		r.mu.Lock()
		if r.aviMuxer != nil {
			if err := r.aviMuxer.Close(); err != nil {
				mjpegLogger.Error("failed to close AVI muxer", "camera_id", r.cfg.CameraID, "error", err)
			}
			r.aviMuxer = nil
		}
		if r.aviFile != nil {
			if err := r.aviFile.Close(); err != nil {
				mjpegLogger.Error("failed to close AVI file", "camera_id", r.cfg.CameraID, "error", err)
			}
			r.aviFile = nil
		}
		r.mu.Unlock()
	}

	if err := r.store.CloseSegment(r.curTempPath, r.curFinalPath); err != nil {
		mjpegLogger.Error("failed to close segment", "camera_id", r.cfg.CameraID, "error", err)
	}

	// Insert recording entry into database
	var totalSize int64
	var recordingID string
	segFormat := r.segmentFormat()
	if r.cfg.DB != nil && r.curFinalPath != "" && r.frameCount > 0 {
		now := time.Now()
		duration := now.Sub(r.segStart).Seconds()
		rec := &model.Recording{
			ID:         strconv.FormatInt(now.UnixNano(), 10),
			CameraID:   r.cfg.CameraID,
			FilePath:   r.curFinalPath,
			Format:     segFormat,
			StartedAt:  r.segStart,
			EndedAt:    now,
			Duration:   duration,
			FrameCount: r.frameCount,
		}
		recordingID = rec.ID

		if r.hasAudio {
			// AVI is a single file.
			if info, err := os.Stat(r.curFinalPath); err == nil {
				totalSize = info.Size()
			}
		} else {
			// MJPEG finalPath is a directory; walk to calculate total size.
			filepath.Walk(r.curFinalPath, func(path string, info os.FileInfo, err error) error {
				if err == nil && !info.IsDir() {
					totalSize += info.Size()
				}
				return nil
			})
		}
		rec.FileSize = totalSize
		if err := r.cfg.DB.InsertRecordingWithRetry(context.Background(), rec, 3, 500*time.Millisecond); err != nil {
			mjpegLogger.Error("failed to insert recording", "camera_id", r.cfg.CameraID, "error", err)
		}

		// Dark frame detection: check if segment is too dark to be useful.
		// Only runs when DarkFrameFilterEnabled is true and threshold > 0.
		if r.cfg.DarkFrameFilterEnabled && r.cfg.DarkFrameThreshold > 0 && recordingID != "" {
			var isDark bool
			if r.hasAudio {
				// AVI format: single file with MJPEG video.
				isDark, _, _ = DetectDarkAVIFile(r.curFinalPath, r.cfg.DarkFrameThreshold)
			} else {
				// MJPEG format: directory of JPEG files.
				isDark, _, _ = DetectDarkMJPEGDir(r.curFinalPath, r.cfg.DarkFrameThreshold)
			}
			if isDark {
				// Mark as dark so merge and cleanup systems can skip/clean it.
				_ = r.cfg.DB.SetMergeStatus(context.Background(), []string{recordingID}, model.MergeStatusDark)
				mjpegLogger.Info("segment marked as dark (night/no-IR)",
					"camera_id", r.cfg.CameraID, "recording_id", recordingID)
				// Skip publishing SegmentCompleted — dark segments should not
				// enter the merge pipeline at all.
				r.curTempPath = ""
				r.curFinalPath = ""
				r.frameCount = 0
				return
			}
		}
	}

	// Publish SegmentCompleted event.
	formatStr := string(segFormat)
	if r.cfg.EventBus != nil && recordingID != "" {
		r.cfg.EventBus.Publish(context.Background(), event.TopicSegmentCompleted, event.SegmentCompleted{
			CameraID:    r.cfg.CameraID,
			FilePath:    r.curFinalPath,
			Format:      formatStr,
			Encoding:    formatStr,
			StartedAt:   r.segStart.Format(time.RFC3339Nano),
			EndedAt:     time.Now().Format(time.RFC3339Nano),
			FileSize:    totalSize,
			RecordingID: recordingID,
		})
	}

	// Update metrics for completed segment
	if r.frameCount > 0 {
		r.recordSegmentCreated()
	}

	r.curTempPath = ""
	r.curFinalPath = ""
	r.frameCount = 0
}
