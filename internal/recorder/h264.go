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
	"github.com/bluenviron/gortsplib/v5/pkg/format/rtph264"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/metrics"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model/nalutil"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/muxer"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/streamhub"
)

var h264Logger = slogx.Component("h264-recorder")

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
	SetMergeStatus(ctx context.Context, ids []string, status string) error
}

const (
	DefaultSegmentDur           = 10 * time.Minute
	DefaultRingBufCap           = 300
	defaultFrameWatchdogTimeout = 30 * time.Second // Max wait for frame data before reconnecting
)

// H264Config is a type alias for BaseConfig.
// This allows flat struct literals (e.g., H264Config{CameraID: "..."}) while
// sharing the common configuration fields across all RTSP recorder types.
type H264Config = BaseConfig

// H264NALDriver implements codecDriver for H.264 video.
type H264NALDriver struct{}

func (d H264NALDriver) codecLabel() string          { return "h264" }
func (d H264NALDriver) segmentFormat() model.Format { return model.FormatH264 }
func (d H264NALDriver) rtpFormat() format.Format    { return &format.H264{} }
func (d H264NALDriver) minNALUDataLen() int         { return 5 }
func (d H264NALDriver) naluType(firstByte byte) int { return int(firstByte & 0x1F) }
func (d H264NALDriver) isIDR(typ int) bool          { return typ == 5 }
func (d H264NALDriver) isParameterSet(typ int) bool { return typ == 7 || typ == 8 }
func (d H264NALDriver) isVCL(typ int) bool          { return typ == 1 || typ == 5 }
func (d H264NALDriver) paramSetsReady(b *baseRecorder) bool {
	sps, pps, _ := b.codecSnapshot()
	return sps != nil && pps != nil
}

func (d H264NALDriver) handleParamSet(b *baseRecorder, nalu []byte, typ int) bool {
	// Load the current snapshot once; this method runs only on the single
	// writeFrames goroutine, so load-then-store is race-free. We always rebuild
	// and store the full triplet so a concurrent reader (live preview via
	// codecSnapshot) never sees a torn mix of old+new params (#219).
	sps, pps, _ := b.codecSnapshot()
	switch typ {
	case 7: // SPS
		// The muxer==nil guard keeps param sync at connect (SDP sprop seed vs
		// first in-band delivery) from consuming rotation budget — there is no
		// segment to protect yet, so the fresh bytes are just cached.
		if sps != nil && !bytes.Equal(sps, nalu) && b.muxer != nil {
			// #642: cameras may alternate decode-equivalent SPS encodings
			// (VUI/timing-only differences) per GOP — only rotate on a real
			// codec change; always cache the fresh bytes for the next segment.
			if b.paramGate.ShouldRotateSPS(sps, nalu, false, time.Now()) {
				b.log.Info("SPS change detected, rotating segment", "camera_id", b.cfg.CameraID)
				b.setCodecParams(nalu, pps, nil)
				return true
			}
			b.log.Debug("SPS variant is decode-compatible, keeping segment open",
				"camera_id", b.cfg.CameraID)
		}
		b.setCodecParams(nalu, pps, nil)
	case 8: // PPS
		if pps != nil && !bytes.Equal(pps, nalu) && b.muxer != nil {
			// No semantic PPS parser — rate-limit rapid variant alternation.
			if b.paramGate.ShouldRotateUnparsed(time.Now()) {
				b.log.Info("PPS change detected, rotating segment", "camera_id", b.cfg.CameraID)
				b.setCodecParams(sps, nalu, nil)
				return true
			}
		}
		b.setCodecParams(sps, nalu, nil)
	}
	return false
}

func (d H264NALDriver) extractParamSets(b *baseRecorder, au [][]byte) {
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

func (d H264NALDriver) addTrack(m *muxer.MP4Muxer, b *baseRecorder) (int, error) {
	sps, pps, _ := b.codecSnapshot()
	return m.AddH264Track(sps, pps)
}

// H264Recorder records H.264 video from an RTSP source.
type H264Recorder struct {
	*baseRecorder
}

// Interface compliance checks.
var (
	_ model.Recorder = (*H264Recorder)(nil)
	_ rtspConnector  = (*H264Recorder)(nil)
)

// NewH264Recorder creates a new H264Recorder.
func NewH264Recorder(cfg H264Config, store SegmentStore, opts ...*metrics.Metrics) *H264Recorder {
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

	rec := &H264Recorder{}
	b := &baseRecorder{
		driver:       H264NALDriver{},
		cfg:          cfg,
		store:        store,
		mtrics:       m,
		frameBufPool: newFrameBufPool(),
		status:       model.StatusStopped,
		log:          h264Logger,
	}
	rec.baseRecorder = b
	b.self = rec
	return rec
}

// Start implements model.Recorder.
func (r *H264Recorder) Start(ctx context.Context) error {
	return r.start(ctx)
}

// Stop implements model.Recorder.
func (r *H264Recorder) Stop() error {
	return r.stop()
}

// Status implements model.Recorder.
func (r *H264Recorder) Status() model.RecorderStatus {
	return r.getStatus()
}

// GetHub returns the StreamHub for frame fan-out.
func (r *H264Recorder) GetHub() *streamhub.StreamHub { return r.Hub }

// HubSource labels the hub for the flow-path observability view.
func (r *H264Recorder) HubSource() string { return "h264" }

// SPS returns the current H264 Sequence Parameter Set NAL unit (without start bytes).
// Reads the atomic codec snapshot — safe for concurrent live-preview reads (#219).
func (r *H264Recorder) SPS() []byte { s, _, _ := r.codecSnapshot(); return s }

// PPS returns the current H264 Picture Parameter Set NAL unit (without start bytes).
// Reads the atomic codec snapshot — safe for concurrent live-preview reads (#219).
func (r *H264Recorder) PPS() []byte { _, p, _ := r.codecSnapshot(); return p }

// CodecParams implements model.HLSProvider, returning the current H.264 codec
// and parameter-set snapshot in a single atomic read. This lets getCodecParams
// (handlers_stream.go) use the HLSProvider fast-path instead of the concrete
// type switch, and guarantees a consistent (non-torn) SPS/PPS pair (#219).
// vps is always nil for H.264.
func (r *H264Recorder) CodecParams() (codec model.Format, sps, pps, vps []byte) {
	sps, pps, vps = r.codecSnapshot()
	return model.FormatH264, sps, pps, vps
}

// AudioCodec returns the audio codec name ("aac", "g711", or "" for no audio).
// Reads the immutable audio snapshot (#226); safe to call from any goroutine
// (WS/relay/status handlers concurrently with connectAndRecord).
func (r *H264Recorder) AudioCodec() string {
	if a := r.audioSnapshot(); a != nil {
		return a.codec
	}
	return ""
}

// AudioConfig returns a copy of the audio codec configuration bytes.
// Returns nil when no audio is configured. The returned slice is a fresh copy
// so callers may mutate it freely (#226).
func (r *H264Recorder) AudioConfig() []byte {
	if a := r.audioSnapshot(); a != nil && a.muxerConfig != nil {
		return append([]byte(nil), a.muxerConfig...)
	}
	return nil
}

// AudioSampleRate returns the audio sample rate in Hz, or 0 if no audio.
func (r *H264Recorder) AudioSampleRate() int {
	if a := r.audioSnapshot(); a != nil {
		return a.sampleRate
	}
	return 0
}

// AudioChannels returns the number of audio channels, or 0 if no audio.
func (r *H264Recorder) AudioChannels() int {
	if a := r.audioSnapshot(); a != nil {
		return a.channels
	}
	return 0
}

// connectAndRecord implements rtspConnector. It delegates to the shared
// connectAndRecordShared template (issue #1012 block 3), supplying the H.264
// video negotiation (FindFormat/decoder/SETUP + SDP SPS/PPS seed) and the
// H.264 RTP decode semantics.
func (r *H264Recorder) connectAndRecord(ctx context.Context) (error, bool) {
	return r.connectAndRecordShared(ctx, rtspConnectHooks{
		log: h264Logger,
		setupVideo: func(client *gortsplib.Client, desc *description.Session) (*rtspVideoStream, error) {
			var forma *format.H264
			medi := desc.FindFormat(&forma)
			if medi == nil {
				return nil, fmt.Errorf("H264 media not found in stream")
			}
			rtpDec, err := forma.CreateDecoder()
			if err != nil {
				return nil, fmt.Errorf("create RTP decoder: %w", err)
			}
			if _, err := client.Setup(desc.BaseURL, medi, 0, 0); err != nil {
				return nil, fmt.Errorf("SETUP: %w", err)
			}

			// Store initial parameter sets from SDP. Merge with any params
			// already captured (rare, but keeps the snapshot consistent) and
			// store atomically as a full triplet so concurrent live-preview
			// reads see a consistent pair (#219).
			sps, pps, _ := r.codecSnapshot()
			if forma.SPS != nil {
				sps = forma.SPS
			}
			if forma.PPS != nil {
				pps = forma.PPS
			}
			r.setCodecParams(sps, pps, nil)

			return &rtspVideoStream{
				medi:   medi,
				forma:  forma,
				decode: rtpDec.Decode,
				silentDecodeErr: func(err error) bool {
					return errors.Is(err, rtph264.ErrNonStartingPacketAndNoPrevious) ||
						errors.Is(err, rtph264.ErrMorePacketsNeeded)
				},
				isIDR: func(au [][]byte) bool { return nalutil.IsIDR(au, false) },
			}, nil
		},
		// H264's copy read the derived ChannelCount (8 for channel-config 7);
		// H265's read the raw ChannelConfig — kept per-codec (issue #1012).
		aacChannels: func(channelCount, _ int) int { return channelCount },
	})
}
