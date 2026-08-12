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

		// Determine start code length (3 or 4 bytes)
		scLen := 3
		if pos+3 <= len(data) && data[pos+3] == 0x01 {
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

// parseVideoPES parses a video PES packet and returns the payload and total PES length.
// Returns (payload, totalPESLength, error).
func parseVideoPES(data []byte) ([]byte, int, error) {
	// PES: start_code (4) + stream_id (1) + PES_packet_length (2) + optional PES_header
	// Minimum: 6 bytes (start_code + stream_id + PES_packet_length)
	if len(data) < 6 {
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

	// Parse optional PES header
	if len(data) < 8 {
		return nil, 0, ErrIncompletePES
	}

	// PES_header_data_length at offset 7
	headerDataLen := int(data[7])
	headerLen := 8 + headerDataLen
	totalLen := 6 + pesPacketLen

	// Check if we have enough data
	if pesPacketLen > 0 && len(data) < totalLen {
		// Incomplete PES
		return nil, 0, ErrIncompletePES
	}

	// Check if we have enough data for the header
	if len(data) < headerLen {
		return nil, 0, ErrIncompletePES
	}

	// Extract payload (after PES header)
	payloadOffset := headerLen
	if pesPacketLen > 0 {
		payload := data[payloadOffset:totalLen]
		return payload, totalLen, nil
	}

	// Unbounded PES (pesPacketLen == 0) - return everything after header
	payload := data[payloadOffset:]
	return payload, len(data), nil
}

// findPSStartCode finds the next MPEG-PS start code in the data.
// Returns the position of the start code, the start code value, and an error if not found.
// NALU start codes (00 00 00 01 followed by 0x67, 0x68, 0x06, etc.) are skipped.
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
			return i, startCodeValue, nil
		default:
			if startCodeValue >= startCodeAudioMin && startCodeValue <= startCodeAudioMax {
				return i, startCodeValue, nil
			}
			if startCodeValue >= startCodeVideoMin && startCodeValue <= startCodeVideoMax {
				return i, startCodeValue, nil
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
// The PSM body starts with: current_next_indicator (1 bit) + PS_version_number (5 bits) + reserved (2 bits) + PS_info_length (2 bytes) + PS_info bytes.
func findVideoStreamType(psmData []byte) (byte, bool) {
	if len(psmData) < 4 {
		return 0, false
	}

	// Skip version byte + reserved byte + PS_info_length + PS_info bytes
	infoLen := int(psmData[2])<<8 | int(psmData[3])
	offset := 4 + infoLen

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
