package merge

// SPS resolution parsing for H.264 and H.265.
// Extracted from internal/muxer/mp4mux.go to avoid cross-package dependency.

// bitReader reads bits from a byte slice, MSB first.
type bitReader struct {
	data   []byte
	offset int
}

func (r *bitReader) readBit() int {
	if r.offset >= len(r.data)*8 {
		return 0
	}
	byteIdx := r.offset / 8
	bitIdx := 7 - (r.offset % 8)
	r.offset++
	return int((r.data[byteIdx] >> bitIdx) & 1)
}

func (r *bitReader) readBits(n int) int {
	var val int
	for i := 0; i < n; i++ {
		val = (val << 1) | r.readBit()
	}
	return val
}

// readUE reads an unsigned Exp-Golomb coded value.
func (r *bitReader) readUE() int {
	leadingZeros := 0
	for r.readBit() == 0 {
		leadingZeros++
		if leadingZeros > 32 {
			return 0
		}
	}
	if leadingZeros == 0 {
		return 0
	}
	return (1 << leadingZeros) - 1 + r.readBits(leadingZeros)
}

// readSE reads a signed Exp-Golomb coded value.
func (r *bitReader) readSE() int {
	val := r.readUE()
	if val%2 == 0 {
		return -(val / 2)
	}
	return (val + 1) / 2
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
// Returns (0, 0) if parsing fails.
func parseSPSResolution(sps []byte) (width, height int) {
	if len(sps) < 8 {
		return 0, 0
	}

	rbsp := removeEmulationPrevention(sps[1:])
	if len(rbsp) < 4 {
		return 0, 0
	}

	r := &bitReader{data: rbsp}

	profileIDC := r.readBits(8)
	r.readBits(8) // constraint_set_flags
	r.readBits(8) // level_idc

	r.readUE() // seq_parameter_set_id

	highProfile := profileIDC == 100 || profileIDC == 110 || profileIDC == 122 ||
		profileIDC == 244 || profileIDC == 44 || profileIDC == 83 ||
		profileIDC == 86 || profileIDC == 118 || profileIDC == 128 ||
		profileIDC == 138 || profileIDC == 139 || profileIDC == 134

	chromaFormatIDC := 1
	if highProfile {
		chromaFormatIDC = r.readUE()
		if chromaFormatIDC == 3 {
			r.readBit() // separate_colour_plane_flag
		}
		r.readUE()  // bit_depth_luma_minus8
		r.readUE()  // bit_depth_chroma_minus8
		r.readBit() // qpprime_y_zero_transform_bypass_flag
		scalingPresent := r.readBit()
		if scalingPresent == 1 {
			count := 8
			if chromaFormatIDC == 3 {
				count = 12
			}
			for i := 0; i < count; i++ {
				present := r.readBit()
				if present == 1 {
					size := 16
					if i >= 6 {
						size = 64
					}
					lastScale := 8
					for j := 0; j < size; j++ {
						delta := r.readSE()
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

	r.readUE() // log2_max_frame_num_minus4

	picOrderCntType := r.readUE()
	if picOrderCntType == 0 {
		r.readUE() // log2_max_pic_order_cnt_lsb_minus4
	} else if picOrderCntType == 1 {
		r.readBit() // delta_pic_order_always_zero_flag
		r.readSE()  // offset_for_non_ref_pic
		r.readSE()  // offset_for_top_to_bottom_field
		numRefFrames := r.readUE()
		for i := 0; i < numRefFrames; i++ {
			r.readSE()
		}
	}

	r.readUE()  // max_num_ref_frames
	r.readBit() // gaps_in_frame_num_value_allowed_flag

	picWidthInMbs := r.readUE() + 1
	picHeightInMapUnits := r.readUE() + 1
	frameMbsOnly := r.readBit()
	if frameMbsOnly == 0 {
		r.readBit() // mb_adaptive_frame_field_flag
	}
	r.readBit() // direct_8x8_inference_flag
	frameCropping := r.readBit()

	var cropLeft, cropRight, cropTop, cropBottom int
	if frameCropping == 1 {
		cropLeftMinus1 := r.readUE()
		cropRightMinus1 := r.readUE()
		cropTopMinus1 := r.readUE()
		cropBottomMinus1 := r.readUE()

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
		return 0, 0
	}
	return width, height
}

// parseHEVCSPSResolution extracts width and height from an HEVC SPS NAL unit.
// Returns (0, 0) if parsing fails.
func parseHEVCSPSResolution(sps []byte) (width, height int) {
	if len(sps) < 8 {
		return 0, 0
	}
	rbsp := removeEmulationPrevention(sps[2:]) // skip 2-byte NAL header
	if len(rbsp) < 13 {
		return 0, 0
	}
	r := &bitReader{data: rbsp}
	r.readBits(4) // sps_video_parameter_set_id
	maxSubLayersMinus1 := r.readBits(3)
	r.readBit() // sps_temporal_id_nesting_flag
	// profile_tier_level
	r.readBits(8)  // general_profile_space + tier + profile_idc
	r.readBits(32) // general_profile_compatibility_flag[32]
	r.readBits(48) // general constraint indicator flags
	r.readBits(8)  // general_level_idc
	for i := 0; i < maxSubLayersMinus1; i++ {
		r.readBits(2)
	}
	if maxSubLayersMinus1 > 0 {
		for i := 0; i < maxSubLayersMinus1; i++ {
			r.readBit()
		}
	}
	r.readUE()                // sps_seq_parameter_set_id
	chromaFormatIDC := r.readUE()
	if chromaFormatIDC == 3 {
		r.readBit() // separate_colour_plane_flag
	}
	width = r.readUE()  // pic_width_in_luma_samples
	height = r.readUE() // pic_height_in_luma_samples
	if width <= 0 || height <= 0 || width > 8192 || height > 8192 {
		return 0, 0
	}
	return width, height
}
