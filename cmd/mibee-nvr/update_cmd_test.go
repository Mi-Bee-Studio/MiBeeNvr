package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/update"
)

func TestHealthURLForConfig(t *testing.T) {
	cases := []struct {
		listen string
		want   string
	}{
		{":9090", "http://127.0.0.1:9090/api/health"},
		{"0.0.0.0:9191", "http://0.0.0.0:9191/api/health"},
		{"192.168.1.5:80", "http://192.168.1.5:80/api/health"},
		{"[::]:9090", "http://[::]:9090/api/health"},
		{"", "http://127.0.0.1:9090/api/health"},
	}
	for _, tc := range cases {
		t.Run(tc.listen, func(t *testing.T) {
			cfg := &config.Config{Server: config.ServerConfig{Listen: tc.listen}}
			if got := healthURLForConfig(cfg); got != tc.want {
				t.Fatalf("healthURLForConfig(%q) = %q, want %q", tc.listen, got, tc.want)
			}
		})
	}
}

func TestRunUpdate_CheckOnly(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "mibee-nvr.yaml")
	cfg := &config.Config{}
	cfg.ApplyDefaults()
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}

	prev := updateCheckFn
	updateCheckFn = func(context.Context, string) (update.Status, error) {
		return update.Status{Current: "v1.0.0", Latest: "v9.9.9", UpdateAvailable: true}, nil
	}
	t.Cleanup(func() { updateCheckFn = prev })

	var out strings.Builder
	err := runUpdate(runUpdateArgs{cfgPath: cfgPath, checkOnly: true}, &out)
	if err != nil {
		t.Fatalf("--check must not fail: %v", err)
	}
	if !strings.Contains(out.String(), "v9.9.9") || !strings.Contains(out.String(), "true") {
		t.Fatalf("check output missing status:\n%s", out.String())
	}
}

// The apply path enforces root. When tests themselves run as root (CI
// containers do), the pipeline seam is stubbed and driven instead.
func TestRunUpdate_ApplyPath(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "mibee-nvr.yaml")
	cfg := &config.Config{}
	cfg.Server.Listen = ":19090"
	cfg.Storage.RootDir = dir
	cfg.ApplyDefaults()
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}

	prevCheck := updateCheckFn
	updateCheckFn = func(context.Context, string) (update.Status, error) {
		return update.Status{Current: "v1.0.0", Latest: "v9.9.9", UpdateAvailable: true}, nil
	}
	t.Cleanup(func() { updateCheckFn = prevCheck })

	if os.Geteuid() != 0 {
		err := runUpdate(runUpdateArgs{cfgPath: cfgPath, version: "v9.9.9"}, os.Stderr)
		if err == nil || !strings.Contains(err.Error(), "root") {
			t.Fatalf("non-root apply must be refused: %v", err)
		}
		return
	}

	prevApply := applyFn
	var applied []update.Request
	var mirrors []string
	applyFn = func(req update.Request, mirror string) error {
		applied = append(applied, req)
		mirrors = append(mirrors, mirror)
		return nil
	}
	t.Cleanup(func() { applyFn = prevApply })

	var out strings.Builder
	if err := runUpdate(runUpdateArgs{cfgPath: cfgPath, version: "v9.9.9"}, &out); err != nil {
		t.Fatalf("root apply (stubbed) failed: %v", err)
	}
	if len(applied) != 1 || applied[0].TargetTag != "v9.9.9" {
		t.Fatalf("pipeline request wrong: %+v", applied)
	}
	if applied[0].HealthURL != "http://127.0.0.1:19090/api/health" {
		t.Fatalf("health URL derivation failed: %q", applied[0].HealthURL)
	}
	if !strings.Contains(out.String(), "v9.9.9") {
		t.Fatalf("output missing upgrade summary:\n%s", out.String())
	}
}

// The root-helper entry consumes the request file exactly once — it must be
// removed even when the pipeline fails, so a stale request can never re-run.
func TestRunUpdate_ApplyRequestConsumedExactlyOnce(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("request-file lifecycle on the apply path requires root in this environment")
	}
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "mibee-nvr.yaml")
	cfg := &config.Config{}
	cfg.Storage.RootDir = dir
	cfg.ApplyDefaults()
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}

	reqPath, err := update.WriteRequest(dir, update.AutoRequest{TargetTag: "v9.9.9"})
	if err != nil {
		t.Fatal(err)
	}

	prevApply := applyFn
	applyFn = func(update.Request, string) error { return errPipelineFailureForTest }
	t.Cleanup(func() { applyFn = prevApply })

	err = runUpdate(runUpdateArgs{cfgPath: cfgPath, applyRequest: reqPath}, os.Stderr)
	if err == nil || !strings.Contains(err.Error(), "pipeline") {
		t.Fatalf("pipeline failure must propagate: %v", err)
	}
	if _, statErr := os.Stat(reqPath); !os.IsNotExist(statErr) {
		t.Fatalf("request file must be removed after a failed run: %v", statErr)
	}
}

var errPipelineFailureForTest = errors.New("pipeline failed for test")
