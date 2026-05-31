package hls

import (
	"fmt"
	"log/slog"

	h264codec "github.com/bluenviron/mediacommon/v2/pkg/codecs/h264"
	h265codec "github.com/bluenviron/mediacommon/v2/pkg/codecs/h265"
	"github.com/bluenviron/mediacommon/v2/pkg/bits"
)

var spsPatchLogger = slog.Default().With("component", "sps-patcher")

// ============================================================
// Bitstream writing helpers
// ============================================================

// writeGolombUnsigned writes an unsigned Exp-Golomb coded value.
// Encoding: val+1 in binary, prefixed with (M) zeros where M = floor(log2(val+1)).
// Total bits: 2*M+1.
func writeGolombUnsigned(buf []byte, pos *int, val uint32) {
	val1 := val + 1
	numBits := 0
	tmp := val1
	for tmp > 0 {
		tmp >>= 1
		numBits++
	}
	bits.WriteBitsUnsafe(buf, pos, uint64(val1), 2*numBits-1)
}

// writeGolombSigned writes a signed Exp-Golomb coded value.
// Encoding: positive n → codeNum 2n-1, negative n → codeNum -2n.
func writeGolombSigned(buf []byte, pos *int, val int32) {
	if val > 0 {
		writeGolombUnsigned(buf, pos, uint32(2*val-1))
	} else if val < 0 {
		writeGolombUnsigned(buf, pos, uint32(-2*val))
	} else {
		writeGolombUnsigned(buf, pos, 0)
	}
}

// emulationPreventionAdd inserts 0x03 emulation prevention bytes.
// Scans for 0x00 0x00 followed by 0x00/0x01/0x02/0x03 and inserts 0x03 between them.
func emulationPreventionAdd(nalu []byte) []byte {
	if len(nalu) == 0 {
		return nalu
	}
	out := make([]byte, 0, len(nalu)+len(nalu)/32)
	for i := 0; i < len(nalu); i++ {
		if i >= 2 && out[len(out)-2] == 0x00 && out[len(out)-1] == 0x00 && nalu[i] <= 0x03 {
			out = append(out, 0x03)
		}
		out = append(out, nalu[i])
	}
	return out
}

// writeRBSPTrailingBits writes RBSP stop bit (1) followed by zero padding to byte-align.
func writeRBSPTrailingBits(buf []byte, pos *int) {
	bits.WriteBitsUnsafe(buf, pos, 1, 1)
	remainder := *pos % 8
	if remainder != 0 {
		bits.WriteBitsUnsafe(buf, pos, 0, 8-remainder)
	}
}

// ============================================================
// H.264 SPS marshaling
// ============================================================

// isHighProfile returns true for H.264 profiles that have extended SPS fields.
func isHighProfile(profileIdc uint8) bool {
	switch profileIdc {
	case 100, 110, 122, 244, 44, 83, 86, 118, 128, 138, 139, 134, 135:
		return true
	}
	return false
}

// writeH264ScalingList writes a single H.264 scaling list back to the bitstream.
func writeH264ScalingList(buf []byte, pos *int, scalingList []int32) {
	lastScale := int32(8)
	nextScale := int32(8)
	for j := range scalingList {
		if nextScale != 0 {
			deltaScale := scalingList[j] - lastScale
			writeGolombSigned(buf, pos, deltaScale)
			nextScale = (lastScale + deltaScale + 256) % 256
		}
		lastScale = scalingList[j]
	}
}

// marshalH264HRD marshals an H.264 SPS HRD parameters structure.
func marshalH264HRD(buf []byte, pos *int, h *h264codec.SPS_HRD) {
	writeGolombUnsigned(buf, pos, h.CpbCntMinus1)
	bits.WriteBitsUnsafe(buf, pos, uint64(h.BitRateScale), 4)
	bits.WriteBitsUnsafe(buf, pos, uint64(h.CpbSizeScale), 4)
	for i := uint32(0); i <= h.CpbCntMinus1; i++ {
		writeGolombUnsigned(buf, pos, h.BitRateValueMinus1[i])
		writeGolombUnsigned(buf, pos, h.CpbSizeValueMinus1[i])
		bits.WriteFlagUnsafe(buf, pos, h.CbrFlag[i])
	}
	bits.WriteBitsUnsafe(buf, pos, uint64(h.InitialCpbRemovalDelayLengthMinus1), 5)
	bits.WriteBitsUnsafe(buf, pos, uint64(h.CpbRemovalDelayLengthMinus1), 5)
	bits.WriteBitsUnsafe(buf, pos, uint64(h.DpbOutputDelayLengthMinus1), 5)
	bits.WriteBitsUnsafe(buf, pos, uint64(h.TimeOffsetLength), 5)
}

// marshalH264TimingInfo marshals H.264 SPS timing info.
func marshalH264TimingInfo(buf []byte, pos *int, t *h264codec.SPS_TimingInfo) {
	bits.WriteBitsUnsafe(buf, pos, uint64(t.NumUnitsInTick), 32)
	bits.WriteBitsUnsafe(buf, pos, uint64(t.TimeScale), 32)
	bits.WriteFlagUnsafe(buf, pos, t.FixedFrameRateFlag)
}

// marshalH264BitstreamRestriction marshals H.264 SPS bitstream restriction info.
func marshalH264BitstreamRestriction(buf []byte, pos *int, r *h264codec.SPS_BitstreamRestriction) {
	bits.WriteFlagUnsafe(buf, pos, r.MotionVectorsOverPicBoundariesFlag)
	writeGolombUnsigned(buf, pos, r.MaxBytesPerPicDenom)
	writeGolombUnsigned(buf, pos, r.MaxBitsPerMbDenom)
	writeGolombUnsigned(buf, pos, r.Log2MaxMvLengthHorizontal)
	writeGolombUnsigned(buf, pos, r.Log2MaxMvLengthVertical)
	writeGolombUnsigned(buf, pos, r.MaxNumReorderFrames)
	writeGolombUnsigned(buf, pos, r.MaxDecFrameBuffering)
}

// marshalH264VUI marshals H.264 SPS VUI parameters.
func marshalH264VUI(buf []byte, pos *int, v *h264codec.SPS_VUI) {
	// aspect_ratio_info
	bits.WriteFlagUnsafe(buf, pos, v.AspectRatioInfoPresentFlag)
	if v.AspectRatioInfoPresentFlag {
		bits.WriteBitsUnsafe(buf, pos, uint64(v.AspectRatioIdc), 8)
		if v.AspectRatioIdc == 255 {
			bits.WriteBitsUnsafe(buf, pos, uint64(v.SarWidth), 16)
			bits.WriteBitsUnsafe(buf, pos, uint64(v.SarHeight), 16)
		}
	}

	// overscan_info
	bits.WriteFlagUnsafe(buf, pos, v.OverscanInfoPresentFlag)
	if v.OverscanInfoPresentFlag {
		bits.WriteFlagUnsafe(buf, pos, v.OverscanAppropriateFlag)
	}

	// video_signal_type
	bits.WriteFlagUnsafe(buf, pos, v.VideoSignalTypePresentFlag)
	if v.VideoSignalTypePresentFlag {
		bits.WriteBitsUnsafe(buf, pos, uint64(v.VideoFormat), 3)
		bits.WriteFlagUnsafe(buf, pos, v.VideoFullRangeFlag)
		bits.WriteFlagUnsafe(buf, pos, v.ColourDescriptionPresentFlag)
		if v.ColourDescriptionPresentFlag {
			bits.WriteBitsUnsafe(buf, pos, uint64(v.ColourPrimaries), 8)
			bits.WriteBitsUnsafe(buf, pos, uint64(v.TransferCharacteristics), 8)
			bits.WriteBitsUnsafe(buf, pos, uint64(v.MatrixCoefficients), 8)
		}
	}

	// chroma_loc_info
	bits.WriteFlagUnsafe(buf, pos, v.ChromaLocInfoPresentFlag)
	if v.ChromaLocInfoPresentFlag {
		writeGolombUnsigned(buf, pos, v.ChromaSampleLocTypeTopField)
		writeGolombUnsigned(buf, pos, v.ChromaSampleLocTypeBottomField)
	}

	// timing_info
	timingInfoPresent := v.TimingInfo != nil
	bits.WriteFlagUnsafe(buf, pos, timingInfoPresent)
	if timingInfoPresent {
		marshalH264TimingInfo(buf, pos, v.TimingInfo)
	}

	// nal_hrd_parameters
	nalHRDPresent := v.NalHRD != nil
	bits.WriteFlagUnsafe(buf, pos, nalHRDPresent)
	if nalHRDPresent {
		marshalH264HRD(buf, pos, v.NalHRD)
	}

	// vcl_hrd_parameters
	vclHRDPresent := v.VclHRD != nil
	bits.WriteFlagUnsafe(buf, pos, vclHRDPresent)
	if vclHRDPresent {
		marshalH264HRD(buf, pos, v.VclHRD)
	}

	// low_delay_hrd_flag (present if either HRD is present)
	if nalHRDPresent || vclHRDPresent {
		bits.WriteFlagUnsafe(buf, pos, v.LowDelayHrdFlag)
	}

	// pic_struct_present_flag
	bits.WriteFlagUnsafe(buf, pos, v.PicStructPresentFlag)

	// bitstream_restriction
	bsrPresent := v.BitstreamRestriction != nil
	bits.WriteFlagUnsafe(buf, pos, bsrPresent)
	if bsrPresent {
		marshalH264BitstreamRestriction(buf, pos, v.BitstreamRestriction)
	}
}

// marshalH264SPS marshals a complete H.264 SPS struct back to RBSP bytes (without NAL header).
func marshalH264SPS(s *h264codec.SPS) []byte {
	buf := make([]byte, 4096)
	pos := 0

	// profile_idc, constraint flags, level_idc (3 raw bytes)
	buf[0] = s.ProfileIdc
	var constraintByte byte
	if s.ConstraintSet0Flag {
		constraintByte |= 0x80
	}
	if s.ConstraintSet1Flag {
		constraintByte |= 0x40
	}
	if s.ConstraintSet2Flag {
		constraintByte |= 0x20
	}
	if s.ConstraintSet3Flag {
		constraintByte |= 0x10
	}
	if s.ConstraintSet4Flag {
		constraintByte |= 0x08
	}
	if s.ConstraintSet5Flag {
		constraintByte |= 0x04
	}
	buf[1] = constraintByte
	buf[2] = s.LevelIdc
	pos = 24

	// seq_parameter_set_id
	writeGolombUnsigned(buf, &pos, s.ID)

	// High profile fields
	if isHighProfile(s.ProfileIdc) {
		writeGolombUnsigned(buf, &pos, s.ChromaFormatIdc)
		if s.ChromaFormatIdc == 3 {
			bits.WriteFlagUnsafe(buf, &pos, s.SeparateColourPlaneFlag)
		}
		writeGolombUnsigned(buf, &pos, s.BitDepthLumaMinus8)
		writeGolombUnsigned(buf, &pos, s.BitDepthChromaMinus8)
		bits.WriteFlagUnsafe(buf, &pos, s.QpprimeYZeroTransformBypassFlag)

		// Scaling lists
		seqScalingMatrixPresentFlag := len(s.ScalingList4x4) > 0 || len(s.ScalingList8x8) > 0
		bits.WriteFlagUnsafe(buf, &pos, seqScalingMatrixPresentFlag)
		if seqScalingMatrixPresentFlag {
			lim := 8
			if s.ChromaFormatIdc == 3 {
				lim = 12
			}
			idx4 := 0
			idx8 := 0
			for i := 0; i < lim; i++ {
				if i < 6 {
					if idx4 < len(s.ScalingList4x4) {
						bits.WriteFlagUnsafe(buf, &pos, true)
						writeH264ScalingList(buf, &pos, s.ScalingList4x4[idx4])
						idx4++
					} else {
						bits.WriteFlagUnsafe(buf, &pos, false)
					}
				} else {
					if idx8 < len(s.ScalingList8x8) {
						bits.WriteFlagUnsafe(buf, &pos, true)
						writeH264ScalingList(buf, &pos, s.ScalingList8x8[idx8])
						idx8++
					} else {
						bits.WriteFlagUnsafe(buf, &pos, false)
					}
				}
			}
		}
	}

	// log2_max_frame_num_minus4
	writeGolombUnsigned(buf, &pos, s.Log2MaxFrameNumMinus4)

	// pic_order_cnt_type
	writeGolombUnsigned(buf, &pos, s.PicOrderCntType)
	switch s.PicOrderCntType {
	case 0:
		writeGolombUnsigned(buf, &pos, s.Log2MaxPicOrderCntLsbMinus4)
	case 1:
		bits.WriteFlagUnsafe(buf, &pos, s.DeltaPicOrderAlwaysZeroFlag)
		writeGolombSigned(buf, &pos, s.OffsetForNonRefPic)
		writeGolombSigned(buf, &pos, s.OffsetForTopToBottomField)
		numRefFrames := uint32(len(s.OffsetForRefFrames))
		writeGolombUnsigned(buf, &pos, numRefFrames)
		for _, offset := range s.OffsetForRefFrames {
			writeGolombSigned(buf, &pos, offset)
		}
	case 2:
		// No additional fields
	}

	// max_num_ref_frames
	writeGolombUnsigned(buf, &pos, s.MaxNumRefFrames)
	// gaps_in_frame_num_value_allowed_flag
	bits.WriteFlagUnsafe(buf, &pos, s.GapsInFrameNumValueAllowedFlag)
	// pic_width_in_mbs_minus1
	writeGolombUnsigned(buf, &pos, s.PicWidthInMbsMinus1)
	// pic_height_in_map_units_minus1
	writeGolombUnsigned(buf, &pos, s.PicHeightInMapUnitsMinus1)
	// frame_mbs_only_flag
	bits.WriteFlagUnsafe(buf, &pos, s.FrameMbsOnlyFlag)
	if !s.FrameMbsOnlyFlag {
		bits.WriteFlagUnsafe(buf, &pos, s.MbAdaptiveFrameFieldFlag)
	}
	// direct_8x8_inference_flag
	bits.WriteFlagUnsafe(buf, &pos, s.Direct8x8InferenceFlag)

	// frame_cropping
	croppingPresent := s.FrameCropping != nil
	bits.WriteFlagUnsafe(buf, &pos, croppingPresent)
	if croppingPresent {
		writeGolombUnsigned(buf, &pos, s.FrameCropping.LeftOffset)
		writeGolombUnsigned(buf, &pos, s.FrameCropping.RightOffset)
		writeGolombUnsigned(buf, &pos, s.FrameCropping.TopOffset)
		writeGolombUnsigned(buf, &pos, s.FrameCropping.BottomOffset)
	}

	// VUI
	vuiPresent := s.VUI != nil
	bits.WriteFlagUnsafe(buf, &pos, vuiPresent)
	if vuiPresent {
		marshalH264VUI(buf, &pos, s.VUI)
	}

	// RBSP trailing bits
	writeRBSPTrailingBits(buf, &pos)

	numBytes := (pos + 7) / 8
	if numBytes > len(buf) {
		return nil
	}
	return buf[:numBytes]
}

// PatchSPSColorRange patches H.264 SPS to include VUI color range signaling.
// If VUI already has video_signal_type_present_flag=1, returns sps unchanged.
// Otherwise, rewrites the SPS with BT.709 limited range VUI signaling.
// Input: raw SPS NAL unit bytes (with NAL header byte).
// Output: patched SPS NAL unit bytes, or original if no patching needed.
func PatchSPSColorRange(sps []byte) []byte {
	if len(sps) < 4 {
		return sps
	}

	var parsed h264codec.SPS
	if err := parsed.Unmarshal(sps); err != nil {
		spsPatchLogger.Warn("failed to parse H.264 SPS for color range patching, skipping",
			"error", err)
		return sps
	}

	// Already has video signal type — no patching needed
	if parsed.VUI != nil && parsed.VUI.VideoSignalTypePresentFlag {
		return sps
	}

	// Apply the patch
	if parsed.VUI == nil {
		parsed.VUI = &h264codec.SPS_VUI{}
	}
	parsed.VUI.VideoSignalTypePresentFlag = true
	parsed.VUI.VideoFormat = 5            // Unspecified
	parsed.VUI.VideoFullRangeFlag = false // Limited/TV range (16-235)
	parsed.VUI.ColourDescriptionPresentFlag = true
	parsed.VUI.ColourPrimaries = 1       // BT.709
	parsed.VUI.TransferCharacteristics = 1 // BT.709
	parsed.VUI.MatrixCoefficients = 1    // BT.709

	// Marshal back to RBSP bytes
	body := marshalH264SPS(&parsed)
	if body == nil {
		spsPatchLogger.Warn("failed to marshal patched H.264 SPS, returning original")
		return sps
	}

	// Add emulation prevention bytes
	body = emulationPreventionAdd(body)

	// Prepend original NAL header byte
	result := make([]byte, 1+len(body))
	result[0] = sps[0]
	copy(result[1:], body)

	spsPatchLogger.Debug("patched H.264 SPS with BT.709 limited range VUI signaling",
		"profile", parsed.ProfileIdc, "level", parsed.LevelIdc)

	return result
}

// ============================================================
// H.265 SPS marshaling
// ============================================================

// isExtendedConstraintProfile returns true for H.265 profiles with extended constraint flags.
func isExtendedConstraintProfile(p *h265codec.SPS_ProfileTierLevel) bool {
	switch p.GeneralProfileIdc {
	case 5, 9, 10, 11:
		return true
	}
	return p.GeneralProfileCompatibilityFlag[5] ||
		p.GeneralProfileCompatibilityFlag[9] ||
		p.GeneralProfileCompatibilityFlag[10] ||
		p.GeneralProfileCompatibilityFlag[11]
}

// marshalH265TimingInfo marshals H.265 SPS timing info.
func marshalH265TimingInfo(buf []byte, pos *int, t *h265codec.SPS_TimingInfo) {
	bits.WriteBitsUnsafe(buf, pos, uint64(t.NumUnitsInTick), 32)
	bits.WriteBitsUnsafe(buf, pos, uint64(t.TimeScale), 32)
	bits.WriteFlagUnsafe(buf, pos, t.POCProportionalToTimingFlag)
	if t.POCProportionalToTimingFlag {
		writeGolombUnsigned(buf, pos, t.NumTicksPOCDiffOneMinus1)
	}
}

// marshalH265ProfileTierLevel marshals H.265 SPS profile tier level.
func marshalH265ProfileTierLevel(buf []byte, pos *int, p *h265codec.SPS_ProfileTierLevel, maxSubLayersMinus1 uint8) {
	bits.WriteBitsUnsafe(buf, pos, uint64(p.GeneralProfileSpace), 2)
	bits.WriteBitsUnsafe(buf, pos, uint64(p.GeneralTierFlag), 1)
	bits.WriteBitsUnsafe(buf, pos, uint64(p.GeneralProfileIdc), 5)

	for j := range 32 {
		bits.WriteFlagUnsafe(buf, pos, p.GeneralProfileCompatibilityFlag[j])
	}

	bits.WriteFlagUnsafe(buf, pos, p.GeneralProgressiveSourceFlag)
	bits.WriteFlagUnsafe(buf, pos, p.GeneralInterlacedSourceFlag)
	bits.WriteFlagUnsafe(buf, pos, p.GeneralNonPackedConstraintFlag)
	bits.WriteFlagUnsafe(buf, pos, p.GeneralFrameOnlyConstraintFlag)
	bits.WriteFlagUnsafe(buf, pos, p.GeneralMax12bitConstraintFlag)
	bits.WriteFlagUnsafe(buf, pos, p.GeneralMax10bitConstraintFlag)
	bits.WriteFlagUnsafe(buf, pos, p.GeneralMax8bitConstraintFlag)
	bits.WriteFlagUnsafe(buf, pos, p.GeneralMax422ChromeConstraintFlag)
	bits.WriteFlagUnsafe(buf, pos, p.GeneralMax420ChromaConstraintFlag)
	bits.WriteFlagUnsafe(buf, pos, p.GeneralMaxMonochromeConstraintFlag)
	bits.WriteFlagUnsafe(buf, pos, p.GeneralIntraConstraintFlag)
	bits.WriteFlagUnsafe(buf, pos, p.GeneralOnePictureOnlyConstraintFlag)
	bits.WriteFlagUnsafe(buf, pos, p.GeneralLowerBitRateConstraintFlag)

	if isExtendedConstraintProfile(p) {
		bits.WriteFlagUnsafe(buf, pos, p.GeneralMax14BitConstraintFlag)
		bits.WriteBitsUnsafe(buf, pos, 0, 34) // reserved zero bits
	} else {
		bits.WriteBitsUnsafe(buf, pos, 0, 35) // reserved zero bits
	}

	bits.WriteBitsUnsafe(buf, pos, uint64(p.GeneralLevelIdc), 8)

	if maxSubLayersMinus1 > 0 {
		for j := range maxSubLayersMinus1 {
			bits.WriteFlagUnsafe(buf, pos, p.SubLayerProfilePresentFlag[j])
			bits.WriteFlagUnsafe(buf, pos, p.SubLayerLevelPresentFlag[j])
		}
		// reserved zero bits
		reservedBits := int((8 - maxSubLayersMinus1) * 2)
		bits.WriteBitsUnsafe(buf, pos, 0, reservedBits)
	}
}

// reconstructH265UsedFlags reconstructs the used_by_curr_pic_flag and use_delta_flag
// arrays for a prediction-mode ShortTermRefPicSet from the stored struct fields.
func reconstructH265UsedFlags(
	refRPS *h265codec.SPS_ShortTermRefPicSet,
	r *h265codec.SPS_ShortTermRefPicSet,
	deltaRps int32,
) ([]bool, []bool) {
	numDeltaPocs := refRPS.NumNegativePics + refRPS.NumPositivePics
	usedByCurrPicFlag := make([]bool, numDeltaPocs+1)
	useDeltaFlag := make([]bool, numDeltaPocs+1)

	// Build lookup maps from dPoc → index in final lists
	s0Map := make(map[int32]int)
	for k, poc := range r.DeltaPocS0 {
		s0Map[poc] = k
	}
	s1Map := make(map[int32]int)
	for k, poc := range r.DeltaPocS1 {
		s1Map[poc] = k
	}

	for j := uint32(0); j <= numDeltaPocs; j++ {
		var dPoc int32
		if j < refRPS.NumNegativePics {
			dPoc = refRPS.DeltaPocS0[j] + deltaRps
		} else if j < numDeltaPocs {
			dPoc = refRPS.DeltaPocS1[j-refRPS.NumNegativePics] + deltaRps
		} else {
			dPoc = deltaRps
		}

		if dPoc < 0 {
			if k, ok := s0Map[dPoc]; ok {
				usedByCurrPicFlag[j] = r.UsedByCurrPicS0Flag[k]
				useDeltaFlag[j] = true
			} else {
				usedByCurrPicFlag[j] = false
				useDeltaFlag[j] = false
			}
		} else if dPoc > 0 {
			if k, ok := s1Map[dPoc]; ok {
				usedByCurrPicFlag[j] = r.UsedByCurrPicS1Flag[k]
				useDeltaFlag[j] = true
			} else {
				usedByCurrPicFlag[j] = false
				useDeltaFlag[j] = false
			}
		} else {
			// dPoc == 0, not included in either list
			usedByCurrPicFlag[j] = false
			useDeltaFlag[j] = true
		}
	}

	return usedByCurrPicFlag, useDeltaFlag
}

// marshalH265ShortTermRefPicSet marshals a single H.265 short-term reference picture set.
func marshalH265ShortTermRefPicSet(
	buf []byte, pos *int,
	r *h265codec.SPS_ShortTermRefPicSet,
	stRpsIdx, numShortTermRefPicSets uint32,
	shortTermRefPicSets []*h265codec.SPS_ShortTermRefPicSet,
) {
	if stRpsIdx != 0 {
		bits.WriteFlagUnsafe(buf, pos, r.InterRefPicSetPredictionFlag)
	}

	if r.InterRefPicSetPredictionFlag {
		if stRpsIdx == numShortTermRefPicSets {
			writeGolombUnsigned(buf, pos, r.DeltaIdxMinus1)
		}
		bits.WriteFlagUnsafe(buf, pos, r.DeltaRpsSign)
		writeGolombUnsigned(buf, pos, r.AbsDeltaRpsMinus1)

		refRpsIdx := stRpsIdx - (r.DeltaIdxMinus1 + 1)
		if refRpsIdx >= uint32(len(shortTermRefPicSets)) {
			panic(fmt.Sprintf("invalid refRpsIdx %d for stRpsIdx %d", refRpsIdx, stRpsIdx))
		}
		refRPS := shortTermRefPicSets[refRpsIdx]

		var s int32
		if r.DeltaRpsSign {
			s = 1
		}
		deltaRps := (1 - 2*s) * (int32(r.AbsDeltaRpsMinus1) + 1)
		numDeltaPocs := refRPS.NumNegativePics + refRPS.NumPositivePics

		usedByCurrPicFlag, useDeltaFlag := reconstructH265UsedFlags(refRPS, r, deltaRps)

		for j := uint32(0); j <= numDeltaPocs; j++ {
			bits.WriteFlagUnsafe(buf, pos, usedByCurrPicFlag[j])
			if !usedByCurrPicFlag[j] {
				bits.WriteFlagUnsafe(buf, pos, useDeltaFlag[j])
			}
		}
	} else {
		writeGolombUnsigned(buf, pos, r.NumNegativePics)
		writeGolombUnsigned(buf, pos, r.NumPositivePics)

		for i := uint32(0); i < r.NumNegativePics; i++ {
			var deltaPocS0Minus1 uint32
			if i == 0 {
				deltaPocS0Minus1 = uint32(-r.DeltaPocS0[i] - 1)
			} else {
				deltaPocS0Minus1 = uint32(r.DeltaPocS0[i-1] - r.DeltaPocS0[i] - 1)
			}
			writeGolombUnsigned(buf, pos, deltaPocS0Minus1)
			bits.WriteFlagUnsafe(buf, pos, r.UsedByCurrPicS0Flag[i])
		}

		for i := uint32(0); i < r.NumPositivePics; i++ {
			var deltaPocS1Minus1 uint32
			if i == 0 {
				deltaPocS1Minus1 = uint32(r.DeltaPocS1[i] - 1)
			} else {
				deltaPocS1Minus1 = uint32(r.DeltaPocS1[i] - r.DeltaPocS1[i-1] - 1)
			}
			writeGolombUnsigned(buf, pos, deltaPocS1Minus1)
			bits.WriteFlagUnsafe(buf, pos, r.UsedByCurrPicS1Flag[i])
		}
	}
}

// marshalH265ScalingListData marshals H.265 SPS scaling list data.
// Note: delta coefficients are not stored by the parser, so entries that used
// prediction mode (PredModeFlag=true) are converted to non-prediction mode with
// delta=0. This changes scaling matrices but is acceptable for color range patching.
func marshalH265ScalingListData(buf []byte, pos *int, d *h265codec.SPS_ScalingListData) {
	for sizeID := range 4 {
		var matrixIDIncr int
		if sizeID == 3 {
			matrixIDIncr = 3
		} else {
			matrixIDIncr = 1
		}
		for matrixID := 0; matrixID < 6; matrixID += matrixIDIncr {
			// Always write non-prediction mode since delta coefficients are lost
			bits.WriteFlagUnsafe(buf, pos, false)
			if d.ScalingListPredModeFlag[sizeID][matrixID] {
				writeGolombUnsigned(buf, pos, 0) // Use default reference
			} else {
				writeGolombUnsigned(buf, pos, d.ScalingListPredmatrixIDDelta[sizeID][matrixID])
			}
		}
	}
}

// marshalH265Window marshals H.265 SPS window (conformance window or default display window).
func marshalH265Window(buf []byte, pos *int, w *h265codec.SPS_Window) {
	writeGolombUnsigned(buf, pos, w.LeftOffset)
	writeGolombUnsigned(buf, pos, w.RightOffset)
	writeGolombUnsigned(buf, pos, w.TopOffset)
	writeGolombUnsigned(buf, pos, w.BottomOffset)
}

// marshalH265VUI marshals H.265 SPS VUI parameters.
func marshalH265VUI(buf []byte, pos *int, v *h265codec.SPS_VUI) {
	// aspect_ratio_info
	bits.WriteFlagUnsafe(buf, pos, v.AspectRatioInfoPresentFlag)
	if v.AspectRatioInfoPresentFlag {
		bits.WriteBitsUnsafe(buf, pos, uint64(v.AspectRatioIdc), 8)
		if v.AspectRatioIdc == 255 {
			bits.WriteBitsUnsafe(buf, pos, uint64(v.SarWidth), 16)
			bits.WriteBitsUnsafe(buf, pos, uint64(v.SarHeight), 16)
		}
	}

	// overscan_info
	bits.WriteFlagUnsafe(buf, pos, v.OverscanInfoPresentFlag)
	if v.OverscanInfoPresentFlag {
		bits.WriteFlagUnsafe(buf, pos, v.OverscanAppropriateFlag)
	}

	// video_signal_type
	bits.WriteFlagUnsafe(buf, pos, v.VideoSignalTypePresentFlag)
	if v.VideoSignalTypePresentFlag {
		bits.WriteBitsUnsafe(buf, pos, uint64(v.VideoFormat), 3)
		bits.WriteFlagUnsafe(buf, pos, v.VideoFullRangeFlag)
		bits.WriteFlagUnsafe(buf, pos, v.ColourDescriptionPresentFlag)
		if v.ColourDescriptionPresentFlag {
			bits.WriteBitsUnsafe(buf, pos, uint64(v.ColourPrimaries), 8)
			bits.WriteBitsUnsafe(buf, pos, uint64(v.TransferCharacteristics), 8)
			bits.WriteBitsUnsafe(buf, pos, uint64(v.MatrixCoefficients), 8)
		}
	}

	// chroma_loc_info
	bits.WriteFlagUnsafe(buf, pos, v.ChromaLocInfoPresentFlag)
	if v.ChromaLocInfoPresentFlag {
		writeGolombUnsigned(buf, pos, v.ChromaSampleLocTypeTopField)
		writeGolombUnsigned(buf, pos, v.ChromaSampleLocTypeBottomField)
	}

	// neutral_chroma_indication (H.265 only)
	bits.WriteFlagUnsafe(buf, pos, v.NeutralChromaIndicationFlag)
	// field_seq_flag (H.265 only)
	bits.WriteFlagUnsafe(buf, pos, v.FieldSeqFlag)
	// frame_field_info_present_flag (H.265 only)
	bits.WriteFlagUnsafe(buf, pos, v.FrameFieldInfoPresentFlag)

	// default_display_window (H.265 only)
	dwPresent := v.DefaultDisplayWindow != nil
	bits.WriteFlagUnsafe(buf, pos, dwPresent)
	if dwPresent {
		marshalH265Window(buf, pos, v.DefaultDisplayWindow)
	}

	// timing_info
	timingInfoPresent := v.TimingInfo != nil
	bits.WriteFlagUnsafe(buf, pos, timingInfoPresent)
	if timingInfoPresent {
		marshalH265TimingInfo(buf, pos, v.TimingInfo)
	}
}

// marshalH265SPS marshals a complete H.265 SPS struct back to RBSP bytes (without NAL header).
func marshalH265SPS(s *h265codec.SPS) ([]byte, error) {
	buf := make([]byte, 4096)
	pos := 0

	// sps_video_parameter_set_id (4 bits)
	bits.WriteBitsUnsafe(buf, &pos, uint64(s.VPSID), 4)
	// sps_max_sub_layers_minus1 (3 bits)
	bits.WriteBitsUnsafe(buf, &pos, uint64(s.MaxSubLayersMinus1), 3)
	// sps_temporal_id_nesting_flag (1 bit)
	bits.WriteFlagUnsafe(buf, &pos, s.TemporalIDNestingFlag)

	// profile_tier_level
	marshalH265ProfileTierLevel(buf, &pos, &s.ProfileTierLevel, s.MaxSubLayersMinus1)

	// sps_seq_parameter_set_id
	writeGolombUnsigned(buf, &pos, uint32(s.ID))

	// chroma_format_idc
	writeGolombUnsigned(buf, &pos, s.ChromaFormatIdc)
	if s.ChromaFormatIdc == 3 {
		bits.WriteFlagUnsafe(buf, &pos, s.SeparateColourPlaneFlag)
	}

	// pic_width_in_luma_samples
	writeGolombUnsigned(buf, &pos, s.PicWidthInLumaSamples)
	// pic_height_in_luma_samples
	writeGolombUnsigned(buf, &pos, s.PicHeightInLumaSamples)

	// conformance_window_flag
	cwPresent := s.ConformanceWindow != nil
	bits.WriteFlagUnsafe(buf, &pos, cwPresent)
	if cwPresent {
		marshalH265Window(buf, &pos, s.ConformanceWindow)
	}

	// bit_depth_luma_minus8
	writeGolombUnsigned(buf, &pos, s.BitDepthLumaMinus8)
	// bit_depth_chroma_minus8
	writeGolombUnsigned(buf, &pos, s.BitDepthChromaMinus8)
	// log2_max_pic_order_cnt_lsb_minus4
	writeGolombUnsigned(buf, &pos, s.Log2MaxPicOrderCntLsbMinus4)

	// sps_sub_layer_ordering_info_present_flag
	bits.WriteFlagUnsafe(buf, &pos, s.SubLayerOrderingInfoPresentFlag)
	var start uint8
	if s.SubLayerOrderingInfoPresentFlag {
		start = 0
	} else {
		start = s.MaxSubLayersMinus1
	}
	for i := start; i <= s.MaxSubLayersMinus1; i++ {
		writeGolombUnsigned(buf, &pos, s.MaxDecPicBufferingMinus1[i])
		writeGolombUnsigned(buf, &pos, s.MaxNumReorderPics[i])
		writeGolombUnsigned(buf, &pos, s.MaxLatencyIncreasePlus1[i])
	}

	// log2_min_luma_coding_block_size_minus3
	writeGolombUnsigned(buf, &pos, s.Log2MinLumaCodingBlockSizeMinus3)
	// log2_diff_max_min_luma_coding_block_size
	writeGolombUnsigned(buf, &pos, s.Log2DiffMaxMinLumaCodingBlockSize)
	// log2_min_luma_transform_block_size_minus2
	writeGolombUnsigned(buf, &pos, s.Log2MinLumaTransformBlockSizeMinus2)
	// log2_diff_max_min_luma_transform_block_size
	writeGolombUnsigned(buf, &pos, s.Log2DiffMaxMinLumaTransformBlockSize)
	// max_transform_hierarchy_depth_inter
	writeGolombUnsigned(buf, &pos, s.MaxTransformHierarchyDepthInter)
	// max_transform_hierarchy_depth_intra
	writeGolombUnsigned(buf, &pos, s.MaxTransformHierarchyDepthIntra)

	// scaling_list_enabled_flag
	bits.WriteFlagUnsafe(buf, &pos, s.ScalingListEnabledFlag)
	if s.ScalingListEnabledFlag {
		scalingListDataPresent := s.ScalingListData != nil
		bits.WriteFlagUnsafe(buf, &pos, scalingListDataPresent)
		if scalingListDataPresent {
			marshalH265ScalingListData(buf, &pos, s.ScalingListData)
		}
	}

	// amp_enabled_flag
	bits.WriteFlagUnsafe(buf, &pos, s.AmpEnabledFlag)
	// sample_adaptive_offset_enabled_flag
	bits.WriteFlagUnsafe(buf, &pos, s.SampleAdaptiveOffsetEnabledFlag)
	// pcm_enabled_flag
	bits.WriteFlagUnsafe(buf, &pos, s.PcmEnabledFlag)
	if s.PcmEnabledFlag {
		bits.WriteBitsUnsafe(buf, &pos, uint64(s.PcmSampleBitDepthLumaMinus1), 4)
		bits.WriteBitsUnsafe(buf, &pos, uint64(s.PcmSampleBitDepthChromaMinus1), 4)
		writeGolombUnsigned(buf, &pos, s.Log2MinPcmLumaCodingBlockSizeMinus3)
		writeGolombUnsigned(buf, &pos, s.Log2DiffMaxMinPcmLumaCodingBlockSize)
		bits.WriteFlagUnsafe(buf, &pos, s.PcmLoopFilterDisabledFlag)
	}

	// short_term_ref_pic_sets
	numSTRefPicSets := uint32(len(s.ShortTermRefPicSets))
	writeGolombUnsigned(buf, &pos, numSTRefPicSets)
	for i := range numSTRefPicSets {
		marshalH265ShortTermRefPicSet(buf, &pos, s.ShortTermRefPicSets[i], i, numSTRefPicSets, s.ShortTermRefPicSets)
	}

	// long_term_ref_pics_present_flag
	bits.WriteFlagUnsafe(buf, &pos, s.LongTermRefPicsPresentFlag)
	if s.LongTermRefPicsPresentFlag {
		// num_long_term_ref_pics_sps is always 0 (parser returns error if > 0)
		writeGolombUnsigned(buf, &pos, 0)
	}

	// sps_temporal_mvp_enabled_flag
	bits.WriteFlagUnsafe(buf, &pos, s.TemporalMvpEnabledFlag)
	// strong_intra_smoothing_enabled_flag
	bits.WriteFlagUnsafe(buf, &pos, s.StrongIntraSmoothingEnabledFlag)

	// VUI
	vuiPresent := s.VUI != nil
	bits.WriteFlagUnsafe(buf, &pos, vuiPresent)
	if vuiPresent {
		marshalH265VUI(buf, &pos, s.VUI)
	}

	// RBSP trailing bits
	writeRBSPTrailingBits(buf, &pos)

	numBytes := (pos + 7) / 8
	if numBytes > len(buf) {
		return nil, fmt.Errorf("SPS marshal buffer overflow")
	}
	return buf[:numBytes], nil
}

// PatchSPSH265ColorRange patches H.265 SPS to include VUI color range signaling.
// If VUI already has video_signal_type_present_flag=1, returns sps unchanged.
// Otherwise, rewrites the SPS with BT.709 limited range VUI signaling.
// Input: raw SPS NAL unit bytes (with 2-byte NAL header).
// Output: patched SPS NAL unit bytes, or original if no patching needed.
func PatchSPSH265ColorRange(sps []byte) []byte {
	if len(sps) < 4 {
		return sps
	}

	var parsed h265codec.SPS
	if err := parsed.Unmarshal(sps); err != nil {
		spsPatchLogger.Warn("failed to parse H.265 SPS for color range patching, skipping",
			"error", err)
		return sps
	}

	// Already has video signal type — no patching needed
	if parsed.VUI != nil && parsed.VUI.VideoSignalTypePresentFlag {
		return sps
	}

	// Apply the patch
	if parsed.VUI == nil {
		parsed.VUI = &h265codec.SPS_VUI{}
	}
	parsed.VUI.VideoSignalTypePresentFlag = true
	parsed.VUI.VideoFormat = 5            // Unspecified
	parsed.VUI.VideoFullRangeFlag = false // Limited/TV range (16-235)
	parsed.VUI.ColourDescriptionPresentFlag = true
	parsed.VUI.ColourPrimaries = 1       // BT.709
	parsed.VUI.TransferCharacteristics = 1 // BT.709
	parsed.VUI.MatrixCoefficients = 1    // BT.709

	// Marshal back to RBSP bytes
	body, err := marshalH265SPS(&parsed)
	if err != nil {
		spsPatchLogger.Warn("failed to marshal patched H.265 SPS, returning original",
			"error", err)
		return sps
	}

	// Add emulation prevention bytes
	body = emulationPreventionAdd(body)

	// Prepend 2-byte NAL header (forbidden_zero=0, nal_type=33, layer_id=0, temporal_id=1)
	result := make([]byte, 2+len(body))
	result[0] = sps[0] // preserve original first header byte
	result[1] = sps[1] // preserve original second header byte
	copy(result[2:], body)

	spsPatchLogger.Debug("patched H.265 SPS with BT.709 limited range VUI signaling")

	return result
}
