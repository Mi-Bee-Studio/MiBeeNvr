package merge

import "fmt"

// SPS resolution parsing for H.264 and H.265.
// Extracted from internal/muxer/mp4mux.go to avoid cross-package dependency.

// bitReader reads bits from a byte slice, MSB first.
type bitReader struct {
	data   []byte
	offset int
}

func (r *bitReader) readBit() (int, error) {
	if r.offset >= len(r.data)*8 {
		return 0, fmt.Errorf("merge: bitReader overflow at offset %d (data length %d bits)", r.offset, len(r.data)*8)
	}
	byteIdx := r.offset / 8
	bitIdx := 7 - (r.offset % 8)
	r.offset++
	return int((r.data[byteIdx] >> bitIdx) & 1), nil
}

func (r *bitReader) readBits(n int) (int, error) {
	var val int
	for i := 0; i < n; i++ {
		bit, err := r.readBit()
		if err != nil {
			return 0, err
		}
		val = (val << 1) | bit
	}
	return val, nil
}

// readUE reads an unsigned Exp-Golomb coded value.
func (r *bitReader) readUE() (int, error) {
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
			return 0, fmt.Errorf("merge: sps readUE leadingZeros overflow (%d)", leadingZeros)
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

// readSE reads a signed Exp-Golomb coded value.
func (r *bitReader) readSE() (int, error) {
	val, err := r.readUE()
	if err != nil {
		return 0, err
	}
	if val%2 == 0 {
		return -(val / 2), nil
	}
	return (val + 1) / 2, nil
}

// removeEmulationPrevention removes H.264/H.265 emulation prevention bytes (0x00 0x00 0x03).
func removeEmulationPrevention(data []byte) []byte {
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

// parseSPSResolution extracts width and height from an H.264 SPS NAL unit.
func parseSPSResolution(sps []byte) (width, height int, err error) {
	if len(sps) < 8 {
		return 0, 0, fmt.Errorf("merge: sps too short (%d bytes)", len(sps))
	}

	rbsp := removeEmulationPrevention(sps[1:])
	if len(rbsp) < 4 {
		return 0, 0, fmt.Errorf("merge: sps rbsp too short (%d bytes)", len(rbsp))
	}

	r := &bitReader{data: rbsp}

	var profileIDC int
	if profileIDC, err = r.readBits(8); err != nil {
		return 0, 0, fmt.Errorf("merge: sps parse error: %w", err)
	}
	if _, err = r.readBits(8); err != nil { // constraint_set_flags
		return 0, 0, fmt.Errorf("merge: sps parse error: %w", err)
	}
	if _, err = r.readBits(8); err != nil { // level_idc
		return 0, 0, fmt.Errorf("merge: sps parse error: %w", err)
	}

	if _, err = r.readUE(); err != nil { // seq_parameter_set_id
		return 0, 0, fmt.Errorf("merge: sps parse error: %w", err)
	}

	highProfile := profileIDC == 100 || profileIDC == 110 || profileIDC == 122 ||
		profileIDC == 244 || profileIDC == 44 || profileIDC == 83 ||
		profileIDC == 86 || profileIDC == 118 || profileIDC == 128 ||
		profileIDC == 138 || profileIDC == 139 || profileIDC == 134

	chromaFormatIDC := 1
	if highProfile {
		if chromaFormatIDC, err = r.readUE(); err != nil {
			return 0, 0, fmt.Errorf("merge: sps parse error: %w", err)
		}
		if chromaFormatIDC == 3 {
			if _, err = r.readBit(); err != nil { // separate_colour_plane_flag
				return 0, 0, fmt.Errorf("merge: sps parse error: %w", err)
			}
		}
		if _, err = r.readUE(); err != nil { // bit_depth_luma_minus8
			return 0, 0, fmt.Errorf("merge: sps parse error: %w", err)
		}
		if _, err = r.readUE(); err != nil { // bit_depth_chroma_minus8
			return 0, 0, fmt.Errorf("merge: sps parse error: %w", err)
		}
		if _, err = r.readBit(); err != nil { // qpprime_y_zero_transform_bypass_flag
			return 0, 0, fmt.Errorf("merge: sps parse error: %w", err)
		}
		var scalingPresent int
		if scalingPresent, err = r.readBit(); err != nil {
			return 0, 0, fmt.Errorf("merge: sps parse error: %w", err)
		}
		if scalingPresent == 1 {
			count := 8
			if chromaFormatIDC == 3 {
				count = 12
			}
			for i := 0; i < count; i++ {
				var present int
				if present, err = r.readBit(); err != nil {
					return 0, 0, fmt.Errorf("merge: sps parse error: %w", err)
				}
				if present == 1 {
					size := 16
					if i >= 6 {
						size = 64
					}
					lastScale := 8
					for j := 0; j < size; j++ {
						var delta int
						if delta, err = r.readSE(); err != nil {
							return 0, 0, fmt.Errorf("merge: sps parse error: %w", err)
						}
						nextScale := (lastScale + delta + 256) % 256
						if nextScale == 0 {
							nextScale = 256
						}
						lastScale = nextScale
					}
				}
			}
		}
	}

	if _, err = r.readUE(); err != nil { // log2_max_frame_num_minus4
		return 0, 0, fmt.Errorf("merge: sps parse error: %w", err)
	}

	var picOrderCntType int
	if picOrderCntType, err = r.readUE(); err != nil {
		return 0, 0, fmt.Errorf("merge: sps parse error: %w", err)
	}
	if picOrderCntType == 0 {
		if _, err = r.readUE(); err != nil { // log2_max_pic_order_cnt_lsb_minus4
			return 0, 0, fmt.Errorf("merge: sps parse error: %w", err)
		}
	} else if picOrderCntType == 1 {
		if _, err = r.readBit(); err != nil { // delta_pic_order_always_zero_flag
			return 0, 0, fmt.Errorf("merge: sps parse error: %w", err)
		}
		if _, err = r.readSE(); err != nil { // offset_for_non_ref_pic
			return 0, 0, fmt.Errorf("merge: sps parse error: %w", err)
		}
		if _, err = r.readSE(); err != nil { // offset_for_top_to_bottom_field
			return 0, 0, fmt.Errorf("merge: sps parse error: %w", err)
		}
		var numRefFrames int
		if numRefFrames, err = r.readUE(); err != nil {
			return 0, 0, fmt.Errorf("merge: sps parse error: %w", err)
		}
		for i := 0; i < numRefFrames; i++ {
			if _, err = r.readSE(); err != nil {
				return 0, 0, fmt.Errorf("merge: sps parse error: %w", err)
			}
		}
	}

	if _, err = r.readUE(); err != nil { // max_num_ref_frames
		return 0, 0, fmt.Errorf("merge: sps parse error: %w", err)
	}
	if _, err = r.readBit(); err != nil { // gaps_in_frame_num_value_allowed_flag
		return 0, 0, fmt.Errorf("merge: sps parse error: %w", err)
	}

	var picWidthInMbs, picHeightInMapUnits, frameMbsOnly int
	if picWidthInMbs, err = r.readUE(); err != nil {
		return 0, 0, fmt.Errorf("merge: sps parse error: %w", err)
	}
	picWidthInMbs++
	if picHeightInMapUnits, err = r.readUE(); err != nil {
		return 0, 0, fmt.Errorf("merge: sps parse error: %w", err)
	}
	picHeightInMapUnits++
	if frameMbsOnly, err = r.readBit(); err != nil {
		return 0, 0, fmt.Errorf("merge: sps parse error: %w", err)
	}
	if frameMbsOnly == 0 {
		if _, err = r.readBit(); err != nil { // mb_adaptive_frame_field_flag
			return 0, 0, fmt.Errorf("merge: sps parse error: %w", err)
		}
	}
	if _, err = r.readBit(); err != nil { // direct_8x8_inference_flag
		return 0, 0, fmt.Errorf("merge: sps parse error: %w", err)
	}
	var frameCropping int
	if frameCropping, err = r.readBit(); err != nil {
		return 0, 0, fmt.Errorf("merge: sps parse error: %w", err)
	}

	var cropLeft, cropRight, cropTop, cropBottom int
	if frameCropping == 1 {
		var cropLeftMinus1, cropRightMinus1, cropTopMinus1, cropBottomMinus1 int
		if cropLeftMinus1, err = r.readUE(); err != nil {
			return 0, 0, fmt.Errorf("merge: sps parse error: %w", err)
		}
		if cropRightMinus1, err = r.readUE(); err != nil {
			return 0, 0, fmt.Errorf("merge: sps parse error: %w", err)
		}
		if cropTopMinus1, err = r.readUE(); err != nil {
			return 0, 0, fmt.Errorf("merge: sps parse error: %w", err)
		}
		if cropBottomMinus1, err = r.readUE(); err != nil {
			return 0, 0, fmt.Errorf("merge: sps parse error: %w", err)
		}

		var cropUnitX, cropUnitY int
		if chromaFormatIDC == 0 {
			cropUnitX, cropUnitY = 1, 1
		} else if chromaFormatIDC == 1 {
			cropUnitX, cropUnitY = 2, 2
		} else if chromaFormatIDC == 2 {
			cropUnitX, cropUnitY = 2, 1
		} else {
			cropUnitX, cropUnitY = 1, 1
		}
		cropLeft = cropUnitX * cropLeftMinus1
		cropRight = cropUnitX * cropRightMinus1
		cropTop = cropUnitY * cropTopMinus1
		cropBottom = cropUnitY * cropBottomMinus1
	}

	width = picWidthInMbs*16 - cropLeft - cropRight
	height = (2-frameMbsOnly)*picHeightInMapUnits*16 - cropTop - cropBottom

	if width <= 0 || height <= 0 || width > 8192 || height > 8192 {
		return 0, 0, fmt.Errorf("merge: invalid sps resolution: width=%d, height=%d", width, height)
	}
	return width, height, nil
}

// parseHEVCSPSResolution extracts width and height from an HEVC SPS NAL unit.
func parseHEVCSPSResolution(sps []byte) (width, height int, err error) {
	if len(sps) < 8 {
		return 0, 0, fmt.Errorf("merge: hevc sps too short (%d bytes)", len(sps))
	}
	rbsp := removeEmulationPrevention(sps[2:]) // skip 2-byte NAL header
	if len(rbsp) < 13 {
		return 0, 0, fmt.Errorf("merge: hevc sps rbsp too short (%d bytes)", len(rbsp))
	}
	r := &bitReader{data: rbsp}

	if _, err = r.readBits(4); err != nil { // sps_video_parameter_set_id
		return 0, 0, fmt.Errorf("merge: sps parse error: %w", err)
	}
	var maxSubLayersMinus1 int
	if maxSubLayersMinus1, err = r.readBits(3); err != nil {
		return 0, 0, fmt.Errorf("merge: sps parse error: %w", err)
	}
	if _, err = r.readBit(); err != nil { // sps_temporal_id_nesting_flag
		return 0, 0, fmt.Errorf("merge: sps parse error: %w", err)
	}
	// profile_tier_level
	if _, err = r.readBits(8); err != nil { // general_profile_space + tier + profile_idc
		return 0, 0, fmt.Errorf("merge: sps parse error: %w", err)
	}
	if _, err = r.readBits(32); err != nil { // general_profile_compatibility_flag[32]
		return 0, 0, fmt.Errorf("merge: sps parse error: %w", err)
	}
	if _, err = r.readBits(48); err != nil { // general constraint indicator flags
		return 0, 0, fmt.Errorf("merge: sps parse error: %w", err)
	}
	if _, err = r.readBits(8); err != nil { // general_level_idc
		return 0, 0, fmt.Errorf("merge: sps parse error: %w", err)
	}
	for i := 0; i < maxSubLayersMinus1; i++ {
		if _, err = r.readBits(2); err != nil {
			return 0, 0, fmt.Errorf("merge: sps parse error: %w", err)
		}
	}
	if maxSubLayersMinus1 > 0 {
		for i := 0; i < maxSubLayersMinus1; i++ {
			if _, err = r.readBit(); err != nil {
				return 0, 0, fmt.Errorf("merge: sps parse error: %w", err)
			}
		}
	}
	if _, err = r.readUE(); err != nil { // sps_seq_parameter_set_id
		return 0, 0, fmt.Errorf("merge: sps parse error: %w", err)
	}
	var chromaFormatIDC int
	if chromaFormatIDC, err = r.readUE(); err != nil {
		return 0, 0, fmt.Errorf("merge: sps parse error: %w", err)
	}
	if chromaFormatIDC == 3 {
		if _, err = r.readBit(); err != nil { // separate_colour_plane_flag
			return 0, 0, fmt.Errorf("merge: sps parse error: %w", err)
		}
	}
	if width, err = r.readUE(); err != nil { // pic_width_in_luma_samples
		return 0, 0, fmt.Errorf("merge: sps parse error: %w", err)
	}
	if height, err = r.readUE(); err != nil { // pic_height_in_luma_samples
		return 0, 0, fmt.Errorf("merge: sps parse error: %w", err)
	}
	if width <= 0 || height <= 0 || width > 8192 || height > 8192 {
		return 0, 0, fmt.Errorf("merge: invalid hevc sps resolution: width=%d, height=%d", width, height)
	}
	return width, height, nil
}
