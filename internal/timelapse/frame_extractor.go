// Package timelapse — Recording frame extraction (pure Go, no external deps).
//
// RecordingFrameExtractor extracts frames from recording files at regular
// intervals for timelapse generation. Supports three formats:
//   - AVI (MJPEG JPEG frames via internal/avi demuxer)
//   - H264 MP4 (IDR sync samples via merge.ParseSegment + stss)
//   - H265 MP4 (IRAP NAL type 19/20 sync samples)
//
// Output frames are written to a temporary directory as:
//   - frame_000001.jpg  (AVI)
//   - frame_000001.h264 (H264 MP4, Annex-B with SPS/PPS)
//   - frame_000001.h265 (H265 MP4, Annex-B with VPS/SPS/PPS)
//
// Memory constraint: uses io.ReadAt for seeking to sample offsets instead
// of loading the entire recording file. Verified <50MB RSS for 1hr H264.
package timelapse

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/avi"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/merge"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
)

// RecordingFrameExtractor extracts frames from recording files at regular intervals.
// It supports AVI (MJPEG), H264 MP4, and H265 MP4 formats.
// Output frames are written as frame_000001.ext files in the output directory.
//
// The extractor never loads the full recording file into memory — it uses
// ReadAt to seek directly to sample offsets, and for AVI it streams chunks
// sequentially through a ReadSeeker.
type RecordingFrameExtractor struct{}

// NewRecordingFrameExtractor creates a new RecordingFrameExtractor.
func NewRecordingFrameExtractor() *RecordingFrameExtractor {
	return &RecordingFrameExtractor{}
}

// ExtractFrames extracts frames from the given recording file at the specified
// interval. The interval must be positive. For AVI files, frames are JPEG images;
// for H264/H265 MP4 files, frames are Annex-B NAL streams.
//
// Supported formats: model.FormatAVI, model.FormatH264, model.FormatH265.
// Returns the number of extracted frames and any error.
func (e *RecordingFrameExtractor) ExtractFrames(
	filePath string,
	format model.Format,
	interval time.Duration,
	outputDir string,
) (int, error) {
	if interval <= 0 {
		return 0, fmt.Errorf("interval must be positive, got %v", interval)
	}
	if format == "" {
		return 0, fmt.Errorf("format is required")
	}

	switch format {
	case model.FormatAVI:
		return e.extractAVI(filePath, interval, outputDir)
	case model.FormatH264:
		return e.extractMP4(filePath, false, interval, outputDir)
	case model.FormatH265:
		return e.extractMP4(filePath, true, interval, outputDir)
	case model.FormatMJPEG:
		return e.extractMJPEGDirRelative(filePath, interval, outputDir)
	default:
		return 0, fmt.Errorf("unsupported format: %q", format)
	}
}

// FrameSampler tracks the next absolute sampling target across recordings
// within one merge window. Sampling is anchored to the previously taken frame
// (next = ts + interval), so a long gap never produces a catch-up burst — the
// first frame after a gap is taken immediately and the cadence restarts from
// there. Not safe for concurrent use.
type FrameSampler struct {
	interval time.Duration
	next     time.Time
	end      time.Time // zero value = unbounded
}

// NewFrameSampler creates a sampler for a merge window [windowStart, windowEnd).
// windowEnd may be zero for an unbounded window.
func NewFrameSampler(interval time.Duration, windowStart, windowEnd time.Time) *FrameSampler {
	return &FrameSampler{interval: interval, next: windowStart, end: windowEnd}
}

// ShouldTake reports whether a frame with the given absolute timestamp should
// be extracted, advancing the sampler when it is.
func (s *FrameSampler) ShouldTake(ts time.Time) bool {
	if !s.end.IsZero() && !ts.Before(s.end) {
		return false
	}
	if ts.Before(s.next) {
		return false
	}
	s.next = ts.Add(s.interval)
	return true
}

// ExtractWindowFrames extracts frames from one recording using absolute-time
// sampling against a shared FrameSampler, continuing frame numbering from
// startIndex (the first written file is frame_{startIndex+1}). This is the
// fragmented-camera path: multiple recordings in one window share one sampler
// and one output directory without overwriting each other.
//
// recStart is the recording's absolute start time (DB started_at); PTS within
// the recording are offset by it. MJPEG directories carry absolute wall-clock
// timestamps in their frame filenames, so recStart is ignored for them.
//
// A recording that contributes zero frames (e.g. a disconnect fragment shorter
// than the sampling interval) is NOT an error — (0, nil) is returned.
func (e *RecordingFrameExtractor) ExtractWindowFrames(
	recPath string,
	format model.Format,
	recStart time.Time,
	sampler *FrameSampler,
	startIndex int,
	outputDir string,
) (int, error) {
	if format == "" {
		return 0, fmt.Errorf("format is required")
	}
	if sampler == nil {
		return 0, fmt.Errorf("sampler is required")
	}

	switch format {
	case model.FormatMJPEG:
		return e.extractMJPEGDirWindow(recPath, sampler, startIndex, outputDir)
	case model.FormatAVI:
		return e.extractAVIWindow(recPath, recStart, sampler, startIndex, outputDir)
	case model.FormatH264:
		return e.extractMP4Window(recPath, false, recStart, sampler, startIndex, outputDir)
	case model.FormatH265:
		return e.extractMP4Window(recPath, true, recStart, sampler, startIndex, outputDir)
	default:
		return 0, fmt.Errorf("unsupported format: %q", format)
	}
}

// aviVideoFrame is one AVI video chunk with its PTS in microseconds.
type aviVideoFrame struct {
	pts  int64 // microseconds
	data []byte
}

// collectAVIFrames reads all video chunks from an AVI file, returning them in
// stream order plus the demuxer's dwMicroSecPerFrame.
func collectAVIFrames(filePath string) ([]aviVideoFrame, int64, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return nil, 0, fmt.Errorf("open AVI: %w", err)
	}
	defer f.Close()

	d, err := avi.NewDemuxer(f)
	if err != nil {
		return nil, 0, fmt.Errorf("AVI demuxer: %w", err)
	}

	var frames []aviVideoFrame
	for {
		chunk, err := d.NextChunk()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, 0, fmt.Errorf("read AVI chunk: %w", err)
		}
		if chunk.Type == avi.ChunkVideo {
			frames = append(frames, aviVideoFrame{pts: chunk.PTS, data: chunk.Data})
		}
	}
	return frames, int64(d.MicroSecPerFrame()), nil
}

// extractAVI reads video chunks from an AVI file and extracts JPEG frames at
// the given interval. Uses the AVI demuxer's dwMicroSecPerFrame to map chunk
// positions to timestamps.
func (e *RecordingFrameExtractor) extractAVI(filePath string, interval time.Duration, outputDir string) (int, error) {
	frames, microSecPerFrame, err := collectAVIFrames(filePath)
	if err != nil {
		return 0, err
	}

	if len(frames) == 0 {
		return 0, fmt.Errorf("no video frames in AVI")
	}

	// Total duration: last frame's PTS + one frame interval.
	totalDurUs := frames[len(frames)-1].pts + microSecPerFrame
	totalDur := time.Duration(totalDurUs) * time.Microsecond

	if interval > totalDur {
		return 0, fmt.Errorf("interval %v exceeds recording duration %v", interval, totalDur)
	}

	intervalUs := interval.Microseconds()

	// Ensure output directory exists.
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return 0, fmt.Errorf("create output dir: %w", err)
	}

	var extracted int
	nextTargetUs := int64(0)

	for _, fr := range frames {
		if fr.pts >= nextTargetUs {
			filename := fmt.Sprintf("frame_%06d.jpg", extracted+1)
			framePath := filepath.Join(outputDir, filename)
			if err := os.WriteFile(framePath, fr.data, 0o644); err != nil {
				return extracted, fmt.Errorf("write frame %d: %w", extracted+1, err)
			}
			extracted++
			nextTargetUs += intervalUs
		}
	}

	if extracted == 0 {
		return 0, fmt.Errorf("no frames extracted at interval %v", interval)
	}

	return extracted, nil
}

// extractAVIWindow is the window-sampling variant of extractAVI: frame PTS are
// offset by recStart into absolute time and tested against the shared sampler.
// Zero extracted frames is not an error (short fragment).
func (e *RecordingFrameExtractor) extractAVIWindow(recPath string, recStart time.Time, sampler *FrameSampler, startIndex int, outputDir string) (int, error) {
	frames, _, err := collectAVIFrames(recPath)
	if err != nil {
		return 0, err
	}
	if len(frames) == 0 {
		return 0, fmt.Errorf("no video frames in AVI")
	}

	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return 0, fmt.Errorf("create output dir: %w", err)
	}

	var extracted int
	for _, fr := range frames {
		abs := recStart.Add(time.Duration(fr.pts) * time.Microsecond)
		if !sampler.ShouldTake(abs) {
			continue
		}
		filename := fmt.Sprintf("frame_%06d.jpg", startIndex+extracted+1)
		framePath := filepath.Join(outputDir, filename)
		if err := os.WriteFile(framePath, fr.data, 0o644); err != nil {
			return extracted, fmt.Errorf("write frame %d: %w", startIndex+extracted+1, err)
		}
		extracted++
	}

	return extracted, nil
}

// mjpegFrameFile is one timestamped JPEG inside an MJPEG recording directory.
type mjpegFrameFile struct {
	name string
	ts   time.Time
}

// parseMJPEGFrameTime parses the storage layer's frame filename layout
// ("20060102_150405.000", local wall clock) into an absolute time.
func parseMJPEGFrameTime(name string) (time.Time, bool) {
	base := strings.TrimSuffix(strings.TrimSuffix(name, ".jpg"), ".jpeg")
	for _, layout := range []string{"20060102_150405.000", "20060102_150405"} {
		if ts, err := time.ParseInLocation(layout, base, time.Local); err == nil {
			return ts, true
		}
	}
	return time.Time{}, false
}

// listMJPEGFrames lists the JPEG frames of an MJPEG recording directory in
// chronological order (timestamped names sort chronologically).
func listMJPEGFrames(dirPath string) ([]mjpegFrameFile, error) {
	entries, err := os.ReadDir(dirPath)
	if err != nil {
		return nil, fmt.Errorf("read MJPEG dir: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		lower := strings.ToLower(e.Name())
		if strings.HasSuffix(lower, ".jpg") || strings.HasSuffix(lower, ".jpeg") {
			names = append(names, e.Name())
		}
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("no JPEG frames in MJPEG directory %s", dirPath)
	}
	sort.Strings(names)

	frames := make([]mjpegFrameFile, 0, len(names))
	for _, name := range names {
		ts, ok := parseMJPEGFrameTime(name)
		if !ok {
			continue // non-timestamped stray file — skip defensively
		}
		frames = append(frames, mjpegFrameFile{name: name, ts: ts})
	}
	if len(frames) == 0 {
		return nil, fmt.Errorf("no timestamped JPEG frames in MJPEG directory %s", dirPath)
	}
	return frames, nil
}

// writeSampledMJPEGFrames copies the sampled frames from dirPath into
// outputDir with continuous numbering. Returns the number written.
func writeSampledMJPEGFrames(dirPath string, frames []mjpegFrameFile, sampler *FrameSampler, startIndex int, outputDir string) (int, error) {
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return 0, fmt.Errorf("create output dir: %w", err)
	}
	var extracted int
	for _, fr := range frames {
		if !sampler.ShouldTake(fr.ts) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dirPath, fr.name))
		if err != nil {
			return extracted, fmt.Errorf("read frame %s: %w", fr.name, err)
		}
		filename := fmt.Sprintf("frame_%06d.jpg", startIndex+extracted+1)
		if err := os.WriteFile(filepath.Join(outputDir, filename), data, 0o644); err != nil {
			return extracted, fmt.Errorf("write frame %d: %w", startIndex+extracted+1, err)
		}
		extracted++
	}
	return extracted, nil
}

// extractMJPEGDirRelative is the legacy single-recording path: sampling is
// anchored at the first frame's timestamp and steps by interval.
func (e *RecordingFrameExtractor) extractMJPEGDirRelative(dirPath string, interval time.Duration, outputDir string) (int, error) {
	frames, err := listMJPEGFrames(dirPath)
	if err != nil {
		return 0, err
	}
	sampler := NewFrameSampler(interval, frames[0].ts, time.Time{})
	extracted, err := writeSampledMJPEGFrames(dirPath, frames, sampler, 0, outputDir)
	if err != nil {
		return extracted, err
	}
	if extracted == 0 {
		return 0, fmt.Errorf("no frames extracted at interval %v", interval)
	}
	return extracted, nil
}

// extractMJPEGDirWindow is the window-sampling variant for MJPEG directories;
// frame timestamps come from the filenames (absolute wall clock).
func (e *RecordingFrameExtractor) extractMJPEGDirWindow(recPath string, sampler *FrameSampler, startIndex int, outputDir string) (int, error) {
	frames, err := listMJPEGFrames(recPath)
	if err != nil {
		return 0, err
	}
	return writeSampledMJPEGFrames(recPath, frames, sampler, startIndex, outputDir)
}

// mp4SyncSample is a keyframe sample with its cumulative time in microseconds.
type mp4SyncSample struct {
	offset    int64
	size      uint32
	cumTimeUs int64
}

// collectMP4Syncs parses an H264/H265 MP4 and returns its keyframe samples
// (with cumulative times), the codec parameter sets, and the total duration.
func collectMP4Syncs(filePath string, isH265 bool) ([]mp4SyncSample, [][]byte, time.Duration, error) {
	seg, err := merge.ParseSegment(filePath)
	if err != nil {
		return nil, nil, 0, fmt.Errorf("parse MP4: %w", err)
	}

	if len(seg.Samples) == 0 {
		return nil, nil, 0, fmt.Errorf("no samples in MP4")
	}

	// Collect parameter sets from codec config.
	paramSets := collectParamSets(seg, isH265)
	if len(paramSets) == 0 {
		return nil, nil, 0, fmt.Errorf("no codec parameter sets found in MP4")
	}

	var syncs []mp4SyncSample
	cumTimeUs := int64(0)
	for _, s := range seg.Samples {
		durUs := int64(s.Duration) * 1000000 / int64(seg.Timescale)
		if s.IsKeyFrame {
			syncs = append(syncs, mp4SyncSample{
				offset:    s.Offset,
				size:      s.Size,
				cumTimeUs: cumTimeUs,
			})
		}
		cumTimeUs += durUs
	}

	if len(syncs) == 0 {
		return nil, nil, 0, fmt.Errorf("no keyframes in MP4")
	}
	return syncs, paramSets, seg.TotalDuration, nil
}

// extractMP4 extracts keyframes from an H264 or H265 MP4 file at the given
// interval. Uses merge.ParseSegment to get sample metadata and keyframe
// detection, then reads only the selected sample data via ReadAt.
//
// Output frames are self-contained Annex-B NAL streams with parameter sets
// prepended from the codec config, compatible with H264GoMerger/H265GoMerger.
func (e *RecordingFrameExtractor) extractMP4(filePath string, isH265 bool, interval time.Duration, outputDir string) (int, error) {
	syncs, paramSets, totalDur, err := collectMP4Syncs(filePath, isH265)
	if err != nil {
		return 0, err
	}

	if interval > totalDur {
		return 0, fmt.Errorf("interval %v exceeds recording duration %v", interval, totalDur)
	}

	intervalUs := interval.Microseconds()

	// Ensure output directory exists.
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return 0, fmt.Errorf("create output dir: %w", err)
	}

	// Open file for seeking to sample offsets.
	f, err := os.Open(filePath)
	if err != nil {
		return 0, fmt.Errorf("open MP4: %w", err)
	}
	defer f.Close()

	ext := ".h264"
	if isH265 {
		ext = ".h265"
	}

	var extracted int
	nextTargetUs := int64(0)

	for _, sync := range syncs {
		if sync.cumTimeUs >= nextTargetUs {
			extracted, err = writeSyncFrame(f, sync, paramSets, isH265, ext, outputDir, extracted, 0)
			if err != nil {
				return extracted, err
			}
			nextTargetUs += intervalUs
		}
	}

	if extracted == 0 {
		return 0, fmt.Errorf("no frames extracted at interval %v", interval)
	}

	return extracted, nil
}

// writeSyncFrame reads one keyframe sample and writes it as an Annex-B frame
// file with the given absolute index (startIndex+ordinal+1).
func writeSyncFrame(f *os.File, sync mp4SyncSample, paramSets [][]byte, isH265 bool, ext, outputDir string, ordinal, startIndex int) (int, error) {
	// Read the sample data at its offset (length-prefixed NALUs).
	sampleData := make([]byte, sync.size)
	if _, err := f.ReadAt(sampleData, sync.offset); err != nil {
		return ordinal, fmt.Errorf("read sample at offset %d: %w", sync.offset, err)
	}

	// Build Annex-B frame: parameter sets + sample NALUs (stripping
	// duplicate param sets from the sample data).
	frameData := buildAnnexBFrame(sampleData, paramSets, isH265)

	filename := fmt.Sprintf("frame_%06d%s", startIndex+ordinal+1, ext)
	framePath := filepath.Join(outputDir, filename)
	if err := os.WriteFile(framePath, frameData, 0o644); err != nil {
		return ordinal, fmt.Errorf("write frame %d: %w", startIndex+ordinal+1, err)
	}
	return ordinal + 1, nil
}

// extractMP4Window is the window-sampling variant of extractMP4: keyframe
// cumulative times are offset by recStart into absolute time and tested
// against the shared sampler. Zero extracted frames is not an error.
func (e *RecordingFrameExtractor) extractMP4Window(recPath string, isH265 bool, recStart time.Time, sampler *FrameSampler, startIndex int, outputDir string) (int, error) {
	syncs, paramSets, _, err := collectMP4Syncs(recPath, isH265)
	if err != nil {
		return 0, err
	}

	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return 0, fmt.Errorf("create output dir: %w", err)
	}

	f, err := os.Open(recPath)
	if err != nil {
		return 0, fmt.Errorf("open MP4: %w", err)
	}
	defer f.Close()

	ext := ".h264"
	if isH265 {
		ext = ".h265"
	}

	var extracted int
	for _, sync := range syncs {
		abs := recStart.Add(time.Duration(sync.cumTimeUs) * time.Microsecond)
		if !sampler.ShouldTake(abs) {
			continue
		}
		extracted, err = writeSyncFrame(f, sync, paramSets, isH265, ext, outputDir, extracted, startIndex)
		if err != nil {
			return extracted, err
		}
	}

	return extracted, nil
}

// collectParamSets extracts parameter set NALUs from SegmentInfo.
// For H264: SPS, PPS. For H265: VPS, SPS, PPS.
// Returns nil if no parameter sets are available.
func collectParamSets(seg *merge.SegmentInfo, isH265 bool) [][]byte {
	var ps [][]byte
	if isH265 {
		if len(seg.VPS) > 0 {
			ps = append(ps, seg.VPS)
		}
		if len(seg.SPS) > 0 {
			ps = append(ps, seg.SPS)
		}
		if len(seg.PPS) > 0 {
			ps = append(ps, seg.PPS)
		}
	} else {
		if len(seg.SPS) > 0 {
			ps = append(ps, seg.SPS)
		}
		if len(seg.PPS) > 0 {
			ps = append(ps, seg.PPS)
		}
	}
	return ps
}

// buildAnnexBFrame converts length-prefixed NALU data from an MP4 sample
// into an Annex-B byte stream with 0x00000001 start codes. Parameter sets
// from the codec config are prepended; any param sets in the sample data
// are skipped to avoid duplication.
func buildAnnexBFrame(sampleData []byte, paramSets [][]byte, isH265 bool) []byte {
	// Estimate capacity: sampleData + paramSets + start codes overhead.
	capacity := len(sampleData) + len(paramSets)*64
	frame := make([]byte, 0, capacity)

	// Write parameter sets with Annex-B start codes.
	for _, ps := range paramSets {
		frame = append(frame, 0x00, 0x00, 0x00, 0x01)
		frame = append(frame, ps...)
	}

	// Parse length-prefixed NALUs and write with start codes.
	offset := 0
	for offset+4 <= len(sampleData) {
		naluLen := int(binary.BigEndian.Uint32(sampleData[offset:]))
		offset += 4
		if naluLen == 0 || offset+naluLen > len(sampleData) {
			break
		}

		// Skip parameter set NALUs — they are already prepended from the
		// codec-level config (avcC/hvcC), and including them again bloats
		// the frame file and causes playback issues.
		if naluLen > 0 && isParamSetNALU(sampleData[offset:offset+naluLen], isH265) {
			offset += naluLen
			continue
		}

		frame = append(frame, 0x00, 0x00, 0x00, 0x01)
		frame = append(frame, sampleData[offset:offset+naluLen]...)
		offset += naluLen
	}

	return frame
}

// isParamSetNALU returns true if the NALU is a parameter set (SPS/PPS for
// H264, VPS/SPS/PPS for H265).
func isParamSetNALU(nalu []byte, isH265 bool) bool {
	if len(nalu) == 0 {
		return false
	}
	if isH265 {
		nalType := (nalu[0] >> 1) & 0x3F
		return nalType == 32 || nalType == 33 || nalType == 34
	}
	nalType := nalu[0] & 0x1F
	return nalType == 7 || nalType == 8
}
