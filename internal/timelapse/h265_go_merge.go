// Package timelapse — Pure Go H.265/HEVC NAL → MP4 muxer.
//
// H265GoMerger converts raw H.265 keyframe files (Annex-B format with
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

func init() {
	// Register hvc1 VisualSampleEntry with go-mp4 so that mp4.Marshal works
	// for H.265/HEVC sample entries.
	mp4.AddAnyTypeBoxDef(&mp4.VisualSampleEntry{}, mp4.StrToBoxType("hvc1"))
}

// H265GoMerger implements TimelapseMerger using pure Go to create an MP4 file
// from raw H.265 IDR keyframe files. Each frame file contains one access unit
// with multiple NAL units (VPS, SPS, PPS, IDR slice) in Annex-B format using
// 0x00000001 start codes.
type H265GoMerger struct{}

// NewH265GoMerger creates a new H265GoMerger.
func NewH265GoMerger() *H265GoMerger {
	return &H265GoMerger{}
}

// CanMerge always returns true since this is a pure Go implementation.
func (m *H265GoMerger) CanMerge() bool {
	return true
}

// Tier returns the merge tier identifier.
func (m *H265GoMerger) Tier() MergeTier {
	return TierGo
}

// Merge reads H.265 keyframe files from framesDir, builds an MP4 file at
// outputPath with the given fps, and returns a MergeResult.
func (m *H265GoMerger) Merge(ctx context.Context, framesDir, outputPath string, fps int) (*MergeResult, error) {
	// List and sort frame files.
	frames, err := listCodecFrameFiles(framesDir, "h265")
	if err != nil {
		return &MergeResult{Tier: TierGo, Error: err.Error()}, err
	}
	if len(frames) == 0 {
		err := fmt.Errorf("no H.265 frames found in %s", framesDir)
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

	// Scan frames for VPS, SPS, and PPS — parameter sets may arrive in separate
	// AUs from IDR frames, so they might not be in the first frame file.
	// Scan up to 50 frames to find them.
	var vps, sps, pps []byte
	scanLimit := 50
	if len(frames) < scanLimit {
		scanLimit = len(frames)
	}
	for i := 0; i < scanLimit && (vps == nil || sps == nil || pps == nil); i++ {
		frameData, err := os.ReadFile(frames[i])
		if err != nil {
			continue
		}
		for _, nalu := range splitAnnexB(frameData) {
			if len(nalu) == 0 {
				continue
			}
			// H.265 NAL type: (first_byte >> 1) & 0x3F
			nalType := (nalu[0] >> 1) & 0x3F
			switch nalType {
			case 32: // VPS
				if vps == nil {
					vps = nalu
				}
			case 33: // SPS
				if sps == nil {
					sps = nalu
				}
			case 34: // PPS
				if pps == nil {
					pps = nalu
				}
			}
		}
	}

	if vps == nil {
		return &MergeResult{Tier: TierGo, Error: "frames missing VPS"},
			fmt.Errorf("frames missing VPS")
	}
	if sps == nil {
		return &MergeResult{Tier: TierGo, Error: "frames missing SPS"},
			fmt.Errorf("frames missing SPS")
	}
	if pps == nil {
		return &MergeResult{Tier: TierGo, Error: "frames missing PPS"},
			fmt.Errorf("frames missing PPS")
	}
	// Build hvcC decoder configuration from VPS/SPS/PPS.
	hvcCData := buildHvcC(vps, sps, pps)

	// Parse dimensions from SPS.
	width, height := parseH265Dimensions(sps)
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
			// Skip parameter sets — they belong in hvcC only, not in sample data.
			if isH265ParamSet(nalu) {
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
		decoderConfigData: hvcCData,
		sampleDur:         sampleDuration,
		sampleEntryType:   [4]byte{'h', 'v', 'c', '1'},
		configBoxType:     [4]byte{'h', 'v', 'c', 'C'},
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
	ftypSize, err := writeCodecFtyp(w, [4]byte{'h', 'v', 'c', '1'})
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

	// Write mdat box (strips VPS/SPS/PPS from samples — they are in hvcC only).
	if err := writeCodecMdat(w, frames, ctx, isH265ParamSet); err != nil {
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
		Codec:        model.TimelapseMergeCodecH265,
	}, nil
}

// --- Helpers ---

// buildHvcC builds the HEVCDecoderConfigurationRecord bytes from raw VPS, SPS,
// and PPS NAL units (without start codes). Format per ISO 14496-15.
//
// This is a thin wrapper over mp4util.BuildHvcC (the shared, single source of
// truth for the hvcC record used by timelapse, merge, and muxer — see #236).
// The shared helper uses conservative Main-tier / Main-profile / zeroed-compat
// defaults rather than fully parsing the SPS, because several H.265 ONVIF
// cameras in production (cam-fa049182 工作室内, cam-5b24e253 视通) emit an SPS
// whose profile_tier_level is internally inconsistent (e.g. profile_idc=1 Main
// paired with tier=1 High + a stray compat bit 0x40000000) — Windows Edge's
// HEVC Video Extension enforces strict HEVC conformance and refuses to play
// such an MP4 (DEMUXER_ERROR_NO_SUPPORTED_STREAMS). Marshaling the typed
// *mp4.HvcC struct produces byte-identical output to the previous hand-rolled
// buffer because go-mp4 bit-packs GeneralProfileIdc as a 5-bit field, so the
// low 5 bits of sps[1] reach byte 1 exactly as the old `sps[1] & 0x1F` did.
//
// Returns the marshaled bytes so callers (and tests) keep working with the same
// []byte contract; the struct is the construction mechanism only.
func buildHvcC(vps, sps, pps []byte) []byte {
	var buf bytes.Buffer
	if _, err := mp4.Marshal(&buf, mp4util.BuildHvcC(vps, sps, pps), mp4.Context{}); err != nil {
		// Marshal of a fully-populated *mp4.HvcC never fails in practice (all
		// fields are concrete, no dynamic lengths beyond the fixed NAL arrays).
		// Return an empty record on the impossible failure so the caller
		// surfaces a clearly broken (unplayable) file rather than panicking.
		return nil
	}
	return buf.Bytes()
}

// isH265ParamSet returns true if the NAL unit is a parameter set (VPS, SPS, or PPS).
// These must only appear in the hvcC box, not in sample data.
func isH265ParamSet(nalu []byte) bool {
	if len(nalu) == 0 {
		return false
	}
	nalType := (nalu[0] >> 1) & 0x3F
	return nalType == 32 || nalType == 33 || nalType == 34 // VPS, SPS, PPS
}

// --- SPS parsing for H.265 ---

// parseH265Dimensions extracts width and height from a raw H.265 SPS NAL unit.
// Returns 0, 0 if parsing fails.
func parseH265Dimensions(sps []byte) (width, height int) {
	if len(sps) < 15 {
		return 0, 0
	}

	// Remove emulation prevention bytes for accurate parsing.
	data := removeEmulationPrevention(sps)

	r := &bitReader{data: data}

	// Skip NAL header (2 bytes).
	for range 16 {
		r.readBit()
	}

	// sps_video_parameter_set_id (4 bits)
	r.readBits(4)
	// sps_max_sub_layers_minus1 (3 bits)
	maxSubLayers := int(r.readBits(3))
	// sps_temporal_id_nesting_flag (1 bit)
	r.readBit()

	if r.overran {
		return 0, 0
	}

	// profile_tier_level( maxSubLayers )
	// Skip: profile_space(2) + tier_flag(1) + profile_idc(5) = 8 bits
	// + compat_flags(32) + constraint_flags(48) + level_idc(8) = 88 bits
	// Total fixed portion: 96 bits = 12 bytes
	for range 96 {
		r.readBit()
	}

	if r.overran {
		return 0, 0
	}

	// Skip sub-layer profile/level if maxSubLayers > 0.
	if maxSubLayers > 0 {
		subLayerFlags := make([]struct {
			profilePresent bool
			levelPresent   bool
		}, maxSubLayers)
		for i := range maxSubLayers - 1 {
			subLayerFlags[i].profilePresent = r.readBit() == 1
			subLayerFlags[i].levelPresent = r.readBit() == 1
		}
		for i := range maxSubLayers - 1 {
			if subLayerFlags[i].profilePresent {
				// profile_tier_level(0) = 96 bits
				for range 96 {
					r.readBit()
				}
			}
			if subLayerFlags[i].levelPresent {
				r.readBits(8)
			}
		}
	}

	if r.overran {
		return 0, 0
	}

	// sps_seq_parameter_set_id (ue(v))
	r.readUE()

	// chroma_format_idc (ue(v))
	chromaFormatIDC := int(r.readUE())

	if r.overran {
		return 0, 0
	}

	if chromaFormatIDC == 3 {
		r.readBit() // separate_colour_plane_flag
	}

	// pic_width_in_luma_samples (ue(v))
	width = int(r.readUE())
	// pic_height_in_luma_samples (ue(v))
	height = int(r.readUE())

	if r.overran {
		return 0, 0
	}

	// conformance_window_flag
	cropFlag := r.readBit()
	if cropFlag != 0 {
		cropLeft := int(r.readUE())
		cropRight := int(r.readUE())
		cropTop := int(r.readUE())
		cropBottom := int(r.readUE())

		// SubWidthC and SubHeightC based on chroma format.
		subWidthC := 1
		subHeightC := 1
		switch chromaFormatIDC {
		case 1: // 4:2:0
			subWidthC = 2
			subHeightC = 2
		case 2: // 4:2:2
			subWidthC = 2
			subHeightC = 1
		case 3: // 4:4:4
			subWidthC = 1
			subHeightC = 1
		}

		width -= (cropLeft + cropRight) * subWidthC
		height -= (cropTop + cropBottom) * subHeightC
	}

	if r.overran {
		return 0, 0
	}

	return width, height
}

// Ensure H265GoMerger satisfies the TimelapseMerger interface.
var _ TimelapseMerger = (*H265GoMerger)(nil)
