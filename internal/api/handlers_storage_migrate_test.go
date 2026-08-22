package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/migration"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
)

// setupMigrationHandler builds a Handler + a real background migrator whose
// install has one camera tree and a database that must not move.
func setupMigrationHandler(t *testing.T) (*Handler, *migration.Migrator, string) {
	t.Helper()
	oldRoot := t.TempDir()
	configPath := filepath.Join(oldRoot, "mibee-nvr.yaml")

	db, err := storage.New(filepath.Join(oldRoot, "mibee-nvr.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	rec := &model.Recording{ID: "r1", CameraID: "cam-one",
		FilePath: filepath.Join(oldRoot, "cam-one", "seg.mp4"),
		Format:   "h264", StartedAt: time.Now().Add(-time.Hour), EndedAt: time.Now(), Duration: 60, FileSize: 3}
	if err := db.InsertRecording(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(rec.FilePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rec.FilePath, []byte("seg"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte("server: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	store, err := storage.NewManager(oldRoot)
	if err != nil {
		t.Fatal(err)
	}
	mig := migration.New(db, store, func() int { return 0 }, func() string { return "" })
	h := &Handler{db: db, store: store, config: &config.Config{}, configPath: configPath}
	h.config.Storage.RootDir = oldRoot
	h.SetStorageMigrator(mig)
	return h, mig, oldRoot
}

func TestCameraStorageRoot_SwitchAndBackgroundMigrate(t *testing.T) {
	h, mig, oldRoot := setupMigrationHandler(t)
	target := t.TempDir()
	h.config.Storage.Candidates = []string{target}

	req := httptest.NewRequest(http.MethodPut, "/api/cameras/cam-one/storage-root",
		strings.NewReader(`{"root": "`+target+`", "delete_source": true}`))
	req.SetPathValue("id", "cam-one")
	w := httptest.NewRecorder()
	h.handleSetCameraStorageRoot(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}

	// Override applied hot.
	if got := h.store.RootFor("cam-one"); got != target {
		t.Fatalf("RootFor = %q, want %q", got, target)
	}
	if h.config.Storage.CameraRoots["cam-one"] != target {
		t.Fatalf("override not persisted: %+v", h.config.Storage.CameraRoots)
	}

	// Run the background migration to completion.
	mig.Start()
	defer mig.Stop()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		state, jobs := mig.Status()
		done := state == "idle"
		for _, j := range jobs {
			if j.CameraID == "cam-one" && j.State != "done" && j.State != "failed" {
				done = false
			}
		}
		if done {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	_, jobs := mig.Status()
	var job *migration.Job
	for _, j := range jobs {
		if j.CameraID == "cam-one" {
			job = j
		}
	}
	if job == nil || job.State != "done" {
		t.Fatalf("job = %+v", job)
	}

	// File moved + row rewritten; the DB stayed put; the source segment is gone.
	if _, err := os.Stat(filepath.Join(target, "cam-one", "seg.mp4")); err != nil {
		t.Fatalf("segment not moved: %v", err)
	}
	recs, err := h.db.ListRecordings(context.Background(), model.RecordingFilter{CameraID: "cam-one", Limit: 10})
	if err != nil || len(recs) != 1 || recs[0].FilePath != filepath.Join(target, "cam-one", "seg.mp4") {
		t.Fatalf("row not rewritten: %+v (%v)", recs, err)
	}
	if _, err := os.Stat(filepath.Join(oldRoot, "mibee-nvr.db")); err != nil {
		t.Fatalf("database must stay on its volume: %v", err)
	}
	if _, err := os.Stat(filepath.Join(oldRoot, "cam-one", "seg.mp4")); !os.IsNotExist(err) {
		t.Errorf("source segment should be removed: %v", err)
	}
}

func TestCameraStorageRoot_Rejections(t *testing.T) {
	h, _, _ := setupMigrationHandler(t)
	target := t.TempDir()
	h.config.Storage.Candidates = []string{target}

	// Not in candidates → rejected.
	req := httptest.NewRequest(http.MethodPut, "/api/cameras/cam-one/storage-root",
		strings.NewReader(`{"root": "/somewhere/else"}`))
	req.SetPathValue("id", "cam-one")
	w := httptest.NewRecorder()
	h.handleSetCameraStorageRoot(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("unknown root status = %d, want 400", w.Code)
	}

	// Clearing back to default is accepted and removes the override.
	req2 := httptest.NewRequest(http.MethodPut, "/api/cameras/cam-one/storage-root",
		strings.NewReader(`{"root": ""}`))
	req2.SetPathValue("id", "cam-one")
	w2 := httptest.NewRecorder()
	h.handleSetCameraStorageRoot(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("clear status = %d, body = %s", w2.Code, w2.Body.String())
	}
	if h.store.CameraRoot("cam-one") != "" {
		t.Fatal("override should be cleared")
	}
}

func TestCameraStorageRoot_Get(t *testing.T) {
	h, _, oldRoot := setupMigrationHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/api/cameras/cam-one/storage-root", nil)
	req.SetPathValue("id", "cam-one")
	w := httptest.NewRecorder()
	h.handleGetCameraStorageRoot(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var resp struct {
		EffectiveRoot string `json:"effective_root"`
		DefaultRoot   string `json:"default_root"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.EffectiveRoot != oldRoot || resp.DefaultRoot != oldRoot {
		t.Fatalf("roots = %+v", resp)
	}
}

// The plain root-switch PUT must be rejected when the target cannot host
// recordings + database requirements (regression: crash-loop bug).
func TestUpdateSettings_RootPreflightRejectsUnusableRoot(t *testing.T) {
	h, _, _ := setupMigrationHandler(t)
	blocked := filepath.Join(t.TempDir(), "blocked")
	if err := os.WriteFile(blocked, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	body := `{"storage": {"root_dir": "` + filepath.Join(blocked, "root") + `"}}`
	req := httptest.NewRequest(http.MethodPut, "/api/settings", strings.NewReader(body))
	w := httptest.NewRecorder()
	h.handleUpdateSettings(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %s)", w.Code, w.Body.String())
	}
	if h.config.Storage.RootDir == filepath.Join(blocked, "root") {
		t.Fatal("root_dir must not be applied on a failed preflight")
	}
}

// A valid plain switch flips the LIVE manager root — no restart required.
func TestUpdateSettings_RootSwitchIsHot(t *testing.T) {
	h, _, _ := setupMigrationHandler(t)
	target := t.TempDir()
	body := `{"storage": {"root_dir": "` + target + `"}}`
	req := httptest.NewRequest(http.MethodPut, "/api/settings", strings.NewReader(body))
	w := httptest.NewRecorder()
	h.handleUpdateSettings(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var resp struct {
		Status          string `json:"status"`
		RestartRequired bool   `json:"restart_required"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.RestartRequired {
		t.Fatal("a switch between mounted candidates must not require a restart")
	}
	if h.store.RootDir() != target {
		t.Fatalf("manager root = %q, want %q (hot switch)", h.store.RootDir(), target)
	}
}
