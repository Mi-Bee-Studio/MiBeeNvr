package timelapse

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/stretchr/testify/require"
)

// fakeCams implements CameraSettings.
type fakeCams struct {
	get func(cameraID string) *config.CameraConfig
}

func (f fakeCams) GetCameraConfig(cameraID string) *config.CameraConfig {
	return f.get(cameraID)
}

func TestManualTrigger_Validation(t *testing.T) {
	t.Parallel()
	db := newTimelapseTestDB(t)
	cfg := &config.Config{Storage: config.StorageConfig{RootDir: t.TempDir()}}
	tr := NewManualTrigger(db, func() *config.Config { return cfg }, nil, nil)
	t.Cleanup(tr.Close)

	// Invalid duration → typed 400-shape error, dedup slot released.
	_, err := tr.Trigger("cam-1", "invalid", "")
	var bad *InvalidDurationError
	require.ErrorAs(t, err, &bad)
	require.Contains(t, err.Error(), "invalid duration")
	require.Equal(t, "TIMELAPSE_INVALID_DURATION", model.ErrorCode(err))

	// Batch: invalid duration fails the whole batch up front.
	_, err = tr.TriggerBatch([]string{"cam-1"}, "nope", "")
	require.ErrorAs(t, err, &bad)
}

func TestManualTrigger_Unwired(t *testing.T) {
	t.Parallel()

	// No db.
	tr := NewManualTrigger(nil, func() *config.Config { return &config.Config{} }, nil, nil)
	t.Cleanup(tr.Close)
	_, err := tr.Trigger("cam-1", "natural-day", "")
	var unw *TriggerUnwiredError
	require.ErrorAs(t, err, &unw)
	require.Equal(t, "database not available", err.Error())

	// db present, config getter returns nil.
	db := newTimelapseTestDB(t)
	tr2 := NewManualTrigger(db, func() *config.Config { return nil }, nil, nil)
	t.Cleanup(tr2.Close)
	_, err = tr2.Trigger("cam-1", "natural-day", "")
	require.ErrorAs(t, err, &unw)
	require.Equal(t, "config not available", err.Error())
}

func TestManualTrigger_LaunchAndClose(t *testing.T) {
	t.Parallel()
	db := newTimelapseTestDB(t)
	cfg := &config.Config{Storage: config.StorageConfig{RootDir: t.TempDir()}}
	tr := NewManualTrigger(db, func() *config.Config { return cfg }, nil, nil)

	// Empty window: the merge runs (and fails to find segments) but the
	// launch itself succeeds and the response carries the reference time.
	refTime, err := tr.Trigger("cam-1", "natural-day", "2026-06-06")
	require.NoError(t, err)
	require.Equal(t, "2026-06-06", refTime.Format("2006-01-02"))

	// Batch launches report per-camera results.
	results, err := tr.TriggerBatch([]string{"cam-1", "cam-2"}, "24h", "")
	require.NoError(t, err)
	require.Len(t, results, 2)
	for _, r := range results {
		require.Equal(t, "merge_initiated", r.Status)
	}

	// Close joins everything and refuses new launches.
	tr.Close()
	tr.Close() // idempotent
	var down *TriggerShuttingDownError
	_, err = tr.Trigger("cam-1", "natural-day", "")
	require.ErrorAs(t, err, &down)
	require.Equal(t, "TIMELAPSE_SHUTTING_DOWN", model.ErrorCode(err))
}

func TestManualTrigger_DedupConflictAfterCloseReleases(t *testing.T) {
	t.Parallel()
	db := newTimelapseTestDB(t)
	cfg := &config.Config{Storage: config.StorageConfig{RootDir: t.TempDir()}}
	tr := NewManualTrigger(db, func() *config.Config { return cfg }, nil, nil)
	t.Cleanup(tr.Close)

	// Error paths release the dedup slot (invalid duration after acquiring).
	_, err := tr.Trigger("cam-1", "bogus", "")
	require.Error(t, err)
	// A subsequent valid trigger for the same camera must not be blocked by
	// the failed attempt's slot.
	_, err = tr.Trigger("cam-1", "natural-day", "")
	require.NoError(t, err)
}

func TestManualTrigger_DisplayTimezone(t *testing.T) {
	t.Parallel()
	mk := func(tz string) *ManualTrigger {
		cfg := &config.Config{Timezone: tz}
		return NewManualTrigger(nil, func() *config.Config { return cfg }, nil, nil)
	}
	require.Equal(t, time.UTC, mk("").displayTimezone())
	require.Equal(t, time.UTC, mk("UTC").displayTimezone())
	require.Equal(t, time.Local, mk("Local").displayTimezone())
	sh, err := time.LoadLocation("Asia/Shanghai")
	require.NoError(t, err)
	require.Equal(t, sh, mk("Asia/Shanghai").displayTimezone())
	require.Equal(t, time.UTC, mk("Not/AZone").displayTimezone(), "invalid zone falls back to UTC")
}

func TestManualTrigger_ReferenceTime(t *testing.T) {
	t.Parallel()
	tr := NewManualTrigger(nil, func() *config.Config { return &config.Config{} }, nil, nil)
	loc := time.UTC

	got := tr.referenceTime("2026-06-06", loc)
	require.Equal(t, "2026-06-06", got.Format("2006-01-02"))

	got = tr.referenceTime("not-a-date", loc)
	require.WithinDuration(t, time.Now(), got, 2*time.Minute, "unparseable date falls back to now")

	got = tr.referenceTime("", loc)
	require.WithinDuration(t, time.Now(), got, 2*time.Minute, "empty date = now")
}

func TestManualTrigger_CameraSettingsLookup(t *testing.T) {
	t.Parallel()
	calls := atomic.Int32{}
	cams := fakeCams{get: func(id string) *config.CameraConfig {
		calls.Add(1)
		if id == "cam-1" {
			return &config.CameraConfig{Timelapse: &config.CameraTimelapseConfig{
				Interval:                   "5s",
				DeleteRecordingsAfterMerge: true,
				MergeOutputFPS:             25,
			}}
		}
		return nil
	}}
	db := newTimelapseTestDB(t)
	cfg := &config.Config{Storage: config.StorageConfig{RootDir: t.TempDir()}}
	tr := NewManualTrigger(db, func() *config.Config { return cfg }, cams, nil)
	t.Cleanup(tr.Close)

	require.Equal(t, 5*time.Second, tr.extractionInterval("cam-1"))
	require.True(t, tr.deleteAfterMerge("cam-1"))
	// Unknown camera → defaults.
	require.Equal(t, 30*time.Second, tr.extractionInterval("nope"))
	require.False(t, tr.deleteAfterMerge("nope"))
	require.Positive(t, calls.Load())
}

func TestManualTrigger_NilCamsDefaults(t *testing.T) {
	t.Parallel()
	db := newTimelapseTestDB(t)
	tr := NewManualTrigger(db, func() *config.Config { return &config.Config{Storage: config.StorageConfig{RootDir: t.TempDir()}} }, nil, nil)
	t.Cleanup(tr.Close)
	require.Equal(t, 30*time.Second, tr.extractionInterval("any"))
	require.False(t, tr.deleteAfterMerge("any"))
}
