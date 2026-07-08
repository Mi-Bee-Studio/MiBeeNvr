// SPDX-License-Identifier: MIT
//
// G.711 μ-law and A-law encode tests.
// Verifies the PCM→G.711 encoding against known reference values.

package xiaomi

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEncodePCMToMuLaw_Silence(t *testing.T) {
	t.Helper()
	// 320 samples of silence (all zeros PCM) at 16-bit LE.
	pcm := make([]byte, 640)
	encoded := EncodePCMToMuLaw(pcm)
	require.Len(t, encoded, 320)
	// Zero PCM → μ-law = 0xFF (all bits inverted, max negative code).
	for i, b := range encoded {
		if b != 0xFF {
			t.Fatalf("sample %d: expected 0xFF for silence, got 0x%02X", i, b)
		}
	}
}

func TestEncodePCMToMuLaw_PositiveFullScale(t *testing.T) {
	t.Helper()
	// Positive full-scale PCM: 0x7FFF (32767)
	pcm := make([]byte, 2)
	pcm[0] = 0xFF
	pcm[1] = 0x7F
	encoded := EncodePCMToMuLaw(pcm)
	require.Len(t, encoded, 1)
	// μ-law: positive 32767 → 0x00FF masked → 0x7F, then XOR 0xFF → 0x80
	require.Equal(t, byte(0x80), encoded[0])
}

func TestEncodePCMToMuLaw_NegativeFullScale(t *testing.T) {
	t.Helper()
	// Negative full-scale PCM: 0x8001 (-32767 in 2's complement)
	pcm := make([]byte, 2)
	pcm[0] = 0x01
	pcm[1] = 0x80
	encoded := EncodePCMToMuLaw(pcm)
	require.Len(t, encoded, 1)
	// μ-law: negative → sign=0x80, magnitude=32767 → 0x80|0x7F=0xFF, XOR 0xFF → 0x00
	require.Equal(t, byte(0x00), encoded[0])
}

func TestEncodePCMToMuLaw_SmallPositive(t *testing.T) {
	t.Helper()
	// Small positive signal: 0x0010 (16 in decimal)
	pcm := make([]byte, 2)
	pcm[0] = 0x10
	pcm[1] = 0x00
	encoded := EncodePCMToMuLaw(pcm)
	require.Len(t, encoded, 1)
	// With bias 132 → 148. Exponent bits 14..8, none match → exponent=0.
	// Mantissa = (148 >> 3) & 0x0F = 18 & 0x0F = 2. val=0x82, ^0x82=0x7D
	// Actually let me compute: val=148, exponent=0, mantissa=(148>>3)&0x0F=18&0x0F=2,
	// compressed=sign(0x00)|(exponent(0)<<4)|mantissa(2)=0x02, XOR 0xFF → 0xFD
	require.Equal(t, byte(0xFD), encoded[0])
}

func TestEncodePCMToALaw_Silence(t *testing.T) {
	t.Helper()
	pcm := make([]byte, 640)
	encoded := EncodePCMToALaw(pcm)
	require.Len(t, encoded, 320)
	// Zero PCM → A-law = 0x55 (XOR 0x55 applied to 0x00).
	for i, b := range encoded {
		if b != 0x55 {
			t.Fatalf("sample %d: expected 0x55 for silence, got 0x%02X", i, b)
		}
	}
}

func TestEncodePCMToALaw_PositiveFullScale(t *testing.T) {
	t.Helper()
	pcm := make([]byte, 2)
	pcm[0] = 0xFF
	pcm[1] = 0x7F
	encoded := EncodePCMToALaw(pcm)
	require.Len(t, encoded, 1)
	// A-law: positive 32767 → bits=1111111111111111, exponent bits
	// val=32767, >=256 → iterate i from 14..8, bit 14 set → exponent=7
	// mantissa=(32767>>10)&0x0F=31&0x0F=15
	// sign=0, compressed=0|(7<<4)|15=0x7F, XOR 0x55 → 0x2A
	require.Equal(t, byte(0x2A), encoded[0])
}

func TestEncodePCMToALaw_SmallSignal(t *testing.T) {
	t.Helper()
	// Small signal (<256): segment 0.
	pcm := make([]byte, 2)
	pcm[0] = 0x20
	pcm[1] = 0x00
	encoded := EncodePCMToALaw(pcm)
	require.Len(t, encoded, 1)
	// val=32, <256 → exponent=0, mantissa=32>>4=2
	// sign=0, compressed=0|0|2=0x02, XOR 0x55 → 0x57
	require.Equal(t, byte(0x57), encoded[0])
}

func TestEncodePCMToMuLaw_RoundtripKnownBytes(t *testing.T) {
	t.Helper()
	// Test with known PCM byte patterns (mixed content).
	pcm := []byte{
		0x00, 0x00, // sample 0: silence
		0xFF, 0x7F, // sample 1: +32767
		0x01, 0x80, // sample 2: -32767
	}
	encoded := EncodePCMToMuLaw(pcm)
	require.Len(t, encoded, 3)
	// silence → 0xFF
	require.Equal(t, byte(0xFF), encoded[0])
	// +32767 → 0x80
	require.Equal(t, byte(0x80), encoded[1])
	// -32767 → 0x00
	require.Equal(t, byte(0x00), encoded[2])
}

func TestEncodePCMToALaw_RoundtripKnownBytes(t *testing.T) {
	t.Helper()
	pcm := []byte{
		0x00, 0x00, // silence
		0xFF, 0x7F, // +32767
		0x01, 0x80, // -32767
	}
	encoded := EncodePCMToALaw(pcm)
	require.Len(t, encoded, 3)
	// silence → 0x55
	require.Equal(t, byte(0x55), encoded[0])
	// +32767 → 0x2A
	require.Equal(t, byte(0x2A), encoded[1])
	// -32767 → sign=0x80, magnitude same as +32767, compressed=0x80|0x7F=0xFF, XOR 0x55 → 0xAA
	require.Equal(t, byte(0xAA), encoded[2])
}

func TestEncodePCM_OddInputPanic(t *testing.T) {
	t.Helper()
	// Odd-length input should produce floor(len/2) output without panic.
	pcm := []byte{0x00, 0x00, 0x00} // 3 bytes = 1.5 samples
	require.NotPanics(t, func() {
		enc := EncodePCMToMuLaw(pcm)
		require.Len(t, enc, 1)
	})
	require.NotPanics(t, func() {
		enc := EncodePCMToALaw(pcm)
		require.Len(t, enc, 1)
	})
}

func TestEncodePCM_EmptyInput(t *testing.T) {
	t.Helper()
	require.NotPanics(t, func() {
		enc := EncodePCMToMuLaw(nil)
		require.Len(t, enc, 0)
	})
	require.NotPanics(t, func() {
		enc := EncodePCMToALaw(nil)
		require.Len(t, enc, 0)
	})
}
