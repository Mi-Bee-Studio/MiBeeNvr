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

import (
	"context"
	"fmt"
	"io"
	"math"
)

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
		lo, hi := ambientWindowBounds(int64(len(in)), nOut, j)
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

// ambientWindowBounds returns the input window [lo,hi) whose RMS becomes
// output sample j — the single definition of the window arithmetic, shared by
// mixdownAmbient (in-memory reference) and writeAmbientBed (streaming merge
// path) so the two cannot drift. Same integer math the original
// mixdownAmbient inlined (j*len/nOut with the min-1-byte and tail clamps).
func ambientWindowBounds(totalIn int64, nOut, j int) (lo, hi int64) {
	lo = int64(j) * totalIn / int64(nOut)
	hi = (int64(j) + 1) * totalIn / int64(nOut)
	if hi <= lo {
		hi = lo + 1
	}
	if hi > totalIn {
		hi = totalIn
	}
	return lo, hi
}

// ambientScratchSize is the fixed chunk size for the streaming ambient
// mixdown's file reads and encoded-output writes (#1011 debt 2).
const ambientScratchSize = 64 << 10

// ambientSpanReader walks the virtual concatenation of a segment's audio
// samples (sample k's bytes immediately followed by sample k+1's) straight
// from the source file — no span bytes are retained (#1011 debt 2: the merge
// ambient pass used to buffer a whole compressed span's G.711 track before
// mixdown, ~4.8MB per 10-minute wall span and linear in wall time).
type ambientSpanReader struct {
	src   io.ReaderAt
	spans []SampleEntry
	base  int64 // virtual offset where spans[idx] starts
	idx   int   // span holding the next window's start; window starts never
	// move backward, so this cursor is monotone
}

// readVirtual consumes the virtual byte range [vlo,vhi) in chunks of at most
// len(buf), calling consume for each chunk. A chunk never crosses a sample's
// file extent, so the chunking is invisible to the decoder. Reads map back to
// per-sample file offsets via ReadAt.
func (r *ambientSpanReader) readVirtual(ctx context.Context, vlo, vhi int64, buf []byte, consume func([]byte) error) error {
	// Advance the window-start cursor to the span containing vlo.
	for r.idx < len(r.spans) && r.base+int64(r.spans[r.idx].Size) <= vlo {
		r.base += int64(r.spans[r.idx].Size)
		r.idx++
	}
	idx, base := r.idx, r.base
	pos := vlo
	for pos < vhi {
		if idx >= len(r.spans) {
			return fmt.Errorf("ambient window [%d,%d) beyond audio spans at virtual offset %d", vlo, vhi, pos)
		}
		spanEnd := base + int64(r.spans[idx].Size)
		end := spanEnd
		if vhi < end {
			end = vhi
		}
		if end <= pos {
			// Zero-size span — skip it.
			idx++
			base = spanEnd
			continue
		}
		n := int(end - pos)
		if n > len(buf) {
			n = len(buf)
		}
		fileOff := r.spans[idx].Offset + (pos - base)
		if _, err := r.src.ReadAt(buf[:n], fileOff); err != nil {
			return fmt.Errorf("read %d audio bytes at offset %d: %w", n, fileOff, err)
		}
		if err := waitIOBudget(ctx, int64(n)); err != nil {
			return err
		}
		if err := consume(buf[:n]); err != nil {
			return err
		}
		pos += int64(n)
		if pos >= spanEnd {
			idx++
			base = spanEnd
		}
	}
	return nil
}

// ambientWindowRMS decodes the G.711 window [lo,hi) and returns its RMS — the
// exact float64 op sequence mixdownAmbient applies per window, so the
// streaming and in-memory paths produce bit-identical envelopes.
func ambientWindowRMS(ctx context.Context, r *ambientSpanReader, muLaw bool, lo, hi int64, buf []byte) (float64, error) {
	var sumSq float64
	err := r.readVirtual(ctx, lo, hi, buf, func(chunk []byte) error {
		if muLaw {
			for _, b := range chunk {
				v := g711DecodeMuLaw(b)
				sumSq += float64(v) * float64(v)
			}
		} else {
			for _, b := range chunk {
				v := g711DecodeALaw(b)
				sumSq += float64(v) * float64(v)
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return math.Sqrt(sumSq / float64(hi-lo)), nil
}

// writeAmbientBed renders one compressed span's ambient bed directly from the
// segment file — the streaming form of mixdownAmbient (#1011 debt 2). It is
// byte-identical to assembling the span's audio in memory and calling
// mixdownAmbient: same window arithmetic (ambientWindowBounds), same float64
// op order, same LCG carrier, same per-window op sequence.
//
// Why two passes over the span's bytes instead of holding the envelope: the
// normalize step needs the envelope's GLOBAL RMS before the first output
// sample can be rendered, so at least the envelope (or the input) must be
// retained across that boundary. Retaining the input is what the old code did
// — linear in wall time. Retaining a float64 env array is 8 B × nOut, which
// at mild compression ratios (just above the 1.5× compressed-span threshold,
// where nOut ≈ inputSize/1.5) would EXCEED the very input it replaced — a
// regression for exactly those spans. Instead both phases recompute windows
// on the fly: retention drops to two 64KB scratch buffers, at the cost of
// reading (page-cache-warm) span bytes twice. Total I/O budget billing is
// unchanged (billed per byte read).
//
// Returns the number of bed bytes written (== nOut); 0 means no bed (empty
// audio or nOut <= 0, mirroring mixdownAmbient's nil result).
func writeAmbientBed(ctx context.Context, src io.ReaderAt, seg *SegmentInfo, nOut int, out io.Writer) (int, error) {
	var totalIn int64
	for _, s := range seg.AudioSamples {
		totalIn += int64(s.Size)
	}
	if totalIn == 0 || nOut <= 0 {
		return 0, nil
	}
	muLaw := seg.G711MULaw
	readBuf := make([]byte, ambientScratchSize)

	// Pass 1: per-window RMS envelope's global mean-square (normalization).
	r1 := &ambientSpanReader{src: src, spans: seg.AudioSamples}
	var ms float64
	for j := range nOut {
		lo, hi := ambientWindowBounds(totalIn, nOut, j)
		envJ, err := ambientWindowRMS(ctx, r1, muLaw, lo, hi, readBuf)
		if err != nil {
			return 0, err
		}
		ms += envJ * envJ
	}
	rms := math.Sqrt(ms / float64(nOut))
	var scale float64
	if rms > 1e-6 {
		scale = ambientGain / rms
	}

	// Pass 2: recompute each window, modulate the low-passed noise carrier,
	// and write the encoded bed in fixed chunks.
	r2 := &ambientSpanReader{src: src, spans: seg.AudioSamples}
	enc := make([]byte, ambientScratchSize)
	encLen := 0
	const carrierTap = 8 // moving average ≈ 1kHz at 8k sample rate
	var seed uint32 = 0x9E3779B9
	var noise, lp float64
	for j := range nOut {
		lo, hi := ambientWindowBounds(totalIn, nOut, j)
		envJ, err := ambientWindowRMS(ctx, r2, muLaw, lo, hi, readBuf)
		if err != nil {
			return 0, err
		}
		if rms > 1e-6 {
			envJ *= scale
		}
		seed = seed*1664525 + 1013904223
		// uniform (−1,1): full int32 range over 2^31
		noise = float64(int32(seed)) / 2147483648.0
		lp += (noise - lp) / carrierTap
		s := envJ * lp
		if s > 32767 {
			s = 32767
		} else if s < -32768 {
			s = -32768
		}
		if muLaw {
			enc[encLen] = g711EncodeMuLaw(int16(s))
		} else {
			enc[encLen] = g711EncodeALaw(int16(s))
		}
		encLen++
		if encLen == len(enc) {
			if _, err := out.Write(enc[:encLen]); err != nil {
				return 0, err
			}
			encLen = 0
		}
	}
	if encLen > 0 {
		if _, err := out.Write(enc[:encLen]); err != nil {
			return 0, err
		}
	}
	return nOut, nil
}
