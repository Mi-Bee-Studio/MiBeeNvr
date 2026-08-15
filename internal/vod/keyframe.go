package vod

import (
	"os"
	"sort"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/merge"
)

// keyframeOracle answers "the next keyframe sample at or after index `from`"
// during fragment planning. Implementations, in order of preference:
//
//   - sidecarOracle: keyframe index persisted next to the recording after a
//     first probing pass — zero media reads, survives restarts.
//   - stssOracle: serves from the file's own sync-sample table — zero media
//     reads.
//   - probeOracle: reads NAL headers on demand for stss-less files. GOP
//     duration is extremely stable in TIME (frame-rate jitter makes sample
//     COUNTS drift), so after the first boundary is found by a short linear
//     scan, subsequent boundaries are predicted from a duration prefix-sum
//     and verified with a single 6-byte read. Probing every sample reads the
//     whole media data — minutes on a USB HDD; this reads a few dozen KB.
type keyframeOracle interface {
	nextAtOrAfter(from int) (idx int, found bool)
}

type stssOracle struct {
	samples []merge.SampleEntry
}

func (o stssOracle) nextAtOrAfter(from int) (int, bool) {
	for i := from; i < len(o.samples); i++ {
		if o.samples[i].IsKeyFrame {
			return i, true
		}
	}
	return 0, false
}

// sidecarOracle serves keyframe positions from a persisted index file.
type sidecarOracle struct {
	samples []merge.SampleEntry
	keys    []int // sorted sample indices
}

func (o sidecarOracle) nextAtOrAfter(from int) (int, bool) {
	i := sort.SearchInts(o.keys, from)
	if i < len(o.keys) {
		return o.keys[i], true
	}
	return 0, false
}

type probeOracle struct {
	file    *os.File
	samples []merge.SampleEntry
	codec   string
	buf     [6]byte

	// prefix[i] = sum of durations of samples [0, i).
	prefix []uint64

	// GOP prediction state (time domain): lastBoundaryUnits is the decode
	// time of the last keyframe returned; gopUnits the learned GOP duration.
	// 0 = not learned yet.
	lastBoundary    int
	lastBoundaryU   uint64
	gopUnits        uint64

	// Found collects every keyframe index returned (for sidecar persistence).
	Found []int
}

func newProbeOracle(file *os.File, info *merge.SegmentInfo) *probeOracle {
	prefix := make([]uint64, len(info.Samples)+1)
	for i, s := range info.Samples {
		prefix[i+1] = prefix[i] + uint64(s.Duration)
	}
	return &probeOracle{
		file:    file,
		samples: info.Samples,
		codec:   info.Codec,
		prefix:  prefix,
		lastBoundary: -1,
	}
}

func (o *probeOracle) isKey(i int) bool {
	s := o.samples[i]
	if s.Size < 5 {
		return false
	}
	n, err := o.file.ReadAt(o.buf[:], s.Offset)
	if err != nil || n < 5 {
		return false
	}
	switch o.codec {
	case "h264":
		nalType := o.buf[4] & 0x1F
		return nalType == 5 || nalType == 7 || nalType == 8
	case "h265":
		nalType := (uint16(o.buf[4]) >> 1) & 0x3F
		return nalType >= 16 && nalType <= 21
	}
	return false
}

func (o *probeOracle) nextAtOrAfter(from int) (int, bool) {
	n := len(o.samples)
	if from < 0 {
		from = 0
	}

	// Time-domain prediction: the next keyframe sits at the sample whose
	// cumulative duration reaches lastBoundaryU + gopUnits. Frame-count drift
	// does not matter; ±4 samples covers residual jitter.
	if o.gopUnits > 0 && o.lastBoundary >= 0 {
		target := o.lastBoundaryU + o.gopUnits
		base := sort.Search(len(o.prefix), func(i int) bool { return o.prefix[i] >= target })
		for d := 0; d <= 4; d++ {
			for _, p := range []int{base + d, base - d} {
				if p >= from && p < n && o.isKey(p) {
					o.record(p)
					return p, true
				}
			}
		}
		// Prediction missed — GOP changed; fall through to a linear scan,
		// which re-learns the period.
	}

	// Linear scan (first boundary, or after a GOP change).
	for i := from; i < n; i++ {
		if o.isKey(i) {
			o.record(i)
			return i, true
		}
	}
	return 0, false
}

func (o *probeOracle) record(idx int) {
	if o.lastBoundary >= 0 && idx > o.lastBoundary {
		o.gopUnits = o.prefix[idx] - o.lastBoundaryU
	}
	o.lastBoundary = idx
	o.lastBoundaryU = o.prefix[idx]
	o.Found = append(o.Found, idx)
}
