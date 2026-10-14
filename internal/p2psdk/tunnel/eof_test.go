package tunnel_test

import (
	"bufio"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestTunnel_RemoteCloseReturnsEOF verifies that after the remote service
// finishes writing and closes its end, a subsequent Read on the client
// streamConn returns io.EOF. Without this, http.ReadResponse (and any
// half-close-aware reader) blocks forever waiting for more data that will
// never arrive — this was the root cause of the demo echo client hanging.
func TestTunnel_RemoteCloseReturnsEOF(t *testing.T) {
	// HTTP server that writes a small response then closes the connection.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("bye"))
	}))
	t.Cleanup(srv.Close)

	devTun, clientTun := setupTunnelPair(t)
	devTun.RegisterService("http", srv.Listener.Addr().String())

	conn, err := clientTun.Dial("http")
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	// Trigger the response.
	_, err = conn.Write([]byte("GET / HTTP/1.1\r\nHost: test\r\nConnection: close\r\n\r\n"))
	require.NoError(t, err)

	// Read the full response (headers + "bye" body).
	buf := make([]byte, 4096)
	total, err := tryReadAll(t, conn, buf, 5*time.Second)
	require.NoError(t, err, "should read full response without error")
	assert.Greater(t, total, 0, "should have received response bytes")
	assert.True(t, strings.Contains(string(buf[:total]), "bye"),
		"response should contain body, got: %s", string(buf[:total]))

	// Now the remote has closed. A further Read must return io.EOF so that
	// readers like http.ReadResponse know the stream is complete.
	_, err = conn.Read(buf)
	assert.ErrorIs(t, err, io.EOF,
		"Read after remote close must return io.EOF, got %v", err)
}

// TestTunnel_HTTPReadResponseEndToEnd drives the full demo scenario: write an
// HTTP request over the tunnel, parse the response with http.ReadResponse, and
// confirm it completes (does not block). This is the regression test for the
// demo echo client hanging.
func TestTunnel_HTTPReadResponseEndToEnd(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("hello from device"))
	}))
	t.Cleanup(srv.Close)

	devTun, clientTun := setupTunnelPair(t)
	devTun.RegisterService("echo", srv.Listener.Addr().String())

	conn, err := clientTun.Dial("echo")
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	_, err = conn.Write([]byte("GET / HTTP/1.1\r\nHost: echo\r\nConnection: close\r\n\r\n"))
	require.NoError(t, err)

	done := make(chan struct{})
	go func() {
		defer close(done)
		resp, rerr := http.ReadResponse(bufio.NewReader(conn), nil)
		require.NoError(t, rerr, "http.ReadResponse must complete")
		_ = resp.Body.Close()
	}()

	select {
	case <-done:
		// success — ReadResponse completed.
	case <-time.After(15 * time.Second):
		t.Fatal("http.ReadResponse blocked past 5s — remote close EOF not propagated")
	}
}

// tryReadAll reads from conn until a read returns fewer bytes than requested
// (a "short read" indicating the remote flushed a response) or the deadline.
// It returns the total bytes read. Unlike io.ReadFull it tolerates partial
// delivery.
func tryReadAll(t *testing.T, conn io.Reader, buf []byte, timeout time.Duration) (int, error) {
	t.Helper()
	deadline := time.After(timeout)
	total := 0
	for total < len(buf) {
		select {
		case <-deadline:
			return total, nil
		default:
		}
		n, err := conn.Read(buf[total:])
		total += n
		if err != nil {
			return total, err
		}
		if n == 0 {
			return total, nil
		}
		// Give the remote a brief window to close, then return what we have.
		if total > 0 {
			time.Sleep(50 * time.Millisecond)
			return total, nil
		}
	}
	return total, nil
}
