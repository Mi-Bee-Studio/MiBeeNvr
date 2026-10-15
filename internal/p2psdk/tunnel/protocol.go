package tunnel

import (
	"encoding/base64"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
)

// MaxPayloadBytes is the maximum payload size for a single TunnelData message.
//
// 32 KiB: a 24h cross-segment WiFi measurement (linktest frame-size sweep,
// 2026-10-09) showed 32 KiB chunks sustain ~20% higher uplink throughput than
// 16 KiB (105.5 vs 85.3 Mbps mean) with downlink unchanged. Base64 framing
// inflates this to ~43.7 KiB on the wire, still under the 64 KiB SCTP
// message-size ceiling. Receivers do not validate payload size, so mixed
// old/new SDK versions interoperate.
const MaxPayloadBytes = 32768 // 32 KiB

// DefaultSendWindowSize is the default unacknowledged send window per stream.
const DefaultSendWindowSize = 8

// TunnelMessageType enumerates tunnel sub-protocol message types.
//
//nolint:revive // stutters as tunnel.TunnelMessageType; renaming would break API
type TunnelMessageType string

// Tunnel message type constants.
const (
	TunnelMessageTypeOpen    TunnelMessageType = "open"
	TunnelMessageTypeData    TunnelMessageType = "data"
	TunnelMessageTypeClose   TunnelMessageType = "close"
	TunnelMessageTypeE2EEAck TunnelMessageType = "e2ee_ack"
)

// TunnelMessage is the envelope for DataChannel tunnel sub-protocol messages.
//
//nolint:revive // stutters as tunnel.TunnelMessage; renaming would break API
type TunnelMessage struct {
	Type    TunnelMessageType `json:"type"`
	Payload string            `json:"payload"`
}

// TunnelOpen opens a new stream to a local service on the device peer.
//
// E2EE, when true, declares that the dialer supports tunnel-layer
// end-to-end encryption and will seal its data frames once the device
// confirms with a sealed frame. Older peers ignore the field (additive
// JSON) and the stream proceeds in plaintext.
//
//nolint:revive // stutters as tunnel.TunnelOpen; renaming would break API
type TunnelOpen struct {
	StreamID      uint32 `json:"stream_id"`
	TargetService string `json:"target_service"`
	E2EE          bool   `json:"e2ee,omitempty"`
}

// TunnelData carries a fragment of stream data.
//
// Enc, when true, marks Payload as base64(nonce||AEAD-ciphertext) sealed
// with the session's E2EE key; the plaintext is the usual base64 payload
// bytes. Older peers never see Enc (they never negotiate E2EE).
//
//nolint:revive // stutters as tunnel.TunnelData; renaming would break API
type TunnelData struct {
	StreamID uint32 `json:"stream_id"`
	Seq      uint32 `json:"seq"`
	Total    uint32 `json:"total"`
	Payload  string `json:"payload"`
	Enc      bool   `json:"enc,omitempty"`
}

// TunnelClose signals the end of a stream.
//
//nolint:revive // stutters as tunnel.TunnelClose; renaming would break API
type TunnelClose struct {
	StreamID uint32 `json:"stream_id"`
}

// StreamIDGenerator produces monotonically increasing stream IDs.
type StreamIDGenerator struct {
	counter atomic.Uint32
}

// NewStreamIDGenerator creates a new StreamIDGenerator starting from ID 1.
func NewStreamIDGenerator() *StreamIDGenerator {
	var g StreamIDGenerator
	g.counter.Store(1)
	return &g
}

// Next returns the next available stream ID.
func (g *StreamIDGenerator) Next() uint32 {
	return g.counter.Add(1) - 1
}

// StreamRegistry enforces unique stream_id per peer connection.
type StreamRegistry struct {
	mu      sync.Mutex
	streams map[uint32]bool
}

// NewStreamRegistry creates a new StreamRegistry.
func NewStreamRegistry() *StreamRegistry {
	return &StreamRegistry{streams: make(map[uint32]bool)}
}

// OpenStream marks a stream ID as open, returning an error if already open.
func (r *StreamRegistry) OpenStream(streamID uint32) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.streams[streamID] {
		return fmt.Errorf("stream %d already open", streamID)
	}
	r.streams[streamID] = true
	return nil
}

// CloseStream marks a stream ID as closed, allowing it to be reused.
func (r *StreamRegistry) CloseStream(streamID uint32) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.streams, streamID)
}

// Fragmenter splits payloads into TunnelData messages.
type Fragmenter struct {
	StreamID uint32
	maxBytes int
}

// NewFragmenter creates a Fragmenter for the given stream ID. maxBytes ≤ 0
// falls back to MaxPayloadBytes.
func NewFragmenter(streamID uint32, maxBytes int) *Fragmenter {
	if maxBytes <= 0 {
		maxBytes = MaxPayloadBytes
	}
	return &Fragmenter{StreamID: streamID, maxBytes: maxBytes}
}

// Fragment splits a payload into TunnelData messages.
func (f *Fragmenter) Fragment(payload []byte) ([]TunnelData, error) {
	if len(payload) == 0 {
		return []TunnelData{{
			StreamID: f.StreamID, Seq: 0, Total: 1,
			Payload: base64.StdEncoding.EncodeToString(payload),
		}}, nil
	}
	//nolint:gosec // G115: payload length is bounded by application
	total := uint32((len(payload) + f.maxBytes - 1) / f.maxBytes)
	var fragments []TunnelData
	for offset := 0; offset < len(payload); offset += f.maxBytes {
		end := offset + f.maxBytes
		if end > len(payload) {
			end = len(payload)
		}
		fragments = append(fragments, TunnelData{
			StreamID: f.StreamID,
			Seq:      uint32(len(fragments)), //nolint:gosec // G115: fragment count is bounded
			Total:    total,
			Payload:  base64.StdEncoding.EncodeToString(payload[offset:end]),
		})
	}
	return fragments, nil
}

// SendWindow provides bounded backpressure for a data stream.
type SendWindow struct {
	mu       sync.Mutex
	capacity int
	inflight int
}

// NewSendWindow creates a send window with the given capacity.
func NewSendWindow(capacity int) *SendWindow {
	return &SendWindow{capacity: capacity}
}

// CanSend returns true if the send window has capacity.
func (w *SendWindow) CanSend() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.inflight < w.capacity
}

// Send increments the inflight counter, returning an error if at capacity.
func (w *SendWindow) Send() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.inflight >= w.capacity {
		return fmt.Errorf("send window full (capacity=%d)", w.capacity)
	}
	w.inflight++
	return nil
}

// Ack decrements the inflight counter.
func (w *SendWindow) Ack() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.inflight > 0 {
		w.inflight--
	}
}

// Inflight returns the current number of outstanding sends.
func (w *SendWindow) Inflight() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.inflight
}

// Reassembler collects TunnelData fragments and reassembles them.
type Reassembler struct {
	mu      sync.Mutex
	streams map[uint32]*reassemblyBuf
	maxSize int
}

type reassemblyBuf struct {
	totalBytes int
	expected   int
	seqs       map[uint32]struct{}
	buf        map[uint32][]byte
}

// NewReassembler creates a Reassembler for collecting tunnel data fragments.
func NewReassembler() *Reassembler {
	return &Reassembler{
		streams: make(map[uint32]*reassemblyBuf),
		maxSize: 4 * 1024 * 1024,
	}
}

// AddFragment adds a fragment and returns the reassembled payload when complete.
func (r *Reassembler) AddFragment(s TunnelData) ([]byte, error) {
	decoded, err := base64.StdEncoding.DecodeString(s.Payload)
	if err != nil {
		return nil, fmt.Errorf("decode tunnel payload: %w", err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	buf, ok := r.streams[s.StreamID]
	if !ok {
		buf = &reassemblyBuf{
			expected: int(s.Total),
			seqs:     make(map[uint32]struct{}),
			buf:      make(map[uint32][]byte),
		}
		r.streams[s.StreamID] = buf
	}
	if _, seen := buf.seqs[s.Seq]; seen {
		return nil, nil
	}
	buf.seqs[s.Seq] = struct{}{}
	buf.buf[s.Seq] = decoded
	buf.totalBytes += len(decoded)
	if buf.totalBytes > r.maxSize {
		return nil, errors.New("reassembly buffer exceeded max total size")
	}
	//nolint:gosec // G115: expected is derived from uint32 Total field
	if buf.expected > 0 && uint32(len(buf.seqs)) < uint32(buf.expected) {
		return nil, nil
	}
	var result []byte
	for i := range uint32(buf.expected) { //nolint:gosec // G115: expected is derived from uint32 Total field
		result = append(result, buf.buf[i]...)
	}
	delete(r.streams, s.StreamID)
	return result, nil
}
