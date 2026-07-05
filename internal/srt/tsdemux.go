package srt

import (
	"errors"
	"fmt"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model/nalutil"
)

// MPEG-TS constants
const (
	tsPacketSize      = 188
	tsSyncByte        = 0x47
	tsHeaderSize      = 4
	tsAdaptFieldSize  = 1
	tsPESHeaderSize   = 9
	tsPayloadUnitSize = 184

	// H.264 NALU types
	naluTypeIDR = 5
	naluTypeSPS = 7
	naluTypePPS = 8
)

var (
	ErrNoSyncByte    = errors.New("srt: MPEG-TS sync byte not found")
	ErrInvalidPacket = errors.New("srt: invalid MPEG-TS packet")
	ErrShortPacket   = errors.New("srt: packet too short")
)

// TSDemuxer demuxes MPEG-TS packets and extracts H.264 NALUs from PES payloads.
// It reassembles PES packets from TS packet payloads and splits the PES payload
// into individual NALUs using Annex B start code detection.
type TSDemuxer struct {
	// Current PES assembly state
	pid     uint16 // Current PID being assembled
	pts     int64  // PTS from PES header (90kHz clock)
	payload []byte // PES payload buffer (NALU data)
	hasPTS  bool   // Whether we've parsed PTS from current PES
	started bool   // Whether we've seen a PES start
}

// NewTSDemuxer creates a new MPEG-TS to H.264 NALU demuxer.
func NewTSDemuxer() *TSDemuxer {
	return &TSDemuxer{}
}

// NALU represents an H.264 NAL unit with its presentation timestamp.
type NALU struct {
	PTS  int64  // PTS in 90kHz clock ticks
	Data []byte // Raw NALU data (with start code stripped)
	Type uint8  // NALU type
}

// isKeyframeNALU returns true if the NALU is an IDR slice.
// Delegates to nalutil.IsKeyframeNALU. SRT currently supports H.264 only.
func isKeyframeNALU(data []byte) bool {
	return nalutil.IsKeyframeNALU(data, false)
}

// Feed processes MPEG-TS data and returns extracted NALUs.
// The input data may contain partial or multiple TS packets.
// NALUs are returned when a complete access unit is detected
// (on the next PES start or at the end of data).
func (d *TSDemuxer) Feed(data []byte) []NALU {
	var nalus []NALU

	offset := 0
	for offset+tsPacketSize <= len(data) {
		// Find sync byte
		if data[offset] != tsSyncByte {
			// Scan for next sync byte
			found := false
			for i := offset; i < len(data); i++ {
				if data[i] == tsSyncByte && i+tsPacketSize <= len(data) {
					offset = i
					found = true
					break
				}
			}
			if !found {
				break
			}
		}

		pkt := data[offset : offset+tsPacketSize]
		offset += tsPacketSize

		nalus = append(nalus, d.processPacket(pkt)...)
	}

	return nalus
}

// Flush returns any remaining buffered NALUs.
func (d *TSDemuxer) Flush() []NALU {
	if len(d.payload) == 0 {
		return nil
	}
	nalus := extractNALUs(d.payload, d.pts)
	d.payload = d.payload[:0]
	d.started = false
	return nalus
}

// processPacket processes a single 188-byte MPEG-TS packet.
func (d *TSDemuxer) processPacket(pkt []byte) []NALU {
	if len(pkt) != tsPacketSize || pkt[0] != tsSyncByte {
		return nil
	}

	// Parse TS header
	// Byte 1: transport error (1) | payload unit start (1) | transport priority (1) | PID (13)
	// Byte 2-3: PID continued | transport scrambling (2) | adaptation field control (2) | continuity counter (4)
	pusi := pkt[1]&0x40 != 0 // Payload Unit Start Indicator
	pid := uint16(pkt[1]&0x07)<<8 | uint16(pkt[2])
	afc := (pkt[3] >> 4) & 0x03 // Adaptation field control

	// Skip null packets and packets without payload
	if pid == 0x1FFF || afc == 0x02 || afc == 0x00 {
		return nil
	}

	// Calculate payload offset
	payloadOffset := tsHeaderSize
	if afc == 0x03 || afc == 0x02 {
		// Adaptation field present
		afLen := int(pkt[4])
		payloadOffset = tsHeaderSize + tsAdaptFieldSize + afLen
		if payloadOffset >= tsPacketSize {
			return nil
		}
	}

	payload := pkt[payloadOffset:]
	if len(payload) == 0 {
		return nil
	}

	// Handle PES start (Payload Unit Start Indicator)
	if pusi {
		var nalus []NALU

		// Flush previous PES if any
		if d.started && len(d.payload) > 0 {
			nalus = extractNALUs(d.payload, d.pts)
			d.payload = d.payload[:0]
		}

		// Parse PES header
		pts, pesPayloadOffset, ok := parsePESHeader(payload)
		if ok {
			d.pts = pts
			d.hasPTS = true
			d.pid = pid
			d.started = true
			if pesPayloadOffset < len(payload) {
				d.payload = append(d.payload, payload[pesPayloadOffset:]...)
			}
		}

		return nalus
	}

	// Continuation of current PES
	if d.started && pid == d.pid {
		d.payload = append(d.payload, payload...)
	}

	return nil
}

// parsePESHeader parses a PES header from the payload and returns the PTS and offset to PES payload.
func parsePESHeader(data []byte) (pts int64, payloadOffset int, ok bool) {
	// PES header: start code (3) + stream ID (1) + PES packet length (2) + flags (3+)
	if len(data) < 9 {
		return 0, 0, false
	}

	// Check PES start code prefix (0x000001)
	if data[0] != 0x00 || data[1] != 0x00 || data[2] != 0x01 {
		return 0, 0, false
	}

	streamID := data[3]
	// Only process video streams (0xE0-0xEF)
	if streamID < 0xE0 || streamID > 0xEF {
		return 0, 0, false
	}

	// PES header data length
	headerDataLength := int(data[8])
	payloadOffset = 9 + headerDataLength

	// Parse PTS/DTS flags
	ptsDTSFlags := (data[7] >> 6) & 0x03

	if ptsDTSFlags >= 2 && len(data) >= 14 {
		// PTS present (5 bytes, 33-bit value at 90kHz)
		pts = int64(data[9]&0x0E)<<29 |
			int64(data[10])<<22 |
			int64(data[11]&0xFE)<<14 |
			int64(data[12])<<7 |
			int64(data[13]&0xFE)>>1
	}

	return pts, payloadOffset, true
}

// extractNALUs splits PES payload data into individual NALUs using Annex B start codes.
func extractNALUs(data []byte, pts int64) []NALU {
	if len(data) == 0 {
		return nil
	}

	var nalus []NALU

	// Find all start code positions
	positions := findStartCodes(data)

	// Extract NALUs between consecutive start codes
	for i, pos := range positions {
		end := len(data)
		if i+1 < len(positions) {
			end = positions[i+1]
		}

		naluData := data[pos:end]
		if len(naluData) == 0 {
			continue
		}

		// Determine start code length (3 or 4 bytes)
		scLen := 3
		if pos+3 <= len(data) && data[pos+2] == 0x00 && data[pos+3] == 0x01 {
			scLen = 4
		}

		// Strip start code from NALU data
		naluRaw := naluData[scLen:]
		if len(naluRaw) == 0 {
			continue
		}

		naluType := naluRaw[0] & 0x1F

		nalus = append(nalus, NALU{
			PTS:  pts,
			Data: naluRaw,
			Type: naluType,
		})
	}

	return nalus
}

func findStartCodes(data []byte) []int {
	var positions []int

	i := 0
	for i <= len(data)-3 {
		if data[i] == 0x00 && data[i+1] == 0x00 {
			if data[i+2] == 0x01 {
				// 3-byte start code: 00 00 01
				positions = append(positions, i)
				i += 3
			} else if data[i+2] == 0x00 && i+3 < len(data) && data[i+3] == 0x01 {
				// 4-byte start code: 00 00 00 01
				positions = append(positions, i)
				i += 4
			} else {
				i++
			}
		} else {
			i++
		}
	}

	return positions
}

// assembleAccessUnit groups NALUs into access units (frames) for broadcast.
// Returns slice of access units, each being a slice of NALUs belonging to one frame.
// An access unit boundary is detected at the start of a new IDR frame or
// when SPS/PPS NALUs appear.
func assembleAccessUnit(nalus []NALU) [][][]byte {
	if len(nalus) == 0 {
		return nil
	}

	// Group into access units: each AU starts at an IDR or SPS NALU
	// preceded by non-parameter-set NALUs
	var frames [][][]byte
	var current [][]byte

	for _, nalu := range nalus {
		// New access unit starts at SPS or when IDR follows non-SPS/PPS NALUs
		if (nalu.Type == naluTypeSPS || nalu.Type == naluTypeIDR) && len(current) > 0 {
			// Check if previous NALUs were all SPS/PPS (prefix of current AU)
			allParamSets := true
			for _, n := range current {
				naluType := n[0] & 0x1F
				if naluType != naluTypeSPS && naluType != naluTypePPS {
					allParamSets = false
					break
				}
			}
			if !allParamSets {
				// Emit current AU
				frames = append(frames, current)
				current = nil
			}
		}

		// Make a copy to avoid aliasing
		naluCopy := make([]byte, len(nalu.Data))
		copy(naluCopy, nalu.Data)
		current = append(current, naluCopy)
	}

	if len(current) > 0 {
		frames = append(frames, current)
	}

	return frames
}

// formatPTS formats PTS from 90kHz ticks to a human-readable string.
func formatPTS(pts int64) string {
	if pts <= 0 {
		return "0.000s"
	}
	return fmt.Sprintf("%.3fs", float64(pts)/90000.0)
}
