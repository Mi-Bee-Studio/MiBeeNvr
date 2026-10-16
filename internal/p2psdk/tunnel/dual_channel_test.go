package tunnel_test

import (
	"context"
	"encoding/json"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	peer "github.com/Mi-Bee-Studio/MiBeeNvr/internal/p2psdk"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/p2psdk/tunnel"
)

// TestCtrlChannelNegotiationFieldCompat (#88①): the wire field is additive —
// JSON from an old peer (field absent) decodes to false, so an old requester
// never triggers a second channel on a new device.
func TestCtrlChannelNegotiationFieldCompat(t *testing.T) {
	var req peer.ConnectRequest
	require.NoError(t, json.Unmarshal([]byte(`{
		"target_peer_id": "device-1",
		"session_id": "s1",
		"service_name": "echo"
	}`), &req))
	assert.False(t, req.CtrlChannel, "absent field must decode false (old peer)")

	out, err := json.Marshal(peer.ConnectRequest{TargetPeerID: "d", SessionID: "s", CtrlChannel: true})
	require.NoError(t, err)
	assert.Contains(t, string(out), `"ctrl_channel":true`)
}

// TestEarlyDataBeforeOpenAcrossLanes (#88①): data frames that overtake their
// open across the ctrl/data channels are buffered and delivered in order
// once the open lands — not dropped as bogus traffic.
func TestEarlyDataBeforeOpenAcrossLanes(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })
	srvCh := make(chan dualTestRead, 4)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		buf := make([]byte, 4096)
		for {
			n, err := conn.Read(buf)
			if n > 0 {
				b := make([]byte, n)
				copy(b, buf[:n])
				srvCh <- dualTestRead{data: b}
			}
			if err != nil {
				srvCh <- dualTestRead{err: err}
				return
			}
		}
	}()

	devDC, clientDC := linkedPair()
	devTun := tunnel.New(peer.NewDataChannelTransport(devDC))
	clientTun := tunnel.New(peer.NewDataChannelTransport(clientDC))
	go devTun.Serve()    //nolint:errcheck
	go clientTun.Serve() //nolint:errcheck
	t.Cleanup(func() {
		_ = clientTun.Close()
		_ = devTun.Close()
	})
	devTun.RegisterService("svc", listener.Addr().String())

	// Data FIRST (stream 9), then the open — the exact cross-channel
	// reorder the dual-channel path makes possible.
	mustSend := func(v any) {
		b, err := json.Marshal(v)
		require.NoError(t, err)
		require.NoError(t, clientDC.Send(b))
	}
	early1 := tunnel.TunnelData{StreamID: 9, Seq: 0, Total: 1, Payload: "ZWFybHktMQ=="} // "early-1"
	early2 := tunnel.TunnelData{StreamID: 9, Seq: 0, Total: 1, Payload: "ZWFybHktMg=="} // "early-2"
	openMsg := tunnel.TunnelOpen{StreamID: 9, TargetService: "svc"}
	live := tunnel.TunnelData{StreamID: 9, Seq: 0, Total: 1, Payload: "bGl2ZQ=="} // "live"
	mustSend(tunnel.TunnelMessage{Type: tunnel.TunnelMessageTypeData, Payload: mustJSONString(t, early1)})
	mustSend(tunnel.TunnelMessage{Type: tunnel.TunnelMessageTypeData, Payload: mustJSONString(t, early2)})
	mustSend(tunnel.TunnelMessage{Type: tunnel.TunnelMessageTypeOpen, Payload: mustJSONString(t, openMsg)})
	mustSend(tunnel.TunnelMessage{Type: tunnel.TunnelMessageTypeData, Payload: mustJSONString(t, live)})

	// The two buffered early payloads arrive first, in order, then live.
	got := readN(t, srvCh, 3, 3*time.Second)
	assert.Equal(t, "early-1", string(got[0]))
	assert.Equal(t, "early-2", string(got[1]))
	assert.Equal(t, "live", string(got[2]))
}

// TestEarlyDataLostOpenAgesOut (#88①): data with NO open ever arriving is
// discarded after the TTL — the buffer is bounded, not a leak.
func TestEarlyDataLostOpenAgesOut(t *testing.T) {
	devDC, clientDC := linkedPair()
	devTun := tunnel.New(peer.NewDataChannelTransport(devDC))
	clientTun := tunnel.New(peer.NewDataChannelTransport(clientDC))
	go devTun.Serve()    //nolint:errcheck
	go clientTun.Serve() //nolint:errcheck
	t.Cleanup(func() {
		_ = clientTun.Close()
		_ = devTun.Close()
	})

	b, err := json.Marshal(tunnel.TunnelMessage{
		Type:    tunnel.TunnelMessageTypeData,
		Payload: mustJSONString(t, tunnel.TunnelData{StreamID: 42, Seq: 0, Total: 1, Payload: "eA=="}),
	})
	require.NoError(t, err)
	require.NoError(t, clientDC.Send(b))

	// No open ever comes; wait past the TTL and confirm the tunnel is
	// still healthy (a probe still round-trips).
	time.Sleep(earlyDataTTLOverride() + 300*time.Millisecond)
	res, err := clientTun.ProbeEcho(context.Background(), 16, false)
	require.NoError(t, err, "tunnel must stay healthy after orphan data ages out")
	assert.Equal(t, 16, res.WantBytes)
}

// mustJSONString marshals v to a string payload (the envelope carries JSON
// payloads as strings).
func mustJSONString(t testing.TB, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return string(b)
}

// dualTestRead is one read result from the backing TCP service.
type dualTestRead struct {
	data []byte
	err  error
}

func readN(t testing.TB, ch chan dualTestRead, n int, timeout time.Duration) [][]byte {
	t.Helper()
	var out [][]byte
	deadline := time.After(timeout)
	for len(out) < n {
		select {
		case r := <-ch:
			require.NoError(t, r.err)
			out = append(out, r.data)
		case <-deadline:
			for i, v := range out {
				t.Logf("payload[%d]=%q", i, string(v))
			}
			t.Fatalf("timeout: got %d/%d payloads", len(out), n)
		}
	}
	return out
}

// earlyDataTTLOverride exposes the package TTL for tests via a registered
// hook (the tunnel package exports it for the external test package).
func earlyDataTTLOverride() time.Duration { return tunnel.EarlyDataTTLForTest() }
