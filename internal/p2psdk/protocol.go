// Package peer provides the MiBee P2P Go SDK for connecting to the signaling
// server, establishing P2P connections via pion/webrtc with ICE/DTLS
// DataChannels, and exchanging data between peers.
//
// Protocol version: 1.0.0 (wire-compatible with the server's internal/protocol v1).
package peer

import (
	"encoding/json"
	"errors"
	"fmt"
)

// ProtocolVersion is the wire protocol version implemented by this SDK.
const ProtocolVersion = "1.0.0"

// SupportedVersions lists all wire protocol versions this SDK accepts.
var SupportedVersions = []string{"1.0.0"}

// MessageType is a string enum for signaling message types.
type MessageType string

// Signaling message type constants.
const (
	MessageTypeHello          MessageType = "hello"
	MessageTypeWelcome        MessageType = "welcome"
	MessageTypeError          MessageType = "error"
	MessageTypeConnectRequest MessageType = "connect_request"
	MessageTypeConnectAccept  MessageType = "connect_accept"
	MessageTypeConnectReject  MessageType = "connect_reject"
	MessageTypeSDPOffer       MessageType = "sdp_offer"
	MessageTypeSDPAnswer      MessageType = "sdp_answer"
	MessageTypeICECandidate   MessageType = "ice_candidate"
	MessageTypeSessionOpen    MessageType = "session_open"
	MessageTypeSessionClose   MessageType = "session_close"
	MessageTypePing           MessageType = "ping"
	MessageTypePong           MessageType = "pong"
	MessageTypeSignalResume   MessageType = "signal_resume"

	// Server-side presence broadcasts: informational only; the SDK accepts
	// them so ParseAndValidate does not reject frames the server sends to
	// every scope member on join/leave.
	MessageTypePresenceOnline  MessageType = "presence_online"
	MessageTypePresenceOffline MessageType = "presence_offline"
	// MessageTypePeerGone: server-side conclusive death verdict (#63) —
	// the peer's resume window expired; harvest sessions immediately.
	MessageTypePeerGone MessageType = "peer_gone"
)

// Message is the common envelope for all signaling messages.
type Message struct {
	Version string          `json:"version"`
	Type    MessageType     `json:"type"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// RejectCode is a closed enum of wire-level rejection codes.
type RejectCode string

// Rejection code constants.
const (
	RejectCodeUnauthorized RejectCode = "unauthorized"
	RejectCodeEntitlement  RejectCode = "entitlement"
	RejectCodeInternal     RejectCode = "internal"
	// RejectCodeDeviceBusy is sent by a device-side OnConnectRequest gate that
	// refused the inbound connection (e.g. concurrent-client cap reached).
	// The requester receives it verbatim via OnReject.
	RejectCodeDeviceBusy RejectCode = "device_busy"
)

// ── Signaling message payloads ──────────────────────────────────────────────

// Hello is the first message a peer sends after connecting.
type Hello struct {
	ProductID string `json:"product_id"`
	PeerID    string `json:"peer_id"`
	Role      string `json:"role"`
	JWT       string `json:"jwt"`
	AccountID string `json:"account_id,omitempty"`
	// Signature is the base64 Ed25519 signature over "<sub>:<jti>" (claims
	// of the attached JWT), required when the server-side device record has
	// a registered public key. Empty otherwise.
	Signature string `json:"signature,omitempty"`
}

// Welcome is sent by the server after a successful hello handshake.
type Welcome struct {
	SessionID       string         `json:"session_id"`
	PeerID          string         `json:"peer_id"`
	MaxPeers        int            `json:"max_peers"`
	MaxDevices      int            `json:"max_devices"`
	RelayEnabled    bool           `json:"relay_enabled"`
	RelayURL        string         `json:"relay_url,omitempty"`
	RelayPubKey     string         `json:"relay_pub_key,omitempty"`
	ServerTime      int64          `json:"server_time"`
	ICEServers      []ICEServer    `json:"ice_servers,omitempty"`
	Capabilities    map[string]any `json:"capabilities,omitempty"`
	ProtocolVersion string         `json:"protocol_version,omitempty"`
}

// ICEServer describes a STUN or TURN server for ICE connectivity.
type ICEServer struct {
	URLs       []string `json:"urls"`
	Username   string   `json:"username,omitempty"`
	Credential string   `json:"credential,omitempty"`
}

// ErrorMsg is a generic error notification from the server.
type ErrorMsg struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// ConnectRequest is sent by a client to request a P2P session with a target.
type ConnectRequest struct {
	TargetPeerID string `json:"target_peer_id"`
	SessionID    string `json:"session_id"`
	ServiceName  string `json:"service_name,omitempty"`
	// CtrlChannel declares that THIS client understands the dual-channel
	// direct path (#88): a new device-side offerer that sees it may open
	// the p2p-ctrl channel (unordered, unreliable) alongside p2p-data.
	// Additive JSON: old peers never set it and never see a second channel.
	CtrlChannel bool `json:"ctrl_channel,omitempty"`
	// RelayURL/RelayPubKey: cross-node (federation) session relay override
	// injected by the servers — both peers must share ONE relay endpoint.
	RelayURL    string `json:"relay_url,omitempty"`
	RelayPubKey string `json:"relay_pub_key,omitempty"`
	// CallerRelayPubKey is the CALLER's relay identity public key (hex),
	// sent upfront so the device-side offerer can arm tunnel E2EE BEFORE
	// creating the DataChannel — on the direct path the SDP exchange (the
	// historical key carrier) arrives too late for that (mibeescout#16).
	// Distinct from federation's relay_pub_key above, which names the
	// shared relay SERVER key. Additive: old devices ignore it (direct
	// path stays plaintext exactly as before); armed only when BOTH sides
	// are new.
	CallerRelayPubKey string `json:"caller_relay_pub_key,omitempty"`
}

// ConnectAccept is sent by the target to accept a connection request.
type ConnectAccept struct {
	SessionID string `json:"session_id"`
	// RelayURL/RelayPubKey: federation session relay override (same
	// semantics as on ConnectRequest).
	RelayURL    string `json:"relay_url,omitempty"`
	RelayPubKey string `json:"relay_pub_key,omitempty"`
}

// ConnectReject is sent to reject a connection request.
type ConnectReject struct {
	Code      RejectCode `json:"code"`
	Message   string     `json:"message"`
	SessionID string     `json:"session_id"`
}

// SDPOffer carries an SDP offer between peers.
type SDPOffer struct {
	SessionID   string `json:"session_id"`
	SDP         string `json:"sdp"`
	RelayPubKey string `json:"relay_pub_key,omitempty"`
}

// SDPAnswer carries an SDP answer between peers.
type SDPAnswer struct {
	SessionID   string `json:"session_id"`
	SDP         string `json:"sdp"`
	RelayPubKey string `json:"relay_pub_key,omitempty"`
}

// ICECandidate carries a trickle ICE candidate between peers.
type ICECandidate struct {
	SessionID string `json:"session_id"`
	Candidate string `json:"candidate"`
}

// SessionOpen is sent to confirm the P2P session is established.
type SessionOpen struct {
	SessionID string `json:"session_id"`
	PeerID    string `json:"peer_id"`
}

// SessionClose indicates the session has ended.
type SessionClose struct {
	SessionID string `json:"session_id"`
	Reason    string `json:"reason"`
}

// Ping is an app-level keepalive message sent by the server.
type Ping struct {
	Timestamp int64 `json:"timestamp"`
}

// Pong is the app-level keepalive response sent by the client.
type Pong struct {
	Timestamp int64 `json:"timestamp"`
}

// SyncState carries resumption state for reconnection.
type SyncState struct {
	SessionID   string `json:"session_id"`
	LastSeenSeq uint64 `json:"last_seen_seq"`
}

// PeerGone is the server's conclusive-death verdict for a peer (#63): its
// resume window expired. Sessions riding that peer should be torn down
// immediately — a resume attempt against it cannot succeed.
//
//nolint:revive // name parity with the server protocol type (v1.md §2.16a)
type PeerGone struct {
	PeerID    string `json:"peer_id"`
	SessionID string `json:"session_id,omitempty"`
	Reason    string `json:"reason,omitempty"`
}

// ── Marshal / Parse helpers ─────────────────────────────────────────────────

// Marshal creates a JSON byte slice from a message type and payload.
func Marshal(msgType MessageType, payload any) ([]byte, error) {
	payloadRaw, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal payload: %w", err)
	}
	msg := Message{
		Version: ProtocolVersion,
		Type:    msgType,
		Payload: payloadRaw,
	}
	data, err := json.Marshal(msg)
	if err != nil {
		return nil, fmt.Errorf("marshal message: %w", err)
	}
	return data, nil
}

// ParseAndValidate parses a raw JSON message and validates version and type.
func ParseAndValidate(raw []byte) (*Message, error) {
	if len(raw) == 0 {
		return nil, errors.New("empty message")
	}
	var msg Message
	if err := json.Unmarshal(raw, &msg); err != nil {
		return nil, fmt.Errorf("parse message: %w", err)
	}
	// Validate version
	versionOk := false
	for _, v := range SupportedVersions {
		if msg.Version == v {
			versionOk = true
			break
		}
	}
	if !versionOk {
		return nil, fmt.Errorf("unsupported protocol version: %s", msg.Version)
	}
	// Validate message type
	switch msg.Type {
	case MessageTypeHello, MessageTypeWelcome, MessageTypeError,
		MessageTypeConnectRequest, MessageTypeConnectAccept, MessageTypeConnectReject,
		MessageTypeSDPOffer, MessageTypeSDPAnswer, MessageTypeICECandidate,
		MessageTypeSessionOpen, MessageTypeSessionClose,
		MessageTypePing, MessageTypePong, MessageTypeSignalResume,
		MessageTypePresenceOnline, MessageTypePresenceOffline, MessageTypePeerGone:
		// valid
	default:
		return nil, fmt.Errorf("invalid message type: %s", msg.Type)
	}
	return &msg, nil
}

// UnmarshalPayload unmarshals the payload of a parsed Message into the target.
func UnmarshalPayload(msg *Message, target any) error {
	if msg.Payload == nil {
		return errors.New("missing payload")
	}
	if err := json.Unmarshal(msg.Payload, target); err != nil {
		return fmt.Errorf("unmarshal payload: %w", err)
	}
	return nil
}
