package mibee_nvr_tests

import (
	"errors"
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
)

func buildBinary(t *testing.T) string {
	t.Helper()
	binPath := filepath.Join(t.TempDir(), "mibee-nvr-test")
	rootDir := filepath.Join("..", "cmd", "mibee-nvr")
	cmd := exec.Command("go", "build", "-o", binPath, rootDir)
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "go build failed: %s", string(output))
	return binPath
}

func TestInitCreatesConfigAndDataDir(t *testing.T) {
	binPath := buildBinary(t)
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "test-config.yaml")
	dataDir := filepath.Join(tmpDir, "data")

	cmd := exec.Command(binPath, "init", "--password", "testpass123", "--data-dir", dataDir, "--config", configPath)
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "init failed: %s", string(output))

	require.FileExists(t, configPath, "config file should be created")
	require.DirExists(t, dataDir, "data directory should be created")

	cfg, err := config.Load(configPath)
	require.NoError(t, err)
	require.NotEmpty(t, cfg.Auth.PasswordHash, "config should have password_hash, not plaintext")
	require.Empty(t, cfg.Auth.Password, "plaintext password field should be empty in saved config")
}

func TestInitRejectsExistingConfig(t *testing.T) {
	binPath := buildBinary(t)
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "test-config.yaml")
	dataDir := filepath.Join(tmpDir, "data")

	err := os.WriteFile(configPath, []byte("existing: true"), 0o644)
	require.NoError(t, err)

	cmd := exec.Command(binPath, "init", "--password", "testpass123", "--data-dir", dataDir, "--config", configPath)
	output, err := cmd.CombinedOutput()
	require.Error(t, err, "init should fail when config already exists")
	require.Contains(t, string(output), "already exists")

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		require.Equal(t, 2, exitErr.ExitCode(), "exit code should be 2 for existing config")
	}
}

func TestHealthSuccess(t *testing.T) {
	binPath := buildBinary(t)
	db, store := setupEnv(t)
	h := newAPI(db, store)

	srv := httptest.NewServer(h.Routes())
	defer srv.Close()

	_, port, err := net.SplitHostPort(srv.Listener.Addr().String())
	require.NoError(t, err)

	cmd := exec.Command(binPath, "health", "--addr", ":"+port)
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "health should succeed against running server: %s", string(output))
	_ = output
}

func TestHealthFailureNoServer(t *testing.T) {
	binPath := buildBinary(t)

	cmd := exec.Command(binPath, "health", "--addr", ":0")
	output, err := cmd.CombinedOutput()
	require.Error(t, err, "health should fail when no server is listening: %s", string(output))

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		require.Equal(t, 1, exitErr.ExitCode(), "exit code should be 1 for failed health check")
	}
}

func TestHealthAgainstRealHTTPServer(t *testing.T) {
	db, store := setupEnv(t)
	h := newAPI(db, store)

	srv := httptest.NewServer(h.Routes())
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", srv.URL+"/api/health", nil)
	require.NoError(t, err)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestHealthExitCodeFromConfig(t *testing.T) {
	binPath := buildBinary(t)
	db, store := setupEnv(t)
	h := newAPI(db, store)

	srv := httptest.NewServer(h.Routes())
	defer srv.Close()

	_, port, err := net.SplitHostPort(srv.Listener.Addr().String())
	require.NoError(t, err)

	configPath := filepath.Join(t.TempDir(), "health-cfg.yaml")
	cfg := &config.Config{
		Server: config.ServerConfig{Listen: ":" + port},
	}
	require.NoError(t, config.Save(configPath, cfg))

	cmd := exec.Command(binPath, "health", "--config", configPath)
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "health --config should succeed: %s", string(output))
	_ = output
}
