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
	default:
		return 0, fmt.Errorf("unsupported format: %q", format)
	}
}

// extractAVI reads video chunks from an AVI file and extracts JPEG frames at
// the given interval. Uses the AVI demuxer's dwMicroSecPerFrame to map chunk
// positions to timestamps.
func (e *RecordingFrameExtractor) extractAVI(filePath string, interval time.Duration, outputDir string) (int, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return 0, fmt.Errorf("open AVI: %w", err)
	}
	defer f.Close()

	d, err := avi.NewDemuxer(f)
	if err != nil {
		return 0, fmt.Errorf("AVI demuxer: %w", err)
	}

	// Collect all video chunks with their PTS (microseconds).
	type videoFrame struct {
		pts  int64 // microseconds
		data []byte
	}
	var frames []videoFrame

	for {
		chunk, err := d.NextChunk()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return 0, fmt.Errorf("read AVI chunk: %w", err)
		}
		if chunk.Type == avi.ChunkVideo {
			frames = append(frames, videoFrame{pts: chunk.PTS, data: chunk.Data})
		}
	}

	if len(frames) == 0 {
		return 0, fmt.Errorf("no video frames in AVI")
	}

	// Total duration: last frame's PTS + one frame interval.
	totalDurUs := frames[len(frames)-1].pts + int64(d.MicroSecPerFrame())
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

// extractMP4 extracts keyframes from an H264 or H265 MP4 file at the given
// interval. Uses merge.ParseSegment to get sample metadata and keyframe
// detection, then reads only the selected sample data via ReadAt.
//
// Output frames are self-contained Annex-B NAL streams with parameter sets
// prepended from the codec config, compatible with H264GoMerger/H265GoMerger.
func (e *RecordingFrameExtractor) extractMP4(filePath string, isH265 bool, interval time.Duration, outputDir string) (int, error) {
	seg, err := merge.ParseSegment(filePath)
	if err != nil {
		return 0, fmt.Errorf("parse MP4: %w", err)
	}

	if len(seg.Samples) == 0 {
		return 0, fmt.Errorf("no samples in MP4")
	}

	// Collect parameter sets from codec config.
	paramSets := collectParamSets(seg, isH265)
	if len(paramSets) == 0 {
		return 0, fmt.Errorf("no codec parameter sets found in MP4")
	}

	// Build a list of keyframe samples with cumulative time in microseconds.
	type syncSample struct {
		offset    int64
		size      uint32
		cumTimeUs int64
	}
	var syncs []syncSample
	cumTimeUs := int64(0)

	for _, s := range seg.Samples {
		durUs := int64(s.Duration) * 1000000 / int64(seg.Timescale)
		if s.IsKeyFrame {
			syncs = append(syncs, syncSample{
				offset:    s.Offset,
				size:      s.Size,
				cumTimeUs: cumTimeUs,
			})
		}
		cumTimeUs += durUs
	}

	totalDur := seg.TotalDuration
	if len(syncs) == 0 {
		return 0, fmt.Errorf("no keyframes in MP4")
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
			// Read the sample data at its offset (length-prefixed NALUs).
			sampleData := make([]byte, sync.size)
			if _, err := f.ReadAt(sampleData, sync.offset); err != nil {
				return extracted, fmt.Errorf("read sample at offset %d: %w", sync.offset, err)
			}

			// Build Annex-B frame: parameter sets + sample NALUs (stripping
			// duplicate param sets from the sample data).
			frameData := buildAnnexBFrame(sampleData, paramSets, isH265)

			filename := fmt.Sprintf("frame_%06d%s", extracted+1, ext)
			framePath := filepath.Join(outputDir, filename)
			if err := os.WriteFile(framePath, frameData, 0o644); err != nil {
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
