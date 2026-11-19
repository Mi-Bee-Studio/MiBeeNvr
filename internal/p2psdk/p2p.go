package peer

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/webrtc/v4"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/p2psdk/relay"
)

// signalSender is the minimal interface for sending raw signaling messages.
// Satisfied by *signalingClient and by test fakes.
type signalSender interface {
	sendRaw(msgType MessageType, payload any) error
}

//nolint:govet // fieldalignment: 剩 8B 尺寸差，整struct重排会毁掉按语义分组的字段注释
type p2pSession struct {
	recoveryStart time.Time
	startedAt     time.Time
	signal        signalSender
	iceReady      chan struct{}
	// watchSignal/watchCfg snapshot the arbitration inputs so the
	// pathological-pair eviction (pathevict.go) can adopt the relay
	// transport later, from the stats callback. Set once by watchTransport.
	watchSignal *signalingClient
	dc          *webrtc.DataChannel
	// relayKick is closed when the peer announces (via signaling session_open)
	// that it adopted the relay transport while our ICE arbitration is still
	// pending — the arbitration then falls to relay immediately instead of
	// burning the full TransportTimeout window (2026-10-08: 20s of dead
	// first-screen time measured in production). Closing after the
	// arbitration has decided is a harmless no-op.
	relayKick chan struct{}
	logger    *slog.Logger
	owner     *Session // live callback source; user callbacks are read at event time
	onStats   func(StatsSnapshot)
	// #88① dual-channel direct path. ctrlRaw is the raw lossy control
	// channel before wrapping (offerer side). Guarded by mu.
	ctrlRaw     *webrtc.DataChannel
	graceTimer  *time.Timer
	relayClient *relay.Client
	// relayIdentity is the session-stable announced key (mibeescout#16):
	// exists even when relayClient is nil (dial gated/failed/pending).
	// Offer/answer/e2ee derivation use it; CONNECTED-transport decisions
	// (fallback, ForceRelay adoption) keep using relayClient.
	relayIdentity *relay.Client
	// ctrlDC holds the wrapped lossy control channel once open; nil
	// otherwise. Guarded by mu.
	ctrlDC       *DataChannel
	restartTimer *time.Timer // periodic driver while a restart offer is unanswered
	// relayChan is this session's channel over the SHARED relay client
	// (nil until relay adoption). Firing its close callbacks on session
	// teardown is what lets the application layer (NVR tunnel registry)
	// unregister — the old shim's OnClose was a registration-swallowing
	// no-op, leaking the registry entry past session reclamation.
	relayChan         *relaySessionChannel
	statsCollector    *StatsCollector
	pc                *webrtc.PeerConnection
	sessionID         string
	remotePeerID      string
	pendingCandidates []string
	watchCfg          SignalingConfig
	// lastActivity is the unix-nano timestamp of the most recent traffic on
	// the user-facing DataChannel (either direction). The Session idle sweep
	// reclaims sessions whose viewers died without session_close (#20).
	lastActivity    atomic.Int64
	restartFailures int // consecutive failed ICE restart rounds
	// evictStreak counts consecutive pathological-pair stats samples
	// (#82): eviction fires only after it sustains — a lone WiFi RTT spike
	// on a healthy LAN pair must never demote the session to relay.
	// Guarded by mu.
	evictStreak    int
	relayKickFn    sync.Once
	evictOnce      sync.Once
	mu             sync.Mutex
	remoteRelayKey [32]byte
	// Telemetry state (research doc 07 §5.1). telOn gates reporting
	// (SignalingConfig.DisableTelemetry); iceTags attributes the selected
	// path; lastPairKey/lastSnap carry change detection; startedAt (moved
	// beside recoveryStart — same alignment class, fieldalignment) and
	// closeReason feed the final session_stats report. telOn/iceTags/startedAt
	// are telMu-guarded (written once by initTelemetryLocked, which callers
	// invoke under p.mu — close()'s report path holds no p.mu and must not
	// race a session still mid-setup).
	telOn       bool
	iceTags     *iceTagIndex
	telMu       sync.Mutex
	lastPairKey string
	lastSnap    StatsSnapshot
	closeReason string
	// #88①: ctrlWanted mirrors the requester's ctrl_channel declaration
	// (offerer gate — an old requester never sees a second channel).
	ctrlWanted bool
	isOfferer  bool
	// Relay fallback fields.
	relayActive  bool // true once relay transport was adopted; ICE restarts are then moot
	iceConnected bool // last known ICE connection state (liveness gate for retries)
	// e2eeWarnOnce gates the plaintext-tunnel WARN: a session may deliver
	// its channel and re-derive multiple times, but the operator only
	// needs to hear about a plaintext tunnel once (mibeescout#16).
	e2eeWarnOnce sync.Once
}

// startAsOfferer creates a PeerConnection and DataChannel (as offerer).
//
//nolint:funlen // complex WebRTC setup
func (p *p2pSession) startAsOfferer(signal *signalingClient, cfg SignalingConfig, welcome *Welcome) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.initTelemetryLocked(cfg, welcome)
	pc, err := newPeerConnection(cfg, welcome, p.log())
	if err != nil {
		return fmt.Errorf("create pc: %w", err)
	}
	p.pc = pc
	p.signal = signal
	pc.OnICEConnectionStateChange(p.handleICEStateChange)
	pc.OnConnectionStateChange(p.watchPCClosed)

	// #88①: open the lossy control channel FIRST (SCTP stream id order is
	// creation order), but ONLY when the requester declared dual-channel
	// support — an old answerer would wrap every incoming channel as its
	// one tunnel, so a second channel must never reach it.
	if p.ctrlWanted {
		unordered := false
		noRetrans := uint16(0)
		ctrl, ctrlErr := pc.CreateDataChannel(DataChannelLabelCtrl, &webrtc.DataChannelInit{
			Ordered:        &unordered,
			MaxRetransmits: &noRetrans,
		})
		if ctrlErr != nil {
			p.log().Warn("ctrl channel create failed — single-channel fallback", errKey, ctrlErr)
		} else {
			p.ctrlRaw = ctrl
		}
	}

	ordered := true
	dc, err := pc.CreateDataChannel(DataChannelLabel, &webrtc.DataChannelInit{Ordered: &ordered})
	if err != nil {
		_ = pc.Close()
		return fmt.Errorf("create datachannel: %w", err)
	}
	p.dc = dc

	pc.OnICECandidate(p.iceCandidateHandler(signal))
	dc.OnOpen(p.dataChannelOpenHandler(signal, cfg))
	dc.OnMessage(p.dataChannelMessageHandler())

	// Send connect_accept so the initiator knows we accepted
	if sendErr := signal.sendRaw(MessageTypeConnectAccept, ConnectAccept{
		SessionID: p.sessionID,
	}); sendErr != nil {
		_ = pc.Close()
		return fmt.Errorf("send connect_accept: %w", sendErr)
	}
	p.log().Info("connect accepted", sessionIDKey, p.sessionID)

	offer, err := pc.CreateOffer(nil)
	if err != nil {
		_ = pc.Close()
		return fmt.Errorf("create offer: %w", err)
	}
	if descErr := pc.SetLocalDescription(offer); descErr != nil {
		_ = pc.Close()
		return fmt.Errorf("set local desc: %w", descErr)
	}

	<-webrtc.GatheringCompletePromise(pc)

	finalOffer := pc.LocalDescription()
	if finalOffer == nil {
		_ = pc.Close()
		return errors.New("no local description")
	}

	offerMsg := SDPOffer{
		SessionID: p.sessionID, SDP: finalOffer.SDP,
	}
	// Announce the session-stable IDENTITY key — see announcedRelayKey
	// (mibeescout#16).
	if pk, ok := p.announcedRelayKey(); ok {
		offerMsg.RelayPubKey = hex.EncodeToString(pk[:])
	}
	if sendErr := signal.sendRaw(MessageTypeSDPOffer, offerMsg); sendErr != nil {
		return sendErr
	}

	p.iceReady = make(chan struct{})
	p.relayKick = make(chan struct{})
	// Stats polling starts only after every fallible setup step succeeded —
	// an error return above must not leave a collector polling a closed PC.
	p.startStatsCollectorLocked(pc)
	go p.watchTransport(signal, cfg)
	return nil
}

// startAsAnswerer creates a PeerConnection and waits for SDP offer.
func (p *p2pSession) startAsAnswerer(signal *signalingClient, cfg SignalingConfig, welcome *Welcome) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.initTelemetryLocked(cfg, welcome)
	pc, err := newPeerConnection(cfg, welcome, p.log())
	if err != nil {
		return fmt.Errorf("create pc: %w", err)
	}
	p.pc = pc
	p.signal = signal
	pc.OnICEConnectionStateChange(p.handleICEStateChange)
	pc.OnConnectionStateChange(p.watchPCClosed)

	p.startStatsCollectorLocked(pc)

	pc.OnDataChannel(func(dc *webrtc.DataChannel) {
		// #88①: the lossy ctrl channel is SDK plumbing — stash it and pair
		// it with the data channel below; the application callback still
		// fires exactly once, for the data channel. Old peers send only
		// p2p-data, so this branch simply never triggers.
		if dc.Label() == DataChannelLabelCtrl {
			wctrl := p.activityDataChannel(wrapDataChannel(dc))
			p.mu.Lock()
			p.ctrlDC = &wctrl
			p.mu.Unlock()
			return
		}
		p.mu.Lock()
		p.dc = dc
		ctrl := p.ctrlDC
		p.mu.Unlock()
		dc.OnOpen(func() {
			p.log().Info(
				"DataChannel opened (dtls)",
				sessionIDKey, p.sessionID, labelKey, dc.Label(),
			)
			if cb := p.owner.dataChannelCallback(); cb != nil {
				wdc := p.activityDataChannel(wrapDataChannel(dc))
				p.armE2EE(&wdc)
				if ctrl != nil {
					wdc.Ctrl = ctrl // #88① pair (may have arrived earlier)
				}
				cb(wdc)
			}
			if cb := p.owner.sessionCallback(); cb != nil {
				cb()
			}
			// Signal that ICE/DataChannel is ready.
			select {
			case <-p.iceReady:
			default:
				close(p.iceReady)
			}
		})
	})

	p.iceReady = make(chan struct{})
	p.relayKick = make(chan struct{})
	go p.watchTransport(signal, cfg)
	return nil
}

func (p *p2pSession) iceCandidateHandler(signal *signalingClient) func(*webrtc.ICECandidate) {
	return func(candidate *webrtc.ICECandidate) {
		if candidate == nil {
			return
		}
		_ = signal.sendRaw(MessageTypeICECandidate, ICECandidate{
			SessionID: p.sessionID, Candidate: candidate.ToJSON().Candidate,
		})
	}
}

func (p *p2pSession) dataChannelOpenHandler(signal *signalingClient, cfg SignalingConfig) func() {
	return func() {
		p.log().Info(
			"DataChannel opened (dtls)",
			sessionIDKey, p.sessionID,
		)
		p.mu.Lock()
		dc := p.dc
		ctrl := p.ctrlRaw
		p.mu.Unlock()
		if cb := p.owner.dataChannelCallback(); cb != nil {
			wdc := p.activityDataChannel(wrapDataChannel(dc))
			p.armE2EE(&wdc)
			// #88①: pair the lossy ctrl channel with the data channel so
			// the transport layer can route control frames off the ordered
			// lane. Wrap order: data first, ctrl attached before delivery.
			if ctrl != nil {
				wctrl := p.activityDataChannel(wrapDataChannel(ctrl))
				p.mu.Lock()
				p.ctrlDC = &wctrl
				p.mu.Unlock()
				wdc.Ctrl = &wctrl
			}
			cb(wdc)
		}
		if cb := p.owner.sessionCallback(); cb != nil {
			cb()
		}
		_ = signal.sendRaw(MessageTypeSessionOpen, SessionOpen{
			SessionID: p.sessionID, PeerID: cfg.PeerID,
		})
		// Signal that ICE/DataChannel is ready.
		select {
		case <-p.iceReady:
		default:
			close(p.iceReady)
		}
	}
}

func (p *p2pSession) dataChannelMessageHandler() func(webrtc.DataChannelMessage) {
	return func(msg webrtc.DataChannelMessage) {
		p.log().Debug("data received", lenKey, len(msg.Data))
	}
}

// handleRemoteOffer processes an incoming SDP offer (for answerer).
func (p *p2pSession) handleRemoteOffer(sdp string, signal *signalingClient) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.pc == nil {
		return errors.New("no peer connection")
	}

	if err := p.pc.SetRemoteDescription(webrtc.SessionDescription{
		Type: webrtc.SDPTypeOffer, SDP: sdp,
	}); err != nil {
		return fmt.Errorf("set remote desc: %w", err)
	}

	answer, err := p.pc.CreateAnswer(nil)
	if err != nil {
		return fmt.Errorf("create answer: %w", err)
	}
	if descErr := p.pc.SetLocalDescription(answer); descErr != nil {
		return fmt.Errorf("set local desc: %w", descErr)
	}

	<-webrtc.GatheringCompletePromise(p.pc)

	finalAnswer := p.pc.LocalDescription()
	if finalAnswer == nil {
		return errors.New("no local description")
	}
	answerMsg := SDPAnswer{
		SessionID: p.sessionID, SDP: finalAnswer.SDP,
	}
	// Identity key announcement — see announcedRelayKey (mibeescout#16).
	if pk, ok := p.announcedRelayKey(); ok {
		answerMsg.RelayPubKey = hex.EncodeToString(pk[:])
	}
	return signal.sendRaw(MessageTypeSDPAnswer, answerMsg)
}

// announcedRelayKey returns the relay identity public key to announce in
// SDP offer/answer and whether to announce it at all.
//
// The IDENTITY key (session-stable, mibeescout#16) — not the connected
// client's: the peer arms tunnel E2EE and aims its routing key at this
// value, so it must exist even while the relay dial is gated/failed/
// pending. Gated on relay intent — with no relay to ever register on, an
// announced key would be a permanently unroutable destination, actively
// misleading the peer's forced-relay path.
func (p *p2pSession) announcedRelayKey() ([32]byte, bool) {
	if p.owner.relayIntent() && p.relayIdentity != nil {
		return p.relayIdentity.PubKey(), true
	}
	return [32]byte{}, false
}

// handleRemoteAnswer processes an incoming SDP answer (for offerer).
func (p *p2pSession) handleRemoteAnswer(sdp string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.pc == nil {
		return errors.New("no peer connection")
	}
	err := p.pc.SetRemoteDescription(webrtc.SessionDescription{
		Type: webrtc.SDPTypeAnswer, SDP: sdp,
	})
	if err != nil {
		return err
	}
	p.flushPendingCandidatesLocked()
	return nil
}

// flushPendingCandidatesLocked replays any ICE candidates that arrived
// before SetRemoteDescription was called. Must hold p.mu.
func (p *p2pSession) flushPendingCandidatesLocked() {
	if len(p.pendingCandidates) == 0 {
		return
	}
	for _, c := range p.pendingCandidates {
		_ = p.pc.AddICECandidate(webrtc.ICECandidateInit{Candidate: c})
	}
	p.pendingCandidates = nil
}

// handleRemoteICECandidate processes an incoming ICE candidate.
func (p *p2pSession) handleRemoteICECandidate(candidate string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.pc == nil {
		p.pendingCandidates = append(p.pendingCandidates, candidate)
		return nil
	}
	// If the remote description hasn't been set yet (answer not received),
	// queue the candidate for replay after SetRemoteDescription.
	// pion/webrtc v4 rejects AddICECandidate before SetRemoteDescription.
	remoteDesc := p.pc.RemoteDescription()
	if remoteDesc == nil {
		p.pendingCandidates = append(p.pendingCandidates, candidate)
		return nil
	}
	return p.pc.AddICECandidate(webrtc.ICECandidateInit{Candidate: candidate})
}

// flushPendingCandidatesLocked replays any ICE candidates that arrived

func (p *p2pSession) close() error {
	// Final telemetry before tearing anything down: the signaling link and
	// stats snapshot are both still alive here (research doc 07 §5.1).
	p.emitSessionStats()

	p.mu.Lock()
	if p.graceTimer != nil {
		p.graceTimer.Stop()
		p.graceTimer = nil
	}
	if p.restartTimer != nil {
		p.restartTimer.Stop()
		p.restartTimer = nil
	}
	sc := p.statsCollector
	p.statsCollector = nil
	dc := p.dc
	p.dc = nil
	// NOTE: p.relayClient is the SESSION-LEVEL shared connection (snapshotted
	// at session creation) — it must NOT be closed here. Closing it ended the
	// relay transport for every other session AND left later sessions handing
	// packets to a corpse: any single session teardown black-holed the whole
	// relay path (production 2026-10-08). Session.Close() owns its lifetime;
	// relay.Client.OnFatal drives mid-life rebuild.
	p.relayClient = nil
	pc := p.pc
	p.pc = nil
	p.mu.Unlock()

	// Stop OUTSIDE p.mu (2026-10-19): the collector's OnStats callback runs
	// checkPathEviction, which takes p.mu — stopping under the lock deadlocks
	// close() against a tick already inside that callback.
	if sc != nil {
		sc.Stop()
	}

	var relayCh *relaySessionChannel
	p.mu.Lock()
	relayCh = p.relayChan
	p.relayChan = nil
	p.mu.Unlock()
	if relayCh != nil {
		// Session-local teardown of the shared-connection channel: drops the
		// inbound route and notifies the application layer (tunnel registry).
		relayCh.sessionClosed()
	}
	if dc != nil {
		_ = dc.Close()
	}
	if pc != nil {
		return pc.Close()
	}
	return nil
}

func (p *p2pSession) log() *slog.Logger {
	if p.logger != nil {
		return p.logger.With(sessionIDKey, p.sessionID)
	}
	return slog.Default().With(sessionIDKey, p.sessionID)
}

// relayActiveSnapshot reports whether this session adopted the relay
// transport (mutex-guarded read; the flag flips once in watchTransport).
func (p *p2pSession) relayActiveSnapshot() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.relayActive
}

// watchPCClosed reacts to PeerConnection → Closed on a still-registered
// session (2026-10-20 root cause of the fleet leak storm): a REMOTE
// graceful close (the peer SDK's session teardown, DTLS close_notify on
// the wire) cascades OUR PeerConnection to Closed without any local
// Close() — pinned by TestRemoteGracefulCloseCascadesToLocalPCClosed.
// The SDK used to ignore that signal, so the session lingered registered
// until the 15-minute idle sweep while its stats collector polled the
// dead PC (the "agent is closed" storm + PC/collector idle churn). Now
// the transition IS the death notice: immediate teardown, session_close
// lets a still-alive peer redial at once. Our own teardown path is
// registry-gone by the time its pc.Close() fires this callback, so the
// identity check below filters it out.
func (p *p2pSession) watchPCClosed(s webrtc.PeerConnectionState) {
	if s != webrtc.PeerConnectionStateClosed {
		return
	}
	if p.owner.getP2P(p.sessionID) != p {
		return // our own teardown already owns the session
	}
	p.log().Info("PeerConnection closed by remote cascade — tearing down session")
	// Async: we are on pion's connection-state goroutine; teardown joins
	// the ICE agent via close() and would deadlock inline.
	go p.teardownWithReason("peerconnection_closed")
}

// boundToRelayPeer reports whether this session's relay peer key matches —
// the FramePeerGone (#106-B) victim filter. Caller holds the owner's RLock.
func (p *p2pSession) boundToRelayPeer(peer [32]byte) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.remoteRelayKey == peer
}

// teardownWithReason removes the session from the owner's registry, closes
// local resources, notifies the peer via signaling session_close (so it
// redials immediately instead of timing out against silence) and fires the
// OnSessionClose callback. Caller: abandonSession (restart path) and the
// relay-fatal victim sweep (peer.go handleRelayFatal).
func (p *p2pSession) teardownWithReason(reason string) {
	p.setTelemetryCloseReason(reason)
	if p.signal != nil {
		_ = p.signal.sendRaw(MessageTypeSessionClose, SessionClose{
			SessionID: p.sessionID, Reason: reason,
		})
	}
	if deleted := p.owner.deleteP2P(p.sessionID); deleted != nil {
		_ = deleted.close()
	}
	if cb := p.owner.sessionCloseCallback(); cb != nil {
		cb(p.sessionID, reason)
	}
}

// touch marks the session as carrying traffic (called on every user-facing
// DataChannel message, inbound and outbound).
func (p *p2pSession) touch() {
	p.lastActivity.Store(time.Now().UnixNano())
}

// activityDataChannel composes touch() into the user-facing DataChannel so
// the idle sweep observes traffic in both directions without touching the
// transport implementations.
func (p *p2pSession) activityDataChannel(dc DataChannel) DataChannel {
	innerOnMessage, innerSend := dc.OnMessage, dc.Send
	if innerOnMessage != nil {
		dc.OnMessage = func(h func([]byte)) {
			innerOnMessage(func(data []byte) {
				p.touch()
				h(data)
			})
		}
	}
	if innerSend != nil {
		dc.Send = func(data []byte) error {
			p.touch()
			return innerSend(data)
		}
	}
	return dc
}

// startStatsCollectorLocked (re)binds the stats poller to pc, stopping any
// collector left over from a previous session build — session rebuilds and
// reconnects otherwise leak a poller that keeps calling GetStats on a closed
// PeerConnection (pion logs "the agent is closed" on every tick).
// Caller must hold p.mu.
func (p *p2pSession) startStatsCollectorLocked(pc *webrtc.PeerConnection) {
	if p.statsCollector != nil {
		p.statsCollector.Stop()
	}
	p.statsCollector = NewStatsCollector(pc, p.log())
	// Stamp the session ID into every snapshot: OnStats is one shared
	// callback for all concurrent sessions of the signaling connection.
	// The pathological-pair eviction rides the same tick so it works even
	// when the application does not consume stats (#82).
	sid := p.sessionID
	p.statsCollector.OnStats = func(snap StatsSnapshot) {
		snap.SessionID = sid
		p.checkPathEviction(snap)
		p.maybeSendIceSelected(snap)
		if p.onStats != nil {
			p.onStats(snap)
		}
	}
	p.statsCollector.Start()
}

// watchTransport monitors ICE and relay readiness, selecting the transport
// once the first available one is ready. It is NOT mid-stream switching —
// the decision is made once during session establishment.
func (p *p2pSession) watchTransport(signal *signalingClient, cfg SignalingConfig) {
	p.mu.Lock()
	iceReady := p.iceReady
	rc := p.relayClient
	p.watchSignal = signal
	p.watchCfg = cfg
	p.mu.Unlock()

	if cfg.ForceRelay && rc != nil {
		// #82: relay pinned by the application — adopt the relay transport
		// without waiting for the direct arbitration. A direct DataChannel
		// may still open (even on a pathological pair); the adoption
		// re-fires the callbacks with the relay channel, replacing it.
		p.adoptRelayTransport(signal, cfg, rc)
		return
	}

	ctx := context.Background()
	relayKick := p.relayKickChan()
	_, err := EstablishTransportKicked(ctx, iceReady, relayKick, rc, cfg.transportTimeout())
	if errors.Is(err, ErrDirectICEConnected) {
		// ICE connected — dataChannelOpenHandler already fired callbacks.
		return
	}
	if err != nil {
		p.log().Warn("transport establishment failed", errorKey, err)
		return
	}

	// ICE timed out — use relay transport.
	p.adoptRelayTransport(signal, cfg, rc)
}

// adoptRelayTransport switches the session onto the shared relay client and
// announces session_open (#82: extracted so the ForceRelay pin shares the
// exact adoption path with the arbitration-timeout fallback). Routing is
// per-session explicit (SendTo/OnPacket by peer key): the shared client must
// never have its single-peer destination mutated by one session out of many.
func (p *p2pSession) adoptRelayTransport(signal *signalingClient, cfg SignalingConfig, rc *relay.Client) {
	p.mu.Lock()
	p.relayActive = true
	ch := &relaySessionChannel{rc: rc, peer: p.remoteRelayKey}
	p.relayChan = ch
	p.mu.Unlock()
	p.log().Info("using relay transport", sessionIDKey, p.sessionID, pinnedKey, cfg.ForceRelay)

	dc := p.activityDataChannel(ch.dataChannel())
	p.armE2EE(&dc)
	if cb := p.owner.dataChannelCallback(); cb != nil {
		cb(dc)
	}
	if cb := p.owner.sessionCallback(); cb != nil {
		cb()
	}
	_ = signal.sendRaw(MessageTypeSessionOpen, SessionOpen{
		SessionID: p.sessionID, PeerID: cfg.PeerID,
	})
}

// relayKickChan snapshots the kick channel for the arbitration select.
// Caller: watchTransport (single goroutine per session).
func (p *p2pSession) relayKickChan() <-chan struct{} {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.relayKick
}

// kickRelay requests immediate relay fallback: the peer announced via
// signaling that it is already on relay. No-op once the arbitration has
// decided (closing a channel nobody selects on) or when never armed.
func (p *p2pSession) kickRelay() {
	p.mu.Lock()
	ch := p.relayKick
	fn := &p.relayKickFn
	p.mu.Unlock()
	if ch == nil {
		return
	}
	fn.Do(func() { close(ch) })
}

// e2eeKey derives the tunnel E2EE session key for this peer session: our
// relay keypair combined with the remote peer's relay public key (received
// via sdp_offer/sdp_answer). Returns nil when either side's key is missing
// (e.g. relay never connected) — tunnel then runs unencrypted, exactly as
// before E2EE existed.
func (p *p2pSession) e2eeKey() []byte {
	p.mu.Lock()
	rc := p.relayIdentity
	remote := p.remoteRelayKey
	p.mu.Unlock()
	// Derivation is pure key math (X25519) — it needs the identity keypair
	// and the remote public key, NOT a live relay connection (mibeescout#16
	// device-side half: this is what lets direct-path sessions arm E2EE
	// while the relay leg is gated/failed/pending).
	if rc == nil || remote == [32]byte{} {
		return nil
	}
	key, err := rc.DeriveE2EEKey(remote)
	if err != nil {
		p.log().Warn("e2ee key derivation failed", errorKey, err)
		return nil
	}
	return key[:]
}

// armE2EE installs the tunnel E2EE session key on a channel about to be
// delivered to the application. When the key cannot be derived (either
// side's relay key material missing) the tunnel silently runs plaintext —
// a one-time WARN per session makes that observable in device logs instead
// of only via App diagnostics (mibeescout#16).
func (p *p2pSession) armE2EE(dc *DataChannel) {
	dc.E2EEKey = p.e2eeKey()
	if len(dc.E2EEKey) == 0 {
		p.e2eeWarnOnce.Do(func() {
			p.log().Warn("tunnel e2ee not armed, session runs plaintext (missing key material)")
		})
	}
}

// relaySessionChannel is one session's view of the shared relay client.
// Routing is explicit (SendTo the session's peer key, inbound dispatched from
// that same key), and Close is session-local: it never touches the shared
// relay.Client, whose lifetime belongs to Session.Close (PR #24 semantics).
type relaySessionChannel struct {
	rc      *relay.Client
	onClose []func()
	mu      sync.Mutex
	peer    [32]byte
	closed  bool
}

func (r *relaySessionChannel) dataChannel() DataChannel {
	return DataChannel{
		Label: "relay",
		OnMessage: func(h func([]byte)) {
			r.rc.OnPacket(r.peer, h)
		},
		Send: func(data []byte) error {
			r.mu.Lock()
			closed := r.closed
			r.mu.Unlock()
			if closed {
				return errors.New("relay session channel closed")
			}
			return r.rc.SendTo(r.peer, data)
		},
		SendPriority: func(data []byte) error {
			r.mu.Lock()
			closed := r.closed
			r.mu.Unlock()
			if closed {
				return errors.New("relay session channel closed")
			}
			// Server capability is negotiated in server_auth; the client
			// downgrades to the normal lane on legacy servers.
			return r.rc.SendPriorityTo(r.peer, data)
		},
		Close: func() error {
			r.sessionClosed()
			return nil
		},
		OnClose: func(cb func()) {
			r.mu.Lock()
			closed := r.closed
			if !closed {
				r.onClose = append(r.onClose, cb)
			}
			r.mu.Unlock()
			if closed {
				// Late registration on an already-closed channel fires
				// immediately so callers never dangle.
				cb()
			}
		},
	}
}

// sessionClosed tears the session-local channel down: drop the inbound route
// and fire registered close callbacks exactly once. The shared relay client
// keeps serving every other session.
func (r *relaySessionChannel) sessionClosed() {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	r.closed = true
	cbs := append([]func(){}, r.onClose...)
	r.onClose = nil
	r.mu.Unlock()
	r.rc.RemovePacketRoute(r.peer)
	for _, cb := range cbs {
		cb()
	}
}

// ── PeerConnection factory ──────────────────────────────────────────────────

func (sc *signalingClient) sendRaw(msgType MessageType, payload any) error {
	data, err := Marshal(msgType, payload)
	if err != nil {
		return err
	}
	return sc.sendBytes(data)
}

func (sc *signalingClient) sendRawMsg(msgType MessageType, payload any) error {
	return sc.sendRaw(msgType, payload)
}
