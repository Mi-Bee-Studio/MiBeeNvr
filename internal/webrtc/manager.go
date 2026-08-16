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
	defaultAudioBufSize   = 50
	defaultFPS            = 30
	h264ClockRate         = 90000
	g711ClockRate         = 8000
	opusClockRate         = 48000
	highDropRateThreshold = 0.20                  // 20% — trigger frame skipping
	lowDropRateThreshold  = 0.05                  // 5% — restore full frame rate
	defaultAudioFrameDur  = 20 * time.Millisecond // typical G.711/Opus frame
)

// peerEntry holds a single WHEP peer connection and its metadata.
type peerEntry struct {
	mu           sync.Mutex
	pc           *webrtc.PeerConnection
	track        *webrtc.TrackLocalStaticSample
	sender       *webrtc.RTPSender
	audioTrack   *webrtc.TrackLocalStaticSample // nil for video-only peers
	audioCh      chan model.AudioFrame          // nil for video-only peers
	audioClock   int                            // RTP clock rate for audio PTS→duration
	lastAudioPTS int64
	cancel       context.CancelFunc
	camID        string
	sessionID    string
	lastUsed     time.Time
	frameCh      chan model.FrameMsg
	drops        uint64             // atomic: total frames dropped due to buffer full
	congestion   *congestionTracker // tracks drop rate for bitrate adaptation
	lastPTS      int64
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
	for i := range ct.windowCount {
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
// H.264 video with optional G.711 (PCMU/PCMA) or Opus audio — the WebRTC
// mandatory codecs, passed through with zero transcoding. AAC cameras stay
// video-only (AAC is not a WebRTC codec; those keep the separate audio WS).
type Manager struct {
	mu            sync.RWMutex
	peers         map[string]*peerEntry       // sessionID -> entry
	camPeers      map[string][]string         // camID -> []sessionID
	hubSubs       map[string]*hubSubscription // camID -> subscription info
	streamProfile map[string]string           // cameraID → H.264 fmtp variant (fmtpForProfile)
	audioCfgs     map[string]audioConfig      // cameraID → negotiated audio (absent = video-only)
	stopped       bool
	api           *webrtc.API
	maxPeers      int
	idleTimeout   time.Duration
	frameBufSize  int
	audioBufSize  int
	iceServers    []webrtc.ICEServer // STUN/TURN servers for cross-network ICE; nil = LAN-only
	drainWg       sync.WaitGroup     // tracks RTCP drain goroutines for clean shutdown
	mets          *metrics.Metrics
}

// audioConfig is the per-camera audio wire format for WHEP tracks (#372).
type audioConfig struct {
	codec     string // "pcmu", "pcma", "opus"
	clockRate int    // RTP clock: 8000 (G.711) / 48000 (Opus)
	sampleHz  int    // source sample rate (informational)
	channels  int
}

// audioCapability maps a negotiated audio config to the pion track capability.
func (a audioConfig) capability() webrtc.RTPCodecCapability {
	codecCap := webrtc.RTPCodecCapability{ClockRate: uint32(a.clockRate)}
	switch a.codec {
	case "pcmu":
		codecCap.MimeType = webrtc.MimeTypePCMU
	case "pcma":
		codecCap.MimeType = webrtc.MimeTypePCMA
	case "opus":
		codecCap.MimeType = webrtc.MimeTypeOpus
		// Matches the conventional browser-offered Opus fmtp; pion requires
		// the capability to be one of the registered variants.
		codecCap.SDPFmtpLine = "minptime=10;useinbandfec=1"
	}
	return codecCap
}

// hubSubscription tracks a StreamHub subscription for a camera.
type hubSubscription struct {
	hub             *model.StreamHub
	subID           string
	audioSubscribed bool // SetAudioInfo ran against this hub instance
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

// WithICEServers sets STUN/TURN servers used when creating PeerConnections.
// This enables cross-network (WAN/4G/remote WiFi) WHEP access. Leave empty for
// LAN-only deployments (preserves the legacy behavior).
func WithICEServers(servers []webrtc.ICEServer) ManagerOption {
	return func(m *Manager) {
		m.iceServers = servers
	}
}

// NewManager creates a new WebRTC Manager with H.264 video-only support.
func NewManager(opts ...ManagerOption) *Manager {
	mediaEngine := &webrtc.MediaEngine{}
	// Register the common H.264 profiles (the same set go2rtc/MediaMTX
	// offer): browsers answer with the variant they can decode, and the
	// per-camera track is created with the variant matching the ACTUAL
	// stream profile. Registering only Constrained Baseline (42001f)
	// black-screened every High-profile camera (e.g. GB28181 cascades,
	// profile 0x64 level 4.0): the answer locked the decoder to Baseline
	// and every frame was silently rejected.
	h264Profiles := []string{
		"level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42001f",
		"level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42e01f",
		"level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=4d001f",
		"level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=640028",
		"level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=640c1f",
	}
	for i, fmtp := range h264Profiles {
		if err := mediaEngine.RegisterCodec(webrtc.RTPCodecParameters{
			RTPCodecCapability: webrtc.RTPCodecCapability{
				MimeType:    webrtc.MimeTypeH264,
				ClockRate:   h264ClockRate,
				SDPFmtpLine: fmtp,
			},
			PayloadType: webrtc.PayloadType(96 + i),
		}, webrtc.RTPCodecTypeVideo); err != nil {
			logger.Error("failed to register H264 codec", "fmtp", fmtp, "error", err)
		}
	}

	// Audio codecs for WHEP muxing (#372) — all WebRTC mandatory codecs, so
	// the NVR passes raw hub bytes through with zero transcoding. Static
	// payload types for G.711, the conventional 111 for Opus. Opus must be
	// registered as stereo (2 channels) per RFC 7587 / browser convention.
	audioCodecs := []struct {
		cap webrtc.RTPCodecCapability
		pt  webrtc.PayloadType
	}{
		{webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypePCMU, ClockRate: g711ClockRate, Channels: 1}, 0},
		{webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypePCMA, ClockRate: g711ClockRate, Channels: 1}, 8},
		{webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: opusClockRate, Channels: 2}, 111},
	}
	for _, ac := range audioCodecs {
		if err := mediaEngine.RegisterCodec(webrtc.RTPCodecParameters{
			RTPCodecCapability: ac.cap,
			PayloadType:        ac.pt,
		}, webrtc.RTPCodecTypeAudio); err != nil {
			logger.Error("failed to register audio codec", "mime", ac.cap.MimeType, "error", err)
		}
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
		peers:         make(map[string]*peerEntry),
		camPeers:      make(map[string][]string),
		hubSubs:       make(map[string]*hubSubscription),
		streamProfile: make(map[string]string),
		audioCfgs:     make(map[string]audioConfig),
		api:           api,
		maxPeers:      defaultMaxPeers,
		idleTimeout:   defaultIdleTimeout,
		frameBufSize:  defaultFrameBufSize,
		audioBufSize:  defaultAudioBufSize,
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
// sps (optional, the recorder's codec params) selects the H.264 profile
// variant offered for this camera's track — see NewManager's codec list.
func (m *Manager) RegisterStream(camID string, hub *model.StreamHub, sps []byte) {
	if hub == nil {
		return
	}
	m.mu.Lock()
	m.streamProfile[camID] = fmtpForProfile(sps)
	if sub, ok := m.hubSubs[camID]; ok {
		if sub.hub == hub {
			m.mu.Unlock()
			return // already registered on this hub
		}
		m.mu.Unlock()
		// The GB session was recycled (or the recorder restarted) and handed
		// out a NEW hub — resubscribe, or every viewer stays fed by the dead
		// one (frozen video until a page reload re-registers).
		sub.hub.Unsubscribe(sub.subID)
		m.mu.Lock()
	}
	subID := "webrtc-" + camID
	_ = hub.Subscribe(subID, func(pts int64, au [][]byte) {
		m.WriteH264(camID, pts, au)
	})
	m.hubSubs[camID] = &hubSubscription{hub: hub, subID: subID}
	m.mu.Unlock()
	logger.Info("WebRTC stream registered", "camera_id", camID)
}

// SetAudioInfo configures the audio track offered for a camera's WHEP
// sessions (#372). Must be called after RegisterStream. codec is the
// recorder's audio codec ("g711", "opus", ...); muLaw selects PCMU vs PCMA
// for G.711. AAC is not a WebRTC codec — it returns without configuring
// (those cameras keep the separate audio-WS path).
func (m *Manager) SetAudioInfo(camID string, codec string, muLaw bool, sampleRate, channels int) error {
	var cfg audioConfig
	switch codec {
	case "g711":
		cfg = audioConfig{codec: "pcmu", clockRate: g711ClockRate, sampleHz: sampleRate, channels: 1}
		if !muLaw {
			cfg.codec = "pcma"
		}
	case "opus":
		cfg = audioConfig{codec: "opus", clockRate: opusClockRate, sampleHz: sampleRate, channels: channels}
	default:
		// AAC and unknown codecs: leave video-only. No error — callers treat
		// audio setup as best-effort.
		return nil
	}

	m.mu.Lock()
	m.audioCfgs[camID] = cfg
	sub, hasSub := m.hubSubs[camID]
	needSubscribe := hasSub && sub != nil && !sub.audioSubscribed
	if needSubscribe {
		sub.audioSubscribed = true
	}
	m.mu.Unlock()
	if !hasSub {
		return fmt.Errorf("webrtc: SetAudioInfo before RegisterStream (camera %s)", camID)
	}
	if !needSubscribe {
		return nil // already subscribed on this hub (codec update only)
	}

	// Idempotent: the subscription fan-outs to every peer entry, so a single
	// audio subscription per camera is enough regardless of peer count.
	audioSubID := "webrtc-audio-" + camID
	if err := sub.hub.SubscribeAudio(audioSubID, func(pts int64, audioCodec model.AudioCodec, data []byte) {
		m.WriteAudio(camID, pts, data)
	}); err != nil {
		return fmt.Errorf("webrtc: subscribe audio: %w", err)
	}
	logger.Info("WebRTC audio configured", "camera_id", camID, "codec", cfg.codec, "sample_rate", sampleRate, "channels", cfg.channels)
	return nil
}

// fmtpForProfile maps a recorder SPS to the H.264 SDP fmtp line matching
// its profile: High-family (profile_idc 100/110/122/244) offers 640028,
// everything else stays on Constrained Baseline 42001f. The variant must be
// one of the codecs registered in NewManager (answers bind by exact fmtp).
func fmtpForProfile(sps []byte) string {
	const base = "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id="
	if len(sps) >= 4 {
		switch sps[1] {
		case 100, 110, 122, 244:
			return base + "640028"
		}
	}
	return base + "42001f"
}

// UnregisterStream unsubscribes from the recorder's StreamHub.
func (m *Manager) UnregisterStream(camID string) {
	m.mu.Lock()
	sub, ok := m.hubSubs[camID]
	if ok {
		delete(m.hubSubs, camID)
	}
	delete(m.audioCfgs, camID)
	m.mu.Unlock()
	if ok {
		sub.hub.Unsubscribe(sub.subID)
		sub.hub.UnsubscribeAudio("webrtc-audio-" + camID)
		logger.Info("WebRTC stream unregistered", "camera_id", camID)
	}
}

// WriteAudio queues an audio frame for async writing to all WHEP peers of
// the given camera. Non-blocking — frames are dropped if a peer's audio
// buffer is full (audio glitches are preferable to stalling the pipeline).
func (m *Manager) WriteAudio(camID string, pts int64, data []byte) {
	m.mu.RLock()
	sids := m.camPeers[camID]
	entries := make([]*peerEntry, 0, len(sids))
	for _, sid := range sids {
		if e, ok := m.peers[sid]; ok && e.audioCh != nil {
			entries = append(entries, e)
		}
	}
	m.mu.RUnlock()

	for _, entry := range entries {
		select {
		case entry.audioCh <- model.AudioFrame{PTS: pts, Data: data}:
		default:
			// Audio buffer full — drop this frame.
		}
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
			slog.Debug(
				"frame_trace",
				"trace_id", traceID,
				"camera_id", camID,
				"stage", "webrtc_recv",
				"is_idr", isKeyframe,
			)
		default:
			slog.Debug(
				"frame_trace",
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

	// Create PeerConnection via shared API factory. ICEServers is populated from
	// config when configured for cross-network access; empty (nil) preserves the
	// legacy LAN-only behavior.
	pc, err := m.api.NewPeerConnection(webrtc.Configuration{
		ICEServers: m.iceServers,
	})
	if err != nil {
		return nil, "", err
	}

	// Create H.264 video track. The SDP fmtp mirrors the camera's actual
	// stream profile so browsers negotiate a decoder that accepts it.
	// (CreateWHEPSession already holds m.mu — do not re-lock.)
	profileFmtp := m.streamProfile[camID]
	if profileFmtp == "" {
		profileFmtp = "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42001f"
	}
	track, err := webrtc.NewTrackLocalStaticSample(
		webrtc.RTPCodecCapability{
			MimeType:    webrtc.MimeTypeH264,
			ClockRate:   h264ClockRate,
			SDPFmtpLine: profileFmtp,
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

	// Audio track (#372): added only when the camera negotiated a
	// WebRTC-compatible audio codec. Video-only cameras leave the browser's
	// audio m-line rejected (pion answers port 0 automatically — no track,
	// no transceiver).
	var audioTrack *webrtc.TrackLocalStaticSample
	var audioSender *webrtc.RTPSender
	var audioClock int
	if cfg, ok := m.audioCfgs[camID]; ok {
		audioTrack, err = webrtc.NewTrackLocalStaticSample(cfg.capability(), "audio", camID)
		if err != nil {
			pc.Close()
			return nil, "", err
		}
		audioSender, err = pc.AddTrack(audioTrack)
		if err != nil {
			pc.Close()
			return nil, "", err
		}
		audioClock = cfg.clockRate
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

	// Separate RTCP drain for the audio sender (if present).
	if audioSender != nil {
		m.drainWg.Add(1)
		go func() {
			defer m.drainWg.Done()
			defer func() {
				if r := recover(); r != nil {
					logger.Warn("audio RTCP drain goroutine panic recovered", "error", r)
				}
			}()
			buf := make([]byte, 1500)
			for {
				select {
				case <-ctx.Done():
					return
				default:
				}
				if _, _, readErr := audioSender.Read(buf); readErr != nil {
					return
				}
			}
		}()
	}

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

	// Create peer entry
	entry := &peerEntry{
		pc:         pc,
		track:      track,
		sender:     sender,
		audioTrack: audioTrack,
		audioClock: audioClock,
		cancel:     cancel,
		camID:      camID,
		sessionID:  sid,
		lastUsed:   time.Now(),
		frameCh:    make(chan model.FrameMsg, m.frameBufSize),
		congestion: newCongestionTracker(m.frameBufSize),
	}
	if audioTrack != nil {
		entry.audioCh = make(chan model.AudioFrame, m.audioBufSize)
	}

	m.peers[sid] = entry
	m.camPeers[camID] = append(m.camPeers[camID], sid)
	if m.mets != nil {
		m.mets.WebRTCActivePeers.WithLabelValues(camID).Set(float64(len(m.camPeers[camID])))
	}

	// Start async frame writer goroutine
	go m.writeLoop(ctx, entry)

	// Start async audio writer goroutine (no-op for video-only peers)
	if audioTrack != nil {
		go m.audioWriteLoop(ctx, entry)
	}

	// Start idle watchdog goroutine
	go m.idleWatchdog(ctx, entry)

	logger.Info("WHEP session created", "camera_id", camID, "session_id", sid, "audio", audioTrack != nil)
	if audioTrack == nil {
		// Video-only peer: pion leaves the offered audio m-line answerable
		// (port 9 + engine codecs) even without a track — reject it
		// explicitly so browsers don't wait for audio that never comes.
		return []byte(rejectAudioInSDP(pc.LocalDescription().SDP)), sid, nil
	}
	return []byte(pc.LocalDescription().SDP), sid, nil
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
	for camID, sub := range hubSubs {
		sub.hub.Unsubscribe(sub.subID)
		sub.hub.UnsubscribeAudio("webrtc-audio-" + camID)
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

// audioWriteLoop drains audio frames and writes them to the audio track.
// Duration comes from the PTS delta on the codec's RTP clock with a 20ms
// fallback (typical G.711/Opus frame length).
func (m *Manager) audioWriteLoop(ctx context.Context, entry *peerEntry) {
	defer func() {
		if r := recover(); r != nil {
			logger.Warn("WebRTC audioWriteLoop panic recovered",
				"session_id", entry.sessionID, "error", r)
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return
		case frame := <-entry.audioCh:
			entry.mu.Lock()
			var dur time.Duration
			if entry.lastAudioPTS == 0 {
				dur = defaultAudioFrameDur
			} else {
				delta := frame.PTS - entry.lastAudioPTS
				if delta > 0 {
					dur = time.Duration(delta) * time.Second / time.Duration(entry.audioClock)
					if dur > time.Second {
						dur = defaultAudioFrameDur
					}
				} else {
					dur = defaultAudioFrameDur
				}
			}
			entry.lastAudioPTS = frame.PTS
			entry.mu.Unlock()
			if dur < time.Millisecond {
				dur = time.Millisecond
			}

			if err := entry.audioTrack.WriteSample(media.Sample{
				Data:     frame.Data,
				Duration: dur,
			}); err != nil {
				// Non-fatal: log and continue (never crash the goroutine)
				if !strings.Contains(err.Error(), "use of closed") {
					logger.Warn("WebRTC audio write sample error",
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
