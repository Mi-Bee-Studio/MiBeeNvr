package srt

import (
	"io"
	"net"
	"testing"
	"time"

	srt "github.com/datarhei/gosrt"
	"github.com/datarhei/gosrt/packet"
	"github.com/stretchr/testify/require"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/streamhub"
)

// mockConn implements srt.Conn for testing purposes.
type mockConn struct {
	streamID string
}

func (m *mockConn) Read(p []byte) (int, error)         { return 0, io.EOF }
func (m *mockConn) ReadPacket() (packet.Packet, error) { return nil, io.EOF }
func (m *mockConn) Write(p []byte) (int, error)        { return len(p), nil }
func (m *mockConn) WritePacket(p packet.Packet) error  { return nil }
func (m *mockConn) Close() error                       { return nil }
func (m *mockConn) LocalAddr() net.Addr                { return &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0} }

func (m *mockConn) RemoteAddr() net.Addr               { return &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0} }
func (m *mockConn) SetDeadline(t time.Time) error      { return nil }
func (m *mockConn) SetReadDeadline(t time.Time) error  { return nil }
func (m *mockConn) SetWriteDeadline(t time.Time) error { return nil }
func (m *mockConn) SocketId() uint32                   { return 0 }
func (m *mockConn) PeerSocketId() uint32               { return 0 }
func (m *mockConn) StreamId() string                   { return m.streamID }
func (m *mockConn) Stats(s *srt.Statistics)            {}
func (m *mockConn) Version() uint32                    { return 4 }

func TestHandlePublishPanicCleanup(t *testing.T) {
	t.Helper()

	ln := NewListener(config.SRTConfig{Port: 0})
	cameraID := "panic-test-cam"

	// Set OnConnect to panic — simulates a failure after the receiver is added to the map.
	// This exercises the defer-based panic recovery in handlePublish().
	ln.OnConnect = func(cid string, hub *streamhub.StreamHub) {
		panic("simulated panic for testing")
	}

	conn := &mockConn{streamID: cameraID}

	// handlePublish should NOT propagate the panic; the defer catches it and cleans up.
	require.NotPanics(t, func() {
		ln.handlePublish(conn)
	})

	// After cleanup, receiver must be removed — subsequent connections for the same
	// camera ID will not be blocked by a stale receiver entry.
	require.Equal(t, 0, ln.receiverCount())

	ln.mu.RLock()
	_, ok := ln.receivers[cameraID]
	ln.mu.RUnlock()
	require.False(t, ok, "camera should be removed from receivers map after panic")
}
