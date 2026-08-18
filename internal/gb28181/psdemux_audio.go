package gb28181

import (
	"fmt"
	"log/slog"
)

// Audio-path helpers for the MPEG-PS demuxer: audio PES parsing, PTS
// extraction, PSM audio stream enumeration, and AAC ADTS framing.

// SetAudioCodecHint seeds the no-PSM audio fallback with the codec declared
// in the device's INVITE answer SDP (a=rtpmap PCMA/PCMU/mpeg4-generic). The
// hint is used only while the stream carries no PSM audio declaration — a
// PSM (the device's own in-stream statement) always wins.
func (d *PSDemuxer) SetAudioCodecHint(codec string) {
	d.audioHint = codec
}

// resolveFallbackAudioCodec resolves the codec of an audio stream that no PSM
// has declared: a previously latched decision, the SDP hint, or a byte-shape
// heuristic (ADTS sync → AAC; μ-law vs A-law by quiet-cluster voting over the
// first ~4KB of payload — near-silent samples cluster at 0xFF/0x7F for μ-law
// and 0x55/0xD5 for A-law). Returns "" while evidence is still insufficient.
func (d *PSDemuxer) resolveFallbackAudioCodec(streamID byte, payload []byte) string {
	if c, ok := d.audioResolved[streamID]; ok {
		return c
	}
	codec := d.guessAudioCodec(payload)
	if codec == "" {
		return ""
	}
	if d.audioResolved == nil {
		d.audioResolved = make(map[byte]string)
	}
	d.audioResolved[streamID] = codec
	slog.Info("gb28181: PS audio stream not declared in PSM — codec inferred",
		"stream_id", fmt.Sprintf("0x%02X", streamID), "codec", codec,
		"hint", d.audioHint, "mulaw_votes", d.guessMulaw, "alaw_votes", d.guessAlaw)
	return codec
}

// guessAudioCodec applies the fallback detection order to one payload: ADTS
// sync (self-describing AAC) → SDP hint → quiet-cluster voting. Voting
// accumulates across calls until 4KB has been scanned.
func (d *PSDemuxer) guessAudioCodec(payload []byte) string {
	// ADTS sync at the payload start: frame length must be sane, which
	// distinguishes real ADTS from G.711 bytes that happen to open with
	// 0xFF 0Fx (μ-law near-silence).
	if len(payload) >= 7 && payload[0] == 0xFF && payload[1]&0xF0 == 0xF0 {
		frameLen := int(payload[3]&0x03)<<11 | int(payload[4])<<3 | int(payload[5])>>5
		if frameLen >= 7 && frameLen <= 8192 {
			return AudioCodecAAC
		}
	}
	if d.audioHint != "" {
		return d.audioHint
	}
	for _, b := range payload {
		switch {
		case b >= 0xFD, b >= 0x7D && b <= 0x7F:
			d.guessMulaw++
		case b >= 0x54 && b <= 0x56, b >= 0xD4 && b <= 0xD6:
			d.guessAlaw++
		}
	}
	d.guessScanned += len(payload)
	if d.guessScanned < 4096 {
		return "" // keep collecting votes
	}
	if d.guessMulaw >= d.guessAlaw {
		return AudioCodecG711U
	}
	return AudioCodecG711A
}

// parseAudioPES parses an audio PES packet and returns the payload, its PTS
// (when present), and the total PES length. Audio uses the standard
// ITU-T H.222.0 §2.4.3.7 layout only — the legacy firmware quirk handled for
// video (pesPayloadStart in psdemux_pes.go) has never been observed on audio
// PES, and the payload carries no Annex-B start code to calibrate against.
// Returns (payload, pts, hasPTS, totalPESLength, error).
func parseAudioPES(data []byte) ([]byte, int64, bool, int, error) {
	data = stripStartCodeLeadingZeros(data)
	if len(data) < 9 {
		return nil, 0, false, 0, ErrIncompletePES
	}
	if data[0] != 0x00 || data[1] != 0x00 || data[2] != 0x01 {
		return nil, 0, false, 0, ErrIncompletePES
	}
	streamID := data[3]
	if streamID < startCodeAudioMin || streamID > startCodeAudioMax {
		return nil, 0, false, 0, ErrIncompletePES
	}

	pesLen := int(data[4])<<8 | int(data[5])
	totalLen := 6 + pesLen
	if pesLen == 0 {
		// Unbounded audio PES — take everything available.
		totalLen = len(data)
	} else if len(data) < totalLen {
		return nil, 0, false, 0, ErrIncompletePES
	}

	headerDataLen := int(data[8])
	payloadStart := 9 + headerDataLen
	if payloadStart > totalLen {
		return nil, 0, false, 0, ErrIncompletePES
	}

	pts, hasPTS := parsePESPTS(data)
	return data[payloadStart:totalLen], pts, hasPTS, totalLen, nil
}

// parsePESPTS extracts the 33-bit PTS from a PES header (H.222.0 §2.4.3.6
// 5-byte encoding at bytes 9-13, present when flag 0x80 is set in byte 7).
func parsePESPTS(data []byte) (int64, bool) {
	if len(data) < 14 || data[7]&0x80 == 0 {
		return 0, false
	}
	b := data[9:14]
	pts := ((int64(b[0])>>1)&0x07)<<30 |
		int64(b[1])<<22 |
		(int64(b[2])>>1)<<15 |
		int64(b[3])<<7 |
		int64(b[4])>>1
	return pts, true
}

// audioStreamTypes scans a PSM elementary_stream_map for audio streams and
// returns stream_id (0xC0-0xDF) → stream_type. psmData starts after the
// 6-byte packet header — same layout as findVideoStreamType.
func audioStreamTypes(psmData []byte) map[byte]byte {
	out := make(map[byte]byte)
	if len(psmData) < 4 {
		return out
	}
	infoLen := int(psmData[2])<<8 | int(psmData[3])
	offset := 4 + infoLen
	if offset+2 > len(psmData) {
		return out
	}
	offset += 2 // elementary_stream_map_length

	for offset <= len(psmData)-4 {
		streamType := psmData[offset]
		esID := psmData[offset+1]
		esInfoLen := int(psmData[offset+2])<<8 | int(psmData[offset+3])
		if esID >= startCodeAudioMin && esID <= startCodeAudioMax {
			out[esID] = streamType
		}
		offset += 4 + esInfoLen
	}
	return out
}

// adtsFrame is one AAC frame: its ADTS header (nil when the payload is raw)
// and the stripped AAC payload.
type adtsFrame struct {
	header []byte
	data   []byte
}

// adtsSampleRates indexes the ADTS sampling_frequency_index (4 bits).
var adtsSampleRates = [...]int{
	96000, 88200, 64000, 48000, 44100, 32000, 24000, 22050,
	16000, 12000, 11025, 8000, 7350, 0, 0, 0,
}

// splitADTS splits an audio PES payload into AAC frames. When the payload
// carries ADTS framing (0xFF 0xF sync — the GB28181 convention), frames are
// split by their declared lengths and headers stripped. A raw payload (no
// sync) is returned as one frame with a nil header.
func splitADTS(payload []byte) []adtsFrame {
	if !hasADTSSync(payload) {
		return []adtsFrame{{header: nil, data: payload}}
	}
	var frames []adtsFrame
	for offset := 0; offset+7 <= len(payload); {
		if !hasADTSSync(payload[offset:]) {
			break // trailing garbage — drop
		}
		hdr := payload[offset : offset+7]
		hdrLen := 7
		if hdr[1]&0x01 == 0 { // protection_absent == 0 → CRC present
			hdrLen = 9
		}
		frameLen := int(hdr[3]&0x03)<<11 | int(hdr[4])<<3 | int(hdr[5])>>5
		if frameLen < hdrLen || offset+frameLen > len(payload) {
			break // corrupt length — drop the remainder
		}
		frames = append(frames, adtsFrame{
			header: hdr,
			data:   payload[offset+hdrLen : offset+frameLen],
		})
		offset += frameLen
	}
	return frames
}

func hasADTSSync(data []byte) bool {
	return len(data) >= 2 && data[0] == 0xFF && data[1]&0xF0 == 0xF0
}

// adtsToASC derives the 2-byte AudioSpecificConfig from an ADTS header
// (AAC-LC assumption: audioObjectType = ADTS profile + 1). Returns nil for a
// nil header or an out-of-range sampling index.
func adtsToASC(header []byte) []byte {
	if len(header) < 7 {
		return nil
	}
	profile := (header[2] >> 6) & 0x03
	freqIdx := (header[2] >> 2) & 0x0F
	channels := (header[2]&0x01)<<2 | header[3]>>6
	if freqIdx >= uint8(len(adtsSampleRates)) || adtsSampleRates[freqIdx] == 0 {
		return nil
	}
	audioObjectType := profile + 1
	return []byte{
		byte(audioObjectType)<<3 | byte(freqIdx)>>1,
		byte(freqIdx&0x01)<<7 | byte(channels)<<3,
	}
}

// adtsSampleRate returns the sample rate encoded in an ADTS header (0 when
// unknown). Used to compute AAC sample durations in sinks.
func adtsSampleRate(header []byte) int {
	if len(header) < 7 {
		return 0
	}
	freqIdx := (header[2] >> 2) & 0x0F
	if int(freqIdx) >= len(adtsSampleRates) {
		return 0
	}
	return adtsSampleRates[freqIdx]
}
