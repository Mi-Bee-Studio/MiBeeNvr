// Package tunnel provides a sub-protocol for bidirectional stream multiplexing over a Transport.
package tunnel

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"

	peer "github.com/Mi-Bee-Studio/MiBeeNvr/internal/p2psdk"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/p2psdk/e2ee"
)

// Tunnel manages tunnel protocol streams over a Transport.
type Tunnel struct {
	transport peer.Transport
	services  map[string]string
	registry  *StreamRegistry
	streams   sync.Map // streamID → *streamConn
	gen       *StreamIDGenerator
	reasm     *Reassembler
	logger    *slog.Logger
	wg        sync.WaitGroup
	closeCh   chan struct{}
	mu        sync.Mutex
	started   bool

	// maxPayload overrides the fragment size (0 = MaxPayloadBytes).
	maxPayload int

	// droppedFrames counts inbound frames dropped at the stream delivery
	// queue (queue full = the peer's consumer is stalled; dropping rather
	// than back-pressuring is the ordered-stream design — surfaced for
	// diagnostics, it is NOT an error path to fix silently).
	droppedFrames atomic.Uint64

	// msgCh carries inbound frames from the (construction-time) transport
	// handler to the Serve loop. Buffering here is what makes pre-Serve
	// frames survivable.
	msgCh chan []byte

	// E2EE state. cipher is set by EnableE2EE (before Serve); remoteConfirmed
	// flips true when a sealed e2ee_ack (or sealed frame) arrives from the
	// peer, after which the dialer seals its own frames — including the
	// first one, because Dial waits (briefly) for the device's ack.
	// Device-side streams seal from the first frame when the incoming open
	// negotiated E2EE.
	e2eeCipher      *e2ee.Cipher
	remoteConfirmed atomic.Bool
	e2eeAckCh       chan struct{}
	e2eeAckOnce     sync.Once

	// egressPrioBudget arms per-stream high-lane marking (see
	// EnableEgressPriority); zero = off (pre-feature behavior).
	egressPrioBudget atomic.Int64
}

// e2eeHandshakeTimeout bounds how long Dial waits for the device's sealed
// e2ee_ack before proceeding (a legacy device never acks; the session then
// runs plaintext exactly as before E2EE existed).
const e2eeHandshakeTimeout = 500 * time.Millisecond

// EnableEgressPriority arms per-stream egress acceleration (#46 v2 device
// side): each stream's first budgetBytes of outbound wire traffic rides the
// transport's high-priority path. Small latency-sensitive responses (API
// JSON, playlists, probe replies) complete inside the budget; bulk media
// gets a fast head start then demotes to the normal lane. Demotion is
// strictly one-way, which keeps intra-stream ordering intact under any
// strict-priority drain. budgetBytes <= 0 leaves the feature off.
func (t *Tunnel) EnableEgressPriority(budgetBytes int64) {
	if budgetBytes <= 0 {
		return
	}
	t.egressPrioBudget.Store(budgetBytes)
}

// EnableE2EE activates tunnel-layer end-to-end encryption with the given
// 32-byte session key (from DataChannel.E2EEKey). Must be called before
// Serve; with an absent key the tunnel runs plaintext, exactly as before.
func (t *Tunnel) EnableE2EE(key []byte) error {
	t.e2eeAckCh = make(chan struct{})
	if len(key) != e2ee.KeySize {
		return fmt.Errorf("tunnel: e2ee key must be %d bytes, got %d", e2ee.KeySize, len(key))
	}
	var k [e2ee.KeySize]byte
	copy(k[:], key)
	c, err := e2ee.NewCipher(k)
	if err != nil {
		return fmt.Errorf("tunnel: e2ee: %w", err)
	}
	t.e2eeCipher = c
	return nil
}

// e2eeActive reports whether encryption is armed AND the peer has confirmed
// with a sealed frame (dialer-side gate; device side is per-stream).
func (t *Tunnel) e2eeActive() bool {
	return t.e2eeCipher != nil && t.remoteConfirmed.Load()
}

// sealFragment mutates frag in place: payload → base64(nonce||ct). aad binds
// the ciphertext to (stream, seq, total) so fragments cannot be spliced
// across streams or replayed at different positions.
func (t *Tunnel) sealFragment(frag *TunnelData) error {
	raw, err := base64.StdEncoding.DecodeString(frag.Payload)
	if err != nil {
		return fmt.Errorf("tunnel: e2ee payload not base64: %w", err)
	}
	aad := fragmentAAD(frag.StreamID, frag.Seq, frag.Total)
	sealed, err := t.e2eeCipher.Seal(raw, aad)
	if err != nil {
		return fmt.Errorf("tunnel: e2ee seal: %w", err)
	}
	frag.Payload = base64.StdEncoding.EncodeToString(sealed)
	frag.Enc = true
	return nil
}

// openFragment reverses sealFragment in place.
func (t *Tunnel) openFragment(frag *TunnelData) error {
	sealed, err := base64.StdEncoding.DecodeString(frag.Payload)
	if err != nil {
		return fmt.Errorf("tunnel: e2ee payload not base64: %w", err)
	}
	aad := fragmentAAD(frag.StreamID, frag.Seq, frag.Total)
	raw, err := t.e2eeCipher.Open(sealed, aad)
	if err != nil {
		return fmt.Errorf("tunnel: e2ee open: %w", err)
	}
	frag.Payload = base64.StdEncoding.EncodeToString(raw)
	frag.Enc = false
	return nil
}

func fragmentAAD(streamID, seq, total uint32) []byte {
	return []byte(fmt.Sprintf("mibee-frag:%d:%d:%d", streamID, seq, total))
}

// New creates a Tunnel that operates over the given Transport.
//
// The inbound handler is registered HERE, synchronously — not in Serve().
// The transport's routing (relay per-peer route / DataChannel OnMessage)
// goes live the moment the channel is handed over, and the peer starts
// sending on session_open, which the SDK fires right after the app's
// OnDataChannel callback returns. Registering lazily inside Serve() — which
// applications launch as `go tun.Serve()` — leaves a scheduling window
// where early frames find no handler and are DROPPED; tunnel_open has no
// application-layer retransmission, so one lost frame deadlocks the first
// request to timeout (2026-10-09 tri-party patrol: R19/R41/R44, links
// healthy yet logins stalled 15s). Frames arriving before Serve() buffer
// in msgCh and drain once Serve runs.
func New(t peer.Transport, opts ...Option) *Tunnel {
	tun := &Tunnel{
		transport: t,
		services:  make(map[string]string),
		registry:  NewStreamRegistry(),
		gen:       NewStreamIDGenerator(),
		reasm:     NewReassembler(),
		logger:    slog.Default().With("component", "tunnel"),
		closeCh:   make(chan struct{}),
		msgCh:     make(chan []byte, 64),
	}
	for _, o := range opts {
		o(tun)
	}
	t.OnMessage(func(data []byte) {
		select {
		case tun.msgCh <- data:
		case <-tun.closeCh:
		}
	})
	return tun
}

// Option customizes a Tunnel at construction.
type Option func(*Tunnel)

// WithMaxPayloadBytes overrides the tunnel fragment size (0/negative is
// ignored → MaxPayloadBytes). Receivers do not validate fragment size, so
// senders with different settings interoperate; the SCTP 64 KiB
// message-size ceiling constrains the upper bound (base64 inflates by 4/3,
// so 32 KiB payload ≈ 43.7 KiB on the wire).
func WithMaxPayloadBytes(n int) Option {
	return func(t *Tunnel) {
		if n > 0 {
			t.maxPayload = n
		}
	}
}

// fragmentSize is the effective per-fragment payload ceiling.
func (t *Tunnel) fragmentSize() int {
	if t.maxPayload > 0 {
		return t.maxPayload
	}
	return MaxPayloadBytes
}

// DroppedFrames reports inbound frames dropped at stream delivery queues
// (consumer stalled, queue full). Monotonic for this Tunnel's lifetime.
func (t *Tunnel) DroppedFrames() uint64 {
	return t.droppedFrames.Load()
}

// RegisterService registers a local service address reachable by name.
func (t *Tunnel) RegisterService(name, addr string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.services[name] = addr
	t.logger.Info("service registered", "name", name, "addr", addr)
}

// Serve starts reading tunnel messages from the DataChannel.
func (t *Tunnel) Serve() {
	t.mu.Lock()
	if t.started {
		t.mu.Unlock()
		return
	}
	t.started = true
	t.mu.Unlock()

	for {
		select {
		case <-t.closeCh:
			return
		case data := <-t.msgCh:
			t.dispatch(data)
		}
	}
}

// Dial opens a tunnel stream to a remote service and returns a net.Conn.
func (t *Tunnel) Dial(service string) (net.Conn, error) {
	streamID := t.gen.Next()

	sc := newStreamConn(t, streamID, service, t.logger)
	if err := t.registry.OpenStream(streamID); err != nil {
		return nil, fmt.Errorf("open stream: %w", err)
	}
	t.streams.Store(streamID, sc)

	openMsg := TunnelMessage{
		Type: TunnelMessageTypeOpen,
		Payload: mustJSON(TunnelOpen{
			StreamID:      streamID,
			TargetService: service,
			E2EE:          t.e2eeCipher != nil,
		}),
	}
	if err := t.sendMsg(openMsg); err != nil {
		t.registry.CloseStream(streamID)
		t.streams.Delete(streamID)
		return nil, fmt.Errorf("send open: %w", err)
	}

	// When E2EE is armed, wait (briefly) for the device's sealed ack so the
	// first data frame can already be ciphertext. A legacy device never
	// acks; the timeout leaves the session in plaintext-compat mode.
	if t.e2eeCipher != nil {
		select {
		case <-t.e2eeAckCh:
		case <-time.After(e2eeHandshakeTimeout):
			t.logger.Warn("e2ee ack not received — proceeding plaintext (legacy peer?)",
				"stream_id", streamID)
		}
	}

	t.logger.Debug("dial initiated", "stream_id", streamID, "service", service)
	return sc, nil
}

// Close shuts down the tunnel and all active streams.
func (t *Tunnel) Close() error {
	select {
	case <-t.closeCh:
	default:
		close(t.closeCh)
	}

	t.streams.Range(func(_, v interface{}) bool {
		if sc, ok := v.(*streamConn); ok {
			_ = sc.Close()
		}
		return true
	})

	t.wg.Wait()
	return nil
}

func (t *Tunnel) dispatch(data []byte) {
	var msg TunnelMessage
	if err := json.Unmarshal(data, &msg); err != nil {
		t.logger.Warn("invalid tunnel message", "error", err)
		return
	}
	switch msg.Type {
	case TunnelMessageTypeOpen:
		t.handleOpen(msg.Payload)
	case TunnelMessageTypeData:
		t.handleData(msg.Payload)
	case TunnelMessageTypeClose:
		t.handleClose(msg.Payload)
	case TunnelMessageTypeE2EEAck:
		t.handleE2EEAck(msg.Payload)
	default:
		t.logger.Warn("unknown tunnel message type", "type", msg.Type)
	}
}
