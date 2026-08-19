package nalutil

import "sync"

// AUAssembler regroups pushed H.264/H.265 NAL units into picture-complete
// access units.
//
// Push publishers do not agree on AU granularity. FFmpeg's RTMP/FLV muxer sends
// one message per picture, but restreamer-style publishers emit one message per
// NAL unit — which splits multi-slice pictures (libx264 sliced-threads encodes
// 1080p frames as 4 slice NALUs) into several "AUs" that no decoder can decode
// individually: every downstream consumer (FLV/MSE, HLS/fMP4, WebRTC) showed
// permanently black video. MPEG-TS (SRT) and RTP (WHIP) sources already deliver
// picture-complete AUs; for them the assembler is a pass-through that delays
// each emission by at most one picture.
//
// Picture boundary detection is header-only and needs no RBSP unescaping — the
// inspected byte directly follows the NAL header and cannot itself be an
// emulation-prevention byte:
//
//	H.264: a VCL NAL (types 1-5) whose slice header starts with
//	       first_mb_in_slice == 0 — the first bit of the byte after the 1-byte
//	       NAL header (ue(v)==0 encodes to a single 1 bit) — begins a new
//	       picture; continuation slices carry a nonzero first_mb_in_slice.
//	       (For types 1-5 the header byte is never 0x00, so the following byte
//	       is raw RBSP.)
//	H.265: a VCL NAL (types 0-31) whose first_slice_segment_in_pic_flag — the
//	       first bit of the byte after the 2-byte NAL header — is set begins a
//	       new picture. (nuh_layer_id and nuh_temporal_id_plus1 keep byte 1
//	       nonzero, so byte 2 is raw RBSP.)
//
// Non-VCL NALUs (SPS/PPS/VPS/SEI) arriving before slices become the picture's
// prefix; arriving after VCL they belong to the NEXT picture and flush the
// pending one. An AUD is an explicit boundary. A pending AU that never receives
// a VCL NALU (e.g. the out-of-band RTMP sequence-header feed) is never emitted.
//
// The emitted picture's PTS comes from the call that delivered its first VCL
// NALU — prefix NALUs may arrive earlier on a publisher's own clock.
//
// Safe for concurrent use. emit runs while the assembler's internal lock is
// held, in decode order; it must not call back into the assembler.
type AUAssembler struct {
	mu      sync.Mutex
	isH265  bool
	pending [][]byte
	pts     int64
	valid   bool // pending is non-empty
	hasVCL  bool // pending contains at least one VCL NALU
}

// NewAUAssembler returns an assembler for the given codec family.
func NewAUAssembler(isH265 bool) *AUAssembler {
	return &AUAssembler{isH265: isH265}
}

// Add absorbs one delivered AU (any granularity) and invokes emit for each
// picture that just became complete.
func (a *AUAssembler) Add(au [][]byte, pts int64, emit func(au [][]byte, pts int64)) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, nalu := range au {
		if len(nalu) == 0 {
			continue
		}
		if isAudNALU(nalu, a.isH265) {
			a.flushLocked(emit)
			a.appendLocked(nalu, pts)
			continue
		}
		if !a.isVCLNALU(nalu) {
			if a.hasVCL {
				// Parameter sets / SEI after VCL slices prefix the next picture.
				a.flushLocked(emit)
			}
			a.appendLocked(nalu, pts)
			continue
		}
		if a.hasVCL && a.startsPicture(nalu) {
			a.flushLocked(emit)
		}
		if !a.hasVCL {
			// First VCL NALU of the picture: its delivery time is the picture's
			// PTS (a prefix may have arrived with a stale one).
			a.pts = pts
		}
		a.appendLocked(nalu, pts)
		a.hasVCL = true
	}
}

// Flush emits the pending picture, if any (call on publisher disconnect/stop).
func (a *AUAssembler) Flush(emit func(au [][]byte, pts int64)) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.flushLocked(emit)
}

func (a *AUAssembler) flushLocked(emit func(au [][]byte, pts int64)) {
	if a.valid && a.hasVCL && emit != nil {
		out := make([][]byte, len(a.pending))
		copy(out, a.pending)
		emit(out, a.pts)
	}
	a.pending = nil
	a.valid = false
	a.hasVCL = false
}

func (a *AUAssembler) appendLocked(nalu []byte, pts int64) {
	if !a.valid {
		a.pts = pts
		a.valid = true
	}
	a.pending = append(a.pending, nalu)
}

func (a *AUAssembler) isVCLNALU(nalu []byte) bool {
	if a.isH265 {
		return (nalu[0]>>1)&0x3F <= 31
	}
	t := nalu[0] & 0x1F
	return t >= 1 && t <= 5
}

// startsPicture reports whether nalu is the first VCL NALU of a new picture.
// Undersized NALUs are treated as picture starts (they cannot be verified as
// continuations; emitting early is the safer failure mode).
func (a *AUAssembler) startsPicture(nalu []byte) bool {
	if a.isH265 {
		if len(nalu) < 3 {
			return true
		}
		return nalu[2]&0x80 != 0 // first_slice_segment_in_pic_flag
	}
	if len(nalu) < 2 {
		return true
	}
	return nalu[1]&0x80 != 0 // first_mb_in_slice == 0
}

func isAudNALU(nalu []byte, isH265 bool) bool {
	if isH265 {
		return (nalu[0]>>1)&0x3F == 35 // AUD_NUT
	}
	return nalu[0]&0x1F == 9
}
