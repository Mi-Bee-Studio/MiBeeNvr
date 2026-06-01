package nalutil

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// --- H.264 Keyframe Detection ---

func TestIsKeyframeNALU_H264_IDR(t *testing.T) {
	// NAL type 5: 0x65 & 0x1F = 5
	idr := []byte{0x65, 0x88, 0x84, 0x00}
	require.True(t, IsKeyframeNALU(idr, false))
}

func TestIsKeyframeNALU_H264_NonIDR(t *testing.T) {
	// NAL type 1 (P-frame): 0x41 & 0x1F = 1
	pFrame := []byte{0x41, 0x9a, 0x21, 0x6c}
	require.False(t, IsKeyframeNALU(pFrame, false))
}

func TestIsKeyframeNALU_H264_SPS(t *testing.T) {
	// NAL type 7: 0x67 & 0x1F = 7
	sps := []byte{0x67, 0x42, 0xc0, 0x0a}
	require.False(t, IsKeyframeNALU(sps, false))
}

func TestIsKeyframeNALU_H264_PPS(t *testing.T) {
	// NAL type 8: 0x68 & 0x1F = 8
	pps := []byte{0x68, 0xce, 0x38, 0x80}
	require.False(t, IsKeyframeNALU(pps, false))
}

func TestIsKeyframeNALU_H264_SEI(t *testing.T) {
	// NAL type 6: 0x06 & 0x1F = 6
	sei := []byte{0x06, 0x01}
	require.False(t, IsKeyframeNALU(sei, false))
}

// --- H.265 Keyframe Detection ---

func TestIsKeyframeNALU_H265_IDR_W_RADL(t *testing.T) {
	// H.265 IDR_W_RADL = type 19.
	// (0x26 >> 1) = 0x13 = 19
	idr := []byte{0x26, 0x01, 0x02}
	require.True(t, IsKeyframeNALU(idr, true))
}

func TestIsKeyframeNALU_H265_IDR_N_LP(t *testing.T) {
	// H.265 IDR_N_LP = type 20.
	// (0x28 >> 1) = 0x14 = 20
	idr := []byte{0x28, 0x01, 0x02}
	require.True(t, IsKeyframeNALU(idr, true))
}

func TestIsKeyframeNALU_H265_NonIDR(t *testing.T) {
	// H.265 TRAIL_N = type 0 (non-IDR).
	// (0x00 >> 1) = 0x00 = 0
	trail := []byte{0x00, 0x01, 0x02}
	require.False(t, IsKeyframeNALU(trail, true))
}

func TestIsKeyframeNALU_H265_VPS(t *testing.T) {
	// H.265 VPS = type 32.
	// (0x40 >> 1) = 0x20 = 32
	vps := []byte{0x40, 0x01, 0x0c, 0x01}
	require.False(t, IsKeyframeNALU(vps, true))
}

func TestIsKeyframeNALU_H265_SPS(t *testing.T) {
	// H.265 SPS = type 33.
	// (0x42 >> 1) = 0x21 = 33
	sps := []byte{0x42, 0x01, 0x01, 0x01}
	require.False(t, IsKeyframeNALU(sps, true))
}

func TestIsKeyframeNALU_H265_PPS(t *testing.T) {
	// H.265 PPS = type 34.
	// (0x44 >> 1) = 0x22 = 34
	pps := []byte{0x44, 0x01, 0xc1, 0x73}
	require.False(t, IsKeyframeNALU(pps, true))
}

// H.265 with H.264 detection (wrong codec) — verifies cross-codec misdetection.
func TestIsKeyframeNALU_H265_WithH264Detection(t *testing.T) {
	// H.265 IDR_W_RADL = type 19 => nalu[0] & 0x1F = 0x26 & 0x1F = 6 (SEI), not IDR
	// This is the bug that existed in FLV's original code.
	idr := []byte{0x26, 0x01, 0x02}
	require.False(t, IsKeyframeNALU(idr, false), "H.265 IDR should NOT be detected with H.264 logic")
}

// --- Edge Cases ---

func TestIsKeyframeNALU_Nil(t *testing.T) {
	require.False(t, IsKeyframeNALU(nil, false))
	require.False(t, IsKeyframeNALU(nil, true))
}

func TestIsKeyframeNALU_Empty(t *testing.T) {
	require.False(t, IsKeyframeNALU([]byte{}, false))
	require.False(t, IsKeyframeNALU([]byte{}, true))
}

func TestIsKeyframeNALU_SingleByte(t *testing.T) {
	// H.264: 0x65 = type 5 = IDR
	require.True(t, IsKeyframeNALU([]byte{0x65}, false))
	// H.265: single byte 0x65 >> 1 = 0x32 = 50 (not IDR)
	require.False(t, IsKeyframeNALU([]byte{0x65}, true))
	// H.265: 0x26 >> 1 = 0x13 = 19 = IDR_W_RADL
	require.True(t, IsKeyframeNALU([]byte{0x26}, true))
}

// --- IsIDR (Access Unit) ---

func TestIsIDR_H264_SingleIDR(t *testing.T) {
	au := [][]byte{
		{0x67, 0x42, 0xc0},       // SPS
		{0x68, 0xce, 0x38},       // PPS
		{0x65, 0x88, 0x84, 0x00}, // IDR
	}
	require.True(t, IsIDR(au, false))
}

func TestIsIDR_H264_NoIDR(t *testing.T) {
	au := [][]byte{
		{0x67, 0x42, 0xc0}, // SPS
		{0x68, 0xce, 0x38}, // PPS
		{0x41, 0x9a, 0x21}, // P-frame
	}
	require.False(t, IsIDR(au, false))
}

func TestIsIDR_H265_SingleIDR(t *testing.T) {
	au := [][]byte{
		{0x40, 0x01},          // VPS (type 32)
		{0x42, 0x01},          // SPS (type 33)
		{0x44, 0x01},          // PPS (type 34)
		{0x26, 0x01, 0x02},    // IDR_W_RADL (type 19)
	}
	require.True(t, IsIDR(au, true))
}

func TestIsIDR_H265_NoIDR(t *testing.T) {
	au := [][]byte{
		{0x42, 0x01}, // SPS (type 33)
		{0x44, 0x01}, // PPS (type 34)
		{0x02, 0x01}, // TRAIL_R (type 1)
	}
	require.False(t, IsIDR(au, true))
}

func TestIsIDR_EmptyAU(t *testing.T) {
	require.False(t, IsIDR(nil, false))
	require.False(t, IsIDR(nil, true))
	require.False(t, IsIDR([][]byte{}, false))
	require.False(t, IsIDR([][]byte{}, true))
}

// --- Cross-codec correctness (the FLV H.265 bug) ---
//
// These tests verify that H.265 IDR frames are NOT detected as keyframes
// when using H.264 detection logic (the bug in FLV's original code).

func TestCrossCodec_H265_IDR_NotDetectedAsH264(t *testing.T) {
	tests := []struct {
		name string
		nalu []byte
	}{
		{"IDR_W_RADL low bit", []byte{0x26, 0x01}},       // type 19, nalu[0]&0x1F = 6
		{"IDR_W_RADL high bit", []byte{0xA6, 0x01}},      // type 19 with forbidden bit
		{"IDR_N_LP low bit", []byte{0x28, 0x01}},          // type 20, nalu[0]&0x1F = 8
		{"IDR_N_LP high bit", []byte{0xA8, 0x01}},         // type 20 with forbidden bit
		{"IDR_W_RADL typical", []byte{0x26, 0x01, 0x02}}, // H.265 IDR
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// With H.264 logic, these should NOT be detected as IDR
			require.False(t, IsKeyframeNALU(tt.nalu, false),
				"H.265 IDR must not be detected with H.264 logic (the FLV bug)")
		})
	}
}
