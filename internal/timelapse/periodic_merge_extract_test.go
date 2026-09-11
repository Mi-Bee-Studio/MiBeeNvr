// Periodic-merge recording-frame extraction tests: MJPEG directory support,
// window sampling via WithExtractionInterval, and opt-in source-recording
// deletion after a successful merge (delete_recordings_after_merge).
package timelapse

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
)

// fakeSourceDeleter records DeleteRecordings calls for assertions.
type fakeSourceDeleter struct {
	calls     [][]model.Recording
	reasons   []string
	returnIDs []string
	err       error
}

func (f *fakeSourceDeleter) DeleteRecordings(_ context.Context, recordings []model.Recording, reason string) ([]string, error) {
	f.calls = append(f.calls, recordings)
	f.reasons = append(f.reasons, reason)
	if f.err != nil {
		return nil, f.err
	}
	if f.returnIDs != nil {
		return f.returnIDs, nil
	}
	ids := make([]string, len(recordings))
	for i, r := range recordings {
		ids[i] = r.ID
	}
	return ids, nil
}

func (f *fakeSourceDeleter) callCount() int { return len(f.calls) }

func (f *fakeSourceDeleter) deletedIDs() []string {
	var ids []string
	for _, call := range f.calls {
		for _, r := range call {
			ids = append(ids, r.ID)
		}
	}
	return ids
}

// TestExtractRecordingFrames_MJPEGDirs_JoinedGroup verifies that MJPEG
// directory recordings are extracted into ONE suffix-less JPEG group with a
// shared window sampler and continuing numbering (no frame_000001 overwrite).
func TestExtractRecordingFrames_MJPEGDirs_JoinedGroup(t *testing.T) {
	t.Helper()
	dataDir := t.TempDir()
	cameraID := "test-cam"
	windowStart := time.Date(2026, 9, 9, 10, 0, 0, 0, time.Local)

	// Two fragmented MJPEG dirs: 60s @1fps each, 5 minutes apart.
	dirA := filepath.Join(dataDir, "segA")
	dirB := filepath.Join(dataDir, "segB")
	writeMJPEGDirFixture(t, dirA, windowStart, 60, time.Second, 0)
	writeMJPEGDirFixture(t, dirB, windowStart.Add(5*time.Minute), 60, time.Second, 100)

	lister := &mockRecordingListerMultiFormat{
		videoSegments: []model.Recording{
			{ID: "vid-mjpeg-1", CameraID: cameraID, FilePath: dirA, Format: model.FormatMJPEG, StartedAt: windowStart},
			{ID: "vid-mjpeg-2", CameraID: cameraID, FilePath: dirB, Format: model.FormatMJPEG, StartedAt: windowStart.Add(5 * time.Minute)},
		},
	}

	mgr := NewPeriodicMergeManager(
		lister, &mockMergeStatusUpdater{}, nil, 30, dataDir, 8*time.Hour, nil,
		WithRecordingEnabledProvider(func(string) bool { return true }),
		WithExtractionInterval(10*time.Second),
	)

	segments, tmpDirs, sources, err := mgr.extractRecordingFrames(context.Background(), cameraID, windowStart, windowStart.Add(8*time.Hour))
	if err != nil {
		t.Fatalf("extractRecordingFrames failed: %v", err)
	}
	if len(tmpDirs) == 0 {
		t.Fatal("expected temp dirs to be returned for cleanup")
	}
	t.Cleanup(func() {
		for _, d := range tmpDirs {
			os.RemoveAll(d)
		}
	})

	// Both MJPEG recordings land in ONE JPEG group (suffix-less output).
	if len(segments) != 1 {
		t.Fatalf("expected 1 synthetic segment (single JPEG group), got %d", len(segments))
	}
	if segments[0].Format != model.FormatTimelapse {
		t.Errorf("synthetic segment format = %q, want %q (joined JPEG group)", segments[0].Format, model.FormatTimelapse)
	}

	// 10s sampling across both dirs: 6 frames from A + 6 from B = 12.
	matches, _ := filepath.Glob(filepath.Join(segments[0].FilePath, "frame_*.jpg"))
	if len(matches) != 12 {
		t.Errorf("expected 12 extracted frames, got %d", len(matches))
	}

	// Source IDs reported for the "jpeg" group.
	jpegSources := sources["jpeg"]
	if len(jpegSources) != 2 {
		t.Fatalf("expected 2 source recordings in jpeg group, got %d", len(jpegSources))
	}
	if jpegSources[0].ID != "vid-mjpeg-1" || jpegSources[1].ID != "vid-mjpeg-2" {
		t.Errorf("unexpected source order: [%s, %s]", jpegSources[0].ID, jpegSources[1].ID)
	}
}

// TestExtractRecordingFrames_DefaultInterval verifies the extraction interval
// falls back to 30s when WithExtractionInterval is not set.
func TestExtractRecordingFrames_DefaultInterval(t *testing.T) {
	t.Helper()
	dataDir := t.TempDir()
	cameraID := "test-cam"
	windowStart := time.Date(2026, 9, 9, 10, 0, 0, 0, time.Local)

	// One MJPEG dir spanning 2 minutes @1fps (120 frames).
	dir := filepath.Join(dataDir, "seg")
	writeMJPEGDirFixture(t, dir, windowStart, 120, time.Second, 0)

	lister := &mockRecordingListerMultiFormat{
		videoSegments: []model.Recording{
			{ID: "vid-1", CameraID: cameraID, FilePath: dir, Format: model.FormatMJPEG, StartedAt: windowStart},
		},
	}

	mgr := NewPeriodicMergeManager(
		lister, &mockMergeStatusUpdater{}, nil, 30, dataDir, 8*time.Hour, nil,
		WithRecordingEnabledProvider(func(string) bool { return true }),
		// No WithExtractionInterval → default 30s.
	)

	segments, tmpDirs, _, err := mgr.extractRecordingFrames(context.Background(), cameraID, windowStart, windowStart.Add(8*time.Hour))
	if err != nil {
		t.Fatalf("extractRecordingFrames failed: %v", err)
	}
	t.Cleanup(func() {
		for _, d := range tmpDirs {
			os.RemoveAll(d)
		}
	})
	if len(segments) != 1 {
		t.Fatalf("expected 1 synthetic segment, got %d", len(segments))
	}
	// 30s sampling over a 2-minute dir: frames at 0,30,60,90 → 4.
	matches, _ := filepath.Glob(filepath.Join(segments[0].FilePath, "frame_*.jpg"))
	if len(matches) != 4 {
		t.Errorf("expected 4 frames at default 30s interval, got %d", len(matches))
	}
}

// TestPeriodicMerge_DeleteRecordingsAfterMerge verifies the opt-in
// delete_recordings_after_merge behavior: after a successful merge the source
// video recordings are handed to the deleter; with the flag off nothing is.
func TestPeriodicMerge_DeleteRecordingsAfterMerge(t *testing.T) {
	t.Helper()
	dataDir := t.TempDir()
	cameraID := "test-cam"
	windowStart := time.Date(2026, 9, 9, 10, 0, 0, 0, time.Local)

	dirA := filepath.Join(dataDir, "segA")
	dirB := filepath.Join(dataDir, "segB")
	writeMJPEGDirFixture(t, dirA, windowStart, 60, time.Second, 0)
	writeMJPEGDirFixture(t, dirB, windowStart.Add(5*time.Minute), 60, time.Second, 100)

	newLister := func() *mockRecordingListerMultiFormat {
		return &mockRecordingListerMultiFormat{
			videoSegments: []model.Recording{
				{ID: "vid-mjpeg-1", CameraID: cameraID, FilePath: dirA, Format: model.FormatMJPEG, StartedAt: windowStart},
				{ID: "vid-mjpeg-2", CameraID: cameraID, FilePath: dirB, Format: model.FormatMJPEG, StartedAt: windowStart.Add(5 * time.Minute)},
			},
		}
	}

	run := func(deleteAfterMerge bool) *fakeSourceDeleter {
		deleter := &fakeSourceDeleter{}
		mgr := NewPeriodicMergeManager(
			newLister(), newTrackDB(), NewGoMerger(), 30, dataDir, 8*time.Hour, nil,
			WithRecordingEnabledProvider(func(string) bool { return true }),
			WithExtractionInterval(10*time.Second),
			WithDeleteRecordingsAfterMerge(deleteAfterMerge),
		)
		mgr.SetSourceRecordingDeleter(deleter)
		if err := mgr.Run(context.Background(), cameraID, windowStart.Add(time.Minute)); err != nil {
			t.Fatalf("Run failed: %v", err)
		}
		return deleter
	}

	// Flag ON → both source recordings deleted after the successful merge.
	deleter := run(true)
	if deleter.callCount() == 0 {
		t.Fatal("expected DeleteRecordings to be called with flag on")
	}
	ids := deleter.deletedIDs()
	if len(ids) != 2 || ids[0] != "vid-mjpeg-1" || ids[1] != "vid-mjpeg-2" {
		t.Errorf("deleted IDs = %v, want [vid-mjpeg-1 vid-mjpeg-2]", ids)
	}
	if deleter.reasons[0] == "" {
		t.Error("expected a deletion reason to be recorded")
	}

	// Flag OFF → no deletion.
	deleterOff := run(false)
	if deleterOff.callCount() != 0 {
		t.Errorf("DeleteRecordings must not be called with flag off, got %d calls", deleterOff.callCount())
	}
}

// TestPeriodicMerge_DeleteSkippedOnMergeFailure verifies sources survive a
// failed merge (no data loss when no output was produced).
func TestPeriodicMerge_DeleteSkippedOnMergeFailure(t *testing.T) {
	t.Helper()
	dataDir := t.TempDir()
	cameraID := "test-cam"
	windowStart := time.Date(2026, 9, 9, 10, 0, 0, 0, time.Local)

	dirA := filepath.Join(dataDir, "segA")
	writeMJPEGDirFixture(t, dirA, windowStart, 60, time.Second, 0)

	lister := &mockRecordingListerMultiFormat{
		videoSegments: []model.Recording{
			{ID: "vid-mjpeg-1", CameraID: cameraID, FilePath: dirA, Format: model.FormatMJPEG, StartedAt: windowStart},
		},
	}

	deleter := &fakeSourceDeleter{}
	mgr := NewPeriodicMergeManager(
		lister, newTrackDB(), NewGoMerger(), 30, dataDir, 8*time.Hour, nil,
		WithRecordingEnabledProvider(func(string) bool { return true }),
		WithExtractionInterval(10*time.Second),
		WithDeleteRecordingsAfterMerge(true),
	)
	mgr.SetSourceRecordingDeleter(deleter)

	// Force merge failure: occupy the camera output directory path with a
	// regular file so the merge's os.MkdirAll fails deterministically.
	if err := os.WriteFile(filepath.Join(dataDir, cameraID), []byte("blocker"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Run error is tolerated (same as production: scheduled path logs it).
	_ = mgr.Run(context.Background(), cameraID, windowStart.Add(time.Minute))

	if deleter.callCount() != 0 {
		t.Errorf("DeleteRecordings must not be called when the merge failed, got %d calls", deleter.callCount())
	}
}

// TestPeriodicMerge_DeleteSkippedForOpenWindow: a manual merge of TODAY's
// still-open window must NOT delete source recordings even with the flag on —
// the scheduled re-run at window close would otherwise regenerate the output
// from only the surviving (post-preview) segments and lose the morning.
func TestPeriodicMerge_DeleteSkippedForOpenWindow(t *testing.T) {
	t.Helper()
	dataDir := t.TempDir()
	cameraID := "test-cam"
	// Reference time INSIDE the window being merged (natural-day: today).
	windowStart := time.Now().Truncate(time.Hour)

	dirA := filepath.Join(dataDir, "segA")
	writeMJPEGDirFixture(t, dirA, windowStart, 60, time.Second, 0)

	lister := &mockRecordingListerMultiFormat{
		videoSegments: []model.Recording{
			{ID: "vid-open-1", CameraID: cameraID, FilePath: dirA, Format: model.FormatMJPEG, StartedAt: windowStart},
		},
	}

	deleter := &fakeSourceDeleter{}
	mgr := NewPeriodicMergeManager(
		lister, newTrackDB(), NewGoMerger(), 30, dataDir, 24*time.Hour, nil,
		WithRecordingEnabledProvider(func(string) bool { return true }),
		WithExtractionInterval(10*time.Second),
		WithDeleteRecordingsAfterMerge(true),
	)
	mgr.SetSourceRecordingDeleter(deleter)

	// Merge a window whose end is in the FUTURE (today, still recording).
	refTime := time.Now()
	if err := mgr.Run(context.Background(), cameraID, refTime.Add(time.Minute)); err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	if deleter.callCount() != 0 {
		t.Errorf("DeleteRecordings must not run for an open (not-yet-elapsed) window, got %d calls", deleter.callCount())
	}

	// The output itself must still be produced (preview use case). The
	// manager aligns natural-day windows in UTC (loc=nil), so the label is
	// the UTC calendar day of the reference time — NOT the local day (the
	// two differ daily between local midnight and UTC midnight; deriving
	// the label from time.Now() local flaked the test in that window).
	label := refTime.UTC().Format("2006-01-02") + "_000000"
	if _, err := os.Stat(filepath.Join(dataDir, cameraID, "periodic_"+label+".mp4")); err != nil {
		t.Errorf("expected preview output for open window (label %s): %v", label, err)
	}
}
