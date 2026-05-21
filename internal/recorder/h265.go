package recorder

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"math/rand"
	"os"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
	"net/url"

	"github.com/bluenviron/gortsplib/v5"
	"github.com/bluenviron/gortsplib/v5/pkg/base"
	"github.com/bluenviron/gortsplib/v5/pkg/format"
	"github.com/bluenviron/gortsplib/v5/pkg/format/rtph265"
	"github.com/pion/rtp"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/metrics"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/muxer"
)

var h265Logger = slog.Default().With("component", "h265-recorder")

// H265Config holds configuration for the H265 recorder.
type H265Config struct {
	CameraID    string
	RTSPURL     string
	Username    string
	Password    string
	SegmentDur  time.Duration
	RingBufCap  int
	MaxBackoff  time.Duration
	InitBackoff time.Duration
	DB RecordingDB
}

// H265Recorder records H.265/HEVC video from an RTSP source.
type H265Recorder struct {
	cfg   H265Config
	store SegmentStore
	metrics *metrics.Metrics

	mu     sync.Mutex
	status model.RecorderStatus
	cancel context.CancelFunc
	done   chan struct{}

	muxer   *muxer.MP4Muxer
	trackID int

	curFinalPath string
	curTempPath  string
	segStart     time.Time
	frameCount   int
	lastFrameTime time.Time

	vps []byte
	sps []byte
	pps []byte
	Hub *model.StreamHub // Frame fan-out to multiple consumers (HLS, WebRTC, etc.)

	frameCh chan []byte
	dropped atomic.Int64
}

// VPS returns the current H265 Video Parameter Set NAL unit (without start bytes).
func (r *H265Recorder) VPS() []byte { return r.vps }

// SPS returns the current H265 Sequence Parameter Set NAL unit (without start bytes).
func (r *H265Recorder) SPS() []byte { return r.sps }

// PPS returns the current H265 Picture Parameter Set NAL unit (without start bytes).
func (r *H265Recorder) PPS() []byte { return r.pps }

// incActive increments the active recordings gauge if metrics is available.
func (r *H265Recorder) incActive() {
	if r.metrics != nil {
		r.metrics.ActiveRecordings.Inc()
	}
}

// decActive decrements the active recordings gauge if metrics is available.
func (r *H265Recorder) decActive() {
	if r.metrics != nil {
		r.metrics.ActiveRecordings.Dec()
	}
}

// recordSegmentCreated increments the segments created counter if metrics is available.
func (r *H265Recorder) recordSegmentCreated() {
	if r.metrics != nil {
		r.metrics.SegmentsCreated.WithLabelValues(r.cfg.CameraID, "h265").Inc()
	}
}

// recordBytes adds to the recording bytes counter if metrics is available.
func (r *H265Recorder) recordBytes(bytes int64) {
	if r.metrics != nil {
		r.metrics.RecordingBytesTotal.WithLabelValues(r.cfg.CameraID, "h265").Add(float64(bytes))
	}
}

// recordError increments the camera errors counter if metrics is available.
func (r *H265Recorder) recordError(errorType string) {
	if r.metrics != nil {
		r.metrics.CameraErrors.WithLabelValues(r.cfg.CameraID, errorType).Inc()
	}
}

var _ model.Recorder = (*H265Recorder)(nil)

func NewH265Recorder(cfg H265Config, store SegmentStore, opts ...*metrics.Metrics) *H265Recorder {
	var m *metrics.Metrics
	if len(opts) > 0 {
		m = opts[0]
	}
	if cfg.SegmentDur == 0 {
		cfg.SegmentDur = DefaultSegmentDur
	}
	if cfg.RingBufCap == 0 {
		cfg.RingBufCap = DefaultRingBufCap
	}
	if cfg.MaxBackoff == 0 {
		cfg.MaxBackoff = DefaultMaxBackoff
	}
	if cfg.InitBackoff == 0 {
		cfg.InitBackoff = DefaultInitBackoff
	}
	return &H265Recorder{
		cfg:     cfg,
		store:   store,
		metrics: m,
		status:  model.StatusStopped,
	}
}

func (r *H265Recorder) Start(ctx context.Context) error {
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

func (r *H265Recorder) Stop() error {
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

func (r *H265Recorder) Status() model.RecorderStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.status
}

func (r *H265Recorder) setStatus(s model.RecorderStatus) {
	r.mu.Lock()
	r.status = s
	r.mu.Unlock()
}

func (r *H265Recorder) run(ctx context.Context) {
	defer func() {
		if panicErr := recover(); panicErr != nil {
			buf := make([]byte, 4096)
			buf = buf[:runtime.Stack(buf, false)]
			h265Logger.Error("PANIC recovered in run", "camera_id", r.cfg.CameraID, "panic", panicErr, "stack", string(buf))
		}
	}()
	defer close(r.done)
	defer r.setStatus(model.StatusStopped)
	backoff := r.cfg.InitBackoff
	for {
		err := r.connectAndRecord(ctx)
		if ctx.Err() != nil {
			return
		}
		h265Logger.Error("connection error, reconnecting", "camera_id", r.cfg.CameraID, "error", err, "backoff", backoff)
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

func (r *H265Recorder) connectAndRecord(ctx context.Context) error {
	u, err := base.ParseURL(r.cfg.RTSPURL)
	if err != nil {
		return fmt.Errorf("invalid RTSP URL: %w", err)
	}
	// Inject credentials from config if URL doesn't have them.
	if u.User == nil && r.cfg.Username != "" {
		u.User = url.UserPassword(r.cfg.Username, r.cfg.Password)
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
	var forma *format.H265
	medi := desc.FindFormat(&forma)
	if medi == nil {
		return fmt.Errorf("H265 media not found in stream")
	}
	rtpDec, err := forma.CreateDecoder()
	if err != nil {
		return fmt.Errorf("create RTP decoder: %w", err)
	}
	if _, err := client.Setup(desc.BaseURL, medi, 0, 0); err != nil {
		return fmt.Errorf("SETUP: %w", err)
	}

	// Store initial parameter sets from SDP
	if forma.VPS != nil {
		r.vps = append([]byte(nil), forma.VPS...)
	}
	if forma.SPS != nil {
		r.sps = append([]byte(nil), forma.SPS...)
	}
	if forma.PPS != nil {
		r.pps = append([]byte(nil), forma.PPS...)
	}

	r.frameCh = make(chan []byte, r.cfg.RingBufCap)
	r.dropped.Store(0)
	writerDone := make(chan struct{})
	go r.writeFrames(writerDone)

	client.OnPacketRTP(medi, forma, func(pkt *rtp.Packet) {
		au, err := rtpDec.Decode(pkt)
		if err != nil {
			if err != rtph265.ErrNonStartingPacketAndNoPrevious && err != rtph265.ErrMorePacketsNeeded {
				h265Logger.Error("RTP decode error", "camera_id", r.cfg.CameraID, "error", err)
			}
			return
		}
		// Fan-out to all stream consumers (HLS, WebRTC, etc.)
		if r.Hub != nil {
			r.Hub.Broadcast(int64(pkt.Timestamp), au)
		}
		for _, nalu := range au {
			data := make([]byte, 4+len(nalu))
			copy(data, []byte{0x00, 0x00, 0x00, 0x01})
			copy(data[4:], nalu)
			select {
			case r.frameCh <- data:
			default:
				d := r.dropped.Add(1)
				if d%100 == 1 {
					h265Logger.Warn("ring buffer full, dropped frames", "camera_id", r.cfg.CameraID, "dropped", d)
				}
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

func (r *H265Recorder) writeFrames(done chan struct{}) {
	defer func() {
		if panicErr := recover(); panicErr != nil {
			buf := make([]byte, 4096)
			buf = buf[:runtime.Stack(buf, false)]
			h265Logger.Error("PANIC recovered in writeFrames", "camera_id", r.cfg.CameraID, "panic", panicErr, "stack", string(buf))
		}
	}()

	defer close(done)
	for data := range r.frameCh {
		if len(data) < 6 {
			continue
		}
		nalu := data[4:]
		// HEVC NALU type: 2-byte header, type is in bits 1-6 of first byte
		// forbidden_zero_bit(1) | nal_unit_type(6) | nuh_layer_id(6) | nuh_temporal_id_plus1(3)
		naluType := (nalu[0] >> 1) & 0x3F
		switch naluType {
		case 32: // VPS
			if r.vps != nil && !bytes.Equal(r.vps, nalu) {
				h265Logger.Info("VPS change detected, rotating segment", "camera_id", r.cfg.CameraID)
				r.closeCurrentSegment()
			}
			r.vps = append([]byte(nil), nalu...)
		case 33: // SPS
			if r.sps != nil && !bytes.Equal(r.sps, nalu) {
				h265Logger.Info("SPS change detected, rotating segment", "camera_id", r.cfg.CameraID)
				r.closeCurrentSegment()
			}
			r.sps = append([]byte(nil), nalu...)
		case 34: // PPS
			if r.pps != nil && !bytes.Equal(r.pps, nalu) {
				h265Logger.Info("PPS change detected, rotating segment", "camera_id", r.cfg.CameraID)
				r.closeCurrentSegment()
			}
			r.pps = append([]byte(nil), nalu...)
		}
		// Only write VCL NALUs (slice segments). HEVC VCL types are 0-31, non-VCL are 32+.
		// Skip parameter sets (VPS=32, SPS=33, PPS=34) and other non-VCL types.
		if naluType >= 32 {
			continue
		}
		if r.vps == nil || r.sps == nil || r.pps == nil {
			continue
		}
		// Wait for an IDR frame before starting a new segment.
		// Without this, segments may start with P-frames that have no reference,
		// causing black/gray output until the first IDR appears.
		// HEVC IDR types: 19 (IDR_W_RADL), 20 (IDR_N_LP).
		if r.muxer == nil && naluType != 19 && naluType != 20 {
			continue
		}
		if r.muxer == nil {
			tempPath, finalPath, err := r.store.CreateSegment(r.cfg.CameraID, string(model.FormatH265))
			if err != nil {
				h265Logger.Error("failed to create segment", "camera_id", r.cfg.CameraID, "error", err)
				continue
			}
			r.muxer = muxer.NewMP4Muxer(tempPath)
			trackID, err := r.muxer.AddH265Track(r.vps, r.sps, r.pps)
			if err != nil {
				h265Logger.Error("failed to add H265 track", "camera_id", r.cfg.CameraID, "error", err)
				r.muxer = nil
				// Clean up empty temp file on muxer init failure
				os.Remove(tempPath)
				continue
			}
			r.trackID = trackID
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
			h265Logger.Error("failed to write sample", "camera_id", r.cfg.CameraID, "error", err)
			continue
		}
		r.frameCount++
		if time.Since(r.segStart) >= r.cfg.SegmentDur {
			r.closeCurrentSegment()
		}
	}
}

func (r *H265Recorder) closeCurrentSegment() {
	if r.muxer == nil {
		return
	}
	if err := r.muxer.Close(); err != nil {
		h265Logger.Error("failed to close muxer", "camera_id", r.cfg.CameraID, "error", err)
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
			h265Logger.Error("failed to close segment", "camera_id", r.cfg.CameraID, "error", err)
		}
	}

	// Insert recording entry into database
	var fileSize int64
	if r.cfg.DB != nil && r.curFinalPath != "" {
		now := time.Now()
		duration := now.Sub(r.segStart).Seconds()
		rec := &model.Recording{
			ID:         fmt.Sprintf("%d", now.UnixNano()),
			CameraID:   r.cfg.CameraID,
			FilePath:   r.curFinalPath,
			Format:     model.FormatH265,
			StartedAt:  r.segStart,
			EndedAt:    now,
			Duration:   duration,
			FrameCount: r.frameCount,
		}
		if info, err := os.Stat(r.curFinalPath); err == nil {
			fileSize = info.Size()
			rec.FileSize = fileSize
		}
		if err := r.cfg.DB.InsertRecordingWithRetry(context.Background(), rec, 3, 500*time.Millisecond); err != nil {
			h265Logger.Error("failed to insert recording", "camera_id", r.cfg.CameraID, "error", err)
		}
	}

	// Update metrics for completed segment
	if r.frameCount > 0 && r.curFinalPath != "" {
		r.recordSegmentCreated()
		if fileSize > 0 {
			r.recordBytes(fileSize)
		}
	}

	r.muxer = nil
	r.curTempPath = ""
	r.curFinalPath = ""
	r.frameCount = 0
}
