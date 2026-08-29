package timelapse

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
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

// extractRecordingFrames queries video-format recordings in the merge window
// and extracts frames into per-codec temporary directories. Returns synthetic
// Recording entries for each codec group and the list of temp dirs to clean up.
//
// Supported formats for frame extraction:
//   - H264 ✅ (keyframe-sync H.264 IDR samples from MP4)
//   - H265 ✅ (IRAP NAL type 19/20 sync samples from MP4)
//   - AVI ✅  (MJPEG JPEG frames via internal/avi demuxer)
//   - MJPEG ✅ (same JPEG extraction as AVI)
//   - MPEG-TS ✗ (no moov/stss boxes, too expensive to probe)

// extractRecordingFrames queries video-format recordings in the merge window
// and extracts frames into per-codec temporary directories. Returns synthetic
// Recording entries for each codec group and the list of temp dirs to clean up.
//
// Supported formats for frame extraction:
//   - H264 ✅ (keyframe-sync H.264 IDR samples from MP4)
//   - H265 ✅ (IRAP NAL type 19/20 sync samples from MP4)
//   - AVI ✅  (MJPEG JPEG frames via internal/avi demuxer)
//   - MJPEG ✅ (same JPEG extraction as AVI)
//   - MPEG-TS ✗ (no moov/stss boxes, too expensive to probe)
func (m *PeriodicMergeManager) extractRecordingFrames(ctx context.Context, cameraID string, startTime, endTime time.Time) ([]model.Recording, []string, error) {
	videoFormats := []model.Format{model.FormatH264, model.FormatH265, model.FormatAVI, model.FormatMJPEG}
	recs, err := m.store.ListRecordings(ctx, model.RecordingFilter{
		CameraID:  cameraID,
		Formats:   videoFormats,
		StartTime: startTime,
		EndTime:   endTime,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("list video recordings: %w", err)
	}
	if len(recs) == 0 {
		return nil, nil, nil
	}

	// Map recording format to extraction codec (grouping key).
	// MJPEG recordings produce JPEG frames like AVI.
	codecKey := func(f model.Format) model.Format {
		if f == model.FormatMJPEG {
			return model.FormatAVI // MJPEG → JPEG extraction like AVI
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
	// Determine extraction interval based on FPS. At least 1 frame per recording.
	interval := time.Second / time.Duration(m.fps)
	if interval <= 0 {
		interval = 100 * time.Millisecond // default 10fps
	}

	var segments []model.Recording
	var tmpDirs []string

	for codec, recs := range codecRecordings {
		tmpDir, err := os.MkdirTemp("", fmt.Sprintf("periodic_extract_%s_*", codec))
		if err != nil {
			// Clean up previously created dirs on error.
			for _, d := range tmpDirs {
				os.RemoveAll(d)
			}
			return nil, nil, fmt.Errorf("create temp dir for %s: %w", codec, err)
		}
		tmpDirs = append(tmpDirs, tmpDir)

		for _, rec := range recs {
			n, err := extractor.ExtractFrames(rec.FilePath, codec, interval, tmpDir)
			if err != nil {
				slog.Warn("periodic merge: frame extraction failed, skipping recording",
					"recording_id", rec.ID, "format", rec.Format, "error", err)
				continue
			}
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
	}

	return segments, tmpDirs, nil
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
