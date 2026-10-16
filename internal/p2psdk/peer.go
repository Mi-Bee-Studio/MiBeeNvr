// Package peer provides the MiBee P2P Go SDK for connecting to the signaling
// server, establishing P2P connections via pion/webrtc with ICE/DTLS
// DataChannels, and exchanging data between peers.
//
// Protocol version: 1.0.0 (wire-compatible with the server's internal/protocol v1).
package peer

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/p2psdk/relay"
)

// DataChannelLabel is the label used for DataChannel created by the SDK.
const DataChannelLabel = "p2p-data"

// DataChannelLabelCtrl is the lossy control channel of the #88① dual
// channel direct path: unordered + maxRetransmits=0, carrying tunnel
// lifecycle frames (open/close/e2ee_ack) so a lost control frame costs one
// retry instead of head-of-line blocking behind bulk data retransmissions.
const DataChannelLabelCtrl = "p2p-ctrl"

// Role constants for SignalingConfig.Role. A device accepts inbound tunnels;
// a client initiates outbound connections to a device.
const (
	RoleDevice = "device"
	RoleClient = "client"
)

// IncomingConnect describes an inbound connect_request delivered to a
// device-role OnConnectRequest admission gate.
type IncomingConnect struct {
	// SessionID identifies the proposed P2P session.
	SessionID string
	// TargetPeerID is this device's own peer ID (the requested target).
	TargetPeerID string
	// ServiceName is the tunnel service the requester wants to reach
	// (may be empty when unspecified).
	ServiceName string
}

// Dial connects to the signaling server and returns a Session.
// It blocks until the hello handshake completes successfully.
//
//nolint:funlen // complex setup
func Dial(ctx context.Context, cfg SignalingConfig) (*Session, error) {
	if err := validateConfig(cfg); err != nil {
		return nil, err
	}

	logger := cfg.Logger
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(discardWriter{}, nil))
	}

	sc := newSignalingClient(cfg)

	welcome, err := sc.connect(ctx)
	if err != nil {
		return nil, fmt.Errorf("signaling connect: %w", err)
	}

	s := &Session{
		cfg:     cfg,
		signal:  sc,
		logger:  logger.With("component", "p2p_session"),
		peers:   make(map[string]*p2pSession),
		welcome: welcome,
		closeCh: make(chan struct{}),
		done:    make(chan struct{}),
	}
	s.lastSessionID = welcome.SessionID
	sc.lastSessionID = welcome.SessionID
	// Connect to relay server if enabled.
	if welcome.RelayURL != "" && welcome.RelayPubKey != "" {
		rc := relay.NewClient()
		if err := rc.Connect(ctx, welcome.RelayURL, welcome.RelayPubKey); err != nil {
			logger.Warn("relay connect failed (non-fatal)", "error", err)
		} else {
			s.relayClient = rc
			rc.OnFatal(s.handleRelayFatal)
			logger.Info(
				"relay connection established",
				"relay_url", welcome.RelayURL,
			)
		}
	}

	sc.onMessage = s.handleMessage
	sc.onClose = func(err error) {
		s.logger.Warn("signaling connection lost", "error", err)
		if cfg.OnDisconnect != nil {
			cfg.OnDisconnect(err)
		}
	}
	if cfg.OnReconnect != nil {
		sc.onReconnect = cfg.OnReconnect
	}

	// Start the read loop only after all callbacks are wired, so recvLoop
	// never observes a nil onMessage/onClose (avoids a startup data race).
	sc.start()

	go s.cleanupLoop()
	go s.idleSweepLoop()

	s.logger.Info(
		"session created",
		"session_id", welcome.SessionID,
		"peer_id", welcome.PeerID,
	)
	// NAT 行为观测（#70/#14：只观测不改策略）——welcome 的 STUN 列表
	// 在同一 socket 上复探比对映射，结果供诊断/后续选路决策消费。
	go s.observeNAT(welcome) //nolint:gosec,contextcheck // deliberately detached: observation outlives the request scope
	return s, nil
}

// NatObservation is one completed NAT behavior probe. Observational only:
// the class is the fact base for path decisions (PreferRelay), the probe
// itself never changes transport selection.
type NatObservation struct {
	Class NATClass `json:"class"`
	// DualStack reports a global-unicast IPv6 address on any interface
	// (#89): a v4-symmetric verdict does not rule out v6 direct paths, so
	// relay pinning consumers may exempt dual-stack hosts.
	DualStack     bool      `json:"dual_stack"`
	StunReachable bool      `json:"stun_reachable"`
	Responsive    int       `json:"responsive"` // STUN servers that answered
	LocalAddr     string    `json:"local_addr,omitempty"`
	At            time.Time `json:"at"`
}

// NATObservation returns the latest NAT observation (nil before the first
// probe completes or when welcome carried no STUN servers).
func (s *Session) NATObservation() *NatObservation {
	if v := s.natObs.Load(); v != nil {
		return v
	}
	return nil
}

// sessionConfig returns a locked snapshot of the signaling config (#89).
// Session-construction points (connect request/accept handling, the idle
// sweep) must read cfg through this — SetForceRelay may write concurrently
// once a device updates its relay pin from a NAT observation after Dial.
func (s *Session) sessionConfig() SignalingConfig {
	s.cfgMu.RLock()
	defer s.cfgMu.RUnlock()
	return s.cfg
}

// SetForceRelay dynamically updates the relay pin (#89): the execution
// surface for device-side "auto decides from NAT observation". Affects
// sessions created after the call (running sessions keep the config they
// captured at construction). Safe to call any time after Dial.
func (s *Session) SetForceRelay(v bool) {
	s.cfgMu.Lock()
	s.cfg.ForceRelay = v
	s.cfgMu.Unlock()
}

// observeNAT probes the local NAT behavior via the welcome STUN list.
// Needs ≥2 distinct responsive servers for a class verdict; fewer stays
// NATUnknown (no false verdicts). Failures are logged once and dropped.
func (s *Session) observeNAT(welcome *Welcome) {
	urls := stunURLs(welcome)
	if len(urls) == 0 {
		s.logger.Debug("NAT observation skipped: no STUN servers in welcome")
		return
	}
	servers := make([]string, 0, len(urls))
	for _, u := range urls {
		servers = append(servers, strings.TrimPrefix(u, "stun:"))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	res := DetectNATClass(ctx, servers, 5*time.Second)
	obs := &NatObservation{
		Class:         res.Class,
		DualStack:     res.DualStack,
		StunReachable: len(res.MappedByServer) > 0,
		Responsive:    len(res.MappedByServer),
		LocalAddr:     res.LocalAddr,
		At:            time.Now(),
	}
	s.natObs.Store(obs)
	s.logger.Info("NAT observation",
		"class", string(res.Class),
		"dual_stack", res.DualStack,
		"stun_responsive", obs.Responsive,
		"local_addr", res.LocalAddr,
		"prefer_relay", PreferRelay(res.Class),
	)
	if cb := s.OnNATObservation; cb != nil {
		cb(*obs)
	}
}

// Session represents a P2P session connected to the signaling server.
type Session struct {
	cfg   SignalingConfig
	cfgMu sync.RWMutex // guards cfg: SetForceRelay writes post-Dial while handlers read

	signal *signalingClient
	logger *slog.Logger

	welcome     *Welcome
	relayClient *relay.Client

	// Callbacks (set by the user; OnSession/OnDataChannel/OnReject are read
	// live at event time, so they may be assigned any time before the event
	// fires — including after Connect() returns via SetOnDataChannel)
	OnSession     func()                            // called when a P2P session establishes
	OnDataChannel func(dc DataChannel)              // called when a DataChannel opens
	OnReject      func(code RejectCode, msg string) // called on connect rejection
	OnError       func(code, msg string)            // called on a server-level error message
	OnStats       func(StatsSnapshot)               // called with GetStats snapshots (optional; assign before Connect)

	// OnConnectRequest is an optional admission gate for inbound connection
	// requests (device role). It runs before any WebRTC machinery starts;
	// returning false makes the SDK answer the requester with connect_reject
	// (code device_busy). When nil, every inbound request is accepted (the
	// historical behavior). Read live at event time like the callbacks above.
	OnConnectRequest func(req IncomingConnect) bool

	// OnSessionClose is called when a P2P session is torn down (remote
	// session_close or local abandonment after repeated ICE restart failures).
	OnSessionClose func(sessionID, reason string)

	// OnNATObservation reports the NAT behavior probe outcome (observational
	// only, #70/#14). Fires once per Dial, shortly after connect, when the
	// welcome STUN list allows a probe; never fires otherwise.
	OnNATObservation func(NatObservation)

	// P2P sessions by session ID
	mu            sync.RWMutex
	peers         map[string]*p2pSession
	closeCh       chan struct{}
	closeOnce     sync.Once
	done          chan struct{}
	lastSessionID string

	// natObs carries the latest NAT behavior probe (observeNAT).
	natObs atomic.Pointer[NatObservation]
}

// Connect initiates a connection to a target peer.
// Returns after the connect_request is sent; session establishes
// asynchronously. Use OnSession and OnDataChannel callbacks.
func (s *Session) Connect(_ context.Context, targetPeerID string, serviceName ...string) error {
	sessionID := uuid.New().String()

	p2p := &p2pSession{
		sessionID:    sessionID,
		remotePeerID: targetPeerID,
		isOfferer:    false,
		logger:       s.logger,
		owner:        s,
		onStats:      s.OnStats,
		relayClient:  s.relayClient,
	}
	// 活跃度锚点=创建时刻：从未承载流量的会话（握手即死）也会被
	// 空闲清扫按超时回收，而不是永久滞留名册。
	p2p.touch()

	s.mu.Lock()
	s.peers[sessionID] = p2p
	s.mu.Unlock()

	req := ConnectRequest{
		TargetPeerID: targetPeerID,
		SessionID:    sessionID,
		// #88①: declare dual-channel support so a NEW device-side offerer
		// may open the lossy control channel. Old devices ignore the
		// unknown field and keep the single p2p-data channel — every
		// direction of the version matrix stays wire-compatible.
		CtrlChannel: true,
	}
	if len(serviceName) > 0 {
		req.ServiceName = serviceName[0]
	}
	if err := s.signal.sendRawMsg(MessageTypeConnectRequest, req); err != nil {
		return fmt.Errorf("send connect_request: %w", err)
	}

	s.logger.Info(
		"connect request sent",
		"session_id", sessionID,
		"target", targetPeerID,
	)
	return nil
}

// Close shuts down the session and all connections.
func (s *Session) Close() error {
	var errs []error
	s.closeOnce.Do(func() {
		close(s.closeCh)
		s.mu.RLock()
		for _, p2p := range s.peers {
			if closeErr := p2p.close(); closeErr != nil {
				errs = append(errs, closeErr)
			}
		}
		if s.relayClient != nil {
			err := s.relayClient.Close()
			if err != nil {
				errs = append(errs, err)
			}
		}
		s.signal.close()
	})
	select {
	case <-s.done:
	case <-time.After(5 * time.Second):
	}
	if len(errs) > 0 {
		return fmt.Errorf("close errors: %v", errs)
	}
	return nil
}

func (s *Session) cleanupLoop() {
	<-s.closeCh
	s.mu.RLock()
	for _, p2p := range s.peers {
		_ = p2p.close()
	}
	s.mu.RUnlock()
	close(s.done)
}

// getP2P returns the p2pSession for a sessionID (read lock).
// idleSweepLoop periodically reclaims sessions whose viewer died without
// session_close (#20 relay slot leak). Tick granularity is well below any
// sane idle timeout; a disabled (negative) config turns this into a no-op.
func (s *Session) idleSweepLoop() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-s.closeCh:
			return
		case <-ticker.C:
			s.sweepIdleSessions()
		}
	}
}

// sweepIdleSessions tears down sessions with no user-facing DataChannel
// traffic for longer than the configured idle timeout. Teardown runs outside
// the session lock (teardownWithReason re-locks internally).
func (s *Session) sweepIdleSessions() {
	timeout := s.sessionConfig().sessionIdleTimeout()
	if timeout < 0 {
		return
	}
	cutoff := time.Now().Add(-timeout)
	var stale []string
	s.mu.RLock()
	for id, p := range s.peers {
		if ts := p.lastActivity.Load(); !tsBeforeEq(ts, cutoff) {
			continue
		}
		stale = append(stale, id)
	}
	s.mu.RUnlock()
	for _, id := range stale {
		if p := s.getP2P(id); p != nil {
			p.log().Info("session idle, reclaiming", "session_id", id,
				"idle_timeout", timeout.String())
			p.teardownWithReason("session_idle")
		}
	}
}

// tsBeforeEq reports whether the unix-nano timestamp is at or before t
// (zero timestamps — never-touched sessions from before the field existed —
// count as stale).
func tsBeforeEq(tsNano int64, t time.Time) bool {
	if tsNano <= 0 {
		return true
	}
	return time.Unix(0, tsNano).Before(t) || time.Unix(0, tsNano).Equal(t)
}

func (s *Session) getP2P(sessionID string) *p2pSession {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.peers[sessionID]
}

// handleRelayFatal reacts to the session-level relay WebSocket dying (read
// timeout / pong starvation / write failure — relay.Client liveness). Per
// the relay contract ("relay disconnect → session-level complete rebuild"):
// every session that had adopted relay is torn down so its peer redials
// fresh, and the shared relay client itself is rebuilt so the NEXT adoption
// isn't handed a dead socket (2026-10-08 production: relay black-holed mid-
// test-day, both peers "on relay", zero data flowing, NVR restart was the
// only recovery).
func (s *Session) handleRelayFatal(err error) {
	select {
	case <-s.closeCh:
		return // Session.Close() owns teardown
	default:
	}
	s.logger.Warn("relay connection fatal — rebuilding", "error", err)

	// 1) Tear down sessions riding the dead transport: signaling
	// session_close makes the peer redial immediately instead of timing out
	// against silence; the OnSessionClose callback surfaces to the embedder.
	s.mu.RLock()
	victims := make([]*p2pSession, 0, len(s.peers))
	for _, p2p := range s.peers {
		if p2p.relayActiveSnapshot() {
			victims = append(victims, p2p)
		}
	}
	s.mu.RUnlock()
	for _, p2p := range victims {
		p2p.teardownWithReason("relay_transport_dead")
	}

	// 2) Rebuild the shared relay client in the background (bounded).
	go s.rebuildRelayClient()
}

// rebuildRelayClient reconnects the session-level relay transport with
// bounded retries. A fresh relay.Client is used each attempt (the previous
// one is permanently failed — fatalOnce is spent), matching the
// full-rebuild contract. New sessions snapshot the new client at creation.
func (s *Session) rebuildRelayClient() {
	w := s.welcome
	if w == nil || w.RelayURL == "" {
		return
	}
	for attempt := 1; attempt <= 3; attempt++ {
		select {
		case <-s.closeCh:
			return
		case <-time.After(time.Duration(attempt) * 5 * time.Second):
		}
		rc := relay.NewClient()
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		err := rc.Connect(ctx, w.RelayURL, w.RelayPubKey)
		cancel()
		if err != nil {
			s.logger.Warn("relay rebuild attempt failed",
				"attempt", attempt, "error", err)
			continue
		}
		rc.OnFatal(s.handleRelayFatal)
		s.mu.Lock()
		s.relayClient = rc
		s.mu.Unlock()
		s.logger.Info("relay connection rebuilt", "attempt", attempt)
		return
	}
	s.logger.Warn("relay rebuild gave up after 3 attempts")
}

// Live callback accessors: user callbacks are read under the session lock at
// event time rather than snapshotted at Connect(), so assigning them after
// Connect() (but before the DataChannel opens) still takes effect. To stay
// race-free, either assign before Connect() (no concurrent reader exists yet)
// or use SetOnDataChannel — direct field assignment once a session is in
// flight races with event dispatch.
func (s *Session) dataChannelCallback() func(DataChannel) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.OnDataChannel
}

// SetOnDataChannel assigns the DataChannel callback under the session lock.
// Use it when assigning after Connect() has already been called; assigning
// the field directly is only race-free before any session is in flight.
func (s *Session) SetOnDataChannel(f func(DataChannel)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.OnDataChannel = f
}

// SetOnConnectRequest assigns the admission gate under the session lock
// (race-free post-Dial assignment, mirroring SetOnDataChannel).
func (s *Session) SetOnConnectRequest(f func(IncomingConnect) bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.OnConnectRequest = f
}

func (s *Session) sessionCallback() func() {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.OnSession
}

func (s *Session) rejectCallback() func(RejectCode, string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.OnReject
}

// connectRequestCallback returns the live OnConnectRequest admission gate.
func (s *Session) connectRequestCallback() func(IncomingConnect) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.OnConnectRequest
}

// ActiveSessions reports the number of P2P sessions currently tracked by
// this Session (diagnostics / capacity accounting; includes sessions still
// negotiating).
func (s *Session) ActiveSessions() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.peers)
}

// sessionCloseCallback returns the live OnSessionClose callback.
func (s *Session) sessionCloseCallback() func(sessionID, reason string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.OnSessionClose
}

// deleteP2P removes a p2pSession by sessionID and returns it.
func (s *Session) deleteP2P(sessionID string) *p2pSession {
	s.mu.Lock()
	defer s.mu.Unlock()
	p2p := s.peers[sessionID]
	delete(s.peers, sessionID)
	return p2p
}

// validateConfig checks required SignalingConfig fields and returns a
// ErrInvalidConfig-wrapped error describing the first problem found.
func validateConfig(cfg SignalingConfig) error {
	switch {
	case cfg.ServerURL == "":
		return fmt.Errorf("signaling server URL is required: %w", ErrInvalidConfig)
	case cfg.JWT == "" && cfg.TokenFunc == nil:
		return fmt.Errorf("JWT is required: %w", ErrInvalidConfig)
	case cfg.ProductID == "":
		return fmt.Errorf("product ID is required: %w", ErrInvalidConfig)
	case cfg.PeerID == "":
		return fmt.Errorf("peer ID is required: %w", ErrInvalidConfig)
	case cfg.Role == "":
		return fmt.Errorf("role is required: %w", ErrInvalidConfig)
	case cfg.Role != RoleDevice && cfg.Role != RoleClient:
		return fmt.Errorf("role must be %q or %q: %w", RoleDevice, RoleClient, ErrInvalidConfig)
	}
	return nil
}
