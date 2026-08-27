package api

// Tests for ai_handler.go — AI config management + ROI zone CRUD (#578).
// Fully hermetic: ai.Manager is an in-memory config store and config.Save
// writes to a t.TempDir yaml. No inference, no network (AI is browser-side
// by design — see AGENTS.md).

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/ai"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/stretchr/testify/require"
)

// aiHandlerEnv wires a Handler whose AI routes are live, with a real temp
// config file so syncAndSaveConfig persistence can be asserted.
func aiHandlerEnv(t *testing.T) (*Handler, http.Handler, string) {
	t.Helper()
	db, store := setupTestDB(t)
	t.Cleanup(func() { db.Close() })

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "mibee-nvr.yaml")
	cfg := &config.Config{Storage: config.StorageConfig{RootDir: store.RootDir()}}
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	h := NewHandler(db, store, noopAuthMW(), cfg, nil, nil, cfgPath, nil, nil, nil, nil, nil)
	h.SetAIHandler(NewAIHandler(ai.NewManager(ai.Config{}, nil), cfg, cfgPath))
	return h, h.Routes(), cfgPath
}

func TestAIHandler_StatusReflectsConfig(t *testing.T) {
	t.Parallel()
	_, routes, _ := aiHandlerEnv(t)

	rr := doRequest(t, routes, http.MethodGet, "/api/ai/status", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	require.Contains(t, rr.Body.String(), `"enabled":false`)

	// Flip the flag through the config endpoint, then re-read status.
	body := strings.NewReader(`{"enabled":true,"confidence_threshold":0.7}`)
	rr = doRequest(t, routes, http.MethodPut, "/api/ai/config", body, "", "")
	require.Equal(t, http.StatusOK, rr.Code)

	rr = doRequest(t, routes, http.MethodGet, "/api/ai/status", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	require.Contains(t, rr.Body.String(), `"enabled":true`)
	require.Contains(t, rr.Body.String(), `"confidence_threshold":0.7`)
}

func TestAIHandler_UpdateConfigValidationAndPersist(t *testing.T) {
	t.Parallel()
	_, routes, cfgPath := aiHandlerEnv(t)

	rr := doRequest(t, routes, http.MethodPut, "/api/ai/config", strings.NewReader(`{bad`), "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)

	// enabled_classes: explicit empty array clears the filter.
	body := strings.NewReader(`{"enabled_classes":[]}`)
	rr = doRequest(t, routes, http.MethodPut, "/api/ai/config", body, "", "")
	require.Equal(t, http.StatusOK, rr.Code)

	// The update must have persisted to the yaml on disk.
	raw, err := os.ReadFile(cfgPath)
	require.NoError(t, err)
	require.Contains(t, string(raw), "ai:")
}

func TestAIHandler_ZoneCRUD(t *testing.T) {
	t.Parallel()
	_, routes, _ := aiHandlerEnv(t)

	// Empty list starts as [].
	rr := doRequest(t, routes, http.MethodGet, "/api/ai/zones", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	require.Contains(t, rr.Body.String(), `"zones":[]`)

	// Create requires camera_id + zone.name.
	rr = doRequest(t, routes, http.MethodPost, "/api/ai/zones", strings.NewReader(`{"camera_id":""}`), "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)
	rr = doRequest(t, routes, http.MethodPost, "/api/ai/zones", strings.NewReader(`{bad`), "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)

	// Create one zone.
	zone := `{"camera_id":"front-door","zone":{"name":"porch","points":[[0,0],[1,0],[1,1]]},"enabled":true}`
	rr = doRequest(t, routes, http.MethodPost, "/api/ai/zones", strings.NewReader(zone), "", "")
	require.Equal(t, http.StatusCreated, rr.Code)
	require.Contains(t, rr.Body.String(), "porch")

	// Duplicate name across ANY camera → 409.
	dup := `{"camera_id":"other-cam","zone":{"name":"porch"}}`
	rr = doRequest(t, routes, http.MethodPost, "/api/ai/zones", strings.NewReader(dup), "", "")
	require.Equal(t, http.StatusConflict, rr.Code)

	// List shows it.
	rr = doRequest(t, routes, http.MethodGet, "/api/ai/zones", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	require.Contains(t, rr.Body.String(), "front-door")
	require.Contains(t, rr.Body.String(), "porch")

	// Update: rename + move points.
	upd := `{"camera_id":"front-door","zone":{"name":"driveway","points":[[2,2],[3,3]]}}`
	rr = doRequest(t, routes, http.MethodPut, "/api/ai/zones/porch", strings.NewReader(upd), "", "")
	require.Equal(t, http.StatusOK, rr.Code)

	// Rename onto an existing name → 409 (create a second zone first).
	rr = doRequest(t, routes, http.MethodPost, "/api/ai/zones",
		strings.NewReader(`{"camera_id":"front-door","zone":{"name":"yard"}}`), "", "")
	require.Equal(t, http.StatusCreated, rr.Code)
	upd = `{"zone":{"name":"yard"}}`
	rr = doRequest(t, routes, http.MethodPut, "/api/ai/zones/driveway", strings.NewReader(upd), "", "")
	require.Equal(t, http.StatusConflict, rr.Code)

	// Update unknown → 404; bad body → 400.
	rr = doRequest(t, routes, http.MethodPut, "/api/ai/zones/nope", strings.NewReader(`{bad`), "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)
	rr = doRequest(t, routes, http.MethodPut, "/api/ai/zones/nope", strings.NewReader(`{}`), "", "")
	require.Equal(t, http.StatusNotFound, rr.Code)

	// Delete: unknown → 404, known → 200, then gone from list.
	rr = doRequest(t, routes, http.MethodDelete, "/api/ai/zones/nope", nil, "", "")
	require.Equal(t, http.StatusNotFound, rr.Code)
	rr = doRequest(t, routes, http.MethodDelete, "/api/ai/zones/yard", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	rr = doRequest(t, routes, http.MethodGet, "/api/ai/zones", nil, "", "")
	require.NotContains(t, rr.Body.String(), "yard")
}

func TestAIHandler_ModelsListsOnnx(t *testing.T) {
	t.Parallel()
	h, routes, _ := aiHandlerEnv(t)

	// Missing models dir → empty list, not 500.
	rr := doRequest(t, routes, http.MethodGet, "/api/ai/models", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	require.Contains(t, rr.Body.String(), `"models":[]`)

	// Populate the models dir with one .onnx + noise files.
	modelDir := h.config.Storage.ModelsDir()
	require.NoError(t, os.MkdirAll(modelDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(modelDir, "yolo11n.onnx"), []byte("x"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(modelDir, "readme.txt"), []byte("x"), 0o644))
	require.NoError(t, os.Mkdir(filepath.Join(modelDir, "nested"), 0o755))

	rr = doRequest(t, routes, http.MethodGet, "/api/ai/models", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	require.Contains(t, rr.Body.String(), "yolo11n.onnx")
	require.Contains(t, rr.Body.String(), "/models/yolo11n.onnx")
	require.NotContains(t, rr.Body.String(), "readme.txt")
}
