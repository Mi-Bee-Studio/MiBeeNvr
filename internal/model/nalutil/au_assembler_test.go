package nalutil

import (
	"reflect"
	"testing"
)

// h264Slice builds a VCL slice NALU. first selects the first_mb_in_slice bit
// (0x80 in the byte after the NAL header = new picture).
func h264Slice(idr, first bool) []byte {
	nalu := []byte{0x01, 0x00, 0x84, 0x00, 0x10}
	if idr {
		nalu[0] = 0x05
	}
	if first {
		nalu[1] = 0x88 // first_mb_in_slice == 0 (first bit set)
	}
	return nalu
}

// h264Param builds a parameter-set NALU (7=SPS, 8=PPS).
func h264Param(t byte) []byte { return []byte{t, 0x42, 0x00, 0x0a} }

type emitted struct {
	au  [][]byte
	pts int64
}

func collector(out *[]emitted) func([][]byte, int64) {
	return func(au [][]byte, pts int64) { *out = append(*out, emitted{au, pts}) }
}

// TestAUAssembler_PerSliceMessagesGrouped feeds the restreamer shape — one
// message per NAL unit, multi-slice picture — and expects one complete AU.
func TestAUAssembler_PerSliceMessagesGrouped(t *testing.T) {
	a := NewAUAssembler(false)
	var got []emitted
	emit := collector(&got)

	s1, s2, s3, s4 := h264Slice(true, true), h264Slice(true, false), h264Slice(true, false), h264Slice(true, false)
	a.Add([][]byte{h264Param(7), h264Param(8), s1}, 100, emit)
	a.Add([][]byte{s2}, 110, emit)
	a.Add([][]byte{s3}, 120, emit)
	a.Add([][]byte{s4}, 130, emit)
	if len(got) != 0 {
		t.Fatalf("picture must not emit before the next one starts, got %d", len(got))
	}

	// Next picture (a P frame) flushes the complete IDR picture.
	a.Add([][]byte{h264Slice(false, true)}, 200, emit)
	if len(got) != 1 {
		t.Fatalf("expected exactly one emitted picture, got %d", len(got))
	}
	want := [][]byte{h264Param(7), h264Param(8), s1, s2, s3, s4}
	if !reflect.DeepEqual(got[0].au, want) {
		t.Fatalf("emitted AU mismatch:\n got  %v\n want %v", got[0].au, want)
	}
	if got[0].pts != 100 {
		t.Fatalf("picture PTS must come from the first VCL delivery, got %d", got[0].pts)
	}
}

// TestAUAssembler_CompleteAUsPassThrough feeds the FFmpeg shape — one complete
// AU per call — and expects one emission per picture (one picture of lag).
func TestAUAssembler_CompleteAUsPassThrough(t *testing.T) {
	a := NewAUAssembler(false)
	var got []emitted
	emit := collector(&got)

	a.Add([][]byte{h264Param(7), h264Param(8), h264Slice(true, true), h264Slice(true, false)}, 0, emit)
	a.Add([][]byte{h264Slice(false, true), h264Slice(false, false)}, 3000, emit)
	a.Add([][]byte{h264Slice(false, true)}, 6000, emit)

	if len(got) != 2 {
		t.Fatalf("expected 2 emitted pictures, got %d", len(got))
	}
	if got[0].pts != 0 || got[1].pts != 3000 {
		t.Fatalf("pts mismatch: %v", got)
	}
}

// TestAUAssembler_NonVCLAfterVCLStartsNextPicture verifies SPS/PPS/SEI arriving
// after slices flush the pending picture and prefix the next one.
func TestAUAssembler_NonVCLAfterVCLStartsNextPicture(t *testing.T) {
	a := NewAUAssembler(false)
	var got []emitted
	emit := collector(&got)

	s1 := h264Slice(false, true)
	a.Add([][]byte{s1}, 10, emit)
	a.Add([][]byte{h264Param(7), h264Param(8)}, 20, emit) // flushes picture 1
	a.Add([][]byte{h264Slice(true, true)}, 30, emit)      // completes picture 2
	a.Flush(emit)

	if len(got) != 2 {
		t.Fatalf("expected 2 emitted pictures, got %d", len(got))
	}
	if len(got[0].au) != 1 || !reflect.DeepEqual(got[0].au[0], s1) {
		t.Fatalf("picture 1 mismatch: %v", got[0].au)
	}
	want2 := [][]byte{h264Param(7), h264Param(8), h264Slice(true, true)}
	if !reflect.DeepEqual(got[1].au, want2) || got[1].pts != 30 {
		t.Fatalf("picture 2 mismatch: au=%v pts=%d", got[1].au, got[1].pts)
	}
}

// TestAUAssembler_ParamOnlyNeverEmits: the RTMP out-of-band sequence-header
// feed carries no VCL NALU and must never surface as an AU.
func TestAUAssembler_ParamOnlyNeverEmits(t *testing.T) {
	a := NewAUAssembler(false)
	var got []emitted
	emit := collector(&got)

	a.Add([][]byte{h264Param(7), h264Param(8)}, 0, emit)
	a.Flush(emit)
	if len(got) != 0 {
		t.Fatalf("param-only feed must not emit, got %d", len(got))
	}
}

// TestAUAssembler_AUDBoundary treats an AUD as an explicit picture boundary.
func TestAUAssembler_AUDBoundary(t *testing.T) {
	a := NewAUAssembler(false)
	var got []emitted
	emit := collector(&got)

	aud := []byte{0x09, 0xf0}
	a.Add([][]byte{h264Slice(false, true), h264Slice(false, false)}, 0, emit)
	a.Add([][]byte{aud, h264Slice(false, true)}, 3000, emit)
	a.Flush(emit)

	if len(got) != 2 {
		t.Fatalf("expected 2 pictures (AUD boundary + flush), got %d", len(got))
	}
	if got[0].pts != 0 || got[1].pts != 3000 {
		t.Fatalf("pts mismatch: %v", got)
	}
}

// TestAUAssembler_PTSFromFirstVCLNotStalePrefix: a prefix arriving with a stale
// PTS must not stamp the picture.
func TestAUAssembler_PTSFromFirstVCLNotStalePrefix(t *testing.T) {
	a := NewAUAssembler(false)
	var got []emitted
	emit := collector(&got)

	a.Add([][]byte{h264Param(7), h264Param(8)}, 0, emit) // out-of-band feed
	a.Add([][]byte{h264Slice(true, true)}, 9000, emit)   // picture VCL at 9000
	a.Add([][]byte{h264Slice(false, true)}, 9300, emit)  // next picture → flush
	if len(got) != 1 || got[0].pts != 9000 {
		t.Fatalf("expected 1 picture with pts=9000, got %+v", got)
	}
}

// TestAUAssembler_H265 groups per-slice H.265 messages using
// first_slice_segment_in_pic_flag.
func TestAUAssembler_H265(t *testing.T) {
	a := NewAUAssembler(true)
	var got []emitted
	emit := collector(&got)

	h265Slice := func(idr, first bool) []byte {
		t := byte(1) // TRAIL_R
		if idr {
			t = 19 // IDR_W_RADL
		}
		b2 := byte(0x00)
		if first {
			b2 = 0x80 // first_slice_segment_in_pic_flag
		}
		return []byte{t << 1, 0x01, b2, 0x01, 0x5a}
	}
	vps := []byte{(32 << 1), 0x01, 0x01}
	sps := []byte{(33 << 1), 0x01, 0x01}
	pps := []byte{(34 << 1), 0x01, 0x01}

	a.Add([][]byte{vps, sps, pps, h265Slice(true, true)}, 0, emit)
	a.Add([][]byte{h265Slice(true, false)}, 10, emit)
	a.Add([][]byte{h265Slice(true, false)}, 20, emit)
	a.Add([][]byte{h265Slice(false, true)}, 100, emit)

	if len(got) != 1 {
		t.Fatalf("expected 1 grouped IDR picture, got %d", len(got))
	}
	want := [][]byte{vps, sps, pps, h265Slice(true, true), h265Slice(true, false), h265Slice(true, false)}
	if !reflect.DeepEqual(got[0].au, want) {
		t.Fatalf("H.265 grouped AU mismatch:\n got  %v\n want %v", got[0].au, want)
	}
}
