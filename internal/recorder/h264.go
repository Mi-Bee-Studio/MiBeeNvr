package recorder

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bluenviron/gortsplib/v5"
	"github.com/bluenviron/gortsplib/v5/pkg/base"
	"github.com/bluenviron/gortsplib/v5/pkg/description"
	"github.com/bluenviron/gortsplib/v5/pkg/format"
	"github.com/bluenviron/gortsplib/v5/pkg/format/rtph264"
	"github.com/bluenviron/gortsplib/v5/pkg/format/rtplpcm"
	"github.com/bluenviron/gortsplib/v5/pkg/format/rtpmpeg4audio"
	"github.com/pion/rtp"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/event"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/metrics"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model/nalutil"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/muxer"
)

var h264Logger = slog.Default().With("component", "h264-recorder")

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
	InsertRecordingWithRetry(ctx context.Context, r *model.Recording, maxRetries int, backoff time.Duration) error
}

const (
	DefaultSegmentDur           = 10 * time.Minute
	DefaultRingBufCap           = 300
	defaultFrameWatchdogTimeout = 30 * time.Second // Max wait for frame data before reconnecting
)

// H264Config holds configuration for the H264 recorder.
type H264Config struct {
	CameraID             string
	RTSPURL              string
	Username             string
	Password             string
	SegmentDur           time.Duration
	RingBufCap           int
	DB                   RecordingDB
	AudioEnabled         bool
	FrameWatchdogTimeout time.Duration // default 30s (0 = use constant default)
	EventBus             *event.EventBus
}

// H264Recorder records H.264 video from an RTSP source.
type H264Recorder struct {
	cfg     H264Config
	store   SegmentStore
	metrics *metrics.Metrics

	mu     sync.Mutex
	status model.RecorderStatus
	cancel context.CancelFunc
	done   chan struct{}

	muxer            *muxer.MP4Muxer
	trackID          int
	audioTrackID     int
	audioMuxerConfig []byte // AudioSpecificConfig for AAC muxer track
	audioCodec       string // "aac" or "g711"
	g711MULaw        bool   // true=μ-law, false=A-law
	g711SampleRate   int    // typically 8000
	audioSampleRate  int    // unified sample rate (Hz), set for both AAC and G.711
	audioChannels    int    // number of audio channels, 0 when no audio

	curFinalPath  string
	curTempPath   string
	segStart      time.Time
	frameCount    int
	lastFrameTime time.Time

	sps []byte
	pps []byte

	frameCh chan []byte
	dropped atomic.Int64
	lastPTS atomic.Int64 // tracks last RTP PTS for monotonicity check

	Hub             *model.StreamHub // Frame fan-out to multiple consumers (HLS, WebRTC, etc.)
	lastHealthLogAt time.Time        // throttled log for storage health failures
}

// GetHub returns the StreamHub for frame fan-out.
func (r *H264Recorder) GetHub() *model.StreamHub { return r.Hub }

// SPS returns the current H264 Sequence Parameter Set NAL unit (without start bytes).
func (r *H264Recorder) SPS() []byte { return r.sps }

// PPS returns the current H264 Picture Parameter Set NAL unit (without start bytes).
func (r *H264Recorder) PPS() []byte { return r.pps }

// AudioCodec returns the audio codec name ("aac", "g711", or "" for no audio).
func (r *H264Recorder) AudioCodec() string { return r.audioCodec }

// AudioConfig returns the audio codec configuration bytes.
// For AAC: AudioSpecificConfig from marshal'd MPEG4Audio config.
// For G.711: 5 bytes [muLawFlag, rate>>24, rate>>16, rate>>8, rate].
func (r *H264Recorder) AudioConfig() []byte { return r.audioMuxerConfig }

// AudioSampleRate returns the audio sample rate in Hz, or 0 if no audio.
func (r *H264Recorder) AudioSampleRate() int { return r.audioSampleRate }

// AudioChannels returns the number of audio channels, or 0 if no audio.
func (r *H264Recorder) AudioChannels() int { return r.audioChannels }

// incActive increments the active recordings gauge if metrics is available.
func (r *H264Recorder) incActive() {
	if r.metrics != nil {
		r.metrics.ActiveRecordings.Inc()
	}
}

// decActive decrements the active recordings gauge if metrics is available.
func (r *H264Recorder) decActive() {
	if r.metrics != nil {
		r.metrics.ActiveRecordings.Dec()
	}
}

// recordSegmentCreated increments the segments created counter if metrics is available.
func (r *H264Recorder) recordSegmentCreated() {
	if r.metrics != nil {
		r.metrics.SegmentsCreated.WithLabelValues(r.cfg.CameraID, "h264").Inc()
	}
}

// recordBytes adds to the recording bytes counter if metrics is available.
func (r *H264Recorder) recordBytes(bytes int64) {
	if r.metrics != nil {
		r.metrics.RecordingBytesTotal.WithLabelValues(r.cfg.CameraID, "h264").Add(float64(bytes))
	}
}

// recordError increments the camera errors counter if metrics is available.
func (r *H264Recorder) recordError(errorType string) {
	if r.metrics != nil {
		r.metrics.CameraErrors.WithLabelValues(r.cfg.CameraID, errorType).Inc()
	}
}

var _ model.Recorder = (*H264Recorder)(nil)

func NewH264Recorder(cfg H264Config, store SegmentStore, opts ...*metrics.Metrics) *H264Recorder {
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
	if cfg.FrameWatchdogTimeout == 0 {
		cfg.FrameWatchdogTimeout = defaultFrameWatchdogTimeout
	}
	return &H264Recorder{
		cfg:     cfg,
		store:   store,
		metrics: m,
		status:  model.StatusStopped,
	}
}

func (r *H264Recorder) Start(ctx context.Context) error {
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

func (r *H264Recorder) Stop() error {
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
	defer func() {
		if panicErr := recover(); panicErr != nil {
			buf := make([]byte, 4096)
			buf = buf[:runtime.Stack(buf, false)]
			h264Logger.Error("PANIC recovered in run", "camera_id", r.cfg.CameraID, "panic", panicErr, "stack", string(buf))
		}
	}()
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
		h264Logger.Error("connection error, reconnecting", "camera_id", r.cfg.CameraID, "error", err, "backoff", backoff, "attempt", retryCount)
		r.recordError("connection")
		r.setStatus(model.StatusReconnecting)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
	}
}

func (r *H264Recorder) connectAndRecord(ctx context.Context) (error, bool) {
	u, err := base.ParseURL(r.cfg.RTSPURL)
	if err != nil {
		return fmt.Errorf("invalid RTSP URL: %w", err), false
	}
	// Inject credentials from config if URL doesn't have them.
	if u.User == nil && r.cfg.Username != "" {
		u.User = url.UserPassword(r.cfg.Username, r.cfg.Password)
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
	var forma *format.H264
	medi := desc.FindFormat(&forma)
	if medi == nil {
		return fmt.Errorf("H264 media not found in stream"), false
	}
	rtpDec, err := forma.CreateDecoder()
	if err != nil {
		return fmt.Errorf("create RTP decoder: %w", err), false
	}
	if _, err := client.Setup(desc.BaseURL, medi, 0, 0); err != nil {
		return fmt.Errorf("SETUP: %w", err), false
	}

	// Store initial parameter sets from SDP.
	// This ensures SPS/PPS are available even if the RTSP source does not
	// repeat them in-band before each IDR frame (common in lightweight implementations).
	if forma.SPS != nil {
		r.sps = append([]byte(nil), forma.SPS...)
	}
	if forma.PPS != nil {
		r.pps = append([]byte(nil), forma.PPS...)
	}

	// Audio setup: find AAC or G.711 format if AudioEnabled.
	var audioDec *rtpmpeg4audio.Decoder
	var audioForma *format.MPEG4Audio
	var g711Dec *rtplpcm.Decoder
	var g711Forma *format.G711
	var audioMedi *description.Media
	if r.cfg.AudioEnabled {
		// Try AAC first
		audioMedi = desc.FindFormat(&audioForma)
		if audioMedi != nil {
			ad, err := audioForma.CreateDecoder()
			if err != nil {
				h264Logger.Warn("audio decoder creation failed, continuing video-only", "camera_id", r.cfg.CameraID, "error", err)
			} else {
				audioDec = ad
				if _, err := client.Setup(desc.BaseURL, audioMedi, 0, 1); err != nil {
					h264Logger.Warn("audio SETUP failed, continuing video-only", "camera_id", r.cfg.CameraID, "error", err)
					audioDec = nil
				} else {
					if audioForma.Config != nil {
						if enc, err := audioForma.Config.Marshal(); err == nil {
							r.audioMuxerConfig = enc
						}
					}
					r.audioCodec = "aac"
					r.audioSampleRate = audioForma.Config.SampleRate
					// ChannelConfig 0 means PCE-defined; fall back to 1 as minimal default.
					aCh := int(audioForma.Config.ChannelConfig)
					if aCh == 0 {
						aCh = 1
					}
					r.audioChannels = aCh
					h264Logger.Info("AAC audio track detected", "camera_id", r.cfg.CameraID)
				}
			}
		}
		// If no AAC, try G.711
		if audioDec == nil {
			audioMedi = desc.FindFormat(&g711Forma)
			if audioMedi != nil {
				dec := &rtplpcm.Decoder{BitDepth: 8, ChannelCount: 1}
				if err := dec.Init(); err != nil {
					h264Logger.Warn("G.711 decoder init failed", "camera_id", r.cfg.CameraID, "error", err)
				} else {
					g711Dec = dec
					if _, err := client.Setup(desc.BaseURL, audioMedi, 0, 1); err != nil {
						h264Logger.Warn("G.711 audio SETUP failed, continuing video-only", "camera_id", r.cfg.CameraID, "error", err)
						g711Dec = nil
					} else {
						r.audioCodec = "g711"
						r.g711MULaw = g711Forma.MULaw
						r.g711SampleRate = g711Forma.SampleRate
						r.audioSampleRate = g711Forma.SampleRate
						// G.711 decoder hardcodes ChannelCount:1 during init.
						r.audioChannels = 1
						muLawByte := byte(0)
						if g711Forma.MULaw {
							muLawByte = 1
						}
						rate := g711Forma.SampleRate
						r.audioMuxerConfig = []byte{muLawByte, byte(rate >> 24), byte(rate >> 16), byte(rate >> 8), byte(rate)}
						h264Logger.Info("G.711 audio track detected", "camera_id", r.cfg.CameraID, "mulaw", g711Forma.MULaw, "rate", rate)
					}
				}
			}
		}
		if audioDec == nil && g711Dec == nil {
			h264Logger.Debug("no supported audio format found in stream", "camera_id", r.cfg.CameraID)
		}
	}

	frameAlive := make(chan struct{}, 1)
	r.frameCh = make(chan []byte, r.cfg.RingBufCap)
	r.dropped.Store(0)
	writerDone := make(chan struct{})
	go r.writeFrames(writerDone)

	client.OnPacketRTP(medi, forma, func(pkt *rtp.Packet) {
		au, err := rtpDec.Decode(pkt)
		if err != nil {
			if err != rtph264.ErrNonStartingPacketAndNoPrevious && err != rtph264.ErrMorePacketsNeeded {
				h264Logger.Error("RTP decode error", "camera_id", r.cfg.CameraID, "error", err)
			}
			return
		}
		// Signal frame received for watchdog
		select {
		case frameAlive <- struct{}{}:
		default:
		}
		// PTS monotonicity check — warn only, never drop frames
		if prevPTS := r.lastPTS.Load(); prevPTS > 0 {
			if result := checkPTSMonotonicity(prevPTS, int64(pkt.Timestamp)); result.Anomaly != ptsAnomalyNone {
				logPTSAnomaly(h264Logger, r.cfg.CameraID, result)
			}
		}
		r.lastPTS.Store(int64(pkt.Timestamp))
		// Fan-out to all stream consumers (HLS, WebRTC, etc.)
		if r.Hub != nil {
			r.Hub.Broadcast(int64(pkt.Timestamp), au, nalutil.IsIDR(au, false))
		}
		for _, nalu := range au {
			data := make([]byte, 4+len(nalu))
			copy(data, []byte{0x00, 0x00, 0x00, 0x01})
			copy(data[4:], nalu)
			select {
			case r.frameCh <- data:
			default:
				d := r.dropped.Add(1)
				if r.metrics != nil {
					r.metrics.RecorderRingBufferDropsTotal.WithLabelValues(r.cfg.CameraID).Inc()
				}
				if d%100 == 1 {
					h264Logger.Warn("ring buffer full, dropped frames", "camera_id", r.cfg.CameraID, "dropped", d)
				}
			}
		}
	})

	// Audio RTP handler.
	if audioDec != nil {
		// AAC handler
		client.OnPacketRTP(audioMedi, audioForma, func(pkt *rtp.Packet) {
			aus, err := audioDec.Decode(pkt)
			if err != nil {
				if err != rtpmpeg4audio.ErrMorePacketsNeeded {
					h264Logger.Error("audio RTP decode error", "camera_id", r.cfg.CameraID, "error", err)
				}
				return
			}
			for _, aacData := range aus {
				if r.Hub != nil {
					r.Hub.BroadcastAudio(int64(pkt.Timestamp), model.AudioAAC, aacData)
				}
				r.mu.Lock()
				m := r.muxer
				aid := r.audioTrackID
				start := r.segStart
				r.mu.Unlock()
				if m != nil && aid > 0 {
					pts := time.Since(start)
					dur := 1024 * time.Second / time.Duration(audioForma.ClockRate())
					if err := m.WriteAudioSample(aid, aacData, pts, dur); err != nil {
						if err.Error() != "muxer is closed" {
							h264Logger.Error("failed to write audio sample", "camera_id", r.cfg.CameraID, "error", err)
						}
					}
				}
			}
		})
	} else if g711Dec != nil {
		// G.711 handler
		client.OnPacketRTP(audioMedi, g711Forma, func(pkt *rtp.Packet) {
			data, err := g711Dec.Decode(pkt)
			if err != nil {
				h264Logger.Error("G.711 RTP decode error", "camera_id", r.cfg.CameraID, "error", err)
				return
			}
			if r.Hub != nil {
				r.Hub.BroadcastAudio(int64(pkt.Timestamp), model.AudioG711, data)
			}
			r.mu.Lock()
			m := r.muxer
			aid := r.audioTrackID
			start := r.segStart
			r.mu.Unlock()
			if m != nil && aid > 0 {
				pts := time.Since(start)
				dur := time.Duration(len(data)) * time.Second / time.Duration(r.g711SampleRate)
				if dur < time.Millisecond {
					dur = time.Millisecond
				}
				if err := m.WriteAudioSample(aid, data, pts, dur); err != nil {
					if err.Error() != "muxer is closed" {
						h264Logger.Error("failed to write G.711 audio sample", "camera_id", r.cfg.CameraID, "error", err)
					}
				}
			}
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

	// Frame watchdog: detect "RTSP alive but no data" state.
	// When gortsplib receives RTSP keep-alives (GET_PARAMETER), it resets the
	// ReadTimeout, so client.Wait() can block indefinitely even with no frames.
	// The watchdog closes the connection if no frame arrives within the timeout.
	stopWatchdog := make(chan struct{})
	watchdogDone := make(chan struct{})
	go func() {
		defer close(watchdogDone)
		watchdog := time.NewTimer(r.cfg.FrameWatchdogTimeout)
		defer watchdog.Stop()
		for {
			select {
			case <-frameAlive:
				if !watchdog.Stop() {
					<-watchdog.C
				}
				watchdog.Reset(r.cfg.FrameWatchdogTimeout)
			case <-watchdog.C:
				h264Logger.Warn("frame watchdog timeout, closing connection",
					"camera_id", r.cfg.CameraID, "timeout", r.cfg.FrameWatchdogTimeout)
				client.Close()
				return
			case <-stopWatchdog:
				return
			case <-ctx.Done():
				return
			}
		}
	}()

	select {
	case err := <-errCh:
		close(stopWatchdog)
		<-watchdogDone
		close(r.frameCh)
		<-writerDone
		r.closeCurrentSegment()
		return err, true
	case <-ctx.Done():
		close(stopWatchdog)
		<-watchdogDone
		client.Close()
		close(r.frameCh)
		<-writerDone
		r.closeCurrentSegment()
		return ctx.Err(), true
	}
}

func (r *H264Recorder) writeFrames(done chan struct{}) {
	defer func() {
		if panicErr := recover(); panicErr != nil {
			buf := make([]byte, 4096)
			buf = buf[:runtime.Stack(buf, false)]
			h264Logger.Error("PANIC recovered in writeFrames", "camera_id", r.cfg.CameraID, "panic", panicErr, "stack", string(buf))
		}
	}()

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
				h264Logger.Info("SPS change detected, rotating segment", "camera_id", r.cfg.CameraID)
				r.closeCurrentSegment()
			}
			r.sps = append([]byte(nil), nalu...)
		case 8:
			if r.pps != nil && !bytes.Equal(r.pps, nalu) {
				h264Logger.Info("PPS change detected, rotating segment", "camera_id", r.cfg.CameraID)
				r.closeCurrentSegment()
			}
			r.pps = append([]byte(nil), nalu...)
		}
		// Only write video frames (IDR=5, non-IDR=1)
		if naluType != 5 && naluType != 1 {
			continue
		}

		// Check storage health — if failed, skip recording but keep stream alive.
		if isStorageFailed(r.store) {
			// Close any existing segment cleanly without storage I/O.
			if r.muxer != nil {
				r.muxer.Close()
				os.Remove(r.curTempPath)
				r.muxer = nil
				r.curTempPath = ""
				r.curFinalPath = ""
				r.audioTrackID = 0
				r.frameCount = 0
			}
			if logNow, ok := shouldLogHealth(r.lastHealthLogAt); ok {
				r.lastHealthLogAt = logNow
				h264Logger.Warn("storage health failed, skipping recording (stream kept alive)",
					"camera_id", r.cfg.CameraID)
			}
			continue
		}

		if r.sps == nil || r.pps == nil {
			continue
		}
		// Wait for an IDR frame before starting a new segment.
		// Without this, segments may start with P-frames that have no reference,
		// causing black/gray output until the first IDR appears.
		if r.muxer == nil && naluType != 5 {
			continue
		}
		if r.muxer == nil {
			tempPath, finalPath, err := r.store.CreateSegment(r.cfg.CameraID, string(model.FormatH264))
			if err != nil {
				h264Logger.Error("failed to create segment", "camera_id", r.cfg.CameraID, "error", err)
				continue
			}
			m := muxer.NewMP4Muxer(tempPath)
			trackID, err := m.AddH264Track(r.sps, r.pps)
			if err != nil {
				h264Logger.Error("failed to add H264 track", "camera_id", r.cfg.CameraID, "error", err)
				// Clean up empty temp file on muxer init failure
				os.Remove(tempPath)
				continue
			}
			r.trackID = trackID
			// Add audio track if audio config is available.
			if len(r.audioMuxerConfig) > 0 && r.audioCodec != "" {
				aID, err := m.AddAudioTrack(r.audioCodec, r.audioMuxerConfig)
				if err != nil {
					h264Logger.Error("failed to add audio track", "camera_id", r.cfg.CameraID, "codec", r.audioCodec, "error", err)
				} else {
					r.audioTrackID = aID
				}
			}
			r.mu.Lock()
			r.muxer = m
			r.segStart = time.Now()
			r.mu.Unlock()
			r.curTempPath = tempPath
			r.curFinalPath = finalPath
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
			h264Logger.Error("failed to write sample", "camera_id", r.cfg.CameraID, "error", err)
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
		h264Logger.Error("failed to close muxer", "camera_id", r.cfg.CameraID, "error", err)
		if r.curTempPath != "" {
			os.Remove(r.curTempPath)
		}
		r.mu.Lock()
		r.muxer = nil
		r.audioTrackID = 0
		r.mu.Unlock()
		r.curTempPath = ""
		r.curFinalPath = ""
		r.frameCount = 0
		return
	}

	// Atomic rename: temp → final
	if r.curTempPath != "" && r.curFinalPath != "" {
		if err := r.store.CloseSegment(r.curTempPath, r.curFinalPath); err != nil {
			h264Logger.Error("failed to close segment", "camera_id", r.cfg.CameraID, "error", err)
		}
	}

	// Insert recording entry into database
	var fileSize int64
	var recordingID string
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
		recordingID = rec.ID
		if info, err := os.Stat(r.curFinalPath); err == nil {
			fileSize = info.Size()
			rec.FileSize = fileSize
		}
		if err := r.cfg.DB.InsertRecordingWithRetry(context.Background(), rec, 3, 500*time.Millisecond); err != nil {
			h264Logger.Error("failed to insert recording", "camera_id", r.cfg.CameraID, "error", err)
		}
	}

	// Publish SegmentCompleted event.
	if r.cfg.EventBus != nil && recordingID != "" {
		r.cfg.EventBus.Publish(context.Background(), event.TopicSegmentCompleted, event.SegmentCompleted{
			CameraID:    r.cfg.CameraID,
			FilePath:    r.curFinalPath,
			Format:      string(model.FormatH264),
			Encoding:    string(model.FormatH264),
			StartedAt:   r.segStart.Format(time.RFC3339Nano),
			EndedAt:     time.Now().Format(time.RFC3339Nano),
			FileSize:    fileSize,
			RecordingID: recordingID,
		})
	}

	// Update metrics for completed segment
	if r.frameCount > 0 && r.curFinalPath != "" {
		r.recordSegmentCreated()
		if fileSize > 0 {
			r.recordBytes(fileSize)
		}
	}

	r.mu.Lock()
	r.muxer = nil
	r.audioTrackID = 0
	r.mu.Unlock()
	r.curTempPath = ""
	r.curFinalPath = ""
	r.frameCount = 0
}
