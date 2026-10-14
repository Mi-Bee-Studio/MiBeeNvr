package peer

import (
	"errors"

	"github.com/gorilla/websocket"
)

// Sentinel errors for branching on failure cause via errors.Is. Internal code
// wraps these with fmt.Errorf("...: %w", err); callers match the root cause
// without parsing error strings.
//
// Mirrors the three RejectCode values the server can send (unauthorized →
// ErrAuthFailed, entitlement → ErrInvalidConfig, internal → ErrICETimeout or
// the specific failure) plus connection-lifecycle causes.
var (
	// ErrInvalidConfig is returned when SignalingConfig is missing a required
	// field or has an invalid value (bad Role, empty URL, etc.).
	ErrInvalidConfig = errors.New("invalid configuration")

	// ErrAuthFailed is returned when the signaling server rejects the JWT or
	// the connection is closed with an authentication error. Corresponds to a
	// connect_reject with code "unauthorized" or a hello-time rejection.
	ErrAuthFailed = errors.New("authentication failed")

	// ErrSignalingDisconnected is returned/propagated when the WebSocket link
	// to the signaling server drops and cannot be re-established.
	ErrSignalingDisconnected = errors.New("signaling connection lost")

	// ErrICETimeout is returned when ICE candidate gathering or connectivity
	// checks exceed the configured timeout and no relay fallback is available.
	ErrICETimeout = errors.New("ICE timed out and no relay available")

	// ErrRelayUnavailable indicates the relay server was not reachable or not
	// advertised by the signaling server, so no fallback path exists.
	ErrRelayUnavailable = errors.New("relay unavailable")

	// ErrSessionNotFound is returned when an operation targets a session ID
	// that does not exist (already closed or never established).
	ErrSessionNotFound = errors.New("session not found")

	// ErrProtocolMismatch is returned when the server's protocol version is
	// not in the client's SupportedVersions list.
	ErrProtocolMismatch = errors.New("protocol version mismatch")
)

// CloseInfo extracts the WebSocket close code and reason from an error chain
// produced by Dial or OnDisconnect. ok is false when err carries no websocket
// close error (e.g. a plain network failure). Applications use this to react
// to server policy closes (1008 handshake rejections, 4003 mid-session
// license revocations) instead of string-matching error text.
func CloseInfo(err error) (code int, reason string, ok bool) {
	var ce *websocket.CloseError
	if errors.As(err, &ce) {
		return ce.Code, ce.Text, true
	}
	return 0, "", false
}
