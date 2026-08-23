// Package recorder: base.go — shared base for RTSP video recorders (H.264/H.265).
//
// This file defines the architectural scaffolding for eliminating the ~600 lines
// of duplication between H264Recorder and H265Recorder. Concrete recorders embed
// *baseRecorder and provide a codecDriver implementation.
//
// DESIGN OVERVIEW
//
// The template method pattern is used:
//
//	                            ┌─────────────────┐
//	                            │  codecDriver    │  ← strategy (codec-specific)
//	                            │  (interface)    │
//	            ┌───────────────┴───────────────┘
//	            │               │
//	baseRecorder│              H264Driver   H265Driver  (T14, not yet)
//	  ├ start()│
//	  ├ stop() │           ┌── H264Recorder ── embed *baseRecorder ── (T14)
//	  ├ status()│          │   + connectAndRecord() [codec-specific RTSP setup]
//	  ├ run()  │←template──┤
//	  │        │           └── H265Recorder ── embed *baseRecorder ── (T14)
//	  ├ writeFrames() ←template (calls driver hooks for NAL differences)
//	  ├ closeCurrentSegment() ← shared (uses driver.segmentFormat())
//	  └ metrics helpers      ← shared (uses driver.codecLabel())
//
// run() and writeFrames() are template methods: they own the shared algorithm
// structure and delegate codec-specific decisions to driver hooks.
//
// connectAndRecord() is NOT a driver method — it stays on the concrete recorder
// because the gortsplib RTP format types (*format.H264 vs *format.H265) and their
// decoders are fundamentally different types. baseRecorder.run() dispatches to it
// via the rtspConnector interface (stored in the `self` field).

package recorder

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bluenviron/gortsplib/v5/pkg/format"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/event"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/metrics"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/muxer"
)

// BaseConfig holds the shared configuration fields used by all RTSP video
// recorders. H264Config and H265Config embed BaseConfig to eliminate the
// duplication of these fields across the H.264 and H.265 recorder configs.
type BaseConfig struct {
	CameraID               string
	RTSPURL                string
	Username               string
	Password               string
	SegmentDur             time.Duration
	RingBufCap             int
	DB                     RecordingDB
	AudioEnabled           bool
	FrameWatchdogTimeout   time.Duration // default 30s (0 = use defaultFrameWatchdogTimeout)
	EventBus               *event.EventBus
	DarkFrameFilterEnabled bool // skip dark/night segments (MJPEG/AVI only)
	DarkFrameThreshold     int  // luminance threshold 0-255 (default 15)
	// RecordEnabled gates whether segments are written to disk. When false the
	// recorder stays connected and keeps feeding the StreamHub (so live preview,
	// relay, and health all work) but writes nothing — a "live-only" / stream-
	// forward-only mode. Driven by per-camera recording_enabled (issue #36:
	// users running the NVR purely as a live/relay gateway, no SD-card writes).
	// Defaults to true (nil => record); set to a pointer to false to opt out.
	RecordEnabled *bool

	// Adaptive enables dynamic-timelapse write density (issue #435,
	// recording_mode: adaptive). Non-nil arms the per-connection adaptive
	// tracker in writeFrames: sustained calm → sparse keyframe writing, any
	// activity spike → GOP-buffer flush + full-rate resume. H.264/H.265 only
	// (the signal is P-frame size vs baseline). Nil = plain continuous
	// recording with zero added per-frame cost.
	Adaptive *AdaptiveConfig
}

// rtspConnector is implemented by concrete RTSP recorders to provide the
// codec-specific RTSP connection and recording loop. baseRecorder.run() calls
// connectAndRecord() in its reconnect loop via this interface.
//
// The method is kept on the concrete recorder (not in codecDriver) because the
// RTSP format negotiation uses codec-specific types (*format.H264 vs *format.H265)
// and their decoders (rtph264.Decoder vs rtph265.Decoder) that cannot be cleanly
// abstracted behind an interface.
type rtspConnector interface {
	connectAndRecord(ctx context.Context) (err error, wasConnected bool)
}

// codecDriver abstracts the codec-specific NAL-level operations that differ
// between H.264 and H.265. It is the strategy in the template method pattern:
// baseRecorder owns the shared lifecycle (start/stop/run/closeCurrentSegment),
// while the driver owns NAL semantics and muxer track creation.
//
// All methods that need shared state (parameter sets, audio config) receive
// *baseRecorder, keeping a single source of truth and avoiding field duplication
// in the driver.
//
// NOTE: FindFormat in gortsplib v5.5+ takes `any` (not generics), so the driver
// could own the FindFormat call. However, the complete RTSP media setup (format
// finding + decoder creation + SETUP + RTP callback registration) is kept in the
// concrete recorder's connectAndRecord() because the decoder types differ.
// rtpFormat() exists for introspection and potential future use.
type codecDriver interface {
	// codecLabel returns the short codec identifier used in Prometheus metric
	// labels and log component tags. E.g. "h264", "h265".
	codecLabel() string

	// segmentFormat returns the model.Format constant for this codec,
	// used in recording metadata and segment creation.
	// H.264: model.FormatH264
	// H.265: model.FormatH265
	segmentFormat() model.Format

	// rtpFormat returns a new zero-value instance of the codec-specific RTP
	// format type, typed as the generic format.Format interface.
	// H.264: &format.H264{}
	// H.265: &format.H265{}
	//
	// NOTE: gortsplib's Session.FindFormat(forma any) uses reflection internally,
	// requiring a pointer-to-pointer of the concrete type. The concrete driver
	// should own the FindFormat call within connectAndRecord(). This method is
	// provided for introspection and testing.
	rtpFormat() format.Format

	// minNALUDataLen returns the minimum number of bytes a frame must have to
	// be parseable (including the 4-byte start-code prefix 00 00 00 01).
	// H.264: 5 (4-byte prefix + 1-byte NAL header)
	// H.265: 6 (4-byte prefix + 2-byte NAL header)
	minNALUDataLen() int

	// naluType extracts the NAL unit type from the first byte of the NALU
	// (the byte immediately after the 4-byte start-code prefix).
	// H.264: firstByte & 0x1F (5-bit type field from 1-byte NAL header)
	// H.265: (firstByte >> 1) & 0x3F (6-bit type field from 2-byte NAL header)
	naluType(firstByte byte) int

	// isIDR reports whether the NAL type is an IDR frame (keyframe).
	// H.264: typ == 5 (IDR slice)
	// H.265: typ == 19 (IDR_W_RADL) || typ == 20 (IDR_N_LP)
	isIDR(typ int) bool

	// isParameterSet reports whether the NAL type carries a parameter set.
	// H.264: typ == 7 (SPS) || typ == 8 (PPS)
	// H.265: typ == 32 (VPS) || typ == 33 (SPS) || typ == 34 (PPS)
	isParameterSet(typ int) bool

	// isVCL reports whether the NAL type is a Video Coding Layer unit — a
	// coded slice that should be written to the muxer (as opposed to parameter
	// sets, SEI, delimiter, or other non-VCL NALUs).
	// H.264: typ == 1 (non-IDR slice) || typ == 5 (IDR slice)
	// H.265: typ < 32 (all VCL types occupy range 0–31)
	isVCL(typ int) bool

	// paramSetsReady reports whether all required parameter sets have been
	// received and a new segment can be safely created without producing
	// unplayable output.
	// H.264: b.sps != nil && b.pps != nil
	// H.265: b.vps != nil && b.sps != nil && b.pps != nil
	paramSetsReady(b *baseRecorder) bool

	// handleParamSet processes a parameter-set NALU. It stores the NALU in
	// the appropriate field on baseRecorder (sps, pps, or vps) and returns
	// true if the parameter set changed from the previously stored value,
	// indicating the current segment should be rotated to avoid mixing
	// samples with different decoder configuration.
	handleParamSet(b *baseRecorder, nalu []byte, typ int) (changed bool)

	// extractParamSets inspects an access unit (decoded NALUs without start
	// codes) and seeds any parameter sets found into the baseRecorder.
	// This is used during RTSP DESCRIBE to pre-populate param sets from the
	// SDP before the first in-band NALU arrives, ensuring the recorder can
	// start recording immediately on the first IDR.
	//
	// The concrete implementation iterates the AU, calls naluType on each
	// NALU's first byte, and invokes handleParamSet for parameter-set types.
	extractParamSets(b *baseRecorder, au [][]byte)

	// addTrack creates the codec-specific video track in the MP4 muxer using
	// the parameter sets stored on baseRecorder, returning the track ID.
	// H.264: m.AddH264Track(b.sps, b.pps)
	// H.265: m.AddH265Track(b.vps, b.sps, b.pps)
	addTrack(m *muxer.MP4Muxer, b *baseRecorder) (trackID int, err error)
}

// baseRecorder contains the shared state and lifecycle methods for RTSP-based
// video recorders. Concrete recorders (H264Recorder, H265Recorder) embed
// *baseRecorder and set the driver + self fields in their constructors.
//
// FIELD ORGANIZATION:
//   - driver/self: strategy + virtual-dispatch indirection
//   - cfg/store/metrics/log: dependencies
//   - mu/status/cancel/done: lifecycle state
//   - muxer/trackID/segStart/etc: segment state (written by writeFrames)
//   - vps/sps/pps: codec parameter sets (written by driver.handleParamSet)
//   - audio*: audio track state (set during connectAndRecord, read during segment creation)
//   - frameCh/dropped/lastPTS: frame pipeline
//   - Hub: stream fan-out to HLS/WebRTC/etc. consumers
//
// baseRecorder is the shared base for RTSP-based recorders (H264Recorder,
// H265Recorder). Its fields follow a three-tier locking discipline (#226):
//
//   - Tier 1 — lifecycle + per-segment state, guarded by mu:
//     status, cancel, done, muxer, audioTrackID, segStart.
//     mu serializes multi-field consistency (e.g. publishing muxer+segStart+
//     audioTrackID together so the audio RTP callback sees an aligned triplet).
//     The writeFrames goroutine is the sole writer of muxer/segStart/
//     audioTrackID; the lock exists for the RTP-callback and lifecycle readers.
//
//   - Tier 2 — cross-goroutine configuration, stored behind atomic.Pointer as
//     an immutable snapshot:
//     codec (SPS/PPS/VPS, #219) and audio (codec/sampleRate/channels/muxerConfig,
//     #226). Both are written once during connectAndRecord and read from many
//     goroutines (writeFrames, audio RTP callbacks, and the external
//     HLS/WebRTC/WS/relay/status accessor paths). The snapshot's immutability
//     after Store makes these reads race-free without a mutex.
//
//   - Tier 3 — writeFrames-owned, NO lock (single-goroutine invariant):
//     trackID, curFinalPath, curTempPath, frameCount, lastFrameTime.
//     Only the writeFrames goroutine reads/writes these; HTTP handlers and RTP
//     callbacks MUST NOT touch them.
//
// Note: muxer.MP4Muxer itself is goroutine-safe (WriteSample/WriteAudioSample
// each take the muxer's own mutex), so concurrent video (writeFrames) and
// audio (RTP callback) writes are safe by construction — the concern in #226
// was the audio *config* fields, now migrated to the Tier-2 snapshot.
type baseRecorder struct {
	// driver provides codec-specific behavior (NAL parsing, track creation).
	driver codecDriver

	// self enables virtual dispatch for connectAndRecord() from the shared
	// run() template method. Set by the concrete recorder's constructor.
	// Without this, Go embedding does not provide virtual method dispatch.
	self rtspConnector

	// Dependencies (set once in constructor).
	cfg    BaseConfig
	store  SegmentStore
	mtrics *metrics.Metrics // "mtrics" avoids collision with package name "metrics"
	log    *slog.Logger

	// Lifecycle state (guarded by mu).
	mu     sync.Mutex
	status model.RecorderStatus
	cancel context.CancelFunc
	done   chan struct{}

	// Active segment state (Tier 1: written from writeFrames, read from audio
	// RTP callbacks under mu). muxer is published together with segStart and
	// audioTrackID in createNewSegment so the audio callback observes an
	// aligned (muxer, trackID, segStart) triplet.
	muxer         *muxer.MP4Muxer
	trackID       int
	audioTrackID  int
	curFinalPath  string
	curTempPath   string
	segStart      time.Time
	frameCount    int
	lastFrameTime time.Time

	// Codec parameter sets (Tier 2, #219): immutable snapshot behind
	// atomic.Pointer so the live-preview (HLS/WebRTC/WS) goroutines can read
	// SPS/PPS/VPS concurrently with the writeFrames writer without a data race
	// or torn triplet reads. vps is H.265-only; always nil for H.264.
	codec atomic.Pointer[codecParams]

	// Audio configuration (Tier 2, #226): immutable snapshot behind
	// atomic.Pointer, detected once during connectAndRecord and read by the
	// external accessors (AudioCodec/AudioConfig/AudioSampleRate/AudioChannels,
	// called from WS/relay/status goroutines) and the G.711 RTP callback.
	// See audioSnapshot / setAudioConfig.
	audio atomic.Pointer[audioConfig]

	// Frame pipeline (written from RTP callback goroutine, read from writeFrames).
	frameCh chan []byte
	dropped atomic.Int64
	lastPTS atomic.Int64 // for PTS monotonicity check

	// Stream fan-out to HLS, WebRTC, etc. Initialized by camera manager
	// via initStreamHub(). Non-blocking broadcasts.
	Hub *model.StreamHub

	// Adaptive write-density state (issue #435). Tier 3: created and owned
	// exclusively by the writeFrames goroutine when cfg.Adaptive != nil;
	// nil otherwise. Never touched by other goroutines.
	adaptive *adaptiveTracker

	// audioSparse gates DISK writes of audio while the adaptive tracker is in
	// sparse (timelapse) mode. Read by the audio RTP callbacks on their own
	// goroutines — hence atomic. Live-preview audio (Hub.BroadcastAudio) is
	// never gated.
	audioSparse atomic.Bool

	// Throttled logging for storage health failures.
	lastHealthLogAt time.Time
}

// codecParams is an immutable snapshot of the video codec parameter sets
// (SPS/PPS/VPS). It is written by setCodecParams (deep-copying the inputs) and
// read via codecSnapshot's single atomic load. Immutability after construction
// is what makes the concurrent reader path (live preview via SPS()/PPS()/VPS())
// race-free without a mutex (#219). vps is H.265-only; always nil for H.264.
type codecParams struct {
	sps []byte
	pps []byte
	vps []byte
}

// setCodecParams atomically replaces the codec parameter snapshot. The slices
// are deep-copied so the stored snapshot is independent of the caller's buffers
// (which may be reused RTP scratch buffers). Intended to be called from the
// single writer path (writeFrames via the driver's handleParamSet, and the SDP
// pre-seed in connectAndRecord) — concurrent callers must not interleave
// partial updates; build the full triplet first, then Store.
func (b *baseRecorder) setCodecParams(sps, pps, vps []byte) {
	b.codec.Store(&codecParams{
		sps: append([]byte(nil), sps...),
		pps: append([]byte(nil), pps...),
		vps: append([]byte(nil), vps...),
	})
}

// codecSnapshot returns the current SPS/PPS/VPS via a single atomic load,
// guaranteeing a consistent triplet (no torn read where SPS comes from one
// configuration and PPS from another). Returns all-nil before the first
// keyframe/SDP arrives.
func (b *baseRecorder) codecSnapshot() (sps, pps, vps []byte) {
	if cp := b.codec.Load(); cp != nil {
		return cp.sps, cp.pps, cp.vps
	}
	return nil, nil, nil
}

// audioConfig is an immutable snapshot of the recorder's audio configuration,
// detected once during connectAndRecord (AAC or G.711 SDP negotiation) and
// thereafter read-only. It is stored behind atomic.Pointer so the external
// reader paths — AudioCodec()/AudioConfig()/AudioSampleRate()/AudioChannels()
// accessors (called from the WS live-preview, relay engine, and camera-status
// goroutines) and the G.711 RTP callback — can read it concurrently with the
// connectAndRecord writer without a data race or a torn view (e.g. an AAC
// codec string paired with a G.711 muxerConfig). Mirrors the codecParams
// pattern from #219. muxerConfig is deep-copied on store so the snapshot is
// independent of the writer's buffer.
type audioConfig struct {
	codec          string // "aac", "g711", or "" for no audio
	sampleRate     int    // unified sample rate (Hz)
	channels       int    // 0 when no audio
	muxerConfig    []byte // AAC: AudioSpecificConfig; G.711: [muLawFlag, rate×4 BE]
	g711MULaw      bool
	g711SampleRate int
}

// setAudioConfig atomically replaces the audio configuration snapshot. The
// muxerConfig slice is deep-copied (see codecParams). Intended to be called
// once per audio codec detection in connectAndRecord (AAC path and G.711 path
// each call it once with their fully-built config); concurrent callers must
// not interleave partial updates (#226).
func (b *baseRecorder) setAudioConfig(cfg *audioConfig) {
	if cfg == nil {
		b.audio.Store(nil)
		return
	}
	stored := &audioConfig{
		codec:          cfg.codec,
		sampleRate:     cfg.sampleRate,
		channels:       cfg.channels,
		g711MULaw:      cfg.g711MULaw,
		g711SampleRate: cfg.g711SampleRate,
	}
	if cfg.muxerConfig != nil {
		stored.muxerConfig = append([]byte(nil), cfg.muxerConfig...)
	}
	b.audio.Store(stored)
}

// audioSnapshot returns the current audio configuration via a single atomic
// load, or nil when no audio has been configured yet.
func (b *baseRecorder) audioSnapshot() *audioConfig {
	return b.audio.Load()
}

// ---------------------------------------------------------------------------
// Shared lifecycle methods (template method pattern)
// ---------------------------------------------------------------------------

// start is the shared Start logic for all RTSP video recorders. The concrete
// recorder's Start() method delegates to this after any codec-specific setup.
func (b *baseRecorder) start(ctx context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.status == model.StatusRecording || b.status == model.StatusReconnecting {
		return fmt.Errorf("recorder for %q already running", b.cfg.CameraID)
	}
	ctx, cancel := context.WithCancel(ctx)
	b.cancel = cancel
	b.done = make(chan struct{})
	b.status = model.StatusRecording
	b.incActive()
	go b.run(ctx)
	return nil
}

// stop is the shared Stop logic. Cancels the context and waits for the run
// goroutine to exit.
func (b *baseRecorder) stop() error {
	b.mu.Lock()
	if b.cancel != nil {
		b.cancel()
	}
	b.mu.Unlock()
	if b.done != nil {
		<-b.done
	}
	b.decActive()
	return nil
}

// getStatus returns the current recorder status (thread-safe). Named
// getStatus (not status) to avoid collision with the status field.
// Concrete recorders' public Status() method delegates to this.
func (b *baseRecorder) getStatus() model.RecorderStatus {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.status
}

// setStatus updates the recorder status (thread-safe).
func (b *baseRecorder) setStatus(s model.RecorderStatus) {
	b.mu.Lock()
	b.status = s
	b.mu.Unlock()
}

// ---------------------------------------------------------------------------
// Template method: run() — auto-reconnect loop
// ---------------------------------------------------------------------------

// run is the template method for the auto-reconnect loop. It wraps
// connectAndRecord() (provided by the concrete recorder via self) with
// exponential backoff + jitter. Panic recovery ensures the goroutine never
// crashes silently.
//
// This method is identical between H.264 and H.265 — the only difference is
// the logger, which comes from b.log (set by the concrete constructor).
func (b *baseRecorder) run(ctx context.Context) {
	defer b.recoverPanic("run")
	defer close(b.done)
	defer b.setStatus(model.StatusStopped)

	var retryCount int
	for {
		err, connected := b.self.connectAndRecord(ctx)
		if ctx.Err() != nil {
			return
		}
		if connected {
			retryCount = 0
			if b.mtrics != nil {
				b.mtrics.CameraReconnectBackoffSeconds.WithLabelValues(b.cfg.CameraID).Set(0)
			}
		}
		retryCount++
		backoff := TieredBackoffWithJitter(retryCount)
		storageFailed := isStorageFailed(b.store, b.cfg.CameraID)
		if storageFailed {
			backoff = StorageBackoffWithJitter()
		}
		if b.mtrics != nil {
			b.mtrics.CameraReconnectBackoffSeconds.WithLabelValues(b.cfg.CameraID).Set(backoff.Seconds())
		}
		b.log.Error("connection error, reconnecting",
			"camera_id", b.cfg.CameraID, "error", err,
			"backoff", backoff, "attempt", retryCount, "storage_failed", storageFailed)
		b.recordError("connection")
		b.setStatus(model.StatusReconnecting)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
	}
}

// ---------------------------------------------------------------------------
// Template method: writeFrames() — NAL processing loop
// ---------------------------------------------------------------------------

// writeFrames is the template method for the NAL processing loop. It reads
// NALUs (with 4-byte start-code prefixes) from frameCh and processes them
// through a codec-agnostic algorithm that delegates codec-specific decisions
// to the driver.
//
// Algorithm:
//  1. Validate minimum data length (driver.minNALUDataLen)
//  2. Extract NAL type (driver.naluType)
//  3. Handle parameter sets → store + rotate segment if changed (driver hooks)
//  4. Skip non-VCL NALUs (driver.isVCL)
//  5. Check storage health (shared)
//  6. Check parameter sets ready (driver.paramSetsReady)
//  7. Wait for IDR before creating segment (driver.isIDR)
//  8. Create segment if needed (shared + driver.addTrack)
//  9. Write sample to muxer (shared)
//  10. Check segment duration rollover (shared)
func (b *baseRecorder) writeFrames(done chan struct{}) {
	defer b.recoverPanic("writeFrames")
	defer close(done)

	// Adaptive write-density tracker (issue #435). Rebuilt per connection so
	// a reconnect always starts in NORMAL mode with a fresh baseline (a
	// reconnect storm can't oscillate the mode). nil when Adaptive recording
	// is not configured — the gate below is then a single nil check per frame.
	if b.cfg.Adaptive != nil {
		b.adaptive = newAdaptiveTracker(*b.cfg.Adaptive, b.cfg.CameraID, b.log)
		b.audioSparse.Store(false)
	}
	sparseAudio := false

	for data := range b.frameCh {
		// Always parse NALUs to capture codec parameter sets (VPS/SPS/PPS), even
		// in live-only mode (RecordEnabled=false). Live preview (HLS/WebRTC/WS)
		// needs these via getCodecParams(); if we skip parsing here, the codec
		// snapshot stays nil and every live-preview endpoint returns 503 "waiting
		// for video stream" → permanent grey/black screen for cameras with
		// recording disabled. (Only the SDP pre-seed in connectAndRecord runs
		// otherwise, which is empty for cameras that send params in-band only.)
		if len(data) < b.driver.minNALUDataLen() {
			continue
		}
		nalu := data[4:] // strip 4-byte start-code prefix
		typ := b.driver.naluType(nalu[0])

		// Step 3: Handle parameter sets (SPS/PPS for H.264, VPS/SPS/PPS for H.265).
		if b.driver.isParameterSet(typ) {
			if b.driver.handleParamSet(b, nalu, typ) {
				b.closeCurrentSegment()
			}
			continue
		}

		// Live-only mode: drain the frame channel (so the RTP callback's
		// non-blocking send never blocks) but perform no segment I/O at all.
		// The StreamHub fan-out already happened in the RTP callback before the
		// send to frameCh, so live preview, relay, and health keep working.
		// nil => recording enabled (default); pointer to false => live-only.
		// (Reached AFTER parameter-set capture above, so live preview still has
		// the codec params it needs.)
		if b.cfg.RecordEnabled != nil && !*b.cfg.RecordEnabled {
			continue
		}

		// Step 4: Skip non-VCL NALUs (SEI, delimiter, etc.).
		if !b.driver.isVCL(typ) {
			continue
		}

		// Step 4.5: adaptive write-density gate (issue #435). While the
		// compressed-domain activity signal is calm, only sparse keyframes
		// reach the disk; on a spike the retained GOP ring is flushed and
		// full-rate writing resumes through the normal path below.
		if b.adaptive != nil {
			now := time.Now()
			isIDR := b.driver.isIDR(typ)
			spike, flush := b.adaptive.observe(nalu, isIDR, now)
			if len(flush) > 0 {
				// Activity burst exiting timelapse: resume full-rate writing with
				// the flushed GOP (complete reference chain since the last IDR)
				// FIRST, so the resume has no missing references. This must be
				// keyed on the flush return, not on mode: observe() has already
				// switched to NORMAL by the time it returns, so the previous
				// `if mode == adaptiveTimelapse { if spike { writeFlushedGOP } }`
				// never ran — the exit frame was written with dangling references
				// (decode artifacts until the next IDR) and the retained pre-buffer
				// was silently dropped (found via the AdaptiveGate facade test,
				// issue #466 field test).
				b.writeFlushedGOP(flush)
			}
			if b.adaptive.mode == adaptiveTimelapse && !spike {
				if !b.adaptive.shouldWriteSparse(isIDR, now) {
					// Sparse: the frame is retained in the GOP ring, not
					// written; a later spike can still flush it.
					if sa := b.adaptive.mode == adaptiveTimelapse; sa != sparseAudio {
						sparseAudio = sa
						b.audioSparse.Store(sa)
					}
					continue
				}
				// Periodic sparse keyframe: falls through to the normal write
				// path (Step 7's IDR requirement is satisfied by definition).
				// Stamp the cadence so the NEXT sparse write waits a full
				// timelapse_interval — without this every IDR passes the
				// shouldWriteSparse check and the sparse mode degenerates to
				// one-keyframe-per-GOP (found on the Docker VM field test).
				if isIDR {
					b.adaptive.lastSparseWrite = now
				}
			}
			if sa := b.adaptive.mode == adaptiveTimelapse; sa != sparseAudio {
				sparseAudio = sa
				b.audioSparse.Store(sa)
			}
		}

		// Step 5: Storage health check — skip recording but keep stream alive.
		if isStorageFailed(b.store, b.cfg.CameraID) {
			b.handleStorageFailure()
			continue
		}

		// Step 6: Ensure all required parameter sets are available.
		if !b.driver.paramSetsReady(b) {
			continue
		}

		// Step 7: Wait for an IDR frame before starting a new segment.
		// Without this, segments may start with P-frames that have no
		// reference, causing black/gray output until the first IDR.
		if b.muxer == nil && !b.driver.isIDR(typ) {
			continue
		}

		// Step 8: Create new segment if needed.
		if b.muxer == nil {
			if !b.createNewSegment() {
				continue
			}
		}

		// Step 9: Write the NALU sample to the muxer.
		now := time.Now()
		pts := now.Sub(b.segStart)
		duration := now.Sub(b.lastFrameTime)
		if duration < time.Millisecond {
			duration = time.Millisecond
		}
		b.lastFrameTime = now

		if err := b.muxer.WriteSample(b.trackID, nalu, pts, duration); err != nil {
			b.log.Error("failed to write sample",
				"camera_id", b.cfg.CameraID, "error", err)
			continue
		}
		b.frameCount++

		// Step 10: Check segment duration rollover.
		if time.Since(b.segStart) >= b.cfg.SegmentDur {
			b.closeCurrentSegment()
		}
	}
}

// writeFlushedGOP writes the adaptive tracker's retained GOP frames (the
// complete reference chain since the last IDR) into the current or a fresh
// segment, back-dating the segment start so pts values stay non-negative.
// Called only from writeFrames on the TIMELAPSE→NORMAL transition; frames
// always start with an IDR (guaranteed by adaptiveTracker.takeGOP).
func (b *baseRecorder) writeFlushedGOP(frames []gopFrame) {
	if len(frames) == 0 {
		return
	}
	if isStorageFailed(b.store, b.cfg.CameraID) {
		b.handleStorageFailure()
		return
	}
	if !b.driver.paramSetsReady(b) {
		return
	}
	for _, f := range frames {
		if b.muxer == nil {
			if !f.isIDR {
				continue // never start a segment on a P frame
			}
			if !b.createNewSegment() {
				return
			}
			// createNewSegment stamps segStart with "now"; back-date it to
			// the first flushed frame so pts = at - segStart >= 0. Published
			// under mu like every segStart write (the audio callback reads it).
			b.mu.Lock()
			b.segStart = f.at
			b.mu.Unlock()
			b.lastFrameTime = f.at
		}
		pts := f.at.Sub(b.segStart)
		dur := f.at.Sub(b.lastFrameTime)
		if dur < time.Millisecond {
			dur = time.Millisecond
		}
		if err := b.muxer.WriteSample(b.trackID, f.nalu, pts, dur); err != nil {
			b.log.Error("failed to write flushed sample",
				"camera_id", b.cfg.CameraID, "error", err)
			continue
		}
		b.lastFrameTime = f.at
		b.frameCount++
	}
	// The triggering (current) frame follows via the normal write path; its
	// duration is computed against the last flushed frame's timestamp, and
	// the segment-rollover check runs there with a fresh clock reading.
}

// ---------------------------------------------------------------------------
// Segment lifecycle (shared)
// ---------------------------------------------------------------------------

// createNewSegment creates a new MP4 segment via the store, adds the
// codec-specific video track (and audio track if configured), and stores
// the muxer + segment metadata. Returns false on failure.
func (b *baseRecorder) createNewSegment() bool {
	tempPath, finalPath, err := b.store.CreateSegment(b.cfg.CameraID, string(b.driver.segmentFormat()))
	if err != nil {
		b.log.Error("failed to create segment",
			"camera_id", b.cfg.CameraID, "error", err)
		return false
	}
	m := muxer.NewMP4Muxer(tempPath)
	trackID, err := b.driver.addTrack(m, b)
	if err != nil {
		b.log.Error("failed to add video track",
			"camera_id", b.cfg.CameraID, "codec", b.driver.codecLabel(), "error", err)
		os.Remove(tempPath) // clean up empty temp file
		return false
	}
	b.trackID = trackID

	// Add audio track if audio config is available (read the immutable snapshot
	// once — the audio config is set during connectAndRecord and read here from
	// writeFrames; the snapshot makes this race-free, #226).
	var audioTrackID int
	if a := b.audioSnapshot(); a != nil && len(a.muxerConfig) > 0 && a.codec != "" {
		aID, err := m.AddAudioTrack(a.codec, a.muxerConfig)
		if err != nil {
			b.log.Error("failed to add audio track",
				"camera_id", b.cfg.CameraID, "codec", a.codec, "error", err)
		} else {
			audioTrackID = aID
		}
	}

	// Publish muxer + segStart + audioTrackID together under mu so the audio
	// RTP callback (a different goroutine) observes an aligned triplet. Earlier
	// audioTrackID was assigned outside this lock, so a callback could see a
	// non-zero trackID before muxer was published (torn view) — fixed (#226).
	b.mu.Lock()
	b.muxer = m
	b.segStart = time.Now()
	b.audioTrackID = audioTrackID
	b.mu.Unlock()
	b.curTempPath = tempPath
	b.curFinalPath = finalPath
	b.lastFrameTime = b.segStart
	b.frameCount = 0
	return true
}

// closeCurrentSegment finalizes the current segment: closes the muxer,
// atomically renames temp→final, inserts the recording record into the DB,
// publishes a SegmentCompleted event, and updates metrics.
//
// This method is fully codec-agnostic: it uses driver.segmentFormat() for
// the recording metadata and driver.codecLabel() for metrics labels.
func (b *baseRecorder) closeCurrentSegment() {
	if b.muxer == nil {
		return
	}
	if err := b.muxer.Close(); err != nil {
		b.log.Error("failed to close muxer",
			"camera_id", b.cfg.CameraID, "error", err)
		if b.curTempPath != "" {
			os.Remove(b.curTempPath)
		}
		b.mu.Lock()
		b.muxer = nil
		b.audioTrackID = 0
		b.mu.Unlock()
		b.curTempPath = ""
		b.curFinalPath = ""
		b.frameCount = 0
		return
	}

	// Atomic rename: temp → final
	if b.curTempPath != "" && b.curFinalPath != "" {
		if err := b.store.CloseSegment(b.curTempPath, b.curFinalPath); err != nil {
			b.log.Error("failed to close segment",
				"camera_id", b.cfg.CameraID, "error", err)
		}
	}

	// Insert recording entry into database
	var fileSize int64
	var recordingID string
	if b.cfg.DB != nil && b.curFinalPath != "" {
		now := time.Now()
		duration := now.Sub(b.segStart).Seconds()
		rec := &model.Recording{
			ID:         strconv.FormatInt(now.UnixNano(), 10),
			CameraID:   b.cfg.CameraID,
			FilePath:   b.curFinalPath,
			Format:     b.driver.segmentFormat(),
			StartedAt:  b.segStart,
			EndedAt:    now,
			Duration:   duration,
			FrameCount: b.frameCount,
		}
		recordingID = rec.ID
		if info, err := os.Stat(b.curFinalPath); err == nil {
			fileSize = info.Size()
			rec.FileSize = fileSize
		}
		if err := b.cfg.DB.InsertRecordingWithRetry(
			context.Background(), rec, 3, 500*time.Millisecond,
		); err != nil {
			b.log.Error("failed to insert recording",
				"camera_id", b.cfg.CameraID, "error", err)
		}
	}

	// Publish SegmentCompleted event.
	if b.cfg.EventBus != nil && recordingID != "" {
		b.cfg.EventBus.Publish(context.Background(), event.TopicSegmentCompleted, event.SegmentCompleted{
			CameraID:    b.cfg.CameraID,
			FilePath:    b.curFinalPath,
			Format:      string(b.driver.segmentFormat()),
			Encoding:    string(b.driver.segmentFormat()),
			StartedAt:   b.segStart.Format(time.RFC3339Nano),
			EndedAt:     time.Now().Format(time.RFC3339Nano),
			FileSize:    fileSize,
			RecordingID: recordingID,
		})
	} else if recordingID != "" {
		b.log.Warn("SegmentCompleted NOT published — EventBus is nil",
			"camera_id", b.cfg.CameraID, "recording_id", recordingID)
	}

	// Update metrics for completed segment
	if b.frameCount > 0 && b.curFinalPath != "" {
		b.recordSegmentCreated()
		if fileSize > 0 {
			b.recordBytes(fileSize)
		}
	}

	b.mu.Lock()
	b.muxer = nil
	b.audioTrackID = 0
	b.mu.Unlock()
	b.curTempPath = ""
	b.curFinalPath = ""
	b.frameCount = 0
}

// handleStorageFailure closes the current segment cleanly without storage I/O
// when storage health check fails. The stream is kept alive; recording resumes
// when storage recovers.
func (b *baseRecorder) handleStorageFailure() {
	if b.muxer != nil {
		b.muxer.Close()
		os.Remove(b.curTempPath)
		b.mu.Lock()
		b.muxer = nil
		b.curTempPath = ""
		b.curFinalPath = ""
		b.audioTrackID = 0
		b.frameCount = 0
		b.mu.Unlock()
	}
	if logNow, ok := shouldLogHealth(b.lastHealthLogAt); ok {
		b.lastHealthLogAt = logNow
		b.log.Warn("storage health failed, skipping recording (stream kept alive)",
			"camera_id", b.cfg.CameraID)
	}
}

// ---------------------------------------------------------------------------
// Metrics helpers (shared)
// ---------------------------------------------------------------------------

func (b *baseRecorder) incActive() {
	if b.mtrics != nil {
		b.mtrics.ActiveRecordings.Inc()
	}
}

func (b *baseRecorder) decActive() {
	if b.mtrics != nil {
		b.mtrics.ActiveRecordings.Dec()
	}
}

func (b *baseRecorder) recordSegmentCreated() {
	if b.mtrics != nil {
		b.mtrics.SegmentsCreated.WithLabelValues(b.cfg.CameraID, b.driver.codecLabel()).Inc()
	}
}

func (b *baseRecorder) recordBytes(bytes int64) {
	if b.mtrics != nil {
		b.mtrics.RecordingBytesTotal.WithLabelValues(b.cfg.CameraID, b.driver.codecLabel()).Add(float64(bytes))
	}
}

func (b *baseRecorder) recordError(errorType string) {
	if b.mtrics != nil {
		b.mtrics.CameraErrors.WithLabelValues(b.cfg.CameraID, errorType).Inc()
	}
}

// ---------------------------------------------------------------------------
// Panic recovery (shared)
// ---------------------------------------------------------------------------

// recoverPanic is a deferred helper that logs panics with a stack trace
// instead of crashing the goroutine. Used by run() and writeFrames().
func (b *baseRecorder) recoverPanic(where string) {
	if panicErr := recover(); panicErr != nil {
		buf := make([]byte, 4096)
		buf = buf[:runtime.Stack(buf, false)]
		b.log.Error("PANIC recovered in "+where,
			"camera_id", b.cfg.CameraID,
			"panic", panicErr,
			"stack", string(buf))
	}
}
