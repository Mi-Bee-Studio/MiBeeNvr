package srt

import (
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	gosrt "github.com/datarhei/gosrt"
	"github.com/datarhei/gosrt/packet"
	"github.com/stretchr/testify/require"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
)

func TestNewReceiver(t *testing.T) {
	t.Helper()

	stream := config.SRTStream{
		CameraID:   "test-cam",
		Mode:       "listener",
		Passphrase: "test-password-123",
		StreamID:   "test-stream",
	}
	hub := model.NewStreamHub()
	rec := NewReceiver(stream, hub)

	require.Equal(t, "test-cam", rec.cameraID)
	require.Equal(t, "listener", rec.mode)
	require.NotNil(t, rec.hub)
	require.False(t, rec.Running())
}

func TestReceiverStopWithoutStart(t *testing.T) {
	t.Helper()

	rec := NewReceiver(config.SRTStream{CameraID: "test", Mode: "listener"}, model.NewStreamHub())
	err := rec.Stop()
	require.NoError(t, err)
}

func TestReceiverFrameDistribution(t *testing.T) {
	t.Helper()

	hub := model.NewStreamHub()
	stream := config.SRTStream{
		CameraID: "dist-test",
		Mode:     "listener",
	}

	_ = NewReceiver(stream, hub)

	// Subscribe a consumer to the hub
	var receivedFrames atomic.Pointer[[][]byte]
	var receivedPTS atomic.Int64
	var frameCount atomic.Int32

	err := hub.Subscribe("test-consumer", func(pts int64, au [][]byte) {
		frameCount.Add(1)
		receivedPTS.Store(pts)
		// Store a copy of au
		auCopy := make([][]byte, len(au))
		for i, b := range au {
			c := make([]byte, len(b))
			copy(c, b)
			auCopy[i] = c
		}
		receivedFrames.Store(&auCopy)
	})
	require.NoError(t, err)
	defer hub.Unsubscribe("test-consumer")

	// Broadcast a frame through the hub directly
	testPTS := int64(90000)
	testAU := [][]byte{
		{0x67, 0x42}, // SPS
		{0x65, 0xb8}, // IDR
	}
	hub.Broadcast(testPTS, testAU, false)

	// Verify consumer received the frame
	require.Eventually(t, func() bool {
		return frameCount.Load() > 0
	}, 2*time.Second, 10*time.Millisecond, "consumer should receive broadcast frame")

	require.Equal(t, testPTS, receivedPTS.Load())
	stored := receivedFrames.Load()
	require.NotNil(t, stored)
	require.Equal(t, testAU, *stored)
}

func TestStreamHubMultipleConsumers(t *testing.T) {
	t.Helper()

	hub := model.NewStreamHub()

	var count1, count2 atomic.Int32

	err := hub.Subscribe("consumer-1", func(pts int64, au [][]byte) {
		count1.Add(1)
	})
	require.NoError(t, err)

	err = hub.Subscribe("consumer-2", func(pts int64, au [][]byte) {
		count2.Add(1)
	})
	require.NoError(t, err)

	hub.Broadcast(90000, [][]byte{{0x65}}, false)
	hub.Broadcast(94500, [][]byte{{0x41}}, false)

	require.Eventually(t, func() bool {
		return count1.Load() == 2 && count2.Load() == 2
	}, 2*time.Second, 10*time.Millisecond)

	hub.Unsubscribe("consumer-1")
	hub.Unsubscribe("consumer-2")
}

func TestNewListener(t *testing.T) {
	t.Helper()

	cfg := config.SRTConfig{
		Port: 9001,
	}
	ln := NewListener(cfg)

	require.False(t, ln.running)
	require.Equal(t, 0, ln.receiverCount())
}

func TestListenerRegisterHub(t *testing.T) {
	t.Helper()

	ln := NewListener(config.SRTConfig{Port: 9002})
	hub := model.NewStreamHub()

	ln.registerHub("test-cam", hub)

	ln.mu.RLock()
	registered, ok := ln.hubs["test-cam"]
	ln.mu.RUnlock()

	require.True(t, ok)
	require.Equal(t, hub, registered)
}

func TestListenerUnregisterHub(t *testing.T) {
	t.Helper()

	ln := NewListener(config.SRTConfig{Port: 9003})
	hub := model.NewStreamHub()
	ln.registerHub("test-cam", hub)
	ln.registerHubAndStopReceiver("test-cam")

	ln.mu.RLock()
	_, ok := ln.hubs["test-cam"]
	ln.mu.RUnlock()

	require.False(t, ok)
}

func TestListenerAddr(t *testing.T) {
	t.Helper()

	ln := NewListener(config.SRTConfig{Port: 9004})
	addr := ln.addr()
	require.NotNil(t, addr)
	require.Contains(t, addr.String(), "9004")
}

func TestTSDemuxerStreamProcessing(t *testing.T) {
	t.Helper()

	demuxer := NewTSDemuxer()

	// Build a sequence of TS packets with multiple PES
	packets1 := buildTestTSPackets(t)
	nalus := demuxer.Feed(packets1)
	nalus = append(nalus, demuxer.Flush()...)

	require.NotEmpty(t, nalus, "should extract NALUs from TS packets")

	// Verify NALU types are valid
	for _, nalu := range nalus {
		require.NotEmpty(t, nalu.Data, "NALU data should not be empty")
		require.True(t, nalu.Type >= 1 && nalu.Type <= 12,
			"NALU type should be valid H.264 type, got %d", nalu.Type)
	}
}

func TestTSDemuxerMultiplePES(t *testing.T) {
	t.Helper()

	demuxer := NewTSDemuxer()

	// Build two sets of TS packets with different PTS
	var allPackets []byte
	packets1 := buildTestTSPackets(t)
	allPackets = append(allPackets, packets1...)
	// Feed again with same data (simulating another frame)
	allPackets = append(allPackets, packets1...)

	nalus := demuxer.Feed(allPackets)
	nalus = append(nalus, demuxer.Flush()...)
	require.NotEmpty(t, nalus)
}

func TestAssembleAccessUnitEmpty(t *testing.T) {
	t.Helper()

	frames := assembleAccessUnit(nil)
	require.Nil(t, frames)

	frames = assembleAccessUnit([]NALU{})
	require.Nil(t, frames)
}

func TestAssembleAccessUnitSingleFrame(t *testing.T) {
	t.Helper()

	nalus := []NALU{
		{PTS: 90000, Data: []byte{0x65, 0x01}, Type: 5}, // IDR
	}

	frames := assembleAccessUnit(nalus)
	require.Len(t, frames, 1)
	require.Len(t, frames[0], 1)
}

func TestParseStreamIDEdgeCases(t *testing.T) {
	t.Helper()

	// Leading/trailing slashes
	require.Equal(t, "cam", ParseStreamID("/cam/"))
	require.Equal(t, "cam", ParseStreamID("///cam///"))

	// Multiple equals signs
	require.Equal(t, "cam=1", ParseStreamID("camera_id=cam=1"))

	// Publish prefix with path
	require.Equal(t, "front-door", ParseStreamID("publish:/stream/front-door"))
}

func TestReceiverMetrics(t *testing.T) {
	t.Helper()

	hub := model.NewStreamHub()
	rec := NewReceiver(config.SRTStream{CameraID: "metrics-test", Mode: "listener"}, hub)

	require.Equal(t, int64(0), rec.frameCount.Load())
	require.Equal(t, int64(0), rec.getDropCount())
	require.False(t, rec.Running())
}

func TestConnectCallback(t *testing.T) {
	t.Helper()

	ln := NewListener(config.SRTConfig{Port: 9005})

	var connectCalled atomic.Bool
	ln.OnConnect = func(cameraID string, hub *model.StreamHub) {
		connectCalled.Store(true)
	}

	require.False(t, connectCalled.Load())
	// The callback is only called on actual connection, which requires a real SRT connection
	// This test verifies the callback is set
	require.NotNil(t, ln.OnConnect)
}

func TestListenerStopWithoutStart(t *testing.T) {
	t.Helper()

	ln := NewListener(config.SRTConfig{Port: 9006})
	err := ln.Stop()
	require.NoError(t, err)
}

func TestListenerDoubleStop(t *testing.T) {
	t.Helper()

	ln := NewListener(config.SRTConfig{Port: 9007})
	err := ln.Stop()
	require.NoError(t, err)
	err = ln.Stop()
	require.NoError(t, err)
}

func TestDisconnectCleanup(t *testing.T) {
	t.Helper()

	hub := model.NewStreamHub()
	stream := config.SRTStream{
		CameraID: "cleanup-test",
		Mode:     "listener",
	}

	// Subscribe a consumer
	var frameCount atomic.Int32
	err := hub.Subscribe("test-consumer", func(pts int64, au [][]byte) {
		frameCount.Add(1)
	})
	require.NoError(t, err)

	// Broadcast while active
	hub.Broadcast(90000, [][]byte{{0x65}}, false)
	require.Eventually(t, func() bool {
		return frameCount.Load() == 1
	}, 2*time.Second, 10*time.Millisecond)

	// Create and stop receiver
	rec := NewReceiver(stream, hub)
	rec.Stop() // Should not panic

	hub.Unsubscribe("test-consumer")
}
// mockSRTConn implements srt.Conn for testing purposes.
type mockSRTConn struct{}

func (m *mockSRTConn) Read(p []byte) (int, error)   { return 0, io.EOF }
func (m *mockSRTConn) Write(p []byte) (int, error)  { return len(p), nil }
func (m *mockSRTConn) Close() error                  { return nil }
func (m *mockSRTConn) ReadPacket() (packet.Packet, error) { return nil, io.EOF }
func (m *mockSRTConn) WritePacket(p packet.Packet) error  { return nil }
func (m *mockSRTConn) LocalAddr() net.Addr          { return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 1234} }
func (m *mockSRTConn) RemoteAddr() net.Addr         { return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 5678} }
func (m *mockSRTConn) SetDeadline(t time.Time) error    { return nil }
func (m *mockSRTConn) SetReadDeadline(t time.Time) error { return nil }
func (m *mockSRTConn) SetWriteDeadline(t time.Time) error { return nil }
func (m *mockSRTConn) SocketId() uint32             { return 0 }
func (m *mockSRTConn) PeerSocketId() uint32         { return 0 }
func (m *mockSRTConn) SetTTL(ttl uint32)             {}
func (m *mockSRTConn) SetLatency(latency time.Duration) error { return nil }
func (m *mockSRTConn) Stats(s *gosrt.Statistics)     {}
func (m *mockSRTConn) StreamId() string             { return "" }
func (m *mockSRTConn) Version() uint32              { return 4 }

// TestReceiverStartStopRace verifies there is no data race when Start() and Stop()
// are called concurrently. Before the fix, Start() closed r.done and then immediately
// rebuilt it (close-then-rebuild), creating a race with Stop() which also closes r.done.
func TestReceiverStartStopRace(t *testing.T) {
	t.Helper()

	for i := 0; i < 100; i++ {
		mock := &mockSRTConn{}
		hub := model.NewStreamHub()
		rec := NewReceiver(config.SRTStream{
			CameraID: fmt.Sprintf("race-test-%d", i),
			Mode:     "listener",
		}, hub)

		ready := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)

		go func() {
			defer wg.Done()
			<-ready
			rec.Start(mock)
		}()

		go func() {
			defer wg.Done()
			<-ready
			rec.Stop()
		}()

		close(ready) // release both goroutines simultaneously
		wg.Wait()
	}
}

