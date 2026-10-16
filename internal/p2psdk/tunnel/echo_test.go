package tunnel_test

import (
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

// prioTransport wraps a transport adding SendPriority, so tests can observe
// the PrioLane flag flipping with the transport's actual capability.
type prioTransport struct {
	peer.Transport
	prioSeen sync.Mutex
	sawPrio  bool
}

func (p *prioTransport) SendPriority(data []byte) error {
	p.prioSeen.Lock()
	p.sawPrio = true
	p.prioSeen.Unlock()
	return p.Transport.Send(data)
}

func startTunnel(t *testing.T, tr peer.Transport) *tunnel.Tunnel {
	t.Helper()
	tun := tunnel.New(tr)
	go tun.Serve()
	t.Cleanup(func() { _ = tun.Close() })
	time.Sleep(20 * time.Millisecond)
	return tun
}

// TestEcho_RoundTrip covers the T2 probe against a real paired responder:
// RTT sane, payload padding honored (want bytes come back), and BOTH lanes
// report.
func TestEcho_RoundTrip(t *testing.T) {
	devDC, clientDC := linkedPair()
	_ = startTunnel(t, peer.NewDataChannelTransport(devDC))
	clientTun := startTunnel(t, peer.NewDataChannelTransport(clientDC))

	normal, err := clientTun.ProbeEcho(512, false)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, normal.RTT, time.Duration(0))
	assert.Less(t, normal.RTT, 2*time.Second, "in-process pair must answer fast")
	assert.Equal(t, 512, normal.WantBytes)
	assert.False(t, normal.PrioLane, "linkedPair DataChannel has no priority sender — degrade must be recorded")

	_, err = clientTun.ProbeEcho(32*1024, true)
	require.NoError(t, err, "priority-lane probe on a degrading transport still measures (normal path)")
}

// TestEcho_WantBytesClampedAndPadded: an oversize request truncates to the
// ceiling rather than failing, and the reply body really is want bytes (the
// same-size-as-data guarantee the measurement depends on).
func TestEcho_WantBytesClampedAndPadded(t *testing.T) {
	raw := &recordingTransport{}
	_ = startTunnel(t, raw)

	// Drive the responder's handler with a crafted request via its own
	// inbound path.
	var payload [16]byte
	binary.BigEndian.PutUint32(payload[12:16], uint32(1<<20)) // over the ceiling
	req := tunnelMessageJSON(t, "echo", payload[:])
	raw.deliver(req)
	time.Sleep(50 * time.Millisecond)

	frame := raw.lastFrame("echo_resp")
	require.NotNil(t, frame, "responder must reply")
	decoded, err := base64.StdEncoding.DecodeString(frame.Payload)
	require.NoError(t, err)
	assert.Len(t, decoded, 16+65536, "oversize want truncates to the ceiling, header + padded body")
}

// TestEcho_StaleReplyIgnored: a reply with no matching pending probe (stale,
// duplicate, or bogus id) is dropped without breaking the next real probe.
func TestEcho_StaleReplyIgnored(t *testing.T) {
	devDC, clientDC := linkedPair()
	_ = startTunnel(t, peer.NewDataChannelTransport(devDC))
	clientTun := startTunnel(t, peer.NewDataChannelTransport(clientDC))

	// Fabricate a reply for an id nobody asked about, delivered INTO the
	// client tunnel from the peer side.
	var bogus [16]byte
	binary.BigEndian.PutUint32(bogus[0:4], 0xdeadbeef)
	binary.BigEndian.PutUint64(bogus[4:12], uint64(time.Now().UnixNano()))
	err := devDC.Send(tunnelMessageJSON(t, "echo_resp", bogus[:]))
	require.NoError(t, err)
	time.Sleep(50 * time.Millisecond)

	res, err := clientTun.ProbeEcho(64, false)
	require.NoError(t, err, "stale reply must not break the next probe")
	assert.Less(t, res.RTT, 2*time.Second)
}

// TestEcho_UnknownTypeIgnored: frames of a type this build predates must not
// throw or disconnect — the tunnel keeps serving business frames afterwards.
func TestEcho_UnknownTypeIgnored(t *testing.T) {
	devDC, clientDC := linkedPair()
	devTun := startTunnel(t, peer.NewDataChannelTransport(devDC))
	clientTun := startTunnel(t, peer.NewDataChannelTransport(clientDC))

	err := devDC.Send([]byte(`{"type":"frame_from_the_future","payload":""}`))
	require.NoError(t, err)
	time.Sleep(30 * time.Millisecond)

	// Business traffic and echo still flow through the same pair.
	_, echoAddr := startEchoServer(t)
	devTun.RegisterService("echo", echoAddr)
	conn, err := clientTun.Dial("echo")
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	_, err = conn.Write([]byte("ping"))
	require.NoError(t, err)
	got, err := pollRead(t, conn, 5*time.Second)
	require.NoError(t, err)
	assert.Equal(t, []byte("ping"), got)

	res, err := clientTun.ProbeEcho(64, false)
	require.NoError(t, err)
	assert.Less(t, res.RTT, 2*time.Second)
}

// TestEcho_UnsupportedAfterTimeouts: a silent peer marks unsupported after
// three consecutive misses and further probes short-circuit without wire
// traffic (legacy peer — absence of a reply is an answer).
func TestEcho_UnsupportedAfterTimeouts(t *testing.T) {
	tunnel.SetProbeEchoTimeoutForTest(t, 60*time.Millisecond)
	tun := tunnel.New(&dropTransport{})
	go tun.Serve()
	t.Cleanup(func() { _ = tun.Close() })

	for i := 0; i < 3; i++ {
		_, err := tun.ProbeEcho(64, false)
		require.Error(t, err, "probe %d must fail", i)
		if i < 2 {
			assert.NotErrorIs(t, err, tunnel.ErrEchoUnsupported, "misses 1-2 are plain timeouts")
		} else {
			assert.ErrorIs(t, err, tunnel.ErrEchoUnsupported, "3rd miss marks the peer legacy")
		}
	}
	assert.True(t, tun.EchoUnsupported())
	start := time.Now()
	_, err := tun.ProbeEcho(64, false)
	assert.ErrorIs(t, err, tunnel.ErrEchoUnsupported)
	assert.Less(t, time.Since(start), 30*time.Millisecond, "short-circuits, no wire wait")
}

// TestEcho_PriorityLaneFlag: with a transport that really implements
// SendPriority the probe reports PrioLane=true (no silent-degrade misread).
func TestEcho_PriorityLaneFlag(t *testing.T) {
	devDC, clientDC := linkedPair()
	_ = startTunnel(t, peer.NewDataChannelTransport(devDC))
	wrapped := &prioTransport{Transport: peer.NewDataChannelTransport(clientDC)}
	clientTun := startTunnel(t, wrapped)

	res, err := clientTun.ProbeEcho(64, true)
	require.NoError(t, err)
	assert.True(t, res.PrioLane, "priority-capable transport must be reported as such")

	res, err = clientTun.ProbeEcho(64, false)
	require.NoError(t, err)
	assert.False(t, res.PrioLane, "normal-lane probe must not claim the priority lane")
}

// --- helpers -------------------------------------------------------------

func tunnelMessageJSON(t *testing.T, typ string, payload []byte) []byte {
	t.Helper()
	data, err := json.Marshal(map[string]string{
		"type":    typ,
		"payload": base64.StdEncoding.EncodeToString(payload),
	})
	require.NoError(t, err)
	return data
}

// dropTransport accepts sends but never answers (a silent legacy peer).
type dropTransport struct{ sink func([]byte) }

func (d *dropTransport) Send([]byte) error        { return nil }
func (d *dropTransport) OnMessage(h func([]byte)) { d.sink = h }
func (d *dropTransport) Close() error             { return nil }

// recordingTransport captures frames the tunnel sends (for asserting the
// responder's wire output without a full pair).
type recordingTransport struct {
	mu     sync.Mutex
	frames []tunnel.TunnelMessage
	sink   func([]byte)
}

func (r *recordingTransport) Send(data []byte) error {
	var msg tunnel.TunnelMessage
	if err := json.Unmarshal(data, &msg); err == nil {
		r.mu.Lock()
		r.frames = append(r.frames, msg)
		r.mu.Unlock()
	}
	if r.sink != nil {
		r.sink(data)
	}
	return nil
}
func (r *recordingTransport) OnMessage(h func([]byte)) { r.sink = h }
func (r *recordingTransport) Close() error             { return nil }

func (r *recordingTransport) lastFrame(typ string) *tunnel.TunnelMessage {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := len(r.frames) - 1; i >= 0; i-- {
		if string(r.frames[i].Type) == typ {
			return &r.frames[i]
		}
	}
	return nil
}

func (r *recordingTransport) deliver(data []byte) {
	if r.sink != nil {
		r.sink(data)
	}
}
