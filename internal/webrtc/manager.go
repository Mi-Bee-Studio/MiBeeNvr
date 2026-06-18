package webrtc

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/pion/interceptor"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/metrics"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model/nalutil"
)

var logger = slog.Default().With("component", "webrtc-manager")

const (
	defaultMaxPeers       = 2
	defaultIdleTimeout    = 60 * time.Second
	defaultFrameBufSize   = 100
	defaultFPS            = 30
	h264ClockRate         = 90000
	highDropRateThreshold = 0.20 // 20% — trigger frame skipping
	lowDropRateThreshold  = 0.05 // 5% — restore full frame rate
)


// peerEntry holds a single WHEP peer connection and its metadata.
type peerEntry struct {
	mu         sync.Mutex
	pc         *webrtc.PeerConnection
	track      *webrtc.TrackLocalStaticSample
	sender     *webrtc.RTPSender
	cancel     context.CancelFunc
	camID      string
	sessionID  string
	lastUsed   time.Time
	frameCh    chan model.FrameMsg
	drops      uint64             // atomic: total frames dropped due to buffer full
	congestion *congestionTracker // tracks drop rate for bitrate adaptation
	lastPTS    int64
}

// congestionTracker tracks frame send/drop rates in a sliding window
// to detect network congestion and trigger frame skipping.
//
// Concurrency: recordDropped (called from WriteH264 goroutine) and recordSent /
// shouldSkipFrame / dropRate (called from writeLoop goroutine) access these fields
// concurrently. All public methods hold mu for the duration of their critical section.
type congestionTracker struct {
	mu          sync.Mutex
	windowSize  int
	window      []bool // true = sent, false = dropped (circular buffer)
	windowPos   int
	windowCount int
	congested   bool
	skipCounter int // alternates skip pattern: 0=send, 1=skip
}

// newCongestionTracker creates a tracker with the given sliding window size.
func newCongestionTracker(windowSize int) *congestionTracker {
	return &congestionTracker{
		windowSize: windowSize,
		window:     make([]bool, windowSize),
	}
}

// recordSent records a successfully sent frame.
func (ct *congestionTracker) recordSent() {
	ct.record(true)
}

// recordDropped records a dropped frame.
func (ct *congestionTracker) recordDropped() {
	ct.record(false)
}

// record adds an entry to the sliding window.
func (ct *congestionTracker) record(sent bool) {
	ct.mu.Lock()
	defer ct.mu.Unlock()
	ct.window[ct.windowPos] = sent
	ct.windowPos = (ct.windowPos + 1) % ct.windowSize
	if ct.windowCount < ct.windowSize {
		ct.windowCount++
	}
}

// dropRate returns the current drop rate (0.0 to 1.0).
func (ct *congestionTracker) dropRate() float64 {
	ct.mu.Lock()
	defer ct.mu.Unlock()
	return ct.dropRateLocked()
}

// dropRateLocked is the lock-free variant — caller must hold ct.mu.
func (ct *congestionTracker) dropRateLocked() float64 {
	if ct.windowCount == 0 {
		return 0
	}
	var drops int
	for i := 0; i < ct.windowCount; i++ {
		if !ct.window[i] {
			drops++
		}
	}
	return float64(drops) / float64(ct.windowCount)
}

// shouldSkipFrame returns true if a non-IDR frame should be skipped due to congestion.
// IDR frames are never skipped. When congested, skips every other non-IDR frame.
func (ct *congestionTracker) shouldSkipFrame(isIDR bool) bool {
	ct.mu.Lock()
	defer ct.mu.Unlock()

	// IDR frames are never skipped
	if isIDR {
		return false
	}

	// Empty window — no congestion
	if ct.windowCount == 0 {
		return false
	}

	rate := ct.dropRateLocked()

	// State transitions
	if !ct.congested && rate > highDropRateThreshold {
		ct.congested = true
		ct.skipCounter = 0
		slog.Debug("webrtc_congestion_start",
			"drop_rate", rate,
			"window_count", ct.windowCount)
	} else if ct.congested && rate < lowDropRateThreshold {
		ct.congested = false
		slog.Debug("webrtc_congestion_end",
			"drop_rate", rate,
			"window_count", ct.windowCount)
	}

	if !ct.congested {
		return false
	}

	// Alternating skip: skip, send, skip, send...
	ct.skipCounter++
	return ct.skipCounter%2 == 1
}
// Manager manages WebRTC WHEP sessions for camera streaming.
// It supports H.264 video only (no audio), with configurable max peers
// per camera and idle eviction.
type Manager struct {
	mu           sync.RWMutex
	peers        map[string]*peerEntry       // sessionID -> entry
	camPeers     map[string][]string         // camID -> []sessionID
	hubSubs      map[string]*hubSubscription // camID -> subscription info
	stopped      bool
	api          *webrtc.API
	maxPeers     int
	idleTimeout  time.Duration
	frameBufSize int
	drainWg      sync.WaitGroup // tracks RTCP drain goroutines for clean shutdown
	mets         *metrics.Metrics
}

// hubSubscription tracks a StreamHub subscription for a camera.
type hubSubscription struct {
	hub   *model.StreamHub
	subID string
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

// withFrameBufSize sets the per-peer frame buffer size.
func withFrameBufSize(n int) ManagerOption {
	return func(m *Manager) {
		if n > 0 {
			m.frameBufSize = n
		}
	}
}


// WithMetrics sets the Prometheus metrics collector.
func WithMetrics(m *metrics.Metrics) ManagerOption {
	return func(mgr *Manager) {
		mgr.mets = m
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
		hubSubs:      make(map[string]*hubSubscription),
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

// RegisterStream subscribes to the recorder's StreamHub for live frames.
// If hub is nil, this is a no-op. Safe to call multiple times for the same camID.
func (m *Manager) RegisterStream(camID string, hub *model.StreamHub) {
	if hub == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.hubSubs[camID]; ok {
		return // already registered
	}
	subID := "webrtc-" + camID
	_ = hub.Subscribe(subID, func(pts int64, au [][]byte) {
		m.WriteH264(camID, pts, au)
	})
	m.hubSubs[camID] = &hubSubscription{hub: hub, subID: subID}
	logger.Info("WebRTC stream registered", "camera_id", camID)
}

// UnregisterStream unsubscribes from the recorder's StreamHub.
func (m *Manager) UnregisterStream(camID string) {
	m.mu.Lock()
	sub, ok := m.hubSubs[camID]
	if ok {
		delete(m.hubSubs, camID)
	}
	m.mu.Unlock()
	if ok {
		sub.hub.Unsubscribe(sub.subID)
		logger.Info("WebRTC stream unregistered", "camera_id", camID)
	}
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

		isKeyframe := nalutil.IsIDR(au, false)
		traceID := "no-trace"
		if isKeyframe {
			traceID = fmt.Sprintf("%s-%d", camID, pts)
		}

		// Non-blocking send — drop frame if buffer full
		select {
		case entry.frameCh <- model.FrameMsg{PTS: pts, AU: au, IsKeyframe: isKeyframe}:
			slog.Debug("frame_trace",
				"trace_id", traceID,
				"camera_id", camID,
				"stage", "webrtc_recv",
				"is_idr", isKeyframe,
			)
		default:
			slog.Debug("frame_trace",
				"trace_id", traceID,
				"camera_id", camID,
				"stage", "webrtc_drop",
				"is_idr", isKeyframe,
				"session_id", entry.sessionID,
				"queue_depth", len(entry.frameCh),
			)
			dropCount := atomic.AddUint64(&entry.drops, 1)
			entry.congestion.recordDropped()
			if m.mets != nil {
				m.mets.WebRTCFramesDropped.WithLabelValues(camID).Inc()
			}
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
	m.drainWg.Add(1)
	go func() {
		defer m.drainWg.Done()
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
		if m.mets != nil {
			m.mets.WebRTCConnectionStateChanges.WithLabelValues(camID, state.String()).Inc()
		}
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

	// Set local description and wait for ICE gathering to complete (with timeout)
	gatherCtx, gatherCancel := context.WithTimeout(ctx, 10*time.Second)
	defer gatherCancel()
	gatherComplete := webrtc.GatheringCompletePromise(pc)
	if err := pc.SetLocalDescription(answer); err != nil {
		cancel()
		pc.Close()
		return nil, "", err
	}
	select {
	case <-gatherComplete:
	case <-gatherCtx.Done():
		logger.Warn("ICE gathering timed out, proceeding with gathered candidates", "camera_id", camID)
	}

	// Reject audio m-lines in the answer SDP (belt-and-suspenders)
	finalSDP := rejectAudioInSDP(pc.LocalDescription().SDP)

	// Create peer entry
	entry := &peerEntry{
		pc:         pc,
		track:      track,
		sender:     sender,
		cancel:     cancel,
		camID:      camID,
		sessionID:  sid,
		lastUsed:   time.Now(),
		frameCh:    make(chan model.FrameMsg, m.frameBufSize),
		congestion: newCongestionTracker(m.frameBufSize),
	}

	m.peers[sid] = entry
	m.camPeers[camID] = append(m.camPeers[camID], sid)
	if m.mets != nil {
		m.mets.WebRTCActivePeers.WithLabelValues(camID).Set(float64(len(m.camPeers[camID])))
	}

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
	if m.mets != nil {
		if len(m.camPeers[entry.camID]) == 0 {
			m.mets.WebRTCActivePeers.DeleteLabelValues(entry.camID)
		} else {
			m.mets.WebRTCActivePeers.WithLabelValues(entry.camID).Set(float64(len(m.camPeers[entry.camID])))
		}
	}
	if len(m.camPeers[entry.camID]) == 0 {
		delete(m.camPeers, entry.camID)
	}
	camID := entry.camID
	_, hasSub := m.hubSubs[camID]
	m.mu.Unlock()

	if hasSub {
		// Schedule cleanup — don't unsubscribe immediately in case a new peer connects
		go func() {
			time.Sleep(5 * time.Second)
			m.mu.RLock()
			if m.stopped {
				m.mu.RUnlock()
				return
			}
			remaining := len(m.camPeers[camID])
			m.mu.RUnlock()
			if remaining == 0 {
				m.UnregisterStream(camID)
			}
		}()
	}
	// Clean up outside the lock to avoid deadlock with OnConnectionStateChange
	entry.cancel()
	if entry.pc != nil {
		_ = entry.pc.Close()
	}

	logger.Info("WHEP session deleted", "camera_id", entry.camID, "session_id", sessionID)
	return nil
}


// StopAll closes all active WHEP sessions and releases resources.
func (m *Manager) StopAll() {
	m.mu.Lock()
	m.stopped = true
	entries := make([]*peerEntry, 0, len(m.peers))
	for sid, entry := range m.peers {
		entries = append(entries, entry)
		delete(m.peers, sid)
	}
	// Collect hub subscriptions to clean up outside lock
	hubSubs := make(map[string]*hubSubscription)
	for id, sub := range m.hubSubs {
		hubSubs[id] = sub
	}
	m.hubSubs = make(map[string]*hubSubscription)
	m.camPeers = make(map[string][]string)
	m.mu.Unlock()

	// Clean up outside the lock to avoid deadlock with callbacks
	for _, entry := range entries {
		entry.cancel()
		if entry.pc != nil {
			_ = entry.pc.Close()
		}
	}

	// Unsubscribe from all StreamHubs
	for _, sub := range hubSubs {
		sub.hub.Unsubscribe(sub.subID)
	}

	// Wait for all RTCP drain goroutines to exit
	m.drainWg.Wait()
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
			data := annexBEncode(frame.AU)
			if len(data) == 0 {
				continue
			}

			// Congestion detection: skip non-IDR frames if drop rate is high
			if entry.congestion.shouldSkipFrame(frame.IsKeyframe) {
				slog.Debug("webrtc_congestion_skip",
					"session_id", entry.sessionID,
					"is_idr", frame.IsKeyframe,
					"drop_rate", entry.congestion.dropRate())
				continue
			}

			// Calculate duration from PTS delta (90kHz clock)

			entry.mu.Lock()
			var dur time.Duration
			if entry.lastPTS == 0 {
				dur = time.Second / defaultFPS
			} else {
				delta := frame.PTS - entry.lastPTS
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
			entry.lastPTS = frame.PTS
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
			// Record successful send for congestion tracking
			entry.congestion.recordSent()
			if m.mets != nil {
				m.mets.WebRTCFramesSent.WithLabelValues(entry.camID).Inc()
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

// activePeerCount returns the number of active WHEP peers for the given camera.
func (m *Manager) activePeerCount(camID string) int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.camPeers[camID])
}

// totalPeerCount returns the total number of active WHEP sessions across all cameras.
func (m *Manager) totalPeerCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.peers)
}
