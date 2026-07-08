// SPDX-License-Identifier: MIT
//
// G.711 μ-law and A-law PCM encoding for two-way audio.
// Converts 16-bit linear PCM (signed LE) to 8-bit G.711 samples.
// Tables are NOT used — encoding is computed via the standard
// ITU-T G.711 algorithm (companding + bit inversions), which is
// the REVERSE of the frontend's lookup-table decode.

package xiaomi

// EncodePCMToMuLaw encodes 16-bit signed little-endian PCM data
// to 8-bit μ-law (PCMU) samples. input must be an even number of
// bytes (each pair = one signed 16-bit LE sample).
// Returns len(input)/2 bytes of μ-law encoded audio.
func EncodePCMToMuLaw(input []byte) []byte {
	out := make([]byte, len(input)/2)
	for i := range out {
		sample := int16(input[2*i]) | int16(input[2*i+1])<<8
		out[i] = pcm16ToMuLaw(sample)
	}
	return out
}

// EncodePCMToALaw encodes 16-bit signed little-endian PCM data
// to 8-bit A-law (PCMA) samples.
func EncodePCMToALaw(input []byte) []byte {
	out := make([]byte, len(input)/2)
	for i := range out {
		sample := int16(input[2*i]) | int16(input[2*i+1])<<8
		out[i] = pcm16ToALaw(sample)
	}
	return out
}

// pcm16ToMuLaw converts one 16-bit signed PCM sample to 8-bit μ-law.
// Implements the standard ITU-T G.711 μ-law companding: sign bit,
// 3-bit exponent (segment), 4-bit mantissa, then all bits inverted (XOR 0xFF).
func pcm16ToMuLaw(sample int16) byte {
	var sign byte
	if sample < 0 {
		sign = 0x80
		sample = -sample
	}

	// Add bias (0x84 = 132)
	val := int(sample) + 132
	if val > 32635 { // 32767 - 132
		val = 32635
	}

	// Find exponent: MSB among bits 14..8
	var exponent byte
	for i := 14; i >= 8; i-- {
		if val&(1<<i) != 0 {
			exponent = byte(i - 7)
			break
		}
	}

	// Mantissa
	mantissa := byte((val >> (exponent + 3)) & 0x0F)

	// Build compressed byte and invert all bits (μ-law characteristic)
	return ^(sign | (exponent << 4) | mantissa)
}

// pcm16ToALaw converts one 16-bit signed PCM sample to 8-bit A-law.
// Implements the standard ITU-T G.711 A-law companding: sign bit,
// 3-bit exponent (segment), 4-bit mantissa, then XOR 0x55.
func pcm16ToALaw(sample int16) byte {
	var sign byte
	val := int(sample)
	if val < 0 {
		sign = 0x80
		val = -val
	}

	if val > 32767 {
		val = 32767
	}

	var exponent byte
	var mantissa byte

	if val >= 256 {
		// Find exponent among bits 14..8
		for i := 14; i >= 8; i-- {
			if val&(1<<i) != 0 {
				exponent = byte(i - 7)
				break
			}
		}
		mantissa = byte((val >> (exponent + 3)) & 0x0F)
	} else {
		// Small signal: segment 0
		exponent = 0
		mantissa = byte(val >> 4)
	}

	// Build compressed byte and XOR 0x55 (A-law alternate-bit inversion)
	return (sign | (exponent << 4) | mantissa) ^ 0x55
}
