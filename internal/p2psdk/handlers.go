package peer

import (
	"context"
	"encoding/hex"
	"log/slog"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/p2psdk/relay"
)

// handleMessage dispatches a parsed signaling message to the right handler.
//
//nolint:gocyclo // one-line-per-case message dispatcher; merging cases would change log texts
func (s *Session) handleMessage(raw []byte) {
	msg, err := ParseAndValidate(raw)
	if err != nil {
		s.logger.Warn("invalid message", "error", err)
		return
	}
	switch msg.Type {
	case MessageTypeConnectRequest:
		s.handleConnectRequest(msg)
	case MessageTypeConnectAccept:
		s.handleConnectAccept(msg)
	case MessageTypeConnectReject:
		s.handleConnectReject(msg)
	case MessageTypeSDPOffer:
		s.handleSDPOffer(msg)
	case MessageTypeSDPAnswer:
		s.handleSDPAnswer(msg)
	case MessageTypeICECandidate:
		s.handleICECandidate(msg)
	case MessageTypeSessionOpen:
		s.handleSessionOpen(msg)
	case MessageTypeSessionClose:
		s.handleSessionClose(msg)
	case MessageTypeError:
		s.handleError(msg)
	case MessageTypePing:
		s.handlePing(msg)
	case MessageTypePong:
		s.logger.Debug("unexpected pong message")
	case MessageTypeSignalResume:
		s.logger.Debug("unexpected signal_resume message")
	case MessageTypeHello:
		s.logger.Debug("unexpected hello message")
	case MessageTypeWelcome:
		s.logger.Debug("unexpected welcome message")
	case MessageTypePresenceOnline, MessageTypePresenceOffline:
		// Server scope broadcasts on peer join/leave: informational only.
		s.logger.Debug("presence broadcast", "type", msg.Type)
	case MessageTypePeerGone:
		s.handlePeerGone(msg)
	default:
		s.logger.Debug("unhandled message type", "type", msg.Type)
	}
}

// handlePeerGone harvests sessions whose remote peer the server declared
// conclusively dead (#63: resume window expired without a reclaim). This
// is the device-side half of fast recovery after phone Doze kills a
// client — without it, dead-viewer sessions linger until the local idle
// sweep (default 15 min) while new connections pile onto the device's
// concurrent-session budget.
func (s *Session) handlePeerGone(msg *Message) {
	var gone PeerGone
	if err := UnmarshalPayload(msg, &gone); err != nil {
		s.logger.Warn("bad peer_gone", "error", err)
		return
	}
	s.logger.Info("peer conclusively offline, harvesting its sessions",
		"peer_id", gone.PeerID,
		"session_id", gone.SessionID,
		"reason", gone.Reason,
	)

	s.mu.RLock()
	var victims []*p2pSession
	for id, p2p := range s.peers {
		if gone.SessionID != "" && id != gone.SessionID {
			continue
		}
		if p2p.remotePeerID == gone.PeerID || gone.SessionID == id {
			victims = append(victims, p2p)
		}
	}
	s.mu.RUnlock()

	for _, p2p := range victims {
		p2p.teardownWithReason("peer_gone")
	}
}

func (s *Session) handleConnectRequest(msg *Message) {
	var req ConnectRequest
	if err := UnmarshalPayload(msg, &req); err != nil {
		s.logger.Warn("bad connect_request", "error", err)
		return
	}

	s.logger.Info(
		"incoming connect request",
		"session_id", req.SessionID,
	)

	// Admission gate: refuse before any WebRTC machinery starts when the
	// application says so (e.g. concurrent-client cap reached).
	if gate := s.connectRequestCallback(); gate != nil &&
		!gate(IncomingConnect{
			SessionID:    req.SessionID,
			TargetPeerID: req.TargetPeerID,
			ServiceName:  req.ServiceName,
		}) {
		reject := ConnectReject{
			Code:      RejectCodeDeviceBusy,
			Message:   "device refused the connection (busy or over capacity)",
			SessionID: req.SessionID,
		}
		if data, err := Marshal(MessageTypeConnectReject, reject); err == nil {
			if err := s.signal.send(data); err != nil {
				s.logger.Warn("send device_busy reject", "error", err)
			}
		}
		s.logger.Info("connect request rejected by device admission gate",
			"session_id", req.SessionID,
			"service", req.ServiceName)
		return
	}

	// Federation cross-node sessions override the relay endpoint so both
	// peers share one relay; re-point BEFORE the session copies the client.
	if req.RelayURL != "" && req.RelayPubKey != "" {
		s.repointRelay(req.RelayURL, req.RelayPubKey)
	}

	// Caller's relay identity key (mibeescout#16): arming the session
	// BEFORE the first DataChannel exists is the whole point — on the
	// direct path the offerer builds the channel ahead of the SDP
	// exchange, so the historical sdp-offer key carrier is too late and
	// e2eeKey() would silently return nil (plaintext direct tunnels).
	var callerRelayKey [32]byte
	if req.CallerRelayPubKey != "" {
		if key, err := hex.DecodeString(req.CallerRelayPubKey); err == nil && len(key) == 32 {
			copy(callerRelayKey[:], key)
		} else {
			s.logger.Warn("connect_request carried an undecodable caller_relay_pub_key",
				"session_id", req.SessionID)
		}
	}

	p2p := &p2pSession{
		sessionID:      req.SessionID,
		remotePeerID:   req.TargetPeerID,
		isOfferer:      true,
		remoteRelayKey: callerRelayKey,
		logger:         s.logger,
		owner:          s,
		onStats:        s.OnStats,
		relayClient:    s.relayClient,
		ctrlWanted:     req.CtrlChannel, // #88① dual-channel gate (requester-declared)
	}
	// 活跃度锚点=创建时刻：从未承载流量的会话（握手即死）也会被
	// 空闲清扫按超时回收，而不是永久滞留名册。
	p2p.touch()

	s.mu.Lock()
	s.peers[req.SessionID] = p2p
	s.mu.Unlock()

	go func() {
		if err := p2p.startAsOfferer(s.signal, s.sessionConfig(), s.welcome); err != nil {
			s.logger.Error("p2p as offerer failed", "error", err)
			s.mu.Lock()
			delete(s.peers, req.SessionID)
			s.mu.Unlock()
		}
	}()
}

func (s *Session) handleConnectAccept(msg *Message) {
	var accept ConnectAccept
	if err := UnmarshalPayload(msg, &accept); err != nil {
		s.logger.Warn("bad connect_accept", "error", err)
		return
	}
	s.logger.Info("connect accepted", "session_id", accept.SessionID)

	p2p := s.getP2P(accept.SessionID)
	if p2p == nil {
		s.logger.Warn("connect_accept for unknown session", "session_id", accept.SessionID)
		return
	}

	// Federation cross-node relay override: re-point before the answer SDP
	// picks up this session's relay identity.
	if accept.RelayURL != "" && accept.RelayPubKey != "" {
		s.repointRelay(accept.RelayURL, accept.RelayPubKey)
		p2p.setRelayClient(s.relayClient)
	}

	// Synchronous, not a goroutine (#89): the server relays connect_accept
	// and sdp_offer back-to-back; with startAsAnswerer deferred to a
	// goroutine, the read loop can process the offer before the
	// PeerConnection exists — handleRemoteOffer hits a nil pc and the offer
	// is dropped, killing the handshake (both sides then eat the transport
	// arbitration timeout). Creating the PC inline closes that window —
	// handleRemoteOffer already runs synchronously on the read loop, same
	// precedent. The offerer stays async: its accept/offer are strictly
	// ordered after PC creation by protocol, so the window does not exist
	// there.
	if err := p2p.startAsAnswerer(s.signal, s.sessionConfig(), s.welcome); err != nil {
		s.logger.Error("p2p as answerer failed", "error", err)
		if deleted := s.deleteP2P(accept.SessionID); deleted != nil {
			_ = deleted.close()
		}
	}
}

func (s *Session) handleConnectReject(msg *Message) {
	var reject ConnectReject
	if err := UnmarshalPayload(msg, &reject); err != nil {
		s.logger.Warn("bad connect_reject", "error", err)
		return
	}

	p2p := s.getP2P(reject.SessionID)
	if p2p != nil {
		if cb := p2p.owner.rejectCallback(); cb != nil {
			cb(reject.Code, reject.Message)
		}
	}
	if deleted := s.deleteP2P(reject.SessionID); deleted != nil {
		_ = deleted.close()
	}

	s.logger.Info(
		"connect rejected",
		"session_id", reject.SessionID,
		"code", reject.Code,
	)
}

func (s *Session) handleSDPOffer(msg *Message) {
	var offer SDPOffer
	if err := UnmarshalPayload(msg, &offer); err != nil {
		s.logger.Warn("bad sdp_offer", "error", err)
		return
	}

	p2p := s.getP2P(offer.SessionID)
	if p2p == nil {
		s.logger.Warn("sdp_offer for unknown session", "session_id", offer.SessionID)
		return
	}

	// Extract remote peer's relay public key.
	if offer.RelayPubKey != "" {
		key, err := hex.DecodeString(offer.RelayPubKey)
		if err == nil && len(key) == 32 {
			p2p.mu.Lock()
			copy(p2p.remoteRelayKey[:], key)
			p2p.mu.Unlock()
		}
	}

	if err := p2p.handleRemoteOffer(offer.SDP, s.signal); err != nil {
		s.logger.Error("handle remote offer", "error", err)
	}
}

func (s *Session) handleSDPAnswer(msg *Message) {
	var answer SDPAnswer
	if err := UnmarshalPayload(msg, &answer); err != nil {
		s.logger.Warn("bad sdp_answer", "error", err)
		return
	}

	p2p := s.getP2P(answer.SessionID)
	if p2p == nil {
		s.logger.Warn("sdp_answer for unknown session", "session_id", answer.SessionID)
		return
	}

	// Extract remote peer's relay public key (under p.mu: e2eeKey() reads
	// the same field from channel-setup paths — an unlocked copy here was
	// a latent data race exposed by the direct-E2EE regression test).
	if answer.RelayPubKey != "" {
		key, err := hex.DecodeString(answer.RelayPubKey)
		if err == nil && len(key) == 32 {
			p2p.mu.Lock()
			copy(p2p.remoteRelayKey[:], key)
			p2p.mu.Unlock()
		}
	}

	if err := p2p.handleRemoteAnswer(answer.SDP); err != nil {
		s.logger.Error("handle remote answer", "error", err)
	}
}

func (s *Session) handleICECandidate(msg *Message) {
	var cand ICECandidate
	if err := UnmarshalPayload(msg, &cand); err != nil {
		s.logger.Warn("bad ice_candidate", "error", err)
		return
	}

	p2p := s.getP2P(cand.SessionID)
	if p2p == nil {
		s.logger.Debug("ice_candidate for unknown session", "session_id", cand.SessionID)
		return
	}
	if err := p2p.handleRemoteICECandidate(cand.Candidate); err != nil {
		s.logger.Debug("add ice candidate", "error", err)
	}
}

func (s *Session) handleSessionOpen(msg *Message) {
	var open SessionOpen
	if err := UnmarshalPayload(msg, &open); err != nil {
		s.logger.Warn("bad session_open", "error", err)
		return
	}
	s.logger.Info(
		"session opened",
		"session_id", open.SessionID,
		"peer_id", open.PeerID,
	)
	// Peer-side relay announcement (Go SDK / Dart clients send session_open
	// the moment they adopt the relay transport): cut our own ICE arbitration
	// short and follow — waiting out the full window while the peer already
	// sits on relay is pure dead time (20s in production measurements).
	// No-op when arbitration already decided (direct won / relay active) or
	// the kick channel was never armed.
	if p2p := s.getP2P(open.SessionID); p2p != nil {
		p2p.kickRelay()
	}
}

func (s *Session) handleSessionClose(msg *Message) {
	var closeMsg SessionClose
	if err := UnmarshalPayload(msg, &closeMsg); err != nil {
		s.logger.Warn("bad session_close", "error", err)
		return
	}
	if deleted := s.deleteP2P(closeMsg.SessionID); deleted != nil {
		_ = deleted.close()
	}
	if cb := s.sessionCloseCallback(); cb != nil {
		cb(closeMsg.SessionID, closeMsg.Reason)
	}
	s.logger.Info(
		"session closed",
		"session_id", closeMsg.SessionID,
		"reason", closeMsg.Reason,
	)
}

func (s *Session) handleError(msg *Message) {
	var errMsg ErrorMsg
	if err := UnmarshalPayload(msg, &errMsg); err != nil {
		return
	}
	s.logger.Warn(
		"server error",
		"code", errMsg.Code,
		"message", errMsg.Message,
	)
	if s.OnError != nil {
		s.OnError(errMsg.Code, errMsg.Message)
	}
}

// handlePing responds to a ping message with a pong.
func (s *Session) handlePing(msg *Message) {
	var ping Ping
	if err := UnmarshalPayload(msg, &ping); err != nil {
		s.logger.Warn("bad ping", "error", err)
		return
	}
	pong := Pong(ping)
	pongData, err := Marshal(MessageTypePong, pong)
	if err != nil {
		s.logger.Warn("marshal pong", "error", err)
		return
	}
	if err := s.signal.send(pongData); err != nil {
		s.logger.Warn("send pong", "error", err)
	}
}

// ensure *Session implements a common interface for logging.
var _ = (*Session)(nil)

// silence unused import warning for slog in this file.
var _ = slog.Default

// repointRelay re-targets the session's fallback-relay connection at a
// federation cross-node relay endpoint. The previous client is NOT closed:
// concurrent local sessions may still ride it, and it dies with the Session.
func (s *Session) repointRelay(serverURL, serverPubKeyHex string) {
	rc := relay.NewClient()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := rc.Connect(ctx, serverURL, serverPubKeyHex); err != nil {
		s.logger.Warn("relay override connect failed (keeping node default)",
			"url", serverURL, "error", err)
		return
	}
	s.mu.Lock()
	s.relayClient = rc
	s.mu.Unlock()
	s.logger.Info("relay re-pointed for cross-node session", "url", serverURL)
}

// setRelayClient swaps a pending session's relay client (pre-start only).
func (p *p2pSession) setRelayClient(rc *relay.Client) {
	p.mu.Lock()
	p.relayClient = rc
	p.mu.Unlock()
}
