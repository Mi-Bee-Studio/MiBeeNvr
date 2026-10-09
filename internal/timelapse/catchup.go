package timelapse

import (
	"context"
	"log/slog"
	"sort"
	"time"
)

// Restart catch-up for interrupted merge windows.
//
// The merge scheduler only ever computes the NEXT aligned boundary for each
// camera. When the NVR stops before a window's merge ran — or kills one
// mid-run (deploy restart, crash, power loss) — that window is skipped
// forever: no timelapse product is built, and with delete_recordings_after_merge
// enabled the source segments are never deleted either (a service restart
// landing on the nightly merge window left cameras accumulating thousands of
// undeleted 30s segments per day until someone noticed).
//
// RunCatchUp closes that gap: on startup it walks closed windows backwards
// and re-runs every one that still has source recordings but no completed
// timelapse_merges row. Windows legitimately without data are skipped,
// completed windows are skipped, and windows holding only failed rows are
// retried (the re-run completes the existing row in place).

// maxCatchUpWindows bounds the backwards walk per camera per boot. 62
// windows ≈ two months of natural-day merges; beyond that, retention has
// almost certainly reclaimed the sources anyway. Hitting the cap with missed
// windows present is logged, not silently truncated.
const maxCatchUpWindows = 62

// CatchUpProbes supplies the per-window facts the discovery walk needs.
// Production wiring adapts *storage.DB; tests inject fakes.
type CatchUpProbes struct {
	// CompletedWindowStarts returns the starts of windows (as UnixNano) that
	// have a COMPLETED timelapse_merges row for cameraID with durationLabel,
	// with window_start within [from, to]. Rows in pending/merging/failed
	// states are NOT included — those windows are retried.
	CompletedWindowStarts func(ctx context.Context, cameraID, durationLabel string, from, to time.Time) (map[int64]struct{}, error)

	// HasSources reports whether the camera has any recording overlapping
	// [start, end) — parity with what a merge run would consume.
	HasSources func(ctx context.Context, cameraID string, start, end time.Time) (bool, error)
}

// CatchUpCamera is one camera's catch-up contract: the merge window duration
// and the duration_label its timelapse_merges rows carry.
type CatchUpCamera struct {
	CameraID      string
	Duration      time.Duration
	DurationLabel string
}

// DiscoverMissedWindows walks closed merge windows backwards from now (at
// most maxCatchUpWindows of them) and returns the starts of windows that have
// source recordings but no completed merge row, oldest first.
//
// Window alignment mirrors parseMergeRange exactly (calendar month for 30d,
// Monday for 7d, integer-hour divisors of 24, wall-clock otherwise), so the
// starts returned here match what manager.Run computes from a refTime inside
// the window.
func DiscoverMissedWindows(ctx context.Context, now time.Time, dur time.Duration, loc *time.Location, cameraID, durationLabel string, probes CatchUpProbes) ([]time.Time, error) {
	if probes.CompletedWindowStarts == nil || probes.HasSources == nil || dur <= 0 {
		return nil, nil
	}
	now = now.In(loc)

	// Enumerate the closed windows, newest first. Keep each window's true end
	// from parseMergeRange — calendar-month (30d) windows are not start+dur.
	firstStart, _ := parseMergeRange(now, dur, loc) // the currently OPEN window — never a candidate
	type window struct {
		start, end time.Time
	}
	var windows []window
	walk := firstStart
	for range maxCatchUpWindows {
		start, end := parseMergeRange(walk.Add(-time.Nanosecond), dur, loc)
		if !end.Equal(walk) {
			// Alignment invariant broken (should be impossible): stop rather
			// than walk garbage windows.
			break
		}
		windows = append(windows, window{start: start, end: end})
		walk = start
	}
	if len(windows) == 0 {
		return nil, nil
	}

	completed, err := probes.CompletedWindowStarts(ctx, cameraID, durationLabel, windows[len(windows)-1].start, firstStart)
	if err != nil {
		return nil, err
	}

	var missed []time.Time
	for _, w := range windows {
		if _, done := completed[w.start.UnixNano()]; done {
			continue
		}
		has, err := probes.HasSources(ctx, cameraID, w.start, w.end)
		if err != nil {
			return nil, err
		}
		if has {
			missed = append(missed, w.start)
		}
	}
	// Chronological order so re-runs fold sources the same way the scheduled
	// path would have.
	sort.Slice(missed, func(i, j int) bool { return missed[i].Before(missed[j]) })
	return missed, nil
}

// RunCatchUp discovers and re-runs missed windows for each camera. Windows run
// oldest-first, cameras sequentially — one merge at a time, mirroring the
// scheduled path's per-camera serialization so a large backlog cannot stampede
// the disk. The refTime passed to run is start+duration/2, safely inside the
// window (parseMergeRange maps any contained instant to that window).
//
// Errors are logged and the walk continues; a cancelled context stops between
// windows (an in-flight run observes it via its own ctx).
func RunCatchUp(ctx context.Context, now time.Time, loc *time.Location, cams []CatchUpCamera, probes CatchUpProbes, run func(ctx context.Context, cameraID string, refTime time.Time) error) {
	for _, cam := range cams {
		if ctx.Err() != nil {
			return
		}
		missed, err := DiscoverMissedWindows(ctx, now, cam.Duration, loc, cam.CameraID, cam.DurationLabel, probes)
		if err != nil {
			slog.Error("merge catch-up: discovery failed",
				"camera_id", cam.CameraID, "error", err)
			continue
		}
		if len(missed) == 0 {
			continue
		}
		slog.Info("merge catch-up: windows missed while the service was down",
			"camera_id", cam.CameraID,
			"count", len(missed),
			"oldest", missed[0].Format(time.RFC3339),
			"newest", missed[len(missed)-1].Format(time.RFC3339))
		if len(missed) == maxCatchUpWindows {
			slog.Warn("merge catch-up: window cap reached, older windows (if any) are not retried",
				"camera_id", cam.CameraID, "cap", maxCatchUpWindows)
		}
		for _, start := range missed {
			if ctx.Err() != nil {
				return
			}
			ref := start.Add(cam.Duration / 2)
			if err := run(ctx, cam.CameraID, ref); err != nil {
				slog.Error("merge catch-up: window merge failed",
					"camera_id", cam.CameraID,
					"window_start", start.Format(time.RFC3339),
					"error", err)
			}
		}
	}
}
