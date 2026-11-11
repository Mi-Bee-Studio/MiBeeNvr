package merge

// transcode_unify.go — the transcode_merge fallback (#1150 C②): a per-camera
// opt-in that joins merge runs spanning MULTIPLE codec/parameter sets into a
// single file via an ffmpeg concat + re-encode. The Go merger
// (MergeMP4Segments) requires byte-compatible parameter sets; cameras whose
// quality state machine flips codecs (HEVC↔H.264, the #1150 field case)
// fragment into separate per-key runs. This path trades CPU for continuity:
// ffmpeg decodes every segment and re-encodes to one common H.264 track
// (software libx264 — the reference boxes have no media hardware;
// hardware-encoder probing lives in the transcoding package and can replace
// the encoder choice later).
//
// Implementation notes (both caught by the M5 field verification):
//   - The inputs are joined with the concat FILTER (decoded-frame join), not
//     the concat DEMUXER. The demuxer fixes the stream layout from the first
//     file and silently drops later files with a different codec — exit 0,
//     valid-looking output, half the content gone. The filter gives each -i
//     its own decoder, which is the whole point here.
//   - The output muxer is pinned with -f mp4: the out path is a dotless .tmp
//     during the run and ffmpeg infers the muxer from the extension.
//   - The output duration is checked against the summed input duration — the
//     net under any "ffmpeg succeeded but dropped inputs" regression.
//
// Audio: within one camera, audio presence is static configuration, so a run
// mixing audio-bearing and silent segments means a mid-history toggle. That
// case is refused with an actionable error instead of guessing padding.
//
// Failure semantics: ffmpeg absence or failure returns an error — the caller
// (mergeAudioRun) marks the run incompatible with the reason (#1149), keeping
// it retryable via backfill once the operator fixes the environment.

import (
	"context"
	"fmt"
	"os/exec"
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

// transcodeMinDurationRatio is the output-duration sanity gate: the unified
// file must cover at least this share of the summed input duration (small
// absolute slack for timescale rounding). Var seam for the stub tests, which
// copy a single input and would trip the real gate.
var transcodeMinDurationRatio = 0.8

// transcodeUnifyTo merges a possibly mixed-codec run into outPath by running
// ffmpeg's concat filter with a re-encode (libx264 + AAC), then sanity-checks
// the output parses AND covers the input span. infos are expected
// chronological; every input is counted as Included (ffmpeg either renders
// the whole run or fails outright).
func transcodeUnifyTo(ctx context.Context, infos []*SegmentInfo, outPath string) (*MergeStats, error) {
	ffmpegPath, err := ffmpegLookup()
	if err != nil {
		return nil, fmt.Errorf("transcode_merge requires ffmpeg in PATH: %w", err)
	}
	if len(infos) == 0 {
		return nil, fmt.Errorf("transcode unify: no segments")
	}

	nAudio := 0
	for _, seg := range infos {
		if seg.HasAudio {
			nAudio++
		}
	}
	if nAudio != 0 && nAudio != len(infos) {
		return nil, fmt.Errorf("transcode unify: mixed audio presence (%d of %d segments carry audio) — unsupported; toggle the camera's audio setting cleanly instead",
			nAudio, len(infos))
	}

	// Scale target: the first segment with resolvable dimensions (tkhd).
	// concat requires equal frame sizes across inputs, and a quality-flipping
	// camera may alternate resolutions — normalize every input to one size.
	targetW, targetH := 0, 0
	for _, seg := range infos {
		if seg.Width > 0 && seg.Height > 0 {
			targetW, targetH = int(seg.Width), int(seg.Height)
			break
		}
	}

	var args []string
	var graph []string
	var videoPads strings.Builder
	for i, seg := range infos {
		args = append(args, "-i", seg.FilePath)
		chain := ""
		if targetW > 0 && targetH > 0 {
			chain += fmt.Sprintf("scale=%d:%d,", targetW, targetH)
		}
		chain += "setsar=1,settb=AVTB"
		graph = append(graph, fmt.Sprintf("[%d:v]%s[v%d]", i, chain, i))
		fmt.Fprintf(&videoPads, "[v%d]", i)
	}
	concatIn := videoPads.String()
	if nAudio == 0 {
		graph = append(graph, fmt.Sprintf("%sconcat=n=%d:v=1:a=0[vout]", concatIn, len(infos)))
		args = append(args,
			"-filter_complex", strings.Join(graph, ";"),
			"-map", "[vout]",
			"-c:v", "libx264",
			"-preset", "veryfast",
			"-pix_fmt", "yuv420p")
	} else {
		// Common audio rate for the concat pads — first audio-bearing input's
		// timescale (same camera ⇒ same rate; aresample is belt-and-braces).
		rate := 0
		for _, seg := range infos {
			if seg.AudioTimescale > 0 {
				rate = int(seg.AudioTimescale)
				break
			}
		}
		var audioPads strings.Builder
		for i := range infos {
			achain := "asetpts=N/SR/TB"
			if rate > 0 {
				achain = fmt.Sprintf("aresample=%d,%s", rate, achain)
			}
			graph = append(graph, fmt.Sprintf("[%d:a]%s[a%d]", i, achain, i))
			fmt.Fprintf(&audioPads, "[a%d]", i)
		}
		graph = append(graph, fmt.Sprintf("%s%sconcat=n=%d:v=1:a=1[vout][aout]",
			concatIn, audioPads.String(), len(infos)))
		args = append(args,
			"-filter_complex", strings.Join(graph, ";"),
			"-map", "[vout]", "-map", "[aout]",
			"-c:v", "libx264",
			"-preset", "veryfast",
			"-pix_fmt", "yuv420p",
			"-c:a", "aac")
	}
	args = append(args,
		"-movflags", "+faststart",
		"-f", "mp4", // the out path is a .tmp — pin the muxer, don't infer
		"-y",
		outPath)

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
	if wallSec > 0 && fileSec+0.25 < transcodeMinDurationRatio*wallSec {
		return nil, fmt.Errorf("transcode unify: output covers %.2fs of %.2fs input span — inputs were dropped",
			fileSec, wallSec)
	}

	return &MergeStats{
		Included:   included,
		WallToFile: [][2]float64{{0, 0}, {wallSec, fileSec}},
	}, nil
}
