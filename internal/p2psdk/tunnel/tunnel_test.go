package tunnel_test

import (
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	peer "github.com/Mi-Bee-Studio/MiBeeNvr/internal/p2psdk"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/p2psdk/tunnel"
)

// linkedPair creates a pair of DataChannels wired together.
func linkedPair() (devDC, clientDC peer.DataChannel) {
	var devMu, clientMu sync.Mutex
	var devHandler, clientHandler func([]byte)

	devDC = peer.DataChannel{
		OnMessage: func(h func([]byte)) {
			devMu.Lock()
			devHandler = h
			devMu.Unlock()
		},
		Send: func(data []byte) error {
			clientMu.Lock()
			h := clientHandler
			clientMu.Unlock()
			if h != nil {
				h(data)
			}
			return nil
		},
		Close: func() error { return nil },
	}

	clientDC = peer.DataChannel{
		OnMessage: func(h func([]byte)) {
			clientMu.Lock()
			clientHandler = h
			clientMu.Unlock()
		},
		Send: func(data []byte) error {
			devMu.Lock()
			h := devHandler
			devMu.Unlock()
			if h != nil {
				h(data)
			}
			return nil
		},
		Close: func() error { return nil },
	}

	return devDC, clientDC
}

// pollRead reads from a net.Conn with polling until data or error.
func pollRead(t testing.TB, conn net.Conn, timeout time.Duration) ([]byte, error) {
	t.Helper()
	deadline := time.After(timeout)
	buf := make([]byte, 65536)
	for {
		select {
		case <-deadline:
			return nil, assert.AnError
		default:
		}
		n, err := conn.Read(buf)
		if n > 0 {
			return buf[:n], nil
		}
		if err != nil {
			return nil, err
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func startEchoServer(t testing.TB) (net.Listener, string) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				_, _ = io.Copy(conn, conn)
				_ = conn.Close()
			}()
		}
	}()

	return listener, listener.Addr().String()
}

func startHTTPServer(t testing.TB) (net.Listener, string) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })

	// Generate a large response payload spanning multiple tunnel fragments.
	largeBody := make([]byte, 100000)
	for i := range largeBody {
		largeBody[i] = byte('A' + i%26)
	}

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = conn.Close() }()
				buf := make([]byte, 4096)
				_, _ = conn.Read(buf)
				_, _ = conn.Write([]byte("HTTP/1.1 200 OK\r\nContent-Length: 20000\r\n\r\n"))
				_, _ = conn.Write(largeBody)
			}()
		}
	}()

	return listener, listener.Addr().String()
}

func setupTunnelPair(t testing.TB) (*tunnel.Tunnel, *tunnel.Tunnel) {
	t.Helper()
	devDC, clientDC := linkedPair()

	devTun := tunnel.New(peer.NewDataChannelTransport(devDC))
	clientTun := tunnel.New(peer.NewDataChannelTransport(clientDC))

	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		devTun.Serve()
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		clientTun.Serve()
	}()

	// Cleanup: LIFO order. Register wg.Wait FIRST so it runs LAST (after Close signals goroutines to exit)
	t.Cleanup(func() { wg.Wait() })
	t.Cleanup(func() { _ = clientTun.Close() })
	t.Cleanup(func() { _ = devTun.Close() })
	// Give goroutines time to start serving
	time.Sleep(50 * time.Millisecond)

	return devTun, clientTun
}

func TestTunnel_Echo(t *testing.T) {
	_, echoAddr := startEchoServer(t)
	devTun, clientTun := setupTunnelPair(t)

	devTun.RegisterService("echo", echoAddr)

	conn, err := clientTun.Dial("echo")
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	payload := []byte("hello tunnel")
	_, err = conn.Write(payload)
	require.NoError(t, err)

	got, err := pollRead(t, conn, 5*time.Second)
	require.NoError(t, err)
	assert.Equal(t, payload, got)
}

func TestTunnel_LargePayload(t *testing.T) {
	_, httpAddr := startHTTPServer(t)
	devTun, clientTun := setupTunnelPair(t)

	devTun.RegisterService("http", httpAddr)

	conn, err := clientTun.Dial("http")
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	// Send HTTP request
	req := []byte("GET / HTTP/1.1\r\nHost: test\r\n\r\n")
	_, err = conn.Write(req)
	require.NoError(t, err)

	// Expected: 42 bytes HTTP headers + 100000 bytes body = 100042
	const expectedTotal = 100042
	var response []byte
	deadline := time.After(10 * time.Second)
	for len(response) < expectedTotal {
		select {
		case <-deadline:
			t.Fatalf("timeout waiting for large response, got %d bytes of %d", len(response), expectedTotal)
		default:
		}
		buf := make([]byte, 65536)
		n, err := conn.Read(buf)
		if n > 0 {
			response = append(response, buf[:n]...)
		}
		if err != nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	assert.Len(t, response, expectedTotal, "should receive full HTTP response")
	assert.Contains(t, string(response), "200 OK")
	assert.Len(t, response[len(response)-100000:], 100000, "body should be 100000 bytes")
}

func TestTunnel_ConcurrentStreams(t *testing.T) {
	_, echoAddr := startEchoServer(t)
	devTun, clientTun := setupTunnelPair(t)

	devTun.RegisterService("echo", echoAddr)

	const numStreams = 8
	var wg sync.WaitGroup
	errs := make(chan error, numStreams)

	for i := range numStreams {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			conn, dialErr := clientTun.Dial("echo")
			if dialErr != nil {
				errs <- dialErr
				return
			}
			defer func() { _ = conn.Close() }()

			msg := []byte{byte('A' + id)} //nolint:gosec // G115: loop variable is bounded by small test count
			if _, writeErr := conn.Write(msg); writeErr != nil {
				errs <- writeErr
				return
			}

			got, readErr := pollRead(t, conn, 10*time.Second)
			if readErr != nil {
				errs <- readErr
				return
			}
			if len(got) != 1 || got[0] != msg[0] {
				errs <- assert.AnError
			}
		}(i)
	}

	wg.Wait()
	close(errs)

	for err := range errs {
		t.Errorf("concurrent stream error: %v", err)
	}
}

func TestTunnel_UnknownService(t *testing.T) {
	_, clientTun := setupTunnelPair(t)
	// Don't register any service
	conn, err := clientTun.Dial("nonexistent")
	require.NoError(t, err)

	// Write should succeed, but read should get close
	_, err = conn.Write([]byte("test"))
	require.NoError(t, err)

	_, err = pollRead(t, conn, 2*time.Second)
	assert.Error(t, err, "should get EOF or error for unknown service")
}

func TestTunnel_DoubleServe(_ *testing.T) {
	dc := peer.DataChannel{
		OnMessage: func(_ func([]byte)) {},
		Send:      func(_ []byte) error { return nil },
		Close:     func() error { return nil },
	}

	tun := tunnel.New(peer.NewDataChannelTransport(dc))
	// Calling Serve twice should not panic
	go tun.Serve()
	time.Sleep(10 * time.Millisecond)
	// Second call should be a no-op
	go tun.Serve()
	time.Sleep(10 * time.Millisecond)
	_ = tun.Close()
}

func TestTunnel_CloseIdempotent(t *testing.T) {
	dc := peer.DataChannel{
		OnMessage: func(_ func([]byte)) {},
		Send:      func(_ []byte) error { return nil },
		Close:     func() error { return nil },
	}

	tun := tunnel.New(peer.NewDataChannelTransport(dc))
	err := tun.Close()
	require.NoError(t, err)
	err = tun.Close()
	require.NoError(t, err)
}

func BenchmarkTunnelEcho(b *testing.B) {
	_, echoAddr := startEchoServer(b)
	devDC, clientDC := linkedPair()

	devTun := tunnel.New(peer.NewDataChannelTransport(devDC))
	devTun.RegisterService("echo", echoAddr)
	go devTun.Serve()
	defer func() { _ = devTun.Close() }()
	time.Sleep(50 * time.Millisecond)

	clientTun := tunnel.New(peer.NewDataChannelTransport(clientDC))
	go clientTun.Serve()
	defer func() { _ = clientTun.Close() }()
	time.Sleep(50 * time.Millisecond)

	conn, err := clientTun.Dial("echo")
	require.NoError(b, err)
	defer func() { _ = conn.Close() }()

	payload := []byte("hello")
	for b.Loop() {
		_, err = conn.Write(payload)
		require.NoError(b, err)
		_, err = pollRead(b, conn, 5*time.Second)
		require.NoError(b, err)
	}
}
