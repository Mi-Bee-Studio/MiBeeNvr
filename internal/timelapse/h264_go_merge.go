// Package timelapse — Pure Go H.264 NAL → MP4 muxer.
//
// H264GoMerger converts raw H.264 keyframe files (Annex-B format with
// 0x00000001 start codes) captured by KeyframeExtractor into playable
// MP4 timelapse videos using only the abema/go-mp4 library and the
// Go standard library. No CGO, no external binaries.
package timelapse

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/mp4util"
	"github.com/abema/go-mp4"
)

// H264GoMerger implements TimelapseMerger using pure Go to create an MP4 file
// from raw H.264 IDR keyframe files. Each frame file contains one access unit
// with multiple NAL units (SPS, PPS, IDR slice) in Annex-B format using
// 0x00000001 start codes.
type H264GoMerger struct{}

// NewH264GoMerger creates a new H264GoMerger.
func NewH264GoMerger() *H264GoMerger {
	return &H264GoMerger{}
}

// CanMerge always returns true since this is a pure Go implementation.
func (m *H264GoMerger) CanMerge() bool {
	return true
}

// Tier returns the merge tier identifier.
func (m *H264GoMerger) Tier() MergeTier {
	return TierGo
}

// Merge reads H.264 keyframe files from framesDir, builds an MP4 file at
// outputPath with the given fps, and returns a MergeResult.
func (m *H264GoMerger) Merge(ctx context.Context, framesDir, outputPath string, fps int) (*MergeResult, error) {
	// List and sort frame files.
	frames, err := listCodecFrameFiles(framesDir, "h264")
	if err != nil {
		return &MergeResult{Tier: TierGo, Error: err.Error()}, err
	}
	if len(frames) == 0 {
		err := fmt.Errorf("no H.264 frames found in %s", framesDir)
		return &MergeResult{Tier: TierGo, Error: err.Error()}, err
	}

	select {
	case <-ctx.Done():
		return &MergeResult{Tier: TierGo, Error: ctx.Err().Error()}, ctx.Err()
	default:
	}

	if fps <= 0 {
		fps = 1
	}
	sampleDuration := time.Duration(1000/fps) * time.Millisecond

	// Scan frames for SPS and PPS — parameter sets may arrive in separate
	// AUs from IDR frames, so they might not be in the first frame file.
	// Scan up to 50 frames to find them.
	var sps, pps []byte
	scanLimit := 50
	if len(frames) < scanLimit {
		scanLimit = len(frames)
	}
	for i := 0; i < scanLimit && (sps == nil || pps == nil); i++ {
		frameData, err := os.ReadFile(frames[i])
		if err != nil {
			continue
		}
		for _, nalu := range splitAnnexB(frameData) {
			if len(nalu) == 0 {
				continue
			}
			nalType := nalu[0] & 0x1F
			switch nalType {
			case 7: // SPS
				if sps == nil {
					sps = nalu
				}
			case 8: // PPS
				if pps == nil {
					pps = nalu
				}
			}
		}
	}

	if sps == nil {
		return &MergeResult{Tier: TierGo, Error: "frames missing SPS"},
			fmt.Errorf("frames missing SPS")
	}
	if pps == nil {
		return &MergeResult{Tier: TierGo, Error: "frames missing PPS"},
			fmt.Errorf("frames missing PPS")
	}

	// Build avcC decoder configuration from SPS/PPS.
	avcCData := buildAvcC(sps, pps)

	// Parse dimensions from SPS.
	width, height := parseH264Dimensions(sps)
	if width == 0 {
		width = 640
	}
	if height == 0 {
		height = 480
	}

	select {
	case <-ctx.Done():
		return &MergeResult{Tier: TierGo, Error: ctx.Err().Error()}, ctx.Err()
	default:
	}

	// Pre-scan all frames for sample sizes (needed by stsz).
	sampleSizes := make([]uint32, len(frames))
	for i, path := range frames {
		select {
		case <-ctx.Done():
			return &MergeResult{Tier: TierGo, Error: ctx.Err().Error()}, ctx.Err()
		default:
		}
		frameData, err := os.ReadFile(path)
		if err != nil {
			return &MergeResult{Tier: TierGo, Error: err.Error()},
				fmt.Errorf("read frame %s: %w", path, err)
		}
		nalus := splitAnnexB(frameData)
		for _, nalu := range nalus {
			// Skip parameter sets — they belong in avcC only, not in sample data.
			if isH264ParamSet(nalu) {
				continue
			}
			sampleSizes[i] += 4 + uint32(len(nalu)) // 4-byte length prefix + NAL data
		}
	}

	select {
	case <-ctx.Done():
		return &MergeResult{Tier: TierGo, Error: ctx.Err().Error()}, ctx.Err()
	default:
	}

	// First pass: calculate moov size by writing to a buffer.
	muxer := &codecMuxer{
		frameCount:        len(frames),
		sampleSizes:       sampleSizes,
		width:             width,
		height:            height,
		decoderConfigData: avcCData,
		sampleDur:         sampleDuration,
		sampleEntryType:   [4]byte{'a', 'v', 'c', '1'},
		configBoxType:     [4]byte{'a', 'v', 'c', 'C'},
	}

	buf := &bytesWriter{}
	bw := mp4.NewWriter(buf)
	if err := muxer.writeMoov(bw, 0); err != nil {
		return &MergeResult{Tier: TierGo, Error: err.Error()},
			fmt.Errorf("calculate moov size: %w", err)
	}
	moovSize := buf.len()

	select {
	case <-ctx.Done():
		return &MergeResult{Tier: TierGo, Error: ctx.Err().Error()}, ctx.Err()
	default:
	}

	// Create the output file.
	f, err := os.Create(outputPath)
	if err != nil {
		return &MergeResult{Tier: TierGo, Error: err.Error()},
			fmt.Errorf("create output: %w", err)
	}
	defer f.Close()

	w := mp4.NewWriter(f)

	// Write ftyp.
	ftypSize, err := writeCodecFtyp(w, [4]byte{'a', 'v', 'c', '1'})
	if err != nil {
		return &MergeResult{Tier: TierGo, Error: err.Error()},
			fmt.Errorf("write ftyp: %w", err)
	}

	// mdat data starts after ftyp + moov + 8-byte mdat header.
	mdatDataOffset := int64(ftypSize) + int64(moovSize) + 8

	// Write moov with correct stco chunk offset.
	muxer.chunkOffset = mdatDataOffset
	if err := muxer.writeMoov(w, mdatDataOffset); err != nil {
		return &MergeResult{Tier: TierGo, Error: err.Error()},
			fmt.Errorf("write moov: %w", err)
	}

	select {
	case <-ctx.Done():
		f.Close()
		os.Remove(outputPath)
		return &MergeResult{Tier: TierGo, Error: ctx.Err().Error()}, ctx.Err()
	default:
	}

	// Write mdat box (strips SPS/PPS from samples — they are in avcC only).
	if err := writeCodecMdat(w, frames, ctx, isH264ParamSet); err != nil {
		f.Close()
		os.Remove(outputPath)
		return &MergeResult{Tier: TierGo, Error: err.Error()}, err
	}

	framesMerged := len(frames)
	return &MergeResult{
		Tier:         TierGo,
		OutputPath:   outputPath,
		FramesMerged: framesMerged,
		Duration:     float64(framesMerged) * sampleDuration.Seconds(),
		Codec:        model.TimelapseMergeCodecH264,
	}, nil
}

// --- Helpers ---

// isH264ParamSet returns true if the NAL unit is a parameter set (SPS or PPS).
// These must only appear in the avcC box, not in sample data.
func isH264ParamSet(nalu []byte) bool {
	if len(nalu) == 0 {
		return false
	}
	nalType := nalu[0] & 0x1F
	return nalType == 7 || nalType == 8 // SPS, PPS
}

// buildAvcC builds the AVCDecoderConfiguration record bytes from raw SPS and PPS
// NAL units (without start codes). Format per ISO 14496-15.
//
// Thin wrapper over mp4util.BuildAvcC (the shared single source of truth for
// the avcC record — see #236). Returns marshaled bytes so callers and tests
// keep working with the same []byte contract.
func buildAvcC(sps, pps []byte) []byte {
	var buf bytes.Buffer
	if _, err := mp4.Marshal(&buf, mp4util.BuildAvcC(sps, pps), mp4.Context{}); err != nil {
		// Marshal of a fully-populated *mp4.AVCDecoderConfiguration never fails
		// in practice. Return nil on the impossible failure so the caller
		// surfaces a clearly broken file rather than panicking.
		return nil
	}
	return buf.Bytes()
}

// parseH264Dimensions extracts width and height from a raw H.264 SPS NAL unit.
// Returns 0, 0 if parsing fails.
func parseH264Dimensions(sps []byte) (width, height int) {
	if len(sps) < 8 {
		return 0, 0
	}

	// Skip NAL header byte.
	data := sps[1:]

	// profile_idc (1 byte), constraint_set_flags (1 byte), level_idc (1 byte).
	if len(data) < 3 {
		return 0, 0
	}
	profileIDC := data[0]
	data = data[3:]

	r := &bitReader{data: data}

	// seq_parameter_set_id (ue(v))
	r.readUE()

	// High-profile-specific fields.
	if profileIDC == 100 || profileIDC == 110 || profileIDC == 122 || profileIDC == 244 ||
		profileIDC == 44 || profileIDC == 83 || profileIDC == 86 || profileIDC == 118 ||
		profileIDC == 128 || profileIDC == 138 || profileIDC == 139 || profileIDC == 134 ||
		profileIDC == 135 {
		chromaFormatIDC := r.readUE()
		if chromaFormatIDC == 3 {
			r.readBit() // separate_colour_plane_flag
		}
		r.readUE()            // bit_depth_luma_minus8
		r.readUE()            // bit_depth_chroma_minus8
		r.readBit()           // qpprime_y_zero_transform_bypass_flag
		if r.readBit() != 0 { // seq_scaling_matrix_present_flag
			skipScalingLists(r)
		}
	}

	// log2_max_frame_num_minus4 (ue(v))
	r.readUE()

	// pic_order_cnt_type (ue(v))
	pocType := r.readUE()
	if pocType == 0 {
		r.readUE() // log2_max_pic_order_cnt_lsb_minus4
	} else if pocType == 1 {
		r.readBit() // delta_pic_order_always_zero_flag
		r.readSE()  // offset_for_non_ref_pic
		numRefFramesInPOC := r.readUE()
		for range numRefFramesInPOC {
			r.readSE() // offset_for_ref_frame
		}
	}
	// pocType == 2: no additional fields

	// max_num_ref_frames (ue(v))
	r.readUE()

	// gaps_in_frame_num_value_allowed_flag (u(1))
	r.readBit()

	// pic_width_in_mbs_minus1 (ue(v))
	picWidthInMBs := r.readUE() + 1
	if r.overran {
		return 0, 0
	}
	width = int(picWidthInMBs) * 16
	// pic_height_in_map_units_minus1 (ue(v))
	picHeightInMapUnits := r.readUE() + 1
	if r.overran {
		return width, 0
	}

	// frame_mbs_only_flag (u(1))
	frameMBsOnlyFlag := r.readBit()
	heightInMBs := picHeightInMapUnits
	if frameMBsOnlyFlag == 0 {
		// Interlaced: each map unit is a field macroblock pair.
		heightInMBs = picHeightInMapUnits * 2
	}
	height = int(heightInMBs) * 16

	// mb_adaptive_frame_field_flag (only when frame_mbs_only_flag == 0)
	if frameMBsOnlyFlag == 0 {
		r.readBit() // mb_adaptive_frame_field_flag
	}

	// direct_8x8_inference_flag (u(1))
	r.readBit()

	// frame_cropping_flag
	croppingFlag := r.readBit()
	if croppingFlag != 0 {
		// frame_crop_left_offset (ue(v))
		cropLeft := r.readUE()
		// frame_crop_right_offset (ue(v))
		cropRight := r.readUE()
		// frame_crop_top_offset (ue(v))
		cropTop := r.readUE()
		// frame_crop_bottom_offset (ue(v))
		cropBottom := r.readUE()

		// For 4:2:0 chroma (assumed), crop units are 2 for horizontal and 2 for vertical.
		width -= int(cropLeft+cropRight) * 2
		height -= int(cropTop+cropBottom) * 2
	}

	return width, height
}
