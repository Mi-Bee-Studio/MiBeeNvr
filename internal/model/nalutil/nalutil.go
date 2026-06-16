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

// ExtractParamSetsH264 scans an H.264 access unit and returns the most recently
// observed SPS (NAL type 7) and PPS (NAL type 8), without the start-code prefix.
// Returns nil for either if not present. This is the single source of truth for
// SPS/PPS extraction (previously duplicated inline across h264/h265/xiaomi
// recorders and the timelapse keyframe extractor).
func ExtractParamSetsH264(au [][]byte) (sps, pps []byte) {
	for _, nalu := range au {
		if len(nalu) == 0 {
			continue
		}
		switch nalu[0] & 0x1F {
		case 7: // SPS
			sps = nalu
		case 8: // PPS
			pps = nalu
		}
	}
	return sps, pps
}

// EqualParamSets reports whether two SPS/PPS NAL units are byte-identical.
// nil comparisons are treated as equal-to-nil only (nil != non-nil).
func EqualParamSets(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
