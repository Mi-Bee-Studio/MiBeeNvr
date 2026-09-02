package nalutil

import (
	"time"
)

// Rotation gating on top of the semantic SPS comparison (#642).
//
// SPSSemanticallyEqual (sps_semantic.go) answers "are these two SPS
// decode-equivalent?", folding unparseable inputs into a conservative
// byte-compare. The rotation decision needs one more distinction: a
// byte-different pair whose semantics are UNKNOWN (parse failure) should not
// rotate a segment every flip — that is exactly the storm #642 describes —
// so it is bounded by a minimum interval instead.

type ParamCompat int

const (
	// ParamCompatEqual means the parameter sets are decode-equivalent: only
	// VUI/timing/decorative bits may differ.
	ParamCompatEqual ParamCompat = iota
	// ParamCompatDifferent means a decode-relevant field differs (resolution,
	// profile, level, chroma/bit depth, ordering, cropping…) — mixing frames
	// across the pair inside one segment would break playback.
	ParamCompatDifferent
	// ParamCompatUnknown means at least one side could not be parsed; frames
	// may or may not stay decodable, so rotation must be rate-limited.
	ParamCompatUnknown
)

// CompareSPS classifies two SPS NAL units (start code excluded) for rotation
// decisions. Byte-identical inputs are Equal without parsing; an unparseable
// input yields Unknown (never a false Equal).
func CompareSPS(oldSPS, newSPS []byte, isH265 bool) ParamCompat {
	if EqualParamSets(oldSPS, newSPS) {
		return ParamCompatEqual
	}
	oldKey, oldOK := SPSSemanticKey(oldSPS, isH265)
	newKey, newOK := SPSSemanticKey(newSPS, isH265)
	if !oldOK || !newOK {
		return ParamCompatUnknown
	}
	if oldKey == newKey {
		return ParamCompatEqual
	}
	return ParamCompatDifferent
}

// DefaultParamRotationMinInterval bounds rotations triggered by parameter
// sets that cannot be semantically classified (parse failure, or VPS/PPS
// which have no semantic parser): a genuine one-shot change still rotates,
// rapid alternation collapses to at most one rotation per interval.
const DefaultParamRotationMinInterval = 60 * time.Second

// ParamRotationGate bounds parameter-set-triggered segment rotations. Each
// recorder keeps one instance; it is touched only from the recorder's single
// write goroutine and is deliberately not goroutine-safe.
type ParamRotationGate struct {
	// MinInterval is the backstop interval for Unknown/unparsed changes.
	// Zero means DefaultParamRotationMinInterval.
	MinInterval time.Duration

	lastRotate time.Time
}

// ShouldRotateSPS decides whether a byte-different SPS should rotate the
// segment:
//   - decode-compatible variants (VUI/timing-only differences) never rotate;
//     the caller still caches the fresh bytes for the NEXT segment,
//   - a real codec change (resolution/profile/…) rotates immediately — MP4
//     avcC/hvcC must stay consistent within a segment,
//   - unparseable changes rotate at most once per MinInterval (storm backstop).
func (g *ParamRotationGate) ShouldRotateSPS(oldSPS, newSPS []byte, isH265 bool, now time.Time) bool {
	switch CompareSPS(oldSPS, newSPS, isH265) {
	case ParamCompatEqual:
		return false
	case ParamCompatDifferent:
		g.lastRotate = now
		return true
	default:
		return g.rateLimited(now)
	}
}

// ShouldRotateUnparsed applies the storm backstop to parameter sets without a
// semantic parser (VPS/PPS).
func (g *ParamRotationGate) ShouldRotateUnparsed(now time.Time) bool {
	return g.rateLimited(now)
}

func (g *ParamRotationGate) rateLimited(now time.Time) bool {
	interval := g.MinInterval
	if interval <= 0 {
		interval = DefaultParamRotationMinInterval
	}
	if now.Sub(g.lastRotate) >= interval {
		g.lastRotate = now
		return true
	}
	return false
}
