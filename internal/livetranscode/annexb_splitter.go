// Package livetranscode provides pure-Go utilities for live transcoding of video streams.
//
// This file implements Annex-B (byte stream format) to Access Unit splitting
// for both H.264 (AVC) and H.265 (HEVC), suitable for processing FFmpeg
// pipeline output.
package livetranscode

import "errors"

// Codec represents the video codec type.
type Codec int

const (
	// CodecH264 represents H.264/AVC.
	CodecH264 Codec = iota
	// CodecH265 represents H.265/HEVC.
	CodecH265
)

// ParamSets contains parameter set NALUs extracted from the stream.
type ParamSets struct {
	SPS []byte // Sequence Parameter Set (H.264: type 7, H.265: type 33)
	PPS []byte // Picture Parameter Set (H.264: type 8, H.265: type 34)
	VPS []byte // Video Parameter Set (H.265 only, type 32)
}

// AccessUnit is a complete access unit (one coded picture) — a slice of NAL units.
type AccessUnit [][]byte

// NALU type constants for H.264 (nal_unit_type from low 5 bits of first byte).
const (
	h264NALUTypeNonIDR = 1 // Coded slice of a non-IDR picture
	h264NALUTypeIDR    = 5 // Coded slice of an IDR picture
	h264NALUTypeSEI    = 6 // Supplemental enhancement information
	h264NALUTypeSPS    = 7 // Sequence parameter set
	h264NALUTypePPS    = 8 // Picture parameter set
	h264NALUTypeAUD    = 9 // Access unit delimiter
)

// NALU type constants for H.265 (nal_unit_type from bits 1-6 of first byte).
const (
	h265NALUTypeTRAIL_N    = 0  // Trailing picture (non-reference)
	h265NALUTypeTRAIL_R    = 1  // Trailing picture (reference)
	h265NALUTypeIDR_W_RADL = 19 // IDR picture with RADL
	h265NALUTypeIDR_N_LP   = 20 // IDR picture, no leading pictures
	h265NALUTypeVPS        = 32 // Video parameter set
	h265NALUTypeSPS        = 33 // Sequence parameter set
	h265NALUTypePPS        = 34 // Picture parameter set
	h265NALUTypeAUD        = 35 // Access unit delimiter
)

var (
	// ErrNoStartCode is returned when no valid Annex-B start code is found.
	ErrNoStartCode = errors.New("no valid Annex-B start code found")
	// ErrEmptyBuffer is returned when the input buffer is empty.
	ErrEmptyBuffer = errors.New("empty buffer")
	// ErrH2653ByteSC is returned when H.265 input contains a 3-byte start code.
	ErrH2653ByteSC = errors.New("H.265 Annex-B does not support 3-byte start codes (00 00 01)")
)

// startCode describes the position and length of a found start code in the buffer.
type startCode struct {
	pos    int // byte offset of the first byte of the start code (first 0x00)
	length int // 3 for 00 00 01, 4 for 00 00 00 01
}

// naluInfo holds information about an extracted NALU.
type naluInfo struct {
	data []byte // the NALU bytes with EPB stripped (includes the header byte(s))
	typ  int    // NALU type (H.264: low 5 bits; H.265: bits 1-6)
}

// --------------- NALU type helpers ---------------

// naluTypeH264 returns the H.264 NALU type from the first byte.
// Extract: nal_unit_type = first_byte & 0x1F
func naluTypeH264(nalu []byte) int {
	if len(nalu) == 0 {
		return -1
	}
	return int(nalu[0] & 0x1F)
}

// naluTypeH265 returns the H.265 NALU type from the first two bytes.
// Extract: nal_unit_type = (first_byte >> 1) & 0x3F
func naluTypeH265(nalu []byte) int {
	if len(nalu) == 0 {
		return -1
	}
	return int((nalu[0] >> 1) & 0x3F)
}

// isVCLH264 returns true if the H.264 NALU type is a VCL (video coding layer) NALU.
// VCL types: 1-5 (coded slice, data partitions, IDR slice).
func isVCLH264(typ int) bool {
	return typ >= 1 && typ <= 5
}

// isVCLH265 returns true if the H.265 NALU type is a VCL NALU.
// VCL types: 0-23 inclusive (all slice segment types).
func isVCLH265(typ int) bool {
	return typ >= 0 && typ <= 23
}

// --------------- Emulation prevention ---------------

// stripEPB removes emulation prevention bytes (0x03 inserted after 0x00 0x00)
// from a NALU as specified in ITU-T H.264 §7.4.1.1 / H.265 §7.4.1.1.
//
// The encoder inserts 0x03 before any byte in {0x00, 0x01, 0x02, 0x03}
// that follows a 0x00 0x00 sequence, to prevent false start codes.
func stripEPB(data []byte) []byte {
	// Fast path: check if any EPB exists
	hasEPB := false
	for i := 2; i < len(data); i++ {
		if data[i-2] == 0 && data[i-1] == 0 && data[i] == 3 {
			hasEPB = true
			break
		}
	}
	if !hasEPB {
		return data
	}

	// Slow path: allocate result and copy with EPB removal
	result := make([]byte, 0, len(data))
	i := 0
	for i < len(data) {
		if i+2 < len(data) && data[i] == 0 && data[i+1] == 0 && data[i+2] == 3 {
			result = append(result, 0, 0)
			i += 3 // skip the 0x03
		} else {
			result = append(result, data[i])
			i++
		}
	}
	return result
}

// --------------- Start code detection ---------------

// findStartCodes scans buf for Annex-B start codes and returns their positions.
//
// For H.264: both 3-byte (00 00 01) and 4-byte (00 00 00 01) codes are accepted.
// For H.265: only 4-byte codes are valid; encountering a 3-byte code returns
// ErrH2653ByteSC.
func findStartCodes(buf []byte, codec Codec) ([]startCode, error) {
	if len(buf) < 3 {
		return nil, nil
	}

	var codes []startCode

	for i := 0; i <= len(buf)-3; i++ {
		// Look for the 00 00 01 pattern
		if buf[i] == 0 && buf[i+1] == 0 && buf[i+2] == 1 {
			// Check if this is a 4-byte code (00 00 00 01)
			if i > 0 && buf[i-1] == 0 {
				// 4-byte start code at position i-1
				sc := startCode{pos: i - 1, length: 4}
				if len(codes) == 0 || codes[len(codes)-1].pos != sc.pos {
					codes = append(codes, sc)
				}
			} else if codec == CodecH265 {
				// H.265 does not allow 3-byte start codes
				return nil, ErrH2653ByteSC
			} else {
				// H.264: 3-byte start code at position i
				sc := startCode{pos: i, length: 3}
				if len(codes) == 0 || codes[len(codes)-1].pos != sc.pos {
					codes = append(codes, sc)
				}
			}
			// Skip ahead to avoid overlapping matches
			i += 2
		}
	}

	return codes, nil
}

// --------------- NALU extraction ---------------

// extractNalus extracts individual NALUs from buf given the start code positions,
// stripping emulation prevention bytes from each.
func extractNalus(buf []byte, codes []startCode, codec Codec) ([]naluInfo, error) {
	if len(codes) == 0 {
		return nil, ErrNoStartCode
	}

	var nalus []naluInfo

	for i := 0; i < len(codes); i++ {
		sc := codes[i]
		naluStart := sc.pos + sc.length

		var naluEnd int
		if i+1 < len(codes) {
			naluEnd = codes[i+1].pos
		} else {
			naluEnd = len(buf)
		}

		// Skip empty NALUs (zero bytes between start codes)
		if naluEnd <= naluStart {
			continue
		}

		rawNalu := buf[naluStart:naluEnd]
		nalu := stripEPB(rawNalu)

		if len(nalu) == 0 {
			continue
		}

		var typ int
		switch codec {
		case CodecH264:
			typ = naluTypeH264(nalu)
		case CodecH265:
			typ = naluTypeH265(nalu)
		}

		nalus = append(nalus, naluInfo{data: nalu, typ: typ})
	}

	if len(nalus) == 0 {
		return nil, ErrNoStartCode
	}

	return nalus, nil
}

// --------------- AU grouping ---------------

// groupAccessUnits groups NALUs into access units based on access unit boundary rules.
//
// An AU boundary is determined when:
//   - An AUD (access unit delimiter) NALU is encountered
//   - An IDR NALU is encountered (starts a new GOP/picture)
//   - A VPS NALU is encountered in H.265 (starts a new sequence)
//   - An SPS or PPS is encountered after VCL data (param set change mid-stream)
//   - A VCL NALU (non-IDR) follows another VCL NALU (new picture)
func groupAccessUnits(nalus []naluInfo, codec Codec) []AccessUnit {
	if len(nalus) == 0 {
		return nil
	}

	var aus []AccessUnit
	var currentAU []naluInfo
	seenVCL := false // tracks if current AU already contains a VCL NALU

	flushAU := func() {
		if len(currentAU) > 0 {
			au := make(AccessUnit, len(currentAU))
			for i, n := range currentAU {
				au[i] = n.data
			}
			aus = append(aus, au)
			currentAU = nil
			seenVCL = false
		}
	}

	for _, nalu := range nalus {
		typ := nalu.typ

		switch codec {
		case CodecH264:
			if typ == h264NALUTypeAUD || typ == h264NALUTypeIDR {
				// AUD and IDR always start a new AU.
				// Flush previous AU only if it has VCL data (we have a complete picture).
				if seenVCL {
					flushAU()
				}
				currentAU = append(currentAU, nalu)
				if typ == h264NALUTypeIDR {
					seenVCL = true
				}
			} else if isVCLH264(typ) {
				// Non-IDR VCL (type 1-4): if we already have a VCL, this is a new picture.
				if seenVCL {
					flushAU()
				}
				currentAU = append(currentAU, nalu)
				seenVCL = true
			} else if typ == h264NALUTypeSPS || typ == h264NALUTypePPS {
				// SPS/PPS after VCL data indicate a param set change → new AU.
				if seenVCL {
					flushAU()
				}
				currentAU = append(currentAU, nalu)
			} else {
				// Other (SEI, filler, etc.) — attach to current AU.
				currentAU = append(currentAU, nalu)
			}

		case CodecH265:
			if typ == h265NALUTypeAUD || typ == h265NALUTypeIDR_W_RADL || typ == h265NALUTypeIDR_N_LP {
				if seenVCL {
					flushAU()
				}
				currentAU = append(currentAU, nalu)
				if typ == h265NALUTypeIDR_W_RADL || typ == h265NALUTypeIDR_N_LP {
					seenVCL = true
				}
			} else if typ == h265NALUTypeVPS || typ == h265NALUTypeSPS || typ == h265NALUTypePPS {
				if seenVCL {
					flushAU()
				}
				currentAU = append(currentAU, nalu)
			} else if isVCLH265(typ) {
				if seenVCL {
					flushAU()
				}
				currentAU = append(currentAU, nalu)
				seenVCL = true
			} else {
				currentAU = append(currentAU, nalu)
			}
		}
	}

	// Flush remaining
	flushAU()

	return aus
}

// --------------- Param set extraction ---------------

// extractParamSets scans the access units for the most recent parameter set NALUs.
func extractParamSets(aus []AccessUnit, codec Codec) ParamSets {
	var ps ParamSets

	for _, au := range aus {
		for _, nalu := range au {
			switch codec {
			case CodecH264:
				typ := naluTypeH264(nalu)
				switch typ {
				case h264NALUTypeSPS:
					ps.SPS = nalu
				case h264NALUTypePPS:
					ps.PPS = nalu
				}
			case CodecH265:
				typ := naluTypeH265(nalu)
				switch typ {
				case h265NALUTypeVPS:
					ps.VPS = nalu
				case h265NALUTypeSPS:
					ps.SPS = nalu
				case h265NALUTypePPS:
					ps.PPS = nalu
				}
			}
		}
	}

	return ps
}

// --------------- Batch parser ---------------

// SplitAnnexB parses a complete Annex-B byte stream buffer and splits it into
// individual access units (coded pictures). It returns the access units, the
// extracted parameter sets, and any error encountered.
//
// The buffer must start with a valid Annex-B start code.
// Emulation prevention bytes are automatically stripped from all returned NALUs.
func SplitAnnexB(data []byte, codec Codec) ([]AccessUnit, ParamSets, error) {
	if len(data) == 0 {
		return nil, ParamSets{}, ErrEmptyBuffer
	}

	codes, err := findStartCodes(data, codec)
	if err != nil {
		return nil, ParamSets{}, err
	}

	nalus, err := extractNalus(data, codes, codec)
	if err != nil {
		return nil, ParamSets{}, err
	}

	aus := groupAccessUnits(nalus, codec)
	ps := extractParamSets(aus, codec)

	return aus, ps, nil
}

// --------------- Streaming parser ---------------

// AnnexBStreamParser incrementally parses an Annex-B byte stream that arrives
// in chunks (e.g., from an FFmpeg stdout pipe). It maintains internal state to
// handle NALUs and start codes that span chunk boundaries.
//
// Usage:
//
//	sp := NewAnnexBStreamParser(CodecH264)
//	for {
//	    chunk := readFromFFmpeg()
//	    if chunk == nil { break }
//	    aus := sp.Feed(chunk)
//	    for _, au := range aus {
//	        process(au)
//	    }
//	}
//	// Finalize: aus := sp.Flush()
type AnnexBStreamParser struct {
	codec     Codec
	buf       []byte     // accumulated raw bytes from chunks
	pending   []naluInfo // NALUs of incomplete AUs
	paramSets ParamSets  // most recently observed parameter sets
}

// NewAnnexBStreamParser creates a new streaming Annex-B parser for the given codec.
func NewAnnexBStreamParser(codec Codec) *AnnexBStreamParser {
	return &AnnexBStreamParser{
		codec: codec,
	}
}

// ParamSets returns the most recently observed parameter sets from the stream.
func (sp *AnnexBStreamParser) ParamSets() ParamSets {
	return sp.paramSets
}

// Feed processes a chunk of Annex-B byte stream data and returns any complete
// access units that have been parsed. The parser handles start codes and NALUs
// that span chunk boundaries.
//
// An AU is only returned once the next AU has been detected (its first NALU),
// ensuring correct AU boundary detection. The last incomplete AU is returned
// by calling Flush() after all data has been fed.
//
// The returned access units have emulation prevention bytes stripped.
func (sp *AnnexBStreamParser) Feed(chunk []byte) []AccessUnit {
	if len(chunk) == 0 {
		return nil
	}

	// Append new data to internal buffer
	sp.buf = append(sp.buf, chunk...)
	if len(sp.buf) < 4 {
		return nil
	}

	// Find start codes in accumulated buffer
	codes, err := findStartCodes(sp.buf, sp.codec)
	if err != nil {
		// For H.265 with 3-byte codes, we can't recover
		sp.buf = nil
		return nil
	}

	if len(codes) < 2 {
		// Need at least 2 start codes to extract a complete NALU
		return nil
	}

	// Trim buffer to start at the first SC (dropping partial prefix bytes)
	if codes[0].pos > 0 {
		sp.buf = sp.buf[codes[0].pos:]
		for i := range codes {
			codes[i].pos -= codes[0].pos
		}
	}

	// Extract complete NALUs (between codes[i] and codes[i+1] for i in [0, N-2])
	var newNalus []naluInfo
	for i := 0; i < len(codes)-1; i++ {
		sc := codes[i]
		naluStart := sc.pos + sc.length
		nextSC := codes[i+1]
		naluEnd := nextSC.pos

		if naluEnd <= naluStart {
			continue
		}

		rawNalu := sp.buf[naluStart:naluEnd]
		nalu := stripEPB(rawNalu)
		if len(nalu) == 0 {
			continue
		}

		var typ int
		switch sp.codec {
		case CodecH264:
			typ = naluTypeH264(nalu)
		case CodecH265:
			typ = naluTypeH265(nalu)
		}

		newNalus = append(newNalus, naluInfo{data: nalu, typ: typ})
	}

	if len(newNalus) == 0 {
		return nil
	}

	// ALWAYS trim buffer to the last start code, so already-processed
	// start codes are removed from the buffer. This prevents re-extracting
	// the same NALUs on subsequent Feed calls.
	lastSC := codes[len(codes)-1]
	sp.buf = sp.buf[lastSC.pos:]

	// Update param sets from new NALUs
	for _, n := range newNalus {
		sp.updateParamSet(n)
	}

	// Append new NALUs to pending
	sp.pending = append(sp.pending, newNalus...)

	// Group ALL pending NALUs into AUs
	aus := groupAccessUnits(sp.pending, sp.codec)

	// If we have more than 1 AU, the first N-1 AUs are complete
	// (confirmed by the start of the Nth AU).
	if len(aus) <= 1 {
		// Not enough info to confirm any AU is complete
		return nil
	}

	// Count NALUs consumed by complete AUs and remove from pending
	totalConsumed := 0
	for i := 0; i < len(aus)-1; i++ {
		totalConsumed += len(aus[i])
	}
	sp.pending = sp.pending[totalConsumed:]

	return aus[:len(aus)-1]
}

// updateParamSet updates paramSets if n is a parameter set NALU.
func (sp *AnnexBStreamParser) updateParamSet(n naluInfo) {
	switch sp.codec {
	case CodecH264:
		switch n.typ {
		case h264NALUTypeSPS:
			sp.paramSets.SPS = n.data
		case h264NALUTypePPS:
			sp.paramSets.PPS = n.data
		}
	case CodecH265:
		switch n.typ {
		case h265NALUTypeVPS:
			sp.paramSets.VPS = n.data
		case h265NALUTypeSPS:
			sp.paramSets.SPS = n.data
		case h265NALUTypePPS:
			sp.paramSets.PPS = n.data
		}
	}
}

// Flush returns any remaining access units in the parser's internal buffer.
// This should be called after the stream has ended (no more Feed calls).
func (sp *AnnexBStreamParser) Flush() []AccessUnit {
	// Process any remaining raw bytes
	if len(sp.buf) >= 4 {
		codes, err := findStartCodes(sp.buf, sp.codec)
		if err == nil && len(codes) > 0 {
			nalus, err := extractNalus(sp.buf, codes, sp.codec)
			if err == nil {
				for _, n := range nalus {
					sp.updateParamSet(n)
				}
				sp.pending = append(sp.pending, nalus...)
			}
		}
	}

	if len(sp.pending) == 0 {
		sp.buf = nil
		return nil
	}

	aus := groupAccessUnits(sp.pending, sp.codec)
	sp.pending = nil
	sp.buf = nil
	return aus
}
