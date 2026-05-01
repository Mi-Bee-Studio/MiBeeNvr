package recorder

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"math/rand"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bluenviron/gortsplib/v5"
	"github.com/bluenviron/gortsplib/v5/pkg/base"
	"github.com/bluenviron/gortsplib/v5/pkg/format"
	"github.com/bluenviron/gortsplib/v5/pkg/format/rtph264"
	"github.com/pion/rtp"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/muxer"
)

// SegmentStore abstracts the storage operations needed by the recorder.
// *storage.Manager satisfies this interface.
type SegmentStore interface {
	CreateSegment(cameraID string, fmt string) (tempPath string, finalPath string, err error)
	WriteFrame(tempPath string, data []byte) (int, error)
	CloseSegment(tempPath, finalPath string) error
}

// RecordingDB abstracts database operations needed by the recorder.
type RecordingDB interface {
	InsertRecording(ctx context.Context, r *model.Recording) error
}

const (
	DefaultSegmentDur  = 10 * time.Minute
	DefaultRingBufCap  = 300
	DefaultMaxBackoff  = 60 * time.Second
	DefaultInitBackoff = 1 * time.Second
)

// H264Config holds configuration for the H264 recorder.
type H264Config struct {
	CameraID    string
	RTSPURL     string
	SegmentDur  time.Duration
	RingBufCap  int
	MaxBackoff  time.Duration
	InitBackoff time.Duration
	DB RecordingDB
}

// H264Recorder records H.264 video from an RTSP source.
type H264Recorder struct {
	cfg   H264Config
	store SegmentStore

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

	sps []byte
	pps []byte

	frameCh chan []byte
	dropped atomic.Int64
}

var _ model.Recorder = (*H264Recorder)(nil)

func NewH264Recorder(cfg H264Config, store SegmentStore) *H264Recorder {
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
	return &H264Recorder{
		cfg:    cfg,
		store:  store,
		status: model.StatusStopped,
	}
}

func (r *H264Recorder) Start(ctx context.Context) error {
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

func (r *H264Recorder) Stop() error {
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

func (r *H264Recorder) Status() model.RecorderStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.status
}

func (r *H264Recorder) setStatus(s model.RecorderStatus) {
	r.mu.Lock()
	r.status = s
	r.mu.Unlock()
}

func (r *H264Recorder) run(ctx context.Context) {
	defer close(r.done)
	defer r.setStatus(model.StatusStopped)
	backoff := r.cfg.InitBackoff
	for {
		err := r.connectAndRecord(ctx)
		if ctx.Err() != nil {
			return
		}
		log.Printf("[h264-recorder %s] connection error: %v, reconnecting in %v", r.cfg.CameraID, err, backoff)
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

func (r *H264Recorder) connectAndRecord(ctx context.Context) error {
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
	var forma *format.H264
	medi := desc.FindFormat(&forma)
	if medi == nil {
		return fmt.Errorf("H264 media not found in stream")
	}
	rtpDec, err := forma.CreateDecoder()
	if err != nil {
		return fmt.Errorf("create RTP decoder: %w", err)
	}
	if _, err := client.Setup(desc.BaseURL, medi, 0, 0); err != nil {
		return fmt.Errorf("SETUP: %w", err)
	}

	r.frameCh = make(chan []byte, r.cfg.RingBufCap)
	r.dropped.Store(0)
	writerDone := make(chan struct{})
	go r.writeFrames(writerDone)

	client.OnPacketRTP(medi, forma, func(pkt *rtp.Packet) {
		au, err := rtpDec.Decode(pkt)
		if err != nil {
			if err != rtph264.ErrNonStartingPacketAndNoPrevious && err != rtph264.ErrMorePacketsNeeded {
				log.Printf("[h264-recorder %s] RTP decode error: %v", r.cfg.CameraID, err)
			}
			return
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
					log.Printf("[h264-recorder %s] ring buffer full, dropped %d frames", r.cfg.CameraID, d)
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

func (r *H264Recorder) writeFrames(done chan struct{}) {
	defer close(done)
	for data := range r.frameCh {
		if len(data) < 5 {
			continue
		}
		nalu := data[4:]
		naluType := nalu[0] & 0x1F
		switch naluType {
		case 7:
			if r.sps != nil && !bytes.Equal(r.sps, nalu) {
				log.Printf("[h264-recorder %s] SPS change detected, rotating segment", r.cfg.CameraID)
				r.closeCurrentSegment()
			}
			r.sps = append([]byte(nil), nalu...)
		case 8:
			if r.pps != nil && !bytes.Equal(r.pps, nalu) {
				log.Printf("[h264-recorder %s] PPS change detected, rotating segment", r.cfg.CameraID)
				r.closeCurrentSegment()
			}
			r.pps = append([]byte(nil), nalu...)
		}
		// Only create muxer and write video frames (IDR=5, non-IDR=1)
		if naluType != 5 && naluType != 1 {
			continue
		}
		if r.sps == nil || r.pps == nil {
			continue
		}
		if r.muxer == nil {
			tempPath, finalPath, err := r.store.CreateSegment(r.cfg.CameraID, string(model.FormatH264))
			if err != nil {
				log.Printf("[h264-recorder %s] create segment: %v", r.cfg.CameraID, err)
				continue
			}
			r.muxer = muxer.NewMP4Muxer(finalPath)
			trackID, err := r.muxer.AddH264Track(r.sps, r.pps)
			if err != nil {
				log.Printf("[h264-recorder %s] add H264 track: %v", r.cfg.CameraID, err)
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
		if duration <= 0 {
			duration = 33 * time.Millisecond
		}
		r.lastFrameTime = now
		if err := r.muxer.WriteSample(r.trackID, nalu, pts, duration); err != nil {
			log.Printf("[h264-recorder %s] write sample: %v", r.cfg.CameraID, err)
			continue
		}
		r.frameCount++
		if time.Since(r.segStart) >= r.cfg.SegmentDur {
			r.closeCurrentSegment()
			}
	}
}

func (r *H264Recorder) closeCurrentSegment() {
	if r.muxer == nil {
		return
	}
	if err := r.muxer.Close(); err != nil {
		log.Printf("[h264-recorder %s] close muxer: %v", r.cfg.CameraID, err)
	}

	// Insert recording entry into database
	if r.cfg.DB != nil && r.curFinalPath != "" {
		now := time.Now()
		duration := now.Sub(r.segStart).Seconds()
		rec := &model.Recording{
			ID:         fmt.Sprintf("%d", now.UnixNano()),
			CameraID:   r.cfg.CameraID,
			FilePath:   r.curFinalPath,
			Format:     model.FormatH264,
			StartedAt:  r.segStart,
			EndedAt:    now,
			Duration:   duration,
			FrameCount: r.frameCount,
		}
		if info, err := os.Stat(r.curFinalPath); err == nil {
			rec.FileSize = info.Size()
		}
		if err := r.cfg.DB.InsertRecording(context.Background(), rec); err != nil {
			log.Printf("[h264-recorder %s] insert recording: %v", r.cfg.CameraID, err)
		}
	}

	r.muxer = nil
	r.curFinalPath = ""
	r.frameCount = 0
	// Remove empty temp file created by CreateSegment (muxer writes to finalPath directly)
	if r.curTempPath != "" {
		os.Remove(r.curTempPath)
		r.curTempPath = ""
	}
}
