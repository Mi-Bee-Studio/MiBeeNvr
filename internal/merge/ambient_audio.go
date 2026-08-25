package merge

// ambient_audio.go — G.711 codec + the ambient-audio envelope mixdown (#496
// audio phase).
//
// While a camera records in adaptive sparse mode with ambient_audio enabled,
// its audio track keeps recording CONTINUOUSLY on the real-time axis. When
// the merge compresses a span's video timeline (sparse dwells → fast cadence),
// the span's audio cannot simply be sped up by the same ~300×: every audible
// frequency would shift into ultrasound and what remains is aliasing noise.
// Instead the span is rendered into a quiet continuous atmosphere bed:
//
//	per output sample, the RMS envelope of the corresponding input window
//	modulates a low-passed noise carrier.
//
// Slow dynamics survive (wind gusts become swells, passing cars become brief
// surges — the "whoosh" timelapse aesthetic), while waveform detail — which
// would alias — is deliberately discarded. Event (full-rate) spans keep their
// real audio verbatim, so anything that happened while recording at full rate
// stays intelligible.
//
// The G.711 codec tables follow the ITU-T reference (Sun ulaw2linear /
// alaw2linear form and their inverses).

import "math"

func g711DecodeMuLaw(u byte) int16 {
	u = ^u
	mag := int16(((int(u&0x0F) << 3) + 0x84) << ((u >> 4) & 0x07))
	if u&0x80 != 0 {
		return 0x84 - mag
	}
	return mag - 0x84
}

func g711DecodeALaw(a byte) int16 {
	a ^= 0x55
	t := int(a&0x0F) << 4
	switch seg := (a >> 4) & 0x07; seg {
	case 0:
		t += 8
	case 1:
		t += 0x108
	default:
		t = (t + 0x108) << (seg - 1)
	}
	if a&0x80 != 0 {
		return int16(t)
	}
	return -int16(t)
}

// g711EncodeMuLaw is the standard 14-bit companded encoder paired with
// g711DecodeMuLaw (the decoder's complement makes a SET sign bit positive).
var g711SegUEnd = [8]int32{0x3F, 0x7F, 0xFF, 0x1FF, 0x3FF, 0x7FF, 0xFFF, 0x1FFF}

func g711EncodeMuLaw(s int16) byte {
	pcm := int32(s) >> 2 // 14-bit
	var mask byte = 0xFF // positive sign (decoder complements)
	if pcm < 0 {
		pcm = -pcm
		mask = 0x7F
	}
	if pcm > 8159 {
		pcm = 8159
	}
	seg := 0
	for seg < 7 && pcm > g711SegUEnd[seg] {
		seg++
	}
	// XOR with the sign mask (complementing the codeword) — OR would smash
	// the segment/mantissa bits.
	return (byte(seg<<4) | byte((pcm>>(seg+1))&0xF)) ^ mask
}

// g711EncodeALaw inverts g711DecodeALaw by construction (the decoder
// reconstructs |v| = (16m+264)<<(seg-1) for seg≥1, 16m+8 for seg 0 — the
// encoder picks the segment and mantissa directly from those ranges, avoiding
// the classic-table shift conventions).
func g711EncodeALaw(s int16) byte {
	v := int32(s)
	neg := v < 0
	if neg {
		v = -v
	}
	seg := int32(0)
	if v > 248 { // seg0 reconstructs 8..248
		seg = 1
		for seg < 7 && v > 504<<(seg-1) {
			seg++
		}
	}
	var m int32
	if seg == 0 {
		m = (v - 8) >> 4
	} else {
		m = ((v >> (seg - 1)) - 264) >> 4
	}
	if m < 0 {
		m = 0
	} else if m > 15 {
		m = 15
	}
	a := byte(seg<<4 | m)
	if !neg {
		a |= 0x80
	}
	return a ^ 0x55
}

// ambientGain is the mixdown's master level relative to full scale. The
// atmosphere bed must sit clearly under the real audio of event spans —
// roughly -16 dBFS RMS after normalization (field-tuning knob).
const ambientGain = 0.16 * 32768

// mixdownAmbient renders one compressed span's G.711 audio (in, muLaw) into
// nOut samples on the span's file timeline: RMS envelope per output window
// modulating a low-passed deterministic noise carrier. Returns nil for empty
// input.
func mixdownAmbient(muLaw bool, in []byte, nOut int) []byte {
	if len(in) == 0 || nOut <= 0 {
		return nil
	}
	// RMS envelope per output sample over its proportional input window.
	env := make([]float64, nOut)
	for j := range nOut {
		lo := j * len(in) / nOut
		hi := (j + 1) * len(in) / nOut
		if hi <= lo {
			hi = lo + 1
		}
		if hi > len(in) {
			hi = len(in)
		}
		var sumSq float64
		for _, b := range in[lo:hi] {
			var v int16
			if muLaw {
				v = g711DecodeMuLaw(b)
			} else {
				v = g711DecodeALaw(b)
			}
			sumSq += float64(v) * float64(v)
		}
		env[j] = math.Sqrt(sumSq / float64(hi-lo))
	}
	// Normalize the envelope so a typical span uses the target headroom.
	var ms float64
	for _, v := range env {
		ms += v * v
	}
	if rms := math.Sqrt(ms / float64(nOut)); rms > 1e-6 {
		scale := ambientGain / rms
		for j := range env {
			env[j] *= scale
		}
	}
	// Low-passed noise carrier (deterministic LCG — reproducible merges).
	out := make([]byte, nOut)
	const carrierTap = 8 // moving average ≈ 1kHz at 8k sample rate
	var seed uint32 = 0x9E3779B9
	var noise, lp float64
	for j := range nOut {
		seed = seed*1664525 + 1013904223
		// uniform (−1,1): full int32 range over 2^31
		noise = float64(int32(seed)) / 2147483648.0
		lp += (noise - lp) / carrierTap
		s := env[j] * lp
		if s > 32767 {
			s = 32767
		} else if s < -32768 {
			s = -32768
		}
		if muLaw {
			out[j] = g711EncodeMuLaw(int16(s))
		} else {
			out[j] = g711EncodeALaw(int16(s))
		}
	}
	return out
}
