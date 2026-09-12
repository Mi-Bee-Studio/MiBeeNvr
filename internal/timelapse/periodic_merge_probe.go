package timelapse

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/mediaprobe"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
)

// checkSegmentCompatibility checks if all segments have compatible resolution and codec.
// Uses the pure-Go mediaprobe by default, falling back to ffprobe when needed.
func checkSegmentCompatibility(ctx context.Context, segments []model.Recording) (bool, error) {
	if len(segments) < 2 {
		return true, nil
	}

	refWidth, refHeight, refCodec, err := probeSegmentMetadata(ctx, segments[0].FilePath)
	if err != nil {
		return false, fmt.Errorf("probe reference segment %s: %w", segments[0].ID, err)
	}

	for i := 1; i < len(segments); i++ {
		width, height, codec, err := probeSegmentMetadata(ctx, segments[i].FilePath)
		if err != nil {
			return false, fmt.Errorf("probe segment %s: %w", segments[i].ID, err)
		}

		if width != refWidth || height != refHeight {
			slog.Warn(
				"merge: segment resolution mismatch",
				"segment_id", segments[i].ID,
				"expected", fmt.Sprintf("%dx%d", refWidth, refHeight),
				"got", fmt.Sprintf("%dx%d", width, height),
			)
			return false, nil
		}

		if codec != refCodec {
			slog.Warn(
				"merge: segment codec mismatch",
				"segment_id", segments[i].ID,
				"expected", refCodec,
				"got", codec,
			)
			return false, nil
		}
	}

	return true, nil
}

// probeSegmentMetadata extracts video resolution and codec from a file.
//
// It prefers the pure-Go mediaprobe (no external process) and falls back to
// ffprobe when mediaprobe fails or the file is not MP4. The returned codec
// uses ffprobe-compatible names ("h264", "hevc") so compatibility comparisons
// behave identically to the previous ffprobe-only implementation.

// probeSegmentMetadata extracts video resolution and codec from a file.
//
// It prefers the pure-Go mediaprobe (no external process) and falls back to
// ffprobe when mediaprobe fails or the file is not MP4. The returned codec
// uses ffprobe-compatible names ("h264", "hevc") so compatibility comparisons
// behave identically to the previous ffprobe-only implementation.
func probeSegmentMetadata(ctx context.Context, filePath string) (width, height int, codec string, err error) {
	// Fast path: pure-Go probe.
	if mediaprobe.IsLikelyMP4(filePath) {
		if info, e := mediaprobe.ProbeMP4(filePath); e == nil {
			return info.Width, info.Height, info.CodecName, nil
		}
	}

	// Fallback: ffprobe subprocess (requires ffprobe on PATH).
	args := []string{
		"-v", "quiet",
		"-print_format", "json",
		"-show_streams",
		filePath,
	}

	cmd := exec.CommandContext(ctx, "ffprobe", args...)
	output, err := cmd.Output()
	if err != nil {
		return 0, 0, "", fmt.Errorf("ffprobe failed: %w", err)
	}

	lines := strings.Split(string(output), "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, `"codec_name":`) {
			codec = strings.Trim(strings.TrimPrefix(trimmed, `"codec_name":`), ` ",`)
		} else if strings.HasPrefix(trimmed, `"width":`) {
			fmt.Sscanf(trimmed, `"width": %d`, &width)
		} else if strings.HasPrefix(trimmed, `"height":`) {
			fmt.Sscanf(trimmed, `"height": %d`, &height)
		}
	}

	if width == 0 || height == 0 || codec == "" {
		return 0, 0, "", fmt.Errorf("incomplete metadata from ffprobe")
	}

	return width, height, codec, nil
}

// probeVideoFrameCount returns the total number of video frames in a file.
//
// Uses the pure-Go mediaprobe (reads stsz box, no decoding) and falls back to
// ffprobe -count_frames when mediaprobe fails or the file is not MP4. The
// mediaprobe path is dramatically faster since ffprobe -count_frames must
// decode the entire file to count frames.

// probeVideoFrameCount returns the total number of video frames in a file.
//
// Uses the pure-Go mediaprobe (reads stsz box, no decoding) and falls back to
// ffprobe -count_frames when mediaprobe fails or the file is not MP4. The
// mediaprobe path is dramatically faster since ffprobe -count_frames must
// decode the entire file to count frames.
func probeVideoFrameCount(ctx context.Context, filePath string) (int, error) {
	// Fast path: pure-Go probe — frame count comes from stsz.SampleCount.
	if mediaprobe.IsLikelyMP4(filePath) {
		if info, err := mediaprobe.ProbeMP4(filePath); err == nil {
			return info.FrameCount, nil
		}
	}

	// Fallback: ffprobe subprocess.
	cmd := exec.CommandContext(
		ctx, "ffprobe",
		"-v", "error",
		"-count_frames",
		"-select_streams", "v:0",
		"-show_entries", "stream=nb_read_frames",
		"-of", "csv=p=0",
		filePath,
	)
	output, err := cmd.Output()
	if err != nil {
		return 0, fmt.Errorf("ffprobe frame count failed: %w", err)
	}
	s := strings.TrimSpace(string(output))
	if s == "" {
		return 0, fmt.Errorf("ffprobe returned empty frame count")
	}
	count, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("ffprobe frame count parse failed: %w", err)
	}
	return count, nil
}

// defaultExtractionInterval is the frame-sampling fallback when the camera's
// timelapse.interval is not wired (legacy constructions / tests).
const defaultExtractionInterval = 30 * time.Second

// extractRecordingFrames queries video-format recordings in the merge window
// and extracts frames into per-codec temporary directories using ONE shared
// absolute-time FrameSampler per codec group — so fragmented recordings
// (disconnect-heavy MJPEG cameras) sample continuously across segments without
// overwriting each other's frame numbering.
//
// Returns synthetic Recording entries for each codec group, the temp dirs to
// clean up, and the source recordings per codec group (used by the opt-in
// delete_recordings_after_merge cleanup after a successful merge).
//
// Supported formats for frame extraction:
//   - H264 ✅ (keyframe-sync H.264 IDR samples from MP4)
//   - H265 ✅ (IRAP NAL type 19/20 sync samples from MP4)
//   - AVI ✅  (MJPEG JPEG frames via internal/avi demuxer)
//   - MJPEG ✅ (timestamped JPEG directory — frame times from filenames)
//   - MPEG-TS ✗ (no moov/stss boxes, too expensive to probe)
func (m *PeriodicMergeManager) extractRecordingFrames(ctx context.Context, cameraID string, startTime, endTime time.Time) ([]model.Recording, []string, map[string][]model.Recording, error) {
	videoFormats := []model.Format{model.FormatH264, model.FormatH265, model.FormatAVI, model.FormatMJPEG}
	recs, err := m.store.ListRecordings(ctx, model.RecordingFilter{
		CameraID:  cameraID,
		Formats:   videoFormats,
		StartTime: startTime,
		EndTime:   endTime,
	})
	if err != nil {
		return nil, nil, nil, fmt.Errorf("list video recordings: %w", err)
	}
	if len(recs) == 0 {
		return nil, nil, nil, nil
	}

	// Chronological order is load-bearing: the shared sampler walks recordings
	// in time order, and continuing frame numbering must not go backwards.
	sort.Slice(recs, func(i, j int) bool { return recs[i].StartedAt.Before(recs[j].StartedAt) })

	// MJPEG/AVI recordings both yield JPEG frames — join them into one
	// suffix-less "jpeg" group (same mapping runPerCodecMerge applies to
	// timelapse segments), so the output is periodic_<window>.mp4.
	codecKey := func(f model.Format) model.Format {
		if f == model.FormatMJPEG || f == model.FormatAVI {
			return model.FormatTimelapse
		}
		return f
	}

	// Group recordings by codec for per-codec extraction.
	codecRecordings := make(map[model.Format][]model.Recording)
	for _, r := range recs {
		ck := codecKey(r.Format)
		codecRecordings[ck] = append(codecRecordings[ck], r)
	}

	extractor := NewRecordingFrameExtractor()
	// Sampling cadence = per-camera timelapse.interval (the timelapse
	// compression knob), NOT 1/output-fps — sampling every frame would just
	// copy the whole window instead of producing a timelapse.
	interval := m.extractionInterval
	if interval <= 0 {
		interval = defaultExtractionInterval
	}

	var segments []model.Recording
	var tmpDirs []string
	sourcesByGroup := make(map[string][]model.Recording)

	for codec, codecRecs := range codecRecordings {
		tmpDir, err := m.mkdirTemp(fmt.Sprintf("periodic_extract_%s_*", codec))
		if err != nil {
			// Clean up previously created dirs on error.
			for _, d := range tmpDirs {
				os.RemoveAll(d)
			}
			return nil, nil, nil, fmt.Errorf("create temp dir for %s: %w", codec, err)
		}
		tmpDirs = append(tmpDirs, tmpDir)

		// One shared window sampler + continuing frame index across ALL
		// recordings in the group — fixes the frame_000001 overwrite that
		// destroyed all but the last recording's frames.
		sampler := NewFrameSampler(interval, startTime, endTime)
		frameIdx := 0
		groupSources := make([]model.Recording, 0, len(codecRecs))

		for _, rec := range codecRecs {
			n, err := extractor.ExtractWindowFrames(rec.FilePath, rec.Format, rec.StartedAt, sampler, frameIdx, tmpDir)
			if err != nil {
				slog.Warn("periodic merge: frame extraction failed, skipping recording",
					"recording_id", rec.ID, "format", rec.Format, "error", err)
				continue
			}
			frameIdx += n
			groupSources = append(groupSources, rec)
			slog.Debug("periodic merge: extracted frames from recording",
				"recording_id", rec.ID, "format", rec.Format, "frames", n, "dir", tmpDir)
		}

		// Check if any frames were actually extracted.
		entries, err := os.ReadDir(tmpDir)
		if err != nil || len(entries) == 0 {
			slog.Warn("periodic merge: no frames extracted for codec, skipping",
				"codec", codec, "camera_id", cameraID)
			continue
		}

		// Create a synthetic Recording entry for this codec's extracted frame directory.
		// MergeStatus is empty (unmerged), so runMergePipeline's hasUnmergedRawSegments
		// check will route through Go keyframe merge path (Tier 4) as a raw frame dir.
		segments = append(segments, model.Recording{
			ID:          fmt.Sprintf("extracted_%s_%s", cameraID, string(codec)),
			CameraID:    cameraID,
			FilePath:    tmpDir,
			Format:      codec,
			MergeStatus: "", // unmerged raw segment
		})
		sourcesByGroup[codecGroupName(codec)] = groupSources
	}

	return segments, tmpDirs, sourcesByGroup, nil
}

// runPerCodecMerge groups segments by codec type and runs a separate merge
// pipeline for each group. This prevents mixing incompatible codecs (e.g.
// H264 + H265) in a single merge output.
//
// Codec grouping:
//   - Timelapse + AVI/MJPEG extracted frames → "jpeg" group
//   - H264 extracted frames → "h264" group
//   - H265 extracted frames → "h265" group
//
// Output naming:
//   - JPEG group: periodic_WINDOW.mp4
//   - H264 group: periodic_WINDOW_h264.mp4
//   - H265 group: periodic_WINDOW_h265.mp4
