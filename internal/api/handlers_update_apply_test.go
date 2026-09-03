package api

// Tests for the apply/status/history endpoints (#648). All external effects
// (helper start, unit liveness) are package-var seams; the update checker is
// a real one pointed at an httptest server so status data is deterministic.

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/update"
)

// newApplyTestEnv builds a handler + fake data dir + stubbed helper seams.
func newApplyTestEnv(t *testing.T, latest string, available bool) (*Handler, *applyTestSeams, string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `{"tag_name":%q,"html_url":"x"}`, latest)
	}))
	t.Cleanup(srv.Close)

	checker := update.New("v1.0.0", "Mi-Bee-Studio/MiBeeNvr", "stable", 0)
	checker.SetEndpoint(srv.URL)
	_, err := checker.Refresh(t.Context())
	require.NoError(t, err)
	prev := updateChecker
	updateChecker = checker
	t.Cleanup(func() { updateChecker = prev })

	seams := &applyTestSeams{}
	prevStart, prevActive := applyStartUnitFn, applyUnitActiveFn
	applyStartUnitFn, applyUnitActiveFn = seams.startUnit, seams.unitActive
	t.Cleanup(func() { applyStartUnitFn, applyUnitActiveFn = prevStart, prevActive })

	db, store := setupTestDB(t)
	t.Cleanup(func() { db.Close() })
	h := TestHandler(db, store)
	dataDir := t.TempDir()
	h.config = &config.Config{}
	h.config.Storage.RootDir = dataDir
	_ = available
	return h, seams, dataDir
}

type applyTestSeams struct {
	mu       sync.Mutex
	started  []string
	active   bool
	startErr error
}

func (s *applyTestSeams) startUnit(unit string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.started = append(s.started, unit)
	return s.startErr
}

func (s *applyTestSeams) unitActive() bool { return s.active }

func TestUpdateApply_HappyPathAndStatus(t *testing.T) {
	h, seams, dataDir := newApplyTestEnv(t, "v9.9.9", true)
	routes := h.Routes()

	rr := doRequest(t, routes, http.MethodPost, "/api/update/apply", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	require.Contains(t, rr.Body.String(), `"state":"requested"`)
	require.Contains(t, rr.Body.String(), `"to":"v9.9.9"`)
	require.Len(t, seams.started, 1)

	// Request file written and helper triggered exactly once; second POST is
	// idempotent while the helper is running.
	seams.active = true
	rr = doRequest(t, routes, http.MethodPost, "/api/update/apply", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	require.Contains(t, rr.Body.String(), `"state":"applying"`)
	require.Len(t, seams.started, 1, "in-flight apply must not re-trigger")

	// Status: helper running → applying; done → terminal state from file.
	rr = doRequest(t, routes, http.MethodGet, "/api/update/apply/status", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	require.Contains(t, rr.Body.String(), `"state":"applying"`)

	seams.active = false
	require.NoError(t, os.Remove(update.RequestFilePath(dataDir)))
	require.NoError(t, update.WriteLastApply(dataDir, update.LastApply{State: "success", From: "v1.0.0", To: "v9.9.9"}))
	rr = doRequest(t, routes, http.MethodGet, "/api/update/apply/status", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	require.Contains(t, rr.Body.String(), `"state":"success"`)
	require.Contains(t, rr.Body.String(), `"auto_apply":false`)
}

func TestUpdateApply_TriggerFailureSurfacesHint(t *testing.T) {
	h, seams, _ := newApplyTestEnv(t, "v9.9.9", true)
	seams.startErr = fmt.Errorf("polkit denied")
	routes := h.Routes()

	rr := doRequest(t, routes, http.MethodPost, "/api/update/apply", nil, "", "")
	require.Equal(t, http.StatusInternalServerError, rr.Code)
	require.Contains(t, rr.Body.String(), "polkit denied")
	require.Contains(t, rr.Body.String(), "sudo mibee-nvr update")
}

func TestUpdateApply_DockerRefusedWith409(t *testing.T) {
	h, _, _ := newApplyTestEnv(t, "v9.9.9", true)
	t.Setenv("NVR_DEPLOYMENT", "docker")
	routes := h.Routes()

	rr := doRequest(t, routes, http.MethodPost, "/api/update/apply", nil, "", "")
	require.Equal(t, http.StatusConflict, rr.Code)
	require.Contains(t, rr.Body.String(), "docker")
}

func TestUpdateApply_UpToDateRefused(t *testing.T) {
	h, seams, _ := newApplyTestEnv(t, "v1.0.0", false) // latest == current
	routes := h.Routes()

	rr := doRequest(t, routes, http.MethodPost, "/api/update/apply", nil, "", "")
	require.Equal(t, http.StatusConflict, rr.Code)
	require.Empty(t, seams.started)
}

func TestUpdateHistory(t *testing.T) {
	h, _, dataDir := newApplyTestEnv(t, "v9.9.9", true)
	f, err := os.OpenFile(filepath.Join(dataDir, "update-history.jsonl"), os.O_CREATE|os.O_WRONLY, 0o644)
	require.NoError(t, err)
	fmt.Fprintln(f, `{"time":"t1","from":"v0.9.0","to":"v1.0.0","result":"ok"}`)
	f.Close()

	routes := h.Routes()
	rr := doRequest(t, routes, http.MethodGet, "/api/update/history", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	require.Contains(t, rr.Body.String(), `"from":"v0.9.0"`)
}
