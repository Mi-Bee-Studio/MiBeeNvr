package main

// repair mjpeg-containerize (#761): one-shot conversion of legacy
// directory-form MJPEG segments (one JPEG file per frame) into single-file
// AVI containers — the recording default since #761. Mirrors the repair CLI
// shape: --dry-run default, --execute to apply, --camera / --limit filters.
//
// Per segment: frames are read sorted (the storage timestamp filenames sort
// chronologically), written through the incremental AVI muxer to a sibling
// .tmp file, atomically renamed, verified by demuxing the frame index back,
// and only then is the DB row flipped (path/format/size/count) and the source
// directory removed (unless --keep-old). A failed verify leaves the .tmp/avi
// behind for inspection and the row untouched.
//
// Run with the server stopped (preferred) or quiet: in-flight merges could
// race the source directory otherwise (same guidance as the removed
// migrate-mjpeg tool this resurrects).

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/avi"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
)

type containerizeOptions struct {
	cameraID string
	limit    int
	dryRun   bool
	keepOld  bool
}

type containerizeReport struct {
	planned   int
	converted int
	skipped   int
	failed    int
}

// containerizeMJPEGDirs converts directory-form MJPEG rows to AVI containers.
// Rows are processed sequentially (a maintenance CLI must not compete with
// itself for disk bandwidth); each row's fate is reported on w.
func containerizeMJPEGDirs(ctx context.Context, db *storage.DB, recs []*model.Recording, opts containerizeOptions, w io.Writer) containerizeReport {
	var rep containerizeReport
	for _, rec := range recs {
		if ctx.Err() != nil {
			break
		}
		if opts.cameraID != "" && rec.CameraID != opts.cameraID {
			continue
		}
		if opts.limit > 0 && rep.planned >= opts.limit {
			break
		}
		fi, err := os.Stat(rec.FilePath)
		if err != nil || !fi.IsDir() {
			rep.skipped++ // already a file / migrated / gone
			continue
		}
		if rec.MergeStatus == model.MergeStatusDark {
			// Dark segments are cleanup-bound — converting them is wasted IO.
			fmt.Fprintf(w, "  SKIP %s — dark segment\n", rec.ID)
			rep.skipped++
			continue
		}
		frames, err := listDirFrames(rec.FilePath)
		if err != nil || len(frames) == 0 {
			fmt.Fprintf(w, "  SKIP %s — no frames (%v)\n", rec.ID, err)
			rep.skipped++
			continue
		}
		rep.planned++
		if opts.dryRun {
			fmt.Fprintf(w, "  PLAN %s — %d frames → %s.avi\n", rec.ID, len(frames), rec.FilePath)
			continue
		}
		if err := containerizeOne(db, rec, frames, opts.keepOld); err != nil {
			fmt.Fprintf(w, "  FAIL %s — %v (row untouched)\n", rec.ID, err)
			rep.failed++
			continue
		}
		fmt.Fprintf(w, "  OK   %s — %d frames → %s.avi\n", rec.ID, len(frames), rec.FilePath)
		rep.converted++
	}
	return rep
}

// containerizeOne performs the full convert-verify-commit cycle for one row.
func containerizeOne(db *storage.DB, rec *model.Recording, frames []string, keepOld bool) error {
	ctx := context.Background()
	aviPath := rec.FilePath + ".avi"
	tmpPath := aviPath + ".tmp"

	w, h := 640, 480
	if len(frames) > 0 {
		if dw, dh, ok := jpegSOFDimensions(filepath.Join(rec.FilePath, frames[0])); ok {
			w, h = dw, dh
		}
	}
	f, err := os.OpenFile(tmpPath, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("create tmp: %w", err)
	}
	m := avi.NewVideoOnlyMuxer(f, w, h)
	wrote := 0
	for _, name := range frames {
		data, err := os.ReadFile(filepath.Join(rec.FilePath, name))
		if err != nil {
			f.Close()
			os.Remove(tmpPath)
			return fmt.Errorf("read frame: %w", err)
		}
		if err := m.WriteVideo(data, 0); err != nil {
			f.Close()
			os.Remove(tmpPath)
			return fmt.Errorf("write frame: %w", err)
		}
		wrote++
	}
	if err := m.Close(); err != nil {
		f.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("close muxer: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("fsync: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("close file: %w", err)
	}

	// Verify before touching the DB: the demuxed frame count must match what
	// we wrote, else the container is untrustworthy.
	vf, err := os.Open(tmpPath)
	if err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("reopen verify: %w", err)
	}
	d, err := avi.NewDemuxer(vf)
	if err != nil {
		vf.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("verify demux: %w", err)
	}
	idx, err := d.VideoFrameIndex()
	vf.Close()
	if err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("verify index: %w", err)
	}
	if len(idx) != wrote {
		os.Remove(tmpPath)
		return fmt.Errorf("verify: wrote %d frames, container has %d", wrote, len(idx))
	}

	if err := os.Rename(tmpPath, aviPath); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("rename: %w", err)
	}
	fi, err := os.Stat(aviPath)
	if err != nil {
		return fmt.Errorf("stat container: %w", err)
	}

	// DB last: the row flips only after the container is durable on disk.
	rec.FilePath = aviPath
	rec.Format = model.FormatAVI
	rec.FrameCount = wrote
	rec.FileSize = fi.Size()
	if err := db.UpdateRecording(ctx, rec); err != nil {
		return fmt.Errorf("db update: %w", err)
	}
	if !keepOld {
		if err := os.RemoveAll(rec.FilePath[:len(rec.FilePath)-len(".avi")]); err != nil {
			return fmt.Errorf("remove source dir (row already migrated): %w", err)
		}
	}
	return nil
}

// listDirFrames returns the JPEG frame filenames inside a directory-form
// segment, sorted lexically (= chronologically for the storage timestamp
// naming scheme).
func listDirFrames(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var frames []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if strings.HasSuffix(e.Name(), ".jpg") || strings.HasSuffix(e.Name(), ".jpeg") {
			frames = append(frames, e.Name())
		}
	}
	sort.Strings(frames)
	return frames, nil
}

// jpegSOFDimensions extracts width/height from a JPEG SOF0/1/2 marker (same
// parser the recorders use; local copy keeps the CLI self-contained).
func jpegSOFDimensions(path string) (width, height int, ok bool) {
	data, err := os.ReadFile(path)
	if err != nil || len(data) < 4 || data[0] != 0xFF || data[1] != 0xD8 {
		return 0, 0, false
	}
	idx := 2
	for idx < len(data)-1 {
		if data[idx] != 0xFF {
			return 0, 0, false
		}
		marker := data[idx+1]
		if marker == 0xC0 || marker == 0xC1 || marker == 0xC2 {
			if idx+9 >= len(data) {
				return 0, 0, false
			}
			height = int(data[idx+5])<<8 | int(data[idx+6])
			width = int(data[idx+7])<<8 | int(data[idx+8])
			return width, height, true
		}
		if marker == 0xD9 || marker == 0xDA {
			return 0, 0, false
		}
		if marker == 0xFF || (marker >= 0xD0 && marker <= 0xD7) || marker == 0x01 {
			idx += 2
			continue
		}
		if idx+3 >= len(data) {
			return 0, 0, false
		}
		segLen := int(data[idx+2])<<8 | int(data[idx+3])
		if segLen < 2 {
			return 0, 0, false
		}
		idx += 2 + segLen
	}
	return 0, 0, false
}

func runRepairMJPEGContainerize() int {
	opts := parseRepairFlags(3)
	if opts.configPath == "__help__" {
		printRepairMJPEGContainerizeUsage()
		return 0
	}
	keepOld := false
	for i := 3; i < len(os.Args); i++ {
		if os.Args[i] == "--keep-old" {
			keepOld = true
		}
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
	fmt.Printf("repair mjpeg-containerize (%s) — #761 dir-form → AVI containers\n", mode)

	recs, err := db.ListRecordings(ctx, model.RecordingFilter{
		Format: model.FormatMJPEG, CameraID: opts.cameraID, Limit: 0,
		SortBy: "started_at", SortOrder: "asc",
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error listing mjpeg recordings: %v\n", err)
		return 1
	}

	ptrs := make([]*model.Recording, len(recs))
	for i := range recs {
		ptrs[i] = &recs[i]
	}
	rep := containerizeMJPEGDirs(ctx, db, ptrs, containerizeOptions{
		cameraID: opts.cameraID, limit: opts.limit, dryRun: opts.dryRun, keepOld: keepOld,
	}, os.Stdout)
	fmt.Printf("\nplanned=%d converted=%d skipped=%d failed=%d\n",
		rep.planned, rep.converted, rep.skipped, rep.failed)
	if rep.failed > 0 {
		return 2
	}
	return 0
}

func printRepairMJPEGContainerizeUsage() {
	fmt.Println(`repair mjpeg-containerize — convert legacy dir-form MJPEG segments to AVI containers (#761)

Usage:
  mibee-nvr repair mjpeg-containerize [flags]

Flags:
  --config PATH   config file (default: mibee-nvr.yaml)
  --camera ID     only convert this camera's segments
  --limit N       convert at most N segments
  --keep-old      keep the source frame directory after conversion
  --dry-run       report the plan without changes (default)
  --execute       apply the conversion

Each segment's frames are written into a verified single-file AVI container
next to the source directory; the DB row flips to format=avi only after the
container passes a frame-count verify. Prefer running with the server stopped.`)
}
