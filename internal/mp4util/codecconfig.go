// Package mp4util holds shared helpers for constructing MP4 decoder
// configuration records (avcC / hvcC) used by the timelapse, merge, and muxer
// packages.
//
// Before this package existed, three near-identical copies of the hvcC builder
// and two of the avcC builder lived across internal/timelapse (hand-rolled
// bytes.Buffer style), internal/merge, and internal/muxer (typed *mp4.HvcC /
// *mp4.AVCDecoderConfiguration style). PR #92 had to keep them byte-aligned by
// hand. This package is the single source of truth; see issue #236.
package mp4util

import (
	"github.com/abema/go-mp4"
)

// Default HEVC profile / level used when the SPS is too short to read them
// (e.g. a truncated/garbage SPS). These are the "safe" values that make a
// non-conformant stream playable by the widest set of decoders: Main profile
// (1), Level 4.0 (120). They mirror the defaults the timelapse hand-rolled
// builder always used; the merge/muxer typed builders previously fell back to
// 0/0, which is a latent bug — this package unifies on the safer Main/4.0
// defaults. See #236.
const (
	defaultHEVCProfile = 1   // Main
	defaultHEVCLevel   = 120 // Level 4.0
)

// Default AVC profile / compat / level used when the H.264 SPS is shorter than
// 4 bytes (too short to read profile_idc / compat / level). Baseline (66) /
// Level 3.0 (30) match what the timelapse hand-rolled buildAvcC always used;
// the merge/muxer inline avcC literals read sps[1..3] unguarded and would
// panic on a truncated SPS. This package unifies on the safe guarded form.
const (
	defaultAVCProfile = 66 // Baseline
	defaultAVCCompat  = 0xC0
	defaultAVCLevel   = 30 // Level 3.0
)

// BuildHvcC constructs an HEVCDecoderConfigurationRecord (*mp4.HvcC) from raw
// VPS, SPS, and PPS NAL units (Annex-B payload without the start code).
//
// The record uses conservative Main-tier defaults (tier=0, profile_space=0,
// zeroed profile-compatibility and constraint-indicator flags) regardless of
// what the SPS advertises. This is deliberate: several ONVIF cameras emit
// self-inconsistent SPS (Main profile + High tier + stray compat bits) that
// Windows Edge's HEVC extension rejects with
// DEMUXER_ERROR_NO_SUPPORTED_STREAMS. Only sps[1] (profile_idc) and sps[12]
// (level_idc) are read; everything else is forced to safe defaults. See PR #92
// and AGENTS.md "H.265 timelapse hvcC compliance".
//
// go-mp4 marshals GeneralProfileIdc as a 5-bit field (HvcC struct tag
// `mp4:"3,size=5"`), so the low 5 bits of sps[1] are what actually reaches the
// output byte; the high 3 bits flow into GeneralProfileSpace (2 bits) and
// GeneralTierFlag (1 bit), which we force to 0 — so the final byte 1 is always
// 0b00_0_PPPPP (Main tier when profile_idc=1).
func BuildHvcC(vps, sps, pps []byte) *mp4.HvcC {
	profile := uint8(defaultHEVCProfile)
	if len(sps) > 1 {
		profile = sps[1]
	}
	level := uint8(defaultHEVCLevel)
	if len(sps) > 12 {
		level = sps[12]
	}
	return &mp4.HvcC{
		ConfigurationVersion:        1,
		GeneralProfileSpace:         0,
		GeneralTierFlag:             false,
		GeneralProfileIdc:           profile,
		GeneralProfileCompatibility: [32]bool{},
		GeneralConstraintIndicator:  [6]uint8{},
		GeneralLevelIdc:             level,
		Reserved1:                   15,
		MinSpatialSegmentationIdc:   0,
		Reserved2:                   63,
		ParallelismType:             0,
		Reserved3:                   63,
		ChromaFormatIdc:             1,
		Reserved4:                   31,
		BitDepthLumaMinus8:          0,
		Reserved5:                   31,
		BitDepthChromaMinus8:        0,
		AvgFrameRate:                0,
		ConstantFrameRate:           0,
		NumTemporalLayers:           1,
		TemporalIdNested:            1,
		LengthSizeMinusOne:          3,
		NumOfNaluArrays:             3,
		NaluArrays: []mp4.HEVCNaluArray{
			{Completeness: true, NaluType: 32, NumNalus: 1, Nalus: []mp4.HEVCNalu{{Length: uint16(len(vps)), NALUnit: vps}}},
			{Completeness: true, NaluType: 33, NumNalus: 1, Nalus: []mp4.HEVCNalu{{Length: uint16(len(sps)), NALUnit: sps}}},
			{Completeness: true, NaluType: 34, NumNalus: 1, Nalus: []mp4.HEVCNalu{{Length: uint16(len(pps)), NALUnit: pps}}},
		},
	}
}

// BuildAvcC constructs an AVCDecoderConfigurationRecord
// (*mp4.AVCDecoderConfiguration) from raw SPS and PPS NAL units (Annex-B
// payload without the start code). Profile/compat/level are read from
// sps[1..3]; for H.264 sps[1] IS profile_idc (a full byte), so no masking is
// needed (unlike H.265's bit-packed profile field). When the SPS is shorter
// than 4 bytes, safe Baseline / Level 3.0 defaults are used (matching the
// former timelapse hand-rolled builder; the merge/muxer inline literals
// panicked on a truncated SPS — #236).
//
// The returned struct carries AnyTypeBox{Type: "avcC"} so mp4.Marshal emits it
// with the correct box type; callers write it as a child of the avc1 sample
// entry.
func BuildAvcC(sps, pps []byte) *mp4.AVCDecoderConfiguration {
	profile := uint8(defaultAVCProfile)
	compat := uint8(defaultAVCCompat)
	level := uint8(defaultAVCLevel)
	if len(sps) >= 4 {
		profile = sps[1]
		compat = sps[2]
		level = sps[3]
	}
	return &mp4.AVCDecoderConfiguration{
		AnyTypeBox:                 mp4.AnyTypeBox{Type: mp4.StrToBoxType("avcC")},
		ConfigurationVersion:       1,
		Profile:                    profile,
		ProfileCompatibility:       compat,
		Level:                      level,
		LengthSizeMinusOne:         3,
		NumOfSequenceParameterSets: 1,
		SequenceParameterSets: []mp4.AVCParameterSet{
			{Length: uint16(len(sps)), NALUnit: sps},
		},
		NumOfPictureParameterSets: 1,
		PictureParameterSets: []mp4.AVCParameterSet{
			{Length: uint16(len(pps)), NALUnit: pps},
		},
	}
}
