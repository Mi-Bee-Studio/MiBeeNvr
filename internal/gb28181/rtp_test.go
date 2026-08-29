package gb28181

import (
	"bytes"
	"context"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/streamhub"
	"github.com/mickeyzzc/gb28181-go/platform"
	"github.com/pion/rtp"
	"github.com/stretchr/testify/require"
)

// Helper to build a test RTP packet with marker bit set.
func buildTestRTPPacket(seq uint16, timestamp uint32, payload []byte, marker bool) []byte {
	pkt := &rtp.Packet{
		Header: rtp.Header{
			SequenceNumber: seq,
			Timestamp:      timestamp,
			Marker:         marker,
		},
		Payload: payload,
	}
	buf, err := pkt.Marshal()
	if err != nil {
		panic(err)
	}
	return buf
}

// Helper to build a minimal MPEG-PS packet.
func buildTestMPEGPSPayload() []byte {
	// Minimal H.264 IDR NALU (NAL unit type 5, slice header).
	// buildPS emits Pack Header (0xBA) + System Header (0xBB) +
	// PSM (0xBC, stream_type 0x1B) + Video PES (0xE0) with the NALU
	// in Annex-B format, so the PS demuxer can detect the codec and
	// extract the NALU.
	idrNalu := []byte{0x65, 0x88, 0x84, 0x00, 0x40, 0xFF, 0xFE, 0xF8, 0x80, 0x80}
	return buildPS([][]byte{idrNalu}, 0x1B) // PS stream type: AVC (lib constant unexported)
}

func TestNewReceiver(t *testing.T) {
	t.Helper()

	hub := streamhub.New()
	pm := platform.NewPortManager(50000, 50010)
	rec := NewReceiver("test-cam", hub, pm)

	require.Equal(t, "test-cam", rec.cameraID)
	require.NotNil(t, rec.hub)
	require.NotNil(t, rec.portManager)
	require.Equal(t, TCPModeAuto, rec.tcpMode)
	require.False(t, rec.Running())
}

func TestReceiverStopWithoutStart(t *testing.T) {
	t.Helper()

	hub := streamhub.New()
	pm := platform.NewPortManager(50000, 50010)
	rec := NewReceiver("test", hub, pm)

	err := rec.Stop()
	require.NoError(t, err)
}

func TestSetTCPMode(t *testing.T) {
	t.Helper()

	hub := streamhub.New()
	pm := platform.NewPortManager(50000, 50010)
	rec := NewReceiver("test", hub, pm)

	rec.SetTCPMode(TCPModeRFC4571)
	require.Equal(t, TCPModeRFC4571, rec.tcpMode)

	rec.SetTCPMode(TCPMode0x24)
	require.Equal(t, TCPMode0x24, rec.tcpMode)

	rec.SetTCPMode(TCPModeAuto)
	require.Equal(t, TCPModeAuto, rec.tcpMode)
}

func TestReceiverUDPBasic(t *testing.T) {
	t.Helper()

	hub := streamhub.New()
	pm := platform.NewPortManager(50000, 50010)
	rec := NewReceiver("test-cam", hub, pm)

	// Create UDP socket pair
	serverAddr, err := net.ResolveUDPAddr("udp", "127.0.0.1:0")
	require.NoError(t, err)

	serverConn, err := net.ListenUDP("udp", serverAddr)
	require.NoError(t, err)
	defer serverConn.Close()

	clientConn, err := net.DialUDP("udp", nil, serverConn.LocalAddr().(*net.UDPAddr))
	require.NoError(t, err)
	defer clientConn.Close()

	// Subscribe to hub
	var receivedFrames atomic.Int64
	var receivedPTS atomic.Int64
	err = hub.Subscribe("test-consumer", func(pts int64, au [][]byte) {
		receivedFrames.Add(1)
		receivedPTS.Store(pts)
	})
	require.NoError(t, err)
	defer hub.Unsubscribe("test-consumer")

	// Start receiver
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err = rec.Start(ctx, serverConn)
	require.NoError(t, err)
	defer rec.Stop()

	// Send test RTP packets
	payload := buildTestMPEGPSPayload()
	pkt1 := buildTestRTPPacket(0, 90000, payload, false)
	pkt2 := buildTestRTPPacket(1, 90360, payload, true) // Marker bit set = AU boundary

	_, err = clientConn.Write(pkt1)
	require.NoError(t, err)

	_, err = clientConn.Write(pkt2)
	require.NoError(t, err)

	// Wait for frame to be received and broadcast
	require.Eventually(t, func() bool {
		return receivedFrames.Load() > 0
	}, 2*time.Second, 10*time.Millisecond, "consumer should receive frame")

	// The first burst carries no marker, so both bursts drain as one run on
	// the marker packet and splitAUsByFrame recovers one AU per frame (the
	// documented lost-marker recovery). Both frames share the marker packet's
	// timestamp — the drain ends there.
	require.Equal(t, int64(2), receivedFrames.Load())
	require.True(t, receivedPTS.Load() > 0)
}

func TestReceiverMetrics(t *testing.T) {
	t.Helper()

	hub := streamhub.New()
	pm := platform.NewPortManager(50000, 50010)
	rec := NewReceiver("metrics-test", hub, pm)

	metrics := rec.Metrics()
	require.Equal(t, int64(0), metrics["packets_received"])
	require.Equal(t, int64(0), metrics["packets_dropped"])
	require.Equal(t, int64(0), metrics["au_emitted"])
}

func TestReceiverCodec(t *testing.T) {
	t.Helper()

	hub := streamhub.New()
	pm := platform.NewPortManager(50000, 50010)
	rec := NewReceiver("test", hub, pm)

	// Initially unknown
	require.Equal(t, "", rec.Codec())
}

func TestReceiverDoubleStop(t *testing.T) {
	t.Helper()

	hub := streamhub.New()
	pm := platform.NewPortManager(50000, 50010)
	rec := NewReceiver("test", hub, pm)

	err := rec.Stop()
	require.NoError(t, err)

	err = rec.Stop()
	require.NoError(t, err)
}

func TestReceiverStartStopRace(t *testing.T) {
	t.Helper()

	for range 50 {
		hub := streamhub.New()
		pm := platform.NewPortManager(50000, 50100)
		rec := NewReceiver("race-test", hub, pm)

		serverAddr, err := net.ResolveUDPAddr("udp", "127.0.0.1:0")
		require.NoError(t, err)

		serverConn, err := net.ListenUDP("udp", serverAddr)
		require.NoError(t, err)

		ctx, cancel := context.WithCancel(context.Background())

		ready := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)

		go func() {
			defer wg.Done()
			<-ready
			_ = rec.Start(ctx, serverConn)
		}()

		go func() {
			defer wg.Done()
			<-ready
			_ = rec.Stop()
		}()

		close(ready)
		time.Sleep(10 * time.Millisecond)
		cancel()
		wg.Wait()
		_ = serverConn.Close()
	}
}

func TestReceiverJitterBufferMarkerBit(t *testing.T) {
	t.Helper()

	hub := streamhub.New()
	pm := platform.NewPortManager(50000, 50010)
	rec := NewReceiver("jitter-test", hub, pm)

	serverAddr, err := net.ResolveUDPAddr("udp", "127.0.0.1:0")
	require.NoError(t, err)

	serverConn, err := net.ListenUDP("udp", serverAddr)
	require.NoError(t, err)
	defer serverConn.Close()

	clientConn, err := net.DialUDP("udp", nil, serverConn.LocalAddr().(*net.UDPAddr))
	require.NoError(t, err)
	defer clientConn.Close()

	var auCount atomic.Int64
	err = hub.Subscribe("test-consumer", func(pts int64, au [][]byte) {
		auCount.Add(1)
	})
	require.NoError(t, err)
	defer hub.Unsubscribe("test-consumer")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err = rec.Start(ctx, serverConn)
	require.NoError(t, err)
	defer rec.Stop()

	// Send multiple packets with marker bit on last one
	payload := buildTestMPEGPSPayload()
	pkt1 := buildTestRTPPacket(0, 90000, payload, false)
	pkt2 := buildTestRTPPacket(1, 90360, payload, false)
	pkt3 := buildTestRTPPacket(2, 90720, payload, true) // Marker = AU boundary

	_, err = clientConn.Write(pkt1)
	require.NoError(t, err)

	_, err = clientConn.Write(pkt2)
	require.NoError(t, err)

	_, err = clientConn.Write(pkt3)
	require.NoError(t, err)

	// Wait for AU to be emitted
	require.Eventually(t, func() bool {
		return auCount.Load() > 0
	}, 2*time.Second, 10*time.Millisecond)
}

func TestReceiverSequenceWrap(t *testing.T) {
	t.Helper()

	hub := streamhub.New()
	pm := platform.NewPortManager(50000, 50010)
	rec := NewReceiver("wrap-test", hub, pm)

	serverAddr, err := net.ResolveUDPAddr("udp", "127.0.0.1:0")
	require.NoError(t, err)

	serverConn, err := net.ListenUDP("udp", serverAddr)
	require.NoError(t, err)
	defer serverConn.Close()

	clientConn, err := net.DialUDP("udp", nil, serverConn.LocalAddr().(*net.UDPAddr))
	require.NoError(t, err)
	defer clientConn.Close()

	var auCount atomic.Int64
	err = hub.Subscribe("test-consumer", func(pts int64, au [][]byte) {
		auCount.Add(1)
	})
	require.NoError(t, err)
	defer hub.Unsubscribe("test-consumer")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err = rec.Start(ctx, serverConn)
	require.NoError(t, err)
	defer rec.Stop()

	payload := buildTestMPEGPSPayload()

	// Send packets near sequence wrap (65535 -> 0)
	pkt1 := buildTestRTPPacket(65534, 90000, payload, false)
	pkt2 := buildTestRTPPacket(65535, 90360, payload, true) // Marker on last packet

	_, err = clientConn.Write(pkt1)
	require.NoError(t, err)

	_, err = clientConn.Write(pkt2)
	require.NoError(t, err)

	// Send post-wrap packet
	pkt3 := buildTestRTPPacket(0, 91080, payload, true)

	_, err = clientConn.Write(pkt3)
	require.NoError(t, err)

	// Wait for AUs to be emitted
	require.Eventually(t, func() bool {
		return auCount.Load() >= 1
	}, 2*time.Second, 10*time.Millisecond)
}

func TestReceiverTCPModeRFC4571(t *testing.T) {
	t.Helper()

	hub := streamhub.New()
	pm := platform.NewPortManager(50000, 50010)
	rec := NewReceiver("tcp-test", hub, pm)
	rec.SetTCPMode(TCPModeRFC4571)

	// Create TCP socket pair
	serverLn, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer serverLn.Close()

	var serverConn net.Conn
	var connErr error
	connReady := make(chan struct{})

	go func() {
		serverConn, connErr = serverLn.Accept()
		close(connReady)
	}()

	clientConn, err := net.Dial("tcp", serverLn.Addr().String())
	require.NoError(t, err)
	defer clientConn.Close()

	<-connReady
	require.NoError(t, connErr)

	var auCount atomic.Int64
	err = hub.Subscribe("test-consumer", func(pts int64, au [][]byte) {
		auCount.Add(1)
	})
	require.NoError(t, err)
	defer hub.Unsubscribe("test-consumer")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err = rec.Start(ctx, serverConn)
	require.NoError(t, err)
	defer rec.Stop()

	// Send RFC 4571 framed packet: 2-byte length + RTP payload
	payload := buildTestMPEGPSPayload()
	rtpPkt := buildTestRTPPacket(0, 90000, payload, true)

	buf := make([]byte, 2+len(rtpPkt))
	buf[0] = byte(len(rtpPkt) >> 8)
	buf[1] = byte(len(rtpPkt))
	copy(buf[2:], rtpPkt)

	_, err = clientConn.Write(buf)
	require.NoError(t, err)

	// Wait for AU to be emitted
	require.Eventually(t, func() bool {
		return auCount.Load() > 0
	}, 2*time.Second, 10*time.Millisecond)
}

func TestReceiverTCPMode0x24(t *testing.T) {
	t.Helper()

	hub := streamhub.New()
	pm := platform.NewPortManager(50000, 50010)
	rec := NewReceiver("tcp-0x24-test", hub, pm)
	rec.SetTCPMode(TCPMode0x24)

	// Create TCP socket pair
	serverLn, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer serverLn.Close()

	var serverConn net.Conn
	var connErr error
	connReady := make(chan struct{})

	go func() {
		serverConn, connErr = serverLn.Accept()
		close(connReady)
	}()

	clientConn, err := net.Dial("tcp", serverLn.Addr().String())
	require.NoError(t, err)
	defer clientConn.Close()

	<-connReady
	require.NoError(t, connErr)

	var auCount atomic.Int64
	err = hub.Subscribe("test-consumer", func(pts int64, au [][]byte) {
		auCount.Add(1)
	})
	require.NoError(t, err)
	defer hub.Unsubscribe("test-consumer")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err = rec.Start(ctx, serverConn)
	require.NoError(t, err)
	defer rec.Stop()

	// Send 0x24 framed packet: 0x24 + 2-byte length + RTP payload
	payload := buildTestMPEGPSPayload()
	rtpPkt := buildTestRTPPacket(0, 90000, payload, true)

	buf := make([]byte, 4+len(rtpPkt))
	buf[0] = 0x24
	buf[1] = 0x00 // Channel
	buf[2] = byte(len(rtpPkt) >> 8)
	buf[3] = byte(len(rtpPkt))
	copy(buf[4:], rtpPkt)

	_, err = clientConn.Write(buf)
	require.NoError(t, err)

	// Wait for AU to be emitted
	require.Eventually(t, func() bool {
		return auCount.Load() > 0
	}, 2*time.Second, 10*time.Millisecond)
}

func TestReceiverTCPModeAutoDetectRFC4571(t *testing.T) {
	t.Helper()

	hub := streamhub.New()
	pm := platform.NewPortManager(50000, 50010)
	rec := NewReceiver("auto-detect-test", hub, pm)
	rec.SetTCPMode(TCPModeAuto)

	// Create TCP socket pair
	serverLn, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer serverLn.Close()

	var serverConn net.Conn
	var connErr error
	connReady := make(chan struct{})

	go func() {
		serverConn, connErr = serverLn.Accept()
		close(connReady)
	}()

	clientConn, err := net.Dial("tcp", serverLn.Addr().String())
	require.NoError(t, err)
	defer clientConn.Close()

	<-connReady
	require.NoError(t, connErr)

	var auCount atomic.Int64
	err = hub.Subscribe("test-consumer", func(pts int64, au [][]byte) {
		auCount.Add(1)
	})
	require.NoError(t, err)
	defer hub.Unsubscribe("test-consumer")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err = rec.Start(ctx, serverConn)
	require.NoError(t, err)
	defer rec.Stop()

	// Send RFC 4571 framed packet (should be auto-detected)
	payload := buildTestMPEGPSPayload()
	rtpPkt := buildTestRTPPacket(0, 90000, payload, true)

	buf := make([]byte, 2+len(rtpPkt))
	buf[0] = byte(len(rtpPkt) >> 8)
	buf[1] = byte(len(rtpPkt))
	copy(buf[2:], rtpPkt)

	_, err = clientConn.Write(buf)
	require.NoError(t, err)

	// Wait for AU to be emitted
	require.Eventually(t, func() bool {
		return auCount.Load() > 0
	}, 2*time.Second, 10*time.Millisecond)

	// Verify mode was detected
	require.Equal(t, TCPModeRFC4571, rec.tcpMode)
}

func TestReceiverTCPModeAutoDetect0x24(t *testing.T) {
	t.Helper()

	hub := streamhub.New()
	pm := platform.NewPortManager(50000, 50010)
	rec := NewReceiver("auto-detect-0x24-test", hub, pm)
	rec.SetTCPMode(TCPModeAuto)

	// Create TCP socket pair
	serverLn, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer serverLn.Close()

	var serverConn net.Conn
	var connErr error
	connReady := make(chan struct{})

	go func() {
		serverConn, connErr = serverLn.Accept()
		close(connReady)
	}()

	clientConn, err := net.Dial("tcp", serverLn.Addr().String())
	require.NoError(t, err)
	defer clientConn.Close()

	<-connReady
	require.NoError(t, connErr)

	var auCount atomic.Int64
	err = hub.Subscribe("test-consumer", func(pts int64, au [][]byte) {
		auCount.Add(1)
	})
	require.NoError(t, err)
	defer hub.Unsubscribe("test-consumer")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err = rec.Start(ctx, serverConn)
	require.NoError(t, err)
	defer rec.Stop()

	// Send 0x24 framed packet (should be auto-detected)
	payload := buildTestMPEGPSPayload()
	rtpPkt := buildTestRTPPacket(0, 90000, payload, true)

	buf := make([]byte, 4+len(rtpPkt))
	buf[0] = 0x24
	buf[1] = 0x00
	buf[2] = byte(len(rtpPkt) >> 8)
	buf[3] = byte(len(rtpPkt))
	copy(buf[4:], rtpPkt)

	_, err = clientConn.Write(buf)
	require.NoError(t, err)

	// Wait for AU to be emitted
	require.Eventually(t, func() bool {
		return auCount.Load() > 0
	}, 2*time.Second, 10*time.Millisecond)

	// Verify mode was detected
	require.Equal(t, TCPMode0x24, rec.tcpMode)
}

func TestReceiverNALUCallback(t *testing.T) {
	t.Helper()

	hub := streamhub.New()
	pm := platform.NewPortManager(50000, 50010)
	rec := NewReceiver("callback-test", hub, pm)

	// Set NALU callback
	var callbackCount atomic.Int64
	var callbackPTS atomic.Int64
	var callbackIsIDR atomic.Bool

	rec.NALUCallback = func(nalu []byte, ptsTicks int64, isIDR bool) {
		callbackCount.Add(1)
		callbackPTS.Store(ptsTicks)
		callbackIsIDR.Store(isIDR)
	}

	serverAddr, err := net.ResolveUDPAddr("udp", "127.0.0.1:0")
	require.NoError(t, err)

	serverConn, err := net.ListenUDP("udp", serverAddr)
	require.NoError(t, err)
	defer serverConn.Close()

	clientConn, err := net.DialUDP("udp", nil, serverConn.LocalAddr().(*net.UDPAddr))
	require.NoError(t, err)
	defer clientConn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err = rec.Start(ctx, serverConn)
	require.NoError(t, err)
	defer rec.Stop()

	payload := buildTestMPEGPSPayload()
	pkt := buildTestRTPPacket(0, 90000, payload, true)

	_, err = clientConn.Write(pkt)
	require.NoError(t, err)

	// Wait for callback
	require.Eventually(t, func() bool {
		return callbackCount.Load() > 0
	}, 2*time.Second, 10*time.Millisecond)

	require.Equal(t, int64(1), callbackCount.Load())
	require.Equal(t, int64(90000), callbackPTS.Load())
}

func TestReceiverStartNilConn(t *testing.T) {
	t.Helper()

	hub := streamhub.New()
	pm := platform.NewPortManager(50000, 50010)
	rec := NewReceiver("nil-conn-test", hub, pm)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := rec.Start(ctx, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "connection is nil")
}

func TestReceiverMultipleAUs(t *testing.T) {
	t.Helper()

	hub := streamhub.New()
	pm := platform.NewPortManager(50000, 50010)
	rec := NewReceiver("multi-au-test", hub, pm)

	serverAddr, err := net.ResolveUDPAddr("udp", "127.0.0.1:0")
	require.NoError(t, err)

	serverConn, err := net.ListenUDP("udp", serverAddr)
	require.NoError(t, err)
	defer serverConn.Close()

	clientConn, err := net.DialUDP("udp", nil, serverConn.LocalAddr().(*net.UDPAddr))
	require.NoError(t, err)
	defer clientConn.Close()

	var auCount atomic.Int64
	err = hub.Subscribe("test-consumer", func(pts int64, au [][]byte) {
		auCount.Add(1)
	})
	require.NoError(t, err)
	defer hub.Unsubscribe("test-consumer")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err = rec.Start(ctx, serverConn)
	require.NoError(t, err)
	defer rec.Stop()

	payload := buildTestMPEGPSPayload()

	// Send 3 complete AUs (each with marker bit)
	for i := range 3 {
		pkt := buildTestRTPPacket(uint16(i), uint32(90000+i*360), payload, true)
		_, err = clientConn.Write(pkt)
		require.NoError(t, err)
	}

	// Wait for 3 AUs to be emitted
	require.Eventually(t, func() bool {
		return auCount.Load() >= 3
	}, 2*time.Second, 10*time.Millisecond)
}

func TestReceiverOutOrderPackets(t *testing.T) {
	t.Helper()

	hub := streamhub.New()
	pm := platform.NewPortManager(50000, 50010)
	rec := NewReceiver("ooo-test", hub, pm)

	serverAddr, err := net.ResolveUDPAddr("udp", "127.0.0.1:0")
	require.NoError(t, err)

	serverConn, err := net.ListenUDP("udp", serverAddr)
	require.NoError(t, err)
	defer serverConn.Close()

	clientConn, err := net.DialUDP("udp", nil, serverConn.LocalAddr().(*net.UDPAddr))
	require.NoError(t, err)
	defer clientConn.Close()

	var auCount atomic.Int64
	err = hub.Subscribe("test-consumer", func(pts int64, au [][]byte) {
		auCount.Add(1)
	})
	require.NoError(t, err)
	defer hub.Unsubscribe("test-consumer")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err = rec.Start(ctx, serverConn)
	require.NoError(t, err)
	defer rec.Stop()

	payload := buildTestMPEGPSPayload()

	// Send packets out of order: 1, 0, 2
	pkt0 := buildTestRTPPacket(0, 90000, payload, false)
	pkt1 := buildTestRTPPacket(1, 90360, payload, false)
	pkt2 := buildTestRTPPacket(2, 90720, payload, true) // Marker

	// Send in wrong order
	_, err = clientConn.Write(pkt1)
	require.NoError(t, err)

	_, err = clientConn.Write(pkt0)
	require.NoError(t, err)

	_, err = clientConn.Write(pkt2)
	require.NoError(t, err)

	// Wait for AU to be emitted
	require.Eventually(t, func() bool {
		return auCount.Load() > 0
	}, 2*time.Second, 10*time.Millisecond)
}

func TestReceiverMaxJitterBufferSize(t *testing.T) {
	t.Helper()

	hub := streamhub.New()
	pm := platform.NewPortManager(50000, 50010)
	rec := NewReceiver("jitter-size-test", hub, pm)
	rec.maxJitterPackets = 4 // Reduce for testing

	serverAddr, err := net.ResolveUDPAddr("udp", "127.0.0.1:0")
	require.NoError(t, err)

	serverConn, err := net.ListenUDP("udp", serverAddr)
	require.NoError(t, err)
	defer serverConn.Close()

	clientConn, err := net.DialUDP("udp", nil, serverConn.LocalAddr().(*net.UDPAddr))
	require.NoError(t, err)
	defer clientConn.Close()

	var auCount atomic.Int64
	err = hub.Subscribe("test-consumer", func(pts int64, au [][]byte) {
		auCount.Add(1)
	})
	require.NoError(t, err)
	defer hub.Unsubscribe("test-consumer")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err = rec.Start(ctx, serverConn)
	require.NoError(t, err)
	defer rec.Stop()

	payload := buildTestMPEGPSPayload()

	// Send more packets than jitter buffer size
	for i := range 10 {
		marker := (i%3 == 2) // Every 3rd packet marks AU boundary
		pkt := buildTestRTPPacket(uint16(i), uint32(90000+i*360), payload, marker)
		_, err = clientConn.Write(pkt)
		require.NoError(t, err)
	}

	// Wait for some AUs to be emitted
	require.Eventually(t, func() bool {
		return auCount.Load() >= 1
	}, 2*time.Second, 10*time.Millisecond)
}

// buildPS constructs a minimal PS packet around the given NALUs (moved here
// from psdemux_test.go when the demuxer migrated to gb28181-go/platform).
func buildPS(nalus [][]byte, streamType byte) []byte {
	var ps bytes.Buffer

	// Pack header: 00 00 01 BA (4) + 10 bytes of fixed fields
	ps.Write([]byte{0x00, 0x00, 0x01, 0xBA})
	ps.Write([]byte{0x44, 0x00, 0x04, 0x00, 0x04, 0x01, 0x01, 0x89, 0xC3, 0xF8})

	// System header: 00 00 01 BB (4) + 2 bytes length + system header data
	ps.Write([]byte{0x00, 0x00, 0x01, 0xBB})
	systemHeader := []byte{
		0x00, 0x09, // length (9 bytes after the length field)
		0x01,                   // rate bound and audio/video bound
		0xB9, 0xE0, 0xE0, 0x80, // rate bound (high) + audio bound + fixed flag
		0xC0, 0x01, // stream 1 (audio)
		0x00, 0x01, // STD buffer scale and size
	}
	ps.Write(systemHeader)

	// Program Stream Map: 00 00 01 BC (4) + PSM data
	ps.Write([]byte{0x00, 0x00, 0x01, 0xBC})

	// PSM packet_length (2 bytes): body length = 10 bytes
	ps.Write([]byte{0x00, 0x0A})

	// PSM body per ISO/IEC 13818-1 program_stream_map:
	// version (1) + reserved (1) + PS_info_length (2) +
	// elementary_stream_map_length (2) + stream_info entry (4)
	ps.Write([]byte{
		0x02,       // version = 2
		0xC0,       // reserved byte
		0x00, 0x00, // PS_info_length = 0 (no PS_info)
		0x00, 0x04, // elementary_stream_map_length = 4 (one entry)
		// stream_info for video
		streamType, // stream_type (0x1B for H.264, 0x24 for H.265)
		0xE0,       // elementary_stream_id (video stream 0)
		0x00, 0x00, // elementary_stream_info_length (0)
	})

	// Video PES: 00 00 01 E0 (4) + PES header + NALU payload
	ps.Write([]byte{0x00, 0x00, 0x01, 0xE0})

	// Calculate total NALU payload length with start codes
	payloadLen := 0
	for _, nalu := range nalus {
		payloadLen += 4 // 4-byte start code
		payloadLen += len(nalu)
	}

	// PES packet length = flags (2) + header_data_length (1) + payload_len
	pesPacketLen := 3 + payloadLen

	// PES header, standard layout (ITU-T H.222.0): byte6='10'+flags,
	// byte7=PTS_DTS_flags (0 = no PTS), byte8=PES_header_data_length (0).
	pesHeader := []byte{
		byte(pesPacketLen >> 8), byte(pesPacketLen), // PES_packet_length (2 bytes)
		0x80, // Byte 6: '10' marker + zero flags
		0x00, // Byte 7: PTS_DTS_flags = '00' (no PTS in synthetic stream)
		0x00, // Byte 8: PES_header_data_length (0)
	}
	ps.Write(pesHeader)

	// Write NALUs with Annex-B start codes
	for _, nalu := range nalus {
		ps.Write([]byte{0x00, 0x00, 0x00, 0x01})
		ps.Write(nalu)
	}

	return ps.Bytes()
}
