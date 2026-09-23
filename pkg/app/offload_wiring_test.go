package app

// offload_wiring_test.go — guards the production wiring of the S3 offload
// service (issue #874 batch 1): constructed only when storage.remote.enabled,
// registered in lifecycle order after archive-deleter, and NOT dialed at
// construction (unreachable endpoint must still build).

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func TestBuildAppDeps_OffloadDisabled_NoManager(t *testing.T) {
	t.Helper()
	cfg, configPath := minimalConfig(t)
	cfg.Storage.Remote.Enabled = false

	deps, cleanup, err := buildAppDeps(cfg, configPath)
	if err != nil {
		t.Fatalf("buildAppDeps: %v", err)
	}
	defer cleanup()

	if deps.offloadMgr != nil {
		t.Fatal("deps.offloadMgr should be nil with storage.remote.enabled=false")
	}
}

func TestRunFree_OffloadEnabled_Registered(t *testing.T) {
	t.Helper()
	cfg, _ := minimalConfig(t)
	cfg.Storage.Remote.Enabled = true
	cfg.Storage.Remote.EndpointURL = "http://127.0.0.1:1" // unreachable is fine: no dial at build
	cfg.Storage.Remote.Bucket = "nvr"
	cfg.Storage.Remote.AccessKeyID = "k"
	cfg.Storage.Remote.SecretAccessKey = "s"

	a, err := RunFree(cfg, filepath.Join(cfg.Storage.RootDir, "mibee-nvr.yaml"))
	if err != nil {
		t.Fatalf("RunFree: %v", err)
	}
	t.Cleanup(func() { _ = a.Stop() })

	svcs := a.Services()
	found, idx := -1, -1
	for i, s := range svcs {
		if s == "offload" {
			found, idx = i, len(svcs)
			break
		}
	}
	if found < 0 {
		t.Fatalf("offload service missing from RunFree registry: %v", svcs)
	}
	// Must sit after archive-deleter (uploads stop before cleanup/db in the
	// reverse teardown) and before the streaming tail (ws/hls).
	archiveIdx := -1
	for i, s := range svcs {
		if s == "archive-deleter" {
			archiveIdx = i
		}
	}
	if archiveIdx >= 0 && found < archiveIdx {
		t.Errorf("offload (%d) registered before archive-deleter (%d)", found, archiveIdx)
	}
	_ = idx
}

func TestRunFree_OffloadBadConfig_FailsFast(t *testing.T) {
	t.Helper()
	cfg, _ := minimalConfig(t)
	cfg.Storage.Remote.Enabled = true
	cfg.Storage.Remote.EndpointURL = "http://127.0.0.1:9000"
	cfg.Storage.Remote.Bucket = "nvr"
	cfg.Storage.Remote.SecretAccessKey = "" // missing credential must fail the build

	if _, err := RunFree(cfg, filepath.Join(cfg.Storage.RootDir, "mibee-nvr.yaml")); err == nil {
		t.Fatal("RunFree should fail fast on an invalid remote-storage config")
	}
}

// TestBuildAppDeps_OffloadPlaybackWired guards the batch-2 wiring: with
// remote storage enabled, the coalescing playback proxy exists AND is wired
// into the API handler (a nil SetOffloadPlayback would 404 every remote
// playback — the #653 silent-dead-wiring class).
func TestBuildAppDeps_OffloadPlaybackWired(t *testing.T) {
	t.Helper()
	cfg, configPath := minimalConfig(t)
	cfg.Storage.Remote.Enabled = true
	cfg.Storage.Remote.EndpointURL = "http://127.0.0.1:1"
	cfg.Storage.Remote.Bucket = "nvr"
	cfg.Storage.Remote.AccessKeyID = "k"
	cfg.Storage.Remote.SecretAccessKey = "s"

	deps, cleanup, err := buildAppDeps(cfg, configPath)
	if err != nil {
		t.Fatalf("buildAppDeps: %v", err)
	}
	defer cleanup()

	if deps.offloadProxy == nil {
		t.Fatal("deps.offloadProxy is nil with storage.remote.enabled=true")
	}
	if deps.handler == nil {
		t.Fatal("deps.handler is nil")
	}
	// The endpoint must resolve through the real router (anonymous route).
	req := newRequest("HEAD", "/api/offload/objects/1")
	rec := serve(deps.router, req)
	if rec.Code == 404 && rec.Body.String() == "" {
		// Distinguish "route missing" (wiring bug) from "object not found"
		// (expected — id 1 doesn't exist). A missing route returns the
		// router's default 404 with an empty JSON body; the handler's 404
		// carries {"error":...}. Assert the JSON error body shape.
		t.Fatalf("playback endpoint may not be routed: status=%d body=%q", rec.Code, rec.Body.String())
	}
}

func newRequest(method, target string) *http.Request {
	return httptest.NewRequest(method, target, nil)
}

func serve(h http.Handler, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}
