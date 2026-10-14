package peer

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/p2psdk/relay"
	"github.com/pion/ice/v4"
	"github.com/pion/webrtc/v4"
)

// signalSender is the minimal interface for sending raw signaling messages.
// Satisfied by *signalingClient and by test fakes.
type signalSender interface {
	sendRaw(msgType MessageType, payload any) error
}

type p2pSession struct {
	sessionID       string
	remotePeerID    string
	isOfferer       bool
	pc              *webrtc.PeerConnection
	dc              *webrtc.DataChannel
	mu              sync.Mutex
	logger          *slog.Logger
	owner           *Session // live callback source; user callbacks are read at event time
	onStats         func(StatsSnapshot)
	signal          signalSender
	graceTimer      *time.Timer
	recoveryStart   time.Time
	restartFailures int         // consecutive failed ICE restart rounds
	restartTimer    *time.Timer // periodic driver while a restart offer is unanswered
	iceConnected    bool        // last known ICE connection state (liveness gate for retries)
	statsCollector  *StatsCollector

	// lastActivity is the unix-nano timestamp of the most recent traffic on
	// the user-facing DataChannel (either direction). The Session idle sweep
	// reclaims sessions whose viewers died without session_close (#20).
	lastActivity atomic.Int64

	// relayChan is this session's channel over the SHARED relay client
	// (nil until relay adoption). Firing its close callbacks on session
	// teardown is what lets the application layer (NVR tunnel registry)
	// unregister — the old shim's OnClose was a registration-swallowing
	// no-op, leaking the registry entry past session reclamation.
	relayChan *relaySessionChannel

	// Relay fallback fields.
	iceReady          chan struct{}
	relayClient       *relay.Client
	remoteRelayKey    [32]byte
	pendingCandidates []string
	relayActive       bool // true once relay transport was adopted; ICE restarts are then moot

	// relayKick is closed when the peer announces (via signaling session_open)
	// that it adopted the relay transport while our ICE arbitration is still
	// pending — the arbitration then falls to relay immediately instead of
	// burning the full TransportTimeout window (2026-10-08: 20s of dead
	// first-screen time measured in production). Closing after the
	// arbitration has decided is a harmless no-op.
	relayKick   chan struct{}
	relayKickFn sync.Once
}

// startAsOfferer creates a PeerConnection and DataChannel (as offerer).
//
//nolint:funlen // complex WebRTC setup
func (p *p2pSession) startAsOfferer(signal *signalingClient, cfg SignalingConfig, welcome *Welcome) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	pc, err := newPeerConnection(welcome, cfg.AdvertiseAddr)
	if err != nil {
		return fmt.Errorf("create pc: %w", err)
	}
	p.pc = pc
	p.signal = signal
	pc.OnICEConnectionStateChange(p.handleICEStateChange)

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
	if err := signal.sendRaw(MessageTypeConnectAccept, ConnectAccept{
		SessionID: p.sessionID,
	}); err != nil {
		_ = pc.Close()
		return fmt.Errorf("send connect_accept: %w", err)
	}
	p.log().Info("connect accepted", "session_id", p.sessionID)

	offer, err := pc.CreateOffer(nil)
	if err != nil {
		_ = pc.Close()
		return fmt.Errorf("create offer: %w", err)
	}
	if err := pc.SetLocalDescription(offer); err != nil {
		_ = pc.Close()
		return fmt.Errorf("set local desc: %w", err)
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
	if p.relayClient != nil {
		pk := p.relayClient.PubKey()
		offerMsg.RelayPubKey = hex.EncodeToString(pk[:])
	}
	if err := signal.sendRaw(MessageTypeSDPOffer, offerMsg); err != nil {
		return err
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

	pc, err := newPeerConnection(welcome, cfg.AdvertiseAddr)
	if err != nil {
		return fmt.Errorf("create pc: %w", err)
	}
	p.pc = pc
	p.signal = signal
	pc.OnICEConnectionStateChange(p.handleICEStateChange)

	p.startStatsCollectorLocked(pc)

	pc.OnDataChannel(func(dc *webrtc.DataChannel) {
		p.mu.Lock()
		p.dc = dc
		p.mu.Unlock()
		dc.OnOpen(func() {
			p.log().Info(
				"DataChannel opened (dtls)",
				"session_id", p.sessionID, "label", dc.Label(),
			)
			if cb := p.owner.dataChannelCallback(); cb != nil {
				wdc := p.activityDataChannel(wrapDataChannel(dc))
				wdc.E2EEKey = p.e2eeKey()
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
			"session_id", p.sessionID,
		)
		p.mu.Lock()
		dc := p.dc
		p.mu.Unlock()
		if cb := p.owner.dataChannelCallback(); cb != nil {
			wdc := p.activityDataChannel(wrapDataChannel(dc))
			wdc.E2EEKey = p.e2eeKey()
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
		p.log().Debug("data received", "len", len(msg.Data))
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
	if err := p.pc.SetLocalDescription(answer); err != nil {
		return fmt.Errorf("set local desc: %w", err)
	}

	<-webrtc.GatheringCompletePromise(p.pc)

	finalAnswer := p.pc.LocalDescription()
	if finalAnswer == nil {
		return errors.New("no local description")
	}
	answerMsg := SDPAnswer{
		SessionID: p.sessionID, SDP: finalAnswer.SDP,
	}
	if p.relayClient != nil {
		pk := p.relayClient.PubKey()
		answerMsg.RelayPubKey = hex.EncodeToString(pk[:])
	}
	return signal.sendRaw(MessageTypeSDPAnswer, answerMsg)
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
	p.mu.Lock()
	if p.graceTimer != nil {
		p.graceTimer.Stop()
		p.graceTimer = nil
	}
	if p.restartTimer != nil {
		p.restartTimer.Stop()
		p.restartTimer = nil
	}
	if p.statsCollector != nil {
		p.statsCollector.Stop()
	}
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
		return p.logger.With("session_id", p.sessionID)
	}
	return slog.Default().With("session_id", p.sessionID)
}

// relayActiveSnapshot reports whether this session adopted the relay
// transport (mutex-guarded read; the flag flips once in watchTransport).
func (p *p2pSession) relayActiveSnapshot() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.relayActive
}

// teardownWithReason removes the session from the owner's registry, closes
// local resources, notifies the peer via signaling session_close (so it
// redials immediately instead of timing out against silence) and fires the
// OnSessionClose callback. Caller: abandonSession (restart path) and the
// relay-fatal victim sweep (peer.go handleRelayFatal).
func (p *p2pSession) teardownWithReason(reason string) {
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
	p.statsCollector.OnStats = p.onStats
	p.statsCollector.Start()
}

// watchTransport monitors ICE and relay readiness, selecting the transport
// once the first available one is ready. It is NOT mid-stream switching —
// the decision is made once during session establishment.
func (p *p2pSession) watchTransport(signal *signalingClient, cfg SignalingConfig) {
	p.mu.Lock()
	iceReady := p.iceReady
	rc := p.relayClient
	p.mu.Unlock()

	ctx := context.Background()
	relayKick := p.relayKickChan()
	_, err := EstablishTransportKicked(ctx, iceReady, relayKick, rc, cfg.transportTimeout())
	if errors.Is(err, ErrDirectICEConnected) {
		// ICE connected — dataChannelOpenHandler already fired callbacks.
		return
	}
	if err != nil {
		p.log().Warn("transport establishment failed", "error", err)
		return
	}

	// ICE timed out — use relay transport. Routing is per-session explicit
	// (SendTo/OnPacket by peer key): the shared client must never have its
	// single-peer destination mutated by one session out of many.
	p.mu.Lock()
	p.relayActive = true
	ch := &relaySessionChannel{rc: rc, peer: p.remoteRelayKey}
	p.relayChan = ch
	p.mu.Unlock()
	p.log().Info("using relay transport", "session_id", p.sessionID)

	dc := p.activityDataChannel(ch.dataChannel())
	dc.E2EEKey = p.e2eeKey()
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
	rc := p.relayClient
	remote := p.remoteRelayKey
	p.mu.Unlock()
	if rc == nil || remote == [32]byte{} {
		return nil
	}
	key, err := rc.DeriveE2EEKey(remote)
	if err != nil {
		p.log().Warn("e2ee key derivation failed", "error", err)
		return nil
	}
	return key[:]
}

// relaySessionChannel is one session's view of the shared relay client.
// Routing is explicit (SendTo the session's peer key, inbound dispatched from
// that same key), and Close is session-local: it never touches the shared
// relay.Client, whose lifetime belongs to Session.Close (PR #24 semantics).
type relaySessionChannel struct {
	rc   *relay.Client
	peer [32]byte

	mu      sync.Mutex
	closed  bool
	onClose []func()
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

// stunURLs flattens the Welcome's ICE server URLs (STUN entries only).
func stunURLs(welcome *Welcome) []string {
	if welcome == nil {
		return nil
	}
	var urls []string
	for _, srv := range welcome.ICEServers {
		for _, u := range srv.URLs {
			if strings.HasPrefix(u, "stun:") {
				urls = append(urls, u)
			}
		}
	}
	return urls
}

func newPeerConnection(welcome *Welcome, advertiseAddr string) (*webrtc.PeerConnection, error) {
	config := webrtc.Configuration{
		BundlePolicy:  webrtc.BundlePolicyMaxBundle,
		RTCPMuxPolicy: webrtc.RTCPMuxPolicyRequire,
	}

	if welcome != nil && len(welcome.ICEServers) > 0 {
		iceServers := make([]webrtc.ICEServer, 0, len(welcome.ICEServers))
		for _, srv := range welcome.ICEServers {
			is := webrtc.ICEServer{URLs: srv.URLs}
			if srv.Username != "" {
				is.Username = srv.Username
				is.Credential = srv.Credential
				is.CredentialType = webrtc.ICECredentialTypePassword
			}
			iceServers = append(iceServers, is)
		}
		config.ICEServers = iceServers
	}

	se := hardenedSettingEngine("")
	// "auto"/"auto:N" resolve per session build via the UPnP→STUN ladder
	// (autoendpoint.go); resolution failure degrades to "" (no advertisement,
	// stock ICE + relay fallback) rather than failing the connection.
	if err := applyAdvertiseAddr(&se, resolveAdvertise(advertiseAddr, stunURLs(welcome))); err != nil {
		return nil, err
	}
	api := webrtc.NewAPI(webrtc.WithSettingEngine(se))
	return api.NewPeerConnection(config)
}

// applyAdvertiseAddr turns SignalingConfig.AdvertiseAddr into pion settings:
// a single-port ephemeral range pins every host-candidate socket to the
// advertised port, and NAT1To1 host mode replaces host addresses with the
// public IP. mDNS is disabled explicitly — its .local candidate advertising
// is mutually exclusive with NAT1To1 host mode.
func applyAdvertiseAddr(se *webrtc.SettingEngine, advertiseAddr string) error {
	if advertiseAddr == "" {
		return nil
	}
	host, portStr, err := net.SplitHostPort(advertiseAddr)
	if err != nil {
		return fmt.Errorf("advertise_addr %q: %w", advertiseAddr, err)
	}
	port, err := strconv.ParseUint(portStr, 10, 16)
	if err != nil || port == 0 {
		return fmt.Errorf("advertise_addr %q: udp port must be 1-65535", advertiseAddr)
	}
	if err := se.SetEphemeralUDPPortRange(uint16(port), uint16(port)); err != nil {
		return fmt.Errorf("advertise_addr %q: pin port: %w", advertiseAddr, err)
	}
	se.SetNAT1To1IPs([]string{host}, webrtc.ICECandidateTypeHost)
	se.SetICEMulticastDNSMode(ice.MulticastDNSModeDisabled)
	return nil
}

// ── DataChannel wrapper ─────────────────────────────────────────────────────

func wrapDataChannel(dc *webrtc.DataChannel) DataChannel {
	var mu sync.Mutex
	var msgHandler func([]byte)
	var pending [][]byte

	dc.OnMessage(func(msg webrtc.DataChannelMessage) {
		mu.Lock()
		h := msgHandler
		if h == nil {
			// The remote peer may send tunnel frames before the local
			// application registers a handler (e.g. tunnel.Serve() not yet
			// started). Buffer in arrival order and replay on registration.
			pending = append(pending, msg.Data)
			mu.Unlock()
			return
		}
		mu.Unlock()
		h(msg.Data)
	})

	return DataChannel{
		Label: dc.Label(),
		OnMessage: func(h func([]byte)) {
			mu.Lock()
			msgHandler = h
			buffered := pending
			pending = nil
			mu.Unlock()
			for _, data := range buffered {
				h(data)
			}
		},
		Send: func(data []byte) error {
			return dc.Send(data)
		},
		// Observable SCTP backlog arms the transport's strict-priority
		// egress scheduler (WebRTC-path HOL fix). pion maintains this per
		// DataChannel; nil consumers simply keep passthrough sends.
		BufferedAmount: dc.BufferedAmount,
		Close: func() error {
			return dc.Close()
		},
		OnClose: func(cb func()) {
			dc.OnClose(func() { cb() })
		},
	}
}

// ── sendRaw helpers on signalingClient ──────────────────────────────────────

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
