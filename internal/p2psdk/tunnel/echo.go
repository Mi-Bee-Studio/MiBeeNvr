package tunnel

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"time"

	peer "github.com/Mi-Bee-Studio/MiBeeNvr/internal/p2psdk"
)

// T2 echo probe (#87): a one-shot tunnel-layer RTT measurement frame pair.
//
// Wire shape — byte-identical with the NVR Dart port and pinned by the
// cross-language test vectors (see the echo section of relay.md; the Go and
// Dart unit tests assert the same bytes):
//
//	echo      payload = base64( 4B client_id | 8B t_send_ns | 4B want_echo_bytes )
//	echo_resp payload = base64( 4B client_id | 8B t_send_ns | want bytes (zeros) )
//
// All integers BigEndian. want is capped at maxEchoWantBytes. The response
// is padded to want deliberately: an unpadded echo would measure a
// "small-packet special lane" and misread what the three-lane fix actually
// bought. The responder always answers on the transport's priority path
// (control plane; harmless fallback when absent); the prober picks its
// request lane and reports the truth of whether that lane was real. Go does
// not validate want or the padding in the response — Dart implementations
// may leave zeros.
//
// Echo frames are NOT E2EE-sealed: they are synthetic, carry no user data,
// and measure the path rather than protect content.

// maxEchoWantBytes caps the echo response size (matches the Dart port's
// _echoMaxWant; also the historical 64 KiB SCTP message ceiling).
const maxEchoWantBytes = 1 << 16

// echoProbeTimeout bounds a single probe round trip.
var echoProbeTimeout = 5 * time.Second

// echoUnsupportedAfter unanswered probes (consecutive) short-circuit further
// probing — a silent peer is a valid answer ("T2 unsupported"), not a
// failure to retry into.
const echoUnsupportedAfter = 3

// ErrEchoUnsupported is returned once the peer has proven (by silence)
// that it does not implement the echo responder.
var ErrEchoUnsupported = errors.New("tunnel: peer does not answer echo (t2 unsupported)")

// EchoResult is one completed T2 probe.
type EchoResult struct {
	// RTT is the tunnel-hop round trip measured with the local clock.
	RTT time.Duration
	// LanePriority reports whether the REQUEST actually rode a priority
	// path — false when priority was not requested OR the transport
	// silently degrades (peer.SendPriority may fall back). Samples must
	// not be read as "priority effective" when this is false.
	LanePriority bool
	// WantBytes is the (clamped) echo size actually used.
	WantBytes int
}

// echo response delivery channel payload.
type echoAnswer struct {
	tSendNS int64
	recvAt  time.Time
}

// priorityLaneActive reports the truth of "a SendPriority on this transport
// actually rides a priority path" (#87): false for plain transports, the
// transport's own negotiated answer when it exposes PriorityActive (relay
// client: capability bit; datachannel: scheduler present), and merely
// "implements PrioritySender" otherwise.
func priorityLaneActive(t peer.Transport) bool {
	if pa, ok := t.(interface{ PriorityActive() bool }); ok {
		return pa.PriorityActive()
	}
	_, ok := t.(peer.PrioritySender)
	return ok
}

// EchoUnsupported reports whether T2 probing has been short-circuited for
// this tunnel (peer never answers echo).
func (t *Tunnel) EchoUnsupported() bool {
	t.echoMu.Lock()
	defer t.echoMu.Unlock()
	return t.echoUnsupported
}

// ProbeEcho sends one T2 echo and waits for the response (#87).
//
// wantBytes <= 0 defaults to the tunnel's fragment size — the measurement
// convention is "want defaults to the data chunk size", keeping T2 numbers
// comparable across clients. priority selects the request lane; the result
// reports the lane truth. Legacy peers never answer: after
// echoUnsupportedAfter consecutive misses the tunnel marks T2 unsupported
// and later calls return ErrEchoUnsupported without sending.
func (t *Tunnel) ProbeEcho(ctx context.Context, wantBytes int, priority bool) (EchoResult, error) {
	t.echoMu.Lock()
	if t.echoUnsupported {
		t.echoMu.Unlock()
		return EchoResult{}, ErrEchoUnsupported
	}
	t.echoMu.Unlock()

	if wantBytes <= 0 {
		wantBytes = t.fragmentSize()
	}
	if wantBytes > maxEchoWantBytes {
		wantBytes = maxEchoWantBytes
	}

	id := rand.Uint32()
	ch := make(chan echoAnswer, 1)
	t.echoMu.Lock()
	if t.echoPending == nil {
		t.echoPending = make(map[uint32]chan echoAnswer)
	}
	t.echoPending[id] = ch
	t.echoMu.Unlock()
	defer func() {
		t.echoMu.Lock()
		delete(t.echoPending, id)
		t.echoMu.Unlock()
	}()

	lanePriority := priority && priorityLaneActive(t.transport)
	req := make([]byte, 16)
	binary.BigEndian.PutUint32(req[0:4], id)
	binary.BigEndian.PutUint64(req[4:12], uint64(time.Now().UnixNano()))
	binary.BigEndian.PutUint32(req[12:16], uint32(wantBytes))
	msg := TunnelMessage{
		Type:    TunnelMessageTypeEcho,
		Payload: base64.StdEncoding.EncodeToString(req),
	}
	data, err := marshalTunnelMessage(msg)
	if err != nil {
		return EchoResult{}, err
	}
	var sendErr error
	if priority {
		sendErr = peer.SendPriority(t.transport, data)
	} else {
		sendErr = t.transport.Send(data)
	}
	if sendErr != nil {
		return EchoResult{}, fmt.Errorf("tunnel: echo send: %w", sendErr)
	}

	timeout := echoProbeTimeout
	if deadline, ok := ctx.Deadline(); ok {
		if d := time.Until(deadline); d < timeout {
			timeout = d
		}
	}
	select {
	case ans := <-ch:
		rtt := ans.recvAt.Sub(time.Unix(0, ans.tSendNS))
		if rtt < 0 {
			rtt = 0 // clock skew guard; both stamps are the local clock
		}
		t.echoMu.Lock()
		t.echoMisses = 0
		t.echoMu.Unlock()
		return EchoResult{RTT: rtt, LanePriority: lanePriority, WantBytes: wantBytes}, nil
	case <-time.After(timeout):
	case <-ctx.Done():
	}
	t.echoMu.Lock()
	t.echoMisses++
	misses := t.echoMisses
	if misses >= echoUnsupportedAfter {
		t.echoUnsupported = true // silence is a valid answer: stop probing
	}
	t.echoMu.Unlock()
	return EchoResult{}, fmt.Errorf("tunnel: echo timed out after %s (miss %d/%d)",
		timeout, misses, echoUnsupportedAfter)
}

// handleEcho answers one echo request. The reply always rides the priority
// path — it is control-plane traffic and must not queue behind bulk data;
// transports without a priority lane fall back harmlessly.
func (t *Tunnel) handleEcho(payload string) {
	raw, err := base64.StdEncoding.DecodeString(payload)
	if err != nil || len(raw) < 16 {
		t.logger.Debug("invalid echo request", "len", len(raw))
		return
	}
	want := binary.BigEndian.Uint32(raw[12:16])
	if int(want) > maxEchoWantBytes {
		want = maxEchoWantBytes
	}
	resp := make([]byte, 16+int(want))
	copy(resp[0:16], raw[0:16]) // id + t_send_ns verbatim (padding stays zero)
	data, err := marshalTunnelMessage(TunnelMessage{
		Type:    TunnelMessageTypeEchoResp,
		Payload: base64.StdEncoding.EncodeToString(resp),
	})
	if err != nil {
		return
	}
	if err := peer.SendPriority(t.transport, data); err != nil {
		t.logger.Debug("echo reply send failed", "error", err)
	}
}

// handleEchoResp matches a response to its pending probe and delivers the
// timing. Unknown ids (stale, duplicate, spoofed) are dropped silently.
func (t *Tunnel) handleEchoResp(payload string) {
	raw, err := base64.StdEncoding.DecodeString(payload)
	if err != nil || len(raw) < 16 {
		return
	}
	id := binary.BigEndian.Uint32(raw[0:4])
	t.echoMu.Lock()
	ch, ok := t.echoPending[id]
	t.echoMu.Unlock()
	if !ok {
		return
	}
	select {
	case ch <- echoAnswer{
		tSendNS: int64(binary.BigEndian.Uint64(raw[4:12])),
		recvAt:  time.Now(),
	}:
	default: // probe already timed out
	}
}

// marshalTunnelMessage serializes one envelope frame (json.Marshal with an
// error, unlike the hot-path mustJSON helper).
func marshalTunnelMessage(msg TunnelMessage) ([]byte, error) {
	return json.Marshal(msg)
}
