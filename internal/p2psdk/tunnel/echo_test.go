package tunnel_test

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	peer "github.com/Mi-Bee-Studio/MiBeeNvr/internal/p2psdk"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/p2psdk/tunnel"
)

// Pinned cross-language vectors (#87): the SAME bytes are asserted by the
// NVR Dart port's echo tests. Any change to the wire shape must change both
// sides and the relay.md vectors section together.
const (
	vectorEchoReqB64  = "ESIzRAECAwQFBgcIAAAABA=="
	vectorEchoRespB64 = "ESIzRAECAwQFBgcIAAAABAAAAAA="
)

// TestEchoWireVectors pins the on-wire payload layout: request 16 bytes
// (4B id | 8B t_send_ns | 4B want, BigEndian), response the same 16 bytes
// plus want zero bytes.
func TestEchoWireVectors(t *testing.T) {
	req, err := base64.StdEncoding.DecodeString(vectorEchoReqB64)
	require.NoError(t, err)
	require.Len(t, req, 16)
	assert.Equal(t, uint32(0x11223344), binary.BigEndian.Uint32(req[0:4]))
	assert.Equal(t, uint64(0x0102030405060708), binary.BigEndian.Uint64(req[4:12]))
	assert.Equal(t, uint32(4), binary.BigEndian.Uint32(req[12:16]))

	resp, err := base64.StdEncoding.DecodeString(vectorEchoRespB64)
	require.NoError(t, err)
	require.Len(t, resp, 20)
	assert.Equal(t, req, resp[:16], "response echoes id + t_send_ns verbatim")
	assert.Equal(t, make([]byte, 4), resp[16:], "padding is want zero bytes")
}

// TestEchoRoundTrip verifies the probe/responder pair over a full tunnel
// pair: the RTT is measured and repeated probes work (pending map hygiene).
func TestEchoRoundTrip(t *testing.T) {
	_, clientTun := setupTunnelPair(t)

	res, err := clientTun.ProbeEcho(context.Background(), 512, false)
	require.NoError(t, err)
	assert.False(t, res.LanePriority, "linkedPair transport has no priority lane")
	assert.Equal(t, 512, res.WantBytes)
	assert.GreaterOrEqual(t, res.RTT, time.Duration(0))
	assert.Less(t, res.RTT, 5*time.Second)

	res, err = clientTun.ProbeEcho(context.Background(), 64, false)
	require.NoError(t, err)
	assert.Equal(t, 64, res.WantBytes)
}

// TestEchoResponderPadsToWant intercepts the raw response frame on the
// wire: it must be an echo_resp envelope whose payload decodes to
// 16+want bytes — the deliberate same-size queueing measurement.
func TestEchoResponderPadsToWant(t *testing.T) {
	devDC, clientDC := linkedPair()

	// Instrument the device side's egress BEFORE the transport copies the
	// struct: every frame the responder sends is recorded, then delivered.
	var mu sync.Mutex
	var responses [][]byte
	origSend := devDC.Send
	devDC.Send = func(b []byte) error {
		mu.Lock()
		responses = append(responses, append([]byte(nil), b...))
		mu.Unlock()
		return origSend(b)
	}

	devTun := tunnel.New(peer.NewDataChannelTransport(devDC))
	clientTun := tunnel.New(peer.NewDataChannelTransport(clientDC))
	go devTun.Serve()    //nolint:errcheck
	go clientTun.Serve() //nolint:errcheck
	t.Cleanup(func() {
		_ = clientTun.Close()
		_ = devTun.Close()
	})

	_, err := clientTun.ProbeEcho(context.Background(), 300, false)
	require.NoError(t, err)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		fs := append([][]byte(nil), responses...)
		mu.Unlock()
		for _, raw := range fs {
			var m tunnel.TunnelMessage
			if json.Unmarshal(raw, &m) != nil || m.Type != tunnel.TunnelMessageTypeEchoResp {
				continue
			}
			payload, err := base64.StdEncoding.DecodeString(m.Payload)
			require.NoError(t, err)
			assert.Len(t, payload, 16+300, "response padded to want")
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("no echo_resp frame observed on the wire")
}

// TestEchoWantClamp: want above the 64 KiB cap is clamped.
func TestEchoWantClamp(t *testing.T) {
	_, clientTun := setupTunnelPair(t)
	res, err := clientTun.ProbeEcho(context.Background(), 1<<20, false)
	require.NoError(t, err)
	assert.Equal(t, 1<<16, res.WantBytes)
}

// TestEchoDefaultWantUsesFragmentSize: want<=0 defaults to the tunnel's
// data chunk size — the measurement convention that keeps T2 numbers
// comparable across clients.
func TestEchoDefaultWantUsesFragmentSize(t *testing.T) {
	_, clientTun := setupTunnelPair(t)
	res, err := clientTun.ProbeEcho(context.Background(), 0, false)
	require.NoError(t, err)
	assert.Equal(t, 16384, res.WantBytes, "must track MaxPayloadBytes (16 KiB alignment)")
}

// TestEchoUnsupportedShortCircuit: a legacy peer that never answers makes
// the prober time out; after 3 consecutive misses the tunnel flips to
// unsupported and later probes fail fast without sending. The ctx deadline
// clamps the per-probe timeout so the test stays fast.
func TestEchoUnsupportedShortCircuit(t *testing.T) {
	devDC, clientDC := linkedPair()
	_ = devDC // the device side never runs a Tunnel — legacy peer that
	// silently drops unknown frame types.

	clientTun := tunnel.New(peer.NewDataChannelTransport(clientDC))
	go clientTun.Serve() //nolint:errcheck
	t.Cleanup(func() { _ = clientTun.Close() })

	for i := 1; i <= 3; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		_, err := clientTun.ProbeEcho(ctx, 16, false)
		cancel()
		require.Error(t, err, "probe %d must time out", i)
	}
	assert.True(t, clientTun.EchoUnsupported())

	start := time.Now()
	_, err := clientTun.ProbeEcho(context.Background(), 16, false)
	require.ErrorIs(t, err, tunnel.ErrEchoUnsupported)
	assert.Less(t, time.Since(start), 20*time.Millisecond, "short-circuit must not send")
}

// TestEchoUnknownTypeIgnored: frames with unknown types neither crash the
// tunnel nor disturb a subsequent probe.
func TestEchoUnknownTypeIgnored(t *testing.T) {
	devDC, clientDC := linkedPair()
	devTun := tunnel.New(peer.NewDataChannelTransport(devDC))
	clientTun := tunnel.New(peer.NewDataChannelTransport(clientDC))
	go devTun.Serve()    //nolint:errcheck
	go clientTun.Serve() //nolint:errcheck
	t.Cleanup(func() {
		_ = clientTun.Close()
		_ = devTun.Close()
	})

	unknown, err := json.Marshal(tunnel.TunnelMessage{Type: "bogus_future_frame", Payload: "e30="})
	require.NoError(t, err)
	require.NoError(t, devDC.Send(unknown))

	res, err := clientTun.ProbeEcho(context.Background(), 64, false)
	require.NoError(t, err)
	assert.Equal(t, 64, res.WantBytes)
}

// TestEchoLaneTruth: the result reports whether the request lane was real.
// A plain transport (no PrioritySender) reports false even when priority
// was requested; a priority-capable transport reports true.
func TestEchoLaneTruth(t *testing.T) {
	devDC, clientDC := linkedPair()
	prioDC := withPriorityHook(clientDC)

	devTun := tunnel.New(peer.NewDataChannelTransport(devDC))
	clientTun := tunnel.New(peer.NewDataChannelTransport(prioDC))
	go devTun.Serve()    //nolint:errcheck
	go clientTun.Serve() //nolint:errcheck
	t.Cleanup(func() {
		_ = clientTun.Close()
		_ = devTun.Close()
	})

	res, err := clientTun.ProbeEcho(context.Background(), 64, false)
	require.NoError(t, err)
	assert.False(t, res.LanePriority, "priority not requested")

	res, err = clientTun.ProbeEcho(context.Background(), 64, true)
	require.NoError(t, err)
	assert.True(t, res.LanePriority, "dc priority hook counts as a real lane")
}

// withPriorityHook fakes a DataChannel with its own priority send hook so
// dataChannelTransport.PriorityActive() reports true.
func withPriorityHook(dc peer.DataChannel) peer.DataChannel {
	dc.SendPriority = func(b []byte) error { return dc.Send(b) }
	return dc
}
