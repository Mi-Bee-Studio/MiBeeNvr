package recorder

import (
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model/nalutil"
)

// detectCodec identifies the stream codec from PARAMETER-SET NALUs only:
// H.264 SPS/PPS/AUD (types 7/8/9) vs H.265 VPS/SPS/PPS ((b>>1)&0x3F = 32-34).
// Slice NALUs are deliberately ambiguous — an H.264 slice with
// nal_ref_idc≥2 (e.g. 0x41) maps into the H.265 type range under the shift
// and previously mislabeled the whole stream as h265, so the recorder waited
// forever for VPS/SPS/PPS that never came and never opened a segment. AUs
// without parameter sets defer to the configured encoding hint.
func detectCodec(au [][]byte, encoding string) string {
	c, _ := detectCodecDetailed(au, encoding)
	return c
}

// detectCodecDetailed is detectCodec with definitiveness: definitive=true
// means the codec was decided from a parameter-set NALU (reliable);
// definitive=false means it fell back to the configured encoding hint — a
// later definitive observation must be allowed to override it (#625).
func detectCodecDetailed(au [][]byte, encoding string) (codec string, definitive bool) {
	for _, nalu := range au {
		if len(nalu) == 0 {
			continue
		}
		firstByte := nalu[0]
		if firstByte&0x80 != 0 {
			continue // forbidden_zero bit set — corrupt NALU
		}
		t264 := firstByte & 0x1F
		if t264 == 7 || t264 == 8 || t264 == 9 { // SPS / PPS / AUD — definitive
			return "h264", true
		}
		if t264 == 1 || t264 == 5 {
			// H.264 slices collide with H.265 param-set slots under the
			// 6-bit shift (0x41→VPS 32, 0x45→PPS 34) — never decide codec
			// from a slice; wait for a real parameter-set NALU.
			continue
		}
		t265 := (firstByte >> 1) & 0x3F
		if t265 == 32 || t265 == 33 || t265 == 34 { // VPS / SPS / PPS — definitive
			return "h265", true
		}
	}
	if encoding != "" {
		return encoding, false
	}
	return "", false
}

func updateParamSetsH264(au [][]byte, currentSPS, currentPPS []byte, gate *nalutil.ParamRotationGate, now time.Time) (newSPS, newPPS []byte, changed bool) {
	sps, pps := nalutil.ExtractParamSetsH264(au)
	if sps != nil {
		newSPS = append([]byte(nil), sps...)
		// #642: decode-equivalent SPS variants (VUI/timing-only differences)
		// don't rotate; unclassifiable changes are rate-limited.
		if currentSPS != nil && !nalutil.EqualParamSets(currentSPS, sps) &&
			gate.ShouldRotateSPS(currentSPS, sps, false, now) {
			changed = true
		}
	}
	if pps != nil {
		newPPS = append([]byte(nil), pps...)
		// No semantic PPS parser — rate-limit rapid variant alternation (#642).
		if currentPPS != nil && !nalutil.EqualParamSets(currentPPS, pps) &&
			gate.ShouldRotateUnparsed(now) {
			changed = true
		}
	}
	return newSPS, newPPS, changed
}

func updateParamSetsH265(au [][]byte, currentVPS, currentSPS, currentPPS []byte, gate *nalutil.ParamRotationGate, now time.Time) (newVPS, newSPS, newPPS []byte, changed bool) {
	vps, sps, pps := nalutil.ExtractParamSetsH265(au)
	if vps != nil {
		newVPS = append([]byte(nil), vps...)
		// No semantic VPS parser — rate-limit rapid variant alternation (#642).
		if currentVPS != nil && !nalutil.EqualParamSets(currentVPS, vps) &&
			gate.ShouldRotateUnparsed(now) {
			changed = true
		}
	}
	if sps != nil {
		newSPS = append([]byte(nil), sps...)
		// #642: decode-equivalent SPS variants don't rotate; real codec
		// changes rotate immediately.
		if currentSPS != nil && !nalutil.EqualParamSets(currentSPS, sps) &&
			gate.ShouldRotateSPS(currentSPS, sps, true, now) {
			changed = true
		}
	}
	if pps != nil {
		newPPS = append([]byte(nil), pps...)
		// No semantic PPS parser — rate-limit rapid variant alternation (#642).
		if currentPPS != nil && !nalutil.EqualParamSets(currentPPS, pps) &&
			gate.ShouldRotateUnparsed(now) {
			changed = true
		}
	}
	return newVPS, newSPS, newPPS, changed
}

// prepareBroadcastAU ensures IDR fan-out carries parameter sets for
// fast-start subscribers. GB28181 PS streams already carry SPS/PPS/VPS in the
// IDR access unit, so they are only prepended when missing — prepending them
// unconditionally produced [sps,pps,sps,pps,idr] duplicates.
func prepareBroadcastAU(au [][]byte, isIDR bool, codecType string, sps, pps, vps []byte) [][]byte {
	if !isIDR {
		return au
	}
	if codecType == "h264" && sps != nil && pps != nil {
		if hasH264ParamSets(au) {
			return au
		}
		broadcastAU := make([][]byte, 0, len(au)+2)
		broadcastAU = append(broadcastAU, sps, pps)
		broadcastAU = append(broadcastAU, au...)
		return broadcastAU
	}
	if codecType == "h265" && vps != nil && sps != nil && pps != nil {
		if hasH265ParamSets(au) {
			return au
		}
		broadcastAU := make([][]byte, 0, len(au)+3)
		broadcastAU = append(broadcastAU, vps, sps, pps)
		broadcastAU = append(broadcastAU, au...)
		return broadcastAU
	}
	return au
}

func hasH264ParamSets(au [][]byte) bool {
	sps, pps := nalutil.ExtractParamSetsH264(au)
	return sps != nil && pps != nil
}

func hasH265ParamSets(au [][]byte) bool {
	vps, sps, pps := nalutil.ExtractParamSetsH265(au)
	return vps != nil && sps != nil && pps != nil
}

func findVCLNALU(au [][]byte, codecType string) []byte {
	for _, nalu := range au {
		if len(nalu) == 0 {
			continue
		}
		firstByte := nalu[0]
		if codecType == "h264" {
			naluType := firstByte & 0x1F
			if naluType == 1 || naluType == 5 {
				return nalu
			}
		} else if codecType == "h265" {
			naluType := (firstByte >> 1) & 0x3F
			if naluType < 32 {
				return nalu
			}
		}
	}
	return nil
}
