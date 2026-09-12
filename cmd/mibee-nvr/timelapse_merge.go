package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/cleanup"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/timelapse"
)

// timelapse-merge converts existing video recordings (H264/H265/AVI/MJPEG)
// into periodic timelapse merges over a date range — the CLI counterpart of
// POST /api/timelapse/{id}/merge, executed in-process against the storage DB
// instead of over HTTP. Frame sampling (--interval), output fps and source
// deletion are per-run overrides that never mutate the camera config.
//
// The DB is opened with the same WAL + busy_timeout pragmas as the server, so
// the command MAY run while the NVR is live (same multi-process model as the
// cleanup/repair CLIs). One guard applies: while the server is running,
// cameras with timelapse.enabled=true are refused unless --force — their
// windows belong to the server's own merge scheduler, and the per-process
// activeMerges dedup cannot see cross-process concurrent merges of the same
// window.

func cmdTimelapseMerge() {
	f, exitCode := parseTimelapseMergeFlags(os.Args)
	if exitCode >= 0 {
		os.Exit(exitCode)
	}
	// Probe the configured listen address (same resolution as merge-cameras)
	// so the CLI knows whether the NVR is live.
	cfg, err := config.Load(f.cfgPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading config %q: %v\n", f.cfgPath, err)
		os.Exit(1)
	}
	probeAddr := net.JoinHostPort(listenHostOf(cfg.Server.Listen), listenPortOf(cfg.Server.Listen))
	os.Exit(runTimelapseMerge(f, os.Stdout, isPortOpen(probeAddr)))
}

// tlmBoolPtr is the local *bool helper (test file reuses it).
func tlmBoolPtr(b bool) *bool { return &b }

// timelapseMergeFlags carries the parsed `timelapse-merge` subcommand flags.
type timelapseMergeFlags struct {
	cfgPath     string
	camerasArg  string // "all" or comma-separated camera IDs
	encoding    string // optional encoding filter, only used with --camera all
	start       string // YYYY-MM-DD (required)
	end         string // YYYY-MM-DD (default: yesterday in config tz)
	duration    string // window size label (default natural-day)
	interval    string // frame sampling interval override (Go duration)
	fps         int    // output fps override (0 = camera config / 10)
	deleteSrc   *bool  // tri-state: nil = camera config default
	execute     bool
	force       bool
	noThrottle  bool   // --no-throttle: skip the automatic nice/ionice self-downgrade
	delThrottle string // --delete-throttle <dur>: per-chunk pause for directory-form source deletion ("0" = off)
}

const timelapseMergeUsage = `Usage: mibee-nvr timelapse-merge [options]

Convert existing video recordings (H264/H265/AVI/MJPEG) into periodic
timelapse merges over a date range, in-process against the storage DB
(the CLI counterpart of POST /api/timelapse/{id}/merge). Open windows
are skipped; windows with an existing completed merge row are skipped;
source recordings can be deleted after a successful merge.

Options:
  --camera <ids|all>       Comma-separated camera IDs, or "all"
  --encoding <enc>         With --camera all: only cameras matching (e.g. jpeg)
  --start <YYYY-MM-DD>     First window date (required, config timezone)
  --end <YYYY-MM-DD>       Last window date (default: yesterday)
  --duration <label>       Window size: natural-day (default), 8h, 12h, 24h, 7d, 30d
  --interval <dur>         Frame sampling interval (default: camera timelapse.interval, else 30s)
  --fps <n>                Output fps (default: camera merge_output_fps, else 10)
  --delete-sources         Delete source recordings after a successful merge
  --no-delete-sources      Never delete sources for this run
  --delete-throttle <dur>  Pause between chunks when deleting directory-form sources (default 200ms, "0"=off)
  --execute                Execute (default: dry-run)
  --force                  Process timelapse-enabled cameras while the NVR is running
  --no-throttle            Skip the automatic self-downgrade (nice 19 + io best-effort)
  --config <path>          Config file path (default: mibee-nvr.yaml)

Examples:
  mibee-nvr timelapse-merge --camera all --encoding jpeg --start 2026-08-26 --dry-run
  mibee-nvr timelapse-merge --camera all --encoding jpeg --start 2026-08-26 --interval 1s --delete-sources --execute
`

// parseTimelapseMergeFlags reads the flags from a full argv (flags start at
// [2]). The int result is an exit code sentinel: 0 = help printed, -1 =
// proceed, 1 = parse error printed.
func parseTimelapseMergeFlags(args []string) (timelapseMergeFlags, int) {
	var f timelapseMergeFlags
	for i := 2; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--execute":
			f.execute = true
		case arg == "--dry-run":
			f.execute = false
		case arg == "--force":
			f.force = true
		case arg == "--no-throttle":
			f.noThrottle = true
		case arg == "--delete-sources":
			f.deleteSrc = tlmBoolPtr(true)
		case arg == "--no-delete-sources":
			f.deleteSrc = tlmBoolPtr(false)
		case arg == "--help" || arg == "-h":
			fmt.Print(timelapseMergeUsage)
			return f, 0
		default:
			v, ok := parseFlag(args, &i, "camera")
			if !ok {
				v, ok = parseFlag(args, &i, "encoding")
			}
			if !ok {
				v, ok = parseFlag(args, &i, "start")
			}
			if !ok {
				v, ok = parseFlag(args, &i, "end")
			}
			if !ok {
				v, ok = parseFlag(args, &i, "duration")
			}
			if !ok {
				v, ok = parseFlag(args, &i, "interval")
			}
			if !ok {
				v, ok = parseFlag(args, &i, "fps")
			}
			if !ok {
				v, ok = parseFlag(args, &i, "config")
			}
			if !ok {
				v, ok = parseFlag(args, &i, "delete-throttle")
			}
			if !ok {
				fmt.Fprintf(os.Stderr, "Error: unknown flag %q\n\n%s", arg, timelapseMergeUsage)
				return f, 1
			}
			switch {
			case strings.HasPrefix(arg, "--camera"):
				f.camerasArg = v
			case strings.HasPrefix(arg, "--encoding"):
				f.encoding = v
			case strings.HasPrefix(arg, "--start"):
				f.start = v
			case strings.HasPrefix(arg, "--end"):
				f.end = v
			case strings.HasPrefix(arg, "--duration"):
				f.duration = v
			case strings.HasPrefix(arg, "--interval"):
				f.interval = v
			case strings.HasPrefix(arg, "--fps"):
				n, err := strconv.Atoi(v)
				if err != nil || n <= 0 {
					fmt.Fprintf(os.Stderr, "Error: invalid --fps %q\n", v)
					return f, 1
				}
				f.fps = n
			case strings.HasPrefix(arg, "--config"):
				f.cfgPath = v
			case strings.HasPrefix(arg, "--delete-throttle"):
				f.delThrottle = v
			}
		}
	}
	if f.duration == "" {
		f.duration = "natural-day"
	}
	if f.cfgPath == "" {
		f.cfgPath = "mibee-nvr.yaml"
	}
	if f.interval != "" {
		d, err := time.ParseDuration(f.interval)
		if err != nil || d <= 0 {
			fmt.Fprintf(os.Stderr, "Error: invalid --interval %q (must be a positive Go duration)\n", f.interval)
			return f, 1
		}
	}
	if f.delThrottle != "" {
		d, err := time.ParseDuration(f.delThrottle)
		if err != nil || d < 0 {
			fmt.Fprintf(os.Stderr, "Error: invalid --delete-throttle %q (must be a non-negative Go duration, e.g. 200ms or 0)\n", f.delThrottle)
			return f, 1
		}
	}
	return f, -1
}

// resolveTimelapseMergeCameras picks the target cameras: explicit comma-
// separated IDs (all must exist), or "all" (optionally filtered by encoding).
func resolveTimelapseMergeCameras(cfg *config.Config, camerasArg, encoding string) ([]config.CameraConfig, error) {
	if camerasArg == "" {
		return nil, fmt.Errorf("--camera is required (comma-separated IDs or \"all\")")
	}
	var picked []config.CameraConfig
	if camerasArg == "all" {
		for _, c := range cfg.Cameras {
			if encoding != "" && c.Encoding != encoding {
				continue
			}
			picked = append(picked, c)
		}
		if len(picked) == 0 {
			return nil, fmt.Errorf("no cameras match --camera all%s", encodingSuffix(encoding))
		}
		return picked, nil
	}
	index := make(map[string]bool, len(cfg.Cameras))
	for _, c := range cfg.Cameras {
		index[c.ID] = true
	}
	for _, id := range strings.Split(camerasArg, ",") {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if !index[id] {
			return nil, fmt.Errorf("camera %q not found in config", id)
		}
		for _, c := range cfg.Cameras {
			if c.ID == id {
				picked = append(picked, c)
			}
		}
	}
	if len(picked) == 0 {
		return nil, fmt.Errorf("no valid camera IDs in %q", camerasArg)
	}
	return picked, nil
}

func encodingSuffix(encoding string) string {
	if encoding == "" {
		return ""
	}
	return " --encoding " + encoding
}

// timelapseMergeWindow is one enumerated merge window [Start, End).
type timelapseMergeWindow struct {
	Start, End time.Time
}

// enumerateMergeWindows tiles merge windows from the local-midnight window
// containing startDay up to (but not including) the day AFTER endDay — i.e.
// both boundary dates are inclusive.
func enumerateMergeWindows(startDay, endDay time.Time, dur time.Duration, loc *time.Location) []timelapseMergeWindow {
	first, _ := timelapse.MergeWindowFor(startDay, dur, loc)
	endBound := time.Date(endDay.Year(), endDay.Month(), endDay.Day(), 0, 0, 0, 0, loc).Add(24 * time.Hour)
	var ws []timelapseMergeWindow
	for cur := first; cur.Before(endBound); cur = cur.Add(dur) {
		ws = append(ws, timelapseMergeWindow{Start: cur, End: cur.Add(dur)})
	}
	return ws
}

// effectiveTimelapseMergeInterval resolves the frame sampling interval:
// override flag > camera timelapse.interval > 30s default. A malformed camera
// config value falls back to the default (config is validated at server
// startup; the CLI degrades instead of hard-failing).
func effectiveTimelapseMergeInterval(cam config.CameraConfig, override string) (time.Duration, error) {
	if override != "" {
		return time.ParseDuration(override)
	}
	if cam.Timelapse != nil && cam.Timelapse.Interval != "" {
		if d, err := time.ParseDuration(cam.Timelapse.Interval); err == nil && d > 0 {
			return d, nil
		}
	}
	return 30 * time.Second, nil
}

// effectiveTimelapseMergeFPS resolves the output fps: override flag >
// camera merge_output_fps > 10 default (mirrors the API handler).
func effectiveTimelapseMergeFPS(cam config.CameraConfig, override int) int {
	if override > 0 {
		return override
	}
	if cam.Timelapse != nil && cam.Timelapse.MergeOutputFPS > 0 {
		return cam.Timelapse.MergeOutputFPS
	}
	return 10
}

// effectiveTimelapseMergeDeleteSources resolves the source-deletion flag:
// CLI tri-state override > camera delete_recordings_after_merge.
func effectiveTimelapseMergeDeleteSources(cam config.CameraConfig, override *bool) bool {
	if override != nil {
		return *override
	}
	return cam.Timelapse != nil && cam.Timelapse.DeleteRecordingsAfterMerge
}

// cameraRecordingEnabled mirrors the API's recording-enabled resolution:
// nil pointer = enabled.
func cameraRecordingEnabled(cam config.CameraConfig) bool {
	return cam.RecordingEnabled == nil || *cam.RecordingEnabled
}

// cliSourceDeleter adapts cleanup.CleanupManager to timelapse.SourceRecordingDeleter
// (the CLI twin of pkg/app's timelapseSourceDeleter).
type cliSourceDeleter struct{ cm *cleanup.CleanupManager }

func (d cliSourceDeleter) DeleteRecordings(ctx context.Context, recordings []model.Recording, reason string) ([]string, error) {
	return d.cm.BatchDeleteRecordingsWithFiles(ctx, recordings, reason)
}

// runTimelapseMerge is the CLI core. serverRunning gates timelapse-enabled
// cameras (their windows belong to the server's scheduler). Returns the
// process exit code.
func runTimelapseMerge(f timelapseMergeFlags, stdout io.Writer, serverRunning bool) int {
	if f.duration == "" {
		f.duration = "natural-day"
	}
	dur, err := config.ParseMergeDuration(f.duration)
	if err != nil {
		fmt.Fprintf(stdout, "Error: %v\n", err)
		return 1
	}
	cfg, err := config.Load(f.cfgPath)
	if err != nil {
		fmt.Fprintf(stdout, "Error loading config %q: %v\n", f.cfgPath, err)
		return 1
	}
	loc := resolveTimelapseMergeLocation(cfg)

	cameras, err := resolveTimelapseMergeCameras(cfg, f.camerasArg, f.encoding)
	if err != nil {
		fmt.Fprintf(stdout, "Error: %v\n", err)
		return 1
	}

	if f.start == "" {
		_, _ = fmt.Fprintln(stdout, "Error: --start is required (YYYY-MM-DD)")
		return 1
	}
	startDay, err := time.ParseInLocation("2006-01-02", f.start, loc)
	if err != nil {
		fmt.Fprintf(stdout, "Error: invalid --start %q: %v\n", f.start, err)
		return 1
	}
	endDay := time.Now().In(loc).AddDate(0, 0, -1)
	if f.end != "" {
		endDay, err = time.ParseInLocation("2006-01-02", f.end, loc)
		if err != nil {
			fmt.Fprintf(stdout, "Error: invalid --end %q: %v\n", f.end, err)
			return 1
		}
	}
	if endDay.Before(startDay) {
		fmt.Fprintf(stdout, "Error: --end (%s) is before --start (%s)\n", f.end, f.start)
		return 1
	}

	windows := enumerateMergeWindows(startDay, endDay, dur, loc)

	db, err := storage.New(filepath.Join(cfg.Storage.RootDir, "mibee-nvr.db"))
	if err != nil {
		fmt.Fprintf(stdout, "Error opening database: %v\n", err)
		return 1
	}
	defer db.Close()
	if err := db.Init(context.Background()); err != nil {
		fmt.Fprintf(stdout, "Error initialising database: %v\n", err)
		return 1
	}

	// While the NVR is live, refuse timelapse-enabled cameras: the server's
	// merge scheduler owns their windows and the per-process dedup can't see
	// a cross-process concurrent merge of the same window. Dry-run bypasses
	// the gate — planning makes no changes.
	if f.execute && serverRunning && !f.force {
		var kept []config.CameraConfig
		for _, cam := range cameras {
			if cam.Timelapse != nil && cam.Timelapse.Enabled {
				fmt.Fprintf(stdout, "Refusing camera %s: timelapse-enabled camera while the NVR is running (its merge scheduler owns these windows; use --force to override or stop the NVR)\n", cam.ID)
				continue
			}
			kept = append(kept, cam)
		}
		if len(kept) == 0 {
			_, _ = fmt.Fprintln(stdout, "No cameras left to process.")
			return 1
		}
		cameras = kept
	} else if f.execute && serverRunning {
		_, _ = fmt.Fprintln(stdout, "Notice: NVR is running; proceeding with WAL-concurrent DB access (--force).")
	}

	mode := "DRY RUN — no changes made (run with --execute)"
	if f.execute {
		mode = "EXECUTE"
	}
	fmt.Fprintf(stdout, "TIME LAPSE MERGE — %s\n", mode)
	fmt.Fprintf(stdout, "  config=%s db=%s\n", f.cfgPath, filepath.Join(cfg.Storage.RootDir, "mibee-nvr.db"))
	fmt.Fprintf(stdout, "  windows=%d duration=%s range=%s..%s tz=%s\n\n",
		len(windows), f.duration, startDay.Format("2006-01-02"), endDay.Format("2006-01-02"), loc.String())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if f.execute {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		go func() {
			<-sigCh
			cancel()
		}()
	}

	// Self-downgrade before any heavy IO (#748): the 2026-09-12 incident had
	// the CLI at the NVR's priority starving online recording (load 8-13,
	// cameras 17→10) — renice after the fact could not undo the storm.
	if f.execute && !f.noThrottle {
		if err := selfThrottle(); err != nil {
			fmt.Fprintf(stdout, "Notice: self-throttle unavailable (%v) — running at default priority\n", err)
		} else {
			_, _ = fmt.Fprintln(stdout, "Self-throttled: nice 19, io best-effort level 7 (--no-throttle to disable).")
		}
	}

	// Directory-form source deletion pacing (#748): --delete-throttle pauses
	// between chunks of frame-file unlinks so the ext4 journal keeps up.
	delThrottle := 200 * time.Millisecond
	if f.delThrottle != "" {
		d, err := time.ParseDuration(f.delThrottle)
		if err == nil {
			delThrottle = d
		}
	}

	var deleter timelapse.SourceRecordingDeleter
	if f.execute {
		store, err := storage.NewManager(cfg.Storage.RootDir)
		if err != nil {
			fmt.Fprintf(stdout, "Error creating storage manager: %v\n", err)
			return 1
		}
		cleanupMgr, err := cleanup.NewCleanupManager(db, store, cfg.Cleanup)
		if err != nil {
			fmt.Fprintf(stdout, "Error creating cleanup manager: %v\n", err)
			return 1
		}
		if delThrottle > 0 {
			cleanupMgr.SetDirectoryDeleteThrottle(200, delThrottle)
		}
		deleter = cliSourceDeleter{cm: cleanupMgr}
	}

	var totalMerged, totalFailed int
	for _, cam := range cameras {
		interval, err := effectiveTimelapseMergeInterval(cam, f.interval)
		if err != nil {
			fmt.Fprintf(stdout, "Error: invalid --interval %q: %v\n", f.interval, err)
			return 1
		}
		fps := effectiveTimelapseMergeFPS(cam, f.fps)
		delSources := effectiveTimelapseMergeDeleteSources(cam, f.deleteSrc)

		fmt.Fprintf(stdout, "camera %s (%s, %s)\n", cam.ID, cam.Name, cam.Encoding)
		fmt.Fprintf(stdout, "  interval=%s fps=%d delete-sources=%v\n", interval, fps, delSources)

		var mgr *timelapse.PeriodicMergeManager
		if f.execute {
			dataDir := filepath.Join(cfg.Storage.RootDir, "periodic-merge")
			mgr = timelapse.NewPeriodicMergeManager(
				db, db, timelapse.NewGoMerger(), fps, dataDir, dur, loc,
				timelapse.WithMergeStore(db),
				timelapse.WithDurationLabel(f.duration),
				timelapse.WithRecordingEnabledProvider(func(string) bool { return cameraRecordingEnabled(cam) }),
				timelapse.WithRetainIntermediateMP4(cam.Timelapse.RetainIntermediateMP4Value()),
				timelapse.WithIntermediateMP4Pruner(db),
				timelapse.WithExtractionInterval(interval),
				timelapse.WithDeleteRecordingsAfterMerge(delSources),
			)
			if delSources {
				mgr.SetSourceRecordingDeleter(deleter)
			}
		}

		for _, w := range windows {
			if ctx.Err() != nil {
				_, _ = fmt.Fprintln(stdout, "\nInterrupted — stopping. Already-merged windows are safe; re-run to resume.")
				return 1
			}
			label := fmt.Sprintf("%s → %s", w.Start.Format("2006-01-02 15:04"), w.End.Format("2006-01-02 15:04"))
			if w.End.After(time.Now()) {
				fmt.Fprintf(stdout, "  window %s: skipped (open — sources not final, deletion suppressed per #734)\n", label)
				continue
			}
			row, err := db.FindTimelapseMergeByWindow(ctx, cam.ID, w.Start, f.duration)
			if err != nil {
				fmt.Fprintf(stdout, "  window %s: lookup failed: %v\n", label, err)
				totalFailed++
				continue
			}
			if row != nil && row.Status == model.TimelapseMergeStatusCompleted {
				fmt.Fprintf(stdout, "  window %s: skipped (already merged, %d frames)\n", label, row.FrameCount)
				continue
			}
			if !f.execute {
				count, size := countWindowRecordings(ctx, db, cam.ID, w)
				fmt.Fprintf(stdout, "  window %s: would merge (%d recordings, %.1f MB)\n", label, count, float64(size)/1e6)
				continue
			}
			fmt.Fprintf(stdout, "  window %s: merging...\n", label)
			if err := mgr.Run(ctx, cam.ID, w.Start.Add(dur/2)); err != nil {
				fmt.Fprintf(stdout, "  window %s: FAILED: %v\n", label, err)
				totalFailed++
				continue
			}
			// Run() writes no row when the window has no segments.
			row, err = db.FindTimelapseMergeByWindow(ctx, cam.ID, w.Start, f.duration)
			switch {
			case err != nil || row == nil:
				fmt.Fprintf(stdout, "  window %s: no segments (empty window)\n", label)
			case row.Status == model.TimelapseMergeStatusCompleted:
				fmt.Fprintf(stdout, "  window %s: completed (%d frames, %.1f MB → %s)\n",
					label, row.FrameCount, float64(row.FileSize)/1e6, row.OutputPath)
				totalMerged++
			default:
				fmt.Fprintf(stdout, "  window %s: finished with status %q (%s)\n", label, row.Status, row.Error)
				totalFailed++
			}
		}
		_, _ = fmt.Fprintln(stdout)
	}

	if f.execute {
		fmt.Fprintf(stdout, "SUMMARY: merged=%d failed=%d (skipped windows not counted)\n", totalMerged, totalFailed)
		if totalFailed > 0 {
			return 1
		}
	} else {
		_, _ = fmt.Fprintln(stdout, "DRY RUN — no changes made. Re-run with --execute to apply.")
	}
	return 0
}

// resolveTimelapseMergeLocation mirrors the API handler's display-timezone
// resolution (Local needs special-casing — time.LoadLocation("Local") fails).
func resolveTimelapseMergeLocation(cfg *config.Config) *time.Location {
	switch {
	case cfg.Timezone == "" || cfg.Timezone == "UTC":
		return time.UTC
	case cfg.Timezone == "Local":
		return time.Local
	default:
		if l, err := time.LoadLocation(cfg.Timezone); err == nil {
			return l
		}
		return time.UTC
	}
}

// countWindowRecordings returns the video-format recording count and bytes
// inside the window (the extraction input — what would be converted).
func countWindowRecordings(ctx context.Context, db *storage.DB, cameraID string, w timelapseMergeWindow) (int, int64) {
	recs, err := db.ListRecordings(ctx, model.RecordingFilter{
		CameraID:  cameraID,
		Formats:   []model.Format{model.FormatH264, model.FormatH265, model.FormatAVI, model.FormatMJPEG},
		StartTime: w.Start,
		EndTime:   w.End,
	})
	if err != nil {
		return 0, 0
	}
	var size int64
	for _, r := range recs {
		size += r.FileSize
	}
	return len(recs), size
}
