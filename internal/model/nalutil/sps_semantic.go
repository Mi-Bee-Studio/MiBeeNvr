// SPS semantic comparison for segment-rotation decisions.
//
// Some cameras (observed: a Xiaomi 2K PTZ model) alternate between two SPS
// encodings that differ only in VUI/timing decoration — semantically
// equivalent, byte-level different. Byte-equality rotation checks turned that
// into a segment-rotation storm: 188 rotations in 4.9h, each spawning a new
// rolling-merge bucket (MiBeeNvr #642). Comparing SPS semantically — every
// field that affects decodability, ignoring VUI — suppresses those rotations.
//
// The trick: H.264/H.265 SPS syntax is a fixed-order bitstream and Exp-Golomb
// coding is canonical, so two SPSs with equal pre-VUI field values ALWAYS
// produce equal bit prefixes. Hashing the consumed RBSP prefix through the end
// of the pre-VUI syntax (frame_cropping for H.264, strong_intra_smoothing for
// H.265) is therefore an exact comparison over all decodability-relevant
// fields, without enumerating them individually.

package nalutil

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
)

// semBitReader reads bits from an RBSP, MSB first. It is deliberately local to
// this file: the merge package keeps its own parser for resolution extraction;
// this one exists to count consumed bits for prefix hashing.
type semBitReader struct {
	data   []byte
	offset int
}

func (r *semBitReader) readBit() (int, error) {
	if r.offset >= len(r.data)*8 {
		return 0, fmt.Errorf("nalutil: sps bitReader overflow at bit %d (rbsp %d bits)", r.offset, len(r.data)*8)
	}
	byteIdx := r.offset / 8
	bitIdx := 7 - (r.offset % 8)
	r.offset++
	return int((r.data[byteIdx] >> bitIdx) & 1), nil
}

func (r *semBitReader) readBits(n int) (int, error) {
	val := 0
	for range n {
		bit, err := r.readBit()
		if err != nil {
			return 0, err
		}
		val = (val << 1) | bit
	}
	return val, nil
}

func (r *semBitReader) readUE() (int, error) {
	leadingZeros := 0
	for {
		bit, err := r.readBit()
		if err != nil {
			return 0, err
		}
		if bit == 1 {
			break
		}
		leadingZeros++
		if leadingZeros > 32 {
			return 0, fmt.Errorf("nalutil: sps readUE leadingZeros overflow (%d)", leadingZeros)
		}
	}
	if leadingZeros == 0 {
		return 0, nil
	}
	bits, err := r.readBits(leadingZeros)
	if err != nil {
		return 0, err
	}
	return (1 << leadingZeros) - 1 + bits, nil
}

func (r *semBitReader) readSE() (int, error) {
	val, err := r.readUE()
	if err != nil {
		return 0, err
	}
	if val%2 == 0 {
		return -(val / 2), nil
	}
	return (val + 1) / 2, nil
}

// semRemoveEPB strips emulation prevention bytes (0x00 0x00 0x03 → 0x00 0x00).
func semRemoveEPB(data []byte) []byte {
	var result []byte
	i := 0
	for i < len(data) {
		if i+2 < len(data) && data[i] == 0 && data[i+1] == 0 && data[i+2] == 3 {
			result = append(result, 0, 0)
			i += 3
		} else {
			result = append(result, data[i])
			i++
		}
	}
	return result
}

// prefixKey hashes codec + consumed bit count + the masked RBSP prefix into a
// stable key. The final partial byte has its unconsumed low bits zeroed so two
// prefixes that merely happen to share trailing bytes cannot collide.
func prefixKey(codec byte, rbsp []byte, bits int) string {
	n := (bits + 7) / 8
	if n > len(rbsp) {
		n = len(rbsp)
	}
	prefix := make([]byte, n)
	copy(prefix, rbsp[:n])
	if rem := bits % 8; rem != 0 && n > 0 {
		prefix[n-1] &= byte(0xFF) << (8 - rem)
	}
	h := sha256.New()
	h.Write([]byte{codec})
	var bitsBuf [8]byte
	binary.BigEndian.PutUint64(bitsBuf[:], uint64(bits))
	h.Write(bitsBuf[:])
	h.Write(prefix)
	return hex.EncodeToString(h.Sum(nil))
}

// SPSSemanticKey returns a canonical key over every SPS field that affects
// decodability — for H.264 from profile_idc through the frame cropping
// offsets; for H.265 from the NAL header through
// strong_intra_smoothing_enabled_flag. VUI parameters (timing info, bitstream
// restrictions) and everything after them are deliberately excluded: they do
// not change how samples decode. ok=false when the SPS cannot be parsed —
// callers must then fall back to byte comparison (conservative: rotate on any
// difference).
func SPSSemanticKey(sps []byte, isH265 bool) (key string, ok bool) {
	if isH265 {
		return h265SPSKey(sps)
	}
	return h264SPSKey(sps)
}

// SPSSemanticallyEqual reports whether two SPS NAL units (without start-code
// prefixes) are interchangeable for decoding: equal across all
// decodability-relevant fields, possibly differing only in VUI/timing
// decoration. When either SPS cannot be parsed it degrades to byte equality —
// a difference then means "rotate", preserving the pre-#642 conservative
// behavior for malformed input.
func SPSSemanticallyEqual(a, b []byte, isH265 bool) bool {
	if EqualParamSets(a, b) {
		return true
	}
	ka, okA := SPSSemanticKey(a, isH265)
	kb, okB := SPSSemanticKey(b, isH265)
	return okA && okB && ka == kb
}

// h264SPSKey parses an H.264 SPS (NAL header byte excluded from the key —
// nal_ref_idc differences are irrelevant) through the frame cropping offsets
// and hashes the consumed prefix.
func h264SPSKey(sps []byte) (string, bool) {
	if len(sps) < 8 {
		return "", false
	}
	rbsp := semRemoveEPB(sps[1:])
	if len(rbsp) < 4 {
		return "", false
	}
	r := &semBitReader{data: rbsp}

	profileIDC, err := r.readBits(8)
	if err != nil {
		return "", false
	}
	if _, err = r.readBits(8); err != nil { // constraint_set_flags
		return "", false
	}
	if _, err = r.readBits(8); err != nil { // level_idc
		return "", false
	}
	if _, err = r.readUE(); err != nil { // seq_parameter_set_id
		return "", false
	}

	highProfile := profileIDC == 100 || profileIDC == 110 || profileIDC == 122 ||
		profileIDC == 244 || profileIDC == 44 || profileIDC == 83 ||
		profileIDC == 86 || profileIDC == 118 || profileIDC == 128 ||
		profileIDC == 138 || profileIDC == 139 || profileIDC == 134

	if highProfile {
		chromaFormatIDC, err := r.readUE()
		if err != nil {
			return "", false
		}
		if chromaFormatIDC == 3 {
			if _, err = r.readBit(); err != nil { // separate_colour_plane_flag
				return "", false
			}
		}
		if _, err = r.readUE(); err != nil { // bit_depth_luma_minus8
			return "", false
		}
		if _, err = r.readUE(); err != nil { // bit_depth_chroma_minus8
			return "", false
		}
		if _, err = r.readBit(); err != nil { // qpprime_y_zero_transform_bypass_flag
			return "", false
		}
		scalingPresent, err := r.readBit()
		if err != nil {
			return "", false
		}
		if scalingPresent == 1 {
			count := 8
			if chromaFormatIDC == 3 {
				count = 12
			}
			for i := range count {
				present, err := r.readBit()
				if err != nil {
					return "", false
				}
				if present == 1 {
					size := 16
					if i >= 6 {
						size = 64
					}
					for range size {
						if _, err = r.readSE(); err != nil { // delta_scale
							return "", false
						}
					}
				}
			}
		}
	}

	if _, err = r.readUE(); err != nil { // log2_max_frame_num_minus4
		return "", false
	}
	picOrderCntType, err := r.readUE()
	if err != nil {
		return "", false
	}
	if picOrderCntType == 0 {
		if _, err = r.readUE(); err != nil { // log2_max_pic_order_cnt_lsb_minus4
			return "", false
		}
	} else if picOrderCntType == 1 {
		if _, err = r.readBit(); err != nil { // delta_pic_order_always_zero_flag
			return "", false
		}
		if _, err = r.readSE(); err != nil { // offset_for_non_ref_pic
			return "", false
		}
		if _, err = r.readSE(); err != nil { // offset_for_top_to_bottom_field
			return "", false
		}
		numRefFrames, err := r.readUE()
		if err != nil {
			return "", false
		}
		for range numRefFrames {
			if _, err = r.readSE(); err != nil { // offset_for_ref_frame[i]
				return "", false
			}
		}
	}

	if _, err = r.readUE(); err != nil { // max_num_ref_frames
		return "", false
	}
	if _, err = r.readBit(); err != nil { // gaps_in_frame_num_value_allowed_flag
		return "", false
	}
	if _, err = r.readUE(); err != nil { // pic_width_in_mbs_minus1
		return "", false
	}
	if _, err = r.readUE(); err != nil { // pic_height_in_map_units_minus1
		return "", false
	}
	frameMbsOnly, err := r.readBit()
	if err != nil {
		return "", false
	}
	if frameMbsOnly == 0 {
		if _, err = r.readBit(); err != nil { // mb_adaptive_frame_field_flag
			return "", false
		}
	}
	if _, err = r.readBit(); err != nil { // direct_8x8_inference_flag
		return "", false
	}
	frameCropping, err := r.readBit()
	if err != nil {
		return "", false
	}
	if frameCropping == 1 {
		for range 4 { // frame_crop_left/right/top/bottom_offset
			if _, err = r.readUE(); err != nil {
				return "", false
			}
		}
	}
	// VUI starts here — intentionally excluded from the key.
	return prefixKey(0x64, rbsp, r.offset), true
}

// h265SPSKey parses an H.265 SPS through strong_intra_smoothing_enabled_flag
// and hashes the consumed prefix. Scaling lists are the one bail-out: their
// DPCM syntax is not worth reimplementing for a comparison used only as a
// rotation gate, and cameras with scaling lists are effectively nonexistent —
// the caller falls back to byte comparison for them.
func h265SPSKey(sps []byte) (string, bool) {
	if len(sps) < 8 {
		return "", false
	}
	rbsp := semRemoveEPB(sps[2:]) // skip the 2-byte NAL header
	if len(rbsp) < 13 {
		return "", false
	}
	r := &semBitReader{data: rbsp}

	if _, err := r.readBits(4); err != nil { // sps_video_parameter_set_id
		return "", false
	}
	maxSubLayersMinus1, err := r.readBits(3)
	if err != nil {
		return "", false
	}
	if _, err = r.readBit(); err != nil { // sps_temporal_id_nesting_flag
		return "", false
	}

	// profile_tier_level(1, maxSubLayersMinus1) — general part.
	if _, err = r.readBits(8); err != nil { // general_profile_space/tier/idc
		return "", false
	}
	if _, err = r.readBits(32); err != nil { // general_profile_compatibility_flag[32]
		return "", false
	}
	if _, err = r.readBits(48); err != nil { // general constraint indicator flags
		return "", false
	}
	if _, err = r.readBits(8); err != nil { // general_level_idc
		return "", false
	}
	// Sub-layer part. Track the presence flags, then consume the actual
	// sub-layer profile/level payloads (unlike a resolution-only parser, a
	// semantic key cannot skip them — they affect decode).
	subProfilePresent := make([]bool, maxSubLayersMinus1)
	subLevelPresent := make([]bool, maxSubLayersMinus1)
	for i := range maxSubLayersMinus1 {
		pp, err := r.readBit()
		if err != nil {
			return "", false
		}
		lp, err := r.readBit()
		if err != nil {
			return "", false
		}
		subProfilePresent[i] = pp == 1
		subLevelPresent[i] = lp == 1
	}
	if maxSubLayersMinus1 > 0 {
		for range 8 - maxSubLayersMinus1 {
			if _, err = r.readBits(2); err != nil { // reserved_zero_2bits
				return "", false
			}
		}
	}
	for i := range maxSubLayersMinus1 {
		if subProfilePresent[i] {
			if _, err = r.readBits(8 + 32 + 48); err != nil {
				return "", false
			}
		}
		if subLevelPresent[i] {
			if _, err = r.readBits(8); err != nil {
				return "", false
			}
		}
	}

	if _, err = r.readUE(); err != nil { // sps_seq_parameter_set_id
		return "", false
	}
	chromaFormatIDC, err := r.readUE()
	if err != nil {
		return "", false
	}
	if chromaFormatIDC == 3 {
		if _, err = r.readBit(); err != nil { // separate_colour_plane_flag
			return "", false
		}
	}
	if _, err = r.readUE(); err != nil { // pic_width_in_luma_samples
		return "", false
	}
	if _, err = r.readUE(); err != nil { // pic_height_in_luma_samples
		return "", false
	}
	confWindow, err := r.readBit()
	if err != nil {
		return "", false
	}
	if confWindow == 1 {
		for range 4 { // conf_win_left/right/top/bottom_offset
			if _, err = r.readUE(); err != nil {
				return "", false
			}
		}
	}
	if _, err = r.readUE(); err != nil { // bit_depth_luma_minus8
		return "", false
	}
	if _, err = r.readUE(); err != nil { // bit_depth_chroma_minus8
		return "", false
	}
	log2MaxPocLsbMinus4, err := r.readUE()
	if err != nil {
		return "", false
	}

	orderingInfoPresent, err := r.readBit()
	if err != nil {
		return "", false
	}
	start := 0
	if orderingInfoPresent == 0 {
		start = maxSubLayersMinus1
	}
	for i := start; i <= maxSubLayersMinus1; i++ {
		if _, err = r.readUE(); err != nil { // sps_max_dec_pic_buffering_minus1[i]
			return "", false
		}
		if _, err = r.readUE(); err != nil { // sps_max_num_reorder_pics[i]
			return "", false
		}
		if _, err = r.readUE(); err != nil { // sps_max_latency_increase_plus1[i]
			return "", false
		}
	}

	if _, err = r.readUE(); err != nil { // log2_min_luma_coding_block_size_minus3
		return "", false
	}
	if _, err = r.readUE(); err != nil { // log2_diff_max_min_luma_coding_block_size
		return "", false
	}
	if _, err = r.readUE(); err != nil { // log2_min_luma_transform_block_size_minus2
		return "", false
	}
	if _, err = r.readUE(); err != nil { // log2_diff_max_min_luma_transform_block_size
		return "", false
	}
	if _, err = r.readUE(); err != nil { // max_transform_hierarchy_depth_inter
		return "", false
	}
	if _, err = r.readUE(); err != nil { // max_transform_hierarchy_depth_intra
		return "", false
	}
	scalingListEnabled, err := r.readBit()
	if err != nil {
		return "", false
	}
	if scalingListEnabled == 1 {
		return "", false // conservative: fall back to byte comparison
	}
	if _, err = r.readBit(); err != nil { // amp_enabled_flag
		return "", false
	}
	if _, err = r.readBit(); err != nil { // sample_adaptive_offset_enabled_flag
		return "", false
	}
	pcmEnabled, err := r.readBit()
	if err != nil {
		return "", false
	}
	if pcmEnabled == 1 {
		if _, err = r.readBits(4); err != nil { // pcm_sample_bit_depth_luma_minus1
			return "", false
		}
		if _, err = r.readBits(4); err != nil { // pcm_sample_bit_depth_chroma_minus1
			return "", false
		}
		if _, err = r.readUE(); err != nil { // log2_min_pcm_luma_coding_block_size_minus3
			return "", false
		}
		if _, err = r.readUE(); err != nil { // log2_diff_max_min_pcm_luma_coding_block_size
			return "", false
		}
		if _, err = r.readBit(); err != nil { // pcm_loop_filter_disabled_flag
			return "", false
		}
	}

	numSTRefPicSets, err := r.readUE()
	if err != nil {
		return "", false
	}
	numDeltaPocs := make([]int, numSTRefPicSets)
	for idx := range numSTRefPicSets {
		interPred := 0
		if idx != 0 {
			if interPred, err = r.readBit(); err != nil {
				return "", false
			}
		}
		if interPred == 1 {
			// In an SPS (as opposed to a slice header) RefRpsIdx is always
			// idx-1: the delta_idx_minus1 field only exists when
			// idx == num_short_term_ref_pic_sets, which cannot happen here.
			if _, err = r.readBit(); err != nil { // delta_rps_sign
				return "", false
			}
			if _, err = r.readUE(); err != nil { // abs_delta_rps_minus1
				return "", false
			}
			count := 0
			for range numDeltaPocs[idx-1] + 1 {
				used, err := r.readBit()
				if err != nil {
					return "", false
				}
				if used == 1 {
					count++
					continue
				}
				useDelta, err := r.readBit()
				if err != nil {
					return "", false
				}
				if useDelta == 1 {
					count++
				}
			}
			numDeltaPocs[idx] = count
		} else {
			numNeg, err := r.readUE()
			if err != nil {
				return "", false
			}
			numPos, err := r.readUE()
			if err != nil {
				return "", false
			}
			for range numNeg {
				if _, err = r.readUE(); err != nil { // delta_poc_s0_minus1[i]
					return "", false
				}
				if _, err = r.readBit(); err != nil { // used_by_curr_pic_s0_flag[i]
					return "", false
				}
			}
			for range numPos {
				if _, err = r.readUE(); err != nil { // delta_poc_s1_minus1[i]
					return "", false
				}
				if _, err = r.readBit(); err != nil { // used_by_curr_pic_s1_flag[i]
					return "", false
				}
			}
			numDeltaPocs[idx] = numNeg + numPos
		}
	}

	longTermPresent, err := r.readBit()
	if err != nil {
		return "", false
	}
	if longTermPresent == 1 {
		numLongTerm, err := r.readUE()
		if err != nil {
			return "", false
		}
		ltLsbBits := log2MaxPocLsbMinus4 + 4
		for range numLongTerm {
			if _, err = r.readBits(ltLsbBits); err != nil { // lt_ref_pic_poc_lsb_sps[i]
				return "", false
			}
			if _, err = r.readBit(); err != nil { // used_by_curr_pic_lt_sps_flag[i]
				return "", false
			}
		}
	}
	if _, err = r.readBit(); err != nil { // sps_temporal_mvp_enabled_flag
		return "", false
	}
	if _, err = r.readBit(); err != nil { // strong_intra_smoothing_enabled_flag
		return "", false
	}
	// vui_parameters_present_flag and everything after are excluded.
	return prefixKey(0x1A, rbsp, r.offset), true
}
