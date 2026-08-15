package vod

import (
	"fmt"

	"github.com/abema/go-mp4"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/merge"
)

// TargetFragmentDur is the target HLS fragment duration. Fragments always
// START on a keyframe (recordings are IDR-started by design), so actual
// durations vary with the camera's GOP length — a fragment is cut at the
// first keyframe at or after this target.
const TargetFragmentDur = 6 // seconds

// trun flag bits (ISO/IEC 14496-12 §8.8.8).
const (
	trunDataOffsetPresent    = 0x000001
	trunSampleDurationPresent = 0x000100
	trunSampleSizePresent     = 0x000200
	trunSampleFlagsPresent    = 0x000400
)

// sample flag bits: bit(4..5) sample_depends_on, bit(20) sample_is_non_sync_sample.
//   keyframe: sample_depends_on=2 (no dependency), sync sample
//   other:    sample_depends_on=1 (depends on others), non-sync
const (
	sampleFlagsKeyframe = 0x02000000
	sampleFlagsOther    = 0x01010000
)

// Fragment describes one HLS media segment: a keyframe-aligned video sample
// range plus the audio samples that overlap it in time. Ranges are half-open
// [First,End).
type Fragment struct {
	First, End int // video sample indices
	AudioFirst, AudioEnd int // audio sample indices

	StartUnits    uint64 // cumulative video timescale units at First
	DurationUnits uint64 // total video timescale units in this fragment
}

// DurationSec returns the fragment duration in (video-timescale) seconds.
func (f Fragment) DurationSec(timescale uint32) float64 {
	if timescale == 0 {
		return 0
	}
	return float64(f.DurationUnits) / float64(timescale)
}

// PlanFragments cuts a recording into keyframe-aligned fragments of ~target
// seconds. The first fragment starts at sample 0 (recordings begin with an
// IDR); subsequent fragments start at the first keyframe at/after the target
// duration, as reported by the oracle (exact via stss, stride-probed
// otherwise). Audio sample ranges are aligned by overlap with each video
// fragment's time span.
func PlanFragments(info *merge.SegmentInfo, targetSec float64, oracle keyframeOracle) []Fragment {
	n := info.SampleCount
	if n == 0 || len(info.Samples) == 0 {
		return nil
	}
	ts := float64(info.Timescale)
	var frags []Fragment

	start := 0
	var startUnits uint64
	for start < n {
		// Accumulate until the target duration is reached, then advance to
		// the next keyframe for the actual cut.
		var acc uint64
		iTarget := n
		for i := start; i < n; i++ {
			acc += uint64(info.Samples[i].Duration)
			if i > start && float64(acc)/ts >= targetSec {
				iTarget = i
				break
			}
		}
		end := n
		if iTarget < n {
			if k, ok := oracle.nextAtOrAfter(iTarget); ok && k > start && k < n {
				end = k
			}
		}

		var units uint64
		for i := start; i < end; i++ {
			units += uint64(info.Samples[i].Duration)
		}
		frags = append(frags, Fragment{
			First: start, End: end,
			StartUnits:    startUnits,
			DurationUnits: units,
		})
		if end <= start {
			break // defensive: never loop forever on a broken oracle
		}
		startUnits += units
		start = end
	}

	// Align audio ranges by time overlap.
	if info.HasAudio && len(info.AudioSamples) > 0 {
		ats := float64(info.AudioTimescale)
		var audioUnits uint64
		ai := 0
		for k := range frags {
			fStartSec := float64(frags[k].StartUnits) / ts
			fEndSec := fStartSec + float64(frags[k].DurationUnits) / ts
			first := ai
			for ai < len(info.AudioSamples) {
				sampleStartSec := float64(audioUnits) / ats
				if sampleStartSec >= fEndSec {
					break // belongs to a later fragment
				}
				audioUnits += uint64(info.AudioSamples[ai].Duration)
				ai++
			}
			frags[k].AudioFirst, frags[k].AudioEnd = first, ai
		}
	}
	return frags
}

// ByteRange is a contiguous byte region of the source MP4 file.
type ByteRange struct {
	Offset int64
	Size   int64
}

// coalesceRanges merges adjacent sample byte ranges so the mdat streamer does
// one copy per contiguous run instead of one per sample. Samples within one
// source chunk are contiguous; chunk breaks (e.g. audio/video interleave)
// split the runs.
func coalesceRanges(samples []merge.SampleEntry, first, end int) []ByteRange {
	if first >= end || first < 0 || end > len(samples) {
		return nil
	}
	ranges := make([]ByteRange, 0, end-first)
	cur := ByteRange{Offset: samples[first].Offset, Size: int64(samples[first].Size)}
	for i := first + 1; i < end; i++ {
		s := samples[i]
		if cur.Offset+cur.Size == s.Offset {
			cur.Size += int64(s.Size)
		} else {
			ranges = append(ranges, cur)
			cur = ByteRange{Offset: s.Offset, Size: int64(s.Size)}
		}
	}
	return append(ranges, cur)
}

// seekableBuffer is an in-memory io.WriteSeeker for mp4.Writer, which seeks
// BACK to patch box sizes on EndBox — so seeks must not truncate (in-place
// overwrite semantics, same as internal/merge's bytesWriter).
type seekableBuffer struct {
	buf []byte
	pos int
}

func (b *seekableBuffer) Write(p []byte) (int, error) {
	end := b.pos + len(p)
	if end > len(b.buf) {
		grown := make([]byte, end)
		copy(grown, b.buf)
		b.buf = grown
	}
	copy(b.buf[b.pos:], p)
	b.pos = end
	return len(p), nil
}

func (b *seekableBuffer) Seek(offset int64, whence int) (int64, error) {
	var pos int64
	switch whence {
	case 0:
		pos = offset
	case 1:
		pos = int64(b.pos) + offset
	case 2:
		pos = int64(len(b.buf)) + offset
	}
	if pos < 0 {
		return 0, fmt.Errorf("seekableBuffer: negative offset %d", pos)
	}
	b.pos = int(pos)
	return pos, nil
}

func (b *seekableBuffer) Bytes() []byte { return b.buf }

// FragmentData is the assembled response for one media segment: the moof box
// bytes followed by the mdat header + sample payload (streamed by the caller
// from ByteRanges of the source file).
type FragmentData struct {
	Moof        []byte
	VideoRanges []ByteRange
	AudioRanges []ByteRange
	TotalBytes  int64 // moof + mdat header + all range bytes (for Content-Length)
}

// BuildFragment assembles the moof for one fragment. data_offset fields need
// the final moof size, which itself does not depend on their VALUES — so the
// box tree is serialized twice (measure, then fill).
func BuildFragment(info *merge.SegmentInfo, frag Fragment, sequenceNumber uint32, includeAudio bool) (*FragmentData, error) {
	if frag.First < 0 || frag.End > info.SampleCount || frag.First >= frag.End {
		return nil, fmt.Errorf("fragment sample range [%d,%d) out of bounds (0,%d)", frag.First, frag.End, info.SampleCount)
	}

	videoRanges := coalesceRanges(info.Samples, frag.First, frag.End)
	var videoBytes int64
	for _, r := range videoRanges {
		videoBytes += r.Size
	}

	includeAudio = includeAudio && info.HasAudio && frag.AudioEnd > frag.AudioFirst
	var audioRanges []ByteRange
	var audioBytes int64
	var audioStartUnits uint64
	if includeAudio {
		audioRanges = coalesceRanges(info.AudioSamples, frag.AudioFirst, frag.AudioEnd)
		for _, r := range audioRanges {
			audioBytes += r.Size
		}
		for i := 0; i < frag.AudioFirst; i++ {
			audioStartUnits += uint64(info.AudioSamples[i].Duration)
		}
	}

	build := func(videoOffset, audioOffset int32) ([]byte, error) {
		buf := &seekableBuffer{}
		w := mp4.NewWriter(buf)
		if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("moof")}); err != nil {
			return nil, err
		}
		// mfhd
		if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("mfhd")}); err != nil {
			return nil, err
		}
		if _, err := mp4.Marshal(w, &mp4.Mfhd{SequenceNumber: sequenceNumber}, mp4.Context{}); err != nil {
			return nil, err
		}
		if _, err := w.EndBox(); err != nil {
			return nil, err
		}
		// video traf
		if err := writeTraf(w, 1, frag.StartUnits, info.Samples[frag.First:frag.End], videoOffset, true); err != nil {
			return nil, fmt.Errorf("video traf: %w", err)
		}
		if includeAudio {
			if err := writeTraf(w, 2, audioStartUnits, info.AudioSamples[frag.AudioFirst:frag.AudioEnd], audioOffset, false); err != nil {
				return nil, fmt.Errorf("audio traf: %w", err)
			}
		}
		if _, err := w.EndBox(); err != nil { // moof
			return nil, err
		}
		return buf.Bytes(), nil
	}

	// Pass 1: measure with zero offsets; pass 2: fill real offsets.
	moof, err := build(0, 0)
	if err != nil {
		return nil, err
	}
	moofSize := int32(len(moof))
	moof, err = build(moofSize+8, moofSize+8+int32(videoBytes))
	if err != nil {
		return nil, err
	}
	if int32(len(moof)) != moofSize {
		return nil, fmt.Errorf("moof size changed between passes: %d vs %d", len(moof), moofSize)
	}

	return &FragmentData{
		Moof:        moof,
		VideoRanges: videoRanges,
		AudioRanges: audioRanges,
		TotalBytes:  int64(len(moof)) + 8 + videoBytes + audioBytes,
	}, nil
}

// writeTraf writes one track fragment: tfhd (default-base-is-moof), tfdt
// (v1, 64-bit base decode time), trun (per-sample duration/size/flags +
// data offset). Keyframe flags mark sync samples for the video track.
func writeTraf(w *mp4.Writer, trackID uint32, baseUnits uint64, samples []merge.SampleEntry, dataOffset int32, video bool) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("traf")}); err != nil {
		return err
	}
	// tfhd
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("tfhd")}); err != nil {
		return err
	}
	tfhd := &mp4.Tfhd{TrackID: trackID}
	tfhd.SetFlags(mp4.TfhdDefaultBaseIsMoof)
	if _, err := mp4.Marshal(w, tfhd, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	// tfdt (version 1 = 64-bit)
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("tfdt")}); err != nil {
		return err
	}
	tfdt := &mp4.Tfdt{BaseMediaDecodeTimeV1: baseUnits}
	tfdt.SetVersion(1)
	if _, err := mp4.Marshal(w, tfdt, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	// trun
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("trun")}); err != nil {
		return err
	}
	entries := make([]mp4.TrunEntry, len(samples))
	for i := range samples {
		// Positional sync flags: every fragment starts on a keyframe (the
		// planner guarantees it) and audio samples are all sync points. Video
		// samples after the first are marked non-sync — trun flags are decode
		// HINTS, and a mid-fragment IDR mis-flagged as non-sync is harmless.
		flag := uint32(sampleFlagsOther)
		if !video || i == 0 {
			flag = sampleFlagsKeyframe
		}
		entries[i] = mp4.TrunEntry{
			SampleDuration: samples[i].Duration,
			SampleSize:     samples[i].Size,
			SampleFlags:    flag,
		}
	}
	trun := &mp4.Trun{
		SampleCount: uint32(len(samples)),
		DataOffset:  dataOffset,
		Entries:     entries,
	}
	trun.SetFlags(trunDataOffsetPresent | trunSampleDurationPresent | trunSampleSizePresent | trunSampleFlagsPresent)
	if _, err := mp4.Marshal(w, trun, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	_, err := w.EndBox() // traf
	return err
}
