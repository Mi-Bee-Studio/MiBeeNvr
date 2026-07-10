package flv

import (
	"encoding/binary"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
)

// FLV tag type constants.
const (
	tagTypeAudio = 0x08
	tagTypeVideo = 0x09
)

// Video codec IDs in FLV.
const (
	codecIDAVC  = 0x07 // H.264
	codecIDHEVC = 0x0C // H.265 (extended codec ID, used with 0x1C frame type)
)

// AVC/HEVC packet types.
const (
	avcPacketTypeSequenceHeader = 0x00
	avcPacketTypeNALU           = 0x01
	avcPacketTypeEndOfSequence  = 0x02
)

// Frame type masks.
const (
	frameTypeKeyframe   = 0x10
	frameTypeInterFrame = 0x20
)

// flvHeader returns the 9-byte FLV file header.
// Signature: "FLV", Version: 1, Flags: 0x05 (audio+video), HeaderSize: 9.
func flvHeader() []byte {
	return []byte{
		'F', 'L', 'V', // signature
		0x01,                   // version
		0x05,                   // flags: hasAudio(0x04) + hasVideo(0x01)
		0x00, 0x00, 0x00, 0x09, // header size = 9
	}
}

// previousTagSize0 returns the 4-byte PreviousTagSize0 (always 0).
func previousTagSize0() []byte {
	return []byte{0x00, 0x00, 0x00, 0x00}
}

// flvTag builds a complete FLV tag: header (11 bytes) + data + previous tag size (4 bytes).
// tagType: 0x08 (audio), 0x09 (video)
// timestamp: PTS in milliseconds
// data: tag payload (after the 11-byte tag header)
func flvTag(tagType byte, timestamp int64, data []byte) []byte {
	dataSize := len(data)
	totalSize := 11 + dataSize + 4 // header + data + prevTagSize

	buf := make([]byte, totalSize)

	// Tag header (11 bytes)
	buf[0] = tagType
	buf[1] = byte(dataSize >> 16)
	buf[2] = byte(dataSize >> 8)
	buf[3] = byte(dataSize)
	// Timestamp (3 bytes) + TimestampExtended (1 byte)
	ts := timestamp & 0xFFFFFF
	buf[4] = byte(ts >> 16)
	buf[5] = byte(ts >> 8)
	buf[6] = byte(ts)
	buf[7] = byte(timestamp >> 24) // timestamp extended
	// StreamID: always 0
	buf[8] = 0x00
	buf[9] = 0x00
	buf[10] = 0x00

	// Data
	copy(buf[11:], data)

	// Previous tag size (4 bytes big-endian)
	prevSize := uint32(11 + dataSize)
	binary.BigEndian.PutUint32(buf[11+dataSize:], prevSize)

	return buf
}

// h264SequenceHeader creates an FLV video tag containing AVCDecoderConfigurationRecord.
// This is sent once at stream start to configure the decoder.
func h264SequenceHeader(sps, pps []byte) []byte {
	// AVCDecoderConfigurationRecord
	config := make([]byte, 0, 64)
	config = append(config, 0x01)   // configurationVersion
	config = append(config, sps[1]) // AVCProfileIndication
	config = append(config, sps[2]) // profile_compatibility
	config = append(config, sps[3]) // AVCLevelIndication
	config = append(config, 0xFF)   // lengthSizeMinusOne = 3 (4-byte NALU length) + reserved bits
	config = append(config, 0xE1)   // numOfSequenceParameterSets = 1 + reserved bits

	// SPS with 2-byte length prefix
	spsLen := uint16(len(sps))
	config = append(config, byte(spsLen>>8), byte(spsLen))
	config = append(config, sps...)

	// numOfPictureParameterSets = 1
	config = append(config, 0x01)

	// PPS with 2-byte length prefix
	ppsLen := uint16(len(pps))
	config = append(config, byte(ppsLen>>8), byte(ppsLen))
	config = append(config, pps...)

	// Video tag data: FrameType(4) + CodecID(4) + AVCPacketType + CompositionTime(3) + config
	videoData := make([]byte, 0, 5+len(config))
	videoData = append(videoData, frameTypeKeyframe|codecIDAVC) // 0x17
	videoData = append(videoData, avcPacketTypeSequenceHeader)  // 0x00
	videoData = append(videoData, 0x00, 0x00, 0x00)             // composition time offset = 0
	videoData = append(videoData, config...)

	return flvTag(tagTypeVideo, 0, videoData)
}

// h265SequenceHeader creates an FLV video tag containing HEVCDecoderConfigurationRecord.
// This is the HEVC equivalent of AVCDecoderConfigurationRecord.
func h265SequenceHeader(vps, sps, pps []byte) []byte {
	// HEVCDecoderConfigurationRecord (ISO 14496-15)
	config := make([]byte, 0, 128)
	config = append(config, 0x01) // configurationVersion

	// general_profile_space(2) + general_tier_flag(1) + general_profile_idc(5)
	// Use profile from SPS if available
	profile := byte(0x01) // Main profile default
	if len(sps) > 3 {
		profile = (sps[1] >> 1) & 0x1F
	}
	config = append(config, profile)

	// general_profile_compatibility_flags (4 bytes)
	config = append(config, 0x60, 0x00, 0x00, 0x00)

	// general_constraint_indicator_flags (6 bytes)
	config = append(config, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00)

	// general_level_idc
	levelIDC := byte(93) // Level 3.1 default
	config = append(config, levelIDC)

	// min_spatial_segmentation_idc with reserved bits (2 bytes)
	config = append(config, 0xF0, 0x00)

	// parallelismType with reserved bits (1 byte)
	config = append(config, 0xFC)

	// chromaFormat with reserved bits (1 byte)
	config = append(config, 0xFD) // chroma 4:2:0 = 1, with reserved F

	// bitDepthLumaMinus8 with reserved bits (1 byte)
	config = append(config, 0xF8) // 8-bit = 0, with reserved F8

	// bitDepthChromaMinus8 with reserved bits (1 byte)
	config = append(config, 0xF8)

	// avgFrameRate (2 bytes)
	config = append(config, 0x00, 0x00)

	// constantFrameRate(2) + numTemporalLayers(3) + temporalIdNested(1) + lengthSizeMinusOne(2)
	// = 0x03 | (1<<2) | (1<<1) | 0x03 = constantFrameRate=0, numTemporalLayers=1,
	//   temporalIdNested=1, lengthSizeMinusOne=3
	config = append(config, 0x0F) // 0000_11_1_1 = 0x07, but with constFR=0 → 0x03|0x0C = 0x0F

	// numOfArrays = 3 (VPS, SPS, PPS)
	config = append(config, 0x03)

	// Array 1: VPS
	config = append(config, 0x20|0x80) // array_completeness=1 + NAL_unit_type=32 (VPS)
	vpsLen := uint16(len(vps))
	config = append(config, 0x00, 0x01) // numNalus = 1
	config = append(config, byte(vpsLen>>8), byte(vpsLen))
	config = append(config, vps...)

	// Array 2: SPS
	config = append(config, 0x21|0x80) // NAL_unit_type=33 (SPS)
	spsLen := uint16(len(sps))
	config = append(config, 0x00, 0x01)
	config = append(config, byte(spsLen>>8), byte(spsLen))
	config = append(config, sps...)

	// Array 3: PPS
	config = append(config, 0x22|0x80) // NAL_unit_type=34 (PPS)
	ppsLen := uint16(len(pps))
	config = append(config, 0x00, 0x01)
	config = append(config, byte(ppsLen>>8), byte(ppsLen))
	config = append(config, pps...)

	// Video tag data: FrameType(4) + CodecID(4) + HEVCPacketType + CompositionTime(3) + config
	videoData := make([]byte, 0, 5+len(config))
	videoData = append(videoData, frameTypeKeyframe|codecIDHEVC) // 0x1C
	videoData = append(videoData, avcPacketTypeSequenceHeader)   // 0x00
	videoData = append(videoData, 0x00, 0x00, 0x00)              // composition time offset
	videoData = append(videoData, config...)

	return flvTag(tagTypeVideo, 0, videoData)
}

// videoFrameTag creates an FLV video tag for a frame NALU.
// codec: H.264 or H.265
// nalus: Access Unit (array of NALUs, already without start codes)
// pts: presentation timestamp in 90kHz clock
// isKeyframe: true for IDR frames
//
// Allocates the final tag buffer in a single pass (was 3 allocations: payload slice,
// videoData slice, then the flvTag buffer). The shared buffer is sent to multiple viewers
// and cached in gopCache, so a sync.Pool is unsafe here — but collapsing the build into
// one allocation still cuts per-frame GC pressure by ~⅔.
func videoFrameTag(codec model.Format, nalus [][]byte, pts int64, isKeyframe bool) []byte {
	// Convert PTS from 90kHz to milliseconds
	tsMs := pts / 90

	// Compute the total payload size up front so we allocate the full FLV tag once.
	// Layout: [tag header 11][frameType+codecID 1][packetType 1][compositionTime 3]
	//         [for each NALU: 4-byte length + nalu][prevTagSize 4]
	naluTotal := 0
	for _, nalu := range nalus {
		naluTotal += 4 + len(nalu)
	}
	const headerAndMeta = 11 + 5 // tag header(11) + frameType(1)+packetType(1)+compTime(3)
	totalSize := headerAndMeta + naluTotal + 4

	buf := make([]byte, totalSize)
	// Tag header (11 bytes)
	buf[0] = tagTypeVideo
	dataSize := 5 + naluTotal // video tag data size (meta + payload)
	buf[1] = byte(dataSize >> 16)
	buf[2] = byte(dataSize >> 8)
	buf[3] = byte(dataSize)
	ts := tsMs & 0xFFFFFF
	buf[4] = byte(ts >> 16)
	buf[5] = byte(ts >> 8)
	buf[6] = byte(ts)
	buf[7] = byte(tsMs >> 24) // timestamp extended
	// StreamID: always 0
	buf[8] = 0x00
	buf[9] = 0x00
	buf[10] = 0x00

	// Video tag data starting at offset 11: frameType+codecID + packetType + compositionTime
	var frameTypeAndCodec byte
	switch codec {
	case model.FormatH265:
		if isKeyframe {
			frameTypeAndCodec = frameTypeKeyframe | codecIDHEVC // 0x1C
		} else {
			frameTypeAndCodec = frameTypeInterFrame | codecIDHEVC // 0x2C
		}
	default: // H.264
		if isKeyframe {
			frameTypeAndCodec = frameTypeKeyframe | codecIDAVC // 0x17
		} else {
			frameTypeAndCodec = frameTypeInterFrame | codecIDAVC // 0x27
		}
	}
	buf[11] = frameTypeAndCodec
	buf[12] = avcPacketTypeNALU // 0x01
	buf[13] = 0x00              // composition time offset (3 bytes, 0 for live)
	buf[14] = 0x00
	buf[15] = 0x00

	// AVCC/HVCC payload: NALU data with 4-byte length prefixes, written directly into buf
	off := 16
	for _, nalu := range nalus {
		naluLen := uint32(len(nalu))
		buf[off] = byte(naluLen >> 24)
		buf[off+1] = byte(naluLen >> 16)
		buf[off+2] = byte(naluLen >> 8)
		buf[off+3] = byte(naluLen)
		off += 4
		copy(buf[off:], nalu)
		off += len(nalu)
	}

	// Previous tag size (4 bytes big-endian)
	binary.BigEndian.PutUint32(buf[off:], uint32(11+dataSize))
	return buf
}
