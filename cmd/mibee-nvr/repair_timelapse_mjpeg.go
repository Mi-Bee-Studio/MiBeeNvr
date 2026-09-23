package main

// repair_timelapse_mjpeg.go — `mibee-nvr repair timelapse-mjpeg`: rewrite
// completed MJPEG (mjpa) periodic-merge outputs whose samples carry the
// double-header JPEG buffers produced by non-compliant RTSP senders (ESP32
// MiBeeCam firmware packs complete JPEGs into RTP payloads; the RFC 2435
// depacketizer prepends a synthesized header block). Browsers reject those
// samples, so the canvas sequence player spun forever. Each sample's complete
// inner JPEG(s) are salvaged losslessly (no re-encode) and remuxed with the
// pure-Go GoMerger; the DB row's frame count / file size are refreshed.

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/merge"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/recorder"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/timelapse"
)

func runRepairTimelapseMJPEG() int {
	opts := parseRepairFlags(3)
	if opts.configPath == "__help__" {
		printRepairTimelapseMJPEGUsage()
		return 0
	}
	db, _, err := openDBFromConfig(opts.configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return 1
	}
	defer db.Close()
	ctx, cancel := setupSignalHandler()
	defer cancel()

	mode := "DRY RUN (no changes)"
	if !opts.dryRun {
		mode = "EXECUTE"
	}
	fmt.Printf("repair timelapse-mjpeg (%s) — salvage inner JPEGs from double-header merges\n", mode)

	merges, err := db.ListTimelapseMerges(ctx, storage.TimelapseMergeFilter{
		CameraID: opts.cameraID,
		Status:   model.TimelapseMergeStatusCompleted,
		Limit:    opts.limit,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error listing merges: %v\n", err)
		return 1
	}

	scanned, corrupt, repaired := 0, 0, 0
	for i := range merges {
		if ctx.Err() != nil {
			fmt.Println("\nInterrupted — earlier results kept.")
			break
		}
		m := merges[i]
		if m.Codec != model.TimelapseMergeCodecMJPEG || m.OutputPath == "" {
			continue
		}
		scanned++
		done, err := repairOneTimelapseMerge(ctx, db, &m, opts.dryRun)
		if err != nil {
			fmt.Printf("  [%d] %s: ERROR: %v\n", m.ID, filepath.Base(m.OutputPath), err)
			continue
		}
		if !done {
			fmt.Printf("  [%d] %s: clean, skipped\n", m.ID, filepath.Base(m.OutputPath))
			continue
		}
		corrupt++
		if !opts.dryRun {
			repaired++
		}
	}

	fmt.Printf("\nSummary: %d mjpeg merges scanned, %d corrupt, %d rewritten\n", scanned, corrupt, repaired)
	if opts.dryRun && corrupt > 0 {
		fmt.Println("Re-run with --execute to apply.")
	}
	return 0
}

// repairOneTimelapseMerge rewrites one merge output if any sample deviates
// from a single clean JPEG. Returns true when the merge was (or would be)
// rewritten.
func repairOneTimelapseMerge(ctx context.Context, db *storage.DB, m *model.TimelapseMerge, dryRun bool) (bool, error) {
	seg, err := merge.ParseSegment(m.OutputPath)
	if err != nil {
		return false, fmt.Errorf("parse: %w", err)
	}
	f, err := os.Open(m.OutputPath)
	if err != nil {
		return false, err
	}
	defer f.Close() // error paths; the success path closes explicitly below

	// Pass 1: scan samples; short-circuit on the first clean one to keep the
	// common (healthy) case at zero allocations.
	//
	// corruptIdx remembers which samples need salvage so pass 2 can skip the
	// (majority of) already-clean bytes when rewriting is needed.
	var corruptIdx []int
	totalFrames := 0
	for i, s := range seg.Samples {
		buf := make([]byte, s.Size)
		if _, err := f.ReadAt(buf, s.Offset); err != nil {
			return false, fmt.Errorf("read sample %d: %w", i, err)
		}
		images := recorder.ExtractCompleteJPEGs(buf)
		totalFrames += len(images)
		if len(images) != 1 || !bytes.Equal(images[0], buf) {
			corruptIdx = append(corruptIdx, i)
		}
	}
	if len(corruptIdx) == 0 {
		return false, nil
	}
	if totalFrames == 0 {
		return false, fmt.Errorf("no salvageable JPEG frames found (%d samples)", len(seg.Samples))
	}
	if totalFrames*4 < len(seg.Samples) {
		// Salvage ratio below 25%: the source data itself is truncated (frames
		// cut mid-entropy at stream EOF), so a rewrite would silently shrink
		// the merge to a sliver. Leave the file for the operator to decide.
		fmt.Printf("  [%d] %s: SKIPPED — only %d/%d frames salvageable (source data truncated at stream EOF)\n",
			m.ID, filepath.Base(m.OutputPath), totalFrames, len(seg.Samples))
		return false, nil
	}

	fmt.Printf("  [%d] %s: %d/%d samples corrupt, %d frames salvageable\n",
		m.ID, filepath.Base(m.OutputPath), len(corruptIdx), len(seg.Samples), totalFrames)
	if dryRun {
		return true, nil
	}

	// Pass 2: dump salvaged frames to a temp dir and remux (GoMerger streams
	// from disk — memory stays bounded).
	tmpDir, err := os.MkdirTemp(filepath.Dir(m.OutputPath), ".repair-tlm-*.d")
	if err != nil {
		return false, err
	}
	defer os.RemoveAll(tmpDir)

	out, err := os.CreateTemp(filepath.Dir(m.OutputPath), ".repair-tlm-*.mp4")
	if err != nil {
		return false, err
	}
	outPath := out.Name()
	out.Close()
	defer func() { os.Remove(outPath) }() // no-op after successful rename

	n := 0
	for _, s := range seg.Samples {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		buf := make([]byte, s.Size)
		if _, err := f.ReadAt(buf, s.Offset); err != nil {
			return false, fmt.Errorf("read sample: %w", err)
		}
		for _, img := range recorder.ExtractCompleteJPEGs(buf) {
			name := filepath.Join(tmpDir, "frame_"+strconv.Itoa(n)+".jpg")
			if err := os.WriteFile(name, img, 0o644); err != nil {
				return false, err
			}
			n++
		}
	}

	merger := timelapse.NewGoMerger()
	res, err := merger.Merge(ctx, tmpDir, outPath, m.FPS)
	if err != nil {
		return false, fmt.Errorf("remux: %w", err)
	}
	if res.FramesMerged != n {
		return false, fmt.Errorf("remux frame count mismatch: merged %d, dumped %d", res.FramesMerged, n)
	}
	st, err := os.Stat(outPath)
	if err != nil {
		return false, err
	}
	// The original must be closed BEFORE the replace-rename: on Windows,
	// renaming onto a file this process still holds open fails with a
	// sharing violation (Linux allows rename-over-open). The deferred Close
	// above only covers error paths now.
	if err := f.Close(); err != nil {
		return false, err
	}
	if err := os.Chmod(outPath, 0o644); err != nil {
		return false, err
	}
	if err := os.Rename(outPath, m.OutputPath); err != nil {
		return false, fmt.Errorf("replace output: %w", err)
	}
	if err := db.CompleteTimelapseMerge(ctx, m.ID, m.OutputPath, st.Size(), n, m.Codec, m.SourceSegmentIDs); err != nil {
		return false, fmt.Errorf("update DB row: %w", err)
	}
	fmt.Printf("  [%d] rewritten: %d frames, %d bytes\n", m.ID, n, st.Size())
	return true, nil
}

func printRepairTimelapseMJPEGUsage() {
	fmt.Print(`Usage: mibee-nvr repair timelapse-mjpeg [options]

Rewrite completed MJPEG (mjpa) periodic-merge outputs whose samples carry
double-header JPEG buffers (RTSP senders that pack complete JPEGs into
RFC 2435 RTP payloads — the depacketizer prepends a synthesized header
block that strict browser decoders reject). The complete inner JPEG of
every sample is salvaged losslessly and remuxed in place; the DB row's
frame count and file size are refreshed.

Options:
  --camera <id>   Only repair merges of this camera
  --limit <n>     Process at most n merges
  --dry-run       Report what would change (default)
  --execute       Apply the repair
  --config <path> Config file path (default: mibee-nvr.yaml)
`)
}
