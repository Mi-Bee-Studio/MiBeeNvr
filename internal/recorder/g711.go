package recorder

import "encoding/binary"

// G711ToPCM decodes a raw G.711 byte stream into little-endian 16-bit PCM.
// Extracted from internal/api so the recording WAV path and the live
// listening path (camera audio domain) share one decode implementation.
func G711ToPCM(g711 []byte, muLaw bool, _ int) []byte {
	pcm := make([]byte, len(g711)*2)
	for i, b := range g711 {
		var s int16
		if muLaw {
			s = DecodeMuLaw(b)
		} else {
			s = DecodeALaw(b)
		}
		binary.LittleEndian.PutUint16(pcm[i*2:], uint16(s))
	}
	return pcm
}
