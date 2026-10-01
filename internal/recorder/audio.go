package recorder

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/event"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/metrics"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/muxer"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/slogx"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/streamhub"

	"github.com/bluenviron/gortsplib/v5"
	"github.com/bluenviron/gortsplib/v5/pkg/base"
	"github.com/bluenviron/gortsplib/v5/pkg/description"
	"github.com/bluenviron/gortsplib/v5/pkg/format"
	"github.com/bluenviron/gortsplib/v5/pkg/format/rtplpcm"
	"github.com/bluenviron/gortsplib/v5/pkg/format/rtpmpeg4audio"
	"github.com/pion/rtp"
)

var audioLogger = slogx.Component("audio-recorder")

const (
	// defaultAudioSegmentDur is the time-driven rotation period. Video
	// recorders rotate on IDR availability within the duration window; audio
	// has no keyframes, so this is a pure wall-clock interval.
	defaultAudioSegmentDur = 60 * time.Second
	// defaultAudioAUChanCap buffers several seconds of access units (AAC at
	// 1024 samples/48kHz ≈ 43 AU/s) between the RTP callbacks and the single
	// writer goroutine.
	defaultAudioAUChanCap = 512
	// audioStallTimeout bounds "connected but silent": some IP mics go mute
	// instead of dropping the TCP connection. Beyond this the connection is
	// treated as dead and the reconnect loop takes over.
	audioStallTimeout = 20 * time.Second
)

// AudioConfig configures the audio-only recorder (independent microphone
// over RTSP — an "audio camera", encoding "audio").
type AudioConfig struct {
	CameraID   string
	RTSPURL    string
	Username   string
	Password   string
	SegmentDur time.Duration // 0 = defaultAudioSegmentDur
	DB         RecordingDB
	Store      SegmentStore
	Metrics    *metrics.Metrics
	EventBus   *event.EventBus
	// RecordEnabled gates disk writes; live hub broadcast is unaffected
	// (same contract as the video recorders' live-only mode).
	RecordEnabled *bool
}

// audioAU is one decoded audio access unit tagged with its ARRIVAL time.
// Arrival-time anchoring follows the #506 discipline: the wall clock at
// receive, not the RTP timestamp, drives rotation and segment boundaries.
type audioAU struct {
	data     []byte
	codec    model.AudioCodec
	duration time.Duration
	at       time.Time
}

// AudioRecorder records a pure-audio RTSP source. It is deliberately NOT
// built on baseRecorder: every existing recorder's segment lifecycle is
// gated on video IDRs inside the frame loop (base.go "DO NOT start segment
// without IDR"), and a video-free stream would never open a segment. Here
// rotation is time-driven — the writer goroutine closes/creates segments on
// a wall-clock check per access unit.
type AudioRecorder struct {
	cfg AudioConfig
	log *slog.Logger

	mu     sync.Mutex
	status model.RecorderStatus
	cancel context.CancelFunc
	done   chan struct{}

	// Hub for live audio listening (WS ?audio_only=1). Set by the camera
	// manager via SetHub; read by the enqueue path.
	Hub *streamhub.StreamHub

	// audioCfg is the codec snapshot (same immutable-snapshot discipline as
	// baseRecorder.audio): written once per connection, read by the WS
	// audio-info probe (AudioCodec/… accessors) from other goroutines.
	audioCfg atomic.Pointer[audioConfig]

	// auCh carries access units from the RTP callback goroutines to the
	// single writer goroutine. auChPtr is the race-free read side for stats.
	auCh    chan audioAU
	auChPtr atomic.Pointer[chan audioAU]
	dropped atomic.Int64
	lastAU  atomic.Int64 // unix nano of last received AU (stats + stall watchdog)

	// Segment state — owned by the single writer goroutine (writeAUs), plus
	// closeSegment from run()'s final teardown after the writer has exited.
	muxer        *muxer.MP4Muxer
	trackID      int
	curTempPath  string
	curFinalPath string
	segStart     time.Time
	segBytes     int64
	auCount      int64
}

// NewAudioRecorder creates an audio-only recorder.
func NewAudioRecorder(cfg AudioConfig, store SegmentStore, m *metrics.Metrics) *AudioRecorder {
	if cfg.SegmentDur <= 0 {
		cfg.SegmentDur = defaultAudioSegmentDur
	}
	if cfg.Store == nil {
		cfg.Store = store
	}
	if cfg.Metrics == nil {
		cfg.Metrics = m
	}
	ch := make(chan audioAU, defaultAudioAUChanCap)
	ptr := ch
	r := &AudioRecorder{
		cfg: cfg,
		log: audioLogger,
		done: make(chan struct{}),
		status: model.StatusStopped,
		// Pre-allocated (NOT only in connectAndStream): stats readers and
		// tests drive the writer before any connection exists.
		auCh: ch,
	}
	r.auChPtr.Store(&ptr)
	return r
}

// SetHub wires the StreamHub (streamhub.HubHost).
func (r *AudioRecorder) SetHub(hub *streamhub.StreamHub) { r.Hub = hub }

// HubSource labels the hub for the flow-path view (streamhub.HubHost).
func (r *AudioRecorder) HubSource() string { return "audio-recorder" }

// GetHub exposes the hub for the camera-manager registry and live consumers.
func (r *AudioRecorder) GetHub() *streamhub.StreamHub { return r.Hub }

// AudioOnly marks this recorder for the health manager: video-frame based
// probes (freeze detection, FPS stats) must not run on it.
func (r *AudioRecorder) AudioOnly() bool { return true }

// AudioCodec / AudioConfig / AudioSampleRate / AudioChannels implement the
// audioInfoProvider probe used by the WS and WebRTC handlers.
func (r *AudioRecorder) AudioCodec() string {
	if a := r.audioCfg.Load(); a != nil {
		return a.codec
	}
	return ""
}

func (r *AudioRecorder) AudioConfig() []byte {
	if a := r.audioCfg.Load(); a != nil {
		return a.muxerConfig
	}
	return nil
}

func (r *AudioRecorder) AudioSampleRate() int {
	if a := r.audioCfg.Load(); a != nil {
		return a.sampleRate
	}
	return 0
}

func (r *AudioRecorder) AudioChannels() int {
	if a := r.audioCfg.Load(); a != nil {
		return a.channels
	}
	return 0
}

// Start launches the reconnect loop.
func (r *AudioRecorder) Start(ctx context.Context) error {
	r.mu.Lock()
	if r.cancel != nil {
		r.mu.Unlock()
		return nil // already running
	}
	runCtx, cancel := context.WithCancel(ctx)
	r.cancel = cancel
	r.status = model.StatusReconnecting
	r.mu.Unlock()

	go func() {
		defer close(r.done)
		defer r.recoverPanic("run")
		r.run(runCtx)
		r.setStatus(model.StatusStopped)
	}()
	return nil
}

// Stop cancels the run loop and waits for teardown.
func (r *AudioRecorder) Stop() error {
	r.mu.Lock()
	cancel := r.cancel
	r.cancel = nil
	r.mu.Unlock()
	if cancel != nil {
		cancel()
		select {
		case <-r.done:
		case <-time.After(5 * time.Second):
			r.log.Warn("audio recorder stop timed out", "camera_id", r.cfg.CameraID)
		}
	}
	return nil
}

// Status returns the current recorder status.
func (r *AudioRecorder) Status() model.RecorderStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.status
}

func (r *AudioRecorder) setStatus(s model.RecorderStatus) {
	r.mu.Lock()
	r.status = s
	r.mu.Unlock()
}

func (r *AudioRecorder) recoverPanic(where string) {
	if p := recover(); p != nil {
		r.log.Error("audio recorder panic recovered", "camera_id", r.cfg.CameraID, "where", where, "panic", p)
	}
}

// run drives the shared reconnect cycle until ctx is done, then finalizes
// whatever segment is still open.
func (r *AudioRecorder) run(ctx context.Context) {
	runReconnectLoop(ctx, reconnectDeps{
		CameraID: r.cfg.CameraID,
		Store:    r.cfg.Store,
		Metrics:  r.cfg.Metrics,
		Log:      r.log,
		Connect:  r.connectAndStream,
		RecordError: func(errorType string) {
			if r.cfg.Metrics != nil {
				r.cfg.Metrics.CameraErrors.WithLabelValues(r.cfg.CameraID, errorType).Inc()
			}
		},
		SetStatus: r.setStatus,
	})
	// The writer goroutine has exited (its conn ctx is a child of ctx), so
	// touching the segment state from here is single-threaded again.
	r.closeSegment(time.Now())
}

func (r *AudioRecorder) recordEnabled() bool {
	return r.cfg.RecordEnabled == nil || *r.cfg.RecordEnabled
}

// connectAndStream performs one connection attempt: DESCRIBE, audio-format
// negotiation (AAC first, then G.711), the writer goroutine, and the stall
// watchdog. Blocks until the connection fails or ctx is done.
func (r *AudioRecorder) connectAndStream(ctx context.Context) (error, bool) {
	u, err := base.ParseURL(r.cfg.RTSPURL)
	if err != nil {
		return fmt.Errorf("invalid RTSP URL: %w", err), false
	}
	if u.User == nil && r.cfg.Username != "" {
		u.User = url.UserPassword(r.cfg.Username, r.cfg.Password)
	}
	tcp := gortsplib.ProtocolTCP
	client := &gortsplib.Client{
		Scheme:       u.Scheme,
		Host:         u.Host,
		Protocol:     &tcp,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: config.DefaultRTSPTimeout,
	}
	if err := client.Start(); err != nil {
		return fmt.Errorf("client start: %w", err), false
	}
	defer client.Close()

	desc, _, err := client.Describe(u)
	if err != nil {
		return fmt.Errorf("DESCRIBE: %w", err), false
	}

	// Negotiate one audio format: AAC, then G.711. Opus sources are refused
	// for now (the muxer config shape is untested against a real device —
	// deferred until one is available for validation).
	var (
		aacForma  *format.MPEG4Audio
		g711Forma *format.G711
		audioMedi *description.Media
	)
	audioMedi = desc.FindFormat(&aacForma)
	if audioMedi != nil {
		if _, err := client.Setup(desc.BaseURL, audioMedi, 0, 0); err != nil {
			return fmt.Errorf("audio SETUP: %w", err), false
		}
	} else {
		audioMedi = desc.FindFormat(&g711Forma)
		if audioMedi == nil {
			return errors.New("no supported audio track (AAC/G.711) in stream"), false
		}
		if _, err := client.Setup(desc.BaseURL, audioMedi, 0, 0); err != nil {
			return fmt.Errorf("audio SETUP: %w", err), false
		}
	}

	// Publish the codec snapshot before any callback can fire (same shape as
	// the H264 recorder's audio paths).
	var auCodec model.AudioCodec
	if aacForma != nil {
		var enc []byte
		if aacForma.Config != nil {
			enc, _ = aacForma.Config.Marshal()
		}
		ch := aacForma.Config.ChannelCount
		if ch == 0 {
			ch = 1
		}
		auCodec = model.AudioAAC
		r.audioCfg.Store(&audioConfig{
			codec:       "aac",
			sampleRate:  aacForma.Config.SampleRate,
			channels:    ch,
			muxerConfig: enc,
		})
	} else {
		rate := g711Forma.SampleRate
		muLawByte := byte(0)
		if g711Forma.MULaw {
			muLawByte = 1
		}
		auCodec = model.AudioG711
		r.audioCfg.Store(&audioConfig{
			codec:          "g711",
			sampleRate:     rate,
			channels:       1,
			g711MULaw:      g711Forma.MULaw,
			g711SampleRate: rate,
			muxerConfig:    []byte{muLawByte, byte(rate >> 24), byte(rate >> 16), byte(rate >> 8), byte(rate)},
		})
	}
	r.log.Info("audio source connected", "camera_id", r.cfg.CameraID, "codec", string(auCodec))

	// connCtx covers exactly this connection attempt: cancelling it stops
	// the writer goroutine on every exit path (error, stall, parent done).
	connCtx, connCancel := context.WithCancel(ctx)
	defer connCancel()

	auCh := make(chan audioAU, defaultAudioAUChanCap)
	r.auCh = auCh
	r.auChPtr.Store(&auCh)
	r.dropped.Store(0)
	r.lastAU.Store(time.Now().UnixNano())

	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		defer r.recoverPanic("writer")
		r.writeAUs(connCtx)
	}()

	// Decode + enqueue from the RTP callback (never blocks: bounded channel
	// with drop accounting, mirroring the video frameCh discipline).
	if aacForma != nil {
		dec, derr := aacForma.CreateDecoder()
		if derr != nil {
			connCancel()
			<-writerDone
			return fmt.Errorf("AAC decoder: %w", derr), false
		}
		rate := aacForma.ClockRate()
		client.OnPacketRTP(audioMedi, aacForma, func(pkt *rtp.Packet) {
			aus, derr := dec.Decode(pkt)
			if derr != nil && !errors.Is(derr, rtpmpeg4audio.ErrMorePacketsNeeded) {
				return
			}
			at := time.Now()
			for _, au := range aus {
				r.enqueue(audioAU{
					data:     au,
					codec:    model.AudioAAC,
					duration: time.Duration(1024) * time.Second / time.Duration(rate),
					at:       at,
				})
			}
		})
	} else {
		dec := &rtplpcm.Decoder{BitDepth: 8, ChannelCount: 1}
		if derr := dec.Init(); derr != nil {
			connCancel()
			<-writerDone
			return fmt.Errorf("G.711 decoder init: %w", derr), false
		}
		rate := g711Forma.SampleRate
		client.OnPacketRTP(audioMedi, g711Forma, func(pkt *rtp.Packet) {
			data, derr := dec.Decode(pkt)
			if derr != nil {
				return
			}
			r.enqueue(audioAU{
				data:     data,
				codec:    model.AudioG711,
				duration: time.Duration(len(data)) * time.Second / time.Duration(rate),
				at:       time.Now(),
			})
		})
	}

	if _, err := client.Play(nil); err != nil {
		connCancel()
		<-writerDone
		return fmt.Errorf("PLAY: %w", err), false
	}
	r.setStatus(model.StatusRecording)

	errCh := make(chan error, 1)
	go func() { errCh <- client.Wait() }()
	stallTicker := time.NewTicker(audioStallTimeout / 2)
	defer stallTicker.Stop()
	for {
		select {
		case <-ctx.Done():
			connCancel()
			<-writerDone
			return nil, true
		case err := <-errCh:
			connCancel()
			<-writerDone
			if err == nil {
				err = errors.New("connection closed")
			}
			return fmt.Errorf("client: %w", err), false
		case <-stallTicker.C:
			last := time.Unix(0, r.lastAU.Load())
			if time.Since(last) > audioStallTimeout {
				connCancel()
				<-writerDone
				return fmt.Errorf("audio stalled (no access unit for %s)", time.Since(last).Round(time.Second)), false
			}
		}
	}
}

// enqueue delivers one access unit to the writer; drops with accounting
// when the writer cannot keep up (bounded memory over live-tail fidelity —
// the same trade the video ring makes).
func (r *AudioRecorder) enqueue(au audioAU) {
	r.lastAU.Store(au.at.UnixNano())
	if r.Hub != nil {
		r.Hub.BroadcastAudio(au.at.UnixNano(), au.codec, au.data)
	}
	ch := r.auCh
	if ch == nil {
		return
	}
	select {
	case ch <- au:
	default:
		d := r.dropped.Add(1)
		if d%100 == 1 {
			r.log.Warn("audio AU channel full, dropping", "camera_id", r.cfg.CameraID, "dropped", d)
		}
		if r.cfg.Metrics != nil {
			r.cfg.Metrics.RecorderRingBufferDropsTotal.WithLabelValues(r.cfg.CameraID).Inc()
		}
	}
}

// writeAUs is the single writer: time-driven rotation + muxer writes.
// Segments open lazily on the first AU after a close (never empty).
func (r *AudioRecorder) writeAUs(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			r.closeSegment(time.Now())
			return
		case au := <-r.auCh:
			if r.muxer != nil && au.at.Sub(r.segStart) >= r.cfg.SegmentDur {
				r.closeSegment(au.at)
			}
			if r.muxer == nil {
				if !r.recordEnabled() {
					continue // live-only mode
				}
				if !r.openSegment(au) {
					continue // storage failure: stay live, retry next AU
				}
			}
			pts := au.at.Sub(r.segStart)
			if err := r.muxer.WriteAudioSample(r.trackID, au.data, pts, au.duration); err != nil {
				if err.Error() != "muxer is closed" {
					r.log.Error("failed to write audio sample", "camera_id", r.cfg.CameraID, "error", err)
				}
				continue
			}
			r.segBytes += int64(len(au.data))
			r.auCount++
		}
	}
}

// openSegment creates a new audio segment anchored at the first AU's
// arrival time.
func (r *AudioRecorder) openSegment(au audioAU) bool {
	a := r.audioCfg.Load()
	if a == nil {
		return false
	}
	tempPath, finalPath, err := r.cfg.Store.CreateSegment(r.cfg.CameraID, string(model.FormatAudio))
	if err != nil {
		r.log.Error("failed to create audio segment", "camera_id", r.cfg.CameraID, "error", err)
		return false
	}
	m := muxer.NewMP4Muxer(tempPath)
	trackID, err := m.AddAudioTrack(a.codec, a.muxerConfig)
	if err != nil {
		r.log.Error("failed to add audio track", "camera_id", r.cfg.CameraID, "codec", a.codec, "error", err)
		os.Remove(tempPath)
		return false
	}
	r.muxer = m
	r.trackID = trackID
	r.curTempPath = tempPath
	r.curFinalPath = finalPath
	r.segStart = au.at
	r.segBytes = 0
	r.auCount = 0
	return true
}

// closeSegment finalizes the current segment: muxer close, atomic rename,
// DB row (born-terminal merge status), SegmentCompleted event, metrics.
// Called only from the writer goroutine or run()'s post-teardown.
func (r *AudioRecorder) closeSegment(now time.Time) {
	if r.muxer == nil {
		return
	}
	m, tempPath, finalPath := r.muxer, r.curTempPath, r.curFinalPath
	start, bytes, count := r.segStart, r.segBytes, r.auCount
	r.muxer = nil
	r.trackID = 0
	r.curTempPath = ""
	r.curFinalPath = ""

	if err := m.Close(); err != nil {
		r.log.Error("failed to close audio muxer", "camera_id", r.cfg.CameraID, "error", err)
		os.Remove(tempPath)
		return
	}
	if err := r.cfg.Store.CloseSegment(tempPath, finalPath); err != nil {
		r.log.Error("failed to finalize audio segment", "camera_id", r.cfg.CameraID, "error", err)
	}

	var fileSize int64
	finalExists := false
	if info, err := os.Stat(finalPath); err == nil {
		finalExists = true
		fileSize = info.Size()
	} else {
		r.log.Warn("audio segment missing after finalize — row skipped", "camera_id", r.cfg.CameraID, "path", finalPath)
	}

	var recordingID string
	if r.cfg.DB != nil && finalExists && count > 0 {
		rec := &model.Recording{
			ID:          strconv.FormatInt(now.UnixNano(), 10),
			CameraID:    r.cfg.CameraID,
			FilePath:    finalPath,
			Format:      model.FormatAudio,
			StartedAt:   start,
			EndedAt:     now,
			Duration:    now.Sub(start).Seconds(),
			FrameCount:  int(count),
			FileSize:    fileSize,
			MergeStatus: model.MergeStatusAudio, // born terminal — never a merge input
		}
		recordingID = rec.ID
		if err := r.cfg.DB.InsertRecordingWithRetry(context.Background(), rec, dbInsertRetries, dbInsertBackoff); err != nil {
			r.log.Error("failed to insert audio recording", "camera_id", r.cfg.CameraID, "error", err)
			recordingID = ""
		}
	}

	if r.cfg.EventBus != nil && recordingID != "" {
		r.cfg.EventBus.Publish(context.Background(), event.TopicSegmentCompleted, event.SegmentCompleted{
			CameraID:    r.cfg.CameraID,
			FilePath:    finalPath,
			Format:      string(model.FormatAudio),
			Encoding:    string(model.FormatAudio),
			StartedAt:   start.Format(time.RFC3339Nano),
			EndedAt:     now.Format(time.RFC3339Nano),
			FileSize:    fileSize,
			RecordingID: recordingID,
		})
	}

	if finalExists && count > 0 && r.cfg.Metrics != nil {
		r.cfg.Metrics.SegmentsCreated.WithLabelValues(r.cfg.CameraID, "audio").Inc()
		r.cfg.Metrics.RecordingBytesTotal.WithLabelValues(r.cfg.CameraID, "audio").Add(float64(bytes))
	}
}

// AudioRecorderStats reports recent activity for flow/health surfaces.
// FrameCount here means access units.
type AudioRecorderStats struct {
	LastAUAge    time.Duration
	DroppedTotal int64
	SegmentOpen  bool
}

// Stats snapshots recorder activity (safe from any goroutine; LastAUAge is
// fed by the atomic lastAU stamp).
func (r *AudioRecorder) Stats() AudioRecorderStats {
	last := time.Unix(0, r.lastAU.Load())
	return AudioRecorderStats{
		LastAUAge:    time.Since(last),
		DroppedTotal: r.dropped.Load(),
		SegmentOpen:  r.muxer != nil,
	}
}
