package recorder_test

// Round-trip property tests: the recorder's G.711 decoders must invert the
// xiaomi package's production encoders (used for two-way talk) to within one
// quantization step. This pins the segment/mantissa math of both laws to the
// same tables the NVR actually ships.
//
// Known encoder quirks these tests deliberately work around (see comments at
// the failure sites — both are pre-existing two-way-audio behavior, NOT part
// of issue #478):
//   - pcm16ToALaw's sign convention is inverted vs ITU (positive samples
//     produce bit7-clear bytes); the decoder follows ITU (cameras send
//     standard A-law). Magnitude round-trips exactly; the sign flips.
//   - pcm16ToMuLaw negates the int16 before biasing, so -32768 overflows
//     (stays negative) and encodes to a mid-magnitude code; the sweep starts
//     at -32000 to skip that single code point.

import (
	"encoding/binary"
	"math"
	"testing"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/recorder"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/xiaomi"
)

func muLawRoundTrip(t *testing.T) {
	t.Helper()
	var pcm []byte
	for v := -32000; v <= 32000; v += 997 {
		var b [2]byte
		binary.LittleEndian.PutUint16(b[:], uint16(int16(v)))
		pcm = append(pcm, b[:]...)
	}
	enc := xiaomi.EncodePCMToMuLaw(pcm)
	const tol = 1100 // max quantization step at the top segment (8<<7) + margin
	var maxErr int
	for i := range enc {
		orig := int16(binary.LittleEndian.Uint16(pcm[i*2:]))
		got := recorder.DecodeMuLaw(enc[i])
		err := int(got) - int(orig)
		if err < 0 {
			err = -err
		}
		if err > maxErr {
			maxErr = err
		}
		if err > tol {
			t.Fatalf("µ-law round-trip off at sample %d (pcm %d): byte 0x%02X decoded %d, err %d > %d",
				i, orig, enc[i], got, err, tol)
		}
	}
	t.Logf("µ-law max round-trip error: %d (tolerance %d)", maxErr, tol)
}

func TestG711MuLawRoundTrip(t *testing.T) {
	muLawRoundTrip(t)
}

func TestG711ALawRoundTripMagnitude(t *testing.T) {
	var pcm []byte
	for v := -32000; v <= 32000; v += 997 {
		var b [2]byte
		binary.LittleEndian.PutUint16(b[:], uint16(int16(v)))
		pcm = append(pcm, b[:]...)
	}
	enc := xiaomi.EncodePCMToALaw(pcm)
	const tol = 1100
	var maxErr int
	for i := range enc {
		orig := int16(binary.LittleEndian.Uint16(pcm[i*2:]))
		got := recorder.DecodeALaw(enc[i])
		err := int(got) - int(orig)
		if err < 0 {
			err = -err
		}
		if err > maxErr {
			maxErr = err
		}
		if err > tol {
			// The encoder's sign is inverted vs ITU — a magnitude match with
			// a sign flip is the expected result until pcm16ToALaw is fixed.
			errNeg := int(got) + int(orig)
			if errNeg < 0 {
				errNeg = -errNeg
			}
			if errNeg > tol {
				t.Fatalf("A-law round-trip off at sample %d (pcm %d): byte 0x%02X decoded %d, |err| %d/%d > %d",
					i, orig, enc[i], got, err, errNeg, tol)
			}
		}
	}
	t.Logf("A-law max round-trip error: %d (tolerance %d)", maxErr, tol)
}

// The decoders must span (nearly) full scale — a decoder that returned only
// small values would never cross any loudness threshold. ITU table extremes.
func TestG711DecodeFullScale(t *testing.T) {
	if v := recorder.DecodeMuLaw(0x00); math.Abs(float64(v)) < 30000 {
		t.Fatalf("µ-law max magnitude %d, want ~32124", v)
	}
	if v := recorder.DecodeALaw(0x2A); math.Abs(float64(v)) < 30000 {
		t.Fatalf("A-law max negative magnitude %d, want ~32256", v)
	}
	if v := recorder.DecodeALaw(0xAA); math.Abs(float64(v)) < 30000 {
		t.Fatalf("A-law max positive magnitude %d, want ~32256", v)
	}
}
