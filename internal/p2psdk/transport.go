package peer

import (
	"errors"
	"fmt"
	"sync"
	"time"
)

// Transport is the interface for sending and receiving data over a P2P
// connection. It abstracts the underlying DataChannel so that the tunnel
// layer can be tested or swapped without coupling to pion/webrtc.
type Transport interface {
	// Send sends a message over the transport.
	Send(data []byte) error

	// OnMessage registers a handler for incoming messages.
	OnMessage(h func([]byte))

	// Close closes the transport and releases resources.
	Close() error
}

// PrioritySender is an OPTIONAL Transport capability (#46 v2): transports
// that can prioritize a send implement it. The tunnel layer type-asserts and
// degrades to plain Send when absent — availability first.
//
// Contract: priority is a PER-STREAM convention. A tunnel stream must use
// the same priority for all its frames (fragmented messages included), or
// intra-stream ordering breaks; cross-stream reordering is what the
// reassembler tolerates by design.
type PrioritySender interface {
	// SendPriority sends a message on the high-priority path when the
	// transport and its remote path support prioritization; it may
	// silently fall back to the normal path otherwise.
	SendPriority(data []byte) error
}

// SendPriority sends data with high priority when the transport implements
// PrioritySender; otherwise it degrades to plain Send. Tunnel layers call
// this instead of branching themselves.
func SendPriority(t Transport, data []byte) error {
	if ps, ok := t.(PrioritySender); ok {
		return ps.SendPriority(data)
	}
	return t.Send(data)
}

// dataChannelTransport wraps a DataChannel to implement Transport.
type dataChannelTransport struct {
	dc    DataChannel
	sched *dcEgressScheduler // non-nil iff dc.BufferedAmount is observable
}

// Egress scheduler knobs (vars so tests can shrink them; production leaves
// the defaults). The admission watermark gates how many bytes may sit in
// SCTP before the scheduler stops feeding it — small enough that a later
// priority frame jumps ahead of queued bulk bytes, large enough to keep
// the pipe full on fast links. Lane caps bound scheduler memory and turn
// saturation into backpressure on the caller.
var (
	dcEgressLowWater  = 64 << 10  // BufferedAmount admission watermark
	dcEgressHighCap   = 256 << 10 // high-lane byte cap
	dcEgressDataCap   = 4 << 20   // data-lane byte cap
	dcEgressPollEvery = 5 * time.Millisecond

	// dcEgressParkBudget bounds how long one enqueue may park waiting for
	// lane space. Without it, a peer that stops draining (ICE dead but SCTP
	// not timed out — BufferedAmount pinned above the watermark) parks
	// enqueue callers forever: Tunnel.Close() then hangs on its pipe
	// goroutines and a Manager-level redial wedges the whole agent until
	// the process is killed (2026-10-14 production: NVR frozen 23 min +
	// systemd SIGKILL on stop). Semantics mirror the relay lanes'
	// bounded-blocking queues: slow-but-alive links backpressure within the
	// budget, truly dead links fail and surface.
	dcEgressParkBudget = 45 * time.Second
)

// NewDataChannelTransport creates a Transport from a DataChannel. When the
// channel can report BufferedAmount (pion), egress goes through the
// strict-priority scheduler; otherwise behavior is passthrough exactly as
// before the scheduler existed.
func NewDataChannelTransport(dc DataChannel) Transport {
	t := &dataChannelTransport{dc: dc}
	if dc.BufferedAmount != nil {
		t.sched = newDCEgressScheduler(dc)
	}
	return t
}

func (t *dataChannelTransport) Send(data []byte) error {
	if t.sched != nil {
		return t.sched.enqueue(data, false)
	}
	return t.dc.Send(data)
}

// SendPriority routes to the scheduler's high lane on the WebRTC path, to
// the optional DataChannel priority hook on the relay path, and degrades to
// Send when neither exists — availability first.
func (t *dataChannelTransport) SendPriority(data []byte) error {
	if t.sched != nil {
		return t.sched.enqueue(data, true)
	}
	if t.dc.SendPriority != nil {
		return t.dc.SendPriority(data)
	}
	return t.Send(data)
}

func (t *dataChannelTransport) OnMessage(h func([]byte)) {
	t.dc.OnMessage(h)
}

func (t *dataChannelTransport) Close() error {
	if t.sched != nil {
		t.sched.shutdown()
	}
	return t.dc.Close()
}

// Compile-time check: *dataChannelTransport implements Transport.
var _ Transport = (*dataChannelTransport)(nil)
var _ PrioritySender = (*dataChannelTransport)(nil)

// errEgressClosed is returned to senders parked on lane capacity once the
// scheduler has shut down (transport close or underlying Send failure).
var errEgressClosed = errors.New("transport egress closed")

// dcEgressScheduler is the strict-priority user-space egress for the WebRTC
// DataChannel path. One ordered SCTP stream is FIFO from the moment bytes
// enter pion's buffer, so the only place a latency-sensitive frame can jump
// ahead of a saturating bulk transfer is BEFORE dc.Send: the scheduler owns
// two lanes and admits frames to SCTP while BufferedAmount stays under the
// low watermark. Strict priority plus one-way (high→normal) stream demotion
// in the tunnel layer keep per-stream ordering intact.
type dcEgressScheduler struct {
	dc DataChannel

	mu        sync.Mutex
	high      [][]byte
	data      [][]byte
	highBytes int
	dataBytes int
	closed    bool
	failErr   error
	wake      chan struct{} // cap-1: nudges the writer and parked senders
	done      chan struct{} // closed exactly once on shutdown
}

func newDCEgressScheduler(dc DataChannel) *dcEgressScheduler {
	s := &dcEgressScheduler{
		dc:   dc,
		wake: make(chan struct{}, 1),
		done: make(chan struct{}),
	}
	go s.run()
	return s
}

// enqueue hands a frame to its lane, blocking while the lane sits over its
// byte cap (backpressure — saturation must surface to the caller, not grow
// unbounded scheduler memory). The first frame of an oversized message is
// always admitted so a frame larger than the cap cannot deadlock its own
// stream.
func (s *dcEgressScheduler) enqueue(data []byte, high bool) error {
	deadline := time.Now().Add(dcEgressParkBudget)
	for {
		s.mu.Lock()
		if s.closed || s.failErr != nil {
			err := s.failErr
			s.mu.Unlock()
			if err == nil {
				err = errEgressClosed
			}
			return err
		}
		cap, laneBytes, lane := dcEgressDataCap, &s.dataBytes, &s.data
		if high {
			cap, laneBytes, lane = dcEgressHighCap, &s.highBytes, &s.high
		}
		if *laneBytes+len(data) <= cap || len(*lane) == 0 {
			*lane = append(*lane, data)
			*laneBytes += len(data)
			s.mu.Unlock()
			s.signal()
			return nil
		}
		s.mu.Unlock()
		select {
		case <-s.wake:
		case <-s.done:
		case <-time.After(time.Until(deadline)):
			stallErr := fmt.Errorf("egress stalled over %s: peer not draining", dcEgressParkBudget)
			s.fail(stallErr)
			return stallErr
		}
	}
}

// run is the single writer goroutine. Poll-driven: BufferedAmount has no
// change callback at this abstraction, and a 5ms poll against a multi-second
// drain is negligible.
func (s *dcEgressScheduler) run() {
	for {
		if s.isClosed() {
			return
		}
		msg, ok := s.nextAdmissible()
		if !ok {
			select {
			case <-s.wake:
			case <-s.done:
			case <-time.After(dcEgressPollEvery):
			}
			continue
		}
		if err := s.dc.Send(msg); err != nil {
			s.fail(fmt.Errorf("egress send: %w", err))
			return
		}
		// Lane space freed — nudge parked enqueuers and re-evaluate.
		s.signal()
	}
}

// fail terminally fails the scheduler: the writer stops admitting, parked
// enqueuers unblock with err, queued frames are dropped, and later enqueues
// fail fast. Callers hold nothing — fail takes the lock itself.
func (s *dcEgressScheduler) fail(err error) {
	s.mu.Lock()
	if !s.closed {
		s.failErr = err
		s.closed = true
		close(s.done)
	}
	s.high, s.data = nil, nil
	s.highBytes, s.dataBytes = 0, 0
	s.mu.Unlock()
}

func (s *dcEgressScheduler) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

// nextAdmissible pops the next frame when SCTP is below the watermark,
// high lane first.
func (s *dcEgressScheduler) nextAdmissible() ([]byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.dc.BufferedAmount() >= uint64(dcEgressLowWater) {
		return nil, false
	}
	if len(s.high) > 0 {
		return s.shift(&s.high, &s.highBytes), true
	}
	if len(s.data) > 0 {
		return s.shift(&s.data, &s.dataBytes), true
	}
	return nil, false
}

func (s *dcEgressScheduler) shift(lane *[][]byte, laneBytes *int) []byte {
	msg := (*lane)[0]
	*lane = (*lane)[1:]
	*laneBytes -= len(msg)
	return msg
}

// shutdown stops the writer, wakes everyone parked on capacity, and drops
// queued frames. Senders observe errEgressClosed.
func (s *dcEgressScheduler) shutdown() {
	s.mu.Lock()
	if !s.closed {
		s.closed = true
		close(s.done)
	}
	s.high, s.data = nil, nil
	s.highBytes, s.dataBytes = 0, 0
	s.mu.Unlock()
}

func (s *dcEgressScheduler) signal() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}
