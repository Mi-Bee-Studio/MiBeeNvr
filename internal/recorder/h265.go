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
	"github.com/bluenviron/gortsplib/v5/pkg/format/rtph265"
	"github.com/bluenviron/gortsplib/v5/pkg/format/rtplpcm"
	"github.com/bluenviron/gortsplib/v5/pkg/format/rtpmpeg4audio"
	"github.com/pion/rtp"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/metrics"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model/nalutil"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/muxer"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/event"
)

var h265Logger = slog.Default().With("component", "h265-recorder")

// H265Config holds configuration for the H265 recorder.
type H265Config struct {
	CameraID             string
	RTSPURL              string
	Username             string
	Password             string
	SegmentDur           time.Duration
	RingBufCap           int
	MaxBackoff           time.Duration // Deprecated: no longer used, tiered backoff is used instead
	InitBackoff          time.Duration // Deprecated: no longer used, tiered backoff is used instead
	DB                   RecordingDB
	AudioEnabled         bool
	FrameWatchdogTimeout time.Duration // default 30s (0 = use constant default)
	EventBus             *event.EventBus
}

// H265Recorder records H.265/HEVC video from an RTSP source.
type H265Recorder struct {
	cfg     H265Config
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
	curFinalPath     string
	curTempPath      string
	segStart         time.Time
	frameCount       int
	lastFrameTime    time.Time

	vps []byte
	sps []byte
	pps []byte
	Hub *model.StreamHub // Frame fan-out to multiple consumers (HLS, WebRTC, etc.)

	frameCh chan []byte
	dropped atomic.Int64
	lastPTS atomic.Int64 // tracks last RTP PTS for monotonicity check
}

// GetHub returns the StreamHub for frame fan-out.
func (r *H265Recorder) GetHub() *model.StreamHub { return r.Hub }

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
	if cfg.FrameWatchdogTimeout == 0 {
		cfg.FrameWatchdogTimeout = defaultFrameWatchdogTimeout
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
		h265Logger.Error("connection error, reconnecting", "camera_id", r.cfg.CameraID, "error", err, "backoff", backoff, "attempt", retryCount)
		r.recordError("connection")
		r.setStatus(model.StatusReconnecting)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
	}
}

func (r *H265Recorder) connectAndRecord(ctx context.Context) (error, bool) {
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
	var forma *format.H265
	medi := desc.FindFormat(&forma)
	if medi == nil {
		return fmt.Errorf("H265 media not found in stream"), false
	}
	rtpDec, err := forma.CreateDecoder()
	if err != nil {
		return fmt.Errorf("create RTP decoder: %w", err), false
	}
	if _, err := client.Setup(desc.BaseURL, medi, 0, 0); err != nil {
		return fmt.Errorf("SETUP: %w", err), false
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
				h265Logger.Warn("audio decoder creation failed, continuing video-only", "camera_id", r.cfg.CameraID, "error", err)
			} else {
				audioDec = ad
				if _, err := client.Setup(desc.BaseURL, audioMedi, 0, 1); err != nil {
					h265Logger.Warn("audio SETUP failed, continuing video-only", "camera_id", r.cfg.CameraID, "error", err)
					audioDec = nil
				} else {
					if audioForma.Config != nil {
						if enc, err := audioForma.Config.Marshal(); err == nil {
							r.audioMuxerConfig = enc
						}
					}
					r.audioCodec = "aac"
					h265Logger.Info("AAC audio track detected", "camera_id", r.cfg.CameraID)
				}
			}
		}
		// If no AAC, try G.711
		if audioDec == nil {
			audioMedi = desc.FindFormat(&g711Forma)
			if audioMedi != nil {
				dec := &rtplpcm.Decoder{BitDepth: 8, ChannelCount: 1}
				if err := dec.Init(); err != nil {
					h265Logger.Warn("G.711 decoder init failed", "camera_id", r.cfg.CameraID, "error", err)
				} else {
					g711Dec = dec
					if _, err := client.Setup(desc.BaseURL, audioMedi, 0, 1); err != nil {
						h265Logger.Warn("G.711 audio SETUP failed, continuing video-only", "camera_id", r.cfg.CameraID, "error", err)
						g711Dec = nil
					} else {
						r.audioCodec = "g711"
						r.g711MULaw = g711Forma.MULaw
						r.g711SampleRate = g711Forma.SampleRate
						// Build config for muxer: 1 byte muLaw flag + 4 bytes sample rate
						muLawByte := byte(0)
						if g711Forma.MULaw {
							muLawByte = 1
						}
						rate := g711Forma.SampleRate
						r.audioMuxerConfig = []byte{muLawByte, byte(rate >> 24), byte(rate >> 16), byte(rate >> 8), byte(rate)}
						h265Logger.Info("G.711 audio track detected", "camera_id", r.cfg.CameraID, "mulaw", g711Forma.MULaw, "rate", rate)
					}
				}
			}
		}
		if audioDec == nil && g711Dec == nil {
			h265Logger.Debug("no supported audio format found in stream", "camera_id", r.cfg.CameraID)
		}
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

	frameAlive := make(chan struct{}, 1)
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
		// Signal frame received for watchdog
		select {
		case frameAlive <- struct{}{}:
		default:
		}
		// PTS monotonicity check — warn only, never drop frames
		if prevPTS := r.lastPTS.Load(); prevPTS > 0 {
			if result := checkPTSMonotonicity(prevPTS, int64(pkt.Timestamp)); result.Anomaly != ptsAnomalyNone {
				logPTSAnomaly(h265Logger, r.cfg.CameraID, result)
			}
		}
		r.lastPTS.Store(int64(pkt.Timestamp))
		// Fan-out to all stream consumers (HLS, WebRTC, etc.)
		if r.Hub != nil {
			r.Hub.Broadcast(int64(pkt.Timestamp), au, nalutil.IsIDR(au, true))
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
					h265Logger.Warn("ring buffer full, dropped frames", "camera_id", r.cfg.CameraID, "dropped", d)
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
					h265Logger.Error("audio RTP decode error", "camera_id", r.cfg.CameraID, "error", err)
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
							h265Logger.Error("failed to write audio sample", "camera_id", r.cfg.CameraID, "error", err)
						}
					}
				}
			}
		})
	} else if g711Dec != nil {
		// G.711 handler — raw PCM, no complex decoding needed
		client.OnPacketRTP(audioMedi, g711Forma, func(pkt *rtp.Packet) {
			data, err := g711Dec.Decode(pkt)
			if err != nil {
				h265Logger.Error("G.711 RTP decode error", "camera_id", r.cfg.CameraID, "error", err)
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
				// G.711: 8kHz, 8-bit, mono. Duration = len(data) / 8000 seconds.
				dur := time.Duration(len(data)) * time.Second / time.Duration(r.g711SampleRate)
				if dur < time.Millisecond {
					dur = time.Millisecond
				}
				if err := m.WriteAudioSample(aid, data, pts, dur); err != nil {
					if err.Error() != "muxer is closed" {
						h265Logger.Error("failed to write G.711 audio sample", "camera_id", r.cfg.CameraID, "error", err)
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
				h265Logger.Warn("frame watchdog timeout, closing connection",
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
			m := muxer.NewMP4Muxer(tempPath)
			trackID, err := m.AddH265Track(r.vps, r.sps, r.pps)
			if err != nil {
				h265Logger.Error("failed to add H265 track", "camera_id", r.cfg.CameraID, "error", err)
				// Clean up empty temp file on muxer init failure
				os.Remove(tempPath)
				continue
			}
			r.trackID = trackID
			// Add audio track if audio config is available.
			if len(r.audioMuxerConfig) > 0 && r.audioCodec != "" {
				aID, err := m.AddAudioTrack(r.audioCodec, r.audioMuxerConfig)
				if err != nil {
					h265Logger.Error("failed to add audio track", "camera_id", r.cfg.CameraID, "codec", r.audioCodec, "error", err)
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
			h265Logger.Error("failed to close segment", "camera_id", r.cfg.CameraID, "error", err)
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
			Format:     model.FormatH265,
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
			h265Logger.Error("failed to insert recording", "camera_id", r.cfg.CameraID, "error", err)
		}
	}

	// Publish SegmentCompleted event.
	if r.cfg.EventBus != nil && recordingID != "" {
		r.cfg.EventBus.Publish(context.Background(), event.TopicSegmentCompleted, event.SegmentCompleted{
			CameraID:    r.cfg.CameraID,
			FilePath:    r.curFinalPath,
			Format:      string(model.FormatH265),
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
