// adaptive.go — dynamic-timelapse recording (issue #435, Phase 3).
//
// Adaptive recording changes the WRITE DENSITY of a recorder based on a
// live compressed-domain activity signal, never the connection: the recorder
// stays connected and keeps feeding the StreamHub at all times.
//
//	NORMAL ──(activity signal calm for CalmThreshold)──▶ TIMELAPSE
//	TIMELAPSE ──(single P-frame size spike)──▶ NORMAL + GOP flush
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
}

// DefaultAdaptiveConfig mirrors the validated ranges in config.validate.
func DefaultAdaptiveConfig() AdaptiveConfig {
	return AdaptiveConfig{
		CalmThreshold:     60 * time.Second,
		TimelapseInterval: 30 * time.Second,
		SpikeFactor:       3.0,
		MaxGOPBuffer:      16 << 20, // 16MB ≈ 64s @ 2Mbps — far above any sane GOP
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
	lastCalc  time.Time
	calmSince time.Time // last spike (or start); sustained calm enters TIMELAPSE

	// Sparse-mode write cadence.
	lastSparseWrite time.Time

	// GOP pre-buffer: VCL frames since the last IDR (complete reference
	// chain). gopBroken marks an overflowed ring — flush is disabled.
	gop       []gopFrame
	gopBytes  int64
	gopBroken bool

	transitions int // diagnostics: mode switches observed
}

// newAdaptiveTracker builds a tracker starting in NORMAL mode; the first
// TIMELAPSE entry requires a full CalmThreshold of calm after connect.
func newAdaptiveTracker(cfg AdaptiveConfig, camID string, log *slog.Logger) *adaptiveTracker {
	if cfg.SpikeFactor <= 0 {
		cfg.SpikeFactor = 3.0
	}
	if cfg.CalmThreshold <= 0 {
		cfg.CalmThreshold = 60 * time.Second
	}
	if cfg.TimelapseInterval <= 0 {
		cfg.TimelapseInterval = 30 * time.Second
	}
	if cfg.MaxGOPBuffer <= 0 {
		cfg.MaxGOPBuffer = 16 << 20
	}
	return &adaptiveTracker{
		cfg:       cfg,
		log:       log.With("component", "adaptive", "camera_id", camID),
		pSizes:    make([]float64, 0, adaptivePWindow),
		calmSince: time.Now(),
	}
}

const adaptivePWindow = 1200 // rolling baseline samples (~60s @ 20fps)

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
		if spike {
			t.calmSince = now
		}
	}

	switch t.mode {
	case adaptiveNormal:
		if now.Sub(t.calmSince) >= t.cfg.CalmThreshold {
			t.setMode(adaptiveTimelapse, now)
			t.lastSparseWrite = now
		}
	case adaptiveTimelapse:
		if spike {
			flush = t.takeGOP()
			t.setMode(adaptiveNormal, now)
		}
	}
	return spike, flush
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
		// IDR): retaining is pointless — degrade to no-flush transitions.
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

// takeGOP detaches the retained GOP for flushing. Returns nil when the ring
// is broken (overflowed) or does not start with an IDR (not independently
// decodable).
func (t *adaptiveTracker) takeGOP() []gopFrame {
	frames := t.gop
	t.gop = nil
	t.gopBytes = 0
	if t.gopBroken || len(frames) == 0 || !frames[0].isIDR {
		return nil
	}
	return frames
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
		t.lastCalc = now
	}
	if t.median <= 0 {
		return false
	}
	floor := t.mad
	if minFloor := t.median * 0.08; floor < minFloor {
		floor = minFloor
	}
	return float64(size) > t.median+t.cfg.SpikeFactor*floor
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

// setMode switches write density with a log line for observability.
func (t *adaptiveTracker) setMode(m adaptiveMode, now time.Time) {
	if t.mode == m {
		return
	}
	t.mode = m
	t.transitions++
	t.log.Info("adaptive recording mode switch",
		"mode", m.String(), "transitions", t.transitions,
		"calm_threshold", t.cfg.CalmThreshold.String(),
		"timelapse_interval", t.cfg.TimelapseInterval.String())
}
