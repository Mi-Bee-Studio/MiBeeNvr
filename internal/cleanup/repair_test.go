package cleanup

// Tests for the DB self-repair strategies (repair.go): zero-duration repair
// via the pure-Go probe + fake-ffprobe fallback, and stale MJPEG record
// marking. The ffprobe fallback is exercised with a stub shell script so the
// tests stay hermetic (no real ffprobe binary, works on any CI runner). See #565.

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/stretchr/testify/require"
)

// writeFakeFFprobe creates an executable stub that prints the given single
// line of stdout, mimicking `ffprobe -show_entries format=duration`.
func writeFakeFFprobe(t *testing.T, output string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake-ffprobe")
	require.NoError(t, os.WriteFile(path, []byte("#!/bin/sh\nprintf '%s\\n' \""+output+"\"\n"), 0o755))
	return path
}

// writeGarbageMedia writes a non-MP4 file so mediaprobe fails and the ffprobe
// fallback path is taken.
func writeGarbageMedia(t *testing.T, path string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte("definitely-not-an-mp4"), 0o644))
}

func TestProbeDuration_FFprobeFallback(t *testing.T) {
	env := newTestEnv(t)
	defer env.close(t)

	file := filepath.Join(env.dir, "garbage.mp4")
	writeGarbageMedia(t, file)

	cfg := defaultCleanupConfig()
	cm, err := NewCleanupManager(env.db, env.store, cfg)
	require.NoError(t, err)
	cm.ffprobePath = writeFakeFFprobe(t, "32.4")

	d := cm.probeDuration(context.Background(), file)
	require.InDelta(t, 32.4, d, 0.001, "ffprobe fallback should return the stub duration")
}

func TestProbeDuration_FFprobeParseFailure(t *testing.T) {
	env := newTestEnv(t)
	defer env.close(t)

	file := filepath.Join(env.dir, "garbage.mp4")
	writeGarbageMedia(t, file)

	cfg := defaultCleanupConfig()
	cm, err := NewCleanupManager(env.db, env.store, cfg)
	require.NoError(t, err)
	cm.ffprobePath = writeFakeFFprobe(t, "not-a-number")

	require.Equal(t, 0.0, cm.probeDuration(context.Background(), file), "unparseable ffprobe output must yield 0, not panic")
}

func TestProbeDuration_NoFFprobeConfigured(t *testing.T) {
	env := newTestEnv(t)
	defer env.close(t)

	file := filepath.Join(env.dir, "garbage.mp4")
	writeGarbageMedia(t, file)

	cfg := defaultCleanupConfig()
	cm, err := NewCleanupManager(env.db, env.store, cfg)
	require.NoError(t, err)

	require.Equal(t, 0.0, cm.probeDuration(context.Background(), file),
		"non-MP4 input with no ffprobe configured must return 0 (best-effort)")
}

// insertZeroDurationRecording inserts a duration=0 recording matching the
// repair candidates query (file_size>1MB, non-MJPEG, ended_at set, pending).
func insertZeroDurationRecording(t *testing.T, env *testEnv, id, filePath string, fileExists bool) {
	t.Helper()
	ctx := context.Background()
	_, err := env.db.DB().ExecContext(ctx,
		`INSERT INTO recordings(id, camera_id, file_path, format, started_at, ended_at, duration, file_size, frame_count, merge_status)
		 VALUES(?,?,?,?,?,?,0,?,?,?);`,
		id, "cam1", filePath, model.FormatH264,
		time.Now().UTC().Add(-time.Hour), time.Now().UTC(),
		2*1024*1024, 0, model.MergeStatusPending)
	require.NoError(t, err)
	if fileExists {
		writeGarbageMedia(t, filePath)
	}
}

func TestRepairZeroDuration_RepairsViaFFprobeFallback(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t)
	defer env.close(t)

	file := filepath.Join(env.dir, "repair.mp4")
	insertZeroDurationRecording(t, env, "repair-me", file, true)

	cfg := defaultCleanupConfig()
	cm, err := NewCleanupManager(env.db, env.store, cfg)
	require.NoError(t, err)
	cm.ffprobePath = writeFakeFFprobe(t, "32.4")

	cm.repairZeroDurationRecordings(ctx)

	rec, err := env.db.GetRecording(ctx, "repair-me")
	require.NoError(t, err)
	require.NotNil(t, rec)
	require.InDelta(t, 32.4, rec.Duration, 0.001, "duration should be updated from the probe result")
	expectedEnd := rec.StartedAt.Add(32400 * time.Millisecond)
	require.WithinDuration(t, expectedEnd, rec.EndedAt, 2*time.Second,
		"ended_at should be recomputed as started_at + probed duration")
}

func TestRepairZeroDuration_SkipsMissingFile(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t)
	defer env.close(t)

	insertZeroDurationRecording(t, env, "gone", filepath.Join(env.dir, "missing.mp4"), false)

	cfg := defaultCleanupConfig()
	cm, err := NewCleanupManager(env.db, env.store, cfg)
	require.NoError(t, err)
	cm.ffprobePath = writeFakeFFprobe(t, "32.4")

	cm.repairZeroDurationRecordings(ctx)

	rec, err := env.db.GetRecording(ctx, "gone")
	require.NoError(t, err)
	require.NotNil(t, rec)
	require.Equal(t, 0.0, rec.Duration, "recording with missing file must be left untouched")
}

// insertPendingMJPEG inserts an mjpeg-format pending recording; onDisk
// controls whether the backing file exists.
func insertPendingMJPEG(t *testing.T, env *testEnv, id string, onDisk bool) string {
	t.Helper()
	ctx := context.Background()
	fullPath := filepath.Join(env.store.RootDir(), "cam1", id+".mjpeg")
	_, err := env.db.DB().ExecContext(ctx,
		`INSERT INTO recordings(id, camera_id, file_path, format, started_at, ended_at, duration, file_size, frame_count, merge_status)
		 VALUES(?,?,?,?,?,?,?,?,?,?);`,
		id, "cam1", fullPath, model.FormatMJPEG,
		time.Now().UTC().Add(-time.Hour), time.Now().UTC(),
		60, 1024, 0, model.MergeStatusPending)
	require.NoError(t, err)
	if onDisk {
		require.NoError(t, os.MkdirAll(filepath.Dir(fullPath), 0o755))
		require.NoError(t, os.WriteFile(fullPath, []byte("mjpeg-data"), 0o644))
	}
	return fullPath
}

func TestStaleRecordCleanup_MarksMissingMJPEGFailed(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t)
	defer env.close(t)

	insertPendingMJPEG(t, env, "stale-mjpeg", false) // file gone → stale
	insertPendingMJPEG(t, env, "live-mjpeg", true)   // file present → untouched

	cfg := defaultCleanupConfig()
	cm, err := NewCleanupManager(env.db, env.store, cfg)
	require.NoError(t, err)

	require.Equal(t, 1, cm.fixStaleMJPEGRecords(ctx, "cam1"), "exactly the missing-file record should be fixed")

	stale, err := env.db.GetRecording(ctx, "stale-mjpeg")
	require.NoError(t, err)
	require.NotNil(t, stale)
	require.Equal(t, model.MergeStatusFailed, stale.MergeStatus, "stale MJPEG record should be marked failed")

	live, err := env.db.GetRecording(ctx, "live-mjpeg")
	require.NoError(t, err)
	require.NotNil(t, live)
	require.Equal(t, model.MergeStatusPending, live.MergeStatus, "record with file on disk must stay pending")
}
