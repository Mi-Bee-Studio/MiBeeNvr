package timelapse

import (
	"context"
	"errors"
	"testing"
	"time"
)

// cst is a fixed +08:00 zone so natural-day alignment is deterministic
// regardless of the host running the tests.
var cst = time.FixedZone("CST", 8*3600)

func mustTime(t *testing.T, layout, value string, loc *time.Location) time.Time {
	t.Helper()
	ts, err := time.ParseInLocation(layout, value, loc)
	if err != nil {
		t.Fatalf("parse %q: %v", value, err)
	}
	return ts
}

// fakeProbes records every probe call so tests can assert which windows were
// inspected. completed is keyed by window start (UnixNano).
type fakeProbes struct {
	completed map[string]bool // "2006-01-02 15:04" in loc → completed
	sources   map[string]bool // "2006-01-02 15:04" in loc → has sources

	completedCalls []string
	sourcesCalls   []string

	completedErr error
	sourcesErr   error
}

func (f *fakeProbes) key(ts time.Time) string { return ts.Format("2006-01-02 15:04") }

func (f *fakeProbes) CompletedWindowStarts(ctx context.Context, cameraID, durationLabel string, from, to time.Time) (map[int64]struct{}, error) {
	if f.completedErr != nil {
		return nil, f.completedErr
	}
	f.completedCalls = append(f.completedCalls, "called")
	out := make(map[int64]struct{})
	for start, done := range f.completed {
		if !done {
			continue
		}
		ts, err := time.ParseInLocation("2006-01-02 15:04", start, cst)
		if err != nil {
			continue
		}
		if !ts.Before(from) && !ts.After(to) {
			out[ts.UnixNano()] = struct{}{}
		}
	}
	return out, nil
}

func (f *fakeProbes) HasSources(ctx context.Context, cameraID string, start, end time.Time) (bool, error) {
	if f.sourcesErr != nil {
		return false, f.sourcesErr
	}
	f.sourcesCalls = append(f.sourcesCalls, f.key(start))
	return f.sources[f.key(start)], nil
}

// asProbes adapts the fake's methods into the function-field struct the
// production code takes.
func (f *fakeProbes) asProbes() CatchUpProbes {
	return CatchUpProbes{
		CompletedWindowStarts: f.CompletedWindowStarts,
		HasSources:            f.HasSources,
	}
}

func TestDiscoverMissedWindows_NaturalDayGap(t *testing.T) {
	// Now = 2026-10-09 12:00 CST. Daily windows. Days 10-05..10-07 completed
	// (window starts 00:00), 10-08 missed with sources, 10-09 is open (never
	// a candidate), 10-03 has sources but was never merged, 10-02 completed.
	now := mustTime(t, "2006-01-02 15:04", "2026-10-09 12:00", cst)
	probes := &fakeProbes{
		completed: map[string]bool{
			"2026-10-07 00:00": true,
			"2026-10-06 00:00": true,
			"2026-10-05 00:00": true,
			"2026-10-02 00:00": true,
		},
		sources: map[string]bool{
			"2026-10-08 00:00": true, // missed by the interruption
			"2026-10-03 00:00": true, // older gap, older than recent completed windows
			"2026-10-01 00:00": true, // within the 62-window walk, also missed
		},
	}
	got, err := DiscoverMissedWindows(context.Background(), now, 24*time.Hour, cst, "cam-1", "natural-day", probes.asProbes())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []time.Time{
		mustTime(t, "2006-01-02 15:04", "2026-10-01 00:00", cst),
		mustTime(t, "2006-01-02 15:04", "2026-10-03 00:00", cst),
		mustTime(t, "2006-01-02 15:04", "2026-10-08 00:00", cst),
	}
	if len(got) != len(want) {
		t.Fatalf("got %d windows (%v), want %d", len(got), got, len(want))
	}
	for i := range want {
		if !got[i].Equal(want[i]) {
			t.Errorf("window[%d] = %s, want %s", i, got[i], want[i])
		}
	}
}

func TestDiscoverMissedWindows_SkipsEmptyAndCompleted(t *testing.T) {
	// 10-08 completed (still has sources — delete disabled camera), 10-07
	// missed but camera offline all day (no sources).
	now := mustTime(t, "2006-01-02 15:04", "2026-10-09 03:00", cst)
	probes := &fakeProbes{
		completed: map[string]bool{"2026-10-08 00:00": true},
		sources: map[string]bool{
			"2026-10-08 00:00": true, // completed → must NOT trigger a re-run
		},
	}
	got, err := DiscoverMissedWindows(context.Background(), now, 24*time.Hour, cst, "cam-1", "natural-day", probes.asProbes())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected no missed windows, got %v", got)
	}
	// HasSources must not even be probed for the completed window.
	for _, key := range probes.sourcesCalls {
		if key == "2026-10-08 00:00" {
			t.Errorf("HasSources probed for completed window 2026-10-08")
		}
	}
}

func TestDiscoverMissedWindows_EightHourWindows(t *testing.T) {
	// 8h windows align to 00:00/08:00/16:00 local. Now = 14:00 → open window
	// [08:00,16:00). Closed candidates: [00,08), prev-day [16,24), [08,16)…
	now := mustTime(t, "2006-01-02 15:04", "2026-10-09 14:00", cst)
	probes := &fakeProbes{
		completed: map[string]bool{
			"2026-10-09 00:00": true, // most recent closed window, done
		},
		sources: map[string]bool{
			"2026-10-08 16:00": true, // killed mid-run — no completed row
			"2026-10-08 08:00": true,
		},
	}
	got, err := DiscoverMissedWindows(context.Background(), now, 8*time.Hour, cst, "cam-1", "8h", probes.asProbes())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []time.Time{
		mustTime(t, "2006-01-02 15:04", "2026-10-08 08:00", cst),
		mustTime(t, "2006-01-02 15:04", "2026-10-08 16:00", cst),
	}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if !got[i].Equal(want[i]) {
			t.Errorf("window[%d] = %s, want %s", i, got[i], want[i])
		}
	}
}

func TestDiscoverMissedWindows_WeeklyAlignment(t *testing.T) {
	// 2026-10-09 is a Friday. Weekly windows start Monday 00:00 local.
	// Current open window: Mon 2026-10-05. Closed: 2026-09-28, 09-21, ...
	now := mustTime(t, "2006-01-02 15:04", "2026-10-09 09:00", cst)
	if now.Weekday() != time.Friday {
		t.Fatalf("test premise: 2026-10-09 should be Friday, got %s", now.Weekday())
	}
	probes := &fakeProbes{
		completed: map[string]bool{"2026-09-28 00:00": true},
		sources:   map[string]bool{"2026-09-21 00:00": true},
	}
	got, err := DiscoverMissedWindows(context.Background(), now, 7*24*time.Hour, cst, "cam-1", "7d", probes.asProbes())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || !got[0].Equal(mustTime(t, "2006-01-02 15:04", "2026-09-21 00:00", cst)) {
		t.Fatalf("got %v, want [2026-09-21 00:00]", got)
	}
}

func TestDiscoverMissedWindows_MonthlyAlignment(t *testing.T) {
	// 30d windows are calendar months. Now = 2026-10-09 → open window is
	// October. Closed: September, August, ... The HasSources probe must
	// receive the TRUE month end (e.g. Sept 30 for the September window), not
	// start+30d — this is the regression anchor for calendar windows.
	now := mustTime(t, "2006-01-02 15:04", "2026-10-09 09:00", cst)
	var sawEnd time.Time
	probes := CatchUpProbes{
		CompletedWindowStarts: func(ctx context.Context, cameraID, durationLabel string, from, to time.Time) (map[int64]struct{}, error) {
			return map[int64]struct{}{}, nil
		},
		HasSources: func(ctx context.Context, cameraID string, start, end time.Time) (bool, error) {
			if start.Equal(mustTime(t, "2006-01-02 15:04", "2026-09-01 00:00", cst)) {
				sawEnd = end
			}
			return false, nil
		},
	}
	if _, err := DiscoverMissedWindows(context.Background(), now, 30*24*time.Hour, cst, "cam-1", "30d", probes); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	wantEnd := mustTime(t, "2006-01-02 15:04", "2026-10-01 00:00", cst)
	if !sawEnd.Equal(wantEnd) {
		t.Fatalf("September window end = %s, want %s (calendar month, not start+30d)", sawEnd, wantEnd)
	}
}

func TestDiscoverMissedWindows_WindowCap(t *testing.T) {
	// Every window in reach has sources and none completed: the walk stops at
	// maxCatchUpWindows and reports exactly that many.
	now := mustTime(t, "2006-01-02 15:04", "2026-10-09 12:00", cst)
	probes := CatchUpProbes{
		CompletedWindowStarts: func(ctx context.Context, cameraID, durationLabel string, from, to time.Time) (map[int64]struct{}, error) {
			return map[int64]struct{}{}, nil
		},
		HasSources: func(ctx context.Context, cameraID string, start, end time.Time) (bool, error) {
			return true, nil
		},
	}
	got, err := DiscoverMissedWindows(context.Background(), now, 24*time.Hour, cst, "cam-1", "natural-day", probes)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != maxCatchUpWindows {
		t.Fatalf("got %d windows, want cap %d", len(got), maxCatchUpWindows)
	}
	// Oldest first, newest = yesterday.
	wantNewest := mustTime(t, "2006-01-02 15:04", "2026-10-08 00:00", cst)
	if !got[len(got)-1].Equal(wantNewest) {
		t.Fatalf("newest window = %s, want %s", got[len(got)-1], wantNewest)
	}
}

func TestDiscoverMissedWindows_ProbeErrorsPropagate(t *testing.T) {
	now := mustTime(t, "2006-01-02 15:04", "2026-10-09 12:00", cst)
	probes := &fakeProbes{completedErr: errors.New("boom")}
	if _, err := DiscoverMissedWindows(context.Background(), now, 24*time.Hour, cst, "cam-1", "natural-day", probes.asProbes()); err == nil {
		t.Fatal("expected completed-probe error to propagate")
	}
	probes2 := &fakeProbes{sourcesErr: errors.New("boom")}
	if _, err := DiscoverMissedWindows(context.Background(), now, 24*time.Hour, cst, "cam-1", "natural-day", probes2.asProbes()); err == nil {
		t.Fatal("expected sources-probe error to propagate")
	}
}

func TestRunCatchUp_RunsOldestFirstWithInsideWindowRefTime(t *testing.T) {
	now := mustTime(t, "2006-01-02 15:04", "2026-10-09 12:00", cst)
	probes := &fakeProbes{
		completed: map[string]bool{},
		sources: map[string]bool{
			"2026-10-08 00:00": true,
			"2026-10-07 00:00": true,
		},
	}
	var refs []time.Time
	RunCatchUp(context.Background(), now, cst,
		[]CatchUpCamera{{CameraID: "cam-1", Duration: 24 * time.Hour, DurationLabel: "natural-day"}},
		probes.asProbes(), func(ctx context.Context, cameraID string, refTime time.Time) error {
			refs = append(refs, refTime)
			return nil
		})
	if len(refs) != 2 {
		t.Fatalf("got %d runs, want 2", len(refs))
	}
	// refTime must be INSIDE its window: parseMergeRange(ref) == window start.
	winA := mustTime(t, "2006-01-02 15:04", "2026-10-07 00:00", cst)
	winB := mustTime(t, "2006-01-02 15:04", "2026-10-08 00:00", cst)
	startA, _ := parseMergeRange(refs[0], 24*time.Hour, cst)
	startB, _ := parseMergeRange(refs[1], 24*time.Hour, cst)
	if !startA.Equal(winA) || !startB.Equal(winB) {
		t.Fatalf("refTimes %v/%v map to %s/%s, want windows %s/%s",
			refs[0], refs[1], startA, startB, winA, winB)
	}
}
