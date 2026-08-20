package recorder

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
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
)

var h265Logger = slog.Default().With("component", "h265-recorder")

// H265Config is a type alias for BaseConfig.
// This allows flat struct literals (e.g., H265Config{CameraID: "..."}) while
// sharing the common configuration fields across all RTSP recorder types.
type H265Config = BaseConfig

// H265NALDriver implements codecDriver for H.265/HEVC video.
type H265NALDriver struct{}

func (d H265NALDriver) codecLabel() string          { return "h265" }
func (d H265NALDriver) segmentFormat() model.Format { return model.FormatH265 }
func (d H265NALDriver) rtpFormat() format.Format    { return &format.H265{} }
func (d H265NALDriver) minNALUDataLen() int         { return 6 }
func (d H265NALDriver) naluType(firstByte byte) int { return int((firstByte >> 1) & 0x3F) }
func (d H265NALDriver) isIDR(typ int) bool          { return typ == 19 || typ == 20 }
func (d H265NALDriver) isParameterSet(typ int) bool { return typ == 32 || typ == 33 || typ == 34 }
func (d H265NALDriver) isVCL(typ int) bool          { return typ < 32 }
func (d H265NALDriver) paramSetsReady(b *baseRecorder) bool {
	vps, sps, pps := b.codecSnapshot()
	return vps != nil && sps != nil && pps != nil
}

func (d H265NALDriver) handleParamSet(b *baseRecorder, nalu []byte, typ int) bool {
	// Load the current snapshot once; this method runs only on the single
	// writeFrames goroutine, so load-then-store is race-free. We always rebuild
	// and store the full triplet (VPS/SPS/PPS) so a concurrent reader (live
	// preview via codecSnapshot) never sees a torn mix of old+new params (#219).
	vps, sps, pps := b.codecSnapshot()
	switch typ {
	case 32: // VPS
		if vps != nil && !bytes.Equal(vps, nalu) {
			b.log.Info("VPS change detected, rotating segment", "camera_id", b.cfg.CameraID)
			b.setCodecParams(nalu, sps, pps)
			return true
		}
		b.setCodecParams(nalu, sps, pps)
	case 33: // SPS
		if sps != nil && !bytes.Equal(sps, nalu) {
			b.log.Info("SPS change detected, rotating segment", "camera_id", b.cfg.CameraID)
			b.setCodecParams(vps, nalu, pps)
			return true
		}
		b.setCodecParams(vps, nalu, pps)
	case 34: // PPS
		if pps != nil && !bytes.Equal(pps, nalu) {
			b.log.Info("PPS change detected, rotating segment", "camera_id", b.cfg.CameraID)
			b.setCodecParams(vps, sps, nalu)
			return true
		}
		b.setCodecParams(vps, sps, nalu)
	}
	return false
}

func (d H265NALDriver) extractParamSets(b *baseRecorder, au [][]byte) {
	for _, nalu := range au {
		if len(nalu) == 0 {
			continue
		}
		typ := d.naluType(nalu[0])
		if d.isParameterSet(typ) {
			d.handleParamSet(b, nalu, typ)
		}
	}
}

func (d H265NALDriver) addTrack(m *muxer.MP4Muxer, b *baseRecorder) (int, error) {
	vps, sps, pps := b.codecSnapshot()
	return m.AddH265Track(vps, sps, pps)
}

// H265Recorder records H.265/HEVC video from an RTSP source.
type H265Recorder struct {
	*baseRecorder
}

// Interface compliance checks.
var (
	_ model.Recorder = (*H265Recorder)(nil)
	_ rtspConnector  = (*H265Recorder)(nil)
)

// NewH265Recorder creates a new H265Recorder.
func NewH265Recorder(cfg H265Config, store SegmentStore, opts ...*metrics.Metrics) *H265Recorder {
	if cfg.SegmentDur == 0 {
		cfg.SegmentDur = DefaultSegmentDur
	}
	if cfg.RingBufCap == 0 {
		cfg.RingBufCap = DefaultRingBufCap
	}
	if cfg.FrameWatchdogTimeout == 0 {
		cfg.FrameWatchdogTimeout = defaultFrameWatchdogTimeout
	}

	var m *metrics.Metrics
	if len(opts) > 0 {
		m = opts[0]
	}

	rec := &H265Recorder{}
	b := &baseRecorder{
		driver: H265NALDriver{},
		cfg:    cfg,
		store:  store,
		mtrics: m,
		status: model.StatusStopped,
		log:    h265Logger,
	}
	rec.baseRecorder = b
	b.self = rec
	return rec
}

// Start implements model.Recorder.
func (r *H265Recorder) Start(ctx context.Context) error {
	return r.start(ctx)
}

// Stop implements model.Recorder.
func (r *H265Recorder) Stop() error {
	return r.stop()
}

// Status implements model.Recorder.
func (r *H265Recorder) Status() model.RecorderStatus {
	return r.getStatus()
}

// GetHub returns the StreamHub for frame fan-out.
func (r *H265Recorder) GetHub() *model.StreamHub { return r.Hub }

// VPS returns the current H265 Video Parameter Set NAL unit (without start bytes).
// Reads the atomic codec snapshot — safe for concurrent live-preview reads (#219).
func (r *H265Recorder) VPS() []byte { _, _, v := r.codecSnapshot(); return v }

// SPS returns the current H265 Sequence Parameter Set NAL unit (without start bytes).
// Reads the atomic codec snapshot — safe for concurrent live-preview reads (#219).
func (r *H265Recorder) SPS() []byte { s, _, _ := r.codecSnapshot(); return s }

// PPS returns the current H265 Picture Parameter Set NAL unit (without start bytes).
// Reads the atomic codec snapshot — safe for concurrent live-preview reads (#219).
func (r *H265Recorder) PPS() []byte { _, p, _ := r.codecSnapshot(); return p }

// CodecParams implements model.HLSProvider, returning the current H.265 codec
// and parameter-set snapshot in a single atomic read. This lets getCodecParams
// (handlers_stream.go) use the HLSProvider fast-path instead of the concrete
// type switch, and guarantees a consistent (non-torn) VPS/SPS/PPS triplet (#219).
func (r *H265Recorder) CodecParams() (codec model.Format, sps, pps, vps []byte) {
	sps, pps, vps = r.codecSnapshot()
	return model.FormatH265, sps, pps, vps
}

// AudioCodec returns the audio codec name ("aac", "g711", or "" for no audio).
// Reads the immutable audio snapshot (#226); safe from any goroutine.
func (r *H265Recorder) AudioCodec() string {
	if a := r.audioSnapshot(); a != nil {
		return a.codec
	}
	return ""
}

// AudioConfig returns a copy of the audio codec configuration bytes, or nil.
// Returned slice is a fresh copy callers may mutate (#226).
func (r *H265Recorder) AudioConfig() []byte {
	if a := r.audioSnapshot(); a != nil && a.muxerConfig != nil {
		return append([]byte(nil), a.muxerConfig...)
	}
	return nil
}

// AudioSampleRate returns the audio sample rate in Hz, or 0 if no audio.
func (r *H265Recorder) AudioSampleRate() int {
	if a := r.audioSnapshot(); a != nil {
		return a.sampleRate
	}
	return 0
}

// AudioChannels returns the number of audio channels, or 0 if no audio.
func (r *H265Recorder) AudioChannels() int {
	if a := r.audioSnapshot(); a != nil {
		return a.channels
	}
	return 0
}

// connectAndRecord implements rtspConnector. It connects to the RTSP server,
// sets up the H.265 video stream (with optional audio), registers RTP
// callbacks, and blocks until error or context cancellation.
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

	// Store initial parameter sets from SDP. Merge with any params already
	// captured and store atomically as a full triplet so concurrent live-preview
	// reads see a consistent VPS/SPS/PPS set (#219).
	vps, sps, pps := r.codecSnapshot()
	if forma.VPS != nil {
		vps = forma.VPS
	}
	if forma.SPS != nil {
		sps = forma.SPS
	}
	if forma.PPS != nil {
		pps = forma.PPS
	}
	r.setCodecParams(vps, sps, pps)

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
					var enc []byte
					if audioForma.Config != nil {
						enc, _ = audioForma.Config.Marshal()
					}
					aCh := int(audioForma.Config.ChannelConfig)
					if aCh == 0 {
						aCh = 1
					}
					// Publish audio config as one immutable snapshot (#226).
					r.setAudioConfig(&audioConfig{
						codec:       "aac",
						sampleRate:  audioForma.Config.SampleRate,
						channels:    aCh,
						muxerConfig: enc,
					})
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
						rate := g711Forma.SampleRate
						muLawByte := byte(0)
						if g711Forma.MULaw {
							muLawByte = 1
						}
						r.setAudioConfig(&audioConfig{
							codec:          "g711",
							sampleRate:     rate,
							channels:       1,
							g711MULaw:      g711Forma.MULaw,
							g711SampleRate: rate,
							muxerConfig:    []byte{muLawByte, byte(rate >> 24), byte(rate >> 16), byte(rate >> 8), byte(rate)},
						})
						h265Logger.Info("G.711 audio track detected", "camera_id", r.cfg.CameraID, "mulaw", g711Forma.MULaw, "rate", rate)
					}
				}
			}
		}
		if audioDec == nil && g711Dec == nil {
			h265Logger.Debug("no supported audio format found in stream", "camera_id", r.cfg.CameraID)
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
			if !errors.Is(err, rtph265.ErrNonStartingPacketAndNoPrevious) && !errors.Is(err, rtph265.ErrMorePacketsNeeded) {
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
				if r.mtrics != nil {
					r.mtrics.RecorderRingBufferDropsTotal.WithLabelValues(r.cfg.CameraID).Inc()
				}
				if d%100 == 1 {
					h265Logger.Warn("ring buffer full, dropped frames", "camera_id", r.cfg.CameraID, "dropped", d)
				}
			}
		}
	})

	// Audio RTP handler.
	if audioDec != nil {
		client.OnPacketRTP(audioMedi, audioForma, func(pkt *rtp.Packet) {
			aus, err := audioDec.Decode(pkt)
			if err != nil {
				if !errors.Is(err, rtpmpeg4audio.ErrMorePacketsNeeded) {
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
				if m != nil && aid > 0 && !r.audioSparse.Load() { // sparse (adaptive-timelapse) mode drops disk audio, live audio continues
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
			if m != nil && aid > 0 && !r.audioSparse.Load() { // sparse (adaptive-timelapse) mode drops disk audio, live audio continues
				pts := time.Since(start)
				// g711SampleRate from the immutable snapshot (race-free read
				// from this RTP-callback goroutine, #226).
				g711Rate := 8000
				if a := r.audioSnapshot(); a != nil && a.g711SampleRate > 0 {
					g711Rate = a.g711SampleRate
				}
				dur := time.Duration(len(data)) * time.Second / time.Duration(g711Rate)
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
