package recorder

import (
	"context"
	"fmt"
	"log"
	"math/rand"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bluenviron/gortsplib/v5"
	"github.com/bluenviron/gortsplib/v5/pkg/base"
	"github.com/bluenviron/gortsplib/v5/pkg/format"
	"github.com/pion/rtp"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
)

// MJPEGConfig holds configuration for the MJPEG recorder.
type MJPEGConfig struct {
	CameraID       string
	RTSPURL        string
	SegmentDur     time.Duration
	SampleInterval int // if >1, only save every Nth frame
	MaxBackoff     time.Duration
	InitBackoff    time.Duration
}

// MJPEGRecorder records Motion-JPEG video from an RTSP source.
type MJPEGRecorder struct {
	cfg   MJPEGConfig
	store SegmentStore

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
}

var _ model.Recorder = (*MJPEGRecorder)(nil)

// NewMJPEGRecorder creates a new MJPEG recorder.
func NewMJPEGRecorder(cfg MJPEGConfig, store SegmentStore) *MJPEGRecorder {
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
		cfg:    cfg,
		store:  store,
		status: model.StatusStopped,
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
		log.Printf("[mjpeg-recorder %s] connection error: %v, reconnecting in %v", r.cfg.CameraID, err, backoff)
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
	client := &gortsplib.Client{
		Scheme: u.Scheme,
		Host:   u.Host,
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
			log.Printf("[mjpeg-recorder %s] RTP decode error: %v", r.cfg.CameraID, err)
			return
		}
		select {
		case r.frameCh <- jpeg:
		default:
			d := r.dropped.Add(1)
			if d%100 == 1 {
				log.Printf("[mjpeg-recorder %s] ring buffer full, dropped %d frames", r.cfg.CameraID, d)
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
				log.Printf("[mjpeg-recorder %s] create segment: %v", r.cfg.CameraID, err)
				continue
			}
			r.curTempPath = tempPath
			r.curFinalPath = finalPath
			r.segStart = time.Now()
			r.frameCount = 0
		}

		if _, err := r.store.WriteFrame(r.curTempPath, data); err != nil {
			log.Printf("[mjpeg-recorder %s] write frame: %v", r.cfg.CameraID, err)
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
		log.Printf("[mjpeg-recorder %s] close segment: %v", r.cfg.CameraID, err)
	}
	r.curTempPath = ""
	r.curFinalPath = ""
	r.frameCount = 0
}
