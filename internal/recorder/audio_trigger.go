// audio_trigger.go — loudness-triggered adaptive recording (issue #478, P0).
//
// Audio is an OR-input to the adaptive gate next to the compressed-domain
// P-frame signal: a static picture with an abnormal sound (glass breaking,
// shouting, alarm) must resume full-rate recording, and the sound itself must
// reach the disk — sparse (timelapse) segments otherwise carry neither the
// frames nor the audio track.
//
// Signal path (G.711 only — µ-law/A-law decode is a table lookup, keeping the
// CGO-free static build; AAC and Opus have no pure-Go decoder, cameras
// streaming those codecs log that the trigger is inactive):
//
//	audio RTP callback → decode → 1s RMS window → dBFS ≥ MinDBFS
//	  → tracker.audioLoud (mutex-guarded cross-thread input)
//	  → next video frame's observe(): timelapse exit + GOP flush (same path
//	    as a spike burst); timelapse entry additionally requires the audio
//	    to have been quiet for CalmThreshold.
//
// Pre-event audio: while armed, decoded G.711 packets are also retained in a
// small ring (PreCapture deep, default 3s). The timelapse-exit flush writes
// the unwritten ring samples into the segment before live audio resumes, so
// the recording starts PreCapture seconds before the trigger — the 1s RMS
// window alone would otherwise eat the onset of the very event that fired.
package recorder

import (
	"log/slog"
	"math"
	"sync"
	"time"
)

// AudioTriggerConfig is the resolved per-camera audio-trigger configuration
// (camera-level `audio_trigger`, effective with recording_mode: adaptive).
type AudioTriggerConfig struct {
	// Enabled arms the loudness input; when false (or nil at the config
	// layer) the recorder behaves exactly as before #478.
	Enabled bool
	// MinDBFS is the loudness threshold over a 1-second window, in dBFS
	// (20·log10(rms/32768)). Default -45.
	MinDBFS float64
	// PreCapture is the audio ring depth — how much pre-trigger audio a
	// timelapse-exit flush back-fills into the segment. Default 3s.
	PreCapture time.Duration
}

// DefaultAudioTriggerConfig returns the validated defaults.
func DefaultAudioTriggerConfig() AudioTriggerConfig {
	return AudioTriggerConfig{
		Enabled:    true,
		MinDBFS:    -45,
		PreCapture: 3 * time.Second,
	}
}

// ResolveAudioTriggerConfig builds the resolved form from raw overrides
// (zero = default), shared by the camera-manager factory and plugin
// recorders so every path resolves identically.
func ResolveAudioTriggerConfig(minDBFS float64, preCaptureS int) AudioTriggerConfig {
	at := DefaultAudioTriggerConfig()
	if minDBFS < 0 {
		at.MinDBFS = minDBFS
	}
	if preCaptureS > 0 {
		at.PreCapture = time.Duration(preCaptureS) * time.Second
	}
	return at
}

// DecodeMuLaw converts one µ-law byte to its 16-bit linear sample (ITU-T
// G.711, Sun ulaw2linear form: transmitted bytes are complemented, the sign
// bit after complement marks negative).
func DecodeMuLaw(u byte) int16 {
	u = ^u
	mag := int16(((int(u&0x0F) << 3) + 0x84) << ((u >> 4) & 0x07))
	if u&0x80 != 0 {
		return 0x84 - mag
	}
	return mag - 0x84
}

// DecodeALaw converts one A-law byte to its 16-bit linear sample (ITU-T G.711,
// Sun alaw2linear form: every other bit is inverted, the sign bit marks
// positive).
func DecodeALaw(a byte) int16 {
	a ^= 0x55
	t := int(a&0x0F) << 4
	switch seg := (a >> 4) & 0x07; seg {
	case 0:
		t += 8
	case 1:
		t += 0x108
	default:
		t = (t + 0x108) << (seg - 1)
	}
	if a&0x80 != 0 {
		return int16(t)
	}
	return -int16(t)
}

// audioLevelMeter turns a G.711 byte stream into 1-second loudness windows.
// Owned by the audio callback goroutine — not safe for concurrent use.
type audioLevelMeter struct {
	rate    int // samples (bytes) per 1s window; 0 → 8000
	minDBFS float64

	sumSq    float64
	n        int
	lastDBFS float64
}

// add accumulates one packet's samples. It reports windowClosed=true when a
// full 1-second window completed, with its dBFS level and whether the level
// was at or above the threshold.
func (m *audioLevelMeter) add(muLaw bool, payload []byte) (windowClosed bool, dbfs float64, loud bool) {
	if m.rate <= 0 {
		m.rate = 8000
	}
	for _, b := range payload {
		var s int16
		if muLaw {
			s = DecodeMuLaw(b)
		} else {
			s = DecodeALaw(b)
		}
		m.sumSq += float64(s) * float64(s)
	}
	m.n += len(payload)
	if m.n < m.rate {
		return false, m.lastDBFS, false
	}
	dbfs = 20 * math.Log10(math.Sqrt(m.sumSq/float64(m.n))/32768)
	m.lastDBFS = dbfs
	m.sumSq, m.n = 0, 0
	return true, dbfs, dbfs >= m.minDBFS
}

// AudioSample is one retained G.711 packet for pre-trigger back-fill.
type AudioSample struct {
	MuLaw bool
	Data  []byte
	Dur   time.Duration
	At    time.Time
	// Written: the packet is already on disk via the live write path — a
	// flush into the existing segment skips it (the audio twin of the
	// gopFrame.written fix, issue #473).
	Written bool
}

// audioRing retains the last max worth of G.711 packets. append/markWritten
// run on the audio callback goroutine, drain on the video goroutine at a
// timelapse exit — hence the mutex.
type audioRing struct {
	max time.Duration
	mu  sync.Mutex
	s   []AudioSample
}

func newAudioRing(depth time.Duration) *audioRing {
	if depth <= 0 {
		depth = 3 * time.Second
	}
	return &audioRing{max: depth}
}

// append retains a deep copy of the packet and trims samples older than max.
func (r *audioRing) append(muLaw bool, data []byte, dur time.Duration, at time.Time) {
	cp := append([]byte(nil), data...)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.s = append(r.s, AudioSample{MuLaw: muLaw, Data: cp, Dur: dur, At: at})
	limit := at.Add(-r.max)
	drop := 0
	for drop < len(r.s) && r.s[drop].At.Before(limit) {
		drop++
	}
	if drop > 0 {
		n := copy(r.s, r.s[drop:])
		r.s = r.s[:n]
	}
}

// markWritten flags the newest retained packet as already on disk.
func (r *audioRing) markWritten() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if n := len(r.s); n > 0 {
		r.s[n-1].Written = true
	}
}

// drain returns all retained packets (oldest first) and clears the ring.
func (r *audioRing) drain() []AudioSample {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.s
	r.s = nil
	return s
}

// AudioTriggerRuntime bundles the per-connection audio-trigger state shared
// by the built-in recorders (via baseRecorder) and plugin recorders (Xiaomi).
// Ingest/MarkWritten run on the audio goroutine; Drain runs on the video
// goroutine at a timelapse-exit flush.
type AudioTriggerRuntime struct {
	cfg   AudioTriggerConfig
	meter audioLevelMeter
	ring  *audioRing
	log   *slog.Logger

	prevLoud bool // edge detection for the loud-window log lines
	windows  int  // completed windows since connect (debug cadence)
}

// NewAudioTriggerRuntime arms the loudness meter and pre-trigger ring.
func NewAudioTriggerRuntime(cfg AudioTriggerConfig, camID string, log *slog.Logger) *AudioTriggerRuntime {
	return &AudioTriggerRuntime{
		cfg:   cfg,
		meter: audioLevelMeter{rate: 8000, minDBFS: cfg.MinDBFS},
		ring:  newAudioRing(cfg.PreCapture),
		log:   log.With("component", "audio-trigger", "camera_id", camID),
	}
}

// Ingest feeds one decoded G.711 packet. A loud completed window invokes
// onLoud(at) — the caller forwards it into the adaptive gate
// (tracker.audioLoud / AdaptiveGate.AudioLoud). Every packet is retained in
// the ring regardless of loudness: the trigger decision lags the signal by
// up to one window, so the onset must be recoverable from history.
func (rt *AudioTriggerRuntime) Ingest(muLaw bool, payload []byte, dur time.Duration, at time.Time, onLoud func(at time.Time)) {
	if len(payload) == 0 {
		return
	}
	closed, dbfs, loud := rt.meter.add(muLaw, payload)
	if closed {
		rt.windows++
		if loud && !rt.prevLoud {
			rt.log.Info("audio trigger: loud window started",
				"dbfs", math.Round(dbfs*10)/10, "min_dbfs", rt.cfg.MinDBFS)
		} else if !loud && rt.prevLoud {
			rt.log.Info("audio trigger: loud window ended",
				"dbfs", math.Round(dbfs*10)/10)
		} else if rt.windows%10 == 0 {
			rt.log.Debug("audio level", "dbfs", math.Round(dbfs*10)/10)
		}
		rt.prevLoud = loud
	}
	if loud && onLoud != nil {
		onLoud(at)
	}
	rt.ring.append(muLaw, payload, dur, at)
}

// MarkWritten flags the newest retained packet as written by the live path.
func (rt *AudioTriggerRuntime) MarkWritten() {
	rt.ring.markWritten()
}

// Drain returns the retained pre-trigger packets (oldest first) and clears
// the ring. The timelapse-exit flush writes the unwritten ones.
func (rt *AudioTriggerRuntime) Drain() []AudioSample {
	return rt.ring.drain()
}
