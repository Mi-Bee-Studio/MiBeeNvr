package merge

// transcode_unify.go — the transcode_merge fallback (#1150 C②): a per-camera
// opt-in that joins merge runs spanning MULTIPLE codec/parameter sets into a
// single file via an ffmpeg concat + re-encode. The Go merger
// (MergeMP4Segments) requires byte-compatible parameter sets; cameras whose
// quality state machine flips codecs (HEVC↔H.264, the #1150 field case)
// fragment into separate per-key runs. This path trades CPU for continuity:
// ffmpeg reads every segment through the concat demuxer and re-encodes to one
// common H.264 track (software libx264 — the reference boxes have no media
// hardware; hardware-encoder probing lives in the transcoding package and can
// replace the encoder choice later).
//
// Failure semantics: ffmpeg absence or failure returns an error — the caller
// (mergeAudioRun) marks the run incompatible with the reason (#1149), keeping
// it retryable via backfill once the operator fixes the environment.

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// ffmpegLookup resolves the ffmpeg binary path. Var seam for tests — a stub
// exercises the unify plumbing deterministically on hosts without ffmpeg.
var ffmpegLookup = func() (string, error) { return exec.LookPath("ffmpeg") }

// transcodeCommandTimeout bounds one unify run — re-encoding a full window
// must not pin the per-camera merge lock forever; a hung ffmpeg is abandoned
// (ctx cancellation kills the child) and the run is retried by the scheduler.
const transcodeCommandTimeout = 15 * time.Minute

// transcodeUnifyTo merges a possibly mixed-codec run into outPath by running
// ffmpeg's concat demuxer with a re-encode (libx264 + AAC), then sanity-checks
// the output parses. infos are expected chronological; every input is counted
// as Included (ffmpeg either renders the whole run or fails outright).
func transcodeUnifyTo(ctx context.Context, infos []*SegmentInfo, outPath string) (*MergeStats, error) {
	ffmpegPath, err := ffmpegLookup()
	if err != nil {
		return nil, fmt.Errorf("transcode_merge requires ffmpeg in PATH: %w", err)
	}
	if len(infos) == 0 {
		return nil, fmt.Errorf("transcode unify: no segments")
	}

	listFile, err := os.CreateTemp("", "transcode_merge_*.txt")
	if err != nil {
		return nil, fmt.Errorf("transcode unify: create list file: %w", err)
	}
	defer os.Remove(listFile.Name())

	listWriter := bufio.NewWriter(listFile)
	for _, seg := range infos {
		absPath, aerr := filepath.Abs(seg.FilePath)
		if aerr != nil {
			listFile.Close()
			return nil, fmt.Errorf("transcode unify: resolve path %s: %w", seg.FilePath, aerr)
		}
		escaped := strings.ReplaceAll(absPath, "'", "'\\''")
		fmt.Fprintf(listWriter, "file '%s'\n", escaped)
	}
	if lerr := listWriter.Flush(); lerr != nil {
		listFile.Close()
		return nil, fmt.Errorf("transcode unify: flush list file: %w", lerr)
	}
	listFile.Close()

	args := []string{
		"-f", "concat",
		"-safe", "0",
		"-i", listFile.Name(),
		"-c:v", "libx264",
		"-preset", "veryfast",
		"-pix_fmt", "yuv420p",
		"-c:a", "aac",
		"-movflags", "+faststart",
		// The outPath is a dotless-basename .tmp file during the run — ffmpeg
		// infers the muxer from the extension and aborts on unknown ones
		// ("Error initializing the muxer: Invalid argument", M5 field catch).
		// Pin the muxer explicitly.
		"-f", "mp4",
		"-y",
		outPath,
	}

	cmdCtx, cancel := context.WithTimeout(ctx, transcodeCommandTimeout)
	defer cancel()
	cmd := exec.CommandContext(cmdCtx, ffmpegPath, args...)
	cmdOut, runErr := cmd.CombinedOutput()
	if runErr != nil {
		detail := strings.TrimSpace(string(cmdOut))
		if len(detail) > 300 {
			detail = detail[len(detail)-300:] // keep the tail — ffmpeg puts the actual error last
		}
		return nil, fmt.Errorf("ffmpeg transcode-unify: %w: %s", runErr, detail)
	}

	info, perr := ParseSegment(outPath)
	if perr != nil {
		return nil, fmt.Errorf("transcode unify: output does not parse: %w", perr)
	}
	if info.SampleCount == 0 {
		return nil, fmt.Errorf("transcode unify: output has no video samples")
	}

	// All inputs are accounted for (ffmpeg renders the run or fails outright).
	included := make([]int, len(infos))
	for i := range included {
		included[i] = i
	}
	var wallSec, fileSec float64
	for _, seg := range infos {
		wallSec += seg.TotalDuration.Seconds()
	}
	fileSec = info.TotalDuration.Seconds()

	return &MergeStats{
		Included:   included,
		WallToFile: [][2]float64{{0, 0}, {wallSec, fileSec}},
	}, nil
}
