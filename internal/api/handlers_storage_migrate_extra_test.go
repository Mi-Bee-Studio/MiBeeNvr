package api

// Tests for the uncovered batch-migrate endpoints of
// handlers_storage_migrate.go (#578): POST /api/storage/migrate and
// GET /api/storage/migrate/status, plus humanBytes. Mirrors the
// setupMigrationHandler pattern from handlers_storage_migrate_test.go
// (per-test SQLite + temp roots; the migrator is the real one but never
// started — only its Enqueue/Status surface is exercised).

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/migration"
	"github.com/stretchr/testify/require"
)

func TestStorageMigrateStatus_NilAndLive(t *testing.T) {
	t.Parallel()

	// Nil migrator → idle with an empty job list.
	db, store := setupTestDB(t)
	t.Cleanup(func() { db.Close() })
	h := TestHandler(db, store)
	rr := doRequest(t, h.Routes(), http.MethodGet, "/api/storage/migrate/status", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	require.Contains(t, rr.Body.String(), `"state":"idle"`)

	// Real migrator (not started) → state string + jobs array.
	h2, _, _ := setupMigrationHandler(t)
	h2.authMW = noopAuthMW()
	rr = doRequest(t, h2.Routes(), http.MethodGet, "/api/storage/migrate/status", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	require.Contains(t, rr.Body.String(), `"state"`)
	require.Contains(t, rr.Body.String(), `"jobs"`)
}

func TestStartStorageMigrate_Validation(t *testing.T) {
	t.Parallel()

	t.Run("bad json", func(t *testing.T) {
		t.Parallel()
		h, _, _ := setupMigrationHandler(t)
		h.authMW = noopAuthMW()
		req := httptest.NewRequest(http.MethodPost, "/api/storage/migrate", strings.NewReader(`{bad`))
		w := httptest.NewRecorder()
		h.Routes().ServeHTTP(w, req)
		require.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("relative target", func(t *testing.T) {
		t.Parallel()
		h, _, _ := setupMigrationHandler(t)
		h.authMW = noopAuthMW()
		req := httptest.NewRequest(http.MethodPost, "/api/storage/migrate",
			strings.NewReader(`{"target":"data/recordings"}`))
		w := httptest.NewRecorder()
		h.Routes().ServeHTTP(w, req)
		require.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("same as current root", func(t *testing.T) {
		t.Parallel()
		h, _, old := setupMigrationHandler(t)
		h.authMW = noopAuthMW()
		req := httptest.NewRequest(http.MethodPost, "/api/storage/migrate",
			strings.NewReader(`{"target":"`+old+`"}`))
		w := httptest.NewRecorder()
		h.Routes().ServeHTTP(w, req)
		require.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("inside current root", func(t *testing.T) {
		t.Parallel()
		h, _, old := setupMigrationHandler(t)
		h.authMW = noopAuthMW()
		req := httptest.NewRequest(http.MethodPost, "/api/storage/migrate",
			strings.NewReader(`{"target":"`+old+"/sub"+`"}`))
		w := httptest.NewRecorder()
		h.Routes().ServeHTTP(w, req)
		require.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("unusable target", func(t *testing.T) {
		t.Parallel()
		h, _, _ := setupMigrationHandler(t)
		h.authMW = noopAuthMW()
		req := httptest.NewRequest(http.MethodPost, "/api/storage/migrate",
			strings.NewReader(`{"target":"/definitely/not/a/real/path"}`))
		w := httptest.NewRecorder()
		h.Routes().ServeHTTP(w, req)
		require.Equal(t, http.StatusBadRequest, w.Code)
	})
}

func TestStartStorageMigrate_AcceptsAndEnqueues(t *testing.T) {
	t.Parallel()
	h, mig, _ := setupMigrationHandler(t)
	h.authMW = noopAuthMW()
	target := t.TempDir()

	req := httptest.NewRequest(http.MethodPost, "/api/storage/migrate",
		strings.NewReader(`{"target":"`+target+`","delete_source":true}`))
	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)
	require.Equal(t, http.StatusAccepted, w.Code, w.Body.String())
	require.Contains(t, w.Body.String(), `"jobs_enqueued":1`)

	// Default switched hot + config updated.
	require.Equal(t, target, h.store.RootDir())
	require.Equal(t, target, h.config.Storage.RootDir)
	require.Empty(t, h.config.Storage.CameraRoots)

	// The camera with recordings outside the new default got one job.
	// (Enqueue auto-starts the worker, so the state may be "running" or
	// already back to "idle" — the job row is the durable fact.)
	t.Cleanup(mig.Stop)
	var job *migration.Job
	require.Eventually(t, func() bool {
		_, jobs := mig.Status()
		for i := range jobs {
			if jobs[i].CameraID == "cam-one" {
				job = jobs[i]
				return true
			}
		}
		return false
	}, 5*time.Second, 50*time.Millisecond, "migration job for cam-one never appeared")
	require.True(t, job.DeleteSource)
}

func TestHumanBytes(t *testing.T) {
	t.Parallel()
	require.Equal(t, "512 B", humanBytes(512))
	require.Equal(t, "1.5 MB", humanBytes(1536*1024))
	require.Equal(t, "2.0 GB", humanBytes(2*1024*1024*1024))
	require.Equal(t, "0 B", humanBytes(0))
}
