package recorder

// PreallocParams carries the operator-tunable segment-preallocation knobs
// (#757). The zero value means "documented defaults, enabled" so hand-built
// configs and tests need no initialization; config defaults materialize the
// same values on Load.
type PreallocParams struct {
	// Disabled turns preallocation off entirely (config:
	// storage.prealloc_enabled: false).
	Disabled bool
	// HeadroomPercent is the growth margin over the previous segment's
	// size (default 10) — absorbs bitrate drift without re-growing.
	HeadroomPercent int
	// MinBytes is the floor below which preallocation is skipped (default
	// 4MiB): tiny segments don't churn extents enough to matter.
	MinBytes int64
	// MaxBytes caps one segment's reservation (default 512MiB) so a
	// pathological segment doesn't reserve absurd space for successors.
	MaxBytes int64
}

func (p PreallocParams) withDefaults() PreallocParams {
	if p.HeadroomPercent <= 0 {
		p.HeadroomPercent = 10
	}
	if p.MinBytes <= 0 {
		p.MinBytes = 4 << 20
	}
	if p.MaxBytes <= 0 {
		p.MaxBytes = 512 << 20
	}
	return p
}

// segmentPreallocEstimate converts the previous segment's final size into
// the fallocate hint for the next one (#757). Recorders know no encoder
// bitrate; the last segment is the best proxy (same camera, same duration
// policy, same scene complexity). Returns 0 = don't preallocate.
func segmentPreallocEstimate(lastSegBytes int64, p PreallocParams) int64 {
	if p.Disabled {
		return 0
	}
	p = p.withDefaults()
	if lastSegBytes < p.MinBytes {
		return 0
	}
	est := lastSegBytes * int64(100+p.HeadroomPercent) / 100
	if est > p.MaxBytes {
		return p.MaxBytes
	}
	return est
}
