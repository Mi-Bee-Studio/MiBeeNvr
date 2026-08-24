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
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/mp4util"
	"github.com/abema/go-mp4"
)

const (
	mergeBufferSize = 1 << 20 // 1MB buffer for sample data copying
)

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
func MergeMP4Segments(ctx context.Context, segments []*SegmentInfo, outputPath string) (MergeStats, error) {
	stats := MergeStats{LeadingDropped: map[int]int{}}
	if len(segments) == 0 {
		return stats, fmt.Errorf("no segments to merge")
	}

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
		return stats, ErrNoKeyframe
	}
	segments = aligned

	first := segments[0]
	codec := first.Codec

	// Validate all segments share the same codec and SPS/PPS.
	for i, seg := range segments {
		if seg.Codec != codec {
			return stats, fmt.Errorf("segment %d: codec mismatch (%s vs %s)", i, seg.Codec, codec)
		}
		if i > 0 {
			if !bytes.Equal(seg.SPS, first.SPS) || !bytes.Equal(seg.PPS, first.PPS) {
				return stats, fmt.Errorf("segment %d: SPS/PPS mismatch", i)
			}
			if codec == "h265" && !bytes.Equal(seg.VPS, first.VPS) {
				return stats, fmt.Errorf("segment %d: VPS mismatch", i)
			}
		}
	}

	// Validate audio consistency. When segments have mixed audio presence
	// (some have audio, some don't), we DROP audio from the merged output
	// rather than failing the merge. This handles camera reconnection scenarios
	// where audio_enabled was toggled or G.711 negotiation succeeded/failed
	// mid-session. Video is always preserved; losing audio in merged files is
	// acceptable — the individual source segments (if still on disk) retain it.
	hasAudio := false
	audioConfig := first.AudioConfig
	audioMixed := false
	for i, seg := range segments {
		if i == 0 {
			hasAudio = seg.HasAudio
			continue
		}
		if seg.HasAudio != hasAudio {
			audioMixed = true
		}
		if seg.HasAudio && !bytes.Equal(seg.AudioConfig, audioConfig) {
			// Different audio codec config — also treat as mixed.
			audioMixed = true
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

	// Validate that segments have samples.
	if first.SampleCount == 0 && (!hasAudio || first.AudioSampleCount == 0) {
		return stats, fmt.Errorf("first segment has empty sample table")
	}

	out, err := os.Create(outputPath)
	if err != nil {
		return stats, fmt.Errorf("create output: %w", err)
	}
	defer out.Close()

	// Step 1: Write ftyp box.
	ftypSize, err := writeMergeFtyp(out, codec)
	if err != nil {
		return stats, fmt.Errorf("write ftyp: %w", err)
	}

	// Step 2: Calculate moov size by writing to a buffer with placeholder offsets.
	// Count total video samples across all segments.
	var totalVideoSamples int
	for _, seg := range segments {
		totalVideoSamples += seg.SampleCount
	}

	videoTrack := &mergeTrack{
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
		w, h, err := parseHEVCSPSResolution(first.SPS)
		if err != nil {
			logger.Warn("failed to parse SPS resolution", "error", err)
		}
		videoTrack.width, videoTrack.height = uint16(w), uint16(h)
	case "h264":
		w, h, err := parseSPSResolution(first.SPS)
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
	// Build audio track placeholder for size calculation.
	var audioTrack *mergeTrack
	var totalAudioSamples int
	if hasAudio {
		for _, seg := range segments {
			totalAudioSamples += seg.AudioSampleCount
		}
		audioTrack = &mergeTrack{
			isAudio:      true,
			audioConfig:  audioConfig,
			audioCodec:   first.AudioCodec,
			g711MULaw:    first.G711MULaw,
			timescale:    first.AudioTimescale,
			totalSamples: totalAudioSamples,
		}
		audioTrack.samples = make([]mergedSample, totalAudioSamples)
		for i := range audioTrack.samples {
			audioTrack.samples[i].duration = 23 // placeholder
		}
	}
	// Write moov to a buffer to get its exact size.
	moovBuf := &bytesWriter{}
	moovW := mp4.NewWriter(moovBuf)
	if err := writeMergeMoov(moovW, videoTrack, audioTrack, 0, 0); err != nil {
		return stats, fmt.Errorf("calculate moov size: %w", err)
	}
	moovSize := moovBuf.len()
	// Add headroom for stts entry expansion — real video may have varying frame durations
	// that prevent full RLE compression, requiring more stts entries than the uniform-duration
	// placeholder (which compresses to 1 entry). Max overhead: 8 bytes per sample per track.
	moovSize += int64(totalVideoSamples) * 8
	if hasAudio {
		moovSize += int64(totalAudioSamples) * 8
	}

	// Clear placeholder samples; real ones will be set after streaming mdat.
	videoTrack.samples = nil
	if audioTrack != nil {
		audioTrack.samples = nil
	}

	// Step 3: Write placeholder moov at the correct position.
	moovOffset := ftypSize
	moovPlaceholder := make([]byte, moovSize)
	if _, err := out.Write(moovPlaceholder); err != nil {
		return stats, fmt.Errorf("write moov placeholder: %w", err)
	}

	// Step 4: Write mdat box header (size placeholder + "mdat").
	mdatHeaderOffset := moovOffset + moovSize
	var mdatHeader [8]byte
	copy(mdatHeader[4:8], "mdat")
	if _, err := out.Write(mdatHeader[:]); err != nil {
		return stats, fmt.Errorf("write mdat header: %w", err)
	}
	mdatDataStart := mdatHeaderOffset + 8

	// Step 5: Stream sample data from each segment into the output.
	buf := make([]byte, mergeBufferSize)
	var currentOffset int64
	var allVideoSamples []mergedSample

	stats.WallToFile = append(stats.WallToFile, [2]float64{0, 0})
	var wallSec, fileSec float64
	for _, seg := range segments {
		if seg.FilePath == "" {
			return stats, fmt.Errorf("segment has empty FilePath")
		}

		src, err := os.Open(seg.FilePath)
		if err != nil {
			return stats, fmt.Errorf("open segment %s: %w", seg.FilePath, err)
		}

		// Sparse dwell compression (#496): no-audio segments (adaptive
		// timelapse drops the disk audio track) have their >2s dwell samples
		// rewritten to timelapseFrameDur so 1×/downloaded playback shows a
		// timelapse instead of a frozen frame. Audio-bearing segments keep
		// real durations — their audio track would desync.
		ts := float64(seg.Timescale)
		compress := !seg.HasAudio && seg.Timescale > 0
		gapTicks := float64(seg.Timescale) * TimelapseGapThreshold.Seconds()
		frameTicks := uint32(float64(seg.Timescale) * TimelapseFrameDur.Seconds())

		for _, s := range seg.Samples {
			select {
			case <-ctx.Done():
				src.Close()
				return stats, ctx.Err()
			default:
			}
			sampleAbsOffset := currentOffset + mdatDataStart

			_, copyErr := copySampleData(src, out, s.Offset, int64(s.Size), buf)
			if copyErr != nil {
				src.Close()
				return stats, fmt.Errorf("copy sample from %s at offset %d: %w", seg.FilePath, s.Offset, copyErr)
			}

			dur := s.Duration
			if compress && float64(dur) > gapTicks {
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

		src.Close()
		stats.WallToFile = append(stats.WallToFile, [2]float64{wallSec, fileSec})
	}

	// Stream audio samples after video samples. Spans whose video timeline was
	// compressed (#496) get their audio rendered by envelope mixdown instead of
	// verbatim copy — see ambient_audio.go; non-G.711 compressed spans drop
	// their audio (logged), uncompressed spans keep it byte-for-byte.
	var allAudioSamples []mergedSample
	if hasAudio {
		for segIdx, seg := range segments {
			if len(seg.AudioSamples) == 0 {
				continue
			}

			spanWall := stats.WallToFile[segIdx+1][0] - stats.WallToFile[segIdx][0]
			spanFile := stats.WallToFile[segIdx+1][1] - stats.WallToFile[segIdx][1]
			compressedSpan := spanWall > 0 && spanFile > 0 && spanWall > 1.5*spanFile

			if compressedSpan {
				if seg.AudioCodec != "g711" {
					logger.Warn("merge: dropping audio of a compressed span with unsupported codec (g711 only)",
						"path", seg.FilePath, "codec", seg.AudioCodec)
					continue
				}
				src, err := os.Open(seg.FilePath)
				if err != nil {
					return stats, fmt.Errorf("open segment %s for audio: %w", seg.FilePath, err)
				}
				raw := make([]byte, 0, 1<<16)
				for _, s := range seg.AudioSamples {
					chunk := make([]byte, s.Size)
					if _, err := src.ReadAt(chunk, s.Offset); err != nil {
						src.Close()
						return stats, fmt.Errorf("read audio sample from %s at offset %d: %w", seg.FilePath, s.Offset, err)
					}
					raw = append(raw, chunk...)
				}
				src.Close()

				nOut := int(spanFile*float64(seg.AudioTimescale) + 0.5)
				bed := mixdownAmbient(seg.G711MULaw, raw, nOut)
				if len(bed) > 0 {
					bedOffset := currentOffset + mdatDataStart
					if _, err := out.Write(bed); err != nil {
						return stats, fmt.Errorf("write ambient bed: %w", err)
					}
					for i := range bed {
						allAudioSamples = append(allAudioSamples, mergedSample{
							offset:   bedOffset + int64(i),
							size:     1,
							duration: 1, // one tick per G.711 byte on the audio timescale
						})
					}
					currentOffset += int64(len(bed))
					stats.AmbientSamples += len(bed)
				}
				continue
			}

			src, err := os.Open(seg.FilePath)
			if err != nil {
				return stats, fmt.Errorf("open segment %s for audio: %w", seg.FilePath, err)
			}

			for _, s := range seg.AudioSamples {
				select {
				case <-ctx.Done():
					src.Close()
					return stats, ctx.Err()
				default:
				}
				sampleAbsOffset := currentOffset + mdatDataStart

				_, copyErr := copySampleData(src, out, s.Offset, int64(s.Size), buf)
				if copyErr != nil {
					src.Close()
					return stats, fmt.Errorf("copy audio sample from %s at offset %d: %w", seg.FilePath, s.Offset, copyErr)
				}

				allAudioSamples = append(allAudioSamples, mergedSample{
					offset:   sampleAbsOffset,
					size:     s.Size,
					duration: s.Duration,
				})
				currentOffset += int64(s.Size)
			}

			src.Close()
		}
	}

	// Step 6: Patch mdat box size.
	mdatBoxSize := uint64(8 + currentOffset)
	if mdatBoxSize > math.MaxUint32 {
		return stats, fmt.Errorf("mdat box size %d exceeds MaxUint32", mdatBoxSize)
	}
	if _, err := out.Seek(mdatHeaderOffset, io.SeekStart); err != nil {
		return stats, fmt.Errorf("seek to mdat header: %w", err)
	}
	var sizeBuf [4]byte
	binary.BigEndian.PutUint32(sizeBuf[:], uint32(mdatBoxSize))
	if _, err := out.Write(sizeBuf[:]); err != nil {
		return stats, fmt.Errorf("write mdat size: %w", err)
	}

	// Step 7: Go back and write the real moov box at the placeholder position.
	if _, err := out.Seek(moovOffset, io.SeekStart); err != nil {
		return stats, fmt.Errorf("seek to moov: %w", err)
	}

	// Calculate total video duration in timescale units.
	var totalVideoDuration uint64
	for _, s := range allVideoSamples {
		totalVideoDuration += uint64(s.duration)
	}
	if totalVideoDuration > math.MaxUint32 {
		return stats, fmt.Errorf("DurationV0 overflow: video duration %d exceeds MaxUint32", totalVideoDuration)
	}
	videoTrack.duration = uint32(totalVideoDuration)
	videoTrack.samples = allVideoSamples

	// Set real audio track data.
	if hasAudio {
		var totalAudioDuration uint64
		for _, s := range allAudioSamples {
			totalAudioDuration += uint64(s.duration)
		}
		if totalAudioDuration > math.MaxUint32 {
			return stats, fmt.Errorf("DurationV0 overflow: audio duration %d exceeds MaxUint32", totalAudioDuration)
		}
		audioTrack.duration = uint32(totalAudioDuration)
		audioTrack.samples = allAudioSamples
	}

	// Use a limited writer to prevent overflow into mdat.
	moovOut := &limitedWriter{w: out, remaining: moovSize, pos: moovOffset}
	moovWriter := mp4.NewWriter(moovOut)
	// Video chunk starts at mdatDataStart; audio chunk starts after video data.
	videoChunkOffset := mdatDataStart
	var audioChunkOffset int64
	if hasAudio {
		audioChunkOffset = mdatDataStart
		for _, s := range allVideoSamples {
			audioChunkOffset += int64(s.size)
		}
	}
	if err := writeMergeMoov(moovWriter, videoTrack, audioTrack, videoChunkOffset, audioChunkOffset); err != nil {
		return stats, fmt.Errorf("write moov: %w", err)
	}

	if moovOut.remaining < 0 {
		return stats, fmt.Errorf("moov box overflow: calculated %d, actual %d", moovSize, moovSize-moovOut.remaining)
	}

	// If the real moov is smaller than the reserved space, pad with a "free" box.
	// This ensures parsers can traverse from moov → free → mdat without breaking.
	if moovOut.remaining > 0 {
		padBuf := make([]byte, moovOut.remaining)
		binary.BigEndian.PutUint32(padBuf[0:4], uint32(moovOut.remaining))
		copy(padBuf[4:8], "free")
		if _, err := out.Write(padBuf); err != nil {
			return stats, fmt.Errorf("write moov padding: %w", err)
		}
	}

	// Sync and close.
	if err := out.Sync(); err != nil {
		return stats, fmt.Errorf("sync output: %w", err)
	}

	return stats, nil
}

// copySampleData copies size bytes from src at offset to dst using the provided buffer.
func copySampleData(src *os.File, dst io.Writer, offset, size int64, buf []byte) (int64, error) {
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
		nw, err := dst.Write(buf[:n])
		if err != nil {
			return written, err
		}
		written += int64(nw)
		remaining -= int64(n)
	}
	return written, nil
}

// mergeTrack holds track info for building the merged moov box.
type mergeTrack struct {
	isH265       bool
	isAudio      bool
	sps          []byte
	pps          []byte
	vps          []byte
	audioConfig  []byte
	audioCodec   string // "g711" for G.711 audio, empty for AAC
	g711MULaw    bool   // true=μ-law, false=A-law
	timescale    uint32
	totalSamples int
	duration     uint32
	width        uint16
	height       uint16
	samples      []mergedSample
}

// writeMergeMoov writes a complete moov box for the merged output.
func writeMergeMoov(w *mp4.Writer, videoTrack *mergeTrack, audioTrack *mergeTrack, videoChunkOffset, audioChunkOffset int64) error {
	_, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("moov")})
	if err != nil {
		return err
	}
	if err := writeMergeMvhd(w, videoTrack, audioTrack != nil); err != nil {
		return err
	}
	if err := writeMergeTrak(w, videoTrack, videoChunkOffset); err != nil {
		return err
	}
	if audioTrack != nil {
		if err := writeMergeTrak(w, audioTrack, audioChunkOffset); err != nil {
			return err
		}
	}
	_, err = w.EndBox()
	return err
}

func writeMergeMvhd(w *mp4.Writer, tr *mergeTrack, hasAudio bool) error {
	bi, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("mvhd")})
	if err != nil {
		return err
	}
	nextTrackID := uint32(2)
	if hasAudio {
		nextTrackID = 3
	}
	mvhd := &mp4.Mvhd{
		Timescale:   tr.timescale,
		DurationV0:  tr.duration,
		Rate:        0x00010000,
		Volume:      0x0100,
		NextTrackID: nextTrackID,
		Matrix: [9]int32{
			0x00010000, 0, 0,
			0, 0x00010000, 0,
			0, 0, 0x40000000,
		},
	}
	if _, err := mp4.Marshal(w, mvhd, mp4.Context{}); err != nil {
		return err
	}
	_, err = w.EndBox()
	_ = bi
	return err
}

func writeMergeTrak(w *mp4.Writer, tr *mergeTrack, chunkOffset int64) error {
	bi, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("trak")})
	if err != nil {
		return err
	}
	// tkhd — width/height unknown from merge, use 0 (players infer from stream).
	bi2, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("tkhd")})
	if err != nil {
		return err
	}
	trackID := uint32(1)
	if tr.isAudio {
		trackID = 2
	}
	tkhd := &mp4.Tkhd{
		TrackID:    trackID,
		DurationV0: tr.duration,
		Width:      uint32(tr.width) << 16,
		Height:     uint32(tr.height) << 16,
		Matrix: [9]int32{
			0x00010000, 0, 0,
			0, 0x00010000, 0,
			0, 0, 0x40000000,
		},
	}
	if _, err := mp4.Marshal(w, tkhd, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	_ = bi2
	if err := writeMergeMdia(w, tr, chunkOffset); err != nil {
		return err
	}
	_, err = w.EndBox()
	_ = bi
	return err
}

func writeMergeMdia(w *mp4.Writer, tr *mergeTrack, chunkOffset int64) error {
	bi, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("mdia")})
	if err != nil {
		return err
	}
	// mdhd
	bi2, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("mdhd")})
	if err != nil {
		return err
	}
	mdhd := &mp4.Mdhd{
		Timescale:  tr.timescale,
		DurationV0: tr.duration,
		Language:   [3]byte{0x15, 0xC0, 0x00}, // 'und' packed
	}
	if _, err := mp4.Marshal(w, mdhd, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	_ = bi2
	// hdlr
	bi3, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("hdlr")})
	if err != nil {
		return err
	}
	var hdlr *mp4.Hdlr
	if tr.isAudio {
		hdlr = &mp4.Hdlr{
			HandlerType: [4]byte{'s', 'o', 'u', 'n'},
			Name:        "SoundHandler\x00",
		}
	} else {
		hdlr = &mp4.Hdlr{
			HandlerType: [4]byte{'v', 'i', 'd', 'e'},
			Name:        "VideoHandler\x00",
		}
	}
	if _, err := mp4.Marshal(w, hdlr, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	_ = bi3
	// minf > stbl
	if err := writeMergeMinf(w, tr, chunkOffset); err != nil {
		return err
	}
	_, err = w.EndBox()
	_ = bi
	return err
}

func writeMergeMinf(w *mp4.Writer, tr *mergeTrack, chunkOffset int64) error {
	bi, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("minf")})
	if err != nil {
		return err
	}
	// vmhd or smhd
	if tr.isAudio {
		// smhd
		bi2, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("smhd")})
		if err != nil {
			return err
		}
		if _, err := mp4.Marshal(w, &mp4.Smhd{}, mp4.Context{}); err != nil {
			return err
		}
		if _, err := w.EndBox(); err != nil {
			return err
		}
		_ = bi2
	} else {
		// vmhd
		bi2, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("vmhd")})
		if err != nil {
			return err
		}
		if _, err := mp4.Marshal(w, &mp4.Vmhd{Graphicsmode: 0}, mp4.Context{}); err != nil {
			return err
		}
		if _, err := w.EndBox(); err != nil {
			return err
		}
		_ = bi2
	}
	// dinf > dref > url
	bi3, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("dinf")})
	if err != nil {
		return err
	}
	bi4, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("dref")})
	if err != nil {
		return err
	}
	if _, err := mp4.Marshal(w, &mp4.Dref{EntryCount: 1}, mp4.Context{}); err != nil {
		return err
	}
	bi5, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("url ")})
	if err != nil {
		return err
	}
	if _, err := mp4.Marshal(w, &mp4.Url{Location: ""}, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	_ = bi5
	if _, err := w.EndBox(); err != nil {
		return err
	}
	_ = bi4
	if _, err := w.EndBox(); err != nil {
		return err
	}
	_ = bi3
	// stbl
	if err := writeMergeStbl(w, tr, chunkOffset); err != nil {
		return err
	}
	_, err = w.EndBox()
	_ = bi
	return err
}

func writeMergeStbl(w *mp4.Writer, tr *mergeTrack, chunkOffset int64) error {
	bi, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("stbl")})
	if err != nil {
		return err
	}
	samples := tr.samples
	n := len(samples)

	// stsd
	bi2, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("stsd")})
	if err != nil {
		return err
	}
	if _, err := mp4.Marshal(w, &mp4.Stsd{EntryCount: 1}, mp4.Context{}); err != nil {
		return err
	}
	if tr.isAudio {
		if err := writeMergeAudioSampleEntry(w, tr); err != nil {
			return err
		}
	} else if tr.isH265 {
		if err := writeMergeH265SampleEntry(w, tr); err != nil {
			return err
		}
	} else {
		if err := writeMergeH264SampleEntry(w, tr); err != nil {
			return err
		}
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	_ = bi2

	// stts — run-length encoded (compressed).
	bi6, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("stts")})
	if err != nil {
		return err
	}
	var sttsEntries []mp4.SttsEntry
	if n > 0 {
		sttsEntries = make([]mp4.SttsEntry, 0, n)
		prevDur := samples[0].duration
		runCount := uint32(1)
		for i := 1; i < n; i++ {
			if samples[i].duration == prevDur {
				runCount++
				continue
			}
			sttsEntries = append(sttsEntries, mp4.SttsEntry{
				SampleCount: runCount,
				SampleDelta: prevDur,
			})
			prevDur = samples[i].duration
			runCount = 1
		}
		// Flush final run.
		sttsEntries = append(sttsEntries, mp4.SttsEntry{
			SampleCount: runCount,
			SampleDelta: prevDur,
		})
	}
	if _, err := mp4.Marshal(w, &mp4.Stts{EntryCount: uint32(len(sttsEntries)), Entries: sttsEntries}, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	_ = bi6

	// stsc — all samples in one chunk, SampleDescriptionIndex MUST be 1.
	bi7, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("stsc")})
	if err != nil {
		return err
	}
	stscEntries := []mp4.StscEntry{
		{FirstChunk: 1, SamplesPerChunk: uint32(n), SampleDescriptionIndex: 1},
	}
	if n == 0 {
		stscEntries = nil
	}
	if _, err := mp4.Marshal(w, &mp4.Stsc{EntryCount: uint32(len(stscEntries)), Entries: stscEntries}, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	_ = bi7

	// stsz
	bi8, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("stsz")})
	if err != nil {
		return err
	}
	sizes := make([]uint32, n)
	for i, s := range samples {
		sizes[i] = s.size
	}
	if _, err := mp4.Marshal(w, &mp4.Stsz{SampleSize: 0, SampleCount: uint32(n), EntrySize: sizes}, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	_ = bi8

	// stss — sync sample table (1-indexed). Lets parsers (VOD fragmenter, other
	// players) find keyframes without probing media bytes. Written whenever any
	// keyframe exists — including the all-sync case — so the SIZE-estimation
	// pass (which marks every placeholder sample as a keyframe = worst case)
	// never under-reserves the moov.
	var syncSamples []uint32
	for i, s := range samples {
		if s.isKeyFrame {
			syncSamples = append(syncSamples, uint32(i+1))
		}
	}
	if len(syncSamples) > 0 {
		biSS, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("stss")})
		if err != nil {
			return err
		}
		stss := &mp4.Stss{EntryCount: uint32(len(syncSamples)), SampleNumber: syncSamples}
		if _, err := mp4.Marshal(w, stss, mp4.Context{}); err != nil {
			return err
		}
		if _, err := w.EndBox(); err != nil {
			return err
		}
		_ = biSS
	}

	// stco or co64 — single chunk at chunkOffset.
	if uint64(chunkOffset) > math.MaxUint32 {
		bi9, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("co64")})
		if err != nil {
			return err
		}
		co64 := &mp4.Co64{
			EntryCount:  0,
			ChunkOffset: nil,
		}
		if n > 0 {
			co64.EntryCount = 1
			co64.ChunkOffset = []uint64{uint64(chunkOffset)}
		}
		if _, err := mp4.Marshal(w, co64, mp4.Context{}); err != nil {
			return err
		}
		if _, err := w.EndBox(); err != nil {
			return err
		}
		_ = bi9
	} else {
		bi9, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("stco")})
		if err != nil {
			return err
		}
		stco := &mp4.Stco{EntryCount: 0, ChunkOffset: nil}
		if n > 0 {
			stco.EntryCount = 1
			stco.ChunkOffset = []uint32{uint32(chunkOffset)}
		}
		if _, err := mp4.Marshal(w, stco, mp4.Context{}); err != nil {
			return err
		}
		if _, err := w.EndBox(); err != nil {
			return err
		}
		_ = bi9
	}

	_, err = w.EndBox()
	_ = bi
	return err
}

// writeMergeAudioSampleEntry writes mp4a+esds (AAC) or ulaw/alaw (G.711) sample entry.
func writeMergeAudioSampleEntry(w *mp4.Writer, tr *mergeTrack) error {
	switch tr.audioCodec {
	case "g711":
		return writeMergeG711SampleEntry(w, tr)
	case "opus":
		return writeMergeOpusSampleEntry(w, tr)
	default:
		return writeMergeAACSampleEntry(w, tr)
	}
}

// writeMergeAACSampleEntry writes mp4a + esds boxes for AAC audio.
func writeMergeAACSampleEntry(w *mp4.Writer, tr *mergeTrack) error {
	bi, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("mp4a")})
	if err != nil {
		return err
	}

	// Parse AudioSpecificConfig for channel count and sample rate.
	channelCount, sampleRate := parseAudioConfig(tr.audioConfig)

	mp4a := &mp4.AudioSampleEntry{
		SampleEntry: mp4.SampleEntry{
			AnyTypeBox:         mp4.AnyTypeBox{Type: mp4.StrToBoxType("mp4a")},
			DataReferenceIndex: 1,
		},
		EntryVersion: 0,
		ChannelCount: channelCount,
		SampleSize:   16,
		SampleRate:   sampleRate << 16, // fixed-point 16.16
	}
	if _, err := mp4.Marshal(w, mp4a, mp4.Context{}); err != nil {
		return err
	}

	// esds box
	bi2, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("esds")})
	if err != nil {
		return err
	}
	esds := buildMergeEsds(tr.audioConfig)
	if _, err := mp4.Marshal(w, esds, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	_ = bi2

	if _, err := w.EndBox(); err != nil {
		return err
	}
	_ = bi
	return nil
}

// writeMergeG711SampleEntry writes ulaw (μ-law) or alaw (A-law) sample entry for G.711 audio.
// G.711 is raw PCM — no esds or codec config boxes needed.
// Written as raw bytes since go-mp4 only registers AudioSampleEntry for mp4a/enca.
func writeMergeG711SampleEntry(w *mp4.Writer, tr *mergeTrack) error {
	boxType := mp4.StrToBoxType("ulaw")
	if !tr.g711MULaw {
		boxType = mp4.StrToBoxType("alaw")
	}

	bi, err := w.StartBox(&mp4.BoxInfo{Type: boxType})
	if err != nil {
		return err
	}

	// G.711: mono, 8-bit samples, 8000 Hz.
	sampleRate := uint32(8000)

	// Write AudioSampleEntry fields manually (same layout as mp4a without esds):
	// reserved[6] + data_reference_index[2] + entry_version[2] + reserved[6] +
	// channel_count[2] + sample_size[2] + pre_defined[2] + reserved[2] + sample_rate[4]
	buf := make([]byte, 28)
	buf[7] = 0x01  // data_reference_index = 1
	buf[17] = 0x01 // channel_count = 1 (mono)
	buf[19] = 0x08 // sample_size = 8
	rateFixed := sampleRate << 16
	buf[24] = byte(rateFixed >> 24)
	buf[25] = byte(rateFixed >> 16)
	buf[26] = byte(rateFixed >> 8)
	buf[27] = byte(rateFixed)

	if _, err := w.Write(buf); err != nil {
		return err
	}

	if _, err := w.EndBox(); err != nil {
		return err
	}
	_ = bi
	return nil
}

// writeMergeOpusSampleEntry writes Opus sample entry + dOps box for Opus audio.
func writeMergeOpusSampleEntry(w *mp4.Writer, tr *mergeTrack) error {
	bi, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeOpus()})
	if err != nil {
		return err
	}

	chans := uint16(1) // mono default for camera audio

	opus := &mp4.AudioSampleEntry{
		SampleEntry: mp4.SampleEntry{
			AnyTypeBox:         mp4.AnyTypeBox{Type: mp4.BoxTypeOpus()},
			DataReferenceIndex: 1,
		},
		EntryVersion: 0,
		ChannelCount: chans,
		SampleSize:   16,
		SampleRate:   48000 << 16,
	}
	if _, err := mp4.Marshal(w, opus, mp4.Context{}); err != nil {
		return err
	}

	// dOps box
	bi2, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeDOps()})
	if err != nil {
		return err
	}
	dOps := &mp4.DOps{
		Version:              0,
		OutputChannelCount:   uint8(chans),
		PreSkip:              0,
		InputSampleRate:      48000,
		OutputGain:           0,
		ChannelMappingFamily: 0,
	}
	if _, err := mp4.Marshal(w, dOps, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	_ = bi2

	if _, err := w.EndBox(); err != nil {
		return err
	}
	_ = bi
	return nil
}

// parseAudioConfig extracts channel count and sample rate from an AAC AudioSpecificConfig.
func parseAudioConfig(config []byte) (uint16, uint32) {
	channelCount := uint16(2)
	sampleRate := uint32(44100)

	if len(config) >= 2 {
		sampleRateIndex := (config[0] >> 3) & 0x0F
		if sampleRateIndex == 0x0F && len(config) >= 4 {
			sampleRate = uint32(config[1])<<16 | uint32(config[2])<<8 | uint32(config[3]&0xFC)<<4>>4
		} else if sampleRateIndex < 15 {
			sampleRates := [...]uint32{96000, 88200, 64000, 48000, 44100, 32000, 24000, 22050, 16000, 12000, 11025, 8000, 7350}
			if int(sampleRateIndex) < len(sampleRates) {
				sampleRate = sampleRates[sampleRateIndex]
			}
		}
		channelConfig := ((config[0] & 0x01) << 2) | ((config[1] >> 6) & 0x03)
		if channelConfig > 0 {
			channelCount = uint16(channelConfig)
		}
	}

	return channelCount, sampleRate
}

// buildMergeEsds constructs an esds (Elementary Stream Descriptor) box for AAC.
// Same structure as internal/muxer buildEsds.
func buildMergeEsds(audioConfig []byte) *mp4.Esds {
	return &mp4.Esds{
		FullBox: mp4.FullBox{Version: 0},
		Descriptors: []mp4.Descriptor{
			{
				Tag:  mp4.ESDescrTag,
				Size: uint32(25 + len(audioConfig)),
				ESDescriptor: &mp4.ESDescriptor{
					ESID:           1,
					StreamPriority: 0,
				},
				DecoderConfigDescriptor: nil,
			},
			{
				Tag:  mp4.DecoderConfigDescrTag,
				Size: uint32(13 + len(audioConfig)),
				DecoderConfigDescriptor: &mp4.DecoderConfigDescriptor{
					ObjectTypeIndication: 0x40, // Audio ISO/IEC 14496-3 (AAC)
					StreamType:           0x05, // AudioStream
					UpStream:             false,
					Reserved:             true,
					BufferSizeDB:         0,
					MaxBitrate:           128000,
					AvgBitrate:           128000,
				},
			},
			{
				Tag:  mp4.DecSpecificInfoTag,
				Size: uint32(len(audioConfig)),
				Data: audioConfig,
			},
			{
				Tag:  mp4.SLConfigDescrTag,
				Size: 1,
				Data: []byte{0x02}, // predefined: use timestamps
			},
		},
	}
}

// writeMergeH264SampleEntry writes avc1 + avcC boxes for H.264.
func writeMergeH264SampleEntry(w *mp4.Writer, tr *mergeTrack) error {
	// Guard against empty/truncated SPS/PPS — these can occur in corrupted
	// segments (camera disconnect mid-IDR, incomplete writes). Without this
	// guard, accessing sps[1] panics and kills the entire process.
	if len(tr.sps) < 4 {
		return fmt.Errorf("h264 sample entry: SPS too short (%d bytes, need >=4)", len(tr.sps))
	}
	if len(tr.pps) < 1 {
		return fmt.Errorf("h264 sample entry: PPS too short (%d bytes, need >=1)", len(tr.pps))
	}

	bi, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("avc1")})
	if err != nil {
		return err
	}
	avc1 := &mp4.VisualSampleEntry{
		SampleEntry: mp4.SampleEntry{
			AnyTypeBox:         mp4.AnyTypeBox{Type: mp4.StrToBoxType("avc1")},
			DataReferenceIndex: 1,
		},
		Width:           uint16(tr.width),
		Height:          uint16(tr.height),
		Horizresolution: 0x00480000,
		Vertresolution:  0x00480000,
		FrameCount:      1,
		Depth:           0x0018,
	}
	if _, err := mp4.Marshal(w, avc1, mp4.Context{}); err != nil {
		return err
	}
	bi2, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("avcC")})
	if err != nil {
		return err
	}
	avcC := mp4util.BuildAvcC(tr.sps, tr.pps)
	if _, err := mp4.Marshal(w, avcC, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	_ = bi2
	if _, err := w.EndBox(); err != nil {
		return err
	}
	_ = bi
	return nil
}

// writeMergeH265SampleEntry writes hvc1 + hvcC boxes for H.265.
func writeMergeH265SampleEntry(w *mp4.Writer, tr *mergeTrack) error {
	bi, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("hvc1")})
	if err != nil {
		return err
	}
	hvc1 := &mp4.VisualSampleEntry{
		SampleEntry: mp4.SampleEntry{
			AnyTypeBox:         mp4.AnyTypeBox{Type: mp4.StrToBoxType("hvc1")},
			DataReferenceIndex: 1,
		},
		Width:           uint16(tr.width),
		Height:          uint16(tr.height),
		Horizresolution: 0x00480000,
		Vertresolution:  0x00480000,
		FrameCount:      1,
		Depth:           0x0018,
	}
	if _, err := mp4.Marshal(w, hvc1, mp4.Context{}); err != nil {
		return err
	}
	bi2, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType("hvcC")})
	if err != nil {
		return err
	}
	hvcC := mp4util.BuildHvcC(tr.vps, tr.sps, tr.pps)
	if _, err := mp4.Marshal(w, hvcC, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	_ = bi2
	if _, err := w.EndBox(); err != nil {
		return err
	}
	_ = bi
	return nil
}

// writeMergeFtyp writes a minimal ftyp box to the output file.
func writeMergeFtyp(w io.Writer, codec string) (int64, error) {
	brands := [][4]byte{
		{'i', 's', 'o', 'm'},
		{'i', 's', 'o', '2'},
		{'m', 'p', '4', '1'},
	}
	if codec == "h264" {
		brands = append(brands, [4]byte{'a', 'v', 'c', '1'})
	} else if codec == "h265" {
		brands = append(brands, [4]byte{'h', 'e', 'v', '1'})
	}

	boxSize := uint32(8 + 4 + 4 + 4*len(brands))
	var buf [4]byte
	binary.BigEndian.PutUint32(buf[:], boxSize)
	if _, err := w.Write(buf[:]); err != nil {
		return 0, err
	}
	if _, err := w.Write([]byte("ftyp")); err != nil {
		return 0, err
	}
	if _, err := w.Write([]byte("isom")); err != nil {
		return 0, err
	}
	binary.BigEndian.PutUint32(buf[:], 0)
	if _, err := w.Write(buf[:]); err != nil {
		return 0, err
	}
	for _, b := range brands {
		if _, err := w.Write(b[:]); err != nil {
			return 0, err
		}
	}
	return int64(boxSize), nil
}

// --- Helper types ---

// bytesWriter implements io.WriteSeeker backed by a byte buffer.
// Used to pre-calculate moov box size.
type bytesWriter struct {
	data []byte
	pos  int64
}

func (b *bytesWriter) Write(p []byte) (int, error) {
	if b.pos+int64(len(p)) > int64(len(b.data)) {
		grow := b.pos + int64(len(p)) - int64(len(b.data))
		b.data = append(b.data, make([]byte, grow)...)
	}
	copy(b.data[b.pos:], p)
	b.pos += int64(len(p))
	return len(p), nil
}

func (b *bytesWriter) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	case 0:
		b.pos = offset
	case 1:
		b.pos += offset
	case 2:
		b.pos = int64(len(b.data)) + offset
	}
	if b.pos < 0 {
		b.pos = 0
	}
	return b.pos, nil
}

func (b *bytesWriter) len() int64 {
	return int64(len(b.data))
}

// limitedWriter wraps an io.WriteSeeker and limits the total bytes written.
// Used to write moov box in-place without overflowing into mdat.
// It tracks the actual file position so seeks are properly accounted for.
type limitedWriter struct {
	w         io.WriteSeeker
	remaining int64
	pos       int64 // tracks actual file position
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	if l.remaining <= 0 {
		return 0, io.EOF
	}
	if int64(len(p)) > l.remaining {
		p = p[:l.remaining]
	}
	n, err := l.w.Write(p)
	l.remaining -= int64(n)
	l.pos += int64(n)
	return n, err
}

// Seek delegates to the underlying writer.
// Adjusts remaining based on position changes to prevent overflow.
func (l *limitedWriter) Seek(offset int64, whence int) (int64, error) {
	newPos, err := l.w.Seek(offset, whence)
	if err != nil {
		return newPos, err
	}
	// Adjust remaining: forward seek reduces, backward seek increases.
	delta := newPos - l.pos
	l.remaining -= delta
	l.pos = newPos
	return newPos, nil
}
