package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/timelapse"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Flag parsing
// ---------------------------------------------------------------------------

func TestParseTimelapseMergeFlags_Defaults(t *testing.T) {
	t.Helper()
	f, code := parseTimelapseMergeFlags([]string{"mibee-nvr", "timelapse-merge", "--camera", "cam-1", "--start", "2026-09-04"})
	require.Equal(t, -1, code, "expected proceed code -1")
	require.False(t, f.execute, "dry-run must be the default")
	require.Equal(t, "natural-day", f.duration)
	require.Equal(t, "mibee-nvr.yaml", f.cfgPath)
	require.Empty(t, f.interval, "no interval override by default")
	require.Equal(t, 0, f.fps, "no fps override by default")
	require.Nil(t, f.deleteSrc, "delete-sources tri-state default = follow camera config")
	require.False(t, f.force)
}

func TestParseTimelapseMergeFlags_FullSet(t *testing.T) {
	t.Helper()
	argv := []string{
		"mibee-nvr", "timelapse-merge",
		"--camera", "cam-1,cam-2",
		"--start", "2026-08-26",
		"--end", "2026-09-10",
		"--duration", "8h",
		"--interval", "1s",
		"--fps", "15",
		"--delete-sources",
		"--execute",
		"--force",
		"--config", "/tmp/x.yaml",
	}
	f, code := parseTimelapseMergeFlags(argv)
	require.Equal(t, -1, code)
	require.Equal(t, "cam-1,cam-2", f.camerasArg)
	require.Equal(t, "2026-08-26", f.start)
	require.Equal(t, "2026-09-10", f.end)
	require.Equal(t, "8h", f.duration)
	require.Equal(t, "1s", f.interval)
	require.Equal(t, 15, f.fps)
	require.True(t, *f.deleteSrc)
	require.True(t, f.execute)
	require.True(t, f.force)
	require.Equal(t, "/tmp/x.yaml", f.cfgPath)
}

func TestParseTimelapseMergeFlags_EqualsFormAndOverrides(t *testing.T) {
	t.Helper()
	// --flag=value form, and --no-delete-sources overriding an earlier --delete-sources.
	f, code := parseTimelapseMergeFlags([]string{
		"mibee-nvr", "timelapse-merge",
		"--camera=all", "--encoding=jpeg", "--start=2026-09-01",
		"--interval=500ms", "--delete-sources", "--no-delete-sources",
	})
	require.Equal(t, -1, code)
	require.Equal(t, "all", f.camerasArg)
	require.Equal(t, "jpeg", f.encoding)
	require.Equal(t, "500ms", f.interval)
	require.NotNil(t, f.deleteSrc)
	require.False(t, *f.deleteSrc)
}

func TestParseTimelapseMergeFlags_Errors(t *testing.T) {
	t.Helper()
	// Unknown flag → error exit code 1.
	_, code := parseTimelapseMergeFlags([]string{"mibee-nvr", "timelapse-merge", "--camera", "a", "--start", "2026-09-01", "--bogus"})
	require.Equal(t, 1, code)
	// Bad fps → error.
	_, code = parseTimelapseMergeFlags([]string{"mibee-nvr", "timelapse-merge", "--camera", "a", "--start", "2026-09-01", "--fps", "abc"})
	require.Equal(t, 1, code)
	// Bad interval → error.
	_, code = parseTimelapseMergeFlags([]string{"mibee-nvr", "timelapse-merge", "--camera", "a", "--start", "2026-09-01", "--interval", "fast"})
	require.Equal(t, 1, code)
	// Non-positive interval → error.
	_, code = parseTimelapseMergeFlags([]string{"mibee-nvr", "timelapse-merge", "--camera", "a", "--start", "2026-09-01", "--interval", "0s"})
	require.Equal(t, 1, code)
	// Help → exit code 0.
	_, code = parseTimelapseMergeFlags([]string{"mibee-nvr", "timelapse-merge", "--help"})
	require.Equal(t, 0, code)
}

// ---------------------------------------------------------------------------
// Camera resolution
// ---------------------------------------------------------------------------

func TestResolveTimelapseMergeCameras(t *testing.T) {
	t.Helper()
	cfg := &config.Config{Cameras: []config.CameraConfig{
		{ID: "cam-jpeg-a", Name: "a", Encoding: "jpeg"},
		{ID: "cam-jpeg-b", Name: "b", Encoding: "jpeg"},
		{ID: "cam-h265", Name: "c", Encoding: "h265"},
	}}

	cams, err := resolveTimelapseMergeCameras(cfg, "cam-jpeg-a,cam-h265", "")
	require.NoError(t, err)
	require.Len(t, cams, 2)
	require.Equal(t, "cam-jpeg-a", cams[0].ID)
	require.Equal(t, "cam-h265", cams[1].ID)

	// "all" without filter → every camera.
	cams, err = resolveTimelapseMergeCameras(cfg, "all", "")
	require.NoError(t, err)
	require.Len(t, cams, 3)

	// "all" + encoding filter.
	cams, err = resolveTimelapseMergeCameras(cfg, "all", "jpeg")
	require.NoError(t, err)
	require.Len(t, cams, 2)
	for _, c := range cams {
		require.Equal(t, "jpeg", c.Encoding)
	}

	// Unknown explicit ID → error.
	_, err = resolveTimelapseMergeCameras(cfg, "cam-nope", "")
	require.Error(t, err)

	// Empty arg → error.
	_, err = resolveTimelapseMergeCameras(cfg, "", "")
	require.Error(t, err)
}

// ---------------------------------------------------------------------------
// Window enumeration
// ---------------------------------------------------------------------------

func TestEnumerateMergeWindows(t *testing.T) {
	t.Helper()
	loc := time.UTC
	start := time.Date(2026, 9, 4, 0, 0, 0, 0, loc)
	end := time.Date(2026, 9, 6, 0, 0, 0, 0, loc)

	// natural-day: 09-04..09-06 inclusive → 3 windows of 24h.
	ws := enumerateMergeWindows(start, end, 24*time.Hour, loc)
	require.Len(t, ws, 3)
	require.Equal(t, start, ws[0].Start)
	require.Equal(t, start.Add(24*time.Hour), ws[0].End)
	require.Equal(t, end, ws[2].Start)

	// 8h: one day → 3 windows (00:00, 08:00, 16:00).
	day := time.Date(2026, 9, 4, 0, 0, 0, 0, loc)
	ws = enumerateMergeWindows(day, day, 8*time.Hour, loc)
	require.Len(t, ws, 3)
	require.Equal(t, day, ws[0].Start)
	require.Equal(t, day.Add(8*time.Hour), ws[1].Start)
	require.Equal(t, day.Add(16*time.Hour), ws[2].Start)
	require.Equal(t, day.Add(24*time.Hour), ws[2].End)

	// 8h across two days → 6 windows.
	ws = enumerateMergeWindows(day, day.AddDate(0, 0, 1), 8*time.Hour, loc)
	require.Len(t, ws, 6)
}

// ---------------------------------------------------------------------------
// Effective per-camera parameters
// ---------------------------------------------------------------------------

func TestEffectiveTimelapseMergeParams(t *testing.T) {
	t.Helper()
	interval1s := "1s"
	cam := config.CameraConfig{
		ID:       "cam-x",
		Encoding: "jpeg",
		Timelapse: &config.CameraTimelapseConfig{
			Interval:                   "5s",
			MergeOutputFPS:             12,
			DeleteRecordingsAfterMerge: true,
		},
	}

	// Overrides win.
	d, err := effectiveTimelapseMergeInterval(cam, interval1s)
	require.NoError(t, err)
	require.Equal(t, time.Second, d)
	require.Equal(t, 15, effectiveTimelapseMergeFPS(cam, 15))
	require.False(t, effectiveTimelapseMergeDeleteSources(cam, tlmBoolPtr(false)))

	// Without overrides: camera config values.
	d, err = effectiveTimelapseMergeInterval(cam, "")
	require.NoError(t, err)
	require.Equal(t, 5*time.Second, d)
	require.Equal(t, 12, effectiveTimelapseMergeFPS(cam, 0))
	require.True(t, effectiveTimelapseMergeDeleteSources(cam, nil))

	// Camera without timelapse config: 30s default, fps 10 default, no delete.
	bare := config.CameraConfig{ID: "cam-bare"}
	d, err = effectiveTimelapseMergeInterval(bare, "")
	require.NoError(t, err)
	require.Equal(t, 30*time.Second, d)
	require.Equal(t, 10, effectiveTimelapseMergeFPS(bare, 0))
	require.False(t, effectiveTimelapseMergeDeleteSources(bare, nil))

	// Invalid camera-config interval falls back to the default (config is
	// validated at server startup; the CLI must not hard-fail on it).
	bad := config.CameraConfig{ID: "cam-bad", Timelapse: &config.CameraTimelapseConfig{Interval: "oops"}}
	d, err = effectiveTimelapseMergeInterval(bad, "")
	require.NoError(t, err)
	require.Equal(t, 30*time.Second, d)
}

// ---------------------------------------------------------------------------
// End-to-end: MJPEG recordings → timelapse merge with 1s sampling + delete
// ---------------------------------------------------------------------------

// writeTLMTestConfig writes a minimal CLI config pointing at rootDir.
func writeTLMTestConfig(t *testing.T, path, rootDir string, cams ...config.CameraConfig) {
	t.Helper()
	cfg := config.Config{
		Server:   config.ServerConfig{Listen: ":9090"},
		Storage:  config.StorageConfig{RootDir: rootDir},
		Cleanup:  config.CleanupConfig{RetentionDays: 30, CheckInterval: "1h"},
		Timezone: "Local", // matches parseMJPEGFrameTime (frame filenames are local wall clock)
		Cameras:  cams,
	}
	require.NoError(t, config.Save(path, &cfg))
}

// makeTLMJPEGFrames writes n timestamped JPEG frames into dir, every step
// starting from start (local wall clock — the storage layer's naming).
func makeTLMJPEGFrames(t *testing.T, dir string, start time.Time, n int, step time.Duration) {
	t.Helper()
	require.NoError(t, os.MkdirAll(dir, 0o755))
	for i := range n {
		name := start.Add(time.Duration(i)*step).Format("20060102_150405.000") + ".jpg"
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte("jpeg-dummy-frame-content"), 0o644))
	}
}

func insertTLMRecording(t *testing.T, db *storage.DB, id, camID, path string, start, end time.Time) {
	t.Helper()
	require.NoError(t, db.InsertRecording(context.Background(), &model.Recording{
		ID:        id,
		CameraID:  camID,
		FilePath:  path,
		Format:    model.FormatMJPEG,
		StartedAt: start,
		EndedAt:   end,
		Duration:  end.Sub(start).Seconds(),
		FileSize:  4096,
	}))
}

// closedLocalDay returns the local-calendar midnight of N days ago — a window
// guaranteed to be closed regardless of when the test runs.
func closedLocalDay(t *testing.T, daysAgo int) time.Time {
	t.Helper()
	d := time.Now().In(time.Local).AddDate(0, 0, -daysAgo)
	return time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, time.Local)
}

func TestRunTimelapseMerge_MJPEGExecuteWithDelete(t *testing.T) {
	t.Helper()
	root := t.TempDir()
	cfgPath := filepath.Join(root, "mibee-nvr.yaml")
	writeTLMTestConfig(t, cfgPath, root, config.CameraConfig{
		ID: "cam-jpeg", Name: "jpeg cam", Protocol: "onvif", Encoding: "jpeg",
		RecordingEnabled: tlmBoolPtr(true),
	})

	db, err := storage.New(filepath.Join(root, "mibee-nvr.db"))
	require.NoError(t, err)
	require.NoError(t, db.Init(context.Background()))
	t.Cleanup(func() { db.Close() })

	dayStart := closedLocalDay(t, 2)
	dayStr := dayStart.Format("2006-01-02")

	// Two MJPEG recordings inside the window: at 00:00 and 01:00 local,
	// each 60s of frames at 2fps (every 500ms) → 120 frames per recording.
	dir1 := filepath.Join(root, "cam-jpeg", "rec-1")
	dir2 := filepath.Join(root, "cam-jpeg", "rec-2")
	rec2Start := dayStart.Add(time.Hour)
	makeTLMJPEGFrames(t, dir1, dayStart, 120, 500*time.Millisecond)
	makeTLMJPEGFrames(t, dir2, rec2Start, 120, 500*time.Millisecond)
	insertTLMRecording(t, db, "rec-1", "cam-jpeg", dir1, dayStart, dayStart.Add(60*time.Second))
	insertTLMRecording(t, db, "rec-2", "cam-jpeg", dir2, rec2Start, rec2Start.Add(60*time.Second))

	var out bytes.Buffer
	code := runTimelapseMerge(timelapseMergeFlags{
		cfgPath:    cfgPath,
		camerasArg: "cam-jpeg",
		start:      dayStr,
		end:        dayStr,
		interval:   "1s",
		deleteSrc:  tlmBoolPtr(true),
		execute:    true,
	}, &out, false /* serverRunning */)
	require.Equal(t, 0, code, "output:\n%s", out.String())

	// The merge row exists, completed, with ~1 frame per sampled second:
	// 2 recordings × 60 samples each = 120 frames.
	row, err := db.FindTimelapseMergeByWindow(context.Background(), "cam-jpeg", dayStart, "natural-day")
	require.NoError(t, err)
	require.NotNil(t, row, "expected a timelapse_merges row; CLI output:\n%s", out.String())
	require.Equal(t, model.TimelapseMergeStatusCompleted, row.Status)
	require.Equal(t, 120, row.FrameCount)
	require.Equal(t, model.TimelapseMergeCodecMJPEG, row.Codec)
	require.Greater(t, row.FileSize, int64(0))

	// Output MP4 exists and is non-empty.
	outPath := filepath.Join(root, "periodic-merge", "cam-jpeg",
		"periodic_"+dayStart.Format("2006-01-02_150405")+".mp4")
	st, err := os.Stat(outPath)
	require.NoError(t, err, "output mp4 must exist")
	require.Greater(t, st.Size(), int64(0))

	// Source recordings were deleted (DB rows + dirs on disk).
	recs, err := db.ListRecordings(context.Background(), model.RecordingFilter{CameraID: "cam-jpeg"})
	require.NoError(t, err)
	require.Empty(t, recs, "source recordings must be deleted after merge")
	_, err = os.Stat(dir1)
	require.True(t, os.IsNotExist(err), "source dir 1 must be removed")
	_, err = os.Stat(dir2)
	require.True(t, os.IsNotExist(err), "source dir 2 must be removed")

	// Re-run is idempotent: the window is skipped as already merged.
	var out2 bytes.Buffer
	code = runTimelapseMerge(timelapseMergeFlags{
		cfgPath: cfgPath, camerasArg: "cam-jpeg", start: dayStr, end: dayStr,
		interval: "1s", deleteSrc: tlmBoolPtr(true), execute: true,
	}, &out2, false)
	require.Equal(t, 0, code, "output:\n%s", out2.String())
	require.Contains(t, out2.String(), "already", "re-run should report the window as already merged")

	row2, err := db.FindTimelapseMergeByWindow(context.Background(), "cam-jpeg", dayStart, "natural-day")
	require.NoError(t, err)
	require.NotNil(t, row2)
	require.Equal(t, 120, row2.FrameCount, "re-run must not re-merge the window")
}

func TestRunTimelapseMerge_DryRunMakesNoChanges(t *testing.T) {
	t.Helper()
	root := t.TempDir()
	cfgPath := filepath.Join(root, "mibee-nvr.yaml")
	writeTLMTestConfig(t, cfgPath, root, config.CameraConfig{
		ID: "cam-jpeg", Name: "jpeg cam", Protocol: "onvif", Encoding: "jpeg",
		RecordingEnabled: tlmBoolPtr(true),
	})

	db, err := storage.New(filepath.Join(root, "mibee-nvr.db"))
	require.NoError(t, err)
	require.NoError(t, db.Init(context.Background()))
	t.Cleanup(func() { db.Close() })

	dayStart := closedLocalDay(t, 3)
	dayStr := dayStart.Format("2006-01-02")
	dir := filepath.Join(root, "cam-jpeg", "rec-1")
	makeTLMJPEGFrames(t, dir, dayStart, 10, time.Second)
	insertTLMRecording(t, db, "rec-1", "cam-jpeg", dir, dayStart, dayStart.Add(10*time.Second))

	var out bytes.Buffer
	code := runTimelapseMerge(timelapseMergeFlags{
		cfgPath: cfgPath, camerasArg: "cam-jpeg", start: dayStr, end: dayStr,
	}, &out, false)
	require.Equal(t, 0, code, "output:\n%s", out.String())
	require.Contains(t, out.String(), "DRY RUN")

	// No merge row, recordings untouched, dir still there.
	row, err := db.FindTimelapseMergeByWindow(context.Background(), "cam-jpeg", dayStart, "natural-day")
	require.NoError(t, err)
	require.Nil(t, row)
	recs, err := db.ListRecordings(context.Background(), model.RecordingFilter{CameraID: "cam-jpeg"})
	require.NoError(t, err)
	require.Len(t, recs, 1)
	_, err = os.Stat(dir)
	require.NoError(t, err)
}

func TestRunTimelapseMerge_SkipsOpenWindows(t *testing.T) {
	t.Helper()
	root := t.TempDir()
	cfgPath := filepath.Join(root, "mibee-nvr.yaml")
	writeTLMTestConfig(t, cfgPath, root, config.CameraConfig{
		ID: "cam-jpeg", Name: "jpeg cam", Protocol: "onvif", Encoding: "jpeg",
		RecordingEnabled: tlmBoolPtr(true),
	})

	// A future date: every window is open → all skipped.
	tomorrow := time.Now().In(time.Local).AddDate(0, 0, 1)
	dayStr := tomorrow.Format("2006-01-02")

	var out bytes.Buffer
	code := runTimelapseMerge(timelapseMergeFlags{
		cfgPath: cfgPath, camerasArg: "cam-jpeg", start: dayStr, end: dayStr, execute: true,
	}, &out, false)
	require.Equal(t, 0, code, "output:\n%s", out.String())
	require.Contains(t, strings.ToLower(out.String()), "open")
}

func TestRunTimelapseMerge_RefusesEnabledCameraWhileServerRunning(t *testing.T) {
	t.Helper()
	root := t.TempDir()
	cfgPath := filepath.Join(root, "mibee-nvr.yaml")
	writeTLMTestConfig(t, cfgPath, root, config.CameraConfig{
		ID: "cam-en", Name: "enabled cam", Protocol: "onvif", Encoding: "jpeg",
		RecordingEnabled: tlmBoolPtr(true),
		Timelapse:        &config.CameraTimelapseConfig{Enabled: true, Interval: "1s"},
	})

	db, err := storage.New(filepath.Join(root, "mibee-nvr.db"))
	require.NoError(t, err)
	require.NoError(t, db.Init(context.Background()))
	t.Cleanup(func() { db.Close() })

	dayStart := closedLocalDay(t, 2)
	dayStr := dayStart.Format("2006-01-02")
	dir := filepath.Join(root, "cam-en", "rec-1")
	makeTLMJPEGFrames(t, dir, dayStart, 10, time.Second)
	insertTLMRecording(t, db, "rec-1", "cam-en", dir, dayStart, dayStart.Add(10*time.Second))

	// Server running + timelapse-enabled camera + no --force → refuse and
	// exit non-zero (nothing processed).
	var out bytes.Buffer
	code := runTimelapseMerge(timelapseMergeFlags{
		cfgPath: cfgPath, camerasArg: "cam-en", start: dayStr, end: dayStr, execute: true,
	}, &out, true /* serverRunning */)
	require.Equal(t, 1, code, "output:\n%s", out.String())
	require.Contains(t, strings.ToLower(out.String()), "timelapse-enabled")

	// Nothing merged.
	row, err := db.FindTimelapseMergeByWindow(context.Background(), "cam-en", dayStart, "natural-day")
	require.NoError(t, err)
	require.Nil(t, row)

	// Dry-run bypasses the refusal gate (planning makes no changes).
	var outDry bytes.Buffer
	code = runTimelapseMerge(timelapseMergeFlags{
		cfgPath: cfgPath, camerasArg: "cam-en", start: dayStr, end: dayStr,
	}, &outDry, true /* serverRunning */)
	require.Equal(t, 0, code, "output:\n%s", outDry.String())
	require.Contains(t, outDry.String(), "DRY RUN")
	require.Contains(t, outDry.String(), "would merge")

	// With --force it proceeds and merges (offline-style access).
	var out2 bytes.Buffer
	code = runTimelapseMerge(timelapseMergeFlags{
		cfgPath: cfgPath, camerasArg: "cam-en", start: dayStr, end: dayStr,
		execute: true, force: true,
	}, &out2, true /* serverRunning */)
	require.Equal(t, 0, code, "output:\n%s", out2.String())
	row, err = db.FindTimelapseMergeByWindow(context.Background(), "cam-en", dayStart, "natural-day")
	require.NoError(t, err)
	require.NotNil(t, row)
	require.Equal(t, model.TimelapseMergeStatusCompleted, row.Status)
}

func TestRunTimelapseMerge_EncodingFilterDryRun(t *testing.T) {
	t.Helper()
	root := t.TempDir()
	cfgPath := filepath.Join(root, "mibee-nvr.yaml")
	writeTLMTestConfig(t, cfgPath, root,
		config.CameraConfig{ID: "cam-jpeg", Name: "j", Protocol: "onvif", Encoding: "jpeg"},
		config.CameraConfig{ID: "cam-h265", Name: "h", Protocol: "onvif", Encoding: "h265"},
	)

	dayStart := closedLocalDay(t, 5)
	dayStr := dayStart.Format("2006-01-02")
	var out bytes.Buffer
	code := runTimelapseMerge(timelapseMergeFlags{
		cfgPath: cfgPath, camerasArg: "all", encoding: "jpeg", start: dayStr, end: dayStr,
	}, &out, false)
	require.Equal(t, 0, code, "output:\n%s", out.String())
	require.Contains(t, out.String(), "cam-jpeg")
	require.NotContains(t, out.String(), "cam-h265")
}

// Sanity-check the exported window helper used for enumeration.
func TestTimelapseMergeWindowFor(t *testing.T) {
	t.Helper()
	loc := time.UTC
	ref := time.Date(2026, 9, 5, 15, 42, 0, 0, loc)
	start, end := timelapse.MergeWindowFor(ref, 24*time.Hour, loc)
	require.Equal(t, time.Date(2026, 9, 5, 0, 0, 0, 0, loc), start)
	require.Equal(t, time.Date(2026, 9, 6, 0, 0, 0, 0, loc), end)

	s, e := timelapse.MergeWindowFor(ref, 8*time.Hour, loc)
	require.Equal(t, time.Date(2026, 9, 5, 8, 0, 0, 0, loc), s)
	require.Equal(t, time.Date(2026, 9, 5, 16, 0, 0, 0, loc), e)
}

func TestParseTimelapseMergeFlags_ThrottleFlags(t *testing.T) {
	t.Helper()
	// Defaults: self-throttle on, delete pacing at the built-in 200ms.
	f, code := parseTimelapseMergeFlags([]string{"mibee-nvr", "timelapse-merge", "--camera", "cam-1", "--start", "2026-09-04"})
	require.Equal(t, -1, code)
	require.False(t, f.noThrottle, "self-throttle is on by default")
	require.Empty(t, f.delThrottle, "delete-throttle default = built-in 200ms")

	// Explicit overrides.
	f, code = parseTimelapseMergeFlags([]string{"mibee-nvr", "timelapse-merge", "--camera", "cam-1", "--start", "2026-09-04", "--no-throttle", "--delete-throttle", "500ms"})
	require.Equal(t, -1, code)
	require.True(t, f.noThrottle)
	require.Equal(t, "500ms", f.delThrottle)

	// "0" disables pacing.
	f, code = parseTimelapseMergeFlags([]string{"mibee-nvr", "timelapse-merge", "--camera", "cam-1", "--start", "2026-09-04", "--delete-throttle", "0"})
	require.Equal(t, -1, code)
	require.Equal(t, "0", f.delThrottle)

	// Invalid durations are rejected.
	_, code = parseTimelapseMergeFlags([]string{"mibee-nvr", "timelapse-merge", "--camera", "cam-1", "--start", "2026-09-04", "--delete-throttle", "fast"})
	require.Equal(t, 1, code, "invalid --delete-throttle must exit 1")
}
