package gb28181

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	gbsip "github.com/mickeyzzc/gb28181-go/platform/sip"
)

// GB/T 28181-2022 platform-side on-demand snapshot sessions (#708): the
// platform sends DeviceControl(SnapShotCmd) with an UploadURL + SessionID;
// the device POSTs the JPEGs to the URL and finishes with an
// UploadSnapShotFinished notify carrying the same SessionID. This manager
// owns the session registry: it validates incoming uploads, persists frames
// via the snapshot store, publishes camera.snapshot events, and closes
// sessions on completion notify OR timeout (the spec's "timeout without
// notify = failure").
//
// Completion notifies reach Finish() from the SIP server's MESSAGE dispatch
// (wired in builders); until that plumbing exists upstream the timeout path
// alone still closes every session — no leaks.

// Snapshot session lifecycle errors.
var (
	ErrSnapshotSessionUnknown = errors.New("gb28181: unknown snapshot session")
	ErrSnapshotSessionClosed  = errors.New("gb28181: snapshot session already closed")
	ErrSnapshotNotJPEG        = errors.New("gb28181: upload body is not a JPEG")
)

// SnapshotOutcome is the terminal state of a snapshot session.
type SnapshotOutcome string

const (
	SnapshotPending  SnapshotOutcome = "pending"
	SnapshotComplete SnapshotOutcome = "complete"
	SnapshotPartial  SnapshotOutcome = "partial"
	SnapshotFailed   SnapshotOutcome = "failed"
	SnapshotTimeout  SnapshotOutcome = "timeout"
)

// SnapshotStore persists one JPEG and returns the storage-root-relative
// path. Satisfied by *snapshot.Persistor.
type SnapshotStore interface {
	Persist(cameraID string, jpeg []byte) (string, error)
}

// SnapshotSession is one SnapShotCmd exchange. All mutating methods are
// called through the manager under its lock; Outcome/Files are safe from any
// goroutine after the session reaches a terminal state.
type SnapshotSession struct {
	ID        string
	ChannelID string
	DeviceID  string
	CameraID  string
	SnapNum   int
	CreatedAt time.Time
	Deadline  time.Time

	mu      sync.Mutex
	files   []string
	outcome SnapshotOutcome
}

// Outcome returns the session's current state (thread-safe).
func (s *SnapshotSession) Outcome() SnapshotOutcome {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.outcome == "" {
		return SnapshotPending
	}
	return s.outcome
}

// Files returns the stored frame paths (thread-safe copy).
func (s *SnapshotSession) Files() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.files...)
}

// SnapshotSessionManager tracks in-flight snapshot sessions.
type SnapshotSessionManager struct {
	mu       sync.Mutex
	sessions map[string]*SnapshotSession

	store   SnapshotStore
	timeout time.Duration

	publish func(topic string, data any)
	now     func() time.Time

	sweepStop chan struct{}
	sweepOnce sync.Once
}

// SnapshotManagerOption configures NewSnapshotSessionManager.
type SnapshotManagerOption func(*SnapshotSessionManager)

// WithSnapshotClock injects the clock (tests).
func WithSnapshotClock(now func() time.Time) SnapshotManagerOption {
	return func(m *SnapshotSessionManager) { m.now = now }
}

// WithSnapshotPublisher injects the event publisher (NVR event bus adapter).
func WithSnapshotPublisher(p func(topic string, data any)) SnapshotManagerOption {
	return func(m *SnapshotSessionManager) { m.publish = p }
}

// NewSnapshotSessionManager creates a manager. timeout bounds a session's
// life from creation (default used when <= 0: 30s — the device-side capture
// of ≤10 frames at ≥1s interval fits well within it).
func NewSnapshotSessionManager(store SnapshotStore, timeout time.Duration, opts ...SnapshotManagerOption) *SnapshotSessionManager {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	m := &SnapshotSessionManager{
		sessions:  make(map[string]*SnapshotSession),
		store:     store,
		timeout:   timeout,
		publish:   func(string, any) {},
		now:       time.Now,
		sweepStop: make(chan struct{}),
	}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

// Start launches the expiry sweeper (1s cadence — sessions are few, the
// tick is a map walk).
func (m *SnapshotSessionManager) Start() {
	m.sweepOnce.Do(func() {
		go func() {
			t := time.NewTicker(time.Second)
			defer t.Stop()
			for {
				select {
				case <-m.sweepStop:
					return
				case <-t.C:
					m.Sweep()
				}
			}
		}()
	})
}

// Stop terminates the sweeper (idempotent).
func (m *SnapshotSessionManager) Stop() {
	select {
	case <-m.sweepStop:
	default:
		close(m.sweepStop)
	}
}

// CreateSession registers a new snapshot session with a random 32-hex-char
// SessionID (spec: [A-Za-z0-9-], 32..128 bytes).
func (m *SnapshotSessionManager) CreateSession(channelID, deviceID, cameraID string, snapNum int) (*SnapshotSession, error) {
	if snapNum < 1 {
		snapNum = 1
	}
	if snapNum > 10 {
		snapNum = 10 // spec: SnapNum 1..10
	}
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return nil, fmt.Errorf("gb28181: session id: %w", err)
	}
	now := m.now()
	s := &SnapshotSession{
		ID:        hex.EncodeToString(buf),
		ChannelID: channelID,
		DeviceID:  deviceID,
		CameraID:  cameraID,
		SnapNum:   snapNum,
		CreatedAt: now,
		Deadline:  now.Add(m.timeout),
	}
	m.mu.Lock()
	m.sessions[s.ID] = s
	m.mu.Unlock()
	return s, nil
}

// Get returns a live (not-yet-swept) session.
func (m *SnapshotSessionManager) Get(sessionID string) (*SnapshotSession, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[sessionID]
	return s, ok
}

// Receive validates and stores one uploaded JPEG frame. Unknown or closed
// sessions are refused so the HTTP endpoint can map them onto 404/409.
func (m *SnapshotSessionManager) Receive(sessionID string, jpeg []byte) (string, error) {
	if len(jpeg) < 4 || jpeg[0] != 0xFF || jpeg[1] != 0xD8 {
		return "", ErrSnapshotNotJPEG
	}
	m.mu.Lock()
	s, ok := m.sessions[sessionID]
	if !ok {
		m.mu.Unlock()
		return "", ErrSnapshotSessionUnknown
	}
	// Session-scoped lock ordering: manager first, then session.
	s.mu.Lock()
	if s.outcome != "" {
		s.mu.Unlock()
		m.mu.Unlock()
		return "", ErrSnapshotSessionClosed
	}
	s.mu.Unlock()
	m.mu.Unlock()

	// Persist outside both locks (disk I/O).
	path, err := m.store.Persist(s.CameraID, jpeg)
	if err != nil {
		return "", fmt.Errorf("gb28181: persist snapshot: %w", err)
	}

	s.mu.Lock()
	s.files = append(s.files, path)
	s.mu.Unlock()

	if m.publish != nil {
		m.publish("camera.snapshot", map[string]any{
			"camera_id":  s.CameraID,
			"file_path":  path,
			"timestamp":  m.now().UTC().Format(time.RFC3339Nano),
			"trigger":    "gb28181",
			"channel_id": s.ChannelID,
			"session_id": s.ID,
		})
	}
	return path, nil
}

// Finish applies an UploadSnapShotFinished notify: successCount is the
// number of successfully captured+uploaded frames (the notify's SnapShotList
// length; 0 = whole exchange failed per A.2.5.7). The session stays
// registered (marked terminal) until Sweep evicts it, so a straggler upload
// lands on ErrSnapshotSessionClosed rather than a misleading unknown-ID.
func (m *SnapshotSessionManager) Finish(sessionID string, successCount int) {
	m.mu.Lock()
	s, ok := m.sessions[sessionID]
	if !ok {
		m.mu.Unlock()
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	m.mu.Unlock()

	if s.outcome != "" {
		return
	}
	switch {
	case successCount <= 0:
		s.outcome = SnapshotFailed
	case successCount >= s.SnapNum:
		s.outcome = SnapshotComplete
	default:
		s.outcome = SnapshotPartial
	}
}

// SubscribeFinished wires the library's UploadSnapShotFinished notify
// (platform/sip event bus, topic gb28181.snapshot.finished — lib PR #54)
// onto Finish: the device's own completion report closes the session
// immediately instead of waiting out the TTL sweep (#708 loop closure).
// The consumer goroutine unwinds with the manager's Stop.
func (m *SnapshotSessionManager) SubscribeFinished(bus *gbsip.EventBus) {
	if bus == nil {
		return
	}
	ch := make(chan gbsip.Event, 16)
	_ = bus.Subscribe(gbsip.TopicGB28181SnapshotFinished, ch, 16)
	go func() {
		for {
			select {
			case <-m.sweepStop:
				return
			case ev, ok := <-ch:
				if !ok {
					return
				}
				fin, valid := ev.Data.(gbsip.GB28181SnapshotFinishedEvent)
				if !valid {
					continue
				}
				m.Finish(fin.SessionID, fin.SuccessCount)
			}
		}
	}()
}

// Sweep expires sessions past their deadline. Also evicts terminal sessions
// missed by Finish's evict (defensive — Finish already evicts).
func (m *SnapshotSessionManager) Sweep() {
	now := m.now()
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, s := range m.sessions {
		s.mu.Lock()
		outcome := s.outcome
		expired := now.After(s.Deadline)
		if outcome == "" && expired {
			s.outcome = SnapshotTimeout
			outcome = SnapshotTimeout
		}
		s.mu.Unlock()
		if outcome != "" {
			delete(m.sessions, id)
		}
	}
}
