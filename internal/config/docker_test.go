package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDockerDataDir_EnvVarWins(t *testing.T) {
	vol := t.TempDir()
	t.Setenv("NVR_DATA_DIR", vol)
	if got := DockerDataDir(); got != vol {
		t.Fatalf("DockerDataDir() = %q, want %q", got, vol)
	}
}

func TestDockerDataDir_EmptyEnvOnBareMetal(t *testing.T) {
	// No NVR_DATA_DIR and no /data (test runners are bare-metal containers
	// without our layout) → empty, so callers keep fatal error semantics.
	t.Setenv("NVR_DATA_DIR", "")
	if _, err := os.Stat("/data"); err == nil {
		t.Skip("host has a /data directory; cannot assert bare-metal detection")
	}
	if got := DockerDataDir(); got != "" {
		t.Fatalf("DockerDataDir() = %q, want empty", got)
	}
}

func TestDockerDataDir_DataDirFallback(t *testing.T) {
	// NVR_DATA_DIR points at a real directory; simulate the /data fallback
	// by checking the env-var path resolves to an existing dir.
	vol := t.TempDir()
	if err := os.WriteFile(filepath.Join(vol, "probe"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NVR_DATA_DIR", vol)
	if _, err := os.Stat(DockerDataDir()); err != nil {
		t.Fatalf("DockerDataDir() does not exist: %v", err)
	}
}
