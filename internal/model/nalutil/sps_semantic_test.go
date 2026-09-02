package nalutil

import (
	"bytes"
	"testing"
)

// --- Test bit writer: builds SPS RBSP bitstreams field by field ---

type spsBits struct {
	buf []byte
	n   int // bits written
}

func (w *spsBits) u(val, width int) {
	for i := width - 1; i >= 0; i-- {
		bit := (val >> i) & 1
		byteIdx := w.n / 8
		if byteIdx >= len(w.buf) {
			w.buf = append(w.buf, 0)
		}
		if bit == 1 {
			w.buf[byteIdx] |= byte(1 << (7 - w.n%8))
		}
		w.n++
	}
}

func (w *spsBits) ue(v int) {
	// Exp-Golomb: codeNum v → leading zeros + info bits
	val := v + 1
	bits := 0
	for m := val; m > 1; m >>= 1 {
		bits++
	}
	w.u(0, bits)
	w.u(val, bits+1)
}

// trailing writes the rbsp_stop_one_bit plus zero padding to the byte boundary.
func (w *spsBits) trailing() {
	w.u(1, 1)
	if w.n%8 != 0 {
		w.u(0, 8-w.n%8)
	}
}

// insertEPB inserts H.264/H.265 emulation prevention bytes (0x03) into an RBSP
// so it becomes a valid NAL payload.
func insertEPB(rbsp []byte) []byte {
	var out []byte
	zeros := 0
	for _, b := range rbsp {
		if zeros >= 2 && b <= 3 {
			out = append(out, 0x03)
			zeros = 0
		}
		out = append(out, b)
		if b == 0 {
			zeros++
		} else {
			zeros = 0
		}
	}
	return out
}

// --- H.264 SPS builders (baseline profile, 640x352) ---

// buildH264SPS writes a baseline-profile H.264 SPS up to frame_cropping, then a
// caller-differentiable VUI. timingScale controls the VUI time_scale field —
// the classic encoder "decoration" difference that must NOT trigger rotation.
func buildH264SPS(t *testing.T, timingScale int, withVUI bool) []byte {
	t.Helper()
	w := &spsBits{}
	w.u(66, 8) // profile_idc = baseline
	w.u(0, 8)  // constraint_set flags
	w.u(30, 8) // level_idc
	w.ue(0)    // seq_parameter_set_id
	w.ue(0)    // log2_max_frame_num_minus4
	w.ue(0)    // pic_order_cnt_type = 0
	w.ue(0)    // log2_max_pic_order_cnt_lsb_minus4
	w.ue(1)    // max_num_ref_frames
	w.u(0, 1)  // gaps_in_frame_num_value_allowed_flag
	w.ue(39)   // pic_width_in_mbs_minus1  → 640
	w.ue(21)   // pic_height_in_map_units_minus1 → 352
	w.u(1, 1)  // frame_mbs_only_flag
	w.u(1, 1)  // direct_8x8_inference_flag
	w.u(0, 1)  // frame_cropping_flag
	if withVUI {
		w.u(1, 1)            // vui_parameters_present_flag
		w.u(0, 1)            // aspect_ratio_info_present_flag
		w.u(0, 1)            // overscan_info_present_flag
		w.u(0, 1)            // video_signal_type_present_flag
		w.u(0, 1)            // chroma_loc_info_present_flag
		w.u(1, 1)            // timing_info_present_flag
		w.u(1, 32)           // num_units_in_tick
		w.u(timingScale, 32) // time_scale  ← the variant difference
		w.u(1, 1)            // fixed_frame_rate_flag
		w.u(0, 1)            // nal_hrd_parameters_present_flag
		w.u(0, 1)            // vcl_hrd_parameters_present_flag
		w.u(0, 1)            // pic_struct_present_flag
		w.u(0, 1)            // bitstream_restriction_flag
	} else {
		w.u(0, 1) // vui_parameters_present_flag
	}
	w.trailing()
	return append([]byte{0x67}, insertEPB(w.buf)...)
}

// buildH264SPSWide is buildH264SPS with a real codec difference (1280 wide).
func buildH264SPSWide(t *testing.T) []byte {
	t.Helper()
	w := &spsBits{}
	w.u(66, 8)
	w.u(0, 8)
	w.u(30, 8)
	w.ue(0)
	w.ue(0)
	w.ue(0)
	w.ue(0)
	w.ue(1)
	w.u(0, 1)
	w.ue(79) // 1280 wide
	w.ue(21)
	w.u(1, 1)
	w.u(1, 1)
	w.u(0, 1)
	w.u(0, 1)
	w.trailing()
	return append([]byte{0x67}, insertEPB(w.buf)...)
}

// buildH264SPSHighProfile changes profile_idc to 77 (Main) — a real difference.
// Note: a true Main-profile SPS would carry the extra chroma/bit-depth syntax;
// for the key comparison the differing profile byte alone must produce a
// different key.
func buildH264SPSHighProfile(t *testing.T) []byte {
	t.Helper()
	w := &spsBits{}
	w.u(77, 8) // profile_idc = Main
	w.u(0, 8)
	w.u(30, 8)
	w.ue(0)
	w.ue(0)
	w.ue(0)
	w.ue(0)
	w.ue(1)
	w.u(0, 1)
	w.ue(39)
	w.ue(21)
	w.u(1, 1)
	w.u(1, 1)
	w.u(0, 1)
	w.u(0, 1)
	w.trailing()
	return append([]byte{0x67}, insertEPB(w.buf)...)
}

// --- H.265 SPS builders (Main profile, 640x352) ---

// buildH265SPS writes a minimal Main-profile HEVC SPS through
// strong_intra_smoothing, then a caller-differentiable VUI.
func buildH265SPS(t *testing.T, timingScale int, withVUI bool) []byte {
	t.Helper()
	w := &spsBits{}
	w.u(0, 4) // sps_video_parameter_set_id
	w.u(0, 3) // sps_max_sub_layers_minus1
	w.u(1, 1) // sps_temporal_id_nesting_flag
	// profile_tier_level (general only, no sub-layers)
	w.u(0, 2)           // general_profile_space
	w.u(0, 1)           // general_tier_flag
	w.u(1, 5)           // general_profile_idc = Main
	w.u(0x60000000, 32) // general_profile_compatibility_flag[32]
	w.u(0, 48)          // general constraint indicator flags + progressive etc.
	w.u(120, 8)         // general_level_idc
	w.ue(0)             // sps_seq_parameter_set_id
	w.ue(1)             // chroma_format_idc = 4:2:0
	w.ue(640)           // pic_width_in_luma_samples
	w.ue(352)           // pic_height_in_luma_samples
	w.u(0, 1)           // conformance_window_flag
	w.ue(0)             // bit_depth_luma_minus8
	w.ue(0)             // bit_depth_chroma_minus8
	w.ue(4)             // log2_max_pic_order_cnt_lsb_minus4
	w.u(1, 1)           // sps_sub_layer_ordering_info_present_flag
	w.ue(1)             // sps_max_dec_pic_buffering_minus1[0]
	w.ue(0)             // sps_max_num_reorder_pics[0]
	w.ue(0)             // sps_max_latency_increase_plus1[0]
	w.ue(0)             // log2_min_luma_coding_block_size_minus3
	w.ue(2)             // log2_diff_max_min_luma_coding_block_size
	w.ue(0)             // log2_min_luma_transform_block_size_minus2
	w.ue(3)             // log2_diff_max_min_luma_transform_block_size
	w.ue(0)             // max_transform_hierarchy_depth_inter
	w.ue(0)             // max_transform_hierarchy_depth_intra
	w.u(0, 1)           // scaling_list_enabled_flag
	w.u(0, 1)           // amp_enabled_flag
	w.u(1, 1)           // sample_adaptive_offset_enabled_flag
	w.u(0, 1)           // pcm_enabled_flag
	w.ue(0)             // num_short_term_ref_pic_sets
	w.u(0, 1)           // long_term_ref_pics_present_flag
	w.u(0, 1)           // sps_temporal_mvp_enabled_flag
	w.u(0, 1)           // strong_intra_smoothing_enabled_flag
	if withVUI {
		w.u(1, 1)            // vui_parameters_present_flag
		w.u(0, 1)            // aspect_ratio_info_present_flag
		w.u(0, 1)            // interlace_conformance... (overscan)
		w.u(0, 1)            // video_signal_type_present_flag
		w.u(0, 1)            // chroma_loc_info_present_flag
		w.u(1, 1)            // timing_info_present_flag
		w.u(1, 32)           // num_units_in_tick
		w.u(timingScale, 32) // time_scale ← the variant difference
		w.u(1, 1)            // poc_proportional_to_timing_flag
		w.u(1, 32)           // num_ticks_poc_diff_one_minus1
		w.u(0, 1)            // hrd_parameters_present_flag
		w.u(0, 1)            // bitstream_restriction_flag
	} else {
		w.u(0, 1)
	}
	w.trailing()
	return append([]byte{0x42, 0x01}, insertEPB(w.buf)...)
}

// --- Real-camera SPS fixtures (from internal/merge/sps_test.go) ---

var realH264SPS640 = []byte{
	0x67, 0x42, 0xc0, 0x1e, 0xd9, 0x00, 0xa0, 0x47, 0xfe, 0xc8,
}

var realH264SPS720 = []byte{
	0x67, 0x42, 0xc0, 0x1e, 0xf4, 0x02, 0x80, 0x2d, 0x80,
}

var realH264SPS1080 = []byte{
	0x67, 0x42, 0xc0, 0x28, 0xf4, 0x03, 0xc0, 0x11, 0x2f, 0x28,
}

var realH265SPS1080 = []byte{
	0x42, 0x01, 0x01, 0x01, 0x60, 0x00, 0x00, 0x03, 0x00, 0x90,
	0x00, 0x00, 0x03, 0x00, 0x00, 0x03, 0x00, 0x78, 0xa0, 0x03,
	0xc0, 0x80, 0x10, 0xe5, 0x96, 0x66, 0x69, 0x24, 0xca, 0xe0,
	0x10, 0x00, 0x00, 0x03, 0x00, 0x10, 0x00, 0x00, 0x03, 0x01,
	0xe0, 0x80,
}

// --- Tests ---

// The production case from #642: a camera alternates between two SPS encodings
// that differ only in VUI timing (time_scale) — 188 rotations in 4.9h. These
// must compare as semantically equal so the recorder keeps its canonical SPS.
func TestSPSSemanticallyEqual_H264_VUITimingVariants(t *testing.T) {
	a := buildH264SPS(t, 50, true)
	b := buildH264SPS(t, 60, true)
	noVUI := buildH264SPS(t, 0, false)

	if bytes.Equal(a, b) {
		t.Fatal("test fixture degenerate: variants must differ at byte level")
	}
	if !SPSSemanticallyEqual(a, b, false) {
		t.Fatal("SPS variants differing only in VUI time_scale must be semantically equal")
	}
	if !SPSSemanticallyEqual(a, noVUI, false) {
		t.Fatal("SPS with and without VUI must be semantically equal")
	}
	if !SPSSemanticallyEqual(a, a, false) {
		t.Fatal("identical SPS must be semantically equal")
	}
}

func TestSPSSemanticallyEqual_H264_RealChanges(t *testing.T) {
	base := buildH264SPS(t, 50, true)
	if SPSSemanticallyEqual(base, buildH264SPSWide(t), false) {
		t.Fatal("resolution change must NOT be semantically equal")
	}
	if SPSSemanticallyEqual(base, buildH264SPSHighProfile(t), false) {
		t.Fatal("profile change must NOT be semantically equal")
	}
}

// Real camera SPS from muxer fixtures: parse must succeed (ok=true) and
// distinct resolutions must yield distinct keys.
func TestSPSSemanticKey_H264_RealFixtures(t *testing.T) {
	k640, ok640 := SPSSemanticKey(realH264SPS640, false)
	k720, ok720 := SPSSemanticKey(realH264SPS720, false)
	k1080, ok1080 := SPSSemanticKey(realH264SPS1080, false)
	if !ok640 || !ok720 || !ok1080 {
		t.Fatalf("real H.264 SPS must parse: ok640=%v ok720=%v ok1080=%v", ok640, ok720, ok1080)
	}
	if k640 == k720 || k720 == k1080 || k640 == k1080 {
		t.Fatal("distinct resolutions must produce distinct semantic keys")
	}
}

// Malformed/truncated SPS must fall back to byte comparison: byte-identical →
// equal, byte-different → not equal (rotate) — never a false "equal".
func TestSPSSemanticallyEqual_H264_UnparseableFallsBackToBytes(t *testing.T) {
	good := realH264SPS1080
	trunc := good[:6] // too short to parse
	trunc2 := append([]byte(nil), trunc...)
	trunc2[len(trunc2)-1] ^= 0xFF

	if !SPSSemanticallyEqual(trunc, trunc, false) {
		t.Fatal("byte-identical unparseable SPS must be equal")
	}
	if SPSSemanticallyEqual(trunc, trunc2, false) {
		t.Fatal("byte-different unparseable SPS must not be equal (conservative rotate)")
	}
	// Parseable vs unparseable: byte-different → not equal.
	if SPSSemanticallyEqual(good, trunc, false) {
		t.Fatal("parseable vs truncated must not be equal")
	}
}

func TestSPSSemanticallyEqual_H265_VUITimingVariants(t *testing.T) {
	a := buildH265SPS(t, 50, true)
	b := buildH265SPS(t, 60, true)
	noVUI := buildH265SPS(t, 0, false)

	if bytes.Equal(a, b) {
		t.Fatal("test fixture degenerate: variants must differ at byte level")
	}
	if !SPSSemanticallyEqual(a, b, true) {
		t.Fatal("H.265 SPS variants differing only in VUI timing must be semantically equal")
	}
	if !SPSSemanticallyEqual(a, noVUI, true) {
		t.Fatal("H.265 SPS with and without VUI must be semantically equal")
	}
}

func TestSPSSemanticallyEqual_H265_RealChanges(t *testing.T) {
	base := buildH265SPS(t, 50, true)

	wide := &spsBits{}
	wide.u(0, 4)
	wide.u(0, 3)
	wide.u(1, 1)
	wide.u(0, 2)
	wide.u(0, 1)
	wide.u(1, 5)
	wide.u(0x60000000, 32)
	wide.u(0, 48)
	wide.u(120, 8)
	wide.ue(0)
	wide.ue(1)
	wide.ue(1280) // different width
	wide.ue(720)
	wide.u(0, 1)
	wide.ue(0)
	wide.ue(0)
	wide.ue(4)
	wide.u(1, 1)
	wide.ue(1)
	wide.ue(0)
	wide.ue(0)
	wide.ue(0)
	wide.ue(2)
	wide.ue(0)
	wide.ue(3)
	wide.ue(0)
	wide.ue(0)
	wide.u(0, 1)
	wide.u(0, 1)
	wide.u(1, 1)
	wide.u(0, 1)
	wide.ue(0)
	wide.u(0, 1)
	wide.u(0, 1)
	wide.u(0, 1)
	wide.u(0, 1)
	wide.trailing()
	wideNAL := append([]byte{0x42, 0x01}, insertEPB(wide.buf)...)

	if SPSSemanticallyEqual(base, wideNAL, true) {
		t.Fatal("H.265 resolution change must NOT be semantically equal")
	}
}

// Real 1080p HEVC SPS (contains emulation prevention bytes) must parse.
func TestSPSSemanticKey_H265_RealFixture(t *testing.T) {
	k, ok := SPSSemanticKey(realH265SPS1080, true)
	if !ok {
		t.Fatal("real HEVC SPS must parse to a semantic key")
	}
	if _, ok2 := SPSSemanticKey(realH265SPS1080, true); !ok2 || k == "" {
		t.Fatal("key must be stable and non-empty")
	}
}
