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
	"time"

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

// rtpReadBufSize is the UDP read buffer size. UDP datagrams can be up to
// 65507 bytes after IP fragmentation reassembly — GB28181 devices commonly
// emit 1400–4KB PS packets, and Go's UDP Read silently truncates anything
// larger than the buffer, so a small "MTU-sized" buffer corrupts AUs.
const rtpReadBufSize = 65535

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
	maxJitterPackets int    // default: 32
	packetsDroppedU  uint64 // count of gap-skipped packets (diagnostics)

	// Stage 2: PS demux
	demuxer *PSDemuxer

	// OnFirstRTP fires once when the first RTP packet is received — used by
	// the session layer to confirm a dialog without a matched SIP response
	// (some device firmwares answer INVITE 200 OK without a Via header, which
	// no SIP stack can transaction-match).
	OnFirstRTP func()

	// Callbacks
	// AUCallback is invoked once per complete access unit (full NALU list).
	// Preferred by recorders: it preserves AU grouping for muxing and hub
	// fan-out. Non-blocking.
	AUCallback func(au [][]byte, ptsTicks int64, isIDR bool)

	// AudioCallback is invoked for each demuxed audio frame (G.711/AAC) from
	// the PS stream. Non-blocking.
	AudioCallback func(frame AudioFrame)

	// NALUCallback is invoked for each NALU extracted from PS demux.
	// Kept for per-NALU consumers; when both are set, AUCallback is used.
	NALUCallback func(nalu []byte, ptsTicks int64, isIDR bool)

	// Metrics
	rtpPacketsReceived atomic.Int64
	rtpPacketsDropped  atomic.Int64
	auEmitted          atomic.Int64
	ptsClock           atomic.Int64 // last emitted PTS (90kHz)
	lastIDRUnix        atomic.Int64 // last IDR AU time (unix nano)
	lastPktUnix        atomic.Int64 // last received RTP packet time (unix nano)

	// SSRC isolation: the first received packet's SSRC is latched as this
	// dialog's source; packets from any other SSRC are dropped. A recycled
	// UDP port can receive stale RTP from a previous dialog's sender (a
	// forwarder that never saw the BYE, or packets in flight during
	// teardown) — interleaving those into this session's byte stream
	// corrupts the PS demuxer and the downstream recorder.
	ssrcLatched   atomic.Bool
	expectedSSRC  atomic.Uint32
	foreignDrops  atomic.Int64
	foreignLogged atomic.Bool // one warning per session (not per packet)
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

// SetAudioCodecHint seeds the PS demuxer's no-PSM audio fallback with the
// codec declared in the device's INVITE answer SDP. No-op once the stream's
// PSM declares the audio codec itself.
func (r *Receiver) SetAudioCodecHint(codec string) {
	r.demuxer.SetAudioCodecHint(codec)
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

// Stop terminates the receiver and closes the connection. Port recycling is
// the SessionManager's job (it owns the port allocation).
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
		"foreign_ssrc_dropped", r.foreignDrops.Load(),
		"au_emitted", r.auEmitted.Load())
	return nil
}

// Running returns whether the receiver is active.
func (r *Receiver) Running() bool {
	return r.running.Load()
}

// HasReceivedRTP reports whether at least one RTP packet arrived.
func (r *Receiver) HasReceivedRTP() bool {
	return r.rtpPacketsReceived.Load() > 0
}

// SinceLastPacket returns how long ago the last RTP packet arrived.
// A session whose stream stops (device firmware stall) must be recycled or
// its zombie receiver blocks every future auto-INVITE (idempotency check).
func (r *Receiver) SinceLastPacket() time.Duration {
	ns := r.lastPktUnix.Load()
	if ns == 0 {
		return 0
	}
	return time.Since(time.Unix(0, ns))
}

// SinceLastIDR returns how long ago the last IDR access unit was emitted
// (never if ok=false). Devices that only emit a keyframe at stream start go
// stale for segment starts; the session layer watches this.
func (r *Receiver) SinceLastIDR() (time.Duration, bool) {
	ns := r.lastIDRUnix.Load()
	if ns == 0 {
		return 0, false
	}
	return time.Since(time.Unix(0, ns)), true
}

// readLoop reads RTP packets from the network, reassembles access units,
// and feeds them to the PS demuxer.
func (r *Receiver) readLoop(ctx context.Context) {
	defer func() {
		r.running.Store(false)
		logger.Info("gb28181: read loop exited", "camera_id", r.cameraID)
	}()

	buf := make([]byte, rtpReadBufSize)

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
		r.lastPktUnix.Store(time.Now().UnixNano())
		if r.rtpPacketsReceived.Load() == 1 && r.OnFirstRTP != nil {
			r.OnFirstRTP()
		}

		// Parse RTP packet. pion/rtp aliases buf — the payload slice points
		// into buf, which the next Read overwrites — so the payload is
		// cloned before the packet is stored in the jitter buffer.
		var pkt rtp.Packet
		if err := pkt.Unmarshal(buf[:n]); err != nil {
			logger.Warn("gb28181: RTP unmarshal failed", "camera_id", r.cameraID, "error", err)
			r.rtpPacketsDropped.Add(1)
			continue
		}

		pkt.Payload = append([]byte(nil), pkt.Payload...)

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
// Supports RFC 4571 (2-byte length) and GB28181 0x24 framing
// ('$' + 1-byte channel + 2-byte big-endian length = 4-byte header).
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
			// Rest of 0x24 framing: channel (1) + 2-byte length
			if _, err := io.ReadFull(r.conn, buf[1:4]); err != nil {
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
			if _, err := io.ReadFull(r.conn, buf[:4]); err != nil {
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
	// SSRC isolation: latch the dialog's SSRC from the first packet and drop
	// everything else (stale sender on a recycled port — see the struct
	// comment). One foreign AU interleaved mid-stream corrupts the demuxer
	// state for the rest of the session.
	if !r.ssrcLatched.Load() {
		r.expectedSSRC.Store(pkt.Header.SSRC)
		r.ssrcLatched.Store(true)
	} else if pkt.Header.SSRC != r.expectedSSRC.Load() {
		r.foreignDrops.Add(1)
		if !r.foreignLogged.Load() {
			r.foreignLogged.Store(true)
			logger.Warn("gb28181: dropping RTP from foreign SSRC on session port",
				"camera_id", r.cameraID,
				"expected_ssrc", r.expectedSSRC.Load(),
				"foreign_ssrc", pkt.Header.SSRC)
		}
		r.rtpPacketsDropped.Add(1)
		return nil
	}

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
		// Force flush to make room. Mid-AU: the demuxer only accumulates
		// (complete=false) — partial NALUs are never emitted.
		r.emitAccessUnitsLocked(false)
	}

	// Store packet
	r.jitterBuffer[seq] = pkt
	r.lastSeq = seq

	// Marker bit indicates AU boundary - emit complete AU
	if pkt.Header.Marker {
		r.emitAccessUnitsLocked(true)
	}

	return nil
}

// emitAccessUnitsLocked reassembles packets in sequence order and emits
// complete access units. Must be called with jitterBufferMu held. complete
// marks whether this emission ends on a marker (real AU boundary).
//
// Loss recovery: when the packet at baseSeq is missing (UDP loss), the
// contiguous drain below would stall forever. Instead the gap is skipped —
// baseSeq advances to the lowest buffered sequence number — so the stream
// continues (the partial AU is dropped, which the PS demuxer tolerates)
// instead of freezing and growing the buffer without bound.
func (r *Receiver) emitAccessUnitsLocked(complete bool) {
	if len(r.jitterBuffer) == 0 {
		return
	}

	// Build ordered list of packets by sequence number,
	// starting from the oldest received (lowest seq number).
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
		// baseSeq itself is missing: skip the gap to the lowest buffered seq
		// that is AHEAD of baseSeq (positive 16-bit distance — wrap-safe).
		next, ok := r.nextBufferedSeqLocked()
		if !ok {
			return
		}
		r.packetsDroppedU++
		logger.Debug("gb28181: RTP packet loss — skipping gap",
			"camera_id", r.cameraID, "from_seq", r.baseSeq, "to_seq", next)
		r.baseSeq = next
		// Re-run so the buffered packets above the gap are emitted now.
		r.emitAccessUnitsLocked(complete)
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
	r.ptsClock.Store(ptsTicks)

	// Feed to Stage 2: PS demuxer
	nalus, err := r.demuxer.FeedAU(auPayload, ptsTicks, complete)
	if err != nil {
		logger.Debug("gb28181: PS demux error", "camera_id", r.cameraID, "error", err)
		return
	}

	// If no NALUs, this was a non-video PS packet (e.g., system header)
	if len(nalus) == 0 {
		r.dispatchAudioLocked()
		return
	}

	// One marker can cover multiple frames when the AU boundary was lost
	// upstream (dropped marker packet → concatenated PS bursts → the demuxer
	// returns both frames' NALUs together). Split at VCL frame boundaries so
	// every consumer gets exactly one frame per access unit.
	subAUs := splitAUsByFrame(nalus, r.demuxer.Codec() == "h265")

	for _, subAU := range subAUs {
		r.emitAULocked(subAU, ptsTicks)
	}
	r.dispatchAudioLocked()
}

// emitAULocked fans one single-frame access unit out to the hub and the
// registered callbacks. Must be called with jitterBufferMu held.
func (r *Receiver) emitAULocked(au [][]byte, ptsTicks int64) {
	r.auEmitted.Add(1)

	// Detect IDR frame
	isH265 := r.demuxer.Codec() == "h265"
	isIDR := nalutil.IsIDR(au, isH265)
	if isIDR {
		r.lastIDRUnix.Store(time.Now().UnixNano())
	}

	// Broadcast to StreamHub (non-blocking, nil-safe for recorder-bridged sessions)
	if r.hub != nil {
		r.hub.Broadcast(ptsTicks, au, isIDR)
	}

	// Prefer AU-granular callback (recorders need full AU grouping); fall
	// back to the legacy per-NALU callback.
	if r.AUCallback != nil {
		r.AUCallback(au, ptsTicks, isIDR)
	} else if r.NALUCallback != nil {
		for _, nalu := range au {
			r.NALUCallback(nalu, ptsTicks, isIDR)
		}
	}
}

// dispatchAudioLocked drains demuxed audio frames to the callback. Must be
// called with jitterBufferMu held (audio demux state lives under the same
// lock as video reassembly).
func (r *Receiver) dispatchAudioLocked() {
	if r.AudioCallback == nil {
		r.demuxer.DrainAudio() // discard — no consumer
		return
	}
	for _, frame := range r.demuxer.DrainAudio() {
		r.AudioCallback(frame)
	}
}

// nextBufferedSeqLocked returns the buffered sequence number closest ahead of
// baseSeq (positive int16 distance, wrap-safe), or ok=false when the buffer
// holds nothing ahead. Must be called with jitterBufferMu held.
func (r *Receiver) nextBufferedSeqLocked() (uint16, bool) {
	var best uint16
	found := false
	for seq := range r.jitterBuffer {
		if int16(seq-r.baseSeq) <= 0 {
			continue // stale (already passed) — drop it
		}
		if !found || int16(seq-best) < 0 {
			best = seq
			found = true
		}
	}
	if !found {
		// Nothing ahead: the buffer only holds stale entries — clear them.
		r.jitterBuffer = make(map[uint16]*rtp.Packet)
	}
	return best, found
}

// flushJitterBuffer emits any remaining packets in the jitter buffer.
func (r *Receiver) flushJitterBuffer() {
	r.jitterBufferMu.Lock()
	defer r.jitterBufferMu.Unlock()

	// Emit any remaining packets as one AU (stream end = boundary)
	if len(r.jitterBuffer) > 0 {
		r.emitAccessUnitsLocked(true)
	}

	// Flush demuxer residual data
	nalus := r.demuxer.Flush()
	if len(nalus) > 0 {
		isH265 := r.demuxer.Codec() == "h265"
		ptsTicks := r.ptsClock.Load()
		for _, subAU := range splitAUsByFrame(nalus, isH265) {
			isIDR := nalutil.IsIDR(subAU, isH265)
			if r.hub != nil {
				r.hub.Broadcast(ptsTicks, subAU, isIDR)
			}

			if r.AUCallback != nil {
				r.AUCallback(subAU, ptsTicks, isIDR)
			} else if r.NALUCallback != nil {
				for _, nalu := range subAU {
					r.NALUCallback(nalu, ptsTicks, isIDR)
				}
			}
		}
		r.dispatchAudioLocked()
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
