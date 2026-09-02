// adaptive.go — dynamic-timelapse recording (issue #435, Phase 3).
//
// Adaptive recording changes the WRITE DENSITY of a recorder based on a
// live compressed-domain activity signal, never the connection: the recorder
// stays connected and keeps feeding the StreamHub at all times.
//
//	NORMAL ──(activity signal calm for CalmThreshold AND audio quiet)──▶ TIMELAPSE
//	TIMELAPSE ──(spike burst / major spike / loud audio)──▶ NORMAL + GOP flush
//
// While in NORMAL, only a spike BURST (2+ oversized P-frames within 2s) resets
// the calm accumulation — real motion produces clustered spikes, while
// encoders emit isolated oversized P-frames even in fully static scenes
// (issue #466 field data). Exiting TIMELAPSE takes a single spike.
//
// Audio is an OR-input on the same state machine (issue #478): a loud 1-second
// G.711 window (or an external semantic event via the trigger API) defers
// TIMELAPSE entry and — while sparse — exits with the same GOP flush, so an
// abnormal sound on a motionless picture is still recorded, with audio.
//
// In TIMELAPSE mode only one keyframe per TimelapseInterval reaches the muxer
// (audio is dropped too); everything else is skipped on disk but retained in
// an in-memory GOP ring — the frames since the last IDR, which form a
// complete decodable reference chain. On an activity spike the ring is
// flushed to the segment first, so the resume has no missing references and
// the recording is seamless across the transition. Hysteresis is deliberate:
// entering sparse mode needs sustained calm, leaving needs only one spike —
// better to over-record than to miss the start of an event.
//
// The activity signal is the same one the offline analyzer uses (P-frame
// size vs a rolling median+MAD baseline, issue #435): differential codecs
// spend bytes proportional to how much the picture changed, so a size spike
// IS a motion burst. Cost per frame is one length read plus a ring append.

package recorder

import (
	"log/slog"
	"sort"
	"sync"
	"time"
)

// AdaptiveConfig is the parsed per-camera adaptive-recording configuration
// (recording_mode: adaptive). The config-file layer carries string durations;
// this is the resolved form used inside the recorder.
type AdaptiveConfig struct {
	CalmThreshold     time.Duration // sustained calm before dropping to sparse mode
	TimelapseInterval time.Duration // keyframe cadence while sparse
	SpikeFactor       float64       // MAD-floored deviations above baseline = spike
	MaxGOPBuffer      int64         // byte cap of the retained GOP ring
	// NoiseFloorBytes is an ABSOLUTE per-frame byte floor (#635): a P-frame
	// smaller than this can never be an exit-worthy spike, no matter how big
	// it looks relative to an equally starved baseline. Guards cameras whose
	// encoder crushes its bitrate (night mode, rate-control collapse) where
	// the purely relative metric fires on jitter. 0 = disabled.
	NoiseFloorBytes float64
	// AutoNoiseFloor self-calibrates NoiseFloorBytes from TIMELAPSE dwell
	// (#635): sparse-mode frames are known-static by construction, so their
	// statistics reveal the camera's own noise level. The learned floor
	// engages ONLY on bitrate-starved streams (TL-ring p50 < 2KB — the regime
	// where rate-control jitter dominates), as min(p99×1.25, p50×8); healthy
	// streams keep the legacy relative-only semantics unchanged. Default
	// true; the zero value in hand-built configs means off (tests).
	AutoNoiseFloor bool
	// NoVideoExit disables the video-spike exit path (#638) — "resident
	// timelapse": the camera stays sparse through video noise (rain, water
	// glare, swaying foliage) and only audio events or external semantic
	// triggers (trigger API / pixgate) can resume full-rate. Zero value =
	// video exits enabled (legacy behavior); the resolver maps
	// adaptive.video_exit: false onto it.
	NoVideoExit bool
	// AmbientArchive additionally keeps the raw G.711 stream as a sidecar file
	// beside each segment for post-production (only meaningful with
	// AmbientAudio; default off).
	AmbientArchive bool
	// AmbientAudio keeps the disk audio track recording CONTINUOUSLY while in
	// sparse mode (#496 audio phase): the video timeline is compressed at
	// merge time and the merge renders the ambient span into a quiet
	// continuous atmosphere bed (envelope mixdown) instead of dropping it.
	// Costs ~28.8MB/h (G.711); enable per camera. Event (full-rate) spans
	// always keep their real audio either way.
	AmbientAudio bool
}

// DefaultAdaptiveConfig mirrors the validated ranges in config.validate.
//
// SpikeFactor 5.0 is calibrated on real-camera capture (issue #466): H.264/H.265
// encoders emit isolated oversized P-frames in fully static scenes (noise /
// intra-refresh / AEC adjustments), so a 3.0 threshold flags ~1.3% of P-frames
// as spikes on a motionless indoor camera — combined with the single-spike
// calm reset that made TIMELAPSE unreachable (max measured spike-free stretch
// 50s < 60s CalmThreshold). 5.0 keeps a genuinely busy camera in NORMAL while
// letting a static one go sparse.
func DefaultAdaptiveConfig() AdaptiveConfig {
	return AdaptiveConfig{
		CalmThreshold:     60 * time.Second,
		TimelapseInterval: 30 * time.Second,
		SpikeFactor:       5.0,
		// 32MB: the ring must hold one full camera GOP. 16MB was sized for
		// ~4s GOPs; a 2K H.265 camera whose IDR interval approaches the 30s
		// timelapse cadence (~12-15MB average, more with motion spikes)
		// overflows it, breaking the ring — the next timelapse exit then
		// flushes nothing and its P-frames land with dangling references
		// ("Could not find ref with POC" at decode, issue #485 field data).
		MaxGOPBuffer:   32 << 20, // ≈ 75s @ 3.5Mbps — above a 30s GOP at 2K
		AutoNoiseFloor: true,
		NoVideoExit:    false,
	}
}

// adaptiveMode is the write-density state.
type adaptiveMode int

const (
	adaptiveNormal adaptiveMode = iota
	adaptiveTimelapse
)

func (m adaptiveMode) String() string {
	if m == adaptiveTimelapse {
		return "timelapse"
	}
	return "normal"
}

// gopFrame is one retained VCL NALU (deep copy — the frameCh buffer is
// reused by the RTP callback) with its capture time for pts reconstruction.
type gopFrame struct {
	nalu  []byte
	isIDR bool
	at    time.Time
	// written marks frames already on disk in the CURRENT segment (normal-path
	// writes and sparse keyframes). The timelapse-exit flush must skip them
	// when writing into the existing segment — re-writing the sparse IDR (the
	// ring's anchor) produced duplicate POC / dts collisions (issue #473).
	// A flush that CREATES a fresh segment still writes the whole ring: the
	// IDR there lands in a new file, which is not a duplicate.
	written bool
}

// adaptiveTracker holds the live activity signal and mode state. It is owned
// by the writeFrames goroutine (Tier 3 — no locking; the recorder only
// creates it when cfg.Adaptive != nil).
type adaptiveTracker struct {
	cfg  AdaptiveConfig
	log  *slog.Logger
	mode adaptiveMode

	// Rolling P-frame size baseline. Fixed-cap ring: once full, the older
	// half is dropped (~60s @ 20fps, 1200 samples).
	pSizes    []float64
	median    float64
	mad       float64
	floor     float64 // MAD-floored deviation floor from the last recompute
	lastCalc  time.Time
	calmSince time.Time // last motion burst (or start); sustained calm enters TIMELAPSE

	// Spike-burst detection (issue #466): only CLUSTERED spikes (>=2 within
	// adaptiveBurstWindow) reset the calm accumulation while in NORMAL — real
	// motion produces runs of consecutive oversized P-frames, encoder noise
	// produces isolated single spikes. Exiting TIMELAPSE still happens on any
	// single spike (better to over-record than to miss an event start).
	recentSpikes []time.Time

	// Absolute noise floor (#635). tlSizes is a ring of P-frame byte sizes
	// observed while in TIMELAPSE (known-static by construction); its p99
	// ×1.25 (capped at 4× median) yields tlFloor, the self-calibrated ceiling
	// below which a "spike" is the camera's own noise, not activity.
	tlSizes []float64
	tlFloor float64

	// Sparse-mode write cadence.
	lastSparseWrite time.Time

	// GOP pre-buffer: VCL frames since the last IDR (complete reference
	// chain). gopBroken marks an overflowed ring — flush is disabled.
	gop       []gopFrame
	gopBytes  int64
	gopBroken bool

	// Audio-trigger input (issue #478). Written by the audio callback (or
	// the external trigger API handler) via audioLoud, read by observe() on
	// the video goroutine. lastLoud defers TIMELAPSE entry by CalmThreshold;
	// a seq bump observed while in TIMELAPSE exits with a GOP flush.
	// audioSeqSeen consumes events seen in NORMAL so they cannot fire a
	// stale exit after a later entry.
	audioMu       sync.Mutex
	audioLastLoud time.Time
	audioSeq      uint64
	audioSeqSeen  uint64
	audioSource   string // "audio" (default) | "pixel" (#636) | "external"

	transitions int // diagnostics: mode switches observed
}

// newAdaptiveTracker builds a tracker starting in NORMAL mode; the first
// TIMELAPSE entry requires a full CalmThreshold of calm after connect.
func newAdaptiveTracker(cfg AdaptiveConfig, camID string, log *slog.Logger) *adaptiveTracker {
	if cfg.SpikeFactor <= 0 {
		cfg.SpikeFactor = 5.0
	}
	if cfg.CalmThreshold <= 0 {
		cfg.CalmThreshold = 60 * time.Second
	}
	if cfg.TimelapseInterval <= 0 {
		cfg.TimelapseInterval = 30 * time.Second
	}
	if cfg.MaxGOPBuffer <= 0 {
		cfg.MaxGOPBuffer = 32 << 20
	}
	return &adaptiveTracker{
		cfg:       cfg,
		log:       log.With("component", "adaptive", "camera_id", camID),
		pSizes:    make([]float64, 0, adaptivePWindow),
		tlSizes:   make([]float64, 0, adaptivePWindow),
		calmSince: time.Now(),
	}
}

// noiseFloor is the effective absolute byte floor applied to spike
// classification: max(explicit config floor, self-calibrated TL floor).
func (t *adaptiveTracker) noiseFloor() float64 {
	f := t.cfg.NoiseFloorBytes
	if t.tlFloor > f {
		f = t.tlFloor
	}
	return f
}

// adaptiveStarvedMedianBytes is the TIMELAPSE-dwell median below which a
// stream is considered bitrate-starved (#635): below ~2KB per P-frame the
// encoder's rate-control jitter dominates any separable activity signal, so
// the self-calibrated floor engages. Above it, healthy streams keep the
// legacy purely-relative semantics — zero behavior change by default, and
// rain/glare separation on healthy streams is pixgate's job (#636), not the
// compressed-domain gate's.
const adaptiveStarvedMedianBytes = 2048

// learnTlFloor recomputes the self-calibrated noise floor from the TIMELAPSE
// dwell ring (#635). Called from the baseline recompute path (every ~2s) on
// the video goroutine. Needs adaptiveAutoFloorMinSamples samples (~12s @
// 20fps) before it engages, and only engages on bitrate-starved streams
// (TL-ring p50 < adaptiveStarvedMedianBytes): there the floor is
// min(p99×1.25, p50×8) — the ring's own noise ceiling, runaway-capped at 8×
// its static level so the floor can never swallow what little real signal a
// starved stream still carries.
func (t *adaptiveTracker) learnTlFloor() {
	if !t.cfg.AutoNoiseFloor || len(t.tlSizes) < adaptiveAutoFloorMinSamples {
		return
	}
	s := append([]float64(nil), t.tlSizes...)
	sort.Float64s(s)
	p50 := s[len(s)/2]
	if p50 >= adaptiveStarvedMedianBytes {
		t.tlFloor = 0
		return
	}
	p99 := s[int(float64(len(s)-1)*0.99)]
	floor := p99 * 1.25
	if capAt := p50 * 8; floor > capAt {
		floor = capAt
	}
	t.tlFloor = floor
}

const adaptivePWindow = 1200 // rolling baseline samples (~60s @ 20fps)

// adaptiveAutoFloorMinSamples is the TIMELAPSE-dwell sample count required
// before the self-calibrated noise floor engages (#635): ~12s @ 20fps.
const adaptiveAutoFloorMinSamples = 240

const (
	// adaptiveBurstWindow is how close two P-frame spikes must be to count as
	// a motion burst (vs isolated encoder noise). Real motion produces
	// consecutive/clustered spikes; static-scene encoder noise produces single
	// spikes separated by seconds (issue #466 field data: isolated spikes up
	// to 1.3% of P-frames on a motionless camera, max spike-free stretch 50s).
	adaptiveBurstWindow = 2 * time.Second
	// adaptiveBurstCount is the spike count within the window that resets the
	// calm accumulation.
	adaptiveBurstCount = 2
	// adaptiveMajorFactor scales the spike threshold for a MAJOR spike — a
	// single frame large enough to exit timelapse on its own (scene cut,
	// light-on, person appearing): size > median + majorFactor*threshold.
	// Guards the burst-gated exit against missing one-frame scene changes
	// (issue #475 field data: an empty room's noise spikes stay under 2x a
	// factor-10 threshold while real scene cuts clear it by an order of
	// magnitude).
	adaptiveMajorFactor = 2.0
)

// observe ingests one VCL NALU (without start code), updates the activity
// baseline and GOP ring, and executes mode transitions. It returns whether
// this frame is an activity spike, and — on the TIMELAPSE→NORMAL transition —
// the retained GOP to flush before the current frame is written.
func (t *adaptiveTracker) observe(nalu []byte, isIDR bool, now time.Time) (spike bool, flush []gopFrame) {
	size := len(nalu)
	if isIDR {
		// A new reference chain starts here: retain the IDR and drop the
		// previous partial GOP (superseded, never flushable).
		t.gop = t.gop[:0]
		t.gopBytes = 0
		t.gopBroken = false
	}
	t.appendGOP(nalu, isIDR, now)
	if !isIDR {
		spike = t.classify(size, now)
		// Absolute noise floor (#635): a spike below the floor is the
		// camera's own noise (starved bitrate / rate-control jitter), never
		// an exit signal. The baseline above already ingested the sample, so
		// the relative statistics stay honest.
		if spike && t.noiseFloor() > 0 && float64(size) < t.noiseFloor() {
			spike = false
		}
		// TIMELAPSE dwell = known-static window: feed the self-calibration
		// ring (#635) regardless of the spike verdict.
		if t.mode == adaptiveTimelapse {
			t.tlSizes = append(t.tlSizes, float64(size))
			if len(t.tlSizes) > adaptivePWindow {
				t.tlSizes = t.tlSizes[len(t.tlSizes)-adaptivePWindow:]
			}
		}
		if spike {
			t.recordSpike(now)
		}
	}

	// Consume the audio-trigger input (issue #478): an event seen while in
	// NORMAL must not fire a stale exit after a later TIMELAPSE entry, so the
	// sequence number is consumed on EVERY frame regardless of mode.
	audioLastLoud, audioSeq := t.audioSnapshot()
	audioEvent := audioSeq != t.audioSeqSeen
	t.audioSeqSeen = audioSeq

	switch t.mode {
	case adaptiveNormal:
		// Only a spike BURST (clustered oversized P-frames) counts as motion
		// and resets the calm accumulation; an isolated spike is encoder
		// noise and must not keep a static camera recording at full rate
		// forever (issue #466). With VideoExit disabled (#638) the video
		// signal never defers entry — the camera is resident-sparse and only
		// audio/external triggers matter.
		if spike && !t.cfg.NoVideoExit && t.spikeBurst(now) {
			t.calmSince = now
		}
		// Entering TIMELAPSE requires BOTH the video signal and the audio to
		// have been calm for CalmThreshold (audioLastLoud's zero value lets a
		// never-loud stream enter immediately once the video is calm).
		if now.Sub(t.calmSince) >= t.cfg.CalmThreshold &&
			now.Sub(audioLastLoud) >= t.cfg.CalmThreshold {
			t.setMode(adaptiveTimelapse, now, "calm")
			t.lastSparseWrite = now
		}
	case adaptiveTimelapse:
		// Exit is burst-gated like entry (issue #475 field data: on a camera
		// confirmed recording an empty scene all day, every isolated noise
		// spike exited sparse mode and cost a fresh CalmThreshold of full-rate
		// writing — timelapse share 2% where the scene was 100% static). A
		// single MAJOR spike (scene-cut scale) still exits alone: one-frame
		// scene changes (light on, person appearing) must resume recording
		// immediately. Real motion always clusters, so the burst path covers
		// it. A loud-audio event (issue #478) exits ungated — sound IS the
		// signal, and over-recording beats missing the event.
		// With VideoExit disabled (#638) this whole path is inert: resident
		// timelapse through rain/glare/foliage; audio + external triggers
		// remain the only exits.
		if audioEvent {
			t.calmSince = now
			flush = t.takeGOP()
			t.setMode(adaptiveNormal, now, t.triggerSource())
		} else if spike && !t.cfg.NoVideoExit && (t.spikeBurst(now) || t.majorSpike(size)) {
			// The exit itself is evidence of activity, so a fresh CalmThreshold
			// window is required before re-entering TIMELAPSE (prevents spike →
			// exit → instant re-entry oscillation, since the entry reset does
			// not fire on isolated spikes).
			t.calmSince = now
			flush = t.takeGOP()
			t.setMode(adaptiveNormal, now, "activity")
		}
	}
	return spike, flush
}

// audioLoud records a loud-audio event (audio-trigger window or external
// trigger API) at at; hold extends how long TIMELAPSE entry stays deferred
// (semantic events carry their own confidence window). Thread-safe: called
// from the audio callback or an API handler goroutine.
func (t *adaptiveTracker) audioLoud(at time.Time, hold time.Duration) {
	t.audioLoudSrc(at, hold, "audio")
}

// audioLoudSrc is audioLoud with an explicit trigger source — "pixel" for
// the pixgate CV gate (#636), "external" for the semantic trigger API — so
// the mode-switch log attributes the exit correctly.
func (t *adaptiveTracker) audioLoudSrc(at time.Time, hold time.Duration, source string) {
	t.audioMu.Lock()
	t.audioLastLoud = at.Add(hold)
	t.audioSeq++
	t.audioSource = source
	t.audioMu.Unlock()
}

// audioSnapshot reads the cross-thread audio-trigger state.
func (t *adaptiveTracker) audioSnapshot() (lastLoud time.Time, seq uint64) {
	t.audioMu.Lock()
	defer t.audioMu.Unlock()
	return t.audioLastLoud, t.audioSeq
}

// triggerSource reads the source recorded with the latest audioLoudSrc event
// (default "audio") for the mode-switch log attribution.
func (t *adaptiveTracker) triggerSource() string {
	t.audioMu.Lock()
	defer t.audioMu.Unlock()
	if t.audioSource == "" {
		return "audio"
	}
	return t.audioSource
}

// recordSpike appends a spike timestamp, trimming entries outside the burst
// window.
func (t *adaptiveTracker) recordSpike(now time.Time) {
	cutoff := now.Add(-adaptiveBurstWindow)
	drop := 0
	for _, at := range t.recentSpikes {
		if at.Before(cutoff) {
			drop++
			continue
		}
		break
	}
	// Shift retained spikes left in place, then append. The previous
	// `append(t.recentSpikes[drop:drop:drop], now)` silently DISCARDED the
	// retained tail when drop == 0 (full-slice expr caps capacity at 0), so
	// only the newest spike ever survived and spikeBurst never saw a pair —
	// the burst rules (entry calm-reset and, from #475, the timelapse exit)
	// were dead in production from #468 until the burst-gated-exit unit test
	// caught it.
	if drop > 0 {
		n := copy(t.recentSpikes, t.recentSpikes[drop:])
		t.recentSpikes = t.recentSpikes[:n]
	}
	t.recentSpikes = append(t.recentSpikes, now)
}

// spikeBurst reports whether the recent spikes within the burst window reach
// adaptiveBurstCount (a motion burst, not isolated noise).
func (t *adaptiveTracker) spikeBurst(now time.Time) bool {
	cutoff := now.Add(-adaptiveBurstWindow)
	n := 0
	for i := len(t.recentSpikes) - 1; i >= 0; i-- {
		if t.recentSpikes[i].Before(cutoff) {
			break
		}
		n++
		if n >= adaptiveBurstCount {
			return true
		}
	}
	return false
}

// markTailWritten flags the newest retained frame as already on disk.
func (t *adaptiveTracker) markTailWritten() {
	if n := len(t.gop); n > 0 {
		t.gop[n-1].written = true
	}
}

// shouldWriteSparse reports whether a frame observed in TIMELAPSE mode may
// reach the muxer: keyframes only, at most one per TimelapseInterval.
func (t *adaptiveTracker) shouldWriteSparse(isIDR bool, now time.Time) bool {
	if !isIDR {
		return false
	}
	return now.Sub(t.lastSparseWrite) >= t.cfg.TimelapseInterval
}

// appendGOP retains a deep copy of the frame.
func (t *adaptiveTracker) appendGOP(nalu []byte, isIDR bool, now time.Time) {
	if t.gopBroken {
		return
	}
	size := int64(len(nalu))
	if t.gopBytes+size > t.cfg.MaxGOPBuffer {
		// Pathological GOP (e.g. intra-refresh encoders with no discrete
		// IDR, or an IDR interval long enough to overflow the cap): retaining
		// is pointless — degrade to no-flush transitions. The next exit
		// writes its P-frames with dangling references (decode artifacts
		// until the following IDR), so make the condition observable.
		if !t.gopBroken {
			t.log.Warn("adaptive GOP pre-buffer overflowed — timelapse exits will flush nothing until the next IDR",
				"gop_bytes", t.gopBytes, "max_gop_buffer", t.cfg.MaxGOPBuffer,
				"hint", "raise adaptive.gop_buffer_bytes for this camera")
		}
		t.gopBroken = true
		t.gop = t.gop[:0]
		t.gopBytes = 0
		return
	}
	payload := make([]byte, len(nalu))
	copy(payload, nalu)
	t.gop = append(t.gop, gopFrame{nalu: payload, isIDR: isIDR, at: now})
	t.gopBytes += size
}

// takeGOP detaches the retained GOP for flushing, EXCLUDING the current frame
// — observe() just appended it (the trigger), and every caller writes that
// frame itself via the normal write path right after the flush, so including
// it here wrote it twice (one duplicate POC per timelapse exit, issue #498).
// When the trigger is itself the IDR (ring == [IDR]) nothing remains that
// needs flushing. Returns nil when the remainder is broken (overflowed ring)
// or not independently decodable (does not start with an IDR). Callers skip
// already-written frames when flushing into an existing segment (issue #473).
func (t *adaptiveTracker) takeGOP() []gopFrame {
	frames := t.gop
	t.gop = nil
	t.gopBytes = 0
	if t.gopBroken || len(frames) <= 1 || !frames[0].isIDR {
		return nil
	}
	return frames[:len(frames)-1]
}

// clearWritten forgets every ring frame's on-disk marking. `written` means
// "on disk in the CURRENT segment"; when that segment closes (rotation,
// storage failure), the frames live in a closed file and a later
// timelapse-exit flush that lands in a FRESH segment must write the whole
// ring. Treating pre-rotation flags as current skipped mid-GOP frames that
// were never in the new file ("Could not find ref with POC", issue #498).
func (t *adaptiveTracker) clearWritten() {
	for i := range t.gop {
		t.gop[i].written = false
	}
}

// classify updates the rolling baseline and reports whether the observed
// P-frame size is an activity spike.
func (t *adaptiveTracker) classify(size int, now time.Time) bool {
	if len(t.pSizes) >= adaptivePWindow {
		copy(t.pSizes, t.pSizes[adaptivePWindow/2:])
		t.pSizes = t.pSizes[:adaptivePWindow/2]
	}
	t.pSizes = append(t.pSizes, float64(size))

	// Recompute the baseline at most every 2s, and only once enough samples
	// exist for a robust median; between recomputes the previous baseline
	// classifies new arrivals (a median of hundreds of samples is stable
	// across a 2s window).
	if now.Sub(t.lastCalc) >= 2*time.Second && len(t.pSizes) >= 24 {
		t.recomputeBaseline()
		t.learnTlFloor() // #635: self-calibrate the absolute noise floor
		t.lastCalc = now
	}
	if t.median <= 0 {
		return false
	}
	t.floor = t.mad
	if minFloor := t.median * 0.08; t.floor < minFloor {
		t.floor = minFloor
	}
	return float64(size) > t.median+t.cfg.SpikeFactor*t.floor
}

// majorSpike reports whether the frame is a scene-cut-scale outlier: big
// enough to leave timelapse on its own despite the burst-gated exit.
func (t *adaptiveTracker) majorSpike(size int) bool {
	return t.median > 0 && float64(size) > t.median+adaptiveMajorFactor*t.cfg.SpikeFactor*t.floor
}

// recomputeBaseline derives median and MAD over the retained size window.
func (t *adaptiveTracker) recomputeBaseline() {
	s := append([]float64(nil), t.pSizes...)
	sort.Float64s(s)
	n := len(s)
	med := s[n/2]
	if n%2 == 0 {
		med = (s[n/2-1] + s[n/2]) / 2
	}
	dev := make([]float64, n)
	for i, v := range s {
		d := v - med
		if d < 0 {
			d = -d
		}
		dev[i] = d
	}
	sort.Float64s(dev)
	mad := dev[n/2]
	if n%2 == 0 {
		mad = (dev[n/2-1] + dev[n/2]) / 2
	}
	t.median, t.mad = med, mad
}

// setMode switches write density with a log line for observability. reason
// names the trigger ("calm", "activity", "audio").
func (t *adaptiveTracker) setMode(m adaptiveMode, now time.Time, reason string) {
	if t.mode == m {
		return
	}
	t.mode = m
	t.transitions++
	t.log.Info("adaptive recording mode switch",
		"mode", m.String(), "reason", reason,
		"transitions", t.transitions,
		"calm_threshold", t.cfg.CalmThreshold.String(),
		"timelapse_interval", t.cfg.TimelapseInterval.String(),
		"noise_floor_bytes", int64(t.noiseFloor()),
		"video_exit", !t.cfg.NoVideoExit)
}

// AdaptiveFrame is one retained GOP frame exposed to plugin-style recorders
// outside this package (see AdaptiveGate).
type AdaptiveFrame struct {
	Nalu  []byte
	IsIDR bool
	At    time.Time
	// Written: the frame is already on disk in the current segment — skip it
	// when flushing into the existing segment (issue #473); include it when
	// the flush creates a fresh segment.
	Written bool
}

// AdaptiveGate is the exported per-connection adaptive-recording gate for
// recorders that do not inherit baseRecorder (e.g. the Xiaomi MISS recorder,
// issue #468). It wraps adaptiveTracker with the same semantics as the
// writeFrames gate: sustained calm drops write density to one keyframe per
// TimelapseInterval; an activity spike flushes the retained GOP and resumes
// full-rate writing. Rebuild per connection so a reconnect always starts in
// NORMAL mode with a fresh baseline.
type AdaptiveGate struct {
	t *adaptiveTracker
}

// NewAdaptiveGate arms an adaptive gate; cfg is resolved camera config
// (recorder.DefaultAdaptiveConfig for unset fields).
func NewAdaptiveGate(cfg AdaptiveConfig, camID string, log *slog.Logger) *AdaptiveGate {
	return &AdaptiveGate{t: newAdaptiveTracker(cfg, camID, log)}
}

// Observe classifies one VCL NALU (without start code) and returns the write
// decision:
//
//	spike — the frame is an activity spike (diagnostics)
//	skip  — drop the frame's DISK write (sparse mode); live fan-out is the
//	        caller's business and must keep flowing
//	flush — retained GOP frames to write BEFORE the current frame (non-empty
//	        only on the timelapse→normal exit; starts with an IDR)
//
// After a non-skip sparse keyframe write, the caller need not stamp anything —
// the cadence is stamped here, mirroring the base recorder.
func (g *AdaptiveGate) Observe(nalu []byte, isIDR bool, now time.Time) (spike, skip bool, flush []AdaptiveFrame) {
	spike, fl := g.t.observe(nalu, isIDR, now)
	if len(fl) > 0 {
		// Timelapse exit: hand the retained GOP to the caller to write BEFORE
		// the current frame. Keyed on the flush return, not on mode — observe()
		// has already switched to NORMAL (same fix as writeFrames).
		for _, f := range fl {
			flush = append(flush, AdaptiveFrame{Nalu: f.nalu, IsIDR: f.isIDR, At: f.at, Written: f.written})
		}
	}
	if g.t.mode == adaptiveTimelapse && !spike {
		if !g.t.shouldWriteSparse(isIDR, now) {
			return spike, true, flush
		}
		if isIDR {
			g.t.lastSparseWrite = now
		}
	}
	return spike, false, flush
}

// Timelapse reports whether the gate is in sparse mode (callers use it to
// gate DISK audio writes, which sparse mode drops; live audio continues).
func (g *AdaptiveGate) Timelapse() bool {
	return g.t.mode == adaptiveTimelapse
}

// AudioLoud records a loud-audio event (or an external semantic trigger, see
// the /api/cameras/{id}/adaptive/trigger endpoint) at at; hold extends how
// long TIMELAPSE entry stays deferred. Thread-safe — the mode transition
// itself is executed by the next Observe on the video path, together with
// the GOP flush (issue #478).
func (g *AdaptiveGate) AudioLoud(at time.Time, hold time.Duration) {
	g.t.audioLoud(at, hold)
}

// PixelLoud injects a pixgate CV activity confirmation (#636) — same state
// machine path as AudioLoud, attributed as reason=pixel.
func (g *AdaptiveGate) PixelLoud(at time.Time, hold time.Duration) {
	g.t.audioLoudSrc(at, hold, "pixel")
}

// MarkLastWritten records that the most recently observed frame was
// successfully written to the current segment, so a later timelapse-exit
// flush skips it (issue #473). Callers invoke it after a successful muxer
// write of the frame Observe just classified. No-op when the ring is empty
// or was detached by a flush.
func (g *AdaptiveGate) MarkLastWritten() {
	g.t.markTailWritten()
}

// ClearWritten forgets the per-segment written markings after the caller's
// current segment closes (rotation, storage failure) — a later flush into a
// FRESH segment must write the whole retained ring instead of skipping
// frames that only exist in the closed file (issue #498).
func (g *AdaptiveGate) ClearWritten() {
	g.t.clearWritten()
}

// AdaptiveOverrides carries the optional per-camera adaptive tuning exactly
// as parsed from config (zero values = "use the default"). Struct form keeps
// the resolver signature stable as knobs are added (#635/#638).
type AdaptiveOverrides struct {
	CalmThreshold     string
	TimelapseInterval string
	SpikeFactor       float64
	GOPBufferBytes    int64
	AmbientAudio      bool
	AmbientArchive    bool
	NoiseFloorBytes   float64 // 0 = no explicit floor
	AutoNoiseFloor    *bool   // nil = default (true)
	VideoExit         *bool   // nil = default (true)
}

// ResolveAdaptiveConfig builds a resolved AdaptiveConfig from optional
// overrides, defaulting unset fields. Shared by the camera-manager factory
// (RTSP/ONVIF paths) and plugin-style recorders (Xiaomi) so both resolve
// identically. String durations follow the frame_watchdog_timeout convention.
func ResolveAdaptiveConfig(ov AdaptiveOverrides) AdaptiveConfig {
	ac := DefaultAdaptiveConfig()
	if d, err := time.ParseDuration(ov.CalmThreshold); err == nil && d > 0 {
		ac.CalmThreshold = d
	}
	if d, err := time.ParseDuration(ov.TimelapseInterval); err == nil && d > 0 {
		ac.TimelapseInterval = d
	}
	if ov.SpikeFactor > 0 {
		ac.SpikeFactor = ov.SpikeFactor
	}
	if ov.GOPBufferBytes > 0 {
		ac.MaxGOPBuffer = ov.GOPBufferBytes
	}
	ac.AmbientAudio = ov.AmbientAudio
	ac.AmbientArchive = ov.AmbientArchive
	if ov.NoiseFloorBytes > 0 {
		ac.NoiseFloorBytes = ov.NoiseFloorBytes
	}
	if ov.AutoNoiseFloor != nil {
		ac.AutoNoiseFloor = *ov.AutoNoiseFloor
	}
	if ov.VideoExit != nil {
		ac.NoVideoExit = !*ov.VideoExit
	}
	return ac
}
