package recorder

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/slogx"

	"github.com/bluenviron/gortsplib/v5"
	"github.com/bluenviron/gortsplib/v5/pkg/description"
	"github.com/bluenviron/gortsplib/v5/pkg/format"
	"github.com/bluenviron/gortsplib/v5/pkg/format/rtph265"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/metrics"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model/nalutil"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/muxer"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/streamhub"
)

var h265Logger = slogx.Component("h265-recorder")

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
	sps, pps, vps := b.codecSnapshot()
	return vps != nil && sps != nil && pps != nil
}

func (d H265NALDriver) handleParamSet(b *baseRecorder, nalu []byte, typ int) bool {
	// Load the current snapshot once; this method runs only on the single
	// writeFrames goroutine, so load-then-store is race-free. We always rebuild
	// and store the full triplet (VPS/SPS/PPS) so a concurrent reader (live
	// preview via codecSnapshot) never sees a torn mix of old+new params (#219).
	// codecSnapshot returns (sps, pps, vps): keep the labels aligned — the
	// previous (vps, sps, pps) unpack silently rotated the triplet, and every
	// re-store below wrote mislabeled bytes back into the snapshot (SDP sprop,
	// the RTSP server's parameter injection, and the MP4 track config all
	// served VPS/SPS/PPS in each other's slots).
	sps, pps, vps := b.codecSnapshot()
	// The muxer==nil guard keeps param sync at connect (SDP sprop seed vs
	// first in-band delivery) from consuming rotation budget — there is no
	// segment to protect yet, so the fresh bytes are just cached.
	switch typ {
	case 32: // VPS
		if vps != nil && !bytes.Equal(vps, nalu) && b.muxer != nil {
			// No semantic VPS parser — rate-limit rapid variant alternation (#642).
			if b.paramGate.ShouldRotateUnparsed(time.Now()) {
				b.log.Info("VPS change detected, rotating segment", "camera_id", b.cfg.CameraID)
				b.setCodecParams(sps, pps, nalu)
				return true
			}
		}
		b.setCodecParams(sps, pps, nalu)
	case 33: // SPS
		if sps != nil && !bytes.Equal(sps, nalu) && b.muxer != nil {
			// #642: decode-equivalent SPS variants (VUI/timing-only differences)
			// never rotate; a real codec change rotates immediately.
			if b.paramGate.ShouldRotateSPS(sps, nalu, true, time.Now()) {
				b.log.Info("SPS change detected, rotating segment", "camera_id", b.cfg.CameraID)
				b.setCodecParams(nalu, pps, vps)
				return true
			}
			b.log.Debug("SPS variant is decode-compatible, keeping segment open",
				"camera_id", b.cfg.CameraID)
		}
		b.setCodecParams(nalu, pps, vps)
	case 34: // PPS
		if pps != nil && !bytes.Equal(pps, nalu) && b.muxer != nil {
			// No semantic PPS parser — rate-limit rapid variant alternation (#642).
			if b.paramGate.ShouldRotateUnparsed(time.Now()) {
				b.log.Info("PPS change detected, rotating segment", "camera_id", b.cfg.CameraID)
				b.setCodecParams(sps, nalu, vps)
				return true
			}
		}
		b.setCodecParams(sps, nalu, vps)
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
	sps, pps, vps := b.codecSnapshot()
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
		driver:       H265NALDriver{},
		cfg:          cfg,
		store:        store,
		mtrics:       m,
		frameBufPool: newFrameBufPool(),
		status:       model.StatusStopped,
		log:          h265Logger,
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
func (r *H265Recorder) GetHub() *streamhub.StreamHub { return r.Hub }

// HubSource labels the hub for the flow-path observability view.
func (r *H265Recorder) HubSource() string { return "h265" }

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

// connectAndRecord implements rtspConnector. It delegates to the shared
// connectAndRecordShared template (issue #1012 block 3), supplying the H.265
// video negotiation (FindFormat/decoder/SETUP + SDP VPS/SPS/PPS seed) and
// the H.265 RTP decode semantics.
func (r *H265Recorder) connectAndRecord(ctx context.Context) (error, bool) {
	return r.connectAndRecordShared(ctx, rtspConnectHooks{
		log: h265Logger,
		setupVideo: func(client *gortsplib.Client, desc *description.Session) (*rtspVideoStream, error) {
			var forma *format.H265
			medi := desc.FindFormat(&forma)
			if medi == nil {
				return nil, fmt.Errorf("H265 media not found in stream")
			}
			rtpDec, err := forma.CreateDecoder()
			if err != nil {
				return nil, fmt.Errorf("create RTP decoder: %w", err)
			}
			if _, err := client.Setup(desc.BaseURL, medi, 0, 0); err != nil {
				return nil, fmt.Errorf("SETUP: %w", err)
			}

			// Store initial parameter sets from SDP. Merge with any params
			// already captured and store atomically as a full triplet so
			// concurrent live-preview reads see a consistent VPS/SPS/PPS
			// set (#219).
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

			return &rtspVideoStream{
				medi:   medi,
				forma:  forma,
				decode: rtpDec.Decode,
				silentDecodeErr: func(err error) bool {
					return errors.Is(err, rtph265.ErrNonStartingPacketAndNoPrevious) ||
						errors.Is(err, rtph265.ErrMorePacketsNeeded)
				},
				isIDR: func(au [][]byte) bool { return nalutil.IsIDR(au, true) },
			}, nil
		},
		// H265's copy read the raw ChannelConfig (7 for 8-channel AAC, where
		// the derived ChannelCount is 8); H264's read the derived count —
		// kept per-codec (issue #1012).
		aacChannels: func(_, channelConfig int) int { return channelConfig },
	})
}
