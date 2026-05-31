package wsstream

import (
	"bytes"
	"testing"
)

// ─── test fixtures ───────────────────────────────────────────────────

// syntheticSPS is a minimal valid H.264 SPS (AVC, Baseline 3.0, 640x480).
// Generated from a standard encoder config.
var syntheticSPS = []byte{
	0x67, 0x42, 0x00, 0x1e, 0x99, 0xa0, 0x30, 0x28,
	0xd0, 0x80, 0x00, 0x00, 0x03, 0x00, 0x02, 0x00,
	0x00, 0x03, 0x00, 0xc8, 0x1e, 0x25, 0x3c, 0x60,
}

// syntheticPPS is a minimal valid H.264 PPS.
var syntheticPPS = []byte{
	0x68, 0xeb, 0xe3, 0xcb, 0x22, 0xc0,
}

// syntheticH265VPS is a minimal valid H.265 VPS.
var syntheticH265VPS = []byte{
	0x40, 0x01, 0x0c, 0x01, 0xff, 0xff, 0x01, 0x60,
	0x00, 0x00, 0x03, 0x00, 0x00, 0x03, 0x00, 0x00,
	0x03, 0x00, 0x00, 0x03, 0x00, 0x6c, 0x1e, 0x40,
}

// syntheticH265SPS is a minimal valid H.265 SPS.
var syntheticH265SPS = []byte{
	0x42, 0x01, 0x01, 0x01, 0x60, 0x00, 0x00, 0x03,
	0x00, 0x00, 0x03, 0x00, 0x00, 0x03, 0x00, 0x00,
	0x03, 0x00, 0x6c, 0x1e, 0x40,
}

// syntheticH265PPS is a minimal valid H.265 PPS.
var syntheticH265PPS = []byte{
	0x44, 0x01, 0xc1, 0x73, 0xd1,
}

// ─── CodecInfo round-trip tests ──────────────────────────────────────

func TestEncodeDecodeCodecInfo_H264(t *testing.T) {
	ci := &CodecInfo{
		Codec:   CodecH264,
		Profile: 0x42, // Baseline profile
		Level:   0x1e, // Level 3.0
		SPS:     syntheticSPS,
		PPS:     syntheticPPS,
	}

	encoded, err := EncodeCodecInfo(ci)
	if err != nil {
		t.Fatalf("EncodeCodecInfo: %v", err)
	}

	decoded, err := DecodeCodecInfo(encoded)
	if err != nil {
		t.Fatalf("DecodeCodecInfo: %v", err)
	}

	if decoded.Codec != CodecH264 {
		t.Errorf("Codec = %q, want %q", decoded.Codec, CodecH264)
	}
	if decoded.Profile != ci.Profile {
		t.Errorf("Profile = %d, want %d", decoded.Profile, ci.Profile)
	}
	if decoded.Level != ci.Level {
		t.Errorf("Level = %d, want %d", decoded.Level, ci.Level)
	}
	if !bytes.Equal(decoded.SPS, syntheticSPS) {
		t.Errorf("SPS mismatch")
	}
	if !bytes.Equal(decoded.PPS, syntheticPPS) {
		t.Errorf("PPS mismatch")
	}
	if len(decoded.VPS) != 0 {
		t.Errorf("expected empty VPS for H.264, got %d bytes", len(decoded.VPS))
	}
}

func TestEncodeDecodeCodecInfo_H265(t *testing.T) {
	ci := &CodecInfo{
		Codec:   CodecH265,
		Profile: 0x01, // Main profile
		Level:   0x5d, // Level 3.1
		SPS:     syntheticH265SPS,
		PPS:     syntheticH265PPS,
		VPS:     syntheticH265VPS,
	}

	encoded, err := EncodeCodecInfo(ci)
	if err != nil {
		t.Fatalf("EncodeCodecInfo: %v", err)
	}

	decoded, err := DecodeCodecInfo(encoded)
	if err != nil {
		t.Fatalf("DecodeCodecInfo: %v", err)
	}

	if decoded.Codec != CodecH265 {
		t.Errorf("Codec = %q, want %q", decoded.Codec, CodecH265)
	}
	if decoded.Profile != ci.Profile {
		t.Errorf("Profile = %d, want %d", decoded.Profile, ci.Profile)
	}
	if decoded.Level != ci.Level {
		t.Errorf("Level = %d, want %d", decoded.Level, ci.Level)
	}
	if !bytes.Equal(decoded.SPS, syntheticH265SPS) {
		t.Errorf("SPS mismatch")
	}
	if !bytes.Equal(decoded.PPS, syntheticH265PPS) {
		t.Errorf("PPS mismatch")
	}
	if !bytes.Equal(decoded.VPS, syntheticH265VPS) {
		t.Errorf("VPS mismatch")
	}
}

func TestEncodeDecodeCodecInfo_Empty_NALUs(t *testing.T) {
	ci := &CodecInfo{
		Codec:   CodecH264,
		Profile: 0x42,
		Level:   0x1e,
		SPS:     []byte{},
		PPS:     []byte{},
	}

	encoded, err := EncodeCodecInfo(ci)
	if err != nil {
		t.Fatalf("EncodeCodecInfo: %v", err)
	}

	decoded, err := DecodeCodecInfo(encoded)
	if err != nil {
		t.Fatalf("DecodeCodecInfo: %v", err)
	}

	if len(decoded.SPS) != 0 {
		t.Errorf("expected empty SPS, got %d bytes", len(decoded.SPS))
	}
	if len(decoded.PPS) != 0 {
		t.Errorf("expected empty PPS, got %d bytes", len(decoded.PPS))
	}
}

func TestDecodeCodecInfo_TooShort(t *testing.T) {
	_, err := DecodeCodecInfo([]byte{0x01, 0x04, 0x00})
	if err == nil {
		t.Fatal("expected error for too-short data")
	}
}

func TestDecodeCodecInfo_WrongType(t *testing.T) {
	_, err := DecodeCodecInfo([]byte{0x02, 0x04, 0x00, 0x00, 0x00, 0x00, 0x00})
	if err == nil {
		t.Fatal("expected error for wrong message type")
	}
}

func TestDecodeCodecInfo_UnknownCodec(t *testing.T) {
	_, err := DecodeCodecInfo([]byte{0x01, 0xFF, 0x00, 0x00, 0x00, 0x00, 0x00})
	if err == nil {
		t.Fatal("expected error for unknown codec byte")
	}
}

func TestEncodeCodecInfo_Nil(t *testing.T) {
	_, err := EncodeCodecInfo(nil)
	if err == nil {
		t.Fatal("expected error for nil CodecInfo")
	}
}

// ─── VideoFrame round-trip tests ─────────────────────────────────────

func TestEncodeDecodeVideoFrame_Keyframe(t *testing.T) {
	vf := &VideoFrame{
		PTS:        2700000, // 30 seconds at 90kHz
		IsKeyframe: true,
		NALUs: [][]byte{
			{0x65, 0x88, 0x84, 0x00, 0x01, 0x0f}, // IDR slice
			{0x61, 0x02, 0x03},                     // non-IDR slice
		},
	}

	encoded, err := EncodeVideoFrame(vf)
	if err != nil {
		t.Fatalf("EncodeVideoFrame: %v", err)
	}

	decoded, err := DecodeVideoFrame(encoded)
	if err != nil {
		t.Fatalf("DecodeVideoFrame: %v", err)
	}

	if decoded.PTS != 2700000 {
		t.Errorf("PTS = %d, want %d", decoded.PTS, 2700000)
	}
	if !decoded.IsKeyframe {
		t.Errorf("IsKeyframe = false, want true")
	}
	if len(decoded.NALUs) != 2 {
		t.Fatalf("got %d NALUs, want 2", len(decoded.NALUs))
	}
	if !bytes.Equal(decoded.NALUs[0], vf.NALUs[0]) {
		t.Errorf("NALU[0] mismatch")
	}
	if !bytes.Equal(decoded.NALUs[1], vf.NALUs[1]) {
		t.Errorf("NALU[1] mismatch")
	}
}

func TestEncodeDecodeVideoFrame_Delta(t *testing.T) {
	vf := &VideoFrame{
		PTS:        90000, // 1 second at 90kHz
		IsKeyframe: false,
		NALUs: [][]byte{
			{0x41, 0x9a, 0x22, 0x10},
		},
	}

	encoded, err := EncodeVideoFrame(vf)
	if err != nil {
		t.Fatalf("EncodeVideoFrame: %v", err)
	}

	decoded, err := DecodeVideoFrame(encoded)
	if err != nil {
		t.Fatalf("DecodeVideoFrame: %v", err)
	}

	if decoded.PTS != 90000 {
		t.Errorf("PTS = %d, want %d", decoded.PTS, 90000)
	}
	if decoded.IsKeyframe {
		t.Errorf("IsKeyframe = true, want false")
	}
	if len(decoded.NALUs) != 1 {
		t.Fatalf("got %d NALUs, want 1", len(decoded.NALUs))
	}
	if !bytes.Equal(decoded.NALUs[0], vf.NALUs[0]) {
		t.Errorf("NALU[0] mismatch")
	}
}

func TestEncodeDecodeVideoFrame_ZeroNALUs(t *testing.T) {
	vf := &VideoFrame{
		PTS:        0,
		IsKeyframe: false,
		NALUs:      [][]byte{},
	}

	encoded, err := EncodeVideoFrame(vf)
	if err != nil {
		t.Fatalf("EncodeVideoFrame: %v", err)
	}

	decoded, err := DecodeVideoFrame(encoded)
	if err != nil {
		t.Fatalf("DecodeVideoFrame: %v", err)
	}

	if decoded.PTS != 0 {
		t.Errorf("PTS = %d, want 0", decoded.PTS)
	}
	if len(decoded.NALUs) != 0 {
		t.Errorf("got %d NALUs, want 0", len(decoded.NALUs))
	}
}

func TestEncodeDecodeVideoFrame_LargePTS(t *testing.T) {
	vf := &VideoFrame{
		PTS:        0x7FFFFFFFFFFFFFFF, // max int64
		IsKeyframe: false,
		NALUs: [][]byte{
			{0x01, 0x02, 0x03},
		},
	}

	encoded, err := EncodeVideoFrame(vf)
	if err != nil {
		t.Fatalf("EncodeVideoFrame: %v", err)
	}

	decoded, err := DecodeVideoFrame(encoded)
	if err != nil {
		t.Fatalf("DecodeVideoFrame: %v", err)
	}

	if decoded.PTS != vf.PTS {
		t.Errorf("PTS = %d, want %d", decoded.PTS, vf.PTS)
	}
}

func TestDecodeVideoFrame_TooShort(t *testing.T) {
	_, err := DecodeVideoFrame([]byte{0x02, 0x00, 0x00, 0x00})
	if err == nil {
		t.Fatal("expected error for too-short data")
	}
}

func TestDecodeVideoFrame_WrongType(t *testing.T) {
	_, err := DecodeVideoFrame([]byte{0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00})
	if err == nil {
		t.Fatal("expected error for wrong message type")
	}
}

func TestEncodeVideoFrame_Nil(t *testing.T) {
	_, err := EncodeVideoFrame(nil)
	if err == nil {
		t.Fatal("expected error for nil VideoFrame")
	}
}

func TestEncodeVideoFrame_TooManyNALUs(t *testing.T) {
	vf := &VideoFrame{
		PTS:        0,
		IsKeyframe: false,
		NALUs:      make([][]byte, 70000), // > 65535
	}
	_, err := EncodeVideoFrame(vf)
	if err == nil {
		t.Fatal("expected error for too many NALUs")
	}
}

// ─── Edge cases: empty slices ────────────────────────────────────────

func TestEncodeCodecInfo_H264_EmptySPS(t *testing.T) {
	ci := &CodecInfo{
		Codec:   CodecH264,
		Profile: 0x42,
		Level:   0x1e,
		SPS:     []byte{},
		PPS:     syntheticPPS,
	}

	encoded, err := EncodeCodecInfo(ci)
	if err != nil {
		t.Fatalf("EncodeCodecInfo: %v", err)
	}

	decoded, err := DecodeCodecInfo(encoded)
	if err != nil {
		t.Fatalf("DecodeCodecInfo: %v", err)
	}

	if len(decoded.SPS) != 0 {
		t.Errorf("expected empty SPS, got %d bytes", len(decoded.SPS))
	}
	if !bytes.Equal(decoded.PPS, syntheticPPS) {
		t.Errorf("PPS mismatch")
	}
}
