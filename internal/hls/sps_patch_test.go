package hls

import (
	"bytes"
	"fmt"
	"testing"

	h264codec "github.com/bluenviron/mediacommon/v2/pkg/codecs/h264"
	h265codec "github.com/bluenviron/mediacommon/v2/pkg/codecs/h265"
	"github.com/bluenviron/mediacommon/v2/pkg/bits"
)

// Real SPS test data from mediacommon library with VUI but NO video_signal_type.
// High Profile (100), Level 1.2, 352x288, VUI with timing info only.
var spsHighProfileNoVideoSignal = []byte{
	0x67, 0x64, 0x00, 0x0c, 0xac, 0x3b, 0x50, 0xb0,
	0x4b, 0x42, 0x00, 0x00, 0x03, 0x00, 0x02, 0x00,
	0x00, 0x03, 0x00, 0x3d, 0x08,
}

// Baseline Profile (66), Level 4.0, 1920x1080, VUI with timing info but no video_signal_type.
var spsBaselineNoVideoSignal = []byte{
	0x67, 0x42, 0xc0, 0x28, 0xd9, 0x00, 0x78, 0x02,
	0x27, 0xe5, 0x84, 0x00, 0x00, 0x03, 0x00, 0x04,
	0x00, 0x00, 0x03, 0x00, 0xf0, 0x3c, 0x60, 0xc9, 0x20,
}

// Hikvision SPS: High Profile, VUI with timing info but no video_signal_type.
var spsHikvisionNoVideoSignal = []byte{103, 100, 0, 32, 172, 23, 42, 1, 64, 30, 104, 64, 0, 1, 194, 0, 0, 87, 228, 33}

// SPS that ALREADY has video_signal_type (should not be patched).
var spsAlreadyHasVideoSignal = []byte{
	0x67, 0x64, 0x00, 0x1f, 0xac, 0xd9, 0x40, 0x50,
	0x05, 0xbb, 0x01, 0x6c, 0x80, 0x00, 0x00, 0x03,
	0x00, 0x80, 0x00, 0x00, 0x1e, 0x07, 0x8c, 0x18,
	0xcb,
}

// SPS with HRD + video_signal_type (Main Profile, Level 4.1).
var spsHikvisionWithHRD = []byte{
	103, 77, 0, 41, 154, 100, 3, 192,
	17, 63, 46, 2, 220, 4, 4, 5,
	0, 0, 3, 3, 232, 0, 0, 195,
	80, 232, 96, 0, 186, 180, 0, 2,
	234, 196, 187, 203, 141, 12, 0, 23,
	86, 128, 0, 93, 88, 151, 121, 112,
	160,
}

// SPS with scaling list data.
var spsWithScalingMatrix = []byte{
	103, 100, 0, 50, 173, 132, 1, 12, 32, 8, 97, 0, 67, 8, 2,
	24, 64, 16, 194, 0, 132, 59, 80, 20, 0, 90, 211,
	112, 16, 16, 20, 0, 0, 3, 0, 4, 0, 0, 3, 0, 162, 16,
}

func TestPatchSPSColorRange_HighProfileNoVideoSignal(t *testing.T) {
	var origSPS h264codec.SPS
	if err := origSPS.Unmarshal(spsHighProfileNoVideoSignal); err != nil {
		t.Fatalf("failed to parse original SPS: %v", err)
	}
	if origSPS.VUI == nil {
		t.Fatal("expected VUI to be present")
	}
	if origSPS.VUI.VideoSignalTypePresentFlag {
		t.Fatal("expected no video_signal_type in original")
	}

	patched := PatchSPSColorRange(spsHighProfileNoVideoSignal)
	if bytes.Equal(patched, spsHighProfileNoVideoSignal) {
		t.Fatal("patched SPS should differ from original")
	}

	var patchedSPS h264codec.SPS
	if err := patchedSPS.Unmarshal(patched); err != nil {
		t.Fatalf("failed to parse patched SPS: %v", err)
	}

	if !patchedSPS.VUI.VideoSignalTypePresentFlag {
		t.Fatal("patched SPS should have video_signal_type_present_flag=true")
	}
	if patchedSPS.VUI.VideoFullRangeFlag {
		t.Fatal("expected VideoFullRangeFlag=false (limited range)")
	}
	if patchedSPS.VUI.ColourPrimaries != 1 || patchedSPS.VUI.TransferCharacteristics != 1 || patchedSPS.VUI.MatrixCoefficients != 1 {
		t.Fatal("expected BT.709 color description")
	}

	// Verify fields preserved
	if patchedSPS.ProfileIdc != origSPS.ProfileIdc || patchedSPS.LevelIdc != origSPS.LevelIdc {
		t.Fatal("profile/level mismatch")
	}
	if patchedSPS.Width() != origSPS.Width() || patchedSPS.Height() != origSPS.Height() {
		t.Fatal("resolution mismatch")
	}
	if origSPS.VUI.TimingInfo != nil && patchedSPS.VUI.TimingInfo == nil {
		t.Fatal("timing info lost after patching")
	}
}

func TestPatchSPSColorRange_BaselineNoVideoSignal(t *testing.T) {
	var origSPS h264codec.SPS
	if err := origSPS.Unmarshal(spsBaselineNoVideoSignal); err != nil {
		t.Fatalf("failed to parse: %v", err)
	}
	if origSPS.VUI == nil || origSPS.VUI.VideoSignalTypePresentFlag {
		t.Fatal("expected VUI without video_signal_type")
	}

	patched := PatchSPSColorRange(spsBaselineNoVideoSignal)
	if bytes.Equal(patched, spsBaselineNoVideoSignal) {
		t.Fatal("should differ")
	}

	var patchedSPS h264codec.SPS
	if err := patchedSPS.Unmarshal(patched); err != nil {
		t.Fatalf("failed to parse patched: %v", err)
	}
	if !patchedSPS.VUI.VideoSignalTypePresentFlag {
		t.Fatal("expected video_signal_type after patch")
	}
	if patchedSPS.Width() != origSPS.Width() || patchedSPS.Height() != origSPS.Height() {
		t.Fatal("resolution mismatch")
	}
}

func TestPatchSPSColorRange_HikvisionNoVideoSignal(t *testing.T) {
	var origSPS h264codec.SPS
	if err := origSPS.Unmarshal(spsHikvisionNoVideoSignal); err != nil {
		t.Fatalf("failed to parse: %v", err)
	}
	if origSPS.VUI == nil || origSPS.VUI.VideoSignalTypePresentFlag {
		t.Fatal("expected VUI without video_signal_type")
	}

	patched := PatchSPSColorRange(spsHikvisionNoVideoSignal)
	var patchedSPS h264codec.SPS
	if err := patchedSPS.Unmarshal(patched); err != nil {
		t.Fatalf("failed to parse patched: %v", err)
	}
	if !patchedSPS.VUI.VideoSignalTypePresentFlag {
		t.Fatal("expected video_signal_type")
	}
	if patchedSPS.Width() != origSPS.Width() || patchedSPS.Height() != origSPS.Height() {
		t.Fatal("resolution mismatch")
	}
}

func TestPatchSPSColorRange_Idempotent(t *testing.T) {
	patched1 := PatchSPSColorRange(spsHighProfileNoVideoSignal)
	patched2 := PatchSPSColorRange(patched1)

	if !bytes.Equal(patched1, patched2) {
		t.Fatal("double-patching should return unchanged SPS")
	}

	var sps h264codec.SPS
	if err := sps.Unmarshal(patched2); err != nil {
		t.Fatalf("double-patched SPS should still parse: %v", err)
	}
	if !sps.VUI.VideoSignalTypePresentFlag {
		t.Fatal("double-patched SPS should still have video_signal_type")
	}
}

func TestPatchSPSColorRange_AlreadyHasVideoSignal(t *testing.T) {
	result := PatchSPSColorRange(spsAlreadyHasVideoSignal)
	if !bytes.Equal(result, spsAlreadyHasVideoSignal) {
		t.Fatal("SPS with existing video_signal_type should be returned unchanged")
	}

	result2 := PatchSPSColorRange(spsHikvisionWithHRD)
	if !bytes.Equal(result2, spsHikvisionWithHRD) {
		t.Fatal("SPS with HRD + video_signal should be returned unchanged")
	}
}

func TestPatchSPSColorRange_ScalingMatrix(t *testing.T) {
	var origSPS h264codec.SPS
	if err := origSPS.Unmarshal(spsWithScalingMatrix); err != nil {
		t.Fatalf("failed to parse: %v", err)
	}

	patched := PatchSPSColorRange(spsWithScalingMatrix)
	var patchedSPS h264codec.SPS
	if err := patchedSPS.Unmarshal(patched); err != nil {
		t.Fatalf("failed to parse patched: %v", err)
	}

	if patchedSPS.Width() != origSPS.Width() || patchedSPS.Height() != origSPS.Height() {
		t.Fatal("resolution mismatch after scaling matrix SPS patch")
	}
	if patchedSPS.FPS() != origSPS.FPS() {
		t.Fatal("FPS mismatch after scaling matrix SPS patch")
	}
}

func TestPatchSPSColorRange_NoVUI(t *testing.T) {
	// Build a minimal SPS with no VUI
	buf := make([]byte, 256)
	pos := 0
	buf[0] = 0x67 // NAL header
	buf[1] = 66   // profile_idc = Baseline
	buf[2] = 0xC0 // constraint_set0_flag=1
	buf[3] = 30    // level_idc = 3.0
	pos = 32

	writeGolombUnsigned(buf, &pos, 0) // sps_id
	writeGolombUnsigned(buf, &pos, 0) // log2_max_frame_num_minus4
	writeGolombUnsigned(buf, &pos, 2) // pic_order_cnt_type
	writeGolombUnsigned(buf, &pos, 1) // max_num_ref_frames
	bits.WriteFlagUnsafe(buf, &pos, false) // gaps
	writeGolombUnsigned(buf, &pos, 79) // pic_width (1280)
	writeGolombUnsigned(buf, &pos, 44) // pic_height (720)
	bits.WriteFlagUnsafe(buf, &pos, true)  // frame_mbs_only
	bits.WriteFlagUnsafe(buf, &pos, false) // direct_8x8
	bits.WriteFlagUnsafe(buf, &pos, false) // frame_cropping
	bits.WriteFlagUnsafe(buf, &pos, false) // vui_parameters_present
	writeRBSPTrailingBits(buf, &pos)

	rbspBytes := buf[1 : (pos+7)/8]
	rbspBytes = emulationPreventionAdd(rbspBytes)
	spsNoVUI := make([]byte, 1+len(rbspBytes))
	spsNoVUI[0] = 0x67
	copy(spsNoVUI[1:], rbspBytes)

	var origSPS h264codec.SPS
	if err := origSPS.Unmarshal(spsNoVUI); err != nil {
		t.Fatalf("failed to parse no-VUI SPS: %v", err)
	}
	if origSPS.VUI != nil {
		t.Fatal("expected VUI to be nil")
	}

	patched := PatchSPSColorRange(spsNoVUI)
	if bytes.Equal(patched, spsNoVUI) {
		t.Fatal("should differ")
	}

	var patchedSPS h264codec.SPS
	if err := patchedSPS.Unmarshal(patched); err != nil {
		t.Fatalf("failed to parse patched: %v", err)
	}
	if patchedSPS.VUI == nil || !patchedSPS.VUI.VideoSignalTypePresentFlag {
		t.Fatal("patched SPS should have VUI with video_signal_type")
	}
}

func TestPatchSPSColorRange_ShortInput(t *testing.T) {
	short := []byte{0x27, 0x4d}
	result := PatchSPSColorRange(short)
	if !bytes.Equal(result, short) {
		t.Fatal("short input should be returned unchanged")
	}
	result = PatchSPSColorRange(nil)
	if result != nil {
		t.Fatal("nil input should return nil")
	}
}

func TestPatchSPSColorRange_InvalidInput(t *testing.T) {
	invalid := []byte{0x27, 0xff, 0xff, 0xff, 0xff}
	result := PatchSPSColorRange(invalid)
	if !bytes.Equal(result, invalid) {
		t.Fatal("invalid input should be returned unchanged")
	}
}

func TestMarshalH264SPS_RoundTrip(t *testing.T) {
	for name, spsBytes := range map[string][]byte{
		"high_profile_no_signal":  spsHighProfileNoVideoSignal,
		"baseline_no_signal":     spsBaselineNoVideoSignal,
		"hikvision_no_signal":   spsHikvisionNoVideoSignal,
		"has_signal":            spsAlreadyHasVideoSignal,
		"with_hrd":             spsHikvisionWithHRD,
		"with_scaling_matrix":  spsWithScalingMatrix,
	} {
		t.Run(name, func(t *testing.T) {
			var orig h264codec.SPS
			if err := orig.Unmarshal(spsBytes); err != nil {
				t.Fatalf("failed to parse: %v", err)
			}

			marshaled := marshalH264SPS(&orig)
			if marshaled == nil {
				t.Fatal("marshal returned nil")
			}
			marshaled = emulationPreventionAdd(marshaled)
			fullNAL := make([]byte, 1+len(marshaled))
			fullNAL[0] = spsBytes[0]
			copy(fullNAL[1:], marshaled)

			var roundTrip h264codec.SPS
			if err := roundTrip.Unmarshal(fullNAL); err != nil {
				t.Fatalf("failed to parse marshaled SPS: %v", err)
			}

			if roundTrip.ProfileIdc != orig.ProfileIdc {
				t.Errorf("ProfileIdc mismatch: %d != %d", roundTrip.ProfileIdc, orig.ProfileIdc)
			}
			if roundTrip.LevelIdc != orig.LevelIdc {
				t.Errorf("LevelIdc mismatch: %d != %d", roundTrip.LevelIdc, orig.LevelIdc)
			}
			if roundTrip.ID != orig.ID {
				t.Errorf("ID mismatch: %d != %d", roundTrip.ID, orig.ID)
			}
			if roundTrip.Width() != orig.Width() {
				t.Errorf("Width mismatch: %d != %d", roundTrip.Width(), orig.Width())
			}
			if roundTrip.Height() != orig.Height() {
				t.Errorf("Height mismatch: %d != %d", roundTrip.Height(), orig.Height())
			}
			if roundTrip.FrameMbsOnlyFlag != orig.FrameMbsOnlyFlag {
				t.Errorf("FrameMbsOnlyFlag mismatch")
			}
			if roundTrip.MaxNumRefFrames != orig.MaxNumRefFrames {
				t.Errorf("MaxNumRefFrames mismatch")
			}
			if roundTrip.PicOrderCntType != orig.PicOrderCntType {
				t.Errorf("PicOrderCntType mismatch")
			}
			if roundTrip.FPS() != orig.FPS() {
				t.Errorf("FPS mismatch: %f != %f", roundTrip.FPS(), orig.FPS())
				}

			// Verify VUI round-trip
			if orig.VUI != nil {
				if roundTrip.VUI == nil {
					t.Fatal("VUI lost after round-trip")
				}
				if roundTrip.VUI.AspectRatioInfoPresentFlag != orig.VUI.AspectRatioInfoPresentFlag {
					t.Errorf("AspectRatioInfoPresentFlag mismatch")
				}
				if orig.VUI.AspectRatioInfoPresentFlag && roundTrip.VUI.AspectRatioIdc != orig.VUI.AspectRatioIdc {
					t.Errorf("AspectRatioIdc mismatch")
				}
				if orig.VUI.TimingInfo != nil {
					if roundTrip.VUI.TimingInfo == nil {
						t.Fatal("TimingInfo lost")
					}
					if roundTrip.VUI.TimingInfo.TimeScale != orig.VUI.TimingInfo.TimeScale {
						t.Errorf("TimeScale mismatch")
					}
					if roundTrip.VUI.TimingInfo.NumUnitsInTick != orig.VUI.TimingInfo.NumUnitsInTick {
						t.Errorf("NumUnitsInTick mismatch")
					}
				}
				if orig.VUI.NalHRD != nil {
					if roundTrip.VUI.NalHRD == nil {
						t.Fatal("NalHRD lost")
					}
					if roundTrip.VUI.NalHRD.CpbCntMinus1 != orig.VUI.NalHRD.CpbCntMinus1 {
						t.Errorf("NalHRD CpbCntMinus1 mismatch")
					}
				}
				if orig.VUI.VclHRD != nil {
					if roundTrip.VUI.VclHRD == nil {
						t.Fatal("VclHRD lost")
					}
				}
				if orig.VUI.BitstreamRestriction != nil {
					if roundTrip.VUI.BitstreamRestriction == nil {
						t.Fatal("BitstreamRestriction lost")
					}
				}
			}
		})
	}
}

func TestPatchSPSColorRange_PreservesFPS(t *testing.T) {
	for name, spsBytes := range map[string][]byte{
		"high_profile": spsHighProfileNoVideoSignal,
		"hikvision":   spsHikvisionNoVideoSignal,
	} {
		t.Run(name, func(t *testing.T) {
			var orig h264codec.SPS
			if err := orig.Unmarshal(spsBytes); err != nil {
				t.Fatalf("failed to parse: %v", err)
			}
			origFPS := orig.FPS()

			patched := PatchSPSColorRange(spsBytes)
			var patchedSPS h264codec.SPS
			if err := patchedSPS.Unmarshal(patched); err != nil {
				t.Fatalf("failed to parse patched: %v", err)
			}
			patchedFPS := patchedSPS.FPS()

			if origFPS != patchedFPS {
				t.Errorf("FPS changed after patching: %f -> %f", origFPS, patchedFPS)
			}
		})
	}
}

// --- H.265 tests ---

func TestPatchSPSH265ColorRange_Basic(t *testing.T) {
	buf := make([]byte, 256)
	pos := 0
	buf[0] = 0x42
	buf[1] = 0x01
	pos = 16

	bits.WriteBitsUnsafe(buf, &pos, 0, 4) // vps_id
	bits.WriteBitsUnsafe(buf, &pos, 0, 3) // max_sub_layers_minus1
	bits.WriteFlagUnsafe(buf, &pos, true)  // temporal_id_nesting

	ptl := &h265codec.SPS_ProfileTierLevel{
		GeneralProfileIdc:                1,
		GeneralLevelIdc:                   93,
		GeneralProgressiveSourceFlag:      true,
		GeneralFrameOnlyConstraintFlag:    true,
		GeneralMax10bitConstraintFlag:     true,
		GeneralMax8bitConstraintFlag:      true,
		GeneralMax420ChromaConstraintFlag: true,
	}
	marshalH265ProfileTierLevel(buf, &pos, ptl, 0)

	writeGolombUnsigned(buf, &pos, 0)  // sps_id
	writeGolombUnsigned(buf, &pos, 1)  // chroma_format_idc
	writeGolombUnsigned(buf, &pos, 1920) // pic_width
	writeGolombUnsigned(buf, &pos, 1080) // pic_height
	bits.WriteFlagUnsafe(buf, &pos, false) // no conformance window
	writeGolombUnsigned(buf, &pos, 0)  // bit_depth_luma_minus8
	writeGolombUnsigned(buf, &pos, 0)  // bit_depth_chroma_minus8
	writeGolombUnsigned(buf, &pos, 4)  // log2_max_poc_lsb_minus4
	bits.WriteFlagUnsafe(buf, &pos, true) // sub_layer_ordering
	writeGolombUnsigned(buf, &pos, 1)  // max_dec_pic_buffering
	writeGolombUnsigned(buf, &pos, 0)  // max_num_reorder
	writeGolombUnsigned(buf, &pos, 0)  // max_latency_increase
	writeGolombUnsigned(buf, &pos, 0)  // log2_min_coding_block_minus3
	writeGolombUnsigned(buf, &pos, 2)  // log2_diff_max_min_coding
	writeGolombUnsigned(buf, &pos, 0)  // log2_min_transform_minus2
	writeGolombUnsigned(buf, &pos, 3)  // log2_diff_max_min_transform
	writeGolombUnsigned(buf, &pos, 0)  // max_transform_depth_inter
	writeGolombUnsigned(buf, &pos, 0)  // max_transform_depth_intra
	bits.WriteFlagUnsafe(buf, &pos, false) // scaling_list_enabled
	bits.WriteFlagUnsafe(buf, &pos, false) // amp_enabled
	bits.WriteFlagUnsafe(buf, &pos, false) // sao_enabled
	bits.WriteFlagUnsafe(buf, &pos, false) // pcm_enabled
	writeGolombUnsigned(buf, &pos, 0)  // num_short_term_ref_pic_sets
	bits.WriteFlagUnsafe(buf, &pos, false) // long_term_ref_pics
	bits.WriteFlagUnsafe(buf, &pos, false) // temporal_mvp
	bits.WriteFlagUnsafe(buf, &pos, false) // strong_intra_smoothing
	bits.WriteFlagUnsafe(buf, &pos, false) // vui_parameters_present
	writeRBSPTrailingBits(buf, &pos)

	rbspBytes := buf[2 : (pos+7)/8]
	rbspBytes = emulationPreventionAdd(rbspBytes)
	h265sps := make([]byte, 2+len(rbspBytes))
	h265sps[0] = 0x42
	h265sps[1] = 0x01
	copy(h265sps[2:], rbspBytes)

	var origSPS h265codec.SPS
	if err := origSPS.Unmarshal(h265sps); err != nil {
		t.Fatalf("failed to parse H.265 no-VUI SPS: %v", err)
	}
	if origSPS.VUI != nil {
		t.Fatal("expected VUI to be nil")
	}

	patched := PatchSPSH265ColorRange(h265sps)
	if bytes.Equal(patched, h265sps) {
		t.Fatal("patched H.265 SPS should differ from original")
	}

	var patchedSPS h265codec.SPS
	if err := patchedSPS.Unmarshal(patched); err != nil {
		t.Fatalf("failed to parse patched H.265 SPS: %v", err)
	}
	if patchedSPS.VUI == nil || !patchedSPS.VUI.VideoSignalTypePresentFlag {
		t.Fatal("patched SPS should have VUI with video_signal_type")
	}
	if patchedSPS.VUI.ColourPrimaries != 1 {
		t.Fatalf("expected BT.709 primaries, got %d", patchedSPS.VUI.ColourPrimaries)
	}
 if patchedSPS.VUI.VideoFullRangeFlag {
		t.Fatal("expected limited range")
	}
	if patchedSPS.Width() != origSPS.Width() || patchedSPS.Height() != origSPS.Height() {
		t.Fatal("resolution mismatch")
	}
}

func TestPatchSPSH265ColorRange_Idempotent(t *testing.T) {
	buf := make([]byte, 256)
	pos := 0
	buf[0] = 0x42
	buf[1] = 0x01
	pos = 16

	bits.WriteBitsUnsafe(buf, &pos, 0, 4)
	bits.WriteBitsUnsafe(buf, &pos, 0, 3)
	bits.WriteFlagUnsafe(buf, &pos, true)

	ptl := &h265codec.SPS_ProfileTierLevel{
		GeneralProfileIdc:            1,
		GeneralLevelIdc:               93,
		GeneralProgressiveSourceFlag:  true,
		GeneralFrameOnlyConstraintFlag: true,
		GeneralMax10bitConstraintFlag: true,
		GeneralMax8bitConstraintFlag: true,
		GeneralMax420ChromaConstraintFlag: true,
	}
	marshalH265ProfileTierLevel(buf, &pos, ptl, 0)
	writeGolombUnsigned(buf, &pos, 0)
	writeGolombUnsigned(buf, &pos, 1)
	writeGolombUnsigned(buf, &pos, 64)
	writeGolombUnsigned(buf, &pos, 64)
	bits.WriteFlagUnsafe(buf, &pos, false)
	writeGolombUnsigned(buf, &pos, 0)
	writeGolombUnsigned(buf, &pos, 0)
	writeGolombUnsigned(buf, &pos, 4)
	bits.WriteFlagUnsafe(buf, &pos, true)
	writeGolombUnsigned(buf, &pos, 1)
	writeGolombUnsigned(buf, &pos, 0)
	writeGolombUnsigned(buf, &pos, 0)
	writeGolombUnsigned(buf, &pos, 0)
	writeGolombUnsigned(buf, &pos, 0)
	writeGolombUnsigned(buf, &pos, 2)
	writeGolombUnsigned(buf, &pos, 0)
	writeGolombUnsigned(buf, &pos, 3)
	writeGolombUnsigned(buf, &pos, 0)
	writeGolombUnsigned(buf, &pos, 0)
	bits.WriteFlagUnsafe(buf, &pos, false)
	bits.WriteFlagUnsafe(buf, &pos, false)
	bits.WriteFlagUnsafe(buf, &pos, false)
	bits.WriteFlagUnsafe(buf, &pos, false)
	writeGolombUnsigned(buf, &pos, 0)
	bits.WriteFlagUnsafe(buf, &pos, false)
	bits.WriteFlagUnsafe(buf, &pos, false)
	bits.WriteFlagUnsafe(buf, &pos, false)
	bits.WriteFlagUnsafe(buf, &pos, false)
	writeRBSPTrailingBits(buf, &pos)

	rbspBytes := buf[2 : (pos+7)/8]
	rbspBytes = emulationPreventionAdd(rbspBytes)
	h265sps := make([]byte, 2+len(rbspBytes))
	h265sps[0] = 0x42
	h265sps[1] = 0x01
	copy(h265sps[2:], rbspBytes)

	patched1 := PatchSPSH265ColorRange(h265sps)
	patched2 := PatchSPSH265ColorRange(patched1)

	if !bytes.Equal(patched1, patched2) {
		t.Fatal("double-patching H.265 SPS should return unchanged")
	}
}

// --- Helper function tests ---

func TestEmulationPreventionAdd(t *testing.T) {
	tests := []struct {
		name   string
		input  []byte
		output []byte
	}{
		{
			name:   "no prevention needed",
			input:  []byte{0x01, 0x02, 0x03},
			output: []byte{0x01, 0x02, 0x03},
		},
		{
			name:   "insert before 0x00",
			input:  []byte{0x00, 0x00, 0x00},
			output: []byte{0x00, 0x00, 0x03, 0x00},
		},
		{
			name:   "insert before 0x01",
			input:  []byte{0x00, 0x00, 0x01},
			output: []byte{0x00, 0x00, 0x03, 0x01},
		},
		{
			name:   "insert before 0x02",
			input:  []byte{0x00, 0x00, 0x02},
			output: []byte{0x00, 0x00, 0x03, 0x02},
		},
		{
			name:   "insert before 0x03",
			input:  []byte{0x00, 0x00, 0x03},
			output: []byte{0x00, 0x00, 0x03, 0x03},
		},
		{
			name:   "no insert before 0x04",
			input:  []byte{0x00, 0x00, 0x04},
			output: []byte{0x00, 0x00, 0x04},
		},
		{
			name:   "multiple insertions",
			input:  []byte{0x00, 0x00, 0x00, 0x00, 0x00, 0x01},
			output: []byte{0x00, 0x00, 0x03, 0x00, 0x00, 0x03, 0x00, 0x01},
		},
		{
			name:   "empty input",
			input:  []byte{},
			output: []byte{},
		},
		{
			name:   "single byte",
			input:  []byte{0x00},
			output: []byte{0x00},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := emulationPreventionAdd(tt.input)
			if !bytes.Equal(result, tt.output) {
				t.Errorf("expected %x, got %x", tt.output, result)
			}
		})
	}
}

func TestWriteGolombRoundTrip(t *testing.T) {
	unsignedVals := []uint32{0, 1, 2, 3, 4, 5, 10, 100, 255, 1023, 65535}
	for _, val := range unsignedVals {
		buf := make([]byte, 256) // fresh buffer per iteration
		pos := 0
		readPos := 0
		writeGolombUnsigned(buf, &pos, val)
		readVal, err := bits.ReadGolombUnsigned(buf[:pos/8+1], &readPos)
		if err != nil {
			t.Fatalf("failed to read golomb value %d: %v", val, err)
		}
		if readVal != val {
			t.Errorf("unsigned golomb roundtrip failed: wrote %d, read %d", val, readVal)
		}
	}

	signedVals := []int32{0, 1, -1, 2, -2, 3, -3, 10, -10, 100, -100}
	for _, val := range signedVals {
		buf := make([]byte, 256) // fresh buffer per iteration
		pos := 0
		readPos := 0
		writeGolombSigned(buf, &pos, val)
		readVal, err := bits.ReadGolombSigned(buf[:pos/8+1], &readPos)
		if err != nil {
			t.Fatalf("failed to read signed golomb value %d: %v", val, err)
		}
		if readVal != val {
			t.Errorf("signed golomb roundtrip failed: wrote %d, read %d", val, readVal)
		}
	}
}

func TestEmulationPreventionAdd_RoundTrip(t *testing.T) {
	// Verify that EP add followed by EP remove gives back the original
	inputs := [][]byte{
		{0x00, 0x01, 0x02, 0x03, 0x04, 0x05},
		{0x00, 0x00, 0x01},
		{0xFF, 0xFF, 0xFF},
		{0x00, 0x00, 0x00, 0x00, 0x00, 0x01, 0x02},
	}

	for i, input := range inputs {
		t.Run(fmt.Sprintf("roundtrip_%d", i), func(t *testing.T) {
			added := emulationPreventionAdd(input)
			removed := h264codec.EmulationPreventionRemove(added)
			if !bytes.Equal(removed, input) {
				t.Errorf("roundtrip failed: original=%x, added=%x, removed=%x",
					input, added, removed)
			}
		})
	}
}
