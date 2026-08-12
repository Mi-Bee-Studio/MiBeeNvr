package gb28181

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model/nalutil"
	"github.com/pion/rtp"
)

var logger = slog.Default().With("component", "gb28181-rtp")

// TCPMode specifies the framing for TCP transport.
type TCPMode string

const (
	TCPModeAuto    TCPMode = "auto"    // Auto-detect from first bytes
	TCPModeRFC4571 TCPMode = "rfc4571" // 2-byte length prefix (RFC 4571)
	TCPMode0x24    TCPMode = "0x24"    // 0x24 + 2-byte length (GB28181 standard)
)

// Receiver manages GB/T 28181 RTP media reception.
// It implements Stage 1 of the two-stage pipeline: RTP packet reassembly
// into complete MPEG-PS access units. Stage 2 is PSDemuxer (psdemux.go).
//
// UDP mode: Binds to a UDP port from PortManager.
// TCP passive mode: Accepts TCP connections with RFC 4571 or 0x24 framing.
type Receiver struct {
	cameraID    string
	hub         *model.StreamHub
	portManager *PortManager
	tcpMode     TCPMode

	// Network connection
	conn    net.Conn // UDP: *net.UDPConn, TCP: *net.TCPConn
	isTCP   atomic.Bool
	done    chan struct{}
	running atomic.Bool

	// Stage 1: RTP reassembly
	jitterBuffer     map[uint16]*rtp.Packet // keyed by RTP sequence number
	jitterBufferMu   sync.Mutex
	lastSeq          uint16
	baseSeq          uint16
	baseSeqSet       bool
	maxJitterPackets int // default: 32

	// Stage 2: PS demux
	demuxer *PSDemuxer

	// Callbacks
	// NALUCallback is invoked for each NALU extracted from PS demux.
	// Used for recording (recorder.WriteNALU). Non-blocking.
	NALUCallback func(nalu []byte, ptsTicks int64, isIDR bool)

	// Metrics
	rtpPacketsReceived atomic.Int64
	rtpPacketsDropped  atomic.Int64
	auEmitted          atomic.Int64
	ptsClock           int64 // 90kHz clock accumulator
}

// NewReceiver creates a new GB28181 RTP receiver.
func NewReceiver(cameraID string, hub *model.StreamHub, portManager *PortManager) *Receiver {
	return &Receiver{
		cameraID:         cameraID,
		hub:              hub,
		portManager:      portManager,
		tcpMode:          TCPModeAuto,
		done:             make(chan struct{}),
		jitterBuffer:     make(map[uint16]*rtp.Packet),
		maxJitterPackets: 32,
		demuxer:          NewPSDemuxer(),
	}
}

// SetTCPMode sets the TCP framing mode for TCP-passive transport.
// Ignored in UDP mode.
func (r *Receiver) SetTCPMode(mode TCPMode) {
	r.tcpMode = mode
}

// Start begins receiving RTP packets.
// UDP mode: Binds to an available UDP port from PortManager.
// TCP mode: Accepts an incoming TCP connection (conn must be non-nil).
func (r *Receiver) Start(ctx context.Context, conn net.Conn) error {
	if r.running.Load() {
		return fmt.Errorf("gb28181: receiver for camera %q already running", r.cameraID)
	}

	if conn == nil {
		return fmt.Errorf("gb28181: connection is nil for camera %q", r.cameraID)
	}

	r.conn = conn

	// Detect TCP mode
	if _, ok := conn.(*net.TCPConn); ok {
		r.isTCP.Store(true)
		logger.Info("gb28181: receiver started in TCP-passive mode",
			"camera_id", r.cameraID, "tcp_mode", r.tcpMode, "remote", conn.RemoteAddr())
	} else {
		r.isTCP.Store(false)
		logger.Info("gb28181: receiver started in UDP mode",
			"camera_id", r.cameraID, "local", conn.LocalAddr(), "remote", conn.RemoteAddr())
	}

	r.running.Store(true)
	go r.readLoop(ctx)
	return nil
}

// Stop terminates the receiver and recycles the UDP port.
func (r *Receiver) Stop() error {
	if !r.running.Swap(false) {
		return nil
	}

	if r.conn != nil {
		_ = r.conn.Close()
	}

	if r.done != nil {
		close(r.done)
	}

	logger.Info("gb28181: receiver stopped",
		"camera_id", r.cameraID,
		"packets_received", r.rtpPacketsReceived.Load(),
		"packets_dropped", r.rtpPacketsDropped.Load(),
		"au_emitted", r.auEmitted.Load())
	return nil
}

// Running returns whether the receiver is active.
func (r *Receiver) Running() bool {
	return r.running.Load()
}

// readLoop reads RTP packets from the network, reassembles access units,
// and feeds them to the PS demuxer.
func (r *Receiver) readLoop(ctx context.Context) {
	defer func() {
		r.running.Store(false)
		logger.Info("gb28181: read loop exited", "camera_id", r.cameraID)
	}()

	buf := make([]byte, 2048) // MTU-safe buffer

	for r.running.Load() {
		var n int
		var err error

		if r.isTCP.Load() {
			n, err = r.readTCP(buf)
		} else {
			n, err = r.conn.Read(buf)
		}

		if err != nil {
			if !r.running.Load() {
				return
			}
			if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
				logger.Info("gb28181: connection closed", "camera_id", r.cameraID)
				return
			}
			logger.Warn("gb28181: read error", "camera_id", r.cameraID, "error", err)
			return
		}

		if n == 0 {
			continue
		}

		r.rtpPacketsReceived.Add(1)

		// Parse RTP packet
		var pkt rtp.Packet
		if err := pkt.Unmarshal(buf[:n]); err != nil {
			logger.Warn("gb28181: RTP unmarshal failed", "camera_id", r.cameraID, "error", err)
			r.rtpPacketsDropped.Add(1)
			continue
		}

		// Feed to jitter buffer for reassembly
		if err := r.feedJitterBuffer(&pkt); err != nil {
			logger.Debug("gb28181: jitter buffer error", "camera_id", r.cameraID, "error", err)
			r.rtpPacketsDropped.Add(1)
			continue
		}
	}

	// Flush any remaining buffered packets
	r.flushJitterBuffer()
}

// readTCP reads an RTP packet from TCP with framing detection.
// Supports RFC 4571 (2-byte length) and 0x24 framing.
func (r *Receiver) readTCP(buf []byte) (int, error) {
	// Read framing header
	if r.tcpMode == TCPModeAuto {
		// Read first byte to auto-detect framing
		var first [1]byte
		if _, err := io.ReadFull(r.conn, first[:]); err != nil {
			return 0, err
		}

		if first[0] == 0x24 {
			r.tcpMode = TCPMode0x24
			buf[0] = first[0]
			// Read rest of 0x24 framing (reserved + 2-byte length + reserved)
			if _, err := io.ReadFull(r.conn, buf[1:5]); err != nil {
				return 0, err
			}
		} else {
			r.tcpMode = TCPModeRFC4571
			buf[0] = first[0]
			// Read second byte of the 2-byte length
			if _, err := io.ReadFull(r.conn, buf[1:2]); err != nil {
				return 0, err
			}
		}
	} else {
		// Read framing based on known mode
		if r.tcpMode == TCPMode0x24 {
			if _, err := io.ReadFull(r.conn, buf[:5]); err != nil {
				return 0, err
			}
		} else {
			if _, err := io.ReadFull(r.conn, buf[:2]); err != nil {
				return 0, err
			}
		}
	}

	// Parse length
	var length uint16
	if r.tcpMode == TCPMode0x24 {
		length = binary.BigEndian.Uint16(buf[2:4])
	} else {
		length = binary.BigEndian.Uint16(buf[:2])
	}

	if length == 0 || int(length) > len(buf) {
		return 0, fmt.Errorf("gb28181: invalid length %d", length)
	}

	// Read RTP payload
	n, err := io.ReadFull(r.conn, buf[:length])
	if err != nil {
		return 0, err
	}

	return n, nil
}

// feedJitterBuffer adds an RTP packet to the jitter buffer and emits
// complete access units when the marker bit is set.
func (r *Receiver) feedJitterBuffer(pkt *rtp.Packet) error {
	r.jitterBufferMu.Lock()
	defer r.jitterBufferMu.Unlock()

	seq := pkt.Header.SequenceNumber

	// Initialize base sequence number on first packet
	if !r.baseSeqSet {
		r.baseSeq = seq
		r.baseSeqSet = true
		r.lastSeq = seq
	}

	// Detect wrap-around (seq < lastSeq and gap > 1000)
	seqDelta := int16(seq - r.lastSeq)
	if seqDelta < -1000 {
		// Wrap-around detected
		r.baseSeq = seq
	}

	// Buffer size check
	if len(r.jitterBuffer) >= r.maxJitterPackets {
		// Force flush to make room
		r.emitAccessUnitsLocked()
	}

	// Store packet
	r.jitterBuffer[seq] = pkt
	r.lastSeq = seq

	// Marker bit indicates AU boundary - emit complete AU
	if pkt.Header.Marker {
		r.emitAccessUnitsLocked()
	}

	return nil
}

// emitAccessUnitsLocked reassembles packets in sequence order and emits
// complete access units. Must be called with jitterBufferMu held.
func (r *Receiver) emitAccessUnitsLocked() {
	if len(r.jitterBuffer) == 0 {
		return
	}

	// Build ordered list of packets by sequence number
	// Start from oldest received (lowest seq number)
	var packets []*rtp.Packet
	for len(packets) < len(r.jitterBuffer) {
		targetSeq := r.baseSeq + uint16(len(packets))
		pkt, ok := r.jitterBuffer[targetSeq]
		if !ok {
			break
		}
		packets = append(packets, pkt)
		delete(r.jitterBuffer, targetSeq)
	}

	if len(packets) == 0 {
		return
	}

	// Advance base sequence to account for emitted packets
	r.baseSeq += uint16(len(packets))

	// Stitch payloads across packets (cross-packet byte stitching)
	var auPayload []byte
	for _, pkt := range packets {
		auPayload = append(auPayload, pkt.Payload...)
	}

	if len(auPayload) == 0 {
		return
	}

	// Convert RTP timestamp to 90kHz PTS
	// RTP timestamp is 90kHz clock, same as NVR PTS
	lastPkt := packets[len(packets)-1]
	ptsTicks := int64(lastPkt.Header.Timestamp)

	// Feed to Stage 2: PS demuxer
	nalus, err := r.demuxer.FeedAU(auPayload, ptsTicks)
	if err != nil {
		logger.Debug("gb28181: PS demux error", "camera_id", r.cameraID, "error", err)
		return
	}

	// If no NALUs, this was a non-video PS packet (e.g., system header)
	if len(nalus) == 0 {
		return
	}

	r.auEmitted.Add(1)

	// Detect IDR frame
	isH265 := r.demuxer.Codec() == "h265"
	isIDR := nalutil.IsIDR(nalus, isH265)

	// Broadcast to StreamHub (non-blocking)
	r.hub.Broadcast(ptsTicks, nalus, isIDR)

	// Invoke NALU callback for recording
	if r.NALUCallback != nil {
		for _, nalu := range nalus {
			r.NALUCallback(nalu, ptsTicks, isIDR)
		}
	}
}

// flushJitterBuffer emits any remaining packets in the jitter buffer.
func (r *Receiver) flushJitterBuffer() {
	r.jitterBufferMu.Lock()
	defer r.jitterBufferMu.Unlock()

	// Emit any remaining packets as one AU
	if len(r.jitterBuffer) > 0 {
		r.emitAccessUnitsLocked()
	}

	// Flush demuxer residual data
	nalus := r.demuxer.Flush()
	if len(nalus) > 0 {
		isH265 := r.demuxer.Codec() == "h265"
		isIDR := nalutil.IsIDR(nalus, isH265)
		r.hub.Broadcast(r.ptsClock, nalus, isIDR)

		if r.NALUCallback != nil {
			for _, nalu := range nalus {
				r.NALUCallback(nalu, r.ptsClock, isIDR)
			}
		}
	}
}

// Metrics returns receiver metrics.
func (r *Receiver) Metrics() map[string]int64 {
	return map[string]int64{
		"packets_received": r.rtpPacketsReceived.Load(),
		"packets_dropped":  r.rtpPacketsDropped.Load(),
		"au_emitted":       r.auEmitted.Load(),
	}
}

// Codec returns the detected codec type from the PS demuxer.
func (r *Receiver) Codec() string {
	return r.demuxer.Codec()
}
