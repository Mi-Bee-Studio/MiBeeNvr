package tunnel

import (
	"encoding/base64"
	"encoding/binary"
	"errors"
	"math/rand"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	peer "github.com/Mi-Bee-Studio/MiBeeNvr/internal/p2psdk"
)

// Tunnel-layer echo frames (#87 T2 decomposition): one RTT measurement hop
// that sits between the transport RTT (T1, relay ping/pong) and the
// full-application RTT (T3, a tunneled API call). Echo is a tunnel-level
// GLOBAL frame — the same tier as open/close — because it measures the fixed
// "encapsulation + transport" cost and must not be bound to any business
// stream's lifecycle.
//
// Wire format (base64 payload inside the TunnelMessage envelope):
//
//	request:  4B client_id | 8B t_send_ns | 4B want_echo_bytes
//	response: 4B client_id | 8B t_send_ns | want_echo_bytes bytes
//
// The reply is padded to want_echo_bytes on purpose: an unpadded echo would
// measure a small-packet side channel and lie about queueing — the exact
// misread that made the three-lane fix look like it solved more than it did.
// The requester picks the lane for the REQUEST (normal vs priority); the
// reply always rides the priority path when the transport has one, because
// echo_resp is control-plane traffic. Callers record whether a priority lane
// was actually available: SendPriority may silently degrade to the normal
// path, and a sample taken on a degraded lane must not be read as
// "priority works".
const (
	echoHeaderLen    = 16 // 4B client_id | 8B t_send_ns | 4B want
	maxEchoWantBytes = 1 << 16
)

// probeEchoTimeout bounds one round trip. Generous on purpose: under video
// load the NORMAL lane is exactly the thing being measured — a tight timeout
// would censor the interesting samples.
var probeEchoTimeout = 8 * time.Second

// echoGiveUpAfter unanswered consecutive probes marks the peer as legacy
// (predates echo support). No retries afterwards — absence of a reply is a
// valid answer, not a failure.
const echoGiveUpAfter = 3

// ErrEchoUnsupported reports a peer that never answered echo probes (the
// tunnel stays fully functional; T2 is simply unmeasurable against it).
var ErrEchoUnsupported = errors.New("tunnel echo: peer unsupported")

// ErrEchoTimeout reports one missed round trip (peer busy or slow): callers
// keep sampling — ProbeEcho itself escalates to ErrEchoUnsupported after
// echoGiveUpAfter consecutive misses.
var ErrEchoTimeout = errors.New("tunnel echo: timeout")

// EchoProbeResult is one completed (or lane-observed) round trip.
type EchoProbeResult struct {
	RTT       time.Duration
	WantBytes int
	PrioLane  bool // the request went out on a real priority lane (not a silent degrade)
}

type echoState struct {
	mu           sync.Mutex
	pending      map[uint32]chan time.Duration
	unsupported  atomic.Bool
	consecMisses atomic.Int32
}

func (t *Tunnel) echo() *echoState {
	t.echoOnce.Do(func() { t.echoState = &echoState{pending: map[uint32]chan time.Duration{}} })
	return t.echoState
}

// ProbeEcho sends one echo request padded-reply size want and waits for the
// matching response. priority selects the request lane (the reply always
// rides the priority path when available). After echoGiveUpAfter consecutive
// timeouts the peer is marked legacy and every further call returns
// ErrEchoUnsupported without touching the wire.
func (t *Tunnel) ProbeEcho(want int, priority bool) (EchoProbeResult, error) {
	if want < 0 || want > maxEchoWantBytes {
		want = maxEchoWantBytes
	}
	st := t.echo()
	if st.unsupported.Load() {
		return EchoProbeResult{}, ErrEchoUnsupported
	}
	id := rand.Uint32() //nolint:gosec // probe correlation id, not a secret
	ch := make(chan time.Duration, 1)
	st.mu.Lock()
	if st.pending == nil {
		st.mu.Unlock()
		return EchoProbeResult{}, errors.New("tunnel echo: closed")
	}
	st.pending[id] = ch
	st.mu.Unlock()
	defer func() {
		st.mu.Lock()
		delete(st.pending, id)
		st.mu.Unlock()
	}()

	var payload [echoHeaderLen]byte
	binary.BigEndian.PutUint32(payload[0:4], id)
	binary.BigEndian.PutUint64(payload[4:12], uint64(time.Now().UnixNano()))
	binary.BigEndian.PutUint32(payload[12:16], uint32(want)) //nolint:gosec // bounded above
	msg := TunnelMessage{
		Type:    TunnelMessageTypeEcho,
		Payload: base64.StdEncoding.EncodeToString(payload[:]),
	}
	data, err := marshalTunnelMessage(msg)
	if err != nil {
		return EchoProbeResult{}, err
	}
	prioLane := false
	if priority {
		if err := peer.SendPriority(t.transport, data); err != nil {
			return EchoProbeResult{}, err
		}
		_, prioLane = t.transport.(peer.PrioritySender)
	} else if err := t.transport.Send(data); err != nil {
		return EchoProbeResult{}, err
	}

	select {
	case rtt := <-ch:
		st.consecMisses.Store(0)
		return EchoProbeResult{RTT: rtt, WantBytes: want, PrioLane: prioLane}, nil
	case <-time.After(probeEchoTimeout):
		if st.consecMisses.Add(1) >= echoGiveUpAfter {
			st.unsupported.Store(true)
			return EchoProbeResult{}, ErrEchoUnsupported
		}
		return EchoProbeResult{}, ErrEchoTimeout
	case <-t.closeCh:
		return EchoProbeResult{}, errors.New("tunnel echo: tunnel closed")
	}
}

// EchoUnsupported reports whether the peer was marked legacy (never answers).
func (t *Tunnel) EchoUnsupported() bool { return t.echo().unsupported.Load() }

// SetProbeEchoTimeoutForTest swaps the probe timeout and restores it via
// cleanup (the external test package cannot reach the package var).
func SetProbeEchoTimeoutForTest(tb testing.TB, d time.Duration) time.Duration {
	tb.Helper()
	saved := probeEchoTimeout
	probeEchoTimeout = d
	tb.Cleanup(func() { probeEchoTimeout = saved })
	return saved
}

// handleEcho answers an inbound request with a reply padded to want bytes.
// The reply rides the priority path when the transport has one (control
// plane); legacy peers never reach here.
func (t *Tunnel) handleEcho(payload string) {
	raw, err := base64.StdEncoding.DecodeString(payload)
	if err != nil || len(raw) != echoHeaderLen {
		t.logger.Warn("invalid echo request", "len", len(raw), "err", err)
		return
	}
	want := binary.BigEndian.Uint32(raw[12:16])
	if want > maxEchoWantBytes {
		want = maxEchoWantBytes // oversize requests truncate, not fail
	}
	resp := make([]byte, echoHeaderLen, echoHeaderLen+int(want))
	copy(resp, raw[:12]) // client_id + t_send_ns echoed verbatim
	resp = append(resp, make([]byte, int(want))...)
	msg := TunnelMessage{
		Type:    TunnelMessageTypeEchoResp,
		Payload: base64.StdEncoding.EncodeToString(resp),
	}
	data, err := marshalTunnelMessage(msg)
	if err != nil {
		return
	}
	_ = peer.SendPriority(t.transport, data)
}

// handleEchoResp delivers a reply to its matching pending probe. Replies
// with a smaller-than-requested body (legacy truncation) still count — the
// correlation id and send timestamp are in the header.
func (t *Tunnel) handleEchoResp(payload string) {
	raw, err := base64.StdEncoding.DecodeString(payload)
	if err != nil || len(raw) < echoHeaderLen {
		return
	}
	id := binary.BigEndian.Uint32(raw[0:4])
	sentNS := int64(binary.BigEndian.Uint64(raw[4:12]))
	st := t.echo()
	st.mu.Lock()
	ch, ok := st.pending[id]
	st.mu.Unlock()
	if !ok {
		return // stale or duplicate reply — no pending probe cares
	}
	rtt := time.Since(time.Unix(0, sentNS))
	// Guard against a peer echoing a bogus timestamp (clock skew across
	// devices does not exist here — both fields are OUR clock — but a
	// negative or absurd RTT means the frame is garbage.
	if rtt < 0 || rtt > 24*time.Hour {
		return
	}
	select {
	case ch <- rtt:
	default:
	}
}
