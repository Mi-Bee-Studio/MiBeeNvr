// Package relay provides a DERP-like relay client for P2P connectivity
// when direct ICE/DTLS connections are unavailable.
package relay

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"golang.org/x/crypto/nacl/box"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/p2psdk/e2ee"
)

// Frame types replicated from internal/relay/frame.go.
const (
	FrameClientAuth byte = 0x01
	FrameServerAuth byte = 0x02
	FrameSendPacket byte = 0x03
	FrameRecvPacket byte = 0x04
	FrameKeepAlive  byte = 0x05
	FramePing       byte = 0x06
	FramePong       byte = 0x07

	// FramePriorityBit (0x80): OR-ed into send/recv types marks high priority;
	// FrameCapPriority is the server_auth capability byte (byte 1). Never send
	// priority-marked frames unless SupportsPriority() is true — legacy servers
	// drop unknown types silently.
	FramePriorityBit byte = 0x80
	FrameCapPriority byte = 0x01
	// FrameCapServerAuth (capability byte bit 1, #64): the success payload
	// carries a server-identity proof after the capability byte —
	// [24B proof nonce][48B box] sealed with the server static key to this
	// client's public key over sha256(clientNonce ‖ clientPub).
	FrameCapServerAuth byte = 0x02
	// FrameCapDualConn (capability byte bit 2, #88②): the server accepts a
	// second ctrl connection per key — client_auth appends 1 byte
	// conn_type (0x01) after the 109-byte legacy payload. Priority frames
	// are delivered to the recipient's ctrl connection when present.
	FrameCapDualConn byte = 0x04

	// FrameThrottle (0x08) arrives server → client on the control lane when
	// the server's inbound rate limit dropped this connection's data frames
	// (#69). Payload: 8B BE [4B dropped frames][4B dropped bytes] over the
	// server's rate window.
	FrameThrottle byte = 0x08

	// FrameRestarting (0x09, #106-B): the server is shutting down
	// gracefully. Payload: 2B BE retry_after_ms — the hinted re-dial delay.
	// Followed by close 4009 relay_restarting.
	FrameRestarting byte = 0x09

	// FramePeerGone (0x0A, #106-B): a peer this client recently routed with
	// left the relay. Payload: [32B peer pubkey][1B reason] (0x00 conn lost,
	// 0x01 idle reaped, 0x02 rate killed). Session owners should tear down
	// sessions bound to that key and redial immediately.
	FramePeerGone byte = 0x0A
)

// Close status 4009 (#106-B): the relay is restarting — transient, re-dial
// on the hinted fast path instead of the exponential ladder.
const closeStatusRestarting = 4009

// FramePeerGone reason codes (payload byte after the 32B peer key) —
// mirror of the server-side values (relay.md §6.2).
const (
	PeerGoneConnLost   byte = 0x00
	PeerGoneIdleReaped byte = 0x01
	PeerGoneRateKilled byte = 0x02
)

// PeerGoneReasonString maps a FramePeerGone reason to its wire name
// (diagnostics; unknown codes render hex).
func PeerGoneReasonString(reason byte) string {
	switch reason {
	case PeerGoneConnLost:
		return "conn_lost"
	case PeerGoneIdleReaped:
		return "idle_reaped"
	case PeerGoneRateKilled:
		return "rate_killed"
	default:
		return fmt.Sprintf("unknown(0x%02x)", reason)
	}
}

// Protocol constants.
const (
	FrameHeaderLen = 5     // 1 byte type + 4 byte length (big-endian)
	KeyLen         = 32    // X25519 public key length
	NonceLen       = 24    // NaCl box nonce length
	MaxPacketSize  = 65536 // maximum payload size
)

// Client implements the peer.Transport interface over a WebSocket relay.
// It connects to a relay server, authenticates with NaCl box, and
// forwards encrypted packets between peers without server-side decryption.
type Client struct {
	conn      *websocket.Conn
	onMessage func([]byte)
	// routes are per-source inbound handlers: with several P2P sessions
	// sharing this one relay connection, packets must be dispatched by the
	// sender's key, not to whichever handler registered last (multi-session
	// cross-talk, found by the 2026-10-09 tri-party patrol).
	routes  map[[32]byte]func([]byte)
	onFatal func(error)
	// onThrottle fires when the server reports dropped data frames due to
	// the inbound rate limit (#69) — video callers should step the bitrate
	// down. At most once per server-side second; nil = ignored.
	onThrottle func(droppedFrames, droppedBytes uint32)
	// onPeerGone fires when the server reports a recently-routed peer's
	// connection ending (#106-B). Session owners tear down sessions bound
	// to that key and redial; nil = ignored (old behavior: wait out the
	// pong/idle timeouts).
	onPeerGone func(peer [32]byte, reason byte)

	closeCh chan struct{}

	// Async egress with a control lane (#46): keepalive pings and pong
	// replies jump the data lane so liveness frames never sit behind bulk
	// tunnel traffic in the write lock; data frames stay strictly FIFO
	// (relay frame order is a protocol contract — no data frame may ever
	// be reordered). One writer goroutine performs all socket writes.
	// #46 v2 adds the high lane for priority-marked frames (negotiated).
	ctrlCh chan []byte
	hiCh   chan []byte
	dataCh chan []byte

	// writeSync substitutes the socket-write primitive in tests.
	writeSync func([]byte) error

	// logger is nil-safe diagnostics output (defaults to slog.Default()).
	loggerField *slog.Logger
	// ctrl is the #88② physically separate control connection (nil until
	// the dual-connection dial succeeds; soft-degrades on death).
	ctrl atomic.Pointer[ctrlLink]
	// serverURL remembers the endpoint for the #88② ctrl dial. Kept last
	// among the pointers: its trailing string-len word falls out of the
	// GC-scanned prefix (fieldalignment).
	serverURL string

	pubKey       [32]byte
	privKey      [32]byte
	serverPubKey [32]byte
	remotePubKey [32]byte

	mu        sync.Mutex
	writeMu   sync.Mutex
	lastRead  atomic.Int64 // unix nanos of the last inbound frame
	dataBytes atomic.Int64 // bytes queued on the data lanes

	// Liveness knobs (overridable in tests). Defaults below.
	pingInterval time.Duration
	pongTimeout  time.Duration
	writeTimeout time.Duration

	// fatalOnce gates the single fatal teardown; the 12-byte sync.Once
	// packs with serverCaps in the struct tail (fieldalignment).
	fatalOnce sync.Once
	// serverCaps carries the capability byte from server_auth (0 when the
	// server is legacy 1-byte). Governed by mu; governs SendPriority gating.
	serverCaps byte
}

// logger returns the client's diagnostics logger (never nil).
func (c *Client) logger() *slog.Logger {
	if c.loggerField != nil {
		return c.loggerField
	}
	return slog.Default()
}

// Default liveness timing. The relay protocol answers FramePing with
// FramePong (internal/relay/server.go), so a client-side ping cadence plus
// a read deadline turns a silently-dead NAT mapping (half-open TCP: no
// FIN, no RST — e.g. after a home-broadband PPPoE re-dial) into a detectable
// failure within ~2 intervals instead of an eternal black hole.
const (
	defaultPingInterval = 20 * time.Second
	defaultPongTimeout  = 45 * time.Second // > 2× ping interval
	defaultWriteTimeout = 10 * time.Second
)

// Egress queue bounds. Overflow fails the transport (session-level rebuild
// per the relay contract): dropping a queued tunnel frame would corrupt an
// ordered stream, so backlog beyond these caps means the link is dead or
// drastically oversubscribed.
const (
	clientDataQueueFrames = 64
	clientDataQueueBytes  = 4 << 20
	clientHighQueueFrames = 32
	clientCtrlQueueFrames = 16
)

// clientDataQueueWait 是车道满/字节超限时等待排空的上限：慢而活着
// 的链路（弱蜂窝中继视频）必须转化为发送方背压而非连接判死——
// 生产实测（2026-10-14）立即判死使中继视频会话 ~15s 必死。真正的
// 死链由 keepalive pong 超时与写错误检出；这里只兜「零排空进展」
// 的极端停滞（var：测试可缩短）。
var clientDataQueueWait = 45 * time.Second

// ErrOverloaded reports server_auth status 0x03: the relay is at capacity.
// Back off and retry later (or use another node) — this is NOT an auth
// failure: the session's keys remain valid. Legacy servers signal the same
// condition with 0x01 (indistinguishable from a crypto failure).
var ErrOverloaded = errors.New("relay overloaded (capacity)")

// RestartingError reports a server-initiated graceful restart (#106-B):
// FrameRestarting or close 4009. The transport is dead (fail the same way
// as any fatal), but the reconnection policy should use RetryAfter (+ small
// jitter) instead of the full exponential ladder — the server expects to be
// back within the hint.
type RestartingError struct {
	// RetryAfter is the server's re-dial hint (from the frame payload; a
	// conservative default when only the close code was observed).
	RetryAfter time.Duration
}

func (e *RestartingError) Error() string {
	return fmt.Sprintf("relay restarting (retry after %s)", e.RetryAfter)
}

// defaultRestartingRetryAfter is used when the 4009 close arrives without
// a preceding FrameRestarting (ctrl lane happened to drop it).
const defaultRestartingRetryAfter = 2 * time.Second

// RetryHint extracts the server's restart re-dial hint from an error chain
// (#106-B). ok=false for every other error.
func RetryHint(err error) (time.Duration, bool) {
	var re *RestartingError
	if errors.As(err, &re) {
		return re.RetryAfter, true
	}
	return 0, false
}

// NewClient creates an unconnected relay client.
func NewClient() *Client {
	// Identity keys are minted EAGERLY (mibeescout#16): the caller's relay
	// public key must be available before any relay connection exists —
	// it rides connect_request to arm direct-path E2EE. A Client's relay
	// identity is now stable across reconnects, matching the server-side
	// static-key semantics (#55).
	pub, priv := GenerateKeyPair()
	c := &Client{
		closeCh:      make(chan struct{}),
		ctrlCh:       make(chan []byte, clientCtrlQueueFrames),
		hiCh:         make(chan []byte, clientHighQueueFrames),
		dataCh:       make(chan []byte, clientDataQueueFrames),
		pingInterval: defaultPingInterval,
		pongTimeout:  defaultPongTimeout,
		writeTimeout: defaultWriteTimeout,

		pubKey:  pub,
		privKey: priv,
	}
	return c
}

// SetRemotePeer sets the remote peer's relay public key for Send operations.
// Single-peer convenience (CLI); multi-session sharing must use SendTo +
// OnPacket instead so sessions never fight over one mutable destination.
func (c *Client) SetRemotePeer(pubKey [32]byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.remotePubKey = pubKey
}

// SendTo sends data to an explicit remote peer. This is the multi-session
// safe form: the destination is a parameter, not shared client state.
func (c *Client) SendTo(dst [32]byte, data []byte) error {
	if len(data) > MaxPacketSize-KeyLen {
		return fmt.Errorf("payload too large: %d", len(data))
	}
	payload := make([]byte, KeyLen+len(data))
	copy(payload[:KeyLen], dst[:])
	copy(payload[KeyLen:], data)
	return c.enqueueData(encodeFrame(FrameSendPacket, payload), false)
}

// SupportsPriority reports whether the connected server advertised the
// priority capability in server_auth.
func (c *Client) SupportsPriority() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.serverCaps&FrameCapPriority != 0
}

// PriorityActive is the T2 lane-truth hook (#87): without the server's
// capability bit, SendPriority silently degrades to the normal path, and
// probe samples must not be read as "priority effective".
func (c *Client) PriorityActive() bool { return c.SupportsPriority() }

// SupportsDualConn reports whether the server advertised the #88②
// dual-connection capability (ctrl lane).
func (c *Client) SupportsDualConn() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.serverCaps&FrameCapDualConn != 0
}

// enqueueCtrl queues a control-lane frame (ping/pong). Best effort: a
// dropped liveness frame only delays death detection by one interval.
func (c *Client) enqueueCtrl(frame []byte) {
	select {
	case c.ctrlCh <- frame:
	case <-c.closeCh:
	default:
	}
}

// enqueueData queues a data-lane frame in strict FIFO order within its lane
// (hi = high lane, #46 v2). 队列满/字节超限时**有界阻塞**等排空（发送
// 方背压，保住慢而活着的链路）；仅当 clientDataQueueWait 内零排空进展
// 才判死传输（真死链另有 keepalive 与写错误检出）。
func (c *Client) enqueueData(frame []byte, hi bool) error {
	ch := c.dataCh
	if hi {
		ch = c.hiCh
	}
	deadline := time.Now().Add(clientDataQueueWait)
	for {
		if c.dataBytes.Load()+int64(len(frame)) > clientDataQueueBytes {
			if time.Now().After(deadline) {
				err := fmt.Errorf("relay egress stalled over byte cap for %v", clientDataQueueWait)
				c.fail(err)
				return err
			}
			time.Sleep(20 * time.Millisecond)
			continue
		}
		select {
		case ch <- frame:
			c.dataBytes.Add(int64(len(frame)))
			return nil
		case <-c.closeCh:
			return errors.New("relay client closed")
		default:
			if time.Now().After(deadline) {
				err := errors.New("relay egress stalled over frame cap")
				c.fail(err)
				return err
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
}

// writerLoop is the single egress writer: control frames drain with
// priority; within each lane order is strictly FIFO. The socket-write
// primitive is injectable for tests.
func (c *Client) writerLoop() {
	w := c.writeSync
	if w == nil {
		w = c.writeFrame
	}
	for {
		select {
		case b := <-c.ctrlCh:
			if err := w(b); err != nil {
				c.fail(fmt.Errorf("relay egress write: %w", err))
				return
			}
			continue
		default:
		}
		select {
		case b := <-c.hiCh:
			if err := w(b); err != nil {
				c.fail(fmt.Errorf("relay egress write: %w", err))
				return
			}
			continue
		default:
		}
		select {
		case b := <-c.ctrlCh:
			if err := w(b); err != nil {
				c.fail(fmt.Errorf("relay egress write: %w", err))
				return
			}
		case b := <-c.hiCh:
			if err := w(b); err != nil {
				c.fail(fmt.Errorf("relay egress write: %w", err))
				return
			}
		case b := <-c.dataCh:
			c.dataBytes.Add(int64(-len(b)))
			if err := w(b); err != nil {
				c.fail(fmt.Errorf("relay egress write: %w", err))
				return
			}
		case <-c.closeCh:
			return
		}
	}
}

// OnPacket registers an inbound handler for packets from one specific
// source key. Takes precedence over the global OnMessage handler.
func (c *Client) OnPacket(src [32]byte, h func([]byte)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.routes == nil {
		c.routes = make(map[[32]byte]func([]byte))
	}
	c.routes[src] = h
}

// RemovePacketRoute drops a per-source handler (session teardown).
func (c *Client) RemovePacketRoute(src [32]byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.routes, src)
}

// Connect dials the relay WebSocket and authenticates with NaCl box.
// serverURL must be ws:// or wss://. serverPubKeyHex is the hex-encoded
// X25519 public key of the relay server (from Welcome.RelayPubKey).
func (c *Client) Connect(ctx context.Context, serverURL string, serverPubKeyHex string) error {
	keyBytes, err := hex.DecodeString(serverPubKeyHex)
	if err != nil {
		return fmt.Errorf("decode server pub key: %w", err)
	}
	if len(keyBytes) != KeyLen {
		return fmt.Errorf("invalid server pub key length: %d", len(keyBytes))
	}
	copy(c.serverPubKey[:], keyBytes)

	dialer := websocket.Dialer{
		HandshakeTimeout: 10 * time.Second,
	}
	conn, resp, err := dialer.DialContext(ctx, serverURL, nil)
	if err != nil {
		return fmt.Errorf("dial relay: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	c.conn = conn

	if authErr := c.authenticate(ctx); authErr != nil {
		_ = conn.Close()
		return fmt.Errorf("relay auth: %w", authErr)
	}

	c.mu.Lock()
	c.serverURL = serverURL
	c.mu.Unlock()
	c.lastRead.Store(time.Now().UnixNano())
	go c.readLoop()
	go c.keepaliveLoop()
	go c.writerLoop()
	// #88②: open the physically separate control connection when the
	// server advertised it. Soft-fail: single-connection behavior on any
	// error (old server, network hiccup).
	if c.SupportsDualConn() {
		go c.dialCtrlLink() //nolint:gosec,contextcheck // G118: ctrl link outlives connect()
	}
	return nil
}

// PubKey returns the client's relay public key.
func (c *Client) PubKey() [32]byte {
	return c.pubKey
}

// DeriveE2EEKey derives the tunnel E2EE session key from this client's
// relay keypair and the remote peer's relay public key (received via
// sdp_offer/sdp_answer relay_pub_key). Both peers call this with their own
// private key and the other's public key and obtain the same key.
func (c *Client) DeriveE2EEKey(remotePub [32]byte) ([32]byte, error) {
	c.mu.Lock()
	priv, pub := c.privKey, c.pubKey
	c.mu.Unlock()
	if priv == [32]byte{} {
		return [32]byte{}, errors.New("relay: keypair not established")
	}
	return e2ee.DeriveSessionKey(priv, pub, remotePub)
}

// authenticate performs NaCl box-based authentication with the relay server.
func (c *Client) authenticate(_ context.Context) error {
	var nonce [NonceLen]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return fmt.Errorf("nonce: %w", err)
	}

	encrypted := box.Seal(nil, c.pubKey[:], &nonce, &c.serverPubKey, &c.privKey)

	payload := make([]byte, KeyLen+NonceLen+len(encrypted))
	copy(payload[:KeyLen], c.pubKey[:])
	copy(payload[KeyLen:KeyLen+NonceLen], nonce[:])
	copy(payload[KeyLen+NonceLen:], encrypted)

	frame := encodeFrame(FrameClientAuth, payload)
	if err := c.writeFrame(frame); err != nil {
		return fmt.Errorf("write auth: %w", err)
	}

	// Read ServerAuth response.
	_, data, err := c.conn.ReadMessage()
	if err != nil {
		return fmt.Errorf("read auth response: %w", err)
	}

	ft, respPayload, err := readFrame(data)
	if err != nil {
		return fmt.Errorf("parse auth response: %w", err)
	}
	if ft != FrameServerAuth {
		return fmt.Errorf("expected server_auth, got 0x%02x", ft)
	}
	if len(respPayload) < 1 {
		return errors.New("relay auth response missing status byte")
	}
	if respPayload[0] != 0x00 {
		if respPayload[0] == 0x03 {
			return ErrOverloaded // #95: capacity, not a key/box failure
		}
		return fmt.Errorf("relay auth rejected (status=0x%02x)", respPayload[0])
	}
	// Capability byte (byte 1, #46 v2): legacy servers send only status.
	if len(respPayload) >= 2 {
		c.mu.Lock()
		c.serverCaps = respPayload[1]
		c.mu.Unlock()
	}

	// #64 server-identity proof: [24B proof nonce][48B box] sealed with the
	// server's static key to OUR public key over sha256(our nonce | our
	// pub). The expected key comes from the authenticated Welcome — a relay
	// that cannot produce this box is not the relay the signaling server
	// announced (MITM with a substituted key). Fail closed; a 2-byte legacy
	// payload carries no proof and stays accepted (old server).
	const proofLen = 24 + 48
	hasProof := len(respPayload) >= 2+proofLen
	if c.serverCaps&FrameCapServerAuth != 0 && !hasProof {
		return errors.New("relay auth: server advertised identity proof but sent none")
	}
	if hasProof {
		if proofErr := c.verifyServerAuthProof(respPayload[2:], nonce); proofErr != nil {
			return proofErr
		}
	}
	return nil
}

// verifyServerAuthProof opens the #64 server-identity box. The bound
// transcript is this client's own auth nonce — replaying a server_auth
// captured from an earlier handshake of the SAME client fails here.
func (c *Client) verifyServerAuthProof(proof []byte, clientNonce [NonceLen]byte) error {
	var proofNonce [NonceLen]byte
	copy(proofNonce[:], proof[:24])
	bind := sha256.Sum256(append(append([]byte{}, clientNonce[:]...), c.pubKey[:]...))
	opened, ok := box.Open(nil, proof[24:], &proofNonce, &c.serverPubKey, &c.privKey)
	if !ok || !bytes.Equal(opened, bind[:]) {
		return errors.New("relay auth: server identity verification failed" +
			" (proof box invalid - possible MITM with substituted relay key)")
	}
	return nil
}

// readLoop reads binary frames from the WebSocket and dispatches them.
// Every read carries a deadline: a half-open connection (NAT mapping gone,
// no FIN/RST ever arrives) must surface as a read timeout instead of
// blocking forever. Any inbound frame — pong or data — proves liveness.
func (c *Client) readLoop() {
	c.mu.Lock()
	pongTimeout := c.pongTimeout
	c.mu.Unlock()
	for {
		_ = c.conn.SetReadDeadline(time.Now().Add(pongTimeout))
		_, data, err := c.conn.ReadMessage()
		if err != nil {
			select {
			case <-c.closeCh:
				return
			default:
			}
			// #106-B: a 4009 close without a preceding restarting frame
			// (ctrl lane dropped it) still carries the transient-restart
			// semantics.
			if websocket.IsCloseError(err, closeStatusRestarting) {
				c.fail(&RestartingError{RetryAfter: defaultRestartingRetryAfter})
				return
			}
			c.fail(fmt.Errorf("relay read: %w", err))
			return
		}
		c.lastRead.Store(time.Now().UnixNano())

		ft, payload, err := readFrame(data)
		if err != nil {
			continue
		}

		// Priority-marked inbound frames dispatch on the base type (#46 v2).
		switch FrameBase(ft) {
		case FrameRecvPacket:
			c.dispatchRecvPacket(payload)

		case FramePing:
			c.enqueueCtrl(encodeFrame(FramePong, nil))

		case FrameKeepAlive, FramePong:
			// No-op (the read itself refreshed liveness).

		case FrameThrottle:
			c.handleThrottle(payload)

		case FrameRestarting:
			// #106-B: server-side graceful shutdown — fail with the typed
			// error so the reconnection policy can use the retry hint. A
			// 4009 close follows; whichever arrives first wins.
			c.fail(&RestartingError{RetryAfter: parseRestartingAfter(payload)})

		case FramePeerGone:
			// #106-B: [32B peer key][1B reason].
			if len(payload) < KeyLen+1 {
				continue
			}
			var peer [32]byte
			copy(peer[:], payload[:KeyLen])
			c.mu.Lock()
			h := c.onPeerGone
			c.mu.Unlock()
			if h != nil {
				h(peer, payload[KeyLen])
			}

		default:
			// Unknown frames ignored.
		}
	}
}

// dispatchRecvPacket routes one inbound recv_packet — [0:32] source key,
// [32:] data payload — to the per-source handler, falling back to the
// global OnMessage handler.
func (c *Client) dispatchRecvPacket(payload []byte) {
	if len(payload) < KeyLen {
		return
	}
	var src [32]byte
	copy(src[:], payload[:KeyLen])
	c.mu.Lock()
	h := c.routes[src]
	if h == nil {
		h = c.onMessage
	}
	c.mu.Unlock()
	if h != nil {
		h(payload[KeyLen:])
	}
}

// parseRestartingAfter reads the 2B BE retry_after_ms hint; a missing or
// malformed payload falls back to the conservative default.
func parseRestartingAfter(payload []byte) time.Duration {
	if len(payload) >= 2 {
		//nolint:gosec // G115: server-controlled hint clamped to uint16 ms
		return time.Duration(binary.BigEndian.Uint16(payload)) * time.Millisecond
	}
	return defaultRestartingRetryAfter
}

// handleThrottle parses a server rate-limit notification (#69): 8B BE
// [4B frames][4B bytes] over the server's rate window.
func (c *Client) handleThrottle(payload []byte) {
	if len(payload) < 8 {
		return
	}
	frames := binary.BigEndian.Uint32(payload[0:4])
	nbytes := binary.BigEndian.Uint32(payload[4:8])
	c.mu.Lock()
	cb := c.onThrottle
	c.mu.Unlock()
	if cb != nil {
		cb(frames, nbytes)
	}
}

// keepaliveLoop pings the relay server on a fixed cadence and declares the
// transport fatal when no inbound frame (pong or data) arrived within the
// pong window. Without this, a black-holed connection stays "healthy"
// forever from the local side — tunnel frames queue in the kernel buffer
// and every write "succeeds" (production incident 2026-10-08: relay adopted
// by both peers, zero data flowing, only an NVR restart cleared it).
func (c *Client) keepaliveLoop() {
	c.mu.Lock()
	interval := c.pingInterval
	pongTimeout := c.pongTimeout
	c.mu.Unlock()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-c.closeCh:
			return
		case <-ticker.C:
			last := time.Unix(0, c.lastRead.Load())
			if since := time.Since(last); since > pongTimeout {
				c.fail(fmt.Errorf("relay keepalive: no inbound frame for %s", since.Truncate(time.Second)))
				return
			}
			// Control lane (#46): the ping must not queue behind bulk
			// tunnel data — a delayed ping reads as a dead relay under
			// video load even though the link is alive.
			c.enqueueCtrl(encodeFrame(FramePing, nil))
		}
	}
}

// Send sends data to the remote peer via the relay server.
// Implements peer.Transport.
func (c *Client) Send(data []byte) error {
	c.mu.Lock()
	dstKey := c.remotePubKey
	c.mu.Unlock()

	if dstKey == ([32]byte{}) {
		return errors.New("remote peer key not set")
	}
	return c.SendTo(dstKey, data)
}

// SendPriority sends data on the high lane when the connected server
// advertised the priority capability (#46 v2); it downgrades to plain Send
// otherwise. Implements peer.PrioritySender.
func (c *Client) SendPriority(data []byte) error {
	c.mu.Lock()
	dstKey := c.remotePubKey
	c.mu.Unlock()

	if dstKey == ([32]byte{}) {
		return errors.New("remote peer key not set")
	}
	return c.SendPriorityTo(dstKey, data)
}

// SendPriorityTo is the multi-session safe form of SendPriority.
// See SendTo for the destination-as-parameter rationale.
func (c *Client) SendPriorityTo(dst [32]byte, data []byte) error {
	if !c.SupportsPriority() {
		return c.SendTo(dst, data)
	}
	// #88②: the physically separate ctrl connection beats any shared-lane
	// queueing when live; its death degrades to the path below.
	if c.sendViaCtrl(dst, data) {
		return nil
	}
	if len(data) > MaxPacketSize-KeyLen {
		return fmt.Errorf("payload too large: %d", len(data))
	}
	payload := make([]byte, KeyLen+len(data))
	copy(payload[:KeyLen], dst[:])
	copy(payload[KeyLen:], data)
	return c.enqueueData(encodeFrame(FrameSendPacket|FramePriorityBit, payload), true)
}

// OnMessage registers a handler for incoming messages from the remote peer.
// Implements peer.Transport.
func (c *Client) OnMessage(h func([]byte)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onMessage = h
}

// OnFatal registers a one-shot callback fired when the relay connection is
// found dead (read timeout / pong starvation / write failure / protocol
// EOF). The owner is expected to tear down sessions riding this transport
// and reconnect — per the relay contract a broken relay means full
// session-level rebuild, never in-place resuscitation.
func (c *Client) OnFatal(h func(error)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onFatal = h
}

// OnThrottle registers the server rate-limit notification callback (#69).
// The server sends at most one throttle frame per second while this
// connection's data frames are being dropped; callers should reduce their
// send rate (e.g. step the video bitrate down).
func (c *Client) OnThrottle(h func(droppedFrames, droppedBytes uint32)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onThrottle = h
}

// OnPeerGone registers the peer-departure callback (#106-B): the server
// sends FramePeerGone when a key this client routed with inside the
// server-side window leaves the relay. Owners should tear down sessions
// bound to that peer key and redial immediately — recovery in ~1s instead
// of the liveness-timeout path.
func (c *Client) OnPeerGone(h func(peer [32]byte, reason byte)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onPeerGone = h
}

// Close shuts down the relay client and releases resources.
// Implements peer.Transport.
func (c *Client) Close() error {
	if link := c.ctrl.Load(); link != nil {
		link.close()
	}
	select {
	case <-c.closeCh:
		return nil
	default:
		close(c.closeCh)
	}

	c.writeMu.Lock()
	conn := c.conn
	c.writeMu.Unlock()

	if conn != nil {
		return conn.Close()
	}
	return nil
}

// writeFrame sends a binary frame over the WebSocket connection. The write
// deadline keeps a dead peer (kernel send buffer full on a half-open TCP)
// from blocking the caller for the OS-level TCP timeout (~minutes).
func (c *Client) writeFrame(data []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	c.mu.Lock()
	wt := c.writeTimeout
	c.mu.Unlock()
	_ = c.conn.SetWriteDeadline(time.Now().Add(wt))
	err := c.conn.WriteMessage(websocket.BinaryMessage, data)
	_ = c.conn.SetWriteDeadline(time.Time{})
	return err
}

// fail marks the transport dead: closes the connection and invokes the
// fatal callback exactly once. Deliberate Close() does not route here.
func (c *Client) fail(err error) {
	c.fatalOnce.Do(func() {
		c.mu.Lock()
		conn := c.conn
		cb := c.onFatal
		c.mu.Unlock()
		if conn != nil {
			_ = conn.Close()
		}
		if cb != nil {
			cb(err)
		}
	})
}

// FrameBase strips the priority bit from a wire frame type.
func FrameBase(ft byte) byte { return ft &^ FramePriorityBit }

// encodeFrame builds a relay frame: [1:type][4:length(big-endian)][payload].
func encodeFrame(ft byte, payload []byte) []byte {
	buf := make([]byte, FrameHeaderLen+len(payload))
	buf[0] = ft
	//nolint:gosec // G115: payload length bounded by relay frame protocol
	binary.BigEndian.PutUint32(buf[1:5], uint32(len(payload)))
	copy(buf[FrameHeaderLen:], payload)
	return buf
}

// readFrame parses a binary relay frame and returns (type, payload, error).
func readFrame(data []byte) (byte, []byte, error) {
	if len(data) < FrameHeaderLen {
		return 0, nil, fmt.Errorf("frame too short: %d", len(data))
	}
	ft := data[0]
	length := int(binary.BigEndian.Uint32(data[1:5]))
	if length > MaxPacketSize {
		return 0, nil, fmt.Errorf("frame payload too large: %d", length)
	}
	if len(data) < FrameHeaderLen+length {
		return 0, nil, errors.New("frame payload truncated")
	}
	return ft, data[FrameHeaderLen : FrameHeaderLen+length], nil
}

// Compile-time check ensures *Client matches the peer.Transport signature.
var _ interface {
	Send([]byte) error
	OnMessage(func([]byte))
	Close() error
} = (*Client)(nil)
