// Package nalutil provides shared NALU (Network Abstraction Layer Unit) detection
// utilities for H.264 and H.265 video streams.
//
// This is the single source of truth for IDR/keyframe detection across the codebase.
package nalutil

// IsKeyframeNALU checks if a single NALU is an IDR frame.
//
// H.264: NAL type 5 = IDR (extract via nalu[0] & 0x1F)
// H.265: NAL type 19 (IDR_W_RADL) and 20 (IDR_N_LP) = IDR (extract via (nalu[0] >> 1) & 0x3F)
func IsKeyframeNALU(nalu []byte, isH265 bool) bool {
	if len(nalu) == 0 {
		return false
	}
	if isH265 {
		naluType := (nalu[0] >> 1) & 0x3F
		return naluType == 19 || naluType == 20
	}
	naluType := nalu[0] & 0x1F
	return naluType == 5
}

// IsIDR checks if an access unit (a slice of NALUs, e.g., [SPS, PPS, IDR])
// contains at least one IDR NALU.
func IsIDR(au [][]byte, isH265 bool) bool {
	for _, nalu := range au {
		if IsKeyframeNALU(nalu, isH265) {
			return true
		}
	}
	return false
}
