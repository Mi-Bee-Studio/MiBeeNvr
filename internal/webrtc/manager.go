package webrtc

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/pion/interceptor"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
)

var logger = slog.Default().With("component", "webrtc-manager")

const (
	defaultMaxPeers     = 2
	defaultIdleTimeout  = 60 * time.Second
	defaultFrameBufSize = 100
	defaultFPS          = 30
	h264ClockRate       = 90000
)

// frameMsg is an async write request for the WebRTC track.
type frameMsg struct {
	pts int64
	au  [][]byte
}

// peerEntry holds a single WHEP peer connection and its metadata.
type peerEntry struct {
	mu        sync.Mutex
	pc        *webrtc.PeerConnection
	track     *webrtc.TrackLocalStaticSample
	sender    *webrtc.RTPSender
	cancel    context.CancelFunc
	camID     string
	sessionID string
	lastUsed  time.Time
	frameCh   chan frameMsg
	drops     uint64 // atomic: total frames dropped due to buffer full
	lastPTS   int64
}

// Manager manages WebRTC WHEP sessions for camera streaming.
// It supports H.264 video only (no audio), with configurable max peers
// per camera and idle eviction.
type Manager struct {
	mu           sync.RWMutex
	peers        map[string]*peerEntry // sessionID -> entry
	camPeers     map[string][]string   // camID -> []sessionID
	api          *webrtc.API
	maxPeers     int
	idleTimeout  time.Duration
	frameBufSize int
}

// ManagerOption configures a Manager.
type ManagerOption func(*Manager)

// WithMaxPeers sets the maximum number of concurrent WHEP peers per camera.
func WithMaxPeers(n int) ManagerOption {
	return func(m *Manager) {
		if n > 0 {
			m.maxPeers = n
		}
	}
}

// WithIdleTimeout sets the idle timeout for WHEP sessions.
func WithIdleTimeout(d time.Duration) ManagerOption {
	return func(m *Manager) {
		if d > 0 {
			m.idleTimeout = d
		}
	}
}

// WithFrameBufSize sets the per-peer frame buffer size.
func WithFrameBufSize(n int) ManagerOption {
	return func(m *Manager) {
		if n > 0 {
			m.frameBufSize = n
		}
	}
}

// NewManager creates a new WebRTC Manager with H.264 video-only support.
func NewManager(opts ...ManagerOption) *Manager {
	mediaEngine := &webrtc.MediaEngine{}
	if err := mediaEngine.RegisterCodec(webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{
			MimeType:    webrtc.MimeTypeH264,
			ClockRate:   h264ClockRate,
			SDPFmtpLine: "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42001f",
		},
		PayloadType: 96,
	}, webrtc.RTPCodecTypeVideo); err != nil {
		logger.Error("failed to register H264 codec", "error", err)
	}

	interceptorRegistry := &interceptor.Registry{}
	if err := webrtc.RegisterDefaultInterceptors(mediaEngine, interceptorRegistry); err != nil {
		logger.Error("failed to register default interceptors", "error", err)
	}

	api := webrtc.NewAPI(
		webrtc.WithMediaEngine(mediaEngine),
		webrtc.WithInterceptorRegistry(interceptorRegistry),
	)

	m := &Manager{
		peers:        make(map[string]*peerEntry),
		camPeers:     make(map[string][]string),
		api:          api,
		maxPeers:     defaultMaxPeers,
		idleTimeout:  defaultIdleTimeout,
		frameBufSize: defaultFrameBufSize,
	}

	for _, opt := range opts {
		opt(m)
	}

	return m
}

// CanHandle returns true if the codec format is supported for WebRTC streaming.
// Only H.264 is supported (no H.265, no MJPEG).
func (m *Manager) CanHandle(codec model.Format) bool {
	return codec == model.FormatH264
}

// WriteH264 queues an H.264 access unit for async writing to all WHEP peers
// for the given camera. This is non-blocking — frames are dropped if a peer's
// buffer is full to protect the recording pipeline.
func (m *Manager) WriteH264(camID string, pts int64, au [][]byte) {
	m.mu.RLock()
	sids := m.camPeers[camID]
	if len(sids) == 0 {
		m.mu.RUnlock()
		return
	}
	// Snapshot entries to avoid holding lock during sends
	entries := make([]*peerEntry, 0, len(sids))
	for _, sid := range sids {
		if e, ok := m.peers[sid]; ok {
			entries = append(entries, e)
		}
	}
	m.mu.RUnlock()

	for _, entry := range entries {
		entry.mu.Lock()
		entry.lastUsed = time.Now()
		entry.mu.Unlock()

		// Non-blocking send — drop frame if buffer full
		select {
		case entry.frameCh <- frameMsg{pts: pts, au: au}:
		default:
			dropCount := atomic.AddUint64(&entry.drops, 1)
			if dropCount%100 == 0 {
				logger.Warn("WebRTC frames dropped",
					"camera_id", camID,
					"session_id", entry.sessionID,
					"total_drops", dropCount)
			}
		}
	}
}

// CreateWHEPSession creates a new WHEP session for the given camera.
// It processes the SDP offer, creates a PeerConnection with H.264 video only,
// and returns the SDP answer and session ID.
func (m *Manager) CreateWHEPSession(camID string, offerSDP []byte) (answerSDP []byte, sessionID string, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Check peer limit for this camera
	if len(m.camPeers[camID]) >= m.maxPeers {
		return nil, "", ErrMaxPeersReached
	}

	// Create PeerConnection via shared API factory
	pc, err := m.api.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		return nil, "", err
	}

	// Create H.264 video track
	track, err := webrtc.NewTrackLocalStaticSample(
		webrtc.RTPCodecCapability{
			MimeType:    webrtc.MimeTypeH264,
			ClockRate:   h264ClockRate,
			SDPFmtpLine: "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42001f",
		},
		"video", camID,
	)
	if err != nil {
		pc.Close()
		return nil, "", err
	}

	// Add track to PeerConnection
	sender, err := pc.AddTrack(track)
	if err != nil {
		pc.Close()
		return nil, "", err
	}

	// Context for goroutine lifecycle
	ctx, cancel := context.WithCancel(context.Background())

	// Drain RTCP for interceptors (NACK, etc.) to function
	go func() {
		defer func() {
			if r := recover(); r != nil {
				logger.Warn("RTCP drain goroutine panic recovered", "error", r)
			}
		}()
		buf := make([]byte, 1500)
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}
			if _, _, readErr := sender.Read(buf); readErr != nil {
				return
			}
		}
	}()

	// Generate session ID
	sid := uuid.New().String()

	// Set up connection state monitor — auto-cleanup on disconnect/failure
	pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		if state == webrtc.PeerConnectionStateClosed ||
			state == webrtc.PeerConnectionStateFailed ||
			state == webrtc.PeerConnectionStateDisconnected {
			_ = m.DeleteWHEPSession(sid)
		}
	})

	// Set remote description (offer from WHEP client)
	if err := pc.SetRemoteDescription(webrtc.SessionDescription{
		Type: webrtc.SDPTypeOffer,
		SDP:  string(offerSDP),
	}); err != nil {
		cancel()
		pc.Close()
		return nil, "", err
	}

	// Create SDP answer
	answer, err := pc.CreateAnswer(nil)
	if err != nil {
		cancel()
		pc.Close()
		return nil, "", err
	}

	// Set local description and wait for ICE gathering to complete
	gatherComplete := webrtc.GatheringCompletePromise(pc)
	if err := pc.SetLocalDescription(answer); err != nil {
		cancel()
		pc.Close()
		return nil, "", err
	}
	<-gatherComplete

	// Reject audio m-lines in the answer SDP (belt-and-suspenders)
	finalSDP := rejectAudioInSDP(pc.LocalDescription().SDP)

	// Create peer entry
	entry := &peerEntry{
		pc:        pc,
		track:     track,
		sender:    sender,
		cancel:    cancel,
		camID:     camID,
		sessionID: sid,
		lastUsed:  time.Now(),
		frameCh:   make(chan frameMsg, m.frameBufSize),
	}

	m.peers[sid] = entry
	m.camPeers[camID] = append(m.camPeers[camID], sid)

	// Start async frame writer goroutine
	go m.writeLoop(ctx, entry)

	// Start idle watchdog goroutine
	go m.idleWatchdog(ctx, entry)

	logger.Info("WHEP session created", "camera_id", camID, "session_id", sid)
	return []byte(finalSDP), sid, nil
}

// DeleteWHEPSession removes a WHEP session and cleans up all associated resources.
// Returns ErrSessionNotFound if the session does not exist.
func (m *Manager) DeleteWHEPSession(sessionID string) error {
	m.mu.Lock()
	entry, ok := m.peers[sessionID]
	if !ok {
		m.mu.Unlock()
		return ErrSessionNotFound
	}
	delete(m.peers, sessionID)

	// Remove session from camera's peer list
	sids := m.camPeers[entry.camID]
	for i, sid := range sids {
		if sid == sessionID {
			m.camPeers[entry.camID] = append(sids[:i], sids[i+1:]...)
			break
		}
	}
	if len(m.camPeers[entry.camID]) == 0 {
		delete(m.camPeers, entry.camID)
	}
	m.mu.Unlock()

	// Clean up outside the lock to avoid deadlock with OnConnectionStateChange
	entry.cancel()
	if entry.pc != nil {
		_ = entry.pc.Close()
	}

	logger.Info("WHEP session deleted", "camera_id", entry.camID, "session_id", sessionID)
	return nil
}

// ActivePeerCount returns the number of active WHEP peers for the given camera.
func (m *Manager) ActivePeerCount(camID string) int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.camPeers[camID])
}

// TotalPeerCount returns the total number of active WHEP sessions across all cameras.
func (m *Manager) TotalPeerCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.peers)
}

// StopAll closes all active WHEP sessions and releases resources.
func (m *Manager) StopAll() {
	m.mu.Lock()
	entries := make([]*peerEntry, 0, len(m.peers))
	for sid, entry := range m.peers {
		entries = append(entries, entry)
		delete(m.peers, sid)
	}
	m.camPeers = make(map[string][]string)
	m.mu.Unlock()

	// Clean up outside the lock to avoid deadlock with callbacks
	for _, entry := range entries {
		entry.cancel()
		if entry.pc != nil {
			_ = entry.pc.Close()
		}
	}
}

// writeLoop drains frames from the async buffer and writes them to the WebRTC track.
// This decouples the non-blocking WriteH264 path from the actual RTP packetization.
func (m *Manager) writeLoop(ctx context.Context, entry *peerEntry) {
	defer func() {
		if r := recover(); r != nil {
			logger.Warn("WebRTC writeLoop panic recovered",
				"session_id", entry.sessionID, "error", r)
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return
		case frame := <-entry.frameCh:
			// Convert access unit to Annex B byte stream
			data := annexBEncode(frame.au)
			if len(data) == 0 {
				continue
			}

			// Calculate duration from PTS delta (90kHz clock)
			entry.mu.Lock()
			var dur time.Duration
			if entry.lastPTS == 0 {
				dur = time.Second / defaultFPS
			} else {
				delta := frame.pts - entry.lastPTS
				if delta > 0 {
					dur = time.Duration(delta) * time.Second / h264ClockRate
					// Cap at 1 second to prevent huge durations from PTS anomalies
					if dur > time.Second {
						dur = time.Second / defaultFPS
					}
				} else {
					dur = time.Second / defaultFPS
				}
			}
			entry.lastPTS = frame.pts
			entry.mu.Unlock()

			if dur < time.Millisecond {
				dur = time.Millisecond
			}

			if err := entry.track.WriteSample(media.Sample{
				Data:     data,
				Duration: dur,
			}); err != nil {
				// Non-fatal: log and continue (never crash the goroutine)
				if !strings.Contains(err.Error(), "use of closed") {
					logger.Warn("WebRTC write sample error",
						"session_id", entry.sessionID, "error", err)
				}
			}
		}
	}
}

// idleWatchdog monitors the peer's lastUsed timestamp and evicts idle sessions.
// Same pattern as HLS manager's idle watchdog.
func (m *Manager) idleWatchdog(ctx context.Context, entry *peerEntry) {
	defer func() {
		if r := recover(); r != nil {
			logger.Warn("WebRTC idleWatchdog panic recovered",
				"session_id", entry.sessionID, "error", r)
		}
	}()

	ticker := time.NewTicker(m.idleTimeout / 2)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			entry.mu.Lock()
			lastUsed := entry.lastUsed
			entry.mu.Unlock()

			if time.Since(lastUsed) > m.idleTimeout {
				logger.Info("WebRTC peer idle timeout, closing",
					"camera_id", entry.camID, "session_id", entry.sessionID)
				_ = m.DeleteWHEPSession(entry.sessionID)
				return
			}
		}
	}
}
