package gb28181

// splitAUsByFrame splits a demuxed NALU list into per-frame access units at
// VCL frame boundaries.
//
// Why: the RTP-marker AU boundary can be lost upstream — one dropped marker
// packet makes the jitter buffer concatenate two PS bursts, and the PS demuxer
// then returns BOTH frames' NALUs as one "access unit" (observed on cascaded
// streams: 150 VCL NALUs across 120 AUs). A multi-frame sample breaks every
// downstream consumer: the WebRTC packetizer stamps both frames with one RTP
// timestamp (browser decoder corrupts its reference chain — persistent green
// macroblocks), the MP4 writer keeps only the first VCL NAL (frames silently
// dropped), and the WASM/WebCodecs decoder sees contradictory frame_nums.
//
// Boundary rule (ITU-T H.264 §7.3.3 / H.265 §7.3.6.1):
//   - H.264: a VCL NAL (types 1-5) opens a new frame when its slice header's
//     first field, first_mb_in_slice (ue(v)), equals 0 — and ue(0) is encoded
//     as the single bit '1', so the top bit of the first RBSP byte after the
//     NAL header decides it.
//   - H.265: a VCL NAL (types 0-31) opens a new frame when
//     first_slice_segment_in_pic_flag (the first bit after the 2-byte NAL
//     header) is 1.
//
// Non-VCL NALUs (SPS/PPS/SEI/AUD) stay attached to the frame that follows
// them, matching Annex-B ordering.
func splitAUsByFrame(nalus [][]byte, isH265 bool) [][][]byte {
	if len(nalus) <= 1 {
		if len(nalus) == 1 {
			return [][][]byte{nalus}
		}
		return nil
	}

	var out [][][]byte
	var cur [][]byte
	var trailing [][]byte // non-VCL NALUs seen after a VCL — they describe the NEXT picture
	seenVCL := false
	for _, nalu := range nalus {
		if len(nalu) == 0 {
			continue
		}
		vcl := isVCLNALU(nalu, isH265)
		if vcl && seenVCL && startsNewFrame(nalu, isH265) && len(cur) > 0 {
			out = append(out, cur)
			cur = nil
		}
		if vcl {
			if len(cur) == 0 {
				// New AU opens with any pending parameter-set/SEI NALUs.
				cur = append(cur, trailing...)
			}
			trailing = nil
			cur = append(cur, nalu)
			seenVCL = true
		} else if seenVCL {
			trailing = append(trailing, nalu)
		} else {
			// Leading non-VCL NALUs (SPS/PPS/SEI before the first frame).
			cur = append(cur, nalu)
		}
	}
	if len(trailing) > 0 {
		// No following frame — keep them with the last AU rather than dropping.
		cur = append(cur, trailing...)
	}
	if len(cur) > 0 {
		out = append(out, cur)
	}
	return out
}

// isVCLNALU reports whether the NALU carries video slice data.
func isVCLNALU(nalu []byte, isH265 bool) bool {
	if isH265 {
		t := (nalu[0] >> 1) & 0x3F
		return t <= 31
	}
	t := nalu[0] & 0x1F
	return t >= 1 && t <= 5
}

// startsNewFrame reports whether a VCL NALU begins a new picture. See the
// function comment above for the bit-level rule.
func startsNewFrame(nalu []byte, isH265 bool) bool {
	hdrLen := 1
	if isH265 {
		hdrLen = 2
	}
	if len(nalu) <= hdrLen {
		return true
	}
	// Skip emulation-prevention sequences (00 00 03 xx) in the first RBSP
	// bytes — first_mb_in_slice / first_slice_segment_in_pic_flag live in the
	// first byte and a preceding escape is rare but must not misread.
	rbsp := nalu[hdrLen:]
	if len(rbsp) >= 4 && rbsp[0] == 0 && rbsp[1] == 0 && rbsp[2] == 3 {
		rbsp = rbsp[3:]
	}
	// Top bit: H.264 ue(v) '1' == first_mb_in_slice 0; H.265
	// first_slice_segment_in_pic_flag 1.
	return rbsp[0]&0x80 != 0
}
