package gb28181

import (
	"errors"
)

// extractNALUs splits video payload data into individual NALUs using Annex B start codes.
// The naluType parameter determines which NALU type mask to use.
// Returns NALUs with start codes stripped.
func extractNALUs(data []byte, naluType string) [][]byte {
	if len(data) == 0 {
		return nil
	}

	var nalus [][]byte
	positions := findStartCodes(data)

	for i, pos := range positions {
		end := len(data)
		if i+1 < len(positions) {
			end = positions[i+1]
		}

		naluData := data[pos:end]
		if len(naluData) == 0 {
			continue
		}

		// Determine start code length (3 or 4 bytes). A 4-byte code is
		// 00 00 00 01; a 3-byte code is 00 00 01. Checking data[pos+3]==0x01
		// alone is wrong: for a 3-byte code that byte is the NAL header,
		// which is 0x01 for H.264 non-ref slices / H.265 TRAIL_N.
		scLen := 3
		if pos+3 < len(data) && data[pos+2] == 0x00 && data[pos+3] == 0x01 {
			// 4-byte start code: 00 00 00 01
			scLen = 4
		}

		// Strip start code from NALU data
		naluRaw := naluData[scLen:]
		if len(naluRaw) == 0 {
			continue
		}

		nalus = append(nalus, naluRaw)
	}

	return nalus
}

// findStartCodes finds all Annex B start code positions in the data.
// Returns positions of 00 00 01 or 00 00 00 01 patterns.
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

// stripStartCodeLeadingZeros tolerates zero bytes stuffed between a PES
// payload's end and the next packet's 23-bit start code. Annex-B permits
// trailing_zero_8bytes after the last NALU, so a payload ending in 0x00
// followed by the next PES header reads 00 00 00 01 E0 — a legal stream that
// a strict 3-byte prefix check rejects. All leading zeros before the 00 00 01
// prefix are skipped.
func stripStartCodeLeadingZeros(data []byte) []byte {
	for len(data) >= 4 && data[0] == 0x00 && data[1] == 0x00 && data[2] == 0x00 && data[3] == 0x01 {
		data = data[1:]
	}
	return data
}

// parseVideoPES parses a video PES packet and returns the payload and total PES length.
// Returns (payload, totalPESLength, error).
func parseVideoPES(data []byte) ([]byte, int, error) {
	// PES: start_code (4) + stream_id (1) + PES_packet_length (2) + optional PES_header
	// pesPayloadStart reads bytes 7-8, so 9 bytes are required up front.
	data = stripStartCodeLeadingZeros(data)
	if len(data) < 9 {
		return nil, 0, ErrIncompletePES
	}

	// Check start_code prefix (0x000001)
	if data[0] != 0x00 || data[1] != 0x00 || data[2] != 0x01 {
		return nil, 0, errors.New("invalid PES start code")
	}

	streamID := data[3]
	// Check if video stream (0xE0-0xEF)
	if streamID < startCodeVideoMin || streamID > startCodeVideoMax {
		return nil, 0, ErrIncompletePES
	}

	pesPacketLen := int(data[4])<<8 | int(data[5])
	totalLen := 6 + pesPacketLen

	// Check if we have enough data
	if pesPacketLen > 0 && len(data) < totalLen {
		// Incomplete PES
		return nil, 0, ErrIncompletePES
	}

	payloadOffset := pesPayloadStart(data, totalLen)
	if payloadOffset > totalLen || payloadOffset > len(data) {
		return nil, 0, ErrIncompletePES
	}

	// Extract payload (after PES header)
	if pesPacketLen > 0 {
		payload := data[payloadOffset:totalLen]
		return payload, totalLen, nil
	}

	// Unbounded PES (pesPacketLen == 0) - return everything after header
	payload := data[payloadOffset:]
	return payload, len(data), nil
}

// pesPayloadStart locates the elementary-stream start within a video PES.
//
// Header-layout tolerance: the standard (ITU-T H.222.0 §2.4.3.7) places
// PES_header_data_length at byte 8 (payload at 9+hdrlen). At least one real
// firmware in the field writes hdrlen at byte 7 (payload at 8+hdrlen) with
// two 5-byte custom timestamps as optional fields — and byte 8 then holds
// optional data (0x31), not a length. Instead of trusting either byte, the
// payload start is CALIBRATED against the first Annex-B start code in the
// header region: the ES begins there in both layouts (Annex-B permits a few
// leading zero bytes). Continuation PES (a frame split across PES packets,
// standard devices) carry no start code — the standard position is used.
func pesPayloadStart(data []byte, totalLen int) int {
	std := 9 + int(data[8])
	legacy := 8 + int(data[7])
	limit := totalLen
	if limit > len(data) {
		limit = len(data)
	}
	clamp := func(v int) int {
		if v < 9 {
			return 9
		}
		if v > limit {
			return limit
		}
		return v
	}
	std, legacy = clamp(std), clamp(legacy)

	// Scan the header region (a few bytes past both candidates) for the
	// first Annex-B start code.
	bound := std
	if legacy > bound {
		bound = legacy
	}
	bound += 4
	if bound > limit {
		bound = limit
	}
	sc := -1
	for i := 8; i+2 < bound; i++ {
		if data[i] == 0x00 && data[i+1] == 0x00 && data[i+2] == 0x01 {
			sc = i
			break
		}
	}
	if sc < 0 {
		return std // no start code → continuation PES (standard layout)
	}
	// Pick the candidate closest to the observed start code.
	near := func(v int) int {
		d := sc - v
		if d < 0 {
			d = -d
		}
		return d
	}
	if near(legacy) < near(std) {
		return legacy
	}
	return std
}

// findPSStartCode finds the next MPEG-PS start code in the data.
// Returns the position of the start code, the start code value, and an error
// if not found. NALU start codes (00 00 00 01 followed by 0x67, 0x68, 0x06,
// etc.) are skipped. The returned position always points at a 3-byte prefix
// (00 00 01 <value>): a 4-byte prefix (00 00 00 01 <value>, produced when a
// PES payload ends in trailing zeros — legal Annex-B — abutting the next
// packet header) is normalized so every consumer indexing pos+3/+4/+5 sees
// stream_id / PES_packet_length at the standard offsets.
func findPSStartCode(data []byte) (int, byte, error) {
	for i := 0; i < len(data); i++ {
		// Need at least 3 bytes for a start code prefix (00 00 01)
		if i+2 >= len(data) || data[i] != 0x00 || data[i+1] != 0x00 {
			continue
		}
		scLen := 3
		if data[i+2] == 0x00 {
			// Potential 4-byte prefix (00 00 00 01)
			if i+3 >= len(data) || data[i+3] != 0x01 {
				continue
			}
			scLen = 4
		} else if data[i+2] != 0x01 {
			continue
		}
		// Start code prefix found; the value byte must be present
		if i+scLen >= len(data) {
			return 0, 0, errors.New("incomplete PS start code at end of data")
		}
		startCodeValue := data[i+scLen]
		switch startCodeValue {
		case startCodePack, startCodeSystem, startCodePSM, startCodePadding, startCodePrivate2:
			return i + scLen - 3, startCodeValue, nil
		default:
			if startCodeValue >= startCodeAudioMin && startCodeValue <= startCodeAudioMax {
				return i + scLen - 3, startCodeValue, nil
			}
			if startCodeValue >= startCodeVideoMin && startCodeValue <= startCodeVideoMax {
				return i + scLen - 3, startCodeValue, nil
			}
			// NALU data - skip past this start code and keep scanning
			i += scLen
		}
	}
	return 0, 0, errors.New("no PS start code found")
}

// parsePSM parses a Program Stream Map and returns the end position of the PSM.
func parsePSM(data []byte) (int, error) {
	// PSM: start_code (4) + packet_length (2) + ...
	// Minimum PSM size: 4 + 2 + 1 + 2 = 9 bytes (for version + PS_info_length)
	if len(data) < 9 {
		return 0, ErrIncompletePSM
	}

	// Skip start_code (4 bytes) and packet_length (2 bytes) to get to PSM specific fields
	// packet_length is the number of bytes after the packet_length field
	psmLen := int(data[4])<<8 | int(data[5])
	if psmLen < 1 {
		return 0, ErrIncompletePSM
	}

	totalLen := 6 + psmLen
	if len(data) < totalLen {
		return 0, ErrIncompletePSM
	}

	return totalLen, nil
}

// findVideoStreamType scans PSM stream_info for the first video stream type.
// psmData starts after the 6-byte packet header (start code + map_stream_id +
// length): [0]=version byte, [1]=reserved/marker byte, [2:4]=
// program_stream_info_length, then PS info, then the 2-byte
// elementary_stream_map_length (ISO/IEC 13818-1 program_stream_map), then the
// 4-byte entries (stream_type + elementary_stream_id + es_info_length) and
// their info blobs.
func findVideoStreamType(psmData []byte) (byte, bool) {
	if len(psmData) < 4 {
		return 0, false
	}

	// Skip version byte + reserved byte + PS_info_length + PS_info bytes
	infoLen := int(psmData[2])<<8 | int(psmData[3])
	offset := 4 + infoLen
	// Skip the 2-byte elementary_stream_map_length that precedes the entries.
	if offset+2 > len(psmData) {
		return 0, false
	}
	offset += 2

	for offset <= len(psmData)-4 {
		streamType := psmData[offset]
		esID := psmData[offset+1]
		esInfoLen := int(psmData[offset+2])<<8 | int(psmData[offset+3])

		// Check if this is a video stream (elementary_stream_id 0xE0-0xEF)
		if esID >= startCodeVideoMin && esID <= startCodeVideoMax {
			if streamType == streamTypeH264 || streamType == streamTypeH265 {
				return streamType, true
			}
		}

		// Move to next stream_info
		offset += 4 + esInfoLen
	}

	return 0, false
}
