// Package recorder: rtsp_connect.go — shared RTSP connect-and-record template
// for H264Recorder and H265Recorder (issue #1012 block 3).
//
// The two connectAndRecord implementations were ~340 lines each at ~58%
// line-identical (URL/credentials/client setup, audio negotiation, RTP
// callback skeletons, watchdog, exit sequence). This file owns the shared
// sequence; the codec-specific remainder lives in per-recorder hooks:
//
//   - setupVideo: FindFormat + CreateDecoder + SETUP + the SDP parameter-set
//     seed (H264 seeds SPS/PPS; H265 seeds VPS/SPS/PPS) — the decoder is
//     captured in the returned decode closure.
//   - decode / silentDecodeErr / isIDR: RTP decode semantics (rtph264 vs
//     rtph265 transient-error sentinels; nalutil.IsIDR's h265 flag).
//   - aacChannels: the AAC channel-count derivation genuinely diverged
//     between the two copies (H264 read Config.ChannelCount — the derived
//     count, 8 for channel-config 7 — while H265 read the raw
//     Config.ChannelConfig). Each codec keeps its own expression; only
//     8-channel AAC (config 7) can tell them apart.
//
// Behavior is preserved line-for-line from the H264 copy (the fuller-commented
// of the two); comment-wording-only differences were merged to the longer
// version.

package recorder

import (
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
	"github.com/bluenviron/gortsplib/v5/pkg/format/rtplpcm"
	"github.com/bluenviron/gortsplib/v5/pkg/format/rtpmpeg4audio"
	"github.com/pion/rtp"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/frametrace"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/muxer"
)

// rtspVideoStream is the codec-specific negotiation result: the media + its
// format (as the format.Format interface OnPacketRTP accepts) plus the
// per-codec RTP decode semantics captured with their concrete decoders.
type rtspVideoStream struct {
	medi  *description.Media
	forma format.Format

	// decode decodes one video RTP packet into NALUs.
	decode func(pkt *rtp.Packet) ([][]byte, error)
	// silentDecodeErr reports whether a decode error is an expected
	// transient (per-codec sentinels) that must not be logged.
	silentDecodeErr func(err error) bool
	// isIDR reports the keyframe-ness of a decoded access unit.
	isIDR func(au [][]byte) bool
}

// rtspConnectHooks carries the per-codec behavior of connectAndRecordShared.
type rtspConnectHooks struct {
	// log is the codec-tagged logger (h264Logger / h265Logger).
	log *slog.Logger

	// setupVideo negotiates the video media: FindFormat, CreateDecoder,
	// SETUP, and the SDP parameter-set seed into the codec snapshot (#219).
	setupVideo func(client *gortsplib.Client, desc *description.Session) (*rtspVideoStream, error)

	// aacChannels derives the AAC channel count from the AudioSpecificConfig
	// — see the file comment for why this stayed per-codec.
	aacChannels func(channelCount, channelConfig int) int
}

// connectAndRecordShared connects to the RTSP server, sets up the video
// stream (with optional audio), registers RTP callbacks, and blocks until
// error or context cancellation. It implements the shared half of the
// rtspConnector contract; the concrete recorders' connectAndRecord methods
// delegate here with their codec hooks.
func (b *baseRecorder) connectAndRecordShared(ctx context.Context, h rtspConnectHooks) (error, bool) {
	u, err := base.ParseURL(b.cfg.RTSPURL)
	if err != nil {
		return fmt.Errorf("invalid RTSP URL: %w", err), false
	}
	// Inject credentials from config if URL doesn't have them.
	if u.User == nil && b.cfg.Username != "" {
		u.User = url.UserPassword(b.cfg.Username, b.cfg.Password)
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
	vs, err := h.setupVideo(client, desc)
	if err != nil {
		return err, false
	}

	// Audio setup: find AAC or G.711 format if AudioEnabled.
	var audioDec *rtpmpeg4audio.Decoder
	var audioForma *format.MPEG4Audio
	var g711Dec *rtplpcm.Decoder
	var g711Forma *format.G711
	var audioMedi *description.Media
	if b.cfg.AudioEnabled {
		// Try AAC first
		audioMedi = desc.FindFormat(&audioForma)
		if audioMedi != nil {
			ad, err := audioForma.CreateDecoder()
			if err != nil {
				h.log.Warn("audio decoder creation failed, continuing video-only", "camera_id", b.cfg.CameraID, "error", err)
			} else {
				audioDec = ad
				if _, err := client.Setup(desc.BaseURL, audioMedi, 0, 1); err != nil {
					h.log.Warn("audio SETUP failed, continuing video-only", "camera_id", b.cfg.CameraID, "error", err)
					audioDec = nil
				} else {
					var enc []byte
					if audioForma.Config != nil {
						enc, _ = audioForma.Config.Marshal()
					}
					aCh := h.aacChannels(audioForma.Config.ChannelCount, int(audioForma.Config.ChannelConfig))
					if aCh == 0 {
						aCh = 1
					}
					// Publish the audio config as one immutable snapshot so the
					// external accessors + G.711 callback read a consistent view
					// (no torn codec/muxerConfig pairing). Single Store (#226).
					b.setAudioConfig(&audioConfig{
						codec:       "aac",
						sampleRate:  audioForma.Config.SampleRate,
						channels:    aCh,
						muxerConfig: enc,
					})
					h.log.Info("AAC audio track detected", "camera_id", b.cfg.CameraID)
					if b.cfg.AudioTrigger != nil && b.cfg.AudioTrigger.Enabled {
						// issue #478: no pure-Go AAC decoder in the static build — the
						// loudness trigger only supports G.711 (mu/A-law).
						h.log.Warn("audio_trigger enabled but camera audio is AAC - trigger stays inactive (G.711 required)", "camera_id", b.cfg.CameraID)
					}
				}
			}
		}
		// If no AAC, try G.711
		if audioDec == nil {
			audioMedi = desc.FindFormat(&g711Forma)
			if audioMedi != nil {
				dec := &rtplpcm.Decoder{BitDepth: 8, ChannelCount: 1}
				if err := dec.Init(); err != nil {
					h.log.Warn("G.711 decoder init failed", "camera_id", b.cfg.CameraID, "error", err)
				} else {
					g711Dec = dec
					if _, err := client.Setup(desc.BaseURL, audioMedi, 0, 1); err != nil {
						h.log.Warn("G.711 audio SETUP failed, continuing video-only", "camera_id", b.cfg.CameraID, "error", err)
						g711Dec = nil
					} else {
						rate := g711Forma.SampleRate
						muLawByte := byte(0)
						if g711Forma.MULaw {
							muLawByte = 1
						}
						b.setAudioConfig(&audioConfig{
							codec:          "g711",
							sampleRate:     rate,
							channels:       1,
							g711MULaw:      g711Forma.MULaw,
							g711SampleRate: rate,
							muxerConfig:    []byte{muLawByte, byte(rate >> 24), byte(rate >> 16), byte(rate >> 8), byte(rate)},
						})
						h.log.Info("G.711 audio track detected", "camera_id", b.cfg.CameraID, "mulaw", g711Forma.MULaw, "rate", rate)
					}
				}
			}
		}
		if audioDec == nil && g711Dec == nil {
			h.log.Debug("no supported audio format found in stream", "camera_id", b.cfg.CameraID)
		}
	}

	frameAlive := make(chan struct{}, 1)
	ch := make(chan framePacket, b.cfg.RingBufCap)
	b.frameCh = ch
	b.frameChPtr.Store(&ch)
	b.dropped.Store(0)
	// Arm the adaptive tracker + audio-trigger runtime BEFORE the writer
	// goroutine and the audio callbacks start (issue #478: the audio
	// callback reads both — creating them inside writeFrames raced it).
	b.resetAdaptive()
	writerDone := make(chan struct{})
	go b.writeFrames(writerDone)

	client.OnPacketRTP(vs.medi, vs.forma, func(pkt *rtp.Packet) {
		au, err := vs.decode(pkt)
		if err != nil {
			if !vs.silentDecodeErr(err) {
				h.log.Error("RTP decode error", "camera_id", b.cfg.CameraID, "error", err)
			}
			return
		}
		// Signal frame received for watchdog
		select {
		case frameAlive <- struct{}{}:
		default:
		}
		// PTS monotonicity check — warn only, never drop frames
		if prevPTS := b.lastPTS.Load(); prevPTS > 0 {
			if result := checkPTSMonotonicity(prevPTS, int64(pkt.Timestamp)); result.Anomaly != ptsAnomalyNone {
				logPTSAnomaly(h.log, b.cfg.CameraID, result)
			}
		}
		b.lastPTS.Store(int64(pkt.Timestamp))
		// Producer-side ingest breadcrumb (#482): the frame left the RTSP
		// transport and is about to enter the hub / recorder. Frames lost
		// between here and streamhub_in would be invisible otherwise.
		isIDR := vs.isIDR(au)
		traceID := "no-trace"
		if isIDR {
			traceID = fmt.Sprintf("%s-%d", b.cfg.CameraID, pkt.Timestamp)
		}
		if frametrace.Active(b.cfg.CameraID) {
			frametrace.Log(
				b.cfg.CameraID,
				"trace_id", traceID,
				"camera_id", b.cfg.CameraID,
				"stage", "ingest",
				"is_idr", isIDR,
			)
		}
		// Fan-out to all stream consumers (HLS, WebRTC, etc.)
		if b.Hub != nil {
			b.Hub.Broadcast(int64(pkt.Timestamp), au, isIDR)
		}
		at := time.Now() // one arrival stamp for the whole AU (#506)
		for _, nalu := range au {
			buf := b.frameBufPool.get(4 + len(nalu))
			data := buf.b
			data[0], data[1], data[2], data[3] = 0, 0, 0, 1
			copy(data[4:], nalu)
			select {
			case b.frameCh <- framePacket{data: data, buf: buf, at: at}:
			default:
				b.frameBufPool.put(buf)
				d := b.dropped.Add(1)
				if b.mtrics != nil {
					b.mtrics.RecorderRingBufferDropsTotal.WithLabelValues(b.cfg.CameraID).Inc()
				}
				if d%100 == 1 {
					h.log.Warn("ring buffer full, dropped frames", "camera_id", b.cfg.CameraID, "dropped", d)
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
					h.log.Error("audio RTP decode error", "camera_id", b.cfg.CameraID, "error", err)
				}
				return
			}
			for _, aacData := range aus {
				if b.Hub != nil {
					b.Hub.BroadcastAudio(int64(pkt.Timestamp), model.AudioAAC, aacData)
				}
				b.mu.Lock()
				m := b.muxer
				aid := b.audioTrackID
				start := b.segStart
				b.mu.Unlock()
				if m != nil && aid > 0 && !b.audioSparse.Load() { // sparse drops disk audio; the track exists only when AudioInRecordings is on
					pts := time.Since(start)
					dur := 1024 * time.Second / time.Duration(audioForma.ClockRate())
					if err := m.WriteAudioSample(aid, aacData, pts, dur); err != nil {
						if !errors.Is(err, muxer.ErrClosed) {
							h.log.Error("failed to write audio sample", "camera_id", b.cfg.CameraID, "error", err)
						}
					}
				}
			}
		})
	} else if g711Dec != nil {
		client.OnPacketRTP(audioMedi, g711Forma, func(pkt *rtp.Packet) {
			data, err := g711Dec.Decode(pkt)
			if err != nil {
				h.log.Error("G.711 RTP decode error", "camera_id", b.cfg.CameraID, "error", err)
				return
			}
			if b.Hub != nil {
				b.Hub.BroadcastAudio(int64(pkt.Timestamp), model.AudioG711, data)
			}
			// Audio-trigger input (issue #478): loudness window + pre-trigger
			// ring. Runs in sparse mode too — that is the whole point.
			g711Rate := 8000
			if a := b.audioSnapshot(); a != nil && a.g711SampleRate > 0 {
				g711Rate = a.g711SampleRate
			}
			b.audioTriggerIngest(g711Forma.MULaw, data, g711Rate, time.Now())
			b.writeAmbientArchive(data) // raw sidecar when adaptive.ambient_archive is on
			b.mu.Lock()
			m := b.muxer
			aid := b.audioTrackID
			start := b.segStart
			b.mu.Unlock()
			if m != nil && aid > 0 && !b.audioSparse.Load() { // sparse drops disk audio; the track exists only when AudioInRecordings is on
				pts := time.Since(start)
				// g711SampleRate is read from the immutable audio snapshot
				// (this callback runs on the RTP reader goroutine, concurrently
				// with connectAndRecord's writer; the snapshot is race-free, #226).
				dur := time.Duration(len(data)) * time.Second / time.Duration(g711Rate)
				if dur < time.Millisecond {
					dur = time.Millisecond
				}
				if err := m.WriteAudioSample(aid, data, pts, dur); err != nil {
					if !errors.Is(err, muxer.ErrClosed) {
						h.log.Error("failed to write G.711 audio sample", "camera_id", b.cfg.CameraID, "error", err)
					}
				} else {
					b.audioTriggerMarkWritten()
				}
			}
		})
	}

	b.setStatus(model.StatusRecording)
	if _, err := client.Play(nil); err != nil {
		close(b.frameCh)
		<-writerDone
		return fmt.Errorf("PLAY: %w", err), false
	}
	if b.cfg.OnStreamSessionStart != nil {
		b.cfg.OnStreamSessionStart()
	}
	errCh := make(chan error, 1)
	go func() { errCh <- client.Wait() }()

	// Frame watchdog: detect "RTSP alive but no data" state.
	stopWatchdog := make(chan struct{})
	watchdogDone := make(chan struct{})
	go func() {
		defer close(watchdogDone)
		watchdog := time.NewTimer(b.cfg.FrameWatchdogTimeout)
		defer watchdog.Stop()
		for {
			select {
			case <-frameAlive:
				if !watchdog.Stop() {
					<-watchdog.C
				}
				watchdog.Reset(b.cfg.FrameWatchdogTimeout)
			case <-watchdog.C:
				h.log.Warn("frame watchdog timeout, closing connection",
					"camera_id", b.cfg.CameraID, "timeout", b.cfg.FrameWatchdogTimeout)
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
		// Wait() returning does not guarantee every RTP callback goroutine
		// has drained; Close() is the explicit barrier. Without it, a
		// residual callback's non-blocking send on frameCh can hit the
		// closed channel and panic (#1001). The deferred Close() below is
		// a no-op double-close.
		client.Close()
		close(b.frameCh)
		<-writerDone
		b.closeCurrentSegment()
		return err, true
	case <-ctx.Done():
		close(stopWatchdog)
		<-watchdogDone
		client.Close()
		close(b.frameCh)
		<-writerDone
		b.closeCurrentSegment()
		return ctx.Err(), true
	}
}
