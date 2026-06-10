package recorder

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bluenviron/gortsplib/v5"
	"github.com/bluenviron/gortsplib/v5/pkg/base"
	"github.com/bluenviron/gortsplib/v5/pkg/format"
	"github.com/pion/rtp"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/metrics"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/event"
)

var mjpegLogger = slog.Default().With("component", "mjpeg-recorder")

// MJPEGConfig holds configuration for the MJPEG recorder.
type MJPEGConfig struct {
	CameraID       string
	RTSPURL        string
	SegmentDur     time.Duration
	SampleInterval int // if >1, only save every Nth frame
	DB             RecordingDB
	MaxBackoff     time.Duration // Deprecated: no longer used, tiered backoff is used instead
	InitBackoff    time.Duration // Deprecated: no longer used, tiered backoff is used instead
	EventBus             *event.EventBus
}

// MJPEGRecorder records Motion-JPEG video from an RTSP source.
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

	frameCh chan []byte
	dropped atomic.Int64
	Hub     *model.StreamHub // Frame fan-out (nil for MJPEG — no HLS support, reserved for future consumers)
	lastHealthLogAt time.Time // throttled log for storage health failures
}

// GetHub returns the StreamHub for frame fan-out.
func (r *MJPEGRecorder) GetHub() *model.StreamHub { return r.Hub }

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
		r.metrics.SegmentsCreated.WithLabelValues(r.cfg.CameraID, "mjpeg").Inc()
	}
}

// recordBytes adds to the recording bytes counter if metrics is available.
func (r *MJPEGRecorder) recordBytes(bytes int64) {
	if r.metrics != nil {
		r.metrics.RecordingBytesTotal.WithLabelValues(r.cfg.CameraID, "mjpeg").Add(float64(bytes))
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
	if cfg.SegmentDur == 0 {
		cfg.SegmentDur = DefaultSegmentDur
	}
	if cfg.SampleInterval <= 0 {
		cfg.SampleInterval = 1
	}
	if cfg.MaxBackoff == 0 {
		cfg.MaxBackoff = DefaultMaxBackoff
	}
	if cfg.InitBackoff == 0 {
		cfg.InitBackoff = DefaultInitBackoff
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
	ctx, cancel := context.WithCancel(context.Background())
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
		if r.metrics != nil {
			r.metrics.CameraReconnectBackoffSeconds.WithLabelValues(r.cfg.CameraID).Set(backoff.Seconds())
		}
		mjpegLogger.Error("connection error, reconnecting", "camera_id", r.cfg.CameraID, "error", err, "backoff", backoff, "attempt", retryCount)
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

	r.frameCh = make(chan []byte, DefaultRingBufCap)
	r.dropped.Store(0)
	r.frameSeq = 0
	writerDone := make(chan struct{})
	go r.writeFrames(writerDone)

	client.OnPacketRTP(medi, forma, func(pkt *rtp.Packet) {
		jpeg, err := rtpDec.Decode(pkt)
		if err != nil {
			mjpegLogger.Error("RTP decode error", "camera_id", r.cfg.CameraID, "error", err)
			return
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
		if len(data) == 0 {
			continue
		}

		// Frame sampling: only save every Nth frame
		seq := atomic.AddInt64(&r.frameSeq, 1)
		if int(seq)%r.cfg.SampleInterval != 0 {
			continue
		}

		// Check storage health — if failed, skip recording but keep stream alive.
		if isStorageFailed(r.store) {
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

		if _, err := r.store.WriteFrame(r.curTempPath, data); err != nil {
			mjpegLogger.Error("failed to write frame", "camera_id", r.cfg.CameraID, "error", err)
			continue
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
	if err := r.store.CloseSegment(r.curTempPath, r.curFinalPath); err != nil {
		mjpegLogger.Error("failed to close segment", "camera_id", r.cfg.CameraID, "error", err)
	}

	// Insert recording entry into database
	var totalSize int64
	var recordingID string
	if r.cfg.DB != nil && r.curFinalPath != "" && r.frameCount > 0 {
		now := time.Now()
		duration := now.Sub(r.segStart).Seconds()
		rec := &model.Recording{
			ID:         fmt.Sprintf("%d", now.UnixNano()),
			CameraID:   r.cfg.CameraID,
			FilePath:   r.curFinalPath,
			Format:     model.FormatMJPEG,
			StartedAt:  r.segStart,
			EndedAt:    now,
			Duration:   duration,
			FrameCount: r.frameCount,
		}
		recordingID = rec.ID
		// Get file size from disk
		// For MJPEG, the finalPath is a directory; calculate total size
		filepath.Walk(r.curFinalPath, func(path string, info os.FileInfo, err error) error {
			if err == nil && !info.IsDir() {
				totalSize += info.Size()
			}
			return nil
		})
		rec.FileSize = totalSize
		if err := r.cfg.DB.InsertRecordingWithRetry(context.Background(), rec, 3, 500*time.Millisecond); err != nil {
			mjpegLogger.Error("failed to insert recording", "camera_id", r.cfg.CameraID, "error", err)
		}
	}

	// Publish SegmentCompleted event.
	if r.cfg.EventBus != nil && recordingID != "" {
		r.cfg.EventBus.Publish(context.Background(), event.TopicSegmentCompleted, event.SegmentCompleted{
			CameraID:    r.cfg.CameraID,
			FilePath:    r.curFinalPath,
			Format:      string(model.FormatMJPEG),
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
