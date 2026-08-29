package whip

import (
	"context"
	"strings"
	"time"

	"github.com/pion/rtp/codecs"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media/samplebuilder"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model/nalutil"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/streamhub"
)

// jitterLatency bounds how long the depacketizer waits for out-of-order or
// fragmented RTP packets before emitting what it has (converted to RTP clock
// ticks per track kind). ~200ms matches common WebRTC jitter buffers: larger
// adds latency, smaller risks dropping late fragments on lossy links.
const jitterLatency = 200 * time.Millisecond

// handleVideoTrack assembles H.264 RTP packets into access units and forwards
// them to the IngestRecorder (which fans out to the StreamHub AND writes MP4
// segments). When no recorder is wired (live-only gateway without the camera
// registered), frames fall back to a direct hub broadcast so live preview
// still works.
func (s *Server) handleVideoTrack(ctx context.Context, sess *session, hub *streamhub.StreamHub, naluCB NALUCallback, track *webrtc.TrackRemote) {
	defer func() {
		if r := recover(); r != nil {
			logger.Warn("WHIP video track handler panic recovered",
				"camera_id", sess.cameraID, "error", r)
		}
	}()

	sb := samplebuilder.New(1500, &codecs.H264Packet{}, 90_000,
		samplebuilder.WithMaxTimeDelay(jitterLatency),
	)

	// PTS accumulates from sample durations on the 90 kHz clock: the
	// samplebuilder does not expose per-sample RTP timestamps, and consumers
	// only need consistent deltas (same approach as the WHEP audio writer).
	var ptsTicks int64

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		pkt, _, err := track.ReadRTP()
		if err != nil {
			return // track closed (session teardown)
		}
		sess.lastPacket.Store(time.Now().UnixNano())
		sb.Push(pkt)
		for {
			sample := sb.Pop()
			if sample == nil {
				break
			}
			au := annexBToNALUs(sample.Data)
			if len(au) == 0 {
				continue
			}
			ptsTicks += sample.Duration.Nanoseconds() * 90 / int64(time.Second) // ns → 90kHz ticks
			if ptsTicks == 0 {
				ptsTicks = 1
			}
			isIDR := nalutil.IsIDR(au, false)
			if naluCB != nil {
				naluCB(au, ptsTicks, isIDR)
			} else if hub != nil {
				hub.Broadcast(ptsTicks, au, isIDR)
			}
		}
	}
}

// handleAudioTrack forwards Opus frames to the IngestRecorder (dual-write:
// hub BroadcastAudio + MP4 WriteAudioSample inside the recorder). Non-Opus
// audio tracks are drained but dropped — G.711 push-in has no producer today.
func (s *Server) handleAudioTrack(ctx context.Context, sess *session, hub *streamhub.StreamHub, track *webrtc.TrackRemote) {
	defer func() {
		if r := recover(); r != nil {
			logger.Warn("WHIP audio track handler panic recovered",
				"camera_id", sess.cameraID, "error", r)
		}
	}()

	codec := track.Codec()
	wireCodec := whipAudioCodec(codec.MimeType)
	if wireCodec == "" {
		// Drain the track so RTCP/RTT stay healthy, discard the frames.
		for {
			if _, _, err := track.ReadRTP(); err != nil {
				return
			}
			sess.lastPacket.Store(time.Now().UnixNano())
		}
	}

	var audioCB AudioCallback
	if s.AudioFormatter != nil {
		s.AudioFormatter(sess.cameraID, wireCodec, int(codec.ClockRate), int(codec.Channels))
	}
	if s.AudioProvider != nil {
		audioCB = s.AudioProvider(sess.cameraID)
	}

	sb := samplebuilder.New(1500, &codecs.OpusPacket{}, 48_000,
		samplebuilder.WithMaxTimeDelay(jitterLatency),
	)
	var ptsTicks int64

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		pkt, _, err := track.ReadRTP()
		if err != nil {
			return
		}
		sess.lastPacket.Store(time.Now().UnixNano())
		sb.Push(pkt)
		for {
			sample := sb.Pop()
			if sample == nil {
				break
			}
			data := sample.Data
			if len(data) == 0 {
				continue
			}
			ptsTicks += sample.Duration.Nanoseconds() * 48_000 / int64(time.Second)
			dur := sample.Duration
			if dur < time.Millisecond {
				dur = 20 * time.Millisecond
			}
			if audioCB != nil {
				audioCB(wireCodec, ptsTicks, data, dur)
			} else if hub != nil {
				// No recorder wired — still feed live consumers directly.
				hub.BroadcastAudio(ptsTicks, model.AudioOpus, data)
			}
		}
	}
}

// whipAudioCodec maps a pion mime type ("audio/opus", ...) to the recorder's
// audio codec string; "" = unsupported for recording.
func whipAudioCodec(mime string) string {
	if strings.HasSuffix(mime, "opus") {
		return "opus"
	}
	return ""
}

// annexBToNALUs splits an Annex B byte stream (what the H.264 depacketizer
// emits) into NAL units without start codes — the AU shape WriteNALU and the
// StreamHub convention expect.
//
// Start codes are "00 00 01" possibly with extra leading zeros (4-byte
// variant). Per Annex B the zeros belong to the delimiter, and a valid NAL
// never ends in 0x00 (RBSP trailing bits end in a 1 bit), so trimming zeros
// before each detected start code is unambiguous.
func annexBToNALUs(data []byte) [][]byte {
	var nalus [][]byte
	start := -1
	for i := 0; i+3 < len(data); i++ {
		if data[i] == 0 && data[i+1] == 0 && data[i+2] == 1 {
			if start >= 0 {
				end := i
				for end > start && data[end-1] == 0 {
					end--
				}
				if end > start {
					nalus = append(nalus, data[start:end])
				}
			}
			start = i + 3
			i += 2
		}
	}
	if start >= 0 && start < len(data) {
		nalus = append(nalus, data[start:])
	}
	return nalus
}
