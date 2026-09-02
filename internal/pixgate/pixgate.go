// pixgate.go — per-camera sampler manager (#636).
//
// For every camera with pixgate enabled the manager resolves the camera's
// sub-stream target (same resolver the live quality=sub path uses), spawns a
// bounded ffmpeg decode (sub-stream → 160×120 gray at ~1fps), runs the pure-Go
// CV engine over each sample, and on confirmed activity fires the recorder's
// pixel trigger (the same exit path as the audio trigger: GOP flush + hold).
// An EventBus event (pixgate.activity) makes the verdict observable for UI
// and automation without coupling them to the trigger.

package pixgate

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/url"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/event"
)

// Target mirrors substream.Target's RTSP-relevant subset (kept local so the
// package has no substream import cycle concerns; the app wiring adapts).
type Target struct {
	URL      string
	Username string
	Password string
	Kind     string // non-empty non-RTSP kinds are unsupported (skipped)
}

// Resolver maps a camera ID to its sub-stream pull target. ok=false → the
// camera has no sub-stream (logged once, gate disabled for it).
type Resolver func(ctx context.Context, cameraID string) (Target, bool, error)

// Trigger resumes full-rate recording on the camera (recorder pixel trigger:
// TL exit + GOP flush + hold). Errors are logged, not fatal.
type Trigger func(cameraID string, hold time.Duration) error

// Event is the pixgate.activity payload.
type Event struct {
	CameraID  string  `json:"camera_id"`
	Active    bool    `json:"active"`
	AreaPct   float64 `json:"area_pct"`
	CX        float64 `json:"cx"`
	CY        float64 `json:"cy"`
	Flood     bool    `json:"flood,omitempty"`
	Ghost     bool    `json:"ghost,omitempty"`
	SampleSec float64 `json:"sample_sec"`
}

// Publisher is the EventBus surface (satisfied by *event.EventBus).
type Publisher interface {
	Publish(ctx context.Context, topic string, data interface{})
}

// CameraConfig is the per-camera pixgate configuration (resolved form).
type CameraConfig struct {
	SampleFPS  float64 // samples per second (default 1)
	MinAreaPct float64
	Persist    int
	Hold       time.Duration // trigger hold per active sample (default 30s)
	// GhostSecs is how long a STATIC blob may keep triggering before it is
	// absorbed into the background (default 300s). The deterministic bound
	// that keeps a switched-on light / parked car from pinning full-rate
	// forever while adaptation alone would also converge, eventually.
	GhostSecs float64
	Masks     []Mask
}

// Config configures the Manager.
type Config struct {
	// FFmpegPath resolves the ffmpeg binary; empty → the manager refuses to
	// start (feature off, one WARN) — ffmpeg stays optional.
	FFmpegPath string
	// FFmpegArgs allows tests to substitute a fake frame source binary.
	FFmpegArgs func(url string, fps float64) []string
	Resolver   Resolver
	Trigger    Trigger
	Bus        Publisher
	Log        *slog.Logger
	// Cameras is the enabled set, refreshed via SetCameras.
	Cameras map[string]CameraConfig
}

// Manager runs one sampler goroutine per enabled camera.
type Manager struct {
	cfg  Config
	log  *slog.Logger
	mu   sync.Mutex
	cams map[string]CameraConfig
	runs map[string]*camRun
	ctx  context.Context
	stop context.CancelFunc
	done chan struct{}

	// fgMu guards the per-camera FG time series (activity-score source) and
	// the PTZ suppression deadlines.
	fgMu     sync.Mutex
	fgRings  map[string]*fgRing
	suppress map[string]time.Time
}

// fgSample is one point of the per-camera foreground time series.
type fgSample struct {
	t       time.Time
	areaPct float32
	valid   bool // false: flood / illumination step / ghost / PTZ suppression
}

// fgRing is a fixed-capacity circular buffer of FG samples (~4.5h at 1fps).
type fgRing struct {
	ring     []fgSample
	head     int
	count    int
	trigArea float64 // camera MinAreaPct — the duty-cycle reference
	fps      float64
}

// FGStats is the pixel-domain activity summary for a wall-time window,
// consumed by the motion analyzer as the preferred activity score source.
type FGStats struct {
	Samples     int     // samples recorded inside the window (valid + invalid)
	Valid       int     // trustworthy samples
	MeanAreaPct float64 // mean largest-blob area over valid samples
	DutyCycle   float64 // fraction of valid samples at/above the trigger area
	TrigAreaPct float64 // the DutyCycle reference (camera MinAreaPct)
	FPS         float64 // configured sampling rate
	SpanSec     float64 // window length in seconds
	Coverage    float64 // min(1, Valid / (SpanSec×FPS)) — how much of the
	//               window the pixel path actually observed
}

const fgRingCap = 16384

// ensureRing creates (or refreshes the tuning of) the camera's FG ring from
// its live config — called once per sampler start.
func (m *Manager) ensureRing(cameraID string, cfg CameraConfig) *fgRing {
	m.fgMu.Lock()
	defer m.fgMu.Unlock()
	if m.fgRings == nil {
		m.fgRings = map[string]*fgRing{}
	}
	r, ok := m.fgRings[cameraID]
	if !ok {
		r = &fgRing{ring: make([]fgSample, fgRingCap)}
		m.fgRings[cameraID] = r
	}
	r.trigArea = cfg.MinAreaPct
	r.fps = cfg.SampleFPS
	if r.trigArea <= 0 {
		r.trigArea = 1.5
	}
	if r.fps <= 0 {
		r.fps = 1
	}
	return r
}

func (m *Manager) recordFG(cameraID string, t time.Time, areaPct float64, valid bool) {
	m.fgMu.Lock()
	defer m.fgMu.Unlock()
	if m.fgRings == nil {
		m.fgRings = map[string]*fgRing{}
	}
	r, ok := m.fgRings[cameraID]
	if !ok {
		// Lazy default tuning; runCamera's ensureRing refreshes it with the
		// camera's real config at sampler start.
		r = &fgRing{ring: make([]fgSample, fgRingCap), trigArea: 1.5, fps: 1}
		m.fgRings[cameraID] = r
	}
	r.ring[r.head] = fgSample{t: t, areaPct: float32(areaPct), valid: valid}
	r.head = (r.head + 1) % len(r.ring)
	if r.count < len(r.ring) {
		r.count++
	}
}

// SegmentStats summarizes the FG time series over [start, end] for the
// activity-score path. ok=false when the camera has no pixgate samples at
// all (disabled camera / not yet started).
func (m *Manager) SegmentStats(cameraID string, start, end time.Time) (FGStats, bool) {
	m.fgMu.Lock()
	defer m.fgMu.Unlock()
	r, ok := m.fgRings[cameraID]
	if !ok || r.count == 0 {
		return FGStats{}, false
	}
	var st FGStats
	st.TrigAreaPct = r.trigArea
	st.FPS = r.fps
	if d := end.Sub(start).Seconds(); d > 0 {
		st.SpanSec = d
	}
	var areaSum float64
	for i := range r.count {
		s := r.ring[(r.head-1-i+len(r.ring)*2)%len(r.ring)]
		if s.t.Before(start) || s.t.After(end) {
			continue
		}
		st.Samples++
		if !s.valid {
			continue
		}
		st.Valid++
		areaSum += float64(s.areaPct)
		if float64(s.areaPct) >= r.trigArea {
			st.DutyCycle++
		}
	}
	if st.Samples == 0 {
		return FGStats{}, false
	}
	if st.Valid > 0 {
		st.MeanAreaPct = areaSum / float64(st.Valid)
		st.DutyCycle /= float64(st.Valid)
	} else {
		st.DutyCycle = 0
	}
	if st.SpanSec > 0 && st.FPS > 0 {
		st.Coverage = float64(st.Valid) / (st.SpanSec * st.FPS)
		if st.Coverage > 1 {
			st.Coverage = 1
		}
	}
	return st, true
}

// Suppress blinds the gate for d (PTZ moves / known scene changes): samples
// land as invalid, triggers never fire, and the background model re-primes
// from the first frame after the window. Repeated calls extend to the max.
func (m *Manager) Suppress(cameraID string, d time.Duration) {
	deadline := time.Now().Add(d)
	m.fgMu.Lock()
	if m.suppress == nil {
		m.suppress = map[string]time.Time{}
	}
	if cur, ok := m.suppress[cameraID]; !ok || deadline.After(cur) {
		m.suppress[cameraID] = deadline
	}
	m.fgMu.Unlock()
}

func (m *Manager) suppressedUntil(cameraID string) time.Time {
	m.fgMu.Lock()
	defer m.fgMu.Unlock()
	return m.suppress[cameraID]
}

// NewManager builds the manager (a pkg/app Service).
func NewManager(cfg Config) *Manager {
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	m := &Manager{
		cfg:  cfg,
		log:  cfg.Log.With("component", "pixgate"),
		cams: cfg.Cameras,
		runs: make(map[string]*camRun),
	}
	if m.cams == nil {
		m.cams = map[string]CameraConfig{}
	}
	return m
}

// Name implements pkg/app.Service.
func (m *Manager) Name() string { return "pixgate" }

// SetCameras swaps the enabled set at runtime; cameras no longer present are
// stopped, new ones started. Safe to call before Start (stages the set).
func (m *Manager) SetCameras(cams map[string]CameraConfig) {
	m.mu.Lock()
	m.cams = cams
	m.mu.Unlock()
	if m.ctx != nil && m.ctx.Err() == nil {
		m.reconcile()
	}
}

// Start launches the per-camera samplers.
func (m *Manager) Start(ctx context.Context) error {
	if m.cfg.FFmpegPath == "" {
		m.log.Warn("pixgate disabled: ffmpeg not available (optional dependency)")
		return nil
	}
	if m.cfg.Resolver == nil || m.cfg.Trigger == nil {
		return fmt.Errorf("pixgate: resolver and trigger must be wired")
	}
	runCtx, cancel := context.WithCancel(ctx)
	m.mu.Lock()
	m.ctx, m.stop, m.done = runCtx, cancel, make(chan struct{})
	cams := m.cams
	m.mu.Unlock()
	for id := range cams {
		m.startCamera(runCtx, id)
	}
	// Keep the service alive until Stop so Service semantics hold even with
	// zero cameras.
	go func() {
		<-runCtx.Done()
	}()
	return nil
}

// Stop terminates all samplers and waits.
func (m *Manager) Stop() error {
	m.mu.Lock()
	stop := m.stop
	done := m.done
	runs := m.runs
	m.runs = map[string]*camRun{}
	m.mu.Unlock()
	if stop != nil {
		stop()
	}
	for _, r := range runs {
		<-r.done
	}
	if done != nil {
		close(done)
	}
	return nil
}

func (m *Manager) reconcile() {
	m.mu.Lock()
	ctx := m.ctx
	for id := range m.cams {
		if _, running := m.runs[id]; !running {
			m.startCamera(ctx, id)
		}
	}
	var toStop []*camRun
	for id, r := range m.runs {
		if _, want := m.cams[id]; !want {
			delete(m.runs, id)
			toStop = append(toStop, r)
		}
	}
	m.mu.Unlock()
	for _, r := range toStop {
		r.cancel()
		<-r.done
	}
}

type camRun struct {
	cancel context.CancelFunc
	done   chan struct{}
}

func (m *Manager) startCamera(ctx context.Context, cameraID string) {
	m.mu.Lock()
	if _, exists := m.runs[cameraID]; exists {
		m.mu.Unlock()
		return
	}
	cfg := m.cams[cameraID]
	runCtx, cancel := context.WithCancel(ctx)
	r := &camRun{cancel: cancel, done: make(chan struct{})}
	m.runs[cameraID] = r
	m.mu.Unlock()
	go m.runCamera(runCtx, cameraID, cfg, r)
}

func (m *Manager) runCamera(ctx context.Context, cameraID string, cfg CameraConfig, r *camRun) {
	defer close(r.done)
	log := m.log.With("camera_id", cameraID)
	if cfg.SampleFPS <= 0 {
		cfg.SampleFPS = 1
	}
	if cfg.Hold <= 0 {
		cfg.Hold = 30 * time.Second
	}
	if cfg.GhostSecs <= 0 {
		cfg.GhostSecs = 300
	}
	ghostSamples := int(math.Ceil(cfg.GhostSecs * cfg.SampleFPS))
	m.ensureRing(cameraID, cfg)

	target, ok, err := m.cfg.Resolver(ctx, cameraID)
	if err != nil || !ok || target.URL == "" || (target.Kind != "" && target.Kind != "rtsp") {
		log.Warn("pixgate: no RTSP sub-stream target; gate disabled",
			"ok", ok, "kind", target.Kind, "error", err)
		return
	}

	engine := NewEngine(EngineConfig{
		MinAreaPct:    cfg.MinAreaPct,
		PersistHits:   cfg.Persist,
		Masks:         cfg.Masks,
		PersistMisses: 3,
		GhostSamples:  ghostSamples,
	})
	frame := make([]byte, GridW*GridH)
	wasActive := false
	wasSuppressed := false
	backoff := time.Second
	interval := time.Duration(float64(time.Second) / cfg.SampleFPS)

	for ctx.Err() == nil {
		n, err := sampleFrames(ctx, m.cfg, target, cfg.SampleFPS, func(gray []byte) bool {
			now := time.Now()
			// PTZ / external suppression window: drain the pipe, record the
			// sample as invalid, keep the model untouched. The first frame
			// after the window re-primes (the scene moved on purpose).
			if until := m.suppressedUntil(cameraID); now.Before(until) {
				wasSuppressed = true
				m.recordFG(cameraID, now, 0, false)
				return ctx.Err() == nil
			}
			if wasSuppressed {
				engine.Reprime()
				wasSuppressed = false
				wasActive = false
			}
			res := engine.Process(gray)
			if res == (EngineResult{}) {
				// Prime frame — carries no activity evidence.
				return ctx.Err() == nil
			}
			m.recordFG(cameraID, now, res.BlobAreaPct, !res.Flood && !res.Ghost)
			if res.Ghost {
				log.Info("pixgate: static foreground absorbed into background (ghost suppression)",
					"area_pct", res.BlobAreaPct, "cx", res.CX, "cy", res.CY)
			}
			if res.Active {
				// Every active sample re-arms the hold — continuous
				// activity keeps full-rate, a single confirmation exits
				// TIMELAPSE for at least Hold.
				if terr := m.cfg.Trigger(cameraID, cfg.Hold); terr != nil {
					log.Debug("pixgate trigger rejected", "error", terr)
				}
			}
			if res.Active != wasActive || res.Active || res.Ghost {
				if m.cfg.Bus != nil {
					m.cfg.Bus.Publish(ctx, event.TopicPixgateActivity, Event{
						CameraID: cameraID, Active: res.Active,
						AreaPct: res.BlobAreaPct, CX: res.CX, CY: res.CY,
						Flood: res.Flood, Ghost: res.Ghost, SampleSec: cfg.SampleFPS,
					})
				}
			}
			wasActive = res.Active
			return ctx.Err() == nil
		}, frame)
		if ctx.Err() != nil {
			return
		}
		log.Warn("pixgate sampler stopped", "frames", n, "error", err, "retry_in", backoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
	_ = interval
}

// sampleFrames runs one ffmpeg process to completion, feeding each decoded
// gray frame to fn (return false stops early). Returns frames consumed.
func sampleFrames(ctx context.Context, cfg Config, target Target, fps float64, fn func([]byte) bool, frame []byte) (int, error) {
	args := ffmpegArgs(target, fps)
	if cfg.FFmpegArgs != nil {
		args = cfg.FFmpegArgs(target.URL, fps)
	}
	cmd := exec.CommandContext(ctx, cfg.FFmpegPath, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return 0, err
	}
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	defer func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()
	n := 0
	for {
		if _, err := io.ReadFull(stdout, frame); err != nil {
			return n, err
		}
		n++
		if !fn(frame) {
			return n, nil
		}
	}
}

// ffmpegArgs builds the decode pipeline: pull RTSP over TCP, resample to the
// fixed gray grid at the sampling rate, stream raw bytes. Credentials ride
// the URL userinfo (injected when the target carries them separately).
func ffmpegArgs(target Target, fps float64) []string {
	u := target.URL
	if target.Username != "" && !strings.Contains(u, "@") {
		if i := strings.Index(u, "://"); i > 0 {
			creds := url.UserPassword(target.Username, target.Password).String()
			u = u[:i+3] + creds + "@" + u[i+3:]
		}
	}
	return []string{
		"-hide_banner", "-loglevel", "error", "-nostdin",
		"-rtsp_transport", "tcp",
		"-i", u,
		"-vf", fmt.Sprintf("fps=%.3f,scale=%d:%d", fps, GridW, GridH),
		"-f", "rawvideo", "-pix_fmt", "gray",
		"pipe:1",
	}
}
