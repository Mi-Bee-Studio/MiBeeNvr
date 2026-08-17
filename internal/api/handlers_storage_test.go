package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/middleware"
)

func TestStorageCandidatesEndpoint(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)
	h.config = &config.Config{}
	h.config.Storage.RootDir = "/data"
	h.config.Storage.Candidates = []string{"/media/WDC", "/data"} // duplicate ignored

	req := httptest.NewRequest(http.MethodGet, "/api/storage/candidates", nil)
	rec := httptest.NewRecorder()
	h.handleStorageCandidates(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Current    string `json:"current"`
		Candidates []struct {
			Path  string `json:"path"`
			Label string `json:"label"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Current != "/data" {
		t.Fatalf("current = %q, want /data", resp.Current)
	}
	if len(resp.Candidates) != 2 {
		t.Fatalf("candidates = %+v, want 2 (current + /media/WDC)", resp.Candidates)
	}
	if resp.Candidates[1].Path != "/media/WDC" || resp.Candidates[1].Label != "WDC" {
		t.Fatalf("unexpected candidate: %+v", resp.Candidates[1])
	}
}

func TestUpdateSettingsStorageRoot(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	dir := t.TempDir()
	newRoot := filepath.Join(dir, "recordings")
	h.config = &config.Config{}
	h.config.Storage.RootDir = "/data"
	h.configPath = filepath.Join(dir, "mibee-nvr.yaml")

	body := `{"storage":{"root_dir":"` + newRoot + `"}}`
	req := httptest.NewRequest(http.MethodPut, "/api/settings", bytes.NewReader([]byte(body)))
	rec := httptest.NewRecorder()
	h.handleUpdateSettings(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Status          string `json:"status"`
		RestartRequired bool   `json:"restart_required"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !resp.RestartRequired {
		t.Fatal("restart_required must be true when root_dir changes")
	}
	if h.config.Storage.RootDir != newRoot {
		t.Fatalf("config root = %q, want %q", h.config.Storage.RootDir, newRoot)
	}
	if _, err := os.Stat(newRoot); err != nil {
		t.Fatalf("new root should be created eagerly: %v", err)
	}
}

func TestUpdateSettingsStorageRootRejectsRelative(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)
	h.config = &config.Config{}
	h.config.Storage.RootDir = "/data"
	h.configPath = t.TempDir() + "/x.yaml"

	req := httptest.NewRequest(http.MethodPut, "/api/settings", bytes.NewReader([]byte(`{"storage":{"root_dir":"relative/path"}}`)))
	rec := httptest.NewRecorder()
	h.handleUpdateSettings(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

// The candidates endpoint must live in the AUTHENTICATED route group (#395):
// with a real auth middleware and no credentials the route 401s.
func TestStorageCandidatesRequiresAuth(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	hash, err := middleware.HashPassword("unit-test-password")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	h := testHandlerWithAuth(db, store, "admin", hash)

	req := httptest.NewRequest(http.MethodGet, "/api/storage/candidates", nil)
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (route is in the protected group)", rec.Code)
	}
}
