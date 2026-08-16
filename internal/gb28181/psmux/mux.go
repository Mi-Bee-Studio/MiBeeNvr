// Package psmux multiplexes H.264/H.265 access units and G.711 audio frames
// into an MPEG-PS byte stream, and packetizes it as RTP/UDP — the wire format
// GB/T 28181 platforms expect from a streaming device. It is the mirror of
// the platform-side demuxer (internal/gb28181 PSDemuxer); round-trip tests
// feed muxer output back through that demuxer.
package psmux

import (
	"encoding/binary"
)

// PSM stream types (GB/T 28181 附录 C — same values as the demuxer).
const (
	streamTypeH264  = 0x1B
	streamTypeH265  = 0x24
	streamTypeG711A = 0x90
	streamTypeG711U = 0x91
)

const (
	videoStreamID = 0xE0
	audioStreamID = 0xC0
	// maxPESPayload keeps PES_packet_length (16-bit) representable; AUs are
	// split into continuation PES packets above it (mirrors device behavior
	// and keeps the platform-side bounded-PES parser happy).
	maxPESPayload = 60000
)

// Muxer builds PS bytes for one access unit or audio frame per call. The
// video codec must be set before the first WriteAU ("h264"|"h265"); audio
// codec ("g711a"|"g711u") whenever it becomes known. Not safe for concurrent
// use — callers serialize per stream.
type Muxer struct {
	videoType byte // 0 until set
	audioType byte // 0 = no audio track
	psmDirty  bool // PSM (re)sent on next IDR
}

func New() *Muxer { return &Muxer{psmDirty: true} }

func (m *Muxer) SetVideoCodec(codec string) {
	switch codec {
	case "h265":
		m.videoType = streamTypeH265
	default:
		m.videoType = streamTypeH264
	}
	m.psmDirty = true
}

// SetAudioCodec declares the audio track in the PSM ("" removes it).
func (m *Muxer) SetAudioCodec(codec string) {
	switch codec {
	case "g711a":
		m.audioType = streamTypeG711A
	case "g711u":
		m.audioType = streamTypeG711U
	default:
		m.audioType = 0
	}
	m.psmDirty = true
}

// WriteAU returns the PS bytes for one video access unit (Annex-B NALUs).
// isIDR governs system-header + PSM inclusion.
func (m *Muxer) WriteAU(annexB []byte, pts int64, isIDR bool) []byte {
	out := packHeader(pts)
	if isIDR {
		out = append(out, systemHeader(m.videoType, m.audioType)...)
		if m.psmDirty {
			out = append(out, psm(m.videoType, m.audioType)...)
			m.psmDirty = false
		}
	}
	// Split into bounded PES packets: the first carries the PTS, continuation
	// packets carry raw payload only.
	first := true
	for off := 0; off < len(annexB) || (off == 0 && len(annexB) == 0); {
		end := off + maxPESPayload
		if end > len(annexB) {
			end = len(annexB)
		}
		chunk := annexB[off:end]
		if first {
			out = append(out, videoPES(chunk, pts, true)...)
			first = false
		} else {
			out = append(out, videoPES(chunk, 0, false)...)
		}
		off = end
		if off >= len(annexB) {
			break
		}
	}
	return out
}

// WriteAudio returns the PS bytes for one G.711 frame (raw codec bytes).
func (m *Muxer) WriteAudio(payload []byte, pts int64) []byte {
	out := packHeader(pts)
	out = append(out, audioPES(payload, pts)...)
	return out
}

func packHeader(pts int64) []byte {
	// 00 00 01 BA + SCR (6, derived from PTS/300, marker bits fixed) +
	// program_mux_rate + stuffing. Devices vary in SCR precision; a PTS-derived
	// base with extension 0 is widely accepted.
	scr := pts / 300
	if scr < 0 {
		scr = 0
	}
	h := make([]byte, 0, 14)
	h = append(h, 0x00, 0x00, 0x01, 0xBA)
	b := byte(scr>>27)&0x18 | 0x44 // '01' + SCR[32..30] + marker
	h = append(h, b)
	h = append(h, byte(scr>>19), byte(scr>>11)|0x01, byte(scr>>3), byte(scr&0x7)<<5|0x04)
	h = append(h, 0x01) // SCR extension (0) + marker
	// program_mux_rate: 3.5Mbps (3500000/50 = 70000) with end markers.
	rate := uint32(70000)
	h = append(h, byte(rate>>14), byte(rate>>6), byte(rate&0x3F)<<2|0x03)
	h = append(h, 0xF8) // stuffing length 0 ('11111' + reserved)
	return h
}

func systemHeader(videoType, audioType byte) []byte {
	body := []byte{
		0x00, 0x00, // header_length (fixed below)
		0x01, // marker_bound_video... rate bound fields (constant, conventional values)
		0xB9, 0xE0, 0xE0, 0x80,
		0xC0, 0x01, // audio bound/flags — matches common device output
	}
	// stream entries: video first, audio when declared
	entries := []byte{videoType, videoStreamID, 0xE1, 0x00}
	if audioType != 0 {
		entries = append(entries, audioType, audioStreamID, 0xC0, 0x03)
	}
	body = append(body, entries...)
	body[0] = byte(len(body) - 2>>8)
	body[1] = byte(len(body) - 2)
	h := append([]byte{0x00, 0x00, 0x01, 0xBB}, byte(len(body)>>8), byte(len(body)))
	return append(h, body...)
}

func psm(videoType, audioType byte) []byte {
	var esm []byte
	esm = append(esm, videoType, videoStreamID, 0x00, 0x00)
	if audioType != 0 {
		esm = append(esm, audioType, audioStreamID, 0x00, 0x00)
	}
	body := make([]byte, 0, 8+len(esm)+4)
	body = append(body, 0xE0, 0xFF, // current_next + version + marker
		0x00, 0x00) // program_stream_info_length
	body = append(body, byte(len(esm)>>8), byte(len(esm)))
	body = append(body, esm...)
	crc := crc32MPEG(body)
	body = binary.BigEndian.AppendUint32(body, crc)

	h := append([]byte{0x00, 0x00, 0x01, 0xBC}, byte(len(body)>>8), byte(len(body)))
	return append(h, body...)
}

func videoPES(payload []byte, pts int64, withPTS bool) []byte {
	hdrLen := 3
	if withPTS {
		hdrLen += 5
	}
	pes := make([]byte, 0, 6+hdrLen+len(payload))
	pes = append(pes, 0x00, 0x00, 0x01, videoStreamID)
	pes = binary.BigEndian.AppendUint16(pes, uint16(hdrLen+len(payload)))
	if withPTS {
		pes = append(pes, 0x80, 0x80, 0x05)
		pes = append(pes, encodePTS(pts)...)
	} else {
		pes = append(pes, 0x80, 0x00, 0x00)
	}
	return append(pes, payload...)
}

func audioPES(payload []byte, pts int64) []byte {
	hdrLen := 8
	pes := make([]byte, 0, 6+hdrLen+len(payload))
	pes = append(pes, 0x00, 0x00, 0x01, audioStreamID)
	pes = binary.BigEndian.AppendUint16(pes, uint16(hdrLen+len(payload)))
	pes = append(pes, 0x80, 0x80, 0x05)
	pes = append(pes, encodePTS(pts)...)
	return append(pes, payload...)
}

// encodePTS packs a 33-bit PTS into the 5-byte MPEG form ('0010' prefix).
func encodePTS(pts int64) []byte {
	p := uint64(pts) & 0x1FFFFFFFF
	b := make([]byte, 5)
	b[0] = 0x21 | byte((p>>30)&0x07)<<1 | 1
	b[1] = byte(p >> 22)
	b[2] = byte(p>>14)&0xFE | 1
	b[3] = byte(p >> 7)
	b[4] = byte(p<<1)&0xFE | 1
	return b
}

// crc32MPEG computes CRC-32/MPEG-2 (poly 0x04C11DB7, init all-ones, no
// reflection) as required for the PSM CRC_32 field.
func crc32MPEG(data []byte) uint32 {
	crc := uint32(0xFFFFFFFF)
	for _, b := range data {
		crc ^= uint32(b) << 24
		for range 8 {
			if crc&0x80000000 != 0 {
				crc = crc<<1 ^ 0x04C11DB7
			} else {
				crc <<= 1
			}
		}
	}
	return crc
}
