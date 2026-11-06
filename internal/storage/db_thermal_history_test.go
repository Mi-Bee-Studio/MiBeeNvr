package storage

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestThermalHistoryRoundtrip covers the P4 history table end to end: insert
// minute samples, window-query them back, re-insert the same minute (restart
// re-flush) merges by extremes instead of overwriting, and the retention
// purge only removes rows before the cutoff.
func TestThermalHistoryRoundtrip(t *testing.T) {
	dir := t.TempDir()
	db, err := New(filepath.Join(dir, "test_thermal.db"))
	require.NoError(t, err)
	ctx := context.Background()
	require.NoError(t, db.Init(ctx))
	t.Cleanup(func() { _ = db.Close() })

	m := func(h, minute int) time.Time {
		return time.Date(2026, 11, 6, h, minute, 0, 0, time.UTC)
	}
	f := func(v float64) *float64 { return &v }

	require.NoError(t, db.InsertThermalMinute(ctx, "gb-hot", m(10, 0), f(30.5), f(28.0), ""))
	require.NoError(t, db.InsertThermalMinute(ctx, "gb-hot", m(10, 1), f(31.0), f(27.5), `[{"name":"n","temp_c":29.1}]`))
	require.NoError(t, db.InsertThermalMinute(ctx, "gb-other", m(10, 1), f(15.0), f(14.0), ""))

	// Window query: inclusive bounds, oldest first, camera-scoped.
	pts, err := db.QueryThermalReadings(ctx, "gb-hot", m(10, 0), m(10, 1))
	require.NoError(t, err)
	require.Len(t, pts, 2)
	require.Equal(t, m(10, 0), pts[0].Minute)
	require.InDelta(t, 30.5, *pts[0].MaxC, 1e-9)
	require.InDelta(t, 27.5, *pts[1].MinC, 1e-9)

	// Half-open window excludes the boundary.
	pts, err = db.QueryThermalReadings(ctx, "gb-hot", m(10, 0).Add(time.Second), m(10, 1))
	require.NoError(t, err)
	require.Len(t, pts, 1)

	// Re-flush the same minute with more extreme values: merges, not replaces.
	require.NoError(t, db.InsertThermalMinute(ctx, "gb-hot", m(10, 0), f(32.0), f(26.0), ""))
	pts, err = db.QueryThermalReadings(ctx, "gb-hot", m(10, 0), m(10, 0))
	require.NoError(t, err)
	require.Len(t, pts, 1)
	require.InDelta(t, 32.0, *pts[0].MaxC, 1e-9, "max keeps the more extreme value")
	require.InDelta(t, 26.0, *pts[0].MinC, 1e-9, "min keeps the more extreme value")

	n, err := db.CountThermalReadings(ctx, "gb-hot")
	require.NoError(t, err)
	require.Equal(t, 2, n)

	// Retention purge: only older-than-cutoff rows go.
	removed, err := db.PurgeThermalReadingsBefore(ctx, m(10, 1))
	require.NoError(t, err)
	require.Equal(t, int64(1), removed)
	n, err = db.CountThermalReadings(ctx, "gb-hot")
	require.NoError(t, err)
	require.Equal(t, 1, n)
}
