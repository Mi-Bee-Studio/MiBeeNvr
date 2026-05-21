package recorder
import (
	"context"
	"fmt"
	"log/slog"
	"math/rand"
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

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/metrics"
)


var mjpegLogger = slog.Default().With("component", "mjpeg-recorder")


// MJPEGConfig holds configuration for the MJPEG recorder.
type MJPEGConfig struct {
	CameraID       string
	RTSPURL        string
	SegmentDur     time.Duration
	SampleInterval int // if >1, only save every Nth frame
	DB             RecordingDB
	MaxBackoff     time.Duration
	InitBackoff    time.Duration
}

// MJPEGRecorder records Motion-JPEG video from an RTSP source.
type MJPEGRecorder struct {
	cfg   MJPEGConfig
	store SegmentStore
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
	Hub *model.StreamHub // Frame fan-out (nil for MJPEG — no HLS support, reserved for future consumers)
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
	backoff := r.cfg.InitBackoff
	for {
		err := r.connectAndRecord(ctx)
		if ctx.Err() != nil {
			return
		}
		mjpegLogger.Error("connection error, reconnecting", "camera_id", r.cfg.CameraID, "error", err, "backoff", backoff)
		r.recordError("connection")
		r.setStatus(model.StatusReconnecting)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		jitter := time.Duration(rand.Int63n(int64(backoff / 2)))
		backoff = backoff*2 + jitter
		if backoff > r.cfg.MaxBackoff {
			backoff = r.cfg.MaxBackoff
		}
	}
}

func (r *MJPEGRecorder) connectAndRecord(ctx context.Context) error {
	u, err := base.ParseURL(r.cfg.RTSPURL)
	if err != nil {
		return fmt.Errorf("invalid RTSP URL: %w", err)
	}
	tcp := gortsplib.ProtocolTCP
	client := &gortsplib.Client{
		Scheme:   u.Scheme,
		Host:     u.Host,
		Protocol: &tcp,
	}
	if err := client.Start(); err != nil {
		return fmt.Errorf("client start: %w", err)
	}
	defer client.Close()


	desc, _, err := client.Describe(u)
	if err != nil {
		return fmt.Errorf("DESCRIBE: %w", err)
	}

	var forma *format.MJPEG
	medi := desc.FindFormat(&forma)
	if medi == nil {
		return fmt.Errorf("MJPEG media not found in stream")
	}

	rtpDec, err := forma.CreateDecoder()
	if err != nil {
		return fmt.Errorf("create RTP decoder: %w", err)
	}

	if _, err := client.Setup(desc.BaseURL, medi, 0, 0); err != nil {
		return fmt.Errorf("SETUP: %w", err)
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
			if d%100 == 1 {
					mjpegLogger.Warn("ring buffer full, dropped frames", "camera_id", r.cfg.CameraID, "dropped", d)
			}
		}
	})

	r.setStatus(model.StatusRecording)
	if _, err := client.Play(nil); err != nil {
		close(r.frameCh)
		<-writerDone
		return fmt.Errorf("PLAY: %w", err)
	}

	errCh := make(chan error, 1)
	go func() { errCh <- client.Wait() }()

	select {
	case err := <-errCh:
		close(r.frameCh)
		<-writerDone
		r.closeCurrentSegment()
		return err
	case <-ctx.Done():
		client.Close()
		close(r.frameCh)
		<-writerDone
		r.closeCurrentSegment()
		return ctx.Err()
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
		// Get file size from disk
		// For MJPEG, the finalPath is a directory; calculate total size
		var totalSize int64
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

	// Update metrics for completed segment
	if r.frameCount > 0 {
		r.recordSegmentCreated()
	}

	r.curTempPath = ""
	r.curFinalPath = ""
	r.frameCount = 0
}
