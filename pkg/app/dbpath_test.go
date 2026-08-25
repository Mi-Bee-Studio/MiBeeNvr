package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
)

// The DB resolves to the data volume when the platform provides one, and to
// the recording root otherwise (bare-metal installs keep the legacy layout).
func TestResolveDBPath(t *testing.T) {
	cfg := &config.Config{}
	cfg.Storage.RootDir = "/mnt/recordings"

	t.Setenv("NVR_DATA_DIR", "")
	if got := resolveDBPath(cfg); got != "/mnt/recordings/mibee-nvr.db" {
		t.Fatalf("bare metal: %q", got)
	}
	t.Setenv("NVR_DATA_DIR", "/data")
	if got := resolveDBPath(cfg); got != "/data/mibee-nvr.db" {
		t.Fatalf("docker: %q", got)
	}
	cfg.Storage.DBPath = "/explicit/nvr.db"
	if got := resolveDBPath(cfg); got != "/explicit/nvr.db" {
		t.Fatalf("explicit override: %q", got)
	}
}

// Legacy adoption: a DB under the recording root moves to the data volume
// once; a stale data-volume copy older than the legacy one is replaced.
func TestAdoptDatabase(t *testing.T) {
	tmp := t.TempDir()
	root := filepath.Join(tmp, "root")
	dataVol := filepath.Join(tmp, "data")
	for _, d := range []string{filepath.Join(root, "cam"), dataVol} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("NVR_DATA_DIR", dataVol)

	// Build a legacy DB under the recording root with one row.
	legacyDB, err := storage.New(filepath.Join(root, "mibee-nvr.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := legacyDB.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := legacyDB.InsertRecording(context.Background(), &model.Recording{
		ID: "r1", CameraID: "cam", FilePath: filepath.Join(root, "cam", "a.mp4"),
		Format: "h264", StartedAt: time.Now(), EndedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	legacyDB.Close()

	cfg := &config.Config{}
	cfg.Storage.RootDir = root
	adoptDatabase(cfg)

	// The data volume now carries the adopted DB with the row intact.
	db, err := storage.New(filepath.Join(dataVol, "mibee-nvr.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	recs, err := db.ListRecordings(context.Background(), model.RecordingFilter{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 || recs[0].ID != "r1" {
		t.Fatalf("adopted db rows = %+v", recs)
	}

	// Re-running adoption is a no-op (data-volume DB is now the newest).
	oldLegacy := filepath.Join(root, "mibee-nvr.db")
	before, _ := os.Stat(filepath.Join(dataVol, "mibee-nvr.db"))
	time.Sleep(10 * time.Millisecond)
	adoptDatabase(cfg)
	after, _ := os.Stat(filepath.Join(dataVol, "mibee-nvr.db"))
	if !after.ModTime().Equal(before.ModTime()) {
		t.Fatal("second adoption should be a no-op")
	}
	_ = oldLegacy
}

// openDatabase uses the resolved (data-volume) path and leaves the recording
// root free of database files.
func TestOpenDatabase_DecoupledFromRoot(t *testing.T) {
	tmp := t.TempDir()
	root := filepath.Join(tmp, "root")
	dataVol := filepath.Join(tmp, "data")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dataVol, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NVR_DATA_DIR", dataVol)

	cfg := &config.Config{}
	cfg.Storage.RootDir = root
	db, err := openDatabase(cfg, filepath.Join(tmp, "mibee-nvr.yaml"))
	if err != nil {
		t.Fatalf("openDatabase: %v", err)
	}
	db.Close()
	if _, err := os.Stat(filepath.Join(dataVol, "mibee-nvr.db")); err != nil {
		t.Fatalf("db must live on the data volume: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "mibee-nvr.db")); !os.IsNotExist(err) {
		t.Fatalf("recording root must stay db-free: %v", err)
	}
	if cfg.Storage.DBPath != "" {
		t.Fatalf("docker installs must not pin db_path, got %q", cfg.Storage.DBPath)
	}
}

// Bare metal (no NVR_DATA_DIR): openDatabase PINS the resolved db location
// into the config so a later root switch cannot re-resolve the DB under the
// new root (which would boot into an empty database).
func TestOpenDatabase_PinsBareMetalDBPath(t *testing.T) {
	tmp := t.TempDir()
	root := filepath.Join(tmp, "root")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NVR_DATA_DIR", "")
	configPath := filepath.Join(tmp, "mibee-nvr.yaml")

	cfg := &config.Config{}
	cfg.Storage.RootDir = root
	db, err := openDatabase(cfg, configPath)
	if err != nil {
		t.Fatalf("openDatabase: %v", err)
	}
	db.Close()

	want := filepath.Join(root, "mibee-nvr.db")
	if cfg.Storage.DBPath != want {
		t.Fatalf("db_path pin = %q, want %q", cfg.Storage.DBPath, want)
	}
	saved, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}
	if saved.Storage.DBPath != want {
		t.Fatalf("persisted db_path = %q, want %q", saved.Storage.DBPath, want)
	}
	// With the pin in place, switching the root keeps resolving the SAME db.
	cfg.Storage.RootDir = filepath.Join(tmp, "new-root")
	if got := resolveDBPath(cfg); got != want {
		t.Fatalf("after root switch, resolveDBPath = %q, want pinned %q", got, want)
	}
}
