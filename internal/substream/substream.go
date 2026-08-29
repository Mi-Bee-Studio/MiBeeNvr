// Package substream implements on-demand secondary-stream (子码流) ingest.
//
// A camera's sub stream (lower-resolution H.264/H.265, typically 480p-720p)
// is pulled over RTSP ONLY while consumers hold references — the pull stops
// after an idle timeout with zero references, so a camera whose sub stream
// is never watched costs nothing (#513). Decoded access units fan out through
// a dedicated streamhub.StreamHub, which egress endpoints (WS/FLV/HLS) consume
// under the camera's "/sub" stream key.
package substream

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/streamhub"
)

var (
	// ErrNoSubStream means the camera has no usable sub-stream configuration
	// (no sub_stream_url and no discoverable secondary profile). Callers treat
	// this as "quality negotiation falls back to main".
	ErrNoSubStream = errors.New("sub-stream not configured for camera")

	// ErrNotReady means a pull was started but no keyframe/parameter sets
	// arrived within the ready timeout (camera offline or sub stream broken).
	ErrNotReady = errors.New("sub-stream not ready in time")

	// ErrStopped means the manager is shut down.
	ErrStopped = errors.New("sub-stream manager stopped")
)

var subLogger = slog.Default().With("component", "substream")

// Pull states reported by Source.State / Manager.Snapshot.
const (
	StateStarting     = "starting"
	StateLive         = "live"
	StateReconnecting = "reconnecting"
	StateFailed       = "failed"
)

// Pull target kinds: KindRTSP (default — Target.URL is dialed over RTSP) and
// KindGB28181 (a probed GB28181 sub-channel INVITE'd via the GBPuller).
const (
	KindRTSP    = "rtsp"
	KindGB28181 = "gb28181"
)

// Target describes where to pull a camera's sub-stream from. Credentials ride
// separately so the puller can inject them into RTSP URLs that lack userinfo.
// Kind KindGB28181 ignores URL/credentials and uses the GB device/channel pair
// instead (#560).
type Target struct {
	URL      string
	Username string
	Password string
	Kind     string // "" / KindRTSP / KindGB28181
	// GBDeviceID/GBChannelID carry the probed sub-channel binding for
	// KindGB28181 targets.
	GBDeviceID  string
	GBChannelID string
}

// GBPuller starts GB28181 sub-channel media sessions. Implemented by the SIP
// server (internal/gb28181/sip) and injected at app wiring — the substream
// package stays free of gb28181 imports.
type GBPuller interface {
	// EnsureSubChannelRegistered registers the probe-discovered channel code
	// as an INVITEable pull target (idempotent; no-op when the catalog
	// already lists it).
	EnsureSubChannelRegistered(deviceID, channelID string) error
	// InviteSubChannel establishes the media session feeding onAU; the
	// returned func BYEs it.
	InviteSubChannel(deviceID, channelID string, onAU func(au [][]byte, ptsTicks int64, isIDR bool)) (func(), error)
}

// Resolver maps a camera ID to its sub-stream pull target. ok=false means the
// camera has no sub-stream configuration (→ ErrNoSubStream). The resolver may
// perform network I/O (ONVIF GetStreamUri); it runs OUTSIDE the manager lock.
type Resolver func(ctx context.Context, cameraID string) (Target, bool, error)

// Config configures a Manager. Zero durations fall back to defaults.
type Config struct {
	Resolver Resolver
	// IdleTimeout is how long a source with zero references survives before
	// the puller is stopped (default 30s).
	IdleTimeout time.Duration
	// ReadyTimeout bounds how long Acquire waits for the first parameter
	// sets (default 8s — typical RTSP connect + first keyframe is 1-4s).
	ReadyTimeout time.Duration
	// DialTimeout bounds each connect/DESCRIBE/SETUP/PLAY sequence (default
	// 5s per attempt; retries continue with backoff up to ReadyTimeout).
	DialTimeout time.Duration
	// FrameStallTimeout: no frames for this long while live → reconnect
	// (default 15s, matching the recorder frame watchdog default).
	FrameStallTimeout time.Duration
	// WireHub attaches observability callbacks to each created hub (camera
	// manager wires Prometheus; optional).
	WireHub func(hub *streamhub.StreamHub, cameraID string)
}

func (c *Config) normalize() {
	if c.IdleTimeout <= 0 {
		c.IdleTimeout = 30 * time.Second
	}
	if c.ReadyTimeout <= 0 {
		c.ReadyTimeout = 8 * time.Second
	}
	if c.DialTimeout <= 0 {
		c.DialTimeout = 5 * time.Second
	}
	if c.FrameStallTimeout <= 0 {
		c.FrameStallTimeout = 15 * time.Second
	}
}

// params is the codec parameter snapshot served to egress registration. The
// SDP provides the initial copy; in-band parameter sets refresh it.
type params struct {
	codec model.Format
	sps   []byte
	pps   []byte
	vps   []byte
}

// Source is a camera's live sub-stream: the fan-out hub plus the codec
// parameters egress endpoints need to register players.
type Source struct {
	cameraID    string
	hub         *streamhub.StreamHub
	params      atomic.Pointer[params]
	ready       chan struct{}
	readyOne    sync.Once
	state       atomic.Value // string
	lastFrameAt atomic.Int64 // unix nano
}

// CameraID returns the owning camera's ID.
func (s *Source) CameraID() string { return s.cameraID }

// Hub returns the fan-out hub for this sub-stream.
func (s *Source) Hub() *streamhub.StreamHub { return s.hub }

// CodecParams returns the current codec parameter snapshot. Before the first
// keyframe the codec is "" — egress endpoints treat that as "not ready".
func (s *Source) CodecParams() (codec model.Format, sps, pps, vps []byte) {
	if p := s.params.Load(); p != nil {
		return p.codec, p.sps, p.pps, p.vps
	}
	return "", nil, nil, nil
}

// State returns the pull state (StateStarting/StateLive/…).
func (s *Source) State() string {
	if v, ok := s.state.Load().(string); ok && v != "" {
		return v
	}
	return StateStarting
}

func (s *Source) publishParams(codec model.Format, sps, pps, vps []byte) {
	s.params.Store(&params{codec: codec, sps: sps, pps: pps, vps: vps})
	s.readyOne.Do(func() { close(s.ready) })
}

// entry couples a Source with the manager's bookkeeping.
type entry struct {
	src    *Source
	cancel context.CancelFunc
	done   chan struct{} // closed when the pull goroutine exited
	target Target
	refs   int
	idle   *time.Timer
	// lastPTS is the cross-session 90 kHz timeline high-water mark (see
	// pullOnce's rebase logic). Atomic: RTP callbacks and session setup run
	// on different goroutines across reconnects.
	lastPTS atomic.Int64
}

// Manager owns per-camera sub-stream sources with reference counting and
// idle recycling. All public methods are safe for concurrent use.
type Manager struct {
	cfg     Config
	mu      sync.Mutex
	sources map[string]*entry
	stopped bool
	// onRecycle is invoked (from a manager goroutine) after a source was torn
	// down for idleness or failure. The hub is passed so the app layer can
	// unsubscribe protocol consumers the managers cannot reach themselves
	// (notably HLS's "hls" consumer — the HLS entry does not store its hub).
	// Set once at wiring time via SetOnRecycle before traffic arrives.
	onRecycle func(cameraID string, hub *streamhub.StreamHub)
	// gbPuller serves KindGB28181 targets (nil → those targets error and the
	// source reconnects until wired). Set at app wiring via SetGBPuller.
	gbPuller GBPuller
}

// NewManager creates a Manager. cfg.Resolver is required for Acquire to work.
func NewManager(cfg Config) *Manager {
	cfg.normalize()
	return &Manager{
		cfg:     cfg,
		sources: make(map[string]*entry),
	}
}

// SetOnRecycle registers the recycle callback (see Manager.onRecycle). Only
// call during wiring, before the first Acquire.
func (m *Manager) SetOnRecycle(fn func(cameraID string, hub *streamhub.StreamHub)) {
	m.mu.Lock()
	m.onRecycle = fn
	m.mu.Unlock()
}

// SetGBPuller wires the GB28181 sub-channel session starter (#560). Only call
// during wiring, before the first Acquire.
func (m *Manager) SetGBPuller(p GBPuller) {
	m.mu.Lock()
	m.gbPuller = p
	m.mu.Unlock()
}

// Acquire returns the camera's sub-stream source, starting the pull if
// needed, and takes one reference. Each successful Acquire must be balanced
// by exactly one Release. Acquire blocks until the source has codec
// parameters (or the ready timeout / caller ctx expires — the pull keeps
// retrying in the background either way).
func (m *Manager) Acquire(ctx context.Context, cameraID string) (*Source, error) {
	if cameraID == "" || m.cfg.Resolver == nil {
		return nil, ErrNoSubStream
	}

	m.mu.Lock()
	if m.stopped {
		m.mu.Unlock()
		return nil, ErrStopped
	}
	if e := m.sources[cameraID]; e != nil {
		e.refs++
		if e.idle != nil {
			e.idle.Stop()
			e.idle = nil
		}
		m.mu.Unlock()
		if e.src.State() == StateFailed {
			m.Release(cameraID)
			return nil, fmt.Errorf("sub-stream pull failed permanently: %w", ErrNotReady)
		}
		if err := m.waitReady(ctx, e); err != nil {
			m.Release(cameraID)
			return nil, err
		}
		return e.src, nil
	}
	m.mu.Unlock()

	// Resolve outside the lock — may perform ONVIF network I/O.
	target, ok, err := m.cfg.Resolver(ctx, cameraID)
	if err != nil {
		return nil, fmt.Errorf("resolve sub-stream: %w", err)
	}
	if !ok || (target.URL == "" && target.Kind != KindGB28181) {
		return nil, ErrNoSubStream
	}

	m.mu.Lock()
	if m.stopped {
		m.mu.Unlock()
		return nil, ErrStopped
	}
	if e := m.sources[cameraID]; e != nil {
		// Another Acquire won the race and started the pull — join it.
		e.refs++
		if e.idle != nil {
			e.idle.Stop()
			e.idle = nil
		}
		m.mu.Unlock()
		if err := m.waitReady(ctx, e); err != nil {
			m.Release(cameraID)
			return nil, err
		}
		return e.src, nil
	}

	src := &Source{
		cameraID: cameraID,
		hub:      streamhub.New(),
		ready:    make(chan struct{}),
	}
	src.hub.SetCameraID(cameraID)
	src.hub.SetSource("substream")
	if m.cfg.WireHub != nil {
		m.cfg.WireHub(src.hub, cameraID)
	}
	src.state.Store(StateStarting)

	pullCtx, cancel := context.WithCancel(context.Background())
	e := &entry{src: src, cancel: cancel, done: make(chan struct{}), refs: 1, target: target}
	m.sources[cameraID] = e
	m.mu.Unlock()

	go m.pull(pullCtx, e)

	if err := m.waitReady(ctx, e); err != nil {
		m.Release(cameraID)
		return nil, err
	}
	return src, nil
}

// waitReady blocks until the source published codec parameters, bounded by
// the ready timeout and the caller's context. Must be called WITHOUT m.mu.
func (m *Manager) waitReady(ctx context.Context, e *entry) error {
	select {
	case <-e.src.ready:
		return nil
	default:
	}
	timeout := m.cfg.ReadyTimeout
	if dl, ok := ctx.Deadline(); ok {
		if t := time.Until(dl); t < timeout {
			timeout = t
		}
	}
	select {
	case <-e.src.ready:
		return nil
	case <-e.done:
		// Puller exited (permanent failure or teardown) without ever
		// producing parameters.
		if e.src.params.Load() != nil {
			return nil
		}
		return fmt.Errorf("sub-stream pull exited before first keyframe: %w", ErrNotReady)
	case <-time.After(timeout):
		return ErrNotReady
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Release drops one reference. When the count reaches zero an idle timer is
// armed; if no Acquire arrives before it fires, the puller is stopped and the
// source deleted (onRecycle then runs).
func (m *Manager) Release(cameraID string) {
	m.mu.Lock()
	e := m.sources[cameraID]
	if e == nil {
		m.mu.Unlock()
		return
	}
	if e.refs > 0 {
		e.refs--
	}
	if e.refs > 0 {
		m.mu.Unlock()
		return
	}
	if e.idle == nil {
		idle := m.cfg.IdleTimeout
		e.idle = time.AfterFunc(idle, func() { m.recycle(cameraID, "idle") })
	}
	m.mu.Unlock()
}

// recycle removes the source when it has no references, stops the puller,
// and fires onRecycle with the (now dead) hub. The teardown itself runs
// detached so the idle-timer goroutine (or a failing pull's self-cleanup)
// never blocks on the puller's dial timeout.
func (m *Manager) recycle(cameraID string, reason string) {
	m.mu.Lock()
	e := m.sources[cameraID]
	if e == nil || e.refs > 0 {
		// Re-acquired inside the window (timer fired late) — keep it.
		m.mu.Unlock()
		return
	}
	delete(m.sources, cameraID)
	if e.idle != nil {
		e.idle.Stop()
		e.idle = nil
	}
	onRecycle := m.onRecycle
	m.mu.Unlock()

	subLogger.Info("sub-stream recycled", "camera_id", cameraID, "reason", reason)
	go func() {
		e.cancel()
		<-e.done
		if onRecycle != nil {
			onRecycle(cameraID, e.src.hub)
		}
	}()
}

// StopCamera tears the camera's source down immediately (camera removed /
// stopped / sub config changed). No-op when absent.
func (m *Manager) StopCamera(cameraID string) {
	m.recycle(cameraID, "camera")
}

// Stop tears down every source and prevents new Acquires.
func (m *Manager) Stop() {
	m.mu.Lock()
	m.stopped = true
	entries := make([]*entry, 0, len(m.sources))
	for id, e := range m.sources {
		entries = append(entries, e)
		delete(m.sources, id)
	}
	onRecycle := m.onRecycle
	m.mu.Unlock()

	for _, e := range entries {
		e.cancel()
		<-e.done
		if onRecycle != nil {
			onRecycle(e.src.cameraID, e.src.hub)
		}
	}
}

// SourceStatus is the observability snapshot served to the app layer.
type SourceStatus struct {
	CameraID    string       `json:"camera_id"`
	State       string       `json:"state"`
	Codec       model.Format `json:"codec,omitempty"` // actual stream codec (empty before first keyframe)
	Refs        int          `json:"refs"`
	LastFrameAt time.Time    `json:"last_frame_at"`
	Consumers   int          `json:"consumers"`
}

// Snapshot returns the status of every active source.
func (m *Manager) Snapshot() []SourceStatus {
	m.mu.Lock()
	out := make([]SourceStatus, 0, len(m.sources))
	for id, e := range m.sources {
		codec, _, _, _ := e.src.CodecParams()
		out = append(out, SourceStatus{
			CameraID:    id,
			State:       e.src.State(),
			Codec:       codec,
			Refs:        e.refs,
			LastFrameAt: time.Unix(0, e.src.lastFrameAt.Load()),
			Consumers:   e.src.hub.ConsumerCount(),
		})
	}
	m.mu.Unlock()
	return out
}

// Status returns the live source status for one camera, or nil when the
// camera has no sub-stream entry (never acquired, or recycled).
func (m *Manager) Status(cameraID string) *SourceStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.sources[cameraID]
	if !ok {
		return nil
	}
	codec, _, _, _ := e.src.CodecParams()
	return &SourceStatus{
		CameraID:    cameraID,
		State:       e.src.State(),
		Codec:       codec,
		Refs:        e.refs,
		LastFrameAt: time.Unix(0, e.src.lastFrameAt.Load()),
		Consumers:   e.src.hub.ConsumerCount(),
	}
}

// Hub returns the camera's sub-stream hub — nil when no entry exists. The
// flow view reads its full consumer fan-out (sends/drops/dwell per consumer).
func (m *Manager) Hub(cameraID string) *streamhub.StreamHub {
	m.mu.Lock()
	defer m.mu.Unlock()
	if e, ok := m.sources[cameraID]; ok {
		return e.src.hub
	}
	return nil
}
