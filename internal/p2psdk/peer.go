// Package peer provides the MiBee P2P Go SDK for connecting to the signaling
// server, establishing P2P connections via pion/webrtc with ICE/DTLS
// DataChannels, and exchanging data between peers.
//
// Protocol version: 1.0.0 (wire-compatible with the server's internal/protocol v1).
package peer

import (
	"context"
	crand "crypto/rand"
	"encoding/binary"
	"encoding/hex"
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
	// Purpose is the requester's declared session purpose (#136, additive
	// JSON — empty from old peers). "playback" marks a playback session so
	// purpose-aware gates can apply product policy.
	Purpose string
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
		logger:  logger.With(componentKey, "p2p_session"),
		peers:   make(map[string]*p2pSession),
		welcome: welcome,
		closeCh: make(chan struct{}),
		done:    make(chan struct{}),
	}
	// #162: the session's relay IDENTITY is minted now — before any dial —
	// so the key announced in connect_request/sdp_* exists even when the
	// relay dial is rejected (ticket gate, capacity) or still in flight.
	// Announcement no longer depends on a successful dial.
	identityPub, identityPriv := relay.GenerateKeyPair()
	s.relayIdentity = &identityKeypair{pub: identityPub, priv: identityPriv}
	s.lastSessionID = welcome.SessionID
	sc.lastSessionID = welcome.SessionID
	// Mid-life TURN credential renewal (#132): long-lived sessions must
	// swap credentials before expiry or the relay fallback dies.
	s.scheduleICERefresh()
	s.dialRelay(ctx, welcome, logger)

	sc.onMessage = s.handleMessage
	sc.onClose = func(err error) {
		s.logger.WarnContext(ctx, "signaling connection lost", errorKey, err)
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

	s.logger.InfoContext(ctx,
		"session created",
		sessionIDKey, welcome.SessionID,
		peerIDKey, welcome.PeerID,
	)
	// NAT 行为观测（#70/#14：只观测不改策略）——welcome 的 STUN 列表
	// 在同一 socket 上复探比对映射，结果供诊断/后续选路决策消费。
	go s.observeNAT(welcome) //nolint:gosec,contextcheck // detached: observation outlives request scope
	return s, nil
}

// dialRelay connects the relay fallback transport when the Welcome grants
// one. Failure is non-fatal — direct ICE still works, and a later re-welcome
// retries the dial. Presents the #143 session ticket when the server minted
// one; ticket-less servers ignore the auth suffix.
func (s *Session) dialRelay(ctx context.Context, welcome *Welcome, logger *slog.Logger) {
	if welcome.RelayURL == "" || welcome.RelayPubKey == "" {
		return
	}
	// Host circuit breaking: a gated attempt never happens, so it must not
	// report an outcome either (an attempt that never happened cannot fail).
	if cfg := s.sessionConfig(); cfg.RelayDialGate != nil && !cfg.RelayDialGate() {
		logger.InfoContext(ctx, "relay connect skipped by host gate (circuit open)")
		return
	}
	// #162: build the client on the session identity, not a fresh keypair.
	rc := s.newIdentityClient()
	// #143: present the signaling-minted session ticket when the server
	// issued one; ticket-less servers ignore the suffix.
	if tb, terr := relay.TicketFromBase64(welcome.RelayTicket); terr == nil && len(tb) > 0 {
		rc.SetTicket(tb)
	} else if terr != nil {
		logger.WarnContext(ctx, "relay ticket decode failed (connecting without)", errorKey, terr)
	}
	connectErr := rc.Connect(ctx, welcome.RelayURL, welcome.RelayPubKey)
	if cfg := s.sessionConfig(); cfg.OnRelayConnect != nil {
		cfg.OnRelayConnect(connectErr)
	}
	if connectErr != nil {
		logger.WarnContext(ctx, "relay connect failed (non-fatal)", errorKey, connectErr)
		return
	}
	s.relayClient = rc
	// #162: the hooks are bound to THIS client — a fatal from a client that
	// has already been superseded must not trigger another rebuild.
	// contextcheck anchors at this dispatch line: the fatal callback fires
	// from the relay read loop, which outlives Dial's ctx, so recovery
	// cannot inherit it.
	rc.OnFatal(s.relayFatalHook(rc)) //nolint:contextcheck // detached session-scoped recovery
	rc.OnPeerGone(s.relayPeerGoneHook(rc))
	logger.InfoContext(ctx,
		"relay connection established",
		relayURLKey, welcome.RelayURL,
	)
}

// NatObservation is one completed NAT behavior probe. Observational only:
// the class is the fact base for path decisions (PreferRelay), the probe
// itself never changes transport selection.
type NatObservation struct {
	At         time.Time `json:"at"`
	Class      NATClass  `json:"class"`
	LocalAddr  string    `json:"local_addr,omitempty"`
	Responsive int       `json:"responsive"` // STUN servers that answered
	// DualStack reports a global-unicast IPv6 address on any interface
	// (#89): a v4-symmetric verdict does not rule out v6 direct paths, so
	// relay pinning consumers may exempt dual-stack hosts.
	DualStack     bool `json:"dual_stack"`
	StunReachable bool `json:"stun_reachable"`
}

// NATObservation returns the latest NAT observation (nil before the first
// probe completes or when welcome carried no STUN servers).
func (s *Session) NATObservation() *NatObservation {
	return s.natObs.Load()
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
		classKey, string(res.Class),
		dualStackKey, res.DualStack,
		stunResponsiveKey, obs.Responsive,
		localAddrKey, res.LocalAddr,
		preferRelayKey, PreferRelay(res.Class),
	)
	if cb := s.OnNATObservation; cb != nil {
		cb(*obs)
	}
}

// identityKeypair is the session's relay identity: the key announced to
// peers (connect_request/sdp_*) and presented to the relay server. It is
// minted at session creation and never re-minted, so the announced key and
// the routing key stay identical across relay rebuilds and re-pointing
// (#162).
type identityKeypair struct {
	pub  [32]byte
	priv [32]byte
}

// Session represents a P2P session connected to the signaling server.
type Session struct {
	OnStats func(StatsSnapshot) // called with GetStats snapshots (optional; assign before Connect)
	// OnPathChange fires when the nominated ICE pair changes within a session
	// (sessionID, fromPair, toPair; pair = localType/addr → remoteType/addr).
	// Additive and independent of telemetry gating. Precision = OnStats tick
	// (5s). Assign before Connect.
	OnPathChange func(sessionID, from, to string)
	// OnConnectRequest is an optional admission gate for inbound connection
	// requests (device role). It runs before any WebRTC machinery starts;
	// returning false makes the SDK answer the requester with connect_reject
	// (code device_busy). When nil, every inbound request is accepted (the
	// historical behavior). Read live at event time like the callbacks above.
	OnConnectRequest func(req IncomingConnect) bool
	signal           *signalingClient
	logger           *slog.Logger
	welcome          *Welcome
	relayClient      *relay.Client
	// relayIdentity is the session's stable relay identity (#162), minted
	// in Dial before any relay dial. Every relay client this session builds
	// (initial dial, rebuild, federated re-point) reuses this keypair, so
	// the key announced to peers never drifts.
	//
	// Held by pointer: the keypair is 64 bytes of immutable data, and
	// inlining it would grow Session past its allocator size class.
	relayIdentity *identityKeypair
	// iceRefreshTimer drives the #132 mid-life TURN credential renewal;
	// re-armed from every refreshed ICE list.
	iceRefreshTimer *time.Timer
	// relayTicketWait is the outstanding relay_ticket request (#143): where
	// the rebuild ladder is blocked waiting for its answer. Nil when none is
	// in flight — the ladder is single-flight (#162), so one slot suffices.
	// Guarded by mu.
	relayTicketWait *relayTicketWait
	// Callbacks (set by the user; OnSession/OnDataChannel/OnReject are read
	// live at event time, so they may be assigned any time before the event
	// fires — including after Connect() returns via SetOnDataChannel)
	OnSession     func()                            // called when a P2P session establishes
	OnDataChannel func(dc DataChannel)              // called when a DataChannel opens
	OnReject      func(code RejectCode, msg string) // called on connect rejection
	OnError       func(code, msg string)            // called on a server-level error message
	// OnLinkDegraded fires when the fast-switch monitor (#134⑤) escalates a
	// degraded session: two consecutive windows with loss above threshold
	// (app-fed via ReportLinkQuality) or an ICE RTT spike. cause is "loss"
	// or "rtt". Observational — the SDK already escalates (offerer restarts
	// ICE); embedders use it for UI quality hints.
	OnLinkDegraded func(sessionID, cause string)
	// OnPlaybackDegraded fires when a playback-purpose session violates the
	// tunnel-only policy (#136): no direct path exists (relay/TURN would
	// carry the media). reason is a PlaybackDegraded* constant. UI semantics:
	// show an explicit "当前网络环境不支持远程回放" notice — never a silent
	// fallback. Followed by session teardown (reason playback_tunnel_only).
	OnPlaybackDegraded func(sessionID, reason string)
	// natObs carries the latest NAT behavior probe (observeNAT).
	natObs atomic.Pointer[NatObservation]
	// OnSessionClose is called when a P2P session is torn down (remote
	// session_close or local abandonment after repeated ICE restart failures).
	OnSessionClose func(sessionID, reason string)
	done           chan struct{}
	// OnNATObservation reports the NAT behavior probe outcome (observational
	// only, #70/#14). Fires once per Dial, shortly after connect, when the
	// welcome STUN list allows a probe; never fires otherwise.
	OnNATObservation func(NatObservation)
	closeCh          chan struct{}
	peers            map[string]*p2pSession
	lastSessionID    string
	cfg              SignalingConfig
	mu               sync.RWMutex
	cfgMu            sync.RWMutex
	closeOnce        sync.Once
	// relayRebuilding is the rebuild single-flight guard (#162). Two
	// concurrent fatal callbacks (a dying old client and the new one that
	// displaced it) used to race into parallel rebuilds that dialed with
	// the same identity and displaced each other — an endless
	// rebuild/takeover loop at ~5s per cycle. Placed last: a 4-byte atomic
	// between pointers would force 4 bytes of padding.
	relayRebuilding atomic.Bool
}

// Connect initiates a connection to a target peer.
// Returns after the connect_request is sent; session establishes
// asynchronously. Use OnSession and OnDataChannel callbacks.
func (s *Session) Connect(ctx context.Context, targetPeerID string, serviceName ...string) error {
	var opts ConnectOptions
	if len(serviceName) > 0 {
		opts.ServiceName = serviceName[0]
	}
	return s.ConnectWith(ctx, targetPeerID, opts)
}

// ConnectOptions declares the dial parameters of ConnectWith.
type ConnectOptions struct {
	// ServiceName is the tunnel service the client wants to reach.
	ServiceName string
	// Purpose declares the session's business purpose (#136). PurposePlayback
	// puts the session under the PlaybackTunnelOnly policy when that switch
	// is on: the session may only ride a direct pair, otherwise it degrades
	// with an explicit OnPlaybackDegraded notice. Default (zero value) keeps
	// the full degradation chain.
	Purpose SessionPurpose
}

// ConnectWith initiates a connection with explicit dial options (the
// variadic serviceName flavor of Connect stays for compatibility).
func (s *Session) ConnectWith(ctx context.Context, targetPeerID string, opts ConnectOptions) error {
	sessionID := uuid.New().String()

	p2p := &p2pSession{
		sessionID:    sessionID,
		remotePeerID: targetPeerID,
		isOfferer:    false,
		logger:       s.logger,
		owner:        s,
		onStats:      s.OnStats,
		relayClient:  s.relayClient,
		purpose:      opts.Purpose,
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
		// #136: declare the session purpose (additive JSON — old devices
		// ignore it). The device side threads it into its own session for
		// symmetric policy enforcement.
		Purpose: string(opts.Purpose),
	}
	req.ServiceName = opts.ServiceName
	// Arm direct-path E2EE upfront (the relay-identity gap): the device creates the
	// DataChannel before the SDP exchange can carry our relay identity, so
	// hand it over with the request. #162: announce the SESSION identity,
	// which exists independently of any live relay connection — a rejected
	// or in-flight dial must not leave the peer without our key.
	callerPub := s.relayIdentity.pub
	req.CallerRelayPubKey = hex.EncodeToString(callerPub[:])
	if err := s.signal.sendRawMsg(MessageTypeConnectRequest, req); err != nil {
		return fmt.Errorf("send connect_request: %w", err)
	}

	s.logger.InfoContext(ctx,
		"connect request sent",
		sessionIDKey, sessionID,
		targetKey, targetPeerID,
	)
	return nil
}

// Close shuts down the session and all connections.
func (s *Session) Close() error {
	var errs []error
	s.closeOnce.Do(func() {
		close(s.closeCh)
		// Delete-then-close, mirroring teardownWithReason: the sentinel in
		// watchPCClosed identifies OUR OWN teardown by registry absence —
		// closing PCs while the entries remain made every close re-fire an
		// async teardown (signal sends from a mid-close session; the 2026-
		// -10-20 full-suite hang).
		s.mu.RLock()
		snapshot := make([]*p2pSession, 0, len(s.peers))
		for _, p2p := range s.peers {
			snapshot = append(snapshot, p2p)
		}
		s.mu.RUnlock()
		for _, p2p := range snapshot {
			if cur := s.deleteP2P(p2p.sessionID); cur != nil {
				if closeErr := cur.close(); closeErr != nil {
					errs = append(errs, closeErr)
				}
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
			p.log().Info("session idle, reclaiming", sessionIDKey, id,
				idleTimeoutKey, timeout.String())
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

// relayFatalHook binds the fatal callback to one relay client (#162).
//
// The callback records WHICH generation of client died. A client that has
// already been superseded (its replacement registered successfully) must
// not start another rebuild: the server's same-key takeover closes the OLD
// connection by design, so without this check every successful rebuild
// immediately schedules the next one — the measured ~5s forever loop.
func (s *Session) relayFatalHook(rc *relay.Client) func(error) {
	return func(err error) {
		if !s.isCurrentRelayClient(rc) {
			s.logger.Info("stale relay client fatal ignored (already superseded)",
				errorKey, err)
			return
		}
		// contextcheck anchors at the chain start (this dispatch line, not
		// the callee): recovery is deliberately detached from any request
		// or Dial scope — the session outlives both, and the rebuild ladder
		// owns its own timeouts.
		s.handleRelayFatal(err)
	}
}

// relayPeerGoneHook binds the peer-gone callback to one relay client so a
// superseded generation cannot drive recovery for its successor (#162).
func (s *Session) relayPeerGoneHook(rc *relay.Client) func([32]byte, byte) {
	return func(peer [32]byte, reason byte) {
		if !s.isCurrentRelayClient(rc) {
			return
		}
		s.handleRelayPeerGone(peer, reason)
	}
}

// isCurrentRelayClient reports whether rc is still the session's active
// relay client.
func (s *Session) isCurrentRelayClient(rc *relay.Client) bool {
	s.mu.RLock()
	cur := s.relayClient
	s.mu.RUnlock()
	return cur != nil && cur == rc
}

// newIdentityClient builds a relay client on the session's stable identity
// (#162). Every construction path — initial dial, rebuild, federated
// re-point, and test doubles — goes through here so the announced key can
// never drift from the routing key.
func (s *Session) newIdentityClient() *relay.Client {
	if s.relayIdentity == nil {
		// Defensive: a Session built outside Dial (tests) has no identity.
		// Fall back to a fresh keypair rather than an all-zero-key client.
		pub, priv := relay.GenerateKeyPair()
		return relay.NewClientWithIdentity(pub, priv)
	}
	return relay.NewClientWithIdentity(s.relayIdentity.pub, s.relayIdentity.priv)
}

// relayIdentityPub returns the session's relay identity public key (#162).
// ok=false when the identity was never minted (zero value), which only
// happens for a Session built outside Dial (tests).
func (s *Session) relayIdentityPub() (pub [32]byte, ok bool) {
	if s.relayIdentity == nil {
		return [32]byte{}, false
	}
	return s.relayIdentity.pub, s.relayIdentity.pub != [32]byte{}
}

// relayIdentityKeys returns the session's relay identity keypair (#162).
func (s *Session) relayIdentityKeys() (pub, priv [32]byte, ok bool) {
	if s.relayIdentity == nil || s.relayIdentity.pub == [32]byte{} {
		return [32]byte{}, [32]byte{}, false
	}
	return s.relayIdentity.pub, s.relayIdentity.priv, true
}

// handleRelayFatal reacts to the session-level relay WebSocket dying (read
// timeout / pong starvation / write failure — relay.Client liveness). Per
// the relay contract ("relay disconnect → session-level complete rebuild"):
// every session that had adopted relay is torn down so its peer redials
// fresh, and the shared relay client itself is rebuilt so the NEXT adoption
// isn't handed a dead socket (2026-10-08 production: relay black-holed mid-
// test-day, both peers "on relay", zero data flowing, NVR restart was the
// only recovery). A RestartingError cause (#106-B) routes the rebuild onto
// the server-hinted fast retry instead of the 5s ladder.
func (s *Session) handleRelayFatal(err error) {
	select {
	case <-s.closeCh:
		return // Session.Close() owns teardown
	default:
	}
	// #162: a same-key takeover means a NEWER connection of ours already
	// owns the key — the server displaced this (older) one on purpose.
	// Rebuilding here is what produced the measured 5s/cycle loop. The
	// sessions riding the displaced socket still need teardown (the server
	// stopped reading it), but the transport itself is healthy.
	if relay.IsReplaced(err) {
		s.logger.Info("relay connection superseded by a newer registration — not rebuilding",
			errorKey, err)
		s.teardownRelayRiders("relay_transport_superseded")
		return
	}
	s.logger.Warn("relay connection fatal — rebuilding", errKey, err)

	s.teardownRelayRiders("relay_transport_dead")

	// 2) Rebuild the shared relay client in the background (bounded).
	go s.rebuildRelayClient(err)
}

// teardownRelayRiders closes every session currently riding the relay
// transport. Signaling session_close makes the peer redial immediately
// instead of timing out against silence; the OnSessionClose callback
// surfaces to the embedder.
func (s *Session) teardownRelayRiders(reason string) {
	s.mu.RLock()
	victims := make([]*p2pSession, 0, len(s.peers))
	for _, p2p := range s.peers {
		if p2p.relayActiveSnapshot() {
			victims = append(victims, p2p)
		}
	}
	s.mu.RUnlock()
	for _, p2p := range victims {
		p2p.teardownWithReason(reason)
	}
}

// handleRelayPeerGone reacts to the server's FramePeerGone (#106-B): a peer we
// recently routed with left the relay. Sessions bound to that peer's relay
// key are torn down with session_close (the peer's signaling may still be
// alive — this also tells IT to redial), turning the old pong-timeout
// recovery into a ~1s redial. Sessions on other transports are untouched.
func (s *Session) handleRelayPeerGone(peer [32]byte, reason byte) {
	select {
	case <-s.closeCh:
		return
	default:
	}
	// #117 asymmetric leg loss: the server self-notifies a key's surviving
	// leg when its sibling registration (data/ctrl) departs. Pong-based
	// liveness can NEVER see this — the survivor keeps answering — so the
	// only recovery is replacing the whole relay client (fresh key, fresh
	// registration; new sessions announce the new key via SDP).
	s.mu.RLock()
	own := s.relayClient != nil && s.relayClient.PubKey() == peer
	s.mu.RUnlock()
	if own {
		s.logger.Warn("relay self-notification — own sibling leg departed, rebuilding relay client",
			peerKey, hex.EncodeToString(peer[:8]),
			reasonKey, relay.PeerGoneReasonString(reason),
		)
		go s.rebuildRelayClient(fmt.Errorf("own sibling relay leg departed (reason 0x%02x)", reason))
		return
	}
	s.mu.RLock()
	victims := make([]*p2pSession, 0, len(s.peers))
	for _, p2p := range s.peers {
		if p2p.boundToRelayPeer(peer) {
			victims = append(victims, p2p)
		}
	}
	s.mu.RUnlock()
	if len(victims) == 0 {
		return
	}
	s.logger.Info("relay peer gone — tearing down sessions",
		peerKey, hex.EncodeToString(peer[:8]),
		reasonKey, relay.PeerGoneReasonString(reason),
		countKey, len(victims),
	)
	for _, p2p := range victims {
		p2p.teardownWithReason("relay_peer_gone")
	}
}

// rebuildRelayClient reconnects the session-level relay transport with
// bounded retries. A fresh relay.Client is used each attempt (the previous
// one is permanently failed — fatalOnce is spent), matching the
// full-rebuild contract. New sessions snapshot the new client at creation.
// A restarting cause (#106-B) shortens the FIRST attempt to the server's
// retry hint (+ jitter, clamped) — the server said it will be back shortly.
func (s *Session) rebuildRelayClient(cause error) {
	w := s.welcome
	if w == nil || w.RelayURL == "" {
		return
	}
	// #162 single-flight: concurrent fatal callbacks (a displaced old client
	// and its replacement) used to run parallel rebuild ladders that dialed
	// with the same identity and displaced each other forever.
	if !s.beginRelayRebuild() {
		s.logger.Debug("relay rebuild already in flight — skipping duplicate")
		return
	}
	defer s.relayRebuilding.Store(false)
	// Host circuit breaking, consulted before the first attempt too: a
	// circuit that is open now means there is nothing to retry — sleeping
	// through the backoff first would just delay the skip.
	if cfg := s.sessionConfig(); cfg.RelayDialGate != nil && !cfg.RelayDialGate() {
		s.logger.Info("relay rebuild skipped by host gate (circuit open)")
		return
	}

	firstDelay := 5 * time.Second
	if hint, ok := relay.RetryHint(cause); ok {
		firstDelay = hint + jitterUpTo(750*time.Millisecond)
		firstDelay = min(max(firstDelay, 300*time.Millisecond), 10*time.Second)
	}
	for attempt := 1; attempt <= 3; attempt++ {
		delay := firstDelay
		if attempt > 1 {
			delay = time.Duration(attempt) * 5 * time.Second
		}
		select {
		case <-s.closeCh:
			return
		case <-time.After(delay):
		}
		// Host circuit breaking is re-consulted per attempt: a circuit that
		// opened while we were sleeping must stop here rather than dial into
		// a known-dead relay. The next signaling session retries naturally.
		cfg := s.sessionConfig()
		if cfg.RelayDialGate != nil && !cfg.RelayDialGate() {
			s.logger.Info("relay rebuild skipped by host gate (circuit open)")
			return
		}
		// #162: same identity across rebuilds — the announced key must not
		// drift, and the server must recognize the re-dial as this peer.
		rc := s.newIdentityClient()
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		// #143: a fresh ticket per attempt — the welcome ticket is spent.
		s.armFreshRelayTicket(ctx, rc, w)
		err := rc.Connect(ctx, w.RelayURL, w.RelayPubKey)
		cancel()
		if cfg.OnRelayConnect != nil {
			cfg.OnRelayConnect(err)
		}
		if err != nil {
			s.logger.Warn("relay rebuild attempt failed",
				attemptKey, attempt, errorKey, err)
			continue
		}
		// Register the replacement BEFORE arming its hooks so the takeover
		// of the previous generation cannot be mistaken for our own death.
		s.mu.Lock()
		s.relayClient = rc
		s.mu.Unlock()
		rc.OnFatal(s.relayFatalHook(rc))
		rc.OnPeerGone(s.relayPeerGoneHook(rc))
		s.logger.Info("relay connection rebuilt", attemptKey, attempt)
		return
	}
	s.logger.Warn("relay rebuild gave up after 3 attempts")
}

// beginRelayRebuild claims the rebuild slot. ok=false means another ladder
// already owns it and this trigger must be dropped (#162 single-flight).
func (s *Session) beginRelayRebuild() bool {
	return s.relayRebuilding.CompareAndSwap(false, true)
}

// jitterUpTo returns a pseudo-random duration in [0, bound) — the
// reconnect thundering-herd spread. crypto/rand (not math/rand) so gosec
// G404 stays quiet; the cost, one 8-byte read per rebuild, is irrelevant
// here.
func jitterUpTo(bound time.Duration) time.Duration {
	if bound <= 0 {
		return 0
	}
	var b [8]byte
	if _, err := crand.Read(b[:]); err != nil {
		return bound / 2
	}
	// Shift out the sign bit: v is non-negative, so the modulo below needs
	// no narrowing conversion.
	v := int64(binary.LittleEndian.Uint64(b[:]) >> 1)
	return time.Duration(v % int64(bound))
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

// linkDegradedCallback returns the live #134⑤ fast-switch event callback.
func (s *Session) linkDegradedCallback() func(sessionID, cause string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.OnLinkDegraded
}

// playbackDegradedCallback returns the live #136 policy degradation
// callback.
func (s *Session) playbackDegradedCallback() func(sessionID, reason string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.OnPlaybackDegraded
}

// ReportLinkQuality feeds a media-layer loss measurement into the session's
// fast-switch monitor (#134⑤). The transport SDK cannot see decoder-level
// loss on the tunnel streams — the embedder (media stack port) measures it
// (per-stream reassembly gaps, decode stalls) and reports it here;
// lossPct is clamped to [0,100]. Samples are valid for 10s (two stats
// ticks); a fresh healthy sample suppresses the RTT-based fallback check.
// The monitor escalates after two consecutive degraded windows (see
// linkmon.go); reporting more often than every ~5s is wasteful but harmless.
func (s *Session) ReportLinkQuality(sessionID string, lossPct float64) error {
	p := s.getP2P(sessionID)
	if p == nil {
		return ErrSessionNotFound
	}
	lossPct = min(max(lossPct, 0), 100)
	p.linkLoss.Store(&linkQuality{lossPct: lossPct, at: time.Now()})
	return nil
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
