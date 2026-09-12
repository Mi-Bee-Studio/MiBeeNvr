// Periodic-merge intermediate-directory placement tests (#746): extraction
// and copy temp dirs must live under the merge data dir (storage-root volume)
// instead of the system /tmp — dense-sampling windows need several GB and the
// root partition on Banana Pi-class devices holds only ~9GB free.
package timelapse

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
)

// TestPeriodicMergeManager_TempDirBase verifies the derived base: empty
// dataDir keeps the os.MkdirTemp default; a data dir yields <dataDir>/tmp.
func TestPeriodicMergeManager_TempDirBase(t *testing.T) {
	t.Parallel()
	if got := (&PeriodicMergeManager{}).TempDirBase(); got != "" {
		t.Errorf("empty dataDir: TempDirBase() = %q, want \"\" (os default)", got)
	}
	dataDir := t.TempDir()
	mgr := NewPeriodicMergeManager(&mockRecordingLister{}, &mockMergeStatusUpdater{}, nil, 30, dataDir, time.Hour, nil)
	want := filepath.Join(dataDir, "tmp")
	if got := mgr.TempDirBase(); got != want {
		t.Errorf("TempDirBase() = %q, want %q", got, want)
	}
	dir, err := mgr.mkdirTemp("periodic_probe_*")
	if err != nil {
		t.Fatalf("mkdirTemp: %v", err)
	}
	defer os.RemoveAll(dir)
	if filepath.Dir(dir) != want {
		t.Errorf("mkdirTemp created %q, want parent %q", dir, want)
	}
}

// TestExtractRecordingFrames_TempDirsUnderStorageRoot runs the real
// extraction path and asserts every intermediate dir lands under
// <dataDir>/tmp with nothing leaking into the system temp directory.
func TestExtractRecordingFrames_TempDirsUnderStorageRoot(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	cameraID := "test-cam"
	windowStart := time.Date(2026, 9, 9, 10, 0, 0, 0, time.Local)

	srcDir := filepath.Join(dataDir, "segA")
	writeMJPEGDirFixture(t, srcDir, windowStart, 30, time.Second, 0)

	lister := &mockRecordingListerMultiFormat{
		videoSegments: []model.Recording{
			{ID: "vid-mjpeg-1", CameraID: cameraID, FilePath: srcDir, Format: model.FormatMJPEG, StartedAt: windowStart},
		},
	}
	mgr := NewPeriodicMergeManager(
		lister, &mockMergeStatusUpdater{}, nil, 30, dataDir, 8*time.Hour, nil,
		WithRecordingEnabledProvider(func(string) bool { return true }),
		WithExtractionInterval(5*time.Second),
	)

	_, tmpDirs, _, err := mgr.extractRecordingFrames(context.Background(), cameraID, windowStart, windowStart.Add(8*time.Hour))
	if err != nil {
		t.Fatalf("extractRecordingFrames: %v", err)
	}
	defer func() {
		for _, d := range tmpDirs {
			os.RemoveAll(d)
		}
	}()
	if len(tmpDirs) == 0 {
		t.Fatal("expected at least one extraction temp dir")
	}
	wantBase := filepath.Join(dataDir, "tmp")
	for _, d := range tmpDirs {
		if filepath.Dir(d) != wantBase {
			t.Errorf("extraction dir %q not under %q", d, wantBase)
		}
	}
	entries, err := os.ReadDir(os.TempDir())
	if err == nil {
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), "periodic_extract_") {
				t.Errorf("system temp leak: %s", e.Name())
			}
		}
	}
}
