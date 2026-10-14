package peer

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

// mockSignalServer implements an in-process protocol v1 signaling server for tests.
type mockSignalServer struct {
	server        *httptest.Server
	url           string
	upgrader      websocket.Upgrader
	mu            sync.RWMutex
	writeMu       sync.Mutex // serializes websocket writes (gorilla/websocket is not goroutine-safe for writes)
	peers         map[string]*websocket.Conn
	sessions      map[string]*mockSession
	sessionToPeer map[string]string // Welcome.SessionID -> PeerID for resume

	// pendingReject, when set, makes the mock respond to the next
	// connect_request with a connect_reject instead of forwarding it.
	pendingReject   *ConnectReject
	pendingRejectMu sync.Mutex

	// pings counts app-level pings received per peer (keepalive tests).
	pings map[string]int

	// connectAcceptDelay, when > 0, holds the delivery of connect_accept to
	// the initiator — used by callback-timing tests to assign OnDataChannel
	// after Connect() returns but before the DataChannel can open.
	connectAcceptDelay time.Duration

	// helloGate, when set, acts as the server's JWT policy: a rejected JWT
	// gets the production server's auth-class close (1008 +
	// "authentication_failed" close text) instead of a welcome. Used by the
	// ladder-token tests.
	helloGate func(jwt string) bool

	// hellos counts hello messages received (any outcome).
	hellos int
}

type mockSession struct {
	sessionID string
	initiator string
	target    string
}

func newMockServer(t *testing.T) *mockSignalServer {
	s := &mockSignalServer{
		peers:         make(map[string]*websocket.Conn),
		sessions:      make(map[string]*mockSession),
		sessionToPeer: make(map[string]string),
		pings:         make(map[string]int),
		upgrader: websocket.Upgrader{
			CheckOrigin: func(_ *http.Request) bool { return true },
		},
	}
	s.server = httptest.NewServer(http.HandlerFunc(s.handleWS))
	s.url = "ws" + s.server.URL[4:] // https -> ws
	if t != nil {
		t.Cleanup(s.server.Close)
	}
	return s
}

//nolint:funlen // mock signaling server handler with full handshake logic.
func (s *mockSignalServer) handleWS(w http.ResponseWriter, r *http.Request) {
	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer func() { _ = conn.Close() }()

	// Read first message
	_, raw, err := conn.ReadMessage()
	if err != nil {
		return
	}
	msg, err := ParseAndValidate(raw)
	if err != nil {
		return
	}

	var peerID string
	switch msg.Type {
	case MessageTypeHello:
		var hello Hello
		if err := UnmarshalPayload(msg, &hello); err != nil {
			return
		}
		peerID = hello.PeerID

		s.mu.Lock()
		s.hellos++
		gate := s.helloGate
		s.mu.Unlock()
		if gate != nil && !gate(hello.JWT) {
			s.writeMu.Lock()
			_ = conn.WriteControl(websocket.CloseMessage,
				websocket.FormatCloseMessage(websocket.ClosePolicyViolation, "authentication_failed"),
				time.Now().Add(time.Second))
			s.writeMu.Unlock()
			_ = conn.Close()
			return
		}

		welcome := Welcome{
			SessionID:    uuid.New().String(),
			PeerID:       hello.PeerID,
			MaxPeers:     10,
			MaxDevices:   10,
			RelayEnabled: true,
			ServerTime:   time.Now().Unix(),
		}

		// Register peer and session->peer mapping
		s.mu.Lock()
		s.peers[hello.PeerID] = conn
		s.sessionToPeer[welcome.SessionID] = hello.PeerID
		s.mu.Unlock()

		welcomeData, _ := Marshal(MessageTypeWelcome, welcome)
		s.writeMu.Lock()
		_ = conn.WriteMessage(websocket.TextMessage, welcomeData)
		s.writeMu.Unlock()

	case MessageTypeSignalResume:
		var state SyncState
		if err := UnmarshalPayload(msg, &state); err != nil {
			return
		}
		// Look up the original peer ID from the session mapping.
		s.mu.RLock()
		origPeerID, ok := s.sessionToPeer[state.SessionID]
		s.mu.RUnlock()
		if !ok {
			// Unknown session.
			return
		}
		peerID = origPeerID

		welcome := Welcome{
			SessionID:    uuid.New().String(),
			PeerID:       origPeerID,
			MaxPeers:     10,
			MaxDevices:   10,
			RelayEnabled: true,
			ServerTime:   time.Now().Unix(),
		}

		// Re-register peer under original ID
		s.mu.Lock()
		s.peers[origPeerID] = conn
		s.sessionToPeer[welcome.SessionID] = origPeerID
		s.mu.Unlock()

		welcomeData, _ := Marshal(MessageTypeWelcome, welcome)
		s.writeMu.Lock()
		_ = conn.WriteMessage(websocket.TextMessage, welcomeData)
		s.writeMu.Unlock()

	case MessageTypeWelcome, MessageTypeError,
		MessageTypeConnectRequest, MessageTypeConnectAccept,
		MessageTypeConnectReject, MessageTypeSDPOffer,
		MessageTypeSDPAnswer, MessageTypeICECandidate,
		MessageTypeSessionOpen, MessageTypeSessionClose,
		MessageTypePing, MessageTypePong,
		MessageTypePresenceOnline, MessageTypePresenceOffline,
		MessageTypePeerGone:
		return
	}

	_ = peerID // used above in each branch

	// Relay loop
	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			break
		}
		msg, err := ParseAndValidate(raw)
		if err != nil {
			continue
		}
		if !s.routeMessage(conn, msg) {
			break
		}
	}
}

//nolint:funlen // test mock function
func (s *mockSignalServer) routeMessage(sender *websocket.Conn, msg *Message) bool {
	switch msg.Type {
	case MessageTypeConnectRequest:
		var req ConnectRequest
		if err := UnmarshalPayload(msg, &req); err != nil {
			return true
		}
		// Find sender's peer ID
		senderID := s.findPeerID(sender)
		if senderID == "" {
			return true
		}
		// Register session
		s.mu.Lock()
		s.sessions[req.SessionID] = &mockSession{
			sessionID: req.SessionID,
			initiator: senderID,
			target:    req.TargetPeerID,
		}
		s.mu.Unlock()
		// If a pending reject is set, respond with connect_reject instead.
		if pending := s.consumePendingReject(); pending != nil {
			pending.SessionID = req.SessionID
			rejMsg, _ := Marshal(MessageTypeConnectReject, pending)
			s.writeMu.Lock()
			_ = sender.WriteMessage(websocket.TextMessage, rejMsg)
			s.writeMu.Unlock()
			return true
		}
		// Forward to target
		s.forwardPeer(req.TargetPeerID, msg)
	case MessageTypeConnectAccept:
		var accept ConnectAccept
		if err := UnmarshalPayload(msg, &accept); err != nil {
			return true
		}
		if s.connectAcceptDelay > 0 {
			time.Sleep(s.connectAcceptDelay)
		}
		s.forwardSession(accept.SessionID, sender, msg)
	case MessageTypeConnectReject:
		var reject ConnectReject
		if err := UnmarshalPayload(msg, &reject); err != nil {
			return true
		}
		s.forwardSession(reject.SessionID, sender, msg)
	case MessageTypeSDPOffer:
		var offer SDPOffer
		if err := UnmarshalPayload(msg, &offer); err != nil {
			return true
		}
		s.forwardSession(offer.SessionID, sender, msg)
	case MessageTypeSDPAnswer:
		var answer SDPAnswer
		if err := UnmarshalPayload(msg, &answer); err != nil {
			return true
		}
		s.forwardSession(answer.SessionID, sender, msg)
	case MessageTypeICECandidate:
		var cand ICECandidate
		if err := UnmarshalPayload(msg, &cand); err != nil {
			return true
		}
		s.forwardSession(cand.SessionID, sender, msg)
	case MessageTypeSessionOpen:
		var open SessionOpen
		if err := UnmarshalPayload(msg, &open); err != nil {
			return true
		}
		s.forwardSession(open.SessionID, sender, msg)
	case MessageTypeSessionClose:
		var sc SessionClose
		if err := UnmarshalPayload(msg, &sc); err != nil {
			return true
		}
		s.forwardSession(sc.SessionID, sender, msg)
	case MessageTypePing:
		// Count client keepalive pings for tests. findPeerID takes s.mu
		// itself, so resolve it before acquiring the write lock.
		peerID := s.findPeerID(sender)
		s.mu.Lock()
		s.pings[peerID]++
		s.mu.Unlock()
		return true
	case MessageTypeHello, MessageTypeWelcome, MessageTypeError, MessageTypeSignalResume,
		MessageTypePong, MessageTypePresenceOnline, MessageTypePresenceOffline, MessageTypePeerGone:
		// Already handled during initial handshake or not expected; continue.
		return true
	}
	return true
}

// pingCount returns how many app-level pings the mock received from peerID.
func (s *mockSignalServer) pingCount(peerID string) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.pings[peerID]
}

// forgetSession removes a session id from the resume map so a subsequent
// signal_resume for it is rejected (server forgets the session).
func (s *mockSignalServer) forgetSession(sessionID string) {
	s.mu.Lock()
	delete(s.sessionToPeer, sessionID)
	s.mu.Unlock()
}

func (s *mockSignalServer) forwardSession(sessionID string, sender *websocket.Conn, msg *Message) {
	s.mu.RLock()
	sess, ok := s.sessions[sessionID]
	s.mu.RUnlock()
	if !ok {
		return
	}
	// Forward to the other peer in the session
	otherID := sess.initiator
	if s.findPeerID(sender) == sess.initiator {
		otherID = sess.target
	}
	s.forwardPeer(otherID, msg)
}

func (s *mockSignalServer) forwardPeer(peerID string, msg *Message) {
	s.mu.RLock()
	conn, ok := s.peers[peerID]
	s.mu.RUnlock()
	if !ok {
		return
	}
	raw, _ := Marshal(msg.Type, msg.Payload)
	s.writeMu.Lock()
	_ = conn.WriteMessage(websocket.TextMessage, raw)
	s.writeMu.Unlock()
}

func (s *mockSignalServer) findPeerID(conn *websocket.Conn) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for id, c := range s.peers {
		if c == conn {
			return id
		}
	}
	return ""
}

// sendError pushes a server-level error message to a connected peer.
func (s *mockSignalServer) sendError(peerID, code, msg string) {
	s.mu.RLock()
	conn, ok := s.peers[peerID]
	s.mu.RUnlock()
	if !ok {
		return
	}
	errMsg, _ := Marshal(MessageTypeError, ErrorMsg{Code: code, Message: msg})
	s.writeMu.Lock()
	_ = conn.WriteMessage(websocket.TextMessage, errMsg)
	s.writeMu.Unlock()
}

// rejectNextConnect makes the mock respond to the next connect_request with a
// connect_reject carrying the given code and message, instead of forwarding.
func (s *mockSignalServer) rejectNextConnect(code RejectCode, msg string) {
	s.pendingRejectMu.Lock()
	defer s.pendingRejectMu.Unlock()
	s.pendingReject = &ConnectReject{Code: code, Message: msg}
}

// consumePendingReject returns and clears the pending reject, if any.
func (s *mockSignalServer) consumePendingReject() *ConnectReject {
	s.pendingRejectMu.Lock()
	defer s.pendingRejectMu.Unlock()
	r := s.pendingReject
	s.pendingReject = nil
	return r
}

// dropPeer forcefully closes the server-side connection for a peer, simulating
// a link drop. The client should detect the loss and reconnect; the mock
// accepts the reconnection via its normal hello path.
func (s *mockSignalServer) dropPeer(peerID string) {
	s.mu.Lock()
	conn, ok := s.peers[peerID]
	if ok {
		delete(s.peers, peerID)
	}
	s.mu.Unlock()
	if conn != nil {
		_ = conn.Close()
	}
}
