package wsstream

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// ─── codec byte helpers ──────────────────────────────────────────────

func codecByte(codec string) (byte, error) {
	switch codec {
	case CodecH264:
		return 4, nil
	case CodecH265:
		return 5, nil
	default:
		return 0, fmt.Errorf("wsstream: unknown codec %q", codec)
	}
}

func codecFromByte(b byte) (string, error) {
	switch b {
	case 4:
		return CodecH264, nil
	case 5:
		return CodecH265, nil
	default:
		return "", fmt.Errorf("wsstream: unknown codec byte 0x%02x", b)
	}
}

// ─── CodecInfo encode/decode ─────────────────────────────────────────

// EncodeCodecInfo encodes a CodecInfo into binary wire format.
//
// Wire format:
//
//	{type:1}{codec:1}{profile:1}{level:1}{sps_len:2}{sps}{pps_len:2}{pps}[vps_len:2][vps]
//
// All multi-byte integers are big-endian.
// codec byte: 4 = H.264, 5 = H.265.
// vps fields are only present for H.265.
func EncodeCodecInfo(ci *CodecInfo) ([]byte, error) {
	if ci == nil {
		return nil, errors.New("wsstream: nil CodecInfo")
	}

	cb, err := codecByte(ci.Codec)
	if err != nil {
		return nil, err
	}

	// type + codec + profile + level + sps_len + sps + pps_len + pps
	size := 1 + 1 + 1 + 1 + 2 + len(ci.SPS) + 2 + len(ci.PPS)
	if ci.Codec == CodecH265 {
		size += 2 + len(ci.VPS) // vps_len + vps
	}

	buf := make([]byte, size)

	offset := 0
	buf[offset] = MsgTypeCodecInfo
	offset++

	buf[offset] = cb
	offset++

	buf[offset] = ci.Profile
	offset++

	buf[offset] = ci.Level
	offset++

	// SPS length + data
	binary.BigEndian.PutUint16(buf[offset:], uint16(len(ci.SPS)))
	offset += 2
	copy(buf[offset:], ci.SPS)
	offset += len(ci.SPS)

	// PPS length + data
	binary.BigEndian.PutUint16(buf[offset:], uint16(len(ci.PPS)))
	offset += 2
	copy(buf[offset:], ci.PPS)
	offset += len(ci.PPS)

	// VPS length + data (H.265 only)
	if ci.Codec == CodecH265 {
		binary.BigEndian.PutUint16(buf[offset:], uint16(len(ci.VPS)))
		offset += 2
		copy(buf[offset:], ci.VPS)
		offset += len(ci.VPS)
	}

	return buf, nil
}

// DecodeCodecInfo decodes binary wire format into a CodecInfo.
func DecodeCodecInfo(data []byte) (*CodecInfo, error) {
	if len(data) < 5 {
		return nil, fmt.Errorf("wsstream: codec info too short: %d bytes", len(data))
	}

	if data[0] != MsgTypeCodecInfo {
		return nil, fmt.Errorf("wsstream: expected message type 0x01, got 0x%02x", data[0])
	}

	codec, err := codecFromByte(data[1])
	if err != nil {
		return nil, err
	}

	ci := &CodecInfo{
		Codec:   codec,
		Profile: data[2],
		Level:   data[3],
	}

	offset := 4

	// SPS
	if offset+2 > len(data) {
		return nil, fmt.Errorf("wsstream: codec info truncated at SPS length")
	}
	spsLen := int(binary.BigEndian.Uint16(data[offset:]))
	offset += 2
	if offset+spsLen > len(data) {
		return nil, fmt.Errorf("wsstream: codec info truncated at SPS data")
	}
	ci.SPS = make([]byte, spsLen)
	copy(ci.SPS, data[offset:offset+spsLen])
	offset += spsLen

	// PPS
	if offset+2 > len(data) {
		return nil, fmt.Errorf("wsstream: codec info truncated at PPS length")
	}
	ppsLen := int(binary.BigEndian.Uint16(data[offset:]))
	offset += 2
	if offset+ppsLen > len(data) {
		return nil, fmt.Errorf("wsstream: codec info truncated at PPS data")
	}
	ci.PPS = make([]byte, ppsLen)
	copy(ci.PPS, data[offset:offset+ppsLen])
	offset += ppsLen

	// VPS (H.265 only)
	if codec == CodecH265 {
		if offset+2 > len(data) {
			return nil, fmt.Errorf("wsstream: codec info truncated at VPS length")
		}
		vpsLen := int(binary.BigEndian.Uint16(data[offset:]))
		offset += 2
		if offset+vpsLen > len(data) {
			return nil, fmt.Errorf("wsstream: codec info truncated at VPS data")
		}
		ci.VPS = make([]byte, vpsLen)
		copy(ci.VPS, data[offset:offset+vpsLen])
		offset += vpsLen
	}

	return ci, nil
}

// ─── VideoFrame encode/decode ────────────────────────────────────────

// EncodeVideoFrame encodes a VideoFrame into binary wire format.
//
// Wire format:
//
//	{type:2}{pts:8bytes_BE}{is_keyframe:1byte}{nalu_count:2bytes}{nalu1_len:4bytes}{nalu1}...
//
// All multi-byte integers are big-endian.
// PTS is in 90kHz clock (matching StreamHub convention).
// NALUs do NOT include start codes.
func EncodeVideoFrame(vf *VideoFrame) ([]byte, error) {
	if vf == nil {
		return nil, errors.New("wsstream: nil VideoFrame")
	}

	if len(vf.NALUs) > 65535 {
		return nil, fmt.Errorf("wsstream: too many NALUs: %d", len(vf.NALUs))
	}

	// type(1) + pts(8) + isKeyframe(1) + naluCount(2)
	size := 1 + 8 + 1 + 2
	for _, nalu := range vf.NALUs {
		size += 4 + len(nalu) // naluLen(4) + nalu
	}

	buf := make([]byte, size)
	offset := 0

	buf[offset] = MsgTypeVideoFrame
	offset++

	binary.BigEndian.PutUint64(buf[offset:], uint64(vf.PTS))
	offset += 8

	if vf.IsKeyframe {
		buf[offset] = 1
	} else {
		buf[offset] = 0
	}
	offset++

	binary.BigEndian.PutUint16(buf[offset:], uint16(len(vf.NALUs)))
	offset += 2

	for _, nalu := range vf.NALUs {
		binary.BigEndian.PutUint32(buf[offset:], uint32(len(nalu)))
		offset += 4
		copy(buf[offset:], nalu)
		offset += len(nalu)
	}

	return buf, nil
}

// DecodeVideoFrame decodes binary wire format into a VideoFrame.
func DecodeVideoFrame(data []byte) (*VideoFrame, error) {
	if len(data) < 12 {
		return nil, fmt.Errorf("wsstream: video frame too short: %d bytes", len(data))
	}

	if data[0] != MsgTypeVideoFrame {
		return nil, fmt.Errorf("wsstream: expected message type 0x02, got 0x%02x", data[0])
	}

	vf := &VideoFrame{
		PTS:        int64(binary.BigEndian.Uint64(data[1:9])),
		IsKeyframe: data[9] != 0,
	}

	naluCount := int(binary.BigEndian.Uint16(data[10:12]))
	offset := 12

	vf.NALUs = make([][]byte, 0, naluCount)
	for i := 0; i < naluCount; i++ {
		if offset+4 > len(data) {
			return nil, fmt.Errorf("wsstream: video frame truncated at NALU %d length", i)
		}
		naluLen := int(binary.BigEndian.Uint32(data[offset:]))
		offset += 4
		if offset+naluLen > len(data) {
			return nil, fmt.Errorf("wsstream: video frame truncated at NALU %d data", i)
		}
		nalu := make([]byte, naluLen)
		copy(nalu, data[offset:offset+naluLen])
		vf.NALUs = append(vf.NALUs, nalu)
		offset += naluLen
	}

	return vf, nil
}
