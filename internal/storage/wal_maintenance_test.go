package storage

// Tests for #752 — planned WAL checkpointing:
//   - journal_size_limit caps the WAL high-water mark (bounds crash-recovery
//     replay; the DSN baseline is intentionally unchanged — the limit rides
//     Init() on the writer, the only connection that checkpoints)
//   - an idle-window PASSIVE ticker decoupled from the hourly cleanup tail,
//     gated by WAL size AND a minimum interval between triggers
//
// Note (verified empirically, TestClose_CheckpointsWALViaLastConnClose):
// DB.Close() already checkpoints and removes the -wal file through SQLite's
// last-connection-close semantics, so no explicit shutdown TRUNCATE is
// needed — crash exits are bounded by journal_size_limit instead.

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/stretchr/testify/require"
)

func TestClose_CheckpointsWALViaLastConnClose(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "close-probe.db")
	db, err := New(dbPath)
	require.NoError(t, err)
	require.NoError(t, db.Init(context.Background()))

	ctx := context.Background()
	now := time.Now().UTC()
	for i := range 50 {
		r := &model.Recording{
			ID:          walTestID(i),
			CameraID:    "cam-probe",
			FilePath:    "/tmp/x.mp4",
			Format:      model.FormatH264,
			StartedAt:   now.Add(-time.Duration(i) * time.Minute),
			EndedAt:     now,
			Duration:    30,
			FileSize:    1024,
			MergeStatus: model.MergeStatusPending,
		}
		require.NoError(t, db.InsertRecording(ctx, r))
	}
	walBefore, err := db.GetWALSize()
	require.NoError(t, err)
	require.Positive(t, walBefore, "50 committed inserts must leave WAL frames")

	require.NoError(t, db.Close())
	_, statErr := os.Stat(dbPath + "-wal")
	require.Error(t, statErr, "last-conn close must checkpoint and remove the -wal file")
}

func TestInit_SetsJournalSizeLimit(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "jsl.db")
	db, err := New(dbPath)
	require.NoError(t, err)
	require.NoError(t, db.Init(context.Background()))
	defer db.Close()

	var limit int64
	require.NoError(t, db.DB().QueryRowContext(context.Background(),
		"PRAGMA journal_size_limit").Scan(&limit))
	require.Equal(t, int64(walJournalSizeLimit), limit,
		"Init must cap the WAL high-water mark at %d bytes", walJournalSizeLimit)
}

func TestShouldTriggerWALCheckpoint_Gating(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name      string
		walBytes  int64
		sinceLast time.Duration
		want      bool
	}{
		{"small wal no trigger", 1 << 20, 10 * time.Minute, false},
		{"large wal fresh checkpoint", 16 << 20, time.Minute, false},
		{"large wal past gap", 16 << 20, 6 * time.Minute, true},
		{"just over both gates", walCheckpointThreshold + 1, walCheckpointMinGap + time.Second, true},
		{"just under size gate", walCheckpointThreshold - 1, walCheckpointMinGap + time.Second, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want,
				shouldTriggerWALCheckpoint(now, now.Add(-tc.sinceLast), tc.walBytes, walCheckpointThreshold))
		})
	}
	// Zero last-checkpoint time (startup) triggers immediately when large.
	require.True(t, shouldTriggerWALCheckpoint(now, time.Time{}, 16<<20, walCheckpointThreshold))
}

func TestWALMaintenanceLoop_TriggersAndStops(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "loop.db")
	db, err := New(dbPath)
	require.NoError(t, err)
	require.NoError(t, db.Init(context.Background()))
	defer db.Close()

	var passiveCalls atomic.Int64
	restore := swapWALCheckpoint(func(_ *DB, mode string) (int, int, int, error) {
		if mode == "PASSIVE" {
			passiveCalls.Add(1)
		}
		return 0, 100, 100, nil
	})
	defer restore()

	base := runtime.NumGoroutine()
	ctx, cancel := context.WithCancel(context.Background())
	db.startWALMaintenance(ctx, 20*time.Millisecond, 1) // 1-byte threshold: any WAL triggers

	// Real inserts keep the size gate honest (the seam only fake-reports the
	// checkpoint call, not the WAL size the gating reads).
	now := time.Now().UTC()
	for i := range 60 {
		r := &model.Recording{
			ID: walTestID(i), CameraID: "cam-loop", FilePath: "/tmp/y.mp4",
			Format: model.FormatH264, StartedAt: now, EndedAt: now,
			Duration: 30, FileSize: 1024, MergeStatus: model.MergeStatusPending,
		}
		require.NoError(t, db.InsertRecording(ctx, r))
	}

	require.Eventually(t, func() bool { return passiveCalls.Load() >= 1 },
		5*time.Second, 50*time.Millisecond, "loop must fire PASSIVE once the gates open")

	cancel()
	// Bounded manual poll instead of require.Eventually: testify runs the
	// condition in its own goroutine, which would count itself in
	// NumGoroutine and never satisfy the baseline.
	deadline := time.Now().Add(5 * time.Second)
	for runtime.NumGoroutine() > base && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	require.LessOrEqual(t, runtime.NumGoroutine(), base,
		"cancelled loop must drain its goroutine")
}

func TestWALMaintenanceLoop_SkipsSmallWAL(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "quiet.db")
	db, err := New(dbPath)
	require.NoError(t, err)
	require.NoError(t, db.Init(context.Background()))
	defer db.Close()

	var passiveCalls atomic.Int64
	restore := swapWALCheckpoint(func(_ *DB, mode string) (int, int, int, error) {
		passiveCalls.Add(1)
		return 0, 0, 0, nil
	})
	defer restore()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	db.StartWALMaintenanceWithInterval(ctx, 20*time.Millisecond)
	time.Sleep(150 * time.Millisecond) // no writes → WAL under threshold
	require.Zero(t, passiveCalls.Load(), "quiet DB must not burn checkpoints")
}

// --- helpers ---

func walTestID(i int) string {
	return "wal-" + string(rune('a'+i%26)) + string(rune('a'+(i/26)%26)) + string(rune('a'+(i/676)%26))
}

func swapWALCheckpoint(fn func(d *DB, mode string) (int, int, int, error)) (restore func()) {
	old := walCheckpointFn
	walCheckpointFn = fn
	return func() { walCheckpointFn = old }
}
