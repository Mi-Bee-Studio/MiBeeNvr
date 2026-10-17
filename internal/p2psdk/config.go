package peer

import (
	"crypto/tls"
	"log/slog"
	"time"
)

// SignalingConfig configures the connection to the P2P signaling server.
type SignalingConfig struct {
	// ServerURL is the WebSocket URL of the signaling server (e.g. wss://host:port).
	ServerURL string

	// NodesURL optionally points at the platform's public node list
	// (GET /api/nodes, #48). When set, every fresh connection first runs
	// nearest-node discovery (concurrent probes, 50 ms latency buckets
	// with in-bucket shuffle) and dials the winner; ANY discovery failure
	// falls straight back to ServerURL. Empty (default) keeps single-URL
	// behavior byte-identical — zero change for existing deployments.
	NodesURL string

	// JWT is the OIDC JWT for authentication with the signaling server.
	JWT string

	// TokenFunc optionally mints a FRESH JWT on demand (#66). Server tokens
	// are single-use (JTI anti-replay) with a short TTL, so a long-lived
	// peer cannot present one static JWT forever: every initial connect and
	// every reconnect attempt asks TokenFunc for a new token and falls back
	// to the static JWT when it errors or is nil. With TokenFunc set,
	// token_expired / token_replayed server closes re-mint and retry
	// (bounded); without it those closes abandon the reconnect ladder.
	// Implementations own their timeout (the ladder runs uncontexted).
	TokenFunc func() (string, error)

	// ProductID identifies the product namespace for isolation.
	ProductID string

	// PeerID is the unique identifier for this peer within the product.
	PeerID string

	// Role indicates the peer role: "device" or "client".
	Role string

	// DeviceKeyPath optionally points at a raw Ed25519 private key file
	// (32-byte seed or 64-byte full key, 0600). When set (and
	// Role == "device"), every hello is signed
	// over "<sub>:<jti>" from the JWT's own claims — required when the
	// device record on the server has a registered public key (zero-trust
	// identity binding; same key used for re-enroll proof-of-possession).
	DeviceKeyPath string

	// ICETimeouts optionally overrides the pion ICE agent timers (zero
	// value = library defaults, see ICETimeouts). High-jitter links (weak
	// 5G, metro, cross-border 300-500ms RTT) read multi-second p99 spikes
	// as failure: the library defaults (keepalive 2s, connected→
	// disconnected 5s, disconnected→failed 25s) then express as ICE
	// restart churn instead of tolerance. Each field may be set
	// independently; zero fields fall back to the matching default.
	ICETimeouts ICETimeouts

	// ICEReceiveMTU optionally overrides the ICE receive MTU (pion
	// SetReceiveMTU). 0 = library default. Useful on small-MTU paths
	// (PPPoE, some corporate egress) — though without RFC 8829 probing this
	// is a static knob, not negotiation.
	ICEReceiveMTU int

	// TunnelMaxPayloadBytes optionally overrides the tunnel fragment size
	// (0 = MaxPayloadBytes, 32 KiB). Receivers do not validate fragment
	// size, so mixed settings interoperate; capping happens at the sender.
	TunnelMaxPayloadBytes int

	// TLSConfig is an optional TLS configuration for the WebSocket connection.
	// When nil, default TLS settings are used.
	TLSConfig *tls.Config

	// Logger is an optional slog logger. When nil, a no-op logger is used.
	Logger *slog.Logger

	// OnDisconnect is called when the signaling WebSocket link drops, with the
	// error that caused the loss. Always followed by either OnReconnect (link
	// recovered) or session closure. Optional.
	OnDisconnect func(err error)

	// OnReconnect is called after the signaling link is successfully
	// re-established following a drop. Optional.
	OnReconnect func()

	// HandshakeTimeout is the maximum time to wait for the WebSocket handshake
	// and hello/welcome exchange. Zero (default) uses DefaultHandshakeTimeout.
	HandshakeTimeout time.Duration

	// ReadTimeout is the maximum time between WebSocket reads before the link
	// is considered dead. Zero (default) uses DefaultReadTimeout.
	ReadTimeout time.Duration

	// TransportTimeout is how long EstablishTransport waits for ICE to connect
	// before falling back to relay. Zero (default) uses DefaultTransportTimeout.
	TransportTimeout time.Duration

	// ForceRelay pins the transport arbitration to the relay path (#82):
	// the session adopts the relay transport as soon as it is available and
	// never waits for direct ICE. For sites where direct attempts only
	// produce pathological pairs (e.g. CGNAT behind an advertised endpoint)
	// or applications wanting deterministic relay behavior. With no relay
	// client available the stock direct-first arbitration applies unchanged.
	ForceRelay bool

	// AdvertiseAddr binds the local ICE host-candidate
	// socket to the given FIXED UDP port and advertises the given public IP
	// in place of host addresses (pion NAT1To1 host mode). Intended for
	// peers sitting behind a 1:1 DNAT to a reachable public endpoint
	// (dynamic-IP WAN + port forward): the remote side — even behind
	// symmetric NAT, where exchanged srflx candidates are useless — can
	// then go DIRECT by dialing outbound to this stable advertised address
	// (the only hole-punch path surviving endpoint-dependent mapping;
	// validated on the linktest mesh, 2026-10-11). Empty keeps stock
	// gathering. The port must actually reach this process (firewall +
	// DNAT), or ICE falls back to TURN as usual.
	//
	// "auto" / "auto:<port>" (default port 40000) run the zero-config
	// ladder per session build (#43-A/B): UPnP mapping request to the
	// gateway (also opens the WAN pinhole on OpenWrt) → STUN-resolved WAN
	// address with assumed manual forward → "" (relay fallback) on total
	// failure. Result is TTL-cached (5min); a later-enabled router UPnP or
	// a rotated WAN IP is picked up on the next refresh.
	AdvertiseAddr string

	// SessionIdleTimeout is how long a P2P session may carry no traffic
	// (neither direction on the user-facing DataChannel) before the device
	// side tears it down with reason "session_idle". This reclaims sessions
	// of viewers that died without session_close (killed app, black-holed
	// NAT) — the relay-slot leak observed in production (2026-10-08: eight
	// stale client slots). Legitimately idle-but-alive viewers are safe:
	// the reference client's quality loop probes every 15s and tunnel TCP
	// ACKs flow on any open stream. Zero (default) uses
	// DefaultSessionIdleTimeout; negative disables the sweep entirely.
	SessionIdleTimeout time.Duration

	// RelayDialGate is consulted before EVERY relay connect attempt (the
	// Dial-time connect and each mid-life rebuild attempt). Returning
	// false skips that attempt entirely — no socket, no auth handshake,
	// and no OnRelayConnect callback (an attempt that never happened
	// cannot fail). Embedders use this for relay circuit breaking: while
	// the circuit is open the device stays signaling-direct and the stock
	// direct-first arbitration applies unchanged. nil = always dial.
	RelayDialGate func() bool

	// OnRelayConnect reports the outcome of every relay connect attempt:
	// nil on success, else the attempt error. Classify with
	// errors.Is(err, relay.ErrOverloaded) — capacity rejections
	// (server_auth 0x03) are NOT key failures and must not be treated as
	// credentials problems. Fired from the dialing goroutine (Dial or the
	// bounded rebuild loop); keep it non-blocking. Optional.
	OnRelayConnect func(err error)
}

// Package-level defaults for timeouts. All are non-zero so an unconfigured
// client never instant-fails an operation.
const (
	DefaultHandshakeTimeout   = 15 * time.Second
	DefaultReadTimeout        = 30 * time.Second
	DefaultTransportTimeout   = 15 * time.Second
	DefaultSessionIdleTimeout = 15 * time.Minute
)

// handshakeTimeout returns the configured handshake timeout or the default.
func (c SignalingConfig) handshakeTimeout() time.Duration {
	if c.HandshakeTimeout > 0 {
		return c.HandshakeTimeout
	}
	return DefaultHandshakeTimeout
}

// readTimeout returns the configured read timeout or the default.
func (c SignalingConfig) readTimeout() time.Duration {
	if c.ReadTimeout > 0 {
		return c.ReadTimeout
	}
	return DefaultReadTimeout
}

// transportTimeout returns the configured transport timeout or the default.
func (c SignalingConfig) transportTimeout() time.Duration {
	if c.TransportTimeout > 0 {
		return c.TransportTimeout
	}
	return DefaultTransportTimeout
}

// sessionIdleTimeout returns the configured session idle timeout, or the
// default. Negative values pass through unchanged: the sweep treats any
// negative duration as "disabled".
func (c SignalingConfig) sessionIdleTimeout() time.Duration {
	if c.SessionIdleTimeout != 0 {
		return c.SessionIdleTimeout
	}
	return DefaultSessionIdleTimeout
}

// ICETimeouts overrides the pion ICE agent timers (SignalingConfig.
// ICETimeouts). Zero fields fall back to the pion/ice v4.4.x defaults
// below — keep these aligned if pion's agent_config.go ever changes, or
// partial overrides silently drift from "library default".
type ICETimeouts struct {
	KeepaliveInterval   time.Duration
	DisconnectedTimeout time.Duration
	FailedTimeout       time.Duration
}

// pion/ice v4.4.x defaults (agent_config.go): substituted for zero
// ICETimeouts fields so callers may override a single timer.
const (
	ICEDefaultKeepaliveInterval   = 2 * time.Second
	ICEDefaultDisconnectedTimeout = 5 * time.Second
	ICEDefaultFailedTimeout       = 25 * time.Second
)

// DataChannel represents an open DataChannel for sending and receiving data.
type DataChannel struct {
	// Label is the DataChannel label (e.g. "p2p-data").
	Label string

	// OnMessage sets the message handler for received data.
	// Call dc.OnMessage(func(data []byte) { ... }) to receive data.
	OnMessage func(h func([]byte))
	// Send sends data over the DataChannel.
	Send func(data []byte) error

	// SendPriority optionally sends on the underlying transport's
	// high-priority path (relay high lane, #46 v2). When nil, priority
	// sends degrade to Send.
	SendPriority func(data []byte) error

	// BufferedAmount optionally reports bytes accepted by Send but not yet
	// handed to the network by the underlying SCTP stack (pion). When set,
	// the transport wraps egress in a strict-priority scheduler that admits
	// frames below a low watermark — without it a saturating bulk transfer
	// head-of-line blocks every later frame on the single ordered channel.
	// Nil = no scheduling (direct Send passthrough).
	BufferedAmount func() uint64

	// Close closes the DataChannel.
	Close func() error

	// OnClose registers a callback fired when the channel dies (remote close
	// or transport teardown). Applications use it to release resources tied
	// to the channel (e.g. stop the tunnel served over it). May be nil on
	// transports that cannot observe close; register defensively.
	OnClose func(cb func())

	// E2EEKey is the tunnel end-to-end encryption session key derived from
	// this session's relay keypair exchange (nil when unavailable, e.g. the
	// relay fallback was never connected). Applications pass it to
	// tunnel.EnableE2EE to encrypt tunnel payloads end-to-end.
	E2EEKey []byte

	// Ctrl is the paired lossy control channel of the #88① dual-channel
	// direct path (nil on single-channel sessions and on the relay
	// transport). Set by the SDK when the peer negotiated ctrl_channel;
	// the transport built from this DataChannel routes tunnel control
	// frames through it and feeds its inbound messages into the same
	// handler. Applications can ignore it — routing is automatic.
	Ctrl *DataChannel
}
