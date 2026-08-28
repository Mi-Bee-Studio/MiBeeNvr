package main

// merge-cameras long tail (#594): flag parsing matrix, runMergeCameras
// validation + dry-run plan against a real config/DB pair, and the execute-mode
// helpers (removeCameraFromConfig / applyTargetOverrides / restoreDBFromBackup)
// exercised directly. The execute path itself is gated on an isPortOpen probe
// whose result is environment-dependent (transparent proxies), so it is not
// driven here.

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
	"github.com/stretchr/testify/require"
)

func TestParseMergeCamerasFlagsMatrix(t *testing.T) {
	var f mergeCamerasFlags
	var rc int
	withArgs([]string{
		"mibee-nvr", "merge-cameras", "--source", "src", "--target", "dst",
		"--config", "/tmp/x.yaml", "--execute", "--force",
		"--target-onvif-endpoint", "http://1.2.3.4/onvif/device_service",
		"--target-url", "rtsp://1.2.3.4/sub",
		"--target-disable-timelapse",
	}, func() {
		f, rc = parseMergeCamerasFlags()
	})
	require.Equal(t, -1, rc)
	require.Equal(t, "src", f.sourceID)
	require.Equal(t, "dst", f.targetID)
	require.Equal(t, "/tmp/x.yaml", f.cfgPath)
	require.True(t, f.execute)
	require.False(t, f.dryRun)
	require.True(t, f.force)
	require.Equal(t, "http://1.2.3.4/onvif/device_service", f.targetOnvifEndpoint)
	require.Equal(t, "rtsp://1.2.3.4/sub", f.targetURL)
	require.True(t, f.targetDisableTimelase)

	// = form + --dry-run resetting execute.
	withArgs([]string{"mibee-nvr", "merge-cameras", "--source=a", "--target=b", "--dry-run"}, func() {
		f, rc = parseMergeCamerasFlags()
	})
	require.Equal(t, -1, rc)
	require.Equal(t, "a", f.sourceID)
	require.Equal(t, "b", f.targetID)
	require.True(t, f.dryRun)
	require.False(t, f.execute)

	// Help exits 0.
	withArgs([]string{"mibee-nvr", "merge-cameras", "--help"}, func() {
		_, rc = parseMergeCamerasFlags()
	})
	require.Equal(t, 0, rc)
}

// writeMergeConfig writes a valid two-camera config into dir.
func writeMergeConfig(t *testing.T, dir string) string {
	t.Helper()
	cfgPath := filepath.Join(dir, "mibee-nvr.yaml")
	content := "storage:\n  root_dir: " + dir + "\n  segment_duration: \"30s\"\n" +
		"cameras:\n" +
		"  - id: src-cam\n    name: Source\n    protocol: rtsp\n    encoding: h264\n    url: rtsp://127.0.0.1:1/src\n" +
		"  - id: dst-cam\n    name: Target\n    protocol: rtsp\n    encoding: h264\n    url: rtsp://127.0.0.1:1/dst\n"
	require.NoError(t, os.WriteFile(cfgPath, []byte(content), 0o644))
	return cfgPath
}

func TestRunMergeCamerasValidation(t *testing.T) {
	dir := t.TempDir()
	cfgPath := writeMergeConfig(t, dir)

	var rc int
	withArgs([]string{"mibee-nvr", "merge-cameras", "--source", "src-cam"}, func() { rc = runMergeCameras() })
	require.Equal(t, 1, rc, "--target is required")

	withArgs([]string{"mibee-nvr", "merge-cameras", "--source", "x", "--target", "x", "--config", cfgPath}, func() { rc = runMergeCameras() })
	require.Equal(t, 1, rc, "source == target must be refused")

	withArgs([]string{"mibee-nvr", "merge-cameras", "--source", "x", "--target", "y", "--config", cfgPath}, func() { rc = runMergeCameras() })
	require.Equal(t, 1, rc, "unknown source camera must be refused")

	withArgs([]string{"mibee-nvr", "merge-cameras", "--source", "src-cam", "--target", "ghost", "--config", cfgPath}, func() { rc = runMergeCameras() })
	require.Equal(t, 1, rc, "unknown target camera must be refused")

	withArgs([]string{
		"mibee-nvr", "merge-cameras", "--source", "src-cam", "--target", "dst-cam",
		"--config", filepath.Join(dir, "missing.yaml"),
	}, func() { rc = runMergeCameras() })
	require.Equal(t, 1, rc, "missing config must be refused")
}

func TestRunMergeCamerasDryRunPlan(t *testing.T) {
	dir := t.TempDir()
	cfgPath := writeMergeConfig(t, dir)

	// One source recording whose file exists on disk.
	recFile := filepath.Join(dir, "src-cam", "seg.mp4")
	require.NoError(t, os.MkdirAll(filepath.Dir(recFile), 0o755))
	require.NoError(t, os.WriteFile(recFile, []byte("seg"), 0o644))
	rec := &model.Recording{
		ID: "mr-1", CameraID: "src-cam", FilePath: recFile, Format: model.FormatH264,
		StartedAt: time.Now().UTC().Add(-time.Minute), EndedAt: time.Now().UTC(),
		Duration: 30, FileSize: 3, MergeStatus: model.MergeStatusPending,
	}
	dbPath := filepath.Join(dir, "mibee-nvr.db")
	db, err := storage.New(dbPath)
	require.NoError(t, err)
	require.NoError(t, db.Init(context.Background()))
	require.NoError(t, db.InsertRecording(context.Background(), rec))
	require.NoError(t, db.Close())

	var rc int
	withArgs([]string{
		"mibee-nvr", "merge-cameras", "--source", "src-cam", "--target", "dst-cam",
		"--config", cfgPath, "--target-url", "rtsp://new/target",
	}, func() { rc = runMergeCameras() })
	require.Equal(t, 0, rc)

	// Dry run changed nothing: config still has both cameras, DB row still on source, file in place.
	cfg, err := config.Load(cfgPath)
	require.NoError(t, err)
	require.Len(t, cfg.Cameras, 2)
	require.FileExists(t, recFile)

	db2, err := storage.New(dbPath)
	require.NoError(t, err)
	recs, err := db2.ListRecordings(context.Background(), model.RecordingFilter{CameraID: "src-cam", Limit: 10})
	require.NoError(t, err)
	require.NoError(t, db2.Close())
	require.Len(t, recs, 1)
}

func TestMergeCamerasExecuteHelpers(t *testing.T) {
	dir := t.TempDir()

	// removeCameraFromConfig drops only the source and preserves the rest.
	cfg := &config.Config{Cameras: []config.CameraConfig{
		{ID: "a"}, {ID: "b"}, {ID: "c"},
	}}
	updated := removeCameraFromConfig(cfg, "b")
	require.Len(t, updated.Cameras, 2)
	require.Equal(t, "a", updated.Cameras[0].ID)
	require.Equal(t, "c", updated.Cameras[1].ID)
	require.Len(t, cfg.Cameras, 3, "source config must not be mutated")

	// applyTargetOverrides: endpoint/url/disabled-timelapse.
	tl := config.CameraTimelapseConfig{Enabled: true}
	cfg2 := &config.Config{Cameras: []config.CameraConfig{{
		ID: "t", ONVIFEndpoint: "old", URL: "old", Timelapse: &tl,
	}}}
	applyTargetOverrides(cfg2, "t", mergeCamerasFlags{
		targetOnvifEndpoint:   "http://new/onvif",
		targetURL:             "rtsp://new/stream",
		targetDisableTimelase: true,
	})
	require.Equal(t, "http://new/onvif", cfg2.Cameras[0].ONVIFEndpoint)
	require.Equal(t, "rtsp://new/stream", cfg2.Cameras[0].URL)
	require.False(t, cfg2.Cameras[0].Timelapse.Enabled)

	// applyTargetOverrides on a missing camera is a no-op.
	applyTargetOverrides(cfg2, "ghost", mergeCamerasFlags{targetURL: "x"})

	// restoreDBFromBackup copies the backup bytes over the DB path.
	dbPath := filepath.Join(dir, "mibee-nvr.db")
	backupPath := filepath.Join(dir, "backup.db")
	require.NoError(t, os.WriteFile(dbPath, []byte("corrupted"), 0o644))
	require.NoError(t, os.WriteFile(backupPath, []byte("good-copy"), 0o644))
	require.NoError(t, restoreDBFromBackup(dbPath, backupPath))
	got, err := os.ReadFile(dbPath)
	require.NoError(t, err)
	require.Equal(t, []byte("good-copy"), got)

	// A missing backup surfaces an error.
	require.Error(t, restoreDBFromBackup(dbPath, filepath.Join(dir, "nope.db")))
}
