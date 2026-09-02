package nalutil

import (
	"testing"
	"time"
)

// Rotation-gate tests. The semantic-comparison semantics themselves (VUI-only
// variants equal, real changes different, real fixtures, EPB canonicalization)
// are covered by sps_semantic_test.go — this file covers the three-way
// classification and the rotation policy layered on top.

func TestCompareSPS_ByteEqualIsEqual(t *testing.T) {
	t.Parallel()
	a := buildH264SPS(t, 50, true)
	if got := CompareSPS(a, a, false); got != ParamCompatEqual {
		t.Fatalf("byte-identical SPS: got %v, want Equal", got)
	}
}

func TestCompareSPS_VUIOnlyVariantsAreEqual(t *testing.T) {
	t.Parallel()
	base := buildH264SPS(t, 50, true)
	vui := buildH264SPS(t, 90000, true)
	if got := CompareSPS(base, vui, false); got != ParamCompatEqual {
		t.Fatalf("VUI-only variant: got %v, want Equal", got)
	}
	h265Base := buildH265SPS(t, 50, true)
	h265VUI := buildH265SPS(t, 90000, true)
	if got := CompareSPS(h265Base, h265VUI, true); got != ParamCompatEqual {
		t.Fatalf("H.265 VUI-only variant: got %v, want Equal", got)
	}
}

func TestCompareSPS_RealChangeIsDifferent(t *testing.T) {
	t.Parallel()
	base := buildH264SPS(t, 50, true)
	if got := CompareSPS(base, buildH264SPSWide(t), false); got != ParamCompatDifferent {
		t.Fatalf("resolution change: got %v, want Different", got)
	}
}

func TestCompareSPS_UnparseableIsUnknown(t *testing.T) {
	t.Parallel()
	good := realH264SPS1080
	trunc := good[:6]
	if got := CompareSPS(good, trunc, false); got != ParamCompatUnknown {
		t.Fatalf("parseable vs truncated: got %v, want Unknown", got)
	}
	if got := CompareSPS(trunc, trunc, false); got != ParamCompatEqual {
		t.Fatalf("byte-identical truncated: got %v, want Equal", got)
	}
}

func TestParamRotationGate_CompatibleVariantsNeverRotate(t *testing.T) {
	t.Parallel()
	var g ParamRotationGate
	old := buildH264SPS(t, 50, true)
	variant := buildH264SPS(t, 90000, true)

	now := time.Now()
	for i := range 120 { // a full day of per-GOP alternation
		t2 := now.Add(time.Duration(i) * 2 * time.Second)
		if g.ShouldRotateSPS(old, variant, false, t2) {
			t.Fatalf("decode-compatible variant must never rotate (flip %d)", i)
		}
	}
}

func TestParamRotationGate_RealChangeRotatesImmediately(t *testing.T) {
	t.Parallel()
	var g ParamRotationGate
	old := buildH264SPS(t, 50, true)
	wide := buildH264SPSWide(t)
	now := time.Now()
	if !g.ShouldRotateSPS(old, wide, false, now) {
		t.Fatal("real codec change must rotate immediately")
	}
	// Even a rapid second genuine change (quality oscillation) still rotates —
	// MP4 avcC consistency outranks churn bounds for known-real changes.
	if !g.ShouldRotateSPS(wide, old, false, now.Add(5*time.Second)) {
		t.Fatal("a second genuine change must still rotate")
	}
}

func TestParamRotationGate_UnknownChangeRateLimited(t *testing.T) {
	t.Parallel()
	var g ParamRotationGate
	a := realH264SPS1080[:6]
	b := append([]byte(nil), a...)
	b[len(b)-1] ^= 0xFF
	now := time.Now()
	if !g.ShouldRotateSPS(a, b, false, now) {
		t.Fatal("first unparseable change must rotate (lastRotate is zero)")
	}
	for i := range 5 { // flips at 11s..55s — all inside the 60s interval
		t2 := now.Add(time.Duration(i+1) * 11 * time.Second)
		if g.ShouldRotateSPS(a, b, false, t2) {
			t.Fatalf("unparseable flip inside the interval must not rotate (t=+%ds)", (i+1)*11)
		}
	}
	if !g.ShouldRotateSPS(a, b, false, now.Add(61*time.Second)) {
		t.Fatal("after the interval an unparseable change rotates again")
	}
}

func TestParamRotationGate_UnparsedRateLimited(t *testing.T) {
	t.Parallel()
	var g ParamRotationGate
	now := time.Now()
	if !g.ShouldRotateUnparsed(now) {
		t.Fatal("first VPS/PPS change must rotate")
	}
	if g.ShouldRotateUnparsed(now.Add(11 * time.Second)) {
		t.Fatal("VPS/PPS flip-flop must be rate-limited")
	}
	if !g.ShouldRotateUnparsed(now.Add(60 * time.Second)) {
		t.Fatal("after the interval VPS/PPS change rotates again")
	}
}

func TestParamRotationGate_CustomMinInterval(t *testing.T) {
	t.Parallel()
	g := ParamRotationGate{MinInterval: 5 * time.Minute}
	now := time.Now()
	if !g.ShouldRotateUnparsed(now) {
		t.Fatal("first change rotates")
	}
	if g.ShouldRotateUnparsed(now.Add(4 * time.Minute)) {
		t.Fatal("custom MinInterval must be honored")
	}
	if !g.ShouldRotateUnparsed(now.Add(5 * time.Minute)) {
		t.Fatal("custom MinInterval elapsed")
	}
}
