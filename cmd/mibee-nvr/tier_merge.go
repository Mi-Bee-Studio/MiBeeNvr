package main

import (
	"context"
	"crypto/sha1"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/mediaprobe"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/merge"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
)

// tier-merge folds a tiered camera's layer-1 (sub-stream) segments into
// longer per-window files — the ops lever for tier-1 row count. Layer-1 rows
// are born terminal (#763: never a merge input), so nothing in the server
// ever consolidates them; this CLI is the manual counterpart. Sources are
// probed with mediaprobe and folded with the same MergeMP4Segments primitive
// the rolling merge uses; a successful window merge replaces its source rows
// with ONE layer-1 row (merge_status stays "sublayer" — the product is still
// excluded from list/timeline/merge/push filters, same as its sources).
//
// The DB is opened with the same WAL + busy_timeout pragmas as the server, so
// the command MAY run while the NVR is live (same model as the
// cleanup/repair/timelapse-merge CLIs). Fresh segments are protected by
// --keep-newest (default 2h — never touches windows the live tierrec writer
// may still be appending to).
func cmdTierMerge() {
	f, exitCode := parseTierMergeFlags(os.Args)
	if exitCode >= 0 {
		os.Exit(exitCode)
	}
	os.Exit(runTierMerge(f, os.Stdout))
}

type tierMergeFlags struct {
	cfgPath    string
	camera     string
	window     string
	from       string
	to         string
	keepNewest string
	minSegs    int
	execute    bool
	noThrottle bool
}

const tierMergeUsage = `Usage: mibee-nvr tier-merge [options]

Fold a tiered camera's layer-1 (sub-stream) segments into longer per-window
files, in-process against the storage DB. Each window's sources are replaced
by ONE consolidated layer-1 row (file + row); the product stays out of the
default list/timeline/merge/push filters, exactly like its sources.

Safe to run while the NVR is live (WAL DB + fresh-window guard). Dry-run by
default.

Options:
  --camera <id>          Camera ID (required)
  --window <dur>         Merge window bucket (default 1h; e.g. 30m, 1h, 6h)
  --from <YYYY-MM-DD>    Only windows starting on/after this date (config tz)
  --to <YYYY-MM-DD>      Only windows starting before this date (exclusive; default: all)
  --keep-newest <dur>    Skip windows whose newest segment is younger (default 2h; "0"=off)
  --min-segments <n>     Skip windows with fewer segments (default 2)
  --execute              Execute (default: dry-run)
  --no-throttle          Skip the automatic self-downgrade (nice 19 + io best-effort)
  --config <path>        Config file path (default: mibee-nvr.yaml)

Examples:
  mibee-nvr tier-merge --camera cam-xxxx
  mibee-nvr tier-merge --camera cam-xxxx --window 1h --execute
  mibee-nvr tier-merge --camera cam-xxxx --from 2026-09-12 --to 2026-10-01 --execute
`

func parseTierMergeFlags(args []string) (tierMergeFlags, int) {
	var f tierMergeFlags
	f.window = "1h"
	f.keepNewest = "2h"
	f.minSegs = 2
	for i := 2; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--execute":
			f.execute = true
		case arg == "--dry-run":
			f.execute = false
		case arg == "--no-throttle":
			f.noThrottle = true
		case arg == "--help" || arg == "-h":
			fmt.Print(tierMergeUsage)
			return f, 0
		default:
			v, ok := parseFlag(args, &i, "camera")
			if !ok {
				v, ok = parseFlag(args, &i, "window")
			}
			if !ok {
				v, ok = parseFlag(args, &i, "from")
			}
			if !ok {
				v, ok = parseFlag(args, &i, "to")
			}
			if !ok {
				v, ok = parseFlag(args, &i, "keep-newest")
			}
			if !ok {
				v, ok = parseFlag(args, &i, "min-segments")
			}
			if !ok {
				v, ok = parseFlag(args, &i, "config")
			}
			if !ok {
				fmt.Fprintf(os.Stderr, "Error: unknown flag %q\n\n%s", arg, tierMergeUsage)
				return f, 1
			}
			switch {
			case strings.HasPrefix(arg, "--camera"):
				f.camera = v
			case strings.HasPrefix(arg, "--window"):
				f.window = v
			case strings.HasPrefix(arg, "--from"):
				f.from = v
			case strings.HasPrefix(arg, "--to"):
				f.to = v
			case strings.HasPrefix(arg, "--keep-newest"):
				f.keepNewest = v
			case strings.HasPrefix(arg, "--min-segments"):
				n, aerr := strconv.Atoi(v)
				if aerr != nil || n < 1 {
					fmt.Fprintf(os.Stderr, "Error: invalid --min-segments %q\n", v)
					return f, 1
				}
				f.minSegs = n
			case strings.HasPrefix(arg, "--config"):
				f.cfgPath = v
			}
		}
	}
	if f.camera == "" {
		fmt.Fprintf(os.Stderr, "--camera is required\n\n%s", tierMergeUsage)
		return f, 1
	}
	return f, -1
}

func runTierMerge(f tierMergeFlags, stdout io.Writer) int {
	cfg, err := config.Load(f.cfgPath)
	if err != nil {
		fmt.Fprintf(stdout, "Error loading config %q: %v\n", f.cfgPath, err)
		return 1
	}
	window, err := time.ParseDuration(f.window)
	if err != nil || window <= 0 {
		fmt.Fprintf(stdout, "invalid --window %q\n", f.window)
		return 1
	}
	keepNewest, err := time.ParseDuration(f.keepNewest)
	if err != nil || keepNewest < 0 {
		fmt.Fprintf(stdout, "invalid --keep-newest %q\n", f.keepNewest)
		return 1
	}

	loc := time.Local
	if cfg.Timezone != "" && cfg.Timezone != "Local" && cfg.Timezone != "UTC" {
		if l, lerr := time.LoadLocation(cfg.Timezone); lerr == nil {
			loc = l
		}
	}
	var from, to time.Time
	if f.from != "" {
		if from, err = time.ParseInLocation("2006-01-02", f.from, loc); err != nil {
			fmt.Fprintf(stdout, "invalid --from %q: %v\n", f.from, err)
			return 1
		}
	}
	if f.to != "" {
		if to, err = time.ParseInLocation("2006-01-02", f.to, loc); err != nil {
			fmt.Fprintf(stdout, "invalid --to %q: %v\n", f.to, err)
			return 1
		}
	}

	if !f.noThrottle {
		if terr := selfThrottle(); terr != nil {
			fmt.Fprintf(stdout, "Self-throttle skipped: %v\n", terr)
		} else {
			_, _ = fmt.Fprintln(stdout, "Self-throttled: nice 19, io best-effort level 7 (use --no-throttle to skip)")
		}
	}

	db, err := storage.New(filepath.Join(cfg.Storage.RootDir, "mibee-nvr.db"))
	if err != nil {
		fmt.Fprintf(stdout, "Error opening DB: %v\n", err)
		return 1
	}
	defer db.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	layerSub := 1
	filter := model.RecordingFilter{
		CameraID: f.camera,
		Layer:    &layerSub,
		StartTime: func() time.Time {
			if !from.IsZero() {
				return from
			}
			return time.Time{}
		}(),
		SortBy:    "started_at",
		SortOrder: "asc",
	}
	if !to.IsZero() {
		filter.EndTime = to
	}
	recs, err := db.ListRecordings(ctx, filter)
	if err != nil {
		fmt.Fprintf(stdout, "Error listing layer-1 rows: %v\n", err)
		return 1
	}
	if len(recs) == 0 {
		fmt.Fprintf(stdout, "No layer-1 segments found for camera %s\n", f.camera)
		return 0
	}

	// Group into window buckets, oldest first.
	buckets := map[time.Time][]model.Recording{}
	var keys []time.Time
	for _, r := range recs {
		k := r.StartedAt.In(loc).Truncate(window)
		if _, seen := buckets[k]; !seen {
			keys = append(keys, k)
		}
		buckets[k] = append(buckets[k], r)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].Before(keys[j]) })

	mode := "DRY-RUN"
	if f.execute {
		mode = "EXECUTE"
	}
	fmt.Fprintf(stdout, "tier-merge %s: camera=%s segments=%d windows=%d window=%s keep-newest=%s\n",
		mode, f.camera, len(recs), len(keys), window, keepNewest)

	var mergedWindows, failedWindows, skippedWindows int
	var srcBytes, outBytes int64
	var srcRows int
	for _, k := range keys {
		rows := buckets[k]
		newest := rows[len(rows)-1]
		if keepNewest > 0 && time.Since(newest.EndedAt) < keepNewest {
			skippedWindows++
			fmt.Fprintf(stdout, "  [%s] SKIP fresh (%d segs, newest %s old)\n", k.Format("2006-01-02 15:04"), len(rows), time.Since(newest.EndedAt).Round(time.Minute))
			continue
		}
		if len(rows) < f.minSegs {
			skippedWindows++
			fmt.Fprintf(stdout, "  [%s] SKIP thin (%d segs < min %d)\n", k.Format("2006-01-02 15:04"), len(rows), f.minSegs)
			continue
		}

		// Probe sources; missing files degrade to row-only deletion, probe
		// failures abort the window (never fold a half-known window).
		var infos []*mediaprobe.SegmentInfo
		var foldRows []model.Recording
		var missingRows []model.Recording
		for _, r := range rows {
			abs := filepath.Join(cfg.Storage.RootDir, r.FilePath)
			if _, serr := os.Stat(abs); serr != nil {
				missingRows = append(missingRows, r)
				continue
			}
			info, perr := mediaprobe.ParseSegment(abs)
			if perr != nil {
				fmt.Fprintf(stdout, "  [%s] FAIL probe %s: %v\n", k.Format("2006-01-02 15:04"), filepath.Base(r.FilePath), perr)
				missingRows = nil
				foldRows = nil
				infos = nil
				break
			}
			infos = append(infos, info)
			foldRows = append(foldRows, r)
		}
		if infos == nil {
			failedWindows++
			continue
		}
		if len(infos) == 0 {
			// Only missing files in this window: drop the phantom rows.
			if f.execute {
				deleteTierRows(ctx, db, missingRows, cfg.Storage.RootDir, stdout)
			}
			fmt.Fprintf(stdout, "  [%s] PURGED %d phantom rows (files already gone)\n", k.Format("2006-01-02 15:04"), len(missingRows))
			skippedWindows++
			continue
		}

		// Split by codec/params key: MergeMP4Segments must not mix streams.
		runs := splitTierRuns(infos, foldRows)
		for _, run := range runs {
			if len(run.infos) < f.minSegs {
				// Param change split left a lone segment — leave untouched.
				continue
			}
			first, last := run.rows[0], run.rows[len(run.rows)-1]
			outRel := filepath.Join(first.CameraID, first.StartedAt.In(loc).Format("200601"), first.StartedAt.In(loc).Format("02"), first.StartedAt.In(loc).Format("15"),
				"subm_"+first.StartedAt.In(loc).Format("20060102_150405")+".mp4")
			outAbs := filepath.Join(cfg.Storage.RootDir, outRel)

			var srcN int64
			for _, r := range run.rows {
				srcN += r.FileSize
			}
			if !f.execute {
				fmt.Fprintf(stdout, "  [%s] would merge %d segs (%.1f MB, %s..%s) -> %s\n",
					k.Format("2006-01-02 15:04"), len(run.rows), float64(srcN)/1e6,
					first.StartedAt.In(loc).Format("15:04:05"), last.EndedAt.In(loc).Format("15:04:05"), filepath.Base(outRel))
				continue
			}

			if err := os.MkdirAll(filepath.Dir(outAbs), 0o755); err != nil {
				fmt.Fprintf(stdout, "  [%s] FAIL mkdir: %v\n", k.Format("2006-01-02 15:04"), err)
				failedWindows++
				continue
			}
			stats, merr := merge.MergeMP4Segments(ctx, run.infos, outAbs)
			if merr != nil {
				fmt.Fprintf(stdout, "  [%s] FAIL merge: %v\n", k.Format("2006-01-02 15:04"), merr)
				_ = os.Remove(outAbs)
				failedWindows++
				continue
			}
			st, _ := os.Stat(outAbs)
			var outSize int64
			if st != nil {
				outSize = st.Size()
			}
			// Replace source rows with ONE consolidated layer-1 row.
			deleteTierRows(ctx, db, run.rows, cfg.Storage.RootDir, stdout)
			prod := model.Recording{
				ID:        strconv.FormatInt(time.Now().UnixNano(), 10),
				CameraID:  first.CameraID,
				FilePath:  filepath.ToSlash(outRel),
				Format:    first.Format,
				StartedAt: first.StartedAt,
				EndedAt:   last.EndedAt,
				Duration:  last.EndedAt.Sub(first.StartedAt).Seconds(),
				FileSize:  outSize,
				FrameCount: func() int {
					var n int
					for _, idx := range stats.Included {
						if idx >= 0 && idx < len(run.rows) {
							n += run.rows[idx].FrameCount
						}
					}
					return n
				}(),
				MergeStatus: model.MergeStatusSublayer,
				Layer:       model.LayerSub,
			}
			if ierr := db.InsertRecordingWithRetry(ctx, &prod, 5, 200*time.Millisecond); ierr != nil {
				fmt.Fprintf(stdout, "  [%s] WARN product row insert failed: %v (file kept at %s)\n", k.Format("2006-01-02 15:04"), ierr, outRel)
			}
			mergedWindows++
			srcRows += len(run.rows)
			srcBytes += srcN
			outBytes += outSize
			fmt.Fprintf(stdout, "  [%s] merged %d segs %.1fMB -> %.1fMB (%s)\n",
				k.Format("2006-01-02 15:04"), len(run.rows), float64(srcN)/1e6, float64(outSize)/1e6, filepath.Base(outRel))
		}
		// Rows whose files were missing inside a folded window.
		if f.execute && len(missingRows) > 0 {
			deleteTierRows(ctx, db, missingRows, cfg.Storage.RootDir, stdout)
		}
	}

	fmt.Fprintf(stdout, "Done: %d windows merged (%d rows -> %d rows, %.2f GB src -> %.2f GB out), %d skipped, %d failed\n",
		mergedWindows, srcRows, mergedWindows, float64(srcBytes)/1e9, float64(outBytes)/1e9, skippedWindows, failedWindows)
	if !f.execute {
		_, _ = fmt.Fprintln(stdout, "Dry-run only — add --execute to apply.")
	}
	return 0
}

// tierRun is one contiguous same-codec/same-params group inside a window.
type tierRun struct {
	infos []*mediaprobe.SegmentInfo
	rows  []model.Recording
}

// splitTierRuns splits probed segments into runs sharing codec + parameter
// sets — MergeMP4Segments must never fold across a param change (rolling
// merge groups by the same key before folding).
func splitTierRuns(infos []*mediaprobe.SegmentInfo, rows []model.Recording) []tierRun {
	keyOf := func(in *mediaprobe.SegmentInfo) string {
		h := sha1.New()
		h.Write([]byte(in.Codec))
		h.Write(in.SPS)
		h.Write(in.PPS)
		h.Write(in.VPS)
		return string(h.Sum(nil))
	}
	var runs []tierRun
	for i, in := range infos {
		if len(runs) > 0 && keyOf(runs[len(runs)-1].infos[0]) == keyOf(in) {
			runs[len(runs)-1].infos = append(runs[len(runs)-1].infos, in)
			runs[len(runs)-1].rows = append(runs[len(runs)-1].rows, rows[i])
			continue
		}
		runs = append(runs, tierRun{infos: []*mediaprobe.SegmentInfo{in}, rows: []model.Recording{rows[i]}})
	}
	return runs
}

// deleteTierRows removes rows (batch) and their files (best effort, paths
// confined to the storage root).
func deleteTierRows(ctx context.Context, db *storage.DB, rows []model.Recording, root string, stdout io.Writer) {
	ids := make([]string, len(rows))
	for i, r := range rows {
		ids[i] = r.ID
	}
	if _, err := db.DeleteRecordingsBatch(ctx, ids); err != nil {
		fmt.Fprintf(stdout, "  WARN row delete failed: %v\n", err)
		return
	}
	for _, r := range rows {
		abs := filepath.Join(root, r.FilePath)
		if !strings.HasPrefix(filepath.Clean(abs), filepath.Clean(root)+string(os.PathSeparator)) {
			continue
		}
		if err := os.Remove(abs); err != nil && !os.IsNotExist(err) {
			fmt.Fprintf(stdout, "  WARN file delete failed: %s: %v\n", r.FilePath, err)
		}
	}
}
