package tunnel_test

import (
	"bytes"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	peer "github.com/Mi-Bee-Studio/MiBeeNvr/internal/p2psdk"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/p2psdk/tunnel"
)

// TestTunnel_InteropMixedFragmentSizes pins cross-version interop when the
// two ends fragment at different sizes (a legacy 32 KiB sender against the
// 16 KiB default, in both directions): receivers reassemble by seq/total and
// never assume a fixed fragment size.
func TestTunnel_InteropMixedFragmentSizes(t *testing.T) {
	devDC, clientDC := linkedPair()
	devTun := tunnel.New(peer.NewDataChannelTransport(devDC),
		tunnel.WithMaxPayloadBytes(32*1024)) // legacy fragment size
	clientTun := tunnel.New(peer.NewDataChannelTransport(clientDC)) // default 16 KiB

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); devTun.Serve() }()
	go func() { defer wg.Done(); clientTun.Serve() }()
	t.Cleanup(func() { wg.Wait() })
	t.Cleanup(func() { _ = clientTun.Close() })
	t.Cleanup(func() { _ = devTun.Close() })
	time.Sleep(50 * time.Millisecond)

	_, echoAddr := startEchoServer(t)
	devTun.RegisterService("echo", echoAddr)

	conn, err := clientTun.Dial("echo")
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	// Larger than 2×32 KiB so BOTH directions carry multi-fragment streams:
	// client→dev fragments at 16 KiB, the dev echo reply at 32 KiB.
	payload := make([]byte, 100_000)
	for i := range payload {
		payload[i] = byte('a' + i%26)
	}
	_, err = conn.Write(payload)
	require.NoError(t, err)

	var got []byte
	deadline := time.After(10 * time.Second)
	for len(got) < len(payload) {
		select {
		case <-deadline:
			t.Fatalf("timeout: echoed %d of %d bytes", len(got), len(payload))
		default:
		}
		buf := make([]byte, 65536)
		n, rerr := conn.Read(buf)
		got = append(got, buf[:n]...)
		if rerr != nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	assert.True(t, bytes.Equal(payload, got),
		"echo mismatch across mixed fragment sizes (got %d bytes)", len(got))
}
