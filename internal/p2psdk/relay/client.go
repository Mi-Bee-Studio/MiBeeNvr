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
	// FrameCapEcho (capability byte bit 2, #87): the deployment supports
	// tunnel-layer echo RTT probes (T2 decomposition). Echo frames ride the
	// peer tunnel, not the relay — this bit lets clients gate probing on
	// deployment support so older fleets read "unsupported", not "broken".
	FrameCapEcho byte = 0x04

	// FrameThrottle (0x08) arrives server → client on the control lane when
	// the server's inbound rate limit dropped this connection's data frames
	// (#69). Payload: 8B BE [4B dropped frames][4B dropped bytes] over the
	// server's rate window.
	FrameThrottle byte = 0x08
)

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
	conn         *websocket.Conn
	pubKey       [32]byte
	privKey      [32]byte
	serverPubKey [32]byte
	remotePubKey [32]byte

	mu        sync.Mutex
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
	// serverCaps carries the capability byte from server_auth (0 when the
	// server is legacy 1-byte). Governed by mu; governs SendPriority gating.
	serverCaps byte

	closeCh   chan struct{}
	writeMu   sync.Mutex
	fatalOnce sync.Once
	lastRead  atomic.Int64 // unix nanos of the last inbound frame

	// Async egress with a control lane (#46): keepalive pings and pong
	// replies jump the data lane so liveness frames never sit behind bulk
	// tunnel traffic in the write lock; data frames stay strictly FIFO
	// (relay frame order is a protocol contract — no data frame may ever
	// be reordered). One writer goroutine performs all socket writes.
	// #46 v2 adds the high lane for priority-marked frames (negotiated).
	ctrlCh    chan []byte
	hiCh      chan []byte
	dataCh    chan []byte
	dataBytes atomic.Int64 // bytes queued on the data lanes

	// writeSync substitutes the socket-write primitive in tests.
	writeSync func([]byte) error

	// Liveness knobs (overridable in tests). Defaults below.
	pingInterval time.Duration
	pongTimeout  time.Duration
	writeTimeout time.Duration
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

// NewClient creates an unconnected relay client.
func NewClient() *Client {
	return &Client{
		closeCh:      make(chan struct{}),
		ctrlCh:       make(chan []byte, clientCtrlQueueFrames),
		hiCh:         make(chan []byte, clientHighQueueFrames),
		dataCh:       make(chan []byte, clientDataQueueFrames),
		pingInterval: defaultPingInterval,
		pongTimeout:  defaultPongTimeout,
		writeTimeout: defaultWriteTimeout,
	}
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

	c.pubKey, c.privKey = GenerateKeyPair()
	if err := c.authenticate(ctx); err != nil {
		_ = conn.Close()
		return fmt.Errorf("relay auth: %w", err)
	}

	c.lastRead.Store(time.Now().UnixNano())
	go c.readLoop()
	go c.keepaliveLoop()
	go c.writerLoop()
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
	if len(respPayload) < 1 || respPayload[0] != 0x00 {
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
		if err := c.verifyServerAuthProof(respPayload[2:], nonce); err != nil {
			return err
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
			// Format: [0:32] source key, [32:] data payload.
			if len(payload) < KeyLen {
				continue
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

		case FramePing:
			c.enqueueCtrl(encodeFrame(FramePong, nil))

		case FrameKeepAlive, FramePong:
			// No-op (the read itself refreshed liveness).

		case FrameThrottle:
			c.handleThrottle(payload)

		default:
			// Unknown frames ignored.
		}
	}
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

// Close shuts down the relay client and releases resources.
// Implements peer.Transport.
func (c *Client) Close() error {
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
