package merge

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/fadvise"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model/nalutil"
	"github.com/abema/go-mp4"
)

// sysTotalMem is the physical-memory probe backing mergeBufferSize (seam for
// tests). 0 = unknown → conservative buffer.
var sysTotalMem = probeTotalMem

// probeTotalMem reads MemTotal from /proc/meminfo (kB → bytes). Portable and
// dependency-free; non-Linux / read failures return 0.
func probeTotalMem() int64 {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(data), "\n") {
		if rest, ok := strings.CutPrefix(line, "MemTotal:"); ok {
			fields := strings.Fields(rest)
			if len(fields) < 2 || fields[1] != "kB" {
				continue
			}
			if kb, err := strconv.ParseInt(fields[0], 10, 64); err == nil {
				return kb * 1024
			}
		}
	}
	return 0
}

// mergeBufferSize is the streaming copy buffer for sample data (#754): 1MB
// on low-memory hosts (<1.5GB RAM — the RPi-3B design baseline) and 4MB on
// roomier systems (¼ the syscalls per merged GB on SSD/eMMC). Single source
// of truth for the merge copy buffer.
func mergeBufferSize() int {
	if sysTotalMem() >= int64(1536)<<20 {
		return 4 << 20
	}
	return 1 << 20
}

// ComputeMergeQuality determines the quality classification for a merged recording.
// quality is:
//   - "fragmented" if wall-clock span (ended-started) exceeds 1.5× actual content duration
//     (indicates significant time gaps from camera disconnects)
//   - "short" if the content duration is below the minimum threshold
//   - "complete" otherwise
func ComputeMergeQuality(startedAt, endedAt time.Time, durationSec float64, minDurationSec float64) string {
	wallSpan := endedAt.Sub(startedAt).Seconds()
	if wallSpan > durationSec*1.5 && durationSec > 0 {
		return "fragmented"
	}
	if minDurationSec > 0 && durationSec < minDurationSec {
		return "short"
	}
	return "complete"
}

// mergedSample holds a sample's info relative to the merged output file.
type mergedSample struct {
	offset     int64
	size       uint32
	duration   uint32
	isKeyFrame bool
}

// MergeStats reports how a merge consumed its inputs after keyframe alignment.
type MergeStats struct {
	// Included holds the input indexes whose samples are present in the output.
	Included []int
	// SkippedNoKeyframe holds input indexes dropped entirely: their video track
	// had no keyframe sample, so they cannot join a merged reference chain (#488).
	SkippedNoKeyframe []int
	// LeadingDropped maps input index → count of leading video samples dropped
	// to reach that segment's first keyframe.
	LeadingDropped map[int]int
	// TimelapseFrames counts sparse dwell samples rewritten to
	// TimelapseFrameDur — the compressed-timeline fix for #496.
	TimelapseFrames int
	// AmbientSamples counts synthesized atmosphere-bed samples rendered from
	// compressed spans' continuous ambient audio (#496 audio phase).
	AmbientSamples int
	// SilenceSamples counts synthesized G.711 silence bytes filling the
	// uncovered spans of audio-bearing segments (#1150 ③) — segments without
	// an audio track in an otherwise audio-consistent run.
	SilenceSamples int
	// WallToFile maps the product's wall-clock span onto its (possibly
	// compressed) file timeline: cumulative [wallSeconds, fileSeconds] pairs
	// at every input boundary, len == len(segments)+1 (starts at [0,0]).
	// Piecewise-linear: within an input, wall time maps onto file time at that
	// input's (possibly rewritten) rate. Callers with no UI plumbing may
	// ignore it; it is always populated.
	WallToFile [][2]float64
}

// ErrNoKeyframe is returned when no input segment carries a keyframe sample —
// nothing decodable can be produced from the inputs.
var ErrNoKeyframe = errors.New("no keyframe-bearing segments")

// TimelineMapJSON renders WallToFile as the compact JSON stored on the
// product row ("[[0,0],[126.3,2.5],...]"). Empty when no map was collected.
func (s MergeStats) TimelineMapJSON() string {
	if len(s.WallToFile) == 0 {
		return ""
	}
	b, err := json.Marshal(s.WallToFile)
	if err != nil {
		return ""
	}
	return string(b)
}

// WallDurationSec returns the product's wall-clock span (the last WallToFile
// point's wall component) — the merge-time companion of the row's
// started_at..ended_at, unaffected by dwell compression.
func (s MergeStats) WallDurationSec() float64 {
	if n := len(s.WallToFile); n > 0 {
		return s.WallToFile[n-1][0]
	}
	return 0
}

// FileDurationSec returns the product's post-compression file span (the last
// WallToFile point's file component) — what a player will actually play.
func (s MergeStats) FileDurationSec() float64 {
	if n := len(s.WallToFile); n > 0 {
		return s.WallToFile[n-1][1]
	}
	return 0
}

// Timelapse dwell compression (#496): adaptive sparse mode stores one keyframe
// per timelapse_interval (~30s) ON THE REAL-TIME AXIS, so any player — the SPA
// included, and crucially a DOWNLOADED file in a local player — freezes each
// frame for the whole gap. A video sample whose duration exceeds
// TimelapseGapThreshold is such a dwell; in segments WITHOUT an audio track
// (sparse mode drops disk audio, so TL segments never carry one) the dwell is
// rewritten to TimelapseFrameDur, making normal 1× playback feel like the
// timelapse it is. Segments WITH audio keep real durations: their audio track
// has its own timeline, and rewriting video there would desync it.
//
// Vars (not consts) so tooling and tests can inject alternative cadences —
// the field-test flow generated per-camera sample clips at different frame
// durations before fixing the default.
var (
	TimelapseGapThreshold = 2 * time.Second
	TimelapseFrameDur     = 100 * time.Millisecond
)

// AlignToKeyframe trims seg's leading video samples up to (and including the
// position of) its first keyframe and trims the audio head by the same wall
// duration so A/V still start together. A segment that starts mid-GOP
// references frames that live only in the PREVIOUS source file; once that file
// is deleted after the merge, players conceal every such P-frame with gray
// until the next IDR (#488: user-visible gray screens in downloaded merged
// recordings). Mutates seg in place (Samples/SampleCount/TotalDuration and the
// audio head). Returns the number of leading video samples dropped and false
// when the segment has no keyframe at all — callers must then keep the segment
// standalone (mark incompatible) instead of merging it.
func AlignToKeyframe(seg *SegmentInfo) (dropped int, ok bool) {
	first := -1
	for i, s := range seg.Samples {
		if s.IsKeyFrame {
			first = i
			break
		}
	}
	if first < 0 {
		return 0, false
	}
	if first == 0 {
		return 0, true
	}
	var droppedDur uint64
	for _, s := range seg.Samples[:first] {
		droppedDur += uint64(s.Duration)
	}
	seg.Samples = seg.Samples[first:]
	seg.SampleCount = len(seg.Samples)
	if seg.Timescale > 0 {
		droppedSec := float64(droppedDur) / float64(seg.Timescale)
		seg.TotalDuration -= time.Duration(droppedSec * float64(time.Second))
		trimAudioHead(seg, droppedSec)
	}
	return first, true
}

// trimAudioHead drops leading audio samples whose midpoint falls before the
// video head moved by droppedSec, keeping A/V start roughly aligned after a
// keyframe trim.
func trimAudioHead(seg *SegmentInfo, droppedSec float64) {
	if len(seg.AudioSamples) == 0 || seg.AudioTimescale == 0 || droppedSec <= 0 {
		return
	}
	var cum float64
	cut := 0
	for i, s := range seg.AudioSamples {
		d := float64(s.Duration) / float64(seg.AudioTimescale)
		if cum+d/2 <= droppedSec {
			cum += d
			cut = i + 1
		} else {
			break
		}
	}
	if cut > 0 {
		seg.AudioSamples = seg.AudioSamples[cut:]
		if seg.AudioSampleCount >= cut {
			seg.AudioSampleCount -= cut
		} else {
			seg.AudioSampleCount = 0
		}
	}
}

// MergeMP4Segments performs a streaming merge of multiple MP4 segments into a single output file.
// All segments must share the same codec and SPS/PPS (for H.264) or VPS/SPS/PPS (for H.265).
// The output file is written to outputPath directly (caller handles temp→final rename).
//
// Keyframe alignment (#488): before stitching, every segment's leading video
// samples are dropped up to its first keyframe (audio head trimmed by the same
// duration), and segments with no keyframe at all are skipped and reported in
// the returned MergeStats. Without this, a segment that starts mid-GOP (e.g. an
// adaptive TL-exit flush segment or a reconnect micro-segment) contributes
// P-frames whose references exist only in an already-deleted source file —
// players conceal them with gray until the next IDR. Input SegmentInfos are
// mutated to their aligned state so callers' duration/frame metadata matches
// what actually landed in the output.
// MergeMP4Segments merges aligned segments into outputPath. The optional
// frameDur overrides the package default TimelapseFrameDur — callers with a
// per-camera cadence (adaptive.timelapse_frame_ms) pass it through.
//
// The merge runs as a phase sequence (issue #1011 debt 1 — this function used
// to be a 457-line monolith): align → validate → budget moov → skeleton →
// video pass → audio pass → patch tail. Each phase is a named function below,
// in original execution order.
func MergeMP4Segments(ctx context.Context, segments []*SegmentInfo, outputPath string, frameDur ...time.Duration) (MergeStats, error) {
	stats := MergeStats{LeadingDropped: map[int]int{}}
	if len(segments) == 0 {
		return stats, fmt.Errorf("no segments to merge")
	}

	segments, err := alignSegmentsToKeyframes(segments, &stats)
	if err != nil {
		return stats, err
	}
	codec, hasAudio, err := validateMergeInputs(segments)
	if err != nil {
		return stats, err
	}

	out, err := os.Create(outputPath)
	if err != nil {
		return stats, fmt.Errorf("create output: %w", err)
	}
	defer out.Close()

	videoTrack, audioTrack, moovSize, err := budgetMoovSize(codec, segments, hasAudio, frameDur)
	if err != nil {
		return stats, err
	}

	// The sample-copy buffer doubles as the moov placeholder's zero scratch:
	// at this point it is freshly allocated and still all-zero (#1011 debt 3 —
	// the placeholder used to be a moovSize-sized allocation).
	buf := make([]byte, mergeBufferSize())
	layout, err := writeOutputSkeleton(out, codec, moovSize, buf)
	if err != nil {
		return stats, err
	}

	allVideoSamples, mdatBytes, err := streamVideoSamples(ctx, out, segments, hasAudio, layout.mdatDataStart, buf, frameDur, &stats)
	if err != nil {
		return stats, err
	}

	var allAudioSamples []mergedSample
	if hasAudio {
		allAudioSamples, mdatBytes, err = streamAudioSamples(ctx, out, segments, &stats, layout.mdatDataStart, mdatBytes, buf)
		if err != nil {
			return stats, err
		}
	}

	if err := patchMdatAndWriteMoov(out, layout, mdatBytes, videoTrack, audioTrack, hasAudio, allVideoSamples, allAudioSamples); err != nil {
		return stats, err
	}

	// Sync and close.
	if err := out.Sync(); err != nil {
		return stats, fmt.Errorf("sync output: %w", err)
	}

	return stats, nil
}

// alignSegmentsToKeyframes drops each segment's leading samples up to its
// first keyframe (see AlignToKeyframe) and collects the mergeable subset.
// Segments without any keyframe are skipped and reported in stats — keeping
// them standalone beats corrupting the merged reference chain.
func alignSegmentsToKeyframes(segments []*SegmentInfo, stats *MergeStats) ([]*SegmentInfo, error) {
	aligned := make([]*SegmentInfo, 0, len(segments))
	for i, seg := range segments {
		dropped, ok := AlignToKeyframe(seg)
		if !ok {
			stats.SkippedNoKeyframe = append(stats.SkippedNoKeyframe, i)
			logger.Warn("merge: skipping keyframe-less segment (would corrupt the merged reference chain)",
				"path", seg.FilePath, "samples", seg.SampleCount)
			continue
		}
		if dropped > 0 {
			stats.LeadingDropped[i] = dropped
		}
		stats.Included = append(stats.Included, i)
		aligned = append(aligned, seg)
	}
	if len(aligned) == 0 {
		return nil, ErrNoKeyframe
	}
	return aligned, nil
}

// validateMergeInputs enforces same-codec/same-parameter-set compatibility
// across all (already aligned) segments and resolves audio presence.
//
// Audio-bearing segments must agree on codec config (AAC AudioSpecificConfig
// bytes / G.711 law flag) — a mismatch still downgrades the merge to
// video-only. PRESENCE gaps (some segments without audio, e.g. camera
// reconnections where audio_enabled was toggled or G.711 negotiation
// succeeded/failed mid-session) no longer drop the track: G.711 runs get
// silence synthesized for the uncovered spans (#1150 ③), while AAC gap-runs
// keep the legacy drop (synthesizing AAC silence needs a real encoder).
// Video is always preserved — the individual source segments (if still on
// disk) retain their audio either way. A first segment with no samples at
// all is rejected.
func validateMergeInputs(segments []*SegmentInfo) (codec string, hasAudio bool, err error) {
	first := segments[0]
	codec = first.Codec

	// Validate all segments share the same codec and SPS/PPS.
	for i, seg := range segments {
		if seg.Codec != codec {
			return "", false, fmt.Errorf("segment %d: codec mismatch (%s vs %s)", i, seg.Codec, codec)
		}
		if i > 0 {
			// SPS compares semantically (#642/#1150): the live dispatcher
			// groups runs by the SPS decode key — VUI/timing bytes rotate on
			// some cameras without affecting decode — so a byte-different but
			// decode-equal segment must not hard-fail here and mark the whole
			// run incompatible. PPS stays byte-exact: it is tiny and any
			// change is meaningful.
			if !nalutil.SPSSemanticallyEqual(seg.SPS, first.SPS, codec == "h265") {
				return "", false, fmt.Errorf("segment %d: SPS mismatch", i)
			}
			if !bytes.Equal(seg.PPS, first.PPS) {
				return "", false, fmt.Errorf("segment %d: PPS mismatch", i)
			}
			if codec == "h265" && !bytes.Equal(seg.VPS, first.VPS) {
				return "", false, fmt.Errorf("segment %d: VPS mismatch", i)
			}
		}
	}

	// Validate audio consistency across audio-BEARING segments: AAC compares
	// AudioSpecificConfig bytes, G.711 compares the law flag. Presence gaps
	// are handled downstream — G.711 runs get silence synthesized
	// (#1150 ③); AAC gap-runs keep the legacy drop.
	hasAudio = false
	var refConfig []byte
	var refIsG711, refMULaw bool
	audioMixed := false
	for _, seg := range segments {
		if !seg.HasAudio {
			continue
		}
		if !hasAudio {
			hasAudio = true
			refConfig, refIsG711, refMULaw = seg.AudioConfig, seg.AudioCodec == "g711", seg.G711MULaw
			continue
		}
		if refIsG711 {
			if seg.AudioCodec != "g711" || seg.G711MULaw != refMULaw {
				audioMixed = true
			}
		} else if seg.AudioCodec == "g711" || !bytes.Equal(seg.AudioConfig, refConfig) {
			audioMixed = true
		}
	}
	// Legacy drop for AAC runs with presence gaps — no AAC silence synthesis.
	if hasAudio && !refIsG711 {
		for _, seg := range segments {
			if !seg.HasAudio {
				audioMixed = true
				break
			}
		}
	}
	if audioMixed {
		// Drop audio entirely — only merge video tracks.
		logger.Warn("audio presence/config mismatch across segments, dropping audio from merged output",
			"segment_count", len(segments))
		hasAudio = false
		for _, seg := range segments {
			seg.HasAudio = false
		}
	}

	if first.SampleCount == 0 && (!hasAudio || first.AudioSampleCount == 0) {
		return "", false, fmt.Errorf("first segment has empty sample table")
	}
	return codec, hasAudio, nil
}

// predictSpanTimeline dry-runs the video pass's dwell-compression math on the
// sample table alone (no media I/O) to predict a segment's wall→file timeline.
// The ambient-audio bed sizes its output from the compressed (file) length,
// which on mildly-compressed spans exceeds the parsed audio sample count — the
// moov budget needs that prediction up front (#1024).
func predictSpanTimeline(seg *SegmentInfo, frameDur []time.Duration) (wallSec, fileSec float64) {
	ts := float64(seg.Timescale)
	if ts == 0 {
		return 0, 0
	}
	cadence := TimelapseFrameDur
	if len(frameDur) > 0 && frameDur[0] > 0 {
		cadence = frameDur[0]
	}
	gap := TimelapseGapThreshold
	if len(frameDur) > 1 && frameDur[1] > 0 {
		gap = frameDur[1]
	}
	gapTicks := float64(seg.Timescale) * gap.Seconds()
	frameTicks := uint32(float64(seg.Timescale) * cadence.Seconds())
	for _, s := range seg.Samples {
		dur := s.Duration
		if float64(dur) > gapTicks {
			dur = frameTicks
		}
		wallSec += float64(s.Duration) / ts
		fileSec += float64(dur) / ts
	}
	return wallSec, fileSec
}

// budgetMoovSize builds placeholder tracks (uniform durations, every sample
// flagged keyframe — worst-case stss so the reserved moov always fits the real
// pass) and measures the moov they would produce, plus per-sample stts
// headroom, to derive the space to reserve for the final moov. Measurement
// runs through countWriter — nothing is retained (#1011 debt 3: this pass
// used to buffer the whole placeholder moov, a retention of the same order
// as the moov itself).
func budgetMoovSize(codec string, segments []*SegmentInfo, hasAudio bool, frameDur []time.Duration) (videoTrack *mergeTrack, audioTrack *mergeTrack, moovSize int64, err error) {
	first := segments[0]

	// Count total video samples across all segments.
	var totalVideoSamples int
	for _, seg := range segments {
		totalVideoSamples += seg.SampleCount
	}

	videoTrack = &mergeTrack{
		isH265:       codec == "h265",
		sps:          first.SPS,
		pps:          first.PPS,
		vps:          first.VPS,
		timescale:    first.Timescale,
		totalSamples: totalVideoSamples,
	}
	// Parse resolution from first segment's SPS.
	switch codec {
	case "h265":
		w, h, err := ParseHEVCSPSResolution(first.SPS)
		if err != nil {
			logger.Warn("failed to parse SPS resolution", "error", err)
		}
		videoTrack.width, videoTrack.height = uint16(w), uint16(h)
	case "h264":
		w, h, err := ParseSPSResolution(first.SPS)
		if err != nil {
			logger.Warn("failed to parse SPS resolution", "error", err)
		}
		videoTrack.width, videoTrack.height = uint16(w), uint16(h)
	}
	// Populate placeholder samples so the size calculation includes per-sample tables.
	// Keyframes marked true on EVERY placeholder sample: the real pass writes
	// fewer-or-equal stss entries, so the reserved moov always fits.
	videoTrack.samples = make([]mergedSample, totalVideoSamples)
	for i := range videoTrack.samples {
		videoTrack.samples[i].duration = 33 // placeholder
		videoTrack.samples[i].isKeyFrame = true
	}
	// Build audio track placeholder for size calculation. The track's config
	// comes from the first AUDIO-BEARING segment — segments[0] may be a
	// presence gap (no audio) in a silence-padded run (#1150 ③).
	if hasAudio {
		var ref *SegmentInfo
		var totalAudioSamples int
		for _, seg := range segments {
			if ref == nil && seg.HasAudio {
				ref = seg
			}
			count := seg.AudioSampleCount
			// Ambient-bed reservation (#1024): a compressed G.711 span renders
			// one audio sample per output byte (nOut, derived from the
			// compressed file timeline) instead of its parsed samples — on
			// mildly-compressed spans nOut exceeds the parsed count, so reserve
			// the larger of the two. Non-G.711 compressed spans drop their
			// audio entirely (over-reservation is harmless).
			if seg.AudioCodec == "g711" && len(seg.AudioSamples) > 0 {
				wall, file := predictSpanTimeline(seg, frameDur)
				if wall > 0 && file > 0 && wall > 1.5*file {
					if nOut := int(file*float64(seg.AudioTimescale) + 0.5); nOut > count {
						count = nOut
					}
				}
			} else if ref != nil && ref.AudioCodec == "g711" && len(seg.AudioSamples) == 0 {
				// Silence-padding reservation (#1150 ③): one synthesized byte
				// per output tick for segments without an audio track.
				if _, file := predictSpanTimeline(seg, frameDur); file > 0 {
					if nSil := int(file*float64(ref.AudioTimescale) + 0.5); nSil > count {
						count = nSil
					}
				}
			}
			totalAudioSamples += count
		}
		audioConfig, audioCodec, audioMULaw, audioTs := first.AudioConfig, first.AudioCodec, first.G711MULaw, first.AudioTimescale
		if ref != nil {
			audioConfig, audioCodec, audioMULaw, audioTs = ref.AudioConfig, ref.AudioCodec, ref.G711MULaw, ref.AudioTimescale
		}
		audioTrack = &mergeTrack{
			isAudio:      true,
			audioConfig:  audioConfig,
			audioCodec:   audioCodec,
			g711MULaw:    audioMULaw,
			timescale:    audioTs,
			totalSamples: totalAudioSamples,
		}
		audioTrack.samples = make([]mergedSample, totalAudioSamples)
		for i := range audioTrack.samples {
			audioTrack.samples[i].duration = 23 // placeholder
		}
	}

	// Measure the placeholder moov's exact size (counted, not buffered).
	sizeW := &countWriter{}
	if err := writeMergeMoov(mp4.NewWriter(sizeW), videoTrack, audioTrack, 0, 0); err != nil {
		return nil, nil, 0, fmt.Errorf("calculate moov size: %w", err)
	}
	moovSize = sizeW.size()
	// Add headroom for stts entry expansion — real video may have varying frame durations
	// that prevent full RLE compression, requiring more stts entries than the uniform-duration
	// placeholder (which compresses to 1 entry). Max overhead: 8 bytes per sample per track.
	moovSize += int64(totalVideoSamples) * 8
	if hasAudio {
		moovSize += int64(audioTrack.totalSamples) * 8
	}

	// Clear placeholder samples; real ones will be set after streaming mdat.
	videoTrack.samples = nil
	if audioTrack != nil {
		audioTrack.samples = nil
	}
	return videoTrack, audioTrack, moovSize, nil
}

// mergeLayout captures the merged output file's fixed header geometry.
type mergeLayout struct {
	moovOffset       int64
	moovSize         int64
	mdatHeaderOffset int64
	mdatDataStart    int64
}

// writeOutputSkeleton lays down the output's fixed header geometry: ftyp, a
// zero-filled moov placeholder of the reserved size (streamed through a still
// pristine zero-filled scratch buffer — #1011 debt 3), and the mdat box
// header whose size gets patched after the sample passes.
func writeOutputSkeleton(out io.Writer, codec string, moovSize int64, zeroScratch []byte) (mergeLayout, error) {
	var layout mergeLayout
	ftypSize, err := writeMergeFtyp(out, codec)
	if err != nil {
		return layout, fmt.Errorf("write ftyp: %w", err)
	}
	layout.moovOffset = ftypSize
	layout.moovSize = moovSize
	if err := writeZeroes(out, moovSize, zeroScratch); err != nil {
		return layout, fmt.Errorf("write moov placeholder: %w", err)
	}
	layout.mdatHeaderOffset = layout.moovOffset + moovSize
	var mdatHeader [8]byte
	copy(mdatHeader[4:8], "mdat")
	if _, err := out.Write(mdatHeader[:]); err != nil {
		return layout, fmt.Errorf("write mdat header: %w", err)
	}
	layout.mdatDataStart = layout.mdatHeaderOffset + 8
	return layout, nil
}

// streamVideoSamples is the video pass: every aligned segment's video samples
// are streamed into mdat with the fixed-size copy buffer (one-pass sequential
// readahead, #754), sparse dwell durations are rewritten to the timelapse
// cadence, and the wall→file timeline map is recorded per segment.
func streamVideoSamples(ctx context.Context, out io.Writer, segments []*SegmentInfo, hasAudio bool, mdatDataStart int64, buf []byte, frameDur []time.Duration, stats *MergeStats) ([]mergedSample, int64, error) {
	var currentOffset int64
	var allVideoSamples []mergedSample

	stats.WallToFile = append(stats.WallToFile, [2]float64{0, 0})
	var wallSec, fileSec float64
	for _, seg := range segments {
		if seg.FilePath == "" {
			return nil, 0, fmt.Errorf("segment has empty FilePath")
		}

		src, err := os.Open(seg.FilePath)
		if err != nil {
			return nil, 0, fmt.Errorf("open segment %s: %w", seg.FilePath, err)
		}
		// One-pass sequential read (#754): aggressive readahead while copying,
		// and — only when no audio pass will reopen this file — drop the cache
		// at the end so read-once media stops evicting hot pages.
		fadvise.Sequential(src)

		// Sparse dwell compression (#496): segments' >2s dwell samples are
		// rewritten to TimelapseFrameDur so 1×/downloaded playback shows a
		// timelapse instead of a frozen frame. Applies to audio-bearing
		// segments too — the audio phase below renders such a span's G.711
		// ambient audio onto the compressed timeline (envelope mixdown) or
		// drops it for non-G.711 codecs.
		ts := float64(seg.Timescale)
		cadence := TimelapseFrameDur
		if len(frameDur) > 0 && frameDur[0] > 0 {
			cadence = frameDur[0]
		}
		gap := TimelapseGapThreshold
		if len(frameDur) > 1 && frameDur[1] > 0 {
			// Per-camera adaptive threshold (see resolveTimelapseGap): slow
			// cameras' normal inter-frame gaps must not read as TL dwells.
			gap = frameDur[1]
		}
		gapTicks := float64(seg.Timescale) * gap.Seconds()
		frameTicks := uint32(float64(seg.Timescale) * cadence.Seconds())

		for _, s := range seg.Samples {
			select {
			case <-ctx.Done():
				src.Close()
				return nil, 0, ctx.Err()
			default:
			}
			sampleAbsOffset := currentOffset + mdatDataStart

			_, copyErr := copySampleData(ctx, src, out, s.Offset, int64(s.Size), buf)
			if copyErr != nil {
				src.Close()
				return nil, 0, fmt.Errorf("copy sample from %s at offset %d: %w", seg.FilePath, s.Offset, copyErr)
			}

			dur := s.Duration
			if ts > 0 && float64(dur) > gapTicks {
				dur = frameTicks
				stats.TimelapseFrames++
			}
			if ts > 0 {
				wallSec += float64(s.Duration) / ts
				fileSec += float64(dur) / ts
			}

			allVideoSamples = append(allVideoSamples, mergedSample{
				offset:     sampleAbsOffset,
				size:       s.Size,
				duration:   dur,
				isKeyFrame: s.IsKeyFrame,
			})
			currentOffset += int64(s.Size)
		}

		if !hasAudio || len(seg.AudioSamples) == 0 {
			// Last consumer done (no audio pass will reopen): release cache.
			fadvise.DontNeed(src)
		}
		src.Close()
		stats.WallToFile = append(stats.WallToFile, [2]float64{wallSec, fileSec})
	}
	return allVideoSamples, currentOffset, nil
}

// streamAudioSamples is the audio pass (after the video pass): spans whose
// video timeline was compressed (#496) get their audio rendered by envelope
// mixdown instead of verbatim copy — see ambient_audio.go; non-G.711
// compressed spans drop their audio (logged), uncompressed spans keep it
// byte-for-byte.
func streamAudioSamples(ctx context.Context, out io.Writer, segments []*SegmentInfo, stats *MergeStats, mdatDataStart, startOffset int64, buf []byte) ([]mergedSample, int64, error) {
	var allAudioSamples []mergedSample
	currentOffset := startOffset

	// G.711 silence padding (#1150 ③): the run kept audio and validated
	// config-consistent — segments without an audio track get silence
	// synthesized for their output span so the track stays continuous.
	// Compressed spans skip padding (TL dwell compression intentionally
	// removes silence); AAC gap-runs never reach here (validate drops them).
	var padMULaw bool
	padTs := uint32(0)
	padG711 := false
	for _, seg := range segments {
		if seg.HasAudio {
			padG711, padMULaw, padTs = seg.AudioCodec == "g711", seg.G711MULaw, seg.AudioTimescale
			break
		}
	}
	silence := byte(0xD5) // A-law positive zero
	if padMULaw {
		silence = 0xFF // μ-law zero
	}

	for segIdx, seg := range segments {
		spanWall := stats.WallToFile[segIdx+1][0] - stats.WallToFile[segIdx][0]
		spanFile := stats.WallToFile[segIdx+1][1] - stats.WallToFile[segIdx][1]
		compressedSpan := spanWall > 0 && spanFile > 0 && spanWall > 1.5*spanFile

		if len(seg.AudioSamples) == 0 {
			// Uncovered span of a kept G.711 track: synthesize silence at one
			// byte per output tick.
			if !compressedSpan && padG711 && padTs > 0 {
				nSil := int(spanFile*float64(padTs) + 0.5)
				if nSil > 0 {
					off := currentOffset + mdatDataStart
					written := 0
					for written < nSil {
						chunk := len(buf)
						if chunk > nSil-written {
							chunk = nSil - written
						}
						for i := range chunk {
							buf[i] = silence
						}
						if _, werr := out.Write(buf[:chunk]); werr != nil {
							return nil, 0, fmt.Errorf("write silence: %w", werr)
						}
						for i := range chunk {
							allAudioSamples = append(allAudioSamples, mergedSample{
								offset:   off + int64(written+i),
								size:     1,
								duration: 1, // one tick per G.711 byte on the audio timescale
							})
						}
						written += chunk
					}
					currentOffset += int64(nSil)
					stats.SilenceSamples += nSil
				}
			}
			continue
		}

		if compressedSpan {
			if seg.AudioCodec != "g711" {
				logger.Warn("merge: dropping audio of a compressed span with unsupported codec (g711 only)",
					"path", seg.FilePath, "codec", seg.AudioCodec)
				continue
			}
			src, err := os.Open(seg.FilePath)
			if err != nil {
				return nil, 0, fmt.Errorf("open segment %s for audio: %w", seg.FilePath, err)
			}
			// Streaming ambient bed (#1011 debt 2): rendered straight from the
			// span's sample table — the span's whole G.711 track is no longer
			// buffered in memory (it grew linearly with wall time, ~4.8MB per
			// 10-minute span).
			nOut := int(spanFile*float64(seg.AudioTimescale) + 0.5)
			bedOffset := currentOffset + mdatDataStart
			n, aerr := writeAmbientBed(ctx, src, seg, nOut, out)
			fadvise.DontNeed(src) // last consumer of this file is done
			src.Close()
			if aerr != nil {
				return nil, 0, fmt.Errorf("ambient bed for %s: %w", seg.FilePath, aerr)
			}
			if n > 0 {
				for i := range n {
					allAudioSamples = append(allAudioSamples, mergedSample{
						offset:   bedOffset + int64(i),
						size:     1,
						duration: 1, // one tick per G.711 byte on the audio timescale
					})
				}
				currentOffset += int64(n)
				stats.AmbientSamples += n
			}
			continue
		}

		src, err := os.Open(seg.FilePath)
		if err != nil {
			return nil, 0, fmt.Errorf("open segment %s for audio: %w", seg.FilePath, err)
		}

		for _, s := range seg.AudioSamples {
			select {
			case <-ctx.Done():
				src.Close()
				return nil, 0, ctx.Err()
			default:
			}
			sampleAbsOffset := currentOffset + mdatDataStart

			_, copyErr := copySampleData(ctx, src, out, s.Offset, int64(s.Size), buf)
			if copyErr != nil {
				src.Close()
				return nil, 0, fmt.Errorf("copy audio sample from %s at offset %d: %w", seg.FilePath, s.Offset, copyErr)
			}

			allAudioSamples = append(allAudioSamples, mergedSample{
				offset:   sampleAbsOffset,
				size:     s.Size,
				duration: s.Duration,
			})
			currentOffset += int64(s.Size)
		}

		fadvise.DontNeed(src) // last consumer of this file is done
		src.Close()
	}
	return allAudioSamples, currentOffset, nil
}

// patchMdatAndWriteMoov finishes the output once both sample passes are done:
// patches the mdat box size, then rewrites the reserved moov region with the
// REAL sample tables through an overflow-guarded limitedWriter, and pads any
// leftover reserved space with a "free" box so parsers can traverse
// moov → free → mdat without breaking.
func patchMdatAndWriteMoov(out *os.File, layout mergeLayout, mdatBytes int64, videoTrack, audioTrack *mergeTrack, hasAudio bool, allVideoSamples, allAudioSamples []mergedSample) error {
	// Patch mdat box size.
	mdatBoxSize := uint64(8 + mdatBytes)
	if mdatBoxSize > math.MaxUint32 {
		return fmt.Errorf("mdat box size %d exceeds MaxUint32", mdatBoxSize)
	}
	if _, err := out.Seek(layout.mdatHeaderOffset, io.SeekStart); err != nil {
		return fmt.Errorf("seek to mdat header: %w", err)
	}
	var sizeBuf [4]byte
	binary.BigEndian.PutUint32(sizeBuf[:], uint32(mdatBoxSize))
	if _, err := out.Write(sizeBuf[:]); err != nil {
		return fmt.Errorf("write mdat size: %w", err)
	}

	// Go back and write the real moov box at the placeholder position.
	if _, err := out.Seek(layout.moovOffset, io.SeekStart); err != nil {
		return fmt.Errorf("seek to moov: %w", err)
	}

	// Calculate total video duration in timescale units.
	var totalVideoDuration uint64
	for _, s := range allVideoSamples {
		totalVideoDuration += uint64(s.duration)
	}
	if totalVideoDuration > math.MaxUint32 {
		return fmt.Errorf("DurationV0 overflow: video duration %d exceeds MaxUint32", totalVideoDuration)
	}
	videoTrack.duration = uint32(totalVideoDuration)
	videoTrack.samples = allVideoSamples

	// A merged product where every span was compressed with a dropped
	// non-G.711 track has zero audio samples — emit no audio track at all.
	if hasAudio && len(allAudioSamples) == 0 {
		audioTrack = nil
	}

	// Set real audio track data.
	if audioTrack != nil {
		var totalAudioDuration uint64
		for _, s := range allAudioSamples {
			totalAudioDuration += uint64(s.duration)
		}
		if totalAudioDuration > math.MaxUint32 {
			return fmt.Errorf("DurationV0 overflow: audio duration %d exceeds MaxUint32", totalAudioDuration)
		}
		audioTrack.duration = uint32(totalAudioDuration)
		audioTrack.samples = allAudioSamples
	}

	// Use a limited writer to prevent overflow into mdat.
	moovOut := &limitedWriter{w: out, remaining: layout.moovSize, pos: layout.moovOffset}
	moovWriter := mp4.NewWriter(moovOut)
	// Video chunk starts at mdatDataStart; audio chunk starts after video data.
	videoChunkOffset := layout.mdatDataStart
	var audioChunkOffset int64
	if audioTrack != nil {
		audioChunkOffset = layout.mdatDataStart
		for _, s := range allVideoSamples {
			audioChunkOffset += int64(s.size)
		}
	}
	if err := writeMergeMoov(moovWriter, videoTrack, audioTrack, videoChunkOffset, audioChunkOffset); err != nil {
		return fmt.Errorf("write moov: %w", err)
	}

	if moovOut.remaining < 0 {
		return fmt.Errorf("moov box overflow: calculated %d, actual %d", layout.moovSize, layout.moovSize-moovOut.remaining)
	}

	// If the real moov is smaller than the reserved space, pad with a "free" box.
	if moovOut.remaining > 0 {
		if err := writeMoovPad(out, moovOut.remaining); err != nil {
			return fmt.Errorf("write moov padding: %w", err)
		}
	}
	return nil
}

// writeZeroes writes n zero bytes through a zero-filled scratch chunk — the
// WriteN-style sequential zero write behind the moov placeholder and the
// free-box payload (#1011 debt 3: both used to be whole-size allocations,
// peaking at moov-order memory).
func writeZeroes(w io.Writer, n int64, zero []byte) error {
	for n > 0 {
		chunk := int64(len(zero))
		if chunk > n {
			chunk = n
		}
		if _, err := w.Write(zero[:chunk]); err != nil {
			return err
		}
		n -= chunk
	}
	return nil
}

// writeMoovPad fills the gap between the real moov's end and the reserved
// region's end with a "free" box (8-byte header + streamed zero payload). If
// the gap cannot fit a box header (<8 bytes, unreachable in practice — box
// sizes are 4-byte aligned and the per-sample headroom keeps the pad ≥ 8),
// plain zero fill avoids the previous version's slice-bounds panic.
func writeMoovPad(w io.Writer, remaining int64) error {
	if remaining < 8 {
		return writeZeroes(w, remaining, make([]byte, remaining))
	}
	var hdr [8]byte
	binary.BigEndian.PutUint32(hdr[0:4], uint32(remaining))
	copy(hdr[4:8], "free")
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	return writeZeroes(w, remaining-8, make([]byte, 64<<10))
}

// copySampleData copies size bytes from src at offset to dst using the
// provided buffer. Each buffer-sized chunk is billed to the I/O budget (#751)
// so a multi-GB merge under a small budget paces in fine-grained increments
// (short waits, prompt ctx cancellation) instead of one long park.
func copySampleData(ctx context.Context, src *os.File, dst io.Writer, offset, size int64, buf []byte) (int64, error) {
	if _, err := src.Seek(offset, io.SeekStart); err != nil {
		return 0, err
	}
	remaining := size
	var written int64
	for remaining > 0 {
		toRead := int64(len(buf))
		if toRead > remaining {
			toRead = remaining
		}
		n, err := src.Read(buf[:toRead])
		if err != nil && !errors.Is(err, io.EOF) {
			return written, err
		}
		if n == 0 {
			break
		}
		if err := waitIOBudget(ctx, int64(n)); err != nil {
			return written, err
		}
		nw, err := dst.Write(buf[:n])
		if err != nil {
			return written, err
		}
		written += int64(nw)
		remaining -= int64(n)
	}
	return written, nil
}
